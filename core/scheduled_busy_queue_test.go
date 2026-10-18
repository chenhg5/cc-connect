package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// A cron/timer job that fires while its channel's turn is running must wait in the queue and
// run when that turn ends, with its reply sent to the channel (2026-10-19: e10bb278 was dropped
// as "session is busy" and nothing showed in the channel).

func busySessionWithLiveState(t *testing.T, e *Engine, p Platform, key string, as AgentSession) (*Session, *interactiveState) {
	t.Helper()
	session := e.sessions.GetOrCreateActive(key)
	if !session.TryLock() {
		t.Fatal("could not lock session to simulate a running turn")
	}
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "user-turn-ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	return session, state
}

func assertQueuedThenDelivered(t *testing.T, e *Engine, p *stubCronReplyTargetPlatform, session *Session, state *interactiveState, key, prompt, result string) {
	t.Helper()
	state.mu.Lock()
	queued := len(state.pendingMessages)
	var content string
	if queued == 1 {
		content = state.pendingMessages[0].content
	}
	state.mu.Unlock()
	if queued != 1 || !strings.Contains(content, prompt) {
		t.Fatalf("pending = %d (content %q), want the job's prompt queued once", queued, content)
	}
	for _, s := range p.getSent() {
		if s == result {
			t.Fatalf("result sent before the running turn ended: %#v", p.getSent())
		}
	}

	// The running turn ends: its drain loop takes the queued job.
	if !e.drainPendingMessages(state, session, e.sessions, key) {
		t.Fatal("drainPendingMessages did not unlock the session")
	}
	found := false
	for _, s := range p.getSent() {
		if s == result {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent = %#v, want the job's result %q in the channel", p.getSent(), result)
	}
	if session.Busy() {
		t.Fatal("session still busy after the queued job ran")
	}
}

func TestExecuteTimerJob_BusySessionQueuesAndDeliversAfterTurn(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	as := newResultAgentSession("timer done")
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	session, state := busySessionWithLiveState(t, e, p, key, as)

	job := &TimerJob{ID: "t1", SessionKey: key, Prompt: "check the build", Description: "build check"}
	if err := e.ExecuteTimerJob(job); err != nil {
		t.Fatalf("ExecuteTimerJob() on a busy session = %v, want nil (queued)", err)
	}
	assertQueuedThenDelivered(t, e, p, session, state, key, "check the build", "timer done")
	if len(as.sentPrompts) != 1 || !strings.Contains(as.sentPrompts[0], "check the build") {
		t.Fatalf("agent prompts = %#v, want the timer prompt", as.sentPrompts)
	}
}

func TestExecuteCronJob_BusySessionQueuesAndDeliversAfterTurn(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	as := newResultAgentSession("cron done")
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	session, state := busySessionWithLiveState(t, e, p, key, as)

	job := &CronJob{ID: "c1", SessionKey: key, Prompt: "daily digest", Description: "digest"}
	if err := e.ExecuteCronJob(job); err != nil {
		t.Fatalf("ExecuteCronJob() on a busy session = %v, want nil (queued, no empty-response error)", err)
	}
	assertQueuedThenDelivered(t, e, p, session, state, key, "daily digest", "cron done")
}

// The race guard: if the running turn finished between the failed TryLock and the queue append,
// the job must still run (drainOrphanedQueue), not sit in the queue.
func TestQueueScheduledTurn_TurnEndedMeanwhile_DrainsOrphan(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	as := newResultAgentSession("orphan done")
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	session, _ := busySessionWithLiveState(t, e, p, key, as)
	session.Unlock() // the running turn has just ended

	msg := &Message{SessionKey: key, Platform: "discord", UserID: "timer", UserName: "timer", Content: "late job", ReplyCtx: "rctx"}
	if got := e.queueScheduledTurn(p, p, msg, session, e.sessions, key, e.agent, ""); got != queueAccepted {
		t.Fatalf("queueScheduledTurn() = %v, want queueAccepted", got)
	}
	waitFor(t, "orphaned queued job delivered", func() bool {
		for _, s := range p.getSent() {
			if s == "orphan done" {
				return true
			}
		}
		return false
	})
}

type modeRecordingResultSession struct {
	*resultAgentSession
	modes []string
}

func (s *modeRecordingResultSession) SetLiveMode(m string) bool {
	s.modes = append(s.modes, m)
	return true
}

// A queued cron turn gets, when it starts (not when it is queued), what a direct one gets: the
// channel posts it has not seen with the "（定时任务，回这条）" marker, the team status note, and
// the job's permission mode (restored afterwards).
func TestExecuteCronJob_BusyQueued_BackfillTeamNoteAndModeAtStart(t *testing.T) {
	now := time.Now()
	p := &cronHistoryPlatform{historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
		{ID: "101", Author: "zuiho", Content: "排队前的新消息", Time: now},
	}}}
	as := &modeRecordingResultSession{resultAgentSession: newResultAgentSession("cron done")}
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	e.SetTeam(t.TempDir(), "maomao", false)

	key := "discord:c1"
	session, state := busySessionWithLiveState(t, e, p, key, as)
	session.SetLastSeenMsgID("100")

	job := &CronJob{ID: "c1", SessionKey: key, Prompt: "回家", Mode: "bypassPermissions"}
	if err := e.ExecuteCronJob(job); err != nil {
		t.Fatalf("ExecuteCronJob() = %v, want nil (queued)", err)
	}
	if len(as.modes) != 0 {
		t.Fatalf("mode switched at queue time: %v", as.modes)
	}
	// Posted while the job waited: the backfill runs when the turn starts, so it sees this too.
	p.history = append(p.history, ChannelHistoryMessage{ID: "102", Author: "龙娘", IsBot: true, Content: "排队期间的消息", Time: now})

	if !e.drainPendingMessages(state, session, e.sessions, key) {
		t.Fatal("drainPendingMessages did not unlock the session")
	}
	if len(as.sentPrompts) != 1 {
		t.Fatalf("agent prompts = %#v, want 1", as.sentPrompts)
	}
	prompt := as.sentPrompts[0]
	for _, want := range []string{"排队前的新消息", "排队期间的消息", "（定时任务，回这条）", "回家"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("queued cron prompt lacks %q:\n%s", want, prompt)
		}
	}
	if session.GetLastSeenMsgID() != "102" {
		t.Fatalf("last seen = %q, want 102", session.GetLastSeenMsgID())
	}
	if strings.Join(as.modes, ",") != "bypassPermissions,default" {
		t.Fatalf("live modes = %v, want job mode then default", as.modes)
	}
	e.team.mu.Lock()
	ts := e.team.seen[key]
	e.team.mu.Unlock()
	if ts == nil || !strings.Contains(ts.task, "回家") {
		t.Fatalf("team note = %#v, want the cron task", ts)
	}
}

type modeRecordingQueuingSession struct {
	*queuingAgentSession
	mu    sync.Mutex
	modes []string
}

func (s *modeRecordingQueuingSession) SetLiveMode(m string) bool {
	s.mu.Lock()
	s.modes = append(s.modes, m)
	s.mu.Unlock()
	return true
}

// Same, for a scheduled turn chained into the running turn's event loop.
func TestProcessInteractiveEvents_ChainedScheduledTurn_BackfillAndMode(t *testing.T) {
	now := time.Now()
	p := &cronHistoryPlatform{historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
		{ID: "101", Author: "zuiho", Content: "前一轮期间的消息", Time: now},
	}}}
	sess := &modeRecordingQueuingSession{queuingAgentSession: newQueuingSession("qs-sched")}
	e := NewEngine("test", &controllableAgent{nextSession: sess.queuingAgentSession}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:c1"
	session := e.sessions.GetOrCreateActive(key)
	session.SetLastSeenMsgID("100")
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx-turn1",
		pendingMessages: []queuedMessage{{
			platform: p, replyCtx: "ctx-timer", content: "到点检查", userID: "timer", userName: "timer",
			msgPlatform: "discord", msgSessionKey: key, modeOverride: "plan", scheduledFrom: p,
		}},
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	go func() {
		sess.events <- Event{Type: EventResult, Content: "response1", Done: true}
		waitFor(t, "queued scheduled turn sent", func() bool {
			sess.sendMu.Lock()
			defer sess.sendMu.Unlock()
			return len(sess.sendCalls) > 0
		})
		sess.events <- Event{Type: EventResult, Content: "response2", Done: true}
	}()
	sendDone := make(chan error, 1)
	sendDone <- nil
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "msg1", time.Now(), nil, sendDone, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processInteractiveEvents did not complete in time")
	}

	sess.sendMu.Lock()
	sent := append([]string(nil), sess.sendCalls...)
	sess.sendMu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], "前一轮期间的消息") || !strings.Contains(sent[0], "（定时任务，回这条）") {
		t.Fatalf("chained scheduled prompt = %#v, want backfill with marker", sent)
	}
	// The chained turn's mode is on until its caller (runTurnWithCapacityRetry) ends the turn.
	e.restoreTurnMode(state)
	sess.mu.Lock()
	modes := strings.Join(sess.modes, ",")
	sess.mu.Unlock()
	if modes != "plan,default" {
		t.Fatalf("live modes = %q, want plan,default", modes)
	}
}

// A full queue turns the job away: it fails the old way ("job failed", last_error), muted or not,
// instead of being recorded as run. The user-message path still treats it as handled.
func TestScheduledJob_BusyQueueFull_Fails(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	as := newResultAgentSession("unused")
	e := NewEngine("test", &resultAgent{session: as}, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	e.maxQueuedMessages = 1

	key := "discord:channel-1:user-1"
	_, state := busySessionWithLiveState(t, e, p, key, as)
	state.pendingMessages = []queuedMessage{{platform: p, content: "already waiting"}}

	if err := e.ExecuteTimerJob(&TimerJob{ID: "t-full", SessionKey: key, Prompt: "x", Mute: true}); err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("muted timer on a full queue: err = %v, want queue-full error", err)
	}
	if err := e.ExecuteCronJob(&CronJob{ID: "c-full", SessionKey: key, Prompt: "y"}); err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("cron on a full queue: err = %v, want queue-full error", err)
	}
	if n := len(state.pendingMessages); n != 1 {
		t.Fatalf("queue depth = %d, want 1 (nothing added)", n)
	}
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Content: "user msg", ReplyCtx: "r"}, key) {
		t.Fatal("user message on a full queue should still count as handled")
	}
}

// capModeSession is a capacityScriptSession that also switches modes, logging mode switches
// and sends in one order.
type capModeSession struct {
	*capacityScriptSession
	logMu sync.Mutex
	log   []string
}

func (s *capModeSession) SetLiveMode(m string) bool {
	s.logMu.Lock()
	s.log = append(s.log, "mode:"+m)
	s.logMu.Unlock()
	return true
}

func (s *capModeSession) Send(prompt string, id string, imgs []ImageAttachment, files []FileAttachment) error {
	tag := "send:user"
	if strings.Contains(prompt, "定时活") {
		tag = "send:cron"
	}
	s.logMu.Lock()
	s.log = append(s.log, tag)
	s.logMu.Unlock()
	return s.capacityScriptSession.Send(prompt, id, imgs, files)
}

func (s *capModeSession) logged() string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return strings.Join(s.log, ",")
}

func newCapModeSession(script ...string) *capModeSession {
	return &capModeSession{capacityScriptSession: &capacityScriptSession{events: make(chan Event, 8), release: make(chan struct{}), script: script}}
}

// A cron job run directly switches the mode for its own turn only: a user message queued
// behind it and chained into the same event loop runs in the default mode.
func TestCronMode_DirectRun_EndsBeforeChainedUserMessage(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	sess := newCapModeSession("hold:cron", "user")
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	done := make(chan error, 1)
	go func() {
		done <- e.ExecuteCronJob(&CronJob{ID: "c", SessionKey: key, Prompt: "定时活", Mode: "plan"})
	}()
	waitFor(t, "cron turn sent", func() bool { return sess.sends() == 1 })
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Platform: "discord", UserID: "u", UserName: "u", Content: "用户消息", ReplyCtx: "r"}, key) {
		t.Fatal("user message not queued")
	}
	close(sess.release)
	if err := <-done; err != nil {
		t.Fatalf("ExecuteCronJob() = %v", err)
	}
	if got := sess.logged(); got != "mode:plan,send:cron,mode:default,send:user" {
		t.Fatalf("mode/send order = %s, want the cron mode switched back before the user message", got)
	}
}

// Same for a queued cron job run by the drain loop, with a user message queued after it.
func TestCronMode_QueuedRun_EndsBeforeChainedUserMessage(t *testing.T) {
	p := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	sess := newCapModeSession("cron", "user")
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:channel-1:user-1"
	session, state := busySessionWithLiveState(t, e, p, key, sess)
	if err := e.ExecuteCronJob(&CronJob{ID: "c", SessionKey: key, Prompt: "定时活", Mode: "plan"}); err != nil {
		t.Fatalf("ExecuteCronJob() = %v", err)
	}
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Platform: "discord", UserID: "u", UserName: "u", Content: "用户消息", ReplyCtx: "r"}, key) {
		t.Fatal("user message not queued")
	}
	e.drainPendingMessages(state, session, e.sessions, key)
	if got := sess.logged(); got != "mode:plan,send:cron,mode:default,send:user" {
		t.Fatalf("mode/send order = %s, want the cron mode switched back before the user message", got)
	}
}

// A queued cron turn that hits capacity in the drain loop is re-delivered after the cooldown
// with the backfill it already read (those posts are marked seen, so reading again finds nothing).
func TestScheduledQueued_CapacityRedelivery_KeepsBackfill(t *testing.T) {
	old := capacityCooldownBase
	capacityCooldownBase = 100 * time.Millisecond
	t.Cleanup(func() { capacityCooldownBase = old })

	now := time.Now()
	p := &cronHistoryPlatform{historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
		{ID: "101", Author: "zuiho", Content: "前一轮期间的消息", Time: now},
	}}}
	sess := newCapModeSession("cap", "cap", "cap", "ok")
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:c1"
	session, state := busySessionWithLiveState(t, e, p, key, sess)
	session.SetLastSeenMsgID("100")
	if err := e.ExecuteCronJob(&CronJob{ID: "c", SessionKey: key, Prompt: "定时活"}); err != nil {
		t.Fatalf("ExecuteCronJob() = %v", err)
	}
	e.drainPendingMessages(state, session, e.sessions, key)
	waitFor(t, "re-delivered cron turn answered", func() bool { return strings.Contains(joined(&p.stubPlatformEngine), "reply:ok") })

	prompts := sess.sentPrompts()
	if len(prompts) != 4 {
		t.Fatalf("sends = %d, want 3 capacity failures + 1 re-delivery", len(prompts))
	}
	for i, pr := range prompts {
		if !strings.Contains(pr, "前一轮期间的消息") || !strings.Contains(pr, "（定时任务，回这条）") {
			t.Fatalf("send %d lost the backfill:\n%s", i+1, pr)
		}
	}
}

// Same when the queued cron turn was chained into a running turn's event loop: the retry put
// back at the queue head carries the backfill.
func TestScheduledChained_CapacityRetry_KeepsBackfill(t *testing.T) {
	now := time.Now()
	p := &cronHistoryPlatform{historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
	}}}
	sess := newCapModeSession("hold:a", "cap", "ok")
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:c1"
	e.handleMessage(p, &Message{SessionKey: key, Platform: "discord", UserID: "u", UserName: "u", Content: "hello", ReplyCtx: "ctx"})
	waitFor(t, "user turn sent", func() bool { return sess.sends() == 1 })
	session := e.sessions.GetOrCreateActive(key)
	session.SetLastSeenMsgID("100")
	p.history = append(p.history, ChannelHistoryMessage{ID: "101", Author: "zuiho", Content: "前一轮期间的消息", Time: now})
	if err := e.ExecuteCronJob(&CronJob{ID: "c", SessionKey: key, Prompt: "定时活"}); err != nil {
		t.Fatalf("ExecuteCronJob() = %v", err)
	}
	close(sess.release)
	waitFor(t, "cron retry answered", func() bool { return strings.Contains(joined(&p.stubPlatformEngine), "reply:ok") })

	prompts := sess.sentPrompts()
	if len(prompts) != 3 {
		t.Fatalf("sends = %d (%q), want user + cron + cron retry", len(prompts), prompts)
	}
	for i, pr := range prompts[1:] {
		if !strings.Contains(pr, "前一轮期间的消息") || !strings.Contains(pr, "（定时任务，回这条）") {
			t.Fatalf("cron send %d lost the backfill:\n%s", i+1, pr)
		}
	}
}

// A queued cron turn that the drain loop defers because a cooldown is running (before it ever
// started) stays a cron turn: when re-delivered it reads the channel and gets the marker.
func TestScheduledQueued_DeferredByCooldown_BackfillsOnRedelivery(t *testing.T) {
	now := time.Now()
	p := &cronHistoryPlatform{historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
		{ID: "101", Author: "zuiho", Content: "冷却期间的消息", Time: now},
	}}}
	sess := newCapModeSession("ok")
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, "", LangEnglish)
	defer e.cancel()

	key := "discord:c1"
	session, state := busySessionWithLiveState(t, e, p, key, sess)
	session.SetLastSeenMsgID("100")
	if err := e.ExecuteCronJob(&CronJob{ID: "c", SessionKey: key, Prompt: "定时活"}); err != nil {
		t.Fatalf("ExecuteCronJob() = %v", err)
	}
	e.capMu.Lock()
	e.capUntil = time.Now().Add(100 * time.Millisecond) // a cooldown is running
	e.capMu.Unlock()
	e.drainPendingMessages(state, session, e.sessions, key)
	if n := sess.sends(); n != 0 {
		t.Fatalf("sends during cooldown = %d, want 0", n)
	}
	waitFor(t, "re-delivered cron turn answered", func() bool { return strings.Contains(joined(&p.stubPlatformEngine), "reply:ok") })
	prompts := sess.sentPrompts()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "冷却期间的消息") || !strings.Contains(prompts[0], "（定时任务，回这条）") {
		t.Fatalf("re-delivered cron prompt = %q, want the backfill with marker", prompts)
	}
}
