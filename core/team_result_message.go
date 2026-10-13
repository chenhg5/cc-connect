package core

import (
	"context"
	"fmt"
	"time"
)

type QuotedResultMessagePlatform interface {
	SendQuotedResultMessage(context.Context, string, string, string, string, string) (string, error)
}

// Deliver before marking a task submitted or waking its recipient. A result
// message is a new handoff; process cards stay under the incoming message.
func (e *Engine) deliverWorkflowResult(ctx context.Context, p Platform, sessionKey, turnID, answer string, task *TeamTask) (bool, error) {
	sender, ok := p.(ResultMessagePlatform)
	if !ok || e.teamProjects == nil {
		return false, nil
	}
	wp, ok := p.(WorkflowPlatform)
	if !ok {
		return false, nil
	}
	project, err := e.teamProject(sessionKey)
	if err != nil || project == nil {
		return false, err
	}
	guard := e.teamProjects.projectGuard(project.ID)
	guard.RLock()
	defer guard.RUnlock()
	project, err = e.teamProject(sessionKey)
	if err != nil {
		return true, err
	}
	if project == nil || !roundAcceptsWork(*project) || project.Paused || archivedManagerRound(*project, sessionKey) {
		return true, fmt.Errorf("result belongs to a stopped project")
	}
	if archivedTaskSession(*project, e.name, sessionKey) {
		return true, fmt.Errorf("result belongs to a superseded task thread")
	}
	to := project.RequesterID
	if to == "" {
		to = project.Creator
	}
	origin := project.ManagerContexts[sessionKey]
	origin.Round = projectRound(*project)
	if origin.RequesterID == "" {
		origin.RequesterID = to
	}
	if task != nil {
		current, ok := project.Tasks[task.ID]
		if !ok || current.Attempt != task.Attempt || current.Epoch != task.Epoch || current.Epoch != project.WorkflowEpoch || !taskActive(current.Status) {
			return true, fmt.Errorf("result belongs to an inactive task attempt")
		}
		turnID = fmt.Sprintf("%s-%d", task.ID, task.Attempt)
		origin = project.ManagerContexts[task.ManagerSessionKey]
		origin.Round = projectRound(*project)
		origin.SourceSessionKey = task.ManagerSessionKey
		origin.RequesterID = task.RequesterID
		if origin.RequesterID == "" {
			origin.RequesterID = project.Creator
		}
		e.teamProjects.runtimeMu.Lock()
		manager := e.teamProjects.engines[project.Manager]
		e.teamProjects.runtimeMu.Unlock()
		if manager == nil {
			return true, fmt.Errorf("result recipient is unavailable")
		}
		identity, ok := manager.platformForName(project.Platform).(TaskMessagePlatform)
		if !ok || identity.BotMentionID() == "" {
			return true, fmt.Errorf("result recipient mention identity is unavailable")
		}
		to = identity.BotMentionID()
	}
	key := fmt.Sprintf("result:%s:%d:%s", e.name, project.WorkflowEpoch, turnID)
	if project.ResultMessages[key] != "" {
		return true, nil
	}
	// Bots need a structured mention to receive a handoff. Ordinary replies
	// to the requester should not notify them with an @ on every turn.
	if task == nil {
		to = ""
	}
	var mid string
	if quoted, ok := p.(QuotedResultMessagePlatform); ok {
		mid, err = quoted.SendQuotedResultMessage(ctx, project.ChatID, sessionKey, to, answer, project.ID+":"+key)
	} else {
		mid, err = sender.SendResultMessage(ctx, project.ChatID, to, answer, project.ID+":"+key)
	}
	if err != nil {
		return true, fmt.Errorf("publish result handoff: %w", err)
	}
	if mid == "" {
		return true, fmt.Errorf("result handoff missing message ID")
	}
	thread := wp.TaskConversationKey(project.ChatID, mid)
	err = e.teamProjects.mutateProject(project.ID, func(pr *TeamProject) error {
		if pr.ResultMessages == nil {
			pr.ResultMessages = map[string]string{}
		}
		pr.ResultMessages[key] = mid
		if pr.ManagerContexts == nil {
			pr.ManagerContexts = map[string]TeamManagerContext{}
		}
		if task == nil {
			previous := pr.ManagerContexts[sessionKey]
			previous.ResultMessageID = mid
			pr.ManagerContexts[sessionKey] = previous
		}
		origin.ProcessMessageIDs = nil
		origin.ResultMessageID = ""
		origin.SourceMessageID = mid
		origin.CreatedAt = time.Now().UTC()
		pr.ManagerContexts[thread] = origin
		if !stringSliceContains(pr.ManagerThreads, thread) {
			pr.ManagerThreads = append(pr.ManagerThreads, thread)
		}
		if task != nil {
			t := pr.Tasks[task.ID]
			if t.Attempt != task.Attempt || t.Epoch != task.Epoch {
				return fmt.Errorf("task changed while delivering result")
			}
			t.ResultMessageID = mid
			pr.Tasks[t.ID] = t
		}
		return nil
	})
	return true, err
}
