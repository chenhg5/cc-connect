package claudecode

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// ReadSessionHandoff reads what a fresh context needs from a session's
// transcript (maomao fork): the last reply's time and prompt size, the newest
// compaction summary and the dialogue text after it.
func (a *Agent) ReadSessionHandoff(sessionID string) (*core.SessionHandoff, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	absWorkDir, _ := filepath.Abs(workDir)
	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return nil, fmt.Errorf("claudecode: project dir not found")
	}
	path := filepath.Join(projectDir, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("claudecode: open session file: %w", err)
	}
	defer f.Close()
	h, err := parseHandoff(f)
	if err != nil {
		return nil, fmt.Errorf("claudecode: read session file: %w", err)
	}
	h.Path = path
	return h, nil
}

type handoffLine struct {
	Type             string    `json:"type"`
	Subtype          string    `json:"subtype"`
	UUID             string    `json:"uuid"`
	Timestamp        time.Time `json:"timestamp"`
	IsSidechain      bool      `json:"isSidechain"`
	IsMeta           bool      `json:"isMeta"`
	IsCompactSummary bool      `json:"isCompactSummary"`
	CompactMetadata  *struct {
		PostTokens        int `json:"postTokens"`
		PreservedMessages struct {
			UUIDs []string `json:"uuids"`
		} `json:"preservedMessages"`
	} `json:"compactMetadata"`
	Attachment *struct {
		Type        string          `json:"type"`
		Prompt      json.RawMessage `json:"prompt"` // a string, or content blocks when it has images
		CommandMode string          `json:"commandMode"`
	} `json:"attachment"`
	Message struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// handoffParser walks a transcript once, keeping what the model's context
// holds after its newest compaction.
type handoffParser struct {
	h       core.SessionHandoff
	uuids   []string // uuid of each Tail entry, to keep compaction's preserved messages
	inReply bool     // the last Tail entry is an assistant reply still being written
}

func parseHandoff(r io.Reader) (*core.SessionHandoff, error) {
	var p handoffParser
	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadBytes('\n') // tool results can be megabytes on one line
		if len(raw) > 0 {
			var l handoffLine
			if json.Unmarshal(raw, &l) == nil && !l.IsSidechain {
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

func (p *handoffParser) add(l *handoffLine) {
	h := &p.h
	switch {
	case l.Type == "system" && l.Subtype == "compact_boundary":
		// What came before is in the summary that follows, except the messages
		// compaction kept verbatim.
		keep := map[string]bool{}
		if m := l.CompactMetadata; m != nil {
			for _, id := range m.PreservedMessages.UUIDs {
				keep[id] = true
			}
			h.ContextTokens = m.PostTokens
		}
		var tail []core.HistoryEntry
		var uuids []string
		for i, e := range h.Tail {
			if keep[p.uuids[i]] {
				tail, uuids = append(tail, e), append(uuids, p.uuids[i])
			}
		}
		h.Tail, p.uuids, p.inReply = tail, uuids, false
		h.CompactedAt = l.Timestamp
	case l.Type == "user" && l.IsCompactSummary:
		h.CompactSummary = cleanCompactSummary(handoffText(l.Message.Content))
	case l.Type == "attachment" && l.Attachment != nil && l.Attachment.Type == "queued_command":
		// A message inserted into a running turn is only recorded here.
		if l.Attachment.CommandMode == "prompt" {
			p.addUser(l.UUID, handoffText(l.Attachment.Prompt), l.Timestamp)
		} else {
			p.inReply = false // a task notification: what follows is a new reply
		}
	case l.Type == "user" && !l.IsMeta:
		p.addUser(l.UUID, handoffText(l.Message.Content), l.Timestamp)
	case l.Type == "assistant" && l.Message.Model != "<synthetic>": // not API errors cc-connect reports itself
		if u := l.Message.Usage; u != nil && u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens > 0 {
			h.LastActivity = l.Timestamp
			h.ContextTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		}
		text := handoffText(l.Message.Content)
		if text == "" || text == "NO_REPLY" {
			return
		}
		// Text before a tool call is progress chatter; the turn's last text
		// block is the reply the channel saw.
		if n := len(h.Tail); p.inReply && n > 0 {
			h.Tail[n-1] = core.HistoryEntry{Role: "assistant", Content: text, Timestamp: l.Timestamp}
			p.uuids[n-1] = l.UUID
			return
		}
		h.Tail = append(h.Tail, core.HistoryEntry{Role: "assistant", Content: text, Timestamp: l.Timestamp})
		p.uuids = append(p.uuids, l.UUID)
		p.inReply = true
	}
}

func (p *handoffParser) addUser(uuid, text string, ts time.Time) {
	if text == "" {
		return // tool result: the same turn goes on
	}
	p.inReply = false // a new turn, even when it is a task notification
	if isHarnessText(text) {
		return
	}
	// An inserted message may be recorded both as an attachment and a user line.
	for i := len(p.h.Tail) - 1; i >= 0 && i >= len(p.h.Tail)-4; i-- {
		if p.h.Tail[i].Role == "user" && p.h.Tail[i].Content == text {
			return
		}
	}
	p.h.Tail = append(p.h.Tail, core.HistoryEntry{Role: "user", Content: text, Timestamp: ts})
	p.uuids = append(p.uuids, uuid)
}

// cleanCompactSummary drops Claude Code's framing around a compaction summary:
// the "being continued" preamble and the "continue the last task" instructions
// would make a fresh context resume old work hours later.
func cleanCompactSummary(s string) string {
	if i := strings.Index(s, "\nSummary:"); i >= 0 && i < 400 {
		s = s[i+1:]
	}
	for _, tail := range []string{"\nIf you need specific details from before compaction", "\nContinue the conversation from where it left off"} {
		if i := strings.Index(s, tail); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// handoffText returns the text blocks of a message ("" for tool use/results).
func handoffText(content json.RawMessage) string {
	if s := extractStringContent(content); s != "" {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// isHarnessText reports text Claude Code itself put in a user turn. A Discord
// message may also start with "<" (a mention), so match the known tags only.
func isHarnessText(text string) bool {
	for _, p := range []string{"<task-notification>", "<command-", "<local-command-", "<system-reminder>", "<bash-"} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}
