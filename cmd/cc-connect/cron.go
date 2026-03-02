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
	"strings"
)

func runCron(args []string) {
	if len(args) == 0 {
		printCronUsage()
		return
	}

	switch args[0] {
	case "add":
		runCronAdd(args[1:])
	case "list":
		runCronList(args[1:])
	case "del", "delete", "rm", "remove":
		runCronDel(args[1:])
	case "--help", "-h", "help":
		printCronUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown cron subcommand: %s\n", args[0])
		printCronUsage()
		os.Exit(1)
	}
}

func runCronAdd(args []string) {
	var project, sessionKey, cronExpr, prompt, desc, dataDir string

	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--session-key", "--session", "-s":
			if i+1 < len(args) {
				i++
				sessionKey = args[i]
			}
		case "--cron", "-c":
			if i+1 < len(args) {
				i++
				cronExpr = args[i]
			}
		case "--prompt":
			if i+1 < len(args) {
				i++
				prompt = args[i]
			}
		case "--desc", "--description":
			if i+1 < len(args) {
				i++
				desc = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h":
			printCronAddUsage()
			return
		default:
			positional = append(positional, args[i])
		}
	}

	// Fallback to env vars (set by cc-connect when spawning agent)
	if project == "" {
		project = os.Getenv("CC_PROJECT")
	}
	if sessionKey == "" {
		sessionKey = os.Getenv("CC_SESSION_KEY")
	}

	// If cron expr not provided via --cron, try positional: first 5 fields are cron, rest is prompt
	if cronExpr == "" && len(positional) >= 6 {
		cronExpr = strings.Join(positional[:5], " ")
		if prompt == "" {
			prompt = strings.Join(positional[5:], " ")
		}
	} else if prompt == "" && len(positional) > 0 {
		prompt = strings.Join(positional, " ")
	}

	if cronExpr == "" || prompt == "" {
		fmt.Fprintln(os.Stderr, "Error: cron expression and prompt are required")
		printCronAddUsage()
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
		"cron_expr":   cronExpr,
		"prompt":      prompt,
		"description": desc,
	})

	resp, err := apiPost(sockPath, "/cron/add", payload)
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

	var result map[string]any
	json.Unmarshal(body, &result)
	fmt.Printf("Cron job created: %s\n", result["id"])
	fmt.Printf("Schedule: %s\n", result["cron_expr"])
	fmt.Printf("Prompt: %s\n", result["prompt"])
}

func runCronList(args []string) {
	var project, dataDir string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		}
	}

	if project == "" {
		project = os.Getenv("CC_PROJECT")
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	url := "/cron/list"
	if project != "" {
		url += "?project=" + project
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	resp, err := client.Get("http://unix" + url)
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

	var jobs []map[string]any
	json.Unmarshal(body, &jobs)

	if len(jobs) == 0 {
		fmt.Println("No scheduled tasks.")
		return
	}

	fmt.Printf("Scheduled tasks (%d):\n\n", len(jobs))
	for _, j := range jobs {
		enabled := "✅"
		if e, ok := j["enabled"].(bool); ok && !e {
			enabled = "⏸"
		}
		id, _ := j["id"].(string)
		expr, _ := j["cron_expr"].(string)
		prompt, _ := j["prompt"].(string)
		desc, _ := j["description"].(string)
		display := desc
		if display == "" {
			display = prompt
			if len(display) > 60 {
				display = display[:60] + "..."
			}
		}
		fmt.Printf("  %s %s  %s  %s\n", enabled, id, expr, display)
	}
}

func runCronDel(args []string) {
	var dataDir string
	var id string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		default:
			id = args[i]
		}
	}

	if id == "" {
		fmt.Fprintln(os.Stderr, "Error: job ID is required")
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	payload, _ := json.Marshal(map[string]string{"id": id})
	resp, err := apiPost(sockPath, "/cron/del", payload)
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

	fmt.Printf("Cron job %s deleted.\n", id)
}

func apiPost(sockPath, path string, payload []byte) (*http.Response, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
	return client.Post("http://unix"+path, "application/json", bytes.NewReader(payload))
}

func printCronUsage() {
	fmt.Println(`Usage: cc-connect cron <command> [options]

Commands:
  add       Create a new scheduled task
  list      List all scheduled tasks
  del <id>  Delete a scheduled task

Run 'cc-connect cron <command> --help' for details.`)
}

func printCronAddUsage() {
	fmt.Println(`Usage: cc-connect cron add [options] [<min> <hour> <day> <month> <weekday> <prompt>]

Create a new scheduled task.

Options:
  -p, --project <name>       Target project (auto-detected from CC_PROJECT env)
  -s, --session-key <key>    Target session (auto-detected from CC_SESSION_KEY env)
  -c, --cron <expr>          Cron expression, e.g. "0 6 * * *"
      --prompt <text>        Task prompt
      --desc <text>          Short description
      --data-dir <path>      Data directory (default: ~/.cc-connect)
  -h, --help                 Show this help

Examples:
  cc-connect cron add --cron "0 6 * * *" --prompt "Collect GitHub trending data" --desc "Daily Trending"
  cc-connect cron add 0 6 * * * Collect GitHub trending data and send me a summary`)
}
