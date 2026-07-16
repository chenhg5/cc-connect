package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JayGarland/cc-connect/config"
)

func runWorktree(args []string) {
	if len(args) == 0 {
		printWorktreeUsage()
		return
	}
	switch args[0] {
	case "prune":
		runWorktreePrune(args[1:])
	case "--help", "-h", "help":
		printWorktreeUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown worktree subcommand: %s\n", args[0])
		printWorktreeUsage()
		os.Exit(1)
	}
}

func printWorktreeUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  cc-connect worktree <command> [args]

Commands:
  prune      Prune abandoned dynamic worktrees (checks active daemon sessions)
`)
}

type activeSessionInfo struct {
	Project    string `json:"project"`
	SessionKey string `json:"session_key"`
	Platform   string `json:"platform"`
}

func runWorktreePrune(args []string) {
	var configPath, dataDir string
	var dryRun, force bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				i++
				configPath = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--dry-run", "-d":
			dryRun = true
		case "--force", "-f":
			force = true
		case "--help", "-h":
			printWorktreePruneUsage()
			return
		}
	}

	// Resolve config path
	configPath = resolveConfigPath(configPath)
	if dataDir == "" && configPath != "" {
		dataDir = filepath.Join(filepath.Dir(configPath), "data")
	}
	dataDir = resolveDataDir(dataDir)

	// Load config
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Connect to api.sock to query active sessions
	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		if !force {
			fmt.Fprintf(os.Stderr, "Error: cc-connect daemon is not running (socket not found: %s).\nUse --force to prune worktrees offline (assumes no active sessions).\n", sockPath)
			os.Exit(1)
		}
		fmt.Println("Warning: daemon is offline. Pruning all dynamic worktrees in offline mode...")
	}

	activeThreads := make(map[string]bool)
	if _, err := os.Stat(sockPath); err == nil {
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", sockPath)
				},
			},
		}

		resp, err := client.Get("http://unix/sessions")
		if err != nil {
			if !force {
				fmt.Fprintf(os.Stderr, "Error: failed to connect to daemon: %v.\nUse --force to override.\n", err)
				os.Exit(1)
			}
			fmt.Printf("Warning: failed to query daemon: %v. Proceeding in offline mode...\n", err)
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode == http.StatusOK {
				var activeSessions []activeSessionInfo
				if err := json.Unmarshal(body, &activeSessions); err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing active sessions: %v\n", err)
					os.Exit(1)
				}
				for _, s := range activeSessions {
					threadID := extractThreadID(s.SessionKey)
					if threadID != "" {
						activeThreads[threadID] = true
					}
				}
			}
		}
	}

	// Scan projects for worktree patterns and prune
	prunedCount := 0
	for _, proj := range cfg.Projects {
		// Read workspace_pattern from project (check both proj.WorkspacePattern and proj.Agent.Options["workspace_pattern"])
		pattern := proj.WorkspacePattern
		if pattern == "" {
			if optPattern, ok := proj.Agent.Options["workspace_pattern"].(string); ok {
				pattern = optPattern
			}
		}
		if pattern == "" {
			continue
		}
		letterByTopic := loadDispatchLettersByTopic(dataDir, proj.Name)
		activeLetters := make(map[string]bool)
		for threadID := range activeThreads {
			if letterID := letterByTopic[threadID]; letterID != "" {
				activeLetters[letterID] = true
			}
		}

		// Read base work_dir / base repo
		workDir, _ := proj.Agent.Options["work_dir"].(string)
		if workDir == "" {
			continue
		}

		// Run git worktree list
		gitArgs := []string{"-C", workDir, "worktree", "list", "--porcelain"}
		cmd := exec.Command("git", gitArgs...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to list worktrees in %s: %v\n", workDir, err)
			continue
		}

		// Parse worktrees from git output
		// Format:
		// worktree /path/to/worktree
		// branch refs/heads/branch_name
		lines := strings.Split(string(output), "\n")
		var currentPath string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "worktree ") {
				currentPath = strings.TrimPrefix(line, "worktree ")
			} else if strings.HasPrefix(line, "branch refs/heads/") && currentPath != "" {
				branch := strings.TrimPrefix(line, "branch refs/heads/")

				// Check if the worktree folder or branch matches the configured pattern.
				threadID := extractThreadIDFromPath(pattern, currentPath)
				letterID := extractLetterIDFromPath(pattern, currentPath)
				shouldPrune := false
				activeLabel := ""
				if threadID != "" && isThreadWorktreeBranch(branch) {
					// Check if active
					shouldPrune = !activeThreads[threadID]
					activeLabel = "thread " + threadID
				} else if letterID != "" && branch == "letter/"+letterID {
					shouldPrune = !activeLetters[letterID]
					activeLabel = "letter " + letterID
				}
				if activeLabel != "" {
					if !shouldPrune {
						fmt.Printf("[%s] Skipping active worktree: %s (%s)\n", proj.Name, currentPath, activeLabel)
					} else {
						// Prune!
						fmt.Printf("[%s] Found abandoned worktree: %s (branch: %s)\n", proj.Name, currentPath, branch)
						if dryRun {
							fmt.Println("  (Dry-run) Would prune worktree and delete branch.")
						} else {
							// 1. Remove worktree
							rmCmd := exec.Command("git", "-C", workDir, "worktree", "remove", currentPath)
							if rmOut, rmErr := rmCmd.CombinedOutput(); rmErr != nil {
								fmt.Fprintf(os.Stderr, "  Error removing worktree %s: %v (output: %s)\n", currentPath, rmErr, string(rmOut))
							} else {
								fmt.Printf("  Removed worktree: %s\n", currentPath)
								// 2. Delete branch
								brCmd := exec.Command("git", "-C", workDir, "branch", "-D", branch)
								if brOut, brErr := brCmd.CombinedOutput(); brErr != nil {
									fmt.Fprintf(os.Stderr, "  Error deleting branch %s: %v (output: %s)\n", branch, brErr, string(brOut))
								} else {
									fmt.Printf("  Deleted branch: %s\n", branch)
								}
								prunedCount++
							}
						}
					}
				}
				currentPath = ""
			}
		}
	}

	if dryRun {
		fmt.Println("Dry-run complete.")
	} else {
		fmt.Printf("Prune complete. Successfully pruned %d worktrees.\n", prunedCount)
	}
}

func printWorktreePruneUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  cc-connect worktree prune [flags]

Flags:
  -c, --config <path>   Path to config file
  --data-dir <path>     Path to session data directory
  -d, --dry-run         Print abandoned worktrees without deleting them
  -f, --force           Prune offline (ignore missing/failed API socket connection)
`)
}

func isThreadWorktreeBranch(branch string) bool {
	return strings.HasPrefix(branch, "letter-") ||
		strings.HasPrefix(branch, "letter/") ||
		strings.HasPrefix(branch, "task-")
}

func extractThreadID(sessionKey string) string {
	parts := strings.Split(sessionKey, ":")
	if len(parts) == 4 { // telegram:chatID:threadID:userID
		return parts[2]
	} else if len(parts) == 5 { // telegram:t:chatID:threadID:userID
		return parts[3]
	}
	return ""
}

type dispatchPruneLedger struct {
	Expectations []dispatchPruneExpectation `json:"expectations"`
}

type dispatchPruneExpectation struct {
	Letter          string `json:"letter"`
	To              string `json:"to"`
	TopicID         string `json:"topic_id,omitempty"`
	TopicSessionKey string `json:"topic_session_key,omitempty"`
}

func loadDispatchLettersByTopic(dataDir, projectName string) map[string]string {
	out := make(map[string]string)
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(projectName) == "" {
		return out
	}
	path := filepath.Join(dataDir, "dispatch_expectations.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var ledger dispatchPruneLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return out
	}
	for _, exp := range ledger.Expectations {
		if !strings.EqualFold(exp.To, projectName) || strings.TrimSpace(exp.Letter) == "" {
			continue
		}
		if strings.TrimSpace(exp.TopicID) != "" {
			out[exp.TopicID] = exp.Letter
		}
		if threadID := extractThreadID(exp.TopicSessionKey); threadID != "" {
			out[threadID] = exp.Letter
		}
	}
	return out
}

func extractThreadIDFromPath(pattern, path string) string {
	// Normalize slashes
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// Replace placeholder patterns with named capturing group (\d+)
	rePattern := regexp.QuoteMeta(pattern)
	// Match ${THREAD_ID}, {{THREAD_ID}}, __THREAD_ID__
	placeholderRe := regexp.MustCompile(`\\\{\\\{THREAD_ID\\\}\\\}|\\\$__THREAD_ID__|\\\$__THREAD_ID__|\$__THREAD_ID__|\$\\\{THREAD_ID\\\}|__THREAD_ID__`)
	rePattern = placeholderRe.ReplaceAllString(rePattern, `(\d+)`)

	// Compile regex and match
	re, err := regexp.Compile("^" + rePattern + "$")
	if err != nil {
		return ""
	}
	matches := re.FindStringSubmatch(path)
	if len(matches) == 2 {
		return matches[1]
	}
	return ""
}

func extractLetterIDFromPath(pattern, path string) string {
	return extractLiteralPlaceholderFromPath(pattern, path, "{{LETTER_ID}}")
}

func extractLiteralPlaceholderFromPath(pattern, path, placeholder string) string {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	idx := strings.Index(pattern, placeholder)
	if idx == -1 {
		return ""
	}
	prefix := pattern[:idx]
	suffix := pattern[idx+len(placeholder):]

	val := path
	if strings.HasPrefix(val, prefix) {
		val = strings.TrimPrefix(val, prefix)
	}
	if suffix != "" && strings.HasSuffix(val, suffix) {
		val = strings.TrimSuffix(val, suffix)
	}
	if val == path && (prefix != "" || suffix != "") {
		return ""
	}
	return val
}
