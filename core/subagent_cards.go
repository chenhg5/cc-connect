package core

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Subagents run beside the turn that spawned them: a background subagent
// keeps working after the main agent has replied and ended its turn, and
// several may run at once. Folding their steps into the turn's progress card
// tied them to the turn's life (a background subagent went dark the moment
// the turn ended) and interleaved them. Each subagent therefore gets its own
// progress card in the conversation, driven by a per-conversation worker that
// is independent of whichever reader (foreground turn or unsolicited) happens
// to see the events.

const (
	// subagentCardInterval is the least time between two edits of one card,
	// so a chatty subagent (or many in parallel) stays under platform rate
	// limits. Edits held back meanwhile go out once the card is due.
	subagentCardInterval = 2 * time.Second
	// subagentCardTick is how often the worker looks for held-back edits;
	// a card lags its subagent by at most interval + tick.
	subagentCardTick = 500 * time.Millisecond
	// subagentCardBacklog is how many events may wait for the worker before
	// steps are dropped. Starts and finishes are never dropped: losing one
	// would leave a card untitled or running forever.
	subagentCardBacklog = 512
	// subagentCardIdle ends a worker whose subagents went silent without
	// finishing (e.g. the agent process died); their cards stay as they are.
	// Their writer state is discarded, so a later event for the same ID
	// starts a new card rather than recovering the idle card's history.
	subagentCardIdle = 2 * time.Hour
	// subagentPromptMaxLen bounds the task line a card opens with.
	subagentPromptMaxLen = 300
)

type subagentOp struct {
	event    SubagentEvent
	platform Platform
	replyCtx any
}

type subagentCard struct {
	writer *compactProgressWriter
	// finished is set when the subagent ended. state is a card state change
	// (closing it, or reopening it) waiting for the card to be due for an
	// edit; empty once sent.
	finished bool
	state    ProgressCardState
	lastText string // the subagent's latest text step
}

// subagentCards owns the subagent progress cards of one conversation. Events
// are queued by the readers and applied in order by a single worker
// goroutine, which also sends held-back edits; the worker exits once no card
// is open.
type subagentCards struct {
	e *Engine

	mu      sync.Mutex
	pending []subagentOp  // events waiting for the worker, in order
	wake    chan struct{} // nudges the worker when pending grows
	running bool
	open    int // cards not yet closed; written by the worker under mu

	cards map[string]*subagentCard // worker only
}

func (e *Engine) subagentCardsFor(state *interactiveState) *subagentCards {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.subagents == nil {
		state.subagents = &subagentCards{
			e:     e,
			wake:  make(chan struct{}, 1),
			cards: make(map[string]*subagentCard),
		}
	}
	return state.subagents
}

// handleSubagentEvent routes one EventSubagent to the conversation's cards.
// It never blocks the reader: when the worker falls far behind, steps are
// dropped (the card misses a step, the turn is unaffected).
func (e *Engine) handleSubagentEvent(state *interactiveState, event Event) {
	if event.Subagent == nil || event.Subagent.ID == "" {
		return
	}
	state.mu.Lock()
	p := state.platform
	replyCtx := state.replyCtx
	state.mu.Unlock()
	if p == nil {
		return
	}
	e.subagentCardsFor(state).enqueue(subagentOp{event: *event.Subagent, platform: p, replyCtx: replyCtx})
}

// drainEventsKeepSubagents empties the agent's event channel before a turn
// like drainEvents, but hands subagent events to their cards: a background
// subagent keeps writing them across turns, so they are no leftovers, and a
// dropped one would leave its card without a title or stuck running.
func (e *Engine) drainEventsKeepSubagents(state *interactiveState, ch <-chan Event) {
	drained := 0
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			if event.Type == EventSubagent {
				e.handleSubagentEvent(state, event)
				continue
			}
			drained++
		default:
			if drained > 0 {
				slog.Warn("drained stale events from previous turn", "count", drained)
			}
			return
		}
	}
}

func (c *subagentCards) enqueue(op subagentOp) {
	c.mu.Lock()
	if op.event.Phase == SubagentStep && len(c.pending) >= subagentCardBacklog {
		c.mu.Unlock()
		slog.Warn("subagent cards: backlog full, dropping step", "subagent", op.event.ID)
		return
	}
	// Queued under mu so the worker's exit check, also under mu, never
	// misses an event.
	c.pending = append(c.pending, op)
	if !c.running {
		c.running = true
		go c.run()
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *subagentCards) take() []subagentOp {
	c.mu.Lock()
	defer c.mu.Unlock()
	ops := c.pending
	c.pending = nil
	return ops
}

func (c *subagentCards) run() {
	ticker := time.NewTicker(subagentCardTick)
	defer ticker.Stop()
	lastEvent := time.Now()
	for {
		select {
		case <-c.e.ctx.Done():
			c.stop()
			return
		case <-c.wake:
			if ops := c.take(); len(ops) > 0 {
				lastEvent = time.Now()
				for _, op := range ops {
					c.apply(op)
				}
			}
		case <-ticker.C:
			for _, card := range c.cards {
				c.flush(card)
			}
			if c.idle(time.Since(lastEvent) >= subagentCardIdle) {
				return
			}
		}
	}
}

// idle reports whether the worker should exit: nothing is queued and no card
// is open, or the open ones have been silent for too long.
func (c *subagentCards) idle(silent bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 || (c.open > 0 && !silent) {
		return false
	}
	if c.open > 0 {
		slog.Info("subagent cards: subagents went silent without finishing", "open", c.open)
		for id, card := range c.cards {
			if !card.finished || card.state != "" {
				delete(c.cards, id)
			}
		}
		c.open = 0
	}
	c.running = false
	return true
}

func (c *subagentCards) stop() {
	c.mu.Lock()
	c.running = false
	c.mu.Unlock()
}

func (c *subagentCards) setOpen(delta int) {
	c.mu.Lock()
	c.open += delta
	c.mu.Unlock()
}

// flush sends a card's held-back edit, with its pending state change, once
// the card is due for an edit: one edit carries both.
func (c *subagentCards) flush(card *subagentCard) {
	w := card.writer
	if !w.due() {
		return
	}
	state := card.state
	if state == "" {
		w.flushPending()
		return
	}
	card.state = ""
	if w.Finalize(state) {
		w.lastUpdateAt = time.Now()
	} else {
		w.flushPending() // a card without states only shows its steps
	}
	if state != ProgressCardStateRunning {
		c.setOpen(-1)
	}
}

func (c *subagentCards) apply(op subagentOp) {
	ev := op.event
	card := c.cards[ev.ID]
	if card == nil {
		if ev.Phase == SubagentFinished {
			return // nothing was shown for it
		}
		card = &subagentCard{writer: c.newWriter(op)}
		c.cards[ev.ID] = card
		c.setOpen(1)
	}
	w := card.writer
	switch ev.Phase {
	case SubagentStarted:
		if card.finished {
			// Woken again (e.g. by a task notification): the same card
			// carries on.
			card.finished = false
			if card.state != "" {
				card.state = "" // its close was never sent: still open
			} else {
				c.setOpen(1)
				card.state = ProgressCardStateRunning
			}
		}
		if prompt := strings.TrimSpace(ev.Prompt); prompt != "" {
			prompt = truncateIf(prompt, subagentPromptMaxLen)
			w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryInfo, Text: prompt}, prompt)
		}
	case SubagentStep:
		if card.finished {
			return // only a new start may reopen a finished card
		}
		step := ev.Step
		switch step.Kind {
		case ProgressEntryThinking:
			if !c.e.display.ThinkingMessages {
				return
			}
			step.Text = truncateIf(step.Text, c.e.display.ThinkingMaxLen)
		case ProgressEntryToolUse:
			if !c.e.display.ToolMessages {
				return
			}
			step.Text = truncateIf(step.Text, c.e.display.ToolMaxLen)
		}
		if step.Kind == ProgressEntryInfo {
			card.lastText = strings.TrimSpace(step.Text)
		}
		w.AppendStructured(step, step.Text)
	case SubagentFinished:
		if card.finished {
			return
		}
		state := ProgressCardStateCompleted
		mark := "✅ "
		if ev.Status != "" && ev.Status != "completed" {
			state = ProgressCardStateFailed
			mark = "❌ " + ev.Status + " "
		}
		// The summary is usually the subagent's last words, already on the
		// card; only a different one is worth another line.
		if summary := strings.TrimSpace(ev.Summary); summary != "" && summary != card.lastText {
			summary = mark + truncateIf(summary, subagentPromptMaxLen)
			w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryInfo, Text: summary}, summary)
		}
		card.finished = true
		card.state = state
		c.flush(card)
	}
}

func (c *subagentCards) newWriter(op subagentOp) *compactProgressWriter {
	label := subagentMarker
	if kind := strings.TrimSpace(op.event.Kind); kind != "" {
		label += kind + ": "
	}
	if title := strings.TrimSpace(op.event.Title); title != "" {
		label += title
	} else {
		label += "subagent"
	}
	w := newCompactProgressWriter(c.e.ctx, op.platform, op.replyCtx, label, c.e.i18n.CurrentLang(), nil)
	// Keep the label as written: the generic agent-name normalisation would
	// capitalise a subagent's task.
	w.agentName = label
	if w.minUpdateInterval < subagentCardInterval {
		w.minUpdateInterval = subagentCardInterval
	}
	return w
}

// subagentMarker prefixes a subagent card's title so it reads apart from the
// main agent's own progress card.
const subagentMarker = "↳ "

// due reports whether the card may be edited again: never edited yet, or
// its update interval since the last edit has passed.
func (w *compactProgressWriter) due() bool {
	return w.lastUpdateAt.IsZero() || time.Since(w.lastUpdateAt) >= w.minUpdateInterval
}

// flushPending sends content that the update interval held back, once the
// card is due.
func (w *compactProgressWriter) flushPending() {
	if !w.enabled || w.failed || w.handle == nil || w.content == "" || w.content == w.lastSent || !w.due() {
		return
	}
	callCtx, cancel := w.withAPITimeout()
	err := w.updater.UpdateMessage(callCtx, w.handle, w.content)
	cancel()
	if err != nil {
		slog.Warn("progress writer: pending UpdateMessage failed", "platform", w.platform.Name(), "style", w.style, "error", err)
		w.failed = true
		return
	}
	w.lastSent = w.content
	w.lastUpdateAt = time.Now()
}
