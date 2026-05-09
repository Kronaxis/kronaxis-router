package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validProfileYAML() string {
	return `
name: testagent
tier: first-class
cli:
  command: "fake-cli --model {model}"
  input_mode: stdin
output:
  format: stream-json
  parser: claude
workspace:
  type: worktree-ephemeral
auth:
  pool: anthropic-oauth
  injection: config-dir
submodel:
  supports: true
  default: claude-3-5-sonnet
  allowed: [claude-3-5-sonnet, claude-3-5-haiku]
graphify:
  default: "off"
capabilities: [agentic, file-edit, tools]
routing_default:
  tier: 1
  cost_class: premium
`
}

func TestValidate_Required(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Profile)
		want string
	}{
		{"missing name", func(p *Profile) { p.Name = "" }, "profile.name is required"},
		{"bad ident", func(p *Profile) { p.Name = "Bad Name!" }, "must match"},
		{"bad tier", func(p *Profile) { p.Tier = "weird" }, "tier"},
		{"missing command", func(p *Profile) { p.CLI.Command = "" }, "cli.command"},
		{"bad output format", func(p *Profile) { p.Output.Format = "invalid" }, "output.format"},
		{"first-class needs parser", func(p *Profile) { p.Output.Parser = "" }, "must declare output.parser"},
		{"bad workspace", func(p *Profile) { p.Workspace.Type = "moon" }, "workspace.type"},
		{"missing pool", func(p *Profile) { p.Auth.Pool = "" }, "auth.pool"},
		{"bad injection", func(p *Profile) { p.Auth.Injection = "telepathy" }, "auth.injection"},
		{"submodel default not in allowed", func(p *Profile) { p.Submodel.Default = "ghost" }, "default"},
		{"allowed without supports", func(p *Profile) { p.Submodel.Supports = false; p.Submodel.Allowed = []string{"x"} }, "supports=false"},
		{"bad graphify", func(p *Profile) { p.Graphify.Default = "compress-everything" }, "graphify"},
		{"bad cost class", func(p *Profile) { p.RoutingDefault.CostClass = "extortionate" }, "cost_class"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := decodeProfile([]byte(validProfileYAML()))
			if err != nil {
				t.Fatalf("baseline decode: %v", err)
			}
			tc.mut(p)
			err = p.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidate_Defaults(t *testing.T) {
	yaml := `
name: minimal
tier: supported
cli:
  command: "fake"
output:
  format: plain
workspace:
  type: dir-ephemeral
auth:
  pool: openai-api-key
`
	p, err := decodeProfile([]byte(yaml))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.CLI.InputMode != InputStdin {
		t.Errorf("expected InputMode default stdin, got %q", p.CLI.InputMode)
	}
	if p.Auth.Injection != AuthInjectEnv {
		t.Errorf("expected Injection default env, got %q", p.Auth.Injection)
	}
	if p.Output.Parser != "generic" {
		t.Errorf("expected Parser default generic for supported tier, got %q", p.Output.Parser)
	}
	if p.Graphify.Default != GraphifyCompress {
		t.Errorf("expected Graphify default compress for supported tier, got %q", p.Graphify.Default)
	}
	if p.RoutingDefault.Tier != 2 {
		t.Errorf("expected default tier 2, got %d", p.RoutingDefault.Tier)
	}
}

func TestRegistry_LoadOverrides_AndWatch(t *testing.T) {
	dir := t.TempDir()
	r := New()
	if err := r.LoadOverrides(dir); err != nil {
		t.Fatal(err)
	}
	if got := len(r.List()); got != 0 {
		t.Fatalf("expected empty registry, got %d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Drop a profile YAML; expect EventAdded.
	path := filepath.Join(dir, "testagent.yaml")
	if err := os.WriteFile(path, []byte(validProfileYAML()), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Err != nil {
			t.Fatalf("event err: %v", ev.Err)
		}
		if ev.Type != EventAdded {
			t.Errorf("expected EventAdded, got %q", ev.Type)
		}
		if ev.Name != "testagent" {
			t.Errorf("expected name testagent, got %q", ev.Name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventAdded")
	}

	// Modify; expect EventUpdated.
	body := strings.Replace(validProfileYAML(), `display_name:`, `display_name: "Test Agent"`, 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Type != EventUpdated {
			t.Errorf("expected EventUpdated, got %q (err=%v)", ev.Type, ev.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventUpdated")
	}

	// Delete; expect EventDeleted.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Type != EventDeleted {
			t.Errorf("expected EventDeleted, got %q", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventDeleted")
	}
}

func TestRegistry_AddRemoveGet(t *testing.T) {
	r := New()
	p, err := decodeProfile([]byte(validProfileYAML()))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("testagent")
	if !ok || got.Name != "testagent" {
		t.Fatalf("expected to find testagent, got %v %v", got, ok)
	}
	if !r.Remove("testagent") {
		t.Fatal("Remove returned false for present profile")
	}
	if r.Remove("testagent") {
		t.Fatal("Remove returned true on second call")
	}
}
