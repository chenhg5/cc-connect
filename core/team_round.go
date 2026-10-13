package core

import (
	"fmt"
	"strings"
	"time"
)

// The group and member model identities outlive a round of work. A round is
// distinct from both WorkflowEpoch (cancellation fence) and task Attempt.
type TeamRound struct {
	Number    uint64                      `json:"number"`
	Goal      string                      `json:"goal"`
	StartedAt string                      `json:"started_at,omitempty"`
	EndedAt   string                      `json:"ended_at"`
	EndedBy   string                      `json:"ended_by"`
	Reports   map[string]TeamMemberReport `json:"reports,omitempty"`
	Tasks     map[string]TeamTask         `json:"tasks"`
}

func projectRound(p TeamProject) uint64 {
	if p.Round == 0 {
		return 1
	}
	return p.Round
}
func taskRound(t TeamTask) uint64 {
	if t.Round == 0 {
		return 1
	}
	return t.Round
}
func roundAcceptsWork(p TeamProject) bool {
	return p.State == "ready" && (p.RoundState == "" || p.RoundState == "active")
}
func currentRoundProject(p TeamProject) TeamProject {
	tasks := make(map[string]TeamTask)
	for id, t := range p.Tasks {
		if taskRound(t) == projectRound(p) {
			tasks[id] = t
		}
	}
	p.Tasks = tasks
	return p
}
func roundGoal(p TeamProject) string {
	if p.RoundGoal != "" {
		return p.RoundGoal
	}
	return p.Goal
}
func archivedManagerRound(p TeamProject, key string) bool {
	if origin, ok := p.ManagerContexts[key]; ok {
		round := origin.Round
		if round == 0 {
			round = 1
		}
		return round != projectRound(p)
	}
	return false
}

// Only an authenticated human's fresh group instruction can open the next
// round. Read-only commands, callbacks, old threads and bot reports cannot.
func (e *Engine) prepareProjectRound(platform Platform, msg *Message, p *TeamProject) bool {
	command := strings.HasPrefix(strings.TrimSpace(msg.Content), "/")
	if p.RoundState == "ending" {
		if !command && !msg.teamInternal {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgRoundEnding))
		}
		return !command
	}
	if command || msg.IsPermissionResponse {
		return false
	}
	if archivedManagerRound(*p, msg.SessionKey) {
		if !msg.teamInternal {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgRoundArchived))
		}
		return true
	}
	// Platform timestamps fence late delivery even after a new round opened.
	if ended, err := time.Parse(time.RFC3339Nano, p.RoundEndedAt); err == nil && msg.UserMessageTimeMs > 0 && msg.UserMessageTimeMs <= ended.UnixMilli() {
		if !msg.teamInternal {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgRoundArchived))
		}
		return true
	}
	if p.RoundState != "idle" {
		return false
	}
	if msg.teamInternal {
		return true
	}
	fresh := e.name == p.Manager && msg.UserID != "" && strings.TrimSpace(msg.Content) != ""
	if cp, ok := platform.(ProjectConversationPlatform); ok {
		fresh = fresh && msg.SessionKey == cp.ProjectConversationKey(p.ChatID, msg.UserID)
	}
	if !fresh {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgRoundIdle))
		return true
	}
	var updated TeamProject
	err := e.teamProjects.mutateProject(p.ID, func(pr *TeamProject) error {
		if pr.RoundState == "idle" {
			pr.Round = projectRound(*pr) + 1
			pr.RoundState, pr.RoundGoal = "active", msg.Content
			pr.RoundStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
			pr.WorkflowEpoch++
			pr.RequesterID = msg.UserID
			// The ended reports are in the round snapshot; they cannot occupy
			// a member or become input to the new round's coordinator.
			pr.Reports = nil
			pr.ManagerSessionKey = ""
		}
		if !roundAcceptsWork(*pr) {
			return fmt.Errorf("%s", e.i18n.T(MsgRoundEnding))
		}
		updated = *pr
		return nil
	})
	if err != nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	*p = updated
	return false
}
