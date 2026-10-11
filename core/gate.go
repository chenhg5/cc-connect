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
	// Only Claude Code merges stdin input into the running turn; codex exec would start a second
	// process, so other agents get busy inserts as a queued turn instead.
	inject := e.agent != nil && e.agent.Name() == "claudecode"
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
	if g == nil || msg.IsPermissionResponse {
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
	if dec.Action == "context" {
		dec.Action = "silent"
		if n := len(msg.Images) + len(msg.Files); n > 0 {
			msg.Content = strings.TrimSpace(msg.Content + fmt.Sprintf("（附了 %d 个图片/文件）", n))
		}
	}
	if dec.Action == "silent" {
		g.mu.Lock()
		b := append(g.buf[interactiveKey], msg.Content)
		if len(b) > maxBuffered {
			b = b[len(b)-maxBuffered:]
		}
		g.buf[interactiveKey] = b
		g.mu.Unlock()
		return true
	}

	content := msg.Content
	if len(buffered) > 0 {
		content = "（之前没回复的消息，按时间顺序）\n" + strings.Join(buffered, "\n") + "\n\n（最新）\n" + content
	}

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
	msg.Content = content
	return false
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
