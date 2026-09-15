package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type anonymousProgressPlatform struct {
	stubRichCardSilentPlatform
}

func (p *anonymousProgressPlatform) BuildAnonymousRichCard(status CardStatus, tools int, markdown string, streaming bool, lang Language) string {
	return fmt.Sprintf("anonymous status=%s tools=%d streaming=%t lang=%s body=%q", status, tools, streaming, lang, markdown)
}

func TestAnonymousProgressStartsBeforeEventsAndStreamsWithoutDetails(t *testing.T) {
	for _, showDetails := range []bool{true, false} {
		t.Run(fmt.Sprintf("display-details-%t", showDetails), func(t *testing.T) {
			p := &anonymousProgressPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test-card"}}}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
			e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous", ThinkingMessages: showDetails, ToolMessages: showDetails})
			e.SetInstantReply(InstantReplyCfg{Enabled: true, Content: "duplicate instant reply"})
			s := e.sessions.GetOrCreateActive("card:user")
			as := newControllableSession("anonymous")
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.processInteractiveEvents(state, s, e.sessions, "card:user", "message", time.Now(), nil, nil, "trigger")
			}()
			t.Cleanup(func() {
				e.cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("event processor did not exit")
				}
			})
			deadline := time.After(time.Second)
			for {
				starts, _, _, _ := p.snapshot()
				if len(starts) == 1 {
					if !strings.Contains(starts[0], "anonymous") || !strings.Contains(starts[0], "tools=0") {
						t.Fatalf("initial card = %q", starts[0])
					}
					break
				}
				select {
				case <-deadline:
					t.Fatal("no immediate progress card before the first agent event")
				case <-time.After(time.Millisecond):
				}
			}
			as.events <- Event{Type: EventThinking, Content: "private-reasoning-sentinel"}
			as.events <- Event{Type: EventToolUse, ToolName: "private-tool-sentinel", ToolInput: "private-input-sentinel"}
			as.events <- Event{Type: EventToolResult, ToolName: "private-tool-sentinel", ToolResult: "private-result-sentinel"}
			as.events <- Event{Type: EventToolUse, ToolName: "private-tool-two"}
			answer := "Answer with `code` and /workspace/main.go"
			as.events <- Event{Type: EventText, Content: answer}
			as.events <- Event{Type: EventResult, Content: answer, Done: true}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not complete")
			}
			starts, streams, updates, deletes := p.snapshot()
			if len(starts) != 1 || len(streams) == 0 || len(updates) == 0 || deletes != 0 {
				t.Fatalf("lifecycle: starts=%v streams=%v updates=%v deletes=%d", starts, streams, updates, deletes)
			}
			final := updates[len(updates)-1]
			if !strings.Contains(final, "status=done") || !strings.Contains(final, "tools=2") || !strings.Contains(final, answer) {
				t.Fatalf("final card = %q", final)
			}
			all := strings.Join(append(append(append(starts, streams...), updates...), p.getSent()...), "\n")
			for _, forbidden := range []string{"private-", "duplicate instant reply", "step=", "footer="} {
				if strings.Contains(all, forbidden) {
					t.Fatalf("outbound content contains %q: %s", forbidden, all)
				}
			}
			if len(p.getSent()) != 0 {
				t.Fatalf("unexpected standalone replies: %v", p.getSent())
			}
		})
	}
}

func TestAnonymousProgressDoesNotChangeRichOrLegacyModes(t *testing.T) {
	for _, mode := range []string{"legacy", "rich"} {
		t.Run(mode, func(t *testing.T) {
			p := &anonymousProgressPlatform{}
			p.n = "test-card"
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: mode, ThinkingMessages: true, ToolMessages: true})
			as := newControllableSession("compat")
			as.events <- Event{Type: EventThinking, Content: "visible reasoning"}
			as.events <- Event{Type: EventText, Content: "answer"}
			as.events <- Event{Type: EventResult, Content: "answer", Done: true}
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
			e.processInteractiveEvents(state, e.sessions.GetOrCreateActive("card:user"), e.sessions, "card:user", "message", time.Now(), nil, nil, "trigger")
			starts, streams, updates, _ := p.snapshot()
			all := strings.Join(append(append(append(starts, streams...), updates...), p.getSent()...), "\n")
			if strings.Contains(all, "anonymous") {
				t.Fatalf("anonymous renderer used in %s mode: %s", mode, all)
			}
		})
	}
}

var _ Platform = (*anonymousProgressPlatform)(nil)

func TestAnonymousProgressAdapterDiscardsDetailedInputs(t *testing.T) {
	p := &anonymousProgressPlatform{}
	a := anonymousRichCardAdapter{renderer: p, lang: LangEnglish}
	steps := []ToolStep{
		{Kind: ToolStepKindThinking, Summary: "private-reasoning"},
		{Kind: ToolStepKindTool, Name: "private-tool", Summary: "private-input", Result: "private-result"},
		{Name: "private-legacy-tool"},
	}
	got := a.BuildRichCard(CardStatusWorking, "private-title", steps, "visible answer", true, "private-footer")
	if strings.Contains(got, "private-") || !strings.Contains(got, "tools=2") || !strings.Contains(got, "visible answer") {
		t.Fatalf("anonymous payload = %q", got)
	}
}

func TestAnonymousProgressSilentReplyRemovesOnlyPlaceholder(t *testing.T) {
	for _, tools := range []bool{false, true} {
		t.Run(fmt.Sprint(tools), func(t *testing.T) {
			p := &anonymousProgressPlatform{}
			p.n = "test-card"
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous"})
			as := newControllableSession("silent")
			if tools {
				as.events <- Event{Type: EventToolUse, ToolName: "private-tool"}
			}
			as.events <- Event{Type: EventText, Content: "NO_R"}
			as.events <- Event{Type: EventText, Content: "EPLY"}
			as.events <- Event{Type: EventResult, Content: "NO_REPLY", Done: true}
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
			e.processInteractiveEvents(state, e.sessions.GetOrCreateActive("card:user"), e.sessions, "card:user", "message", time.Now(), nil, nil, "trigger")
			starts, streams, updates, deletes := p.snapshot()
			if len(starts) != 1 || len(streams) != 0 || deletes != 1 || len(p.getSent()) != 0 {
				t.Fatalf("silent lifecycle: starts=%v streams=%v updates=%v deletes=%d sent=%v", starts, streams, updates, deletes, p.getSent())
			}
			if strings.Contains(strings.Join(append(starts, updates...), "\n"), "NO_R") {
				t.Fatal("silent marker was rendered")
			}
		})
	}
}

func TestAnonymousProgressResolvesImmediateCardOnEarlyExit(t *testing.T) {
	for _, mode := range []string{"cancel", "channel-close", "send-error"} {
		t.Run(mode, func(t *testing.T) {
			p := &anonymousProgressPlatform{}
			p.n = "test-card"
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous"})
			as := newControllableSession("early-exit")
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
			sendDone := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.processInteractiveEvents(state, e.sessions.GetOrCreateActive("card:user"), e.sessions, "card:user", "message", time.Now(), nil, sendDone, "trigger")
			}()
			t.Cleanup(e.cancel)
			awaitAnonymousProgress(t, func() bool { starts, _, _, _ := p.snapshot(); return len(starts) == 1 })
			switch mode {
			case "cancel":
				e.cancel()
			case "channel-close":
				close(as.events)
			case "send-error":
				sendDone <- fmt.Errorf("agent unavailable")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("early exit did not finish")
			}
			_, _, updates, _ := p.snapshot()
			if len(updates) != 1 || !strings.Contains(updates[0], "status=error") {
				t.Fatalf("placeholder was left active: %v", updates)
			}
		})
	}
}

func awaitAnonymousProgress(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for anonymous card activity")
}

func TestAnonymousProgressAppearsBeforeAgentStartup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			p := &anonymousProgressPlatform{}
			p.n = "test-card"
			a := &receiptStartAgent{
				session: &receiptAgentSession{queuingAgentSession: newQueuingSession("startup")},
				entered: make(chan struct{}), release: make(chan struct{}),
			}
			if fail {
				a.err = fmt.Errorf("startup unavailable")
			}
			e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
			e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous"})
			t.Cleanup(e.cancel)
			e.ReceiveMessage(p, &Message{Platform: p.Name(), SessionKey: "card:user", UserID: "user", Content: "start work", ReplyCtx: "trigger"})
			select {
			case <-a.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("agent did not reach startup")
			}
			starts, _, _, _ := p.snapshot()
			if len(starts) != 1 {
				t.Fatalf("no progress card while startup is blocked: %v", starts)
			}
			close(a.release)
			if !fail {
				awaitAnonymousProgress(t, func() bool {
					a.session.sendMu.Lock()
					defer a.session.sendMu.Unlock()
					return len(a.session.sendCalls) == 1
				})
				a.session.events <- Event{Type: EventResult, Content: "answer", Done: true}
			}
			awaitAnonymousProgress(t, func() bool { return !e.sessions.GetOrCreateActive("card:user").Busy() })
			starts, _, updates, _ := p.snapshot()
			status := "status=done"
			if fail {
				status = "status=error"
			}
			if len(starts) != 1 || len(updates) != 1 || !strings.Contains(updates[0], status) {
				t.Fatalf("startup card was duplicated or left active: starts=%v updates=%v", starts, updates)
			}
		})
	}
}

func TestAnonymousProgressKeepsPartialAnswerOnInterruptedCard(t *testing.T) {
	p := &anonymousProgressPlatform{}
	p.n = "test-card"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous"})
	as := newControllableSession("partial-exit")
	as.events <- Event{Type: EventText, Content: "partial answer\nNO_REPLY"}
	close(as.events)
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
	e.processInteractiveEvents(state, e.sessions.GetOrCreateActive("card:user"), e.sessions, "card:user", "message", time.Now(), nil, nil, "trigger")
	_, _, updates, _ := p.snapshot()
	final := updates[len(updates)-1]
	if !strings.Contains(final, "status=error") || !strings.Contains(final, "partial answer") || strings.Contains(final, "NO_REPLY") || len(p.getSent()) != 0 {
		t.Fatalf("interrupted answer left its card or exposed marker: updates=%v sent=%v", updates, p.getSent())
	}
}

func TestAnonymousProgressPermissionKeepsAnswerOnCard(t *testing.T) {
	p := &anonymousProgressPlatform{}
	p.n = "test-card"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous", ToolMessages: true})
	// A disabled legacy preview used to flush a duplicate answer at permission boundaries.
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: false})
	as := newControllableSession("permission")
	key := "card:user"
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
	e.interactiveStates[key] = state
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.processInteractiveEvents(state, e.sessions.GetOrCreateActive(key), e.sessions, key, "message", time.Now(), nil, nil, "trigger")
	}()
	as.events <- Event{Type: EventText, Content: "Before permission. "}
	as.events <- Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "echo approved"}
	awaitAnonymousProgress(t, func() bool { return strings.Contains(strings.Join(p.getSent(), "\n"), "echo approved") })
	if !e.handlePendingPermission(p, &Message{SessionKey: key, UserID: "user", Content: "allow", ReplyCtx: "trigger"}, "allow", key) {
		t.Fatal("permission reply was not handled")
	}
	as.events <- Event{Type: EventText, Content: "After permission."}
	as.events <- Event{Type: EventResult, Content: "After permission.", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		e.cancel()
		t.Fatal("permission turn did not finish")
	}
	starts, _, updates, _ := p.snapshot()
	if len(starts) != 1 || !strings.Contains(updates[len(updates)-1], "Before permission. After permission.") {
		t.Fatalf("answer did not remain on one card: starts=%v updates=%v", starts, updates)
	}
	if strings.Contains(strings.Join(p.getSent(), "\n"), "Before permission.") {
		t.Fatalf("duplicated answer outside card: %v", p.getSent())
	}
}

type anonymousCardCall struct {
	kind             string
	handle, replyCtx any
	content          string
}

type anonymousDeliveryPlatform struct {
	anonymousProgressPlatform
	callMu                            sync.Mutex
	calls                             []anonymousCardCall
	failStart, failStream, failUpdate bool
}

func (p *anonymousDeliveryPlatform) record(call anonymousCardCall) {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *anonymousDeliveryPlatform) callSnapshot() []anonymousCardCall {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	return append([]anonymousCardCall(nil), p.calls...)
}

func (p *anonymousDeliveryPlatform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	if p.failStart {
		return nil, fmt.Errorf("initial card unavailable")
	}
	h, err := p.anonymousProgressPlatform.SendPreviewStart(ctx, rctx, content)
	p.record(anonymousCardCall{kind: "start", handle: h, replyCtx: rctx, content: content})
	return h, err
}

func (p *anonymousDeliveryPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	p.record(anonymousCardCall{kind: "update", handle: handle, content: content})
	if p.failUpdate {
		return fmt.Errorf("card update unavailable")
	}
	return p.anonymousProgressPlatform.UpdateMessage(ctx, handle, content)
}

func (p *anonymousDeliveryPlatform) StreamRichCardText(ctx context.Context, handle any, text string) error {
	p.record(anonymousCardCall{kind: "stream", handle: handle, content: text})
	if p.failStream {
		return ErrNotSupported
	}
	return p.anonymousProgressPlatform.StreamRichCardText(ctx, handle, text)
}

func TestAnonymousProgressFallbackNeverRestoresDetails(t *testing.T) {
	for _, failure := range []string{"start", "stream", "update"} {
		t.Run(failure, func(t *testing.T) {
			p := &anonymousDeliveryPlatform{failStart: failure == "start", failStream: failure == "stream", failUpdate: failure == "update"}
			p.n = "test-card"
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous", ToolMessages: true, ThinkingMessages: true})
			as := newControllableSession("fallback")
			as.events <- Event{Type: EventThinking, Content: "private-reasoning"}
			as.events <- Event{Type: EventToolUse, ToolName: "private-tool", ToolInput: "private-input"}
			as.events <- Event{Type: EventToolResult, ToolResult: "private-result"}
			as.events <- Event{Type: EventText, Content: "visible answer"}
			as.events <- Event{Type: EventResult, Content: "visible answer", Done: true}
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
			e.processInteractiveEvents(state, e.sessions.GetOrCreateActive("card:user"), e.sessions, "card:user", "message", time.Now(), nil, nil, "trigger")
			var outbound []string
			for _, call := range p.callSnapshot() {
				outbound = append(outbound, call.content)
			}
			outbound = append(outbound, p.getSent()...)
			all := strings.Join(outbound, "\n")
			if strings.Contains(all, "private-") || !strings.Contains(all, "visible answer") || !strings.Contains(all, "status=done") {
				t.Fatalf("fallback violated anonymous delivery: %s", all)
			}
			if failure == "stream" && len(p.getSent()) != 0 {
				t.Fatalf("stream fallback should patch original card: %v", p.getSent())
			}
		})
	}
}

func TestAnonymousProgressHonorsDisabledAnswerPreview(t *testing.T) {
	for _, cfg := range []StreamPreviewCfg{{Enabled: false}, {Enabled: true, DisabledPlatforms: []string{"test-card"}}} {
		p := &anonymousDeliveryPlatform{}
		p.n = "test-card"
		e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
		e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich-anonymous"})
		e.SetStreamPreviewCfg(cfg)
		as := newControllableSession("disabled-preview")
		as.events <- Event{Type: EventText, Content: "visible only at completion"}
		as.events <- Event{Type: EventToolUse, ToolName: "private-tool"}
		as.events <- Event{Type: EventResult, Content: "visible only at completion", Done: true}
		state := &interactiveState{agentSession: as, platform: p, replyCtx: "trigger"}
		e.processInteractiveEvents(state, e.sessions.GetOrCreateActive("card:user"), e.sessions, "card:user", "message", time.Now(), nil, nil, "trigger")
		for _, call := range p.callSnapshot() {
			if call.kind == "stream" || (strings.Contains(call.content, "visible only") && !strings.Contains(call.content, "status=done")) {
				t.Fatalf("disabled preview still streamed answer: %+v", call)
			}
		}
		_, _, updates, _ := p.snapshot()
		if !strings.Contains(updates[len(updates)-1], "visible only at completion") {
			t.Fatal("final answer missing")
		}
	}
}
