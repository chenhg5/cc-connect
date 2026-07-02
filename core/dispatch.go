package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	dispatchStateDispatched  = "dispatched"
	dispatchStateResultReady = "result_ready"
)

var dispatchLetterRe = regexp.MustCompile(`^L-\d{4,}$`)

// DispatchConfig enables strict [DISPATCH] interception for one source engine.
type DispatchConfig struct {
	Enabled             bool
	SourceProject       string
	DashboardSessionKey string
	PollInterval        time.Duration
}

type dispatchRequest struct {
	To     string
	Letter string
	Thread string
	Path   string
}

type DispatchExpectation struct {
	Letter              string    `json:"letter"`
	Thread              string    `json:"thread"`
	To                  string    `json:"to"`
	Path                string    `json:"path"`
	ResultPath          string    `json:"result_path"`
	IndexPath           string    `json:"index_path"`
	TopicID             string    `json:"topic_id,omitempty"`
	TopicName           string    `json:"topic_name,omitempty"`
	TopicSessionKey     string    `json:"topic_session_key,omitempty"`
	SourceProject       string    `json:"source_project"`
	SourcePlatform      string    `json:"source_platform"`
	SourceSessionKey    string    `json:"source_session_key"`
	DashboardSessionKey string    `json:"dashboard_session_key"`
	DispatchedAt        time.Time `json:"dispatched_at"`
	ResultReadyAt       time.Time `json:"result_ready_at,omitempty"`
	State               string    `json:"state"`
}

type dispatchLedger struct {
	Expectations []DispatchExpectation `json:"expectations"`
}

type dispatchStore struct {
	mu   sync.Mutex
	path string
}

func newDispatchStore(dataDir string) *dispatchStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &dispatchStore{path: filepath.Join(dataDir, "dispatch_expectations.json")}
}

func (s *dispatchStore) upsert(exp DispatchExpectation) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ledger, err := s.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range ledger.Expectations {
		cur := &ledger.Expectations[i]
		if cur.Letter == exp.Letter && cur.Thread == exp.Thread && cur.To == exp.To {
			if cur.State == dispatchStateResultReady {
				exp.State = cur.State
				exp.ResultReadyAt = cur.ResultReadyAt
			}
			ledger.Expectations[i] = exp
			replaced = true
			break
		}
	}
	if !replaced {
		ledger.Expectations = append(ledger.Expectations, exp)
	}
	return s.saveLocked(ledger)
}

func (s *dispatchStore) listOpen() ([]DispatchExpectation, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	var out []DispatchExpectation
	for _, exp := range ledger.Expectations {
		if exp.State == dispatchStateDispatched {
			out = append(out, exp)
		}
	}
	return out, nil
}

func (s *dispatchStore) markResultReady(letter, thread, to string, when time.Time) (DispatchExpectation, bool, error) {
	var zero DispatchExpectation
	if s == nil {
		return zero, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return zero, false, err
	}
	for i := range ledger.Expectations {
		exp := &ledger.Expectations[i]
		if exp.Letter == letter && exp.Thread == thread && exp.To == to {
			if exp.State == dispatchStateResultReady {
				return *exp, false, nil
			}
			exp.State = dispatchStateResultReady
			exp.ResultReadyAt = when
			if err := s.saveLocked(ledger); err != nil {
				return zero, false, err
			}
			return *exp, true, nil
		}
	}
	return zero, false, nil
}

func (s *dispatchStore) loadLocked() (dispatchLedger, error) {
	var ledger dispatchLedger
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ledger, nil
		}
		return ledger, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return ledger, nil
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, err
	}
	return ledger, nil
}

func (s *dispatchStore) saveLocked(ledger dispatchLedger) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(s.path, data, 0o644)
}

func parseDispatchBlock(content string) (dispatchRequest, bool, error) {
	var req dispatchRequest
	text := strings.TrimSpace(content)
	if text == "" {
		return req, false, nil
	}
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(lines[0]) != "[DISPATCH]" {
		return req, false, nil
	}
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return req, true, fmt.Errorf("invalid dispatch line %q", line)
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "to":
			req.To = value
		case "letter":
			req.Letter = value
		case "thread":
			req.Thread = value
		case "path":
			req.Path = value
		default:
			return req, true, fmt.Errorf("unknown dispatch field %q", key)
		}
	}
	if req.To == "" || req.Letter == "" || req.Thread == "" || req.Path == "" {
		return req, true, fmt.Errorf("dispatch requires To, Letter, Thread, and Path")
	}
	if !dispatchLetterRe.MatchString(req.Letter) {
		return req, true, fmt.Errorf("invalid Letter %q", req.Letter)
	}
	return req, true, nil
}

func validateDispatchArchive(req dispatchRequest) (resultPath, indexPath string, err error) {
	info, err := os.Stat(req.Path)
	if err != nil {
		return "", "", fmt.Errorf("letter path: %w", err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("letter path is a directory")
	}
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return "", "", fmt.Errorf("read letter: %w", err)
	}
	headers := parseArchiveFrontMatter(string(data))
	if headers["ID"] != req.Letter {
		return "", "", fmt.Errorf("letter header ID=%q does not match %s", headers["ID"], req.Letter)
	}
	if headers["Thread"] != req.Thread {
		return "", "", fmt.Errorf("letter header Thread=%q does not match %s", headers["Thread"], req.Thread)
	}
	if headers["Type"] != "QUERY" {
		return "", "", fmt.Errorf("letter Type=%q is not QUERY", headers["Type"])
	}

	resultPath = strings.TrimSuffix(req.Path, ".query.md") + ".result.md"
	if resultPath == req.Path {
		resultPath = filepath.Join(filepath.Dir(req.Path), req.Letter+".result.md")
	}
	archiveRoot := archiveRootFromLetterPath(req.Path)
	if archiveRoot != "" {
		indexPath = filepath.Join(archiveRoot, "INDEX.md")
	}
	return resultPath, indexPath, nil
}

func parseArchiveFrontMatter(text string) map[string]string {
	headers := map[string]string{}
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return headers
	}
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return headers
}

func archiveRootFromLetterPath(path string) string {
	clean := filepath.Clean(path)
	dir := filepath.Dir(clean)
	threadParent := filepath.Dir(dir)
	if strings.EqualFold(filepath.Base(threadParent), "threads") {
		return filepath.Dir(threadParent)
	}
	return ""
}

func indexHasResultRow(indexPath, letter, thread string) bool {
	if strings.TrimSpace(indexPath) == "" {
		return false
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return false
	}
	for _, raw := range strings.Split(string(data), "\n") {
		fields := strings.Split(raw, "|")
		if len(fields) < 5 {
			continue
		}
		if strings.TrimSpace(fields[1]) == letter &&
			strings.TrimSpace(fields[2]) == "RESULT" &&
			strings.TrimSpace(fields[3]) == thread {
			return true
		}
	}
	return false
}

func dispatchResultReady(exp DispatchExpectation) bool {
	if _, err := os.Stat(exp.ResultPath); err != nil {
		return false
	}
	return indexHasResultRow(exp.IndexPath, exp.Letter, exp.Thread)
}

func (e *Engine) configureDispatch(cfg DispatchConfig) {
	if strings.TrimSpace(cfg.SourceProject) == "" {
		cfg.SourceProject = e.name
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 60 * time.Second
	}
	e.dispatchConfig = cfg
	if cfg.Enabled {
		e.ensureDispatchStore()
		e.startDispatchWatcher()
	}
}

func (e *Engine) ensureDispatchStore() *dispatchStore {
	if e.dispatchStore != nil {
		return e.dispatchStore
	}
	e.dispatchStore = newDispatchStore(e.dataDir)
	return e.dispatchStore
}

func (e *Engine) maybeHandleDispatchBlock(p Platform, sourceSessionKey, fullResponse string) (handled bool, replacement string) {
	req, ok, err := parseDispatchBlock(fullResponse)
	if !ok {
		return false, ""
	}
	if err != nil {
		return true, "⚠️ Dispatch rejected: " + err.Error()
	}
	if !e.dispatchConfig.Enabled || e.name != e.dispatchConfig.SourceProject {
		return true, fmt.Sprintf("⚠️ Dispatch rejected: project %s is not allowed to emit [DISPATCH].", e.name)
	}
	receipt, err := e.executeDispatch(p, sourceSessionKey, req)
	if err != nil {
		slog.Warn("dispatch rejected", "project", e.name, "letter", req.Letter, "to", req.To, "error", err)
		return true, "⚠️ Dispatch rejected: " + err.Error()
	}
	return true, receipt
}

func (e *Engine) executeDispatch(p Platform, sourceSessionKey string, req dispatchRequest) (string, error) {
	if e.relayManager == nil {
		return "", fmt.Errorf("relay manager is not configured")
	}
	target := e.relayManager.Engine(req.To)
	if target == nil {
		return "", fmt.Errorf("target seat %q is not running", req.To)
	}
	resultPath, indexPath, err := validateDispatchArchive(req)
	if err != nil {
		return "", err
	}
	dashboardSessionKey := strings.TrimSpace(e.dispatchConfig.DashboardSessionKey)
	if dashboardSessionKey == "" {
		return "", fmt.Errorf("dispatch dashboard session key is not configured")
	}
	platformName, _, err := parseSessionKeyParts(dashboardSessionKey)
	if err != nil {
		return "", fmt.Errorf("invalid dispatch dashboard session key: %w", err)
	}

	dispatchMessage := buildDispatchMessage(req)
	sessionKey := dashboardSessionKey
	var replyCtx any
	var topicID, topicName string

	if target.UsesWorkspacePattern() {
		creator, ok := p.(TaskTopicCreator)
		if !ok {
			return "", fmt.Errorf("platform %s cannot create task topics", p.Name())
		}
		topic, err := creator.CreateTaskTopic(e.ctx, dashboardSessionKey, "task-new", "Task intake from [DISPATCH]:\n\n"+dispatchMessage)
		if err != nil {
			return "", fmt.Errorf("create task topic: %w", err)
		}
		if topic == nil || strings.TrimSpace(topic.SessionKey) == "" {
			return "", fmt.Errorf("create task topic returned no session key")
		}
		sessionKey = topic.SessionKey
		replyCtx = topic.ReplyCtx
		topicID = topic.ThreadID
		topicName = topic.Name
	}

	if replyCtx == nil {
		targetPlatform := target.platformForName(platformName)
		if rc, ok := targetPlatform.(ReplyContextReconstructor); ok {
			if reconstructed, err := rc.ReconstructReplyCtx(sessionKey); err == nil {
				replyCtx = reconstructed
			}
		}
	}
	if replyCtx == nil {
		replyCtx = sessionKey
	}

	target.dispatchSyntheticMessage(platformName, sessionKey, replyCtx, dispatchMessage)

	exp := DispatchExpectation{
		Letter:              req.Letter,
		Thread:              req.Thread,
		To:                  req.To,
		Path:                req.Path,
		ResultPath:          resultPath,
		IndexPath:           indexPath,
		TopicID:             topicID,
		TopicName:           topicName,
		TopicSessionKey:     sessionKey,
		SourceProject:       e.name,
		SourcePlatform:      platformName,
		SourceSessionKey:    sourceSessionKey,
		DashboardSessionKey: dashboardSessionKey,
		DispatchedAt:        time.Now(),
		State:               dispatchStateDispatched,
	}
	if err := e.ensureDispatchStore().upsert(exp); err != nil {
		return "", fmt.Errorf("write dispatch ledger: %w", err)
	}

	if topicID != "" {
		return fmt.Sprintf("✅ Dispatched %s to %s in Topic #%s", req.Letter, req.To, topicID), nil
	}
	return fmt.Sprintf("✅ Dispatched %s to %s", req.Letter, req.To), nil
}

func buildDispatchMessage(req dispatchRequest) string {
	return fmt.Sprintf("Dispatch letter %s.\n\nThread: %s\nPath: %s\n\nRead the QUERY, execute the requested work, then write %s.result.md and append the RESULT row to INDEX.md.", req.Letter, req.Thread, req.Path, req.Letter)
}

func (e *Engine) dispatchSyntheticMessage(platformName, sessionKey string, replyCtx any, content string) {
	var platform Platform
	for _, p := range e.platforms {
		if p.Name() == platformName {
			platform = p
			break
		}
	}
	if platform == nil {
		slog.Warn("dispatch: target platform not found", "project", e.name, "platform", platformName)
		return
	}
	msg := &Message{
		Platform:          platformName,
		SessionKey:        sessionKey,
		Content:           content,
		UserID:            "cc-connect-dispatch",
		UserName:          "cc-connect dispatch",
		ReplyCtx:          replyCtx,
		WasMentioned:      true,
		UserMessageTimeMs: time.Now().UnixMilli(),
	}
	go e.handleMessage(platform, msg)
}

func (e *Engine) startDispatchWatcher() {
	if e.dispatchWatcherStarted {
		return
	}
	e.dispatchWatcherStarted = true
	go e.runDispatchWatcher()
}

func (e *Engine) runDispatchWatcher() {
	ticker := time.NewTicker(e.dispatchConfig.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.checkDispatchResults()
		}
	}
}

func (e *Engine) checkDispatchResults() {
	store := e.ensureDispatchStore()
	if store == nil {
		return
	}
	open, err := store.listOpen()
	if err != nil {
		slog.Warn("dispatch: failed to load ledger", "error", err)
		return
	}
	for _, exp := range open {
		if !dispatchResultReady(exp) {
			continue
		}
		updated, changed, err := store.markResultReady(exp.Letter, exp.Thread, exp.To, time.Now())
		if err != nil {
			slog.Warn("dispatch: failed to mark result ready", "letter", exp.Letter, "error", err)
			continue
		}
		if !changed {
			continue
		}
		e.notifyDispatchResultReady(updated)
	}
}

func (e *Engine) notifyDispatchResultReady(exp DispatchExpectation) {
	content := fmt.Sprintf("[RESULT_READY]\nLetter: %s\nThread: %s\nPath: %s", exp.Letter, exp.Thread, exp.ResultPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.InjectSyntheticMessage(ctx, exp.SourcePlatform, exp.SourceSessionKey, "cc-connect-dispatch", "cc-connect dispatch", content); err != nil {
		slog.Warn("dispatch: failed to inject RESULT_READY", "letter", exp.Letter, "error", err)
	}
	if exp.DashboardSessionKey != "" {
		for _, p := range e.platforms {
			if p.Name() != exp.SourcePlatform {
				continue
			}
			replyCtx := any(exp.DashboardSessionKey)
			if rc, ok := p.(ReplyContextReconstructor); ok {
				if reconstructed, err := rc.ReconstructReplyCtx(exp.DashboardSessionKey); err == nil {
					replyCtx = reconstructed
				}
			}
			_ = p.Send(ctx, replyCtx, fmt.Sprintf("✅ RESULT received for %s from %s", exp.Letter, exp.To))
			return
		}
	}
}

func (e *Engine) UsesWorkspacePattern() bool {
	return strings.TrimSpace(e.workspacePattern) != ""
}

func (e *Engine) InjectSyntheticMessage(ctx context.Context, platformName, sessionKey, userID, userName, content string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if strings.TrimSpace(sessionKey) == "" {
		return fmt.Errorf("synthetic message: empty session key")
	}
	var platform Platform
	for _, p := range e.platforms {
		if p.Name() == platformName {
			platform = p
			break
		}
	}
	if platform == nil {
		return fmt.Errorf("synthetic message: platform %q not found", platformName)
	}
	replyCtx := any(sessionKey)
	if rc, ok := platform.(ReplyContextReconstructor); ok {
		if reconstructed, err := rc.ReconstructReplyCtx(sessionKey); err == nil {
			replyCtx = reconstructed
		} else {
			slog.Warn("synthetic message: failed to reconstruct reply ctx; using session key fallback",
				"project", e.name,
				"platform", platformName,
				"session", sessionKey,
				"error", err,
			)
		}
	}
	msg := &Message{
		Platform:          platformName,
		SessionKey:        sessionKey,
		Content:           content,
		UserID:            userID,
		UserName:          userName,
		ReplyCtx:          replyCtx,
		UserMessageTimeMs: time.Now().UnixMilli(),
	}
	go e.handleMessage(platform, msg)
	return nil
}
