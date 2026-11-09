package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// antigravitySession manages multi-turn conversations with the Antigravity CLI (agy).
type antigravitySession struct {
	cmd              string
	extraArgs        []string // extra args from cmd, prepended before agy args
	workDir          string
	model            string
	mode             string
	timeout          time.Duration
	extraEnv         []string
	events           chan core.Event
	closeOnce        sync.Once
	chatID           atomic.Value // stores string
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	alive            atomic.Bool
	permissionBridge *agyPermissionBridge
	// bridgeAllowsNonWorkspace is set when the permission bridge is active and
	// the installed agy accepts --settings, so the per-run override is used.
	bridgeAllowsNonWorkspace bool
}

func newAntigravitySession(ctx context.Context, cmd string, extraArgs []string, workDir, model, mode, resumeID string, extraEnv []string, timeout time.Duration) (*antigravitySession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	as := &antigravitySession{
		cmd:       cmd,
		extraArgs: extraArgs,
		workDir:   workDir,
		model:     model,
		mode:      mode,
		timeout:   timeout,
		extraEnv:  extraEnv,
		events:    make(chan core.Event, 64),
		ctx:       sessionCtx,
		cancel:    cancel,
	}
	as.alive.Store(true)

	if mode == "default" {
		bridge, err := newAgyPermissionBridge(sessionCtx, as.events)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("antigravity: initialize permission bridge: %w", err)
		}
		as.permissionBridge = bridge
		as.bridgeAllowsNonWorkspace = agyBridgeCanAllowNonWorkspace(cmd, extraArgs, extraEnv)
	}

	resumeID = strings.TrimSpace(resumeID)
	if resumeID != "" && resumeID != core.ContinueSession {
		if isValidAntigravitySessionID(resumeID) {
			as.chatID.Store(resumeID)
		} else {
			// Dropping the ID starts a fresh conversation; make that diagnosable.
			slog.Warn("antigravitySession: ignoring resume ID with unsupported characters; starting a new conversation", "resume_id", resumeID)
		}
	}

	return as, nil
}

func (as *antigravitySession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !as.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	chatID := as.CurrentSessionID()
	isResume := chatID != ""

	// Capture existing chat logs and workspace session IDs only when starting a fresh session.
	var preEntries map[string]bool
	if !isResume {
		preEntries = captureAntigravityPreEntries(as.workDir)
	}

	// Save images and files into the workspace
	attachDir := filepath.Join(as.workDir, ".cc-connect", "attachments")
	if (len(images) > 0 || len(files) > 0) && os.MkdirAll(attachDir, 0o755) != nil {
		attachDir = os.TempDir()
	}

	var imageRefs []string
	for i, img := range images {
		ext := ".png"
		switch img.MimeType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o600); err == nil {
			imageRefs = append(imageRefs, fpath)
		}
	}

	var fileRefs []string
	for i, f := range files {
		fname := filepath.Base(f.FileName)
		if fname == "" || fname == "." || fname == ".." {
			fname = fmt.Sprintf("file_%d_%d", time.Now().UnixMilli(), i)
		}
		fpath := filepath.Join(attachDir, fname)
		if err := os.WriteFile(fpath, f.Data, 0o600); err == nil {
			fileRefs = append(fileRefs, fpath)
		}
	}

	// Attach image and file references to prompt
	fullPrompt := prompt
	if len(imageRefs) > 0 {
		if fullPrompt == "" {
			fullPrompt = "Please analyze the attached image(s)."
		}
		fullPrompt += "\n\n[Attached images saved at: " + strings.Join(imageRefs, ", ") + "]"
	}
	if len(fileRefs) > 0 {
		if fullPrompt == "" {
			fullPrompt = "Please analyze the attached file(s)."
		}
		fullPrompt += "\n\n[Attached files saved at: " + strings.Join(fileRefs, ", ") + "]"
	}
	agyConfigDir := ""
	if as.permissionBridge != nil {
		agyConfigDir = as.permissionBridge.AgyConfigDir()
	}
	args := as.buildAntigravityArgs(chatID, isResume, as.mode, agyConfigDir, fullPrompt)
	if strings.TrimSpace(as.model) != "" {
		slog.Warn("antigravitySession: model is configured but ignored because agy does not support --model yet", "model", as.model)
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if as.timeout > 0 {
		ctx, cancel = context.WithTimeout(as.ctx, as.timeout)
	} else {
		ctx, cancel = context.WithCancel(as.ctx)
	}

	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()

	slog.Debug("antigravitySession: launching", "resume", isResume, "args", core.RedactArgs(args))
	cmd := exec.CommandContext(ctx, as.cmd, args...)
	cmd.WaitDelay = 1 * time.Second
	cmd.Dir = as.workDir
	env := os.Environ()
	if len(as.extraEnv) > 0 {
		env = core.MergeEnv(env, as.extraEnv)
	}
	if as.permissionBridge != nil {
		env = core.MergeEnv(env, as.permissionBridge.Env())
	}
	cmd.Env = env

	// Keep stdin disconnected: agy --print consumes piped stdin to EOF before
	// processing the prompt, so an open pipe would deadlock the turn.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("antigravitySession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("antigravitySession: start: %w", err)
	}

	started = true
	as.wg.Add(1)
	go func() {
		defer cancel()
		as.readLoop(ctx, cmd, stdout, &stderrBuf, append(imageRefs, fileRefs...), preEntries, time.Now())
	}()

	return nil
}

const agyBridgeSessionSettings = `{"allowNonWorkspaceAccess":true}`

// agyBridgeCanAllowNonWorkspace decides once per session whether the bridge
// may pass the --settings override. It is skipped when the configured cmd
// already sets --settings (a second flag would replace the user's value) or
// when the installed agy does not define the flag (older releases abort
// with "flags provided but not defined"). Without the override agy keeps its
// own confirmation, and a soft-denied action is reported by the parser.
func agyBridgeCanAllowNonWorkspace(cmd string, extraArgs, extraEnv []string) bool {
	if hasAgySettingsFlag(extraArgs) {
		slog.Warn("antigravitySession: cmd already sets --settings; not adding allowNonWorkspaceAccess, so agy may soft-deny access outside the workspace")
		return false
	}
	if !agyCLISupportsFlag(cmd, extraArgs, extraEnv, "--settings") {
		slog.Warn("antigravitySession: installed agy does not support --settings; access outside the workspace will be soft-denied in default mode (upgrade agy to enable approval through cc-connect)")
		return false
	}
	return true
}

var (
	agyHelpCache      sync.Map // command line -> agy --help output (string)
	agyHelpFlagLineRe = regexp.MustCompile(`(?m)^\s+--[A-Za-z]`)
)

// agyCLISupportsFlag reports whether the agy reached through cmd/extraArgs
// lists flag in its --help output. The help text is the source of truth for
// supported flags, so no version comparison is needed. Successful probes are
// cached per command line and environment for the life of the process (an agy
// upgrade is picked up after a restart); failures are not cached and report
// false.
func agyCLISupportsFlag(cmd string, extraArgs, extraEnv []string, flag string) bool {
	key := cmd + "\x00" + strings.Join(extraArgs, "\x00") + "\x00\x00" + strings.Join(extraEnv, "\x00")
	help, ok := agyHelpCache.Load(key)
	if !ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		probe := exec.CommandContext(ctx, cmd, append(append([]string{}, extraArgs...), "--help")...)
		env := os.Environ()
		if len(extraEnv) > 0 {
			env = core.MergeEnv(env, extraEnv)
		}
		probe.Env = env
		out, err := probe.CombinedOutput()
		trimmed := bytes.TrimSpace(out)
		if len(trimmed) == 0 || (err != nil && !agyHelpFlagLineRe.Match(out)) {
			snippet := string(trimmed)
			if r := []rune(snippet); len(r) > 200 {
				snippet = string(r[:200]) + "..."
			}
			slog.Warn("antigravitySession: probing agy --help failed", "cmd", cmd, "error", err, "output", snippet)
			return false
		}
		// agy prints usage and exits 0; keep the output even on a non-zero
		// exit so wrappers that return the CLI's status still work.
		help, _ = agyHelpCache.LoadOrStore(key, string(out))
	}
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(flag) + `(\s|=|$)`)
	return re.MatchString(help.(string))
}

func hasAgySettingsFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--settings" || strings.HasPrefix(arg, "--settings=") {
			return true
		}
	}
	return false
}

func (as *antigravitySession) buildAntigravityArgs(chatID string, isResume bool, mode, agyConfigDir, fullPrompt string) []string {
	// Prepend extra args from cmd so wrappers like "timeout 3600 agy" work.
	// Keep "-p <prompt>" at the very end because agy consumes the immediate next arg.
	args := append([]string{}, as.extraArgs...)
	if agyConfigDir != "" {
		// Antigravity currently names this compatibility flag --gemini_dir.
		args = append(args, "--gemini_dir="+agyConfigDir, "--print-timeout=24h")
		if mode == "default" && as.bridgeAllowsNonWorkspace {
			// The PreToolUse hook already asks the chat user before every tool
			// call. agy's own non-workspace confirmation cannot be answered in
			// print mode and is soft-denied, ending the turn with an empty reply,
			// so leave that decision to the hook for this run only. --settings
			// values are not written to settings.json (verified on agy 1.5.3).
			args = append(args, "--settings="+agyBridgeSessionSettings)
		}
	}
	if isResume {
		args = append(args, "--conversation", chatID)
	}
	args = append(args, "--output-format=stream-json")
	switch mode {
	case "yolo":
		args = append(args, "--dangerously-skip-permissions")
	case "plan":
		args = append(args, "--sandbox")
	}
	args = append(args, "-p", fullPrompt)
	return args
}

func (as *antigravitySession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, tempFiles []string, preEntries map[string]bool, sendStartedAt time.Time) {
	defer as.wg.Done()
	hasResponse := false
	defer func() {
		for _, f := range tempFiles {
			_ = os.Remove(f)
		}

		err := cmd.Wait()

		// Detect conversation ID if this was the first turn of a fresh session.
		// agy may flush the chat file only while the process is exiting, so wait
		// for process reaping before scanning the chat directory.
		if as.CurrentSessionID() == "" {
			var sid string
			for attempt := 0; attempt < 15; attempt++ {
				sid = as.detectNewSessionID(preEntries, sendStartedAt)
				if sid != "" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if sid != "" {
				as.chatID.Store(sid)
				// agy normally streams its conversation ID. Reaching this
				// heuristic means it did not; file-based detection can pick
				// another chat's conversation when several chats share one
				// workspace, so keep it visible in the logs.
				slog.Warn("antigravitySession: conversation ID not streamed by agy; detected from local state", "session_id", sid, "work_dir", as.workDir)
				// Emit an EventText carrying the session ID back to core.
				select {
				case as.events <- core.Event{Type: core.EventText, SessionID: sid}:
				case <-as.ctx.Done():
				}
			}
		}

		sid := as.CurrentSessionID()
		stderrMsg := redactAgyStderr(stderrBuf.String(), cmd.Env)
		var processErr error
		if err != nil {
			if stderrMsg != "" {
				processErr = fmt.Errorf("agy process failed (%w): %s", err, stderrMsg)
			} else {
				processErr = fmt.Errorf("agy process failed: %w", err)
			}
		} else if !hasResponse {
			if stderrMsg != "" {
				processErr = fmt.Errorf("agy returned no response: %s", stderrMsg)
			} else {
				processErr = fmt.Errorf("agy exited successfully without producing a response")
			}
		}
		if processErr != nil {
			slog.Error("antigravitySession: process failed", "error", processErr)
			select {
			case as.events <- core.Event{Type: core.EventError, Error: processErr}:
			case <-as.ctx.Done():
			}
		}

		// Finalize turn.
		select {
		case as.events <- core.Event{Type: core.EventResult, SessionID: sid, Done: true}:
		case <-as.ctx.Done():
		}
	}()

	go func() {
		<-ctx.Done()
		_ = stdout.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	parser := newAgyOutputParser()
	streamFormatDetected := false
	for scanner.Scan() {
		line := scanner.Bytes()
		isStreamEvent, events := parser.parseLine(line)
		if isStreamEvent {
			streamFormatDetected = true
		} else if !streamFormatDetected {
			// Keep compatibility with wrappers or older CLIs that ignore the
			// output-format flag and still write plain text.
			events = []core.Event{{Type: core.EventText, Content: string(line) + "\n"}}
		} else {
			continue
		}
		for _, event := range events {
			if event.SessionID != "" {
				if isValidAntigravitySessionID(event.SessionID) {
					as.chatID.Store(event.SessionID)
				} else {
					slog.Warn("antigravitySession: ignoring streamed conversation ID with unsupported characters", "session_id", event.SessionID)
					event.SessionID = ""
				}
			}
			if event.Type == core.EventText && strings.TrimSpace(event.Content) != "" {
				hasResponse = true
			}
			select {
			case as.events <- event:
			case <-as.ctx.Done():
				return
			}
		}
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "file already closed") {
		slog.Error("antigravitySession: read error", "error", err)
		select {
		case as.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}:
		case <-as.ctx.Done():
		}
	}
}

func redactAgyStderr(stderr string, env []string) string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		key = strings.ToUpper(key)
		if strings.Contains(key, "TOKEN") || strings.Contains(key, "KEY") || strings.Contains(key, "SECRET") || strings.Contains(key, "PASSWORD") {
			stderr = core.RedactToken(stderr, value)
		}
	}
	return strings.TrimSpace(stderr)
}

func captureAntigravityPreEntries(workDir string) map[string]bool {
	preEntries := make(map[string]bool)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return preEntries
	}

	cliRoot := filepath.Join(homeDir, ".gemini", "antigravity-cli")
	if data, err := os.ReadFile(filepath.Join(cliRoot, "cache", "last_conversations.json")); err == nil {
		var lastMap map[string]string
		if json.Unmarshal(data, &lastMap) == nil {
			for ws, rawID := range lastMap {
				id := strings.TrimSpace(rawID)
				if isValidAntigravitySessionID(id) && antigravityWorkspaceMatches(workDir, ws) {
					preEntries[id] = true
				}
			}
		}
	}
	if metaEntries, err := loadAntigravityMetadataEntries(cliRoot); err == nil {
		for id, entry := range metaEntries {
			for _, uri := range entry.Summary.WorkspaceURIs {
				if antigravityWorkspaceMatches(workDir, uri) {
					preEntries[id] = true
					break
				}
			}
		}
	}
	if file, err := os.Open(filepath.Join(cliRoot, "history.jsonl")); err == nil {
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var entry agyHistoryEntry
			if json.Unmarshal(scanner.Bytes(), &entry) == nil {
				id := strings.TrimSpace(entry.ConversationID)
				if isValidAntigravitySessionID(id) && antigravityWorkspaceMatches(workDir, entry.Workspace) {
					preEntries[id] = true
				}
			}
		}
		_ = file.Close()
	}

	slug := antigravityProjectSlug(workDir)
	chatsDir := filepath.Join(homeDir, ".gemini", "tmp", slug, "chats")
	if entries, err := os.ReadDir(chatsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
				preEntries[entry.Name()] = true
			}
		}
	}
	return preEntries
}

func (as *antigravitySession) detectNewSessionID(preEntries map[string]bool, sendStartedAt time.Time) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	type candidate struct {
		sessionID string
		modTime   time.Time
		diff      time.Duration
	}
	makeCandidate := func(id string, mod time.Time) (candidate, bool) {
		if !isValidAntigravitySessionID(id) || preEntries[id] {
			return candidate{}, false
		}
		if !sendStartedAt.IsZero() && !mod.IsZero() && mod.Before(sendStartedAt.Add(-2*time.Second)) {
			return candidate{}, false
		}
		diff := time.Duration(0)
		if !sendStartedAt.IsZero() {
			if mod.IsZero() {
				diff = time.Duration(1<<63 - 1)
			} else if mod.After(sendStartedAt) {
				diff = mod.Sub(sendStartedAt)
			} else {
				diff = sendStartedAt.Sub(mod)
			}
		}
		return candidate{sessionID: id, modTime: mod, diff: diff}, true
	}

	cliRoot := filepath.Join(homeDir, ".gemini", "antigravity-cli")
	internalIDs := make(map[string]bool)
	metaModByID := make(map[string]time.Time)
	var cliCandidates []candidate
	seenCLI := make(map[string]bool)

	// 1. Inspect Antigravity CLI metadata (conversation_summaries.db and conversation_metadata.json) first
	// to collect internal session IDs and workspace candidates.
	if metaEntries, err := loadAntigravityMetadataEntries(cliRoot); err == nil {
		for id, entry := range metaEntries {
			if entry.IsInternal || entry.Summary.Internal {
				internalIDs[id] = true
				continue
			}
			mod := parseAntigravityTimestamp(entry.Summary.UpdatedAt, entry.LastModifiedTime, entry.Summary.CreatedAt)
			if !mod.IsZero() {
				metaModByID[id] = mod
			}
			matchedWorkspace := false
			for _, uri := range entry.Summary.WorkspaceURIs {
				if antigravityWorkspaceMatches(as.workDir, uri) {
					matchedWorkspace = true
					break
				}
			}
			if !matchedWorkspace {
				continue
			}
			if c, ok := makeCandidate(id, mod); ok {
				cliCandidates = append(cliCandidates, c)
				seenCLI[id] = true
			}
		}
	}

	// 2. Check workspace-keyed last_conversations.json (excluding internal or stale sessions).
	lastPath := filepath.Join(cliRoot, "cache", "last_conversations.json")
	if lastInfo, err := os.Stat(lastPath); err == nil {
		lastFileMod := lastInfo.ModTime()
		if sendStartedAt.IsZero() || !lastFileMod.Before(sendStartedAt.Add(-2*time.Second)) {
			if data, err := os.ReadFile(lastPath); err == nil {
				var lastMap map[string]string
				if json.Unmarshal(data, &lastMap) == nil {
					var lastCandidates []candidate
					for ws, rawID := range lastMap {
						id := strings.TrimSpace(rawID)
						if !isValidAntigravitySessionID(id) || preEntries[id] || internalIDs[id] {
							continue
						}
						if !antigravityWorkspaceMatches(as.workDir, ws) {
							continue
						}
						mod := metaModByID[id]
						if mod.IsZero() {
							mod = lastFileMod
						}
						if c, ok := makeCandidate(id, mod); ok {
							lastCandidates = append(lastCandidates, c)
						}
					}
					if len(lastCandidates) > 0 {
						sort.Slice(lastCandidates, func(i, j int) bool {
							if lastCandidates[i].diff == lastCandidates[j].diff {
								if lastCandidates[i].modTime.Equal(lastCandidates[j].modTime) {
									return lastCandidates[i].sessionID < lastCandidates[j].sessionID
								}
								return lastCandidates[i].modTime.After(lastCandidates[j].modTime)
							}
							return lastCandidates[i].diff < lastCandidates[j].diff
						})
						return lastCandidates[0].sessionID
					}
				}
			}
		}
	}

	// 3. Check workspace-scoped entries in history.jsonl.
	historyPath := filepath.Join(cliRoot, "history.jsonl")
	if file, err := os.Open(historyPath); err == nil {
		latestByID := make(map[string]time.Time)
		var order []string
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var entry agyHistoryEntry
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			id := strings.TrimSpace(entry.ConversationID)
			if !isValidAntigravitySessionID(id) || seenCLI[id] || internalIDs[id] || !antigravityWorkspaceMatches(as.workDir, entry.Workspace) {
				continue
			}
			if _, ok := latestByID[id]; !ok {
				order = append(order, id)
			}
			if entry.Timestamp > 0 {
				ts := time.UnixMilli(entry.Timestamp)
				if ts.After(latestByID[id]) {
					latestByID[id] = ts
				}
			}
		}
		_ = file.Close()

		for _, id := range order {
			if c, ok := makeCandidate(id, latestByID[id]); ok {
				cliCandidates = append(cliCandidates, c)
			}
		}
	}

	if len(cliCandidates) > 0 {
		sort.Slice(cliCandidates, func(i, j int) bool {
			if cliCandidates[i].diff == cliCandidates[j].diff {
				if cliCandidates[i].modTime.Equal(cliCandidates[j].modTime) {
					return cliCandidates[i].sessionID < cliCandidates[j].sessionID
				}
				return cliCandidates[i].modTime.After(cliCandidates[j].modTime)
			}
			return cliCandidates[i].diff < cliCandidates[j].diff
		})
		return cliCandidates[0].sessionID
	}

	// 4. Fallback to legacy ~/.gemini/tmp/<slug>/chats/*.jsonl.
	slug := antigravityProjectSlug(as.workDir)
	chatsDir := filepath.Join(homeDir, ".gemini", "tmp", slug, "chats")

	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		return ""
	}

	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if preEntries[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime()
		if !sendStartedAt.IsZero() && mod.Before(sendStartedAt.Add(-2*time.Second)) {
			continue
		}

		fpath := filepath.Join(chatsDir, entry.Name())
		file, err := os.Open(fpath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		if scanner.Scan() {
			var sf struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal([]byte(scanner.Text()), &sf) == nil {
				if c, ok := makeCandidate(sf.SessionID, mod); ok {
					candidates = append(candidates, c)
				}
			}
		}
		_ = file.Close()
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].diff == candidates[j].diff {
			return candidates[i].modTime.After(candidates[j].modTime)
		}
		return candidates[i].diff < candidates[j].diff
	})
	return candidates[0].sessionID
}

func (as *antigravitySession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !as.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	if as.permissionBridge == nil {
		return fmt.Errorf("antigravity: permission responses are only available in default mode")
	}
	return as.permissionBridge.RespondPermission(requestID, result)
}

func (as *antigravitySession) Events() <-chan core.Event {
	return as.events
}

func (as *antigravitySession) CurrentSessionID() string {
	v, _ := as.chatID.Load().(string)
	return v
}

func (as *antigravitySession) Alive() bool {
	return as.alive.Load()
}

func (as *antigravitySession) Close() error {
	as.alive.Store(false)
	as.cancel()
	if as.permissionBridge != nil {
		as.permissionBridge.Close()
	}
	done := make(chan struct{})
	go func() {
		as.wg.Wait()
		as.closeOnce.Do(func() {
			close(as.events)
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("antigravitySession: close timed out")
	}
	return nil
}
