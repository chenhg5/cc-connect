package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

func (e *Engine) dispatchWorkflowTask(ctx context.Context, project TeamProject, req TeamActionRequest) (string, error) {
	if req.To == "" && req.TaskID != "" {
		req.To = project.Tasks[req.TaskID].Assignee
	}
	if _, ok := e.platformForName(project.Platform).(WorkflowPlatform); ok && req.TaskID == "" && (strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Deliverable) == "") {
		return "", fmt.Errorf("dispatch requires --title and --deliverable")
	}
	e.teamProjects.runtimeMu.Lock()
	target := e.teamProjects.engines[req.To]
	e.teamProjects.runtimeMu.Unlock()
	if target == nil || target == e || target.ctx.Err() != nil {
		return "", fmt.Errorf("target bot is not running")
	}
	// Serialize creation/routing against pause; no task is accepted until its visible root exists.
	guard := e.teamProjects.projectGuard(project.ID)
	guard.RLock()
	defer guard.RUnlock()
	current, lookupErr := e.teamProject(req.SessionKey)
	if lookupErr != nil {
		return "", lookupErr
	}
	if current == nil || current.WorkflowEpoch != project.WorkflowEpoch || projectRound(*current) != projectRound(project) || archivedManagerRound(*current, req.SessionKey) {
		return "", errProjectFinishChanged
	}
	task, err := e.teamProjects.assignTask(project.ID, req)
	if err != nil {
		return "", err
	}
	fail := func(cause error) (string, error) {
		if err := e.teamProjects.saveReport(project.ID, TeamMemberReport{Epoch: task.Epoch, TaskID: task.ID, Attempt: task.Attempt, ID: task.ID, Bot: task.Assignee, Status: "failed", Content: cause.Error()}); err != nil {
			slog.Warn("dispatch failure persistence failed", "error", err)
		}
		e.queueWorkflowRefresh(project.ID)
		return "", cause
	}
	_, msg, err := target.teamMessage(project, "")
	if err != nil {
		return fail(err)
	}
	if wp, ok := e.platformForName(project.Platform).(WorkflowPlatform); ok {
		if sender, ok := e.platformForName(project.Platform).(TaskMessagePlatform); ok {
			identity, ok := target.platformForName(project.Platform).(TaskMessagePlatform)
			if !ok || identity.BotMentionID() == "" {
				return fail(fmt.Errorf("target bot mention identity is unavailable"))
			}
			body := task.ID + " · " + task.Title + "\n" + task.Instruction + "\n" + task.Deliverable
			if task.Attempt > 1 {
				body = e.i18n.Tf(MsgWorkflowReworkTitle, task.ID, task.Attempt) + " · " + task.Title + "\n" + task.Instruction + "\n" + task.Deliverable
			}
			var mid string
			mid, err = sender.SendTaskMessage(ctx, project.ChatID, "", identity.BotMentionID(), body, fmt.Sprintf("%s-%s-%d", project.ID, task.ID, task.Attempt))
			if err == nil && mid == "" {
				err = fmt.Errorf("task handoff missing message ID")
			}
			task.RootMessageID = mid
			msg.MessageID = mid
			task.NativeThread = true
		} else {
			task.RootMessageID, err = wp.UpsertWorkflowCard(ctx, project.ChatID, task.RootMessageID, project.ID+"-"+task.ID, e.taskCard(project, task))
		}
		if err != nil {
			return fail(err)
		}
		tw, ok := target.platformForName(project.Platform).(WorkflowPlatform)
		if !ok {
			return fail(fmt.Errorf("member does not support task conversations"))
		}
		msg.SessionKey = tw.TaskConversationKey(project.ChatID, task.RootMessageID)
	}
	if task.RequesterID == "" {
		task.RequesterID = project.RequesterID
		if task.RequesterID == "" {
			task.RequesterID = project.Creator
		}
	}
	if task.ManagerSessionKey == "" {
		task.ManagerSessionKey = req.SessionKey
	}
	task.SessionKey = msg.SessionKey
	if err = e.teamProjects.updateTask(project.ID, task.ID, func(t *TeamTask) error {
		t.SessionKey = task.SessionKey
		t.RootMessageID = task.RootMessageID
		t.NativeThread, t.RequesterID, t.ManagerSessionKey = task.NativeThread, task.RequesterID, task.ManagerSessionKey
		return nil
	}); err != nil {
		return fail(err)
	}
	rc, err := target.platformForName(project.Platform).(ReplyContextReconstructor).ReconstructReplyCtx(msg.SessionKey)
	if err != nil {
		return fail(err)
	}
	msg.ReplyCtx = rc
	data, _ := json.Marshal(task)
	msg.Content = "Task delegated by the project manager. Execute only this task with the supplied inputs. Dependencies are owned by the manager: never poll other tasks, tell the manager whom to dispatch next, or assign work to another role. Publish native public progress, tools and the original final response with artifacts, verification and blockers. Do not produce private internal reasoning or a separate management report. Your result is submitted for manager acceptance, never self-accepted. Each rework instruction is a new mentioned group message: execute under that incoming message, not an earlier attempt's thread. The same task ID and supplied attempt history preserve prior instructions, results and their message references. The runtime publishes your final result as a new group message mentioning the manager.\n" + string(data)
	msg.teamEpoch = task.Epoch
	e.queueWorkflowRefresh(project.ID)
	go func() {
		accepted := false
		msg.OnAccepted = func() { accepted = true }
		target.ReceiveMessage(target.platformForName(project.Platform), msg)
		if !accepted {
			target.PublishTeamReport(msg.SessionKey, GenerateToken(16), "failed", "Task rejected before execution. Inspect configuration and access controls.")
		}
	}()
	result, _ := json.Marshal(map[string]any{"task_id": task.ID, "status": "queued", "assignee": req.To, "thread": task.RootMessageID})
	return string(result), nil
}
