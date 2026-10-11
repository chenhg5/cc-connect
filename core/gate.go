package core

// maomao fork: fast-brain gate.
//
// Before a user message reaches the agent, an external gate service decides:
//   - "silent": keep it in a per-session buffer; it is delivered with the next message that goes through.
//   - "insert": supplementary info. Busy → written straight into the running turn (mid-turn
//               injection, like typing while Claude Code works). Idle → sent as a turn that may
//               answer NO_REPLY.
//   - "reply":  the user wants an answer. Busy → also injected into the running turn, whose
//               final reply then covers it. Idle → normal turn.
// Any gate error or timeout falls back to "reply", so the bot never goes deaf.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const insertNote = "（以下是补充信息，不是提问。若它不改变你之前的结论、也无需动作，只回复 NO_REPLY；否则简短回应。）"

type gateState struct {
	url     string
	timeout time.Duration
	mu      sync.Mutex
	buf     map[string][]string // interactiveKey → silent messages not yet delivered
	held    map[string]*heldMsg // interactiveKey → attachment-only message waiting for its text
}

// Discord (and most IM clients) send "image + caption" as two messages a few hundred ms
// apart. An attachment-only message is held this long so the caption that follows joins it
// in one turn, instead of starting a turn on a bare image and injecting the question later.
const attachmentHold = 3 * time.Second

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
}

type gateResponse struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// SetGate enables the gate; an empty url disables it.
func (e *Engine) SetGate(url string, timeoutMs int) {
	if url == "" {
		e.gate = nil
		return
	}
	if timeoutMs <= 0 {
		timeoutMs = 3000
	}
	e.gate = &gateState{url: url, timeout: time.Duration(timeoutMs) * time.Millisecond, buf: map[string][]string{}, held: map[string]*heldMsg{}}
	slog.Info("gate enabled", "url", url, "timeout_ms", timeoutMs)
}

func (g *gateState) decide(ctx context.Context, req gateRequest) gateResponse {
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	if err != nil {
		return gateResponse{Action: "reply", Reason: "gate request: " + err.Error()}
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		return gateResponse{Action: "reply", Reason: "gate unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	var out gateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		return gateResponse{Action: "reply", Reason: fmt.Sprintf("gate bad response: %d %v", resp.StatusCode, err)}
	}
	switch out.Action {
	case "reply", "insert", "silent":
		return out
	}
	return gateResponse{Action: "reply", Reason: "gate unknown action " + out.Action}
}

// applyGate runs the gate for msg. It returns true when the message has been fully handled
// (buffered or injected) and must not continue down the normal path. Otherwise it may have
// rewritten msg.Content (buffered context prepended, insert note) for the normal turn.
func (e *Engine) applyGate(p Platform, msg *Message, session *Session, interactiveKey string) bool {
	g := e.gate
	if g == nil || msg.IsPermissionResponse {
		return false
	}
	busy := session.Busy()

	g.mu.Lock()
	if h := g.held[interactiveKey]; h != nil && h.msg != msg {
		// A message arrived while an attachment waits: merge the attachments into it.
		h.timer.Stop()
		delete(g.held, interactiveKey)
		msg.Images = append(h.msg.Images, msg.Images...)
		msg.Files = append(h.msg.Files, msg.Files...)
	} else if h == nil && !busy && strings.TrimSpace(msg.Content) == "" && (len(msg.Images) > 0 || len(msg.Files) > 0) {
		h := &heldMsg{msg: msg}
		h.timer = time.AfterFunc(attachmentHold, func() {
			g.mu.Lock()
			still := g.held[interactiveKey] == h
			g.mu.Unlock()
			if still {
				e.handleMessage(p, msg) // nothing followed: process the attachment alone
			}
		})
		g.held[interactiveKey] = h
		g.mu.Unlock()
		slog.Info("gate: holding attachment for its caption", "session", msg.SessionKey)
		return true
	} else if h != nil {
		delete(g.held, interactiveKey) // timer re-dispatch of the held message itself
	}
	buffered := append([]string(nil), g.buf[interactiveKey]...)
	g.mu.Unlock()

	dec := g.decide(e.ctx, gateRequest{
		SessionKey: msg.SessionKey, Content: msg.Content, Mentioned: msg.Mentioned, Busy: busy,
		HasImages: len(msg.Images) > 0, HasFiles: len(msg.Files) > 0, Buffered: buffered,
	})
	slog.Info("gate decision", "session", msg.SessionKey, "action", dec.Action, "busy", busy,
		"mentioned", msg.Mentioned, "buffered", len(buffered), "reason", dec.Reason)

	// Attachments can't be buffered as text; never silence them.
	if dec.Action == "silent" && (len(msg.Images) > 0 || len(msg.Files) > 0) {
		dec.Action = "insert"
	}
	if dec.Action == "silent" {
		g.mu.Lock()
		g.buf[interactiveKey] = append(g.buf[interactiveKey], msg.Content)
		g.mu.Unlock()
		return true
	}

	content := msg.Content
	if len(buffered) > 0 {
		content = "（之前没回复的消息，按时间顺序）\n" + strings.Join(buffered, "\n") + "\n\n（最新）\n" + content
	}

	if busy && len(msg.Images) == 0 && len(msg.Files) == 0 && e.injectIntoRunningTurn(interactiveKey, content) {
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
	msg.Content = content
	return false
}

// injectIntoRunningTurn writes text to the live agent's stdin while a turn is in flight.
// Claude Code folds it into the current turn (one result), exactly like typing mid-turn.
func (e *Engine) injectIntoRunningTurn(interactiveKey, content string) bool {
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
	if err := as.Send(content, "", nil, nil); err != nil {
		slog.Warn("gate: mid-turn inject failed", "error", err)
		return false
	}
	slog.Info("gate: injected into running turn", "interactive_key", interactiveKey, "len", len(content))
	return true
}
