package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// startWorkspaceSweeper deletes per-request workspace directories under root
// older than maxAge. Runs every interval. Safe to start at boot and cancel via
// ctx. Only touches dirs whose name starts with "req-" so we never delete
// unrelated content if workspace_root is mis-configured.
func startWorkspaceSweeper(ctx context.Context, root string, maxAge, interval time.Duration, logger AuditLogger) {
	if maxAge <= 0 || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// run once immediately to clean leftovers from a crash
		sweep(root, maxAge, logger)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep(root, maxAge, logger)
			}
		}
	}()
}

func sweep(root string, maxAge time.Duration, logger AuditLogger) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	cleaned := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), "req-") {
			continue
		}
		path := filepath.Join(root, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(path); err == nil {
			cleaned++
		}
	}
	if cleaned > 0 && logger != nil {
		logger.Event("workspace_sweep", map[string]any{"cleaned": cleaned, "max_age_seconds": int(maxAge.Seconds())})
	}
}
