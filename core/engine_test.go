package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- stubs for Engine tests ---

type stubAgent struct{}

func (a *stubAgent) Name() string { return "stub" }
func (a *stubAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return &stubAgentSession{}, nil
}
func (a *stubAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *stubAgent) Stop() error                                                { return nil }

type stubAgentSession struct{}

func (s *stubAgentSession) Send(_ string, _ []ImageAttachment, _ []FileAttachment) error { return nil }
func (s *stubAgentSession) RespondPermission(_ string, _ PermissionResult) error         { return nil }
func (s *stubAgentSession) Events() <-chan Event                                         { return make(chan Event) }
func (s *stubAgentSession) CurrentSessionID() string                                     { return "stub-session" }
func (s *stubAgentSession) Alive() bool                                                  { return true }
func (s *stubAgentSession) Close() error                                                 { return nil }

type recordingAgentSession struct {
	stubAgentSession
	lastID     string
	lastResult PermissionResult
	calls      int
}

func (s *recordingAgentSession) RespondPermission(id string, res PermissionResult) error {
	s.lastID = id
	s.lastResult = res
	s.calls++
	return nil
}

type stubPlatformEngine struct {
	n    string
	sent []string
	mu   sync.Mutex
}

func (p *stubPlatformEngine) Name() string               { return p.n }
func (p *stubPlatformEngine) Start(MessageHandler) error { return nil }
func (p *stubPlatformEngine) Reply(_ context.Context, _ any, content string) error {
	p.mu.Lock()
	p.sent = append(p.sent, content)
	p.mu.Unlock()
	return nil
}
func (p *stubPlatformEngine) Send(_ context.Context, _ any, content string) error {
	p.mu.Lock()
	p.sent = append(p.sent, content)
	p.mu.Unlock()
	return nil
}
func (p *stubPlatformEngine) Stop() error { return nil }

func (p *stubPlatformEngine) getSent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]string, len(p.sent))
	copy(cp, p.sent)
	return cp
}

func (p *stubPlatformEngine) clearSent() {
	p.mu.Lock()
	p.sent = nil
	p.mu.Unlock()
}

type stubCronReplyTargetPlatform struct {
	stubPlatformEngine
	reconstructSessionKey string
	resolvedSessionKey    string
	resolveTitle          string
}

func (p *stubCronReplyTargetPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	p.reconstructSessionKey = sessionKey
	return "base-rctx", nil
}

func (p *stubCronReplyTargetPlatform) ResolveCronReplyTarget(sessionKey string, title string) (string, any, error) {
	p.resolvedSessionKey = sessionKey
	p.resolveTitle = title
	return "discord:thread-fresh", "fresh-rctx", nil
}

type resultAgent struct {
	session AgentSession
}

func (a *resultAgent) Name() string { return "stub" }
func (a *resultAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return a.session, nil
}
func (a *resultAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *resultAgent) Stop() error                                                { return nil }

type sessionEnvRecordingAgent struct {
	stubAgent
	session AgentSession
	mu      sync.Mutex
	env     []string
}

func (a *sessionEnvRecordingAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	if a.session != nil {
		return a.session, nil
	}
	return &stubAgentSession{}, nil
}

func (a *sessionEnvRecordingAgent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.env = append([]string(nil), env...)
}

func (a *sessionEnvRecordingAgent) EnvValue(key string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	prefix := key + "="
	for _, entry := range a.env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

type resultAgentSession struct {
	events      chan Event
	result      string
	sendOnce    sync.Once
	sentPrompts []string
}

func newResultAgentSession(result string) *resultAgentSession {
	return &resultAgentSession{
		events: make(chan Event, 1),
		result: result,
	}
}

func (s *resultAgentSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sentPrompts = append(s.sentPrompts, prompt)
	s.sendOnce.Do(func() {
		s.events <- Event{Type: EventResult, Content: s.result, Done: true}
	})
	return nil
}

func (s *resultAgentSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *resultAgentSession) Events() <-chan Event                                 { return s.events }
func (s *resultAgentSession) CurrentSessionID() string                             { return "result-session" }
func (s *resultAgentSession) Alive() bool                                          { return true }
func (s *resultAgentSession) Close() error                                         { return nil }

type stubLifecyclePlatform struct {
	stubPlatformEngine
	handler            PlatformLifecycleHandler
	registerCalls      int
	registeredCommands []BotCommandInfo
	cardNavSetCalls    int
	startCalls         int
	stopCalls          int
}

func (p *stubLifecyclePlatform) Start(MessageHandler) error {
	p.startCalls++
	return nil
}

func (p *stubLifecyclePlatform) Stop() error {
	p.stopCalls++
	return nil
}

func (p *stubLifecyclePlatform) SetLifecycleHandler(h PlatformLifecycleHandler) {
	p.handler = h
}

func (p *stubLifecyclePlatform) RegisterCommands(commands []BotCommandInfo) error {
	p.registerCalls++
	p.registeredCommands = append([]BotCommandInfo(nil), commands...)
	return nil
}

func (p *stubLifecyclePlatform) SetCardNavigationHandler(CardNavigationHandler) {
	p.cardNavSetCalls++
}

type blockingRegisterPlatform struct {
	stubLifecyclePlatform
	registerStarted chan struct{}
	allowRegister   chan struct{}
	stopCalled      chan struct{}
	registerOnce    sync.Once
	stopOnce        sync.Once
}

func newBlockingRegisterPlatform(name string) *blockingRegisterPlatform {
	return &blockingRegisterPlatform{
		stubLifecyclePlatform: stubLifecyclePlatform{
			stubPlatformEngine: stubPlatformEngine{n: name},
		},
		registerStarted: make(chan struct{}),
		allowRegister:   make(chan struct{}),
		stopCalled:      make(chan struct{}),
	}
}

func (p *blockingRegisterPlatform) RegisterCommands([]BotCommandInfo) error {
	p.registerOnce.Do(func() {
		close(p.registerStarted)
	})
	<-p.allowRegister
	p.registerCalls++
	return nil
}

func (p *blockingRegisterPlatform) Stop() error {
	p.stopCalls++
	p.stopOnce.Do(func() {
		close(p.stopCalled)
	})
	return nil
}

type stubMediaPlatform struct {
	stubPlatformEngine
	images []ImageAttachment
	files  []FileAttachment
}

func (p *stubMediaPlatform) SendImage(_ context.Context, _ any, img ImageAttachment) error {
	p.images = append(p.images, img)
	return nil
}

func (p *stubMediaPlatform) SendFile(_ context.Context, _ any, file FileAttachment) error {
	p.files = append(p.files, file)
	return nil
}

type stubInlineButtonPlatform struct {
	stubPlatformEngine
	buttonContent string
	buttonRows    [][]ButtonOption
}

func (p *stubInlineButtonPlatform) SendWithButtons(_ context.Context, _ any, content string, buttons [][]ButtonOption) error {
	p.buttonContent = content
	p.buttonRows = buttons
	return nil
}

type stubCardPlatform struct {
	stubPlatformEngine
	mu             sync.Mutex
	repliedCards   []*Card
	sentCards      []*Card
	refreshedCards []*Card
	cardErr        error
}

func (p *stubCardPlatform) ReplyCard(_ context.Context, _ any, card *Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cardErr != nil {
		return p.cardErr
	}
	p.repliedCards = append(p.repliedCards, card)
	return nil
}

func (p *stubCardPlatform) SendCard(_ context.Context, _ any, card *Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cardErr != nil {
		return p.cardErr
	}
	p.sentCards = append(p.sentCards, card)
	return nil
}

func (p *stubCardPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	return "reconstructed-ctx:" + sessionKey, nil
}

func (p *stubCardPlatform) RefreshCard(_ context.Context, _ string, card *Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cardErr != nil {
		return p.cardErr
	}
	p.refreshedCards = append(p.refreshedCards, card)
	return nil
}

func (p *stubCardPlatform) getRefreshedCards() []*Card {
	p.mu.Lock()
	defer p.mu.Unlock()
	dst := make([]*Card, len(p.refreshedCards))
	copy(dst, p.refreshedCards)
	return dst
}

type stubCompactProgressPlatform struct {
	stubPlatformEngine
	style          string
	supportPayload bool
	previewMu      sync.Mutex
	previewStarts  []string
	previewEdits   []string
}

func (p *stubCompactProgressPlatform) ProgressStyle() string {
	if p.style == "" {
		return "compact"
	}
	return p.style
}

func (p *stubCompactProgressPlatform) SupportsProgressCardPayload() bool {
	return p.supportPayload
}

func (p *stubCompactProgressPlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	p.previewMu.Lock()
	p.previewStarts = append(p.previewStarts, content)
	p.previewMu.Unlock()
	return "preview-handle", nil
}

func (p *stubCompactProgressPlatform) UpdateMessage(_ context.Context, _ any, content string) error {
	p.previewMu.Lock()
	p.previewEdits = append(p.previewEdits, content)
	p.previewMu.Unlock()
	return nil
}

func (p *stubCompactProgressPlatform) getPreviewStarts() []string {
	p.previewMu.Lock()
	defer p.previewMu.Unlock()
	out := make([]string, len(p.previewStarts))
	copy(out, p.previewStarts)
	return out
}

func (p *stubCompactProgressPlatform) getPreviewEdits() []string {
	p.previewMu.Lock()
	defer p.previewMu.Unlock()
	out := make([]string, len(p.previewEdits))
	copy(out, p.previewEdits)
	return out
}

type stubModelModeAgent struct {
	stubAgent
	model           string
	mode            string
	reasoningEffort string
	providers       []ProviderConfig
	active          string
}

type stubStrictModelAgent struct {
	stubModelModeAgent
	models []ModelOption
	calls  int
}

type stubLiveModeSession struct {
	stubAgentSession
	modes []string
}

func (s *stubLiveModeSession) SetLiveMode(mode string) bool {
	s.modes = append(s.modes, mode)
	return true
}

func (a *stubModelModeAgent) SetModel(model string) {
	a.model = model
}

func (a *stubModelModeAgent) GetModel() string {
	return a.model
}

func (a *stubModelModeAgent) AvailableModels(_ context.Context) []ModelOption {
	return []ModelOption{
		{Name: "gpt-4.1", Desc: "Balanced", Alias: "gpt"},
		{Name: "gpt-4.1-mini", Desc: "Fast"},
	}
}

func (a *stubStrictModelAgent) AvailableModels(_ context.Context) []ModelOption {
	a.calls++
	return append([]ModelOption(nil), a.models...)
}

func (a *stubModelModeAgent) SetProviders(providers []ProviderConfig) {
	a.providers = providers
}

func (a *stubModelModeAgent) GetActiveProvider() *ProviderConfig {
	for i := range a.providers {
		if a.providers[i].Name == a.active {
			return &a.providers[i]
		}
	}
	return nil
}

func (a *stubModelModeAgent) ListProviders() []ProviderConfig {
	result := make([]ProviderConfig, len(a.providers))
	copy(result, a.providers)
	return result
}

func (a *stubModelModeAgent) SetActiveProvider(name string) bool {
	if name == "" {
		a.active = ""
		return true
	}
	for _, prov := range a.providers {
		if prov.Name == name {
			a.active = name
			return true
		}
	}
	return false
}

func (a *stubModelModeAgent) SetMode(mode string) {
	a.mode = mode
}

func (a *stubModelModeAgent) GetMode() string {
	if a.mode == "" {
		return "default"
	}
	return a.mode
}

func (a *stubModelModeAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask before risky actions", DescZh: "危险操作前询问"},
		{Key: "yolo", Name: "YOLO", NameZh: "放手做", Desc: "Skip confirmations", DescZh: "跳过确认"},
	}
}

func (a *stubModelModeAgent) SetReasoningEffort(effort string) {
	a.reasoningEffort = effort
}

func (a *stubModelModeAgent) GetReasoningEffort() string {
	return a.reasoningEffort
}

func (a *stubModelModeAgent) AvailableReasoningEfforts() []string {
	return []string{"low", "medium", "high", "xhigh"}
}

type namedStubModelModeAgent struct {
	stubModelModeAgent
	name string
}

func (a *namedStubModelModeAgent) Name() string {
	if a.name == "" {
		return "named-stub-model"
	}
	return a.name
}

type namedStubWorkspaceOptionAgent struct {
	namedStubModelModeAgent
	opts      map[string]any
	runAsUser string
	runAsEnv  []string
}

func (a *namedStubWorkspaceOptionAgent) WorkspaceAgentOptions() map[string]any {
	out := make(map[string]any, len(a.opts))
	for k, v := range a.opts {
		out[k] = v
	}
	return out
}

func (a *namedStubWorkspaceOptionAgent) GetRunAsUser() string { return a.runAsUser }

func (a *namedStubWorkspaceOptionAgent) GetRunAsEnv() []string {
	if len(a.runAsEnv) == 0 {
		return nil
	}
	out := make([]string, len(a.runAsEnv))
	copy(out, a.runAsEnv)
	return out
}

type stubWorkDirAgent struct {
	stubAgent
	workDir string
}

func (a *stubWorkDirAgent) SetWorkDir(dir string) {
	a.workDir = dir
}

func (a *stubWorkDirAgent) GetWorkDir() string {
	return a.workDir
}

type namedStubWorkDirAgent struct {
	stubWorkDirAgent
	name string
}

func (a *namedStubWorkDirAgent) Name() string {
	if a.name == "" {
		return "named-stub-workdir"
	}
	return a.name
}

type stubListAgent struct {
	stubAgent
	sessions []AgentSessionInfo
}

func (a *stubListAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return a.sessions, nil
}

type stubDeleteAgent struct {
	stubListAgent
	deleted []string
	errByID map[string]error
}

func (a *stubDeleteAgent) DeleteSession(_ context.Context, sessionID string) error {
	if err := a.errByID[sessionID]; err != nil {
		return err
	}
	a.deleted = append(a.deleted, sessionID)
	return nil
}

// waitDeleteModePhase polls the delete-mode state for the given session key
// until it reaches the target phase or the timeout expires.
func waitDeleteModePhase(t *testing.T, e *Engine, sessionKey, targetPhase string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dm := e.getDeleteModeState(sessionKey)
		if dm != nil && dm.phase == targetPhase {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for delete mode phase %q", targetPhase)
}

type stubProviderAgent struct {
	stubAgent
	providers []ProviderConfig
	active    string
}

func (a *stubProviderAgent) ListProviders() []ProviderConfig {
	return a.providers
}

func (a *stubProviderAgent) SetProviders(providers []ProviderConfig) {
	a.providers = providers
}

func (a *stubProviderAgent) GetActiveProvider() *ProviderConfig {
	for i := range a.providers {
		if a.providers[i].Name == a.active {
			return &a.providers[i]
		}
	}
	return nil
}

func (a *stubProviderAgent) SetActiveProvider(name string) bool {
	if name == "" {
		a.active = ""
		return true
	}
	for _, prov := range a.providers {
		if prov.Name == name {
			a.active = name
			return true
		}
	}
	return false
}

type stubUsageAgent struct {
	stubAgent
	report *UsageReport
	err    error
}

func (a *stubUsageAgent) GetUsage(_ context.Context) (*UsageReport, error) {
	return a.report, a.err
}

type stubReplyFooterAgent struct {
	stubModelModeAgent
	workDir string
	report  *UsageReport
	err     error
}

func (a *stubReplyFooterAgent) SetWorkDir(dir string) {
	a.workDir = dir
}

func (a *stubReplyFooterAgent) GetWorkDir() string {
	return a.workDir
}

func (a *stubReplyFooterAgent) GetUsage(_ context.Context) (*UsageReport, error) {
	return a.report, a.err
}

func newTestEngine() *Engine {
	return NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
}

func TestEngineSendToSessionWithAttachments(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.interactiveStates["session-1"] = &interactiveState{
		platform: p,
		replyCtx: "ctx-1",
	}

	err := e.SendToSessionWithAttachments(
		"session-1",
		"delivery ready",
		[]ImageAttachment{{MimeType: "image/png", Data: []byte("img"), FileName: "chart.png"}},
		[]FileAttachment{{MimeType: "text/plain", Data: []byte("doc"), FileName: "report.txt"}},
	)
	if err != nil {
		t.Fatalf("SendToSessionWithAttachments returned error: %v", err)
	}

	if got := p.getSent(); len(got) != 1 || got[0] != "delivery ready" {
		t.Fatalf("sent text = %#v, want one message", got)
	}
	if len(p.images) != 1 || p.images[0].FileName != "chart.png" {
		t.Fatalf("images = %#v", p.images)
	}
	if len(p.files) != 1 || p.files[0].FileName != "report.txt" {
		t.Fatalf("files = %#v", p.files)
	}
}

func TestEngineSendToSessionWithAttachments_UnsupportedPlatform(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.interactiveStates["session-1"] = &interactiveState{
		platform: p,
		replyCtx: "ctx-1",
	}

	err := e.SendToSessionWithAttachments(
		"session-1",
		"delivery ready",
		[]ImageAttachment{{MimeType: "image/png", Data: []byte("img"), FileName: "chart.png"}},
		nil,
	)
	if err == nil {
		t.Fatal("expected unsupported attachment send to fail")
	}
	if got := p.getSent(); len(got) != 0 {
		t.Fatalf("sent text = %#v, want no sends on failure", got)
	}
}

func TestEngineSendToSessionWithAttachments_DisabledByConfig(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAttachmentSendEnabled(false)
	e.interactiveStates["session-1"] = &interactiveState{
		platform: p,
		replyCtx: "ctx-1",
	}

	err := e.SendToSessionWithAttachments(
		"session-1",
		"delivery ready",
		nil,
		[]FileAttachment{{MimeType: "text/plain", Data: []byte("doc"), FileName: "report.txt"}},
	)
	if err == nil {
		t.Fatal("expected attachment send to be blocked")
	}
	if !errors.Is(err, ErrAttachmentSendDisabled) {
		t.Fatalf("err = %v, want ErrAttachmentSendDisabled", err)
	}
	if got := p.getSent(); len(got) != 0 {
		t.Fatalf("sent text = %#v, want no sends when disabled", got)
	}
	if len(p.files) != 0 {
		t.Fatalf("files = %#v, want no files sent when disabled", p.files)
	}
}

func TestEngineSendToSessionWithAttachments_MultiWorkspaceRawSessionKey(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindingPath)

	wsDir := filepath.Join(baseDir, "ws1")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	normalizedWsDir := normalizeWorkspacePath(wsDir)
	channelID := "C123"
	rawKey := "slack:" + channelID + ":U1"
	e.workspaceBindings.Bind("project:test", channelID, "chan", normalizedWsDir)

	iKey := normalizedWsDir + ":" + rawKey
	e.interactiveStates[iKey] = &interactiveState{
		platform: p,
		replyCtx: "ctx-1",
	}

	err := e.SendToSessionWithAttachments(rawKey, "delivery ready", nil, nil)
	if err != nil {
		t.Fatalf("SendToSessionWithAttachments returned error: %v", err)
	}
	if got := p.getSent(); len(got) != 1 || got[0] != "delivery ready" {
		t.Fatalf("sent text = %#v, want one message", got)
	}
}

// stubProactiveSendPlatform implements ReplyContextReconstruct for proactive
// SendToSessionWithAttachments when there is no interactive session.
type stubProactiveSendPlatform struct {
	stubMediaPlatform
	reconstructKey string
}

func (p *stubProactiveSendPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	p.reconstructKey = sessionKey
	return "proactive-rctx", nil
}

func TestEngineSendToSessionWithAttachments_WorkspacePrefixedSessionKey(t *testing.T) {
	p := &stubProactiveSendPlatform{
		stubMediaPlatform: stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "slack"}},
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	prefixed := "/tmp/myproject:slack:C123:U1"
	err := e.SendToSessionWithAttachments(prefixed, "delivery ready", nil, nil)
	if err != nil {
		t.Fatalf("SendToSessionWithAttachments returned error: %v", err)
	}
	if p.reconstructKey != "slack:C123:U1" {
		t.Fatalf("ReconstructReplyCtx key = %q, want slack:C123:U1", p.reconstructKey)
	}
	if got := p.getSent(); len(got) != 1 || got[0] != "delivery ready" {
		t.Fatalf("sent text = %#v, want one message", got)
	}
}

func TestEngineStart_DefersAsyncPlatformReadyInitialization(t *testing.T) {
	p := &stubLifecyclePlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.AddCommand("help", "help", "", "", "", "test")

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p.handler == nil {
		t.Fatal("lifecycle handler not installed")
	}
	if p.registerCalls != 0 {
		t.Fatalf("registerCalls = %d, want 0 before ready", p.registerCalls)
	}
	if p.cardNavSetCalls != 0 {
		t.Fatalf("cardNavSetCalls = %d, want 0 before ready", p.cardNavSetCalls)
	}
}

func TestEngine_OnPlatformReady_IsIdempotentUntilUnavailable(t *testing.T) {
	p := &stubLifecyclePlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.AddCommand("help", "help", "", "", "", "test")

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	e.OnPlatformReady(p)
	e.OnPlatformReady(p)

	if p.registerCalls != 1 {
		t.Fatalf("registerCalls = %d, want 1", p.registerCalls)
	}
	if p.cardNavSetCalls != 1 {
		t.Fatalf("cardNavSetCalls = %d, want 1", p.cardNavSetCalls)
	}

	e.OnPlatformUnavailable(p, errors.New("lost"))
	e.OnPlatformReady(p)

	if p.registerCalls != 2 {
		t.Fatalf("registerCalls after recover = %d, want 2", p.registerCalls)
	}
}

func TestEngine_OnPlatformUnavailable_IsIdempotent(t *testing.T) {
	p := &stubLifecyclePlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.AddCommand("help", "help", "", "", "", "test")

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	e.OnPlatformReady(p)
	e.OnPlatformUnavailable(p, errors.New("lost"))
	e.OnPlatformUnavailable(p, errors.New("lost-again"))
	e.OnPlatformReady(p)

	if p.registerCalls != 2 {
		t.Fatalf("registerCalls after duplicate unavailable = %d, want 2", p.registerCalls)
	}
}

func TestEngine_LifecycleCallbacksIgnoredAfterStopBegins(t *testing.T) {
	p := &stubLifecyclePlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.AddCommand("help", "help", "", "", "", "test")

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	e.OnPlatformReady(p)
	e.OnPlatformUnavailable(p, errors.New("late"))

	if p.registerCalls != 0 {
		t.Fatalf("registerCalls = %d, want 0 after stop", p.registerCalls)
	}
}

func TestEngine_StopDoesNotWaitForBlockedPlatformCapabilityInit(t *testing.T) {
	p := newBlockingRegisterPlatform("telegram")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.AddCommand("help", "help", "", "", "", "test")

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	readyDone := make(chan struct{})
	go func() {
		e.OnPlatformReady(p)
		close(readyDone)
	}()

	select {
	case <-p.registerStarted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("RegisterCommands was not called")
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- e.Stop()
	}()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Stop blocked on platform capability initialization")
	}

	select {
	case <-p.stopCalled:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("platform Stop was not called while RegisterCommands was blocked")
	}

	close(p.allowRegister)

	select {
	case <-readyDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("OnPlatformReady did not finish after RegisterCommands was released")
	}
}

func TestProcessInteractiveEvents_SuppressesDuplicateSideChannelText(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	sideText := "已发送 AGENTS.md 文件给你。"
	if err := e.SendToSessionWithAttachments(sessionKey, sideText, nil, []FileAttachment{{
		MimeType: "text/markdown",
		Data:     []byte("body"),
		FileName: "AGENTS.md",
	}}); err != nil {
		t.Fatalf("SendToSessionWithAttachments returned error: %v", err)
	}

	agentSession.events <- Event{Type: EventResult, Content: sideText, Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)

	if got := p.getSent(); len(got) != 1 || got[0] != sideText {
		t.Fatalf("sent text = %#v, want one side-channel message", got)
	}
}

func TestProcessInteractiveEvents_DoesNotSuppressDifferentFinalText(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	if err := e.SendToSessionWithAttachments(sessionKey, "已发送 AGENTS.md 文件给你。", nil, []FileAttachment{{
		MimeType: "text/markdown",
		Data:     []byte("body"),
		FileName: "AGENTS.md",
	}}); err != nil {
		t.Fatalf("SendToSessionWithAttachments returned error: %v", err)
	}

	finalText := "文件已发出，另外我也把使用方法整理好了。"
	agentSession.events <- Event{Type: EventResult, Content: finalText, Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)

	if got := p.getSent(); len(got) != 2 || got[0] == got[1] {
		t.Fatalf("sent text = %#v, want side-channel and final reply", got)
	}
	if got := p.getSent()[1]; got != finalText {
		t.Fatalf("final sent text = %q, want %q", got, finalText)
	}
}

func TestProcessInteractiveEvents_AppendsReplyFooterWhenEnabled(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	agent := &stubReplyFooterAgent{
		stubModelModeAgent: stubModelModeAgent{
			model:           "gpt-5.4",
			reasoningEffort: "xhigh",
		},
		workDir: filepath.Join(homeDir, "codes", "cc-connect"),
		report: &UsageReport{
			Buckets: []UsageBucket{{
				Name: "Rate limit",
				Windows: []UsageWindow{{
					Name:          "Primary",
					UsedPercent:   0,
					WindowSeconds: 18000,
				}},
			}},
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetReplyFooterEnabled(true)

	sessionKey := "telegram:user-footer"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-footer")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-footer",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventResult, Content: "answer", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-footer", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one final reply", sent)
	}
	want := "answer\n\n*gpt-5.4 · xhigh · 100% left · ~/codes/cc-connect*"
	if sent[0] != want {
		t.Fatalf("final reply = %q, want %q", sent[0], want)
	}
}

func TestProcessInteractiveEvents_DoesNotAppendReplyFooterWhenDisabled(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	agent := &stubReplyFooterAgent{
		stubModelModeAgent: stubModelModeAgent{
			model:           "gpt-5.4",
			reasoningEffort: "xhigh",
		},
		workDir: filepath.Join(homeDir, "codes", "cc-connect"),
		report: &UsageReport{
			Buckets: []UsageBucket{{
				Name: "Rate limit",
				Windows: []UsageWindow{{
					Name:          "Primary",
					UsedPercent:   0,
					WindowSeconds: 18000,
				}},
			}},
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetReplyFooterEnabled(false)

	sessionKey := "telegram:user-footer-off"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-footer-off")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-footer-off",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventResult, Content: "answer", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-footer-off", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one final reply", sent)
	}
	if sent[0] != "answer" {
		t.Fatalf("final reply = %q, want plain answer without footer", sent[0])
	}
}

func TestProcessInteractiveEvents_ReplyFooterPrefersSessionRuntimeState(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	agent := &stubReplyFooterAgent{
		stubModelModeAgent: stubModelModeAgent{
			model:           "agent-model",
			reasoningEffort: "medium",
		},
		workDir: filepath.Join(homeDir, "codes", "agent-default"),
		report: &UsageReport{
			Buckets: []UsageBucket{{
				Name: "Rate limit",
				Windows: []UsageWindow{{
					Name:          "Primary",
					UsedPercent:   80,
					WindowSeconds: 18000,
				}},
			}},
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetReplyFooterEnabled(true)

	sessionKey := "telegram:user-footer-runtime"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-footer-runtime")
	agentSession.model = "gpt-5.4"
	agentSession.reasoningEffort = "xhigh"
	agentSession.workDir = filepath.Join(homeDir, "codes", "cc-connect")
	agentSession.report = &UsageReport{
		Buckets: []UsageBucket{{
			Name: "Rate limit",
			Windows: []UsageWindow{{
				Name:          "Primary",
				UsedPercent:   0,
				WindowSeconds: 18000,
			}},
		}},
	}
	agentSession.contextUsage = &ContextUsage{
		UsedTokens:     181424,
		BaselineTokens: 12000,
		TotalTokens:    50821769,
		ContextWindow:  258400,
	}
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-footer-runtime",
		agent:        agent,
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventResult, Content: "answer", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-footer-runtime", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one final reply", sent)
	}
	want := "answer\n\n*gpt-5.4 · xhigh · 31% left · ~/codes/cc-connect*"
	if sent[0] != want {
		t.Fatalf("final reply = %q, want %q", sent[0], want)
	}
}

// Regression: an agent that only exposes a workdir (no model/effort/usage)
// must not emit a footer at all. Previously this produced a footer like
// "*~*" when the agent was running in the user's home directory, which
// rendered as a bare "~" on Feishu/Weixin.
func TestProcessInteractiveEvents_SuppressesReplyFooterWhenOnlyWorkDir(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	agent := &stubWorkDirAgent{workDir: homeDir}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetReplyFooterEnabled(true)

	sessionKey := "telegram:user-footer-workdir-only"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-footer-workdir-only")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-footer-workdir-only",
		agent:        agent,
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventResult, Content: "answer", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-footer-workdir-only", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one final reply", sent)
	}
	if sent[0] != "answer" {
		t.Fatalf("final reply = %q, want plain answer without footer", sent[0])
	}
}

func TestProcessInteractiveEvents_HiddenToolProgressKeepsPreviewOnFinalize(t *testing.T) {
	p := &mockKeepPreviewPlatform{}
	p.n = "feishu"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{ThinkingMessages: true, ThinkingMaxLen: 300, ToolMaxLen: 500, ToolMessages: false})
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventText, Content: "final response"}
	agentSession.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo hi"}
	agentSession.events <- Event{Type: EventResult, Content: "", Done: true}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)

	if got := p.getSent(); len(got) != 0 {
		t.Fatalf("sent text = %#v, want no plain-text fallback sends", got)
	}

	p.mu.Lock()
	deletedCount := len(p.deleted)
	previewMsgs := append([]string(nil), p.messages...)
	p.mu.Unlock()

	if deletedCount != 0 {
		t.Fatalf("deleted previews = %d, want 0", deletedCount)
	}
	if len(previewMsgs) == 0 || previewMsgs[len(previewMsgs)-1] != "update:final response" {
		t.Fatalf("preview messages = %#v, want in-place final update", previewMsgs)
	}
}

func TestProcessInteractiveEvents_ToolMessagesDisabledSuppressesToolProgressOnly(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{ThinkingMessages: true, ThinkingMaxLen: 300, ToolMaxLen: 500, ToolMessages: false})
	sessionKey := "telegram:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventThinking, Content: "planning"}
	agentSession.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo hi"}
	agentSession.events <- Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "hi"}
	agentSession.events <- Event{Type: EventText, Content: "done"}
	agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)

	sent := p.getSent()
	if len(sent) < 1 || len(sent) > 2 {
		t.Fatalf("sent = %#v, want final response with optional standalone thinking message", sent)
	}
	for _, msg := range sent {
		if strings.Contains(msg, "Bash") || strings.Contains(msg, "echo hi") || strings.Contains(msg, "hi") {
			t.Fatalf("tool progress should stay hidden, got %q", msg)
		}
	}
	if len(sent) == 2 && !strings.Contains(sent[0], "planning") {
		t.Fatalf("thinking message = %q, want planning", sent[0])
	}
	if sent[len(sent)-1] != "done" {
		t.Fatalf("final message = %q, want done", sent[len(sent)-1])
	}
}

func TestProcessInteractiveEvents_CompactProgressCoalescesThinkingAndToolUse(t *testing.T) {
	p := &stubCompactProgressPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "feishu:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-compact",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventThinking, Content: "Thinking about command"}
	agentSession.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"}
	agentSession.events <- Event{Type: EventText, Content: "done"}
	agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 || sent[0] != "done" {
		t.Fatalf("sent = %#v, want only final assistant reply", sent)
	}

	starts := p.getPreviewStarts()
	if len(starts) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(starts))
	}
	if !strings.Contains(starts[0], "Thinking") {
		t.Fatalf("start preview should contain thinking text, got %q", starts[0])
	}

	edits := p.getPreviewEdits()
	if len(edits) != 1 {
		t.Fatalf("preview edits = %d, want 1", len(edits))
	}
	if !strings.Contains(edits[0], "pwd") {
		t.Fatalf("updated preview should contain tool input, got %q", edits[0])
	}
}

func TestProcessInteractiveEvents_CardProgressUsesCardTemplate(t *testing.T) {
	p := &stubCompactProgressPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		style:              "card",
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "feishu:user2"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s2")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-card",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventThinking, Content: "Plan first"}
	agentSession.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo hi"}
	agentSession.events <- Event{Type: EventText, Content: "done"}
	agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m2", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 || sent[0] != "done" {
		t.Fatalf("sent = %#v, want only final assistant reply", sent)
	}

	starts := p.getPreviewStarts()
	if len(starts) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(starts))
	}
	if !strings.Contains(starts[0], "**Progress**") {
		t.Fatalf("start preview should contain fallback progress title, got %q", starts[0])
	}
	if !strings.Contains(starts[0], "1.") {
		t.Fatalf("start preview should contain first item index, got %q", starts[0])
	}

	edits := p.getPreviewEdits()
	if len(edits) != 1 {
		t.Fatalf("preview edits = %d, want 1", len(edits))
	}
	if !strings.Contains(edits[0], "2.") {
		t.Fatalf("updated preview should contain second item index, got %q", edits[0])
	}
	if !strings.Contains(edits[0], "echo hi") {
		t.Fatalf("updated preview should contain tool command, got %q", edits[0])
	}
}

func TestProcessInteractiveEvents_FinalReplyUsesWorkspaceForReferenceRendering(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	a := &namedStubModelModeAgent{name: "codex"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	e.SetReferenceConfig(ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	})

	sessionKey := "feishu:user-relative"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-relative")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-relative",
		workspaceDir: "/root/code",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{
		Type:    EventResult,
		Content: "/root/code/demo-repo/src/services/user_profile_service.ts:42",
		Done:    true,
	}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-relative", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one final reply", sent)
	}
	if got := sent[0]; got != "📄 `demo-repo/src/services/user_profile_service.ts:42`" {
		t.Fatalf("final reply = %q, want workspace-relative rendered reference", got)
	}
}

func TestProcessInteractiveEvents_FinalReplyRemainsRawWhenReferencesDisabled(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	a := &namedStubModelModeAgent{name: "codex"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	e.SetReferenceConfig(ReferenceRenderCfg{
		NormalizeAgents: []string{},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	})

	sessionKey := "feishu:user-relative-raw"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-relative-raw")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-relative-raw",
		workspaceDir: "/root/code/demo",
	}
	e.interactiveStates[sessionKey] = state

	raw := "Check [/root/code/demo/ui/recovery_contact_form.tsx](/root/code/demo/ui/recovery_contact_form.tsx) and /root/code/demo/ui/recovery_contact_form.tsx:11"
	agentSession.events <- Event{
		Type:    EventResult,
		Content: raw,
		Done:    true,
	}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-relative-raw", time.Now(), nil, nil, state.replyCtx)

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one final reply", sent)
	}
	if got := sent[0]; got != raw {
		t.Fatalf("final reply = %q, want raw unchanged content %q", got, raw)
	}
}

func TestProcessInteractiveEvents_CardProgressUsesStructuredPayloadWhenSupported(t *testing.T) {
	p := &stubCompactProgressPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		style:              "card",
		supportPayload:     true,
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "feishu:user3"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s3")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-card-structured",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventThinking, Content: "Plan first"}
	agentSession.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo hi"}
	agentSession.events <- Event{Type: EventText, Content: "done"}
	agentSession.events <- Event{Type: EventResult, Content: "done", Done: true}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m3", time.Now(), nil, nil, state.replyCtx)

	starts := p.getPreviewStarts()
	if len(starts) != 1 {
		t.Fatalf("preview starts = %d, want 1", len(starts))
	}
	if !strings.HasPrefix(starts[0], ProgressCardPayloadPrefix) {
		t.Fatalf("start preview should be structured payload, got %q", starts[0])
	}
	startPayload, ok := ParseProgressCardPayload(starts[0])
	if !ok {
		t.Fatalf("start preview should parse as structured payload, got %q", starts[0])
	}
	if len(startPayload.Items) != 1 {
		t.Fatalf("start payload items = %d, want 1", len(startPayload.Items))
	}
	if startPayload.Items[0].Kind != ProgressEntryThinking {
		t.Fatalf("start payload kind = %q, want %q", startPayload.Items[0].Kind, ProgressEntryThinking)
	}
	if startPayload.State != ProgressCardStateRunning {
		t.Fatalf("start payload state = %q, want %q", startPayload.State, ProgressCardStateRunning)
	}

	edits := p.getPreviewEdits()
	if len(edits) != 2 {
		t.Fatalf("preview edits = %d, want 2", len(edits))
	}
	updatePayload, ok := ParseProgressCardPayload(edits[0])
	if !ok {
		t.Fatalf("update preview should parse as structured payload, got %q", edits[0])
	}
	if len(updatePayload.Items) != 2 {
		t.Fatalf("update payload items = %d, want 2", len(updatePayload.Items))
	}
	if !strings.Contains(updatePayload.Items[1].Text, "echo hi") {
		t.Fatalf("second payload item should contain tool command, got %q", updatePayload.Items[1].Text)
	}

	finalPayload, ok := ParseProgressCardPayload(edits[1])
	if !ok {
		t.Fatalf("final preview should parse as structured payload, got %q", edits[1])
	}
	if finalPayload.State != ProgressCardStateCompleted {
		t.Fatalf("final payload state = %q, want %q", finalPayload.State, ProgressCardStateCompleted)
	}
}

func TestAgentSystemPrompt_MentionsAttachmentSend(t *testing.T) {
	prompt := AgentSystemPrompt()
	if !strings.Contains(prompt, "cc-connect send --image") {
		t.Fatalf("prompt missing image send instructions: %q", prompt)
	}
	if !strings.Contains(prompt, "cc-connect send --file") {
		t.Fatalf("prompt missing file send instructions: %q", prompt)
	}
}

func countCardActionValues(card *Card, prefix string) int {
	count := 0
	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case CardActions:
			for _, btn := range e.Buttons {
				if strings.HasPrefix(btn.Value, prefix) {
					count++
				}
			}
		case CardListItem:
			if strings.HasPrefix(e.BtnValue, prefix) {
				count++
			}
		}
	}
	return count
}

func findCardAction(card *Card, value string) (CardButton, bool) {
	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case CardActions:
			for _, btn := range e.Buttons {
				if btn.Value == value {
					return btn, true
				}
			}
		case CardListItem:
			if e.BtnValue == value {
				return CardButton{Text: e.BtnText, Type: e.BtnType, Value: e.BtnValue}, true
			}
		}
	}
	return CardButton{}, false
}

// --- alias tests ---

func TestEngine_Alias(t *testing.T) {
	e := newTestEngine()
	e.AddAlias("帮助", "/help")
	e.AddAlias("新建", "/new")

	got := e.resolveAlias("帮助")
	if got != "/help" {
		t.Errorf("resolveAlias('帮助') = %q, want /help", got)
	}

	got = e.resolveAlias("新建 my-session")
	if got != "/new my-session" {
		t.Errorf("resolveAlias('新建 my-session') = %q, want '/new my-session'", got)
	}

	got = e.resolveAlias("random text")
	if got != "random text" {
		t.Errorf("resolveAlias should not modify unmatched content, got %q", got)
	}
}

func TestEngine_ClearAliases(t *testing.T) {
	e := newTestEngine()
	e.AddAlias("帮助", "/help")
	e.ClearAliases()

	got := e.resolveAlias("帮助")
	if got != "帮助" {
		t.Errorf("after ClearAliases, should not resolve, got %q", got)
	}
}

// --- banned words tests ---

func TestEngine_BannedWords(t *testing.T) {
	e := newTestEngine()
	e.SetBannedWords([]string{"spam", "BadWord"})

	if w := e.matchBannedWord("this is spam content"); w != "spam" {
		t.Errorf("expected 'spam', got %q", w)
	}
	if w := e.matchBannedWord("CONTAINS BADWORD HERE"); w != "badword" {
		t.Errorf("expected case-insensitive match 'badword', got %q", w)
	}
	if w := e.matchBannedWord("clean message"); w != "" {
		t.Errorf("expected empty, got %q", w)
	}
}

func TestEngine_BannedWordsEmpty(t *testing.T) {
	e := newTestEngine()
	if w := e.matchBannedWord("anything"); w != "" {
		t.Errorf("no banned words set, should return empty, got %q", w)
	}
}

// --- disabled commands tests ---

func TestEngine_DisabledCommands(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"upgrade", "restart"})

	if !e.disabledCmds["upgrade"] {
		t.Error("upgrade should be disabled")
	}
	if !e.disabledCmds["restart"] {
		t.Error("restart should be disabled")
	}
	if e.disabledCmds["help"] {
		t.Error("help should not be disabled")
	}
}

func TestEngine_DisabledCommandsWithSlash(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"/upgrade"})

	if !e.disabledCmds["upgrade"] {
		t.Error("upgrade should be disabled even when prefixed with /")
	}
}

func TestResolveDisabledCmds_Wildcard(t *testing.T) {
	m := resolveDisabledCmds([]string{"*"})
	for _, bc := range builtinCommands {
		if !m[bc.id] {
			t.Errorf("wildcard should disable %q", bc.id)
		}
	}
}

func TestResolveDisabledCmds_Specific(t *testing.T) {
	m := resolveDisabledCmds([]string{"upgrade", "/restart", "Help"})
	if !m["upgrade"] {
		t.Error("upgrade should be disabled")
	}
	if !m["restart"] {
		t.Error("restart should be disabled (slash stripped)")
	}
	if !m["help"] {
		t.Error("help should be disabled (case insensitive)")
	}
	if m["shell"] {
		t.Error("shell should not be disabled")
	}
}

func TestResolveDisabledCmds_Empty(t *testing.T) {
	m1 := resolveDisabledCmds(nil)
	if len(m1) != 0 {
		t.Errorf("nil input should produce empty map, got %d entries", len(m1))
	}
	m2 := resolveDisabledCmds([]string{})
	if len(m2) != 0 {
		t.Errorf("empty input should produce empty map, got %d entries", len(m2))
	}
}

func TestEngine_DisabledCommandsWildcard(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"*"})

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}

	e.handleCommand(p, msg, "/help")
	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "disabled") && !strings.Contains(p.sent[0], "禁用") {
		t.Errorf("expected disabled message, got: %s", p.sent[0])
	}
}

// --- admin_from tests ---

func TestEngine_AdminFrom_DenyByDefault(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/shell echo hi")

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "admin") {
		t.Errorf("expected admin required message, got: %s", p.sent[0])
	}
}

func TestEngine_AdminFrom_ExplicitUser(t *testing.T) {
	e := newTestEngine()
	e.SetAdminFrom("admin1,admin2")
	p := &stubPlatformEngine{n: "test"}

	if !e.isAdmin("admin1") {
		t.Error("admin1 should be admin")
	}
	if !e.isAdmin("admin2") {
		t.Error("admin2 should be admin")
	}
	if e.isAdmin("user3") {
		t.Error("user3 should not be admin")
	}

	// non-admin user tries /shell
	msg := &Message{SessionKey: "test:u3", UserID: "user3", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/shell echo hi")
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "admin") {
		t.Errorf("non-admin should be blocked from /shell, got: %v", p.sent)
	}
}

func TestEngine_AdminFrom_Wildcard(t *testing.T) {
	e := newTestEngine()
	e.SetAdminFrom("*")

	if !e.isAdmin("anyone") {
		t.Error("wildcard admin_from should allow any user")
	}
	if !e.isAdmin("12345") {
		t.Error("wildcard admin_from should allow any user ID")
	}
}

func TestEngine_AdminFrom_GatesRestart(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/restart")

	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "admin") {
		t.Errorf("non-admin should be blocked from /restart, got: %v", p.sent)
	}
}

func TestEngine_AdminFrom_GatesUpgrade(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/upgrade")

	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "admin") {
		t.Errorf("non-admin should be blocked from /upgrade, got: %v", p.sent)
	}
}

func TestEngine_AdminFrom_AllowsNonPrivileged(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/help")

	if len(p.sent) == 0 {
		t.Fatal("expected /help to produce a reply")
	}
	if strings.Contains(p.sent[0], "requires admin") {
		t.Errorf("/help should not require admin, got: %s", p.sent[0])
	}
}

func TestEngine_AdminFrom_GatesCommandsAddExec(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/commands addexec mysh echo hello")

	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "admin") {
		t.Errorf("non-admin should be blocked from /commands addexec, got: %v", p.sent)
	}
}

func TestEngine_AdminFrom_GatesCustomExecCommand(t *testing.T) {
	e := newTestEngine()
	e.commands.Add("deploy", "", "", "echo deploying", "", "config")
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/deploy")

	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "admin") {
		t.Errorf("non-admin should be blocked from custom exec command, got: %v", p.sent)
	}
}

func TestEngine_AdminFrom_AdminCanRunShell(t *testing.T) {
	e := newTestEngine()
	e.SetAdminFrom("admin1")
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{SessionKey: "test:a1", UserID: "admin1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/shell echo hello")

	// Shell runs async in a goroutine; wait for it to complete.
	time.Sleep(500 * time.Millisecond)

	for _, s := range p.getSent() {
		if strings.Contains(s, "admin") {
			t.Errorf("admin user should not be blocked, got: %s", s)
		}
	}
}

// --- role-based ACL tests ---

func TestEngine_RoleBasedACL_AdminCanRunAll(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"help", "status"}) // project-level disables

	urm := NewUserRoleManager()
	urm.Configure("member", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{"*"}},
	})
	e.SetUserRoles(urm)

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:a1", UserID: "admin1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/help")

	// Admin role has disabled_commands=[], so /help should NOT be blocked
	for _, s := range p.sent {
		if strings.Contains(s, "disabled") || strings.Contains(s, "禁用") {
			t.Errorf("admin should not have /help disabled, got: %s", s)
		}
	}
}

func TestEngine_RoleBasedACL_MemberBlocked(t *testing.T) {
	e := newTestEngine()

	urm := NewUserRoleManager()
	urm.Configure("member", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{"*"}},
	})
	e.SetUserRoles(urm)

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/help")

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "disabled") && !strings.Contains(p.sent[0], "禁用") {
		t.Errorf("member should have /help disabled, got: %s", p.sent[0])
	}
}

func TestEngine_RoleBasedACL_NoUserID_UsesDefaultRole(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"help"}) // project-level disables /help

	// Default role "member" has wildcard with disabled_commands=["*"]
	urm := NewUserRoleManager()
	urm.Configure("member", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{"*"}},
	})
	e.SetUserRoles(urm)

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:anon", UserID: "", ReplyCtx: "ctx"} // no UserID
	e.handleCommand(p, msg, "/help")

	// Empty UserID resolves to default/wildcard role, which disables all commands
	if len(p.sent) != 1 || (!strings.Contains(p.sent[0], "disabled") && !strings.Contains(p.sent[0], "禁用")) {
		t.Errorf("empty UserID should resolve to default role ACL, got: %v", p.sent)
	}
}

func TestEngine_RoleBasedACL_NoUsersConfig_Legacy(t *testing.T) {
	e := newTestEngine()
	e.SetDisabledCommands([]string{"help"})
	// No SetUserRoles — legacy mode

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/help")

	if len(p.sent) != 1 || (!strings.Contains(p.sent[0], "disabled") && !strings.Contains(p.sent[0], "禁用")) {
		t.Errorf("legacy mode should use project-level disabled_commands, got: %v", p.sent)
	}
}

func TestEngine_CustomCommand_DisabledByRole(t *testing.T) {
	e := newTestEngine()
	e.commands.Add("deploy", "deploy command", "deploy it", "", "", "test")

	urm := NewUserRoleManager()
	urm.Configure("member", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{"deploy"}},
	})
	e.SetUserRoles(urm)

	// Member should be blocked from custom command
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/deploy")

	if len(p.sent) != 1 || (!strings.Contains(p.sent[0], "disabled") && !strings.Contains(p.sent[0], "禁用")) {
		t.Errorf("custom command should be blocked for member, got: %v", p.sent)
	}

	// Admin should be allowed
	p2 := &stubPlatformEngine{n: "test"}
	msg2 := &Message{SessionKey: "test:a1", UserID: "admin1", ReplyCtx: "ctx"}
	e.handleCommand(p2, msg2, "/deploy")

	if len(p2.sent) > 0 && (strings.Contains(p2.sent[0], "disabled") || strings.Contains(p2.sent[0], "禁用")) {
		t.Errorf("custom command should be allowed for admin, got: %v", p2.sent)
	}
}

func TestEngine_SkillCommand_DisabledByRole(t *testing.T) {
	e := newTestEngine()

	// Create a temporary skill directory with a SKILL.md
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "deploy-prod")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("deploy to production"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.skills.SetDirs([]string{dir})

	urm := NewUserRoleManager()
	urm.Configure("member", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{"deploy-prod"}},
	})
	e.SetUserRoles(urm)

	// Member should be blocked from skill command
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/deploy-prod")

	if len(p.sent) != 1 || (!strings.Contains(p.sent[0], "disabled") && !strings.Contains(p.sent[0], "禁用")) {
		t.Errorf("skill should be blocked for member, got: %v", p.sent)
	}

	// Admin should NOT be blocked (but may fail at session level — that's fine,
	// we only check that the "disabled" message is NOT returned)
	p2 := &stubPlatformEngine{n: "test"}
	msg2 := &Message{SessionKey: "test:a1", UserID: "admin1", ReplyCtx: "ctx"}
	e.handleCommand(p2, msg2, "/deploy-prod")

	for _, s := range p2.sent {
		if strings.Contains(s, "disabled") || strings.Contains(s, "禁用") {
			t.Errorf("skill should be allowed for admin, got: %v", p2.sent)
		}
	}
}

func TestEngine_SkillCommand_DisabledByProjectLevel(t *testing.T) {
	e := newTestEngine()

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("a skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.skills.SetDirs([]string{dir})
	e.SetDisabledCommands([]string{"my-skill"})

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/my-skill")

	if len(p.sent) != 1 || (!strings.Contains(p.sent[0], "disabled") && !strings.Contains(p.sent[0], "禁用")) {
		t.Errorf("skill should be blocked by project-level disabled_commands, got: %v", p.sent)
	}
}

// --- role-based rate limit tests ---

func TestEngine_RateLimit_RoleSpecific(t *testing.T) {
	e := newTestEngine()

	urm := NewUserRoleManager()
	urm.Configure("member", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{},
			RateLimit: &RateLimitCfg{MaxMessages: 50, Window: time.Minute}},
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{},
			RateLimit: &RateLimitCfg{MaxMessages: 2, Window: time.Minute}},
	})
	e.SetUserRoles(urm)

	// Member should be limited after 2 messages
	msg := &Message{SessionKey: "test:u1", UserID: "user1"}
	if !e.checkRateLimit(msg) {
		t.Error("1st message should be allowed")
	}
	if !e.checkRateLimit(msg) {
		t.Error("2nd message should be allowed")
	}
	if e.checkRateLimit(msg) {
		t.Error("3rd message should be rate-limited")
	}

	// Admin should still be allowed
	adminMsg := &Message{SessionKey: "test:a1", UserID: "admin1"}
	if !e.checkRateLimit(adminMsg) {
		t.Error("admin should not be rate-limited")
	}
}

func TestEngine_RateLimit_NoUsersConfig_Legacy(t *testing.T) {
	e := newTestEngine()
	e.SetRateLimitCfg(RateLimitCfg{MaxMessages: 2, Window: time.Minute})

	msg := &Message{SessionKey: "test:session1", UserID: "user1"}
	if !e.checkRateLimit(msg) {
		t.Error("1st should be allowed")
	}
	if !e.checkRateLimit(msg) {
		t.Error("2nd should be allowed")
	}
	if e.checkRateLimit(msg) {
		t.Error("3rd should be rate-limited")
	}

	// Different session key should be independent (legacy keying)
	msg2 := &Message{SessionKey: "test:session2", UserID: "user1"}
	if !e.checkRateLimit(msg2) {
		t.Error("different session key should have independent bucket in legacy mode")
	}
}

func TestEngine_RateLimit_GlobalFallback(t *testing.T) {
	e := newTestEngine()
	e.SetRateLimitCfg(RateLimitCfg{MaxMessages: 2, Window: time.Minute})

	// User roles configured but role has no rate_limit
	urm := NewUserRoleManager()
	urm.Configure("member", []RoleInput{
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{}},
		// No RateLimit on this role
	})
	e.SetUserRoles(urm)

	msg := &Message{SessionKey: "test:s1", UserID: "user1"}
	if !e.checkRateLimit(msg) {
		t.Error("1st should be allowed")
	}
	if !e.checkRateLimit(msg) {
		t.Error("2nd should be allowed")
	}
	if e.checkRateLimit(msg) {
		t.Error("3rd should be rate-limited by global limiter")
	}

	// Same user, different session → should share limit (keyed by userID when users config active)
	msg2 := &Message{SessionKey: "test:s2", UserID: "user1"}
	if e.checkRateLimit(msg2) {
		t.Error("same user from different session should still be rate-limited")
	}
}

// --- permission prompt card tests ---

func TestSendPermissionPrompt_CardPlatform(t *testing.T) {
	e := newTestEngine()
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	e.sendPermissionPrompt(p, "ctx", "full prompt text", "write_file", "/tmp/test.txt")

	if len(p.sentCards) != 1 {
		t.Fatalf("expected 1 sent card, got %d", len(p.sentCards))
	}
	card := p.sentCards[0]
	if card.Header == nil || card.Header.Color != "orange" {
		t.Errorf("expected orange header, got %+v", card.Header)
	}
	if !card.HasButtons() {
		t.Error("expected card to have buttons")
	}
	buttons := card.CollectButtons()
	if len(buttons) < 2 {
		t.Fatalf("expected at least 2 button rows, got %d", len(buttons))
	}
	if buttons[0][0].Data != "perm:allow" {
		t.Errorf("expected first button data=perm:allow, got %s", buttons[0][0].Data)
	}
	if buttons[0][1].Data != "perm:deny" {
		t.Errorf("expected second button data=perm:deny, got %s", buttons[0][1].Data)
	}
	if buttons[1][0].Data != "perm:allow_all" {
		t.Errorf("expected third button data=perm:allow_all, got %s", buttons[1][0].Data)
	}
	if len(p.sent) != 0 {
		t.Errorf("plain text should not be sent when card is used, got %v", p.sent)
	}

	// Verify Extra fields carry i18n labels and body for card callback updates
	var allowBtn, denyBtn CardButton
	for _, elem := range card.Elements {
		if actions, ok := elem.(CardActions); ok {
			for _, btn := range actions.Buttons {
				switch btn.Value {
				case "perm:allow":
					allowBtn = btn
				case "perm:deny":
					denyBtn = btn
				}
			}
		}
	}
	if allowBtn.Extra == nil {
		t.Fatal("allow button should have Extra map")
	}
	if allowBtn.Extra["perm_color"] != "green" {
		t.Errorf("allow button perm_color should be green, got %s", allowBtn.Extra["perm_color"])
	}
	if allowBtn.Extra["perm_body"] == "" {
		t.Error("allow button perm_body should not be empty")
	}
	if !strings.Contains(allowBtn.Extra["perm_label"], "Allow") {
		t.Errorf("allow button perm_label should contain 'Allow', got %s", allowBtn.Extra["perm_label"])
	}
	if denyBtn.Extra["perm_color"] != "red" {
		t.Errorf("deny button perm_color should be red, got %s", denyBtn.Extra["perm_color"])
	}
}

func TestSendPermissionPrompt_InlineButtonPlatform(t *testing.T) {
	e := newTestEngine()
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}

	e.sendPermissionPrompt(p, "ctx", "full prompt text", "write_file", "/tmp/test.txt")

	if p.buttonContent != "full prompt text" {
		t.Errorf("expected button content to be prompt, got %s", p.buttonContent)
	}
	if len(p.buttonRows) < 2 {
		t.Fatalf("expected at least 2 button rows, got %d", len(p.buttonRows))
	}
	if p.buttonRows[0][0].Data != "perm:allow" {
		t.Errorf("expected perm:allow, got %s", p.buttonRows[0][0].Data)
	}
}

func TestSendPermissionPrompt_PlainPlatform(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "plain"}

	e.sendPermissionPrompt(p, "ctx", "full prompt text", "write_file", "/tmp/test.txt")

	if len(p.sent) != 1 || p.sent[0] != "full prompt text" {
		t.Errorf("expected plain text fallback, got %v", p.sent)
	}
}

func TestCmdList_MultiWorkspaceUsesWorkspaceSessions(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	globalAgent := &stubListAgent{
		sessions: []AgentSessionInfo{
			{ID: "g1", Summary: "Global One", MessageCount: 1},
		},
	}
	e := NewEngine("test", globalAgent, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindingPath)

	wsDir := filepath.Join(baseDir, "ws1")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Normalize the path so it matches what resolveWorkspace/getOrCreateWorkspaceAgent will use
	normalizedWsDir := normalizeWorkspacePath(wsDir)
	channelID := "C123"
	e.workspaceBindings.Bind("project:test", channelID, "chan", normalizedWsDir)

	ws := e.workspacePool.GetOrCreate(normalizedWsDir)
	ws.agent = &stubListAgent{
		sessions: []AgentSessionInfo{
			{ID: "w1", Summary: "Workspace One", MessageCount: 2},
		},
	}
	ws.sessions = NewSessionManager("")

	msg := &Message{SessionKey: "slack:" + channelID + ":U1", ReplyCtx: "ctx"}
	e.cmdList(p, msg, nil)

	if len(p.sent) == 0 {
		t.Fatal("expected /list to send a response")
	}
	if strings.Contains(p.sent[0], "Global One") {
		t.Fatalf("expected workspace sessions, got global list: %q", p.sent[0])
	}
	if !strings.Contains(p.sent[0], "Workspace One") {
		t.Fatalf("expected workspace list to contain session summary, got %q", p.sent[0])
	}
}

func TestHandlePendingPermission_MultiWorkspaceLookup(t *testing.T) {
	e := newTestEngine()

	// Set up multi-workspace with proper bindings so interactiveKeyForSessionKey works
	wsDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(t.TempDir(), bindingPath)

	channelID := "C123"
	e.workspaceBindings.Bind("project:test", channelID, "chan", wsDir)

	sessionKey := "slack:" + channelID + ":U1"
	// interactiveKeyForSessionKey resolves symlinks, so use the normalized path
	interactiveKey := normalizeWorkspacePath(wsDir) + ":" + sessionKey

	pending := &pendingPermission{
		RequestID: "req-1",
		ToolInput: map[string]any{"path": "/tmp/x"},
		Resolved:  make(chan struct{}),
	}
	session := &recordingAgentSession{}

	e.interactiveMu.Lock()
	e.interactiveStates[interactiveKey] = &interactiveState{
		agentSession: session,
		pending:      pending,
	}
	e.interactiveMu.Unlock()

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: sessionKey, ReplyCtx: "ctx"}

	if !e.handlePendingPermission(p, msg, "allow") {
		t.Fatal("expected pending permission to be handled")
	}

	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state == nil {
		t.Fatal("expected interactive state to remain")
	}
	state.mu.Lock()
	hasPending := state.pending != nil
	state.mu.Unlock()
	if hasPending {
		t.Fatal("expected pending permission to be cleared")
	}

	select {
	case <-pending.Resolved:
	default:
		t.Fatal("expected pending permission to be resolved")
	}

	if session.calls != 1 {
		t.Fatalf("RespondPermission calls = %d, want 1", session.calls)
	}
	if session.lastID != "req-1" {
		t.Fatalf("RespondPermission id = %q, want %q", session.lastID, "req-1")
	}
	if session.lastResult.Behavior != "allow" {
		t.Fatalf("RespondPermission behavior = %q, want %q", session.lastResult.Behavior, "allow")
	}
}

func TestHandleMessage_MultiWorkspacePreservesCCSessionKey(t *testing.T) {
	p := &stubPlatformEngine{n: "discord"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindingPath)

	wsDir := filepath.Join(baseDir, "ws1")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	normalizedWsDir := normalizeWorkspacePath(wsDir)
	channelID := "C123"
	e.workspaceBindings.Bind("project:test", channelID, "chan", normalizedWsDir)

	wsAgent := &sessionEnvRecordingAgent{session: newResultAgentSession("ok")}
	ws := e.workspacePool.GetOrCreate(normalizedWsDir)
	ws.agent = wsAgent
	ws.sessions = NewSessionManager("")

	msg := &Message{
		SessionKey: "discord:" + channelID + ":U1",
		Platform:   "discord",
		UserID:     "U1",
		UserName:   "user",
		Content:    "hello",
		ReplyCtx:   "ctx",
	}
	e.handleMessage(p, msg)

	deadline := time.After(2 * time.Second)
	for {
		if got := wsAgent.EnvValue("CC_SESSION_KEY"); got != "" {
			if got != msg.SessionKey {
				t.Fatalf("CC_SESSION_KEY = %q, want %q", got, msg.SessionKey)
			}
			if strings.Contains(got, normalizedWsDir) {
				t.Fatalf("CC_SESSION_KEY leaked workspace path: %q", got)
			}
			return
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for CC_SESSION_KEY to be injected")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestHandleMessage_AutoResetOnIdle_RotatesToNewSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agentSession := newResultAgentSession("fresh reply")
	agent := &resultAgent{session: agentSession}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetResetOnIdle(60 * time.Minute)

	key := "test:user1"
	old := e.sessions.GetOrCreateActive(key)
	old.AddHistory("user", "stale context")
	old.SetAgentSessionID("old-session", "stub")
	staleAt := time.Now().Add(-2 * time.Hour)
	old.mu.Lock()
	old.UpdatedAt = staleAt
	old.mu.Unlock()

	msg := &Message{
		SessionKey: key,
		Platform:   "test",
		UserID:     "u1",
		UserName:   "user",
		Content:    "hello after idle",
		ReplyCtx:   "ctx",
	}
	e.handleMessage(p, msg)

	deadline := time.After(2 * time.Second)
	for {
		active := e.sessions.GetOrCreateActive(key)
		sent := p.getSent()
		if active.ID != old.ID && len(active.GetHistory(0)) >= 2 && len(sent) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for idle auto-reset, sent=%v active=%s old=%s", sent, active.ID, old.ID)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	active := e.sessions.GetOrCreateActive(key)
	if active.ID == old.ID {
		t.Fatal("expected a new active session after idle auto-reset")
	}
	if got := old.GetAgentSessionID(); got != "old-session" {
		t.Fatalf("old session agent id = %q, want old-session preserved", got)
	}
	if got := len(old.GetHistory(0)); got != 1 {
		t.Fatalf("old session history len = %d, want 1 preserved entry", got)
	}
	if got := old.GetUpdatedAt(); !got.Equal(staleAt) {
		t.Fatalf("old session updated_at = %v, want unchanged %v", got, staleAt)
	}

	history := active.GetHistory(0)
	if len(history) != 2 {
		t.Fatalf("new session history len = %d, want 2", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello after idle" {
		t.Fatalf("unexpected first history entry: %#v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "fresh reply" {
		t.Fatalf("unexpected second history entry: %#v", history[1])
	}

	sent := p.getSent()
	if !strings.Contains(sent[0], "Session auto-reset") {
		t.Fatalf("first reply = %q, want auto-reset notice", sent[0])
	}
	if got := sent[len(sent)-1]; got != "fresh reply" {
		t.Fatalf("final reply = %q, want fresh reply", got)
	}
}

func TestHandleMessage_AutoResetOnIdle_DoesNotRotateFreshSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agentSession := newResultAgentSession("normal reply")
	agent := &resultAgent{session: agentSession}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetResetOnIdle(60 * time.Minute)

	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	session.AddHistory("user", "recent context")
	session.SetAgentSessionID("existing-session", "stub")
	recentAt := time.Now().Add(-5 * time.Minute)
	session.mu.Lock()
	session.UpdatedAt = recentAt
	session.mu.Unlock()

	msg := &Message{
		SessionKey: key,
		Platform:   "test",
		UserID:     "u1",
		UserName:   "user",
		Content:    "follow up",
		ReplyCtx:   "ctx",
	}
	e.handleMessage(p, msg)

	deadline := time.After(2 * time.Second)
	for {
		if len(session.GetHistory(0)) >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for normal turn, sent=%v", p.getSent())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	active := e.sessions.GetOrCreateActive(key)
	if active.ID != session.ID {
		t.Fatalf("active session = %s, want unchanged %s", active.ID, session.ID)
	}
	sent := p.getSent()
	for _, line := range sent {
		if strings.Contains(line, "Session auto-reset") {
			t.Fatalf("unexpected auto-reset notice in replies: %v", sent)
		}
	}
}

func TestHandleMessage_AutoResetOnIdle_DoesNotTriggerForSlashCommand(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetResetOnIdle(60 * time.Minute)

	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	session.AddHistory("user", "stale context")
	session.SetAgentSessionID("old-session", "stub")
	staleAt := time.Now().Add(-2 * time.Hour)
	session.mu.Lock()
	session.UpdatedAt = staleAt
	session.mu.Unlock()

	msg := &Message{
		SessionKey: key,
		Platform:   "test",
		UserID:     "u1",
		UserName:   "user",
		Content:    "/list",
		ReplyCtx:   "ctx",
	}
	e.handleMessage(p, msg)

	active := e.sessions.GetOrCreateActive(key)
	if active.ID != session.ID {
		t.Fatalf("active session = %s, want unchanged %s", active.ID, session.ID)
	}
	for _, line := range p.getSent() {
		if strings.Contains(line, "Session auto-reset") {
			t.Fatalf("unexpected auto-reset notice for slash command: %v", p.getSent())
		}
	}
}

func TestConfigItems_ThinkingMessagesToggle(t *testing.T) {
	e := newTestEngine()
	items := e.configItems()

	var item *configItem
	for i := range items {
		if items[i].key == "thinking_messages" {
			item = &items[i]
			break
		}
	}
	if item == nil {
		t.Fatal("expected thinking_messages config item")
	}
	if err := item.setFunc("false"); err != nil {
		t.Fatalf("set thinking_messages: %v", err)
	}
	if e.display.ThinkingMessages {
		t.Fatal("expected thinking messages to be disabled")
	}
}

func TestReplyWithCard_FallsBackToTextWhenPlatformHasNoCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	card := NewCard().Title("Help", "blue").Markdown("Plain fallback").Build()

	e.replyWithCard(p, "ctx", card)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if got, want := p.sent[0], card.RenderText(); got != want {
		t.Fatalf("fallback text = %q, want %q", got, want)
	}
}

func TestReplyWithCard_UsesCardSenderWhenSupported(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "card"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	card := NewCard().Markdown("Interactive").Build()

	e.replyWithCard(p, "ctx", card)

	if len(p.repliedCards) != 1 {
		t.Fatalf("replied cards = %d, want 1", len(p.repliedCards))
	}
	if len(p.sent) != 0 {
		t.Fatalf("plain replies = %d, want 0", len(p.sent))
	}
}

func TestReply_DoesNotTransformLocalReferencesWhenEnabled(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	a := &namedStubModelModeAgent{name: "codex"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	e.SetBaseWorkDir("/root/code/demo")
	e.SetReferenceConfig(ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	})

	e.reply(p, "ctx", "See /root/code/demo/src/app.ts:42")

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if got := p.sent[0]; got != "See /root/code/demo/src/app.ts:42" {
		t.Fatalf("reply content = %q, want raw path", got)
	}
}

func TestReplyWithCard_DoesNotTransformMarkdownOrFallback(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	a := &namedStubModelModeAgent{name: "codex"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	e.SetBaseWorkDir("/root/code/demo")
	e.SetReferenceConfig(ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "basename",
		MarkerStyle:     "ascii",
		EnclosureStyle:  "code",
	})
	card := NewCard().Markdown("Inspect /root/code/demo/src/app.ts:42").Build()

	e.replyWithCard(p, "ctx", card)

	if len(p.repliedCards) != 1 {
		t.Fatalf("replied cards = %d, want 1", len(p.repliedCards))
	}
	rendered := p.repliedCards[0]
	md, ok := rendered.Elements[0].(CardMarkdown)
	if !ok {
		t.Fatalf("first card element = %T, want CardMarkdown", rendered.Elements[0])
	}
	if md.Content != "Inspect /root/code/demo/src/app.ts:42" {
		t.Fatalf("card markdown = %q, want raw reference", md.Content)
	}
	if got := rendered.RenderText(); !strings.Contains(got, "/root/code/demo/src/app.ts:42") {
		t.Fatalf("fallback RenderText() = %q, want raw reference", got)
	}
}

func TestCmdHelp_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdHelp(p, msg)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if got := p.sent[0]; got != e.i18n.T(MsgHelp) {
		t.Fatalf("help text = %q, want legacy help text", got)
	}
	if strings.Contains(p.sent[0], "cc-connect 帮助") {
		t.Fatalf("help text = %q, should not be card title fallback", p.sent[0])
	}
}

func TestCmdList_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	sessions := []AgentSessionInfo{{ID: "session-a", Summary: "First session", MessageCount: 3, ModifiedAt: time.Date(2026, 3, 11, 2, 0, 0, 0, time.UTC)}}
	e := NewEngine("test", &stubListAgent{sessions: sessions}, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdList(p, msg, nil)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "Sessions") {
		t.Fatalf("list text = %q, want legacy list title", p.sent[0])
	}
	if strings.Contains(p.sent[0], "[← 返回]") {
		t.Fatalf("list text = %q, should not be card fallback text", p.sent[0])
	}
}

func TestCmdCurrent_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	session := e.sessions.GetOrCreateActive(msg.SessionKey)
	session.Name = "Focus"
	session.SetAgentSessionID("session-123", "test")
	session.History = append(session.History, HistoryEntry{Role: "user", Content: "hello", Timestamp: time.Now()})

	e.cmdCurrent(p, msg)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "Current session") {
		t.Fatalf("current text = %q, want legacy current session text", p.sent[0])
	}
	if strings.Contains(p.sent[0], "cc-connect") {
		t.Fatalf("current text = %q, should not be card fallback title", p.sent[0])
	}
}

func TestCmdDelete_BatchCommaList(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
		{ID: "session-4", Summary: "Four"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, []string{"1,2,3"})

	if got, want := strings.Join(agent.deleted, ","), "session-1,session-2,session-3"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "Session deleted: One") || !strings.Contains(p.sent[0], "Session deleted: Three") {
		t.Fatalf("reply = %q, want combined delete summary", p.sent[0])
	}
}

func TestCmdDelete_BatchRange(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
		{ID: "session-4", Summary: "Four"},
		{ID: "session-5", Summary: "Five"},
		{ID: "session-6", Summary: "Six"},
		{ID: "session-7", Summary: "Seven"},
		{ID: "session-8", Summary: "Eight"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, []string{"3-7"})

	if got, want := strings.Join(agent.deleted, ","), "session-3,session-4,session-5,session-6,session-7"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
}

func TestCmdDelete_BatchMixedSyntax(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
		{ID: "session-4", Summary: "Four"},
		{ID: "session-5", Summary: "Five"},
		{ID: "session-6", Summary: "Six"},
		{ID: "session-7", Summary: "Seven"},
		{ID: "session-8", Summary: "Eight"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, []string{"1,3-5,8"})

	if got, want := strings.Join(agent.deleted, ","), "session-1,session-3,session-4,session-5,session-8"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
}

func TestCmdDelete_InvalidExplicitBatchSyntaxShowsUsage(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, []string{"1,3-a,8"})

	if len(agent.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", agent.deleted)
	}
	if len(p.sent) != 1 || p.sent[0] != e.i18n.T(MsgDeleteUsage) {
		t.Fatalf("sent = %v, want usage", p.sent)
	}
}

func TestCmdDelete_WhitespaceSeparatedArgsAreRejected(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, []string{"1", "2", "3"})

	if len(agent.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", agent.deleted)
	}
	if len(p.sent) != 1 || p.sent[0] != e.i18n.T(MsgDeleteUsage) {
		t.Fatalf("sent = %v, want usage", p.sent)
	}
}

func TestCmdDelete_SingleSessionPrefixStillWorks(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "abc123456789", Summary: "One"},
		{ID: "def987654321", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, []string{"abc123"})

	if got, want := strings.Join(agent.deleted, ","), "abc123456789"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
}

func TestCmdDelete_SyncsLocalSessionSnapshot(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	victim := e.sessions.NewSession("test:user2", "victim")
	victim.SetAgentSessionID("session-1", "stub")
	keep := e.sessions.NewSession("test:user3", "keep")
	keep.SetAgentSessionID("session-2", "stub")

	e.cmdDelete(p, msg, []string{"1"})

	if got, want := strings.Join(agent.deleted, ","), "session-1"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
	if got := e.sessions.FindByID(victim.ID); got != nil {
		t.Fatalf("victim session should be removed, got %+v", got)
	}
	if got := e.sessions.FindByID(keep.ID); got == nil {
		t.Fatal("keep session should remain")
	}
}

func TestCmdDelete_NoArgsOnCardPlatformShowsDeleteModeCard(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)

	if len(p.repliedCards) != 1 {
		t.Fatalf("replied cards = %d, want 1", len(p.repliedCards))
	}
	card := p.repliedCards[0]
	if got := countCardActionValues(card, "act:/delete-mode toggle "); got != 2 {
		t.Fatalf("toggle action count = %d, want 2", got)
	}
	if _, ok := findCardAction(card, "act:/delete-mode cancel"); !ok {
		t.Fatal("expected delete mode cancel action")
	}
}

func TestDeleteMode_ToggleSelectionReturnsUpdatedCard(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)
	card := e.handleCardNav("act:/delete-mode toggle session-2", msg.SessionKey)
	if card == nil {
		t.Fatal("expected card update after toggle")
	}
	if !strings.Contains(card.RenderText(), "1 selected") {
		t.Fatalf("card text = %q, want selected count", card.RenderText())
	}

	confirmCard := e.handleCardNav("act:/delete-mode confirm", msg.SessionKey)
	if confirmCard == nil {
		t.Fatal("expected confirmation card")
	}
	if !strings.Contains(confirmCard.RenderText(), "Two") {
		t.Fatalf("confirmation text = %q, want selected session", confirmCard.RenderText())
	}
}

func TestDeleteMode_ConfirmAndSubmitDeletesSelectedSessions(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)
	_ = e.handleCardNav("act:/delete-mode toggle session-1", msg.SessionKey)
	_ = e.handleCardNav("act:/delete-mode toggle session-3", msg.SessionKey)

	confirmCard := e.handleCardNav("act:/delete-mode confirm", msg.SessionKey)
	if confirmCard == nil {
		t.Fatal("expected confirmation card")
	}
	confirmText := confirmCard.RenderText()
	if !strings.Contains(confirmText, "One") || !strings.Contains(confirmText, "Three") {
		t.Fatalf("confirmation text = %q, want selected session names", confirmText)
	}

	resultCard := e.handleCardNav("act:/delete-mode submit", msg.SessionKey)
	if resultCard == nil {
		t.Fatal("expected deleting card after submit")
	}
	// Submit is now async; the returned card is a "deleting" indicator.
	// Wait for the background goroutine to complete and push the result card.
	waitDeleteModePhase(t, e, msg.SessionKey, "result")
	if got, want := strings.Join(agent.deleted, ","), "session-1,session-3"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
	refreshed := p.getRefreshedCards()
	if len(refreshed) == 0 {
		t.Fatal("expected refreshed result card via RefreshCard")
	}
	pushedCard := refreshed[len(refreshed)-1]
	if !strings.Contains(pushedCard.RenderText(), "Session deleted: One") {
		t.Fatalf("result text = %q, want delete result", pushedCard.RenderText())
	}
}

func TestDeleteMode_SubmitReportsMissingSelectedSessions(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)
	_ = e.handleCardNav("act:/delete-mode toggle session-1", msg.SessionKey)
	_ = e.handleCardNav("act:/delete-mode toggle session-3", msg.SessionKey)

	agent.sessions = []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}

	resultCard := e.handleCardNav("act:/delete-mode submit", msg.SessionKey)
	if resultCard == nil {
		t.Fatal("expected deleting card after submit")
	}
	// Wait for async deletion to complete.
	waitDeleteModePhase(t, e, msg.SessionKey, "result")
	refreshed := p.getRefreshedCards()
	if len(refreshed) == 0 {
		t.Fatal("expected refreshed result card via RefreshCard")
	}
	pushedCard := refreshed[len(refreshed)-1]
	resultText := pushedCard.RenderText()
	if !strings.Contains(resultText, "Session deleted: One") {
		t.Fatalf("result text = %q, want deleted session line", resultText)
	}
	if !strings.Contains(resultText, "Missing selected session") || !strings.Contains(resultText, "session-3") {
		t.Fatalf("result text = %q, want missing selected session to be reported", resultText)
	}
}

func TestDeleteMode_CancelReturnsListCard(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)
	card := e.handleCardNav("act:/delete-mode cancel", msg.SessionKey)
	if card == nil {
		t.Fatal("expected list card after cancel")
	}
	if got := countCardActionValues(card, "act:/switch "); got != 2 {
		t.Fatalf("switch action count = %d, want 2", got)
	}
}

func TestDeleteMode_ConfirmWithoutSelectionShowsHint(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)
	card := e.handleCardNav("act:/delete-mode confirm", msg.SessionKey)
	if card == nil {
		t.Fatal("expected delete mode card when confirming empty selection")
	}
	if !strings.Contains(card.RenderText(), "Select at least one session.") {
		t.Fatalf("card text = %q, want empty-selection hint", card.RenderText())
	}
}

func TestDeleteMode_PageNavigationPreservesSelection(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sessions := make([]AgentSessionInfo, 0, 8)
	for i := 1; i <= 8; i++ {
		sessions = append(sessions, AgentSessionInfo{ID: fmt.Sprintf("session-%d", i), Summary: fmt.Sprintf("Session %d", i)})
	}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: sessions}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)
	_ = e.handleCardNav("act:/delete-mode toggle session-1", msg.SessionKey)
	pageTwo := e.handleCardNav("act:/delete-mode page 2", msg.SessionKey)
	if pageTwo == nil {
		t.Fatal("expected page 2 card")
	}
	if !strings.Contains(pageTwo.RenderText(), "1 selected") {
		t.Fatalf("page 2 text = %q, want preserved selected count", pageTwo.RenderText())
	}
	pageOne := e.handleCardNav("act:/delete-mode page 1", msg.SessionKey)
	if pageOne == nil {
		t.Fatal("expected page 1 card")
	}
	btn, ok := findCardAction(pageOne, "act:/delete-mode toggle session-1")
	if !ok {
		t.Fatal("expected toggle action for session-1")
	}
	if btn.Type != "primary" {
		t.Fatalf("selected button type = %q, want primary", btn.Type)
	}
}

func TestDeleteMode_SubmitBlocksActiveSession(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}
	e.sessions.GetOrCreateActive(msg.SessionKey).SetAgentSessionID("session-1", "test")

	e.cmdDelete(p, msg, nil)
	_ = e.handleCardNav("act:/delete-mode toggle session-1", msg.SessionKey)
	resultCard := e.handleCardNav("act:/delete-mode submit", msg.SessionKey)
	if resultCard == nil {
		t.Fatal("expected deleting card")
	}
	// Wait for async deletion to complete.
	waitDeleteModePhase(t, e, msg.SessionKey, "result")
	if len(agent.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", agent.deleted)
	}
	if len(p.getRefreshedCards()) == 0 {
		t.Fatal("expected refreshed result card via RefreshCard")
	}
	pushedCard := p.getRefreshedCards()[len(p.getRefreshedCards())-1]
	if !strings.Contains(pushedCard.RenderText(), "Cannot delete the currently active session") {
		t.Fatalf("result text = %q, want active-session warning", pushedCard.RenderText())
	}
}

func TestDeleteMode_ActiveSessionMarkedWithArrowAndNotSelectable(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}
	// Register both sessions so they pass the owned-session filter.
	s1 := e.sessions.GetOrCreateActive(msg.SessionKey)
	s1.SetAgentSessionID("session-1", "test")
	s2 := e.sessions.NewSession(msg.SessionKey, "two")
	s2.SetAgentSessionID("session-2", "test")
	// Switch back to s1 as the active session.
	e.sessions.SwitchSession(msg.SessionKey, s1.ID)

	e.cmdDelete(p, msg, nil)
	if len(p.repliedCards) != 1 {
		t.Fatalf("replied cards = %d, want 1", len(p.repliedCards))
	}
	card := p.repliedCards[0]
	if _, ok := findCardAction(card, "act:/delete-mode toggle session-1"); ok {
		t.Fatal("active session should not be toggle-selectable")
	}
	if _, ok := findCardAction(card, "act:/delete-mode noop session-1"); !ok {
		t.Fatal("expected noop action for active session")
	}
	if got := countCardActionValues(card, "act:/delete-mode toggle "); got != 1 {
		t.Fatalf("toggle action count = %d, want 1", got)
	}
	if !strings.Contains(card.RenderText(), "▶ **1.**") {
		t.Fatalf("card text = %q, want arrow marker for active session", card.RenderText())
	}
}

func TestDeleteMode_FormSubmitShowsConfirmThenDeletes(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubDeleteAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{
		{ID: "session-1", Summary: "One"},
		{ID: "session-2", Summary: "Two"},
		{ID: "session-3", Summary: "Three"},
	}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "feishu:user1", ReplyCtx: "ctx"}

	e.cmdDelete(p, msg, nil)
	confirmCard := e.handleCardNav("act:/delete-mode form-submit session-1,session-3", msg.SessionKey)
	if confirmCard == nil {
		t.Fatal("expected confirm card after form-submit")
	}
	if len(agent.deleted) != 0 {
		t.Fatalf("deleted = %v, want none before confirm", agent.deleted)
	}
	confirmText := confirmCard.RenderText()
	if !strings.Contains(confirmText, "One") || !strings.Contains(confirmText, "Three") {
		t.Fatalf("confirm text = %q, want selected sessions", confirmText)
	}

	resultCard := e.handleCardNav("act:/delete-mode submit", msg.SessionKey)
	if resultCard == nil {
		t.Fatal("expected deleting card after submit")
	}
	// Wait for async deletion to complete.
	waitDeleteModePhase(t, e, msg.SessionKey, "result")
	if got, want := strings.Join(agent.deleted, ","), "session-1,session-3"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
	refreshed := p.getRefreshedCards()
	if len(refreshed) == 0 {
		t.Fatal("expected pushed result card via RefreshCard")
	}
	pushedCard := refreshed[len(refreshed)-1]
	if !strings.Contains(pushedCard.RenderText(), "Session deleted: One") {
		t.Fatalf("result text = %q, want delete result", pushedCard.RenderText())
	}
}

func TestExecuteCardActionStop_RemovesInteractiveState(t *testing.T) {
	e := newTestEngine()
	e.interactiveMu.Lock()
	e.interactiveStates["test:user1"] = &interactiveState{}
	e.interactiveMu.Unlock()

	e.executeCardAction("/stop", "", "test:user1")

	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	if state != nil {
		t.Fatal("expected interactive state to be removed")
	}
}

func TestCmdLang_UsesInlineButtonsOnButtonOnlyPlatform(t *testing.T) {
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "inline-only"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	e.cmdLang(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.buttonRows) == 0 {
		t.Fatal("expected /lang to send inline buttons on button-only platform")
	}
	if got := p.buttonRows[0][0].Data; got != "cmd:/lang en" {
		t.Fatalf("first /lang button = %q, want %q", got, "cmd:/lang en")
	}
}

func TestCmdLang_UsesPlainTextChoicesOnPlatformWithoutCardsOrButtons(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	e.cmdLang(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "/lang en") || !strings.Contains(p.sent[0], "/lang auto") {
		t.Fatalf("lang text = %q, want plain-text language choices", p.sent[0])
	}
}

func TestCmdProvider_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubProviderAgent{
		providers: []ProviderConfig{
			{Name: "openai", BaseURL: "https://api.openai.com", Model: "gpt-4.1"},
			{Name: "azure", BaseURL: "https://azure.example", Model: "gpt-4.1-mini"},
		},
		active: "openai",
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.cmdProvider(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "Active provider") {
		t.Fatalf("provider text = %q, want current provider section", p.sent[0])
	}
	if !strings.Contains(p.sent[0], "openai") || !strings.Contains(p.sent[0], "azure") {
		t.Fatalf("provider text = %q, want provider list", p.sent[0])
	}
	if !strings.Contains(p.sent[0], "switch") {
		t.Fatalf("provider text = %q, want switch hint", p.sent[0])
	}
}

func TestCmdModel_UsesInlineButtonsOnButtonOnlyPlatform(t *testing.T) {
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "inline-only"}}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.cmdModel(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.buttonRows) == 0 {
		t.Fatal("expected /model to send inline buttons on button-only platform")
	}
	if got := p.buttonRows[0][0].Data; got != "cmd:/model switch 1" {
		t.Fatalf("first /model button = %q, want %q", got, "cmd:/model switch 1")
	}
}

func TestCmdModel_UpdatesActiveProviderModel(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{
		model: "gpt-4.1-mini",
		providers: []ProviderConfig{
			{
				Name:   "openai",
				Model:  "gpt-4.1-mini",
				Models: []ModelOption{{Name: "gpt-4.1", Alias: "gpt"}, {Name: "gpt-4.1-mini", Alias: "mini"}},
			},
		},
		active: "openai",
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	var savedProvider, savedModel string
	e.SetProviderModelSaveFunc(func(providerName, model string) error {
		savedProvider = providerName
		savedModel = model
		return nil
	})
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.SetAgentSessionID("existing-session", "test")

	e.cmdModel(p, msg, []string{"switch", "gpt"})

	if agent.model != "gpt-4.1" {
		t.Fatalf("agent model = %q, want gpt-4.1", agent.model)
	}
	if got := agent.GetActiveProvider(); got == nil || got.Model != "gpt-4.1" {
		t.Fatalf("active provider model = %#v, want gpt-4.1", got)
	}
	if got := agent.GetModel(); got != "gpt-4.1" {
		t.Fatalf("GetModel() = %q, want gpt-4.1", got)
	}
	if savedProvider != "openai" || savedModel != "gpt-4.1" {
		t.Fatalf("saved provider/model = %q/%q, want openai/gpt-4.1", savedProvider, savedModel)
	}
	if active := e.sessions.GetOrCreateActive(msg.SessionKey); active.AgentSessionID != "" {
		t.Fatalf("session id = %q, want cleared after model switch", active.AgentSessionID)
	}
}

func TestCmdModel_DirectNameDoesNotNeedModelListMatch(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubStrictModelAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdModel(p, msg, []string{"switch", "custom/provider-model"})

	if agent.model != "custom/provider-model" {
		t.Fatalf("agent model = %q, want custom/provider-model", agent.model)
	}
	if agent.calls != 0 {
		t.Fatalf("AvailableModels calls = %d, want 0 for direct name switch", agent.calls)
	}
}

func TestCmdModel_AliasWithPunctuationStillResolves(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubStrictModelAgent{models: []ModelOption{{Name: "openai/gpt-4.1", Alias: "gpt-4.1"}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdModel(p, msg, []string{"switch", "gpt-4.1"})

	if agent.model != "openai/gpt-4.1" {
		t.Fatalf("agent model = %q, want openai/gpt-4.1", agent.model)
	}
	if agent.calls != 1 {
		t.Fatalf("AvailableModels calls = %d, want 1 for punctuated alias lookup", agent.calls)
	}
}

func TestCmdModel_AliasStillResolvesOnColdStart(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubStrictModelAgent{models: []ModelOption{{Name: "gpt-4.1", Alias: "gpt"}}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdModel(p, msg, []string{"switch", "gpt"})

	if agent.model != "gpt-4.1" {
		t.Fatalf("agent model = %q, want gpt-4.1", agent.model)
	}
}

func TestCmdModel_LegacySyntaxStillWorks(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdModel(p, msg, []string{"gpt"})

	if agent.model != "gpt-4.1" {
		t.Fatalf("agent model = %q, want gpt-4.1", agent.model)
	}
}

func TestCmdModel_SavesModelWhenNoActiveProvider(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{
		model: "gpt-4.1-mini",
		providers: []ProviderConfig{
			{
				Name:   "openai",
				Model:  "gpt-4.1-mini",
				Models: []ModelOption{{Name: "gpt-4.1", Alias: "gpt"}, {Name: "gpt-4.1-mini", Alias: "mini"}},
			},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	var savedModel string
	e.SetModelSaveFunc(func(model string) error {
		savedModel = model
		return nil
	})

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	e.cmdModel(p, msg, []string{"switch", "gpt"})

	if agent.model != "gpt-4.1" {
		t.Fatalf("agent model = %q, want gpt-4.1", agent.model)
	}
	if savedModel != "gpt-4.1" {
		t.Fatalf("saved model = %q, want gpt-4.1", savedModel)
	}
}

func TestCmdModel_DoesNotClaimSuccessWhenModelSaveFails(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{
		model: "gpt-4.1-mini",
		providers: []ProviderConfig{
			{
				Name:   "openai",
				Model:  "gpt-4.1-mini",
				Models: []ModelOption{{Name: "gpt-4.1", Alias: "gpt"}, {Name: "gpt-4.1-mini", Alias: "mini"}},
			},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetModelSaveFunc(func(model string) error {
		return errors.New("disk full")
	})

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.SetAgentSessionID("existing-session", "test")
	s.AddHistory("user", "keep me")

	e.cmdModel(p, msg, []string{"switch", "gpt"})

	if agent.model != "gpt-4.1-mini" {
		t.Fatalf("agent model = %q, want unchanged gpt-4.1-mini", agent.model)
	}
	if active := e.sessions.GetOrCreateActive(msg.SessionKey); active.AgentSessionID != "existing-session" {
		t.Fatalf("session id = %q, want existing-session after failure", active.AgentSessionID)
	}
	if active := e.sessions.GetOrCreateActive(msg.SessionKey); len(active.History) != 1 {
		t.Fatalf("history length = %d, want 1 after failure", len(active.History))
	}
	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0], "Failed to change model") {
		t.Fatalf("reply = %q, want model change failure message", sent[0])
	}
}

func TestCmdModel_MultiWorkspaceUsesWorkspaceAgentAndSessions(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	globalAgent := &stubModelModeAgent{model: "gpt-4.1-mini"}
	e := NewEngine("test", globalAgent, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindingPath)

	wsDir := normalizeWorkspacePath(t.TempDir())
	channelID := "C-model"
	e.workspaceBindings.Bind("project:test", channelID, "chan", wsDir)

	ws := e.workspacePool.GetOrCreate(wsDir)
	wsAgent := &stubModelModeAgent{model: "gpt-4.1-mini"}
	ws.agent = wsAgent
	ws.sessions = NewSessionManager("")

	msg := &Message{SessionKey: "feishu:" + channelID + ":u1", ReplyCtx: "ctx"}

	globalSession := e.sessions.GetOrCreateActive(msg.SessionKey)
	globalSession.SetAgentSessionID("global-session", "test")
	wsSession := ws.sessions.GetOrCreateActive(msg.SessionKey)
	wsSession.SetAgentSessionID("workspace-session", "test")

	e.cmdModel(p, msg, []string{"switch", "gpt"})

	if wsAgent.model != "gpt-4.1" {
		t.Fatalf("workspace agent model = %q, want gpt-4.1", wsAgent.model)
	}
	if globalAgent.model != "gpt-4.1-mini" {
		t.Fatalf("global agent model = %q, want unchanged", globalAgent.model)
	}
	if got := ws.sessions.GetOrCreateActive(msg.SessionKey).AgentSessionID; got != "" {
		t.Fatalf("workspace session id = %q, want cleared", got)
	}
	if got := e.sessions.GetOrCreateActive(msg.SessionKey).AgentSessionID; got != "global-session" {
		t.Fatalf("global session id = %q, want untouched", got)
	}
}

func TestCmdModel_MultiWorkspaceSwitchDoesNotMutateProviderModel(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	globalAgent := &stubModelModeAgent{model: "gpt-4.1-mini"}
	e := NewEngine("test", globalAgent, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindingPath)

	wsDir := normalizeWorkspacePath(t.TempDir())
	channelID := "C-model-provider"
	e.workspaceBindings.Bind("project:test", channelID, "chan", wsDir)

	ws := e.workspacePool.GetOrCreate(wsDir)
	wsAgent := &stubModelModeAgent{
		model: "gpt-4.1-mini",
		providers: []ProviderConfig{{
			Name:   "openai",
			Model:  "gpt-4.1-mini",
			Models: []ModelOption{{Name: "gpt-4.1", Alias: "gpt"}, {Name: "gpt-4.1-mini", Alias: "mini"}},
		}},
		active: "openai",
	}
	ws.agent = wsAgent
	ws.sessions = NewSessionManager("")

	msg := &Message{SessionKey: "feishu:" + channelID + ":u1", ReplyCtx: "ctx"}

	e.cmdModel(p, msg, []string{"switch", "gpt"})

	if wsAgent.model != "gpt-4.1" {
		t.Fatalf("workspace agent model = %q, want gpt-4.1", wsAgent.model)
	}
	if got := wsAgent.GetActiveProvider(); got == nil || got.Model != "gpt-4.1-mini" {
		t.Fatalf("workspace active provider = %#v, want unchanged model gpt-4.1-mini", got)
	}
}

func TestGetOrCreateWorkspaceAgent_InheritsActiveProvider(t *testing.T) {
	agentName := "test-workspace-provider-inherit"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		agent := &namedStubModelModeAgent{name: agentName}
		if model, ok := opts["model"].(string); ok {
			agent.model = model
		}
		if mode, ok := opts["mode"].(string); ok {
			agent.mode = mode
		}
		return agent, nil
	})

	globalAgent := &namedStubModelModeAgent{
		name: agentName,
		stubModelModeAgent: stubModelModeAgent{
			model: "gpt-4.1-mini",
			mode:  "default",
			providers: []ProviderConfig{
				{Name: "openai", Model: "gpt-4.1-mini"},
				{Name: "azure", Model: "gpt-4.1"},
			},
			active: "azure",
		},
	}
	e := NewEngine("test", globalAgent, []Platform{&stubPlatformEngine{n: "plain"}}, "", LangEnglish)
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))

	wsAgentRaw, _, err := e.getOrCreateWorkspaceAgent(normalizeWorkspacePath(t.TempDir()))
	if err != nil {
		t.Fatalf("getOrCreateWorkspaceAgent returned error: %v", err)
	}

	wsAgent, ok := wsAgentRaw.(*namedStubModelModeAgent)
	if !ok {
		t.Fatalf("workspace agent type = %T, want *namedStubModelModeAgent", wsAgentRaw)
	}
	if wsAgent.model != "gpt-4.1-mini" {
		t.Fatalf("workspace model = %q, want inherited global model", wsAgent.model)
	}
	if got := wsAgent.GetActiveProvider(); got == nil || got.Name != "azure" {
		t.Fatalf("workspace active provider = %#v, want azure", got)
	}
}

func TestGetOrCreateWorkspaceAgent_InheritsSnapshotOptions(t *testing.T) {
	agentName := "test-workspace-option-snapshot"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		snapshot := make(map[string]any, len(opts))
		for k, v := range opts {
			snapshot[k] = v
		}
		return &namedStubWorkspaceOptionAgent{
			namedStubModelModeAgent: namedStubModelModeAgent{
				name: agentName,
				stubModelModeAgent: stubModelModeAgent{
					model:           "gpt-5.4",
					mode:            "yolo",
					reasoningEffort: "high",
				},
			},
			opts: snapshot,
		}, nil
	})

	globalAgent := &namedStubWorkspaceOptionAgent{
		namedStubModelModeAgent: namedStubModelModeAgent{
			name: agentName,
			stubModelModeAgent: stubModelModeAgent{
				model:           "gpt-5.4",
				mode:            "yolo",
				reasoningEffort: "high",
			},
		},
		opts: map[string]any{
			"backend":          "app_server",
			"app_server_url":   "ws://127.0.0.1:3846",
			"codex_home":       "/tmp/codex-home",
			"reasoning_effort": "high",
			"mode":             "yolo",
			"model":            "gpt-5.4",
			"run_as_user":      "workspace-snapshot-user",
			"run_as_env":       []string{"SNAPSHOT_ONLY"},
		},
		runAsUser: "fallback-user",
		runAsEnv:  []string{"FALLBACK_ONLY"},
	}
	e := NewEngine("test", globalAgent, []Platform{&stubPlatformEngine{n: "plain"}}, "", LangEnglish)
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))

	workspace := normalizeWorkspacePath(t.TempDir())
	wsAgentRaw, _, err := e.getOrCreateWorkspaceAgent(workspace)
	if err != nil {
		t.Fatalf("getOrCreateWorkspaceAgent returned error: %v", err)
	}

	wsAgent, ok := wsAgentRaw.(*namedStubWorkspaceOptionAgent)
	if !ok {
		t.Fatalf("workspace agent type = %T, want *namedStubWorkspaceOptionAgent", wsAgentRaw)
	}
	if got := wsAgent.opts["backend"]; got != "app_server" {
		t.Fatalf("workspace backend = %#v, want app_server", got)
	}
	if got := wsAgent.opts["app_server_url"]; got != "ws://127.0.0.1:3846" {
		t.Fatalf("workspace app_server_url = %#v, want ws://127.0.0.1:3846", got)
	}
	if got := wsAgent.opts["codex_home"]; got != "/tmp/codex-home" {
		t.Fatalf("workspace codex_home = %#v, want /tmp/codex-home", got)
	}
	if got := wsAgent.opts["reasoning_effort"]; got != "high" {
		t.Fatalf("workspace reasoning_effort = %#v, want high", got)
	}
	if got := wsAgent.opts["work_dir"]; got != workspace {
		t.Fatalf("workspace work_dir = %#v, want %q", got, workspace)
	}
	if got := wsAgent.opts["run_as_user"]; got != "workspace-snapshot-user" {
		t.Fatalf("workspace run_as_user = %#v, want snapshot value", got)
	}
	gotRunAsEnv, _ := wsAgent.opts["run_as_env"].([]string)
	if len(gotRunAsEnv) != 1 || gotRunAsEnv[0] != "SNAPSHOT_ONLY" {
		t.Fatalf("workspace run_as_env = %#v, want snapshot value", wsAgent.opts["run_as_env"])
	}
}

func TestWorkspaceContext_PerChannelIndependence(t *testing.T) {
	agentName := "test-workspace-context-dir-override"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		agent := &namedStubWorkDirAgent{name: agentName}
		if workDir, ok := opts["work_dir"].(string); ok {
			agent.workDir = workDir
		}
		return agent, nil
	})

	workspace := normalizeWorkspacePath(t.TempDir())
	dirA := filepath.Join(workspace, "channelA")
	dirB := filepath.Join(workspace, "channelB")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	store := NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "test.state.json"))
	keyA := workspace + ":feishu:oc_aaa:ou_111"
	keyB := workspace + ":feishu:oc_bbb:ou_222"
	store.SetWorkspaceDirOverride(keyA, dirA)
	store.SetWorkspaceDirOverride(keyB, dirB)
	store.Save()

	e := NewEngine("test", &namedStubWorkDirAgent{name: agentName, stubWorkDirAgent: stubWorkDirAgent{workDir: workspace}}, []Platform{&stubPlatformEngine{n: "plain"}}, "", LangEnglish)
	e.SetMultiWorkspace(workspace, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetProjectStateStore(store)

	agentA, sessionsA, interactiveKeyA, effectiveDirA, err := e.workspaceContext(workspace, "feishu:oc_aaa:ou_111")
	if err != nil {
		t.Fatalf("workspaceContext A error: %v", err)
	}
	agentB, sessionsB, interactiveKeyB, effectiveDirB, err := e.workspaceContext(workspace, "feishu:oc_bbb:ou_222")
	if err != nil {
		t.Fatalf("workspaceContext B error: %v", err)
	}

	if interactiveKeyA != keyA {
		t.Fatalf("interactiveKeyA = %q, want %q", interactiveKeyA, keyA)
	}
	if interactiveKeyB != keyB {
		t.Fatalf("interactiveKeyB = %q, want %q", interactiveKeyB, keyB)
	}
	if effectiveDirA != dirA {
		t.Fatalf("effectiveDirA = %q, want %q", effectiveDirA, dirA)
	}
	if effectiveDirB != dirB {
		t.Fatalf("effectiveDirB = %q, want %q", effectiveDirB, dirB)
	}
	if agentA == agentB {
		t.Fatal("workspaceContext returned same agent for different effective dirs")
	}
	if sessionsA == sessionsB {
		t.Fatal("workspaceContext returned same session manager for different effective dirs")
	}
	if got := agentA.(interface{ GetWorkDir() string }).GetWorkDir(); got != dirA {
		t.Fatalf("agentA workDir = %q, want %q", got, dirA)
	}
	if got := agentB.(interface{ GetWorkDir() string }).GetWorkDir(); got != dirB {
		t.Fatalf("agentB workDir = %q, want %q", got, dirB)
	}
}

func TestCmdDir_ShowsCurrentDirectory(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubWorkDirAgent{workDir: "/tmp/project-a"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.cmdDir(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "/tmp/project-a") {
		t.Fatalf("sent = %q, want current work dir", p.sent[0])
	}
}

func TestCmdDir_SwitchesDirectoryAndResetsSession(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	tempDir := t.TempDir()
	nextDir := filepath.Join(tempDir, "next")
	if err := os.Mkdir(nextDir, 0o755); err != nil {
		t.Fatalf("mkdir next dir: %v", err)
	}

	agent := &stubWorkDirAgent{workDir: tempDir}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.SetAgentSessionID("existing-session", "test")
	s.AddHistory("user", "hello")

	e.cmdDir(p, msg, []string{"next"})

	if agent.workDir != nextDir {
		t.Fatalf("workDir = %q, want %q", agent.workDir, nextDir)
	}
	if s.GetAgentSessionID() != "" {
		t.Fatalf("AgentSessionID = %q, want cleared", s.GetAgentSessionID())
	}
	if len(s.History) != 0 {
		t.Fatalf("history length = %d, want 0", len(s.History))
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], nextDir) {
		t.Fatalf("sent = %v, want directory changed message", p.sent)
	}
}

func TestCmdDir_RejectsMissingDirectory(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	tempDir := t.TempDir()
	missingDir := filepath.Join(tempDir, "missing")
	agent := &stubWorkDirAgent{workDir: tempDir}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.cmdDir(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, []string{"missing"})

	if agent.workDir != tempDir {
		t.Fatalf("workDir = %q, want unchanged %q", agent.workDir, tempDir)
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], missingDir) {
		t.Fatalf("sent = %v, want invalid path message", p.sent)
	}
}

func TestCmdDir_AliasCdStillWorks(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	tempDir := t.TempDir()
	nextDir := filepath.Join(tempDir, "next")
	if err := os.Mkdir(nextDir, 0o755); err != nil {
		t.Fatalf("mkdir next dir: %v", err)
	}
	agent := &stubWorkDirAgent{workDir: tempDir}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin1")

	e.handleCommand(p, &Message{SessionKey: "test:user1", UserID: "admin1", ReplyCtx: "ctx"}, "/cd next")

	if agent.workDir != nextDir {
		t.Fatalf("workDir = %q, want %q", agent.workDir, nextDir)
	}
}

func TestCmdDir_HelpShowsUsage(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubWorkDirAgent{workDir: "/tmp/project-a"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.cmdDir(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, []string{"help"})

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "/dir <path>") {
		t.Fatalf("sent = %q, want /dir usage", p.sent[0])
	}
}

func TestCmdDir_PersistsAbsoluteOverride(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	baseDir := t.TempDir()
	nextDir := filepath.Join(baseDir, "next")
	if err := os.Mkdir(nextDir, 0o755); err != nil {
		t.Fatalf("mkdir next dir: %v", err)
	}
	statePath := filepath.Join(t.TempDir(), "projects", "test.state.json")
	store := NewProjectStateStore(statePath)

	agent := &stubWorkDirAgent{workDir: baseDir}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetBaseWorkDir(baseDir)
	e.SetProjectStateStore(store)

	e.cmdDir(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, []string{"next"})

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkDirOverride(); got != nextDir {
		t.Fatalf("WorkDirOverride() = %q, want %q", got, nextDir)
	}
}

func TestDirApply_MultiWorkspacePersistsWorkspaceSpecificOverride(t *testing.T) {
	baseDir := t.TempDir()
	workspace := normalizeWorkspacePath(t.TempDir())
	nextDir := filepath.Join(workspace, "next")
	if err := os.MkdirAll(nextDir, 0o755); err != nil {
		t.Fatalf("mkdir next dir: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "projects", "test.state.json")
	store := NewProjectStateStore(statePath)
	agent := &stubWorkDirAgent{workDir: workspace}
	e := NewEngine("test", agent, []Platform{&stubPlatformEngine{n: "plain"}}, "", LangEnglish)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetProjectStateStore(store)

	sessions := NewSessionManager("")
	interactiveKey := workspace + ":feishu:oc_xxx:ou_yyy"

	errMsg, successMsg := e.dirApply(agent, sessions, interactiveKey, "feishu:oc_xxx:ou_yyy", []string{"next"})
	if errMsg != "" {
		t.Fatalf("dirApply errMsg = %q, want empty", errMsg)
	}
	if !strings.Contains(successMsg, nextDir) {
		t.Fatalf("successMsg = %q, want path %q", successMsg, nextDir)
	}

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkspaceDirOverride(interactiveKey); got != nextDir {
		t.Fatalf("WorkspaceDirOverride(%q) = %q, want %q", interactiveKey, got, nextDir)
	}
	if got := reloaded.WorkDirOverride(); got != "" {
		t.Fatalf("WorkDirOverride() = %q, want empty in multi-workspace mode", got)
	}
}

func TestDirApply_MultiWorkspaceResetClearsWorkspaceSpecificOverride(t *testing.T) {
	baseDir := t.TempDir()
	workspace := normalizeWorkspacePath(t.TempDir())
	overrideDir := filepath.Join(workspace, "override")
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "projects", "test.state.json")
	store := NewProjectStateStore(statePath)
	interactiveKey := workspace + ":feishu:oc_xxx:ou_yyy"
	store.SetWorkspaceDirOverride(interactiveKey, overrideDir)
	store.Save()

	agent := &stubWorkDirAgent{workDir: overrideDir}
	e := NewEngine("test", agent, []Platform{&stubPlatformEngine{n: "plain"}}, "", LangEnglish)
	e.SetBaseWorkDir(baseDir)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetProjectStateStore(store)

	sessions := NewSessionManager("")

	errMsg, _ := e.dirApply(agent, sessions, interactiveKey, "feishu:oc_xxx:ou_yyy", []string{"reset"})
	if errMsg != "" {
		t.Fatalf("dirApply errMsg = %q, want empty", errMsg)
	}

	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkspaceDirOverride(interactiveKey); got != "" {
		t.Fatalf("WorkspaceDirOverride(%q) after reset = %q, want empty", interactiveKey, got)
	}
}

func TestCmdDir_ResetRestoresBaseWorkDirAndClearsState(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	baseDir := t.TempDir()
	overrideDir := filepath.Join(baseDir, "override")
	if err := os.Mkdir(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}
	statePath := filepath.Join(t.TempDir(), "projects", "test.state.json")
	store := NewProjectStateStore(statePath)
	store.SetWorkDirOverride(overrideDir)
	store.Save()

	agent := &stubWorkDirAgent{workDir: overrideDir}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetBaseWorkDir(baseDir)
	e.SetProjectStateStore(store)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.SetAgentSessionID("existing-session", "test")
	s.Name = "old"
	s.AddHistory("user", "hello")

	e.cmdDir(p, msg, []string{"reset"})

	if agent.workDir != baseDir {
		t.Fatalf("workDir = %q, want %q", agent.workDir, baseDir)
	}
	reloaded := NewProjectStateStore(statePath)
	if got := reloaded.WorkDirOverride(); got != "" {
		t.Fatalf("WorkDirOverride() = %q, want empty", got)
	}
	if s.GetAgentSessionID() != "" {
		t.Fatalf("AgentSessionID = %q, want cleared", s.GetAgentSessionID())
	}
	if s.Name != "old" {
		t.Fatalf("Name = %q, want unchanged", s.Name)
	}
	if len(s.History) != 0 {
		t.Fatalf("history length = %d, want 0", len(s.History))
	}
	if len(p.sent) != 1 || !strings.Contains(strings.ToLower(p.sent[0]), "default") {
		t.Fatalf("sent = %v, want reset success message", p.sent)
	}
}

func TestCmdDir_SwitchesByHistoryIndex(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	tempDir := t.TempDir()
	dir1 := filepath.Join(tempDir, "dir1")
	dir2 := filepath.Join(tempDir, "dir2")
	dir3 := filepath.Join(tempDir, "dir3")
	for _, d := range []string{dir1, dir2, dir3} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	dataDir := t.TempDir() // separate data dir for history
	agent := &stubWorkDirAgent{workDir: dir1}
	e := NewEngine("test", agent, []Platform{p}, dataDir, LangEnglish)
	e.SetDirHistory(NewDirHistory(dataDir))

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Build history: dir1 -> dir2 -> dir3
	e.cmdDir(p, msg, []string{dir2})
	if agent.workDir != dir2 {
		t.Fatalf("after /dir dir2: workDir = %q, want %q", agent.workDir, dir2)
	}

	e.cmdDir(p, msg, []string{dir3})
	if agent.workDir != dir3 {
		t.Fatalf("after /dir dir3: workDir = %q, want %q", agent.workDir, dir3)
	}

	// Now history should be: [dir3, dir2, dir1] (dir1 might not be in history since it wasn't added initially)
	// Current dir is dir3
	// Index 2 should be dir2

	p.sent = nil
	e.cmdDir(p, msg, []string{"2"})

	// Should have switched to dir2
	if agent.workDir != dir2 {
		t.Fatalf("after /dir 2: workDir = %q, want %q", agent.workDir, dir2)
	}

	// Check the reply mentions dir2
	if len(p.sent) != 1 {
		t.Fatalf("sent = %d messages, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], dir2) {
		t.Fatalf("sent = %q, want message containing %q", p.sent[0], dir2)
	}
}

func TestCmdDir_DisplaysCorrectIndices(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	tempDir := t.TempDir()
	dir1 := filepath.Join(tempDir, "dir1")
	dir2 := filepath.Join(tempDir, "dir2")
	dir3 := filepath.Join(tempDir, "dir3")
	for _, d := range []string{dir1, dir2, dir3} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	dataDir := t.TempDir()
	agent := &stubWorkDirAgent{workDir: dir1}
	e := NewEngine("test", agent, []Platform{p}, dataDir, LangEnglish)
	e.SetDirHistory(NewDirHistory(dataDir))

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Build history
	e.cmdDir(p, msg, []string{dir2})
	e.cmdDir(p, msg, []string{dir3})

	// Now current is dir3, history is [dir3, dir2]
	p.sent = nil
	e.cmdDir(p, msg, nil) // show current + history

	if len(p.sent) != 1 {
		t.Fatalf("sent = %d messages, want 1", len(p.sent))
	}

	// Verify the display shows:
	// - dir3 with ▶ marker (current)
	// - dir2 with ◻ marker at index 2
	output := p.sent[0]

	// Check that dir3 is marked as current
	if !strings.Contains(output, "▶ 1. "+dir3) {
		t.Fatalf("output should contain '▶ 1. %s', got: %s", dir3, output)
	}

	// Check that dir2 is at index 2
	if !strings.Contains(output, "◻ 2. "+dir2) {
		t.Fatalf("output should contain '◻ 2. %s', got: %s", dir2, output)
	}
}

func TestCmdDir_ExpandsTilde(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir:", err)
	}

	p := &stubPlatformEngine{n: "plain"}
	agent := &stubWorkDirAgent{workDir: homeDir}
	e := NewEngine("test", agent, []Platform{p}, t.TempDir(), LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	tests := []struct {
		input   string
		wantDir string
	}{
		{"~", homeDir},
		{"~/", homeDir},
		{"~/Documents", filepath.Join(homeDir, "Documents")},
	}

	for _, tc := range tests {
		agent.workDir = homeDir
		// Ensure the target directory exists before switching
		if err := os.MkdirAll(tc.wantDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %q: %v", tc.wantDir, err)
		}
		e.cmdDir(p, msg, []string{tc.input})
		if agent.workDir != tc.wantDir {
			t.Errorf("input %q: workDir = %q, want %q", tc.input, agent.workDir, tc.wantDir)
		}
	}
}

func TestEngine_AdminFrom_GatesDir(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	tempDir := t.TempDir()
	agent := &stubWorkDirAgent{workDir: tempDir}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:u1", UserID: "user1", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, "/dir .")

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	if !strings.Contains(strings.ToLower(p.sent[0]), "admin") {
		t.Fatalf("expected admin required message, got: %s", p.sent[0])
	}
	if agent.workDir != tempDir {
		t.Fatalf("workDir = %q, want unchanged %q", agent.workDir, tempDir)
	}
}

func TestCmdReasoning_UsesInlineButtonsOnButtonOnlyPlatform(t *testing.T) {
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "inline-only"}}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.cmdReasoning(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.buttonRows) == 0 {
		t.Fatal("expected /reasoning to send inline buttons on button-only platform")
	}
	if got := p.buttonRows[0][0].Data; got != "cmd:/reasoning 1" {
		t.Fatalf("first /reasoning button = %q, want %q", got, "cmd:/reasoning 1")
	}
	if got := p.buttonRows[0][0].Text; got != "low" {
		t.Fatalf("first /reasoning button text = %q, want low", got)
	}
}

func TestCmdReasoning_SwitchesEffortAndResetsSession(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	s := e.sessions.GetOrCreateActive(msg.SessionKey)
	s.SetAgentSessionID("existing-session", "test")
	s.AddHistory("user", "hello")

	e.cmdReasoning(p, msg, []string{"3"})

	if agent.reasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want high", agent.reasoningEffort)
	}
	if s.GetAgentSessionID() != "" {
		t.Fatalf("AgentSessionID = %q, want cleared", s.GetAgentSessionID())
	}
	if len(s.History) != 0 {
		t.Fatalf("history length = %d, want 0", len(s.History))
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "Reasoning effort switched to `high`") {
		t.Fatalf("sent = %v, want reasoning changed message", p.sent)
	}
}

func TestCmdReasoning_RejectsMinimal(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdReasoning(p, msg, []string{"minimal"})

	if agent.reasoningEffort != "" {
		t.Fatalf("reasoning effort = %q, want unchanged empty", agent.reasoningEffort)
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "/reasoning <number>") || strings.Contains(p.sent[0], "minimal") {
		t.Fatalf("sent = %v, want usage without minimal", p.sent)
	}
}

func TestCmdMode_UsesInlineButtonsOnButtonOnlyPlatform(t *testing.T) {
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "inline-only"}}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.cmdMode(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.buttonRows) == 0 {
		t.Fatal("expected /mode to send inline buttons on button-only platform")
	}
	if got := p.buttonRows[0][0].Data; got != "cmd:/mode default" {
		t.Fatalf("first /mode button = %q, want %q", got, "cmd:/mode default")
	}
	if !strings.Contains(p.buttonContent, "Available: `default` / `yolo`") {
		t.Fatalf("button content = %q, want dynamic mode list", p.buttonContent)
	}
	if strings.Contains(p.buttonContent, "`edit`") {
		t.Fatalf("button content = %q, want no hardcoded mode list", p.buttonContent)
	}
}

func TestCmdMode_AppliesLiveModeWithoutReset(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	live := &stubLiveModeSession{}
	state := &interactiveState{agentSession: live, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	session := e.sessions.GetOrCreateActive(key)
	session.SetAgentSessionID("existing-session", "stub")
	session.AddHistory("user", "hello")

	e.cmdMode(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"yolo"})

	if len(live.modes) != 1 || live.modes[0] != "yolo" {
		t.Fatalf("live modes = %v, want [yolo]", live.modes)
	}
	if session.GetAgentSessionID() != "existing-session" {
		t.Fatalf("agent session id = %q, want existing-session", session.GetAgentSessionID())
	}
	if len(session.GetHistory(0)) != 1 {
		t.Fatalf("history len = %d, want 1", len(session.GetHistory(0)))
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "Current session updated immediately.") {
		t.Fatalf("sent = %v, want live mode update reply", p.sent)
	}
	if got := agent.GetMode(); got != "yolo" {
		t.Fatalf("agent mode = %q, want yolo", got)
	}
}

func TestCmdStatus_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.cmdStatus(p, msg)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "Status") {
		t.Fatalf("status text = %q, want legacy status text", p.sent[0])
	}
	if strings.Contains(p.sent[0], "[← Back]") {
		t.Fatalf("status text = %q, should not be card fallback text", p.sent[0])
	}
}

func TestCmdQuiet_TogglesDisplay(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{ThinkingMessages: true, ToolMessages: true, ThinkingMaxLen: 300, ToolMaxLen: 500})
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// First /quiet: both on → both off (quiet ON)
	e.cmdQuiet(p, msg, nil)
	if e.display.ThinkingMessages || e.display.ToolMessages {
		t.Fatalf("after first /quiet: ThinkingMessages=%v, ToolMessages=%v, want both false",
			e.display.ThinkingMessages, e.display.ToolMessages)
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "Quiet mode ON") {
		t.Fatalf("sent = %q, want quiet ON message", p.sent)
	}

	// Second /quiet: both off → both on (quiet OFF)
	p.sent = nil
	e.cmdQuiet(p, msg, nil)
	if !e.display.ThinkingMessages || !e.display.ToolMessages {
		t.Fatalf("after second /quiet: ThinkingMessages=%v, ToolMessages=%v, want both true",
			e.display.ThinkingMessages, e.display.ToolMessages)
	}
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "Quiet mode OFF") {
		t.Fatalf("sent = %q, want quiet OFF message", p.sent)
	}
}

func TestHandleMessage_ExtraContentPreservedThroughAlias(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &stubAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.aliasMu.Lock()
	e.aliases["hi"] = "hello world"
	e.aliasMu.Unlock()

	msg := &Message{
		SessionKey:   "test:user1",
		ReplyCtx:     "ctx",
		Content:      "hi",
		ExtraContent: "> quoted reply context",
		Platform:     "test",
		UserID:       "user1",
	}

	e.handleMessage(p, msg)

	if !strings.Contains(msg.Content, "> quoted reply context") {
		t.Fatalf("ExtraContent lost after alias resolution: msg.Content = %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "hello world") {
		t.Fatalf("alias not resolved: msg.Content = %q", msg.Content)
	}
}

func TestCmdDiff_RejectsDashTarget(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx", UserID: "admin1"}
	e.SetAdminFrom("admin1")

	e.handleCommand(p, msg, "/diff --output=/tmp/evil")

	if len(p.sent) == 0 {
		t.Fatal("expected error reply for dash target")
	}
	if !strings.Contains(p.sent[0], "must not start with '-'") {
		t.Fatalf("sent = %q, want rejection of dash target", p.sent[0])
	}
}

func TestCmdUsage_UnsupportedAgent(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.handleCommand(p, msg, "/usage")

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(strings.ToLower(p.sent[0]), "does not support") {
		t.Fatalf("sent = %q, want unsupported usage message", p.sent[0])
	}
}

func TestCmdUsage_Success(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubUsageAgent{
		report: &UsageReport{
			Provider: "codex",
			Email:    "dev@example.com",
			Plan:     "team",
			Buckets: []UsageBucket{
				{
					Name:         "Rate limit",
					Allowed:      true,
					LimitReached: false,
					Windows: []UsageWindow{
						{Name: "Primary", UsedPercent: 23, WindowSeconds: 18000, ResetAfterSeconds: 6665},
						{Name: "Secondary", UsedPercent: 42, WindowSeconds: 604800, ResetAfterSeconds: 512698},
					},
				},
				{
					Name:         "Code review",
					Allowed:      true,
					LimitReached: false,
					Windows: []UsageWindow{
						{Name: "Primary", UsedPercent: 0, WindowSeconds: 604800, ResetAfterSeconds: 604800},
					},
				},
			},
			Credits: &UsageCredits{
				HasCredits: false,
				Unlimited:  false,
			},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.handleCommand(p, msg, "/usage")

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	got := p.sent[0]
	for _, want := range []string{
		"Account: dev@example.com (team)",
		"5h limit",
		"Remaining: 77%",
		"Resets: 1h 51m",
		"5h limit",
		"7d limit",
		"Remaining: 58%",
		"Resets: 5d 22h 24m",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage text = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "```") {
		t.Fatalf("usage text = %q, should not use code block on plain platform", got)
	}
}

func TestCmdUsage_UsesCardOnCardPlatform(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubUsageAgent{
		report: &UsageReport{
			Email: "dev@example.com",
			Plan:  "team",
			Buckets: []UsageBucket{
				{
					Name:         "Rate limit",
					Allowed:      true,
					LimitReached: false,
					Windows: []UsageWindow{
						{Name: "Primary", UsedPercent: 23, WindowSeconds: 18000, ResetAfterSeconds: 6665},
						{Name: "Secondary", UsedPercent: 42, WindowSeconds: 604800, ResetAfterSeconds: 512698},
					},
				},
			},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangChinese)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.handleCommand(p, msg, "/usage")

	if len(p.repliedCards) != 1 {
		t.Fatalf("replied cards = %d, want 1", len(p.repliedCards))
	}
	if len(p.sent) != 0 {
		t.Fatalf("sent text = %v, want no plain text fallback", p.sent)
	}
	text := p.repliedCards[0].RenderText()
	for _, want := range []string{
		"账号：dev@example.com (team)",
		"5小时限额",
		"剩余：77%",
		"重置：1小时 51分钟",
		"7日限额",
		"剩余：58%",
		"重置：5天 22小时 24分钟",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("card text = %q, want substring %q", text, want)
		}
	}
}

func TestCmdUsage_LocalizedChinese(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubUsageAgent{
		report: &UsageReport{
			Email: "dev@example.com",
			Plan:  "team",
			Buckets: []UsageBucket{
				{
					Name:         "Rate limit",
					Allowed:      true,
					LimitReached: false,
					Windows: []UsageWindow{
						{Name: "Primary", UsedPercent: 23, WindowSeconds: 18000, ResetAfterSeconds: 6665},
						{Name: "Secondary", UsedPercent: 42, WindowSeconds: 604800, ResetAfterSeconds: 512698},
					},
				},
			},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangChinese)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	e.handleCommand(p, msg, "/usage")

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	got := p.sent[0]
	for _, want := range []string{
		"账号：dev@example.com (team)",
		"5小时限额",
		"剩余：77%",
		"重置：1小时 51分钟",
		"7日限额",
		"剩余：58%",
		"重置：5天 22小时 24分钟",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage text = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "```") {
		t.Fatalf("usage text = %q, should not use code block on plain platform", got)
	}
}

func TestCmdCommands_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.AddCommand("deploy", "Deploy app", "ship it", "", "", "config")

	e.cmdCommands(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "/deploy") {
		t.Fatalf("commands text = %q, want legacy command list", p.sent[0])
	}
	if strings.Contains(p.sent[0], "[← Back]") {
		t.Fatalf("commands text = %q, should not be card fallback text", p.sent[0])
	}
}

func TestCmdConfig_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	e.cmdConfig(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "thinking_max_len") {
		t.Fatalf("config text = %q, want legacy config list", p.sent[0])
	}
	if strings.Contains(p.sent[0], "[← Back]") {
		t.Fatalf("config text = %q, should not be card fallback text", p.sent[0])
	}
}

func TestCmdAlias_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.AddAlias("ls", "/list")

	e.cmdAlias(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, nil)

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "ls") || !strings.Contains(p.sent[0], "/list") {
		t.Fatalf("alias text = %q, want legacy alias list", p.sent[0])
	}
	if strings.Contains(p.sent[0], "[← Back]") {
		t.Fatalf("alias text = %q, should not be card fallback text", p.sent[0])
	}
}

func TestCmdSkills_UsesLegacyTextOnPlatformWithoutCardSupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	temp := t.TempDir()
	skillDir := temp + "/demo"
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillDir+"/SKILL.md", []byte("---\ndescription: Demo skill\n---\nDo demo"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	e.skills.SetDirs([]string{temp})

	e.cmdSkills(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"})

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "/demo") {
		t.Fatalf("skills text = %q, want legacy skills list", p.sent[0])
	}
	if strings.Contains(p.sent[0], "[← Back]") {
		t.Fatalf("skills text = %q, should not be card fallback text", p.sent[0])
	}
}

func TestCmdSkills_UsesTelegramSafeNamesOnTelegramPlatform(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	temp := t.TempDir()
	skillDir := temp + "/telegram-codex-bot"
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(skillDir+"/SKILL.md", []byte("---\ndescription: Demo skill\n---\nDo demo"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	e.skills.SetDirs([]string{temp})

	e.cmdSkills(p, &Message{SessionKey: "telegram:user1", ReplyCtx: "ctx"})

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "/telegram_codex_bot") {
		t.Fatalf("skills text = %q, want Telegram-safe skill command", p.sent[0])
	}
	if strings.Contains(p.sent[0], "/telegram-codex-bot") {
		t.Fatalf("skills text = %q, should not show raw hyphenated command", p.sent[0])
	}
}

func TestMenuCommandsForPlatform_TelegramOmitsAllSkillsWhenMenuWouldOverflow(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	temp := t.TempDir()
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		skillDir := filepath.Join(temp, name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir skill dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Demo skill\n---\nDo demo"), 0o644); err != nil {
			t.Fatalf("write skill file: %v", err)
		}
	}
	e.skills.SetDirs([]string{temp})

	commands, skillsOmitted := e.menuCommandsForPlatform("telegram")

	if !skillsOmitted {
		t.Fatalf("expected Telegram menu planner to omit skill commands when command menu overflows")
	}
	for _, cmd := range commands {
		if cmd.IsSkill {
			t.Fatalf("menu commands should omit skills when overflowed, got %+v", cmd)
		}
	}
}

func TestCmdSkills_TelegramShowsManualInvocationHintWhenSkillsAreOmittedFromMenu(t *testing.T) {
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	temp := t.TempDir()
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		skillDir := filepath.Join(temp, name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir skill dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Demo skill\n---\nDo demo"), 0o644); err != nil {
			t.Fatalf("write skill file: %v", err)
		}
	}
	e.skills.SetDirs([]string{temp})

	e.cmdSkills(p, &Message{SessionKey: "telegram:user1", ReplyCtx: "ctx"})

	if len(p.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "command menu is full") {
		t.Fatalf("skills text = %q, want Telegram overflow hint", p.sent[0])
	}
}

func TestRenderListCard_MakesEveryVisibleSessionClickable(t *testing.T) {
	sessions := make([]AgentSessionInfo, 0, 7)
	base := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		sessions = append(sessions, AgentSessionInfo{
			ID:           "agent-session-" + string(rune('A'+i)),
			Summary:      "Session summary",
			MessageCount: i + 1,
			ModifiedAt:   base.Add(time.Duration(i) * time.Minute),
		})
	}

	e := NewEngine("test", &stubListAgent{sessions: sessions}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	// Register all agent sessions with the session manager so they pass the
	// owned-session filter (simulates cc-connect having created each session).
	var internalIDs []string
	for i, s := range sessions {
		sess := e.sessions.NewSession("test:user1", "session-"+string(rune('A'+i)))
		sess.SetAgentSessionID(s.ID, "test")
		internalIDs = append(internalIDs, sess.ID)
	}
	// Switch active to the session mapped to sessions[5] (agent-session-F).
	e.sessions.SwitchSession("test:user1", internalIDs[5])

	card, err := e.renderListCard("test:user1", 1)
	if err != nil {
		t.Fatalf("renderListCard returned error: %v", err)
	}

	if got := countCardActionValues(card, "act:/switch "); got != len(sessions) {
		t.Fatalf("switch action count = %d, want %d", got, len(sessions))
	}

	btn, ok := findCardAction(card, "act:/switch 6")
	if !ok {
		t.Fatal("expected active session switch action to exist")
	}
	if btn.Type != "primary" {
		t.Fatalf("active session button type = %q, want primary", btn.Type)
	}
}

func TestRenderDirCard_HistoryRowsUseSelectActions(t *testing.T) {
	tempDir := t.TempDir()
	dir1 := filepath.Join(tempDir, "dir1")
	dir2 := filepath.Join(tempDir, "dir2")
	for _, d := range []string{dir1, dir2} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	dataDir := t.TempDir()
	agent := &stubWorkDirAgent{workDir: dir2}
	e := NewEngine("test", agent, []Platform{&stubPlatformEngine{n: "test"}}, dataDir, LangEnglish)
	e.SetDirHistory(NewDirHistory(dataDir))
	e.dirHistory.Add("test", dir1)
	e.dirHistory.Add("test", dir2)

	card, err := e.renderDirCard("test:user1", 1)
	if err != nil {
		t.Fatalf("renderDirCard: %v", err)
	}
	if got := countCardActionValues(card, "act:/dir select "); got != 2 {
		t.Fatalf("dir select actions = %d, want 2", got)
	}
}

func TestHandleCardNav_DirSelectSwitchesWorkDir(t *testing.T) {
	temp := t.TempDir()
	d1 := filepath.Join(temp, "a")
	d2 := filepath.Join(temp, "b")
	d3 := filepath.Join(temp, "c")
	for _, d := range []string{d1, d2, d3} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	dataDir := t.TempDir()
	agent := &stubWorkDirAgent{workDir: d3}
	e := NewEngine("test", agent, []Platform{&stubPlatformEngine{n: "test"}}, dataDir, LangEnglish)
	e.SetDirHistory(NewDirHistory(dataDir))
	e.dirHistory.Add("test", d1)
	e.dirHistory.Add("test", d2)
	e.dirHistory.Add("test", d3)

	sk := "test:user1"
	_ = e.handleCardNav("act:/dir select 2", sk)
	if agent.workDir != d2 {
		t.Fatalf("workDir = %q, want %q", agent.workDir, d2)
	}
	card := e.handleCardNav("nav:/dir 1", sk)
	if card == nil {
		t.Fatal("expected dir card after nav")
	}
}

func TestRenderHelpCard_DefaultsToSessionTab(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)

	card := e.renderHelpCard()
	text := card.RenderText()

	if got := countCardActionValues(card, "nav:/help "); got != 4 {
		t.Fatalf("help tab action count = %d, want 4", got)
	}
	btn, ok := findCardAction(card, "nav:/help session")
	if !ok {
		t.Fatal("expected session help tab to exist")
	}
	if btn.Type != "primary" {
		t.Fatalf("session help tab type = %q, want primary", btn.Type)
	}
	if btn.Text != "Session Management" {
		t.Fatalf("session help tab text = %q, want full title", btn.Text)
	}
	if !strings.Contains(text, "**/new**") {
		t.Fatalf("default help text = %q, want session commands", text)
	}
	if strings.Contains(text, "**Session Management**") {
		t.Fatalf("default help text = %q, should not repeat tab title in body", text)
	}
	if strings.Contains(text, "**/model**") {
		t.Fatalf("default help text = %q, should not include agent commands", text)
	}
}

func TestHandleCardNav_HelpSwitchesTabs(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)

	card := e.handleCardNav("nav:/help agent", "test:user1")
	if card == nil {
		t.Fatal("expected help nav card")
	}
	text := card.RenderText()

	if !strings.Contains(text, "**/model**") {
		t.Fatalf("agent help text = %q, want agent commands", text)
	}
	if strings.Contains(text, "**Agent Configuration**") {
		t.Fatalf("agent help text = %q, should not repeat tab title in body", text)
	}
	if strings.Contains(text, "**/new**") {
		t.Fatalf("agent help text = %q, should not include session commands", text)
	}
}

// --- AskUserQuestion tests ---

func testQuestions() []UserQuestion {
	return []UserQuestion{{
		Question: "Which database?",
		Header:   "Setup",
		Options: []UserQuestionOption{
			{Label: "PostgreSQL", Description: "Recommended for production"},
			{Label: "SQLite", Description: "Lightweight, file-based"},
			{Label: "MySQL", Description: "Popular open-source"},
		},
		MultiSelect: false,
	}}
}

func testMultiQuestions() []UserQuestion {
	return []UserQuestion{
		{
			Question: "Which database?",
			Header:   "Database",
			Options: []UserQuestionOption{
				{Label: "PostgreSQL"},
				{Label: "SQLite"},
			},
		},
		{
			Question: "Which framework?",
			Header:   "Framework",
			Options: []UserQuestionOption{
				{Label: "Gin"},
				{Label: "Echo"},
			},
		},
	}
}

func TestResolveAskQuestionAnswer_NumericIndex(t *testing.T) {
	e := newTestEngine()
	q := testQuestions()[0]
	got := e.resolveAskQuestionAnswer(q, "2")
	if got != "SQLite" {
		t.Errorf("expected SQLite, got %s", got)
	}
}

func TestResolveAskQuestionAnswer_ButtonCallback(t *testing.T) {
	e := newTestEngine()
	q := testQuestions()[0]
	got := e.resolveAskQuestionAnswer(q, "askq:0:1")
	if got != "PostgreSQL" {
		t.Errorf("expected PostgreSQL, got %s", got)
	}
}

func TestResolveAskQuestionAnswer_FreeText(t *testing.T) {
	e := newTestEngine()
	q := testQuestions()[0]
	got := e.resolveAskQuestionAnswer(q, "Redis")
	if got != "Redis" {
		t.Errorf("expected Redis, got %s", got)
	}
}

func TestResolveAskQuestionAnswer_MultiSelect(t *testing.T) {
	e := newTestEngine()
	q := testQuestions()[0]
	q.MultiSelect = true
	got := e.resolveAskQuestionAnswer(q, "1,3")
	if got != "PostgreSQL, MySQL" {
		t.Errorf("expected 'PostgreSQL, MySQL', got %s", got)
	}
}

func TestResolveAskQuestionAnswer_OutOfRange(t *testing.T) {
	e := newTestEngine()
	q := testQuestions()[0]
	got := e.resolveAskQuestionAnswer(q, "99")
	if got != "99" {
		t.Errorf("expected raw '99' for out-of-range, got %s", got)
	}
}

func TestBuildAskQuestionResponse(t *testing.T) {
	input := map[string]any{
		"questions": []any{map[string]any{"question": "Which?"}},
	}
	collected := map[int]string{0: "PostgreSQL", 1: "Gin"}
	result := buildAskQuestionResponse(input, testQuestions(), collected)
	answers, ok := result["answers"].(map[string]any)
	if !ok {
		t.Fatal("expected answers map")
	}
	if answers["0"] != "PostgreSQL" {
		t.Errorf("expected answer[0]=PostgreSQL, got %v", answers["0"])
	}
	if answers["1"] != "Gin" {
		t.Errorf("expected answer[1]=Gin, got %v", answers["1"])
	}
	if _, ok := result["questions"]; !ok {
		t.Error("expected original questions to be preserved")
	}
}

func TestSendAskQuestionPrompt_CardPlatform(t *testing.T) {
	e := newTestEngine()
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e.sendAskQuestionPrompt(p, "ctx", testQuestions(), 0)

	if len(p.sentCards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(p.sentCards))
	}
	card := p.sentCards[0]
	if card.Header == nil || card.Header.Color != "blue" {
		t.Errorf("expected blue header, got %+v", card.Header)
	}
	askqCount := countCardActionValues(card, "askq:")
	if askqCount != 3 {
		t.Errorf("expected 3 askq buttons, got %d", askqCount)
	}
}

func TestSendAskQuestionPrompt_CardPlatform_MultiQuestion_ShowsIndex(t *testing.T) {
	e := newTestEngine()
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	qs := testMultiQuestions()
	e.sendAskQuestionPrompt(p, "ctx", qs, 0)

	if len(p.sentCards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(p.sentCards))
	}
	card := p.sentCards[0]
	if !strings.Contains(card.Header.Title, "(1/2)") {
		t.Errorf("expected (1/2) in title, got %s", card.Header.Title)
	}
}

func TestSendAskQuestionPrompt_InlineButtonPlatform(t *testing.T) {
	e := newTestEngine()
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	e.sendAskQuestionPrompt(p, "ctx", testQuestions(), 0)

	if len(p.buttonRows) != 3 {
		t.Fatalf("expected 3 button rows, got %d", len(p.buttonRows))
	}
	if p.buttonRows[0][0].Data != "askq:0:1" {
		t.Errorf("expected askq:0:1, got %s", p.buttonRows[0][0].Data)
	}
}

func TestSendAskQuestionPrompt_PlainPlatform(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "plain"}
	e.sendAskQuestionPrompt(p, "ctx", testQuestions(), 0)

	if len(p.sent) != 1 {
		t.Fatal("expected 1 message")
	}
	msg := p.sent[0]
	if !strings.Contains(msg, "Which database?") {
		t.Errorf("expected question text, got %s", msg)
	}
	if !strings.Contains(msg, "1. **PostgreSQL**") {
		t.Errorf("expected numbered options, got %s", msg)
	}
}

func TestHandlePendingPermission_AskUserQuestion_SingleQuestion(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	rec := &recordingAgentSession{}

	state := &interactiveState{
		agentSession: rec,
		platform:     p,
		replyCtx:     "ctx",
		pending: &pendingPermission{
			RequestID: "req-1",
			ToolName:  "AskUserQuestion",
			ToolInput: map[string]any{
				"questions": []any{map[string]any{"question": "Which?"}},
			},
			Questions: testQuestions(),
			Resolved:  make(chan struct{}),
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = state
	e.interactiveMu.Unlock()

	handled := e.handlePendingPermission(p, &Message{
		SessionKey: "test:chat:user1",
		UserID:     "user1",
		Content:    "2",
		ReplyCtx:   "ctx",
	}, "2")

	if !handled {
		t.Fatal("expected handlePendingPermission to return true")
	}
	if rec.calls != 1 {
		t.Fatalf("expected 1 RespondPermission call, got %d", rec.calls)
	}
	answers, ok := rec.lastResult.UpdatedInput["answers"].(map[string]any)
	if !ok {
		t.Fatal("expected answers in updatedInput")
	}
	if answers["0"] != "SQLite" {
		t.Errorf("expected answer=SQLite, got %v", answers["0"])
	}

	state.mu.Lock()
	if state.pending != nil {
		t.Error("expected pending to be cleared after response")
	}
	state.mu.Unlock()
}

func TestHandlePendingPermission_AskUserQuestion_MultiQuestion_Sequential(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	rec := &recordingAgentSession{}

	qs := testMultiQuestions()
	state := &interactiveState{
		agentSession: rec,
		platform:     p,
		replyCtx:     "ctx",
		pending: &pendingPermission{
			RequestID: "req-1",
			ToolName:  "AskUserQuestion",
			ToolInput: map[string]any{"questions": []any{}},
			Questions: qs,
			Resolved:  make(chan struct{}),
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = state
	e.interactiveMu.Unlock()

	// Answer question 0 — should NOT resolve yet
	handled := e.handlePendingPermission(p, &Message{
		SessionKey: "test:chat:user1",
		UserID:     "user1",
		Content:    "1",
		ReplyCtx:   "ctx",
	}, "1")
	if !handled {
		t.Fatal("expected handled=true for question 0")
	}
	if rec.calls != 0 {
		t.Fatalf("should not have called RespondPermission yet, got %d calls", rec.calls)
	}
	state.mu.Lock()
	if state.pending == nil {
		t.Fatal("pending should still exist (more questions)")
	}
	if state.pending.CurrentQuestion != 1 {
		t.Errorf("expected CurrentQuestion=1, got %d", state.pending.CurrentQuestion)
	}
	state.mu.Unlock()

	// Answer question 1 — should resolve
	handled = e.handlePendingPermission(p, &Message{
		SessionKey: "test:chat:user1",
		UserID:     "user1",
		Content:    "2",
		ReplyCtx:   "ctx",
	}, "2")
	if !handled {
		t.Fatal("expected handled=true for question 1")
	}
	if rec.calls != 1 {
		t.Fatalf("expected 1 RespondPermission call, got %d", rec.calls)
	}
	answers, ok := rec.lastResult.UpdatedInput["answers"].(map[string]any)
	if !ok {
		t.Fatal("expected answers in updatedInput")
	}
	if answers["0"] != "PostgreSQL" {
		t.Errorf("expected answer[0]=PostgreSQL, got %v", answers["0"])
	}
	if answers["1"] != "Echo" {
		t.Errorf("expected answer[1]=Echo, got %v", answers["1"])
	}

	state.mu.Lock()
	if state.pending != nil {
		t.Error("expected pending to be cleared after all questions answered")
	}
	state.mu.Unlock()
}

func TestHandlePendingPermission_AskUserQuestion_SkipsPermFlow(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	rec := &recordingAgentSession{}

	state := &interactiveState{
		agentSession: rec,
		platform:     p,
		replyCtx:     "ctx",
		pending: &pendingPermission{
			RequestID: "req-1",
			ToolName:  "AskUserQuestion",
			ToolInput: map[string]any{
				"questions": []any{map[string]any{"question": "Which?"}},
			},
			Questions: testQuestions(),
			Resolved:  make(chan struct{}),
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = state
	e.interactiveMu.Unlock()

	// "allow" should NOT be interpreted as permission allow; should be treated as free text answer
	handled := e.handlePendingPermission(p, &Message{
		SessionKey: "test:chat:user1",
		UserID:     "user1",
		Content:    "allow",
		ReplyCtx:   "ctx",
	}, "allow")

	if !handled {
		t.Fatal("expected handled=true")
	}
	answers, ok := rec.lastResult.UpdatedInput["answers"].(map[string]any)
	if !ok {
		t.Fatal("expected answers in updatedInput")
	}
	if answers["0"] != "allow" {
		t.Errorf("expected free text 'allow' as answer, got %v", answers["0"])
	}
}

// ──────────────────────────────────────────────────────────────
// Session routing / cleanup CAS tests
// ──────────────────────────────────────────────────────────────

// controllableAgentSession is an AgentSession stub whose session ID, liveness,
// and events channel can be controlled by the test.
type controllableAgentSession struct {
	sessionID       string
	alive           bool
	events          chan Event
	closed          chan struct{} // closed when Close() is called
	model           string
	reasoningEffort string
	workDir         string
	report          *UsageReport
	contextUsage    *ContextUsage
	usageErr        error
}

func newControllableSession(id string) *controllableAgentSession {
	return &controllableAgentSession{
		sessionID: id,
		alive:     true,
		events:    make(chan Event, 8),
		closed:    make(chan struct{}),
	}
}

func (s *controllableAgentSession) Send(_ string, _ []ImageAttachment, _ []FileAttachment) error {
	return nil
}
func (s *controllableAgentSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *controllableAgentSession) Events() <-chan Event                                 { return s.events }
func (s *controllableAgentSession) CurrentSessionID() string                             { return s.sessionID }
func (s *controllableAgentSession) GetModel() string                                     { return s.model }
func (s *controllableAgentSession) GetReasoningEffort() string                           { return s.reasoningEffort }
func (s *controllableAgentSession) GetWorkDir() string                                   { return s.workDir }
func (s *controllableAgentSession) GetUsage(_ context.Context) (*UsageReport, error) {
	if s.report == nil && s.usageErr == nil {
		return nil, fmt.Errorf("usage unavailable")
	}
	return s.report, s.usageErr
}
func (s *controllableAgentSession) GetContextUsage() *ContextUsage { return s.contextUsage }
func (s *controllableAgentSession) Alive() bool                    { return s.alive }
func (s *controllableAgentSession) Close() error {
	s.alive = false
	close(s.events)
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// controllableAgent lets tests control which session is returned by StartSession.
type controllableAgent struct {
	nextSession AgentSession
	listFn      func() ([]AgentSessionInfo, error)
}

func (a *controllableAgent) Name() string { return "controllable" }
func (a *controllableAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	if a.nextSession != nil {
		return a.nextSession, nil
	}
	return newControllableSession("default"), nil
}
func (a *controllableAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	if a.listFn != nil {
		return a.listFn()
	}
	return nil, nil
}
func (a *controllableAgent) Stop() error { return nil }

// TestCleanupCAS_SkipsWhenStateReplaced verifies that cleanupInteractiveState
// with an expected state pointer is a no-op when the map entry has been replaced.
// This is the core of the /new race fix: old goroutine's cleanup must not delete
// a replacement state created by a new turn.
func TestCleanupCAS_SkipsWhenStateReplaced(t *testing.T) {
	e := newTestEngine()
	key := "test:user1"

	oldState := &interactiveState{agentSession: newControllableSession("old")}
	newState := &interactiveState{agentSession: newControllableSession("new")}

	// Place the NEW state in the map (simulating: /new already cleaned up and
	// a new turn created a replacement state).
	e.interactiveMu.Lock()
	e.interactiveStates[key] = newState
	e.interactiveMu.Unlock()

	// Old goroutine calls cleanup with the OLD state pointer — should be skipped.
	e.cleanupInteractiveState(key, oldState)

	e.interactiveMu.Lock()
	current := e.interactiveStates[key]
	e.interactiveMu.Unlock()

	if current != newState {
		t.Fatal("CAS cleanup deleted the replacement state — race not prevented")
	}
}

// TestCleanupCAS_DeletesWhenStateMatches verifies that cleanup proceeds normally
// when the expected state matches the current map entry.
func TestCleanupCAS_DeletesWhenStateMatches(t *testing.T) {
	e := newTestEngine()
	key := "test:user1"

	state := &interactiveState{agentSession: newControllableSession("s1")}

	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	e.cleanupInteractiveState(key, state)

	e.interactiveMu.Lock()
	current := e.interactiveStates[key]
	e.interactiveMu.Unlock()

	if current != nil {
		t.Fatal("expected state to be deleted when expected pointer matches")
	}
}

// TestCleanupCAS_UnconditionalWithoutExpected verifies that cleanup without an
// expected pointer always deletes (backward compat for command handlers).
func TestCleanupCAS_UnconditionalWithoutExpected(t *testing.T) {
	e := newTestEngine()
	key := "test:user1"

	state := &interactiveState{agentSession: newControllableSession("s1")}

	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// No expected pointer — unconditional cleanup (used by /new, /switch).
	e.cleanupInteractiveState(key)

	e.interactiveMu.Lock()
	current := e.interactiveStates[key]
	e.interactiveMu.Unlock()

	if current != nil {
		t.Fatal("expected unconditional cleanup to delete state")
	}
}

// TestCleanupCAS_ConcurrentUnconditionalCloseOnce verifies that two concurrent
// unconditional cleanups for the same key only Close() the agent session once.
func TestCleanupCAS_ConcurrentUnconditionalCloseOnce(t *testing.T) {
	e := newTestEngine()
	key := "test:user1"

	var closeCount atomic.Int32
	sess := newControllableSession("s1")
	origClose := sess.Close
	_ = origClose
	state := &interactiveState{agentSession: sess}

	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			e.cleanupInteractiveState(key)
		}()
	}
	wg.Wait()

	// The session's Close() should have been called at most once because
	// the first cleanup nil's out state.agentSession under the lock.
	select {
	case <-sess.closed:
		closeCount.Add(1)
	default:
	}
	if closeCount.Load() > 1 {
		t.Fatalf("expected at most 1 close, got %d", closeCount.Load())
	}

	e.interactiveMu.Lock()
	if e.interactiveStates[key] != nil {
		t.Fatal("expected state to be deleted after cleanup")
	}
	e.interactiveMu.Unlock()
}

// TestSessionMismatch_RecyclesStaleAgent verifies that getOrCreateInteractiveStateWith
// detects when the running agent session ID differs from the active Session's
// AgentSessionID and creates a fresh agent instead of reusing the stale one.
func TestSessionMismatch_RecyclesStaleAgent(t *testing.T) {
	newSess := newControllableSession("new-agent-id")
	agent := &controllableAgent{nextSession: newSess}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"

	// Seed a live agent session with ID "old-agent-id".
	oldSess := newControllableSession("old-agent-id")
	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{
		agentSession: oldSess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Unlock()

	// The active Session now wants a DIFFERENT agent session ID.
	session := &Session{AgentSessionID: "new-agent-id"}

	state := e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")

	if state.agentSession == oldSess {
		t.Fatal("expected stale agent session to be replaced")
	}
	if state.agentSession != newSess {
		t.Fatal("expected new agent session from StartSession")
	}

	// Old session should be closed asynchronously.
	select {
	case <-oldSess.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("old agent session was not closed after mismatch")
	}
}

// TestSessionClearedAfterNew_RecyclesAliveAgent verifies issue #238: after /new the
// Session's AgentSessionID is empty but an older Claude process may still be alive;
// it must be recycled instead of reused (which would keep prior --resume context).
func TestSessionClearedAfterNew_RecyclesAliveAgent(t *testing.T) {
	newSess := newControllableSession("fresh-id")
	agent := &controllableAgent{nextSession: newSess}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "test:user1"
	oldSess := newControllableSession("prior-claude-session")
	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{
		agentSession: oldSess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Unlock()

	session := &Session{AgentSessionID: ""}

	state := e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")
	if state.agentSession == oldSess {
		t.Fatal("expected stale agent to be recycled when AgentSessionID was cleared")
	}
	if state.agentSession != newSess {
		t.Fatal("expected new agent session from StartSession")
	}
	select {
	case <-oldSess.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("old agent session was not closed after /new-style clear")
	}
}

// TestSessionMismatch_ReusesWhenIDsMatch verifies that getOrCreateInteractiveStateWith
// returns the existing state when agent session IDs match (no unnecessary recycling).
func TestSessionMismatch_ReusesWhenIDsMatch(t *testing.T) {
	agent := &controllableAgent{}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"

	existingSess := newControllableSession("matching-id")
	existingState := &interactiveState{
		agentSession: existingSess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = existingState
	e.interactiveMu.Unlock()

	session := &Session{AgentSessionID: "matching-id"}

	state := e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")
	if state != existingState {
		t.Fatal("expected existing state to be reused when session IDs match")
	}
}

// TestSessionIDWriteback_ImmediateAfterStartSession verifies that after
// StartSession, the agent's CurrentSessionID is immediately written back
// to the Session's AgentSessionID when it was previously empty.
func TestSessionIDWriteback_ImmediateAfterStartSession(t *testing.T) {
	sess := newControllableSession("agent-uuid-123")
	agent := &controllableAgent{nextSession: sess}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	session := &Session{AgentSessionID: ""} // empty — no prior binding

	e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")

	got := session.GetAgentSessionID()

	if got != "agent-uuid-123" {
		t.Fatalf("AgentSessionID = %q, want %q — immediate writeback not working", got, "agent-uuid-123")
	}
}

// TestSessionIDWriteback_MapsSessionName verifies that when startOrResumeSession
// sets the AgentSessionID, it also maps the session's pending name via
// SetSessionName so that /list displays the custom name from /new.
func TestSessionIDWriteback_MapsSessionName(t *testing.T) {
	sess := newControllableSession("agent-uuid-456")
	agent := &controllableAgent{nextSession: sess}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	session := e.sessions.NewSession(key, "我的自定义会话")

	e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")

	got := e.sessions.GetSessionName("agent-uuid-456")
	if got != "我的自定义会话" {
		t.Fatalf("GetSessionName = %q, want %q — name not mapped during startOrResumeSession", got, "我的自定义会话")
	}
}

// TestSessionIDWriteback_DoesNotOverwriteExisting verifies that immediate
// writeback does not clobber an existing AgentSessionID (e.g. from --resume).
func TestSessionIDWriteback_DoesNotOverwriteExisting(t *testing.T) {
	sess := newControllableSession("new-uuid")
	agent := &controllableAgent{nextSession: sess}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	session := &Session{AgentSessionID: "existing-uuid"}

	e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")

	got := session.GetAgentSessionID()

	if got != "existing-uuid" {
		t.Fatalf("AgentSessionID = %q, want %q — writeback should not overwrite", got, "existing-uuid")
	}
}

// TestStaleGoroutineCleanup_RaceSimulation simulates the full race scenario:
// old turn still processing → /new creates new Session → new turn starts →
// old turn exits and calls cleanup. Verifies the new state survives.
func TestStaleGoroutineCleanup_RaceSimulation(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	newSess := newControllableSession("new-agent")
	agent := &controllableAgent{nextSession: newSess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"

	// Step 1: Old turn created state S1 with old agent.
	oldSess := newControllableSession("old-agent")
	oldState := &interactiveState{
		agentSession: oldSess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = oldState
	e.interactiveMu.Unlock()

	// Step 2: /new runs — unconditional cleanup deletes S1.
	e.cleanupInteractiveState(key)

	// Step 3: New turn creates Session B and calls getOrCreateInteractiveStateWith.
	sessionB := &Session{AgentSessionID: ""}
	newState := e.getOrCreateInteractiveStateWith(key, p, "ctx", sessionB, e.sessions, nil, "")

	// Verify S2 is in the map.
	e.interactiveMu.Lock()
	current := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if current != newState {
		t.Fatal("new state not in map")
	}

	// Step 4: Old goroutine exits and calls cleanup with OLD state pointer.
	// This simulates processInteractiveEvents channelClosed path.
	e.cleanupInteractiveState(key, oldState)

	// Verify: new state must survive.
	e.interactiveMu.Lock()
	afterCleanup := e.interactiveStates[key]
	e.interactiveMu.Unlock()

	if afterCleanup != newState {
		t.Fatal("stale goroutine's cleanup deleted the replacement state — CAS not working")
	}
	if newState.agentSession.Alive() != true {
		t.Fatal("replacement agent session was killed by stale cleanup")
	}
}

func TestSplitMessageUTF8Safety(t *testing.T) {
	t.Run("ASCII short", func(t *testing.T) {
		result := splitMessage("hello", 10)
		if len(result) != 1 || result[0] != "hello" {
			t.Fatalf("expected single chunk 'hello', got %v", result)
		}
	})

	t.Run("CJK characters split at rune boundary", func(t *testing.T) {
		// 10 CJK characters (each 3 bytes in UTF-8), total 30 bytes
		input := "你好世界测试一二三四"
		if len([]rune(input)) != 10 {
			t.Fatalf("expected 10 runes, got %d", len([]rune(input)))
		}
		// maxLen=5 runes should split into 2 chunks of 5 runes each
		chunks := splitMessage(input, 5)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
		}
		if chunks[0] != "你好世界测" {
			t.Errorf("chunk[0] = %q, want %q", chunks[0], "你好世界测")
		}
		if chunks[1] != "试一二三四" {
			t.Errorf("chunk[1] = %q, want %q", chunks[1], "试一二三四")
		}
	})

	t.Run("emoji split at rune boundary", func(t *testing.T) {
		// Emoji: 4 bytes each in UTF-8
		input := "😀😁😂🤣😄😅"
		runes := []rune(input)
		if len(runes) != 6 {
			t.Fatalf("expected 6 runes, got %d", len(runes))
		}
		chunks := splitMessage(input, 3)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
		}
		if chunks[0] != "😀😁😂" {
			t.Errorf("chunk[0] = %q, want %q", chunks[0], "😀😁😂")
		}
		if chunks[1] != "🤣😄😅" {
			t.Errorf("chunk[1] = %q, want %q", chunks[1], "🤣😄😅")
		}
	})

	t.Run("prefers newline split", func(t *testing.T) {
		input := "abcde\nfghij"
		chunks := splitMessage(input, 8)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
		}
		// Should split at newline (rune index 5), which is >= 8/2=4
		if chunks[0] != "abcde\n" {
			t.Errorf("chunk[0] = %q, want %q", chunks[0], "abcde\n")
		}
		if chunks[1] != "fghij" {
			t.Errorf("chunk[1] = %q, want %q", chunks[1], "fghij")
		}
	})

	t.Run("CJK with newline split", func(t *testing.T) {
		input := "你好\n世界测试一二三四"
		chunks := splitMessage(input, 5)
		if len(chunks) < 2 {
			t.Fatalf("expected at least 2 chunks, got %d: %v", len(chunks), chunks)
		}
		// First chunk should split at the newline
		if chunks[0] != "你好\n" {
			t.Errorf("chunk[0] = %q, want %q", chunks[0], "你好\n")
		}
	})
}

// ── setupMemoryFile / /cron setup / /bind setup ──────────────

type stubMemoryAgent struct {
	stubAgent
	memFile string
}

func (a *stubMemoryAgent) ProjectMemoryFile() string { return a.memFile }
func (a *stubMemoryAgent) GlobalMemoryFile() string  { return "" }

type stubNativePromptAgent struct {
	stubAgent
}

func (a *stubNativePromptAgent) HasSystemPromptSupport() bool { return true }

func TestSetupMemoryFile_WritesInstructions(t *testing.T) {
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "AGENTS.md")

	p := &stubPlatformEngine{n: "plain"}
	agent := &stubMemoryAgent{memFile: memFile}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	result, baseName, err := e.setupMemoryFile()
	if result != setupOK {
		t.Fatalf("result = %d, want setupOK; err = %v", result, err)
	}
	if baseName != "AGENTS.md" {
		t.Errorf("baseName = %q, want AGENTS.md", baseName)
	}

	content, _ := os.ReadFile(memFile)
	if !strings.Contains(string(content), ccConnectInstructionMarker) {
		t.Error("expected instruction marker in file")
	}
	if !strings.Contains(string(content), "cc-connect cron add") {
		t.Error("expected cron instructions in file")
	}
}

func TestSetupMemoryFile_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "AGENTS.md")

	p := &stubPlatformEngine{n: "plain"}
	agent := &stubMemoryAgent{memFile: memFile}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	r1, _, _ := e.setupMemoryFile()
	if r1 != setupOK {
		t.Fatalf("first call: result = %d, want setupOK", r1)
	}

	r2, _, _ := e.setupMemoryFile()
	if r2 != setupExists {
		t.Fatalf("second call: result = %d, want setupExists", r2)
	}
}

func TestSetupMemoryFile_RefreshesLegacyInstructions(t *testing.T) {
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "AGENTS.md")
	legacy := "\n" + ccConnectInstructionMarker + "\nlegacy instructions\n"
	if err := os.WriteFile(memFile, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy mem file: %v", err)
	}

	p := &stubPlatformEngine{n: "plain"}
	agent := &stubMemoryAgent{memFile: memFile}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	result, _, err := e.setupMemoryFile()
	if result != setupOK {
		t.Fatalf("result = %d, want setupOK; err = %v", result, err)
	}

	content, _ := os.ReadFile(memFile)
	if strings.Contains(string(content), "legacy instructions") {
		t.Fatalf("legacy instructions should be refreshed, got %q", string(content))
	}
	if !strings.Contains(string(content), "cc-connect send --image") {
		t.Fatalf("expected refreshed attachment instructions, got %q", string(content))
	}
}

func TestSetupMemoryFile_NativeAgent(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubNativePromptAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	result, _, _ := e.setupMemoryFile()
	if result != setupNative {
		t.Fatalf("result = %d, want setupNative", result)
	}
}

func TestSetupMemoryFile_NoMemorySupport(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	result, _, _ := e.setupMemoryFile()
	if result != setupNoMemory {
		t.Fatalf("result = %d, want setupNoMemory", result)
	}
}

func TestCmdCronSetup_WritesAndReplies(t *testing.T) {
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "AGENTS.md")

	p := &stubPlatformEngine{n: "plain"}
	agent := &stubMemoryAgent{memFile: memFile}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.cronScheduler = &CronScheduler{}

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	e.cmdCron(p, msg, []string{"setup"})

	if len(p.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "AGENTS.md") {
		t.Errorf("reply = %q, want to contain filename", p.sent[0])
	}
	if !strings.Contains(p.sent[0], "attachment send-back") {
		t.Errorf("reply = %q, want unified cc-connect setup success message", p.sent[0])
	}

	content, _ := os.ReadFile(memFile)
	if !strings.Contains(string(content), ccConnectInstructionMarker) {
		t.Error("expected instructions written to file")
	}
}

func TestCmdCronSetup_NativeAgentSkips(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubNativePromptAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.cronScheduler = &CronScheduler{}

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	e.cmdCron(p, msg, []string{"setup"})

	if len(p.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "natively supports") {
		t.Errorf("reply = %q, want native support message", p.sent[0])
	}
}

func TestCmdBindSetup_UsesSharedLogic(t *testing.T) {
	tmpDir := t.TempDir()
	memFile := filepath.Join(tmpDir, "AGENTS.md")

	p := &stubPlatformEngine{n: "plain"}
	agent := &stubMemoryAgent{memFile: memFile}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}
	e.cmdBindSetup(p, msg)

	if len(p.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "AGENTS.md") {
		t.Errorf("reply = %q, want to contain filename", p.sent[0])
	}

	content, _ := os.ReadFile(memFile)
	if !strings.Contains(string(content), ccConnectInstructionMarker) {
		t.Error("expected instructions written to file")
	}
}

// --- session resilience tests ---

// stubStartSessionAgent records StartSession calls and can fail on specific session IDs.
type stubStartSessionAgent struct {
	calls   []string
	failIDs map[string]error // session IDs that should fail
	mu      sync.Mutex
}

func (a *stubStartSessionAgent) Name() string { return "stub" }
func (a *stubStartSessionAgent) StartSession(_ context.Context, sessionID string) (AgentSession, error) {
	a.mu.Lock()
	a.calls = append(a.calls, sessionID)
	a.mu.Unlock()

	if err, ok := a.failIDs[sessionID]; ok {
		return nil, err
	}
	return &stubAgentSession{}, nil
}
func (a *stubStartSessionAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *stubStartSessionAgent) Stop() error { return nil }

func TestResumeFailureFallbackToFreshSession(t *testing.T) {
	agent := &stubStartSessionAgent{
		failIDs: map[string]error{
			"old-session-id": fmt.Errorf("Prompt is too long"),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := &Engine{
		agent:             agent,
		sessions:          NewSessionManager(""),
		ctx:               ctx,
		i18n:              NewI18n("en"),
		interactiveStates: make(map[string]*interactiveState),
		display:           DisplayCfg{},
	}

	session := e.sessions.GetOrCreateActive("test:user1")
	session.SetAgentSessionID("old-session-id", "stub")

	p := &stubPlatformEngine{n: "test"}
	state := e.getOrCreateInteractiveStateWith("test:user1", p, "ctx", session, e.sessions, nil, "")

	if state.agentSession == nil {
		t.Fatal("expected agentSession to be non-nil after fallback")
	}

	agent.mu.Lock()
	calls := append([]string{}, agent.calls...)
	agent.mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected 2 StartSession calls, got %d: %v", len(calls), calls)
	}
	if calls[0] != "old-session-id" {
		t.Fatalf("first StartSession call = %q, want saved session id", calls[0])
	}
	if calls[1] != "" {
		t.Fatalf("second StartSession call = %q, want empty string", calls[1])
	}
}

func TestFreshSessionWithoutSavedSessionIDStartsFresh(t *testing.T) {
	agent := &stubStartSessionAgent{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := &Engine{
		agent:             agent,
		sessions:          NewSessionManager(""),
		ctx:               ctx,
		i18n:              NewI18n("en"),
		interactiveStates: make(map[string]*interactiveState),
		display:           DisplayCfg{},
	}
	session := e.sessions.GetOrCreateActive("test:user2")

	p := &stubPlatformEngine{n: "test"}
	state := e.getOrCreateInteractiveStateWith("test:user2", p, "ctx", session, e.sessions, nil, "")

	if state.agentSession == nil {
		t.Fatal("expected agentSession to be non-nil")
	}

	agent.mu.Lock()
	calls := append([]string{}, agent.calls...)
	agent.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected 1 StartSession call, got %d: %v", len(calls), calls)
	}
	if calls[0] != "" {
		t.Fatalf("StartSession call = %q, want empty string (fresh session)", calls[0])
	}
}

func TestWorkspaceReconnectWithSavedSessionIDUsesExactResume(t *testing.T) {
	agent := &stubStartSessionAgent{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := &Engine{
		agent:             agent,
		sessions:          NewSessionManager(""),
		ctx:               ctx,
		i18n:              NewI18n("en"),
		interactiveStates: make(map[string]*interactiveState),
		display:           DisplayCfg{},
	}

	session := e.sessions.GetOrCreateActive("test:user3")
	session.SetAgentSessionID("saved-session-id", "stub")

	p := &stubPlatformEngine{n: "test"}
	state := e.getOrCreateInteractiveStateWith("test:user3", p, "ctx", session, e.sessions, nil, "")

	if state.agentSession == nil {
		t.Fatal("expected agentSession to be non-nil")
	}

	agent.mu.Lock()
	calls := append([]string{}, agent.calls...)
	agent.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected 1 StartSession call, got %d: %v", len(calls), calls)
	}
	if calls[0] != "saved-session-id" {
		t.Fatalf("StartSession call = %q, want saved session id", calls[0])
	}
}

func TestParseSelfReportedCtx(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"here is my response\n[ctx: ~42%]", 42},
		{"no context here", 0},
		{"response\n[ctx: ~100%]", 100},
		{"response\n[ctx: ~5%]", 5},
		{"", 0},
	}
	for _, tt := range tests {
		got := parseSelfReportedCtx(tt.input)
		if got != tt.want {
			t.Errorf("parseSelfReportedCtx(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestDrainEventsClosedChannel(t *testing.T) {
	ch := make(chan Event, 2)
	ch <- Event{Type: EventToolUse, Content: "a"}
	ch <- Event{Type: EventToolUse, Content: "b"}
	close(ch)

	done := make(chan struct{})
	go func() {
		drainEvents(ch)
		close(done)
	}()

	select {
	case <-done:
		// ok — returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("drainEvents did not return on closed channel (infinite loop)")
	}
}

func TestDrainEventsOpenChannel(t *testing.T) {
	ch := make(chan Event, 3)
	ch <- Event{Type: EventToolUse, Content: "a"}
	ch <- Event{Type: EventToolUse, Content: "b"}

	done := make(chan struct{})
	go func() {
		drainEvents(ch)
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("drainEvents did not return on open channel with buffered events")
	}

	// Channel should now be empty.
	select {
	case <-ch:
		t.Fatal("expected channel to be drained")
	default:
	}
}

// --- Message queuing tests ---

// queuingAgentSession records Send calls and emits events via a controllable channel.
type queuingAgentSession struct {
	controllableAgentSession
	sendCalls []string
	sendMu    sync.Mutex
}

func newQueuingSession(id string) *queuingAgentSession {
	return &queuingAgentSession{
		controllableAgentSession: controllableAgentSession{
			sessionID: id,
			alive:     true,
			events:    make(chan Event, 16),
			closed:    make(chan struct{}),
		},
	}
}

func (s *queuingAgentSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sendMu.Lock()
	s.sendCalls = append(s.sendCalls, prompt)
	s.sendMu.Unlock()
	return nil
}

// blockingSendAgentSession blocks in Send until unblock is closed, mimicking agents
// whose Send does not return until the prompt turn completes (e.g. ACP session/prompt).
type blockingSendAgentSession struct {
	controllableAgentSession
	sendStarted chan struct{} // sent to when Send begins waiting on unblock
	unblock     chan struct{} // close to let Send return
}

func newBlockingSendSession(id string) *blockingSendAgentSession {
	return &blockingSendAgentSession{
		controllableAgentSession: controllableAgentSession{
			sessionID: id,
			alive:     true,
			events:    make(chan Event, 16),
			closed:    make(chan struct{}),
		},
		sendStarted: make(chan struct{}, 1),
		unblock:     make(chan struct{}),
	}
}

func (s *blockingSendAgentSession) Send(_ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sendStarted <- struct{}{}
	<-s.unblock
	return nil
}

// blockingCloseAgentSession blocks in Close until releaseClose is closed.
// It is used to verify that /stop detaches the session and stops forwarding
// events before the underlying agent process has fully exited.
type blockingCloseAgentSession struct {
	controllableAgentSession
	closeStarted chan struct{}
	releaseClose chan struct{}
}

func newBlockingCloseSession(id string) *blockingCloseAgentSession {
	return &blockingCloseAgentSession{
		controllableAgentSession: controllableAgentSession{
			sessionID: id,
			alive:     true,
			events:    make(chan Event, 16),
			closed:    make(chan struct{}),
		},
		closeStarted: make(chan struct{}, 1),
		releaseClose: make(chan struct{}),
	}
}

func (s *blockingCloseAgentSession) Close() error {
	s.alive = false
	select {
	case s.closeStarted <- struct{}{}:
	default:
	}
	<-s.releaseClose
	close(s.events)
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// permSignalInlinePlatform wraps stubInlineButtonPlatform and signals when a
// SendWithButtons call includes perm:allow, so tests do not read buttonRows
// from another goroutine (race with the engine under -race).
type permSignalInlinePlatform struct {
	stubInlineButtonPlatform
	permAllowSent chan<- struct{}
}

func (p *permSignalInlinePlatform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]ButtonOption) error {
	if err := p.stubInlineButtonPlatform.SendWithButtons(ctx, replyCtx, content, buttons); err != nil {
		return err
	}
	for _, row := range buttons {
		for _, b := range row {
			if b.Data == "perm:allow" {
				select {
				case p.permAllowSent <- struct{}{}:
				default:
				}
				return nil
			}
		}
	}
	return nil
}

// Regression: permission events must be handled while Send is still blocked.
// If the engine called Send synchronously before reading Events(), this would deadlock
// and never call sendPermissionPrompt.
func TestProcessInteractiveEvents_PermissionWhileSendBlocked(t *testing.T) {
	permAllowSent := make(chan struct{}, 1)
	p := &permSignalInlinePlatform{
		stubInlineButtonPlatform: stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}},
		permAllowSent:            permAllowSent,
	}
	sess := newBlockingSendSession("blk-perm")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- sess.Send("prompt", nil, nil)
	}()

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, sendDone, nil)
		close(done)
	}()

	select {
	case <-sess.sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not reach blocking wait")
	}

	sess.events <- Event{
		Type:         EventPermissionRequest,
		RequestID:    "req-blocked-send",
		ToolName:     "write_file",
		ToolInput:    "/tmp/x",
		ToolInputRaw: map[string]any{"path": "/tmp/x"},
	}

	select {
	case <-permAllowSent:
	case <-time.After(2 * time.Second):
		t.Fatal("permission inline buttons not sent while Send blocked")
	}

	if !e.handlePendingPermission(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, "allow") {
		t.Fatal("expected handlePendingPermission to resolve pending request")
	}
	close(sess.unblock)

	sess.events <- Event{Type: EventResult, Content: "ok", Done: true}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processInteractiveEvents did not complete")
	}
}

func TestReapIdleWorkspaces_SkipsWorkspaceWithActiveTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingSendSession("busy-turn")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	e.workspacePool = newWorkspacePool(50 * time.Millisecond)

	workspaceDir := normalizeWorkspacePath(t.TempDir())
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		t.Fatal("expected session lock")
	}

	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{
			SessionKey: sessionKey,
			UserID:     "user1",
			Content:    "long running task",
			ReplyCtx:   "ctx",
		}, session, e.agent, e.sessions, sessionKey, workspaceDir, sessionKey)
		close(done)
	}()

	select {
	case <-sess.sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not reach blocking wait")
	}

	time.Sleep(100 * time.Millisecond)
	e.reapIdleWorkspaces()

	if !sess.Alive() {
		t.Fatal("idle reaper closed a session with an active turn")
	}
	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()
	if !exists {
		t.Fatal("idle reaper removed interactive state for an active turn")
	}

	close(sess.unblock)
	sess.events <- Event{Type: EventResult, Content: "done", Done: true}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processInteractiveMessageWith did not complete")
	}
}

func TestReapIdleWorkspaces_SkipsWorkspaceWaitingForPermission(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingSendSession("perm-wait")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	e.workspacePool = newWorkspacePool(50 * time.Millisecond)

	workspaceDir := normalizeWorkspacePath(t.TempDir())
	sessionKey := "test:user2"
	session := e.sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		t.Fatal("expected session lock")
	}

	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{
			SessionKey: sessionKey,
			UserID:     "user2",
			Content:    "needs approval",
			ReplyCtx:   "ctx",
		}, session, e.agent, e.sessions, sessionKey, workspaceDir, sessionKey)
		close(done)
	}()

	select {
	case <-sess.sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not reach blocking wait")
	}

	sess.events <- Event{
		Type:         EventPermissionRequest,
		RequestID:    "req-1",
		ToolName:     "write_file",
		ToolInput:    "/tmp/x",
		ToolInputRaw: map[string]any{"path": "/tmp/x"},
	}

	var pending *pendingPermission
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.interactiveMu.Lock()
		state := e.interactiveStates[sessionKey]
		e.interactiveMu.Unlock()
		if state != nil {
			state.mu.Lock()
			pending = state.pending
			state.mu.Unlock()
			if pending != nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending == nil {
		t.Fatal("expected pending permission while turn is waiting")
	}

	time.Sleep(100 * time.Millisecond)
	e.reapIdleWorkspaces()

	if !sess.Alive() {
		t.Fatal("idle reaper closed a session waiting for permission")
	}
	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()
	if !exists {
		t.Fatal("idle reaper removed interactive state while waiting for permission")
	}

	if !e.handlePendingPermission(p, &Message{
		SessionKey: sessionKey,
		UserID:     "user2",
		Content:    "allow",
		ReplyCtx:   "ctx",
	}, "allow") {
		t.Fatal("expected pending permission to be handled")
	}
	close(sess.unblock)
	sess.events <- Event{Type: EventResult, Content: "done", Done: true}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processInteractiveMessageWith did not complete after permission")
	}
}

func TestQueueMessageForBusySession_FIFODequeue(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("qs1")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"

	// Set up an interactive state as if a turn is in progress.
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx1",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// Queue two messages while the session is "busy".
	msg1 := &Message{SessionKey: key, Content: "msg1", ReplyCtx: "ctx-msg1"}
	msg2 := &Message{SessionKey: key, Content: "msg2", ReplyCtx: "ctx-msg2"}

	ok1 := e.queueMessageForBusySession(p, msg1, key)
	ok2 := e.queueMessageForBusySession(p, msg2, key)

	if !ok1 || !ok2 {
		t.Fatal("expected both messages to be queued successfully")
	}

	// Since deferred-send, messages are NOT sent to agent stdin at queue
	// time — only metadata is stored. Verify no Send calls occurred.
	sess.sendMu.Lock()
	if len(sess.sendCalls) != 0 {
		t.Fatalf("sendCalls = %v, want [] (deferred send)", sess.sendCalls)
	}
	sess.sendMu.Unlock()

	// Verify pending messages queue has correct FIFO order.
	state.mu.Lock()
	if len(state.pendingMessages) != 2 {
		t.Fatalf("pendingMessages len = %d, want 2", len(state.pendingMessages))
	}
	if state.pendingMessages[0].content != "msg1" || state.pendingMessages[1].content != "msg2" {
		t.Fatalf("pendingMessages = [%s, %s], want [msg1, msg2]",
			state.pendingMessages[0].content, state.pendingMessages[1].content)
	}
	state.mu.Unlock()
}

func TestProcessInteractiveEvents_DrainsQueuedMessages(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("qs2")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)

	// Pre-populate the interactive state with one queued message.
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx-turn1",
		pendingMessages: []queuedMessage{
			{platform: p, replyCtx: "ctx-turn2", content: "queued-msg"},
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// Simulate the agent completing turn 1 then turn 2.
	// Turn 2 events are pushed only after Send() is called for the queued
	// message, matching real-world timing where the agent doesn't produce
	// events for a turn until it receives the prompt on stdin.
	go func() {
		// Turn 1 result
		sess.events <- Event{Type: EventText, Content: "response1"}
		sess.events <- Event{Type: EventResult, Content: "response1", Done: true}
		// Wait for the queued message's Send() call before pushing turn 2 events.
		sess.sendMu.Lock()
		for len(sess.sendCalls) == 0 {
			sess.sendMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			sess.sendMu.Lock()
		}
		sess.sendMu.Unlock()
		// Turn 2 result (for the queued message)
		sess.events <- Event{Type: EventText, Content: "response2"}
		sess.events <- Event{Type: EventResult, Content: "response2", Done: true}
	}()

	session.AddHistory("user", "initial-msg")

	sendDone := make(chan error, 1)
	sendDone <- nil

	// processInteractiveEvents should handle both turns.
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "msg1", time.Now(), nil, sendDone, nil)
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("processInteractiveEvents did not complete in time")
	}

	// Verify queue is empty after processing.
	state.mu.Lock()
	remaining := len(state.pendingMessages)
	state.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("pendingMessages after processing = %d, want 0", remaining)
	}

	// Verify both turns recorded in session history.
	history := session.GetHistory(100)
	var assistantMsgs []string
	for _, h := range history {
		if h.Role == "assistant" {
			assistantMsgs = append(assistantMsgs, h.Content)
		}
	}
	if len(assistantMsgs) != 2 {
		t.Fatalf("assistant history entries = %d, want 2", len(assistantMsgs))
	}

	// Verify the queued message was also added to history.
	var userMsgs []string
	for _, h := range history {
		if h.Role == "user" {
			userMsgs = append(userMsgs, h.Content)
		}
	}
	if len(userMsgs) < 2 {
		t.Fatalf("user history entries = %d, want >= 2", len(userMsgs))
	}
}

// TestDrainOrphanedQueue_UsesWorkspaceSessionManager verifies that
// drainOrphanedQueue saves session history through the passed sessions
// manager (workspace-specific) rather than e.sessions (global).
func TestDrainOrphanedQueue_UsesWorkspaceSessionManager(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("qs-orphan")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Create a separate "workspace" session manager that drainOrphanedQueue should use.
	wsSessionsPath := filepath.Join(t.TempDir(), "ws_sessions.json")
	wsSessions := NewSessionManager(wsSessionsPath)

	key := "ws1:test:user1"
	session := wsSessions.GetOrCreateActive("test:user1")
	if !session.TryLock() {
		t.Fatal("expected TryLock to succeed")
	}

	// Set up interactive state with a queued message.
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
		pendingMessages: []queuedMessage{
			{platform: p, replyCtx: "ctx-q", content: "queued-orphan"},
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// Push events so the drain completes.
	go func() {
		sess.sendMu.Lock()
		for len(sess.sendCalls) == 0 {
			sess.sendMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			sess.sendMu.Lock()
		}
		sess.sendMu.Unlock()
		sess.events <- Event{Type: EventResult, Content: "orphan-response", Done: true}
	}()

	done := make(chan struct{})
	go func() {
		e.drainOrphanedQueue(session, wsSessions, key, agent, "")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainOrphanedQueue did not complete in time")
	}

	// The assistant response should be saved in the workspace session manager,
	// NOT in e.sessions (global).
	wsHistory := wsSessions.GetOrCreateActive("test:user1").GetHistory(0)
	var wsAssistant []string
	for _, h := range wsHistory {
		if h.Role == "assistant" {
			wsAssistant = append(wsAssistant, h.Content)
		}
	}
	if len(wsAssistant) == 0 {
		t.Fatal("expected assistant history in workspace session manager, got none")
	}

	// Verify e.sessions (global) does NOT have this history.
	globalSession := e.sessions.GetOrCreateActive("test:user1")
	globalHistory := globalSession.GetHistory(0)
	for _, h := range globalHistory {
		if h.Role == "assistant" && h.Content == "orphan-response" {
			t.Fatal("orphan response was saved to global e.sessions instead of workspace sessions")
		}
	}
}

// ── executeCardAction interactiveKey tests ───────────────────

func TestHandleCardNav_ModelSwitchesAndRefreshesCard(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubModelModeAgent{model: "old"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	sessionKey := "feishu:channel1:user1"
	card := e.handleCardNav("act:/model new-model", sessionKey)
	if card == nil {
		t.Fatal("expected immediate result card")
	}
	if text := card.RenderText(); !strings.Contains(text, "Model switched to `new-model`.") {
		t.Fatalf("result card = %q", text)
	}
	if agent.model != "new-model" {
		t.Fatalf("model = %q, want new-model", agent.model)
	}
	if refreshed := p.getRefreshedCards(); len(refreshed) != 0 {
		t.Fatalf("unexpected async refreshed cards: %d", len(refreshed))
	}
}

func TestHandleCardNav_ModelUsesWorkspaceContext(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	globalAgent := &stubModelModeAgent{model: "global-old"}
	e := NewEngine("test", globalAgent, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindingPath)

	wsDir := normalizeWorkspacePath(t.TempDir())
	channelID := "channel1"
	sessionKey := "feishu:" + channelID + ":user1"
	e.workspaceBindings.Bind("project:test", channelID, "chan", wsDir)

	ws := e.workspacePool.GetOrCreate(wsDir)
	wsAgent := &stubModelModeAgent{model: "workspace-old"}
	ws.agent = wsAgent
	ws.sessions = NewSessionManager("")

	interactiveKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	e.interactiveStates[interactiveKey] = &interactiveState{}
	e.interactiveMu.Unlock()

	globalSession := e.sessions.GetOrCreateActive(sessionKey)
	globalSession.SetAgentSessionID("global-session", "test")
	wsSession := ws.sessions.GetOrCreateActive(sessionKey)
	wsSession.SetAgentSessionID("workspace-session", "test")

	card := e.handleCardNav("act:/model switch 1", sessionKey)
	if card == nil {
		t.Fatal("expected immediate result card")
	}
	if text := card.RenderText(); !strings.Contains(text, "gpt-4.1") {
		t.Fatalf("result card = %q, want switched workspace model", text)
	}

	if wsAgent.model != "gpt-4.1" {
		t.Fatalf("workspace agent model = %q, want gpt-4.1", wsAgent.model)
	}
	if globalAgent.model != "global-old" {
		t.Fatalf("global agent model = %q, want unchanged", globalAgent.model)
	}
	if got := ws.sessions.GetOrCreateActive(sessionKey).AgentSessionID; got != "" {
		t.Fatalf("workspace session id = %q, want cleared", got)
	}
	if got := e.sessions.GetOrCreateActive(sessionKey).AgentSessionID; got != "global-session" {
		t.Fatalf("global session id = %q, want untouched", got)
	}
	if refreshed := p.getRefreshedCards(); len(refreshed) != 0 {
		t.Fatalf("unexpected async refreshed cards: %d", len(refreshed))
	}
}

func TestHandleCardNav_ModelSwitchFailureRefreshesCard(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubModelModeAgent{model: "old"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.modelSaveFunc = func(string) error { return errors.New("save failed") }

	sessionKey := "feishu:channel1:user1"
	card := e.handleCardNav("act:/model broken-model", sessionKey)
	if card == nil {
		t.Fatal("expected immediate failure card")
	}
	if text := card.RenderText(); !strings.Contains(text, "Failed to switch model: save model: save failed") {
		t.Fatalf("failure card = %q", text)
	}
	if refreshed := p.getRefreshedCards(); len(refreshed) != 0 {
		t.Fatalf("unexpected async refreshed cards: %d", len(refreshed))
	}
}

func TestHandleCardNav_ModelResultBackReturnsModelCard(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{model: "gpt-5.4"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	sessionKey := "feishu:channel1:user1"
	result := e.renderModelSwitchResultCard("gpt-5.4", nil)
	buttons := result.CollectButtons()
	if len(buttons) != 1 || len(buttons[0]) != 1 {
		t.Fatalf("result buttons = %#v, want single back button", buttons)
	}
	if buttons[0][0].Data != "nav:/model" {
		t.Fatalf("back button value = %q, want nav:/model", buttons[0][0].Data)
	}

	card := e.handleCardNav(buttons[0][0].Data, sessionKey)
	if card == nil {
		t.Fatal("expected /model card")
	}
	text := card.RenderText()
	if !strings.Contains(text, "Current model: gpt-5.4") {
		t.Fatalf("model card text = %q", text)
	}
}

func TestHandleCardNav_ModelCardUsesWorkspaceAgent(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	globalAgent := &stubModelModeAgent{model: "global-model"}
	e := NewEngine("test", globalAgent, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindingPath)

	wsDir := normalizeWorkspacePath(t.TempDir())
	channelID := "channel-nav"
	sessionKey := "feishu:" + channelID + ":user1"
	e.workspaceBindings.Bind("project:test", channelID, "chan", wsDir)

	ws := e.workspacePool.GetOrCreate(wsDir)
	ws.agent = &stubModelModeAgent{model: "workspace-model"}
	ws.sessions = NewSessionManager("")

	card := e.handleCardNav("nav:/model", sessionKey)
	if card == nil {
		t.Fatal("expected /model card")
	}
	text := card.RenderText()
	if !strings.Contains(text, "workspace-model") {
		t.Fatalf("model card text = %q, want workspace model", text)
	}
	if strings.Contains(text, "global-model") {
		t.Fatalf("model card text = %q, should not use global model", text)
	}
}

func TestExecuteCardAction_ModeCleansUpWithInteractiveKey(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{mode: "default"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	sessionKey := "feishu:channel1:user1"

	e.interactiveMu.Lock()
	e.interactiveStates[sessionKey] = &interactiveState{}
	e.interactiveMu.Unlock()

	e.executeCardAction("/mode", "yolo", sessionKey)

	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()
	if exists {
		t.Error("expected interactive state to be cleaned up after /mode")
	}
}

// ===========================================================================
// P0 Beta release tests
// ===========================================================================

// --- 1. Message queue overflow ---

func TestQueueMessageOverflow_DropsOldestAndReturnsfalse(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("qs-overflow")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:overflow-user"

	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// Fill the queue to defaultMaxQueuedMessages (5).
	for i := 0; i < defaultMaxQueuedMessages; i++ {
		msg := &Message{SessionKey: key, Content: fmt.Sprintf("msg-%d", i), ReplyCtx: fmt.Sprintf("ctx-%d", i)}
		ok := e.queueMessageForBusySession(p, msg, key)
		if !ok {
			t.Fatalf("expected msg-%d to be queued, got false", i)
		}
	}

	state.mu.Lock()
	if len(state.pendingMessages) != defaultMaxQueuedMessages {
		t.Fatalf("queue depth = %d, want %d", len(state.pendingMessages), defaultMaxQueuedMessages)
	}
	state.mu.Unlock()

	// The 6th message should be handled (returns true) but not queued — MsgQueueFull sent.
	overflow := &Message{SessionKey: key, Content: "msg-overflow", ReplyCtx: "ctx-overflow"}
	ok := e.queueMessageForBusySession(p, overflow, key)
	if !ok {
		t.Fatal("expected 6th message to be handled (queue-full reply), got false")
	}

	// Queue should still have exactly defaultMaxQueuedMessages items (the original 5).
	state.mu.Lock()
	if len(state.pendingMessages) != defaultMaxQueuedMessages {
		t.Fatalf("queue depth after overflow = %d, want %d", len(state.pendingMessages), defaultMaxQueuedMessages)
	}
	// First message should still be msg-0 (FIFO preserved, no silent drop).
	if state.pendingMessages[0].content != "msg-0" {
		t.Fatalf("first queued = %q, want msg-0", state.pendingMessages[0].content)
	}
	state.mu.Unlock()

	// Platform should have received MsgMessageQueued for 5 accepted + MsgQueueFull for the overflow.
	sent := p.getSent()
	if len(sent) != defaultMaxQueuedMessages+1 {
		t.Fatalf("platform replies = %d, want %d (queued + queue-full)", len(sent), defaultMaxQueuedMessages+1)
	}
}

func TestQueueMessage_NoState_ReturnsFalse(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := newTestEngine()

	msg := &Message{SessionKey: "nonexistent:key", Content: "hello"}
	ok := e.queueMessageForBusySession(p, msg, "nonexistent:key")
	if ok {
		t.Fatal("expected false when no interactive state exists")
	}
}

func TestQueueMessage_DeadSession_ReturnsFalse(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("dead")
	sess.alive = false
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:dead-session"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: key, Content: "hello"}
	ok := e.queueMessageForBusySession(p, msg, key)
	if ok {
		t.Fatal("expected false for dead session")
	}
}

// TestQueueMessage_NilAgentSession_DuringStartup verifies that messages can be
// queued when the interactiveState exists but agentSession is nil (session is
// still starting up). This is the fix for issue #565.
func TestQueueMessage_NilAgentSession_DuringStartup(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := newTestEngine()

	key := "test:starting-session"
	// Simulate the placeholder state created by ensureInteractiveStateForQueueing
	state := &interactiveState{
		platform: p,
		replyCtx: "ctx",
		// agentSession is nil — session is starting up
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: key, Content: "queued during startup", ReplyCtx: "ctx-startup"}
	ok := e.queueMessageForBusySession(p, msg, key)
	if !ok {
		t.Fatal("expected true: messages should be queueable during session startup")
	}

	state.mu.Lock()
	if len(state.pendingMessages) != 1 {
		t.Fatalf("pendingMessages len = %d, want 1", len(state.pendingMessages))
	}
	if state.pendingMessages[0].content != "queued during startup" {
		t.Fatalf("queued content = %q, want %q", state.pendingMessages[0].content, "queued during startup")
	}
	state.mu.Unlock()
}

// --- 2. /compress flow ---

type stubCompressorAgent struct {
	stubAgent
	cmd string
}

func (a *stubCompressorAgent) CompressCommand() string { return a.cmd }

func TestCmdCompress_NoCompressor_RepliesNotSupported(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:user1", Content: "/compress", ReplyCtx: "ctx"}
	e.cmdCompress(p, msg)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected a reply")
	}
	if !strings.Contains(sent[0], e.i18n.T(MsgCompressNotSupported)) {
		t.Fatalf("expected MsgCompressNotSupported, got %q", sent[0])
	}
}

func TestCmdCompress_NoSession_RepliesNoSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &stubCompressorAgent{cmd: "/compact"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:user1", Content: "/compress", ReplyCtx: "ctx"}
	e.cmdCompress(p, msg)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected a reply")
	}
	if !strings.Contains(sent[0], e.i18n.T(MsgCompressNoSession)) {
		t.Fatalf("expected MsgCompressNoSession, got %q", sent[0])
	}
}

func TestAutoCompress_TriggerAfterResult(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("auto-compress")
	agent := &stubCompressorAgent{cmd: "/compact"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetAutoCompressConfig(true, 4, 0) // tiny threshold

	key := "test:user1"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// Seed history so estimate crosses threshold after assistant response.
	session := e.sessions.GetOrCreateActive(key)
	session.AddHistory("user", "hello world")

	// Simulate a full turn.
	go e.processInteractiveEvents(state, session, e.sessions, key, "msg1", time.Now(), func() {}, nil, nil)

	sess.events <- Event{Type: EventResult, Content: "response", Done: true}

	// The auto-compress should send /compact to the agent session.
	deadline := time.After(2 * time.Second)
	for {
		sess.sendMu.Lock()
		n := len(sess.sendCalls)
		sess.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for auto-compress send")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	sess.sendMu.Lock()
	last := sess.sendCalls[len(sess.sendCalls)-1]
	sess.sendMu.Unlock()
	if last != "/compact" {
		t.Fatalf("expected /compact auto-compress, got %q", last)
	}
}

func TestCmdCompress_SessionBusy_RepliesPreviousProcessing(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("compress-busy")
	agent := &stubCompressorAgent{cmd: "/compact"}
	agent.stubAgent = stubAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// Lock the session to simulate busy.
	session := e.sessions.GetOrCreateActive(key)
	if !session.TryLock() {
		t.Fatal("expected TryLock to succeed")
	}

	msg := &Message{SessionKey: key, Content: "/compress", ReplyCtx: "ctx"}
	e.cmdCompress(p, msg)

	sent := p.getSent()
	found := false
	for _, s := range sent {
		if strings.Contains(s, e.i18n.T(MsgPreviousProcessing)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected MsgPreviousProcessing reply, got %v", sent)
	}
	session.Unlock()
}

func TestCmdCompress_Success_SendsCompressDone(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("compress-ok")
	agent := &stubCompressorAgent{cmd: "/compact"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: key, Content: "/compress", ReplyCtx: "ctx"}
	e.cmdCompress(p, msg)

	// Wait for Send to be called (happens after drainEvents), then inject the result event.
	deadline := time.After(3 * time.Second)
	for {
		sess.sendMu.Lock()
		n := len(sess.sendCalls)
		sess.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for compress Send call")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	sess.events <- Event{Type: EventResult, Content: "", Done: true}

	for {
		sent := p.getSent()
		foundDone := false
		for _, s := range sent {
			if strings.Contains(s, e.i18n.T(MsgCompressDone)) {
				foundDone = true
			}
		}
		if foundDone {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for MsgCompressDone, sent = %v", p.getSent())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCmdCompress_WithText_SendsResult(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("compress-text")
	agent := &stubCompressorAgent{cmd: "/compact"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: key, Content: "/compress", ReplyCtx: "ctx"}
	e.cmdCompress(p, msg)

	// Wait for Send to be called (happens after drainEvents).
	deadline := time.After(3 * time.Second)
	for {
		sess.sendMu.Lock()
		n := len(sess.sendCalls)
		sess.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for compress Send call")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	sess.events <- Event{Type: EventText, Content: "Compressed to 50%"}
	sess.events <- Event{Type: EventResult, Content: "Compression complete", Done: true}

	for {
		sent := p.getSent()
		foundResult := false
		for _, s := range sent {
			if strings.Contains(s, "Compression complete") {
				foundResult = true
			}
		}
		if foundResult {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for compress result, sent = %v", p.getSent())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCmdCompress_DrainsQueueAfterSuccess(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("compress-drain")
	agent := &stubCompressorAgent{cmd: "/compact"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
		pendingMessages: []queuedMessage{
			{platform: p, replyCtx: "ctx-q1", content: "queued-after-compress"},
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: key, Content: "/compress", ReplyCtx: "ctx"}
	e.cmdCompress(p, msg)

	// Complete compress.
	sess.events <- Event{Type: EventResult, Content: "", Done: true}

	// Wait for Send to be called (drain of queued message).
	deadline := time.After(3 * time.Second)
	for {
		sess.sendMu.Lock()
		n := len(sess.sendCalls)
		sess.sendMu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for queued message to be sent after compress")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Provide events for the drained turn so processInteractiveEvents completes.
	sess.events <- Event{Type: EventResult, Content: "drain-done", Done: true}

	// Verify the queued message was actually sent.
	time.Sleep(100 * time.Millisecond)
	sess.sendMu.Lock()
	calls := make([]string, len(sess.sendCalls))
	copy(calls, sess.sendCalls)
	sess.sendMu.Unlock()

	if len(calls) == 0 {
		t.Fatal("expected at least one Send call for the queued message")
	}
	found := false
	for _, c := range calls {
		if strings.Contains(c, "queued-after-compress") {
			found = true
		}
	}
	if !found {
		t.Fatalf("queued message not found in send calls: %v", calls)
	}
}

// --- 3. executeCardAction routing ---

func TestExecuteCardAction_CronEnable(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Add(&CronJob{ID: "job1", CronExpr: "0 9 * * *", Enabled: false})
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	e.executeCardAction("/cron", "enable job1", "test:user1")

	job := store.Get("job1")
	if job == nil {
		t.Fatal("job not found")
	}
	if !job.Enabled {
		t.Error("expected job to be enabled after card action")
	}
}

func TestExecuteCardAction_CronDisable(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Add(&CronJob{ID: "job1", CronExpr: "0 9 * * *", Enabled: true})
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	e.executeCardAction("/cron", "disable job1", "test:user1")

	job := store.Get("job1")
	if job == nil {
		t.Fatal("job not found")
	}
	if job.Enabled {
		t.Error("expected job to be disabled after card action")
	}
}

func TestExecuteCardAction_CronDelete(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Add(&CronJob{ID: "del-job", CronExpr: "0 9 * * *", Enabled: true})
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	e.executeCardAction("/cron", "delete del-job", "test:user1")

	job := store.Get("del-job")
	if job != nil {
		t.Error("expected job to be deleted after card action")
	}
}

func TestExecuteCardAction_CronMuteUnmute(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Add(&CronJob{ID: "mute-job", CronExpr: "0 9 * * *", Enabled: true})
	scheduler := NewCronScheduler(store)
	e.cronScheduler = scheduler

	e.executeCardAction("/cron", "mute mute-job", "test:user1")
	job := store.Get("mute-job")
	if job == nil || !job.Mute {
		t.Error("expected job to be muted")
	}

	e.executeCardAction("/cron", "unmute mute-job", "test:user1")
	job = store.Get("mute-job")
	if job == nil || job.Mute {
		t.Error("expected job to be unmuted")
	}
}

func TestExecuteCardAction_CronNoScheduler_NoPanic(t *testing.T) {
	e := newTestEngine()
	// cronScheduler is nil — should not panic.
	e.executeCardAction("/cron", "enable job1", "test:user1")
}

func TestExecuteCardAction_CronBadArgs_NoPanic(t *testing.T) {
	store, _ := NewCronStore(t.TempDir())
	scheduler := NewCronScheduler(store)
	e := newTestEngine()
	e.cronScheduler = scheduler

	// Missing ID.
	e.executeCardAction("/cron", "enable", "test:user1")
	// Empty args.
	e.executeCardAction("/cron", "", "test:user1")
}

func TestExecuteCardAction_StopCleansUp(t *testing.T) {
	sess := newControllableSession("stop-test")
	e := newTestEngine()
	key := "test:user1"

	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{agentSession: sess}
	e.interactiveMu.Unlock()

	e.executeCardAction("/stop", "", key)

	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[key]
	e.interactiveMu.Unlock()

	if exists {
		t.Error("expected interactive state to be removed after /stop")
	}
}

func TestExecuteCardAction_StopClearsInteractiveState(t *testing.T) {
	sess := newControllableSession("stop-quiet")
	e := newTestEngine()
	key := "test:user1"

	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{agentSession: sess}
	e.interactiveMu.Unlock()

	e.executeCardAction("/stop", "", key)

	e.interactiveMu.Lock()
	state, exists := e.interactiveStates[key]
	e.interactiveMu.Unlock()

	if exists || state != nil {
		t.Fatal("expected interactive state to be removed after /stop")
	}
}

func TestCmdStop_ReturnsWhileCloseBlockedAndStopsEventLoop(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingCloseSession("stop-blocked")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)

	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "msg-1", time.Now(), nil, nil, "ctx")
		close(done)
	}()

	stopDone := make(chan struct{})
	go func() {
		e.cmdStop(p, &Message{SessionKey: key, ReplyCtx: "ctx"})
		close(stopDone)
	}()

	select {
	case <-sess.closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected Close to start after /stop")
	}

	select {
	case <-stopDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cmdStop blocked on Close")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event loop did not stop after /stop")
	}

	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if exists {
		t.Fatal("expected interactive state to be removed after /stop")
	}

	sess.events <- Event{Type: EventText, Content: "stale output"}
	sess.events <- Event{Type: EventResult, Content: "stale result", Done: true}
	time.Sleep(50 * time.Millisecond)

	sent := p.getSent()
	if len(sent) != 1 || sent[0] != e.i18n.T(MsgExecutionStopped) {
		t.Fatalf("sent messages = %v, want only execution stopped", sent)
	}

	close(sess.releaseClose)
	select {
	case <-sess.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after release")
	}
}

func TestExecuteCardAction_NewCleansUpAndCreatesSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:user1"

	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{agentSession: newControllableSession("old")}
	e.interactiveMu.Unlock()

	e.executeCardAction("/new", "", key)

	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[key]
	e.interactiveMu.Unlock()

	if exists {
		t.Error("expected old interactive state to be cleaned up after /new")
	}
}

func TestExecuteCardAction_LangSwitch(t *testing.T) {
	e := newTestEngine()

	e.executeCardAction("/lang", "zh", "test:user1")
	if e.i18n.CurrentLang() != LangChinese {
		t.Errorf("expected LangChinese, got %v", e.i18n.CurrentLang())
	}

	e.executeCardAction("/lang", "en", "test:user1")
	if e.i18n.CurrentLang() != LangEnglish {
		t.Errorf("expected LangEnglish, got %v", e.i18n.CurrentLang())
	}

	e.executeCardAction("/lang", "ja", "test:user1")
	if e.i18n.CurrentLang() != LangJapanese {
		t.Errorf("expected LangJapanese, got %v", e.i18n.CurrentLang())
	}
}

func TestExecuteCardAction_UnknownCommand_NoPanic(t *testing.T) {
	e := newTestEngine()
	// Should not panic for unrecognized commands.
	e.executeCardAction("/nonexistent", "args", "test:user1")
	e.executeCardAction("", "", "test:user1")
}

// --- 4. Multi-workspace command handlers use interactiveKey ---

func TestCmdStatus_UsesInteractiveKeyForMultiWorkspace(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "card"}}
	agent := &stubModelModeAgent{model: "gpt-4.1", mode: "default"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{
		ThinkingMessages: false,
		ThinkingMaxLen:   300,
		ToolMaxLen:       500,
		ToolMessages:     true,
	})

	msg := &Message{SessionKey: "feishu:ch1:user1", Content: "/status", ReplyCtx: "ctx"}
	e.cmdStatus(p, msg)

	if len(p.repliedCards) == 0 && len(p.sentCards) == 0 {
		sent := strings.Join(p.getSent(), "\n")
		if !strings.Contains(sent, "Thinking messages: OFF") || !strings.Contains(sent, "Tool progress: ON") {
			t.Fatalf("expected status to reflect display flags, got %q", sent)
		}
	}
}

func TestCmdStop_UsesInteractiveKeyForMultiWorkspace(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("ws-stop-test")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	wsDir := t.TempDir()
	rawKey := "feishu:ch1:user1"
	wsKey := wsDir + ":" + rawKey

	iKey := e.interactiveKeyForSessionKey(wsKey)
	e.interactiveMu.Lock()
	e.interactiveStates[iKey] = &interactiveState{agentSession: sess}
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: wsKey, Content: "/stop", ReplyCtx: "ctx"}
	e.cmdStop(p, msg)

	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[iKey]
	e.interactiveMu.Unlock()

	if exists {
		t.Error("expected interactive state to be cleaned up by /stop using interactiveKey")
	}
}

// ===========================================================================
// Beta pre-release tests: inject_sender, idle_timeout, /shell, /workspace,
//                         /switch, /memory
// ===========================================================================

// --- 1. inject_sender ---

func TestBuildSenderPrompt_Enabled(t *testing.T) {
	e := newTestEngine()
	e.SetInjectSender(true)

	result := e.buildSenderPrompt("hello world", "user123", "Alice", "feishu", "feishu:channel42:user123")
	expected := "[cc-connect sender_id=user123 sender_name=\"Alice\" platform=feishu chat_id=channel42]\nhello world"
	if result != expected {
		t.Fatalf("got %q, want %q", result, expected)
	}
}

func TestBuildSenderPrompt_Disabled(t *testing.T) {
	e := newTestEngine()
	e.SetInjectSender(false)

	result := e.buildSenderPrompt("hello", "user1", "Alice", "feishu", "feishu:ch:user1")
	if result != "hello" {
		t.Fatalf("expected raw content when disabled, got %q", result)
	}
}

func TestBuildSenderPrompt_EmptyUserID(t *testing.T) {
	e := newTestEngine()
	e.SetInjectSender(true)

	result := e.buildSenderPrompt("hello", "", "Bob", "telegram", "telegram:ch:user1")
	if result != "hello" {
		t.Fatalf("expected raw content when userID is empty, got %q", result)
	}
}

func TestBuildSenderPrompt_EmptyUserName(t *testing.T) {
	e := newTestEngine()
	e.SetInjectSender(true)

	result := e.buildSenderPrompt("hello", "user1", "", "feishu", "feishu:ch:user1")
	expected := "[cc-connect sender_id=user1 platform=feishu chat_id=ch]\nhello"
	if result != expected {
		t.Fatalf("got %q, want %q", result, expected)
	}
}

func TestBuildSenderPrompt_NameWithSpaces(t *testing.T) {
	e := newTestEngine()
	e.SetInjectSender(true)

	result := e.buildSenderPrompt("hi", "U999", "Jim Tang", "slack", "slack:C012:U999")
	expected := "[cc-connect sender_id=U999 sender_name=\"Jim Tang\" platform=slack chat_id=C012]\nhi"
	if result != expected {
		t.Fatalf("got %q, want %q", result, expected)
	}
}

func TestExtractChannelID(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"feishu:channel42:user1", "channel42"},
		{"telegram:group123:user2", "group123"},
		{"plain", ""},
		{"a:b", "b"},
		{"a:b:c:d", "b"},
	}
	for _, tt := range tests {
		got := extractChannelID(tt.key)
		if got != tt.want {
			t.Errorf("extractChannelID(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestBuildSenderPrompt_DifferentPlatforms(t *testing.T) {
	e := newTestEngine()
	e.SetInjectSender(true)

	platforms := []struct {
		platform   string
		sessionKey string
		wantChat   string
	}{
		{"telegram", "telegram:group99:alice", "group99"},
		{"discord", "discord:server1:bob", "server1"},
		{"slack", "slack:C012345:carol", "C012345"},
	}
	for _, tc := range platforms {
		result := e.buildSenderPrompt("msg", "uid", "TestUser", tc.platform, tc.sessionKey)
		if !strings.Contains(result, "platform="+tc.platform) {
			t.Errorf("missing platform=%s in %q", tc.platform, result)
		}
		if !strings.Contains(result, "chat_id="+tc.wantChat) {
			t.Errorf("missing chat_id=%s in %q", tc.wantChat, result)
		}
	}
}

func TestBuildSenderPrompt_SanitizesSpecialChars(t *testing.T) {
	e := newTestEngine()
	e.SetInjectSender(true)

	result := e.buildSenderPrompt("hi", "U1", "Evil\"Name\nInject", "slack", "slack:C1:U1")
	if strings.Contains(result, `"Name`) || strings.Contains(result, "\n"+`Inject`) {
		t.Fatalf("quotes/newlines should be sanitized, got %q", result)
	}
	if !strings.Contains(result, `sender_name="Evil'Name Inject"`) {
		t.Fatalf("expected sanitized name, got %q", result)
	}
}

func TestResolveLocalDirPath_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	_, err := resolveLocalDirPath("../../etc", base)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestResolveLocalDirPath_AcceptsSubdir(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "project")
	os.MkdirAll(sub, 0755)
	got, err := resolveLocalDirPath("project", base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sub {
		t.Fatalf("expected %q, got %q", sub, got)
	}
}

func TestResolveLocalDirPath_AbsoluteAllowed(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveLocalDirPath(dir, "/some/base")
	if err != nil {
		t.Fatalf("absolute path should be allowed: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty path")
	}
}

// --- 2. idle_timeout ---

func TestEventIdleTimeout_CleansUpSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("idle-test")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetEventIdleTimeout(100 * time.Millisecond)

	key := "test:idle-user"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	session := e.sessions.GetOrCreateActive(key)
	session.TryLock()

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "", time.Now(), nil, nil, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processInteractiveEvents did not return after idle timeout")
	}

	sent := p.getSent()
	foundTimeout := false
	for _, s := range sent {
		if strings.Contains(s, "timed out") {
			foundTimeout = true
		}
	}
	if !foundTimeout {
		t.Fatalf("expected timeout error message, got %v", sent)
	}
}

func TestEventIdleTimeout_ResetOnEvent(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("idle-reset")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetEventIdleTimeout(200 * time.Millisecond)

	key := "test:idle-reset"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	session := e.sessions.GetOrCreateActive(key)
	session.TryLock()

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "", time.Now(), nil, nil, nil)
		close(done)
	}()

	// Send a text event at 100ms (before the 200ms timeout), resetting the timer.
	time.Sleep(100 * time.Millisecond)
	sess.events <- Event{Type: EventText, Content: "thinking..."}

	// Then send the result at 150ms after the text event (within the reset 200ms window).
	time.Sleep(150 * time.Millisecond)
	sess.events <- Event{Type: EventResult, Content: "done", Done: true}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processInteractiveEvents did not complete after events")
	}

	sent := p.getSent()
	foundTimeout := false
	for _, s := range sent {
		if strings.Contains(s, "timed out") {
			foundTimeout = true
		}
	}
	if foundTimeout {
		t.Error("should NOT have timed out — events should have reset the timer")
	}
}

func TestEventIdleTimeout_DisabledWhenZero(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("idle-zero")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetEventIdleTimeout(0)

	key := "test:idle-zero"
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	session := e.sessions.GetOrCreateActive(key)
	session.TryLock()

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "", time.Now(), nil, nil, nil)
		close(done)
	}()

	// With timeout disabled, it should block until we send a result.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("should not have returned yet — timeout is disabled and no events sent")
	default:
	}

	sess.events <- Event{Type: EventResult, Content: "ok", Done: true}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after result event")
	}
}

// --- 3. /shell command ---

func TestCmdShell_BlockedWithoutAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	msg := &Message{
		SessionKey: "test:ch:user1",
		Content:    "/shell ls -la",
		ReplyCtx:   "ctx",
		UserID:     "user1",
		Platform:   "test",
	}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundAdmin := false
	for _, s := range sent {
		if strings.Contains(s, e.i18n.T(MsgAdminRequired)[:10]) || strings.Contains(s, "admin") {
			foundAdmin = true
		}
	}
	if !foundAdmin {
		t.Fatalf("expected admin required reply, got %v", sent)
	}
}

func TestCmdShell_AllowedForAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin-user")

	msg := &Message{
		SessionKey: "test:ch:admin-user",
		Content:    "/shell echo hello",
		ReplyCtx:   "ctx",
		UserID:     "admin-user",
		Platform:   "test",
	}
	e.handleCommand(p, msg, msg.Content)

	// Give the async goroutine time to complete.
	time.Sleep(500 * time.Millisecond)

	sent := p.getSent()
	foundAdmin := false
	for _, s := range sent {
		if strings.Contains(s, "admin") && strings.Contains(s, "privilege") {
			foundAdmin = true
		}
	}
	if foundAdmin {
		t.Fatalf("admin user should not be blocked, got %v", sent)
	}
}

func TestCmdShell_EmptyCommand_ShowsUsage(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")

	// Call cmdShell directly with empty command to test usage path.
	msg := &Message{
		SessionKey: "test:ch:admin",
		Content:    "/shell",
		ReplyCtx:   "ctx",
		UserID:     "admin",
		Platform:   "test",
	}
	e.cmdShell(p, msg, "/shell ")

	sent := p.getSent()
	foundUsage := false
	for _, s := range sent {
		if strings.Contains(s, "Usage") || strings.Contains(s, "/shell") {
			foundUsage = true
		}
	}
	if !foundUsage {
		t.Fatalf("expected usage message, got %v", sent)
	}
}

func TestCmdShell_MultiWorkspaceUsesSharedBindingWorkDir(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	wsDir := filepath.Join(baseDir, "shared-shell-workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	normalizedWsDir := normalizeWorkspacePath(wsDir)
	e.workspaceBindings.Bind(sharedWorkspaceBindingsKey, "ch1", "shared-shell", normalizedWsDir)

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/shell pwd",
		ReplyCtx:   "ctx",
	}
	e.cmdShell(p, msg, "/shell pwd")

	deadline := time.Now().Add(2 * time.Second)
	for {
		sent := p.getSent()
		if len(sent) > 0 {
			if !strings.Contains(sent[0], normalizedWsDir) {
				t.Fatalf("expected shell output to contain shared workspace %q, got %q", normalizedWsDir, sent[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for shell response")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCmdShell_MultiWorkspaceIgnoresMissingSharedBinding(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &stubWorkDirAgent{workDir: t.TempDir()}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	missingDir := filepath.Join(baseDir, "missing-shared-workspace")
	e.workspaceBindings.Bind(sharedWorkspaceBindingsKey, "ch1", "shared-shell", missingDir)

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/shell pwd",
		ReplyCtx:   "ctx",
	}
	e.cmdShell(p, msg, "/shell pwd")

	deadline := time.Now().Add(2 * time.Second)
	// Normalize both the expected and missing paths to handle macOS symlink
	// resolution (e.g. /var/folders/ -> /private/var/folders/). Then check
	// that the shell output contains the resolved expected path and does NOT
	// contain the resolved missing path.
	expectedResolved := normalizeWorkspacePath(agent.workDir)
	missingResolved := normalizeWorkspacePath(missingDir)
	for {
		sent := p.getSent()
		if len(sent) > 0 {
			output := sent[0]
			if !strings.Contains(output, agent.workDir) && !strings.Contains(output, expectedResolved) {
				t.Fatalf("expected shell output to fall back to agent work dir %q (resolved %q), got %q", agent.workDir, expectedResolved, output)
			}
			if strings.Contains(output, missingDir) || strings.Contains(output, missingResolved) {
				t.Fatalf("expected shell output to ignore missing shared workspace %q, got %q", missingDir, output)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for shell response")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- /diff command tests ---

func TestCmdDiff_BlockedWithoutAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	msg := &Message{
		SessionKey: "test:ch:user1",
		Content:    "/diff main",
		ReplyCtx:   "ctx",
		UserID:     "user1",
		Platform:   "test",
	}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundAdmin := false
	for _, s := range sent {
		if strings.Contains(s, "admin") || strings.Contains(s, e.i18n.T(MsgAdminRequired)[:10]) {
			foundAdmin = true
		}
	}
	if !foundAdmin {
		t.Fatalf("expected admin required reply, got %v", sent)
	}
}

func TestCmdDiff_EmptyDiff(t *testing.T) {
	// Create a temp git repo with no changes
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s %v", args, out, err)
		}
	}

	agent := &stubWorkDirAgent{workDir: dir}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")

	msg := &Message{
		SessionKey: "test:ch:admin",
		Content:    "/diff",
		ReplyCtx:   "ctx",
		UserID:     "admin",
		Platform:   "test",
	}
	e.cmdDiff(p, msg, "/diff")

	deadline := time.Now().Add(2 * time.Second)
	for {
		sent := p.getSent()
		if len(sent) > 0 {
			found := false
			for _, s := range sent {
				if strings.Contains(s, "diff") || strings.Contains(s, "clean") {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected empty diff message, got %v", sent)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for diff response")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCmdDiff_PlainTextFallback(t *testing.T) {
	// Create a temp git repo with uncommitted changes
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s %v", args, out, err)
		}
	}
	// Create and commit a file, then modify it
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "test.txt"},
		{"git", "commit", "-m", "add test.txt"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s %v", args, out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello\nworld\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Use stubPlatformEngine (no FileSender) → should fall back to plain text
	agent := &stubWorkDirAgent{workDir: dir}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")

	msg := &Message{
		SessionKey: "test:ch:admin",
		Content:    "/diff",
		ReplyCtx:   "ctx",
		UserID:     "admin",
		Platform:   "test",
	}
	e.cmdDiff(p, msg, "/diff")

	deadline := time.Now().Add(2 * time.Second)
	for {
		sent := p.getSent()
		if len(sent) > 0 {
			found := false
			for _, s := range sent {
				if strings.Contains(s, "```diff") && strings.Contains(s, "world") {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected plain text diff with ```diff block, got %v", sent)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for diff response")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCmdDiff_FileSenderPath(t *testing.T) {
	// Create a temp git repo with uncommitted changes
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s %v", args, out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "test.txt"},
		{"git", "commit", "-m", "add test.txt"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s %v", args, out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	agent := &stubWorkDirAgent{workDir: dir}
	mp := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", agent, []Platform{mp}, "", LangEnglish)
	e.SetAdminFrom("admin")

	msg := &Message{
		SessionKey: "test:ch:admin",
		Content:    "/diff",
		ReplyCtx:   "ctx",
		UserID:     "admin",
		Platform:   "test",
	}
	e.cmdDiff(mp, msg, "/diff")

	deadline := time.Now().Add(2 * time.Second)
	for {
		// If diff2html is installed, we get a file; otherwise plain text fallback
		files := mp.files
		sent := mp.getSent()
		if len(files) > 0 {
			f := files[0]
			if f.MimeType != "text/html" {
				t.Fatalf("expected text/html, got %s", f.MimeType)
			}
			if !strings.HasSuffix(f.FileName, ".html") {
				t.Fatalf("expected .html filename, got %s", f.FileName)
			}
			return
		}
		if len(sent) > 0 {
			// diff2html not installed → plain text fallback is also acceptable
			found := false
			for _, s := range sent {
				if strings.Contains(s, "```diff") {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected diff output (file or plain text), got %v", sent)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for diff response")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCmdShow_EmptyReference_ShowsUsage(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")

	msg := &Message{
		SessionKey: "test:ch:admin",
		Content:    "/show",
		ReplyCtx:   "ctx",
		UserID:     "admin",
		Platform:   "test",
	}
	e.cmdShow(p, msg, nil)

	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "/show") {
		t.Fatalf("sent = %v, want show usage", sent)
	}
}

func TestCmdShow_MultiWorkspaceUsesBoundWorkDirForRelativeReference(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agentName := "test-show-workspace"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		return &namedStubModelModeAgent{name: agentName}, nil
	})
	e := NewEngine("test", &namedStubModelModeAgent{name: agentName}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	wsDir := filepath.Join(baseDir, "demo-repo")
	if err := os.MkdirAll(filepath.Join(wsDir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "svc", "handler.go"), []byte("package svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.workspaceBindings.Bind(sharedWorkspaceBindingsKey, "ch1", "demo", normalizeWorkspacePath(wsDir))

	msg := &Message{
		SessionKey: "test:ch1:admin",
		Content:    "/show svc/handler.go",
		ReplyCtx:   "ctx",
		UserID:     "admin",
		Platform:   "test",
	}
	e.cmdShow(p, msg, []string{"svc/handler.go"})

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		sent := p.getSent()
		if len(sent) > 0 {
			if !strings.Contains(sent[0], "📄 svc/handler.go") {
				t.Fatalf("output = %q, want relative title", sent[0])
			}
			if !strings.Contains(sent[0], "package svc") {
				t.Fatalf("output = %q, want file content", sent[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for /show response")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleCommand_ShowRequiresAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")

	msg := &Message{
		SessionKey: "test:ch:user1",
		Content:    "/show foo.txt",
		ReplyCtx:   "ctx",
		UserID:     "user1",
		Platform:   "test",
	}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
		t.Fatalf("sent = %v, want admin required message", sent)
	}
}

func TestCmdShow_OutputRemainsRawWhenReferencesEnabled(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	agent := &stubWorkDirAgent{workDir: t.TempDir()}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")
	e.references = normalizeReferenceRenderCfg(ReferenceRenderCfg{
		NormalizeAgents: []string{"all"},
		RenderPlatforms: []string{"all"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	})

	file := filepath.Join(agent.workDir, "svc", "handler.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	rawLine := "/root/code/demo-repo/ui/recovery_contact_form.tsx:11"
	if err := os.WriteFile(file, []byte(rawLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := &Message{
		SessionKey: "test:ch:admin",
		Content:    "/show svc/handler.go",
		ReplyCtx:   "ctx",
		UserID:     "admin",
		Platform:   "feishu",
	}
	e.cmdShow(p, msg, []string{"svc/handler.go"})

	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent = %v, want one response", sent)
	}
	if !strings.Contains(sent[0], rawLine) {
		t.Fatalf("output = %q, want raw code content preserved", sent[0])
	}
}

// --- 4. /workspace subcommands ---

func TestWorkspace_NotEnabled_RepliesDisabled(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace list", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected a reply")
	}
}

func TestWorkspace_Bind_Unbind_List(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	wsDir := filepath.Join(baseDir, "my-project")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	// Bind
	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace bind my-project", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundBind := false
	for _, s := range sent {
		if strings.Contains(s, "my-project") || strings.Contains(s, e.i18n.T(MsgWsBindSuccess)[:5]) {
			foundBind = true
		}
	}
	if !foundBind {
		t.Fatalf("expected bind success, got %v", sent)
	}

	// List
	p.clearSent()
	msg = &Message{SessionKey: "test:ch1:user1", Content: "/workspace list", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent = p.getSent()
	foundList := false
	for _, s := range sent {
		if strings.Contains(s, "my-project") {
			foundList = true
		}
	}
	if !foundList {
		t.Fatalf("expected list to show binding, got %v", sent)
	}

	// Unbind
	p.clearSent()
	msg = &Message{SessionKey: "test:ch1:user1", Content: "/workspace unbind", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent = p.getSent()
	foundUnbind := false
	for _, s := range sent {
		if strings.Contains(s, e.i18n.T(MsgWsUnbindSuccess)[:5]) {
			foundUnbind = true
		}
	}
	if !foundUnbind {
		t.Fatalf("expected unbind success, got %v", sent)
	}

	// List again — should be empty
	p.clearSent()
	msg = &Message{SessionKey: "test:ch1:user1", Content: "/workspace list", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent = p.getSent()
	foundEmpty := false
	for _, s := range sent {
		if strings.Contains(s, e.i18n.T(MsgWsListEmpty)[:5]) {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Fatalf("expected empty list, got %v", sent)
	}
}

func TestWorkspace_Bind_NonexistentDir(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace bind nonexistent", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	found := false
	for _, s := range sent {
		if strings.Contains(s, "nonexistent") || strings.Contains(s, "not found") || strings.Contains(s, "Not found") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected not-found reply, got %v", sent)
	}
}

func TestWorkspace_Route_ShowsCurrentAndSupportsSpaces(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	targetDir := filepath.Join(t.TempDir(), "routed project")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace route " + targetDir, ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	normalizedTarget := normalizeWorkspacePath(targetDir)
	channelKey := workspaceChannelKey("test", "ch1")
	if got := e.workspaceBindings.Lookup("project:test", channelKey); got == nil || got.Workspace != normalizedTarget {
		t.Fatalf("expected routed binding %q, got %+v", normalizedTarget, got)
	}

	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], normalizedTarget) {
		t.Fatalf("expected route success reply to contain %q, got %v", normalizedTarget, sent)
	}

	p.clearSent()
	msg.Content = "/workspace"
	e.handleCommand(p, msg, msg.Content)
	sent = p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], normalizedTarget) {
		t.Fatalf("expected workspace info to contain routed path %q, got %v", normalizedTarget, sent)
	}
}

func TestWorkspace_Route_RejectsRelativePath(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace route relative/path", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "absolute") {
		t.Fatalf("expected absolute-path validation reply, got %v", sent)
	}
	if got := e.workspaceBindings.Lookup("project:test", workspaceChannelKey("test", "ch1")); got != nil {
		t.Fatalf("expected no binding for relative route, got %+v", got)
	}
}

func TestWorkspace_Route_RejectsNonexistentPath(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	missingPath := filepath.Join(t.TempDir(), "missing")
	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace route " + missingPath, ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], missingPath) {
		t.Fatalf("expected missing-path reply, got %v", sent)
	}
	if got := e.workspaceBindings.Lookup("project:test", workspaceChannelKey("test", "ch1")); got != nil {
		t.Fatalf("expected no binding for missing route target, got %+v", got)
	}
}

func TestWorkspace_Route_RejectsFileTarget(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	fileTarget := filepath.Join(t.TempDir(), "workspace.txt")
	if err := os.WriteFile(fileTarget, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace route " + fileTarget, ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "directory") {
		t.Fatalf("expected not-directory reply, got %v", sent)
	}
	if got := e.workspaceBindings.Lookup("project:test", workspaceChannelKey("test", "ch1")); got != nil {
		t.Fatalf("expected no binding for file route target, got %+v", got)
	}
}

func TestWorkspace_NoArgs_ShowsCurrent(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	// No binding yet — should show "no binding"
	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected a reply")
	}
}

func TestWorkspace_NoArgs_ShowsSharedBinding(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	wsDir := filepath.Join(baseDir, "shared-project")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	normalizedWsDir := normalizeWorkspacePath(wsDir)
	e.workspaceBindings.Bind(sharedWorkspaceBindingsKey, "ch1", "shared-project", normalizedWsDir)

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected a reply")
	}
	if !strings.Contains(sent[0], normalizedWsDir) {
		t.Fatalf("expected workspace info to contain shared workspace %q, got %q", normalizedWsDir, sent[0])
	}
	if !strings.Contains(strings.ToLower(sent[0]), "shared") {
		t.Fatalf("expected workspace info to mention shared source, got %q", sent[0])
	}
}

func TestWorkspace_SharedBind_AllowsRegularUser(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	wsDir := filepath.Join(baseDir, "shared-project")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/workspace shared bind shared-project",
		ReplyCtx:   "ctx",
		UserID:     "user1",
	}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected shared bind reply")
	}
	normalizedWsDir := normalizeWorkspacePath(wsDir)
	if !strings.Contains(sent[0], "shared-project") {
		t.Fatalf("expected shared bind success reply to contain workspace name, got %v", sent)
	}
	if got := e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, workspaceChannelKey("test", "ch1")); got == nil || got.Workspace != normalizedWsDir {
		t.Fatalf("expected shared binding %q for regular user, got %+v", normalizedWsDir, got)
	}
}

func TestWorkspace_SharedBind_Unbind_List(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	wsDir := filepath.Join(baseDir, "shared-project")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/workspace shared bind shared-project",
		ReplyCtx:   "ctx",
		UserID:     "user1",
	}
	e.handleCommand(p, msg, msg.Content)

	normalizedWsDir := normalizeWorkspacePath(wsDir)
	channelKey := workspaceChannelKey("test", "ch1")
	if got := e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, channelKey); got == nil || got.Workspace != normalizedWsDir {
		t.Fatalf("expected shared binding %q, got %+v", normalizedWsDir, got)
	}

	p.clearSent()
	msg.Content = "/workspace shared"
	e.handleCommand(p, msg, msg.Content)
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], normalizedWsDir) || !strings.Contains(strings.ToLower(sent[0]), "shared") {
		t.Fatalf("expected shared workspace info, got %v", sent)
	}

	p.clearSent()
	msg.Content = "/workspace shared list"
	e.handleCommand(p, msg, msg.Content)
	sent = p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], "shared-project") {
		t.Fatalf("expected shared list output, got %v", sent)
	}

	p.clearSent()
	msg.Content = "/workspace shared unbind"
	e.handleCommand(p, msg, msg.Content)
	sent = p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "shared workspace") {
		t.Fatalf("expected shared unbind success, got %v", sent)
	}
	if got := e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, channelKey); got != nil {
		t.Fatalf("expected shared binding removed, got %+v", got)
	}
}

func TestWorkspace_SharedRoute_Unbind_List(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	targetDir := filepath.Join(t.TempDir(), "shared routed workspace")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/workspace shared route " + targetDir,
		ReplyCtx:   "ctx",
		UserID:     "user1",
	}
	e.handleCommand(p, msg, msg.Content)

	normalizedTarget := normalizeWorkspacePath(targetDir)
	channelKey := workspaceChannelKey("test", "ch1")
	if got := e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, channelKey); got == nil || got.Workspace != normalizedTarget {
		t.Fatalf("expected shared route binding %q, got %+v", normalizedTarget, got)
	}

	p.clearSent()
	msg.Content = "/workspace shared"
	e.handleCommand(p, msg, msg.Content)
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], normalizedTarget) || !strings.Contains(strings.ToLower(sent[0]), "shared") {
		t.Fatalf("expected shared route info, got %v", sent)
	}

	p.clearSent()
	msg.Content = "/workspace shared list"
	e.handleCommand(p, msg, msg.Content)
	sent = p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], normalizedTarget) {
		t.Fatalf("expected shared route list output, got %v", sent)
	}

	p.clearSent()
	msg.Content = "/workspace shared unbind"
	e.handleCommand(p, msg, msg.Content)
	sent = p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "shared workspace") {
		t.Fatalf("expected shared unbind success, got %v", sent)
	}
	if got := e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, channelKey); got != nil {
		t.Fatalf("expected shared route binding removed, got %+v", got)
	}
}

func TestWorkspace_SharedInit_BindsExistingDir(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	wsDir := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/workspace shared init https://github.com/example/repo.git",
		ReplyCtx:   "ctx",
		UserID:     "user1",
	}
	e.handleCommand(p, msg, msg.Content)

	normalizedWsDir := normalizeWorkspacePath(wsDir)
	if got := e.workspaceBindings.Lookup(sharedWorkspaceBindingsKey, workspaceChannelKey("test", "ch1")); got == nil || got.Workspace != normalizedWsDir {
		t.Fatalf("expected shared init binding %q, got %+v", normalizedWsDir, got)
	}
}

func TestWorkspace_Init_LocalDirAbsolute(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	wsDir := filepath.Join(baseDir, "my-project")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/workspace init " + wsDir,
		ReplyCtx:   "ctx",
		UserID:     "user1",
	}
	e.handleCommand(p, msg, msg.Content)

	normalizedWsDir := normalizeWorkspacePath(wsDir)
	projectKey := "project:test"
	if got := e.workspaceBindings.Lookup(projectKey, workspaceChannelKey("test", "ch1")); got == nil || got.Workspace != normalizedWsDir {
		t.Fatalf("expected init binding %q, got %+v", normalizedWsDir, got)
	}
}

func TestWorkspace_Init_LocalDirRelative(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	wsDir := filepath.Join(baseDir, "my-project")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	// Use relative name — should resolve under baseDir.
	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/workspace init my-project",
		ReplyCtx:   "ctx",
		UserID:     "user1",
	}
	e.handleCommand(p, msg, msg.Content)

	normalizedWsDir := normalizeWorkspacePath(wsDir)
	projectKey := "project:test"
	if got := e.workspaceBindings.Lookup(projectKey, workspaceChannelKey("test", "ch1")); got == nil || got.Workspace != normalizedWsDir {
		t.Fatalf("expected init binding %q, got %+v", normalizedWsDir, got)
	}
}

func TestWorkspace_Init_LocalDirNotFound(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	msg := &Message{
		SessionKey: "test:ch1:user1",
		Content:    "/workspace init nonexistent-dir",
		ReplyCtx:   "ctx",
		UserID:     "user1",
	}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], "nonexistent-dir") {
		t.Fatalf("expected error mentioning missing dir, got %v", sent)
	}

	projectKey := "project:test"
	if got := e.workspaceBindings.Lookup(projectKey, workspaceChannelKey("test", "ch1")); got != nil {
		t.Fatalf("expected no binding for nonexistent dir, got %+v", got)
	}
}

func TestWorkspace_Unbind_SharedBindingShowsHint(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	wsDir := filepath.Join(baseDir, "shared-project")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.workspaceBindings.Bind(sharedWorkspaceBindingsKey, "ch1", "shared-project", normalizeWorkspacePath(wsDir))

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace unbind", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], "/workspace shared unbind") {
		t.Fatalf("expected hint to use shared unbind, got %v", sent)
	}
}

func TestWorkspace_NoArgs_IgnoresMissingSharedBinding(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	baseDir := t.TempDir()
	bindStore := filepath.Join(t.TempDir(), "bindings.json")
	e.SetMultiWorkspace(baseDir, bindStore)

	missingDir := filepath.Join(baseDir, "missing-shared-project")
	e.workspaceBindings.Bind(sharedWorkspaceBindingsKey, "ch1", "shared-project", missingDir)

	msg := &Message{SessionKey: "test:ch1:user1", Content: "/workspace", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected a reply")
	}
	if !strings.Contains(sent[0], e.i18n.T(MsgWsNoBinding)) {
		t.Fatalf("expected missing shared binding to be treated as no binding, got %q", sent[0])
	}
}

// --- 5. /switch ---

type switchableAgent struct {
	stubAgent
	sessions []AgentSessionInfo
}

func (a *switchableAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return a.sessions, nil
}

func TestCmdSwitch_NoArgs_ShowsUsage(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch:user1", Content: "/switch", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundUsage := false
	for _, s := range sent {
		if strings.Contains(s, "Usage") || strings.Contains(s, "/switch") {
			foundUsage = true
		}
	}
	if !foundUsage {
		t.Fatalf("expected usage reply, got %v", sent)
	}
}

func TestCmdSwitch_ByIndex_SetsSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &switchableAgent{
		sessions: []AgentSessionInfo{
			{ID: "sess-aaa", Summary: "First session", MessageCount: 5},
			{ID: "sess-bbb", Summary: "Second session", MessageCount: 3},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:ch:user1"

	// Pre-create an interactive state to verify cleanup.
	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{agentSession: newControllableSession("old")}
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: key, Content: "/switch 2", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundSwitch := false
	for _, s := range sent {
		if strings.Contains(s, "Second session") || strings.Contains(s, "sess-bbb") {
			foundSwitch = true
		}
	}
	if !foundSwitch {
		t.Fatalf("expected switch success reply referencing session 2, got %v", sent)
	}

	// Verify old interactive state was cleaned up.
	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if exists {
		t.Error("expected old interactive state to be cleaned up after /switch")
	}

	// Verify session was updated.
	session := e.sessions.GetOrCreateActive(key)
	if id := session.GetAgentSessionID(); id != "sess-bbb" {
		t.Errorf("expected session ID sess-bbb, got %q", id)
	}
}

func TestCmdSwitch_ByIDPrefix(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &switchableAgent{
		sessions: []AgentSessionInfo{
			{ID: "abc-123-def", Summary: "Target session"},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch:user1", Content: "/switch abc-123", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundSwitch := false
	for _, s := range sent {
		if strings.Contains(s, "Target session") || strings.Contains(s, "abc-123") {
			foundSwitch = true
		}
	}
	if !foundSwitch {
		t.Fatalf("expected switch by prefix to succeed, got %v", sent)
	}
}

func TestCmdSwitch_NoMatch(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &switchableAgent{
		sessions: []AgentSessionInfo{
			{ID: "sess-111", Summary: "Only session"},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch:user1", Content: "/switch nonexistent", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundNoMatch := false
	for _, s := range sent {
		if strings.Contains(s, "nonexistent") {
			foundNoMatch = true
		}
	}
	if !foundNoMatch {
		t.Fatalf("expected no-match reply, got %v", sent)
	}
}

func TestCmdSwitch_ByName(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &switchableAgent{
		sessions: []AgentSessionInfo{
			{ID: "sess-named-1", Summary: "Unnamed"},
			{ID: "sess-named-2", Summary: "My Feature"},
		},
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:ch:user1"
	// Set a custom name for the second session.
	e.sessions.SetSessionName("sess-named-2", "feature-branch")

	msg := &Message{SessionKey: key, Content: "/switch feature-branch", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundSwitch := false
	for _, s := range sent {
		if strings.Contains(s, "My Feature") || strings.Contains(s, "feature-branch") || strings.Contains(s, "sess-named-2") {
			foundSwitch = true
		}
	}
	if !foundSwitch {
		t.Fatalf("expected switch by name to succeed, got %v", sent)
	}
}

// --- 6. /memory ---

type stubMemoryAgentFull struct {
	stubAgent
	projectFile string
	globalFile  string
}

func (a *stubMemoryAgentFull) ProjectMemoryFile() string { return a.projectFile }
func (a *stubMemoryAgentFull) GlobalMemoryFile() string  { return a.globalFile }

func TestCmdMemory_NotSupported(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch:user1", Content: "/memory", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	found := false
	for _, s := range sent {
		if strings.Contains(s, e.i18n.T(MsgMemoryNotSupported)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected MsgMemoryNotSupported, got %v", sent)
	}
}

func TestCmdMemory_ShowEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	projectFile := filepath.Join(tmpDir, "MEMORY.md")

	p := &stubPlatformEngine{n: "test"}
	agent := &stubMemoryAgentFull{projectFile: projectFile, globalFile: ""}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch:user1", Content: "/memory", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	found := false
	for _, s := range sent {
		if strings.Contains(s, projectFile) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected empty memory reply with file path, got %v", sent)
	}
}

func TestCmdMemory_Add_And_Show(t *testing.T) {
	tmpDir := t.TempDir()
	projectFile := filepath.Join(tmpDir, "MEMORY.md")

	p := &stubPlatformEngine{n: "test"}
	agent := &stubMemoryAgentFull{projectFile: projectFile, globalFile: ""}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Add memory entry.
	msg := &Message{SessionKey: "test:ch:user1", Content: "/memory add always use gofmt", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundAdded := false
	for _, s := range sent {
		if strings.Contains(s, projectFile) {
			foundAdded = true
		}
	}
	if !foundAdded {
		t.Fatalf("expected memory added confirmation, got %v", sent)
	}

	// Verify file content.
	data, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatalf("failed to read memory file: %v", err)
	}
	if !strings.Contains(string(data), "always use gofmt") {
		t.Fatalf("memory file should contain entry, got %q", string(data))
	}

	// Show memory.
	p.clearSent()
	msg = &Message{SessionKey: "test:ch:user1", Content: "/memory show", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent = p.getSent()
	foundShow := false
	for _, s := range sent {
		if strings.Contains(s, "always use gofmt") {
			foundShow = true
		}
	}
	if !foundShow {
		t.Fatalf("expected memory show to contain the entry, got %v", sent)
	}
}

func TestCmdMemory_Add_EmptyText_ShowsUsage(t *testing.T) {
	tmpDir := t.TempDir()
	p := &stubPlatformEngine{n: "test"}
	agent := &stubMemoryAgentFull{projectFile: filepath.Join(tmpDir, "M.md")}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch:user1", Content: "/memory add", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	found := false
	for _, s := range sent {
		if strings.Contains(s, e.i18n.T(MsgMemoryAddUsage)[:10]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected add usage reply, got %v", sent)
	}
}

func TestCmdMemory_Global_Add_And_Show(t *testing.T) {
	tmpDir := t.TempDir()
	globalFile := filepath.Join(tmpDir, "GLOBAL.md")

	p := &stubPlatformEngine{n: "test"}
	agent := &stubMemoryAgentFull{projectFile: "", globalFile: globalFile}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Add global memory.
	msg := &Message{SessionKey: "test:ch:user1", Content: "/memory global add prefer structured logging", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	foundAdded := false
	for _, s := range sent {
		if strings.Contains(s, globalFile) {
			foundAdded = true
		}
	}
	if !foundAdded {
		t.Fatalf("expected global memory added, got %v", sent)
	}

	// Show global memory.
	p.clearSent()
	msg = &Message{SessionKey: "test:ch:user1", Content: "/memory global", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent = p.getSent()
	foundShow := false
	for _, s := range sent {
		if strings.Contains(s, "prefer structured logging") {
			foundShow = true
		}
	}
	if !foundShow {
		t.Fatalf("expected global show to contain entry, got %v", sent)
	}
}

func TestCmdMemory_Help(t *testing.T) {
	tmpDir := t.TempDir()
	p := &stubPlatformEngine{n: "test"}
	agent := &stubMemoryAgentFull{projectFile: filepath.Join(tmpDir, "M.md")}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	msg := &Message{SessionKey: "test:ch:user1", Content: "/memory help", ReplyCtx: "ctx"}
	e.handleCommand(p, msg, msg.Content)

	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected help reply")
	}
}

// ── /whoami tests ───────────────────────────────────────────

func TestCmdWhoami_ShowsUserID(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "telegram"}

	msg := &Message{
		SessionKey: "telegram:chat123:user456",
		Platform:   "telegram",
		UserID:     "user456",
		UserName:   "Alice",
		ReplyCtx:   "ctx",
		Content:    "/whoami",
	}
	e.handleCommand(p, msg, msg.Content)

	if len(p.sent) == 0 {
		t.Fatal("expected /whoami to produce a reply")
	}
	reply := p.sent[0]
	if !strings.Contains(reply, "user456") {
		t.Errorf("expected reply to contain user ID 'user456', got: %s", reply)
	}
	if !strings.Contains(reply, "Alice") {
		t.Errorf("expected reply to contain user name 'Alice', got: %s", reply)
	}
	if !strings.Contains(reply, "telegram") {
		t.Errorf("expected reply to contain platform 'telegram', got: %s", reply)
	}
	if !strings.Contains(reply, "chat123") {
		t.Errorf("expected reply to contain chat ID 'chat123', got: %s", reply)
	}
	if !strings.Contains(reply, "allow_from") {
		t.Errorf("expected reply to mention allow_from usage, got: %s", reply)
	}
}

func TestCmdWhoami_EmptyUserID(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{
		SessionKey: "test:ch1",
		Platform:   "test",
		UserID:     "",
		ReplyCtx:   "ctx",
		Content:    "/whoami",
	}
	e.handleCommand(p, msg, msg.Content)

	if len(p.sent) == 0 {
		t.Fatal("expected /whoami to produce a reply")
	}
	if !strings.Contains(p.sent[0], "(unknown)") {
		t.Errorf("expected '(unknown)' for empty UserID, got: %s", p.sent[0])
	}
}

func TestCmdWhoami_AliasMyID(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{
		SessionKey: "test:ch1:u1",
		Platform:   "test",
		UserID:     "u1",
		ReplyCtx:   "ctx",
		Content:    "/myid",
	}
	e.handleCommand(p, msg, msg.Content)

	if len(p.sent) == 0 {
		t.Fatal("expected /myid alias to produce a reply")
	}
	if !strings.Contains(p.sent[0], "u1") {
		t.Errorf("expected reply to contain user ID, got: %s", p.sent[0])
	}
}

func TestCmdStatus_ShowsUserID(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}

	msg := &Message{
		SessionKey: "test:ch1:myuser123",
		Platform:   "test",
		UserID:     "myuser123",
		ReplyCtx:   "ctx",
		Content:    "/status",
	}
	e.handleCommand(p, msg, msg.Content)

	if len(p.sent) == 0 {
		t.Fatal("expected /status to produce a reply")
	}
	if !strings.Contains(p.sent[0], "myuser123") {
		t.Errorf("expected status to contain user ID 'myuser123', got: %s", p.sent[0])
	}
}

func TestCmdWhoami_CardPlatform(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agent := &stubModelModeAgent{model: "gpt-4.1", mode: "default"}
	e := NewEngine("test", agent, []Platform{p}, "", LangChinese)

	msg := &Message{
		SessionKey: "feishu:chat999:ou_abc123",
		Platform:   "feishu",
		UserID:     "ou_abc123",
		UserName:   "张三",
		ReplyCtx:   "ctx",
		Content:    "/whoami",
	}
	e.handleCommand(p, msg, msg.Content)

	if len(p.repliedCards) == 0 && len(p.sentCards) == 0 {
		t.Fatal("expected /whoami to produce a card")
	}

	var card *Card
	if len(p.repliedCards) > 0 {
		card = p.repliedCards[0]
	} else {
		card = p.sentCards[0]
	}

	if card.Header == nil || card.Header.Title == "" {
		t.Fatal("expected card to have a header title")
	}

	text := card.RenderText()
	if !strings.Contains(text, "ou_abc123") {
		t.Errorf("expected card to contain user ID, got: %s", text)
	}
	if !strings.Contains(text, "张三") {
		t.Errorf("expected card to contain user name, got: %s", text)
	}
	if !strings.Contains(text, "feishu") {
		t.Errorf("expected card to contain platform, got: %s", text)
	}
	if !strings.Contains(text, "chat999") {
		t.Errorf("expected card to contain chat ID, got: %s", text)
	}
}

// ---------------------------------------------------------------------------
// Engine method coverage tests
// ---------------------------------------------------------------------------

func TestEngine_AddPlatform(t *testing.T) {
	agent := &stubAgent{}
	p1 := &stubPlatformEngine{n: "feishu"}
	p2 := &stubPlatformEngine{n: "telegram"}

	e := NewEngine("test", agent, []Platform{p1}, "", LangEnglish)

	// Initially has 1 platform
	if len(e.platforms) != 1 {
		t.Fatalf("expected 1 platform, got %d", len(e.platforms))
	}

	// Add another platform
	e.AddPlatform(p2)

	if len(e.platforms) != 2 {
		t.Fatalf("expected 2 platforms, got %d", len(e.platforms))
	}

	if e.platforms[0].Name() != "feishu" {
		t.Errorf("expected first platform to be feishu, got %s", e.platforms[0].Name())
	}
	if e.platforms[1].Name() != "telegram" {
		t.Errorf("expected second platform to be telegram, got %s", e.platforms[1].Name())
	}
}

func TestEngine_GetAgent(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}

	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// GetAgent should return the agent
	got := e.GetAgent()
	if got == nil {
		t.Fatal("expected GetAgent to return agent, got nil")
	}
	if got.Name() != "stub" {
		t.Errorf("expected agent name 'stub', got %s", got.Name())
	}
}

func TestEngine_ClearCommands(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}

	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Add commands from two sources
	e.AddCommand("cmd1", "desc1", "prompt1", "", "", "config")
	e.AddCommand("cmd2", "desc2", "prompt2", "", "", "agent")

	// Verify commands exist
	if _, ok := e.commands.Resolve("cmd1"); !ok {
		t.Fatal("expected cmd1 to exist")
	}

	// Clear commands from config source
	e.ClearCommands("config")

	// cmd1 should be gone, cmd2 should remain
	if _, ok := e.commands.Resolve("cmd1"); ok {
		t.Error("expected cmd1 to be cleared")
	}
	if _, ok := e.commands.Resolve("cmd2"); !ok {
		t.Error("expected cmd2 to remain after clearing config source")
	}
}

func TestEngine_SetAndGetAgent(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}

	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Verify GetAgent returns correct agent
	got := e.GetAgent()
	if got.Name() != "stub" {
		t.Errorf("expected agent name 'stub', got %s", got.Name())
	}
}

func TestEngine_AddCommand(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}

	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Add a command
	e.AddCommand("testcmd", "A test command", "This is a test {{args}}", "", "", "config")

	// Resolve should find it
	cmd, ok := e.commands.Resolve("testcmd")
	if !ok {
		t.Fatal("expected to resolve testcmd")
	}
	if cmd.Name != "testcmd" {
		t.Errorf("expected command name 'testcmd', got %s", cmd.Name)
	}
	if cmd.Description != "A test command" {
		t.Errorf("expected description 'A test command', got %s", cmd.Description)
	}
	if cmd.Prompt != "This is a test {{args}}" {
		t.Errorf("expected prompt 'This is a test {{args}}', got %s", cmd.Prompt)
	}
}

func TestEngine_AddAlias(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}

	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Add an alias
	e.AddAlias("shortcut", "very-long-command")

	// Check alias was stored (via internal map)
	// We can verify this through command resolution if shortcut is used as a command
	e.AddCommand("very-long-command", "Long command", "prompt", "", "", "config")

	// The alias mechanism works through the alias map
	if len(e.aliases) != 1 {
		t.Fatalf("expected 1 alias, got %d", len(e.aliases))
	}
}

func TestEstimateTokens(t *testing.T) {
	// Test with empty entries
	if got := estimateTokens(nil); got != 0 {
		t.Errorf("estimateTokens(nil) = %d, want 0", got)
	}

	if got := estimateTokens([]HistoryEntry{}); got != 0 {
		t.Errorf("estimateTokens([]) = %d, want 0", got)
	}

	// Test with entries
	entries := []HistoryEntry{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}
	got := estimateTokens(entries)
	if got <= 0 {
		t.Errorf("estimateTokens([Hello, Hi there!]) = %d, want > 0", got)
	}

	// Test with Chinese characters (should count as 1 token per character)
	entriesChinese := []HistoryEntry{
		{Role: "user", Content: "你好世界"}, // 4 characters
	}
	gotChinese := estimateTokens(entriesChinese)
	// 4 characters / 4 = 1 token, but minimum should account for the formula
	if gotChinese < 1 {
		t.Errorf("estimateTokens([你好世界]) = %d, want >= 1", gotChinese)
	}
}

func TestEstimateTokensWithPendingAssistant(t *testing.T) {
	// Test with pending assistant message
	entries := []HistoryEntry{
		{Role: "user", Content: "Hello"},
	}
	got := estimateTokensWithPendingAssistant(entries, "Thinking...")
	if got <= 0 {
		t.Errorf("estimateTokensWithPendingAssistant([Hello], Thinking...) = %d, want > 0", got)
	}

	// Pending message should add to the count
	gotWithoutPending := estimateTokensWithPendingAssistant(entries, "")
	gotWithPending := estimateTokensWithPendingAssistant(entries, "Extra content here")
	if gotWithPending <= gotWithoutPending {
		t.Errorf("expected pending message to increase token count")
	}
}

// ---------------------------------------------------------------------------
// Engine setter method coverage tests
// ---------------------------------------------------------------------------

func TestEngine_SetterMethods(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	// Test SetSpeechConfig
	e.SetSpeechConfig(SpeechCfg{Enabled: true})

	// Test SetTTSConfig
	e.SetTTSConfig(&TTSCfg{Voice: "voice-1"})

	// Test SetTTSSaveFunc (just verify it doesn't panic)
	e.SetTTSSaveFunc(func(text string) error {
		return nil
	})

	// Test SetLanguageSaveFunc
	e.SetLanguageSaveFunc(func(lang Language) error {
		return nil
	})

	// Test SetProviderSaveFunc
	e.SetProviderSaveFunc(func(providerName string) error {
		return nil
	})

	// Test SetProviderAddSaveFunc
	e.SetProviderAddSaveFunc(func(cfg ProviderConfig) error {
		return nil
	})

	// Test SetProviderRemoveSaveFunc
	e.SetProviderRemoveSaveFunc(func(name string) error {
		return nil
	})

	// Test SetCommandSaveAddFunc
	e.SetCommandSaveAddFunc(func(name, desc, prompt, exec, workDir string) error {
		return nil
	})

	// Test SetCommandSaveDelFunc
	e.SetCommandSaveDelFunc(func(name string) error {
		return nil
	})

	// Test SetDisplaySaveFunc
	e.SetDisplaySaveFunc(func(thinkingMessages *bool, thinkMax, toolMax *int, toolMessages *bool) error {
		return nil
	})

	// Test SetConfigReloadFunc
	e.SetConfigReloadFunc(func() (*ConfigReloadResult, error) {
		return nil, nil
	})

	// Test SetAliasSaveAddFunc
	e.SetAliasSaveAddFunc(func(alias, cmd string) error {
		return nil
	})

	// Test SetAliasSaveDelFunc
	e.SetAliasSaveDelFunc(func(alias string) error {
		return nil
	})

	// Test SetStreamPreviewCfg
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})

	// Verify setters didn't break core functionality
	if e.GetAgent() == nil {
		t.Error("GetAgent should still work after setters")
	}
}

func TestEngine_SetUserRoles(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	mgr := NewUserRoleManager()
	mgr.Configure("member", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
		{Name: "member", UserIDs: []string{"*"}, DisabledCommands: []string{}},
	})

	e.SetUserRoles(mgr)

	// Verify the manager was stored
	e.userRolesMu.RLock()
	stored := e.userRoles
	e.userRolesMu.RUnlock()
	if stored == nil {
		t.Error("userRoles manager should be set")
	}
	if stored != mgr {
		t.Error("stored manager should be the same as configured manager")
	}
}

func TestEngine_SetStreamPreviewCfg(t *testing.T) {
	agent := &stubAgent{}
	p := &stubPlatformEngine{n: "feishu"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	cfg := StreamPreviewCfg{Enabled: true, IntervalMs: 1000, MinDeltaChars: 10}
	e.SetStreamPreviewCfg(cfg)

	if e.streamPreview.Enabled != true {
		t.Error("streamPreview.Enabled should be true")
	}
	if e.streamPreview.IntervalMs != 1000 {
		t.Error("streamPreview.IntervalMs mismatch")
	}
}

func TestEngine_AddPlatform_Multiple(t *testing.T) {
	agent := &stubAgent{}
	p1 := &stubPlatformEngine{n: "feishu"}
	e := NewEngine("test", agent, []Platform{p1}, "", LangEnglish)

	p2 := &stubPlatformEngine{n: "telegram"}
	p3 := &stubPlatformEngine{n: "discord"}

	e.AddPlatform(p2)
	e.AddPlatform(p3)

	if len(e.platforms) != 3 {
		t.Fatalf("expected 3 platforms, got %d", len(e.platforms))
	}
}

func TestExecuteCronJob_ResolvesCronReplyTarget(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatalf("NewCronStore() error = %v", err)
	}
	scheduler := NewCronScheduler(store)

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "discord"},
	}
	agentSession := newResultAgentSession("cron complete")
	agent := &resultAgent{session: agentSession}

	e := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	defer e.cancel()
	e.cronScheduler = scheduler

	job := &CronJob{
		ID:          "job-1",
		SessionKey:  "discord:channel-1:user-1",
		Prompt:      "summarize activity",
		Description: "Daily summary",
	}
	if err := store.Add(job); err != nil {
		t.Fatalf("store.Add() error = %v", err)
	}

	if err := e.ExecuteCronJob(job); err != nil {
		t.Fatalf("ExecuteCronJob() error = %v", err)
	}
	if platform.resolvedSessionKey != "discord:channel-1:user-1" {
		t.Fatalf("ResolveCronReplyTarget sessionKey = %q, want base session key", platform.resolvedSessionKey)
	}
	if platform.resolveTitle != "Daily summary" {
		t.Fatalf("ResolveCronReplyTarget title = %q, want Daily summary", platform.resolveTitle)
	}

	sent := platform.getSent()
	if len(sent) != 2 {
		t.Fatalf("sent messages = %d, want 2", len(sent))
	}
	if sent[0] != "⏰ Daily summary" {
		t.Fatalf("sent[0] = %q, want cron start notice", sent[0])
	}
	if sent[1] != "cron complete" {
		t.Fatalf("sent[1] = %q, want final result", sent[1])
	}

	if got := len(e.sessions.ListSessions("discord:thread-fresh")); got != 0 {
		t.Fatalf("fresh session count = %d, want 0 for reuse mode", got)
	}
	if got := len(e.sessions.ListSessions("discord:channel-1:user-1")); got != 1 {
		t.Fatalf("base session count = %d, want 1", got)
	}
	if job.SessionKey != "discord:channel-1:user-1" {
		t.Fatalf("job.SessionKey = %q, want unchanged base session key", job.SessionKey)
	}
	stored := store.Get("job-1")
	if stored == nil || stored.SessionKey != "discord:channel-1:user-1" {
		t.Fatalf("stored sessionKey = %#v, want unchanged base session key", stored)
	}

	if len(agentSession.sentPrompts) != 1 || !strings.Contains(agentSession.sentPrompts[0], "summarize activity") {
		t.Fatalf("agent prompts = %#v, want prompt containing summarize activity", agentSession.sentPrompts)
	}
}

func TestExecuteCronJob_WorkspacePrefixedSessionKey(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatalf("NewCronStore() error = %v", err)
	}
	scheduler := NewCronScheduler(store)

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "slack"},
	}
	agentSession := newResultAgentSession("done")
	agent := &resultAgent{session: agentSession}

	e := NewEngine("test", agent, []Platform{platform}, "", LangEnglish)
	defer e.cancel()
	e.cronScheduler = scheduler

	// Simulate a session key that was stored with a workspace prefix
	// (as happens in multi-workspace mode).
	prefixedKey := "/home/user/workspace/myproject:slack:C123:U456"
	job := &CronJob{
		ID:          "job-ws",
		SessionKey:  prefixedKey,
		Prompt:      "daily standup",
		Description: "Standup",
	}
	if err := store.Add(job); err != nil {
		t.Fatalf("store.Add() error = %v", err)
	}

	if err := e.ExecuteCronJob(job); err != nil {
		t.Fatalf("ExecuteCronJob() with workspace-prefixed key error = %v", err)
	}

	// The platform should have received the cron start notice and agent reply.
	sent := platform.getSent()
	if len(sent) < 1 {
		t.Fatalf("expected at least one message sent to platform, got %d", len(sent))
	}

	// Stored session key must remain unchanged.
	if job.SessionKey != prefixedKey {
		t.Fatalf("job.SessionKey = %q, want unchanged %q", job.SessionKey, prefixedKey)
	}
}

func TestExtractSessionKeyParts(t *testing.T) {
	tests := []struct {
		name         string
		sessionKey   string
		wantPlatform string
		wantChannel  string
		wantKey      string
		wantUser     string
	}{
		{"full format", "feishu:channel123:user456", "feishu", "channel123", "feishu:channel123", "user456"},
		{"platform and channel only", "telegram:987654321", "telegram", "987654321", "telegram:987654321", ""},
		{"no colons", "simplekey", "simplekey", "", "", ""},
		{"single colon", "discord:channel1", "discord", "channel1", "discord:channel1", ""},
		{"empty string", "", "", "", "", ""},
		{"just platform colon user", "line::user1", "line", "", "", "user1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPlatform := extractPlatformName(tt.sessionKey)
			if gotPlatform != tt.wantPlatform {
				t.Errorf("extractPlatformName(%q) = %q, want %q", tt.sessionKey, gotPlatform, tt.wantPlatform)
			}

			gotChannel := extractChannelID(tt.sessionKey)
			if gotChannel != tt.wantChannel {
				t.Errorf("extractChannelID(%q) = %q, want %q", tt.sessionKey, gotChannel, tt.wantChannel)
			}

			gotKey := extractWorkspaceChannelKey(tt.sessionKey)
			if gotKey != tt.wantKey {
				t.Errorf("extractWorkspaceChannelKey(%q) = %q, want %q", tt.sessionKey, gotKey, tt.wantKey)
			}

			gotUser := extractUserID(tt.sessionKey)
			if gotUser != tt.wantUser {
				t.Errorf("extractUserID(%q) = %q, want %q", tt.sessionKey, gotUser, tt.wantUser)
			}
		})
	}
}

func TestSetObserveConfig(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	e.SetObserveConfig("/tmp/test-project", "slack:C123:U456")
	if !e.observeEnabled {
		t.Fatal("observe should be enabled")
	}
	if e.observeProjectDir != "/tmp/test-project" {
		t.Fatalf("unexpected project dir: %s", e.observeProjectDir)
	}
}

func TestObserveStartsOnlyWithSlack(t *testing.T) {
	stub := &stubPlatformWithObserve{stubPlatform: stubPlatform{n: "slack"}}
	e := NewEngine("test", &stubAgent{}, []Platform{stub}, "", LangEnglish)
	e.SetObserveConfig("/tmp/fake-project", "slack:C123:U456")

	target := e.findObserverTarget()
	if target == nil {
		t.Fatal("expected to find observer target for Slack")
	}
}

func TestObserveNoTargetWithoutSlack(t *testing.T) {
	stub := &stubPlatform{n: "telegram"}
	e := NewEngine("test", &stubAgent{}, []Platform{stub}, "", LangEnglish)
	e.SetObserveConfig("/tmp/fake-project", "slack:C123:U456")

	target := e.findObserverTarget()
	if target != nil {
		t.Fatal("expected no observer target without Slack")
	}
}

type stubPlatformWithObserve struct {
	stubPlatform
}

func (s *stubPlatformWithObserve) SendObservation(_ context.Context, _, _ string) error {
	return nil
}

func TestIsSilentReply(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"exact marker", "NO_REPLY", true},
		{"lowercase", "no_reply", true},
		{"mixed case", "No_Reply", true},
		{"leading/trailing spaces", "  NO_REPLY  ", true},
		{"surrounding newlines", "\nNO_REPLY\n", true},
		{"tabs around", "\tNO_REPLY\t", true},

		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"mixed with content", "Hello NO_REPLY", false},
		{"marker with suffix", "NO_REPLY_EXTRA", false},
		{"marker with prefix", "X NO_REPLY", false},
		{"missing underscore", "NO REPLY", false},
		{"partial", "NO_REPL", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSilentReply(tc.in); got != tc.want {
				t.Errorf("isSilentReply(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripTrailingSilent(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"trailing on new line", "Hello\nNO_REPLY", "Hello", true},
		{"trailing lowercase on new line", "Hello\nno_reply", "Hello", true},
		{"trailing after space", "Some reasoning here NO_REPLY", "Some reasoning here", true},
		{"multi-line then marker", "Line1\nLine2\nNO_REPLY", "Line1\nLine2", true},
		{"trailing with markdown emphasis", "Done. *NO_REPLY*", "Done. *NO_REPLY*", false},
		{"trailing preceded by asterisks", "Done.**NO_REPLY", "Done.", true},
		{"trailing with crlf", "Hello\r\nNO_REPLY", "Hello", true},
		{"marker followed by trailing whitespace", "Hello NO_REPLY   ", "Hello", true},

		{"no marker", "Hello world", "Hello world", false},
		{"marker not at end", "NO_REPLY then more", "NO_REPLY then more", false},
		{"marker with suffix token", "Hello NO_REPLY_EXTRA", "Hello NO_REPLY_EXTRA", false},
		{"marker touching prior letters", "somethingNO_REPLY", "somethingNO_REPLY", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := stripTrailingSilent(tc.in)
			if ok != tc.wantOK {
				t.Errorf("stripTrailingSilent(%q) ok=%v, want %v", tc.in, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("stripTrailingSilent(%q) got=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCouldBeSilentPrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"one letter", "N", true},
		{"two letters", "NO", true},
		{"underscore partial", "NO_", true},
		{"five letters", "NO_RE", true},
		{"almost full", "NO_REPL", true},
		{"full marker", "NO_REPLY", true},
		{"lowercase partial", "no_r", true},
		{"mixed case partial", "No_Re", true},
		{"trimmed surrounding whitespace", "  NO_  ", true},

		{"non-N start", "Hello", false},
		{"one wrong letter", "X", false},
		{"longer than marker", "NO_REPLYX", false},
		{"similar but divergent", "NO-REPLY", false},
		{"partial then wrong", "NO_Q", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := couldBeSilentPrefix(tc.in); got != tc.want {
				t.Errorf("couldBeSilentPrefix(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration tests for /list visibility after /new and provider switches
// ---------------------------------------------------------------------------

// TestCmdList_AllSessionsVisibleAfterRepeatedNew verifies that /list shows ALL
// sessions after multiple /new cycles. This is the exact reproduction scenario
// reported by users: /new clears the active session's AgentSessionID, causing
// filterOwnedSessions to progressively hide older sessions.
func TestCmdList_AllSessionsVisibleAfterRepeatedNew(t *testing.T) {
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	agentSessions := make([]AgentSessionInfo, 5)
	for i := range agentSessions {
		agentSessions[i] = AgentSessionInfo{
			ID:           fmt.Sprintf("codex-thread-%d", i+1),
			Summary:      fmt.Sprintf("Session %d summary", i+1),
			MessageCount: (i + 1) * 2,
			ModifiedAt:   base.Add(time.Duration(i) * time.Hour),
		}
	}

	agent := &stubListAgent{sessions: agentSessions}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	for i, as := range agentSessions {
		if i > 0 {
			old := e.sessions.GetOrCreateActive(userKey)
			old.SetAgentSessionID("", "")
			old.ClearHistory()
			e.sessions.Save()
			e.sessions.NewSession(userKey, fmt.Sprintf("session-%d", i+1))
		}
		s := e.sessions.GetOrCreateActive(userKey)
		s.SetAgentSessionID(as.ID, "codex")
		e.sessions.Save()
	}

	p.sent = nil
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}
	e.cmdList(p, msg, nil)

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	for _, as := range agentSessions {
		if !strings.Contains(p.sent[0], as.Summary) {
			t.Errorf("/list output missing session %q:\n%s", as.ID, p.sent[0])
		}
	}
}

// TestCmdList_AllSessionsVisibleAfterResetAllSessions simulates a management
// API provider switch (resetAllSessions) followed by creating a new session.
// All previously tracked sessions must remain visible in /list.
func TestCmdList_AllSessionsVisibleAfterResetAllSessions(t *testing.T) {
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	agentSessions := make([]AgentSessionInfo, 4)
	for i := range agentSessions {
		agentSessions[i] = AgentSessionInfo{
			ID:           fmt.Sprintf("thread-%d", i+1),
			Summary:      fmt.Sprintf("Chat %d", i+1),
			MessageCount: 5,
			ModifiedAt:   base.Add(time.Duration(i) * time.Hour),
		}
	}

	agent := &stubListAgent{sessions: agentSessions}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	for _, as := range agentSessions[:3] {
		s := e.sessions.NewSession(userKey, "")
		s.SetAgentSessionID(as.ID, "codex")
	}
	e.sessions.Save()

	e.resetAllSessions()

	newS := e.sessions.NewSession(userKey, "fresh")
	newS.SetAgentSessionID(agentSessions[3].ID, "codex")
	e.sessions.Save()

	p.sent = nil
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}
	e.cmdList(p, msg, nil)

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	for _, as := range agentSessions {
		if !strings.Contains(p.sent[0], as.Summary) {
			t.Errorf("/list output missing session %q after resetAllSessions:\n%s", as.ID, p.sent[0])
		}
	}
}

// TestCmdList_SessionVisibleDuringAgentProcessing simulates the window where
// a new session has been created (/new) and a message sent, but the agent
// has not yet responded with a session ID. During this window, the active
// session has no AgentSessionID. Previously this caused filterOwnedSessions
// to either return all sessions (empty known set) or hide sessions (if other
// sessions also had cleared IDs). The fix ensures deterministic behavior.
func TestCmdList_SessionVisibleDuringAgentProcessing(t *testing.T) {
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	agentSessions := []AgentSessionInfo{
		{ID: "old-thread-1", Summary: "Old session 1", MessageCount: 10, ModifiedAt: base},
		{ID: "old-thread-2", Summary: "Old session 2", MessageCount: 8, ModifiedAt: base.Add(time.Hour)},
		{ID: "new-thread-3", Summary: "Processing...", MessageCount: 1, ModifiedAt: base.Add(2 * time.Hour)},
	}

	agent := &stubListAgent{sessions: agentSessions}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	s1 := e.sessions.GetOrCreateActive(userKey)
	s1.SetAgentSessionID("old-thread-1", "codex")
	e.sessions.Save()

	s1.SetAgentSessionID("", "")
	s2 := e.sessions.NewSession(userKey, "session-2")
	s2.SetAgentSessionID("old-thread-2", "codex")
	e.sessions.Save()

	s2.SetAgentSessionID("", "")
	e.sessions.NewSession(userKey, "processing")
	e.sessions.Save()

	p.sent = nil
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}
	e.cmdList(p, msg, nil)

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	reply := p.sent[0]
	if !strings.Contains(reply, "Old session 1") {
		t.Errorf("/list missing 'Old session 1' during processing:\n%s", reply)
	}
	if !strings.Contains(reply, "Old session 2") {
		t.Errorf("/list missing 'Old session 2' during processing:\n%s", reply)
	}
}

// TestRenderListCard_AllSessionsVisibleAfterRepeatedNew is the card-based
// variant of the /new regression test.
func TestRenderListCard_AllSessionsVisibleAfterRepeatedNew(t *testing.T) {
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	agentSessions := make([]AgentSessionInfo, 6)
	for i := range agentSessions {
		agentSessions[i] = AgentSessionInfo{
			ID:           fmt.Sprintf("thread-%d", i+1),
			Summary:      fmt.Sprintf("Session %d", i+1),
			MessageCount: 3,
			ModifiedAt:   base.Add(time.Duration(i) * time.Minute),
		}
	}

	agent := &stubListAgent{sessions: agentSessions}
	e := NewEngine("test", agent, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	userKey := "test:user1"

	for i, as := range agentSessions {
		if i > 0 {
			old := e.sessions.GetOrCreateActive(userKey)
			old.SetAgentSessionID("", "")
			old.ClearHistory()
			e.sessions.NewSession(userKey, fmt.Sprintf("s%d", i+1))
		}
		s := e.sessions.GetOrCreateActive(userKey)
		s.SetAgentSessionID(as.ID, "codex")
	}
	e.sessions.Save()

	card, err := e.renderListCard(userKey, 1)
	if err != nil {
		t.Fatalf("renderListCard error: %v", err)
	}

	switchActions := countCardActionValues(card, "act:/switch ")
	if switchActions != len(agentSessions) {
		t.Fatalf("card switch actions = %d, want %d (some sessions hidden by filter)",
			switchActions, len(agentSessions))
	}
}

// TestCmdList_ProviderSwitchThenNewDoesNotHideSessions simulates the full
// real-world scenario: user has sessions → switches provider → creates new
// sessions → all sessions (old and new) must remain visible.
func TestCmdList_ProviderSwitchThenNewDoesNotHideSessions(t *testing.T) {
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	allAgentSessions := []AgentSessionInfo{
		{ID: "old-1", Summary: "Before switch 1", MessageCount: 5, ModifiedAt: base},
		{ID: "old-2", Summary: "Before switch 2", MessageCount: 3, ModifiedAt: base.Add(time.Hour)},
		{ID: "new-1", Summary: "After switch 1", MessageCount: 2, ModifiedAt: base.Add(2 * time.Hour)},
		{ID: "new-2", Summary: "After switch 2", MessageCount: 1, ModifiedAt: base.Add(3 * time.Hour)},
	}
	agent := &stubListAgent{sessions: allAgentSessions}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	for _, as := range allAgentSessions[:2] {
		s := e.sessions.NewSession(userKey, "")
		s.SetAgentSessionID(as.ID, "codex")
	}
	e.sessions.Save()

	e.resetAllSessions()

	for i, as := range allAgentSessions[2:] {
		if i > 0 {
			old := e.sessions.GetOrCreateActive(userKey)
			old.SetAgentSessionID("", "")
		}
		s := e.sessions.NewSession(userKey, "")
		s.SetAgentSessionID(as.ID, "codex")
	}
	e.sessions.Save()

	p.sent = nil
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}
	e.cmdList(p, msg, nil)

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	for _, as := range allAgentSessions {
		if !strings.Contains(p.sent[0], as.Summary) {
			t.Errorf("/list missing %q after provider switch + new:\n%s", as.Summary, p.sent[0])
		}
	}
}

// TestCmdList_RealWorldLegacyDataFullFlow is a precise reproduction of the
// user-reported bug using data shaped exactly like the real qa-release project:
//   - 15 internal sessions, 14 with lost AgentSessionIDs (old code damage)
//   - 1 active session (s15) with a valid AgentSessionID
//   - 37 codex sessions on disk
//
// Steps (matching user's exact reproduction):
//  1. /list → must show all 37 sessions (legacy data, no filtering)
//  2. /new "我的新会话" → create named session
//  3. send message (agent hasn't replied yet) → /list → must STILL show all sessions
//  4. agent replies with SessionID → /list → must show all sessions + new one
//  5. session name "我的新会话" must appear in the list
func TestCmdList_RealWorldLegacyDataFullFlow(t *testing.T) {
	dir := t.TempDir()
	sessPath := filepath.Join(dir, "sessions.json")

	// Write legacy session data (no past_id_tracking, simulates pre-fix data)
	legacyJSON := `{
		"sessions": {
			"s1":  {"id":"s1", "name":"default",    "agent_session_id":"", "history":null, "created_at":"2026-03-26T22:25:56Z", "updated_at":"2026-03-26T22:25:56Z"},
			"s2":  {"id":"s2", "name":"default",    "agent_session_id":"", "history":null, "created_at":"2026-04-18T09:02:57Z", "updated_at":"2026-04-18T09:02:57Z"},
			"s3":  {"id":"s3", "name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T09:03:07Z", "updated_at":"2026-04-18T09:03:07Z"},
			"s4":  {"id":"s4", "name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T09:07:15Z", "updated_at":"2026-04-18T09:07:15Z"},
			"s5":  {"id":"s5", "name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T11:14:14Z", "updated_at":"2026-04-18T11:14:14Z"},
			"s6":  {"id":"s6", "name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T11:39:15Z", "updated_at":"2026-04-18T11:39:15Z"},
			"s7":  {"id":"s7", "name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T11:42:27Z", "updated_at":"2026-04-18T11:42:27Z"},
			"s8":  {"id":"s8", "name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T12:01:02Z", "updated_at":"2026-04-18T12:01:22Z"},
			"s9":  {"id":"s9", "name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T12:06:31Z", "updated_at":"2026-04-18T12:08:37Z"},
			"s10": {"id":"s10","name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T12:18:55Z", "updated_at":"2026-04-18T12:18:55Z"},
			"s11": {"id":"s11","name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T14:07:03Z", "updated_at":"2026-04-18T14:07:47Z"},
			"s12": {"id":"s12","name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T14:07:59Z", "updated_at":"2026-04-18T14:18:49Z"},
			"s13": {"id":"s13","name":"",           "agent_session_id":"", "history":null, "created_at":"2026-04-18T15:50:39Z", "updated_at":"2026-04-20T21:44:37Z"},
			"s14": {"id":"s14","name":"今天",       "agent_session_id":"", "history":null, "created_at":"2026-04-20T21:44:58Z", "updated_at":"2026-04-20T21:44:58Z"},
			"s15": {"id":"s15","name":"新的会话",   "agent_session_id":"019dab28-1a0f-7f60-87ed-b4fda306ebef", "agent_type":"codex", "history":null, "created_at":"2026-04-20T21:50:14Z", "updated_at":"2026-04-20T21:50:14Z"}
		},
		"active_session": {"feishu:chat:user1":"s15"},
		"user_sessions":  {"feishu:chat:user1":["s2","s3","s4","s5","s6","s7","s8","s9","s10","s11","s12","s13","s14","s15"]},
		"counter": 15
	}`
	if err := os.WriteFile(sessPath, []byte(legacyJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 4, 18, 9, 0, 0, 0, time.UTC)
	agentSessions := make([]AgentSessionInfo, 37)
	for i := range agentSessions {
		agentSessions[i] = AgentSessionInfo{
			ID:           fmt.Sprintf("codex-thread-%03d", i+1),
			Summary:      fmt.Sprintf("Codex session %d", i+1),
			MessageCount: 3,
			ModifiedAt:   base.Add(time.Duration(i) * 30 * time.Minute),
		}
	}
	// s15's actual codex session is at index 36 (most recent)
	agentSessions[36].ID = "019dab28-1a0f-7f60-87ed-b4fda306ebef"
	agentSessions[36].Summary = "陈奕迅最有名是那首歌"

	agent := &stubListAgent{sessions: agentSessions}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.sessions = NewSessionManager(sessPath) // load real data
	userKey := "feishu:chat:user1"
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}

	// ── Step 1: /list on startup ───────────────────────────────
	p.sent = nil
	e.cmdList(p, msg, nil)
	if len(p.sent) != 1 {
		t.Fatalf("step1: expected 1 reply, got %d", len(p.sent))
	}
	step1Count := strings.Count(p.sent[0], "msgs")
	if step1Count != 20 {
		t.Fatalf("step1: /list should show first page (20 sessions), got %d", step1Count)
	}

	// ── Step 2: /new "我的新会话" ──────────────────────────────
	e.cmdNew(p, msg, []string{"我的新会话"})

	// ── Step 3: send message, agent not yet replied → /list ────
	// (agent process started but hasn't returned SessionID yet)
	p.sent = nil
	e.cmdList(p, msg, nil)
	if len(p.sent) != 1 {
		t.Fatalf("step3: expected 1 reply, got %d", len(p.sent))
	}
	step3Count := strings.Count(p.sent[0], "msgs")
	if step3Count < 20 {
		t.Fatalf("step3: /list BEFORE agent reply should still show all sessions (page 1 = 20), got %d\nreply:\n%s",
			step3Count, p.sent[0])
	}

	// ── Step 4: agent replies → set SessionID → /list ──────────
	newSession := e.sessions.GetOrCreateActive(userKey)
	newThreadID := "codex-thread-new-038"
	newSession.CompareAndSetAgentSessionID(newThreadID, "codex")
	// Engine maps the pending name to the new agent session ID
	pendingName := newSession.GetName()
	if pendingName != "" && pendingName != "session" && pendingName != "default" {
		e.sessions.SetSessionName(newThreadID, pendingName)
	}
	e.sessions.Save()

	// Agent now reports this new session in ListSessions
	agent.sessions = append(agent.sessions, AgentSessionInfo{
		ID:           newThreadID,
		Summary:      "我的新消息内容",
		MessageCount: 2,
		ModifiedAt:   time.Now(),
	})

	p.sent = nil
	e.cmdList(p, msg, nil)
	if len(p.sent) != 1 {
		t.Fatalf("step4: expected 1 reply, got %d", len(p.sent))
	}
	step4Count := strings.Count(p.sent[0], "msgs")
	if step4Count < 20 {
		t.Fatalf("step4: /list AFTER agent reply should show all sessions (page 1 = 20), got %d\nreply:\n%s",
			step4Count, p.sent[0])
	}

	// ── Step 5: verify session name on page 2 ─────────────────
	// The newest session is at the end of the list; check page 2.
	p.sent = nil
	e.cmdList(p, msg, []string{"2"})
	if len(p.sent) != 1 {
		t.Fatalf("step5: expected 1 reply for page 2, got %d", len(p.sent))
	}
	// The new session should show "我的新会话" (the name from /new), not the message content
	if !strings.Contains(p.sent[0], "我的新会话") {
		t.Errorf("step5: /list page 2 should display session name '我的新会话' but it's missing:\n%s", p.sent[0])
	}
}

// TestCmdList_FilterExternalSessionsEnabled verifies that when
// filter_external_sessions is enabled, only cc-connect-tracked sessions
// appear in /list.
func TestCmdList_FilterExternalSessionsEnabled(t *testing.T) {
	agentSessions := []AgentSessionInfo{
		{ID: "tracked-1", Summary: "Tracked 1", MessageCount: 5},
		{ID: "tracked-2", Summary: "Tracked 2", MessageCount: 3},
		{ID: "external-1", Summary: "External CLI session", MessageCount: 10},
	}

	agent := &stubListAgent{sessions: agentSessions}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetFilterExternalSessions(true)
	userKey := "test:user1"

	s1 := e.sessions.GetOrCreateActive(userKey)
	s1.SetAgentSessionID("tracked-1", "codex")
	e.sessions.Save()
	s1.SetAgentSessionID("", "")
	s2 := e.sessions.NewSession(userKey, "session2")
	s2.SetAgentSessionID("tracked-2", "codex")
	e.sessions.Save()

	p.sent = nil
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}
	e.cmdList(p, msg, nil)

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	reply := p.sent[0]
	if !strings.Contains(reply, "Tracked 1") {
		t.Errorf("filter enabled: should show tracked session 'Tracked 1':\n%s", reply)
	}
	if !strings.Contains(reply, "Tracked 2") {
		t.Errorf("filter enabled: should show tracked session 'Tracked 2':\n%s", reply)
	}
	if strings.Contains(reply, "External CLI session") {
		t.Errorf("filter enabled: should NOT show external session:\n%s", reply)
	}
}

// TestCmdList_DefaultShowsAllSessions verifies that with default config
// (filter_external_sessions=false), all sessions including external ones appear.
func TestCmdList_DefaultShowsAllSessions(t *testing.T) {
	agentSessions := []AgentSessionInfo{
		{ID: "tracked-1", Summary: "Tracked session", MessageCount: 5},
		{ID: "external-1", Summary: "External session", MessageCount: 10},
	}

	agent := &stubListAgent{sessions: agentSessions}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	s := e.sessions.GetOrCreateActive(userKey)
	s.SetAgentSessionID("tracked-1", "codex")
	e.sessions.Save()

	p.sent = nil
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}
	e.cmdList(p, msg, nil)

	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	reply := p.sent[0]
	if !strings.Contains(reply, "Tracked session") {
		t.Errorf("default mode: should show tracked session:\n%s", reply)
	}
	if !strings.Contains(reply, "External session") {
		t.Errorf("default mode: should show external session:\n%s", reply)
	}
}

// ---------------------------------------------------------------------------
// filter_external_sessions integration test suite
// Covers /list, /switch, /delete, renderListCard under both modes.
// ---------------------------------------------------------------------------

// setupFilterTestEngine creates a test Engine with 3 agent sessions, 2 tracked
// by cc-connect and 1 external. Returns (engine, platform, userKey, agentSessions).
func setupFilterTestEngine(t *testing.T, filterEnabled bool) (*Engine, *stubPlatformEngine, string, []AgentSessionInfo) {
	t.Helper()
	agentSessions := []AgentSessionInfo{
		{ID: "tracked-1", Summary: "Tracked session 1", MessageCount: 5, ModifiedAt: time.Now().Add(-2 * time.Hour)},
		{ID: "tracked-2", Summary: "Tracked session 2", MessageCount: 3, ModifiedAt: time.Now().Add(-time.Hour)},
		{ID: "external-1", Summary: "External CLI session", MessageCount: 10, ModifiedAt: time.Now()},
	}
	agent := &stubDeleteAgent{
		stubListAgent: stubListAgent{sessions: agentSessions},
		errByID:       map[string]error{},
	}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetFilterExternalSessions(filterEnabled)
	userKey := "test:filter-user"

	s1 := e.sessions.GetOrCreateActive(userKey)
	s1.SetAgentSessionID("tracked-1", "codex")
	e.sessions.Save()
	s1.SetAgentSessionID("", "")
	s2 := e.sessions.NewSession(userKey, "session2")
	s2.SetAgentSessionID("tracked-2", "codex")
	e.sessions.Save()

	return e, p, userKey, agentSessions
}

func TestFilterExternalSessions_SwitchByIndex(t *testing.T) {
	t.Run("disabled: index 3 reaches external session", func(t *testing.T) {
		e, p, userKey, _ := setupFilterTestEngine(t, false)
		p.sent = nil
		e.cmdSwitch(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"3"})
		if len(p.sent) != 1 {
			t.Fatalf("expected 1 reply, got %d", len(p.sent))
		}
		if !strings.Contains(p.sent[0], "External CLI session") {
			t.Errorf("default mode: /switch 3 should reach external session:\n%s", p.sent[0])
		}
	})

	t.Run("enabled: index 3 out of range", func(t *testing.T) {
		e, p, userKey, _ := setupFilterTestEngine(t, true)
		p.sent = nil
		e.cmdSwitch(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"3"})
		if len(p.sent) != 1 {
			t.Fatalf("expected 1 reply, got %d", len(p.sent))
		}
		if strings.Contains(p.sent[0], "External CLI session") {
			t.Errorf("filter enabled: /switch 3 should NOT reach external session:\n%s", p.sent[0])
		}
	})
}

func TestFilterExternalSessions_SwitchByIDPrefix(t *testing.T) {
	t.Run("disabled: can switch to external by ID prefix", func(t *testing.T) {
		e, p, userKey, _ := setupFilterTestEngine(t, false)
		p.sent = nil
		e.cmdSwitch(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"external"})
		if len(p.sent) != 1 {
			t.Fatalf("expected 1 reply, got %d", len(p.sent))
		}
		if !strings.Contains(p.sent[0], "External CLI session") {
			t.Errorf("default mode: /switch external should find external session:\n%s", p.sent[0])
		}
	})

	t.Run("enabled: external ID prefix not found", func(t *testing.T) {
		e, p, userKey, _ := setupFilterTestEngine(t, true)
		p.sent = nil
		e.cmdSwitch(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"external"})
		if len(p.sent) != 1 {
			t.Fatalf("expected 1 reply, got %d", len(p.sent))
		}
		if strings.Contains(p.sent[0], "External CLI session") {
			t.Errorf("filter enabled: /switch external should NOT find external session:\n%s", p.sent[0])
		}
	})
}

func TestFilterExternalSessions_DeleteByIndex(t *testing.T) {
	t.Run("disabled: /delete 3 hits external session", func(t *testing.T) {
		e, p, userKey, _ := setupFilterTestEngine(t, false)
		p.sent = nil
		e.cmdDelete(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"3"})
		if len(p.sent) == 0 {
			t.Fatal("expected reply from /delete")
		}
		reply := strings.Join(p.sent, "\n")
		if !strings.Contains(reply, "external-1") && !strings.Contains(reply, "External CLI session") {
			t.Errorf("default mode: /delete 3 should target external session:\n%s", reply)
		}
	})

	t.Run("enabled: /delete 3 out of range", func(t *testing.T) {
		e, p, userKey, _ := setupFilterTestEngine(t, true)
		p.sent = nil
		e.cmdDelete(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"3"})
		if len(p.sent) == 0 {
			t.Fatal("expected reply from /delete")
		}
		reply := strings.Join(p.sent, "\n")
		if strings.Contains(reply, "external-1") || strings.Contains(reply, "External CLI session") {
			t.Errorf("filter enabled: /delete 3 should NOT target external session:\n%s", reply)
		}
	})
}

func TestFilterExternalSessions_RenderListCard(t *testing.T) {
	t.Run("disabled: card shows all sessions", func(t *testing.T) {
		e, _, userKey, agentSessions := setupFilterTestEngine(t, false)
		card, err := e.renderListCard(userKey, 1)
		if err != nil {
			t.Fatalf("renderListCard: %v", err)
		}
		switchActions := countCardActionValues(card, "act:/switch ")
		if switchActions != len(agentSessions) {
			t.Errorf("default mode: card should show %d sessions, got %d", len(agentSessions), switchActions)
		}
	})

	t.Run("enabled: card hides external sessions", func(t *testing.T) {
		e, _, userKey, _ := setupFilterTestEngine(t, true)
		card, err := e.renderListCard(userKey, 1)
		if err != nil {
			t.Fatalf("renderListCard: %v", err)
		}
		switchActions := countCardActionValues(card, "act:/switch ")
		if switchActions != 2 {
			t.Errorf("filter enabled: card should show 2 tracked sessions, got %d", switchActions)
		}
	})
}

func TestFilterExternalSessions_DynamicToggle(t *testing.T) {
	e, p, userKey, agentSessions := setupFilterTestEngine(t, false)
	msg := &Message{SessionKey: userKey, ReplyCtx: "ctx"}

	p.sent = nil
	e.cmdList(p, msg, nil)
	count1 := strings.Count(p.sent[0], "msgs")
	if count1 != len(agentSessions) {
		t.Fatalf("before toggle: expected %d sessions, got %d", len(agentSessions), count1)
	}

	e.SetFilterExternalSessions(true)

	p.sent = nil
	e.cmdList(p, msg, nil)
	count2 := strings.Count(p.sent[0], "msgs")
	if count2 != 2 {
		t.Fatalf("after enabling filter: expected 2 sessions, got %d\nreply:\n%s", count2, p.sent[0])
	}

	e.SetFilterExternalSessions(false)

	p.sent = nil
	e.cmdList(p, msg, nil)
	count3 := strings.Count(p.sent[0], "msgs")
	if count3 != len(agentSessions) {
		t.Fatalf("after disabling filter: expected %d sessions, got %d", len(agentSessions), count3)
	}
}

// codexLikeSession simulates real codex agent behavior:
// - CurrentSessionID() returns "" until Send() is called
// - Send() sets the thread ID and pushes an EventResult with the SessionID
type codexLikeSession struct {
	threadID  string
	events    chan Event
	alive     bool
	hasSentID bool
}

func newCodexLikeSession(threadID string) *codexLikeSession {
	return &codexLikeSession{
		threadID: threadID,
		events:   make(chan Event, 8),
		alive:    true,
	}
}

func (s *codexLikeSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.hasSentID = true
	s.events <- Event{Type: EventText, Content: "Agent reply to: " + prompt}
	s.events <- Event{Type: EventResult, SessionID: s.threadID, Content: "Done", Done: true}
	return nil
}
func (s *codexLikeSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *codexLikeSession) Events() <-chan Event                                 { return s.events }
func (s *codexLikeSession) CurrentSessionID() string {
	if s.hasSentID {
		return s.threadID
	}
	return ""
}
func (s *codexLikeSession) Alive() bool  { return s.alive }
func (s *codexLikeSession) Close() error { s.alive = false; return nil }

// TestSessionName_CodexLikeFlow does an end-to-end test simulating real codex
// behavior: CurrentSessionID()="" initially, thread ID only available after Send().
// This is the exact bug: /new xxx → send message → agent replies with SessionID
// in EventResult → name "xxx" must appear in /list.
func TestSessionName_CodexLikeFlow(t *testing.T) {
	sess := newCodexLikeSession("codex-thread-new-001")
	listSessions := []AgentSessionInfo{
		{ID: "codex-thread-old", Summary: "Old session", MessageCount: 5, ModifiedAt: time.Now().Add(-time.Hour)},
	}
	agent := &controllableAgent{
		nextSession: sess,
		listFn: func() ([]AgentSessionInfo, error) {
			return listSessions, nil
		},
	}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	// Setup: create initial session with a known agent session ID
	initial := e.sessions.GetOrCreateActive(userKey)
	initial.SetAgentSessionID("codex-thread-old", "codex")
	e.sessions.Save()

	// Step 1: /new "我的新会话"
	e.cmdNew(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"我的新会话"})

	// Step 2: send a message (this triggers startOrResumeSession + processInteractiveEvents)
	e.ReceiveMessage(p, &Message{
		SessionKey: userKey,
		Content:    "请帮我做个功能",
		ReplyCtx:   "ctx2",
	})

	// Wait for the event loop to complete
	time.Sleep(200 * time.Millisecond)

	// Step 3: verify session name was mapped
	newSession := e.sessions.GetOrCreateActive(userKey)
	agentID := newSession.GetAgentSessionID()
	if agentID != "codex-thread-new-001" {
		t.Fatalf("AgentSessionID = %q, want %q", agentID, "codex-thread-new-001")
	}

	gotName := e.sessions.GetSessionName("codex-thread-new-001")
	if gotName != "我的新会话" {
		t.Fatalf("GetSessionName(%q) = %q, want %q", "codex-thread-new-001", gotName, "我的新会话")
	}

	// Step 4: verify /list displays the name
	listSessions = append(listSessions, AgentSessionInfo{
		ID:           "codex-thread-new-001",
		Summary:      "请帮我做个功能",
		MessageCount: 2,
		ModifiedAt:   time.Now(),
	})
	p.sent = nil
	e.cmdList(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, nil)
	if len(p.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(p.sent))
	}
	if !strings.Contains(p.sent[0], "我的新会话") {
		t.Errorf("/list should show session name '我的新会话':\n%s", p.sent[0])
	}
}

// claudeCodeLikeSession simulates claudecode/gemini/cursor behavior:
// - CurrentSessionID() returns "" at creation
// - Send() emits an early EventText with SessionID (system/init event)
// - Then normal EventText without SessionID
// - Finally EventResult with SessionID
type claudeCodeLikeSession struct {
	threadID  string
	events    chan Event
	alive     bool
	hasSentID bool
}

func newClaudeCodeLikeSession(threadID string) *claudeCodeLikeSession {
	return &claudeCodeLikeSession{
		threadID: threadID,
		events:   make(chan Event, 8),
		alive:    true,
	}
}

func (s *claudeCodeLikeSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.hasSentID = true
	// claudecode sends an early system event with SessionID (empty content)
	s.events <- Event{Type: EventText, Content: "", SessionID: s.threadID}
	// Normal streaming text (no SessionID)
	s.events <- Event{Type: EventText, Content: "Reply to: " + prompt}
	// Final result
	s.events <- Event{Type: EventResult, SessionID: s.threadID, Content: "Done", Done: true}
	return nil
}
func (s *claudeCodeLikeSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *claudeCodeLikeSession) Events() <-chan Event                                 { return s.events }
func (s *claudeCodeLikeSession) CurrentSessionID() string {
	if s.hasSentID {
		return s.threadID
	}
	return ""
}
func (s *claudeCodeLikeSession) Alive() bool  { return s.alive }
func (s *claudeCodeLikeSession) Close() error { s.alive = false; return nil }

// TestSessionName_ClaudeCodeLikeFlow tests the claudecode/gemini/cursor pattern:
// CurrentSessionID()="" initially, but an early EventText carries SessionID.
func TestSessionName_ClaudeCodeLikeFlow(t *testing.T) {
	sess := newClaudeCodeLikeSession("claude-session-001")
	agent := &controllableAgent{nextSession: sess}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	initial := e.sessions.GetOrCreateActive(userKey)
	initial.SetAgentSessionID("claude-session-old", "claudecode")
	e.sessions.Save()

	// /new with a custom name
	e.cmdNew(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"Claude任务"})

	// Send message
	e.ReceiveMessage(p, &Message{
		SessionKey: userKey,
		Content:    "帮我重构代码",
		ReplyCtx:   "ctx2",
	})
	time.Sleep(200 * time.Millisecond)

	// Verify session name mapped via EventText path
	gotName := e.sessions.GetSessionName("claude-session-001")
	if gotName != "Claude任务" {
		t.Fatalf("GetSessionName(%q) = %q, want %q — claudecode-like EventText name mapping failed",
			"claude-session-001", gotName, "Claude任务")
	}
}

// acpLikeSession simulates ACP behavior:
// - CurrentSessionID() returns the thread ID immediately after creation
//   (ACP does handshake before returning from StartSession)
type acpLikeSession struct {
	threadID string
	events   chan Event
	alive    bool
}

func newACPLikeSession(threadID string) *acpLikeSession {
	return &acpLikeSession{
		threadID: threadID,
		events:   make(chan Event, 8),
		alive:    true,
	}
}

func (s *acpLikeSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.events <- Event{Type: EventText, Content: "Reply", SessionID: s.threadID}
	s.events <- Event{Type: EventResult, SessionID: s.threadID, Content: "Done", Done: true}
	return nil
}
func (s *acpLikeSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *acpLikeSession) Events() <-chan Event                                 { return s.events }
func (s *acpLikeSession) CurrentSessionID() string                             { return s.threadID }
func (s *acpLikeSession) Alive() bool                                          { return s.alive }
func (s *acpLikeSession) Close() error                                         { s.alive = false; return nil }

// TestSessionName_ACPLikeFlow tests ACP pattern: CurrentSessionID() is non-empty
// immediately at creation, so name mapping happens in startOrResumeSession.
func TestSessionName_ACPLikeFlow(t *testing.T) {
	sess := newACPLikeSession("acp-session-001")
	agent := &controllableAgent{nextSession: sess}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	userKey := "test:user1"

	// /new with a custom name
	e.cmdNew(p, &Message{SessionKey: userKey, ReplyCtx: "ctx"}, []string{"ACP任务"})

	// Send message — startOrResumeSession should map the name immediately
	e.ReceiveMessage(p, &Message{
		SessionKey: userKey,
		Content:    "帮我部署",
		ReplyCtx:   "ctx2",
	})
	time.Sleep(200 * time.Millisecond)

	gotName := e.sessions.GetSessionName("acp-session-001")
	if gotName != "ACP任务" {
		t.Fatalf("GetSessionName(%q) = %q, want %q — ACP-like immediate ID name mapping failed",
			"acp-session-001", gotName, "ACP任务")
	}
}

// TestBtwAlias_ResolvesToPs verifies that /btw is accepted as an alias for /ps.
func TestBtwAlias_ResolvesToPs(t *testing.T) {
	id := matchPrefix("btw", builtinCommands)
	if id != "ps" {
		t.Fatalf("matchPrefix(\"btw\") = %q, want \"ps\"", id)
	}
	id2 := matchPrefix("ps", builtinCommands)
	if id2 != "ps" {
		t.Fatalf("matchPrefix(\"ps\") = %q, want \"ps\"", id2)
	}
}
