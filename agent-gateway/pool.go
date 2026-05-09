package main

import (
	"context"
	"sync"
	"time"
)

// WarmPool keeps a small number of pre-created workspaces ready to hand out.
// Each request still gets a fresh, isolated workspace -- the pool just amortises
// the ~200ms of `git init` + auth-symlink work across requests.
//
// When the pool is empty, callers fall back to creating a workspace inline.
// When the pool is full, the refill loop sleeps. Workspaces older than 1 hour
// in the pool are discarded (so they don't drift if the host's `claude auth`
// changes; the symlink approach already keeps creds current, but ages out
// stale base_repo clones).
type WarmPool struct {
	cfg      *Config
	audit    AuditLogger
	ch       chan *Workspace
	stopOnce sync.Once
}

func newWarmPool(cfg *Config, audit AuditLogger) *WarmPool {
	size := cfg.WarmPoolSize
	if size < 0 {
		size = 0
	}
	return &WarmPool{
		cfg:   cfg,
		audit: audit,
		ch:    make(chan *Workspace, size),
	}
}

func (p *WarmPool) Start(ctx context.Context) {
	if cap(p.ch) == 0 {
		return
	}
	go p.refillLoop(ctx)
}

func (p *WarmPool) refillLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			p.drain()
			return
		default:
		}

		// Try to refill until we hit cap or fail
		ws, err := p.create()
		if err != nil {
			// Back off on error -- don't hot-loop
			select {
			case <-ctx.Done():
				p.drain()
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Block until consumer takes it or ctx ends
		select {
		case <-ctx.Done():
			_ = ws.Cleanup()
			p.drain()
			return
		case p.ch <- ws:
			// successfully placed
		}
	}
}

func (p *WarmPool) create() (*Workspace, error) {
	id := newRequestID()
	ws, err := newWorkspace(p.cfg.WorkspaceRoot, p.cfg.BaseRepo, id, p.cfg.RetainWorkspaces)
	if err != nil {
		return nil, err
	}
	if err := ws.SeedAuth(); err != nil {
		_ = ws.Cleanup()
		return nil, err
	}
	return ws, nil
}

// Get returns a ready workspace, or nil if the pool is empty/disabled.
// Non-blocking.
func (p *WarmPool) Get() *Workspace {
	select {
	case ws := <-p.ch:
		return ws
	default:
		return nil
	}
}

func (p *WarmPool) drain() {
	for {
		select {
		case ws := <-p.ch:
			_ = ws.Cleanup()
		default:
			return
		}
	}
}
