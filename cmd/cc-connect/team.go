package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func runTeam(args []string) {
	if err := teamCommand(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func teamCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(core.NewI18n(core.LangChinese).T(core.MsgTeamUsage))
		return nil
	}
	if args[0] == "plan" || args[0] == "cancel" || args[0] == "pause" || args[0] == "accept" || args[0] == "dispatch" || args[0] == "ensure-role" || args[0] == "status" {
		return teamActionCLI(args)
	}
	if args[0] == "project" || args[0] == "projects" || args[0] == "members" {
		return teamProjectCLI(args)
	}
	if args[0] == "init" {
		return teamInitCommand(args[1:])
	}
	if args[0] != "migrate" && args[0] != "adopt" {
		return fmt.Errorf("unknown team command %q; use team --help", args[0])
	}
	fs := flag.NewFlagSet("team migrate", flag.ContinueOnError)
	input := fs.String("input", "", "source config")
	output := fs.String("output", "", "new destination config (must not exist)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *input == "" || *output == "" || fs.NArg() != 0 {
		return fmt.Errorf("both --input and --output are required")
	}
	if args[0] == "adopt" {
		return adoptTeamFile(*input, *output)
	}
	return migrateTeamFile(*input, *output)
}

func migrateTeamFile(input, output string) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var cfg config.Config
	// Preserve environment placeholders: never resolve credentials for an export.
	if err = toml.Unmarshal(data, &cfg); err != nil {
		return err
	}
	migrated, err := cfg.MigrateAgentProfiles()
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err = toml.NewEncoder(&out).Encode(migrated); err != nil {
		return err
	}
	return writeNewPrivateFile(output, out.Bytes())
}

// writeNewPrivateFile publishes a complete 0600 file without replacing an
// existing file or symlink, including one created after preflight checks.
func writeNewPrivateFile(output string, data []byte) error {
	dir := filepath.Dir(output)
	f, err := os.CreateTemp(dir, ".team-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Linking is atomic and refuses to overwrite an existing destination.
	if err = os.Link(f.Name(), output); err != nil {
		return fmt.Errorf("create private file: %w", err)
	}
	return nil
}
