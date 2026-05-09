package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kronaxis/agent-gateway/accounts"
	regpkg "github.com/kronaxis/agent-gateway/registry"
)

// handleAgents serves /v1/agents (list) and /v1/agents (POST = register).
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listAgents(w, r)
	case http.MethodPost:
		s.registerAgent(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAgentItem serves /v1/agents/<name> (DELETE).
func (s *Server) handleAgentItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/v1/agents/")
	name = strings.TrimSpace(name)
	if name == "" {
		http.Error(w, "agent name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		p, ok := s.profileReg.Get(name)
		if !ok {
			http.Error(w, "agent not registered", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p)
	case http.MethodDelete:
		s.removeAgent(w, name)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listAgents(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   s.profileReg.List(),
	})
}

func (s *Server) registerAgent(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := readAll(r.Body, 256*1024)
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	// Accept JSON or YAML body — both can decode into Profile.
	var p regpkg.Profile
	if isJSONBody(r) {
		if err := json.Unmarshal(body, &p); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if err := yaml.Unmarshal(body, &p); err != nil {
			http.Error(w, "bad yaml: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := s.profileReg.Add(&p); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if s.profileDir != "" {
		path := filepath.Join(s.profileDir, p.Name+".yaml")
		out, err := yaml.Marshal(&p)
		if err != nil {
			http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Write atomically: tmp + rename.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, 0o644); err != nil {
			http.Error(w, "write profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			http.Error(w, "commit profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.audit.Event("agent_registered", map[string]any{"name": p.Name, "tier": p.Tier})
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(&p)
}

func (s *Server) removeAgent(w http.ResponseWriter, name string) {
	p, ok := s.profileReg.Get(name)
	if !ok {
		http.Error(w, "agent not registered", http.StatusNotFound)
		return
	}
	if strings.HasPrefix(p.Source, "builtin:") {
		http.Error(w, "cannot delete a built-in profile; supply an override yaml that disables it instead", http.StatusForbidden)
		return
	}
	s.profileReg.Remove(name)
	if s.profileDir != "" {
		_ = os.Remove(filepath.Join(s.profileDir, name+".yaml"))
		_ = os.Remove(filepath.Join(s.profileDir, name+".yml"))
	}
	s.audit.Event("agent_removed", map[string]any{"name": name})
	w.WriteHeader(http.StatusNoContent)
}

// handleAccountsTest serves POST /v1/accounts/test {"pool": "..."}.
func (s *Server) handleAccountsTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var body struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Pool == "" {
		http.Error(w, "pool required", http.StatusBadRequest)
		return
	}
	acc, remaining, err := s.accountMgr.Peek(body.Pool)
	resp := map[string]any{
		"pool":                  body.Pool,
		"cooldown_remaining_ms": remaining.Milliseconds(),
	}
	if err == nil && acc != nil {
		resp["would_use"] = acc.ID
		resp["status"] = "ok"
	} else if errors.Is(err, accounts.ErrPoolExhausted) {
		resp["status"] = "exhausted"
	} else {
		resp["status"] = "error"
		resp["error"] = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleAccountsV2 enumerates all pools (extends the v1 single-pool view).
func (s *Server) handleAccountsV2(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	out := map[string]any{
		"object": "list",
		"pools":  s.accountMgr.Status(),
	}
	if s.auth != nil {
		out["legacy_anthropic"] = s.auth.Snapshot()
	}
	_ = json.NewEncoder(w).Encode(out)
}

// readAll reads up to maxBytes from r and returns the full body, or an error
// if the body exceeds maxBytes.
func readAll(r interface{ Read(p []byte) (int, error) }, maxBytes int) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > maxBytes {
				return nil, fmt.Errorf("body too large")
			}
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}

func isJSONBody(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/json")
}
