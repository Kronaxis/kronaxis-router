package accounts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// Outcome classifies the result of a CLI invocation so the pool can update
// the leased account's cooldown / counters.
type Outcome int

const (
	OutcomeSuccess Outcome = iota
	OutcomeRateLimit
	OutcomeAuthFailure
	OutcomeTransient
)

// ErrPoolExhausted indicates every account in the pool is in cooldown.
var ErrPoolExhausted = errors.New("pool exhausted: all accounts in cooldown")

// ErrUnknownPool indicates the pool name has no accounts registered.
var ErrUnknownPool = errors.New("pool not configured")

// Lease wraps a checked-out account; Release MUST be called by the runner
// after the CLI invocation completes. Releasing twice is a no-op.
type Lease struct {
	pool     *Pool
	account  *Account
	released atomic.Bool
}

// Account returns the underlying *Account. The runner reads the credential
// via account.Resolve() so env interpolation happens at checkout time.
func (l *Lease) Account() *Account { return l.account }

// Release returns the account to the pool, applying cooldown if outcome != success.
func (l *Lease) Release(outcome Outcome, errMsg string) {
	if l.released.Swap(true) {
		return
	}
	l.pool.release(l.account, outcome, errMsg)
}

// Manager owns all configured pools and serves checkouts.
type Manager struct {
	mu    sync.RWMutex
	pools map[string]*Pool
}

// Pool is a single round-robin queue of accounts sharing a pool name.
type Pool struct {
	name     string
	mu       sync.RWMutex
	accounts []*Account
	rr       atomic.Uint64
}

// New constructs an empty manager.
func New() *Manager {
	return &Manager{pools: map[string]*Pool{}}
}

type accountsFile struct {
	Accounts []*Account `yaml:"accounts"`
}

// Load reads accounts.yaml from the given path. If path is a directory, all
// *.yaml/*.yml inside are loaded and merged. Missing path is not an error.
func (m *Manager) Load(path string) error {
	if path == "" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	var paths []string
	if st.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return fmt.Errorf("read dir %s: %w", path, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if !strings.HasSuffix(n, ".yaml") && !strings.HasSuffix(n, ".yml") {
				continue
			}
			paths = append(paths, filepath.Join(path, n))
		}
	} else {
		paths = []string{path}
	}
	for _, p := range paths {
		if err := m.loadFile(p); err != nil {
			return fmt.Errorf("load %s: %w", p, err)
		}
	}
	return nil
}

func (m *Manager) loadFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var f accountsFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	for _, acc := range f.Accounts {
		if err := acc.Validate(); err != nil {
			return err
		}
		m.add(acc)
	}
	return nil
}

func (m *Manager) add(a *Account) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pools[a.Pool]
	if !ok {
		p = &Pool{name: a.Pool}
		m.pools[a.Pool] = p
	}
	p.mu.Lock()
	// Replace if same ID exists; otherwise append.
	replaced := false
	for i, existing := range p.accounts {
		if existing.ID == a.ID {
			p.accounts[i] = a
			replaced = true
			break
		}
	}
	if !replaced {
		p.accounts = append(p.accounts, a)
	}
	p.mu.Unlock()
}

// Add registers an account at runtime (e.g. from /v1/accounts POST).
func (m *Manager) Add(a *Account) error {
	if err := a.Validate(); err != nil {
		return err
	}
	m.add(a)
	return nil
}

// Pools returns the names of all configured pools.
func (m *Manager) Pools() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.pools))
	for n := range m.pools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Status returns a snapshot of all pools and their accounts.
func (m *Manager) Status() []PoolStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PoolStatus, 0, len(m.pools))
	now := time.Now()
	for name, p := range m.pools {
		p.mu.RLock()
		accs := make([]Stats, 0, len(p.accounts))
		active := 0
		for _, a := range p.accounts {
			s := a.Stats()
			if a.IsActive(now) {
				active++
			}
			accs = append(accs, s)
		}
		p.mu.RUnlock()
		out = append(out, PoolStatus{
			Pool:           name,
			AccountCount:   len(accs),
			ActiveCount:    active,
			Accounts:       accs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pool < out[j].Pool })
	return out
}

// PoolStatus is the marshallable view of one pool.
type PoolStatus struct {
	Pool         string  `json:"pool"`
	AccountCount int     `json:"account_count"`
	ActiveCount  int     `json:"active_count"`
	Accounts     []Stats `json:"accounts"`
}

// Checkout selects an active account from the named pool using round-robin.
// Returns ErrPoolExhausted when every account is in cooldown.
func (m *Manager) Checkout(poolName string) (*Lease, error) {
	m.mu.RLock()
	p, ok := m.pools[poolName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPool, poolName)
	}
	return p.checkout()
}

// Peek returns what Checkout WOULD return without actually leasing. Used by
// /v1/accounts/test for dry-run inspection.
func (m *Manager) Peek(poolName string) (*Account, time.Duration, error) {
	m.mu.RLock()
	p, ok := m.pools[poolName]
	m.mu.RUnlock()
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrUnknownPool, poolName)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	soonest := time.Duration(1<<62 - 1)
	for _, a := range p.accounts {
		if a.IsActive(now) {
			return a, 0, nil
		}
		rem := a.CooldownRemaining(now)
		if rem > 0 && rem < soonest {
			soonest = rem
		}
	}
	if soonest == time.Duration(1<<62-1) {
		soonest = 0
	}
	return nil, soonest, ErrPoolExhausted
}

func (p *Pool) checkout() (*Lease, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.accounts)
	if n == 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPool, p.name)
	}
	now := time.Now()
	// Try every account starting at the round-robin cursor.
	start := int(p.rr.Add(1)-1) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		a := p.accounts[idx]
		if a.IsActive(now) {
			a.lastUsed.Store(now.UnixNano())
			return &Lease{pool: p, account: a}, nil
		}
	}
	return nil, ErrPoolExhausted
}

func (p *Pool) release(a *Account, outcome Outcome, errMsg string) {
	now := time.Now()
	switch outcome {
	case OutcomeSuccess:
		a.successCount.Add(1)
		a.disabledUntil.Store(0)
	case OutcomeRateLimit:
		a.failureCount.Add(1)
		a.lastError.Store(errMsg)
		dur := a.CooldownPolicy.RateLimit.Std()
		if dur == 0 {
			dur = 5 * time.Minute
		}
		a.disabledUntil.Store(now.Add(dur).UnixNano())
	case OutcomeAuthFailure:
		a.failureCount.Add(1)
		a.lastError.Store(errMsg)
		dur := a.CooldownPolicy.AuthFailure.Std()
		if dur == 0 {
			dur = 5 * time.Minute
		}
		a.disabledUntil.Store(now.Add(dur).UnixNano())
	case OutcomeTransient:
		a.failureCount.Add(1)
		a.lastError.Store(errMsg)
		dur := a.CooldownPolicy.Transient.Std()
		if dur == 0 {
			dur = 30 * time.Second
		}
		a.disabledUntil.Store(now.Add(dur).UnixNano())
	}
}

// Reset clears cooldown on every account in the named pool. Useful for
// recovery after a transient external outage. Returns count cleared.
func (m *Manager) Reset(poolName string) int {
	m.mu.RLock()
	p, ok := m.pools[poolName]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, a := range p.accounts {
		if a.disabledUntil.Swap(0) != 0 {
			n++
		}
	}
	return n
}
