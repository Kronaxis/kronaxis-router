package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Workspace struct {
	// Path is the worktree the agent runs in. The agent's `cwd`.
	Path string
	// ClaudeDir is a per-request CLAUDE_CONFIG_DIR. Sibling of Path so it
	// never appears in `git diff` output.
	ClaudeDir string
	// rootDir is the parent that holds both Path and ClaudeDir; cleanup
	// removes it as a unit.
	rootDir    string
	hostClaude string
	retain     bool
	requestID  string
}

func newWorkspace(root, baseRepo, requestID string, retain bool) (*Workspace, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir root: %w", err)
	}
	parent, err := os.MkdirTemp(root, "req-"+requestID+"-")
	if err != nil {
		return nil, fmt.Errorf("mkdtemp: %w", err)
	}
	dir := filepath.Join(parent, "workspace")
	claudeDir := filepath.Join(parent, "claude-config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		os.RemoveAll(parent)
		return nil, fmt.Errorf("mkdir workspace: %w", err)
	}
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		os.RemoveAll(parent)
		return nil, fmt.Errorf("mkdir claude: %w", err)
	}

	if baseRepo == "" {
		cmd := exec.Command("git", "init", "--quiet")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(parent)
			return nil, fmt.Errorf("git init: %w (%s)", err, string(out))
		}
		_ = exec.Command("git", "-C", dir, "config", "user.email", "agent@kronaxis.local").Run()
		_ = exec.Command("git", "-C", dir, "config", "user.name", "agent-gateway").Run()
		_ = os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte{}, 0o644)
		_ = exec.Command("git", "-C", dir, "add", ".gitkeep").Run()
		_ = exec.Command("git", "-C", dir, "commit", "-q", "-m", "init").Run()
	} else {
		// Clone into the workspace dir. We have to remove the empty dir first because
		// `git clone target` requires target to be empty/non-existent.
		_ = os.Remove(dir)
		cmd := exec.Command("git", "clone", "--depth", "1", baseRepo, dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(parent)
			return nil, fmt.Errorf("git clone %s: %w (%s)", baseRepo, err, string(out))
		}
	}

	host := os.Getenv("HOME")
	if host == "" {
		host = "/root"
	}
	hostClaude := filepath.Join(host, ".claude")

	return &Workspace{
		Path:       dir,
		ClaudeDir:  claudeDir,
		rootDir:    parent,
		hostClaude: hostClaude,
		retain:     retain,
		requestID:  requestID,
	}, nil
}

// SeedAuth links the host's claude credentials into the per-request config
// dir so the spawned `claude` process inherits the user's `claude auth login`
// token without sharing the live session/transcript files (which would race).
//
// Symlinks (not copies) so re-auth on the host propagates without restart.
func (w *Workspace) SeedAuth() error {
	return w.SeedAuthFrom(w.hostClaude, "")
}

// SeedAuthFrom seeds the per-request claude-config dir from a chosen source.
//
// If credentialsPath is non-empty, that exact .credentials.json is symlinked
// in (overrides any same-named file from configSrcDir). This is how the
// auth-pool plumbs per-account OAuth subscriptions into a request.
//
// configSrcDir defaults to the host's ~/.claude when empty. Files beyond
// .credentials.json (settings.json, etc.) are taken from there.
func (w *Workspace) SeedAuthFrom(configSrcDir, credentialsPath string) error {
	if configSrcDir == "" {
		configSrcDir = w.hostClaude
	}
	candidates := []string{".credentials.json", "settings.json"}
	for _, name := range candidates {
		src := filepath.Join(configSrcDir, name)
		// .credentials.json override
		if name == ".credentials.json" && credentialsPath != "" {
			src = credentialsPath
		}
		dst := filepath.Join(w.ClaudeDir, name)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat %s: %w", src, err)
		}
		// Replace any pre-existing entry (warm-pool workspaces may have one).
		_ = os.Remove(dst)
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dst, src, err)
		}
	}
	return nil
}

func (w *Workspace) GitDiff() (string, error) {
	cmd := exec.Command("git", "-C", w.Path, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add: %w (%s)", err, string(out))
	}
	cmd = exec.Command("git", "-C", w.Path, "diff", "--cached")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out), nil
}

func (w *Workspace) Cleanup() error {
	if w.retain {
		return nil
	}
	return os.RemoveAll(w.rootDir)
}
