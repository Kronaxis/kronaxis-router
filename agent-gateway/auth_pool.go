package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// AuthPool holds named API-key/credentials accounts grouped by provider.
// It enables horizontal scaling past per-account limits: when one account is
// rate-limited or out of credit, the pool rotates to the next.
//
// For Claude Code OAuth subscriptions (Pro/Max), set provider=claude-cli and
// `claude_credentials_path` pointing at the per-account .credentials.json
// produced by `agent-gateway claude-login --name <id>`. Each request rotates
// to a different account; when one hits the 5-hour message limit, it is
// auto-disabled for the remainder of its window and the pool falls through
// to the next.
type AuthPool struct {
	mu       sync.RWMutex
	accounts map[string][]*Account
	rr       map[string]*atomic.Uint64
}

type Account struct {
	ID                    string `yaml:"id" json:"id"`
	Provider              string `yaml:"provider" json:"provider"`
	APIKey                string `yaml:"api_key,omitempty" json:"-"`
	APIKeyEnv             string `yaml:"api_key_env,omitempty" json:"-"`
	ClaudeCredentialsPath string `yaml:"claude_credentials_path,omitempty" json:"-"`
	ClaudeConfigDir       string `yaml:"claude_config_dir,omitempty" json:"-"` // alternative: full config dir
	Notes                 string `yaml:"notes,omitempty" json:"notes,omitempty"`

	// Optional: window-based budgeting (Claude OAuth subscriptions reset every
	// 5 hours; configure to match the user's tier).
	WindowDuration Duration `yaml:"window_duration,omitempty" json:"-"`
	WindowMaxUSD   float64  `yaml:"window_max_usd,omitempty" json:"window_max_usd,omitempty"`

	// runtime health
	disabled       atomic.Bool
	disabledUntil  atomic.Int64
	lastError      atomic.Value // string
	successCount   atomic.Uint64
	failureCount   atomic.Uint64

	// runtime: rolling window counters (cleared when window expires)
	winMu             sync.Mutex
	windowStartedAt   time.Time
	windowRequestsCnt uint64
	windowInputTok    uint64
	windowOutputTok   uint64
	windowUSDEquiv    float64

	// lifetime counters (never cleared)
	totalInputTok  atomic.Uint64
	totalOutputTok atomic.Uint64
	totalUSDEquiv  uint64 // store as cents*100 to keep atomic; helper converts
}

// Duration is a yaml-friendly time.Duration so config can use "5h", "1m", etc.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	dd, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(dd)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type authPoolFile struct {
	Accounts []*Account `yaml:"accounts"`
}

func loadAuthPool(path string) (*AuthPool, error) {
	pool := &AuthPool{
		accounts: map[string][]*Account{},
		rr:       map[string]*atomic.Uint64{},
	}
	if path == "" {
		return pool, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return pool, nil
		}
		return nil, fmt.Errorf("read auth pool %s: %w", path, err)
	}
	var f authPoolFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse auth pool %s: %w", path, err)
	}
	for _, acc := range f.Accounts {
		if acc.ID == "" || acc.Provider == "" {
			return nil, fmt.Errorf("auth pool: each account needs id and provider")
		}
		// Default 5h window for claude-cli (matches Claude Pro/Max tier limits).
		if acc.Provider == "claude-cli" && acc.WindowDuration.Std() == 0 {
			acc.WindowDuration = Duration(5 * time.Hour)
		}
		// ToS guard: every claude-cli account must have been registered via
		// `agent-gateway claude-login` (which writes an .personal-use-acknowledged
		// marker into the same dir as the credentials). Refuse to load
		// otherwise.
		if acc.Provider == "claude-cli" {
			credPath := acc.ClaudeCredentialsPath
			if credPath == "" && acc.ClaudeConfigDir != "" {
				credPath = filepath.Join(acc.ClaudeConfigDir, ".credentials.json")
			}
			if credPath == "" {
				return nil, fmt.Errorf("auth pool: claude-cli account %q needs claude_credentials_path or claude_config_dir", acc.ID)
			}
			ackPath := filepath.Join(filepath.Dir(credPath), ".personal-use-acknowledged")
			if _, err := os.Stat(ackPath); os.IsNotExist(err) {
				return nil, fmt.Errorf(
					"auth pool: claude-cli account %q is missing the personal-use acknowledgement at %s.\n"+
						"Add the account via `agent-gateway claude-login --name %s` -- this enforces the\n"+
						"ToS guard that pooled OAuth subscriptions are for personal use only. For commercial\n"+
						"deployments, use provider=anthropic with a paid API key instead.",
					acc.ID, ackPath, acc.ID)
			} else if err != nil {
				return nil, fmt.Errorf("auth pool: stat %s: %w", ackPath, err)
			}
		}
		pool.accounts[acc.Provider] = append(pool.accounts[acc.Provider], acc)
	}
	for provider := range pool.accounts {
		var c atomic.Uint64
		pool.rr[provider] = &c
	}
	return pool, nil
}

// Pick chooses the next available account. Same logic as before; window
// budget enforcement happens here too — accounts past WindowMaxUSD are
// skipped until the window rolls over.
func (p *AuthPool) Pick(provider, accountID string) (*Account, error) {
	if p == nil {
		return nil, errors.New("auth pool not configured")
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	accs := p.accounts[provider]
	if len(accs) == 0 {
		return nil, fmt.Errorf("no accounts for provider %q", provider)
	}
	if accountID != "" {
		for _, a := range accs {
			if a.ID == accountID {
				if a.IsAvailable() {
					return a, nil
				}
				return nil, fmt.Errorf("account %q is %s", accountID, a.unavailableReason())
			}
		}
		return nil, fmt.Errorf("account %q not found for provider %q", accountID, provider)
	}
	counter := p.rr[provider]
	for i := 0; i < len(accs); i++ {
		idx := int(counter.Add(1)-1) % len(accs)
		if accs[idx].IsAvailable() {
			return accs[idx], nil
		}
	}
	return nil, fmt.Errorf("all accounts disabled or over budget for provider %q", provider)
}

func (a *Account) IsEnabled() bool {
	if a.disabled.Load() {
		return false
	}
	until := a.disabledUntil.Load()
	if until > 0 && time.Now().UnixNano() < until {
		return false
	}
	return true
}

// IsAvailable = enabled AND not over its window budget.
func (a *Account) IsAvailable() bool {
	if !a.IsEnabled() {
		return false
	}
	if a.WindowMaxUSD > 0 {
		_, _, _, usdInWin := a.snapshotWindow()
		if usdInWin >= a.WindowMaxUSD {
			return false
		}
	}
	return true
}

func (a *Account) unavailableReason() string {
	if a.disabled.Load() {
		return "permanently disabled"
	}
	until := a.disabledUntil.Load()
	if until > 0 && time.Now().UnixNano() < until {
		left := time.Until(time.Unix(0, until)).Round(time.Second)
		return fmt.Sprintf("cooling down (%s remaining)", left)
	}
	if a.WindowMaxUSD > 0 {
		_, _, _, usdInWin := a.snapshotWindow()
		if usdInWin >= a.WindowMaxUSD {
			return fmt.Sprintf("over window budget ($%.4f >= $%.4f)", usdInWin, a.WindowMaxUSD)
		}
	}
	return "unknown"
}

// Disable knocks an account out for a duration. Setting d == 0 disables
// permanently. Provider-aware default cooldowns are picked at the call site
// via cooldownFor().
func (a *Account) Disable(reason string, d time.Duration) {
	a.lastError.Store(reason)
	if d <= 0 {
		a.disabled.Store(true)
		return
	}
	a.disabledUntil.Store(time.Now().Add(d).UnixNano())
}

func (a *Account) RecordSuccess() { a.successCount.Add(1) }
func (a *Account) RecordFailure() { a.failureCount.Add(1) }

// AddWindowUSD adds a directly-reported USD figure (e.g. from claude CLI's
// total_cost_usd) to the current window. This is used as the authoritative
// USD-equivalent for OAuth subscriptions, since the CLI tells us exactly
// what the same usage would have cost on the API.
func (a *Account) AddWindowUSD(usd float64) {
	if usd <= 0 {
		return
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	a.rolloverIfExpiredLocked()
	a.windowUSDEquiv += usd
}

// RecordUsage adds token consumption to the account's window + lifetime
// counters. usdEquivPriceIn/Out are the per-1M-token API-equivalent prices
// (so a Claude Pro/Max OAuth sub gets a $-equivalent comparison value vs.
// what the tokens would have cost on the API). Pass 0/0 to skip.
func (a *Account) RecordUsage(inputTokens, outputTokens int, usdEquivPriceIn1M, usdEquivPriceOut1M float64) {
	a.totalInputTok.Add(uint64(inputTokens))
	a.totalOutputTok.Add(uint64(outputTokens))

	usdEquiv := (float64(inputTokens)*usdEquivPriceIn1M + float64(outputTokens)*usdEquivPriceOut1M) / 1_000_000

	a.winMu.Lock()
	defer a.winMu.Unlock()
	a.rolloverIfExpiredLocked()
	a.windowRequestsCnt++
	a.windowInputTok += uint64(inputTokens)
	a.windowOutputTok += uint64(outputTokens)
	a.windowUSDEquiv += usdEquiv
}

func (a *Account) rolloverIfExpiredLocked() {
	if a.WindowDuration.Std() == 0 {
		return
	}
	if a.windowStartedAt.IsZero() {
		a.windowStartedAt = time.Now()
		return
	}
	if time.Since(a.windowStartedAt) >= a.WindowDuration.Std() {
		a.windowStartedAt = time.Now()
		a.windowRequestsCnt = 0
		a.windowInputTok = 0
		a.windowOutputTok = 0
		a.windowUSDEquiv = 0
	}
}

// snapshotWindow returns (started_at, requests_in_window, tokens_in_window_total, usd_equiv_in_window).
func (a *Account) snapshotWindow() (time.Time, uint64, uint64, float64) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	a.rolloverIfExpiredLocked()
	return a.windowStartedAt, a.windowRequestsCnt, a.windowInputTok + a.windowOutputTok, a.windowUSDEquiv
}

// ResolveKey returns the actual key string for an account, reading the env
// var if APIKeyEnv is set.
func (a *Account) ResolveKey() string {
	if a.APIKey != "" {
		return a.APIKey
	}
	if a.APIKeyEnv != "" {
		return os.Getenv(a.APIKeyEnv)
	}
	return ""
}

// Snapshot returns the public-facing summary for /v1/accounts.
func (p *AuthPool) Snapshot() []AccountSummary {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []AccountSummary
	for provider, accs := range p.accounts {
		for _, a := range accs {
			le, _ := a.lastError.Load().(string)
			startedAt, reqs, tok, usd := a.snapshotWindow()
			windowDur := a.WindowDuration.Std()
			var resetsAt string
			var pct float64
			if windowDur > 0 && !startedAt.IsZero() {
				resetsAt = startedAt.Add(windowDur).UTC().Format(time.RFC3339)
			}
			if a.WindowMaxUSD > 0 {
				pct = (usd / a.WindowMaxUSD) * 100
			}
			out = append(out, AccountSummary{
				ID:                  a.ID,
				Provider:            provider,
				Enabled:             a.IsEnabled(),
				Available:           a.IsAvailable(),
				Notes:               a.Notes,
				SuccessCount:        a.successCount.Load(),
				FailureCount:        a.failureCount.Load(),
				LastError:           le,
				WindowDurationSec:   int(windowDur.Seconds()),
				WindowResetsAt:      resetsAt,
				WindowRequests:      reqs,
				WindowTokensTotal:   tok,
				WindowUSDEquiv:      usd,
				WindowMaxUSD:        a.WindowMaxUSD,
				WindowPctConsumed:   pct,
				LifetimeInputTokens: a.totalInputTok.Load(),
				LifetimeOutputTokens: a.totalOutputTok.Load(),
			})
		}
	}
	return out
}

type AccountSummary struct {
	ID                   string  `json:"id"`
	Provider             string  `json:"provider"`
	Enabled              bool    `json:"enabled"`
	Available            bool    `json:"available"`
	Notes                string  `json:"notes,omitempty"`
	SuccessCount         uint64  `json:"success_count"`
	FailureCount         uint64  `json:"failure_count"`
	LastError            string  `json:"last_error,omitempty"`
	WindowDurationSec    int     `json:"window_duration_seconds,omitempty"`
	WindowResetsAt       string  `json:"window_resets_at,omitempty"`
	WindowRequests       uint64  `json:"window_requests,omitempty"`
	WindowTokensTotal    uint64  `json:"window_tokens_total,omitempty"`
	WindowUSDEquiv       float64 `json:"window_usd_equiv,omitempty"`
	WindowMaxUSD         float64 `json:"window_max_usd,omitempty"`
	WindowPctConsumed    float64 `json:"window_pct_consumed,omitempty"`
	LifetimeInputTokens  uint64  `json:"lifetime_input_tokens,omitempty"`
	LifetimeOutputTokens uint64  `json:"lifetime_output_tokens,omitempty"`
}

// providerForAdapter maps an adapter name to the auth-pool provider key.
func providerForAdapter(adapterName string) string {
	switch adapterName {
	case "claude-cli":
		return "claude-cli"
	case "anthropic-sdk":
		return "anthropic"
	case "gemini-cli":
		return "gemini"
	default:
		return ""
	}
}

// classifyAccountError returns (kind, cooldown). The cooldown depends on the
// provider type because OAuth subscriptions (claude-cli) reset on a 5-hour
// window, whereas API keys reset per-minute. Pass provider="" for the
// API-default 5-minute cooldown.
//
// Kinds:
//   ""           ok / unrelated
//   "rate_limit" 429 or rate-limit text -> cool down
//   "auth"       401/403 / invalid_api_key -> permanent disable
//   "credit"     insufficient credit / billing -> permanent disable
//   "transient"  network / 5xx -> brief cooldown
func classifyAccountError(errMsg, provider string) (string, time.Duration) {
	if errMsg == "" {
		return "", 0
	}
	low := strings.ToLower(errMsg)
	switch {
	case strings.Contains(low, "429"),
		strings.Contains(low, "rate_limit"),
		strings.Contains(low, "rate limit"),
		strings.Contains(low, "too many requests"),
		strings.Contains(low, "usage limit"),
		strings.Contains(low, "5-hour"),
		strings.Contains(low, "5 hour"),
		strings.Contains(low, "five-hour"):
		// OAuth subs reset per 5h window; API keys typically per minute.
		if provider == "claude-cli" {
			return "rate_limit", 5 * time.Hour
		}
		return "rate_limit", 5 * time.Minute
	case strings.Contains(low, "401"),
		strings.Contains(low, "403"),
		strings.Contains(low, "invalid_api_key"),
		strings.Contains(low, "authentication_error"),
		strings.Contains(low, "permission_error"):
		return "auth", 0
	case strings.Contains(low, "insufficient_credit"),
		strings.Contains(low, "credit_balance_too_low"),
		strings.Contains(low, "billing"):
		return "credit", 0
	case strings.Contains(low, "500"),
		strings.Contains(low, "502"),
		strings.Contains(low, "503"),
		strings.Contains(low, "504"),
		strings.Contains(low, "overloaded"):
		return "transient", 30 * time.Second
	}
	return "", 0
}

// recordOutcome updates the account counters and disables on rate-limit /
// auth / credit. Safe to call with a nil account.
func recordOutcome(acc *Account, runErr error) {
	if acc == nil {
		return
	}
	if runErr == nil {
		acc.RecordSuccess()
		return
	}
	acc.RecordFailure()
	kind, cooldown := classifyAccountError(runErr.Error(), acc.Provider)
	if kind == "" {
		return
	}
	acc.Disable(kind+": "+truncate(runErr.Error(), 240), cooldown)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
