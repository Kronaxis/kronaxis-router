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

const version = "1.0.0"

var (
	db          *sql.DB
	cfg         *Config
	pool        *BackendPool
	rtr         *Router
	bat         *Batcher
	batchMgr    *BatchManager
	costs       *CostTracker
	respCache   *ResponseCache
	logger      = log.New(os.Stdout, "[router] ", log.LstdFlags|log.Lmsgprefix)
	startupTime = time.Now()
)

func main() {
	logger.Printf("kronaxis-router v%s starting", version)

	configPath := env("CONFIG_PATH", "config.yaml")
	port := env("ROUTER_PORT", "8050")
	databaseURL := env("DATABASE_URL", "")

	// Load config
	var err error
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
	rtr = newRouter(cfg.Rules, cfg.Defaults, pool)
	bat = newBatcher(cfg.Batching)
	batchMgr = newBatchManager(env("BATCH_DATA_DIR", ""))
	costs = newCostTracker(cfg.Budgets, db)
	respCache = newResponseCache(envInt("CACHE_MAX_SIZE", 1000), envInt("CACHE_TTL_SECONDS", 3600))

	// Start background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool.startHealthChecks(ctx, cfg.Server.HealthCheckInterval.Duration)
	go watchConfig(ctx, configPath)

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
	registerUI(mux)

	// Wrap with middleware (auth -> CORS -> logging)
	handler := corsMiddleware(authMiddleware(loggingMiddleware(mux)))

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
