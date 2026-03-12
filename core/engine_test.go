package core

import (
	"context"
	"fmt"
	"os"
	"strings"
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

func (s *stubAgentSession) Send(_ string, _ []ImageAttachment) error             { return nil }
func (s *stubAgentSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *stubAgentSession) Events() <-chan Event                                 { return make(chan Event) }
func (s *stubAgentSession) CurrentSessionID() string                             { return "stub-session" }
func (s *stubAgentSession) Alive() bool                                          { return true }
func (s *stubAgentSession) Close() error                                         { return nil }

type stubPlatformEngine struct {
	n    string
	sent []string
}

func (p *stubPlatformEngine) Name() string               { return p.n }
func (p *stubPlatformEngine) Start(MessageHandler) error { return nil }
func (p *stubPlatformEngine) Reply(_ context.Context, _ any, content string) error {
	p.sent = append(p.sent, content)
	return nil
}
func (p *stubPlatformEngine) Send(_ context.Context, _ any, content string) error {
	p.sent = append(p.sent, content)
	return nil
}
func (p *stubPlatformEngine) Stop() error { return nil }

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
	repliedCards []*Card
	sentCards    []*Card
	cardErr      error
}

func (p *stubCardPlatform) ReplyCard(_ context.Context, _ any, card *Card) error {
	if p.cardErr != nil {
		return p.cardErr
	}
	p.repliedCards = append(p.repliedCards, card)
	return nil
}

func (p *stubCardPlatform) SendCard(_ context.Context, _ any, card *Card) error {
	if p.cardErr != nil {
		return p.cardErr
	}
	p.sentCards = append(p.sentCards, card)
	return nil
}

type stubModelModeAgent struct {
	stubAgent
	model           string
	mode            string
	reasoningEffort string
}

func (a *stubModelModeAgent) SetModel(model string) {
	a.model = model
}

func (a *stubModelModeAgent) GetModel() string {
	return a.model
}

func (a *stubModelModeAgent) AvailableModels(_ context.Context) []ModelOption {
	return []ModelOption{
		{Name: "gpt-4.1", Desc: "Balanced"},
		{Name: "gpt-4.1-mini", Desc: "Fast"},
	}
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

func newTestEngine() *Engine {
	return NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
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

func collectCardActionRows(card *Card) []CardActions {
	rows := make([]CardActions, 0)
	for _, elem := range card.Elements {
		if row, ok := elem.(CardActions); ok {
			rows = append(rows, row)
		}
	}
	return rows
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
	if strings.Contains(p.sent[0], "admin") {
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

	// Shell runs async in a goroutine, so the command should be accepted (not blocked).
	// No "admin" error should be in replies.
	for _, s := range p.sent {
		if strings.Contains(s, "admin") {
			t.Errorf("admin user should not be blocked, got: %s", s)
		}
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

// --- quiet tests ---

func TestQuietSessionToggle(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// /quiet — per-session toggle on
	e.cmdQuiet(p, msg, nil)

	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()

	if state == nil {
		t.Fatal("expected interactiveState to be created")
	}
	state.mu.Lock()
	q := state.quiet
	state.mu.Unlock()
	if !q {
		t.Fatal("expected session quiet to be true")
	}

	// /quiet — per-session toggle off
	e.cmdQuiet(p, msg, nil)
	state.mu.Lock()
	q = state.quiet
	state.mu.Unlock()
	if q {
		t.Fatal("expected session quiet to be false after second toggle")
	}
}

func TestQuietSessionResetsOnNewSession(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Enable per-session quiet
	e.cmdQuiet(p, msg, nil)

	// Simulate /new
	e.cleanupInteractiveState("test:user1")

	// State should be gone, quiet resets
	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	if state != nil {
		t.Fatal("expected interactiveState to be cleaned up")
	}

	// Global quiet should still be off
	e.quietMu.RLock()
	gq := e.quiet
	e.quietMu.RUnlock()
	if gq {
		t.Fatal("expected global quiet to be false")
	}
}

func TestQuietGlobalToggle(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Default: global quiet is off
	if e.quiet {
		t.Fatal("expected global quiet to be false by default")
	}

	// /quiet global — toggle on
	e.cmdQuiet(p, msg, []string{"global"})
	e.quietMu.RLock()
	q := e.quiet
	e.quietMu.RUnlock()
	if !q {
		t.Fatal("expected global quiet to be true")
	}

	// /quiet global — toggle off
	e.cmdQuiet(p, msg, []string{"global"})
	e.quietMu.RLock()
	q = e.quiet
	e.quietMu.RUnlock()
	if q {
		t.Fatal("expected global quiet to be false after second toggle")
	}
}

func TestQuietGlobalPersistsAcrossSessions(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Enable global quiet
	e.cmdQuiet(p, msg, []string{"global"})

	// Simulate /new
	e.cleanupInteractiveState("test:user1")

	// Global quiet should still be on
	e.quietMu.RLock()
	q := e.quiet
	e.quietMu.RUnlock()
	if !q {
		t.Fatal("expected global quiet to remain true after session cleanup")
	}
}

func TestQuietGlobalAndSessionCombined(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	// Only global quiet on — should suppress
	e.cmdQuiet(p, msg, []string{"global"})
	e.quietMu.RLock()
	gq := e.quiet
	e.quietMu.RUnlock()
	if !gq {
		t.Fatal("expected global quiet on")
	}

	// Session quiet is off (no state yet) — global alone should be enough
	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	if state != nil {
		t.Fatal("expected no session state yet")
	}

	// Turn off global, turn on session
	e.cmdQuiet(p, msg, []string{"global"}) // global off
	e.cmdQuiet(p, msg, nil)                // session on

	e.quietMu.RLock()
	gq = e.quiet
	e.quietMu.RUnlock()
	if gq {
		t.Fatal("expected global quiet off")
	}

	e.interactiveMu.Lock()
	state = e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	sq := state.quiet
	state.mu.Unlock()
	if !sq {
		t.Fatal("expected session quiet on")
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
	session.AgentSessionID = "session-123"
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

func TestDeleteMode_ToggleSelectionUpdatesCard(t *testing.T) {
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
		t.Fatal("expected delete mode card after toggle")
	}

	btn, ok := findCardAction(card, "act:/delete-mode toggle session-2")
	if !ok {
		t.Fatal("expected toggle action for selected session")
	}
	if btn.Type != "primary" {
		t.Fatalf("selected toggle type = %q, want primary", btn.Type)
	}
	if !strings.Contains(card.RenderText(), "1 selected") {
		t.Fatalf("card text = %q, want selected count", card.RenderText())
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
		t.Fatal("expected result card after submit")
	}
	if got, want := strings.Join(agent.deleted, ","), "session-1,session-3"; got != want {
		t.Fatalf("deleted = %q, want %q", got, want)
	}
	if !strings.Contains(resultCard.RenderText(), "Session deleted: One") {
		t.Fatalf("result text = %q, want delete result", resultCard.RenderText())
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
		t.Fatal("expected result card after submit")
	}
	resultText := resultCard.RenderText()
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
	e.sessions.GetOrCreateActive(msg.SessionKey).AgentSessionID = "session-1"

	e.cmdDelete(p, msg, nil)
	_ = e.handleCardNav("act:/delete-mode toggle session-1", msg.SessionKey)
	resultCard := e.handleCardNav("act:/delete-mode submit", msg.SessionKey)
	if resultCard == nil {
		t.Fatal("expected result card")
	}
	if len(agent.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", agent.deleted)
	}
	if !strings.Contains(resultCard.RenderText(), "Cannot delete the currently active session") {
		t.Fatalf("result text = %q, want active-session warning", resultCard.RenderText())
	}
}

func TestExecuteCardActionStop_PreservesQuietStateWithoutCleanupReinsert(t *testing.T) {
	e := newTestEngine()
	e.interactiveMu.Lock()
	e.interactiveStates["test:user1"] = &interactiveState{quiet: true}
	e.interactiveMu.Unlock()

	e.executeCardAction("/stop", "", "test:user1")

	e.interactiveMu.Lock()
	state := e.interactiveStates["test:user1"]
	e.interactiveMu.Unlock()
	if state == nil {
		t.Fatal("expected interactive state to remain for quiet preservation")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.quiet {
		t.Fatal("expected quiet state to remain enabled")
	}
	if state.pending != nil {
		t.Fatal("expected pending permission to be cleared")
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
	if got := p.buttonRows[0][0].Data; got != "cmd:/model 1" {
		t.Fatalf("first /model button = %q, want %q", got, "cmd:/model 1")
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
	s.AgentSessionID = "existing-session"
	s.AddHistory("user", "hello")

	e.cmdReasoning(p, msg, []string{"3"})

	if agent.reasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want high", agent.reasoningEffort)
	}
	if s.AgentSessionID != "" {
		t.Fatalf("AgentSessionID = %q, want cleared", s.AgentSessionID)
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
	e.sessions.GetOrCreateActive("test:user1").AgentSessionID = sessions[5].ID

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
