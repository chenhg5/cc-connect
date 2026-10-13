package feishu

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// queryCardStream owns one turn's ordered message handles. Cumulative snapshots
// may revise tool results, so reconcile existing parts rather than appending the
// whole snapshot again. Only overflow allocates a new message.
type queryCardStream struct {
	forceDecisionUpdate bool
	recordedIDs         string
	messageObserver     func([]string) error
	throttle            bool
	notificationKey     string
	decisionTitle       string
	decisionOwner       int
	decisionUser        string
	decisionNotify      func(context.Context, core.DecisionNotification) error

	native bool
	blocks []core.ExecutionBlock

	taskKey         string
	mu              sync.Mutex
	p               *Platform
	rc              replyContext
	cards           []*queryCard
	content, notice string
	summary         string
	prompt          *core.Card
	status          core.CardStatus
	done, answer    bool
	lastWrite       time.Time
	flushTimer      *time.Timer
}

var _ core.InteractiveStreamingCard = (*queryCardStream)(nil)
var _ core.FinalAnswerCard = (*queryCardStream)(nil)

func (p *Platform) createQueryCardStream(ctx context.Context, rc replyContext) (*queryCardStream, error) {
	c := &queryCardStream{p: p, rc: rc, native: p.isTaskThread(rc), status: core.CardStatusWorking}
	if err := c.writeLocked(ctx); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *queryCardStream) SetProgressSummary(summary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.done {
		c.summary = summary
	}
}
func (c *queryCardStream) Failed() bool              { return false }
func (c *queryCardStream) SeparateFinalAnswer() bool { return c.p.separateFinalCard }
func (c *queryCardStream) Update(ctx context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.content = content
	if c.native {
		c.blocks = executionKeys([]core.ExecutionBlock{{Text: content}})
	}
	if time.Since(c.lastWrite) < 500*time.Millisecond {
		return nil
	}
	return c.writeLocked(ctx)
}
func (c *queryCardStream) SetPrompt(ctx context.Context, prompt *core.Card) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	if c.prompt == nil && prompt != nil {
		c.decisionOwner = max(0, len(c.cards)-1)
	}
	c.prompt = prompt
	if err := c.writeLocked(ctx); err != nil {
		return err
	}
	if c.native && prompt != nil {
		c.sendDecisionNotification(ctx)
	}
	return nil
}
func (c *queryCardStream) SetNotice(ctx context.Context, notice string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.notice = notice
	return c.writeLocked(ctx)
}
func (c *queryCardStream) Finalize(ctx context.Context, content string) error {
	return c.Finish(ctx, content, core.CardStatusDone)
}
func (c *queryCardStream) Finish(ctx context.Context, content string, status core.CardStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	if content != "" {
		c.content = content
	}
	c.done, c.status, c.prompt = true, status, nil
	return c.writeLocked(ctx)
}
func (c *queryCardStream) FinalizeWithAnswer(ctx context.Context, progress, answer string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.content, c.prompt, c.done, c.status = progress, nil, true, core.CardStatusDone
	if c.native && progress != "" {
		c.blocks = executionKeys([]core.ExecutionBlock{{Text: progress, Done: true}})
	}
	progressErr := c.writeLocked(ctx)
	if answer == "" {
		return progressErr
	}
	final := &queryCardStream{p: c.p, rc: c.rc, content: answer, answer: true, done: true, status: core.CardStatusDone, taskKey: c.taskKey}
	if err := final.restoreTaskParts(); err != nil {
		return err
	}
	if err := final.writeLocked(ctx); err != nil {
		// A failure after some answer parts were sent must not resend those parts.
		// Keep the undelivered suffix accessible using existing process handles.
		pages := core.SplitMessageCodeFenceAware(answer, queryPageRunes)
		remaining := pages[len(final.cards):]
		var fallbackErr error
		available := min(len(remaining), len(c.cards))
		for i, body := range remaining[:available] {
			part := c.cards[len(c.cards)-available+i]
			part.mu.Lock()
			part.content, part.answer, part.prompt, part.notice = body, true, nil, ""
			part.part = len(final.cards) + i + 1
			if len(remaining) > available {
				part.notice = c.p.cardI18n().T(core.MsgQueryDeliveryIncomplete)
			}
			fallbackErr = errors.Join(fallbackErr, part.writeLocked(ctx))
			part.mu.Unlock()
		}
		return errors.Join(progressErr, fmt.Errorf("create final answer continuation: %w", err), fallbackErr)
	}
	return progressErr
}

func (c *queryCardStream) writeLocked(ctx context.Context) error {
	if !c.throttle && c.flushTimer != nil {
		c.flushTimer.Stop()
		c.flushTimer = nil
	}
	// No network message is needed for an empty native execution. Receipt
	// reactions already acknowledge the incoming task. Prompts and notices
	// remain visible even before any public text or tool has arrived.
	if c.native && c.prompt == nil && strings.TrimSpace(c.notice) == "" && !executionHasContent(c.blocks) {
		if c.done {
			return c.discardEmptyExecutionLocked(ctx)
		}
		if len(c.cards) == 0 {
			return nil
		}
	}
	pages := core.SplitMessageCodeFenceAware(c.content, queryPageRunes)
	var nativePages [][]core.ExecutionBlock
	if c.native {
		var err error
		nativePages, err = c.nativePages(ctx)
		if err != nil {
			return err
		}
		pages = make([]string, len(nativePages))
	}
	if len(pages) == 0 {
		pages = []string{""}
	}
	// Revisions can shorten a snapshot. Preserve already published text as
	// history, but remove its approval controls and identify it as superseded.
	count := max(len(pages), len(c.cards))
	for i := 0; i < count; i++ {
		body, notice, prompt, status, done := "", "", (*core.Card)(nil), core.CardStatusDone, true
		if i < len(pages) {
			body = pages[i]
		}
		if i == len(pages)-1 {
			notice, status, done = c.notice, c.status, c.done
		}
		if (!c.native && i == len(pages)-1) || (c.native && i == c.decisionOwner) {
			prompt = c.prompt
		}
		if c.native {
			done = c.done
			status = c.status
		}
		if i >= len(c.cards) {
			part := &queryCard{p: c.p, rc: c.rc, id: rand.Text(), created: time.Now(), content: body, summary: c.summary, notice: notice, prompt: prompt, status: status, done: done, answer: c.answer, unpaged: true, part: i + 1}
			if c.native {
				part.native = true
				part.blocks = nativePages[i]
				part.syncExecutionRecords(c.blocks)
			}
			if c.taskKey != "" {
				part.id = c.taskPartID(i + 1)
				part.taskKey = c.taskKey
			}
			sent, err := c.p.sendQueryCard(ctx, part)
			if err != nil {
				return err
			}
			c.cards = append(c.cards, sent)
			if err := c.recordExecutionMessages(); err != nil {
				return err
			}
			continue
		}
		part := c.cards[i]
		part.mu.Lock()
		if i >= len(pages) {
			body = part.content
			notice = c.p.cardI18n().T(core.MsgQuerySuperseded)
		}
		part.pinned.Store(!done)
		oldPrompt := part.prompt
		part.content, part.notice, part.prompt, part.status, part.done = body, notice, prompt, status, done
		if prompt == nil {
			part.decisionDraft = ""
			part.notification = nil
			part.decisionNotice = ""
		}
		if oldPrompt != nil && prompt != nil && oldPrompt.DecisionToken != prompt.DecisionToken {
			part.decisionDraft = ""
		}
		if c.native && i < len(nativePages) {
			part.native = true
			part.blocks = nativePages[i]
		}
		if c.native && i >= len(nativePages) {
			part.blocks = nil
		}
		if c.native {
			part.syncExecutionRecords(c.blocks)
		}
		part.summary = c.summary
		var err error
		// Do not replace an input form behind a user's unsent draft. Navigation
		// callbacks collect the form draft before replacing the shared view.
		if c.throttle {
			// Live records above stay available to callbacks. Coalesce disk/API
			// writes too; the scheduled trailing flush persists the latest state.
		} else if !c.forceDecisionUpdate && oldPrompt != nil && prompt != nil && prompt.DecisionToken != "" && oldPrompt.DecisionToken == prompt.DecisionToken {
			err = part.persistLocked(false)
		} else {
			err = part.writeLocked(ctx)
		}
		part.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if !c.throttle {
		c.lastWrite = time.Now()
	}
	return c.recordExecutionMessages()
}
