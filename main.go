package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
)

var (
	// Set by goreleaser ldflags; defaults for dev builds
	version = "1.1.0"
	commit  = "dev"
	date    = "unknown"
)

var (
	db               *sql.DB
	cfg              *Config
	pool             *BackendPool
	rtr              *Router
	bat              *Batcher
	batchMgr         *BatchManager
	costs            *CostTracker
	respCache        *ResponseCache
	rateLim          *RateLimiter
	qualVal          *QualityValidator
	auditLog         *AuditLogger
	abTests          *ABTestManager
	qGate            *QualityGate
	graphifyEmbedder Embedder
	graphifyMW       *GraphifyMiddleware
	logger           = log.New(os.Stdout, "[router] ", log.LstdFlags|log.Lmsgprefix)
	startupTime      = time.Now()
)

func main() {
	// Subcommand dispatch
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("kronaxis-router v%s (%s, %s)\n", version, commit, date)
			return
		case "init":
			runInit(os.Args[2:])
			return
		case "mcp":
			runMCP(os.Args[2:])
			return
		case "ingest", "graphify":
			runGraphifyCmd(os.Args[2:])
			return
		case "agents":
			runAgentsCmd(os.Args[2:])
			return
		case "serve":
			// Explicit serve: strip the subcommand and fall through
		}
	}

	runServer()
}

func runServer() {
	logger.Printf("kronaxis-router v%s starting", version)

	configPath := env("CONFIG_PATH", "config.yaml")
	// Support -config / --config flag (overrides env var)
	for i, arg := range os.Args[1:] {
		if (arg == "-config" || arg == "--config") && i+1 < len(os.Args[1:]) {
			configPath = os.Args[i+2]
			break
		}
	}
	port := env("ROUTER_PORT", "8050")
	databaseURL := env("DATABASE_URL", "")

	// Load config (generate default if missing)
	var err error
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		logger.Printf("no config file at %s, generating default", configPath)
		generateDefaultConfig(configPath)
	}
	cfg, err = loadConfig(configPath)
	if err != nil {
		logger.Fatalf("failed to load config: %v", err)
	}
	logger.Printf("loaded config: %d backends, %d rules, %d budgets",
		len(cfg.Backends), len(cfg.Rules), len(cfg.Budgets))

	// Connect to database (optional, for cost logging)
	if databaseURL != "" {
		db, err = sql.Open("postgres", databaseURL)
		if err != nil {
			logger.Fatalf("database connection failed: %v", err)
		}
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(5)
		db.SetConnMaxIdleTime(5 * time.Minute)
		if err := db.Ping(); err != nil {
			logger.Printf("WARNING: database ping failed: %v (cost logging disabled)", err)
			db = nil
		} else {
			runMigrations()
			logger.Println("database connected, cost logging enabled")
		}
	} else {
		logger.Println("no DATABASE_URL set, cost logging disabled")
	}

	// Initialise subsystems
	pool = newBackendPool(cfg.Backends)
	initDPOExporterFromEnv()

	// Build KV cache index for any backends that opted in via kv_pinning.
	// Each backend gets its own per-prefix tree; the index biases routing
	// toward backends with a deep matching prefix (whose vLLM KV cache is
	// presumed warm for that path).
	kvIndex = NewKVIndex(0)
	for _, b := range cfg.Backends {
		if b.KVPinning != nil && b.KVPinning.Enabled {
			maxAge := time.Duration(b.KVPinning.MaxPrefixAgeSeconds) * time.Second
			kvIndex.AddBackend(b.Name, maxAge, b.KVPinning.MaxNodes)
			if b.KVPinning.HashChunkTokens > 0 {
				kvIndex.hashChunkTokens = b.KVPinning.HashChunkTokens
			}
		}
	}
	if pools := len(kvIndex.trees); pools > 0 {
		logger.Printf("KV cache pinning enabled for %d backend(s)", pools)
		// Periodic sweep evicts subtrees older than maxAge.
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				if removed := kvIndex.SweepAll(); removed > 0 {
					logger.Printf("kv index sweep: evicted %d stale prefix nodes", removed)
				}
			}
		}()
	}
	rtr = newRouter(cfg.Rules, cfg.Defaults, pool)
	bat = newBatcher(cfg.Batching)
	batchMgr = newBatchManager(env("BATCH_DATA_DIR", ""))
	costs = newCostTracker(cfg.Budgets, db)
	respCache = newResponseCache(envInt("CACHE_MAX_SIZE", 1000), envInt("CACHE_TTL_SECONDS", 3600))
	rateLim = newRateLimiter(cfg.RateLimits)
	qualVal = newQualityValidator(QualityConfig{
		Enabled:    env("QUALITY_ENABLED", "") == "true",
		SampleRate: 0.05,
		Threshold:  0.6,
	})
	auditLog = newAuditLogger(AuditConfig{
		Enabled:    env("AUDIT_ENABLED", "") == "true",
		LogFile:    env("AUDIT_LOG_FILE", "audit.jsonl"),
		MaxEntries: envInt("AUDIT_MAX_ENTRIES", 100000),
	})
	abTests = newABTestManager(nil)
	qGate = newQualityGate(QualityGateConfig{
		Enabled:         env("QUALITY_GATE_ENABLED", "") == "true",
		Mode:            env("QUALITY_GATE_MODE", "sequential"),
		SampleRate:      1.0, // Gate all requests when enabled
		FallbackBackend: env("QUALITY_GATE_FALLBACK", ""),
		Checks: GateChecks{
			MinLength:    1,
			MaxEmptyRate: 0,
			ValidJSON:    true,
			NoRefusal:    true,
		},
	})

	// Start background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool.startHealthChecks(ctx, cfg.Server.HealthCheckInterval.Duration)
	go watchConfig(ctx, configPath)

	// Graphify pre-stage: optional embedder + middleware. If embedder is
	// configured but unreachable, log and continue (router still works).
	//
	// Kronaxis Platform integration: when graphify.fabric_url is set we
	// can run in "fabric-only" mode -- no local embedder, no local
	// kr_chunks table. Retrieval is delegated to the Fabric service.
	gcfg := cfg.Graphify.WithDefaults()
	cfg.Graphify = gcfg
	if gcfg.Enabled {
		var emb Embedder
		// Probe local embedder + bootstrap embedded schema -- best-effort.
		// We still build the middleware below even if these fail, so the
		// fabric-only path stays operational.
		if db != nil {
			probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
			e, err := newEmbedder(probeCtx, gcfg.Embedder)
			probeCancel()
			if err != nil {
				logger.Printf("graphify embedder unavailable: %v (embedded retrieval disabled)", err)
			} else if err := graphifyEnsureSchema(ctx, db, e.Dim()); err != nil {
				logger.Printf("graphify schema bootstrap failed: %v (embedded retrieval disabled)", err)
			} else {
				emb = e
				graphifyEmbedder = e
				if gcfg.WatchEnabled {
					watcher := newGraphifyWatcher(db, e, gcfg)
					go func() {
						if err := watcher.Run(ctx); err != nil {
							logger.Printf("graphify watcher exited: %v", err)
						}
					}()
					logger.Printf("graphify watcher running on %d roots", len(gcfg.IngestRoots))
				}
			}
		}
		// Construct the middleware iff we have *some* retrieval backend.
		if emb != nil || gcfg.FabricEnabled() {
			graphifyMW = newGraphifyMiddleware(gcfg, emb)
			mode := "embedded"
			if gcfg.FabricEnabled() && emb == nil {
				mode = "fabric-only"
			} else if gcfg.FabricEnabled() {
				mode = "fabric+embedded-fallback"
			}
			embName := "(none)"
			embDim := 0
			if emb != nil {
				embName = emb.Name()
				embDim = emb.Dim()
			}
			logger.Printf("graphify enabled: mode=%s embedder=%s dim=%d fabric_url=%q default=%s",
				mode, embName, embDim, gcfg.FabricURL, gcfg.Default)
		} else {
			logger.Printf("graphify enabled in config but no retrieval backend available (no embedder, no fabric_url); disabled")
		}
	}

	// Register routes
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChatCompletions)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/costs", handleCosts)
	mux.HandleFunc("/api/backends", handleBackends)
	mux.HandleFunc("/api/config", handleConfigView)
	mux.HandleFunc("/api/rules", handleRules)
	mux.HandleFunc("/api/budgets", handleBudgets)
	mux.HandleFunc("/api/config/yaml", handleConfigYAML)
	mux.HandleFunc("/api/config/reload", handleConfigReload)
	mux.HandleFunc("/api/stats", handleStats)
	mux.HandleFunc("/api/batch", handleBatchStatus)
	mux.HandleFunc("/api/batch/submit", handleBatchSubmit)
	mux.HandleFunc("/api/batch/results", handleBatchResults)
	mux.HandleFunc("/api/batch/stream", handleBatchStream)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/api/classifier", handleClassifierStats)
	mux.HandleFunc("/api/abtests", handleABTests)
	mux.HandleFunc("/v1/retrieve", handleGraphifyRetrieve)
	mux.HandleFunc("/api/graphify", handleGraphifyStats)
	mux.HandleFunc("/api/agents", handleAgents)
	mux.HandleFunc("/api/kv-trees", handleKVTrees)
	mux.HandleFunc("/v1/sessions", handleSessions)
	mux.HandleFunc("/v1/sessions/", handleSessionItem)
	mux.HandleFunc("/api/dpo", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, handleDPOStatsBody())
	})
	mux.HandleFunc("/api/costs/forecast", handleCostForecast)
	mux.HandleFunc("/api/shadow/stats", handleShadowResults)
	mux.HandleFunc("/cost-lab", handleCostLab)
	mux.HandleFunc("/cost-lab/", handleCostLab)

	// Video generation — routes to ltx-video backends
	mux.HandleFunc("/v1/video/generate", handleVideoGenerate)
	mux.HandleFunc("/v1/video/", handleVideoProxy) // proxy file retrieval to backend

	registerUI(mux)

	// Wrap with middleware (rate limit -> auth -> CORS -> logging)
	handler := corsMiddleware(authMiddleware(rateLimitMiddleware(loggingMiddleware(mux))))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Printf("listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.Printf("received %s, shutting down", sig)
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown error: %v", err)
	}
	if db != nil {
		db.Close()
	}
	auditLog.Close()
	logger.Println("stopped")
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	backends := pool.allStatuses()
	healthy := 0
	for _, b := range backends {
		if b.Status == "healthy" {
			healthy++
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"status":           "ok",
		"service":          "kronaxis-router",
		"version":          version,
		"time":             time.Now().UTC().Format(time.RFC3339),
		"uptime_seconds":   int(time.Since(startupTime).Seconds()),
		"backends_total":   len(backends),
		"backends_healthy": healthy,
		"backends":         backends,
		"cache":            respCache.Stats(),
		"quality":          qualVal.Stats(),
		"quality_gate":     qGate.Stats(),
	})
}

func handleConfigView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	configMu.RLock()
	result := map[string]interface{}{
		"rules_count":    len(cfg.Rules),
		"backends_count": len(cfg.Backends),
		"budgets":        cfg.Budgets,
		"batching":       cfg.Batching,
		"branding":       cfg.Server.Branding,
	}
	configMu.RUnlock()
	writeJSON(w, 200, result)
}

func runMigrations() {
	migrations := []string{
		`ALTER TABLE llm_call_log ADD COLUMN IF NOT EXISTS service TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_llm_call_log_service ON llm_call_log (service) WHERE service IS NOT NULL`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			logger.Printf("migration warning: %v", err)
		}
	}
	// Stateful sessions table (kr_sessions).
	if err := runSessionMigrations(db); err != nil {
		logger.Printf("session migration warning: %v", err)
	} else {
		sessionStore = NewSessionStore(db, 3600)
		go SessionSweeperLoop(context.Background(), sessionStore, 5*time.Minute)
		logger.Println("stateful sessions enabled (kr_sessions table)")
	}
}

func handleClassifierStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"thresholds": map[string]float64{
			"tier2_ceiling": Tier2Ceiling,
			"tier1_floor":   Tier1Floor,
		},
		"feedback_adjustments": classifier.FeedbackState(),
		"heavy_keywords":       len(classifier.heavyKeywords),
		"light_keywords":       len(classifier.lightKeywords),
	})
}

func generateDefaultConfig(path string) {
	defaultYAML := `# Kronaxis Router - Auto-generated default config
# See https://github.com/kronaxis/kronaxis-router for full documentation.

server:
  port: 8050
  health_check_interval: 30s
  default_timeout: 120s
  branding:
    headers: true
    header_name: "Kronaxis Router"

backends:
  - name: local
    url: "http://localhost:8000"
    type: vllm
    model_name: "default"
    cost_input_1m: 0.01
    cost_output_1m: 0.01
    capabilities: [json_output]
    max_concurrent: 4
    health_endpoint: "/v1/models"

rules:
  - name: default
    priority: 100
    match: {}
    backends: [local]

defaults:
  fallback_chain: [local]
  default_timeout_ms: 120000

batching:
  enabled: true
  window_ms: 50
  max_batch_size: 8
  priority_bypass: [interactive]
`
	if err := os.WriteFile(path, []byte(defaultYAML), 0644); err != nil {
		logger.Printf("WARNING: failed to write default config: %v", err)
	}
}

// Helpers

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := 0
	fmt.Sscanf(v, "%d", &n)
	if n > 0 {
		return n
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	data, _ := jsonMarshal(v)
	w.Write(data)
}
