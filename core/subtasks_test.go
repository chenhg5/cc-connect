package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// subTaskSession is a controllable session that also reports sub-agents.
type subTaskSession struct {
	*controllableAgentSession
	mu      sync.Mutex
	gen     uint64
	tasks   []SubTask
	changed chan struct{}
}

func newSubTaskSession() *subTaskSession {
	return &subTaskSession{
		controllableAgentSession: newControllableSession("s1"),
		changed:                  make(chan struct{}, 1),
	}
}

func (s *subTaskSession) SubTasks() (uint64, []SubTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen, append([]SubTask(nil), s.tasks...)
}

func (s *subTaskSession) SubTasksChanged() <-chan struct{} { return s.changed }

func (s *subTaskSession) set(gen uint64, tasks ...SubTask) {
	s.mu.Lock()
	s.gen, s.tasks = gen, tasks
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// subTablePlatform: card-style progress with payloads, a short edit interval,
// and the sub-agent table enabled.
type subTablePlatform struct {
	sizedCardPlatform
	interval time.Duration
}

func (p *subTablePlatform) ProgressUpdateInterval() time.Duration { return p.interval }
func (p *subTablePlatform) SupportsSubTaskTablePayload() bool     { return true }

func newSubTablePlatform() *subTablePlatform {
	return &subTablePlatform{
		sizedCardPlatform: sizedCardPlatform{stubCompactProgressPlatform: stubCompactProgressPlatform{
			stubPlatformEngine: stubPlatformEngine{n: "discord"},
			style:              "card",
			supportPayload:     true,
		}},
		interval: 20 * time.Millisecond,
	}
}

func onlyPrefix(in []string, prefix string) []string {
	var out []string
	for _, s := range in {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

func waitSub(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func lastTable(t *testing.T, p *subTablePlatform) *SubTaskTablePayload {
	t.Helper()
	all := append(p.getPreviewStarts(), p.getPreviewEdits()...)
	// Edits come after the start; the newest is the last edit if any.
	edits := onlyPrefix(p.getPreviewEdits(), SubTaskTablePayloadPrefix)
	starts := onlyPrefix(p.getPreviewStarts(), SubTaskTablePayloadPrefix)
	var raw string
	switch {
	case len(edits) > 0:
		raw = edits[len(edits)-1]
	case len(starts) > 0:
		raw = starts[len(starts)-1]
	default:
		t.Fatalf("no table message among %d previews", len(all))
	}
	tab, ok := ParseSubTaskTablePayload(raw)
	if !ok {
		t.Fatalf("bad table payload %q", raw)
	}
	return tab
}

// No turn is running here (processInteractiveEvents never called): a
// background sub-agent's progress after the main turn still edits the table.
func TestSubTaskTable_UpdatesOutsideTurn(t *testing.T) {
	p := newSubTablePlatform()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	defer e.Stop()
	sess := newSubTaskSession()
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-1"}
	e.ensureSubTaskTable(state)
	e.ensureSubTaskTable(state) // idempotent: one updater per agent session

	start := time.Now()
	bg := SubTask{ID: "b1", Description: "后台跑测试", Background: true, Status: SubTaskRunning, StartedAt: start, Step: "Running go test"}
	sess.set(1, bg)
	waitSub(t, "table sent", func() bool { return len(onlyPrefix(p.getPreviewStarts(), SubTaskTablePayloadPrefix)) == 1 })

	bg.Step = "Running go vet"
	sess.set(1, bg)
	waitSub(t, "step edit", func() bool { return lastTable(t, p).Tasks[0].Step == "Running go vet" })

	bg.Status, bg.EndedAt, bg.Summary = SubTaskCompleted, start.Add(11*time.Second), "测试全过。"
	sess.set(1, bg)
	waitSub(t, "completed edit", func() bool { return lastTable(t, p).Tasks[0].Status == SubTaskCompleted })
	if got := lastTable(t, p).Tasks[0].Summary; got != "测试全过。" {
		t.Fatalf("summary = %q", got)
	}
	if n := len(onlyPrefix(p.getPreviewStarts(), SubTaskTablePayloadPrefix)); n != 1 {
		t.Fatalf("table messages = %d, want 1 (edited in place)", n)
	}

	// A new batch (new generation) posts a fresh table message.
	sess.set(2, SubTask{ID: "a2", Description: "新一批", Status: SubTaskRunning, StartedAt: time.Now()})
	waitSub(t, "second table", func() bool { return len(onlyPrefix(p.getPreviewStarts(), SubTaskTablePayloadPrefix)) == 2 })
}

// Rapid changes inside the edit interval are not lost: the last one is
// flushed once the interval is up.
func TestSubTaskTable_ThrottleKeepsLastChange(t *testing.T) {
	p := newSubTablePlatform()
	p.interval = 200 * time.Millisecond
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	defer e.Stop()
	sess := newSubTaskSession()
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-1"}
	e.ensureSubTaskTable(state)

	row := SubTask{ID: "a1", Description: "d", Status: SubTaskRunning, StartedAt: time.Now()}
	sess.set(1, row)
	waitSub(t, "table sent", func() bool { return len(onlyPrefix(p.getPreviewStarts(), SubTaskTablePayloadPrefix)) == 1 })
	for _, s := range []string{"s1", "s2", "s3"} {
		row.Step = s
		sess.set(1, row)
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(onlyPrefix(p.getPreviewEdits(), SubTaskTablePayloadPrefix)); n != 0 {
		t.Fatalf("edited %d times inside the interval", n)
	}
	waitSub(t, "flushed last step", func() bool {
		return len(onlyPrefix(p.getPreviewEdits(), SubTaskTablePayloadPrefix)) == 1 && lastTable(t, p).Tasks[0].Step == "s3"
	})
}

// While the turn waits for a foreground sub-agent the progress card shows
// "等子 agent：<描述> — <当前那一步>", follows the step, and its timer runs.
func TestProgressCard_ShowsForegroundSubAgentWait(t *testing.T) {
	p := newSubTablePlatform()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	defer e.Stop()
	sessionKey := "discord:c1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newSubTaskSession()
	sess.events = make(chan Event, 8)
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-1"}
	e.interactiveStates[sessionKey] = state

	fg := SubTask{ID: "a1", Description: "前台跑两个 sleep 命令", Status: SubTaskRunning, StartedAt: time.Now(), Step: "Running sleep 3; echo a"}
	sess.set(1, fg)
	sess.events <- Event{Type: EventToolUse, ToolName: "Agent", ToolInput: "前台跑两个 sleep 命令"}
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)
		close(done)
	}()

	cardWith := func(sub string) func() bool {
		return func() bool {
			for _, c := range onlyPrefix(p.getPreviewEdits(), ProgressCardPayloadPrefix) {
				if pl, ok := ParseProgressCardPayload(c); ok && strings.Contains(pl.Items[len(pl.Items)-1].Text, sub) {
					return true
				}
			}
			return false
		}
	}
	defer func() {
		if t.Failed() {
			t.Logf("starts=%q edits=%q", p.getPreviewStarts(), p.getPreviewEdits())
		}
	}()
	waitSub(t, "wait line", cardWith("等子 agent：前台跑两个 sleep 命令 — Running sleep 3; echo a"))

	fg.Step = "Running sleep 3; echo b"
	sess.set(1, fg)
	waitSub(t, "wait line follows the step", cardWith("— Running sleep 3; echo b"))

	// Replaced in place, not appended: one wait line, right after the Agent step.
	edits := onlyPrefix(p.getPreviewEdits(), ProgressCardPayloadPrefix)
	last, _ := ParseProgressCardPayload(edits[len(edits)-1])
	waits := 0
	for _, it := range last.Items {
		if strings.HasPrefix(it.Text, "等子 agent") {
			waits++
		}
	}
	if waits != 1 || last.Items[0].Kind != ProgressEntryToolUse {
		t.Fatalf("items = %+v, want the Agent step then one wait line", last.Items)
	}
	// The elapsed time keeps moving while nothing but the sub-agent runs.
	first := last.ElapsedSec
	waitSub(t, "timer moves", func() bool {
		e := onlyPrefix(p.getPreviewEdits(), ProgressCardPayloadPrefix)
		pl, _ := ParseProgressCardPayload(e[len(e)-1])
		return pl.ElapsedSec > first
	})

	fg.Status, fg.EndedAt = SubTaskCompleted, time.Now()
	sess.set(1, fg)
	sess.events <- Event{Type: EventResult, Content: "ok", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not end")
	}
}
