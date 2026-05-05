package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func runWeb(args []string) {
	configPath := resolveConfigPath("")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\nRun cc-connect first to create a default config.\n", configPath)
		os.Exit(1)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	config.ConfigPath = configPath

	mgmtEnabled := cfg.Management.Enabled != nil && *cfg.Management.Enabled
	port := cfg.Management.Port
	if port == 0 {
		port = 9820
	}
	token := cfg.Management.Token

	if !mgmtEnabled {
		fmt.Println("Web admin is not enabled. Configuring...")

		mgmtToken := core.GenerateToken(16)
		bridgeToken := core.GenerateToken(16)
		result, err := config.EnableWebAdmin(mgmtToken, bridgeToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling web admin: %v\n", err)
			os.Exit(1)
		}
		port = result.ManagementPort
		token = result.ManagementToken
		fmt.Printf("Web admin configured on port %d.\n", port)
		fmt.Println("Restart cc-connect for the changes to take effect.")
	}

	baseURL := fmt.Sprintf("http://localhost:%d", port)

	noBrowser := false
	for _, a := range args {
		if a == "--no-browser" || a == "-n" {
			noBrowser = true
		}
	}

	if noBrowser {
		fmt.Printf("URL:   %s\n", baseURL)
		fmt.Printf("Token: %s\n", token)
		return
	}

	loginURL := fmt.Sprintf("%s/login?token=%s",
		baseURL, url.QueryEscape(token))

	fmt.Printf("Opening: %s\n", baseURL)
	if err := openBrowser(loginURL); err != nil {
		fmt.Printf("\nCould not open browser automatically.\n")
		fmt.Printf("URL:   %s\n", baseURL)
		fmt.Printf("Token: %s\n", token)
	}
}

func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "linux":
		if isWSL() {
			return exec.Command("cmd.exe", "/c", "start", rawURL).Start()
		}
		return exec.Command("xdg-open", rawURL).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", rawURL).Start()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}
