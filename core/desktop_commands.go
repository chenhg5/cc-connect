package core

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// Split only routing fields; preserve quotes, JSON, whitespace and newlines in the answer.
func desktopFields(raw string, count int) ([]string, string) {
	fields := make([]string, 0, count)
	for len(fields) < count {
		raw = strings.TrimLeftFunc(raw, unicode.IsSpace)
		if raw == "" {
			break
		}
		i := strings.IndexFunc(raw, unicode.IsSpace)
		if i < 0 {
			fields = append(fields, raw)
			raw = ""
			break
		}
		fields = append(fields, raw[:i])
		raw = raw[i:]
	}
	return fields, strings.TrimLeftFunc(raw, unicode.IsSpace)
}

func (e *Engine) cmdDesktop(p Platform, msg *Message, command, raw string) {
	count := 2
	if command == "answer" {
		count = 3
	}
	fields, text := desktopFields(raw, count)
	if len(fields) != count || text == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopUsage))
		return
	}
	id := fields[1]
	mode := "queue"
	if command == "reply" && strings.HasPrefix(text, "--") {
		options, remaining := desktopFields(text, 1)
		if len(options) != 1 || (options[0] != "--queue" && options[0] != "--now") || remaining == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopUsage))
			return
		}
		mode, text = strings.TrimPrefix(options[0], "--"), remaining
	}
	directory, ok := e.threadRoute(msg.SessionKey, id)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopUnknown))
		return
	}
	agent, _, err := e.getOrCreateWorkspaceAgent(directory)
	if err == nil {
		if validator, ok := agent.(interface{ ValidateDesktopThread(string) error }); ok {
			err = validator.ValidateDesktopThread(id)
		} else {
			err = fmt.Errorf("desktop thread validation unavailable")
		}
	}
	if err == nil {
		switch command {
		case "reply":
			if owner, ok := agent.(interface {
				ReplyToThreadWithMode(context.Context, string, string, string, string) error
			}); ok {
				messageID := ""
				if msg.MessageID != "" {
					messageID = msg.Platform + ":" + msg.SessionKey + ":" + msg.MessageID
				}
				err = owner.ReplyToThreadWithMode(e.ctx, id, text, mode, messageID)
			} else {
				err = fmt.Errorf("desktop helper unavailable")
			}
		case "answer":
			if owner, ok := agent.(interface {
				AnswerThreadRequest(context.Context, string, string, string) error
			}); ok {
				err = owner.AnswerThreadRequest(e.ctx, id, fields[2], text)
			} else {
				err = fmt.Errorf("desktop helper unavailable")
			}
		}
	}
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgDesktopError, err))
		return
	}
	acknowledgement := MsgDesktopAccepted
	if command == "reply" && mode == "queue" {
		acknowledgement = MsgDesktopQueued
	}
	e.reply(p, msg.ReplyCtx, e.i18n.T(acknowledgement))
}
