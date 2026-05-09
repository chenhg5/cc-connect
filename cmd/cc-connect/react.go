package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func runReact(args []string) {
	runReactionCmd(args, false)
}

func runUnreact(args []string) {
	runReactionCmd(args, true)
}

func runReactionCmd(args []string, remove bool) {
	req, dataDir, err := parseReactArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printReactUsage(remove)
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	endpoint := "/react"
	if remove {
		endpoint = "/unreact"
	}

	payload, _ := json.Marshal(req)
	resp, err := apiPost(sockPath, endpoint, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	verb := "added"
	if remove {
		verb = "removed"
	}
	fmt.Printf("Reaction :%s: %s.\n", req.Emoji, verb)
}

func parseReactArgs(args []string) (core.ReactRequest, string, error) {
	var req core.ReactRequest
	var dataDir string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--emoji", "-e":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--emoji requires a value")
			}
			i++
			req.Emoji = strings.Trim(args[i], ":")
		case "--channel", "-c":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--channel requires a value")
			}
			i++
			req.Channel = args[i]
		case "--ts", "-t":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--ts requires a value")
			}
			i++
			req.Ts = args[i]
		case "--project", "-p":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--project requires a value")
			}
			i++
			req.Project = args[i]
		case "--data-dir":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--data-dir requires a value")
			}
			i++
			dataDir = args[i]
		case "--help", "-h":
			return req, "", fmt.Errorf("show usage")
		default:
			return req, "", fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	if req.Project == "" {
		req.Project = strings.TrimSpace(os.Getenv("CC_PROJECT"))
	}

	if req.Channel == "" {
		return req, "", fmt.Errorf("--channel is required")
	}
	if req.Ts == "" {
		return req, "", fmt.Errorf("--ts is required")
	}
	if req.Emoji == "" {
		return req, "", fmt.Errorf("--emoji is required")
	}

	return req, dataDir, nil
}

func printReactUsage(remove bool) {
	cmd := "react"
	verb := "Add"
	if remove {
		cmd = "unreact"
		verb = "Remove"
	}
	fmt.Printf(`Usage: cc-connect %s --emoji <name> --channel <channel_id> --ts <message_ts>

%s an emoji reaction to a Slack message.

Options:
  -e, --emoji <name>       Emoji short name without colons (e.g. white_check_mark)
  -c, --channel <id>       Slack channel ID (e.g. C0AL12WCNBG)
  -t, --ts <timestamp>     Message timestamp (e.g. 1775870955.961349)
  -p, --project <name>     Target project (optional if only one project)
      --data-dir <path>    Data directory (default: ~/.cc-connect)
  -h, --help               Show this help

Examples:
  cc-connect %s --emoji white_check_mark --channel C0AL12WCNBG --ts 1775870955.961349
  cc-connect %s --emoji eyes --channel C0AL12WCNBG --ts 1775870955.961349
`, cmd, verb, cmd, cmd)
}
