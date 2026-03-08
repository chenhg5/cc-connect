package iflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	"github.com/creack/pty"
)

var (
	sessionIDRe = regexp.MustCompile(`"session-id"\s*:\s*"([^"]+)"`)
	ansiCSIRe   = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSCRe   = regexp.MustCompile(`\x1b\][^\a]*(?:\a|\x1b\\)`)
)

const (
	iflowTurnIdle       = 900 * time.Millisecond
	iflowTranscriptPoll = 200 * time.Millisecond
)

var iflowPendingToolTimeout = 45 * time.Second
var iflowPendingToolTimeoutDefaultMode = 6 * time.Second

// iflowSession manages multi-turn conversations with iFlow CLI.
// Each Send() launches a fresh interactive `iflow -i` process inside a PTY,
// then tails the transcript JSONL to recover structured assistant/tool events.
type iflowSession struct {
	cmd        string
	workDir    string
	model      string
	mode       string
	extraEnv   []string
	events     chan core.Event
	sessionID  atomic.Value // stores string
	sentOnce   atomic.Bool
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	alive      atomic.Bool
	turnActive atomic.Bool
}

type iflowTurn struct {
	cancel         context.CancelFunc
	startedAt      time.Time
	mode           string
	pendingTimeout time.Duration
	sessionDir     string
	transcriptPath string
	offset         int64
	partial        string
	processDone    chan struct{}

	mu             sync.Mutex
	pendingToolIDs map[string]struct{}
	pendingTools   map[string]iflowToolUse
	seenToolIDs    map[string]struct{}
	doneToolIDs    map[string]struct{}
	pendingTimer   *time.Timer
	resultTimer    *time.Timer
	resultText     string
	toolFallback   []string
	awaitingTool   bool
	resultReady    bool
	resultSent     bool
}

type iflowToolUse struct {
	ID    string
	Name  string
	Input any
}

type iflowToolResult struct {
	ID     string
	Output string
}

func newIFlowSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string) (*iflowSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &iflowSession{
		cmd:      cmd,
		workDir:  workDir,
		model:    model,
		mode:     mode,
		extraEnv: extraEnv,
		events:   make(chan core.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,
	}
	s.alive.Store(true)

	if resumeID != "" {
		s.sessionID.Store(resumeID)
		s.sentOnce.Store(true)
	}

	return s, nil
}

func (s *iflowSession) Send(prompt string, images []core.ImageAttachment) error {
	if len(images) > 0 {
		slog.Warn("iflowSession: images are not supported, ignoring")
	}
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	if !s.turnActive.CompareAndSwap(false, true) {
		return fmt.Errorf("iflow session is busy")
	}

	turnCtx, turnCancel := context.WithCancel(s.ctx)
	turn := &iflowTurn{
		cancel:         turnCancel,
		startedAt:      time.Now(),
		mode:           s.mode,
		pendingTimeout: s.pendingToolTimeout(),
		processDone:    make(chan struct{}),
		pendingToolIDs: make(map[string]struct{}),
		pendingTools:   make(map[string]iflowToolUse),
		seenToolIDs:    make(map[string]struct{}),
		doneToolIDs:    make(map[string]struct{}),
	}

	defer func() {
		if !s.turnActive.Load() {
			turnCancel()
		}
	}()

	sessionDir, err := iflowSessionDir(s.workDir)
	if err != nil {
		s.turnActive.Store(false)
		return fmt.Errorf("iflowSession: resolve session dir: %w", err)
	}
	turn.sessionDir = sessionDir

	args := make([]string, 0, 16)
	if s.model != "" {
		args = append(args, "-m", s.model)
	}

	switch s.mode {
	case "yolo":
		args = append(args, "--yolo")
	case "plan":
		args = append(args, "--plan")
	case "auto-edit":
		args = append(args, "--autoEdit")
	default:
		args = append(args, "--default")
	}

	sid := s.CurrentSessionID()
	if sid != "" {
		args = append(args, "-r", sid)
		turn.transcriptPath = filepath.Join(sessionDir, sid+".jsonl")
		turn.offset = fileSize(turn.transcriptPath)
	} else if s.sentOnce.Load() {
		args = append(args, "-c")
	}

	args = append(args, "-i", prompt)
	slog.Debug("iflowSession: launching interactive turn", "resume", sid != "", "args", core.RedactArgs(args))

	cmd := exec.CommandContext(turnCtx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		s.turnActive.Store(false)
		return fmt.Errorf("iflowSession: start pty: %w", err)
	}

	s.sentOnce.Store(true)
	s.wg.Add(1)
	go s.readLoop(turn, cmd, ptmx)
	return nil
}

func (s *iflowSession) readLoop(turn *iflowTurn, cmd *exec.Cmd, ptmx *os.File) {
	defer s.wg.Done()
	defer s.turnActive.Store(false)
	defer turn.cancel()
	defer ptmx.Close()

	var termBuf bytes.Buffer
	drainDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&termBuf, ptmx)
		close(drainDone)
	}()

	watchDone := make(chan struct{})
	go func() {
		s.watchTranscript(turn)
		close(watchDone)
	}()

	waitErr := cmd.Wait()
	close(turn.processDone)
	_ = ptmx.Close()
	<-drainDone
	<-watchDone
	turn.stopResultTimer()

	termText := strings.TrimSpace(stripANSI(termBuf.String()))
	if turn.readyForResult() {
		turn.markResultSent()
		s.emitEvent(core.Event{
			Type:      core.EventResult,
			Content:   turn.finalContent(),
			SessionID: s.CurrentSessionID(),
			Done:      true,
		})
		return
	}

	if turn.resultWasSent() {
		return
	}

	if waitErr != nil {
		if s.ctx.Err() != nil || errors.Is(waitErr, context.Canceled) {
			return
		}
		s.emitEvent(core.Event{Type: core.EventError, Error: summarizeIFlowError(termText, waitErr)})
		return
	}

	if termText != "" && isIFlowAPIFailure(termText) {
		s.emitEvent(core.Event{Type: core.EventError, Error: summarizeIFlowError(termText, nil)})
		return
	}

	s.emitEvent(core.Event{
		Type:      core.EventResult,
		Content:   turn.finalContent(),
		SessionID: s.CurrentSessionID(),
		Done:      true,
	})
}

func (s *iflowSession) watchTranscript(turn *iflowTurn) {
	ticker := time.NewTicker(iflowTranscriptPoll)
	defer ticker.Stop()

	processDone := false

	for {
		if turn.transcriptPath == "" {
			if path := findIFlowTranscriptPath(turn.sessionDir, turn.startedAt); path != "" {
				turn.transcriptPath = path
				turn.offset = 0
			}
		}

		if turn.transcriptPath != "" {
			if err := s.consumeTranscript(turn); err != nil {
				slog.Debug("iflowSession: transcript poll failed", "path", turn.transcriptPath, "error", err)
			}
		}

		if turn.resultWasSent() || processDone {
			return
		}

		select {
		case <-s.ctx.Done():
			return
		case <-turn.processDone:
			processDone = true
		case <-ticker.C:
		}
	}
}

func (s *iflowSession) pendingToolTimeout() time.Duration {
	if strings.EqualFold(s.mode, "default") {
		return iflowPendingToolTimeoutDefaultMode
	}
	return iflowPendingToolTimeout
}

func (s *iflowSession) consumeTranscript(turn *iflowTurn) error {
	f, err := os.Open(turn.transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() < turn.offset {
		turn.offset = fi.Size()
		turn.partial = ""
	}
	if _, err := f.Seek(turn.offset, io.SeekStart); err != nil {
		return err
	}

	chunk, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if len(chunk) == 0 {
		return nil
	}
	turn.offset += int64(len(chunk))

	data := turn.partial + string(chunk)
	lines := strings.Split(data, "\n")
	if !strings.HasSuffix(data, "\n") {
		turn.partial = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	} else {
		turn.partial = ""
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		s.handleTranscriptLine(turn, line)
	}
	return nil
}

func (s *iflowSession) handleTranscriptLine(turn *iflowTurn, line string) {
	var item iflowTranscriptLine
	if err := json.Unmarshal([]byte(line), &item); err != nil {
		slog.Debug("iflowSession: invalid transcript line", "error", err)
		return
	}

	if item.SessionID != "" {
		s.sessionID.Store(item.SessionID)
	}

	switch item.Type {
	case "assistant":
		texts, toolUses := extractIFlowAssistantEvents(item.Message.Content)
		if len(toolUses) > 0 {
			newTools := turn.addPendingTools(toolUses)
			for _, tool := range newTools {
				s.emitEvent(core.Event{
					Type:      core.EventToolUse,
					ToolName:  tool.Name,
					ToolInput: summarizeIFlowToolInput(tool.Input),
				})
			}
		}
		for _, text := range texts {
			turn.appendText(text)
			s.emitEvent(core.Event{Type: core.EventText, Content: text, SessionID: s.CurrentSessionID()})
		}
		if len(texts) > 0 && !turn.hasPendingTools() {
			turn.scheduleResult(s)
		}

	case "user":
		toolResults := extractIFlowToolResults(item.Message.Content)
		_ = turn.completeTools(toolResults)
	}
}

func iflowSessionDir(workDir string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".iflow", "projects", iflowProjectKey(iflowResolvedWorkDir(workDir))), nil
}

func findIFlowTranscriptPath(sessionDir string, startedAt time.Time) string {
	matches, err := filepath.Glob(filepath.Join(sessionDir, "session-*.jsonl"))
	if err != nil {
		return ""
	}

	cutoff := startedAt.Add(-2 * time.Second)
	var best string
	var bestMod time.Time
	for _, path := range matches {
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() {
			continue
		}
		if fi.ModTime().Before(cutoff) {
			continue
		}
		if best == "" || fi.ModTime().After(bestMod) {
			best = path
			bestMod = fi.ModTime()
		}
	}
	return best
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func extractIFlowAssistantEvents(content any) ([]string, []iflowToolUse) {
	switch v := content.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil, nil
		}
		return []string{text}, nil

	case []any:
		var texts []string
		var tools []iflowToolUse
		for _, raw := range v {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch itemType, _ := item["type"].(string); itemType {
			case "text":
				if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
					texts = append(texts, strings.TrimSpace(text))
				}
			case "tool_use":
				name, _ := item["name"].(string)
				if name == "" {
					continue
				}
				toolID, _ := item["id"].(string)
				tools = append(tools, iflowToolUse{
					ID:    toolID,
					Name:  name,
					Input: item["input"],
				})
			}
		}
		return texts, tools
	}

	return nil, nil
}

func extractIFlowToolResults(content any) []iflowToolResult {
	items, ok := content.([]any)
	if !ok {
		return nil
	}

	var results []iflowToolResult
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if itemType, _ := item["type"].(string); itemType != "tool_result" {
			continue
		}
		if toolID, _ := item["tool_use_id"].(string); toolID != "" {
			results = append(results, iflowToolResult{
				ID:     toolID,
				Output: summarizeIFlowToolResult(item["content"]),
			})
		}
	}
	return results
}

func summarizeIFlowToolInput(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}

	for _, key := range []string{
		"absolute_path",
		"path",
		"file_path",
		"command",
		"query",
		"pattern",
		"prompt",
		"url",
	} {
		if v, _ := m[key].(string); v != "" {
			return truncateRunes(v, 300)
		}
	}

	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return truncateRunes(string(b), 300)
}

func summarizeIFlowToolResult(content any) string {
	m, ok := content.(map[string]any)
	if !ok {
		return ""
	}

	for _, path := range [][]string{
		{"functionResponse", "response", "output"},
		{"responseParts", "functionResponse", "response", "output"},
	} {
		if v := nestedString(m, path...); v != "" {
			return truncateRunes(strings.TrimSpace(v), 2000)
		}
	}

	if v := nestedString(m, "resultDisplay"); v != "" {
		return truncateRunes(strings.TrimSpace(v), 2000)
	}
	if v := nestedString(m, "output"); v != "" {
		return truncateRunes(strings.TrimSpace(v), 2000)
	}

	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return truncateRunes(string(b), 2000)
}

func nestedString(v any, path ...string) string {
	cur := v
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = m[key]
		if !ok {
			return ""
		}
	}
	s, _ := cur.(string)
	return s
}

func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}

func (t *iflowTurn) addPendingTools(tools []iflowToolUse) []iflowToolUse {
	t.mu.Lock()
	defer t.mu.Unlock()

	var added []iflowToolUse
	for _, tool := range tools {
		if tool.ID == "" {
			added = append(added, tool)
			continue
		}
		if _, ok := t.doneToolIDs[tool.ID]; ok {
			continue
		}
		if _, ok := t.seenToolIDs[tool.ID]; ok {
			continue
		}
		t.seenToolIDs[tool.ID] = struct{}{}
		t.pendingToolIDs[tool.ID] = struct{}{}
		t.pendingTools[tool.ID] = tool
		added = append(added, tool)
	}
	if len(added) > 0 {
		timeout := t.pendingTimeout
		if timeout <= 0 {
			timeout = iflowPendingToolTimeout
		}
		if t.pendingTimer != nil {
			t.pendingTimer.Stop()
			t.pendingTimer = nil
		}
		t.pendingTimer = time.AfterFunc(timeout, func() {
			t.mu.Lock()
			if t.resultSent || len(t.pendingToolIDs) == 0 {
				t.pendingTimer = nil
				t.mu.Unlock()
				return
			}

			var names []string
			for _, tool := range t.pendingTools {
				if tool.Name != "" {
					names = append(names, tool.Name)
				}
			}
			msg := "Tool call timed out before completion."
			if len(names) > 0 {
				msg += " Pending: " + strings.Join(names, ", ") + "."
			}
			if strings.EqualFold(t.mode, "default") {
				msg += " Default mode requires interactive approval; use /mode yolo or /mode auto-edit for tool calls."
			} else {
				msg += " The tool appears stalled."
			}

			if t.resultText == "" {
				t.resultText = msg
			} else {
				t.resultText = t.resultText + "\n\n" + msg
			}
			t.awaitingTool = false
			t.resultReady = true
			t.pendingToolIDs = make(map[string]struct{})
			t.pendingTools = make(map[string]iflowToolUse)
			t.pendingTimer = nil
			t.mu.Unlock()

			t.cancel()
		})

		if t.resultTimer != nil {
			t.resultTimer.Stop()
			t.resultTimer = nil
		}
		t.awaitingTool = false
		t.toolFallback = nil
	}
	return added
}

func (t *iflowTurn) appendText(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.awaitingTool = false
	t.resultText += text
}

func (t *iflowTurn) completeTools(results []iflowToolResult) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, result := range results {
		if _, ok := t.doneToolIDs[result.ID]; ok {
			continue
		}
		delete(t.pendingToolIDs, result.ID)
		delete(t.pendingTools, result.ID)
		t.doneToolIDs[result.ID] = struct{}{}
		if result.Output != "" {
			t.toolFallback = append(t.toolFallback, result.Output)
		}
	}
	if len(t.pendingToolIDs) == 0 && t.pendingTimer != nil {
		t.pendingTimer.Stop()
		t.pendingTimer = nil
	}
	if len(results) == 0 || len(t.pendingToolIDs) > 0 {
		return false
	}
	t.awaitingTool = true
	return true
}

func (t *iflowTurn) hasPendingTools() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pendingToolIDs) > 0
}

func (t *iflowTurn) scheduleResult(s *iflowSession) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.resultSent || len(t.pendingToolIDs) > 0 {
		return
	}
	if t.pendingTimer != nil {
		t.pendingTimer.Stop()
		t.pendingTimer = nil
	}
	if t.resultTimer != nil {
		t.resultTimer.Stop()
		t.resultTimer = nil
	}

	t.resultTimer = time.AfterFunc(iflowTurnIdle, func() {
		t.mu.Lock()
		if t.resultSent || len(t.pendingToolIDs) > 0 {
			t.resultTimer = nil
			t.mu.Unlock()
			return
		}
		t.resultReady = true
		t.resultTimer = nil
		t.mu.Unlock()

		t.cancel()
	})
}

func (t *iflowTurn) stopResultTimer() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.resultTimer != nil {
		t.resultTimer.Stop()
		t.resultTimer = nil
	}
	if t.pendingTimer != nil {
		t.pendingTimer.Stop()
		t.pendingTimer = nil
	}
}

func (t *iflowTurn) resultWasSent() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resultSent
}

func (t *iflowTurn) readyForResult() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resultReady
}

func (t *iflowTurn) markResultSent() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resultSent = true
}

func (t *iflowTurn) finalContent() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.awaitingTool || len(t.toolFallback) == 0 {
		return t.resultText
	}

	fallback := strings.Join(t.toolFallback, "\n\n")
	if t.resultText == "" {
		return fallback
	}
	if strings.Contains(t.resultText, fallback) {
		return t.resultText
	}
	return t.resultText + "\n\n" + fallback
}

func (s *iflowSession) emitEvent(evt core.Event) {
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

func readExecutionInfoSessionID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var payload struct {
		SessionID string `json:"session-id"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.SessionID), nil
}

func extractSessionIDFromExecutionInfo(stderrText string) string {
	m := sessionIDRe.FindStringSubmatch(stderrText)
	if len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func stripANSI(s string) string {
	s = ansiOSCRe.ReplaceAllString(s, "")
	s = ansiCSIRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func isIFlowAPIFailure(stderrText string) bool {
	if stderrText == "" {
		return false
	}
	lower := strings.ToLower(stderrText)
	if strings.Contains(lower, "error when talking to iflow api") {
		return true
	}
	if strings.Contains(lower, "generate data error") {
		return true
	}
	if strings.Contains(lower, "retrying with backoff") && strings.Contains(lower, "fetch failed") {
		return true
	}
	return false
}

func summarizeIFlowError(stderrText string, waitErr error) error {
	if stderrText != "" {
		for _, line := range strings.Split(stderrText, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "<Execution Info>") || strings.HasPrefix(line, "</Execution Info>") {
				continue
			}
			if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "}") || strings.HasPrefix(line, "\"") {
				continue
			}
			if utf8.RuneCountInString(line) > 300 {
				line = string([]rune(line)[:300]) + "..."
			}
			return fmt.Errorf("%s", line)
		}
	}
	if waitErr != nil {
		return fmt.Errorf("iflow process failed: %w", waitErr)
	}
	return fmt.Errorf("iflow API request failed")
}

func (s *iflowSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *iflowSession) Events() <-chan core.Event {
	return s.events
}

func (s *iflowSession) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *iflowSession) Alive() bool {
	return s.alive.Load()
}

func (s *iflowSession) Close() error {
	s.alive.Store(false)
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("iflowSession: close timed out, abandoning wg.Wait")
	}
	close(s.events)
	return nil
}
