package main

import (
	"fmt"
	"sort"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func buildAgentChoices(cfg *config.Config, project config.ProjectConfig) (string, []core.AgentChoice) {
	refs := make(map[string]string, len(project.AgentChoices)+1)
	for name, ref := range project.AgentChoices {
		refs[name] = ref
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	defaultName := ""
	for _, name := range names {
		if refs[name] == project.Name {
			defaultName = name
			break
		}
	}
	if defaultName == "" {
		defaultName = "default"
		refs[defaultName] = project.Name
		names = append(names, defaultName)
		sort.Strings(names)
	}
	var choices []core.AgentChoice
	for _, name := range names {
		for _, source := range cfg.Projects {
			if source.Name != refs[name] {
				continue
			}
			choices = append(choices, core.AgentChoice{
				Name: name, Type: source.Agent.Type,
				New: func(workDir string) (core.Agent, error) {
					opts := buildAgentOptions(cfg.DataDir, source)
					// Keep the receiving conversation's directory and identity.
					// The referenced project supplies only agent settings.
					opts["work_dir"] = workDir
					opts["cc_project"] = project.Name
					if source.RunAsUser != "" {
						opts["run_as_user"] = source.RunAsUser
						opts["run_as_env"] = append([]string(nil), source.RunAsEnv...)
					}
					agent, err := core.CreateAgent(source.Agent.Type, opts)
					if err != nil {
						return nil, err
					}
					wiring := wireAgentProviders(agent, source.Agent)
					if wiring.explicitProviderRequested && !wiring.activeProviderApplied {
						_ = agent.Stop()
						return nil, fmt.Errorf("configured provider is unavailable")
					}
					return agent, nil
				},
			})
			break
		}
	}
	return defaultName, choices
}
