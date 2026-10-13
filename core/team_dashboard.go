package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A source link is supplied by the platform, never guessed by core.
type WorkflowMessageLinker interface {
	WorkflowMessageLink(context.Context, string) (string, error)
}

const dashboardPageSize = 6

func dashboardPage(raw string) int {
	n, _ := strconv.Atoi(raw)
	if n < 0 {
		return 0
	}
	return n
}
func reusableCard(c *Card) *Card { c.Reusable = true; return c }
func (e *Engine) dashboardCard(p TeamProject, page int) *Card {
	if p.State == "ended" {
		return reusableCard(e.boardCard(p, false))
	}
	view := currentRoundProject(p)
	c := NewCard().Title(e.i18n.Tf(MsgWorkflowBoard, p.Name), "blue").Markdown(e.i18n.Tf(MsgRoundCurrent, projectRound(p), truncateIf(roundGoal(p), 1000)))
	if p.RoundState == "idle" {
		c.Markdown(e.i18n.T(MsgRoundEnded)).Markdown(e.i18n.T(MsgRoundIdle)).Buttons(PrimaryBtn(e.i18n.T(MsgDashboardNew), "cmd:/project instruction"))
	} else {
		if p.RoundState == "ending" {
			c.Markdown(e.i18n.T(MsgRoundEnding))
		} else if p.Paused {
			c.Markdown(e.i18n.T(MsgWorkflowPaused))
		} else {
			c.Note(workflowStatus(e.i18n, "active"))
		}
		a, r, planned, u := projectFinishCounts(view)
		c.Markdown(e.i18n.Tf(MsgDashboardCounts, a, r, planned, u))
		pendingCount := 0
		for _, t := range sortedTasks(view) {
			if t.Status == "waiting_input" || t.Status == "waiting_approval" {
				if pendingCount >= dashboardPageSize {
					break
				}
				pendingCount++
				c.ListItem(e.i18n.Tf(MsgDashboardPending, t.ID+" · "+e.teamProjects.memberLabel(t.Assignee)), e.i18n.T(MsgWorkflowThread), "cmd:/project task "+t.ID)
			}
		}
		e.dashboardTaskRows(c, sortedTasks(view), page, "/project dashboard")
		if p.RoundState != "ending" {
			action, title := "pause", MsgRoundPause
			if p.Paused {
				action, title = "resume", MsgRoundResume
			}
			c.Buttons(DefaultBtn(e.i18n.T(title), fmt.Sprintf("cmd:/project %s round %d", action, projectRound(p))), DangerBtn(e.i18n.T(MsgProjectFinish), "cmd:/project finish"))
		} else {
			c.Buttons(DefaultBtn(e.i18n.T(MsgRoundRetry), "cmd:/project finish"))
		}
	}
	c.Buttons(DefaultBtn(e.i18n.T(MsgDashboardRefresh), "cmd:/project dashboard "+strconv.Itoa(page)), DefaultBtn(e.i18n.T(MsgDashboardHistory), "cmd:/project history"))
	c.Buttons(DefaultBtn(e.i18n.T(MsgExecHistory), "cmd:/project executions"))
	c.Note(e.i18n.Tf(MsgDashboardUpdated, time.Now().Format("01-02 15:04:05")))
	return reusableCard(c.Build())
}
func (e *Engine) dashboardTaskRows(c *CardBuilder, tasks []TeamTask, page int, command string) {
	pages := (len(tasks) + dashboardPageSize - 1) / dashboardPageSize
	if pages == 0 {
		c.Markdown(e.i18n.T(MsgWorkflowEmpty))
		return
	}
	if page >= pages {
		page = pages - 1
	}
	end := min((page+1)*dashboardPageSize, len(tasks))
	for _, t := range tasks[page*dashboardPageSize : end] {
		// The bounded title is a label; the details contain the complete task.
		c.ListItem(fmt.Sprintf("**%s · %s**\n%s · %s", t.ID, truncateIf(strings.Join(strings.Fields(t.Title), " "), 100), e.teamProjects.memberLabel(t.Assignee), workflowStatus(e.i18n, t.Status)), e.i18n.T(MsgWorkflowThread), "cmd:/project task "+t.ID)
	}
	c.Note(e.i18n.Tf(MsgDashboardPage, page+1, pages))
	var buttons []CardButton
	if page > 0 {
		buttons = append(buttons, DefaultBtn(e.i18n.T(MsgDashboardPrevious), fmt.Sprintf("cmd:%s %d", command, page-1)))
	}
	if page+1 < pages {
		buttons = append(buttons, DefaultBtn(e.i18n.T(MsgDashboardNext), fmt.Sprintf("cmd:%s %d", command, page+1)))
	}
	c.Buttons(buttons...)
}
func (e *Engine) roundHistoryCard(p TeamProject, args []string) *Card {
	c := NewCard().Title(e.i18n.T(MsgDashboardHistory), "blue").Buttons(DefaultBtn(e.i18n.T(MsgExecHistory), "cmd:/project executions"))
	if len(args) > 1 && args[1] == "round" && len(args) > 2 {
		n, _ := strconv.ParseUint(args[2], 10, 64)
		for _, r := range p.Rounds {
			if r.Number == n {
				c.Markdown(e.i18n.Tf(MsgDashboardHistoryEntry, r.Number, r.Goal, r.EndedAt, r.EndedBy))
				page := 0
				if len(args) > 3 {
					page = dashboardPage(args[3])
				}
				// Snapshot detail routes must never resolve to a newer rework of
				// the same task ID; include the archived round explicitly.
				view := p
				view.Tasks = r.Tasks
				view.Round = r.Number
				a, running, planned, u := projectFinishCounts(view)
				c.Markdown(e.i18n.Tf(MsgDashboardCounts, a, running, planned, u))
				e.dashboardTaskRows(c, sortedTasks(view), page, fmt.Sprintf("/project history round %d", n))
				card := c.Build()
				for i, el := range card.Elements {
					if row, ok := el.(CardListItem); ok {
						row.BtnValue += fmt.Sprintf(" round %d", n)
						card.Elements[i] = row
					}
				}
				card.Elements = append(card.Elements, CardActions{Buttons: []CardButton{DefaultBtn(e.i18n.T(MsgDashboardHistory), "cmd:/project history"), DefaultBtn(e.i18n.T(MsgDashboardCurrent), "cmd:/project dashboard")}})
				return reusableCard(card)
			}
		}
		c.Markdown(e.i18n.T(MsgDashboardNoHistory))
	} else {
		page := 0
		if len(args) > 1 {
			page = dashboardPage(args[1])
		}
		pages := max(1, (len(p.Rounds)+dashboardPageSize-1)/dashboardPageSize)
		if page >= pages {
			page = pages - 1
		}
		if len(p.Rounds) == 0 {
			c.Markdown(e.i18n.T(MsgDashboardNoHistory))
		}
		for i := len(p.Rounds) - 1 - page*dashboardPageSize; i >= max(0, len(p.Rounds)-(page+1)*dashboardPageSize); i-- {
			r := p.Rounds[i]
			c.ListItem(e.i18n.Tf(MsgDashboardHistoryEntry, r.Number, truncateIf(r.Goal, 300), r.EndedAt, r.EndedBy), e.i18n.T(MsgDashboardRoundRecords), fmt.Sprintf("cmd:/project history round %d", r.Number))
		}
		if page > 0 {
			c.Buttons(DefaultBtn(e.i18n.T(MsgDashboardPrevious), fmt.Sprintf("cmd:/project history %d", page-1)))
		}
		if page+1 < pages {
			c.Buttons(DefaultBtn(e.i18n.T(MsgDashboardNext), fmt.Sprintf("cmd:/project history %d", page+1)))
		}
	}
	c.Buttons(DefaultBtn(e.i18n.T(MsgDashboardCurrent), "cmd:/project dashboard"))
	return reusableCard(c.Build())
}

// Explicit viewing is separate from cached background refresh. Send to the
// current reply context (or update the clicked reusable panel), even if equal.
func (e *Engine) sendProjectPanel(platform Platform, msg *Message, card *Card) {
	if sender, ok := platform.(CardSender); ok {
		if err := e.waitOutgoing(platform); err != nil {
			return
		}
		if err := sender.SendCard(e.ctx, msg.ReplyCtx, e.renderCardForPlatform(platform, card)); err != nil {
			e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgDashboardFailed, err.Error())+"\n/project dashboard")
		}
		return
	}
	e.sendWithCard(platform, msg.ReplyCtx, card)
}

// Full details retain instructions, output and acceptance evidence separately.
// A large detail is an intact file rather than a silently truncated command.
func (e *Engine) sendProjectTaskDetail(platform Platform, msg *Message, p TeamProject, t TeamTask) error {
	c := NewCard().Title(t.ID+" · "+truncateIf(t.Title, 80), "blue").Markdown(t.Title).
		Markdown(e.i18n.Tf(MsgWorkflowTask, e.teamProjects.memberLabel(t.Assignee), t.Reason, t.Deliverable, strings.Join(t.DependsOn, ", "), workflowStatus(e.i18n, t.Status), t.UpdatedAt.Local().Format("01-02 15:04:05"), t.Instruction)).
		Markdown(t.Result).Markdown(t.Evidence)
	if t.Progress != nil && taskActive(t.Status) {
		c.Markdown(t.Progress.text(e.i18n, time.Now()))
	}
	if links, ok := platform.(WorkflowMessageLinker); ok && t.RootMessageID != "" {
		link, err := links.WorkflowMessageLink(e.ctx, t.RootMessageID)
		if err != nil {
			c.Note(e.i18n.Tf(MsgDashboardLinkFailed, err.Error()))
		} else {
			c.Markdown("[" + e.i18n.T(MsgDashboardTopic) + "](" + link + ")")
		}
	}
	if links, ok := platform.(WorkflowMessageLinker); ok {
		for _, id := range append(append([]string(nil), t.ProcessMessageIDs...), t.ResultMessageID) {
			if id == "" {
				continue
			}
			label := e.i18n.T(MsgExecOpenOriginal)
			if id == t.ResultMessageID {
				label = e.i18n.T(MsgQueryAnswer)
			}
			if link, err := links.WorkflowMessageLink(e.ctx, id); err == nil {
				c.Markdown("[" + label + "](" + link + ")")
			} else {
				c.Note(e.i18n.Tf(MsgDashboardLinkFailed, err.Error()))
			}
		}
	}
	c.Buttons(DefaultBtn(e.i18n.T(MsgDashboardCurrent), "cmd:/project dashboard"))
	card := reusableCard(c.Build())
	if len(card.RenderText()) > 12000 {
		return e.sendWorkflowFile(platform, msg, []byte(card.RenderText()), t.ID+"-detail.txt")
	}
	e.sendProjectPanel(platform, msg, card)
	return nil
}
