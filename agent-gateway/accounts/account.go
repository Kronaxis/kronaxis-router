package accounts

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// CredType describes how the runner installs the credential.
type CredType string

const (
	TypeAPIKey    CredType = "api-key"
	TypeOAuth     CredType = "oauth"
	TypeConfigDir CredType = "config-dir"
)

// CooldownPolicy maps outcome classes to cooldown durations.
type CooldownPolicy struct {
	AuthFailure Duration `yaml:"auth_failure,omitempty" json:"auth_failure,omitempty"`
	RateLimit   Duration `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	Transient   Duration `yaml:"transient,omitempty" json:"transient,omitempty"`
}

// Duration is a yaml-friendly time.Duration.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Value == "" {
		return nil
	}
	dd, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(dd)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// Account represents a single credential the gateway can use to invoke a CLI.
//
// `Credential` accepts environment-variable references via `${VAR}` syntax;
// resolution happens at checkout time, not load time, so rotating an env var
// at runtime is reflected on the next checkout without restarting the
// gateway.
type Account struct {
	ID             string         `yaml:"id" json:"id"`
	Pool           string         `yaml:"pool" json:"pool"`
	Type           CredType       `yaml:"type" json:"type"`
	Credential     string         `yaml:"credential,omitempty" json:"-"`
	CooldownPolicy CooldownPolicy `yaml:"cooldown_policy,omitempty" json:"cooldown_policy,omitempty"`
	TosPersonalUse bool           `yaml:"tos_personal_use,omitempty" json:"tos_personal_use,omitempty"`
	Notes          string         `yaml:"notes,omitempty" json:"notes,omitempty"`
	Enabled        *bool          `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// runtime
	disabledUntil atomic.Int64 // unix nano; 0 == active
	successCount  atomic.Uint64
	failureCount  atomic.Uint64
	lastError     atomic.Value // string
	lastUsed      atomic.Int64
}

// IsEnabled returns whether the account participates in checkout.
func (a *Account) IsEnabled() bool {
	if a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

// IsActive returns true when the account is enabled and not in cooldown.
func (a *Account) IsActive(now time.Time) bool {
	if !a.IsEnabled() {
		return false
	}
	until := a.disabledUntil.Load()
	if until == 0 {
		return true
	}
	return now.UnixNano() >= until
}

// CooldownRemaining returns the remaining cooldown; zero or negative when active.
func (a *Account) CooldownRemaining(now time.Time) time.Duration {
	until := a.disabledUntil.Load()
	if until == 0 {
		return 0
	}
	return time.Duration(until - now.UnixNano())
}

// Resolve replaces ${VAR} references in Credential with the environment
// value. Returns an error if any referenced var is unset.
func (a *Account) Resolve() (string, error) {
	return interpolateEnv(a.Credential)
}

// Stats returns a public snapshot of the account's runtime counters.
func (a *Account) Stats() Stats {
	lastErr, _ := a.lastError.Load().(string)
	return Stats{
		ID:                a.ID,
		Pool:              a.Pool,
		Enabled:           a.IsEnabled(),
		DisabledUntilUnix: a.disabledUntil.Load(),
		Successes:         a.successCount.Load(),
		Failures:          a.failureCount.Load(),
		LastError:         lastErr,
		LastUsedUnix:      a.lastUsed.Load(),
	}
}

// Stats is the public marshallable snapshot.
type Stats struct {
	ID                string `json:"id"`
	Pool              string `json:"pool"`
	Enabled           bool   `json:"enabled"`
	DisabledUntilUnix int64  `json:"disabled_until_unix"`
	Successes         uint64 `json:"successes"`
	Failures          uint64 `json:"failures"`
	LastError         string `json:"last_error,omitempty"`
	LastUsedUnix      int64  `json:"last_used_unix,omitempty"`
}

var envRefRe = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

func interpolateEnv(s string) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	var missing []string
	out := envRefRe.ReplaceAllStringFunc(s, func(m string) string {
		key := m[2 : len(m)-1]
		v, ok := os.LookupEnv(key)
		if !ok {
			missing = append(missing, key)
			return ""
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("env var(s) not set: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// Validate enforces required fields and enum membership.
func (a *Account) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("account.id is required")
	}
	if strings.TrimSpace(a.Pool) == "" {
		return fmt.Errorf("account[%s].pool is required", a.ID)
	}
	switch a.Type {
	case TypeAPIKey, TypeOAuth, TypeConfigDir:
	case "":
		return fmt.Errorf("account[%s].type is required", a.ID)
	default:
		return fmt.Errorf("account[%s].type %q invalid", a.ID, a.Type)
	}
	if strings.TrimSpace(a.Credential) == "" {
		return fmt.Errorf("account[%s].credential is required", a.ID)
	}
	if a.Type == TypeConfigDir {
		// Must be an absolute path (after env interpolation we'd verify
		// existence at checkout; here just reject obvious non-paths).
		if !strings.HasPrefix(a.Credential, "/") && !strings.HasPrefix(a.Credential, "${") {
			return fmt.Errorf("account[%s].credential for config-dir must be absolute path or ${VAR}", a.ID)
		}
	}
	return nil
}
