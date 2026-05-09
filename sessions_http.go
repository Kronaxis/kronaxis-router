package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleSessions services GET /v1/sessions (list) and DELETE /v1/sessions
// (no-op; per session DELETE goes through handleSessionItem).
func handleSessions(w http.ResponseWriter, r *http.Request) {
	if sessionStore == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "sessions disabled (no database)")
		return
	}
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		list, err := sessionStore.List(ctx, limit)
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   list,
		})
	default:
		w.Header().Set("Allow", "GET")
		writeErrorJSON(w, http.StatusMethodNotAllowed, "use POST /v1/chat/completions with X-Kronaxis-Session-Create header to create")
	}
}

// handleSessionItem services GET /v1/sessions/<id> and DELETE /v1/sessions/<id>.
func handleSessionItem(w http.ResponseWriter, r *http.Request) {
	if sessionStore == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "sessions disabled (no database)")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeErrorJSON(w, http.StatusBadRequest, "session id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		sess, err := sessionStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNoSession) {
				writeErrorJSON(w, http.StatusNotFound, "session not found")
				return
			}
			writeErrorJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sess)
	case http.MethodDelete:
		if err := sessionStore.Delete(ctx, id); err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// hydrateSessionRequest is the proxy.go side hook: detects session
// headers on an incoming chat completion request and either creates a
// new session, hydrates an existing one, or no-ops when sessions aren't
// in use. Returns (sessionID, hydratedRequest, isNewSession, err).
//
// Behaviour:
//   - X-Kronaxis-Session-Create: true → create a new session from the
//     full incoming messages, then process the request normally; the
//     session ID lands in a response header before the body is sent.
//   - X-Kronaxis-Session-ID: <id> → hydrate the stored messages, append
//     the new ones from the request, and forward the merged array.
//   - Neither header → return (nil, original request, false, nil).
func hydrateSessionRequest(ctx context.Context, r *http.Request, req *ChatRequest) (sessionID string, isNew bool, err error) {
	if sessionStore == nil {
		return "", false, nil
	}
	if v := strings.ToLower(r.Header.Get("X-Kronaxis-Session-Create")); v == "true" || v == "1" || v == "yes" {
		raw, mErr := json.Marshal(req.Messages)
		if mErr != nil {
			return "", false, mErr
		}
		ttl := 0
		if v := r.Header.Get("X-Kronaxis-Session-TTL"); v != "" {
			if n, _ := strconv.Atoi(v); n > 0 {
				ttl = n
			}
		}
		sess, sErr := sessionStore.Create(ctx, raw, ttl, nil)
		if sErr != nil {
			return "", false, sErr
		}
		return sess.ID, true, nil
	}
	if id := strings.TrimSpace(r.Header.Get("X-Kronaxis-Session-ID")); id != "" {
		sess, gErr := sessionStore.Get(ctx, id)
		if gErr != nil {
			return "", false, gErr
		}
		var stored []ChatMessage
		if uErr := json.Unmarshal(sess.Messages, &stored); uErr != nil {
			return "", false, uErr
		}
		// Append the new turn and persist (also bumps last_used_at).
		newRaw, _ := json.Marshal(req.Messages)
		if _, aErr := sessionStore.AppendMessages(ctx, id, newRaw); aErr != nil {
			return "", false, aErr
		}
		// Replace the request's messages with the merged transcript.
		req.Messages = append(stored, req.Messages...)
		return id, false, nil
	}
	return "", false, nil
}
