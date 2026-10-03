package core

import (
	"fmt"
	"path/filepath"
)

type threadDestination struct{ session, thread string }

// The owner-only local API registers routes for one exact messaging destination.
// Receiving another destination's UUID must not grant access to that route.
func (e *Engine) registerThreadNotification(session, directory, id string) error {
	if session == "" || id == "" || !filepath.IsAbs(directory) {
		return fmt.Errorf("session key, thread ID and absolute work directory are required")
	}
	directory, err := normalizeSendWorkDir(directory, "")
	if err != nil {
		return err
	}
	agent, _, err := e.getOrCreateWorkspaceAgent(directory)
	if err != nil {
		return err
	}
	validator, ok := agent.(interface{ ValidateDesktopThread(string) error })
	if !ok {
		return fmt.Errorf("desktop thread validation is unavailable")
	}
	if err := validator.ValidateDesktopThread(id); err != nil {
		return err
	}
	e.desktopThreads.Store(threadDestination{session, id}, directory)
	return nil
}

func (e *Engine) threadRoute(session, id string) (string, bool) {
	directory, ok := e.desktopThreads.Load(threadDestination{session, id})
	if !ok {
		return "", false
	}
	return directory.(string), true
}

func (e *Engine) desktopNotice(req SendRequest) (string, error) {
	if req.DesktopEvent == "" {
		return req.Message, nil
	}
	if req.ReplyThreadID == "" {
		return "", fmt.Errorf("thread ID is required for a desktop event")
	}
	switch req.DesktopEvent {
	case "completed":
		return e.i18n.Tf(MsgDesktopCompleted, req.ReplyThreadID, req.Message), nil
	case "request":
		if req.DesktopRequestID == "" {
			return "", fmt.Errorf("request ID is required")
		}
		return e.i18n.Tf(MsgDesktopRequest, req.ReplyThreadID, req.DesktopRequestID, req.Message, req.ReplyThreadID, req.DesktopRequestID), nil
	case "progress":
		return e.i18n.Tf(MsgDesktopProgress, req.ReplyThreadID, req.Message), nil
	default:
		return "", fmt.Errorf("unsupported desktop event")
	}
}
