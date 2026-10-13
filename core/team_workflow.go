package core

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WorkflowPlatform provides durable, bot-owned messages and explicit task threads.
// Updates must fail rather than silently creating replacement messages.
type WorkflowPlatform interface {
	UpsertWorkflowCard(context.Context, string, string, string, *Card) (string, error)
	TaskConversationKey(chatID, rootID string) string
	SetTaskThreadResolver(func(chatID, rootID string) bool)
}
type TaskStreamingCardPlatform interface {
	CreateTaskStreamingCard(context.Context, any, string) (StreamingCard, error)
}

func (s *TeamProjectStore) SetMemberLabel(id, label string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.labels == nil {
		s.labels = map[string]string{}
	}
	s.labels[id] = label
}
func (s *TeamProjectStore) memberLabel(id string) string {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.labels[id] != "" {
		return s.labels[id]
	}
	return id
}
func workflowStatus(i *I18n, status string) string {
	if status == "ended" {
		return i.T(MsgProjectStatusEnded)
	}
	keys := map[string]MsgKey{"planned": MsgWorkflowStatusPlanned, "cancelled": MsgWorkflowStatusCancelled, "active": MsgWorkflowStatusActive, "paused": MsgWorkflowStatusPaused, "queued": MsgWorkflowStatusQueued, "running": MsgWorkflowStatusRunning, "waiting_approval": MsgWorkflowStatusWaitingApproval, "waiting_input": MsgWorkflowStatusWaitingInput, "submitted": MsgWorkflowStatusSubmitted, "completed": MsgWorkflowStatusSubmitted, "accepted": MsgWorkflowStatusAccepted, "failed": MsgWorkflowStatusFailed, "blocked": MsgWorkflowStatusBlocked, "stopped": MsgWorkflowStatusStopped, "interrupted": MsgWorkflowStatusInterrupted}
	if k, ok := keys[status]; ok {
		return i.T(k)
	}
	return status
}
func sortedTasks(p TeamProject) []TeamTask {
	out := make([]TeamTask, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (e *Engine) taskCard(p TeamProject, t TeamTask) *Card {
	fill := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return e.i18n.T(MsgWorkflowMissing)
		}
		return truncateIf(s, 800)
	}
	body := t.Result
	if body == "" {
		body = t.Instruction
	}
	if t.Evidence != "" {
		body = t.Evidence
	}
	card := NewCard().Title(t.ID+" · "+truncateIf(t.Title, 80), "blue").Markdown(e.i18n.Tf(MsgWorkflowTask, e.teamProjects.memberLabel(t.Assignee), fill(t.Reason), fill(t.Deliverable), strings.Join(t.DependsOn, ", "), workflowStatus(e.i18n, t.Status), t.UpdatedAt.Local().Format("01-02 15:04:05"), truncateIf(body, 1200))).Build()
	if t.Progress != nil && taskActive(t.Status) {
		card.Elements = append(card.Elements, CardMarkdown{Content: t.Progress.text(e.i18n, time.Now())})
	}
	card.Elements = append(card.Elements, CardNote{Text: e.i18n.Tf(MsgWorkflowLogs, t.ID, t.ID)})
	return card
}
func (e *Engine) boardCard(p TeamProject, allTasks bool) *Card {
	tasks := sortedTasks(p)
	var lines []string
	accepted := 0
	for _, t := range tasks {
		if t.Status == "accepted" {
			accepted++
		}
	}
	if !allTasks && len(tasks) > 12 {
		tasks = tasks[len(tasks)-12:]
	}
	for _, t := range tasks {
		title := t.ID + " · " + truncateIf(strings.Join(strings.Fields(t.Title), " "), 70)
		line := fmt.Sprintf("- %s\n  %s · %s", title, e.teamProjects.memberLabel(t.Assignee), workflowStatus(e.i18n, t.Status))
		if t.Status == "failed" || t.Status == "blocked" || t.Status == "waiting_input" {
			line += "\n  " + truncateIf(strings.Join(strings.Fields(t.Result), " "), 160)
		}
		if t.Progress != nil && taskActive(t.Status) {
			line += "\n" + t.Progress.text(e.i18n, time.Now())
		}
		lines = append(lines, line)
	}
	// Old projects retain member reports, explicitly labelled without inventing tasks.
	if len(p.Tasks) == 0 {
		for _, bot := range p.Bots {
			if r, ok := p.Reports[bot]; ok {
				lines = append(lines, fmt.Sprintf("- %s · %s\n  %s", e.teamProjects.memberLabel(bot), workflowStatus(e.i18n, r.Status), truncateIf(r.Content, 160)))
			}
		}
	}
	if len(lines) == 0 {
		lines = []string{e.i18n.T(MsgWorkflowEmpty)}
	}
	state := "active"
	if p.Paused {
		state = "paused"
	}
	if p.State == "ended" {
		state = "ended"
	}
	card := NewCard().Title(e.i18n.Tf(MsgWorkflowBoard, p.Name), "blue").Markdown(e.i18n.Tf(MsgWorkflowSummary, truncateIf(p.Goal, 500), workflowStatus(e.i18n, state), projectDisplayMode(&p), len(p.Tasks), accepted, strings.Join(lines, "\n")))
	if p.State == "ended" {
		return card.Markdown(e.i18n.T(MsgProjectEnded)).Note(e.i18n.Tf(MsgProjectEndedAt, p.EndedAt)).Build()
	}
	return card.Markdown(e.i18n.T(MsgWorkflowHelp)).Buttons(DangerBtn(e.i18n.T(MsgProjectFinish), "cmd:/project finish")).Build()
}
func (e *Engine) refreshWorkflow(ctx context.Context, id string) error {
	s := e.teamProjects
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	all, err := s.All()
	if err != nil {
		return err
	}
	p, ok := all[id]
	if !ok {
		return fmt.Errorf("project not found")
	}
	s.runtimeMu.Lock()
	manager := s.engines[p.Manager]
	s.runtimeMu.Unlock()
	if manager == nil {
		return nil
	}
	wp, ok := manager.platformForName(p.Platform).(WorkflowPlatform)
	if !ok {
		return nil
	}
	if s.viewContent == nil {
		s.viewContent = map[string]string{}
	}
	board := manager.boardCard(p, false)
	boardKey := p.ID + ":board"
	if p.BoardMessageID == "" || s.viewContent[boardKey] != board.RenderText() {
		mid, err := wp.UpsertWorkflowCard(ctx, p.ChatID, p.BoardMessageID, p.ID+"-board", board)
		if err != nil {
			return err
		}
		if mid != p.BoardMessageID {
			if err = s.mutateProject(id, func(p *TeamProject) error { p.BoardMessageID = mid; return nil }); err != nil {
				return err
			}
		}
		s.viewContent[boardKey] = board.RenderText()
	}
	for _, t := range sortedTasks(p) {
		if t.RootMessageID == "" || t.NativeThread {
			continue
		}
		card := manager.taskCard(p, t)
		key := p.ID + ":" + t.ID
		if s.viewContent[key] == card.RenderText() {
			continue
		}
		if _, err = wp.UpsertWorkflowCard(ctx, p.ChatID, t.RootMessageID, p.ID+"-"+t.ID, card); err != nil {
			return err
		}
		s.viewContent[key] = card.RenderText()
	}

	return nil
}
func (e *Engine) queueWorkflowRefresh(id string) {
	// Conversation-first teams expose task messages and native thread replies.
	// The optional board remains available through /project board, not auto-posted.
	for _, p := range e.platforms {
		if _, ok := p.(TaskMessagePlatform); ok {
			return
		}
	}
	go func() {
		if err := e.refreshWorkflow(e.ctx, id); err != nil {
			slog.Warn("project overview update failed", "project", id, "error", err)
		}
	}()
}
func (e *Engine) handleProjectWorkflow(platform Platform, msg *Message, args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "dashboard", "board", "history", "executions", "instruction", "tasks", "task", "pause", "resume", "display", "finish":
	default:
		return false
	}
	p, err := e.teamProject(msg.SessionKey)
	if err != nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgDashboardFailed, err.Error()))
		return true
	}
	if p == nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectGroupRequired))
		return true
	}
	// Runtime reports and bot messages cannot resume a paused project.
	if msg.teamInternal {
		return true
	}
	if p.State == "ended" && (args[0] == "pause" || args[0] == "resume" || args[0] == "display") {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectEnded))
		return true
	}
	switch args[0] {
	case "finish":
		err = e.handleProjectFinish(platform, msg, *p, args)
	case "instruction":
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgRoundIdle))
	case "executions":
		err = e.sendExecutionHistory(platform, msg, *p, args)
	case "history":
		e.sendProjectPanel(platform, msg, e.roundHistoryCard(*p, args))
	case "pause", "resume":
		if !roundAcceptsWork(*p) {
			e.sendProjectPanel(platform, msg, e.dashboardCard(*p, 0))
			return true
		}
		if len(args) > 1 && (len(args) != 3 || args[1] != "round" || args[2] != strconv.FormatUint(projectRound(*p), 10)) {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgRoundArchived))
			return true
		}
		guard := e.teamProjects.projectGuard(p.ID)
		guard.Lock()
		latest, lookupErr := e.teamProject(msg.SessionKey)
		if lookupErr != nil {
			err = lookupErr
		} else if latest == nil || projectRound(*latest) != projectRound(*p) || !roundAcceptsWork(*latest) {
			err = errProjectFinishChanged
		} else if args[0] == "resume" && e.projectBusy(*p) {
			err = fmt.Errorf("%s", e.i18n.T(MsgProjectBusy))
		} else {
			err = e.teamProjects.setWorkflowPaused(p.ID, args[0] == "pause")
		}
		if err == nil && args[0] == "pause" {
			e.stopProject(*p)
		}
		guard.Unlock()
		if err == nil {
			if args[0] == "pause" {
				e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgWorkflowPaused))
			} else {
				e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgWorkflowResumed))
			}
			if latest, lookupErr := e.teamProject(msg.SessionKey); lookupErr == nil && latest != nil {
				e.sendProjectPanel(platform, msg, e.dashboardCard(*latest, 0))
			}
			e.queueWorkflowRefresh(p.ID)
		}
	case "display":
		if len(args) != 2 || (args[1] != "compact" && args[1] != "standard" && args[1] != "debug") {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgWorkflowHelp))
			return true
		}
		err = e.teamProjects.mutateProject(p.ID, func(p *TeamProject) error { p.DisplayMode = args[1]; return nil })
		if err == nil {
			e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgWorkflowSaved, args[1]))
			e.queueWorkflowRefresh(p.ID)
		}
	case "task":
		if len(args) < 2 {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgWorkflowHelp))
			return true
		}
		tasks := p.Tasks
		if len(args) == 4 && args[2] == "round" {
			tasks = nil
			for _, round := range p.Rounds {
				if strconv.FormatUint(round.Number, 10) == args[3] {
					tasks = round.Tasks
					break
				}
			}
		}
		t, ok := tasks[strings.ToUpper(args[1])]
		if !ok {
			err = fmt.Errorf("task not found: %s", args[1])
			break
		}
		if len(args) == 3 && args[2] == "logs" {
			err = e.sendTaskLog(platform, msg, *p, t)
		} else {
			err = e.sendProjectTaskDetail(platform, msg, *p, t)
		}
	case "tasks", "board", "dashboard":
		page := 0
		if len(args) > 1 {
			page = dashboardPage(args[1])
		}
		e.sendProjectPanel(platform, msg, e.dashboardCard(*p, page))
	}
	if err != nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
	}
	return true
}
func (e *Engine) stopProject(p TeamProject) {
	for _, bot := range e.teamProjects.projectEngines(p) {
		var keys []string
		bot.interactiveMu.Lock()
		for key, state := range bot.interactiveStates {
			state.mu.Lock()
			source := state.sourceSessionKey
			state.mu.Unlock()
			if projectSessionKey(source, p) || projectSessionKey(key, p) {
				keys = append(keys, key)
			}
		}
		bot.interactiveMu.Unlock()
		for _, key := range keys {
			if card := bot.queryCardForSession(key); card != nil {
				notice := MsgWorkflowPaused
				if p.RoundState == "ending" {
					notice = MsgRoundEnding
				} else if p.State == "ended" {
					notice = MsgProjectEnded
				}
				queryCardNotice(bot.ctx, card, bot.i18n.T(notice))
				if err := card.Finish(bot.ctx, "", CardStatusDone); err != nil {
					slog.Warn("paused card update failed", "error", err)
				}
			}
			bot.stopInteractiveSessionWithOptions(key, false, true)
		}
	}
}
func (e *Engine) sendWorkflowTasks(platform Platform, msg *Message, p TeamProject) error {
	var lines []string
	for _, t := range sortedTasks(p) {
		lines = append(lines, fmt.Sprintf("%s | %s | %s | %s", t.ID, t.Title, e.teamProjects.memberLabel(t.Assignee), workflowStatus(e.i18n, t.Status)))
	}
	return e.sendWorkflowFile(platform, msg, []byte(strings.Join(lines, "\n")), "tasks.txt")
}
func (e *Engine) sendWorkflowFile(platform Platform, msg *Message, data []byte, name string) error {
	if fs, ok := platform.(FileSender); ok {
		return fs.SendFile(e.ctx, msg.ReplyCtx, FileAttachment{Data: data, FileName: name, MimeType: "text/plain"})
	}
	for _, part := range SplitMessageCodeFenceAware(string(data), 3000) {
		e.reply(platform, msg.ReplyCtx, part)
	}
	return nil
}

func (e *Engine) workflowMessageStopped(key string, epoch uint64, state *interactiveState) bool {
	if state.isStopped() {
		return true
	}
	p, err := e.teamProject(key)
	return err != nil || (p != nil && (!roundAcceptsWork(*p) || p.Paused || p.WorkflowEpoch != epoch))
}
