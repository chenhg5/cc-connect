package core

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	taskCardHeartbeatInterval = 5 * time.Second
	// taskCardFirstVisibleAfter suppresses cards for quick turns: a Q&A that
	// finishes in under two seconds leaves no extra card behind.
	taskCardFirstVisibleAfter  = 2 * time.Second
	taskCardMinRenderGap      = 1500 * time.Millisecond
	taskCardMaxQueueItems     = 5
	taskCardMaxStepLen        = 80
	taskCardMaxQueueItemLen   = 40
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

	// renderMu serializes remote calls so two refreshes never race to create
	// the initial message twice.
	renderMu sync.Mutex

	mu         sync.Mutex
	handle     any
	started    time.Time
	status     taskCardStatus
	step       string
	queue      []string
	lastRender time.Time
	finished   bool
	disabled   bool
	visible    bool // true once the card message exists

	done chan struct{}
	once sync.Once
}

// newTurnCard returns nil when the platform cannot host a live card, so all
// call sites can stay nil-safe.
func newTurnCard(e *Engine, p Platform, replyCtx any) *turnCard {
	sender, ok := p.(TaskCardSender)
	if !ok || sender == nil {
		return nil
	}
	return &turnCard{
		e:        e,
		sender:   sender,
		replyCtx: replyCtx,
		started:  time.Now(),
		status:   taskCardRunning,
		done:     make(chan struct{}),
	}
}

// Start schedules the first render. Short turns finish before the delay and
// never create a card at all.
func (tc *turnCard) Start() {
	if tc == nil {
		return
	}
	go tc.run()
}

func (tc *turnCard) run() {
	timer := time.NewTimer(taskCardFirstVisibleAfter)
	defer timer.Stop()
	select {
	case <-tc.done:
		return
	case <-tc.e.ctx.Done():
		return
	case <-timer.C:
	}
	tc.render(true)
	tc.heartbeat()
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

	tc.renderMu.Lock()
	defer tc.renderMu.Unlock()

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
	now := time.Now()
	if !force && now.Sub(tc.lastRender) < taskCardMinRenderGap {
		tc.mu.Unlock()
		return
	}
	tc.lastRender = now
	card := tc.buildCardLocked()
	handle := tc.handle
	tc.mu.Unlock()

	if handle == nil {
		h, err := tc.sender.SendTaskCard(tc.e.ctx, tc.replyCtx, card)
		if err != nil {
			slog.Debug("task card: initial send failed, disabling", "platform", tc.senderName(), "error", err)
			tc.mu.Lock()
			tc.disabled = true
			tc.mu.Unlock()
			return
		}
		tc.mu.Lock()
		tc.handle = h
		tc.visible = true
		tc.mu.Unlock()
		return
	}
	if err := tc.sender.UpdateTaskCard(tc.e.ctx, handle, card); err != nil {
		slog.Debug("task card: update failed, disabling", "platform", tc.senderName(), "error", err)
		tc.mu.Lock()
		tc.disabled = true
		tc.mu.Unlock()
	}
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
	body := tc.e.i18n.Tf(MsgTaskCardStep, step)

	if len(tc.queue) > 0 && tc.status == taskCardRunning {
		body += "\n" + tc.e.i18n.Tf(MsgTaskCardQueue, len(tc.queue))
		shown := 0
		for i, item := range tc.queue {
			if i >= taskCardMaxQueueItems {
				body += fmt.Sprintf("\n↳ …+%d", len(tc.queue)-shown)
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
	input = strings.TrimSpace(strings.ReplaceAll(input, "\n", " "))
	if tool == "" {
		return truncateIf(input, taskCardMaxStepLen)
	}
	if input == "" {
		return tool
	}
	return tool + " · " + truncateIf(input, 60)
}
