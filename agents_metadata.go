package main

import (
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// agentMetadata fetches the agent_metadata sub-tree (tier + cost_class)
// from the current config.yaml. The standard config loader doesn't model
// this field, so we re-parse the file lazily with a small struct.
//
// Cached for the lifetime of the process; reset by the config reload
// handler so /api/agents stays in step.
var (
	metaMu       sync.RWMutex
	metaCache    map[string]agentMetaEntry
	metaLoadedAt int64
)

type agentMetaEntry struct {
	Tier      int
	CostClass string
}

type metaBackendEntry struct {
	Name          string `yaml:"name"`
	AgentMetadata *struct {
		Tier      int    `yaml:"tier"`
		CostClass string `yaml:"cost_class"`
	} `yaml:"agent_metadata"`
}

type metaConfigShape struct {
	Backends []metaBackendEntry `yaml:"backends"`
}

func agentMetadata(name string) (tier int, costClass string, ok bool) {
	metaMu.RLock()
	if metaCache == nil {
		metaMu.RUnlock()
		loadAgentMetadata()
		metaMu.RLock()
	}
	entry, found := metaCache[name]
	metaMu.RUnlock()
	if !found {
		return 0, "", false
	}
	return entry.Tier, entry.CostClass, true
}

func loadAgentMetadata() {
	metaMu.Lock()
	defer metaMu.Unlock()
	metaCache = map[string]agentMetaEntry{}
	path := AgentConfigPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var shape metaConfigShape
	if err := yaml.Unmarshal(raw, &shape); err != nil {
		return
	}
	for _, b := range shape.Backends {
		if b.Name == "" || b.AgentMetadata == nil {
			continue
		}
		metaCache[b.Name] = agentMetaEntry{
			Tier:      b.AgentMetadata.Tier,
			CostClass: b.AgentMetadata.CostClass,
		}
	}
}

// resetAgentMetadata invalidates the cache; called on config reload.
func resetAgentMetadata() {
	metaMu.Lock()
	metaCache = nil
	metaMu.Unlock()
}
