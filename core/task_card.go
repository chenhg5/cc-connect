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

// taskCardMinRenderGap is a variable so tests can disable the throttle.
var taskCardMinRenderGap = 1500 * time.Millisecond

const (
	defaultCardTickInterval = 5 * time.Second
	taskCardMaxQueueItems   = 5
	taskCardMaxStepLen      = 80
	taskCardMaxQueueItemLen = 40
	// taskCardDegradedThreshold is when the user is told the card is being
	// throttled. The ticker keeps retrying; it is never permanently disabled
	// mid-turn.
	taskCardDegradedThreshold = 5
	// taskCardRetryGap slows refreshes down while the platform is rejecting them.
	taskCardRetryGap = 15 * time.Second
	// taskCardDegradedGap is the slower cadence after sustained rejections.
	taskCardDegradedGap = 30 * time.Second
)

// taskCardTerminalRetryDelays is a variable so tests can shorten the wait.
// Six attempts over ~47s: the terminal update is the only signal that a turn
// finished, so it is worth outlasting a burst of Feishu 230429 throttling.
var taskCardTerminalRetryDelays = []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 15 * time.Second}

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

	mu               sync.Mutex
	handle           any
	started          time.Time
	status           taskCardStatus
	step             string
	queue            []string
	lastRender       time.Time
	lastActivityAt   time.Time // last agent text/tool event; quiets the ticker
	finished         bool
	degradedNotified bool
	creating         bool
	cardStarted      bool
	rendering        bool // a card request is in flight
	pendingRender    bool // a refresh was requested while rendering
	failCount        int
	visible          bool          // true once the card message exists
	tickInterval     time.Duration // 0 = no independent ticker

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
		e:            e,
		sender:       sender,
		replyCtx:     replyCtx,
		key:          key,
		started:      time.Now(),
		status:       taskCardRunning,
		tickInterval: e.cardTickInterval,
		done:         make(chan struct{}),
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
	interval := tc.tickInterval
	tc.mu.Unlock()
	tc.render(true)
	if interval > 0 {
		go tc.heartbeat(interval)
	}
}

// heartbeat refreshes the card independently of agent events, so a long tool
// call does not freeze the elapsed clock.
func (tc *turnCard) heartbeat(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-tc.done:
			return
		case <-tc.e.ctx.Done():
			return
		case <-ticker.C:
			tc.mu.Lock()
			lastActivity := tc.lastActivityAt
			finished := tc.finished
			tc.mu.Unlock()
			if finished {
				return
			}
			// While the agent is streaming text/tool steps the user already
			// sees progress; don't burn quota on card PATCHes. Resume once the
			// turn has been quiet for two tick intervals.
			if !lastActivity.IsZero() && time.Since(lastActivity) < 2*interval {
				continue
			}
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

// NoteActivity records agent output so the independent ticker can stay quiet
// while the user is already seeing streamed text/tool steps.
func (tc *turnCard) NoteActivity() {
	if tc == nil {
		return
	}
	tc.mu.Lock()
	tc.lastActivityAt = time.Now()
	tc.mu.Unlock()
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
	tc.lastActivityAt = time.Now()
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
	tc.lastActivityAt = time.Now()
	tc.mu.Unlock()
	tc.render(false)
}

// Finish renders the terminal state once and stops the heartbeat.
//
// The terminal update is the only signal that tells the user the turn is over.
// Rendering it best-effort left cards frozen at "⏳ 正在处理" forever when a
// single PATCH failed (typically a 230429 rate limit), which users then read as
// "still running". Deliver it with retries and fall back to a plain message.
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
	// creating: the initial card POST is in flight. The terminal task queued
	// below will run after it (FIFO outbox), so the card must not be treated as
	// non-existent just because the create has not completed yet.
	hasCard := tc.visible || tc.handle != nil || tc.creating
	tc.mu.Unlock()

	tc.once.Do(func() { close(tc.done) })

	if !hasCard {
		// The card was never posted (quick answer / no tool call); there is
		// nothing to finish and no text fallback is needed.
		return
	}

	tc.mu.Lock()
	card := tc.buildCardLocked()
	tc.mu.Unlock()

	// First attempt runs on the outbox so it stays ordered with the turn's
	// final reply. Backoff retries run out of band: a stale card must not delay
	// the next turn's answer behind ~47s of sleeps.
	tc.e.outboxFor(tc.key).push(func() {
		err := tc.applyCard(card)
		if err == nil {
			slog.Info("task card terminal state delivered",
				"platform", tc.senderName(), "attempt", 1, "status", status)
			return
		}
		slog.Warn("task card terminal update failed",
			"platform", tc.senderName(), "attempt", 1,
			"will_retry", len(taskCardTerminalRetryDelays) > 1, "error", err)
		go tc.retryTerminal(card, status, 1)
	})
}

// retryTerminal applies the final card state with exponential backoff. If the
// platform keeps rejecting the update it falls back to a plain message so the
// turn never ends invisibly.
func (tc *turnCard) retryTerminal(card *Card, status taskCardStatus, startAttempt int) {
	delays := taskCardTerminalRetryDelays
	var lastErr error
	for i := startAttempt; i < len(delays); i++ {
		if delay := delays[i]; delay > 0 {
			select {
			case <-tc.e.ctx.Done():
				return
			case <-time.After(delay):
			}
		}
		if err := tc.applyCard(card); err == nil {
			slog.Info("task card terminal state delivered",
				"platform", tc.senderName(), "attempt", i+1, "status", status)
			return
		} else {
			lastErr = err
			slog.Warn("task card terminal update failed",
				"platform", tc.senderName(), "attempt", i+1,
				"will_retry", i+1 < len(delays), "error", err)
		}
	}
	tc.deliverTerminalFallback(status, lastErr)
}

// deliverTerminalFallback is the visible status when every card update failed.
func (tc *turnCard) deliverTerminalFallback(status taskCardStatus, lastErr error) {
	slog.Error("task card terminal update failed permanently",
		"platform", tc.senderName(), "status", status, "error", lastErr)
	tc.sendTerminalText()
}

// applyCard performs one card create/update with a bounded network timeout.
// It runs on the session outbox goroutine (terminal delivery).
func (tc *turnCard) applyCard(card *Card) error {
	ctx, cancel := context.WithTimeout(tc.e.ctx, 10*time.Second)
	defer cancel()

	tc.mu.Lock()
	handle := tc.handle
	tc.mu.Unlock()

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
	return err
}

// sendTerminalText is the last-resort visible status when the card itself
// cannot be updated. It retries a few times too: the same frequency limit that
// blocked the card PATCH can block a plain send.
func (tc *turnCard) sendTerminalText() {
	text := tc.terminalText()
	if !tc.sendPlainText(text, "terminal fallback") {
		slog.Error("task card terminal fallback exhausted: user was not notified of the turn result",
			"platform", tc.senderName(), "text", text)
	}
}

// sendPlainText delivers a non-card status message with a few retries. Used for
// the terminal fallback and for the "card updates are throttled" notice. It
// runs on the session outbox goroutine.
func (tc *turnCard) sendPlainText(text, kind string) bool {
	p, ok := tc.sender.(Platform)
	if !ok {
		slog.Error("task card plain-text fallback unavailable: sender is not a Platform",
			"platform", tc.senderName(), "kind", kind)
		return false
	}
	delays := []time.Duration{0, time.Second, 3 * time.Second}
	var lastErr error
	for attempt, delay := range delays {
		if delay > 0 {
			select {
			case <-tc.e.ctx.Done():
				return false
			case <-time.After(delay):
			}
		}
		if err := p.Send(tc.e.ctx, tc.replyCtx, text); err != nil {
			lastErr = err
			slog.Error("task card plain-text send failed",
				"platform", tc.senderName(), "kind", kind, "attempt", attempt+1,
				"will_retry", attempt+1 < len(delays), "error", err, "text", text)
			continue
		}
		slog.Warn("task card plain-text fallback sent",
			"platform", tc.senderName(), "kind", kind, "attempt", attempt+1, "text", text)
		return true
	}
	slog.Error("task card plain-text send exhausted",
		"platform", tc.senderName(), "kind", kind, "text", text, "error", lastErr)
	return false
}

// terminalText renders the terminal status as plain text. It keeps the last
// step line too, since a user whose card never updated never saw it.
func (tc *turnCard) terminalText() string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	card := tc.buildCardLocked()
	title := tc.e.i18n.T(MsgTaskCardDone)
	if card.Header != nil && card.Header.Title != "" {
		title = card.Header.Title
	}
	for _, el := range card.Elements {
		if md, ok := el.(CardMarkdown); ok && strings.TrimSpace(md.Content) != "" {
			return title + "\n" + md.Content
		}
	}
	return title
}

func (tc *turnCard) render(force bool) {
	if tc == nil {
		return
	}

	tc.mu.Lock()
	// Finish uses applyCard directly; a normal render after it must not enqueue.
	if tc.finished {
		tc.mu.Unlock()
		return
	}
	// A create is already in flight; don't enqueue a second one.
	if tc.handle == nil && tc.creating {
		tc.pendingRender = true
		tc.mu.Unlock()
		return
	}
	// Single-flight: one card request at a time. Requests arriving while a
	// slow (or rate-limited) PATCH is in flight are coalesced into one pending
	// refresh instead of queueing unboundedly behind it.
	if tc.rendering {
		tc.pendingRender = true
		tc.mu.Unlock()
		return
	}
	now := time.Now()
	minGap := taskCardMinRenderGap
	if tc.failCount > 0 {
		// Back off after a failure (usually a platform rate limit while the
		// answer text is streaming through its own message). Sustained
		// failures slow down further but never disable the ticker.
		minGap = taskCardRetryGap
	}
	if tc.failCount >= taskCardDegradedThreshold {
		minGap = taskCardDegradedGap
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
	tc.rendering = true
	tc.mu.Unlock()

	// The network call runs on the session's outbox goroutine so the turn loop
	// never waits for the platform.
	tc.e.outboxFor(tc.key).push(func() {
		defer func() {
			tc.mu.Lock()
			tc.rendering = false
			pending := tc.pendingRender
			tc.pendingRender = false
			finished := tc.finished
			tc.mu.Unlock()
			// Flush one coalesced refresh now that the in-flight request is done.
			if pending && !finished {
				tc.render(false)
			}
		}()
		// A heartbeat refresh queued before Finish may run after the terminal
		// state was applied (e.g. the outbox was full and ran out of band).
		// Never let a stale running-state card overwrite the final one.
		tc.mu.Lock()
		if tc.finished {
			tc.mu.Unlock()
			return
		}
		tc.mu.Unlock()

		ctx, cancel := context.WithTimeout(tc.e.ctx, 10*time.Second)
		defer cancel()

		var err error
		op := "update"
		if handle == nil {
			op = "create"
			var h any
			h, err = tc.sender.SendTaskCard(ctx, tc.replyCtx, card)
			tc.mu.Lock()
			tc.creating = false
			if err == nil {
				tc.handle = h
				tc.visible = true
			}
			tc.mu.Unlock()
		} else {
			// Ticker refreshes use a single attempt so the card's own backoff
			// (15s/30s) controls the cadence; terminal delivery still uses the
			// full-retry UpdateTaskCard path.
			if ticker, ok := tc.sender.(TaskCardTickerSender); ok {
				err = ticker.UpdateTaskCardOnce(ctx, handle, card)
			} else {
				err = tc.sender.UpdateTaskCard(ctx, handle, card)
			}
		}
		tc.mu.Lock()
		prevFails := tc.failCount
		if err == nil {
			tc.failCount = 0
			tc.degradedNotified = false
		} else {
			tc.failCount++
		}
		fails := tc.failCount
		degraded := fails >= taskCardDegradedThreshold
		notify := degraded && !tc.degradedNotified
		if notify {
			tc.degradedNotified = true
		}
		finished := tc.finished
		visible := tc.visible
		tc.mu.Unlock()
		if err == nil {
			if prevFails > 0 {
				slog.Info("task card refresh recovered",
					"platform", tc.senderName(), "op", op,
					"previous_failures", prevFails, "finished", finished)
			}
			return
		}

		// Log every failed refresh with enough context to reconstruct the
		// incident from cc-connect.log alone.
		slog.Warn("task card refresh failed",
			"platform", tc.senderName(), "op", op, "consecutive", fails,
			"degraded", degraded, "finished", finished,
			"visible", visible, "error", err)
		if notify {
			slog.Error("task card updates degraded after consecutive failures",
				"platform", tc.senderName(), "failures", fails, "error", err)
			// Tell the user the card is unreliable instead of letting a frozen
			// card look like a dead task. The ticker keeps retrying at a slower
			// cadence; it is not permanently disabled.
			tc.sendPlainText(tc.e.i18n.T(MsgTaskCardDegraded), "degraded notice")
		}
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
	stepKey := MsgTaskCardStep
	if tc.finished {
		stepKey = MsgTaskCardLastStep
	}
	var body string
	if step == "" {
		body = tc.e.i18n.Tf(stepKey, tc.e.i18n.T(MsgTaskCardPreparing))
	} else {
		body = tc.e.i18n.Tf(stepKey, cardCodeBlock(step))
	}

	if len(tc.queue) > 0 && tc.status == taskCardRunning {
		body += "\n" + tc.e.i18n.Tf(MsgTaskCardQueue, len(tc.queue))
		shown := 0
		for i, item := range tc.queue {
			if i >= taskCardMaxQueueItems {
				body += fmt.Sprintf("\n↳ 还有 %d 条未显示", len(tc.queue)-shown)
				break
			}
			body += fmt.Sprintf("\n↳ %d. %s", i+1, cardInlineCode(truncateIf(item, taskCardMaxQueueItemLen)))
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

// cardCodeBlock wraps user-controlled command text in a fenced code block so
// it renders literally: <at>, backticks, asterisks and brackets stay inert.
// The fence is longer than any backtick run inside the text, otherwise a
// command containing "```" could close the block early.
func cardCodeBlock(s string) string {
	n := backtickRun(s) + 1
	if n < 3 {
		n = 3
	}
	fence := strings.Repeat("`", n)
	return fence + "\n" + s + "\n" + fence
}

// cardInlineCode renders user text as a single-line code span: links, bold,
// mentions and other markdown stay literal. Newlines are collapsed because an
// inline span cannot span lines; the delimiter is longer than any backtick run
// inside the text.
func cardInlineCode(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	fence := strings.Repeat("`", backtickRun(s)+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

func backtickRun(s string) int {
	maxRun, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > maxRun {
				maxRun = run
			}
			continue
		}
		run = 0
	}
	return maxRun
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

// taskCardStepText turns a tool event into the one-line "current step" label:
// the tool name plus its compacted input, so the card stays a raw, useful
// trace of what the agent is actually doing.
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

// compactCommand collapses a raw tool input into a single readable line while
// keeping the actual command intact.
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

// shortenPaths collapses long path tokens to their last two segments.
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
