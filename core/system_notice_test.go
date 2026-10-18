package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// maomao fork: cc-connect's own notices go out with a marked reply context
// (SystemNoticeMarker), so a platform can post them elsewhere (Discord
// aux_channels); agent output and results go out unmarked.

type noticeMarked struct{ inner any }

// noticeRecordingPlatform records the reply context of every send and marks
// notices the way the Discord platform does.
type noticeRecordingPlatform struct {
	stubPlatformEngine
	ctxMu sync.Mutex
	ctxs  map[string]any // content → reply context it was sent with
}

func (p *noticeRecordingPlatform) MarkSystemNotice(ctx any) any {
	if _, ok := ctx.(noticeMarked); ok {
		return ctx
	}
	return noticeMarked{ctx}
}

func (p *noticeRecordingPlatform) record(rctx any, content string) {
	p.ctxMu.Lock()
	if p.ctxs == nil {
		p.ctxs = map[string]any{}
	}
	p.ctxs[content] = rctx
	p.ctxMu.Unlock()
}

func (p *noticeRecordingPlatform) Send(ctx context.Context, rctx any, content string) error {
	p.record(rctx, content)
	return p.stubPlatformEngine.Send(ctx, rctx, content)
}

func (p *noticeRecordingPlatform) Reply(ctx context.Context, rctx any, content string) error {
	p.record(rctx, content)
	return p.stubPlatformEngine.Reply(ctx, rctx, content)
}

func (p *noticeRecordingPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	return "rctx:" + sessionKey, nil
}

func (p *noticeRecordingPlatform) ctxOf(t *testing.T, contains string) any {
	t.Helper()
	p.ctxMu.Lock()
	defer p.ctxMu.Unlock()
	for content, rctx := range p.ctxs {
		if strings.Contains(content, contains) {
			return rctx
		}
	}
	t.Fatalf("no send containing %q; sent: %v", contains, p.getSent())
	return nil
}

func TestCronStartNoticeIsMarkedAsSystemNotice_ResultIsNot(t *testing.T) {
	dir := t.TempDir()
	store, err := NewCronStore(dir)
	if err != nil {
		t.Fatalf("NewCronStore() error = %v", err)
	}
	p := &noticeRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("cron complete")}, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	e.cronScheduler = NewCronScheduler(store)

	job := &CronJob{ID: "job-1", SessionKey: "discord:channel-1", Prompt: "summarize", Description: "Daily summary"}
	if err := store.Add(job); err != nil {
		t.Fatalf("store.Add() error = %v", err)
	}
	if err := e.ExecuteCronJob(job); err != nil {
		t.Fatalf("ExecuteCronJob() error = %v", err)
	}
	if _, ok := p.ctxOf(t, "⏰ Daily summary").(noticeMarked); !ok {
		t.Fatalf("⏰ start notice sent with %#v, want a marked system notice", p.ctxOf(t, "⏰ Daily summary"))
	}
	if _, ok := p.ctxOf(t, "cron complete").(noticeMarked); ok {
		t.Fatal("the cron result is the agent's text and must not be marked as a notice")
	}
}

func TestCapacityCooldownNoticeIsMarkedAsSystemNotice(t *testing.T) {
	old := capacityCooldownBase
	capacityCooldownBase = 100 * time.Millisecond
	t.Cleanup(func() { capacityCooldownBase = old })

	sess := &capacityScriptSession{events: make(chan Event, 8), script: []string{"cap", "cap", "cap", "finally"}}
	p := &noticeRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	e.handleMessage(p, &Message{SessionKey: "test:c1", Platform: "test", UserID: "u", UserName: "u", Content: "hello", ReplyCtx: "ctx"})
	waitFor(t, "re-delivered message to succeed", func() bool { return strings.Contains(joined(&p.stubPlatformEngine), "reply:finally") })

	if _, ok := p.ctxOf(t, "模型满载").(noticeMarked); !ok {
		t.Fatalf("⏳ cooldown notice sent with %#v, want a marked system notice", p.ctxOf(t, "模型满载"))
	}
	if _, ok := p.ctxOf(t, "reply:finally").(noticeMarked); ok {
		t.Fatal("the reply must not be marked as a notice")
	}
}

func TestMarkSystemNoticePassesThroughWithoutMarker(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	if got := markSystemNotice(p, "ctx"); got != "ctx" {
		t.Fatalf("markSystemNotice on a platform without the marker = %#v, want the context unchanged", got)
	}
	m := &noticeRecordingPlatform{}
	if _, ok := markSystemNotice(m, "ctx").(noticeMarked); !ok {
		t.Fatal("markSystemNotice must use the platform's marker")
	}
}
