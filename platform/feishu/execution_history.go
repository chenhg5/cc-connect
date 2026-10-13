package feishu

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (c *queryCardStream) SetExecutionMessageObserver(fn func([]string) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messageObserver = fn
	if err := c.recordExecutionMessages(); err != nil {
		slog.Warn("record execution messages", "error", err)
	}
}
func (c *queryCardStream) recordExecutionMessages() error {
	if c.messageObserver == nil {
		return nil
	}
	ids := []string{}
	for _, card := range c.cards {
		if h, ok := card.handle.(*feishuPreviewHandle); ok {
			h.mu.Lock()
			ids = append(ids, h.messageID)
			h.mu.Unlock()
		}
	}
	key := strings.Join(ids, "\x00")
	if key == c.recordedIDs {
		return nil
	}
	if err := c.messageObserver(ids); err != nil {
		return err
	}
	c.recordedIDs = key
	return nil
}

func (p *Platform) ShowExecutionArchive(ctx context.Context, rctx any, messageID string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("invalid execution archive conversation")
	}
	entries, err := os.ReadDir(p.queryCardStateDir)
	if err != nil {
		return fmt.Errorf("execution archive unavailable: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.queryCardStateDir, entry.Name()))
		if err != nil {
			continue
		}
		var saved queryCardSnapshot
		if json.Unmarshal(data, &saved) != nil || !saved.Native || saved.MessageID != messageID || saved.ChatID != rc.chatID {
			continue
		}
		original, err := p.loadQueryCard(saved.ID)
		if err != nil {
			return err
		}
		original.mu.Lock()
		copy := &queryCard{p: p, rc: rc, id: rand.Text(), created: time.Now(), native: true, done: true, status: core.CardStatusThinking, blocks: append([]core.ExecutionBlock(nil), original.blocks...), records: map[string]core.ExecutionBlock{}, notice: p.cardI18n().T(core.MsgExecArchived)}
		for key, b := range original.records {
			copy.records[key] = b
		}
		original.mu.Unlock()
		// A moved group's old card may have no current summary; preserve access to
		// all records which that historical message once displayed.
		if len(copy.blocks) == 0 {
			for _, b := range copy.records {
				copy.blocks = append(copy.blocks, b)
			}
			sort.Slice(copy.blocks, func(i, j int) bool {
				a, _ := strconv.Atoi(strings.TrimPrefix(copy.blocks[i].Key, "b"))
				b, _ := strconv.Atoi(strings.TrimPrefix(copy.blocks[j].Key, "b"))
				return a < b
			})
		}
		copy.rc.forceChatReply = true
		_, err = p.sendQueryCard(ctx, copy)
		return err
	}
	return errQueryCardExpired
}
