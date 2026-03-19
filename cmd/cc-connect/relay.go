package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func runRelay(args []string) {
	if len(args) == 0 {
		printRelayUsage()
		return
	}
	switch args[0] {
	case "send":
		runRelaySend(args[1:])
	case "--help", "-h", "help":
		printRelayUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown relay subcommand: %s\n", args[0])
		printRelayUsage()
		os.Exit(1)
	}
}

func runRelaySend(args []string) {
	var from, to, sessionKey, message, dataDir string

	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--from", "-f":
			if i+1 < len(args) {
				i++
				from = args[i]
			}
		case "--to", "-t":
			if i+1 < len(args) {
				i++
				to = args[i]
			}
		case "--session-key", "--session", "-s":
			if i+1 < len(args) {
				i++
				sessionKey = args[i]
			}
		case "--message", "-m":
			if i+1 < len(args) {
				i++
				message = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h":
			printRelaySendUsage()
			return
		default:
			positional = append(positional, args[i])
		}
	}

	if from == "" {
		from = os.Getenv("CC_PROJECT")
	}
	if sessionKey == "" {
		sessionKey = os.Getenv("CC_SESSION_KEY")
	}
	if message == "" && len(positional) > 0 {
		if to == "" && len(positional) >= 2 {
			to = positional[0]
			message = strings.Join(positional[1:], " ")
		} else {
			message = strings.Join(positional, " ")
		}
	}

	if to == "" || message == "" {
		fmt.Fprintln(os.Stderr, "Error: target project (--to) and message are required")
		printRelaySendUsage()
		os.Exit(1)
	}
	if sessionKey == "" {
		fmt.Fprintln(os.Stderr, "Error: session key is required (set CC_SESSION_KEY or use --session-key)")
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	payload, _ := json.Marshal(map[string]string{
		"from":        from,
		"to":          to,
		"session_key": sessionKey,
		"message":     message,
	})

	resp, err := apiPost(sockPath, "/relay/send", payload)
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

	var result struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error: decode response: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(result.Response)
}

func printRelayUsage() {
	fmt.Println(`Usage: cc-connect relay <command> [options]

Commands:
  send      Send a message to another bot via relay

Run 'cc-connect relay <command> --help' for details.`)
}

func printRelaySendUsage() {
	fmt.Println(`Usage: cc-connect relay send [options] [<target_project> <message>]

Send a message to another bot and wait for the response.

Options:
  -f, --from <project>       Source project (auto-detected from CC_PROJECT env)
  -t, --to <project>         Target bot project name
  -s, --session-key <key>    Session key (auto-detected from CC_SESSION_KEY env)
  -m, --message <text>       Message to send
      --data-dir <path>      Data directory (default: ~/.cc-connect)
  -h, --help                 Show this help

Examples:
  cc-connect relay send --to claude-bot "What's the weather today?"
  cc-connect relay send claude-bot What is the weather today`)
}
