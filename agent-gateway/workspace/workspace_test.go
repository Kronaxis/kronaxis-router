package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorktreeEphemeral_LifecycleAndGitInit(t *testing.T) {
	root := t.TempDir()
	w, err := New(Spec{
		Type:         TypeWorktreeEphemeral,
		Root:         root,
		RequestID:    "abc-123",
		AuxDirs:      []string{"claude-config"},
		GitInit:      true,
		GitUserEmail: "agent@kronaxis.local",
		GitUserName:  "Agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Path() == "" {
		t.Fatal("Path empty after Setup")
	}
	if _, err := os.Stat(w.Path()); err != nil {
		t.Errorf("workspace dir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Path(), ".git")); err != nil {
		t.Errorf("git init did not produce .git: %v", err)
	}
	aux, ok := w.AuxPath("claude-config")
	if !ok || aux == "" {
		t.Errorf("aux dir not registered")
	}
	if _, err := os.Stat(aux); err != nil {
		t.Errorf("aux dir missing: %v", err)
	}
	parentRoot := filepath.Dir(w.Path())
	if err := w.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(parentRoot); !os.IsNotExist(err) {
		t.Errorf("parent root not cleaned: %v", err)
	}
	// Cleanup is idempotent.
	if err := w.Cleanup(); err != nil {
		t.Errorf("second cleanup err: %v", err)
	}
}

func TestDirEphemeral_NoGit(t *testing.T) {
	root := t.TempDir()
	w, err := New(Spec{Type: TypeDirEphemeral, Root: root, RequestID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Path(), ".git")); !os.IsNotExist(err) {
		t.Errorf("dir-ephemeral should not have .git: %v", err)
	}
	if err := w.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestStateless_NoSetupCleanup(t *testing.T) {
	w, err := New(Spec{Type: TypeStateless})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Path() != os.TempDir() {
		t.Errorf("expected TempDir, got %q", w.Path())
	}
	// Cleanup must NOT remove TempDir.
	if err := w.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(os.TempDir()); err != nil {
		t.Errorf("stateless cleanup nuked TempDir: %v", err)
	}
}

func TestSweeper_RemovesOldDirs(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	fresh := filepath.Join(root, "fresh")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	pastTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, pastTime, pastTime); err != nil {
		t.Fatal(err)
	}
	removed, err := Sweep(context.Background(), root, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed=%d, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old dir still present: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh dir nuked: %v", err)
	}
}

func TestSweeper_NoOpOnMissingRoot(t *testing.T) {
	removed, err := Sweep(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed=%d, want 0", removed)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"abc-123":      "abc-123",
		"hello world":  "hello_world",
		"../../../etc": ".._.._.._etc",
		"":             "anon",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
