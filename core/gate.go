package core

// maomao fork: fast-brain gate.
//
// Before a user message reaches the agent, an external gate service decides:
//   - "silent": keep it in a per-session buffer; it is delivered with the next message that goes through.
//   - "insert": supplementary info. Busy → written straight into the running turn (mid-turn
//               injection, like typing while Claude Code works). Idle → sent as a turn that may
//               answer NO_REPLY.
//   - "context": like silent, but attachments don't force a turn (they are noted as text). For
//               sub agents that hear every message but only speak when @-ed (main + subs setup).
//   - "reply":  the user wants an answer. Busy → also injected into the running turn, whose
//               final reply then covers it. Idle → normal turn.
// Any gate error or timeout falls back to gate_fallback (default "reply", so the bot never goes deaf).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const insertNote = "（以下是补充信息，不是提问。若它不改变你之前的结论、也无需动作，只回复 NO_REPLY；否则简短回应。）"

type gateState struct {
	url      string
	timeout  time.Duration
	fallback string // action on gate error/timeout
	inject   bool   // agent folds mid-turn input into the running turn (claudecode only)
	mu       sync.Mutex
	buf      map[string][]string // interactiveKey → silent messages not yet delivered
	held     map[string]*heldMsg // interactiveKey → burst being collected while idle
	released map[*Message]bool   // bursts handed back to handleMessage by their timer
}

// Discord (and most IM clients) split "image + caption" into two messages, in either order,
// and people often send a few lines in a row. When idle, a message is held burstWindow
// (attachment-only: attachmentHold) and anything arriving meanwhile joins it, so a burst
// becomes one turn instead of a turn plus an injection that ends up answered twice.
// maxBuffered caps the silent buffer; subs hear every message and may stay quiet for a long time.
const maxBuffered = 30

const (
	burstWindow    = 1500 * time.Millisecond
	attachmentHold = 3 * time.Second
)

type heldMsg struct {
	msg   *Message
	timer *time.Timer
}

type gateRequest struct {
	SessionKey string   `json:"session_key"`
	Content    string   `json:"content"`
	Mentioned  bool     `json:"mentioned"`
	Busy       bool     `json:"busy"`
	HasImages  bool     `json:"has_images"`
	HasFiles   bool     `json:"has_files"`
	Buffered   []string `json:"buffered"`
	// LastReply is the tail of this bot's last answer in the session, so the gate can tell
	// "可以" answering its question from idle chatter. LastReplyAge is seconds since then.
	LastReply    string `json:"last_reply,omitempty"`
	LastReplyAge int    `json:"last_reply_age_s,omitempty"`
}

const lastReplyMaxRunes = 600

// lastReply returns the tail of the session's latest assistant message and its age in seconds.
func lastReply(session *Session) (string, int) {
	h := session.GetHistory(4)
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role != "assistant" {
			continue
		}
		r := []rune(strings.TrimSpace(h[i].Content))
		if len(r) > lastReplyMaxRunes {
			r = r[len(r)-lastReplyMaxRunes:]
		}
		return string(r), int(time.Since(h[i].Timestamp).Seconds())
	}
	return "", 0
}

type gateResponse struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// SetGate enables the gate; an empty url disables it. fallback is the action used when the gate
// errors or times out ("" = "reply").
func (e *Engine) SetGate(url string, timeoutMs int, fallback string) {
	if url == "" {
		e.gate = nil
		return
	}
	if timeoutMs <= 0 {
		timeoutMs = 3000
	}
	switch fallback {
	case "reply", "insert", "silent", "context":
	default:
		fallback = "reply"
	}
	// Claude Code merges stdin input into the running turn, Codex app-server via turn/steer;
	// codex exec would start a second process, so other agents get busy inserts as a queued turn.
	inject := e.agent != nil && e.agent.Name() == "claudecode"
	if m, ok := e.agent.(interface{ SupportsMidTurnInput() bool }); ok && m.SupportsMidTurnInput() {
		inject = true
	}
	e.gate = &gateState{url: url, timeout: time.Duration(timeoutMs) * time.Millisecond, fallback: fallback, inject: inject,
		buf: map[string][]string{}, held: map[string]*heldMsg{}, released: map[*Message]bool{}}
	slog.Info("gate enabled", "url", url, "timeout_ms", timeoutMs, "fallback", fallback, "mid_turn_inject", inject)
}

func (g *gateState) decide(ctx context.Context, req gateRequest) gateResponse {
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	if err != nil {
		return gateResponse{Action: g.fallback, Reason: "gate request: " + err.Error()}
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return gateResponse{Action: g.fallback, Reason: "gate unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	var out gateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		return gateResponse{Action: g.fallback, Reason: fmt.Sprintf("gate bad response: %d %v", resp.StatusCode, err)}
	}
	switch out.Action {
	case "reply", "insert", "silent", "context":
		return out
	}
	return gateResponse{Action: g.fallback, Reason: "gate unknown action " + out.Action}
}

// applyGate runs the gate for msg. It returns true when the message has been fully handled
// (buffered or injected) and must not continue down the normal path. Otherwise it may have
// rewritten msg.Content (buffered context prepended, insert note) for the normal turn.
func (e *Engine) applyGate(p Platform, msg *Message, session *Session, interactiveKey string) bool {
	g := e.gate
	if msg.FromBot && !msg.Mentioned && e.isTeamLead() && isBotErrorNotice(msg.Content) {
		// maomao fork: a teammate reporting a failure is the lead's job, not background chatter.
		msg.Mentioned = true
		msg.Content = "（队友 " + msg.UserName + " 报错了。你是负责人：查原因，告诉用户，必要时换人接手）\n" + msg.Content
	}
	if msg.FromBot && !msg.Mentioned {
		// A teammate talking to someone else: only listen. Never start a turn from it, gate or
		// not, so bots can't chain each other into a loop.
		if g != nil {
			g.bufferContext(interactiveKey, msg)
		}
		slog.Info("gate: teammate context", "session", msg.SessionKey, "from", msg.UserName, "len", len(msg.Content))
		return true
	}
	if g == nil || msg.IsPermissionResponse {
		if !msg.IsPermissionResponse {
			e.teamNoteTurn(msg, session)
		}
		return false
	}
	busy := session.Busy()

	g.mu.Lock()
	if g.released[msg] {
		delete(g.released, msg) // the burst's timer handed it back: process normally
	} else if h := g.held[interactiveKey]; h != nil {
		// Same burst: fold this message into the held one and wait a bit longer.
		h.msg.Content = strings.TrimSpace(h.msg.Content + "\n" + msg.Content)
		h.msg.Images = append(h.msg.Images, msg.Images...)
		h.msg.Files = append(h.msg.Files, msg.Files...)
		h.msg.Mentioned = h.msg.Mentioned || msg.Mentioned
		h.msg.SeenUpToID = msg.MessageID
		h.timer.Reset(burstWindow)
		g.mu.Unlock()
		return true
	} else if !busy {
		window := burstWindow
		if strings.TrimSpace(msg.Content) == "" && (len(msg.Images) > 0 || len(msg.Files) > 0) {
			window = attachmentHold
		}
		h := &heldMsg{msg: msg}
		h.timer = time.AfterFunc(window, func() {
			g.mu.Lock()
			delete(g.held, interactiveKey)
			g.released[h.msg] = true
			g.mu.Unlock()
			e.handleMessage(p, h.msg)
		})
		g.held[interactiveKey] = h
		g.mu.Unlock()
		return true
	}
	buffered := append([]string(nil), g.buf[interactiveKey]...)
	g.mu.Unlock()

	last, lastAge := lastReply(session)
	dec := g.decide(e.ctx, gateRequest{
		SessionKey: msg.SessionKey, Content: msg.Content, Mentioned: msg.Mentioned, Busy: busy,
		HasImages: len(msg.Images) > 0, HasFiles: len(msg.Files) > 0, Buffered: buffered,
		LastReply: last, LastReplyAge: lastAge,
	})
	slog.Info("gate decision", "session", msg.SessionKey, "action", dec.Action, "busy", busy,
		"mentioned", msg.Mentioned, "buffered", len(buffered), "reason", dec.Reason)

	// Attachments can't be buffered as text; never silence them.
	if dec.Action == "silent" && (len(msg.Images) > 0 || len(msg.Files) > 0) {
		dec.Action = "insert"
	}
	if dec.Action == "context" {
		g.bufferContext(interactiveKey, msg)
		return true
	}
	if dec.Action == "silent" {
		g.buffer(interactiveKey, msg.Content)
		return true
	}
	e.teamNoteTurn(msg, session)

	if busy && g.inject {
		if idle := e.turnIdleFor(interactiveKey); idle >= deadTurnAfter {
			// maomao fork: don't pretend to deliver into a turn that has stopped moving.
			// The message stays in the channel; backfill hands it to the next turn
			// (so it must not be marked seen).
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("⚠️ 当前这一轮已经 %d 分钟没有动静，可能卡住了，这条没插进去；发 /new 可重置", int(idle.Minutes())))
			return true
		}
	}
	content := msg.Content
	if back, ok := e.channelBackfill(p, msg, session); ok {
		// The channel itself is the record: it covers the gate buffer and also
		// what the buffer never saw (downtime, restarts, dropped messages).
		if back != "" {
			content = back + "\n\n（最新，回这条）\n" + content
		}
	} else if len(buffered) > 0 {
		content = "（之前没回复的消息，按时间顺序）\n" + strings.Join(buffered, "\n") + "\n\n（最新）\n" + content
	}
	markSeen(session, msg)

	inject := content
	if strings.TrimSpace(inject) == "" {
		inject = "（补充附件，和刚才的消息是一起发的）"
	}
	if busy && g.inject && e.injectIntoRunningTurn(interactiveKey, inject, msg.Images, msg.Files) {
		g.mu.Lock()
		g.buf[interactiveKey] = g.buf[interactiveKey][len(buffered):]
		g.mu.Unlock()
		e.reply(p, msg.ReplyCtx, "↪ 已插入")
		return true
	}

	g.mu.Lock()
	g.buf[interactiveKey] = g.buf[interactiveKey][len(buffered):]
	g.mu.Unlock()
	if dec.Action == "insert" {
		content = insertNote + "\n" + content
	}
	msg.TurnCtx, msg.TurnCtxQuery = true, msg.Content
	msg.Content = content
	return false
}

// buffer keeps text for the next turn of interactiveKey (capped at maxBuffered).
func (g *gateState) buffer(interactiveKey, text string) {
	g.mu.Lock()
	b := append(g.buf[interactiveKey], text)
	if len(b) > maxBuffered {
		b = b[len(b)-maxBuffered:]
	}
	g.buf[interactiveKey] = b
	g.mu.Unlock()
}

// bufferContext buffers msg as listen-only context; attachments become a note.
func (g *gateState) bufferContext(interactiveKey string, msg *Message) {
	text := msg.Content
	if n := len(msg.Images) + len(msg.Files); n > 0 {
		text = strings.TrimSpace(text + fmt.Sprintf("（附了 %d 个图片/文件）", n))
	}
	if text != "" {
		g.buffer(interactiveKey, text)
	}
}

// injectIntoRunningTurn writes text (and any attachments) to the live agent's stdin while a
// turn is in flight; covers "text first, image 300ms later" splits too.
// Claude Code folds it into the current turn (one result), exactly like typing mid-turn.
func (e *Engine) injectIntoRunningTurn(interactiveKey, content string, images []ImageAttachment, files []FileAttachment) bool {
	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	as := state.agentSession
	state.mu.Unlock()
	if as == nil || !as.Alive() {
		return false
	}
	if err := as.Send(content, "", images, files); err != nil {
		slog.Warn("gate: mid-turn inject failed", "error", err)
		return false
	}
	slog.Info("gate: injected into running turn", "interactive_key", interactiveKey, "len", len(content))
	return true
}

// deadTurnAfter is how long a running turn may go without agent events before
// mid-turn inserts are refused (matches the stuck-turn watchdog).
var deadTurnAfter = 10 * time.Minute

// turnIdleFor returns how long the running turn of interactiveKey has had no agent event.
func (e *Engine) turnIdleFor(interactiveKey string) time.Duration {
	e.interactiveMu.Lock()
	state := e.interactiveStates[interactiveKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return 0
	}
	last := state.lastEventAt.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// isBotErrorNotice reports whether a teammate message is an error report.
func isBotErrorNotice(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "❌") || strings.HasPrefix(t, "⚠️")
}

// ---- maomao fork: middle and long context layers (served by the gate process) ----

type gateContextResponse struct {
	Summary string `json:"summary"`
	Wiki    []struct {
		Path string `json:"path"`
		Desc string `json:"desc"`
	} `json:"wiki"`
}

// endpoint returns the gate URL with its path replaced (query dropped).
func (g *gateState) endpoint(path string) string {
	base := g.url
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	if i := strings.LastIndex(base, "/"); i > len("http://") {
		base = base[:i]
	}
	return base + path
}

func (g *gateState) post(ctx context.Context, path string, req any, out any) error {
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// turnContext returns the channel's progress summary and matching wiki pages as a
// block to put before the user's message ("" when the gate has nothing or is down).
// Each block goes into an agent context once: the summary on the first turn after
// a new session or a compaction, a wiki page the first time it matches. Repeating
// them every turn filled long sessions with dozens of near-identical copies.
func (e *Engine) turnContext(session *Session, sessionKey, text string) string {
	g := e.gate
	if g == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	var out gateContextResponse
	if err := g.post(ctx, "/context", map[string]string{"key": sessionKey, "text": text}, &out); err != nil {
		slog.Warn("gate: context fetch failed", "session", sessionKey, "error", err)
		return ""
	}
	summary := strings.TrimSpace(out.Summary)
	summaryHead := "（本频道进展总结，跨会话、重启都保留；以频道记录为准，有冲突时信频道）\n"
	var wiki []string
	session.UpdateTurnCtx(func(t *TurnCtxSent) {
		if summary == "" {
			return
		}
		lines := summaryLines(summary)
		if t.Summary {
			// The context has an earlier version: send only the new entries.
			summary = summaryDelta(t.SummaryLines, lines)
			summaryHead = "（本频道进展总结有更新，下面是新增或改动的条目）\n"
		}
		t.Summary, t.SummaryLines = true, lines
	})
	session.UpdateTurnCtx(func(t *TurnCtxSent) {
		for _, w := range out.Wiki {
			if !slices.Contains(t.Wiki, w.Path) {
				t.Wiki = append(t.Wiki, w.Path)
				wiki = append(wiki, "\n- "+w.Path+" — "+w.Desc)
			}
		}
	})
	var b strings.Builder
	if summary != "" {
		b.WriteString(summaryHead)
		b.WriteString(summary)
	}
	if len(wiki) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("（可能相关的 wiki 页，需要时去读）")
		b.WriteString(strings.Join(wiki, ""))
	}
	return b.String()
}

// withTurnBlocks puts the teammate status, progress summary and wiki pages the
// agent's context does not hold yet before content. Called right before the
// send, so a message that never reaches the agent marks nothing as sent.
func (e *Engine) withTurnBlocks(session *Session, sessionKey, query, content string) string {
	if c := e.turnContext(session, sessionKey, query); c != "" {
		content = c + "\n\n" + content
	}
	if sum := e.teamBlock(session, sessionKey); sum != "" {
		content = sum + "\n\n" + content
	}
	return content
}

func summaryLines(summary string) []string {
	var out []string
	for _, l := range strings.Split(summary, "\n") {
		if l = strings.TrimRight(l, " "); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// summaryDelta returns the entries of cur not in prev, each under its "## "
// section heading ("" when nothing is new).
func summaryDelta(prev, cur []string) string {
	seen := make(map[string]bool, len(prev))
	for _, l := range prev {
		seen[l] = true
	}
	var b strings.Builder
	heading, headingDone := "", false
	for _, l := range cur {
		if strings.HasPrefix(l, "#") {
			heading, headingDone = l, false
			continue
		}
		if seen[l] {
			continue
		}
		if heading != "" && !headingDone {
			b.WriteString(heading + "\n")
			headingDone = true
		}
		b.WriteString(l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// teamBlock returns the teammate status when it changed since the agent's
// context last got it (maomao fork).
func (e *Engine) teamBlock(session *Session, sessionKey string) string {
	if !e.isTeamLead() {
		return ""
	}
	sum, key := e.teamSummary(sessionKey)
	if sum == "" {
		sum = "（队友状态，系统自动附上，不用回应）\n现在都没有在忙或刚做完的事"
	}
	send := false
	session.UpdateTurnCtx(func(t *TurnCtxSent) {
		if t.Team != key {
			t.Team, send = key, true
		}
	})
	if !send {
		return ""
	}
	return sum
}

// reportTurn hands a finished turn to the gate so it can update the channel summary.
func (e *Engine) reportTurn(sessionKey, user, reply string) {
	g := e.gate
	if g == nil || strings.TrimSpace(reply) == "" || isSilentReply(reply) {
		return
	}
	bot := e.name
	if e.team != nil && e.team.name != "" {
		bot = e.team.name
	}
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		defer cancel()
		if err := g.post(ctx, "/turn", map[string]string{"key": sessionKey, "bot": bot, "user": user, "reply": reply}, nil); err != nil {
			slog.Warn("gate: turn report failed", "session", sessionKey, "error", err)
		}
	}()
}

// lastUserEntry returns the newest user entry in the session history.
func lastUserEntry(session *Session) string {
	h := session.GetHistory(4)
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == "user" {
			return h[i].Content
		}
	}
	return ""
}
