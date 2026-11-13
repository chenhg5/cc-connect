package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	taskCardHeartbeatInterval = 5 * time.Second
	taskCardMinRenderGap      = 1500 * time.Millisecond
	taskCardMaxQueueItems     = 5
	taskCardMaxStepLen        = 80
	taskCardMaxQueueItemLen   = 40
	// taskCardMaxFailures tolerates transient platform errors before giving up.
	taskCardMaxFailures = 5
	// taskCardRetryGap slows refreshes down while the platform is rejecting them.
	taskCardRetryGap = 15 * time.Second
)

// taskCardStatus is the lifecycle state shown on the live task card.
type taskCardStatus int

const (
	taskCardRunning taskCardStatus = iota
	taskCardDone
	taskCardStopped
	taskCardFailed
)

// turnCard keeps one live progress card for the duration of a turn (or a
// drained queue of turns):
//
//	⏳ 正在处理 · 3:12
//	当前步骤：查 jinkela 日志
//	📋 排队中 (2)
//	[⏹ 停止当前]
//
// It refreshes on three triggers: agent step changes, queue changes, and a 5s
// heartbeat that keeps the elapsed clock moving while the agent is silent.
type turnCard struct {
	e        *Engine
	sender   TaskCardSender
	replyCtx any
	key      string

	// renderMu serializes remote calls so two refreshes never race to create
	// the initial message twice.
	renderMu sync.Mutex

	mu          sync.Mutex
	handle      any
	started     time.Time
	status      taskCardStatus
	step        string
	queue       []string
	lastRender  time.Time
	finished    bool
	disabled    bool
	creating    bool
	cardStarted bool
	failCount   int
	visible     bool // true once the card message exists

	done chan struct{}
	once sync.Once
}

// newTurnCard returns nil when the platform cannot host a live card, so all
// call sites can stay nil-safe.
func newTurnCard(e *Engine, p Platform, replyCtx any, key string) *turnCard {
	sender, ok := p.(TaskCardSender)
	if !ok || sender == nil {
		return nil
	}
	return &turnCard{
		e:        e,
		sender:   sender,
		replyCtx: replyCtx,
		key:      key,
		started:  time.Now(),
		status:   taskCardRunning,
		done:     make(chan struct{}),
	}
}

// Start is intentionally a no-op: the card appears on the first real step
// (see EnsureStarted), so quick answers never leave an empty "准备中" card.
func (tc *turnCard) Start() {}

// EnsureStarted posts the card the first time the agent actually does
// something (a tool call). Idempotent and safe to call from the event loop.
func (tc *turnCard) EnsureStarted() {
	if tc == nil {
		return
	}
	tc.mu.Lock()
	if tc.cardStarted || tc.finished || tc.visible {
		tc.mu.Unlock()
		return
	}
	tc.cardStarted = true
	tc.mu.Unlock()
	tc.render(true)
	go tc.heartbeat()
}

func (tc *turnCard) heartbeat() {
	ticker := time.NewTicker(taskCardHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-tc.done:
			return
		case <-tc.e.ctx.Done():
			return
		case <-ticker.C:
			tc.render(false)
		}
	}
}

// Visible reports whether the card message has been posted already. Before
// that, callers must keep using plain-text fallbacks.
func (tc *turnCard) Visible() bool {
	if tc == nil {
		return false
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.visible
}

// SetStep records what the agent is doing now and refreshes (throttled).
func (tc *turnCard) SetStep(step string) {
	if tc == nil {
		return
	}
	tc.mu.Lock()
	if tc.finished {
		tc.mu.Unlock()
		return
	}
	tc.step = truncateIf(strings.TrimSpace(step), taskCardMaxStepLen)
	tc.mu.Unlock()
	tc.render(false)
}

// SetQueue mirrors the pending message queue onto the card.
func (tc *turnCard) SetQueue(items []string) {
	if tc == nil {
		return
	}
	tc.mu.Lock()
	if tc.finished {
		tc.mu.Unlock()
		return
	}
	tc.queue = items
	tc.mu.Unlock()
	tc.render(false)
}

// Finish renders the terminal state once and stops the heartbeat.
func (tc *turnCard) Finish(status taskCardStatus, note string) {
	if tc == nil {
		return
	}
	tc.mu.Lock()
	if tc.finished {
		tc.mu.Unlock()
		return
	}
	tc.finished = true
	tc.status = status
	if note != "" {
		tc.step = truncateIf(note, taskCardMaxStepLen)
	}
	tc.mu.Unlock()

	tc.render(true)
	tc.once.Do(func() { close(tc.done) })
}

func (tc *turnCard) render(force bool) {
	if tc == nil {
		return
	}

	tc.mu.Lock()
	if tc.disabled {
		tc.mu.Unlock()
		return
	}
	// Never surface a card for a turn that already ended before the first
	// render: that would leave a pointless "✅ 已完成 · 0:00" message.
	if tc.handle == nil && !tc.visible && tc.finished {
		tc.mu.Unlock()
		return
	}
	// A create is already in flight; don't enqueue a second one.
	if tc.handle == nil && tc.creating {
		tc.mu.Unlock()
		return
	}
	now := time.Now()
	minGap := taskCardMinRenderGap
	if tc.failCount > 0 {
		// Back off after a failure (usually a platform rate limit while the
		// answer text is streaming through its own message).
		minGap = taskCardRetryGap
	}
	if !force && now.Sub(tc.lastRender) < minGap {
		tc.mu.Unlock()
		return
	}
	tc.lastRender = now
	card := tc.buildCardLocked()
	handle := tc.handle
	if handle == nil {
		tc.creating = true
	}
	tc.mu.Unlock()

	// The network call runs on the session's outbox goroutine so the turn loop
	// never waits for the platform.
	tc.e.outboxFor(tc.key).push(func() {
		ctx, cancel := context.WithTimeout(tc.e.ctx, 10*time.Second)
		defer cancel()

		var err error
		if handle == nil {
			var h any
			h, err = tc.sender.SendTaskCard(ctx, tc.replyCtx, card)
			tc.mu.Lock()
			tc.creating = false
			if err == nil {
				tc.handle = h
				tc.visible = true
				tc.failCount = 0
			}
			tc.mu.Unlock()
		} else {
			err = tc.sender.UpdateTaskCard(ctx, handle, card)
			if err == nil {
				tc.mu.Lock()
				tc.failCount = 0
				tc.mu.Unlock()
			}
		}
		if err == nil {
			return
		}

		tc.mu.Lock()
		tc.failCount++
		fails := tc.failCount
		if fails >= taskCardMaxFailures {
			tc.disabled = true
		}
		tc.mu.Unlock()
		slog.Warn("task card refresh failed", "platform", tc.senderName(), "consecutive", fails, "error", err)
	})
}

func (tc *turnCard) senderName() string {
	if p, ok := tc.sender.(Platform); ok {
		return p.Name()
	}
	return "unknown"
}

// buildCardLocked renders the card. Callers must hold tc.mu.
func (tc *turnCard) buildCardLocked() *Card {
	elapsed := time.Since(tc.started)
	header := &CardHeader{}
	switch tc.status {
	case taskCardDone:
		header.Title = tc.e.i18n.Tf(MsgTaskCardDone, formatCardElapsed(elapsed))
		header.Color = "green"
	case taskCardStopped:
		header.Title = tc.e.i18n.Tf(MsgTaskCardStopped, formatCardElapsed(elapsed))
		header.Color = "grey"
	case taskCardFailed:
		header.Title = tc.e.i18n.Tf(MsgTaskCardFailed, formatCardElapsed(elapsed))
		header.Color = "red"
	default:
		header.Title = tc.e.i18n.Tf(MsgTaskCardRunning, formatCardElapsed(elapsed))
		header.Color = "blue"
	}

	step := tc.step
	if step == "" {
		step = tc.e.i18n.T(MsgTaskCardPreparing)
	}
	stepKey := MsgTaskCardStep
	if tc.finished {
		stepKey = MsgTaskCardLastStep
	}
	body := tc.e.i18n.Tf(stepKey, step)

	if len(tc.queue) > 0 && tc.status == taskCardRunning {
		body += "\n" + tc.e.i18n.Tf(MsgTaskCardQueue, len(tc.queue))
		shown := 0
		for i, item := range tc.queue {
			if i >= taskCardMaxQueueItems {
				body += fmt.Sprintf("\n↳ 还有 %d 条未显示", len(tc.queue)-shown)
				break
			}
			body += fmt.Sprintf("\n↳ %d. %s", i+1, truncateIf(item, taskCardMaxQueueItemLen))
			shown++
		}
	}

	card := &Card{
		Header:   header,
		Elements: []CardElement{CardMarkdown{Content: body}},
	}
	if tc.status == taskCardRunning {
		card.Elements = append(card.Elements, CardActions{
			Buttons: []CardButton{Btn(tc.e.i18n.T(MsgTaskCardStopBtn), "danger", "cmd:/stop")},
		})
	}
	return card
}

// formatElapsed renders a duration as m:ss (or h:mm:ss past an hour).
func formatCardElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// stateTaskCard returns the state's task card without racing the writer.
func stateTaskCard(state *interactiveState) *turnCard {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	tc := state.taskCard
	state.mu.Unlock()
	return tc
}

// syncTaskCardQueue mirrors the pending queue onto the task card.
func (e *Engine) syncTaskCardQueue(state *interactiveState) {
	tc := stateTaskCard(state)
	if tc == nil {
		return
	}
	state.mu.Lock()
	items := make([]string, 0, len(state.pendingMessages))
	for _, queued := range state.pendingMessages {
		items = append(items, queued.content)
	}
	state.mu.Unlock()
	tc.SetQueue(items)
}

// taskCardStepText turns a tool event into the one-line "current step" label.
func taskCardStepText(tool, input string) string {
	tool = strings.TrimSpace(tool)
	input = compactCommand(input)
	if tool == "" {
		return truncateMiddle(input, taskCardMaxStepLen)
	}
	if input == "" {
		return tool
	}
	return tool + " · " + truncateMiddle(input, 70)
}

// compactCommand keeps the informative part of a shell command: a leading
// "cd <long path> &&" (or export prelude) is dropped, the home directory
// becomes "~", over-long paths keep their tail, and long commands are
// truncated in the middle so the script name and args stay readable.
func compactCommand(input string) string {
	s := strings.TrimSpace(strings.ReplaceAll(input, "\n", " "))
	for _, prefix := range []string{"bash -lc \"", "bash -c \"", "sh -c \"", "zsh -c \""} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimSuffix(strings.TrimPrefix(s, prefix), "\"")
			break
		}
	}
	for {
		trimmed := strings.TrimSpace(s)
		cut := -1
		switch {
		case strings.HasPrefix(trimmed, "cd "), strings.HasPrefix(trimmed, "export "):
			if i := strings.Index(trimmed, "&&"); i >= 0 {
				cut = i + 2
			} else if i := strings.Index(trimmed, ";"); i >= 0 {
				cut = i + 1
			}
		}
		if cut < 0 {
			break
		}
		s = trimmed[cut:]
	}
	s = strings.Join(strings.Fields(s), " ")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "~")
	}
	s = strings.ReplaceAll(s, "$HOME", "~")
	return shortenPaths(s)
}

// shortenPaths turns "~/a/b/c/d/script.js" into "…/d/script.js" when a token
// is too long to read in a card.
func shortenPaths(s string) string {
	parts := strings.Fields(s)
	for i, tok := range parts {
		if len(tok) <= 28 || !strings.Contains(tok, "/") {
			continue
		}
		segs := strings.Split(tok, "/")
		if len(segs) < 4 {
			continue
		}
		parts[i] = "…/" + strings.Join(segs[len(segs)-2:], "/")
	}
	return strings.Join(parts, " ")
}

// truncateMiddle keeps both ends of a long string so the tail (usually the
// script name or arguments) stays readable.
func truncateMiddle(s string, max int) string {
	// Slice by runes, not bytes: a Chinese character is 3 bytes and cutting
	// mid-rune renders as garbage in the card.
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	head := max * 2 / 5
	tail := max - head - 1
	if tail < 0 {
		tail = 0
	}
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

// Finished reports whether the card already reached a terminal state.
func (tc *turnCard) Finished() bool {
	if tc == nil {
		return true
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.finished
}

// rotateTaskCardIfFinished gives a queued turn a fresh card when the previous
// one was already closed (e.g. the user stopped mid-run and then followed up).
// Without this the follow-up would run under a card that still says
// "已停止", which looks like the bot never picked the message up.
func (e *Engine) rotateTaskCardIfFinished(state *interactiveState, p Platform, replyCtx any, key string) *turnCard {
	if state == nil {
		return nil
	}
	if cur := stateTaskCard(state); cur != nil && !cur.Finished() {
		return cur
	}
	tc := newTurnCard(e, p, replyCtx, key)
	if tc == nil {
		return nil
	}
	state.mu.Lock()
	state.taskCard = tc
	state.mu.Unlock()
	tc.Start()
	e.syncTaskCardQueue(state)
	return tc
}
