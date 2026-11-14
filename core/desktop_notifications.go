package core

import (
	"fmt"
	"path/filepath"
	"time"
)

type threadDestination struct{ session, thread string }

type desktopThreadValidator interface {
	ValidateDesktopThreadAt(id, directory string) error
}

type desktopThreadRoute struct {
	directory string
	expiresAt time.Time
}

const desktopRouteTTL = 24 * time.Hour

// The owner-only local API registers routes for one exact messaging destination.
// Receiving another destination's UUID must not grant access to that route.
func (e *Engine) registerThreadNotification(session, directory, id string, expiresAt int64) error {
	if session == "" || id == "" || !filepath.IsAbs(directory) {
		return fmt.Errorf("session key, thread ID and absolute work directory are required")
	}
	now := time.Now()
	expiry := now.Add(desktopRouteTTL)
	if expiresAt != 0 {
		expiry = time.Unix(expiresAt, 0)
		if !expiry.After(now) || expiry.After(now.Add(desktopRouteTTL)) {
			return fmt.Errorf("desktop route expiry must be within the next 24 hours")
		}
	}
	directory, err := normalizeSendWorkDir(directory, "")
	if err != nil {
		return err
	}
	validator, ok := e.agent.(desktopThreadValidator)
	if !ok {
		return fmt.Errorf("desktop thread validation is unavailable")
	}
	if err := validator.ValidateDesktopThreadAt(id, directory); err != nil {
		return err
	}
	e.pruneDesktopRoutes(now)
	e.desktopThreads.Store(threadDestination{session, id}, desktopThreadRoute{directory, expiry})
	return nil
}

func (e *Engine) pruneDesktopRoutes(now time.Time) {
	e.desktopThreads.Range(func(key, value any) bool {
		if !value.(desktopThreadRoute).expiresAt.After(now) {
			e.desktopThreads.CompareAndDelete(key, value)
		}
		return true
	})
}

func (e *Engine) threadRoute(session, id string) (string, bool) {
	e.pruneDesktopRoutes(time.Now())
	route, ok := e.desktopThreads.Load(threadDestination{session, id})
	if !ok {
		return "", false
	}
	return route.(desktopThreadRoute).directory, true
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
	case "progress":
		return e.i18n.Tf(MsgDesktopProgress, req.ReplyThreadID, req.Message), nil
	default:
		return "", fmt.Errorf("unsupported desktop event")
	}
}
