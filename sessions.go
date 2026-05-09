package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Session is a server stored conversation transcript. Clients upload the
// full message array once with X-Kronaxis-Session-Create: true, get back
// a session ID, then send only the new turn on subsequent calls.
//
// Storage: Postgres kr_sessions table (created in runMigrations). Messages
// are kept as JSONB for fast hydration and easy admin inspection. TTL is
// per session; the SessionSweeper goroutine evicts idle sessions.
type Session struct {
	ID         string          `json:"id"`
	Messages   json.RawMessage `json:"messages"`
	CreatedAt  time.Time       `json:"created_at"`
	LastUsedAt time.Time       `json:"last_used_at"`
	TTLSeconds int             `json:"ttl_seconds"`
	Meta       json.RawMessage `json:"meta,omitempty"`
}

// SessionStore is the package wide handle. Set in main.go after the DB
// is connected; nil means "sessions disabled" and the proxy treats every
// request as stateless.
var sessionStore *SessionStoreImpl

// SessionStoreImpl wraps the DB-backed session store with an in-memory
// last-write cache so consecutive turns of the same session don't hit
// Postgres twice in a single second.
type SessionStoreImpl struct {
	db          *sql.DB
	defaultTTL  int
	cacheMu     sync.RWMutex
	cache       map[string]*Session
	cacheMaxAge time.Duration
}

// NewSessionStore returns a store backed by the given DB connection.
// defaultTTLSeconds is applied to new sessions when the caller doesn't
// override it via X-Kronaxis-Session-TTL.
func NewSessionStore(db *sql.DB, defaultTTLSeconds int) *SessionStoreImpl {
	if defaultTTLSeconds <= 0 {
		defaultTTLSeconds = 3600
	}
	return &SessionStoreImpl{
		db:          db,
		defaultTTL:  defaultTTLSeconds,
		cache:       map[string]*Session{},
		cacheMaxAge: 30 * time.Second,
	}
}

// runSessionMigrations creates the kr_sessions table if missing. Called
// from runMigrations once the DB connection is healthy.
func runSessionMigrations(db *sql.DB) error {
	stmt := `
CREATE TABLE IF NOT EXISTS kr_sessions (
  id            TEXT PRIMARY KEY,
  messages      JSONB NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_used_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ttl_seconds   INT NOT NULL DEFAULT 3600,
  meta          JSONB
);
CREATE INDEX IF NOT EXISTS kr_sessions_last_used_idx ON kr_sessions (last_used_at);`
	_, err := db.Exec(stmt)
	return err
}

// Create writes a new session, returning its generated ID.
func (s *SessionStoreImpl) Create(ctx context.Context, messages json.RawMessage, ttl int, meta json.RawMessage) (*Session, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sessions disabled (no database)")
	}
	if ttl <= 0 {
		ttl = s.defaultTTL
	}
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:         id,
		Messages:   messages,
		CreatedAt:  now,
		LastUsedAt: now,
		TTLSeconds: ttl,
		Meta:       meta,
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO kr_sessions (id, messages, created_at, last_used_at, ttl_seconds, meta)
VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.ID, []byte(sess.Messages), sess.CreatedAt, sess.LastUsedAt, sess.TTLSeconds, []byte(rawOrNull(sess.Meta)))
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	s.cachePut(sess)
	return sess, nil
}

// Get fetches a session by ID, returning ErrNoSession if absent or expired.
func (s *SessionStoreImpl) Get(ctx context.Context, id string) (*Session, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sessions disabled")
	}
	if cached := s.cacheGet(id); cached != nil {
		return cached, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, messages, created_at, last_used_at, ttl_seconds, COALESCE(meta, 'null'::jsonb)
FROM kr_sessions WHERE id = $1`, id)
	sess := &Session{}
	var msgs, meta []byte
	if err := row.Scan(&sess.ID, &msgs, &sess.CreatedAt, &sess.LastUsedAt, &sess.TTLSeconds, &meta); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSession
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	sess.Messages = msgs
	if len(meta) > 0 && string(meta) != "null" {
		sess.Meta = meta
	}
	// Expired?
	if !sess.IsAlive(time.Now()) {
		_ = s.Delete(ctx, id)
		return nil, ErrNoSession
	}
	s.cachePut(sess)
	return sess, nil
}

// AppendMessages atomically appends the new messages to the session and
// bumps last_used_at. Returns the updated session for hydration.
//
// Postgres semantics: the JSONB || operator on arrays appends in place.
// We pass `newMessages` as a JSONB array (the caller serialises
// []ChatMessage with json.Marshal) so the SQL is a single UPDATE.
func (s *SessionStoreImpl) AppendMessages(ctx context.Context, id string, newMessages json.RawMessage) (*Session, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sessions disabled")
	}
	now := time.Now().UTC()
	row := s.db.QueryRowContext(ctx, `
UPDATE kr_sessions
SET messages = messages || $2::jsonb,
    last_used_at = $3
WHERE id = $1
RETURNING id, messages, created_at, last_used_at, ttl_seconds, COALESCE(meta, 'null'::jsonb)`,
		id, []byte(newMessages), now)
	sess := &Session{}
	var msgs, meta []byte
	if err := row.Scan(&sess.ID, &msgs, &sess.CreatedAt, &sess.LastUsedAt, &sess.TTLSeconds, &meta); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSession
		}
		return nil, fmt.Errorf("update session: %w", err)
	}
	sess.Messages = msgs
	if len(meta) > 0 && string(meta) != "null" {
		sess.Meta = meta
	}
	s.cachePut(sess)
	return sess, nil
}

// Delete removes a session.
func (s *SessionStoreImpl) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM kr_sessions WHERE id = $1`, id)
	s.cacheMu.Lock()
	delete(s.cache, id)
	s.cacheMu.Unlock()
	return err
}

// List returns the most recently used sessions for /v1/sessions.
func (s *SessionStoreImpl) List(ctx context.Context, limit int) ([]Session, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sessions disabled")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, '[]'::jsonb AS messages, created_at, last_used_at, ttl_seconds, COALESCE(meta, 'null'::jsonb)
FROM kr_sessions
ORDER BY last_used_at DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess := Session{}
		var msgs, meta []byte
		if err := rows.Scan(&sess.ID, &msgs, &sess.CreatedAt, &sess.LastUsedAt, &sess.TTLSeconds, &meta); err != nil {
			return nil, err
		}
		sess.Messages = msgs
		if len(meta) > 0 && string(meta) != "null" {
			sess.Meta = meta
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Sweep deletes sessions whose last_used_at + ttl_seconds is in the past.
// Returns the number of rows removed.
func (s *SessionStoreImpl) Sweep(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
DELETE FROM kr_sessions
WHERE last_used_at + (ttl_seconds || ' seconds')::interval < NOW()`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.cacheMu.Lock()
		s.cache = map[string]*Session{}
		s.cacheMu.Unlock()
	}
	return int(n), nil
}

// IsAlive reports whether the session is still within its TTL window.
func (sess *Session) IsAlive(now time.Time) bool {
	expiry := sess.LastUsedAt.Add(time.Duration(sess.TTLSeconds) * time.Second)
	return now.Before(expiry)
}

// ErrNoSession is returned by Get and AppendMessages when the session
// doesn't exist or has expired.
var ErrNoSession = errors.New("session not found")

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sess_" + hex.EncodeToString(b), nil
}

func rawOrNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// ---- in memory cache (sessionCacheEntry to avoid collision with cache.go) ----

type sessionCacheEntry struct {
	sess     *Session
	storedAt time.Time
}

var _ = sessionCacheEntry{} // keep type alive for future use

func (s *SessionStoreImpl) cacheGet(id string) *Session {
	s.cacheMu.RLock()
	sess, ok := s.cache[id]
	s.cacheMu.RUnlock()
	if !ok {
		return nil
	}
	// Skip cached entries older than cacheMaxAge so we re-read after
	// writes from another gateway replica.
	if time.Since(sess.LastUsedAt) > s.cacheMaxAge {
		return nil
	}
	return sess
}

func (s *SessionStoreImpl) cachePut(sess *Session) {
	s.cacheMu.Lock()
	s.cache[sess.ID] = sess
	// Tiny LRU: cap cache at 1024 entries by random eviction.
	if len(s.cache) > 1024 {
		for k := range s.cache {
			delete(s.cache, k)
			break
		}
	}
	s.cacheMu.Unlock()
}

// SessionSweeperLoop runs a periodic Sweep on a ticker. Started from main.go.
func SessionSweeperLoop(ctx context.Context, store *SessionStoreImpl, interval time.Duration) {
	if store == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := store.Sweep(ctx); err != nil {
				logger.Printf("session sweep error: %v", err)
			} else if n > 0 {
				logger.Printf("session sweep: evicted %d expired sessions", n)
			}
		}
	}
}
