package core

import (
	"context"
	"fmt"
	"sort"
)

// The platform reports concrete sent message IDs, never guessed links.
type ExecutionMessageObserver interface{ SetExecutionMessageObserver(func([]string) error) }

func (e *Engine) observeWorkflowMessages(card StreamingCard, project *TeamProject, task *TeamTask, key string) {
	observer, ok := card.(ExecutionMessageObserver)
	if !ok || project == nil || e.teamProjects == nil {
		return
	}
	observer.SetExecutionMessageObserver(func(ids []string) error {
		return e.teamProjects.mutateProject(project.ID, func(p *TeamProject) error {
			if task != nil {
				t, ok := p.Tasks[task.ID]
				if !ok || t.Attempt != task.Attempt {
					return fmt.Errorf("execution card belongs to another attempt")
				}
				t.ProcessMessageIDs = append([]string(nil), ids...)
				p.Tasks[t.ID] = t
			} else {
				if p.ManagerContexts == nil {
					p.ManagerContexts = map[string]TeamManagerContext{}
				}
				origin := p.ManagerContexts[key]
				origin.ProcessMessageIDs = append([]string(nil), ids...)
				p.ManagerContexts[key] = origin
			}
			return nil
		})
	})
}

// Historical cards which can no longer be updated can be reopened as a fresh,
// read-only view, on an explicit user request only.
type ExecutionArchiveViewer interface {
	ShowExecutionArchive(context.Context, any, string) error
}
type executionHistoryRow struct {
	label, message string
	archive        bool
}

func (e *Engine) executionHistoryRows(p TeamProject) []executionHistoryRow {
	var rows []executionHistoryRow
	add := func(label, source string, process []string, result string) {
		if source != "" {
			rows = append(rows, executionHistoryRow{label, source, false})
		}
		for _, id := range process {
			rows = append(rows, executionHistoryRow{label + " · " + e.i18n.T(MsgQueryProcess), id, true})
		}
		if result != "" {
			rows = append(rows, executionHistoryRow{label + " · " + e.i18n.T(MsgQueryAnswer), result, false})
		}
	}
	keys := make([]string, 0, len(p.ManagerContexts))
	for key, v := range p.ManagerContexts {
		if len(v.ProcessMessageIDs) > 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := p.ManagerContexts[keys[i]], p.ManagerContexts[keys[j]]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		v := p.ManagerContexts[key]
		add(truncateIf(v.Instruction, 100), v.SourceMessageID, v.ProcessMessageIDs, v.ResultMessageID)
	}
	tasks := sortedTasks(p)
	for _, round := range p.Rounds {
		tasks = append(tasks, sortedTasks(TeamProject{Tasks: round.Tasks})...)
	}
	seen := map[string]bool{}
	for _, t := range tasks {
		add(t.Title, t.RootMessageID, t.ProcessMessageIDs, t.ResultMessageID)
		for i := len(t.History) - 1; i >= 0; i-- {
			h := t.History[i]
			add(t.Title, h.RootMessageID, h.ProcessMessageIDs, h.ResultMessageID)
		}
	}
	unique := []executionHistoryRow{}
	for _, r := range rows {
		if !seen[r.message] {
			seen[r.message] = true
			unique = append(unique, r)
		}
	}
	return unique
}
func (e *Engine) sendExecutionHistory(platform Platform, msg *Message, p TeamProject, args []string) error {
	rows := e.executionHistoryRows(p)
	if len(args) > 2 && args[1] == "open" {
		for _, r := range rows {
			if r.archive && r.message == args[2] {
				viewer, ok := platform.(ExecutionArchiveViewer)
				if !ok {
					return ErrNotSupported
				}
				return viewer.ShowExecutionArchive(e.ctx, msg.ReplyCtx, r.message)
			}
		}
		return fmt.Errorf("execution archive is not part of this project")
	}
	page := 0
	if len(args) > 1 {
		page = dashboardPage(args[1])
	}
	pages := max(1, (len(rows)+dashboardPageSize-1)/dashboardPageSize)
	page = min(page, pages-1)
	c := NewCard().Title(e.i18n.T(MsgExecHistory), "blue")
	if len(rows) == 0 {
		c.Markdown(e.i18n.T(MsgDashboardNoHistory))
	}
	for _, r := range rows[min(page*dashboardPageSize, len(rows)):min((page+1)*dashboardPageSize, len(rows))] {
		c.Markdown(r.label)
		if linker, ok := platform.(WorkflowMessageLinker); ok {
			if link, err := linker.WorkflowMessageLink(e.ctx, r.message); err == nil {
				c.Markdown("[" + e.i18n.T(MsgExecOpenOriginal) + "](" + link + ")")
			} else {
				c.Note(e.i18n.Tf(MsgDashboardLinkFailed, err.Error()))
			}
		}
		if r.archive {
			c.Buttons(DefaultBtn(e.i18n.T(MsgExecReadArchive), "cmd:/project executions open "+r.message))
		}
	}
	if page > 0 {
		c.Buttons(DefaultBtn(e.i18n.T(MsgCardPrev), fmt.Sprintf("cmd:/project executions %d", page-1)))
	}
	if page+1 < pages {
		c.Buttons(DefaultBtn(e.i18n.T(MsgCardNext), fmt.Sprintf("cmd:/project executions %d", page+1)))
	}
	c.Buttons(DefaultBtn(e.i18n.T(MsgDashboardCurrent), "cmd:/project dashboard"))
	e.sendProjectPanel(platform, msg, reusableCard(c.Build()))
	return nil
}
