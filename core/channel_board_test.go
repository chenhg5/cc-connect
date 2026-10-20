package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// boardPlatform: a Discord card-style platform that also hosts the board.
type boardPlatform struct {
	stubCompactProgressPlatform
	interval time.Duration

	bmu       sync.Mutex
	nextID    int
	posts     []string            // board payloads posted (one per new message)
	postCh    []string            // channel of each post
	postID    []string            // message ID of each post
	edits     map[string][]string // message ID → payloads edited onto it
	pins      []string            // message IDs pin was attempted on
	pinErr    error
	gone      map[string]bool // message IDs that no longer exist
	failEdits int             // the next this many edits fail (network down)
	block     chan struct{}   // when set, Post/Edit wait for it (or ctx) first
	editTries int             // edit calls, failed ones included
	found     string          // FindChannelBoard result
}

func newBoardPlatform() *boardPlatform {
	return &boardPlatform{
		stubCompactProgressPlatform: stubCompactProgressPlatform{
			stubPlatformEngine: stubPlatformEngine{n: "discord"},
			style:              "card",
			supportPayload:     true,
		},
		interval: 20 * time.Millisecond,
		edits:    map[string][]string{},
		gone:     map[string]bool{},
	}
}

func (p *boardPlatform) ProgressUpdateInterval() time.Duration { return p.interval }

func (p *boardPlatform) wait(ctx context.Context) error {
	p.bmu.Lock()
	b := p.block
	p.bmu.Unlock()
	if b == nil {
		return nil
	}
	select {
	case <-b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *boardPlatform) PostChannelBoard(ctx context.Context, ch, content string) (string, error) {
	if err := p.wait(ctx); err != nil {
		return "", err
	}
	p.bmu.Lock()
	defer p.bmu.Unlock()
	p.nextID++
	id := fmt.Sprintf("%s-m%d", ch, p.nextID)
	p.posts = append(p.posts, content)
	p.postCh = append(p.postCh, ch)
	p.postID = append(p.postID, id)
	return id, nil
}

func (p *boardPlatform) EditChannelBoard(ctx context.Context, _, id, content string) error {
	if err := p.wait(ctx); err != nil {
		return err
	}
	p.bmu.Lock()
	defer p.bmu.Unlock()
	p.editTries++
	if p.gone[id] {
		return fmt.Errorf("%w: 10008", ErrChannelBoardGone)
	}
	if p.failEdits > 0 {
		p.failEdits--
		return errors.New("dial tcp: i/o timeout")
	}
	p.edits[id] = append(p.edits[id], content)
	return nil
}

func (p *boardPlatform) PinChannelBoard(_ context.Context, _, id string) error {
	p.bmu.Lock()
	defer p.bmu.Unlock()
	p.pins = append(p.pins, id)
	return p.pinErr
}

func (p *boardPlatform) FindChannelBoard(context.Context, string) (string, bool, error) {
	p.bmu.Lock()
	defer p.bmu.Unlock()
	return p.found, false, nil
}

func (p *boardPlatform) counts() (posts, edits, pins int) {
	p.bmu.Lock()
	defer p.bmu.Unlock()
	for _, e := range p.edits {
		edits += len(e)
	}
	return len(p.posts), edits, len(p.pins)
}

// latest is channel c1's newest board payload (last edit of its newest
// message, else the post).
func (p *boardPlatform) latest(t *testing.T) *ChannelBoardPayload {
	t.Helper()
	p.bmu.Lock()
	defer p.bmu.Unlock()
	raw, id := "", ""
	for i := range p.posts {
		if p.postCh[i] == "c1" {
			raw, id = p.posts[i], p.postID[i]
		}
	}
	if id == "" && len(p.edits) == 1 { // board posted before a restart
		for k := range p.edits {
			id = k
		}
	}
	if e := p.edits[id]; len(e) > 0 {
		raw = e[len(e)-1]
	}
	b, ok := ParseChannelBoardPayload(raw)
	if !ok {
		return nil
	}
	return b
}

func newBoardEngine(t *testing.T, h *channelBoardHub, name string, p Platform) *Engine {
	t.Helper()
	e := NewEngine(name, &stubAgent{}, []Platform{p}, "", LangChinese)
	defaultChannelBoard.removeSource(e)
	e.board = h
	h.addSource(e)
	t.Cleanup(func() { e.Stop() })
	return e
}

func waitBoard(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Four bots in one channel share one board message, posted and pinned once by
// the writer (橘), one row per bot.
func TestChannelBoard_FourBotsShareOneMessage(t *testing.T) {
	h := newChannelBoardHub()
	writerP := newBoardPlatform()
	maomao := newBoardEngine(t, h, "maomao", writerP)
	maomao.SetChannelBoard(true)
	var subs []*Engine
	for _, n := range []string{"longniang", "jingyu", "caihongmao"} {
		subs = append(subs, newBoardEngine(t, h, n, &stubPlatformEngine{n: "discord"}))
	}

	seq := map[string]uint64{}
	seq["maomao"] = h.beginTurn(maomao, "discord:c1", "整理任务看板")
	for i, e := range subs {
		seq[e.name] = h.beginTurn(e, "discord:c1", fmt.Sprintf("活 %d", i))
	}
	waitBoard(t, "board with 4 rows", func() bool {
		b := writerP.latest(t)
		return b != nil && len(b.Running) == 4
	})
	if posts, _, pins := writerP.counts(); posts != 1 || pins != 1 {
		t.Fatalf("posts=%d pins=%d, want one shared board pinned once", posts, pins)
	}
	bots := map[string]bool{}
	for _, r := range writerP.latest(t).Running {
		bots[r.Bot] = true
	}
	for _, n := range []string{"maomao", "longniang", "jingyu", "caihongmao"} {
		if !bots[n] {
			t.Fatalf("row for %s missing: %+v", n, writerP.latest(t).Running)
		}
	}

	h.endTurn(subs[0], "discord:c1", seq["longniang"], BoardDoneCompleted)
	waitBoard(t, "done row", func() bool {
		b := writerP.latest(t)
		return b != nil && len(b.Running) == 3 && len(b.Done) == 1 && b.Done[0].Bot == "longniang"
	})
	if posts, edits, _ := writerP.counts(); posts != 1 || edits == 0 {
		t.Fatalf("posts=%d edits=%d, want edits on the same message", posts, edits)
	}
	// A stale end (old seq) never ends the bot's newer turn.
	newSeq := h.beginTurn(subs[0], "discord:c1", "第二件")
	h.endTurn(subs[0], "discord:c1", seq["longniang"], BoardDoneFailed)
	h.mu.Lock()
	tr := h.turns["longniang\x00discord:c1"]
	h.mu.Unlock()
	if tr == nil || tr.seq != newSeq {
		t.Fatal("stale endTurn removed the newer turn")
	}
}

// Changes inside the merge window become one edit carrying the last state.
func TestChannelBoard_MergesChangesIntoOneEdit(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	p.interval = 150 * time.Millisecond
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)

	seq := h.beginTurn(e, "discord:c1", "跑测试")
	waitBoard(t, "posted", func() bool { n, _, _ := p.counts(); return n == 1 })
	for _, s := range []string{"go vet", "go test ./core/", "go test ./platform/discord/"} {
		h.stepTurn(e, "discord:c1", seq, ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: s})
		time.Sleep(10 * time.Millisecond)
	}
	if _, edits, _ := p.counts(); edits != 0 {
		t.Fatalf("edited %d times inside the window", edits)
	}
	waitBoard(t, "one merged edit", func() bool {
		_, edits, _ := p.counts()
		b := p.latest(t)
		return edits == 1 && b.Running[0].Step != nil && b.Running[0].Step.Text == "go test ./platform/discord/"
	})
	time.Sleep(3 * p.interval)
	if _, edits, _ := p.counts(); edits != 1 {
		t.Fatalf("edits = %d, want 1", edits)
	}
}

// After a restart the stored message ID is edited again (no new post); when that
// message is gone a new one is posted, pinned and stored.
func TestChannelBoard_RestartReusesMessageID(t *testing.T) {
	dir := t.TempDir()
	h1 := newChannelBoardHub()
	p1 := newBoardPlatform()
	e1 := newBoardEngine(t, h1, "maomao", p1)
	e1.SetDataDir(dir)
	e1.SetChannelBoard(true)
	seq := h1.beginTurn(e1, "discord:c1", "第一轮")
	waitBoard(t, "posted", func() bool { n, _, _ := p1.counts(); return n == 1 })
	h1.endTurn(e1, "discord:c1", seq, BoardDoneCompleted)
	waitBoard(t, "edited", func() bool { _, n, _ := p1.counts(); return n >= 1 })
	raw, err := os.ReadFile(filepath.Join(dir, channelBoardFile))
	if err != nil || !strings.Contains(string(raw), `"message_id": "c1-m1"`) {
		t.Fatalf("state file = %s, %v", raw, err)
	}

	// "Restart": a new hub and engine read the same data dir.
	h2 := newChannelBoardHub()
	p2 := newBoardPlatform()
	e2 := newBoardEngine(t, h2, "maomao", p2)
	e2.SetDataDir(dir)
	e2.SetChannelBoard(true)
	h2.beginTurn(e2, "discord:c1", "重启后")
	waitBoard(t, "edit of the old message", func() bool {
		p2.bmu.Lock()
		defer p2.bmu.Unlock()
		return len(p2.edits["c1-m1"]) == 1
	})
	if posts, _, pins := p2.counts(); posts != 0 || pins != 0 {
		t.Fatalf("posts=%d pins=%d after restart, want the old message reused", posts, pins)
	}
	if b := p2.latest(t); b != nil && len(b.Done) != 1 {
		t.Fatalf("done list not kept across restart: %+v", b.Done)
	}

	// The message was deleted: post a new one and pin it.
	p2.bmu.Lock()
	p2.gone["c1-m1"] = true
	p2.nextID = 10
	p2.bmu.Unlock()
	h2.mu.Lock()
	h2.channels["c1"].lastSent = "" // force a send even though nothing changed
	h2.mu.Unlock()
	h2.touch("c1")
	waitBoard(t, "new post after gone", func() bool { n, _, pins := p2.counts(); return n == 1 && pins == 1 })
	waitBoard(t, "new ID stored", func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, channelBoardFile))
		return strings.Contains(string(raw), `"message_id": "c1-m11"`)
	})
}

// No pin permission: the board is still posted and edited, only not pinned,
// and pin is not retried on every edit.
func TestChannelBoard_PinFailureDegrades(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	p.pinErr = errors.New("HTTP 403 Forbidden, Missing Permissions")
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	seq := h.beginTurn(e, "discord:c1", "活")
	waitBoard(t, "posted", func() bool { n, _, _ := p.counts(); return n == 1 })
	h.stepTurn(e, "discord:c1", seq, ProgressCardEntry{Kind: ProgressEntryInfo, Text: "一步"})
	waitBoard(t, "edited", func() bool { _, n, _ := p.counts(); return n == 1 })
	h.endTurn(e, "discord:c1", seq, BoardDoneCompleted)
	waitBoard(t, "edited again", func() bool { _, n, _ := p.counts(); return n == 2 })
	if _, _, pins := p.counts(); pins != 1 {
		t.Fatalf("pin attempts = %d, want 1", pins)
	}
	h.mu.Lock()
	pinned := h.channels["c1"].Pinned
	h.mu.Unlock()
	if pinned {
		t.Fatal("marked pinned after a failed pin")
	}
}

// The real event loop: the turn's text, the progress card's step, and the end
// (completed) reach the board; a queued message shows under "排着队的".
func TestChannelBoard_EventLoopFeedsBoard(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	sessionKey := "discord:c1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s1")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-1", turnDesc: "帮我看下任务看板"}
	state.pendingMessages = []queuedMessage{{messageID: "q1", boardDesc: "排队的那条"}}
	e.interactiveStates[sessionKey] = state

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)
		close(done)
	}()
	sess.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "go test ./core/"}
	waitBoard(t, "running row with step", func() bool {
		b := p.latest(t)
		return b != nil && len(b.Running) == 1 && b.Running[0].Desc == "帮我看下任务看板" && b.Running[0].Step != nil &&
			len(b.Queued) == 1 && b.Queued[0].Desc == "排队的那条"
	})
	state.mu.Lock()
	state.pendingMessages = nil // not chained in this test
	state.mu.Unlock()
	sess.events <- Event{Type: EventResult, Content: "好了", Done: true}
	<-done
	waitBoard(t, "done row", func() bool {
		b := p.latest(t)
		return b != nil && len(b.Running) == 0 && len(b.Done) == 1 && b.Done[0].Outcome == BoardDoneCompleted && b.Done[0].Desc == "帮我看下任务看板"
	})
}

// Other platforms are not tracked; yesterday's finished turns are not shown.
func TestChannelBoard_OnlyDiscordAndToday(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	if seq := h.beginTurn(e, "telegram:123:456", "x"); seq != 0 {
		t.Fatal("telegram turn tracked")
	}
	if got := boardChannelOf("/ws/proj:discord:c9:u1"); got != "c9" {
		t.Fatalf("workspace key channel = %q", got)
	}
	if got := boardChannelOf("discord:c9#cron:abc"); got != "c9" {
		t.Fatalf("cron key channel = %q", got)
	}
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
	h.mu.Lock()
	h.now = func() time.Time { return now }
	h.addDoneLocked("c1", BoardDone{Bot: "maomao", Desc: "昨天的", EndedAt: now.Add(-24 * time.Hour), Outcome: BoardDoneCompleted})
	h.addDoneLocked("c1", BoardDone{Bot: "maomao", Desc: "今天的", EndedAt: now.Add(-time.Hour), Outcome: BoardDoneFailed})
	h.mu.Unlock()
	b := h.render("c1")
	if len(b.Done) != 1 || b.Done[0].Desc != "今天的" {
		t.Fatalf("done = %+v, want only today's", b.Done)
	}
	for i := 0; i < channelBoardDoneMax+3; i++ {
		h.mu.Lock()
		h.addDoneLocked("c1", BoardDone{Bot: "maomao", EndedAt: now})
		h.mu.Unlock()
	}
	if n := len(h.render("c1").Done); n != channelBoardDoneMax {
		t.Fatalf("done rows = %d, want %d", n, channelBoardDoneMax)
	}
	if posts, _, _ := p.counts(); posts != 0 {
		t.Fatalf("posted %d boards for nothing", posts)
	}
}

// Cron jobs of the channel are listed under "排着队的" with their next run.
func TestChannelBoard_ListsCronAndTimer(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	cstore, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cs := NewCronScheduler(cstore)
	cs.RegisterEngine("maomao", e)
	e.SetCronScheduler(cs)
	if err := cs.Start(); err != nil {
		t.Fatal(err)
	}
	defer cs.Stop()
	if err := cs.AddJob(&CronJob{ID: "j1", Project: "maomao", SessionKey: "discord:c1", CronExpr: "0 9 * * *", Prompt: "早报", Description: "每天早报", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := cs.AddJob(&CronJob{ID: "j2", Project: "maomao", SessionKey: "discord:other", CronExpr: "0 9 * * *", Prompt: "别处", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tstore, err := NewTimerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ts := NewTimerScheduler(tstore)
	ts.RegisterEngine("maomao", e)
	e.SetTimerScheduler(ts)
	defer ts.Stop()
	at := time.Now().Add(time.Hour)
	if err := ts.AddJob(&TimerJob{ID: "t1", Project: "maomao", SessionKey: "discord:c1", ScheduledAt: at, Prompt: "提醒", Description: "一小时后提醒"}); err != nil {
		t.Fatal(err)
	}
	// Adding the jobs alone refreshes the board.
	defer func() { n, m, _ := p.counts(); t.Logf("posts=%d edits=%d render=%+v", n, m, h.render("c1")) }()
	waitBoard(t, "board with jobs", func() bool { b := p.latest(t); return b != nil && len(b.Queued) == 2 })
	b := p.latest(t)
	kinds := map[string]BoardQueued{}
	for _, q := range b.Queued {
		kinds[q.Kind] = q
	}
	if c := kinds["cron"]; c.Desc != "每天早报" || c.At.IsZero() {
		t.Fatalf("cron row = %+v", c)
	}
	if tm := kinds["timer"]; tm.Desc != "一小时后提醒" || !tm.At.Equal(at) {
		t.Fatalf("timer row = %+v", tm)
	}
}

// Engine stop: running turns end as stopped in one last edit.
func TestChannelBoard_ShutdownMarksStopped(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	h.beginTurn(e, "discord:c1", "长活")
	waitBoard(t, "posted", func() bool { n, _, _ := p.counts(); return n == 1 })
	e.Stop()
	b := p.latest(t)
	if b == nil || len(b.Running) != 0 || len(b.Done) != 1 || b.Done[0].Outcome != BoardDoneStopped {
		t.Fatalf("board after stop = %+v", b)
	}
}

// A turn that hits "model at capacity" and gets through on a retry is one row
// on the board, finished as completed — no failed row for the retried attempts.
func TestChannelBoard_CapacityRetryKeepsOneRow(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	sess := &capacityScriptSession{events: make(chan Event, 8), script: []string{"cap", "cap", "third"}}
	e := NewEngine("maomao", &resultAgent{session: sess}, []Platform{p}, "", LangChinese)
	defaultChannelBoard.removeSource(e)
	e.board = h
	h.addSource(e)
	t.Cleanup(func() { e.Stop() })
	e.SetChannelBoard(true)

	e.handleMessage(p, &Message{SessionKey: "discord:c1", Platform: "discord", UserID: "u", UserName: "u", Content: "整理看板", ReplyCtx: "ctx"})
	waitBoard(t, "done row", func() bool {
		b := p.latest(t)
		return b != nil && len(b.Running) == 0 && len(b.Done) == 1
	})
	if n := sess.sends(); n != 3 {
		t.Fatalf("sends = %d, want 3", n)
	}
	b := p.latest(t)
	if d := b.Done[0]; d.Outcome != BoardDoneCompleted || d.Desc != "整理看板" {
		t.Fatalf("done row = %+v, want one completed row", d)
	}
	time.Sleep(3 * p.interval)
	if b := p.latest(t); len(b.Done) != 1 {
		t.Fatalf("done rows = %+v, want 1", b.Done)
	}
}

// A cron/timer side session that hits capacity is not re-sent: its held row
// ends as failed.
func TestChannelBoard_CapacityHeldRowEndsWhenNotResent(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	key := "discord:c1#cron:x"
	seq := h.beginTurn(e, key, "⏰ 早报")
	(&boardTurnRef{h: h, e: e, key: key, seq: seq}).hold()
	e.deferForCapacity(p, Message{ReplyCtx: "ctx"}, nil, e.sessions, key, "", time.Minute, false)
	waitBoard(t, "failed row", func() bool {
		b := p.latest(t)
		return b != nil && len(b.Running) == 0 && len(b.Done) == 1 && b.Done[0].Outcome == BoardDoneFailed
	})
}

// A sub bot's engine stopping first: its running turn ends as stopped, not lost.
func TestChannelBoard_SubEngineStopMarksStopped(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	w := newBoardEngine(t, h, "maomao", p)
	w.SetChannelBoard(true)
	sub := newBoardEngine(t, h, "longniang", &stubPlatformEngine{n: "discord"})
	h.beginTurn(sub, "discord:c1", "写代码")
	waitBoard(t, "posted", func() bool { n, _, _ := p.counts(); return n == 1 })
	sub.Stop()
	waitBoard(t, "stopped row", func() bool {
		b := p.latest(t)
		return b != nil && len(b.Running) == 0 && len(b.Done) == 1 && b.Done[0].Outcome == BoardDoneStopped && b.Done[0].Bot == "longniang"
	})
}

// The last change's edit fails and nothing else happens in the channel: the
// worker retries (with backoff) until the board shows the last state.
func TestChannelBoard_FailedEditIsRetried(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	seq := h.beginTurn(e, "discord:c1", "活")
	waitBoard(t, "posted", func() bool { n, _, _ := p.counts(); return n == 1 })
	p.bmu.Lock()
	p.failEdits = 2
	p.bmu.Unlock()
	h.endTurn(e, "discord:c1", seq, BoardDoneCompleted) // the last change
	waitBoard(t, "retried edit lands", func() bool {
		b := p.latest(t)
		return b != nil && len(b.Running) == 0 && len(b.Done) == 1
	})
	p.bmu.Lock()
	tries := p.editTries
	p.bmu.Unlock()
	if tries != 3 {
		t.Fatalf("edit tries = %d, want 2 failed + 1 ok", tries)
	}
	h.mu.Lock()
	backoff := h.channels["c1"].backoff
	h.mu.Unlock()
	if backoff != 0 {
		t.Fatalf("backoff not reset after success: %v", backoff)
	}
}

// A held row (capacity cooldown) whose message waits in the queue is listed
// once, under "排着队的"; when the queue is dropped it ends as failed.
func TestChannelBoard_HeldRowQueuedThenDropped(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	key := "discord:c1"
	state := &interactiveState{platform: p, turnDesc: "整理看板"}
	e.interactiveStates[key] = state
	ref := e.boardBegin(state, key)
	ref.hold()
	state.mu.Lock()
	state.pendingMessages = []queuedMessage{{messageID: "m1", boardDesc: "整理看板", platform: p}}
	state.mu.Unlock()
	b := h.render("c1")
	if len(b.Running) != 0 || len(b.Queued) != 1 || b.Queued[0].Desc != "整理看板" {
		t.Fatalf("held+queued shown twice or not at all: running=%+v queued=%+v", b.Running, b.Queued)
	}
	e.notifyDroppedQueuedMessages(state, errors.New("agent session ended"))
	waitBoard(t, "failed row", func() bool {
		b := p.latest(t)
		return b != nil && len(b.Running) == 0 && len(b.Queued) == 0 && len(b.Done) == 1 && b.Done[0].Outcome == BoardDoneFailed
	})

	// Stale drop (drain paths call boardDropped): same end.
	ref2 := e.boardBegin(state, key)
	ref2.hold()
	e.boardDropped(key, []string{"别的", "整理看板"})
	waitBoard(t, "second failed row", func() bool { b := p.latest(t); return b != nil && len(b.Done) == 2 && len(b.Running) == 0 })
	// A drop of other messages leaves a held row alone.
	ref3 := e.boardBegin(state, key)
	ref3.hold()
	e.boardDropped(key, []string{"别的"})
	h.mu.Lock()
	tr := h.turns["maomao\x00"+key]
	h.mu.Unlock()
	if tr == nil || !tr.held {
		t.Fatal("unrelated drop ended the held row")
	}
}

// Shutdown with Discord unreachable: all channels share one deadline (in
// parallel), instead of one timeout per channel.
func TestChannelBoard_ShutdownSharesOneDeadline(t *testing.T) {
	h := newChannelBoardHub()
	h.shutdownTimeout = 300 * time.Millisecond
	p := newBoardPlatform()
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	for _, ch := range []string{"c1", "c2", "c3", "c4"} {
		h.beginTurn(e, "discord:"+ch, "活 "+ch)
	}
	waitBoard(t, "4 boards", func() bool { n, _, _ := p.counts(); return n == 4 })
	p.bmu.Lock()
	p.block = make(chan struct{}) // Discord down: calls hang until their ctx ends
	p.bmu.Unlock()
	start := time.Now()
	h.shutdown(e)
	if d := time.Since(start); d > 2*h.shutdownTimeout {
		t.Fatalf("shutdown took %v for 4 channels, want about one deadline (%v)", d, h.shutdownTimeout)
	}
}

// Shutdown while a worker is posting: it waits for that post, then makes its
// last edit on the same message (no second board).
func TestChannelBoard_ShutdownWaitsForWorker(t *testing.T) {
	h := newChannelBoardHub()
	p := newBoardPlatform()
	p.block = make(chan struct{})
	e := newBoardEngine(t, h, "maomao", p)
	e.SetChannelBoard(true)
	h.beginTurn(e, "discord:c1", "长活")
	time.Sleep(5 * p.interval) // the worker is now inside PostChannelBoard
	done := make(chan struct{})
	go func() { h.shutdown(e); close(done) }()
	time.Sleep(5 * p.interval)
	select {
	case <-done:
		t.Fatal("shutdown did not wait for the worker's post")
	default:
	}
	close(p.block)
	<-done
	posts, edits, _ := p.counts()
	if posts != 1 || edits != 1 {
		t.Fatalf("posts=%d edits=%d, want one board and its last edit", posts, edits)
	}
	if b := p.latest(t); b == nil || len(b.Done) != 1 || b.Done[0].Outcome != BoardDoneStopped {
		t.Fatalf("last edit = %+v", b)
	}
}
