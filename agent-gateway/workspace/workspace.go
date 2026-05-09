package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Type mirrors registry.WorkspaceType (string-typed to avoid importing
// the registry package and creating a cycle).
type Type string

const (
	TypeWorktreeEphemeral Type = "worktree-ephemeral"
	TypeDirEphemeral      Type = "dir-ephemeral"
	TypeStateless         Type = "stateless"
)

// Workspace is the per-request execution context: a directory the CLI runs
// in, plus optional auxiliary directories (e.g. CLAUDE_CONFIG_DIR).
type Workspace interface {
	Path() string                       // working directory passed as cwd
	AuxPath(name string) (string, bool) // optional named aux dirs (e.g. "claude-config")
	Setup(ctx context.Context) error
	Cleanup() error
	RequestID() string
}

// Spec describes how to construct a Workspace. Set ConfigDirName on
// worktree types when the runner needs a sibling auxiliary dir.
type Spec struct {
	Type           Type
	Root           string // parent dir; created if missing
	RequestID      string // surfaces in dir name; defaults to random hex
	BaseRepo       string // optional source repo to copy in (worktree only)
	InitCmd        string // optional shell command run after Setup, cwd=Path()
	AuxDirs        []string // names of sibling aux dirs to create
	GitInit        bool    // if true (default for worktree), run `git init --quiet`
	GitUserEmail   string  // optional commit-author email
	GitUserName    string  // optional commit-author name
}

// New constructs a Workspace from the spec.
func New(spec Spec) (Workspace, error) {
	if spec.Type == "" {
		return nil, errors.New("workspace.Spec.Type is required")
	}
	if spec.RequestID == "" {
		spec.RequestID = randomHex(8)
	}
	if spec.Root == "" {
		spec.Root = filepath.Join(os.TempDir(), "kronaxis-workspaces")
	}
	switch spec.Type {
	case TypeWorktreeEphemeral:
		if !spec.hasGitInitField() {
			spec.GitInit = true
		}
		return &worktreeEphemeral{spec: spec}, nil
	case TypeDirEphemeral:
		return &dirEphemeral{spec: spec}, nil
	case TypeStateless:
		return &stateless{spec: spec}, nil
	default:
		return nil, fmt.Errorf("unknown workspace type %q", spec.Type)
	}
}

// hasGitInitField is a placeholder so callers can pass GitInit:false
// explicitly (Go zero-value boolean prevents distinguishing "unset" from
// "false"; treating it as "default true for worktree" is safe because
// worktree without git is supported via TypeDirEphemeral).
func (s Spec) hasGitInitField() bool { return false }

// ---- worktreeEphemeral ----

type worktreeEphemeral struct {
	spec    Spec
	root    string
	main    string
	auxes   map[string]string
	created bool
}

func (w *worktreeEphemeral) RequestID() string { return w.spec.RequestID }
func (w *worktreeEphemeral) Path() string      { return w.main }
func (w *worktreeEphemeral) AuxPath(name string) (string, bool) {
	p, ok := w.auxes[name]
	return p, ok
}

func (w *worktreeEphemeral) Setup(ctx context.Context) error {
	if err := os.MkdirAll(w.spec.Root, 0o755); err != nil {
		return fmt.Errorf("mkdir root: %w", err)
	}
	parent, err := os.MkdirTemp(w.spec.Root, "req-"+sanitize(w.spec.RequestID)+"-")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	w.root = parent
	w.main = filepath.Join(parent, "workspace")
	if err := os.MkdirAll(w.main, 0o755); err != nil {
		_ = os.RemoveAll(parent)
		return fmt.Errorf("mkdir workspace: %w", err)
	}
	w.auxes = map[string]string{}
	for _, name := range w.spec.AuxDirs {
		p := filepath.Join(parent, sanitize(name))
		if err := os.MkdirAll(p, 0o755); err != nil {
			_ = os.RemoveAll(parent)
			return fmt.Errorf("mkdir aux %s: %w", name, err)
		}
		w.auxes[name] = p
	}
	if w.spec.GitInit {
		cmd := exec.CommandContext(ctx, "git", "init", "--quiet")
		cmd.Dir = w.main
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(parent)
			return fmt.Errorf("git init: %w (%s)", err, string(out))
		}
		if w.spec.GitUserEmail != "" {
			_ = exec.CommandContext(ctx, "git", "-C", w.main, "config", "user.email", w.spec.GitUserEmail).Run()
		}
		if w.spec.GitUserName != "" {
			_ = exec.CommandContext(ctx, "git", "-C", w.main, "config", "user.name", w.spec.GitUserName).Run()
		}
	}
	if w.spec.InitCmd != "" {
		cmd := exec.CommandContext(ctx, "sh", "-c", w.spec.InitCmd)
		cmd.Dir = w.main
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(parent)
			return fmt.Errorf("init_cmd: %w (%s)", err, string(out))
		}
	}
	w.created = true
	return nil
}

func (w *worktreeEphemeral) Cleanup() error {
	if !w.created {
		return nil
	}
	w.created = false
	return os.RemoveAll(w.root)
}

// ---- dirEphemeral ----

type dirEphemeral struct {
	spec    Spec
	dir     string
	auxes   map[string]string
	created bool
}

func (d *dirEphemeral) RequestID() string { return d.spec.RequestID }
func (d *dirEphemeral) Path() string      { return d.dir }
func (d *dirEphemeral) AuxPath(name string) (string, bool) {
	p, ok := d.auxes[name]
	return p, ok
}

func (d *dirEphemeral) Setup(ctx context.Context) error {
	if err := os.MkdirAll(d.spec.Root, 0o755); err != nil {
		return fmt.Errorf("mkdir root: %w", err)
	}
	dir, err := os.MkdirTemp(d.spec.Root, "req-"+sanitize(d.spec.RequestID)+"-")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	d.dir = dir
	d.auxes = map[string]string{}
	for _, name := range d.spec.AuxDirs {
		p := filepath.Join(dir, sanitize(name))
		if err := os.MkdirAll(p, 0o755); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("mkdir aux %s: %w", name, err)
		}
		d.auxes[name] = p
	}
	if d.spec.InitCmd != "" {
		cmd := exec.CommandContext(ctx, "sh", "-c", d.spec.InitCmd)
		cmd.Dir = d.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("init_cmd: %w (%s)", err, string(out))
		}
	}
	d.created = true
	return nil
}

func (d *dirEphemeral) Cleanup() error {
	if !d.created {
		return nil
	}
	d.created = false
	return os.RemoveAll(d.dir)
}

// ---- stateless ----

type stateless struct {
	spec Spec
	dir  string
}

func (s *stateless) RequestID() string                    { return s.spec.RequestID }
func (s *stateless) Path() string                         { return s.dir }
func (s *stateless) AuxPath(_ string) (string, bool)      { return "", false }

func (s *stateless) Setup(_ context.Context) error {
	s.dir = os.TempDir()
	return nil
}

func (s *stateless) Cleanup() error { return nil }

// ---- helpers ----

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback: empty string is fine for caller (they prefix with timestamp).
		return ""
	}
	return hex.EncodeToString(b)
}

// sanitize keeps a string safe to embed in a directory name.
func sanitize(s string) string {
	if s == "" {
		return "anon"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			r = '_'
		}
		out = append(out, r)
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return strings.TrimSpace(string(out))
}
