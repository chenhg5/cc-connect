package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// TaskMessagePlatform publishes a real mentioned message before work is routed.
// Empty root starts a new handoff, including rework. A non-empty root is a reply.
type TaskMessagePlatform interface {
	BotMentionID() string
	MentionText(string) string
	SendTaskMessage(context.Context, string, string, string, string, string) (string, error)
}

// ResultMessagePlatform publishes an intact final response in the group.
// The mention is required for bot handoffs, empty for ordinary human replies.
// Its returned ID is the recipient's processing thread.
type ResultMessagePlatform interface {
	TaskMessagePlatform
	SendResultMessage(context.Context, string, string, string, string) (string, error)
}

type TeamManagerContext struct {
	SourceMessageID   string    `json:"source_message_id,omitempty"`
	ResultMessageID   string    `json:"result_message_id,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	ProcessMessageIDs []string  `json:"process_message_ids,omitempty"`
	Round             uint64    `json:"round,omitempty"`
	RequesterID       string    `json:"requester_id"`
	Instruction       string    `json:"instruction"`
	SourceSessionKey  string    `json:"source_session_key,omitempty"`
}

func (e *Engine) registerTaskFollowup(msg *Message) (bool, error) {
	if e.teamProjects == nil || msg.teamInternal || msg.IsPermissionResponse || strings.HasPrefix(strings.TrimSpace(msg.Content), "/") {
		return false, nil
	}
	p, err := e.teamProject(msg.SessionKey)
	if err != nil || p == nil || p.Manager == e.name {
		return false, err
	}
	if archivedTaskSession(*p, e.name, msg.SessionKey) {
		return false, fmt.Errorf("%s", e.i18n.T(MsgWorkflowReworkInGroup))
	}
	t := taskForSession(*p, e.name, msg.SessionKey)
	if t == nil {
		return false, nil
	}
	if taskRound(*t) != projectRound(*p) || !roundAcceptsWork(*p) {
		return false, fmt.Errorf("%s", e.i18n.T(MsgRoundArchived))
	}
	if _, native := e.platformForName(p.Platform).(TaskMessagePlatform); native && !taskActive(t.Status) {
		return false, fmt.Errorf("%s", e.i18n.T(MsgWorkflowReworkInGroup))
	}
	if msg.MessageID == "" {
		msg.MessageID = GenerateToken(16)
	}
	duplicate := false
	err = e.teamProjects.updateTask(p.ID, t.ID, func(t *TeamTask) error {
		if stringSliceContains(t.SeenMessages, msg.MessageID) {
			duplicate = true
			return nil
		}
		if t.Followups == nil {
			t.Followups = map[string]string{}
		}
		t.Followups[msg.MessageID] = msg.Content
		t.SeenMessages = append(t.SeenMessages, msg.MessageID)
		return nil
	})
	return duplicate, err
}

// Called only after the session queue grants this turn execution. Pending input
// blocks acceptance immediately, but never starts a second concurrent attempt.
func (e *Engine) beginTaskFollowup(key, messageID string) error {
	if e.teamProjects == nil {
		return nil
	}
	p, err := e.teamProject(key)
	if err != nil || p == nil {
		return err
	}
	t := taskForSession(*p, e.name, key)
	if t == nil {
		return nil
	}
	if _, ok := t.Followups[messageID]; !ok {
		return nil
	}
	return e.teamProjects.mutateProject(p.ID, func(p *TeamProject) error {
		if !roundAcceptsWork(*p) || p.Paused {
			return fmt.Errorf("project paused")
		}
		t := p.Tasks[t.ID]
		instruction, ok := t.Followups[messageID]
		if !ok {
			return nil
		}
		nextTaskAttempt(&t, instruction)
		delete(t.Followups, messageID)
		t.Epoch = p.WorkflowEpoch
		t.UpdatedAt = time.Now().UTC()
		p.Tasks[t.ID] = t
		delete(p.Reports, e.name)
		return nil
	})
}

func (e *Engine) routeManagerThread(p Platform, msg *Message, project *TeamProject) error {
	if !e.teamCoordinator || msg.IsPermissionResponse || msg.teamInternal || msg.MessageID == "" || strings.HasPrefix(strings.TrimSpace(msg.Content), "/") {
		return nil
	}
	wp, ok := p.(WorkflowPlatform)
	if !ok {
		return nil
	}
	if _, ok := p.(TaskMessagePlatform); !ok {
		return nil
	}
	// Preserve a task-scoped session and origin without requiring a visible
	// platform thread. Platforms choose ordinary quoted replies for this scope.
	if cp, ok := p.(ProjectConversationPlatform); ok && msg.SessionKey == cp.ProjectConversationKey(project.ChatID, msg.UserID) {
		msg.SessionKey = wp.TaskConversationKey(project.ChatID, msg.MessageID)
		r, ok := p.(ReplyContextReconstructor)
		if !ok {
			return ErrNotSupported
		}
		var err error
		msg.ReplyCtx, err = r.ReconstructReplyCtx(msg.SessionKey)
		if err != nil {
			return err
		}
	}
	return e.teamProjects.mutateProject(project.ID, func(p *TeamProject) error {
		p.ManagerSessionKey, p.RequesterID = msg.SessionKey, msg.UserID
		if p.ManagerContexts == nil {
			p.ManagerContexts = map[string]TeamManagerContext{}
		}
		if _, exists := p.ManagerContexts[msg.SessionKey]; !exists {
			p.ManagerContexts[msg.SessionKey] = TeamManagerContext{SourceMessageID: msg.MessageID, CreatedAt: time.Now().UTC(), Round: projectRound(*p), RequesterID: msg.UserID, Instruction: msg.Content}
		}
		if !stringSliceContains(p.ManagerThreads, msg.SessionKey) {
			p.ManagerThreads = append(p.ManagerThreads, msg.SessionKey)
		}
		return nil
	})
}

func (e *Engine) cancelTaskFollowup(key, id string) {
	p, err := e.teamProject(key)
	if err != nil || p == nil {
		return
	}
	t := taskForSession(*p, e.name, key)
	if t == nil {
		return
	}
	if err := e.teamProjects.updateTask(p.ID, t.ID, func(t *TeamTask) error {
		body, ok := t.Followups[id]
		if !ok {
			return nil
		}
		if t.CancelledFollowups == nil {
			t.CancelledFollowups = map[string]string{}
		}
		t.CancelledFollowups[id] = body
		delete(t.Followups, id)
		return nil
	}); err != nil {
		slog.Warn("record cancelled rework input", "error", err)
	}
}
