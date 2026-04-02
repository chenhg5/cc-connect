package main

import (
	"flag"
	"fmt"
	"os"

	ccconnect "github.com/chenhg5/cc-connect"
	"github.com/chenhg5/cc-connect/config"
)

func runConfig(args []string) {
	if len(args) == 0 {
		printConfigUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "example":
		fmt.Print(ccconnect.ConfigExampleTOML)
	case "format", "fmt":
		runConfigFormat(args[1:])
	case "path":
		fmt.Println(resolveConfigPath(""))
	default:
		fmt.Fprintf(os.Stderr, "Unknown config subcommand: %s\n", args[0])
		printConfigUsage()
		os.Exit(1)
	}
}

func runConfigFormat(args []string) {
	fs := flag.NewFlagSet("config format", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: auto-detect)")
	fs.Parse(args)

	path := resolveConfigPath(*configPath)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\n", path)
		os.Exit(1)
	}

	if err := config.FormatConfigFile(path); err != nil {
		fmt.Fprintf(os.Stderr, "Error formatting config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Formatted %s\n", path)
}

func printConfigUsage() {
	fmt.Fprintf(os.Stderr, `Usage: cc-connect config <subcommand>

Subcommands:
  example    Print a complete annotated config.toml example
  format     Format the config file (alias: fmt)
  path       Print the resolved config file path

Flags for 'format':
  --config <path>   Path to config file (default: auto-detect)

Examples:
  cc-connect config example              Print example config
  cc-connect config example > config.toml  Save example config
  cc-connect config format               Format default config file
  cc-connect config fmt --config /path/to/config.toml
`)
}
