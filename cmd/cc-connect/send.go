package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func runSend(args []string) {
	var project, sessionKey, dataDir string

	var positional []string
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
			printSendUsage()
			return
		default:
			positional = append(positional, args[i])
		}
	}

	message := strings.Join(positional, " ")
	if message == "" {
		fmt.Fprintln(os.Stderr, "Error: message is required")
		printSendUsage()
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	payload, _ := json.Marshal(map[string]string{
		"project":     project,
		"session_key": sessionKey,
		"message":     message,
	})

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	resp, err := client.Post("http://unix/send", "application/json", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	fmt.Println("Message sent successfully.")
}

func resolveSocketPath(dataDir string) string {
	if dataDir != "" {
		return filepath.Join(dataDir, "run", "api.sock")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cc-connect", "run", "api.sock")
	}
	return filepath.Join(".cc-connect", "run", "api.sock")
}

func printSendUsage() {
	fmt.Println(`Usage: cc-connect send [options] <message>

Send a message to an active cc-connect session.

Options:
  -p, --project <name>     Target project (optional if only one project)
  -s, --session <key>      Target session key (optional, picks first active)
      --data-dir <path>    Data directory (default: ~/.cc-connect)
  -h, --help               Show this help

Examples:
  cc-connect send "Daily summary: ..."
  cc-connect send -p my-backend "Build completed successfully"
  cc-connect send -p my-backend -s "feishu:oc_xxx:ou_yyy" "Scheduled report"`)
}
