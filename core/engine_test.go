package core

import (
	"context"
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

type stubModelModeAgent struct {
	stubAgent
	model string
	mode  string
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

type stubListAgent struct {
	stubAgent
	sessions []AgentSessionInfo
}

func (a *stubListAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return a.sessions, nil
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
	btn, ok := findCardAction(card, "nav:/help agent")
	if !ok {
		t.Fatal("expected agent help tab to exist")
	}
	if btn.Type != "primary" {
		t.Fatalf("agent help tab type = %q, want primary", btn.Type)
	}
	if btn.Text != "Agent Configuration" {
		t.Fatalf("agent help tab text = %q, want full title", btn.Text)
	}
}

func TestRenderHelpCard_UsesCurrentLanguage(t *testing.T) {
	tests := []struct {
		name        string
		lang        Language
		wantTab     string
		wantCommand string
	}{
		{name: "traditional chinese", lang: LangTraditionalChinese, wantTab: "會話管理", wantCommand: "建立新會話，參數: [名稱]"},
		{name: "japanese", lang: LangJapanese, wantTab: "セッション管理", wantCommand: "新しいセッションを開始、引数: [名前]"},
		{name: "spanish", lang: LangSpanish, wantTab: "Gestión de sesiones", wantCommand: "Iniciar una nueva sesión, arg: [nombre]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", tt.lang)

			card := e.renderHelpCard()
			got := card.RenderText()
			btn, ok := findCardAction(card, "nav:/help session")
			if !ok {
				t.Fatal("expected session help tab to exist")
			}
			if btn.Text != tt.wantTab {
				t.Fatalf("session tab text = %q, want %q", btn.Text, tt.wantTab)
			}
			if !strings.Contains(got, tt.wantCommand) {
				t.Fatalf("help card = %q, want substring %q", got, tt.wantCommand)
			}
			if strings.Contains(got, "**/model**") {
				t.Fatalf("help card = %q, should default to session tab only", got)
			}
		})
	}
}

func TestRenderHelpCard_FeishuUsesTwoTabRows(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "feishu"}}, "", LangEnglish)

	card := e.renderHelpCardForPlatform("feishu")
	rows := collectCardActionRows(card)
	if len(rows) != 2 {
		t.Fatalf("help tab row count = %d, want 2", len(rows))
	}

	for i, row := range rows {
		if row.Layout != CardActionLayoutEqualColumns {
			t.Fatalf("row %d layout = %q, want %q", i, row.Layout, CardActionLayoutEqualColumns)
		}
		if len(row.Buttons) != 2 {
			t.Fatalf("row %d button count = %d, want 2", i, len(row.Buttons))
		}
	}

	wants := []struct {
		row   int
		index int
		text  string
		value string
		typ   string
	}{
		{row: 0, index: 0, text: "Session Management", value: "nav:/help session", typ: "primary"},
		{row: 0, index: 1, text: "Agent Configuration", value: "nav:/help agent", typ: "default"},
		{row: 1, index: 0, text: "Tools & Automation", value: "nav:/help tools", typ: "default"},
		{row: 1, index: 1, text: "System", value: "nav:/help system", typ: "default"},
	}
	for _, want := range wants {
		btn := rows[want.row].Buttons[want.index]
		if btn.Text != want.text {
			t.Fatalf("row %d button %d text = %q, want %q", want.row, want.index, btn.Text, want.text)
		}
		if btn.Value != want.value {
			t.Fatalf("row %d button %d value = %q, want %q", want.row, want.index, btn.Value, want.value)
		}
		if btn.Type != want.typ {
			t.Fatalf("row %d button %d type = %q, want %q", want.row, want.index, btn.Type, want.typ)
		}
	}
}

func TestRenderHelpCard_NonFeishuKeepsSingleTabRow(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "telegram"}}, "", LangEnglish)

	card := e.renderHelpCardForPlatform("telegram")
	rows := collectCardActionRows(card)
	if len(rows) != 1 {
		t.Fatalf("help tab row count = %d, want 1", len(rows))
	}
	if rows[0].Layout != CardActionLayoutEqualColumns {
		t.Fatalf("tab row layout = %q, want %q", rows[0].Layout, CardActionLayoutEqualColumns)
	}
	if len(rows[0].Buttons) != 4 {
		t.Fatalf("tab row button count = %d, want 4", len(rows[0].Buttons))
	}
}

func TestHandleCardNav_HelpUsesFeishuLayoutFromSessionKey(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "feishu"}}, "", LangEnglish)

	card := e.handleCardNav("nav:/help system", "feishu:chat:user")
	if card == nil {
		t.Fatal("expected help nav card")
	}
	rows := collectCardActionRows(card)
	if len(rows) != 2 {
		t.Fatalf("help tab row count = %d, want 2", len(rows))
	}
	btn, ok := findCardAction(card, "nav:/help system")
	if !ok {
		t.Fatal("expected system help tab to exist")
	}
	if btn.Type != "primary" {
		t.Fatalf("system help tab type = %q, want primary", btn.Type)
	}
}

func TestRenderStatusCard_UsesCurrentLanguage(t *testing.T) {
	tests := []struct {
		name       string
		lang       Language
		wantTitle  string
		wantLabel  string
		wantBack   string
		notWantEng string
	}{
		{name: "chinese", lang: LangChinese, wantTitle: "cc-connect 状态", wantLabel: "项目:", wantBack: "← 返回", notWantEng: "Project:"},
		{name: "japanese", lang: LangJapanese, wantTitle: "cc-connect ステータス", wantLabel: "プロジェクト:", wantBack: "← 戻る", notWantEng: "Project:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", tt.lang)

			card := e.renderStatusCard("test:user1")
			if card.Header == nil {
				t.Fatal("expected status card header")
			}
			if card.Header.Title != tt.wantTitle {
				t.Fatalf("status card title = %q, want %q", card.Header.Title, tt.wantTitle)
			}

			text := card.RenderText()
			if !strings.Contains(text, tt.wantLabel) {
				t.Fatalf("status card = %q, want localized label %q", text, tt.wantLabel)
			}
			if strings.Contains(text, tt.notWantEng) {
				t.Fatalf("status card = %q, should not contain hardcoded english label %q", text, tt.notWantEng)
			}

			btn, ok := findCardAction(card, "nav:/help")
			if !ok {
				t.Fatal("expected status back button")
			}
			if btn.Text != tt.wantBack {
				t.Fatalf("status back button = %q, want %q", btn.Text, tt.wantBack)
			}
		})
	}
}

func TestRenderLangCard_LocalizesTitleAndBackButton(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangJapanese)

	card := e.renderLangCard()
	if card.Header == nil {
		t.Fatal("expected language card header")
	}
	if card.Header.Title != "言語" {
		t.Fatalf("language card title = %q, want %q", card.Header.Title, "言語")
	}

	text := card.RenderText()
	if !strings.Contains(text, "現在の言語") {
		t.Fatalf("language card = %q, want japanese body", text)
	}

	btn, ok := findCardAction(card, "nav:/help")
	if !ok {
		t.Fatal("expected language back button")
	}
	if btn.Text != "← 戻る" {
		t.Fatalf("language back button = %q, want %q", btn.Text, "← 戻る")
	}
}

func TestRenderListCard_LocalizesNavigationButtons(t *testing.T) {
	sessions := make([]AgentSessionInfo, 0, 45)
	base := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 45; i++ {
		sessions = append(sessions, AgentSessionInfo{
			ID:           "agent-session-" + string(rune('A'+(i%26))) + string(rune('a'+(i/26))),
			Summary:      "Session summary",
			MessageCount: i + 1,
			ModifiedAt:   base.Add(time.Duration(i) * time.Minute),
		})
	}

	e := NewEngine("test", &stubListAgent{sessions: sessions}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangJapanese)

	card, err := e.renderListCard("test:user1", 2)
	if err != nil {
		t.Fatalf("renderListCard returned error: %v", err)
	}
	if card.Header == nil {
		t.Fatal("expected list card header")
	}
	if !strings.Contains(card.Header.Title, "セッション") {
		t.Fatalf("list card title = %q, want localized session title", card.Header.Title)
	}

	prev, ok := findCardAction(card, "nav:/list 1")
	if !ok {
		t.Fatal("expected previous page button")
	}
	if prev.Text != "← 前へ" {
		t.Fatalf("previous page button = %q, want %q", prev.Text, "← 前へ")
	}

	back, ok := findCardAction(card, "nav:/help")
	if !ok {
		t.Fatal("expected back button")
	}
	if back.Text != "← 戻る" {
		t.Fatalf("back button = %q, want %q", back.Text, "← 戻る")
	}

	next, ok := findCardAction(card, "nav:/list 3")
	if !ok {
		t.Fatal("expected next page button")
	}
	if next.Text != "次へ →" {
		t.Fatalf("next page button = %q, want %q", next.Text, "次へ →")
	}
}
