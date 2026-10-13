package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Manager reports are background bookkeeping. Create an execution card only
// if a human decision is needed; a NO_REPLY report must leave no empty cards.
type quietWorkflowCard struct {
	mu      sync.Mutex
	create  func() (StreamingCard, error)
	send    func(string) error
	inner   StreamingCard
	notice  string
	summary string
	done    bool
}

func (c *quietWorkflowCard) SetProgressSummary(summary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.summary = summary
	if f, ok := c.inner.(ProgressSummaryCard); ok {
		f.SetProgressSummary(summary)
	}
}
func (c *quietWorkflowCard) Failed() bool              { return false }
func (c *quietWorkflowCard) SeparateFinalAnswer() bool { return true }
func (c *quietWorkflowCard) Update(ctx context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inner != nil {
		return c.inner.Update(ctx, text)
	}
	return nil
}
func (c *quietWorkflowCard) SetPrompt(ctx context.Context, p *Card) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	if c.inner == nil && p != nil {
		var err error
		c.inner, err = c.create()
		if f, ok := c.inner.(ProgressSummaryCard); ok {
			f.SetProgressSummary(c.summary)
		}
		if err != nil {
			return err
		}
	}
	if c.inner == nil {
		return nil
	}
	if f, ok := c.inner.(InteractiveStreamingCard); ok {
		return f.SetPrompt(ctx, p)
	}
	return ErrNotSupported
}
func (c *quietWorkflowCard) SetNotice(ctx context.Context, s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notice = s
	if f, ok := c.inner.(InteractiveStreamingCard); ok {
		return f.SetNotice(ctx, s)
	}
	return nil
}
func (c *quietWorkflowCard) Finish(ctx context.Context, text string, status CardStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.done = true
	if f, ok := c.inner.(InteractiveStreamingCard); ok {
		return f.Finish(ctx, text, status)
	}
	if status == CardStatusError && c.notice != "" {
		return c.send(c.notice)
	}
	return nil
}
func (c *quietWorkflowCard) Finalize(ctx context.Context, s string) error {
	return c.FinalizeWithAnswer(ctx, "", s)
}
func (c *quietWorkflowCard) FinalizeWithAnswer(ctx context.Context, progress, answer string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.done = true
	if f, ok := c.inner.(FinalAnswerCard); ok {
		return f.FinalizeWithAnswer(ctx, progress, answer)
	}
	if answer != "" {
		return c.send(answer)
	}
	return nil
}
func (e *Engine) workflowStream(p Platform, rc any, project *TeamProject, task *TeamTask, internal bool, sessionKey string) (StreamingCard, *workflowCard) {
	scp, ok := p.(StreamingCardPlatform)
	if !ok {
		return nil, nil
	}
	create := func() (StreamingCard, error) {
		if task != nil {
			if tp, ok := p.(TaskStreamingCardPlatform); ok {
				return tp.CreateTaskStreamingCard(e.ctx, rc, fmt.Sprintf("%s-%s-%d", project.ID, task.ID, task.Attempt))
			}
		}
		return scp.CreateStreamingCard(e.ctx, rc)
	}
	var sc StreamingCard
	if _, ok := p.(TaskMessagePlatform); ok && project != nil {
		card, err := create()
		if err != nil {
			slog.Warn("native workflow card creation failed", "error", err)
			return nil, nil
		}
		e.observeWorkflowMessages(card, project, task, sessionKey)
		inner, interactive := card.(InteractiveStreamingCard)
		sink, native := card.(ExecutionCard)
		if interactive && native {
			mention := ""
			if task != nil && e.teamProjects != nil {
				e.teamProjects.runtimeMu.Lock()
				manager := e.teamProjects.engines[project.Manager]
				e.teamProjects.runtimeMu.Unlock()
				if manager != nil {
					if identity, ok := manager.platformForName(project.Platform).(TaskMessagePlatform); ok && identity.BotMentionID() != "" {
						mention = p.(TaskMessagePlatform).MentionText(identity.BotMentionID())
					}
				}
			}
			_, handoff := p.(ResultMessagePlatform)
			return &nativeWorkflowCard{inner: inner, sink: sink, mention: mention, omitFinal: handoff}, nil
		}
		return card, nil
	}
	_, native := p.(WorkflowPlatform)
	if project != nil && native && e.teamCoordinator && internal && projectDisplayMode(project) != "debug" {
		sc = &quietWorkflowCard{create: create, send: func(text string) error {
			if sender, ok := p.(CardSender); ok {
				for _, chunk := range SplitMessageCodeFenceAware(text, 3000) {
					if err := sender.SendCard(e.ctx, rc, NewCard().Title(e.i18n.T(MsgQueryAnswer), "green").Markdown(chunk).Build()); err != nil {
						return err
					}
				}
				return nil
			}
			return e.sendWithErrorForWorkspace(p, rc, text, project.Directories[0])
		}}
	} else {
		var err error
		sc, err = create()
		if err != nil {
			slog.Warn("workflow card creation failed", "error", err)
			return nil, nil
		}
	}
	if sc != nil && project != nil && projectDisplayMode(project) != "debug" {
		title := project.Name
		if task != nil {
			title = task.ID + " · " + task.Title
		}
		summary := newWorkflowCard(sc, e.i18n, projectDisplayMode(project), title)
		if task != nil {
			summary.owner = e.i18n.Tf(MsgProgressOwner, e.teamProjects.memberLabel(task.Assignee), truncateIf(task.Deliverable, 180))
		}
		return summary, summary
	}
	return sc, nil
}
