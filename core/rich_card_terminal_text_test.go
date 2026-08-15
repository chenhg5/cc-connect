package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubRichCardTerminalTextPlatform struct {
	*stubRichCardSilentPlatform
	mu             sync.Mutex
	prepared       []string
	terminalTexts  []string
	deletedHandles []any
}

type blockingOriginalDeleteTerminalTextPlatform struct {
	*stubRichCardTerminalTextPlatform
	deleteStarted chan struct{}
	releaseDelete chan struct{}
	startOnce     sync.Once
}

func (p *blockingOriginalDeleteTerminalTextPlatform) DeletePreviewMessage(ctx context.Context, handle any) error {
	p.mu.Lock()
	p.deletedHandles = append(p.deletedHandles, handle)
	p.mu.Unlock()
	if handle == "handle-1" {
		p.startOnce.Do(func() { close(p.deleteStarted) })
		select {
		case <-p.releaseDelete:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (p *stubRichCardTerminalTextPlatform) PrepareRichCardTerminalText(_ context.Context, _ any, markdown string) (string, bool, error) {
	p.mu.Lock()
	p.prepared = append(p.prepared, markdown)
	p.mu.Unlock()
	if !strings.Contains(markdown, "@Reviewer-Bot") {
		return markdown, false, nil
	}
	return strings.ReplaceAll(markdown, "@Reviewer-Bot", `<at user_id="ou_bot">Reviewer-Bot</at>`), true, nil
}

func (p *stubRichCardTerminalTextPlatform) SendRichCardTerminalText(_ context.Context, _ any, content string) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminalTexts = append(p.terminalTexts, content)
	return "terminal-text-handle", nil
}

func (p *stubRichCardTerminalTextPlatform) DeletePreviewMessage(_ context.Context, handle any) error {
	p.mu.Lock()
	p.deletedHandles = append(p.deletedHandles, handle)
	p.mu.Unlock()
	return nil
}

func (p *stubRichCardTerminalTextPlatform) terminalSnapshot() (prepared, sent []string, deleted []any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prepared...), append([]string(nil), p.terminalTexts...), append([]any(nil), p.deletedHandles...)
}

func TestProcessInteractiveEventsRichCardUsesTrackableTextForNativeMention(t *testing.T) {
	base := &stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	p := &stubRichCardTerminalTextPlatform{stubRichCardSilentPlatform: base}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "compact", CardMode: "rich"})
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: false})

	sessionKey := "feishu:user-rich-native-mention"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-rich-native-mention")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-rich-native-mention"}
	e.interactiveStates[sessionKey] = state

	answer := "完成，请 @Reviewer-Bot 复核"
	agentSession.events <- Event{Type: EventText, Content: answer}
	agentSession.events <- Event{Type: EventResult, Content: answer, Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-rich-native-mention", time.Now(), nil, nil, state.replyCtx)

	prepared, sent, deleted := p.terminalSnapshot()
	if len(prepared) != 1 || prepared[0] != answer {
		t.Fatalf("terminal preparations = %v", prepared)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], `user_id="ou_bot"`) {
		t.Fatalf("terminal text sends = %v", sent)
	}
	if len(deleted) != 1 || deleted[0] != "handle-1" {
		t.Fatalf("deleted handles = %v, want original lifecycle card", deleted)
	}
	_, _, updates, _ := base.snapshot()
	for _, update := range updates {
		if strings.Contains(update, "status=done") {
			t.Fatalf("native mention was also delivered as a Done card: %v", updates)
		}
	}
	history := session.GetHistory(0)
	if len(history) == 0 || history[len(history)-1].Content != answer {
		t.Fatalf("assistant history = %+v", history)
	}
}

func TestProcessInteractiveEventsRichCardMentionRecallDeletesTextReplacement(t *testing.T) {
	base := &stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	terminal := &stubRichCardTerminalTextPlatform{stubRichCardSilentPlatform: base}
	p := &blockingOriginalDeleteTerminalTextPlatform{
		stubRichCardTerminalTextPlatform: terminal,
		deleteStarted:                    make(chan struct{}),
		releaseDelete:                    make(chan struct{}),
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "compact", CardMode: "rich"})
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: false})

	const (
		sessionKey = "feishu:user-rich-native-mention-recall"
		messageID  = "m-rich-native-mention-recall"
	)
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-rich-native-mention-recall")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-rich-native-mention-recall"}
	e.interactiveStates[sessionKey] = state

	turnDone := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, sessionKey, messageID, time.Now(), nil, nil, state.replyCtx)
		close(turnDone)
	}()
	answer := "完成，请 @Reviewer-Bot 复核"
	agentSession.events <- Event{Type: EventResult, Content: answer, Done: true}

	select {
	case <-p.deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("terminal text was not sent before old-card cleanup")
	}
	state.cancelTurnSilently(messageID)
	close(p.releaseDelete)

	select {
	case <-turnDone:
	case <-time.After(time.Second):
		t.Fatal("recalled terminal mention turn did not finish")
	}
	_, sent, deleted := terminal.terminalSnapshot()
	if len(sent) != 1 {
		t.Fatalf("terminal text sends = %v", sent)
	}
	if len(deleted) != 2 || deleted[0] != "handle-1" || deleted[1] != "terminal-text-handle" {
		t.Fatalf("deleted handles = %v, want lifecycle card and text replacement", deleted)
	}
	for _, entry := range session.GetHistory(0) {
		if entry.Role == "assistant" {
			t.Fatalf("recalled terminal text entered history: %+v", entry)
		}
	}
}
