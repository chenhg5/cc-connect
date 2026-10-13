package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// workflowCard exposes execution summaries and approvals, not internal reasoning.
type workflowCard struct {
	inner         StreamingCard
	progress      *executionTracker
	owner         string
	mu            sync.Mutex
	i18n          *I18n
	title, status string
}

func newWorkflowCard(inner StreamingCard, i *I18n, mode, title string) *workflowCard {
	return &workflowCard{inner: inner, i18n: i, title: title, status: "running", progress: newExecutionTracker(time.Now())}
}
func (c *workflowCard) observe(ev Event) { c.progress.observe(ev, time.Now()) }
func (c *workflowCard) text() string {
	return c.title + "\n" + c.owner + "\n" + c.progress.snapshot().details(c.i18n, time.Now())
}
func (c *workflowCard) setSummary() {
	if f, ok := c.inner.(ProgressSummaryCard); ok {
		owner := strings.SplitN(c.owner, "\n", 2)[0]
		f.SetProgressSummary(strings.TrimSpace(c.title + "\n" + owner + "\n" + c.progress.snapshot().compactText(c.i18n, time.Now())))
	}
}
func (c *workflowCard) Update(ctx context.Context, _ string) error {
	c.setSummary()
	return c.inner.Update(ctx, c.text())
}
func (c *workflowCard) Failed() bool { return c.inner.Failed() }
func (c *workflowCard) SeparateFinalAnswer() bool {
	f, ok := c.inner.(FinalAnswerCard)
	return ok && f.SeparateFinalAnswer()
}
func (c *workflowCard) Finalize(ctx context.Context, text string) error {
	if f, ok := c.inner.(ProgressSummaryCard); ok {
		f.SetProgressSummary("")
	}
	return c.inner.Finalize(ctx, text)
}
func (c *workflowCard) FinalizeWithAnswer(ctx context.Context, _, answer string) error {
	c.setSummary()
	if f, ok := c.inner.(FinalAnswerCard); ok {
		return f.FinalizeWithAnswer(ctx, c.text(), answer)
	}
	return c.inner.Finalize(ctx, answer)
}
func (c *workflowCard) SetPrompt(ctx context.Context, p *Card) error {
	c.mu.Lock()
	if p == nil {
		c.status = "running"
		c.progress.resume(time.Now())
	} else {
		c.status = "waiting_approval"
	}
	c.mu.Unlock()
	if f, ok := c.inner.(InteractiveStreamingCard); ok {
		return f.SetPrompt(ctx, p)
	}
	return ErrNotSupported
}
func (c *workflowCard) SetNotice(ctx context.Context, s string) error {
	if f, ok := c.inner.(InteractiveStreamingCard); ok {
		return f.SetNotice(ctx, s)
	}
	return c.inner.Update(ctx, c.text()+"\n"+s)
}
func (c *workflowCard) Finish(ctx context.Context, _ string, s CardStatus) error {
	c.mu.Lock()
	if s == CardStatusError {
		c.status = "failed"
	} else {
		c.status = "stopped"
	}
	c.progress.finish(c.status)
	c.mu.Unlock()
	c.setSummary()
	if f, ok := c.inner.(InteractiveStreamingCard); ok {
		return f.Finish(ctx, c.text(), s)
	}
	return c.inner.Finalize(ctx, c.text())
}
func (e *Engine) taskLogPath(p TeamProject, t TeamTask) string {
	return filepath.Join(filepath.Dir(e.teamProjects.path), "task-logs", p.ID, t.ID+".log")
}
func (e *Engine) recordTaskEvent(p *TeamProject, t *TeamTask, ev Event) {
	if p == nil || t == nil {
		return
	}
	var data string
	switch ev.Type {
	case EventToolUse:
		data = ev.ToolName + "\n" + ev.ToolInput
	case EventToolResult:
		data = ev.ToolName + "\n" + ev.ToolResult
		if ev.ToolResult == "" {
			data += ev.Content
		}
	case EventError:
		if ev.Error != nil {
			data = ev.Error.Error()
		}
	default:
		return
	}
	path := e.taskLogPath(*p, *t)
	// Per-event and per-task bounds keep runaway tools from filling the disk.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		slog.Warn("task log directory failed", "error", err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		slog.Warn("task log open failed", "error", err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() >= 2<<20 {
		return
	}
	if _, err = fmt.Fprintf(f, "\n%s attempt=%d %s\n%s\n", time.Now().UTC().Format(time.RFC3339), t.Attempt, ev.Type, truncateIf(data, 8000)); err != nil {
		slog.Warn("task log write failed", "error", err)
	}
}
func (e *Engine) sendTaskLog(platform Platform, msg *Message, p TeamProject, t TeamTask) error {
	if !ValidBotName(p.ID) || !strings.HasPrefix(t.ID, "T") || strings.ContainsAny(t.ID, "/\\.") {
		return fmt.Errorf("invalid task ID")
	}
	data, err := os.ReadFile(e.taskLogPath(p, t))
	if os.IsNotExist(err) {
		data = []byte(e.i18n.T(MsgWorkflowEmpty))
		err = nil
	}
	if err != nil {
		return err
	}
	return e.sendWorkflowFile(platform, msg, data, t.ID+"-tools.log")
}
