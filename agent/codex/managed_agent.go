package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type managedOptions struct {
	socket                                 string
	attachOnly                             bool
	reconnect                              int
	steer, interrupt, approvals, questions bool
	deleteThreads                          bool
	asyncQuestions                         bool
	busyMessageMode                        string
}

func parseManagedOptions(opts map[string]any) (managedOptions, error) {
	config := managedOptions{reconnect: 3, steer: true, interrupt: true, approvals: true, questions: true, asyncQuestions: true, deleteThreads: true, busyMessageMode: "queue"}
	for key := range opts {
		if !strings.HasPrefix(key, "daemon_") {
			continue
		}
		switch key {
		case "daemon_socket", "daemon_attach_only", "daemon_reconnect_attempts", "daemon_enable_steer", "daemon_enable_interrupt", "daemon_enable_approvals", "daemon_enable_questions", "daemon_enable_async_questions", "daemon_enable_delete", "daemon_busy_message_mode":
		default:
			return config, fmt.Errorf("codex: unknown managed daemon option %s", key)
		}
	}
	var err error
	if value, ok := opts["daemon_socket"]; ok {
		config.socket, ok = value.(string)
		if !ok {
			return config, fmt.Errorf("codex: daemon_socket must be a string")
		}
		config.socket = strings.TrimSpace(config.socket)
	}
	for key, target := range map[string]*bool{"daemon_attach_only": &config.attachOnly, "daemon_enable_steer": &config.steer, "daemon_enable_interrupt": &config.interrupt, "daemon_enable_approvals": &config.approvals, "daemon_enable_questions": &config.questions, "daemon_enable_async_questions": &config.asyncQuestions, "daemon_enable_delete": &config.deleteThreads} {
		if value, ok := opts[key]; ok {
			parsed, ok := value.(bool)
			if !ok {
				return config, fmt.Errorf("codex: %s must be a boolean", key)
			}
			*target = parsed
		}
	}
	if value, exists := opts["daemon_busy_message_mode"]; exists {
		mode, ok := value.(string)
		if !ok {
			return config, fmt.Errorf("codex: daemon_busy_message_mode must be queue or steer")
		}
		mode = strings.ToLower(strings.TrimSpace(mode))
		if mode != "queue" && mode != "steer" {
			return config, fmt.Errorf("codex: daemon_busy_message_mode must be queue or steer")
		}
		config.busyMessageMode = mode
	}
	if config.busyMessageMode == "steer" && !config.steer {
		return config, fmt.Errorf("codex: daemon_busy_message_mode=steer requires daemon_enable_steer=true")
	}
	if value, ok := opts["daemon_reconnect_attempts"]; ok {
		switch n := value.(type) {
		case int:
			config.reconnect = n
		case int64:
			config.reconnect = int(n)
		default:
			err = fmt.Errorf("codex: daemon_reconnect_attempts must be an integer")
		}
		if err != nil {
			return config, err
		}
		if config.reconnect < 0 || config.reconnect > 10 {
			return config, fmt.Errorf("codex: daemon_reconnect_attempts must be between 0 and 10")
		}
	}
	for _, key := range []string{"env", "app_server_url"} {
		if _, exists := opts[key]; exists {
			return config, fmt.Errorf("codex: %s is for owned processes; managed_daemon owns its environment and endpoint", key)
		}
	}
	return config, nil
}

// Only this transport exposes live attachment to core. The ordinary Agent's
// optional interface set and stdio process lifecycle are unchanged.
type managedAgent struct {
	*Agent
	daemon managedOptions
}

func (*managedAgent) DefaultSettingsOnly() bool { return true }

// Preserve the selected provider by name when its config catalog is edited.
// Index shifts must not silently select another provider for future threads.
func (a *managedAgent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := ""
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		name = a.providers[a.activeIdx].Name
	}
	a.providers = append([]core.ProviderConfig(nil), providers...)
	a.activeIdx = -1
	for i, p := range a.providers {
		if name != "" && p.Name == name {
			a.activeIdx = i
			break
		}
	}
}

func (a *managedAgent) daemonSocket(ctx context.Context) (string, error) {
	if a.daemon.socket != "" {
		return a.daemon.socket, nil
	}
	a.mu.RLock()
	cmd := a.cmd
	args := append([]string(nil), a.cliExtraArgs...)
	home := a.codexHome
	a.mu.RUnlock()
	var env []string
	if home != "" {
		env = core.MergeEnv(os.Environ(), []string{"CODEX_HOME=" + home})
	}
	return discoverDaemon(ctx, cmd, args, env)
}

func (a *managedAgent) StartSession(ctx context.Context, id string) (core.AgentSession, error) {
	if id == core.ContinueSession {
		return nil, fmt.Errorf("codex daemon requires an explicit thread ID to attach")
	}
	if id != "" {
		return a.AttachSession(ctx, id)
	}
	if a.daemon.attachOnly {
		return nil, fmt.Errorf("codex daemon attach-only mode: select a thread with /attach or /switch first")
	}
	return newManagedSession(ctx, a, "")
}

func (a *managedAgent) AttachSession(ctx context.Context, id string) (core.AgentSession, error) {
	if strings.TrimSpace(id) == "" || id == core.ContinueSession || id == os.Getenv("CODEX_THREAD_ID") {
		return nil, fmt.Errorf("codex daemon refuses an empty, implicit or current agent thread ID")
	}
	return newManagedSession(ctx, a, id)
}

// An already-running daemon cannot receive per-CC-session process environment.
// This deliberately avoids changing its global environment or credentials.
func (a *managedAgent) SetSessionEnv(_ []string) {}

// Query the account of the connected daemon, not this process's auth.json.
func (a *managedAgent) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	rpc, err := a.openRPC(ctx)
	if err != nil {
		return nil, err
	}
	defer rpc.close()
	var payload appServerRateLimitsResponse
	if err := rpc.request(ctx, "account/rateLimits/read", map[string]any{}, &payload); err != nil {
		return nil, err
	}
	return mapAppServerRateLimits(payload), nil
}

func sameManagedCwd(a, b string) bool {
	canonical := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return ""
		}
		if real, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = real
		}
		return filepath.Clean(absolute)
	}
	return b != "" && canonical(a) == canonical(b)
}

func (a *managedAgent) openRPC(ctx context.Context) (*daemonRPC, error) {
	socket, err := a.daemonSocket(ctx)
	if err != nil {
		return nil, err
	}
	return connectDaemon(ctx, socket)
}

func (a *managedAgent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	rpc, err := a.openRPC(ctx)
	if err != nil {
		return nil, err
	}
	defer rpc.close()
	cwd := a.GetWorkDir()
	var result []core.AgentSessionInfo
	cursor := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Data       []managedThread `json:"data"`
			NextCursor *string         `json:"nextCursor"`
		}
		absolute, _ := filepath.Abs(cwd)
		params := map[string]any{"limit": 100, "cwd": absolute, "sortKey": "updated_at"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := rpc.request(ctx, "thread/list", params, &page); err != nil {
			return nil, err
		}
		for _, thread := range page.Data {
			if sameManagedCwd(cwd, thread.Cwd) && !managedSubagent(thread.Source) {
				result = append(result, core.AgentSessionInfo{ID: thread.ID, Summary: thread.Preview, ModifiedAt: time.Unix(thread.UpdatedAt, 0)})
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("codex daemon thread/list repeated cursor")
		}
		seen[cursor] = true
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ModifiedAt.After(result[j].ModifiedAt) })
	return result, nil
}

func (a *managedAgent) GetSessionHistory(ctx context.Context, id string, limit int) ([]core.HistoryEntry, error) {
	if id == os.Getenv("CODEX_THREAD_ID") {
		return nil, fmt.Errorf("codex daemon refuses current agent thread")
	}
	rpc, err := a.openRPC(ctx)
	if err != nil {
		return nil, err
	}
	defer rpc.close()
	var response struct {
		Thread managedThread `json:"thread"`
	}
	if err := rpc.request(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &response); err != nil {
		return nil, fmt.Errorf("codex daemon history metadata: %w", err)
	}
	if response.Thread.ID != id || !sameManagedCwd(a.GetWorkDir(), response.Thread.Cwd) {
		return nil, fmt.Errorf("codex daemon history is outside configured work_dir")
	}
	return readManagedHistory(ctx, rpc, id, limit)
}

// Oversample message limits because some turns have no displayable history.
// Even unlimited history uses bounded pages rather than full thread hydration.
func readManagedHistory(ctx context.Context, rpc *daemonRPC, id string, limit int) ([]core.HistoryEntry, error) {
	pageSize := 50
	if limit > 0 && limit < pageSize/2 {
		pageSize = 2 * limit
	}
	var pages [][]core.HistoryEntry
	count := 0
	cursor := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Data       []managedTurn `json:"data"`
			NextCursor *string       `json:"nextCursor"`
		}
		params := map[string]any{"threadId": id, "limit": pageSize, "sortDirection": "desc", "itemsView": "full"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := rpc.request(ctx, "thread/turns/list", params, &page); err != nil {
			return nil, fmt.Errorf("codex daemon history page: %w", err)
		}
		// Reverse newest-first turns, preserving item order within each turn.
		for i, j := 0, len(page.Data)-1; i < j; i, j = i+1, j-1 {
			page.Data[i], page.Data[j] = page.Data[j], page.Data[i]
		}
		entries := managedTurnsHistory(page.Data)
		pages = append(pages, entries)
		count += len(entries)
		if (limit > 0 && count >= limit) || page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("codex daemon thread/turns/list repeated cursor")
		}
		seen[cursor] = true
	}
	entries := make([]core.HistoryEntry, 0, count)
	for i := len(pages) - 1; i >= 0; i-- {
		entries = append(entries, pages[i]...)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func managedTurnsHistory(turns []managedTurn) []core.HistoryEntry {
	var entries []core.HistoryEntry
	for _, turn := range turns {
		for _, item := range turn.Items {
			kind, _ := item["type"].(string)
			text, _ := item["text"].(string)
			role := "assistant"
			if kind == "userMessage" {
				role = "user"
				if content, ok := item["content"].([]any); ok {
					var parts []string
					for _, raw := range content {
						if part, ok := raw.(map[string]any); ok {
							if value, ok := part["text"].(string); ok {
								parts = append(parts, value)
							}
						}
					}
					text = strings.Join(parts, "\n")
				}
				text = core.StripSharedBridgePrompt(text)
			} else if kind != "agentMessage" {
				continue
			}
			if text != "" {
				timestamp := turn.StartedAt
				if role == "assistant" && turn.CompletedAt != nil {
					timestamp = turn.CompletedAt
				}
				entry := core.HistoryEntry{Role: role, Content: text}
				if timestamp != nil {
					entry.Timestamp = time.Unix(*timestamp, 0)
				}
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

func (a *managedAgent) DeleteSession(ctx context.Context, id string) error {
	if !a.daemon.deleteThreads {
		return fmt.Errorf("codex managed thread deletion is disabled")
	}
	if strings.TrimSpace(id) == "" || id == core.ContinueSession || id == os.Getenv("CODEX_THREAD_ID") {
		return fmt.Errorf("codex managed refuses implicit or current agent thread deletion")
	}
	rpc, err := a.openRPC(ctx)
	if err != nil {
		return err
	}
	defer rpc.close()
	var response struct {
		Thread managedThread `json:"thread"`
	}
	if err := rpc.request(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true}, &response); err != nil {
		return err
	}
	if response.Thread.ID != id || !sameManagedCwd(a.GetWorkDir(), response.Thread.Cwd) || managedSubagent(response.Thread.Source) {
		return fmt.Errorf("codex managed deletion is outside configured root thread scope")
	}
	for _, turn := range response.Thread.Turns {
		if turn.Status == "inProgress" {
			return fmt.Errorf("codex managed cannot delete an active thread")
		}
	}
	return rpc.request(ctx, "thread/delete", map[string]any{"threadId": id}, nil)
}

func (a *managedAgent) WorkspaceAgentOptions() map[string]any {
	opts := a.Agent.WorkspaceAgentOptions()
	opts["app_server_transport"] = "managed_daemon"
	if a.daemon.socket != "" {
		opts["daemon_socket"] = a.daemon.socket
	}
	opts["daemon_attach_only"] = a.daemon.attachOnly
	opts["daemon_reconnect_attempts"] = a.daemon.reconnect
	opts["daemon_enable_steer"] = a.daemon.steer
	opts["daemon_enable_interrupt"] = a.daemon.interrupt
	opts["daemon_enable_approvals"] = a.daemon.approvals
	opts["daemon_enable_questions"] = a.daemon.questions
	opts["daemon_enable_async_questions"] = a.daemon.asyncQuestions
	opts["daemon_enable_delete"] = a.daemon.deleteThreads
	opts["daemon_busy_message_mode"] = a.daemon.busyMessageMode
	delete(opts, "app_server_url")
	return opts
}

func (a *managedAgent) AutoSteerBusyMessages() bool { return a.daemon.busyMessageMode == "steer" }

func managedSubagent(source json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(source, &object) != nil {
		return false
	}
	_, exists := object["subagent"]
	return exists
}
