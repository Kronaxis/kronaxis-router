package main

import (
	"os"
	"strings"
)

type Registry struct {
	byModel map[string]AgentAdapter
}

func newRegistry(cfg *Config) *Registry {
	claude := &ClaudeCLIAdapter{Binary: cfg.ClaudeBinary}
	gemini := &GeminiCLIAdapter{Binary: cfg.GeminiBinary}
	sdk := newAnthropicSDKAdapter(cfg)
	return &Registry{
		byModel: map[string]AgentAdapter{
			"claude-code-agent": claude,
			"claude-code":       claude,
			"gemini-cli-agent":  gemini,
			"claude-sdk-agent":  sdk,
		},
	}
}

// Resolve maps a model id to an adapter. Model names support a "+skill"
// suffix (e.g. "claude-code-agent+brainstorming"); the suffix is stripped
// for lookup. Caller pulls the skill out via splitModelSkill.
func (r *Registry) Resolve(model string) AgentAdapter {
	base, _ := splitModelSkill(model)
	if a, ok := r.byModel[base]; ok {
		return a
	}
	return nil
}

// splitModelSkill splits "claude-code-agent+brainstorming" into
// ("claude-code-agent", "brainstorming"). If there's no '+', skill is "".
func splitModelSkill(model string) (string, string) {
	idx := strings.IndexByte(model, '+')
	if idx < 0 {
		return model, ""
	}
	return model[:idx], model[idx+1:]
}

func (r *Registry) Models() []ModelInfo {
	out := make([]ModelInfo, 0, len(r.byModel))
	for id, a := range r.byModel {
		out = append(out, ModelInfo{
			ID:        id,
			Object:    "model",
			OwnedBy:   "kronaxis",
			Available: a.Available(),
			Adapter:   a.Name(),
		})
	}
	return out
}

func osEnviron() []string {
	return os.Environ()
}
