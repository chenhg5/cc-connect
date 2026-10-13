package feishu

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// A snapshot contains display state only. Approval prompts and callbacks are
// never revived after a restart. Each app/runtime has its own private archive.
type queryCardSnapshot struct {
	RecordRefs map[string]string     `json:"record_refs,omitempty"`
	View       executionView         `json:"view,omitempty"`
	ViewRef    string                `json:"view_ref,omitempty"`
	Native     bool                  `json:"native,omitempty"`
	Blocks     []core.ExecutionBlock `json:"blocks,omitempty"`
	Summary    string                `json:"summary,omitempty"`
	TaskKey    string                `json:"task_key,omitempty"`
	Version    int                   `json:"version"`
	ID         string                `json:"id"`
	Created    time.Time             `json:"created"`
	ChatID     string                `json:"chat_id"`
	MessageID  string                `json:"message_id"`
	SessionKey string                `json:"session_key"`
	CardID     string                `json:"card_id,omitempty"`
	Sequence   int                   `json:"sequence"`
	Content    string                `json:"content"`
	Notice     string                `json:"notice,omitempty"`
	Status     core.CardStatus       `json:"status"`
	Done       bool                  `json:"done"`
	Answer     bool                  `json:"answer"`
	Page       int                   `json:"page"`
	Unpaged    bool                  `json:"unpaged,omitempty"`
	Part       int                   `json:"part,omitempty"`
}

func queryCardStateDir(opts map[string]any, domain, app string) string {
	dir, _ := opts["cc_data_dir"].(string)
	if dir == "" {
		return ""
	}
	project, _ := opts["cc_project"].(string)
	hash := sha256.Sum256([]byte(domain + "\x00" + app + "\x00" + project))
	return filepath.Join(dir, "query-cards", fmt.Sprintf("%x", hash[:16]))
}

func validQueryCardID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (c *queryCard) persistLocked(reserveSequence bool) error {
	dir := c.p.queryCardStateDir
	if dir == "" {
		return nil
	}
	h, ok := c.handle.(*feishuPreviewHandle)
	if !ok || !validQueryCardID(c.id) {
		return fmt.Errorf("invalid query card snapshot")
	}
	h.mu.Lock()
	snapshot := queryCardSnapshot{Native: c.native, Blocks: c.blocks, Summary: c.summary, TaskKey: c.taskKey, Version: 1, ID: c.id, Created: c.created, ChatID: c.rc.chatID,
		MessageID: h.messageID, SessionKey: c.rc.sessionKey, CardID: h.cardID, Sequence: h.sequence,
		Content: c.content, Notice: c.notice, Status: c.status, Done: c.done, Answer: c.answer, Page: c.page, Unpaged: c.unpaged, Part: c.part}
	if reserveSequence && h.cardID != "" {
		snapshot.Sequence++
	}
	h.mu.Unlock()
	if err := c.archiveExecutionSnapshot(&snapshot); err != nil {
		return fmt.Errorf("archive execution records: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return fmt.Errorf("query card snapshot exceeds 32 MiB")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".query-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, c.id+".json"))
}

func (p *Platform) loadQueryCard(id string) (*queryCard, error) {
	if !validQueryCardID(id) {
		return nil, errQueryCardExpired
	}
	p.queryCardsMu.Lock()
	defer p.queryCardsMu.Unlock()
	if card := p.queryCards[id]; card != nil {
		return card, nil
	}
	if p.queryCardStateDir == "" {
		return nil, errQueryCardExpired
	}
	f, err := os.Open(filepath.Join(p.queryCardStateDir, id+".json"))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("query card snapshot read failed", "error", err)
		}
		return nil, errQueryCardExpired
	}
	defer f.Close()
	var snapshot queryCardSnapshot
	if err := json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&snapshot); err != nil {
		slog.Warn("query card snapshot decode failed", "error", err)
		return nil, errQueryCardExpired
	}
	if snapshot.Version != 1 || snapshot.ID != id || snapshot.ChatID == "" || snapshot.MessageID == "" || snapshot.Created.IsZero() || (snapshot.TaskKey == "" && !snapshot.Native && time.Since(snapshot.Created) > 24*time.Hour) {
		return nil, errQueryCardExpired
	}
	c := &queryCard{native: snapshot.Native, blocks: snapshot.Blocks, taskKey: snapshot.TaskKey, p: p, rc: replyContext{chatID: snapshot.ChatID, sessionKey: snapshot.SessionKey},
		handle: &feishuPreviewHandle{messageID: snapshot.MessageID, chatID: snapshot.ChatID, cardID: snapshot.CardID, sequence: snapshot.Sequence},
		id:     id, created: snapshot.Created, content: snapshot.Content, summary: snapshot.Summary, notice: snapshot.Notice, status: snapshot.Status,
		done: true, answer: snapshot.Answer, page: snapshot.Page, unpaged: snapshot.Unpaged, part: snapshot.Part}
	if err := p.restoreExecutionSnapshot(&snapshot, c); err != nil {
		slog.Warn("execution archive unavailable", "error", err)
		return nil, errQueryCardExpired
	}
	if !snapshot.Done {
		c.status = core.CardStatusError
		c.notice = p.cardI18n().T(core.MsgQueryRestored)
		for key, b := range c.records {
			if b.Tool != "" && !b.Done {
				b.Done = true
				b.Status = "interrupted"
				c.records[key] = b
			}
		}
		for i, b := range c.blocks {
			if updated, ok := c.records[b.Key]; ok {
				c.blocks[i] = updated
			}
		}
		if c.view.Snapshot.Tool != "" && !c.view.Snapshot.Done {
			c.view.Snapshot.Done = true
			c.view.Snapshot.Status = "interrupted"
		}
	}
	if p.queryCards == nil {
		p.queryCards = make(map[string]*queryCard)
	}
	if len(p.queryCards) >= 256 {
		oldest := ""
		for key, card := range p.queryCards {
			if !card.pinned.Load() && (oldest == "" || card.created.Before(p.queryCards[oldest].created)) {
				oldest = key
			}
		}
		delete(p.queryCards, oldest)
	}
	p.queryCards[id] = c
	return c, nil
}

func (p *Platform) pruneQueryCardSnapshots() {
	if p.queryCardStateDir == "" {
		return
	}
	entries, err := os.ReadDir(p.queryCardStateDir)
	if err != nil {
		slog.Warn("query card snapshot cleanup failed", "error", err)
		return
	}
	type file struct {
		name     string
		modified time.Time
	}
	var files []file
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), "task-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, file{entry.Name(), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.After(files[j].modified) })
	for i, file := range files {
		if i >= 256 || time.Since(file.modified) > 24*time.Hour {
			data, err := os.ReadFile(filepath.Join(p.queryCardStateDir, file.name))
			var saved queryCardSnapshot
			if err == nil && json.Unmarshal(data, &saved) == nil && saved.Native {
				continue
			}
			if err := os.Remove(filepath.Join(p.queryCardStateDir, file.name)); err != nil && !os.IsNotExist(err) {
				slog.Warn("query card snapshot remove failed", "error", err)
			}
		}
	}
}
