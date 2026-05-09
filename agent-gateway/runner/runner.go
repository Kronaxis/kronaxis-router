package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/shlex"

	"github.com/kronaxis/agent-gateway/accounts"
	"github.com/kronaxis/agent-gateway/registry"
	"github.com/kronaxis/agent-gateway/workspace"
)

// Message is a single chat-completions message.
type Message struct {
	Role    string
	Content string
}

// Request bundles everything the runner needs for one CLI invocation.
type Request struct {
	Profile       *registry.Profile
	Submodel      string
	Messages      []Message
	SystemPrompt  string
	AppendSystem  string
	Workspace     workspace.Workspace
	Lease         *accounts.Lease // resolved before the call; runner releases on completion
	TimeoutSec    int

	// First-class passthrough fields. Adapters consult them per-profile; the
	// generic path ignores them. Profile.FlagsPassthrough decides which to
	// add to argv.
	Agent             string
	PermissionMode    string
	Effort            string
	AllowedTools      []string
	DisallowedTools   []string
	MCPConfig         string
	AddDirs           []string
	Bare              bool
	IncludeHookEvents bool

	// Extra environment to add (after auth injection).
	ExtraEnv []string
}

// Result summarises an invocation after the parser has finished.
type Result struct {
	ExitCode int
	Outcome  accounts.Outcome
	ErrMsg   string
}

// classifyExitCode maps a subprocess exit code to an account-pool outcome
// per the profile's exit_codes configuration.
func classifyExitCode(p *registry.Profile, code int) accounts.Outcome {
	in := func(slice []int, c int) bool {
		for _, x := range slice {
			if x == c {
				return true
			}
		}
		return false
	}
	if code == 0 {
		return accounts.OutcomeSuccess
	}
	if in(p.CLI.ExitCodes.Success, code) {
		return accounts.OutcomeSuccess
	}
	if in(p.CLI.ExitCodes.RateLimit, code) {
		return accounts.OutcomeRateLimit
	}
	if in(p.CLI.ExitCodes.AuthFailure, code) {
		return accounts.OutcomeAuthFailure
	}
	return accounts.OutcomeTransient
}

// Run launches the CLI configured by req.Profile, streams events through
// parser, and returns a Result. The lease is released on return.
func Run(ctx context.Context, req Request, parser Parser, events chan<- Event) (Result, error) {
	if req.Profile == nil {
		return Result{}, errors.New("runner.Run: nil Profile")
	}
	if parser == nil {
		return Result{}, errors.New("runner.Run: nil Parser")
	}
	if req.Workspace == nil {
		return Result{}, errors.New("runner.Run: nil Workspace")
	}

	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args, err := buildArgs(req)
	if err != nil {
		return Result{}, err
	}
	if len(args) == 0 {
		return Result{}, errors.New("runner.Run: empty cli command after substitution")
	}
	bin := args[0]
	rest := args[1:]
	cmd := exec.CommandContext(runCtx, bin, rest...)
	cmd.Dir = req.Workspace.Path()
	// Run the CLI in its own process group so we can SIGKILL the whole tree
	// on context cancellation. Without Setpgid, killing the direct child
	// (e.g. /bin/sh) leaves grandchildren (e.g. sleep) orphaned and adopted
	// by init. Cancel.go below sends SIGKILL to -pgid to nuke the whole
	// subtree atomically.
	configureProcessGroup(cmd)

	env, err := buildEnv(req)
	if err != nil {
		return Result{}, err
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", bin, err)
	}

	// On context cancellation, kill the whole process group so grandchildren
	// (shells spawning sleeps, node spawning child workers, etc) don't leak.
	// Note: exec.CommandContext already sends SIGKILL to the direct child,
	// but that doesn't reach grandchildren. This goroutine adds the group kill.
	cancelDone := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			killProcessGroup(cmd)
		case <-cancelDone:
		}
	}()
	defer close(cancelDone)

	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	go func() {
		defer stdin.Close()
		feedStdin(req, stdin)
	}()

	parseErr := parser.Parse(stdout, events)
	waitErr := cmd.Wait()

	exitCode := 0
	var errMsg string
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
		errMsg = waitErr.Error()
		if parseErr == nil {
			select {
			case events <- Event{Type: EventError, Error: errMsg}:
			default:
			}
		}
	}

	outcome := classifyExitCode(req.Profile, exitCode)
	if req.Lease != nil {
		req.Lease.Release(outcome, errMsg)
	}
	return Result{ExitCode: exitCode, Outcome: outcome, ErrMsg: errMsg}, parseErr
}

// buildArgs resolves the command template, substituting {model} and feeding
// argv through shlex so quoting works.
func buildArgs(req Request) ([]string, error) {
	tmpl := req.Profile.CLI.Command
	model := req.Submodel
	if model == "" {
		model = req.Profile.Submodel.Default
	}
	tmpl = strings.ReplaceAll(tmpl, "{model}", model)
	args, err := shlex.Split(tmpl)
	if err != nil {
		return nil, fmt.Errorf("shlex split %q: %w", tmpl, err)
	}
	if req.Profile.CLI.InputMode == registry.InputArgv {
		args = append(args, flattenMessages(req.Messages))
	}
	return args, nil
}

// buildEnv constructs the subprocess environment. Order:
//  1. cleaned-host env (drop credentials we're about to override)
//  2. profile.auth.env_vars with `{credential}` substituted from the lease
//  3. config-dir injection (HOME / CLAUDE_CONFIG_DIR / etc) per profile
//  4. ExtraEnv from the request
func buildEnv(req Request) ([]string, error) {
	out := cleanHostEnv()
	cred := ""
	configDir := ""
	if req.Lease != nil {
		acc := req.Lease.Account()
		resolved, err := acc.Resolve()
		if err != nil {
			return nil, err
		}
		switch acc.Type {
		case accounts.TypeAPIKey, accounts.TypeOAuth:
			cred = resolved
		case accounts.TypeConfigDir:
			configDir = resolved
		}
	}
	for k, v := range req.Profile.Auth.EnvVars {
		v = strings.ReplaceAll(v, "{credential}", cred)
		v = strings.ReplaceAll(v, "{config_dir}", configDir)
		out = append(out, k+"="+v)
	}
	if req.Profile.Auth.Injection == registry.AuthInjectConfigDir && configDir != "" {
		// Treat the credential as a directory the CLI reads. We export both
		// HOME (most CLIs respect it) and CLAUDE_CONFIG_DIR (used by claude).
		out = append(out, "HOME="+configDir)
		if req.Profile.Auth.EnvVars["CLAUDE_CONFIG_DIR"] == "" {
			out = append(out, "CLAUDE_CONFIG_DIR="+configDir)
		}
	}
	out = append(out, req.ExtraEnv...)
	return out, nil
}

// cleanHostEnv strips known credential vars from the inherited env so the
// subprocess only sees the credential we explicitly inject.
func cleanHostEnv() []string {
	strip := []string{
		"ANTHROPIC_API_KEY=",
		"OPENAI_API_KEY=",
		"GOOGLE_API_KEY=",
		"GEMINI_API_KEY=",
		"XAI_API_KEY=",
		"GROK_API_KEY=",
		"CLAUDE_CONFIG_DIR=",
		"CLAUDE_CODE_",
		"CODEX_HOME=",
	}
	out := []string{}
	for _, kv := range os.Environ() {
		drop := false
		for _, p := range strip {
			if strings.HasPrefix(kv, p) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

func feedStdin(req Request, w io.WriteCloser) {
	switch req.Profile.CLI.InputMode {
	case registry.InputStdin:
		_, _ = io.WriteString(w, flattenMessages(req.Messages))
		_, _ = io.WriteString(w, "\n")
	case registry.InputArgv, registry.InputTempfile:
		// argv: prompt is already in args; tempfile: caller pre-wrote the file.
	}
}

func flattenMessages(msgs []Message) string {
	if len(msgs) == 0 {
		return ""
	}
	if len(msgs) == 1 {
		return msgs[0].Content
	}
	var b strings.Builder
	for _, m := range msgs {
		role := strings.ToUpper(m.Role)
		fmt.Fprintf(&b, "%s: %s\n\n", role, m.Content)
	}
	return strings.TrimSpace(b.String())
}
