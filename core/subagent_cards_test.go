package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// cardRecorderPlatform renders progress as structured card payloads and
// keeps the latest content of every card it was asked to show, by handle.
type cardRecorderPlatform struct {
	stubPlatformEngine
	cardsMu sync.Mutex
	order   []string
	latest  map[string]string
	edits   map[string][]time.Time // when each card was sent or edited
	gate    chan struct{}          // when set, sending a card waits for it
}

func (p *cardRecorderPlatform) ProgressStyle() string             { return "card" }
func (p *cardRecorderPlatform) SupportsProgressCardPayload() bool { return true }

func (p *cardRecorderPlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	if p.gate != nil {
		<-p.gate
	}
	p.cardsMu.Lock()
	defer p.cardsMu.Unlock()
	if p.latest == nil {
		p.latest = make(map[string]string)
		p.edits = make(map[string][]time.Time)
	}
	handle := fmt.Sprintf("card-%d", len(p.order)+1)
	p.order = append(p.order, handle)
	p.latest[handle] = content
	p.edits[handle] = append(p.edits[handle], time.Now())
	return handle, nil
}

func (p *cardRecorderPlatform) UpdateMessage(_ context.Context, handle any, content string) error {
	p.cardsMu.Lock()
	defer p.cardsMu.Unlock()
	p.latest[handle.(string)] = content
	p.edits[handle.(string)] = append(p.edits[handle.(string)], time.Now())
	return nil
}

// cards returns the latest payload of every card, keyed by its title.
func (p *cardRecorderPlatform) cards(t *testing.T) map[string]*ProgressCardPayload {
	t.Helper()
	p.cardsMu.Lock()
	defer p.cardsMu.Unlock()
	out := make(map[string]*ProgressCardPayload)
	for _, handle := range p.order {
		payload, ok := ParseProgressCardPayload(p.latest[handle])
		if !ok {
			t.Fatalf("card %s is not a progress payload: %q", handle, p.latest[handle])
		}
		if _, dup := out[payload.Agent]; dup {
			t.Fatalf("two cards titled %q", payload.Agent)
		}
		out[payload.Agent] = payload
	}
	return out
}

func waitForCards(t *testing.T, p *cardRecorderPlatform, ok func(map[string]*ProgressCardPayload) bool) map[string]*ProgressCardPayload {
	t.Helper()
	deadline := time.Now().Add(3 * subagentCardInterval)
	for {
		cards := p.cards(t)
		if ok(cards) || time.Now().After(deadline) {
			return cards
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func cardTexts(payload *ProgressCardPayload) []string {
	var out []string
	for _, item := range payload.Items {
		text := item.Text
		if item.Tool != "" {
			text = item.Tool + ": " + text
		}
		out = append(out, string(item.Kind)+"|"+text)
	}
	return out
}

func subagentEvent(ev SubagentEvent) Event {
	return Event{Type: EventSubagent, Subagent: &ev, Metadata: map[string]any{MetaParentToolUseID: ev.ID}}
}

const (
	cardA = subagentMarker + "general-purpose: Count to three"
	cardB = subagentMarker + "Explore: Find the config"
)

// TestSubagentCards_EachSubagentGetsItsOwnCard runs a turn in which the main
// agent spawns two subagents at once. Each must get a card of its own, with
// its own steps; the main agent's progress card must not carry them.
func TestSubagentCards_EachSubagentGetsItsOwnCard(t *testing.T) {
	p := &cardRecorderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()
	e.display.ThinkingMessages = true
	e.display.ToolMessages = true
	sessionKey := "test:subagent-cards"
	session := e.sessions.GetOrCreateActive(sessionKey)
	as := newControllableSession("sid")
	as.events = make(chan Event, 32)
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventToolUse, ToolName: "Agent", ToolInput: "Count to three"},
		{Type: EventToolUse, ToolName: "Agent", ToolInput: "Find the config"},
		subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStarted, Title: "Count to three", Kind: "general-purpose", Prompt: "count 1 2 3"}),
		subagentEvent(SubagentEvent{ID: "b", Phase: SubagentStarted, Title: "Find the config", Kind: "Explore", Prompt: "where is config.toml"}),
		subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "seq 3"}}),
		subagentEvent(SubagentEvent{ID: "b", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryThinking, Text: "look in config/"}}),
		subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: "A-SAYS 1 2 3"}}),
		subagentEvent(SubagentEvent{ID: "a", Phase: SubagentFinished, Status: "completed", Summary: "counted"}),
		subagentEvent(SubagentEvent{ID: "b", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: "B-LAST"}}),
		subagentEvent(SubagentEvent{ID: "b", Phase: SubagentFinished, Status: "failed", Summary: "B-LAST"}),
		{Type: EventToolResult, ToolResult: "1 2 3", ToolStatus: "completed"},
		{Type: EventResult, Content: "done", Done: true},
	} {
		as.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil, 0)

	cards := waitForCards(t, p, func(c map[string]*ProgressCardPayload) bool {
		return c[cardA] != nil && c[cardA].State == ProgressCardStateCompleted &&
			c[cardB] != nil && c[cardB].State == ProgressCardStateFailed
	})
	if got, want := strings.Join(cardTexts(cards[cardA]), "\n"), "info|count 1 2 3\ntool_use|Bash: seq 3\ninfo|A-SAYS 1 2 3\ninfo|✅ counted"; got != want {
		t.Errorf("card A = %v (state %s)\nwant %q", got, cards[cardA].State, want)
	}
	if got, want := strings.Join(cardTexts(cards[cardB]), "\n"), "info|where is config.toml\nthinking|look in config/\ninfo|B-LAST"; got != want || cards[cardB].State != ProgressCardStateFailed {
		t.Errorf("card B = %q (state %s), want %q failed", got, cards[cardB].State, want)
	}
	for title, card := range cards {
		if title == cardA || title == cardB {
			continue
		}
		for _, text := range cardTexts(card) {
			if strings.Contains(text, "A-SAYS") || strings.Contains(text, "look in config/") || strings.Contains(text, "seq 3") {
				t.Errorf("card %q carries a subagent step: %q", title, text)
			}
		}
	}
	for _, sent := range p.getSent() {
		if strings.Contains(sent, "A-SAYS") {
			t.Errorf("subagent text reached the reply: %q", sent)
		}
	}
}

// TestSubagentCards_BackgroundSubagentOutlivesTheTurn reproduces a background
// subagent that keeps working after the main agent ended its turn. Its card
// must keep updating from the unsolicited reader, without opening a turn of
// its own or sending anything else, and close when the subagent finishes.
func TestSubagentCards_BackgroundSubagentOutlivesTheTurn(t *testing.T) {
	p := &cardRecorderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()
	e.display.ToolMessages = true
	sessionKey := "test:subagent-bg"
	session := e.sessions.GetOrCreateActive(sessionKey)
	as := newControllableSession("sid-bg")
	as.events = make(chan Event, 32)
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[sessionKey] = state
	e.interactiveMu.Unlock()

	as.events <- Event{Type: EventToolUse, ToolName: "Agent", ToolInput: "Count to three"}
	as.events <- subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStarted, Title: "Count to three", Kind: "general-purpose", Background: true, Prompt: "count 1 2 3"})
	as.events <- Event{Type: EventToolResult, ToolResult: "Async agent launched", ToolStatus: "completed"}
	as.events <- Event{Type: EventResult, Content: "LAUNCHED", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil, 0)
	sentAfterTurn := len(p.getSent())

	e.startUnsolicitedReader(state, session, e.sessions, sessionKey, "")
	defer e.stopUnsolicitedReader(state)
	as.events <- subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryToolUse, Tool: "Bash", Text: "seq 3"}})
	as.events <- subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: "BG-FINAL"}})

	cards := waitForCards(t, p, func(c map[string]*ProgressCardPayload) bool {
		return c[cardA] != nil && len(c[cardA].Items) == 3
	})
	if got, want := strings.Join(cardTexts(cards[cardA]), "\n"), "info|count 1 2 3\ntool_use|Bash: seq 3\ninfo|BG-FINAL"; got != want || cards[cardA].State != ProgressCardStateRunning {
		t.Fatalf("card after the turn = %q (state %s), want %q running", got, cards[cardA].State, want)
	}

	as.events <- subagentEvent(SubagentEvent{ID: "a", Phase: SubagentFinished, Status: "completed"})
	cards = waitForCards(t, p, func(c map[string]*ProgressCardPayload) bool {
		return c[cardA].State == ProgressCardStateCompleted
	})
	if cards[cardA].State != ProgressCardStateCompleted {
		t.Fatalf("card state = %s, want completed", cards[cardA].State)
	}
	if sent := p.getSent(); len(sent) != sentAfterTurn {
		t.Errorf("subagent activity sent messages of its own: %q", sent[sentAfterTurn:])
	}
	if history := session.GetHistory(0); len(history) != 1 || history[0].Content != "LAUNCHED" {
		t.Errorf("history = %+v, want only the foreground turn", history)
	}

	// Woken again by a notification, the same card reopens.
	as.events <- subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStarted, Title: "Count to three", Kind: "general-purpose", Background: true})
	cards = waitForCards(t, p, func(c map[string]*ProgressCardPayload) bool {
		return c[cardA].State == ProgressCardStateRunning
	})
	if len(cards) != 2 || cards[cardA].State != ProgressCardStateRunning {
		t.Fatalf("after waking: %d cards, card A state %s; want the same card running again", len(cards), cards[cardA].State)
	}
}

// TestSubagentCards_DrainKeepsSubagentEvents covers the drains before a turn
// (queued message, resync): a background subagent's events sitting in the
// channel are not leftovers of the previous turn and must reach their card,
// or a dropped finish would leave the card running forever.
func TestSubagentCards_DrainKeepsSubagentEvents(t *testing.T) {
	p := &cardRecorderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()
	state := &interactiveState{platform: p, replyCtx: "ctx"}

	ch := make(chan Event, 8)
	ch <- subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStarted, Title: "Count to three", Kind: "general-purpose", Background: true, Prompt: "count 1 2 3"})
	ch <- Event{Type: EventError, Error: fmt.Errorf("stale")}
	ch <- subagentEvent(SubagentEvent{ID: "a", Phase: SubagentFinished, Status: "completed", Summary: "counted"})
	e.drainEventsKeepSubagents(state, ch)
	if len(ch) != 0 {
		t.Fatalf("%d events left in the channel", len(ch))
	}

	cards := waitForCards(t, p, func(c map[string]*ProgressCardPayload) bool {
		return c[cardA] != nil && c[cardA].State == ProgressCardStateCompleted
	})
	if got, want := strings.Join(cardTexts(cards[cardA]), "\n"), "info|count 1 2 3\ninfo|✅ counted"; got != want || cards[cardA].State != ProgressCardStateCompleted {
		t.Fatalf("card = %q (state %s), want %q completed", got, cards[cardA].State, want)
	}
}

// TestSubagentCards_BacklogKeepsStartsAndFinishes stalls the worker on a slow
// platform call while a chatty subagent floods it. Steps beyond the backlog
// may be dropped, but its start and finish must get through: a lost finish
// would leave the card running forever.
func TestSubagentCards_BacklogKeepsStartsAndFinishes(t *testing.T) {
	p := &cardRecorderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, gate: make(chan struct{})}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()
	state := &interactiveState{platform: p, replyCtx: "ctx"}

	e.handleSubagentEvent(state, subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStarted, Title: "Count to three", Kind: "general-purpose", Prompt: "count 1 2 3"}))
	// The worker is now stuck sending card A; flood it past the backlog.
	for i := 0; i < 2*subagentCardBacklog; i++ {
		e.handleSubagentEvent(state, subagentEvent(SubagentEvent{ID: "a", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: fmt.Sprintf("step %d", i)}}))
	}
	e.handleSubagentEvent(state, subagentEvent(SubagentEvent{ID: "b", Phase: SubagentStarted, Title: "Find the config", Kind: "Explore", Prompt: "where is config.toml"}))
	e.handleSubagentEvent(state, subagentEvent(SubagentEvent{ID: "a", Phase: SubagentFinished, Status: "completed"}))
	e.handleSubagentEvent(state, subagentEvent(SubagentEvent{ID: "b", Phase: SubagentFinished, Status: "completed"}))
	close(p.gate)

	cards := waitForCards(t, p, func(c map[string]*ProgressCardPayload) bool {
		return c[cardA] != nil && c[cardA].State == ProgressCardStateCompleted &&
			c[cardB] != nil && c[cardB].State == ProgressCardStateCompleted
	})
	if cards[cardA] == nil || cards[cardA].State != ProgressCardStateCompleted {
		t.Fatalf("card A = %+v, want it closed despite the flood", cards[cardA])
	}
	if cards[cardB] == nil || cards[cardB].State != ProgressCardStateCompleted || cardTexts(cards[cardB])[0] != "info|where is config.toml" {
		t.Fatalf("card B = %+v, want it titled, with its task, closed", cards[cardB])
	}
}

// TestSubagentCards_EditsStayApart checks the per-card rate limit: however
// the steps, held-back edits and the final close fall, two edits of one card
// are at least subagentCardInterval apart.
func TestSubagentCards_EditsStayApart(t *testing.T) {
	p := &cardRecorderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()
	state := &interactiveState{platform: p, replyCtx: "ctx"}

	send := func(ev SubagentEvent) { e.handleSubagentEvent(state, subagentEvent(ev)) }
	send(SubagentEvent{ID: "a", Phase: SubagentStarted, Title: "Count to three", Kind: "general-purpose", Prompt: "count 1 2 3"})
	time.Sleep(subagentCardInterval - 10*time.Millisecond)
	// Card B opens just before the worker's ticks line up with card A's.
	send(SubagentEvent{ID: "b", Phase: SubagentStarted, Title: "Find the config", Kind: "Explore", Prompt: "where is config.toml"})
	for i := 0; i < 5; i++ {
		send(SubagentEvent{ID: "a", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: fmt.Sprintf("a %d", i)}})
		send(SubagentEvent{ID: "b", Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: fmt.Sprintf("b %d", i)}})
		time.Sleep(300 * time.Millisecond)
	}
	send(SubagentEvent{ID: "a", Phase: SubagentFinished, Status: "completed"})
	send(SubagentEvent{ID: "b", Phase: SubagentFinished, Status: "completed"})

	cards := waitForCards(t, p, func(c map[string]*ProgressCardPayload) bool {
		return c[cardA] != nil && c[cardA].State == ProgressCardStateCompleted &&
			c[cardB] != nil && c[cardB].State == ProgressCardStateCompleted
	})
	if cards[cardA].State != ProgressCardStateCompleted || cards[cardB].State != ProgressCardStateCompleted {
		t.Fatalf("cards not closed: A %s, B %s", cards[cardA].State, cards[cardB].State)
	}
	if got := len(cards[cardB].Items); got != 6 {
		t.Errorf("card B has %d items, want its task and 5 steps", got)
	}
	p.cardsMu.Lock()
	defer p.cardsMu.Unlock()
	for handle, times := range p.edits {
		for i := 1; i < len(times); i++ {
			if gap := times[i].Sub(times[i-1]); gap < subagentCardInterval {
				t.Errorf("card %s edited %v after its previous edit, want at least %v", handle, gap, subagentCardInterval)
			}
		}
	}
}

// Late steps cannot mutate a terminal card, whether its close is still pending
// or already sent. A subsequent explicit start can reopen the same card.
func TestSubagentCards_IgnoresStepsAfterFinishUntilRestart(t *testing.T) {
	for _, closeSent := range []bool{false, true} {
		t.Run(fmt.Sprintf("closeSent=%v", closeSent), func(t *testing.T) {
			p := &cardRecorderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			defer func() { _ = e.Stop() }()
			c := &subagentCards{e: e, cards: make(map[string]*subagentCard)}
			apply := func(ev SubagentEvent) {
				ev.ID = "a"
				c.apply(subagentOp{event: ev, platform: p, replyCtx: "ctx"})
			}
			apply(SubagentEvent{Phase: SubagentStarted, Title: "Count to three", Kind: "general-purpose", Prompt: "count"})
			card := c.cards["a"]
			// Drive the worker synchronously and move its send timestamp rather
			// than sleeping for the platform edit interval.
			card.writer.lastUpdateAt = time.Now()
			apply(SubagentEvent{Phase: SubagentFinished, Status: "completed", Summary: "done"})
			if closeSent {
				card.writer.lastUpdateAt = time.Time{}
				c.flush(card)
			}
			content, lastText := card.writer.content, card.lastText
			apply(SubagentEvent{Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: "late"}})
			if card.writer.content != content || card.lastText != lastText {
				t.Fatal("late step mutated a finished card")
			}
			card.writer.lastUpdateAt = time.Time{}
			c.flush(card)
			if payload := p.cards(t)[cardA]; payload == nil || payload.State != ProgressCardStateCompleted || strings.Contains(strings.Join(cardTexts(payload), "\n"), "late") {
				t.Fatalf("unexpected completed card: %+v", payload)
			}
			apply(SubagentEvent{Phase: SubagentStarted})
			apply(SubagentEvent{Phase: SubagentStep, Step: ProgressCardEntry{Kind: ProgressEntryInfo, Text: "resumed"}})
			card.writer.lastUpdateAt = time.Time{}
			c.flush(card)
			payload := p.cards(t)[cardA]
			if payload.State != ProgressCardStateRunning || !strings.Contains(strings.Join(cardTexts(payload), "\n"), "resumed") || c.open != 1 {
				t.Fatalf("restarted card did not resume: %+v (open=%d)", payload, c.open)
			}
		})
	}
}
