package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/config"
)

// adoptTeamFile converts the experimental bots schema into ordinary runtimes
// plus optional team metadata. It never replaces the source file or sessions.
func adoptTeamFile(input, output string) error {
	b, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var c config.Config
	if err = toml.Unmarshal(b, &c); err != nil {
		return err
	}
	if len(c.BusinessProjects) > 0 {
		return fmt.Errorf("existing business_projects require explicit group mapping before adoption")
	}
	team := config.TeamConfig{ID: "default"}
	// Resolve only in a detached copy when locating old session storage; raw
	// credentials and environment placeholders in the exported config stay intact.
	resolved, err := config.Load(input)
	if err != nil {
		return err
	}
	for _, b := range c.Bots {
		p := config.ProjectConfig(b)
		p.Name = b.ID
		var effective config.ProjectConfig
		for _, r := range resolved.RuntimeProjects() {
			if r.Name == b.ID {
				effective, err = resolved.EffectiveProject(r)
				if err != nil {
					return err
				}
			}
		}
		oldDir, err := botContextDataDir(resolved.DataDir, effective, nil)
		if err != nil {
			return err
		}
		p.SessionDataDir = oldDir
		team.Members = append(team.Members, config.TeamMember{Bot: p.Name, Role: p.Role, Prompt: p.Prompt})
		if p.Role == "manager" && team.Manager == "" {
			team.Manager = p.Name
		}
		p.ID, p.Role, p.Prompt, p.ContextProjectID = "", "", "", ""
		c.Projects = append(c.Projects, p)
	}
	if len(c.Bots) > 0 {
		if team.Manager == "" {
			return fmt.Errorf("a manager is required")
		}
		c.Teams = append(c.Teams, team)
	}
	c.Bots = nil
	var out bytes.Buffer
	if err = toml.NewEncoder(&out).Encode(c); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return err
	}
	return writeNewPrivateFile(output, out.Bytes())
}
