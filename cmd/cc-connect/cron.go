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
	case "edit":
		runCronEdit(args[1:])
	case "info":
		runCronInfo(args[1:])
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
	var project, sessionKey, cronExpr, prompt, execCmd, desc, dataDir, sessionMode string
	var timeoutMins *int

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
				n, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --timeout-mins: %v\n", err)
					os.Exit(1)
				}
				timeoutMins = &n
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

	// If cron expr not provided via --cron, try positional: first 5 fields are cron, rest is prompt/exec
	if cronExpr == "" && len(positional) >= 6 {
		cronExpr = strings.Join(positional[:5], " ")
		if prompt == "" && execCmd == "" {
			prompt = strings.Join(positional[5:], " ")
		}
	} else if prompt == "" && execCmd == "" && len(positional) > 0 {
		prompt = strings.Join(positional, " ")
	}

	if cronExpr == "" || (prompt == "" && execCmd == "") {
		fmt.Fprintln(os.Stderr, "Error: cron expression and either --prompt or --exec are required")
		printCronAddUsage()
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
		"cron_expr":   cronExpr,
		"prompt":      prompt,
		"exec":        execCmd,
		"description": desc,
	}
	if sessionMode != "" {
		body["session_mode"] = sessionMode
	}
	if timeoutMins != nil {
		body["timeout_mins"] = *timeoutMins
	}
	payload, _ := json.Marshal(body)

	resp, err := apiPost(sockPath, "/cron/add", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

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
	fmt.Printf("Cron job created: %s\n", result["id"])
	fmt.Printf("Schedule: %s\n", result["cron_expr"])
	if execCmd != "" {
		fmt.Printf("Command: %s\n", result["exec"])
	} else {
		fmt.Printf("Prompt: %s\n", result["prompt"])
	}
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
	if err := json.Unmarshal(body, &jobs); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid response: %v\n", err)
		os.Exit(1)
	}

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
		execCmd, _ := j["exec"].(string)
		desc, _ := j["description"].(string)
		display := desc
		if display == "" {
			if execCmd != "" {
				display = "🖥 " + execCmd
			} else {
				display = prompt
			}
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

func runCronInfo(args []string) {
	var dataDir, id, field string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		default:
			if id == "" {
				id = args[i]
			} else if field == "" {
				field = args[i]
			}
		}
	}

	if id == "" {
		fmt.Fprintln(os.Stderr, "Error: job ID is required")
		fmt.Fprintln(os.Stderr, "Usage: cc-connect cron info <id> [field]")
		fmt.Fprintln(os.Stderr, "Use 'cc-connect cron list' to see all task IDs.")
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

	resp, err := client.Get("http://unix/cron/info?id=" + id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "Error: job '%s' not found\n", id)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	// If field specified, extract and print only that field
	if field != "" {
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid JSON response: %v\n", err)
			os.Exit(1)
		}
		val, ok := result[field]
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: field '%s' not found\n", field)
			fmt.Fprintln(os.Stderr, "Available fields:")
			for k := range result {
				fmt.Fprintf(os.Stderr, "  - %s\n", k)
			}
			os.Exit(1)
		}
		// Output field value (string directly, otherwise JSON formatted)
		if s, ok := val.(string); ok {
			fmt.Println(s)
		} else {
			data, _ := json.MarshalIndent(val, "", "  ")
			fmt.Println(string(data))
		}
		return
	}

	// Pretty-print full JSON
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, body, "", "  "); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid JSON response: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(prettyJSON.String())
}

func runCronEdit(args []string) {
	var dataDir string
	var id, field string
	var valueStr string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h":
			printCronEditUsage()
			return
		default:
			if id == "" {
				id = args[i]
			} else if field == "" {
				field = args[i]
			} else if valueStr == "" {
				valueStr = args[i]
			}
		}
	}

	if id == "" {
		fmt.Fprintln(os.Stderr, "Error: job ID is required")
		fmt.Fprintln(os.Stderr, "Run 'cc-connect cron edit --help' for usage.")
		os.Exit(1)
	}
	if field == "" {
		fmt.Fprintln(os.Stderr, "Error: field name is required")
		fmt.Fprintln(os.Stderr, "Run 'cc-connect cron edit --help' for usage.")
		os.Exit(1)
	}
	if valueStr == "" {
		fmt.Fprintln(os.Stderr, "Error: value is required")
		fmt.Fprintln(os.Stderr, "Run 'cc-connect cron edit --help' for usage.")
		os.Exit(1)
	}

	// Parse value based on field type
	var value any
	switch field {
	case "enabled", "mute":
		v, err := strconv.ParseBool(valueStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s must be true or false\n", field)
			os.Exit(1)
		}
		value = v
	case "timeout_mins":
		v, err := strconv.Atoi(valueStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: timeout_mins must be an integer\n")
			os.Exit(1)
		}
		value = v
	default:
		// String fields: project, session_key, cron_expr, prompt, exec, work_dir, description, session_mode
		value = valueStr
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	payload, _ := json.Marshal(map[string]any{"id": id, "field": field, "value": value})
	resp, err := apiPost(sockPath, "/cron/edit", payload)
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

	// Pretty-print updated job
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, body, "", "  "); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid JSON response: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Updated job %s:\n%s\n", id, prettyJSON.String())
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
  edit      Edit a scheduled task field
  info <id> [field]  Show detailed info of a scheduled task
                     (optionally filter to a single field)
  del <id>  Delete a scheduled task

Run 'cc-connect cron <command> --help' for details.`)
}

func printCronAddUsage() {
	fmt.Println(`Usage: cc-connect cron add [options] [<min> <hour> <day> <month> <weekday> <prompt>]

Create a new scheduled task (agent prompt or shell command).

Options:
  -p, --project <name>       Target project (auto-detected from CC_PROJECT env)
  -s, --session-key <key>    Target session (auto-detected from CC_SESSION_KEY env)
  -c, --cron <expr>          Cron expression, e.g. "0 6 * * *"
      --prompt <text>        Task prompt (runs through agent)
      --exec <command>       Shell command (runs directly, mutually exclusive with --prompt)
      --desc <text>          Short description
      --session-mode <mode>  reuse (default) or new-per-run — fresh agent session each run
      --timeout-mins <n>     Max minutes to wait per run (0 = no limit; default 30 if omitted)
      --data-dir <path>      Data directory (default: ~/.cc-connect)
  -h, --help                 Show this help

Examples:
  cc-connect cron add --cron "0 6 * * *" --prompt "Collect GitHub trending data" --desc "Daily Trending"
  cc-connect cron add --cron "*/30 * * * *" --exec "df -h" --desc "Disk usage check"
  cc-connect cron add 0 6 * * * Collect GitHub trending data and send me a summary`)
}

func printCronEditUsage() {
	fmt.Println(`Usage: cc-connect cron edit <id> <field> <value> [options]

Edit a specific field of an existing scheduled task.

Editable Fields (string):
  project       Target project name
  session_key   Target session key
  cron_expr     Cron expression, e.g. "0 6 * * *"
  prompt        Task prompt (runs through agent)
  exec          Shell command (runs directly)
  work_dir      Working directory for exec
  description   Short description
  session_mode  reuse or new_per_run

Editable Fields (bool: true/false):
  enabled       Enable or disable the task
  mute          Suppress all messages
  silent        Suppress start notification

Editable Fields (int: number):
  timeout_mins  Max minutes per run (0 = no limit)

Read-only Fields (cannot be edited):
  id, created_at, last_run, last_error

Options:
      --data-dir <path>  Data directory (default: ~/.cc-connect)
  -h, --help             Show this help

Examples:
  cc-connect cron edit abc123 cron_expr "0 9 * * *"
  cc-connect cron edit abc123 enabled false
  cc-connect cron edit abc123 description "Daily standup reminder"
  cc-connect cron edit abc123 timeout_mins 60
  cc-connect cron edit abc123 mute true`)
}
