package feishu

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// feedbackCardLimit bounds the remembered reply cards; the oldest go first.
const feedbackCardLimit = 1000

// feedbackCard is what a click needs to draw a reply card again with the
// last choice highlighted: what the card says, and what was chosen on it.
type feedbackCard struct {
	SessionKey string `json:"session_key"`
	Markdown   string `json:"markdown,omitempty"`
	Footer     string `json:"footer,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
	Comment    string `json:"comment,omitempty"`
	// Which parts the card was drawn without, so a redraw keeps its shape.
	// A card stored without these flags carries both.
	NoTurn   bool  `json:"no_turn,omitempty"`
	NoResult bool  `json:"no_result,omitempty"`
	At       int64 `json:"at"`
}

// sections are the parts the card carries.
func (c feedbackCard) sections() feedbackSections {
	return feedbackSections{Turn: !c.NoTurn, Result: !c.NoResult}
}

// feedbackCards remembers the reply cards that carry the controls, by
// message id. Kept in a file (turn_feedback_state) when one is configured,
// so a card clicked after the bridge restarted still lights up; without one
// it lives in memory only.
type feedbackCards struct {
	mu     sync.Mutex
	path   string
	loaded bool
	items  map[string]*feedbackCard
}

func (c *feedbackCards) loadLocked() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.items = map[string]*feedbackCard{}
	if c.path == "" {
		return
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &c.items); err != nil {
		slog.Warn("feishu: turn feedback state unreadable; starting empty", "path", c.path, "error", err)
		c.items = map[string]*feedbackCard{}
	}
}

func (c *feedbackCards) saveLocked() {
	if len(c.items) > feedbackCardLimit {
		ids := make([]string, 0, len(c.items))
		for id := range c.items {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return c.items[ids[i]].At < c.items[ids[j]].At })
		for _, id := range ids[:len(ids)-feedbackCardLimit] {
			delete(c.items, id)
		}
	}
	if c.path == "" {
		return
	}
	data, err := json.Marshal(c.items)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		slog.Warn("feishu: turn feedback state not saved", "path", c.path, "error", err)
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		err = os.Rename(tmp, c.path)
		if err != nil {
			slog.Warn("feishu: turn feedback state not saved", "path", c.path, "error", err)
		}
	} else {
		slog.Warn("feishu: turn feedback state not saved", "path", c.path, "error", err)
	}
}

// remember records a reply card that now carries the controls -- the
// parts in sections.
func (c *feedbackCards) remember(messageID, sessionKey, markdown, footer string, sections feedbackSections) {
	if messageID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	c.items[messageID] = &feedbackCard{
		SessionKey: sessionKey,
		Markdown:   markdown,
		Footer:     footer,
		NoTurn:     !sections.Turn,
		NoResult:   !sections.Result,
		At:         time.Now().Unix(),
	}
	c.saveLocked()
}

// choose records a click and returns the card as it now stands; false when
// the card is not known (sent before this was kept).
func (c *feedbackCards) choose(messageID, verdict, comment string) (feedbackCard, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	card, ok := c.items[messageID]
	if !ok {
		return feedbackCard{}, false
	}
	if verdict != "" {
		card.Verdict = verdict
	}
	if comment != "" {
		card.Comment = comment
	}
	card.At = time.Now().Unix()
	c.saveLocked()
	return *card, true
}
