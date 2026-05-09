package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Subcommand dispatch (must come before flag.Parse so flags are
	// scoped to each subcommand's flag set, not the server's).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "claude-login":
			runClaudeLogin(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Println("agent-gateway")
			return
		}
	}

	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	logger := log.New(os.Stderr, "[agent-gateway] ", log.LstdFlags|log.Lmicroseconds)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}
	logger.Printf("config: port=%d claude=%s workspace_root=%s max_concurrent=%d keepalive=%ds sweep_after=%dh",
		cfg.Port, cfg.ClaudeBinary, cfg.WorkspaceRoot, cfg.MaxConcurrent, cfg.KeepaliveSeconds, cfg.WorkspaceMaxAgeHours)

	// Audit log writer (file or stderr).
	var auditWriter io.Writer = os.Stderr
	if cfg.AuditFile != "" {
		f, err := os.OpenFile(cfg.AuditFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			logger.Fatalf("open audit file %s: %v", cfg.AuditFile, err)
		}
		defer f.Close()
		auditWriter = f
		logger.Printf("audit log: %s", cfg.AuditFile)
	}
	bus := newLiveBus()
	audit := newAuditWithLive(newJSONAudit(auditWriter), bus)
	audit.Event("startup", map[string]any{
		"port":           cfg.Port,
		"claude_binary":  cfg.ClaudeBinary,
		"workspace_root": cfg.WorkspaceRoot,
	})

	// Workspaces, pool, registry, server.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	pool := newWarmPool(cfg, audit)
	pool.Start(rootCtx)

	reg := newRegistry(cfg)
	metrics := newMetrics()

	authPool, err := loadAuthPool(cfg.AuthPoolFile)
	if err != nil {
		logger.Fatalf("load auth pool: %v", err)
	}
	if authPool != nil {
		audit.Event("auth_pool_loaded", map[string]any{
			"file":     cfg.AuthPoolFile,
			"accounts": len(authPool.Snapshot()),
		})
	}

	srv := newServer(cfg, reg, logger, audit, pool, metrics, bus, authPool)

	startWorkspaceSweeper(rootCtx, cfg.WorkspaceRoot,
		time.Duration(cfg.WorkspaceMaxAgeHours)*time.Hour,
		time.Duration(cfg.SweepIntervalMinutes)*time.Minute,
		audit,
	)

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: streaming agents can run for minutes.
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Printf("received %s, shutting down", sig)
		audit.Event("shutdown", map[string]any{"signal": sig.String()})
		rootCancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	logger.Printf("listening on %s", httpSrv.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("server: %v", err)
	}
	logger.Printf("stopped")
}
