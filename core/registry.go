package core

import (
	"fmt"
	"sync"
)

// PlatformFactory creates a Platform from config options.
type PlatformFactory func(opts map[string]any) (Platform, error)

// AgentFactory creates an Agent from config options.
type AgentFactory func(opts map[string]any) (Agent, error)

// AgentModelConfigSaver updates persisted agent config options for a specific
// agent type when /model changes the active model.
type AgentModelConfigSaver func(options map[string]any, model string) error

var (
	platformFactories = make(map[string]PlatformFactory)
	agentFactories    = make(map[string]AgentFactory)
	agentModelSavers  = make(map[string]AgentModelConfigSaver)
	registryMu        sync.RWMutex
)

func RegisterPlatform(name string, factory PlatformFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	platformFactories[name] = factory
}

func RegisterAgent(name string, factory AgentFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	agentFactories[name] = factory
}

func RegisterAgentModelConfigSaver(name string, saver AgentModelConfigSaver) {
	registryMu.Lock()
	defer registryMu.Unlock()
	agentModelSavers[name] = saver
}

func CreatePlatform(name string, opts map[string]any) (Platform, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := platformFactories[name]
	if !ok {
		available := make([]string, 0, len(platformFactories))
		for k := range platformFactories {
			available = append(available, k)
		}
		return nil, fmt.Errorf("unknown platform %q, available: %v", name, available)
	}
	return f(opts)
}

func CreateAgent(name string, opts map[string]any) (Agent, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := agentFactories[name]
	if !ok {
		available := make([]string, 0, len(agentFactories))
		for k := range agentFactories {
			available = append(available, k)
		}
		return nil, fmt.Errorf("unknown agent %q, available: %v", name, available)
	}
	return f(opts)
}

func LookupAgentModelConfigSaver(name string) (AgentModelConfigSaver, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	saver, ok := agentModelSavers[name]
	return saver, ok
}
