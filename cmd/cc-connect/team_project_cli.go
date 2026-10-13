package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func runProject(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(core.NewI18n(core.LangChinese).T(core.MsgProjectCLIUsage))
		return
	}
	if err := teamProjectCLI(append([]string{"project"}, args...)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func teamProjectCLI(args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	path := fs.String("config", filepath.Join(home, ".cc-connect-team", "config.toml"), "configuration path")
	bot := fs.String("manager", os.Getenv("CC_PROJECT"), "manager runtime name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *bot == "" && len(cfg.Teams) == 1 {
		*bot = cfg.Teams[0].Manager
	}
	team, _ := cfg.TeamForBot(*bot)
	if team == nil || team.Manager != *bot {
		return fmt.Errorf("select a configured team manager")
	}
	var runtime config.ProjectConfig
	found := false
	for _, p := range cfg.Projects {
		if p.Name == *bot {
			runtime = p
			found = true
		}
	}
	if !found || runtime.BotOwnerID == "" {
		return fmt.Errorf("manager requires a registered owner")
	}
	for _, pc := range runtime.Platforms {
		opts := map[string]any{}
		for k, v := range pc.Options {
			opts[k] = v
		}
		opts["cc_data_dir"] = cfg.DataDir
		p, err := core.CreatePlatform(pc.Type, opts)
		if err != nil {
			return err
		}
		if _, ok := p.(core.ProjectGroupCreator); !ok {
			continue
		}
		service := newTeamProjectService(*path, cfg.DataDir)
		command := fs.Args()
		handler := service.projectHandler(*bot)
		switch args[0] {
		case "projects":
			command = append([]string{"list"}, command...)
		case "members":
			handler = service.handler(*bot)
			command = append([]string{"members"}, command...)
		}
		result, err := handler(context.Background(), p, &core.Message{UserID: runtime.BotOwnerID}, command)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	}
	return fmt.Errorf("manager has no platform supporting group creation")
}
