package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// sessionFileData mirrors the unexported sessionSnapshot in core/session.go
// for JSON deserialization of session files.
type sessionFileData struct {
	Sessions      map[string]*sessionData    `json:"sessions"`
	ActiveSession map[string]string          `json:"active_session"`
	UserSessions  map[string][]string        `json:"user_sessions"`
	Counter       int64                      `json:"counter"`
	SessionNames  map[string]string          `json:"session_names,omitempty"`
	UserMeta      map[string]*userMetaData   `json:"user_meta,omitempty"`
}

type userMetaData struct {
	UserName string `json:"user_name,omitempty"`
	ChatName string `json:"chat_name,omitempty"`
}

type sessionData struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	AgentSessionID string              `json:"agent_session_id"`
	History        []core.HistoryEntry `json:"history"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

// sessionRecord is a flattened view of one session with its project context.
type sessionRecord struct {
	Project    string
	SessionID  string
	GlobalID   string // "project:session_id" for unique addressing
	Name       string
	Platform   string
	GroupUser  string
	UserName   string // human-readable user name (from UserMeta)
	ChatName   string // human-readable chat/group name (from UserMeta)
	Messages   int
	LastActive time.Time
	History    []core.HistoryEntry
}

func runSessions(args []string) {
	var dataDir string
	var subcommand string
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h", "help":
			printSessionsUsage()
			return
		default:
			if subcommand == "" && (args[i] == "list" || args[i] == "show" || args[i] == "prune") {
				subcommand = args[i]
			} else {
				positional = append(positional, args[i])
			}
		}
	}

	dataDir = resolveDataDir(dataDir)

	switch subcommand {
	case "list":
		runSessionsList(dataDir)
	case "show":
		var id string
		var limit int
		for i := 0; i < len(positional); i++ {
			if (positional[i] == "-n" || positional[i] == "--last") && i+1 < len(positional) {
				i++
				n, err := strconv.Atoi(positional[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid -n value: %s\n", positional[i])
					os.Exit(1)
				}
				limit = n
			} else if id == "" {
				id = positional[i]
			}
		}
		if id == "" {
			fmt.Fprintln(os.Stderr, "Error: session ID is required")
			fmt.Fprintln(os.Stderr, "Usage: cc-connect sessions show <session-id> [-n N]")
			os.Exit(1)
		}
		runSessionsShow(dataDir, id, limit)
	case "prune":
		var mergeHistory bool
		var project string
		for i := 0; i < len(positional); i++ {
			if positional[i] == "--merge" {
				mergeHistory = true
			} else if positional[i] == "--empty" {
				// Only remove empty sessions
			} else if project == "" {
				project = positional[i]
			}
		}
		runSessionsPrune(dataDir, project, mergeHistory)
	default:
		// Default: launch TUI
		runSessionsTUI(dataDir)
	}
}

func resolveDataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cc-connect")
	}
	return ".cc-connect"
}

func loadAllSessions(dataDir string) ([]sessionRecord, error) {
	sessionsDir := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var records []sessionRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		project := strings.TrimSuffix(entry.Name(), ".json")
		filePath := filepath.Join(sessionsDir, entry.Name())

		data, err := os.ReadFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", entry.Name(), err)
			continue
		}

		var fileData sessionFileData
		if err := json.Unmarshal(data, &fileData); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot parse %s: %v\n", entry.Name(), err)
			continue
		}

		// Build reverse index: session_id -> user_key
		sessionToUserKey := make(map[string]string)
		for userKey, sids := range fileData.UserSessions {
			for _, sid := range sids {
				if _, exists := sessionToUserKey[sid]; !exists {
					sessionToUserKey[sid] = userKey
				}
			}
		}

		for _, s := range fileData.Sessions {
			if s == nil {
				continue
			}
			platform, groupUser := "", ""
			var userName, chatName string
			if userKey, ok := sessionToUserKey[s.ID]; ok {
				platform, groupUser = parseSessionKey(userKey)
				if fileData.UserMeta != nil {
					if meta := fileData.UserMeta[userKey]; meta != nil {
						userName = meta.UserName
						chatName = meta.ChatName
					}
				}
			}

			records = append(records, sessionRecord{
				Project:    project,
				SessionID:  s.ID,
				GlobalID:   project + ":" + s.ID,
				Name:       s.Name,
				Platform:   platform,
				GroupUser:  groupUser,
				UserName:   userName,
				ChatName:   chatName,
				Messages:   len(s.History),
				LastActive: s.UpdatedAt,
				History:    s.History,
			})
		}
	}

	// Sort by LastActive descending
	sort.Slice(records, func(i, j int) bool {
		return records[i].LastActive.After(records[j].LastActive)
	})

	return records, nil
}

func parseSessionKey(key string) (platform, groupUser string) {
	if i := strings.Index(key, ":"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

func runSessionsList(dataDir string) {
	records, err := loadAllSessions(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(records) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tProject\tPlatform\tUser\tGroup/Chat\tMsgs\tLast Activity")
	for i, r := range records {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%d\t%s\n",
			i+1,
			r.Project,
			r.Platform,
			displayUser(r),
			displayGroupTrunc(r, 30),
			r.Messages,
			r.LastActive.Format("2006-01-02 15:04"),
		)
	}
	w.Flush()
}

func runSessionsShow(dataDir, id string, limit int) {
	records, err := loadAllSessions(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(records) == 0 {
		fmt.Fprintln(os.Stderr, "No sessions found.")
		os.Exit(1)
	}

	var record *sessionRecord

	// Try index format: "1" or "#1"
	idStr := strings.TrimPrefix(id, "#")
	if idx, err := strconv.Atoi(idStr); err == nil && idx >= 1 && idx <= len(records) {
		record = &records[idx-1]
	} else {
		// Try composite format: "project:session_id"
		for i := range records {
			if records[i].GlobalID == id {
				record = &records[i]
				break
			}
		}
	}

	if record == nil {
		fmt.Fprintf(os.Stderr, "Error: session %q not found\n", id)
		fmt.Fprintln(os.Stderr, "Use 'cc-connect sessions list' to see available sessions.")
		os.Exit(1)
	}

	// Print header
	fmt.Printf("Session: %s (%s)\n", record.GlobalID, record.Name)
	fmt.Printf("Platform: %s | User: %s | Group: %s | Messages: %d\n\n",
		record.Platform, displayUser(*record), displayGroup(*record), record.Messages)

	history := record.History
	if limit > 0 && limit < len(history) {
		history = history[len(history)-limit:]
	}

	if len(history) == 0 {
		fmt.Println("No messages.")
		return
	}

	var lastDate string
	for _, entry := range history {
		date := entry.Timestamp.Format("2006-01-02")
		if date != lastDate {
			fmt.Printf("--- %s ---\n", date)
			lastDate = date
		}
		fmt.Printf("%s  [%s]  %s\n",
			entry.Timestamp.Format("15:04"),
			entry.Role,
			entry.Content,
		)
	}
}

func runSessionsPrune(dataDir, project string, mergeHistory bool) {
	sessionsDir := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No sessions directory found.")
			return
		}
		fmt.Fprintf(os.Stderr, "Error: cannot read sessions dir: %v\n", err)
		os.Exit(1)
	}

	totalRemoved := 0
	totalMerged := 0
	totalChats := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		projectName := strings.TrimSuffix(entry.Name(), ".json")
		// If user specified a project, skip others
		if project != "" && projectName != project {
			continue
		}

		filePath := filepath.Join(sessionsDir, entry.Name())
		sm := core.NewSessionManager(filePath)

		result := sm.PruneDuplicateSessions(mergeHistory)
		if len(result.RemovedSessions) > 0 {
			fmt.Printf("Project %s:\n", projectName)
			fmt.Printf("  Removed %d duplicate sessions\n", len(result.RemovedSessions))
			if mergeHistory && result.MergedHistory > 0 {
				fmt.Printf("  Merged %d history entries\n", result.MergedHistory)
			}
			fmt.Printf("  %d chats had duplicates\n", result.ChatsAffected)
			for _, sid := range result.RemovedSessions {
				fmt.Printf("    - %s\n", sid)
			}
			totalRemoved += len(result.RemovedSessions)
			totalMerged += result.MergedHistory
			totalChats += result.ChatsAffected
		}
	}

	if totalRemoved == 0 {
		fmt.Println("No duplicate sessions found.")
	} else {
		fmt.Println()
		fmt.Printf("Total: removed %d sessions, merged %d entries, %d chats affected\n",
			totalRemoved, totalMerged, totalChats)
	}
}

func displayUser(r sessionRecord) string {
	if r.UserName != "" {
		return r.UserName
	}
	return "-"
}

func displayGroup(r sessionRecord) string {
	if r.ChatName != "" {
		return r.ChatName
	}
	if r.GroupUser != "" {
		return r.GroupUser
	}
	return "-"
}

func displayGroupTrunc(r sessionRecord, maxLen int) string {
	s := displayGroup(r)
	return truncate(s, maxLen)
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func printSessionsUsage() {
	fmt.Println(`Usage: cc-connect sessions [command] [options]

Browse and manage session history.

Commands:
  (none)             Interactive TUI browser (default)
  list               List all sessions (pipe-friendly)
  show <id> [-n N]   Show session messages
  prune [project] [--merge]  Remove duplicate sessions per chat

Options:
  --data-dir <path>  Data directory (default: ~/.cc-connect)
  -h, --help         Show this help

Session ID formats for 'show':
  <project>:<session>   e.g. "feishu_bot_64788ce0:s1"
  <number> or #<number> Index from 'sessions list', e.g. "1" or "#1"

Prune options:
  --merge    Merge history from removed sessions into kept one
             (without --merge, only removes sessions with no history)

Examples:
  cc-connect sessions                           Interactive TUI browser
  cc-connect sessions list                      List all sessions
  cc-connect sessions show "mybot:s1"           Show all messages in session
  cc-connect sessions show "#1" -n 20           Show last 20 messages of first session
  cc-connect sessions prune                     Remove empty duplicate sessions
  cc-connect sessions prune --merge             Merge duplicates, keeping most recent
  cc-connect sessions prune mybot --merge       Prune specific project`)
}
