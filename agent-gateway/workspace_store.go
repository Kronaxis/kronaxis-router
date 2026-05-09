package main

import (
	"sync"
	"time"
)

// WorkspaceStore tracks persistent named workspaces for /v1/workspaces.
// Each entry is a *Workspace plus access metadata. Cleanup is on explicit
// DELETE or via TTL sweep on Touch (workspaces idle longer than maxIdle
// are not kept; that's enforced in the sweeper using the workspace dir's
// mtime, which Touch refreshes).
type WorkspaceStore struct {
	mu  sync.RWMutex
	wss map[string]*storedWorkspace
}

type storedWorkspace struct {
	ws        *Workspace
	createdAt time.Time
	lastUsed  time.Time
	createdBy string
}

func newWorkspaceStore() *WorkspaceStore {
	return &WorkspaceStore{wss: map[string]*storedWorkspace{}}
}

func (s *WorkspaceStore) Put(id string, ws *Workspace, createdBy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wss[id] = &storedWorkspace{
		ws:        ws,
		createdAt: time.Now(),
		lastUsed:  time.Now(),
		createdBy: createdBy,
	}
}

func (s *WorkspaceStore) Get(id string) (*Workspace, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.wss[id]
	if !ok {
		return nil, false
	}
	return w.ws, true
}

func (s *WorkspaceStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.wss[id]
	if !ok {
		return false
	}
	_ = w.ws.Cleanup()
	delete(s.wss, id)
	return true
}

func (s *WorkspaceStore) List() []WorkspaceSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WorkspaceSummary, 0, len(s.wss))
	for id, w := range s.wss {
		out = append(out, WorkspaceSummary{
			ID:        id,
			Path:      w.ws.Path,
			CreatedAt: w.createdAt.UTC().Format(time.RFC3339),
			LastUsed:  w.lastUsed.UTC().Format(time.RFC3339),
			CreatedBy: w.createdBy,
		})
	}
	return out
}

type WorkspaceSummary struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
	LastUsed  string `json:"last_used"`
	CreatedBy string `json:"created_by,omitempty"`
}

// Touch updates the lastUsed timestamp and the dir's mtime so the sweeper
// doesn't reap an actively-used workspace.
func (sw *Workspace) Touch() {
	now := time.Now()
	_ = touchDir(sw.rootDir, now)
	_ = touchDir(sw.Path, now)
}

func touchDir(path string, t time.Time) error {
	return chtimes(path, t, t)
}
