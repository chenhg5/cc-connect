package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.SessionHandoffReader = (*Agent)(nil)

// ReadSessionHandoff reads what a fresh context needs from a thread's rollout
// file (maomao fork): the last model reply's time and prompt size, the newest
// compaction summary and the dialogue text after it.
func (a *Agent) ReadSessionHandoff(sessionID string) (*core.SessionHandoff, error) {
	a.mu.RLock()
	codexHome := a.codexHome
	a.mu.RUnlock()
	path := findSessionFile(sessionID, codexHome)
	if path == "" {
		return nil, fmt.Errorf("codex: rollout file not found for %s", sessionID)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("codex: open rollout file: %w", err)
	}
	defer f.Close()
	h, err := parseHandoff(f)
	if err != nil {
		return nil, fmt.Errorf("codex: read rollout file: %w", err)
	}
	h.Path = path
	return h, nil
}

type rolloutLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// rolloutPayload holds the fields of the payload kinds the handoff reads:
// response_item messages, token_count/task_started events and compacted.
type rolloutPayload struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Phase   string `json:"phase"` // "commentary" | "final_answer" ("" on providers that don't set it)
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Meta *struct {
		Kinds []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
	Info *struct {
		Last struct {
			InputTokens int `json:"input_tokens"`
		} `json:"last_token_usage"`
	} `json:"info"`
	Message string `json:"message"` // compacted
}

// handoffParser walks a rollout once, keeping what the model's context holds
// after its newest compaction.
type handoffParser struct {
	h       core.SessionHandoff
	inReply bool // the last Tail entry is an assistant reply of the running turn
}

func parseHandoff(r io.Reader) (*core.SessionHandoff, error) {
	var p handoffParser
	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadBytes('\n') // tool outputs can be megabytes on one line
		if len(raw) > 0 {
			var l rolloutLine
			if json.Unmarshal(raw, &l) == nil {
				p.add(&l)
			}
		}
		if errors.Is(err, io.EOF) {
			return &p.h, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *handoffParser) add(l *rolloutLine) {
	switch l.Type {
	case "compacted", "event_msg", "response_item":
	default:
		return
	}
	if l.Type == "response_item" {
		// Images ride in user messages and in tool outputs (view_image) as
		// data URLs; every later request re-sends them.
		p.h.ImageBytes += imageDataBytes(l.Payload)
	}
	var pl rolloutPayload
	if json.Unmarshal(l.Payload, &pl) != nil {
		return
	}
	h := &p.h
	switch {
	case l.Type == "compacted":
		// The summary replaces everything before it. Codex also carries the
		// earlier user messages over verbatim; the summary already covers them.
		h.CompactSummary = cleanCompactSummary(pl.Message)
		h.CompactedAt = l.Timestamp
		h.Tail, p.inReply = nil, false
		// The compacted history is a text summary; the images before it are
		// no longer re-sent.
		h.ImageBytes = 0
		// Codex records no post-compaction size: the token_count it writes
		// right after reports 0 input tokens. The next reply sets the real one.
		h.ContextTokens = 0
	case l.Type == "event_msg" && pl.Type == "token_count":
		// One per model response; its input is the whole prompt (cached or not).
		if pl.Info != nil && pl.Info.Last.InputTokens > 0 {
			h.LastActivity = l.Timestamp
			h.ContextTokens = pl.Info.Last.InputTokens
		}
	case l.Type == "event_msg" && pl.Type == "task_started":
		p.inReply = false
	case l.Type == "response_item" && pl.Type == "message" && pl.Role == "user":
		p.addUser(&pl, l.Timestamp)
	case l.Type == "response_item" && pl.Type == "message" && pl.Role == "assistant":
		if pl.Phase == "commentary" {
			return // progress chatter before a tool call
		}
		text := outputText(&pl)
		if text == "" || text == "NO_REPLY" {
			return
		}
		e := core.HistoryEntry{Role: "assistant", Content: text, Timestamp: l.Timestamp}
		// Without phases, a turn's last message is the reply the channel saw.
		if n := len(h.Tail); p.inReply && n > 0 {
			h.Tail[n-1] = e
			return
		}
		h.Tail = append(h.Tail, e)
		p.inReply = true
	}
}

func (p *handoffParser) addUser(pl *rolloutPayload, ts time.Time) {
	text := userText(pl)
	if text == "" {
		return // AGENTS.md, environment context and other harness input
	}
	p.inReply = false // also a message steered into a running turn
	p.h.Tail = append(p.h.Tail, core.HistoryEntry{Role: "user", Content: text, Timestamp: ts})
}

// userText returns the text a user turn carries from cc-connect. Codex tags
// each content part with its kind; only "user.text" parts came from the
// prompt. Older rollouts have no kinds, so known harness tags are dropped too.
func userText(pl *rolloutPayload) string {
	var kinds []string
	if pl.Meta != nil && len(pl.Meta.Kinds) == len(pl.Content) {
		kinds = pl.Meta.Kinds
	}
	var parts []string
	for i, c := range pl.Content {
		if c.Type != "input_text" || (kinds != nil && kinds[i] != "user.text") {
			continue
		}
		t := strings.TrimSpace(c.Text)
		if t == "" || isHarnessText(t) {
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n\n")
}

func outputText(pl *rolloutPayload) string {
	var parts []string
	for _, c := range pl.Content {
		if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// isHarnessText reports text Codex itself put in a user turn. A Discord
// message may also start with "<" (a mention), so match known tags only.
func isHarnessText(text string) bool {
	for _, p := range []string{
		"# AGENTS.md", "#AGENTS.md", "<environment_context>", "<user_instructions>",
		"<turn_aborted>", "<image name=", "</image>", "<external_", "<user_shell_command>",
		"<subagent_notification>", "<skills_instructions>", "<permissions instructions>",
	} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// imageDataBytes counts the base64 characters of the data-URL images in a
// rollout payload.
func imageDataBytes(payload []byte) int {
	const marker = ";base64,"
	n := 0
	for {
		i := bytes.Index(payload, []byte(marker))
		if i < 0 {
			return n
		}
		head := payload[:i]
		payload = payload[i+len(marker):]
		if j := bytes.LastIndex(head, []byte("data:")); j < 0 || !bytes.HasPrefix(head[j:], []byte("data:image/")) {
			continue
		}
		k := 0
		for k < len(payload) && isBase64Char(payload[k]) {
			k++
		}
		n += k
		payload = payload[k:]
	}
}

func isBase64Char(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '='
}

// compactSummaryLead ends the preamble Codex puts before a compaction summary.
const compactSummaryLead = "use the information in this summary to assist with your own analysis:"

// cleanCompactSummary drops Codex's "another language model started to solve
// this problem" preamble, which would make a fresh context resume old work.
func cleanCompactSummary(s string) string {
	if i := strings.Index(s, compactSummaryLead); i >= 0 && i < 600 {
		s = s[i+len(compactSummaryLead):]
	}
	return strings.TrimSpace(s)
}
