package core

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

type desktopReplyOwner interface {
	ReplyToThreadWithMode(context.Context, string, string, string, string) error
}

type desktopAnswerOwner interface {
	AnswerThreadRequest(context.Context, string, string, string) error
}

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
	if len(msg.Images) > 0 || len(msg.Files) > 0 || msg.Audio != nil || msg.Location != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopTextOnly))
		return
	}
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
	if command == "reply" {
		options, remaining := desktopFields(text, 1)
		if options[0] == "--queue" || options[0] == "--now" {
			if remaining == "" {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopUsage))
				return
			}
			mode, text = strings.TrimPrefix(options[0], "--"), remaining
		}
	}
	directory, ok := e.threadRoute(msg.SessionKey, id)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopUnknown))
		return
	}
	validator, validatable := e.agent.(desktopThreadValidator)
	supported := false
	switch command {
	case "reply":
		_, supported = e.agent.(desktopReplyOwner)
	case "answer":
		_, supported = e.agent.(desktopAnswerOwner)
	}
	var agent Agent
	var err error
	if !validatable || !supported {
		err = fmt.Errorf("desktop helper unavailable")
	} else {
		err = validator.ValidateDesktopThreadAt(id, directory)
	}
	if err == nil {
		agent, _, err = e.getOrCreateWorkspaceAgent(directory)
	}
	if err == nil {
		switch command {
		case "reply":
			if owner, ok := agent.(desktopReplyOwner); ok {
				messageID := ""
				if msg.MessageID != "" {
					messageID = msg.Platform + ":" + msg.SessionKey + ":" + msg.MessageID
				}
				err = owner.ReplyToThreadWithMode(e.ctx, id, text, mode, messageID)
			} else {
				err = fmt.Errorf("desktop helper unavailable")
			}
		case "answer":
			if owner, ok := agent.(desktopAnswerOwner); ok {
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
