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
	// subagentCardInterval bounds how often one card is edited, so a chatty
	// subagent (or many in parallel) stays under platform rate limits. A held
	// back edit is sent on the next tick, so a card never lags by more.
	subagentCardInterval = 2 * time.Second
	// subagentCardQueue is how many events may wait for the worker.
	subagentCardQueue = 512
	// subagentCardIdle ends a worker whose subagents went silent without
	// finishing (e.g. the agent process died); their cards stay as they are.
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
	writer   *compactProgressWriter
	finished bool
	lastText string // the subagent's latest text step
}

// subagentCards owns the subagent progress cards of one conversation. Events
// are queued by the readers and applied by a single worker goroutine, which
// also sends held-back edits; the worker exits once no card is open.
type subagentCards struct {
	e *Engine

	mu      sync.Mutex
	queue   chan subagentOp
	running bool
	open    int // cards not yet finished; written by the worker under mu

	cards map[string]*subagentCard // worker only
}

func (e *Engine) subagentCardsFor(state *interactiveState) *subagentCards {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.subagents == nil {
		state.subagents = &subagentCards{
			e:     e,
			queue: make(chan subagentOp, subagentCardQueue),
			cards: make(map[string]*subagentCard),
		}
	}
	return state.subagents
}

// handleSubagentEvent routes one EventSubagent to the conversation's cards.
// It never blocks the reader: when the worker falls far behind, the event is
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

func (c *subagentCards) enqueue(op subagentOp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Sent under mu so the worker's exit check, also under mu, never misses
	// a queued event.
	select {
	case c.queue <- op:
	default:
		slog.Warn("subagent cards: queue full, dropping event", "subagent", op.event.ID, "phase", op.event.Phase)
		return
	}
	if !c.running {
		c.running = true
		go c.run()
	}
}

func (c *subagentCards) run() {
	ticker := time.NewTicker(subagentCardInterval)
	defer ticker.Stop()
	lastEvent := time.Now()
	for {
		select {
		case <-c.e.ctx.Done():
			c.stop()
			return
		case op := <-c.queue:
			lastEvent = time.Now()
			c.apply(op)
		case <-ticker.C:
			for _, card := range c.cards {
				if !card.finished {
					card.writer.flushPending()
				}
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
	if len(c.queue) > 0 || (c.open > 0 && !silent) {
		return false
	}
	if c.open > 0 {
		slog.Info("subagent cards: subagents went silent without finishing", "open", c.open)
		for id, card := range c.cards {
			if !card.finished {
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
			c.setOpen(1)
			w.Finalize(ProgressCardStateRunning)
		}
		if prompt := strings.TrimSpace(ev.Prompt); prompt != "" {
			prompt = truncateIf(prompt, subagentPromptMaxLen)
			w.AppendStructured(ProgressCardEntry{Kind: ProgressEntryInfo, Text: prompt}, prompt)
		}
	case SubagentStep:
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
		w.flushPending()
		w.Finalize(state)
		card.finished = true
		c.setOpen(-1)
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

// flushPending sends content that the update interval held back.
func (w *compactProgressWriter) flushPending() {
	if !w.enabled || w.failed || w.handle == nil || w.content == "" || w.content == w.lastSent {
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
