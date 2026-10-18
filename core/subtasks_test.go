package core

import (
	"context"
	"fmt"
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
	prev    []SubTask
	changed chan struct{}
}

func newSubTaskSession() *subTaskSession {
	return &subTaskSession{
		controllableAgentSession: newControllableSession("s1"),
		changed:                  make(chan struct{}, 1),
	}
}

func (s *subTaskSession) SubTasks() (uint64, []SubTask, []SubTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen, append([]SubTask(nil), s.tasks...), append([]SubTask(nil), s.prev...)
}

// setBatch starts generation gen with tasks, keeping prev as the final state
// of the batch before it.
func (s *subTaskSession) setBatch(gen uint64, prev []SubTask, tasks ...SubTask) {
	s.mu.Lock()
	s.prev = prev
	s.mu.Unlock()
	s.set(gen, tasks...)
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

	hmu     sync.Mutex
	handles int
	byHand  map[string][]string // handle -> table payloads sent/edited on it
}

// SendPreviewStart/UpdateMessage also record which message each table payload
// went to, so tests can tell the old table from the new one.
func (p *subTablePlatform) SendPreviewStart(ctx context.Context, rc any, content string) (any, error) {
	_, _ = p.sizedCardPlatform.SendPreviewStart(ctx, rc, content)
	p.hmu.Lock()
	defer p.hmu.Unlock()
	p.handles++
	h := fmt.Sprintf("h%d", p.handles)
	if p.byHand == nil {
		p.byHand = map[string][]string{}
	}
	p.byHand[h] = append(p.byHand[h], content)
	return h, nil
}

func (p *subTablePlatform) UpdateMessage(ctx context.Context, h any, content string) error {
	_ = p.sizedCardPlatform.UpdateMessage(ctx, h, content)
	p.hmu.Lock()
	defer p.hmu.Unlock()
	k, _ := h.(string)
	p.byHand[k] = append(p.byHand[k], content)
	return nil
}

// lastOn returns the newest table payload sent to handle h.
func (p *subTablePlatform) lastOn(h string) *SubTaskTablePayload {
	p.hmu.Lock()
	defer p.hmu.Unlock()
	msgs := onlyPrefix(p.byHand[h], SubTaskTablePayloadPrefix)
	if len(msgs) == 0 {
		return nil
	}
	tab, _ := ParseSubTaskTablePayload(msgs[len(msgs)-1])
	return tab
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

// Review fix 1: the old batch's "completed" edit is throttled when the next
// batch starts right away; the old message must still get it.
func TestSubTaskTable_PreviousBatchGetsFinalEdit(t *testing.T) {
	p := newSubTablePlatform()
	p.interval = 300 * time.Millisecond
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	defer e.Stop()
	sess := newSubTaskSession()
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-1"}
	e.ensureSubTaskTable(state)

	one := SubTask{ID: "a1", Description: "第一批", Status: SubTaskRunning, StartedAt: time.Now()}
	sess.set(1, one)
	waitSub(t, "first table", func() bool { return p.lastOn("h1") != nil })

	done := one
	done.Status, done.EndedAt, done.Summary = SubTaskCompleted, time.Now(), "做完了。"
	sess.set(1, done) // throttled: inside the interval
	time.Sleep(10 * time.Millisecond)
	sess.setBatch(2, []SubTask{done}, SubTask{ID: "a2", Description: "第二批", Status: SubTaskRunning, StartedAt: time.Now()})

	waitSub(t, "second table", func() bool { return p.lastOn("h2") != nil })
	waitSub(t, "old table's final edit", func() bool {
		tab := p.lastOn("h1")
		return tab != nil && tab.Tasks[0].Status == SubTaskCompleted
	})
	if tab := p.lastOn("h2"); tab.Tasks[0].ID != "a2" {
		t.Fatalf("new table = %+v", tab)
	}
}

// Review fix 4: on engine stop (restart) the table must not stay at 🔄: one
// last edit marks rows still running as stopped.
func TestSubTaskTable_StopMarksRunningRowsStopped(t *testing.T) {
	p := newSubTablePlatform()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	sess := newSubTaskSession()
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-1"}
	e.ensureSubTaskTable(state)
	sess.set(1, SubTask{ID: "a1", Description: "后台", Background: true, Status: SubTaskRunning, StartedAt: time.Now()})
	waitSub(t, "table", func() bool { return p.lastOn("h1") != nil })
	time.Sleep(2 * p.interval) // the stop edit is not held back by the throttle in this test

	e.Stop()
	waitSub(t, "stopped edit", func() bool {
		tab := p.lastOn("h1")
		return tab != nil && tab.Tasks[0].Status == SubTaskStopped && !tab.Tasks[0].EndedAt.IsZero()
	})
}

func countWaitLines(c string) int {
	pl, ok := ParseProgressCardPayload(c)
	if !ok {
		return -1
	}
	n := 0
	for _, it := range pl.Items {
		if strings.HasPrefix(it.Text, "等子 agent") {
			n++
		}
	}
	return n
}

func lastCard(p *subTablePlatform) string {
	all := append(onlyPrefix(p.getPreviewStarts(), ProgressCardPayloadPrefix), onlyPrefix(p.getPreviewEdits(), ProgressCardPayloadPrefix)...)
	edits := onlyPrefix(p.getPreviewEdits(), ProgressCardPayloadPrefix)
	if len(edits) > 0 {
		return edits[len(edits)-1]
	}
	if len(all) > 0 {
		return all[len(all)-1]
	}
	return ""
}

// Review fix 2: another step between refreshes must not leave a second wait
// line, and the line goes away once the foreground sub-agent has finished.
func TestProgressCard_WaitLineSingleAndRemovedAfterSubAgent(t *testing.T) {
	p := newSubTablePlatform()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	defer e.Stop()
	sessionKey := "discord:c1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newSubTaskSession()
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-1"}
	e.interactiveStates[sessionKey] = state

	fg := SubTask{ID: "a1", Description: "前台", Status: SubTaskRunning, StartedAt: time.Now(), Step: "s1"}
	sess.set(1, fg)
	sess.events <- Event{Type: EventToolUse, ToolName: "Agent", ToolInput: "前台"}
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)
		close(done)
	}()
	waitSub(t, "wait line", func() bool { return countWaitLines(lastCard(p)) == 1 })

	sess.events <- Event{Type: EventToolUse, ToolName: "Read", ToolInput: "x.go"}
	fg.Step = "s2"
	sess.set(1, fg)
	waitSub(t, "refreshed wait line", func() bool { return strings.Contains(lastCard(p), "前台 — s2") })
	if n := countWaitLines(lastCard(p)); n != 1 {
		t.Fatalf("wait lines = %d, want 1: %s", n, lastCard(p))
	}

	fg.Status, fg.EndedAt = SubTaskCompleted, time.Now()
	sess.set(1, fg)
	waitSub(t, "wait line removed", func() bool { return countWaitLines(lastCard(p)) == 0 })

	sess.events <- Event{Type: EventResult, Content: "ok", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not end")
	}
}
