package feishu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func executionHasContent(blocks []core.ExecutionBlock) bool {
	for _, b := range blocks {
		if b.Tool != "" || strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// A wait notice may create a card before a final-only answer arrives. Remove
// that turn's now-empty preview instead of keeping a bare completion badge.
func (c *queryCardStream) discardEmptyExecutionLocked(ctx context.Context) error {
	for len(c.cards) > 0 {
		part := c.cards[0]
		part.mu.Lock()
		err := c.p.DeletePreviewMessage(ctx, part.handle)
		if err != nil {
			part.mu.Unlock()
			return err
		}
		part.done, part.prompt = true, nil
		part.pinned.Store(false)
		if dir := c.p.queryCardStateDir; dir != "" {
			if err := os.Remove(filepath.Join(dir, part.id+".json")); err != nil && !os.IsNotExist(err) {
				slog.Warn("remove empty execution snapshot", "error", err)
			}
		}
		part.mu.Unlock()
		c.p.queryCardsMu.Lock()
		delete(c.p.queryCards, part.id)
		c.p.queryCardsMu.Unlock()
		c.cards = c.cards[1:]
	}
	return c.recordExecutionMessages()
}

func (c *queryCardStream) UpdateExecution(ctx context.Context, blocks []core.ExecutionBlock) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.native = true
	firstContent := len(c.blocks) == 0 && len(blocks) > 0
	c.blocks = executionKeys(blocks)
	if !firstContent && time.Since(c.lastWrite) < 500*time.Millisecond {
		c.scheduleExecutionFlushLocked(ctx)
		c.throttle = true
		defer func() { c.throttle = false }()
	}
	return c.writeLocked(ctx)
}

// Coalesce rapid deltas, but always publish the tail even if the agent pauses.
// The stream mutex serializes the timer with prompts, navigation and finalization.
func (c *queryCardStream) scheduleExecutionFlushLocked(ctx context.Context) {
	if c.flushTimer != nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(max(0, 500*time.Millisecond-time.Since(c.lastWrite)), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.flushTimer != timer {
			return
		}
		c.flushTimer = nil
		if c.done || ctx.Err() != nil {
			return
		}
		if err := c.writeLocked(ctx); err != nil {
			slog.Warn("feishu: deferred execution update failed", "error", err)
		}
	})
	c.flushTimer = timer
}

func (c *queryCardStream) FinishExecution(ctx context.Context, blocks []core.ExecutionBlock, answer string, status core.CardStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.native = true
	c.blocks = executionKeys(blocks)
	for i := range c.blocks {
		if c.blocks[i].Tool != "" && !c.blocks[i].Done {
			c.blocks[i].Status = "interrupted"
		}
		c.blocks[i].Done = true
	}
	c.done, c.status, c.prompt = true, status, nil
	err := c.writeLocked(ctx)
	if answer != "" {
		final := &queryCardStream{p: c.p, rc: c.rc, content: answer, answer: true, done: true, status: status, taskKey: c.taskKey}
		if restoreErr := final.restoreTaskParts(); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		err = errors.Join(err, final.writeLocked(ctx))
	}
	return err
}

// UI keys are opaque to readers and independent of a CLI's reusable tool name.
func executionKeys(blocks []core.ExecutionBlock) []core.ExecutionBlock {
	out := append([]core.ExecutionBlock(nil), blocks...)
	for i := range out {
		if out[i].Key == "" {
			out[i].Key = fmt.Sprintf("b%d", i)
		}
	}
	return out
}
func literalCode(s string) string {
	fence := "```"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence + "\n" + s + "\n" + fence
}
func executionBlockText(b core.ExecutionBlock) string {
	if b.Tool != "" {
		return b.Command + "\n\n" + b.Output
	}
	return b.Text
}

// A new public explanation begins a group. All following calls belong to it,
// including concurrent calls and results that arrive out of order.
func executionGroups(blocks []core.ExecutionBlock) [][]core.ExecutionBlock {
	var groups [][]core.ExecutionBlock
	for _, b := range blocks {
		if len(groups) == 0 || b.Tool == "" {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], b)
	}
	return groups
}

// Capacity is checked using the actual summary envelope, never full log output.
// An oversized group stays in one message and its summary becomes pageable.
func (c *queryCardStream) nativePages(ctx context.Context) ([][]core.ExecutionBlock, error) {
	c.blocks = executionKeys(c.blocks)
	pages := [][]core.ExecutionBlock{{}}
	for _, group := range executionGroups(c.blocks) {
		last := len(pages) - 1
		candidate := append(append([]core.ExecutionBlock(nil), pages[last]...), group...)
		fits, err := c.nativePageFits(candidate, last, false)
		if err != nil {
			return nil, err
		}
		if !fits && len(pages[last]) > 0 {
			pages = append(pages, append([]core.ExecutionBlock(nil), group...))
		} else {
			pages[last] = candidate
		}
	}
	return pages, nil
}

// Store records by key on every card which has ever displayed them. Old links
// remain useful when a growing group moves intact to the next card.
func (c *queryCard) syncExecutionRecords(all []core.ExecutionBlock) {
	if c.records == nil {
		c.records = map[string]core.ExecutionBlock{}
	}
	for _, b := range c.blocks {
		c.records[b.Key] = b
	}
	for _, b := range all {
		if _, ok := c.records[b.Key]; ok {
			c.records[b.Key] = b
		}
	}
}

var _ core.ExecutionCard = (*queryCardStream)(nil)
