package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// A backend/role/roots change creates a new compatible session namespace. Only
// the digest is persisted; credentials from agent options never enter metadata.
func botContextDataDir(base string, bot config.ProjectConfig, project *core.BusinessProject) (string, error) {
	id := bot.ContextProjectID
	if id == "" {
		id = "_control"
	}
	role, err := core.AgentRoleByID(bot.Role)
	if err != nil {
		return "", err
	}
	// Display names and transport identities do not change native context.
	var roots []core.ProjectDirectory
	primary, goal, rules := "", "", ""
	if project != nil {
		roots, primary, goal, rules = project.Directories, project.PrimaryDirectory, project.Goal, project.Rules
	}
	// Fixing runtime tool access does not make the existing model history
	// incompatible. Keep the pre-option fingerprint and never mutate config.
	agent := bot.Agent
	if agent.Options != nil {
		agent.Options = make(map[string]any, len(bot.Agent.Options))
		for key, value := range bot.Agent.Options {
			if key != "sandbox_writable_roots" && key != "sandbox_network_access" {
				agent.Options[key] = value
			}
		}
	}
	data, err := json.Marshal(struct {
		Agent                config.AgentConfig
		Role                 core.AgentRoleTemplate
		Prompt               string
		RunAs                string
		RunEnv               []string
		Roots                []core.ProjectDirectory
		Primary, Goal, Rules string
	}{agent, role, bot.Prompt, bot.RunAsUser, bot.RunAsEnv, roots, primary, goal, rules})
	if err != nil {
		return "", fmt.Errorf("fingerprint bot context: %w", err)
	}
	hash := sha256.Sum256(data)
	return filepath.Join(base, "contexts", bot.ID, id, hex.EncodeToString(hash[:16])), nil
}
