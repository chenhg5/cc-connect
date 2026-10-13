package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var groupReworkTaskID = regexp.MustCompile(`\bT[0-9]{3,}\b`)

// A human may address a member directly in the group with an explicit task ID.
// The incoming message itself is the handoff; do not send another assignment.
func (e *Engine) routeMemberGroupRework(p Platform, msg *Message, project *TeamProject) (bool, error) {
	if e.name == project.Manager || msg.teamInternal || msg.IsPermissionResponse || strings.HasPrefix(strings.TrimSpace(msg.Content), "/") {
		return false, nil
	}
	wp, ok := p.(WorkflowPlatform)
	if !ok {
		return false, nil
	}
	cp, ok := p.(ProjectConversationPlatform)
	if !ok || msg.SessionKey != cp.ProjectConversationKey(project.ChatID, msg.UserID) {
		return false, nil
	}
	if _, ok := p.(TaskMessagePlatform); !ok {
		return false, nil
	}
	ids := groupReworkTaskID.FindAllString(strings.ToUpper(msg.Content), -1)
	if len(ids) == 0 {
		return false, nil
	}
	for _, id := range ids {
		if id != ids[0] {
			return false, fmt.Errorf("%s", e.i18n.T(MsgWorkflowReworkInvalid))
		}
	}
	task, ok := project.Tasks[ids[0]]
	if !ok || task.Assignee != e.name || msg.MessageID == "" || msg.UserID == "" || (msg.UserID != task.RequesterID && msg.UserID != project.Creator) {
		return false, fmt.Errorf("%s", e.i18n.T(MsgWorkflowReworkInvalid))
	}
	key := wp.TaskConversationKey(project.ChatID, msg.MessageID)
	r, ok := p.(ReplyContextReconstructor)
	if !ok {
		return false, ErrNotSupported
	}
	rc, err := r.ReconstructReplyCtx(key)
	if err != nil {
		return false, err
	}
	task, err = e.teamProjects.assignTask(project.ID, TeamActionRequest{To: e.name, TaskID: task.ID, Message: msg.Content, sourceMessageID: msg.MessageID, sourceThread: key})
	if errors.Is(err, errDuplicateTaskHandoff) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	data, _ := json.Marshal(task)
	msg.SessionKey, msg.ReplyCtx = key, rc
	msg.teamInternal, msg.teamEpoch = true, task.Epoch
	msg.Content = "The original requester has sent a new group rework instruction for this task. Execute in this incoming message's thread. Preserve the task ID and prior attempt history. Return only your own original execution result; dependencies, acceptance and next assignments belong to the manager. The runtime sends your result as a new group message mentioning the manager.\n" + string(data)
	e.queueWorkflowRefresh(project.ID)
	return false, nil
}
