package registry

import (
	"fmt"
	"strings"
)

// Tier classifies how deeply the agent-gateway integrates with a CLI.
type Tier string

const (
	TierFirstClass Tier = "first-class" // hand-tuned output parser, deep semantics
	TierSupported  Tier = "supported"   // generic stdout-as-text streamer
)

// OutputFormat selects which parser the runner uses.
type OutputFormat string

const (
	OutputStreamJSON OutputFormat = "stream-json" // claude / codex style
	OutputJSONL      OutputFormat = "jsonl"       // aider style
	OutputSSE        OutputFormat = "sse"
	OutputPlain      OutputFormat = "plain"
)

// WorkspaceType controls per-request workspace lifecycle.
type WorkspaceType string

const (
	WorkspaceWorktreeEphemeral WorkspaceType = "worktree-ephemeral"
	WorkspaceDirEphemeral      WorkspaceType = "dir-ephemeral"
	WorkspaceStateless         WorkspaceType = "stateless"
)

// InputMode tells the runner how to feed the request body to the CLI.
type InputMode string

const (
	InputStdin    InputMode = "stdin"
	InputArgv     InputMode = "argv"
	InputTempfile InputMode = "tempfile"
)

// AuthInjection tells the runner how to install credentials before spawning.
type AuthInjection string

const (
	AuthInjectEnv       AuthInjection = "env"
	AuthInjectConfigDir AuthInjection = "config-dir"
	AuthInjectArgv      AuthInjection = "argv"
)

// GraphifyMode mirrors the graphify pre-stage modes already used by the
// router. Profiles declare a default per-role; per-request headers override.
type GraphifyMode string

const (
	GraphifyCompress GraphifyMode = "compress"
	GraphifyAugment  GraphifyMode = "augment"
	GraphifyAuto     GraphifyMode = "auto"
	GraphifyOff      GraphifyMode = "off"
)

// CostClass orders backends within a tier rule chain.
type CostClass string

const (
	CostCheap    CostClass = "cheap"
	CostStandard CostClass = "standard"
	CostPremium  CostClass = "premium"
)

// CLISpec is the executable side of a profile.
type CLISpec struct {
	Command   string         `yaml:"command" json:"command"`
	InputMode InputMode      `yaml:"input_mode,omitempty" json:"input_mode,omitempty"`
	ExitCodes ExitCodeGroups `yaml:"exit_codes,omitempty" json:"exit_codes,omitempty"`
}

// ExitCodeGroups classifies subprocess exit codes for account-pool feedback.
type ExitCodeGroups struct {
	Success     []int `yaml:"success,omitempty" json:"success,omitempty"`
	RateLimit   []int `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	AuthFailure []int `yaml:"auth_failure,omitempty" json:"auth_failure,omitempty"`
}

// OutputSpec selects the stream parser.
type OutputSpec struct {
	Format OutputFormat `yaml:"format" json:"format"`
	Parser string       `yaml:"parser,omitempty" json:"parser,omitempty"`
}

// WorkspaceSpec declares per-request workspace shape.
type WorkspaceSpec struct {
	Type    WorkspaceType `yaml:"type" json:"type"`
	InitCmd string        `yaml:"init_cmd,omitempty" json:"init_cmd,omitempty"`
}

// SubmodelSpec controls the <agent>/<submodel> dispatch surface.
type SubmodelSpec struct {
	Supports bool     `yaml:"supports" json:"supports"`
	Default  string   `yaml:"default,omitempty" json:"default,omitempty"`
	Allowed  []string `yaml:"allowed,omitempty" json:"allowed,omitempty"`
}

// AuthSpec wires the profile into the universal account pool.
type AuthSpec struct {
	Pool      string            `yaml:"pool" json:"pool"`
	Injection AuthInjection     `yaml:"injection,omitempty" json:"injection,omitempty"`
	EnvVars   map[string]string `yaml:"env_vars,omitempty" json:"env_vars,omitempty"`
}

// GraphifySpec sets the per-profile graphify default; requests can override.
type GraphifySpec struct {
	Default GraphifyMode `yaml:"default,omitempty" json:"default,omitempty"`
}

// RoutingDefault describes how rule synthesis should slot the agent into
// kronaxis-router rules.
type RoutingDefault struct {
	Tier      int       `yaml:"tier" json:"tier"`
	CostClass CostClass `yaml:"cost_class,omitempty" json:"cost_class,omitempty"`
}

// Profile is the canonical description of a registered TUI CLI agent.
type Profile struct {
	Name            string         `yaml:"name" json:"name"`
	DisplayName     string         `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Description     string         `yaml:"description,omitempty" json:"description,omitempty"`
	Tier            Tier           `yaml:"tier" json:"tier"`
	CLI             CLISpec        `yaml:"cli" json:"cli"`
	Output          OutputSpec     `yaml:"output" json:"output"`
	Workspace       WorkspaceSpec  `yaml:"workspace" json:"workspace"`
	Submodel        SubmodelSpec   `yaml:"submodel,omitempty" json:"submodel,omitempty"`
	Auth            AuthSpec       `yaml:"auth" json:"auth"`
	Graphify        GraphifySpec   `yaml:"graphify,omitempty" json:"graphify,omitempty"`
	Capabilities    []string       `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	RoutingDefault  RoutingDefault `yaml:"routing_default,omitempty" json:"routing_default,omitempty"`
	FlagsPassthrough []string      `yaml:"flags_passthrough,omitempty" json:"flags_passthrough,omitempty"`
	// Source records where the profile came from (builtin filename or
	// override path). Empty until a registry sets it.
	Source string `yaml:"-" json:"source,omitempty"`
}

// Validate enforces required fields, enum values, and internal consistency.
func (p *Profile) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("profile.name is required")
	}
	if !validIdent(p.Name) {
		return fmt.Errorf("profile.name %q must match [a-z0-9][a-z0-9_-]*", p.Name)
	}
	switch p.Tier {
	case TierFirstClass, TierSupported:
	case "":
		return fmt.Errorf("profile.tier is required (%q or %q)", TierFirstClass, TierSupported)
	default:
		return fmt.Errorf("profile.tier %q must be %q or %q", p.Tier, TierFirstClass, TierSupported)
	}
	if strings.TrimSpace(p.CLI.Command) == "" {
		return fmt.Errorf("profile.cli.command is required")
	}
	if p.CLI.InputMode == "" {
		p.CLI.InputMode = InputStdin
	}
	switch p.CLI.InputMode {
	case InputStdin, InputArgv, InputTempfile:
	default:
		return fmt.Errorf("profile.cli.input_mode %q invalid", p.CLI.InputMode)
	}
	switch p.Output.Format {
	case OutputStreamJSON, OutputJSONL, OutputSSE, OutputPlain:
	case "":
		return fmt.Errorf("profile.output.format is required")
	default:
		return fmt.Errorf("profile.output.format %q invalid", p.Output.Format)
	}
	if p.Output.Parser == "" {
		if p.Tier == TierFirstClass {
			return fmt.Errorf("first-class profile %q must declare output.parser", p.Name)
		}
		p.Output.Parser = "generic"
	}
	switch p.Workspace.Type {
	case WorkspaceWorktreeEphemeral, WorkspaceDirEphemeral, WorkspaceStateless:
	case "":
		return fmt.Errorf("profile.workspace.type is required")
	default:
		return fmt.Errorf("profile.workspace.type %q invalid", p.Workspace.Type)
	}
	if strings.TrimSpace(p.Auth.Pool) == "" {
		return fmt.Errorf("profile.auth.pool is required")
	}
	if p.Auth.Injection == "" {
		p.Auth.Injection = AuthInjectEnv
	}
	switch p.Auth.Injection {
	case AuthInjectEnv, AuthInjectConfigDir, AuthInjectArgv:
	default:
		return fmt.Errorf("profile.auth.injection %q invalid", p.Auth.Injection)
	}
	if p.Submodel.Supports && p.Submodel.Default != "" && len(p.Submodel.Allowed) > 0 {
		ok := false
		for _, a := range p.Submodel.Allowed {
			if a == p.Submodel.Default {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("profile.submodel.default %q not in allowed list", p.Submodel.Default)
		}
	}
	if !p.Submodel.Supports && len(p.Submodel.Allowed) > 0 {
		return fmt.Errorf("profile.submodel.allowed set but supports=false")
	}
	if p.Graphify.Default == "" {
		// Sane default by tier role.
		if p.Tier == TierFirstClass {
			p.Graphify.Default = GraphifyOff
		} else {
			p.Graphify.Default = GraphifyCompress
		}
	}
	switch p.Graphify.Default {
	case GraphifyCompress, GraphifyAugment, GraphifyAuto, GraphifyOff:
	default:
		return fmt.Errorf("profile.graphify.default %q invalid", p.Graphify.Default)
	}
	if p.RoutingDefault.Tier == 0 {
		p.RoutingDefault.Tier = 2
	}
	if p.RoutingDefault.Tier < 1 || p.RoutingDefault.Tier > 9 {
		return fmt.Errorf("profile.routing_default.tier %d out of [1,9]", p.RoutingDefault.Tier)
	}
	if p.RoutingDefault.CostClass == "" {
		p.RoutingDefault.CostClass = CostStandard
	}
	switch p.RoutingDefault.CostClass {
	case CostCheap, CostStandard, CostPremium:
	default:
		return fmt.Errorf("profile.routing_default.cost_class %q invalid", p.RoutingDefault.CostClass)
	}
	return nil
}

// validIdent matches lowercase [a-z0-9][a-z0-9_-]*; used for agent names so
// they're safe for OpenAI model strings, file names, and URL paths.
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	if !(first >= 'a' && first <= 'z') && !(first >= '0' && first <= '9') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}
