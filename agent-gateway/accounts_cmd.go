package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/kronaxis/agent-gateway/accounts"
)

// runAccountsCmd handles `agent-gateway accounts <subcommand>`.
//
//	add   <pool>          interactive wizard, appends to accounts.yaml
//	list                  show all configured accounts
//	test  <pool>          dry-run a checkout from the pool
func runAccountsCmd(args []string) {
	if len(args) == 0 {
		printAccountsUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "add":
		runAccountsAdd(args[1:])
	case "list", "ls":
		runAccountsList(args[1:])
	case "test", "peek":
		runAccountsTest(args[1:])
	case "help", "-h", "--help":
		printAccountsUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown accounts subcommand: %q\n", args[0])
		printAccountsUsage()
		os.Exit(2)
	}
}

func printAccountsUsage() {
	fmt.Fprintln(os.Stderr, `usage: agent-gateway accounts <subcommand>

subcommands:
  add  <pool>     interactively add an account to the named pool
  list            list configured accounts (group by pool)
  test <pool>     dry-run an account checkout (peek)

env vars:
  AGENT_GATEWAY_ACCOUNTS_FILE  path to accounts.yaml (default ./accounts.yaml)`)
}

func accountsPath() string {
	if v := os.Getenv("AGENT_GATEWAY_ACCOUNTS_FILE"); v != "" {
		return v
	}
	return "accounts.yaml"
}

func runAccountsAdd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "add: pool name required (e.g. openai-api-key, anthropic-oauth, google-aistudio)")
		os.Exit(2)
	}
	pool := args[0]
	path := accountsPath()
	in := bufio.NewReader(os.Stdin)

	fmt.Printf("Adding account to pool %q in %s\n", pool, path)
	fmt.Printf("(Press Ctrl-C at any time to abort.)\n\n")

	id := promptDefault(in, "Account ID", deriveID(pool))
	if id == "" {
		fail("ID is required")
	}

	credType := promptChoice(in, "Credential type",
		[]string{"api-key", "oauth", "config-dir"},
		guessCredType(pool))

	credential := ""
	switch credType {
	case "api-key":
		credential = promptCredential(in, pool)
	case "config-dir":
		credential = promptDefault(in, "Absolute path to config directory", "")
		if !strings.HasPrefix(credential, "/") {
			fail("config-dir credential must be an absolute path")
		}
	case "oauth":
		credential = promptDefault(in, "Path to credentials file (or ${ENV_VAR})", "")
		if credential == "" {
			fail("OAuth credential path is required")
		}
		fmt.Println()
		fmt.Println("⚠  OAuth subscriptions: confirm this account is for PERSONAL, NON-COMMERCIAL use only.")
		fmt.Println("   Pooling consumer subscriptions for commercial / multi-tenant use is a ToS violation.")
		fmt.Println("   Use API keys (api-key type) for any commercial scenario.")
		ack := promptYN(in, "I confirm this account is for personal use only", false)
		if !ack {
			fail("aborted: OAuth account requires personal-use acknowledgement")
		}
	}

	cd := promptCooldownPolicy(in, credType)
	enabled := promptYN(in, "Enable account immediately", true)

	acc := &accounts.Account{
		ID:             id,
		Pool:           pool,
		Type:           accounts.CredType(credType),
		Credential:     credential,
		CooldownPolicy: cd,
		Enabled:        &enabled,
		TosPersonalUse: credType == "oauth",
	}
	if err := acc.Validate(); err != nil {
		fail("validation failed: " + err.Error())
	}

	// Optional smoke check: try resolving the credential right now.
	if credType == "api-key" || credType == "oauth" {
		if _, err := acc.Resolve(); err != nil {
			fmt.Printf("\n⚠  Credential failed to resolve: %v\n", err)
			fmt.Println("    The account will still be saved; fix the env var before invoking.")
		}
	}

	if err := appendAccount(path, acc); err != nil {
		fail("write " + path + ": " + err.Error())
	}
	fmt.Printf("\n✓ Saved %s to pool %q in %s\n", id, pool, path)
	fmt.Println("  The gateway picks up account changes on restart (no hot-reload yet for accounts).")
}

func runAccountsList(_ []string) {
	mgr := accounts.New()
	if err := mgr.Load(accountsPath()); err != nil {
		fail(err.Error())
	}
	st := mgr.Status()
	if len(st) == 0 {
		fmt.Printf("(no accounts configured in %s)\n", accountsPath())
		return
	}
	for _, p := range st {
		fmt.Printf("\n=== pool: %s (%d/%d active) ===\n", p.Pool, p.ActiveCount, p.AccountCount)
		for _, a := range p.Accounts {
			fmt.Printf("  %-25s success=%-5d fail=%-3d enabled=%v\n",
				a.ID, a.Successes, a.Failures, a.Enabled)
		}
	}
}

func runAccountsTest(args []string) {
	if len(args) == 0 {
		fail("test: pool name required")
	}
	pool := args[0]
	mgr := accounts.New()
	if err := mgr.Load(accountsPath()); err != nil {
		fail(err.Error())
	}
	acc, remaining, err := mgr.Peek(pool)
	if err != nil {
		fmt.Printf("pool %q: %v (cooldown remaining: %s)\n", pool, err, remaining)
		return
	}
	fmt.Printf("pool %q: would issue %s (cooldown remaining: %s)\n", pool, acc.ID, remaining)
}

// ---- helpers ----

func appendAccount(path string, acc *accounts.Account) error {
	type fileShape struct {
		Accounts []*accounts.Account `yaml:"accounts"`
	}
	var f fileShape
	if raw, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(raw, &f)
	}
	// Reject duplicate IDs.
	for _, ex := range f.Accounts {
		if ex.ID == acc.ID && ex.Pool == acc.Pool {
			return fmt.Errorf("account %q already exists in pool %q", acc.ID, acc.Pool)
		}
	}
	f.Accounts = append(f.Accounts, acc)
	out, err := yaml.Marshal(&f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600) // 600: contains credentials
}

func promptDefault(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptChoice(r *bufio.Reader, label string, choices []string, def string) string {
	for {
		fmt.Printf("%s (%s) [%s]: ", label, strings.Join(choices, "/"), def)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			line = def
		}
		for _, c := range choices {
			if c == line {
				return c
			}
		}
		fmt.Printf("  invalid; pick one of: %s\n", strings.Join(choices, ", "))
	}
}

func promptYN(r *bufio.Reader, label string, def bool) bool {
	defStr := "y/N"
	if def {
		defStr = "Y/n"
	}
	for {
		fmt.Printf("%s [%s]: ", label, defStr)
		line, _ := r.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			return def
		}
		if line == "y" || line == "yes" {
			return true
		}
		if line == "n" || line == "no" {
			return false
		}
	}
}

// promptCredential gets the credential value, hiding it on stdin if
// possible. Accepts either a literal value or a ${VAR} env reference.
func promptCredential(r *bufio.Reader, pool string) string {
	envHint := guessEnvVar(pool)
	fmt.Printf("Credential value (paste key, OR type ${VAR_NAME} for env reference, suggested env: %s)\n  > ", envHint)
	if term.IsTerminal(int(syscall.Stdin)) {
		// Don't echo the literal value.
		bytes, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err == nil {
			s := strings.TrimSpace(string(bytes))
			if s == "" {
				return "${" + envHint + "}"
			}
			return s
		}
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return "${" + envHint + "}"
	}
	return line
}

func promptCooldownPolicy(r *bufio.Reader, credType string) accounts.CooldownPolicy {
	// Sensible defaults per credential type.
	rate, auth, transient := "5m", "5m", "30s"
	if credType == "oauth" {
		rate, auth = "5h", "5h" // match Claude Pro/Max 5-hour window
	}
	rateStr := promptDefault(r, "Cooldown on rate-limit (e.g. 5m, 5h)", rate)
	authStr := promptDefault(r, "Cooldown on auth-failure (e.g. 5m, 5h)", auth)
	transStr := promptDefault(r, "Cooldown on transient error (e.g. 30s)", transient)
	parse := func(s string) accounts.Duration {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0
		}
		return accounts.Duration(d)
	}
	return accounts.CooldownPolicy{
		RateLimit:   parse(rateStr),
		AuthFailure: parse(authStr),
		Transient:   parse(transStr),
	}
}

func guessCredType(pool string) string {
	switch {
	case strings.Contains(pool, "oauth"):
		return "oauth"
	case strings.HasSuffix(pool, "-api-key") || strings.Contains(pool, "openai") || strings.Contains(pool, "google") || strings.Contains(pool, "anthropic-api"):
		return "api-key"
	default:
		return "api-key"
	}
}

func guessEnvVar(pool string) string {
	switch {
	case strings.Contains(pool, "openai"):
		return "OPENAI_API_KEY"
	case strings.Contains(pool, "anthropic-api"):
		return "ANTHROPIC_API_KEY"
	case strings.Contains(pool, "anthropic-oauth"), strings.Contains(pool, "claude-oauth"):
		return "CLAUDE_CONFIG_DIR"
	case strings.Contains(pool, "google"), strings.Contains(pool, "gemini"):
		return "GEMINI_API_KEY"
	case strings.Contains(pool, "xai"), strings.Contains(pool, "grok"):
		return "XAI_API_KEY"
	default:
		return strings.ToUpper(strings.ReplaceAll(pool, "-", "_")) + "_KEY"
	}
}

func deriveID(pool string) string {
	return pool + "-1"
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}

// keep imports honest in case we add more uses later
var _ = context.Background
