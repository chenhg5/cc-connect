package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
)

func runChatlog(args []string) {
	var project, sessionKey, dataDir, nStr string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--session", "-s":
			if i+1 < len(args) {
				i++
				sessionKey = args[i]
			}
		case "-n":
			if i+1 < len(args) {
				i++
				nStr = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h":
			printChatlogUsage()
			return
		}
	}

	// Default from env (set by cc-connect for agent subprocesses)
	if project == "" {
		project = os.Getenv("CC_PROJECT")
	}
	if sessionKey == "" {
		sessionKey = os.Getenv("CC_SESSION_KEY")
	}

	if sessionKey == "" {
		fmt.Fprintln(os.Stderr, "Error: --session or CC_SESSION_KEY is required")
		printChatlogUsage()
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	// Build query URL
	params := url.Values{}
	params.Set("session_key", sessionKey)
	if project != "" {
		params.Set("project", project)
	}
	if nStr != "" {
		params.Set("n", nStr)
	}
	queryURL := "http://unix/chatlog?" + params.Encode()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	resp, err := client.Get(queryURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", string(body))
		os.Exit(1)
	}

	// Parse and format as readable text
	var entries []struct {
		UserName  string `json:"user_name"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		// Fallback: print raw JSON
		fmt.Println(string(body))
		return
	}

	if len(entries) == 0 {
		fmt.Println("No chat messages recorded yet.")
		return
	}

	for _, e := range entries {
		name := e.UserName
		if name == "" {
			name = "unknown"
		}
		fmt.Printf("[%s] %s: %s\n", e.Timestamp, name, e.Content)
	}
}

func printChatlogUsage() {
	fmt.Println(`Usage: cc-connect chatlog [options]

Retrieve recent group chat messages from the running cc-connect process.

Options:
  -n <count>               Number of recent messages (default: all)
  -p, --project <name>     Target project (optional if only one project)
  -s, --session <key>      Session key (default: CC_SESSION_KEY env var)
      --data-dir <path>    Data directory (default: ~/.cc-connect)
  -h, --help               Show this help

Examples:
  cc-connect chatlog
  cc-connect chatlog -n 100
  cc-connect chatlog -s "feishu:oc_xxx:ou_xxx"`)
}

func runChatlogClear(args []string) {
	var project, sessionKey, dataDir string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--session", "-s":
			if i+1 < len(args) {
				i++
				sessionKey = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h":
			fmt.Println(`Usage: cc-connect chatlog-clear [options]

Clear all recorded group chat messages for the current chat.

Options:
  -p, --project <name>     Target project (optional if only one project)
  -s, --session <key>      Session key (default: CC_SESSION_KEY env var)
      --data-dir <path>    Data directory (default: ~/.cc-connect)
  -h, --help               Show this help`)
			return
		}
	}

	if project == "" {
		project = os.Getenv("CC_PROJECT")
	}
	if sessionKey == "" {
		sessionKey = os.Getenv("CC_SESSION_KEY")
	}
	if sessionKey == "" {
		fmt.Fprintln(os.Stderr, "Error: --session or CC_SESSION_KEY is required")
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	reqBody, _ := json.Marshal(map[string]string{
		"project":     project,
		"session_key": sessionKey,
	})

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	resp, err := client.Post("http://unix/clear_chatlog", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", string(body))
		os.Exit(1)
	}

	fmt.Println("Chat log cleared.")
}
