package opencode

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	_ "modernc.org/sqlite"
)

// ndjsonDumpDir is where raw opencode stdout/stderr is captured for diagnosing
// "(empty response)" and other agent-level issues. One file per `opencode run`
// invocation. Set OPENCODE_NDJSON_DUMP_DIR to override; set to "off" to disable.
func ndjsonDumpDir() string {
	if v := os.Getenv("OPENCODE_NDJSON_DUMP_DIR"); v != "" {
		if v == "off" {
			return ""
		}
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cc-connect", "logs", "opencode")
}

// opencodeSession manages multi-turn conversations with the OpenCode CLI.
// Each Send() launches a new `opencode run --format json` process
// with --session for conversation continuity.
type opencodeSession struct {
	cmd      string
	workDir  string
	model    string
	mode     string
	extraEnv []string
	events   chan core.Event
	chatID   atomic.Value // stores string — OpenCode session ID
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	alive    atomic.Bool
	expectingContinue atomic.Bool // true when compaction_continue received, waiting for next step

	// Per-Send() diagnostic counters; reset at start of each readLoop.
	dumpMu           sync.Mutex
	dumpFile         *os.File // raw NDJSON dump for current run (nil if disabled)
	dumpPath         string   // path of dump file (for logging)
	textEvents       int      // count of EventText emitted in this run
	emptyTexts       int      // count of "type=text" events with empty text
	rawLineCount     int      // total NDJSON lines read in this run
	lastStepFinished bool     // true if the last step_start was matched by a step_finish
	stepStartCount   int      // number of step_start events seen in this run
	runStartTime     time.Time // wall-clock time when current Send() began
}

func newOpencodeSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string) (*opencodeSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	s := &opencodeSession{
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

	if resumeID != "" && resumeID != core.ContinueSession {
		s.chatID.Store(resumeID)
	}

	return s, nil
}

func (s *opencodeSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	prompt, imagePaths, err := s.stageImages(prompt, images)
	if err != nil {
		return err
	}
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	chatID := s.CurrentSessionID()
	isResume := chatID != ""

	args := s.buildRunArgs(prompt, imagePaths, chatID)

	slog.Debug("opencodeSession: launching", "resume", isResume, "args", core.RedactArgs(args))

	cmd := exec.CommandContext(s.ctx, s.cmd, args...)
	cmd.Dir = s.workDir
	env := os.Environ()
	if len(s.extraEnv) > 0 {
		env = core.MergeEnv(env, s.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("opencodeSession: stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("opencodeSession: stderr pipe: %w", err)
	}

	// Reset per-run diagnostics and open NDJSON dump file.
	s.dumpMu.Lock()
	s.textEvents = 0
	s.emptyTexts = 0
	s.rawLineCount = 0
	s.lastStepFinished = false
	s.stepStartCount = 0
	s.runStartTime = time.Now()
	if s.dumpFile != nil {
		_ = s.dumpFile.Close()
		s.dumpFile = nil
		s.dumpPath = ""
	}
	if dir := ndjsonDumpDir(); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr == nil {
			ts := time.Now().Format("20060102-150405.000")
			ts = strings.ReplaceAll(ts, ".", "")
			path := filepath.Join(dir, fmt.Sprintf("run-%s.ndjson", ts))
			if f, ferr := os.Create(path); ferr == nil {
				s.dumpFile = f
				s.dumpPath = path
				slog.Info("opencodeSession: NDJSON dump opened", "path", path, "session_id", chatID, "resume", isResume)
				// Write a header line so the dump self-documents the run.
				header := map[string]any{
					"__cc_connect": "run_start",
					"ts":           time.Now().Format(time.RFC3339Nano),
					"args":         core.RedactArgs(args),
					"resume":       isResume,
					"session_id":   chatID,
					"workdir":      s.workDir,
					"model":        s.model,
				}
				if b, jerr := json.Marshal(header); jerr == nil {
					_, _ = s.dumpFile.Write(append(b, '\n'))
				}
			} else {
				slog.Warn("opencodeSession: failed to create NDJSON dump", "path", path, "error", ferr)
			}
		} else {
			slog.Warn("opencodeSession: failed to create NDJSON dump dir", "dir", dir, "error", mkErr)
		}
	}
	s.dumpMu.Unlock()

	var stderrBuf bytes.Buffer

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opencodeSession: start: %w", err)
	}

	// Stream stderr live: log every line and copy to buffer for end-of-run summary.
	s.wg.Add(1)
	go s.stderrLoop(stderrPipe, &stderrBuf)

	s.wg.Add(1)
	go s.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

// stderrLoop reads opencode stderr line-by-line. To keep cc-connect.log clean,
// every line is mirrored to the per-run NDJSON dump (with "__stderr " prefix),
// but only WARN/ERROR lines are forwarded to slog. opencode's --print-logs at
// DEBUG level is very verbose and would drown cc-connect's own debug logs.
//
// Set OPENCODE_STDERR_TO_LOG=all to forward every stderr line at slog.Debug
// (useful when actively debugging), or =off to suppress entirely.
func (s *opencodeSession) stderrLoop(r io.ReadCloser, buf *bytes.Buffer) {
	defer s.wg.Done()
	defer r.Close()
	mode := strings.ToLower(os.Getenv("OPENCODE_STDERR_TO_LOG")) // "", "all", "off", "warn"
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		// Always mirror to NDJSON dump (per-run, won't pollute global log).
		s.dumpMu.Lock()
		if s.dumpFile != nil {
			_, _ = s.dumpFile.WriteString("__stderr " + line + "\n")
		}
		s.dumpMu.Unlock()
		// Forward to slog only when severity warrants it.
		switch mode {
		case "off":
			// don't forward
		case "all":
			slog.Debug("opencodeSession: stderr", "line", line)
		default:
			// Heuristic: opencode --print-logs prefixes lines with level, e.g.
			//   "WARN  2026-... message"  or  "ERROR ...".
			// Forward only WARN/ERROR/FATAL lines so cc-connect.log stays focused.
			trimmed := strings.TrimLeft(line, " \t")
			upper := strings.ToUpper(trimmed)
			if strings.HasPrefix(upper, "ERROR") || strings.HasPrefix(upper, "FATAL") {
				slog.Error("opencodeSession: stderr", "line", line)
			} else if strings.HasPrefix(upper, "WARN") {
				slog.Warn("opencodeSession: stderr", "line", line)
			}
		}
	}
}

func (s *opencodeSession) stageImages(prompt string, images []core.ImageAttachment) (string, []string, error) {
	if len(images) == 0 {
		return prompt, nil, nil
	}

	imgDir := filepath.Join(s.workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("opencodeSession: create image dir: %w", err)
	}

	imagePaths := make([]string, 0, len(images))
	for i, img := range images {
		ext := opencodeImageExt(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			return "", nil, fmt.Errorf("opencodeSession: save image: %w", err)
		}
		imagePaths = append(imagePaths, fpath)
	}

	if prompt == "" {
		prompt = "Please analyze the attached image(s)."
	}

	return prompt, imagePaths, nil
}

func opencodeImageExt(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func (s *opencodeSession) buildRunArgs(prompt string, imagePaths []string, chatID string) []string {
	args := []string{"run", "--format", "json"}

	if chatID != "" {
		args = append(args, "--session", chatID)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.workDir != "" {
		args = append(args, "--dir", s.workDir)
	}

	// Forward opencode internal logs to stderr (captured in stderrLoop and
	// written to the per-run NDJSON dump). These never reach the IM platform —
	// the readLoop below only treats stderr as a user-facing error when the
	// process actually exits non-zero. Defaults ON at DEBUG to maximize the
	// information available for diagnosing empty responses; override via
	// OPENCODE_PRINT_LOGS=0 (off) or OPENCODE_LOG_LEVEL=DEBUG|INFO|WARN|ERROR.
	if os.Getenv("OPENCODE_PRINT_LOGS") != "0" {
		args = append(args, "--print-logs")
		lvl := strings.ToUpper(strings.TrimSpace(os.Getenv("OPENCODE_LOG_LEVEL")))
		switch lvl {
		case "DEBUG", "INFO", "WARN", "ERROR":
			args = append(args, "--log-level", lvl)
		default:
			args = append(args, "--log-level", "DEBUG")
		}
	}

	// Enable thinking blocks.
	args = append(args, "--thinking")

	for _, imagePath := range imagePaths {
		if imagePath == "" {
			continue
		}
		args = append(args, "--file", imagePath)
	}

	// Use "--" to separate flags from the positional prompt so that
	// --file (yargs [array]) does not greedily consume the prompt text.
	args = append(args, "--", prompt)
	return args
}

func (s *opencodeSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer s.wg.Done()
	// cmd.Wait() must be called exactly once. We do it via this defer so all
	// early-return paths (scanner errors, ctx cancellation) still reap the
	// child. The main exit-code based stderr decision below uses a non-blocking
	// path: if Wait already ran, ProcessState will be populated.
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	}()
	defer func() {
		// Close dump file on exit; record summary line.
		s.dumpMu.Lock()
		if s.dumpFile != nil {
			summary := map[string]any{
				"__cc_connect":   "run_end",
				"ts":             time.Now().Format(time.RFC3339Nano),
				"raw_line_count": s.rawLineCount,
				"text_events":    s.textEvents,
				"empty_texts":    s.emptyTexts,
				"session_id":     s.CurrentSessionID(),
			}
			if b, jerr := json.Marshal(summary); jerr == nil {
				_, _ = s.dumpFile.Write(append(b, '\n'))
			}
			_ = s.dumpFile.Close()
			s.dumpFile = nil
		}
		dumpPath := s.dumpPath
		rawCount := s.rawLineCount
		textCount := s.textEvents
		emptyTexts := s.emptyTexts
		s.dumpMu.Unlock()
		// If a run produced zero text events, warn loudly with dump pointer.
		if textCount == 0 {
			slog.Warn("opencodeSession: run produced ZERO text events (empty response likely)",
				"session_id", s.CurrentSessionID(),
				"raw_lines", rawCount,
				"empty_texts", emptyTexts,
				"ndjson_dump", dumpPath,
			)
		} else if emptyTexts > 0 {
			slog.Warn("opencodeSession: run had empty text parts",
				"session_id", s.CurrentSessionID(),
				"text_events", textCount,
				"empty_texts", emptyTexts,
				"ndjson_dump", dumpPath,
			)
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Mirror raw stdout line to dump file before parsing.
		s.dumpMu.Lock()
		if s.dumpFile != nil && line != "" {
			_, _ = s.dumpFile.WriteString(line + "\n")
		}
		if line != "" {
			s.rawLineCount++
		}
		s.dumpMu.Unlock()

		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("opencodeSession: non-JSON line", "line", line)
			continue
		}

		s.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("opencodeSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
		return
	}

	// Wait for process to exit so we have a real ExitCode before judging stderr.
	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	stderrMsg := stderrBuf.String()
	// Always handle "Session not found" regardless of exit code, since some
	// opencode versions may emit it on stderr without a non-zero exit.
	if stderrMsg != "" && strings.Contains(stderrMsg, "Session not found") {
		s.chatID.Store("")
		slog.Warn("opencodeSession: cleared stale session ID")
	}
	// Only treat as fatal when the process actually failed. A clean exit with
	// noisy stderr (e.g. --print-logs INFO lines) must NOT become a reply.
	if waitErr != nil && exitCode != 0 {
		// Truncate aggressively so a multi-megabyte log dump never reaches IM.
		short := truncate(stderrMsg, 400)
		slog.Error("opencodeSession: process error", "exit_code", exitCode, "stderr", short)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("opencode exited %d: %s", exitCode, short)}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
		}
		return
	}
	if stderrMsg != "" {
		// Process succeeded but produced stderr (verbose logs). Just record it.
		slog.Debug("opencodeSession: process exited cleanly with stderr", "stderr_bytes", len(stderrMsg))
	}

	// Check if we received compaction_continue before readLoop ended.
	// If so, OpenCode will continue with a new turn - do NOT send EventResult.
	// The subsequent process will send its own EventResult when it finishes.
	if s.expectingContinue.Load() {
		slog.Info("opencodeSession: readLoop ended after compaction_continue, skipping EventResult", "session_id", s.CurrentSessionID())
		s.expectingContinue.Store(false)
		return
	}

	// SQLite fallback: opencode 1.15.x has two known failure modes:
	// 1. stdout drop: text/step-finish never arrive on stdout (zeroText=true)
	// 2. mid-run exit: process exits after step_start but before step_finish
	//    (lastStepFinished=false && stepStartCount>0), e.g. caused by the
	//    background npm install failure crashing the process mid-stream.
	// In both cases the full text was already persisted to opencode.db, so we
	// recover from there. For mode 2 we also check that the DB has MORE text
	// than what we already forwarded (to avoid double-sending on clean exit).
	s.dumpMu.Lock()
	zeroText := s.textEvents == 0
	stepStarted := s.stepStartCount > 0
	truncated := stepStarted && !s.lastStepFinished
	runStartMs := s.runStartTime.UnixMilli()
	s.dumpMu.Unlock()
	sid := s.CurrentSessionID()
	needFallback := (zeroText || truncated) && sid != ""
	if needFallback {
		// For truncated runs (process exited mid-stream), only look for messages
		// created after this run started — to avoid re-sending the previous turn.
		var sinceMs int64
		if truncated && !zeroText {
			sinceMs = runStartMs
		}
		if recovered := s.recoverTextFromDB(sid, sinceMs); recovered != "" {
			slog.Warn("opencodeSession: recovered reply from opencode.db after stdout drop",
				"session_id", sid,
				"recovered_len", len(recovered),
				"zero_text", zeroText,
				"truncated", truncated,
			)
			textEvt := core.Event{Type: core.EventText, Content: recovered}
			select {
			case s.events <- textEvt:
			case <-s.ctx.Done():
				return
			}
		} else {
			slog.Warn("opencodeSession: SQLite fallback found no text part either",
				"session_id", sid,
				"zero_text", zeroText,
				"truncated", truncated,
			)
		}
	}

	// Emit EventResult after all steps are done and the process has finished writing.
	slog.Debug("opencodeSession: readLoop complete, sending fallback EventResult", "session_id", sid)
	evt := core.Event{Type: core.EventResult, SessionID: sid, Done: true}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// recoverTextFromDB reads opencode.db (SQLite) and returns the concatenated
// text from all "text" parts of the most recent assistant message in the given
// session. Returns "" on any error or if nothing useful is found.
// sinceMs: if > 0, only consider assistant messages created at or after this
// Unix-millisecond timestamp (used for truncated-run recovery to avoid
// returning the previous turn's message).
func (s *opencodeSession) recoverTextFromDB(sessionID string, sinceMs int64) string {
	dbPath := opencodeDBPath()
	if dbPath == "" {
		return ""
	}
	// Open read-only with WAL-friendly mode so we don't fight the live opencode
	// process (which uses WAL). The "_pragma=query_only=true" + "mode=ro" combo
	// guarantees we never write.
	dsn := "file:" + dbPath + "?mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(true)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		slog.Debug("recoverTextFromDB: open failed", "error", err, "path", dbPath)
		return ""
	}
	defer db.Close()

	// Find latest assistant message for this session.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var msgID string
	var query string
	var args []any
	if sinceMs > 0 {
		query = `
			SELECT id FROM message
			WHERE session_id = ?
			  AND json_extract(data, '$.role') = 'assistant'
			  AND time_created >= ?
			ORDER BY time_created DESC
			LIMIT 1
		`
		args = []any{sessionID, sinceMs}
	} else {
		query = `
			SELECT id FROM message
			WHERE session_id = ?
			  AND json_extract(data, '$.role') = 'assistant'
			ORDER BY time_created DESC
			LIMIT 1
		`
		args = []any{sessionID}
	}
	err = db.QueryRowContext(ctx, query, args...).Scan(&msgID)
	if err != nil {
		slog.Debug("recoverTextFromDB: no assistant message", "error", err, "session_id", sessionID)
		return ""
	}

	rows, err := db.QueryContext(ctx, `
		SELECT data FROM part
		WHERE message_id = ?
		ORDER BY time_created
	`, msgID)
	if err != nil {
		slog.Debug("recoverTextFromDB: query parts failed", "error", err, "message_id", msgID)
		return ""
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var p struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			continue
		}
		if p.Type == "text" && p.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// opencodeDBPath is defined in opencode.go.

// OpenCode NDJSON event structure:
//
//	{ "type": "text|tool_use|reasoning|step_start|step_finish",
//	  "part": { "type": "text|tool|reasoning|step-start|step-finish", ... } }
func (s *opencodeSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "text":
		s.handleText(raw)
	case "tool_use":
		s.handleToolUse(raw)
	case "reasoning":
		s.handleReasoning(raw)
	case "step_start":
		s.handleStepStart(raw)
	case "step_finish":
		s.handleStepFinish(raw)
	case "error":
		s.handleError(raw)
	default:
		b, _ := json.Marshal(raw)
		slog.Debug("opencodeSession: unhandled event", "type", eventType, "raw", string(b))
	}
}

func (s *opencodeSession) handleText(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		slog.Warn("opencodeSession: text event with nil part", "raw", truncate(jsonMustMarshal(raw), 500))
		return
	}
	text, _ := part["text"].(string)

	// Extract metadata and synthetic flags to identify compaction_continue
	metadata, _ := part["metadata"].(map[string]any)
	synthetic, _ := part["synthetic"].(bool)

	// Check for compaction_continue: this is OpenCode's auto-continuation signal.
	// When received, we should NOT send EventText to engine, but mark that we expect
	// a continuation (next step_start will start a new turn without EventResult).
	if synthetic && metadata != nil {
		if cc, ok := metadata["compaction_continue"].(bool); ok && cc {
			slog.Info("opencodeSession: compaction_continue detected, marking expectingContinue", "session_id", s.CurrentSessionID())
			s.expectingContinue.Store(true)
			// Do NOT send EventText - this is internal continuation signal
			return
		}
	}

	if text == "" {
		// Critical diagnostic: empty text part is the prime suspect for "(empty response)".
		s.dumpMu.Lock()
		s.emptyTexts++
		s.dumpMu.Unlock()
		slog.Warn("opencodeSession: text event with EMPTY text",
			"session_id", s.CurrentSessionID(),
			"synthetic", synthetic,
			"has_metadata", metadata != nil,
			"raw", truncate(jsonMustMarshal(raw), 500),
		)
		return
	}

	s.dumpMu.Lock()
	s.textEvents++
	s.dumpMu.Unlock()

	evt := core.Event{Type: core.EventText, Content: text, Metadata: metadata, Synthetic: synthetic}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
		return
	}
}

func jsonMustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<marshal err: %v>", err)
	}
	return string(b)
}

func (s *opencodeSession) handleToolUse(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}

	toolName, _ := part["tool"].(string)

	state, _ := part["state"].(map[string]any)
	status := ""
	if state != nil {
		status, _ = state["status"].(string)
	}

	// Extract tool input summary for display
	input := extractToolInput(state)

	if status == "completed" {
		// OpenCode bundles call + result in one event; emit both for UI.
		useEvt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
		select {
		case s.events <- useEvt:
		case <-s.ctx.Done():
			return
		}

		output, _ := state["output"].(string)
		resultEvt := core.Event{Type: core.EventToolResult, ToolName: toolName, Content: truncate(output, 500)}
		select {
		case s.events <- resultEvt:
		case <-s.ctx.Done():
			return
		}
	} else {
		evt := core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: input}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
	}
}

func extractToolInput(state map[string]any) string {
	if state == nil {
		return ""
	}
	// Prefer title as a concise description (e.g. "List files in current directory")
	if title, ok := state["title"].(string); ok && title != "" {
		return title
	}
	switch input := state["input"].(type) {
	case string:
		return input
	case map[string]any:
		// Use "description" or "command" fields if available
		if desc, ok := input["description"].(string); ok && desc != "" {
			return desc
		}
		if cmd, ok := input["command"].(string); ok && cmd != "" {
			return cmd
		}
		b, _ := json.Marshal(input)
		return truncate(string(b), 200)
	}
	return ""
}

func (s *opencodeSession) handleReasoning(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}
	text, _ := part["text"].(string)
	if text != "" {
		evt := core.Event{Type: core.EventThinking, Content: text}
		select {
		case s.events <- evt:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *opencodeSession) handleError(raw map[string]any) {
	errMsg := extractErrorMessage(raw)
	slog.Error("opencodeSession: agent error", "error", errMsg)
	evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", errMsg)}
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
		return
	}
}

// extractErrorMessage tries to pull a human-readable message from various
// OpenCode error JSON shapes.
func extractErrorMessage(raw map[string]any) string {
	// Shape: {"error": {"data": {"message": "..."}, "name": "..."}}
	if errObj, ok := raw["error"].(map[string]any); ok {
		if data, ok := errObj["data"].(map[string]any); ok {
			if msg, ok := data["message"].(string); ok && msg != "" {
				name, _ := errObj["name"].(string)
				if name != "" {
					return name + ": " + msg
				}
				return msg
			}
		}
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			return msg
		}
		if name, ok := errObj["name"].(string); ok && name != "" {
			return name
		}
	}
	// Shape: {"error": "string message"}
	if errStr, ok := raw["error"].(string); ok && errStr != "" {
		return errStr
	}
	// Shape: {"part": {"error": "...", "message": "..."}}
	if part, ok := raw["part"].(map[string]any); ok {
		if msg, ok := part["error"].(string); ok && msg != "" {
			return msg
		}
		if msg, ok := part["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := raw["message"].(string); ok && msg != "" {
		return msg
	}
	b, _ := json.Marshal(raw)
	return string(b)
}

func (s *opencodeSession) handleStepStart(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	if part == nil {
		return
	}
	sessionID, _ := part["sessionID"].(string)
	if sessionID != "" {
		s.chatID.Store(sessionID)
		slog.Debug("opencodeSession: session started", "session_id", sessionID)
	}
	// Each step_start resets the "last step finished" flag; it will be set
	// again when the matching step_finish arrives. If the process exits before
	// step_finish we know the response was truncated.
	s.dumpMu.Lock()
	s.lastStepFinished = false
	s.stepStartCount++
	s.dumpMu.Unlock()
}

func (s *opencodeSession) handleStepFinish(raw map[string]any) {
	part, _ := raw["part"].(map[string]any)
	reason, _ := part["reason"].(string)
	slog.Debug("opencodeSession: step finished", "reason", reason, "session_id", s.CurrentSessionID())
	s.dumpMu.Lock()
	s.lastStepFinished = true
	s.dumpMu.Unlock()
}

// RespondPermission is a no-op — OpenCode handles permissions internally.
func (s *opencodeSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *opencodeSession) Events() <-chan core.Event {
	return s.events
}

func (s *opencodeSession) CurrentSessionID() string {
	v, _ := s.chatID.Load().(string)
	return v
}

func (s *opencodeSession) Alive() bool {
	return s.alive.Load()
}

func (s *opencodeSession) Close() error {
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
		slog.Warn("opencodeSession: close timed out, abandoning wg.Wait")
	}
	s.dumpMu.Lock()
	if s.dumpFile != nil {
		_ = s.dumpFile.Close()
		s.dumpFile = nil
	}
	s.dumpMu.Unlock()
	close(s.events)
	return nil
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
