package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// End to end through core.Engine: an appServerSession whose turn hits a retried
// "prompt is too long" and then fails with new-api's 503 makes the engine start
// a fresh session and run the cron job once more (2026-10-21 jingyu outing cron).

type fakeCronPlatform struct {
	mu   sync.Mutex
	sent []string
}

func (p *fakeCronPlatform) Name() string                    { return "discord" }
func (p *fakeCronPlatform) Start(core.MessageHandler) error { return nil }
func (p *fakeCronPlatform) Reply(_ context.Context, _ any, c string) error {
	return p.Send(context.Background(), nil, c)
}
func (p *fakeCronPlatform) Send(_ context.Context, _ any, c string) error {
	p.mu.Lock()
	p.sent = append(p.sent, c)
	p.mu.Unlock()
	return nil
}
func (p *fakeCronPlatform) Stop() error                             { return nil }
func (p *fakeCronPlatform) ReconstructReplyCtx(string) (any, error) { return "rctx", nil }
func (p *fakeCronPlatform) out() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.sent, "\n")
}

// scriptedAgent starts sessions s1, s2, …; each send plays the next script step
// as app-server notifications into a real appServerSession.
type scriptedAgent struct {
	mu     sync.Mutex
	script []string
	sentBy []string
	starts []string
}

func (a *scriptedAgent) Name() string { return "codex" }
func (a *scriptedAgent) StartSession(_ context.Context, resume string) (core.AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts = append(a.starts, resume)
	s := &appServerSession{events: make(chan core.Event, 16)}
	s.alive.Store(true)
	return &scriptedSession{a: a, s: s, id: fmt.Sprintf("s%d", len(a.starts))}, nil
}
func (a *scriptedAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *scriptedAgent) Stop() error { return nil }

type scriptedSession struct {
	a    *scriptedAgent
	s    *appServerSession
	id   string
	turn int
}

func (w *scriptedSession) Send(string, string, []core.ImageAttachment, []core.FileAttachment) error {
	w.a.mu.Lock()
	step := "ok"
	if len(w.a.script) > 0 {
		step, w.a.script = w.a.script[0], w.a.script[1:]
	}
	w.a.sentBy = append(w.a.sentBy, w.id)
	w.a.mu.Unlock()
	w.turn++
	turn := fmt.Sprintf("turn-%d", w.turn)
	n := func(method, params string) { w.s.handleNotification(method, json.RawMessage(params)) }
	n("turn/started", `{"threadId":"t","turn":{"id":"`+turn+`"}}`)
	switch step {
	case "long503":
		n("error", tooLongRetryNotif)
		n("error", `{"error":{"message":"Reconnecting... 2/5\n`+noChannel503+`"},"willRetry":true}`)
		n("turn/completed", `{"threadId":"t","turn":{"id":"`+turn+`","status":"failed","error":{"message":"`+noChannel503+`"}}}`)
	case "503":
		n("turn/completed", `{"threadId":"t","turn":{"id":"`+turn+`","status":"failed","error":{"message":"`+noChannel503+`"}}}`)
	default:
		n("item/completed", `{"threadId":"t","turnId":"`+turn+`","item":{"type":"agentMessage","id":"m","text":"reply:`+step+`"}}`)
		n("turn/completed", `{"threadId":"t","turn":{"id":"`+turn+`","status":"completed"}}`)
	}
	return nil
}
func (w *scriptedSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (w *scriptedSession) Events() <-chan core.Event                             { return w.s.events }
func (w *scriptedSession) CurrentSessionID() string                              { return w.id }
func (w *scriptedSession) Alive() bool                                           { return w.s.alive.Load() }
func (w *scriptedSession) Close() error                                          { w.s.alive.Store(false); return nil }

func runCronScript(t *testing.T, script ...string) (*fakeCronPlatform, *scriptedAgent) {
	t.Helper()
	p := &fakeCronPlatform{}
	a := &scriptedAgent{script: script}
	e := core.NewEngine("test", a, []core.Platform{p}, "", core.LangEnglish)
	t.Cleanup(func() { e.Stop() })
	// The first job binds the channel to s1; the second is the one under test.
	if err := e.ExecuteCronJob(&core.CronJob{ID: "bind", SessionKey: "discord:c1", Prompt: "hi"}); err != nil {
		t.Fatalf("first job: %v", err)
	}
	_ = e.ExecuteCronJob(&core.CronJob{ID: "92af21b3", SessionKey: "discord:c1", Prompt: "外出"})
	return p, a
}

func TestEngine_RetriedPromptTooLongThen503StartsFreshAndReruns(t *testing.T) {
	p, a := runCronScript(t, "ok", "long503", "again")
	a.mu.Lock()
	by, starts := append([]string(nil), a.sentBy...), append([]string(nil), a.starts...)
	a.mu.Unlock()
	if len(by) != 3 || by[1] != "s1" || by[2] != "s2" || len(starts) != 2 || starts[1] != "" {
		t.Fatalf("sends on %q, starts %q; want the job re-run once on a fresh s2", by, starts)
	}
	out := p.out()
	if strings.Count(out, "♻️") != 1 || !strings.Contains(out, "reply:again") {
		t.Fatalf("channel:\n%s", out)
	}
}

func TestEngine_Only503NoFreshSession(t *testing.T) {
	p, a := runCronScript(t, "ok", "503", "never")
	a.mu.Lock()
	by, starts := append([]string(nil), a.sentBy...), append([]string(nil), a.starts...)
	a.mu.Unlock()
	if len(by) != 2 || len(starts) != 1 {
		t.Fatalf("sends on %q, starts %q; want no re-run", by, starts)
	}
	if out := p.out(); strings.Contains(out, "♻️") || strings.Contains(out, "reply:never") {
		t.Fatalf("channel:\n%s", out)
	}
}
