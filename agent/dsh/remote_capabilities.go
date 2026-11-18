package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (c *rpcClient) remoteFirst(ctx context.Context, endpoint string, args any, out any) error {
	lifetime, cancel := context.WithTimeout(ctx, 15*time.Second)
	r, err := c.connection(lifetime)
	if err != nil {
		cancel()
		return err
	}
	stream, err := r.open(lifetime, endpoint, args)
	if err != nil {
		cancel()
		return err
	}
	defer func() { cancel(); r.forget(stream) }()
	raw, err := r.next(lifetime, stream)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func (c *rpcClient) sessionSnapshot(ctx context.Context, id string, limit int) (remoteFollowFrame, error) {
	var snapshot remoteFollowFrame
	err := c.remoteFirst(ctx, "session/follow", map[string]any{"request": map[string]any{"address": map[string]any{"kind": "session", "sessionId": id}, "maxMessages": limit}}, &snapshot)
	if err == nil && snapshot.Type != "snapshot" {
		err = fmt.Errorf("dsh: expected session history snapshot")
	}
	return snapshot, err
}

// GetSessionHistory reads DSH's current conversation surface, respecting native
// replacement ranges from edits and compaction rather than showing stale text.
func (a *Agent) GetSessionHistory(ctx context.Context, id string, limit int) ([]core.HistoryEntry, error) {
	if a.client.token == "" {
		return nil, core.ErrNotSupported
	}
	if !a.ValidateSessionID(ctx, id) {
		return nil, fmt.Errorf("dsh: session does not belong to this workspace")
	}
	if limit <= 0 {
		limit = 100
	}
	snapshot, err := a.client.sessionSnapshot(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	records := snapshot.Records
	for snapshot.HasMore && len(records) > 0 {
		before := records[0].Event.Seq
		var page struct {
			Records []remoteHistoryRecord `json:"records"`
			HasMore bool                  `json:"hasMore"`
		}
		err := a.client.remoteInvoke(ctx, "session/page", map[string]any{"request": map[string]any{"address": map[string]any{"kind": "session", "sessionId": id}, "throughSeq": snapshot.Cursor, "beforeSeq": before, "maxMessages": limit}}, &page)
		if err != nil {
			return nil, err
		}
		if len(page.Records) == 0 || page.Records[0].Event.Seq >= before {
			return nil, fmt.Errorf("dsh: history pagination did not advance")
		}
		records = append(page.Records, records...)
		snapshot.HasMore = page.HasMore
	}
	return foldHistory(records, limit), nil
}
func foldHistory(records []remoteHistoryRecord, limit int) []core.HistoryEntry {
	type item struct {
		seq   int64
		entry core.HistoryEntry
	}
	var surface []item
	sort.SliceStable(records, func(i, j int) bool { return records[i].Event.Seq < records[j].Event.Seq })
	for _, record := range records {
		ev := record.Event
		role := ""
		switch ev.Type {
		case "user/message":
			role = "user"
		case "assistant/message":
			role = "assistant"
		case "system/message", "developer/message", "tool/result":
			// Retain hidden surface nodes: replacement endpoints can name them.
		default:
			continue
		}
		content := eventMessageText(ev.Data)
		at := len(surface)
		var replacement struct {
			Op               string `json:"op"`
			StartSeq, EndSeq int64
		}
		if json.Unmarshal(ev.SurfaceOp, &replacement) == nil && replacement.Op == "replace" {
			// Native endpoints name positions in surface order, not numeric seq ranges.
			start, end := -1, -1
			for i, old := range surface {
				if old.seq == replacement.StartSeq {
					start = i
				}
				if old.seq == replacement.EndSeq {
					end = i
				}
			}
			if start >= 0 && end >= start {
				at = start
				surface = append(surface[:start], surface[end+1:]...)
			}
		}
		next := item{seq: ev.Seq, entry: core.HistoryEntry{Role: role, Content: content, Timestamp: time.UnixMilli(int64(ev.Time))}}
		surface = append(surface, item{})
		copy(surface[at+1:], surface[at:])
		surface[at] = next
	}
	entries := make([]core.HistoryEntry, 0, len(surface))
	for _, item := range surface {
		if item.entry.Role != "" && item.entry.Content != "" {
			entries = append(entries, item.entry)
		}
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries
}

func eventMessageText(raw json.RawMessage) string {
	var data struct {
		Content []struct{ Type, Text string }
		Message struct{ Content []struct{ Type, Text string } }
	}
	if json.Unmarshal(raw, &data) != nil {
		return ""
	}
	blocks := data.Message.Content
	if blocks == nil {
		blocks = data.Content
	}
	var text strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	return text.String()
}

// DSH's native delete action archives the session, preserving its durable log.
func (a *Agent) DeleteSession(ctx context.Context, id string) error {
	if a.client.token == "" {
		return core.ErrNotSupported
	}
	if !a.ValidateSessionID(ctx, id) {
		return fmt.Errorf("dsh: session does not belong to this workspace")
	}
	return a.client.remoteInvoke(ctx, "workspace/archiveSession", map[string]any{"request": map[string]any{"sessionId": id}}, nil)
}
func (a *Agent) SetPlatformPrompt(prompt string) {
	a.mu.Lock()
	a.platformPrompt = prompt
	a.mu.Unlock()
}
func (a *Agent) HasSystemPromptSupport() bool { return true }
func (a *Agent) CompressCommand() string {
	if a.client.token != "" {
		return "/compact"
	}
	return ""
}
func (a *Agent) SkillDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dshHome := os.Getenv("DSH_HOME")
	if dshHome == "" {
		dshHome = filepath.Join(home, ".dsh")
	}
	return []string{filepath.Join(a.absWorkDir(), ".dsh", "skills"), filepath.Join(a.absWorkDir(), ".agents", "skills"), filepath.Join(dshHome, "skills"), filepath.Join(home, ".agents", "skills")}
}

func (s *dshSession) GetContextUsage() *core.ContextUsage {
	if s.client.token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	var view struct {
		Values struct {
			ContextPressure struct {
				PressureTokens, ProjectedTokens *int
				ContextWindow                   int
			} `json:"contextPressure"`
			TokenUsage struct{ UncachedInputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int } `json:"tokenUsage"`
		} `json:"values"`
	}
	if err := s.client.remoteInvoke(ctx, "session/projections", map[string]any{"request": map[string]any{"sessionId": s.sessionID}}, &view); err != nil {
		return nil
	}
	pressure := view.Values.ContextPressure
	usage := view.Values.TokenUsage
	if pressure.PressureTokens == nil && pressure.ProjectedTokens == nil {
		return nil
	}
	used := 0
	if pressure.PressureTokens != nil {
		used = *pressure.PressureTokens
	}
	if pressure.ProjectedTokens != nil {
		used = *pressure.ProjectedTokens
	}
	return &core.ContextUsage{UsedTokens: used, ContextWindow: pressure.ContextWindow, InputTokens: usage.UncachedInputTokens, CachedInputTokens: usage.CacheReadTokens, CacheCreationInputTokens: usage.CacheWriteTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.UncachedInputTokens + usage.CacheReadTokens + usage.CacheWriteTokens + usage.OutputTokens}
}

// Native commands settle outside turns. Run them asynchronously so a command
// that needs approval can be answered while its HTTP call is still pending.
func (s *dshSession) sendNativeCommand(line string) error {
	var commands []struct {
		Name string `json:"name"`
	}
	if err := s.client.remoteInvoke(s.ctx, "commands/list", map[string]any{"agentId": s.sessionID}, &commands); err != nil {
		return err
	}
	name := strings.TrimPrefix(strings.Fields(line)[0], "/")
	found := false
	for _, command := range commands {
		if command.Name == name {
			found = true
			break
		}
	}
	if !found {
		return core.ErrNotSupported
	}
	ctx, cancel := context.WithCancel(s.ctx)
	if s.timeout > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(s.ctx, s.timeout)
	}
	s.turnMu.Lock()
	if s.commandCancel != nil {
		s.turnMu.Unlock()
		cancel()
		return fmt.Errorf("dsh: a native command is already running")
	}
	s.commandCancel = cancel
	s.turnMu.Unlock()
	go func() {
		defer cancel()
		var execution *struct {
			Result struct{ Kind, Text string } `json:"result"`
		}
		err := s.client.remoteInvoke(ctx, "commands/execute", map[string]any{"agentId": s.sessionID, "line": line, "submittedAttachments": []any{}}, &execution)
		if err == nil && execution == nil {
			err = fmt.Errorf("dsh: native command disappeared")
		}
		if err == nil {
			if execution.Result.Kind == "error" {
				err = fmt.Errorf("dsh: %s", execution.Result.Text)
			} else if execution.Result.Text != "" {
				s.emit(core.Event{Type: core.EventText, Content: execution.Result.Text})
			}
		}
		s.turnMu.Lock()
		s.commandCancel = nil
		s.turnMu.Unlock()
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		if err != nil {
			s.emit(core.Event{Type: core.EventError, Error: err})
		}
		s.emit(core.Event{Type: core.EventResult, SessionID: s.sessionID, Done: true, Error: err})
	}()
	return nil
}

var (
	_ core.HistoryProvider        = (*Agent)(nil)
	_ core.SessionDeleter         = (*Agent)(nil)
	_ core.ContextCompressor      = (*Agent)(nil)
	_ core.SkillProvider          = (*Agent)(nil)
	_ core.PlatformPromptInjector = (*Agent)(nil)
	_ core.SystemPromptSupporter  = (*Agent)(nil)
	_ core.ContextUsageReporter   = (*dshSession)(nil)
)
