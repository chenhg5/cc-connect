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
	"strconv"
	"strings"
)

func runTimer(args []string) {
	if len(args) == 0 {
		printTimerUsage()
		return
	}

	switch args[0] {
	case "add":
		runTimerAdd(args[1:])
	case "list":
		runTimerList(args[1:])
	case "info":
		runTimerInfo(args[1:])
	case "del", "delete", "rm", "remove":
		runTimerDel(args[1:])
	case "--help", "-h", "help":
		printTimerUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown timer subcommand: %s\n", args[0])
		printTimerUsage()
		os.Exit(1)
	}
}

func runTimerAdd(args []string) {
	var project, sessionKey, delay, atTime, prompt, execCmd, desc, dataDir, sessionMode string
	var timeoutMins *int
	var mute bool

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
		case "--delay", "-d":
			if i+1 < len(args) {
				i++
				delay = args[i]
			}
		case "--at", "-a":
			if i+1 < len(args) {
				i++
				atTime = args[i]
			}
		case "--prompt":
			if i+1 < len(args) {
				i++
				prompt = args[i]
			}
		case "--exec":
			if i+1 < len(args) {
				i++
				execCmd = args[i]
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
		case "--session-mode":
			if i+1 < len(args) {
				i++
				sessionMode = args[i]
			}
		case "--timeout-mins":
			if i+1 < len(args) {
				i++
				v, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --timeout-mins: %v\n", err)
					os.Exit(1)
				}
				timeoutMins = &v
			}
		case "--mute":
			mute = true
		case "--help", "-h":
			printTimerAddUsage()
			return
		default:
			positional = append(positional, args[i])
		}
	}

	// Fallback to env vars
	if project == "" {
		project = os.Getenv("CC_PROJECT")
	}
	if sessionKey == "" {
		sessionKey = os.Getenv("CC_SESSION_KEY")
	}

	// Positional: <delay_or_time> <prompt...>
	if delay == "" && atTime == "" && len(positional) >= 2 {
		delay = positional[0]
		if prompt == "" && execCmd == "" {
			prompt = strings.Join(positional[1:], " ")
		}
	} else if prompt == "" && execCmd == "" && len(positional) > 0 {
		prompt = strings.Join(positional, " ")
	}

	fireTime := delay
	if atTime != "" {
		fireTime = atTime
	}

	if fireTime == "" || (prompt == "" && execCmd == "") {
		fmt.Fprintln(os.Stderr, "Error: delay/at time and either --prompt or --exec are required")
		printTimerAddUsage()
		os.Exit(1)
	}
	if prompt != "" && execCmd != "" {
		fmt.Fprintln(os.Stderr, "Error: --prompt and --exec are mutually exclusive")
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	body := map[string]any{
		"project":     project,
		"session_key": sessionKey,
		"delay":       fireTime,
		"prompt":      prompt,
		"exec":        execCmd,
		"description": desc,
		"mute":        mute,
	}
	if sessionMode != "" {
		body["session_mode"] = sessionMode
	}
	if timeoutMins != nil {
		body["timeout_mins"] = *timeoutMins
	}
	payload, _ := json.Marshal(body)

	resp, err := apiPost(sockPath, "/timer/add", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(respBody)))
		os.Exit(1)
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid response: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Timer created: %s\n", result["id"])
	fmt.Printf("Fires at: %s\n", result["scheduled_at"])
	if execCmd != "" {
		fmt.Printf("Command: %s\n", result["exec"])
	} else {
		fmt.Printf("Prompt: %s\n", result["prompt"])
	}
}

func runTimerList(args []string) {
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

	url := "/timer/list"
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
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	var jobs []map[string]any
	if err := json.Unmarshal(body, &jobs); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid response: %v\n", err)
		os.Exit(1)
	}

	if len(jobs) == 0 {
		fmt.Println("No pending timers.")
		return
	}

	fmt.Printf("Pending timers (%d):\n\n", len(jobs))
	for _, j := range jobs {
		id, _ := j["id"].(string)
		scheduledAt, _ := j["scheduled_at"].(string)
		prompt, _ := j["prompt"].(string)
		execCmd, _ := j["exec"].(string)
		desc, _ := j["description"].(string)
		display := desc
		if display == "" {
			if execCmd != "" {
				display = execCmd
			} else {
				display = prompt
			}
			if len(display) > 60 {
				display = display[:60] + "..."
			}
		}
		muteStr := ""
		if m, ok := j["mute"].(bool); ok && m {
			muteStr = " [mute]"
		}
		fmt.Printf("  ⏰ %s  %s  %s%s\n", id, scheduledAt, display, muteStr)
	}
}

func runTimerDel(args []string) {
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
		fmt.Fprintln(os.Stderr, "Error: timer ID is required")
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	payload, _ := json.Marshal(map[string]string{"id": id})
	resp, err := apiPost(sockPath, "/timer/del", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	fmt.Printf("Timer %s cancelled.\n", id)
}

func runTimerInfo(args []string) {
	var dataDir, id string

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
		fmt.Fprintln(os.Stderr, "Error: timer ID is required")
		fmt.Fprintln(os.Stderr, "Usage: cc-connect timer info <id>")
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	resp, err := client.Get("http://unix/timer/info?id=" + id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "Error: timer '%s' not found\n", id)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, body, "", "  "); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid JSON response: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(prettyJSON.String())
}

func printTimerUsage() {
	fmt.Println(`Usage: cc-connect timer <command> [options]

Commands:
  add       Create a one-shot timer
  list      List pending timers
  info <id> Show detailed info of a timer
  del <id>  Cancel a timer

Run 'cc-connect timer <command> --help' for details.`)
}

func printTimerAddUsage() {
	fmt.Println(`Usage: cc-connect timer add [options] <delay> <prompt>

Create a one-shot timer (fires once after the specified delay).

Options:
  -p, --project <name>       Target project (auto-detected from CC_PROJECT env)
  -s, --session-key <key>    Target session (auto-detected from CC_SESSION_KEY env)
  -d, --delay <duration>     Delay from now (e.g. 30m, 2h, 1h30m)
  -a, --at <time>            Absolute ISO time (e.g. 2026-05-16T09:00, local timezone)
      --prompt <text>        Task prompt (runs through agent)
      --exec <command>       Shell command (runs directly, mutually exclusive with --prompt)
      --desc <text>          Short description
      --session-mode <mode>  reuse (default) or new-per-run
      --timeout-mins <n>     Max minutes to wait per run (0 = no limit; default 30)
      --mute                 Suppress all messages (start + result)
      --data-dir <path>      Data directory (default: ~/.cc-connect)
  -h, --help                 Show this help

Examples:
  cc-connect timer add --delay 2h --prompt "Check PR status"
  cc-connect timer add --delay 30m --exec "df -h" --desc "Disk check"
  cc-connect timer add --at "2026-05-16T09:00" --prompt "Morning standup reminder"
  cc-connect timer add 2h Check PR status`)
}
