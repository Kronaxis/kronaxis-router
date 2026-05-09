package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type createWorkspaceRequest struct {
	BaseRepo  string `json:"base_repo,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

type createWorkspaceResponse struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createWorkspace(w, r)
	case http.MethodGet:
		s.listWorkspaces(w)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req createWorkspaceRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	id := newRequestID()
	baseRepo := s.cfg.BaseRepo
	if req.BaseRepo != "" {
		baseRepo = req.BaseRepo
	}
	ws, err := newWorkspace(s.cfg.WorkspaceRoot, baseRepo, "ws-"+id, true)
	if err != nil {
		http.Error(w, "workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := ws.SeedAuth(); err != nil {
		s.logger.Printf("seed auth on workspace %s: %v", id, err)
	}
	s.wsStore.Put(id, ws, req.CreatedBy)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(createWorkspaceResponse{
		ID:        id,
		Path:      ws.Path,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	s.audit.Event("workspace_created", map[string]any{"id": id, "path": ws.Path, "created_by": req.CreatedBy})
}

func (s *Server) listWorkspaces(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   s.wsStore.List(),
	})
}

func (s *Server) handleWorkspaceItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/workspaces/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "bad workspace id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ws, ok := s.wsStore.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		diff, _ := ws.GitDiff()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       id,
			"path":     ws.Path,
			"git_diff": diff,
		})
	case http.MethodDelete:
		if !s.wsStore.Delete(id) {
			http.NotFound(w, r)
			return
		}
		s.audit.Event("workspace_deleted", map[string]any{"id": id})
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
