package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	// DynamicDashboard, when true, routes the [DISPATCH] receipt to the
	// session that emitted [DISPATCH] instead of the static DashboardSessionKey,
	// but only when that session
	// is a private (DM) Telegram chat. Group/Topic sessions always use the
	// static DashboardSessionKey regardless of this flag, so default fleet
	// behavior (General group dispatch) is unaffected (L-0429).
	DynamicDashboard bool
	PollInterval     time.Duration
}

type dispatchRequest struct {
	To     string
	Letter string
	Thread string
	Path   string
}

type DispatchExpectation struct {
	Letter          string `json:"letter"`
	Thread          string `json:"thread"`
	To              string `json:"to"`
	Path            string `json:"path"`
	ResultPath      string `json:"result_path"`
	IndexPath       string `json:"index_path"`
	TopicID         string `json:"topic_id,omitempty"`
	TopicName       string `json:"topic_name,omitempty"`
	TopicSessionKey string `json:"topic_session_key,omitempty"`
	// BaseRepo is the git base repository the target seat's dynamic worktree is
	// created from, resolved by the Secretary from ROUTING.md and carried in the
	// letter front-matter (Base-Repo:). Empty means fall back to the seat's
	// static work_dir. Enables one seat to serve multiple products/repos (L-0422).
	BaseRepo         string `json:"base_repo,omitempty"`
	SourceProject    string `json:"source_project"`
	SourcePlatform   string `json:"source_platform"`
	SourceSessionKey string `json:"source_session_key"`
	// ResultAgentSessionID and SourceSessionPath identify the target agent
	// session that produced this RESULT. They are set by the target engine,
	// never inferred by the notification watcher.
	ResultAgentSessionID string    `json:"result_agent_session_id,omitempty"`
	SourceSessionPath    string    `json:"source_session_path,omitempty"`
	DashboardSessionKey  string    `json:"dashboard_session_key"`
	DispatchedAt         time.Time `json:"dispatched_at"`
	ResultReadyAt        time.Time `json:"result_ready_at,omitempty"`
	State                string    `json:"state"`
}

func (s *dispatchStore) recordResultProvenance(letter, target, agentSessionID, transcriptPath string) error {
	if s == nil || letter == "" || target == "" || agentSessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range ledger.Expectations {
		exp := &ledger.Expectations[i]
		if exp.Letter != letter || !strings.EqualFold(exp.To, target) {
			continue
		}
		exp.ResultAgentSessionID = agentSessionID
		exp.SourceSessionPath = transcriptPath
		return s.saveLocked(ledger)
	}
	return nil
}

func (s *dispatchStore) resultProvenance(letter string) (agentSessionID, transcriptPath string) {
	if s == nil || letter == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return "", ""
	}
	for _, exp := range ledger.Expectations {
		if exp.Letter == letter && exp.ResultAgentSessionID != "" {
			return exp.ResultAgentSessionID, exp.SourceSessionPath
		}
	}
	return "", ""
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
	if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			return req, false, nil
		}
		lines = lines[1 : len(lines)-1]
	}
	if len(lines) != 5 || strings.TrimSpace(lines[0]) != "[DISPATCH]" {
		return req, false, nil
	}

	for i, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return req, false, nil
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		wantKey := []string{"to", "letter", "thread", "path"}[i]
		if key != wantKey || value == "" {
			return req, false, nil
		}

		switch key {
		case "to":
			req.To = value
		case "letter":
			req.Letter = value
		case "thread":
			req.Thread = value
		case "path":
			req.Path = value
		}
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

// resolveBaseRepoFromLetter reads a QUERY letter's front-matter and returns the
// Base-Repo path if present and pointing at a git working tree. The Secretary
// resolves ROUTING.md's "Base repo" column into this header, keeping cc-connect
// dumb: it consumes an already-resolved concrete path, never parsing ROUTING.md.
// Returns "" when the header is absent or does not name a git repo, so callers
// fall back to the seat's static work_dir (L-0422).
func resolveBaseRepoFromLetter(letterPath string) string {
	data, err := os.ReadFile(letterPath)
	if err != nil {
		return ""
	}
	baseRepo := strings.TrimSpace(parseArchiveFrontMatter(string(data))["Base-Repo"])
	if baseRepo == "" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(baseRepo, ".git")); err != nil || (!info.IsDir() && info.Size() == 0) {
		slog.Warn("dispatch: Base-Repo header does not name a git repo, ignoring", "letter_path", letterPath, "base_repo", baseRepo)
		return ""
	}
	return baseRepo
}

// baseRepoForLetter returns the resolved BaseRepo recorded for a dispatched
// letter, or "" if no expectation carries one. The target seat's worktree
// creation calls this to pick its base repo per-letter (L-0422).
func (s *dispatchStore) baseRepoForLetter(letter string) string {
	if s == nil || strings.TrimSpace(letter) == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return ""
	}
	for _, exp := range ledger.Expectations {
		if exp.Letter == letter && strings.TrimSpace(exp.BaseRepo) != "" {
			return exp.BaseRepo
		}
	}
	return ""
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

// isPrivateTelegramSessionKey reports whether a Telegram session key
// addresses a private one-to-one (DM) chat rather than a group or
// supergroup. Telegram assigns negative chat IDs to groups/supergroups and
// positive IDs to private chats with users, so the sign of the chat ID
// disambiguates without any platform-specific lookup (L-0429).
func isPrivateTelegramSessionKey(sessionKey string) bool {
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 2 || parts[0] != "telegram" {
		return false
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	return chatID > 0
}

// resolveDashboardSessionKey picks the session that receives the [DISPATCH]
// receipt. By default this is the statically configured DashboardSessionKey
// (typically the General group).
// When DynamicDashboard is enabled and the emitting session is a private
// Telegram chat, dispatch instead routes to that same DM so a Secretary
// conversing one-on-one with Boss keeps the whole exchange in that DM
// instead of leaking into the group (L-0429).
func (e *Engine) resolveDashboardSessionKey(sourceSessionKey string) string {
	static := strings.TrimSpace(e.dispatchConfig.DashboardSessionKey)
	if e.dispatchConfig.DynamicDashboard && isPrivateTelegramSessionKey(sourceSessionKey) {
		return sourceSessionKey
	}
	return static
}

// virtualTopicSessionKey builds a synthetic per-letter session inside
// dashboardSessionKey when the platform cannot create a real topic there —
// either because Telegram private chats have no forum-topic support at all,
// or because CreateTaskTopic failed for some other reason (e.g. missing
// rights in a group). Concurrent letters stay isolated in the engine's
// internal session state even though, for a DM, they render as plain
// messages with no visual thread separation.
func virtualTopicSessionKey(dashboardSessionKey, letter string) (sessionKey, channelKey, topicName string, err error) {
	parts := strings.Split(dashboardSessionKey, ":")
	if len(parts) < 2 || parts[0] != "telegram" {
		return "", "", "", fmt.Errorf("dashboard session key is invalid: %q", dashboardSessionKey)
	}
	rawChatID := parts[1]
	userID := parts[len(parts)-1]

	var letterNum string
	for _, r := range letter {
		if r >= '0' && r <= '9' {
			letterNum += string(r)
		}
	}
	if letterNum == "" {
		letterNum = "9999"
	}

	sessionKey = fmt.Sprintf("telegram:%s:%s:%s", rawChatID, letterNum, userID)
	channelKey = rawChatID + ":" + letterNum
	topicName = "letter-" + letterNum
	return sessionKey, channelKey, topicName, nil
}

func dispatchResultReady(exp DispatchExpectation) bool {
	// Under C' protocol, result.md file creation or update is the primary delivery channel
	// and does not require the INDEX RESULT row.
	info, err := os.Stat(exp.ResultPath)
	if err != nil {
		return false
	}
	return !info.IsDir()
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
	// Map protocol role keys to config project names (B2)
	alias := map[string]string{
		"architect":        "architect-claude",
		"reviewer":         "reviewer-seat",
		"counsel":          "counsel-seat",
		"researcher":       "researcher-seat",
		"security-auditor": "security-auditor-seat",
	}
	if mapped, ok := alias[req.To]; ok {
		req.To = mapped
	}
	target := e.relayManager.Engine(req.To)
	if target == nil {
		return "", fmt.Errorf("target seat %q is not running", req.To)
	}
	resultPath, indexPath, err := validateDispatchArchive(req)
	if err != nil {
		return "", err
	}
	dashboardSessionKey := e.resolveDashboardSessionKey(sourceSessionKey)
	if dashboardSessionKey == "" {
		return "", fmt.Errorf("dispatch dashboard session key is not configured")
	}
	platformName, chatID, err := parseSessionKeyParts(dashboardSessionKey)
	if err != nil {
		return "", fmt.Errorf("invalid dispatch dashboard session key: %w", err)
	}

	dispatchMessage := buildDispatchMessage(req)
	sessionKey := dashboardSessionKey
	var replyCtx any
	var topicID, topicName string
	var channelKey string

	if target.UsesWorkspacePattern() || target.UsesDispatchTopicIsolation() {
		if isPrivateTelegramSessionKey(dashboardSessionKey) {
			// Telegram private chats have no forum-topic support at all, so
			// skip the doomed CreateForumTopic round-trip and go straight to
			// the virtual-topic session-isolation path (L-0429).
			sessionKey, channelKey, topicName, err = virtualTopicSessionKey(dashboardSessionKey, req.Letter)
			if err != nil {
				return "", err
			}
			if rc, ok := p.(ReplyContextReconstructor); ok {
				if reconstructed, rErr := rc.ReconstructReplyCtx(dashboardSessionKey); rErr == nil {
					replyCtx = reconstructed
				}
			}
			if replyCtx == nil {
				replyCtx = dashboardSessionKey
			}
		} else {
			creator, ok := p.(TaskTopicCreator)
			if !ok {
				return "", fmt.Errorf("platform %s cannot create task topics", p.Name())
			}
			topic, err := creator.CreateTaskTopic(e.ctx, dashboardSessionKey, "letter-"+req.Letter, "Letter intake from [DISPATCH]:\n\n"+dispatchMessage)
			if err != nil {
				slog.Warn("failed to create task topic, falling back to virtual topic", "project", e.name, "letter", req.Letter, "error", err)

				sessionKey, channelKey, topicName, err = virtualTopicSessionKey(dashboardSessionKey, req.Letter)
				if err != nil {
					return "", fmt.Errorf("create task topic failed: %w (and dashboard session key is invalid)", err)
				}

				if rc, ok := p.(ReplyContextReconstructor); ok {
					if reconstructed, rErr := rc.ReconstructReplyCtx(dashboardSessionKey); rErr == nil {
						replyCtx = reconstructed
					}
				}
				if replyCtx == nil {
					replyCtx = dashboardSessionKey
				}
			} else {
				if topic == nil || strings.TrimSpace(topic.SessionKey) == "" {
					return "", fmt.Errorf("create task topic returned no session key")
				}
				sessionKey = topic.SessionKey
				replyCtx = topic.ReplyCtx
				topicID = topic.ThreadID
				topicName = topic.Name
				channelKey = chatID + ":" + topicID
			}
		}
	} else {
		channelKey = chatID
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

	// Write dispatch ledger BEFORE dispatching the synthetic message so
	// the target engine's findLetterIDByTopic always finds the mapping
	// (workspace resolution with {{LETTER_ID}} depends on the ledger being
	// populated when the async handleMessage goroutine runs). Fixes L-0275
	// where worktrees were named letter-L-2433 instead of letter-L-0275.
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
		BaseRepo:            resolveBaseRepoFromLetter(req.Path),
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

	target.dispatchSyntheticMessage(platformName, sessionKey, channelKey, replyCtx, dispatchMessage)

	if topicID != "" {
		return fmt.Sprintf("✅ Dispatched %s to %s in Topic #%s", req.Letter, req.To, topicID), nil
	}
	return fmt.Sprintf("✅ Dispatched %s to %s", req.Letter, req.To), nil
}

func buildDispatchMessage(req dispatchRequest) string {
	return fmt.Sprintf("Dispatch letter %s.\n\nThread: %s\nPath: %s\n\nRead the QUERY, execute the requested work, then write %s.result.md (with Status field). Writing the INDEX RESULT row is optional as a delivery radar but is NOT task closure (CLOSED is handled only by Secretary after Boss approval).", req.Letter, req.Thread, req.Path, req.Letter)
}

func (e *Engine) dispatchSyntheticMessage(platformName, sessionKey, channelKey string, replyCtx any, content string) {
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
		ChannelKey:        channelKey,
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
		_, _, err := store.markResultReady(exp.Letter, exp.Thread, exp.To, time.Now())
		if err != nil {
			slog.Warn("dispatch: failed to mark result ready", "letter", exp.Letter, "error", err)
			continue
		}
	}
}

func (e *Engine) UsesWorkspacePattern() bool {
	return strings.TrimSpace(e.workspacePattern) != ""
}

func (e *Engine) UsesDispatchTopicIsolation() bool {
	return e.dispatchTopicIsolation
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
