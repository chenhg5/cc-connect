package core

import (
	"context"
	"encoding/json"
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
	if len(fields) != count || (command != "progress" && text == "") || (command == "progress" && text != "") {
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
		case "progress":
			if reader, ok := agent.(interface {
				ThreadProgress(context.Context, string) (json.RawMessage, error)
			}); ok {
				var data json.RawMessage
				data, err = reader.ThreadProgress(e.ctx, id)
				if err == nil {
					var report string
					report, err = e.desktopProgressText(id, data)
					if err == nil {
						e.reply(p, msg.ReplyCtx, report)
						return
					}
				}
			} else {
				err = fmt.Errorf("desktop progress unavailable")
			}
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

func (e *Engine) desktopProgressText(id string, data json.RawMessage) (string, error) {
	var report struct {
		Live      bool   `json:"live"`
		Status    string `json:"status"`
		Elapsed   int    `json:"elapsed_seconds"`
		Remaining *int   `json:"remaining_seconds"`
		Summary   string `json:"summary"`
		Plan      []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
		Pending []struct {
			ID         string `json:"request_id"`
			Answerable bool   `json:"answerable"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return "", fmt.Errorf("invalid progress response")
	}
	if report.Status == "" || report.Elapsed < 0 {
		return "", fmt.Errorf("invalid progress response")
	}
	status := MsgDesktopStatusUnknown
	switch report.Status {
	case "running":
		if report.Live {
			status = MsgDesktopStatusRunning
		}
	case "waiting":
		if report.Live {
			status = MsgDesktopStatusWaiting
		}
	case "completed":
		status = MsgDesktopStatusCompleted
	case "failed":
		status = MsgDesktopStatusFailed
	case "interrupted":
		status = MsgDesktopStatusInterrupted
	}
	source := MsgDesktopLive
	if !report.Live {
		source = MsgDesktopSaved
	}
	estimate := e.i18n.T(MsgDesktopEtaUnknown)
	completed := 0
	for _, step := range report.Plan {
		if step.Status == "completed" {
			completed++
		}
	}
	if report.Elapsed > 0 && completed > 0 && completed < len(report.Plan) && report.Live && report.Status == "running" && len(report.Pending) == 0 && report.Remaining != nil && *report.Remaining > 0 {
		estimate = e.i18n.Tf(MsgDesktopEtaRough, *report.Remaining)
	}
	lines := []string{e.i18n.Tf(MsgDesktopProgressReport, id, e.i18n.T(source), e.i18n.T(status), report.Elapsed, estimate)}
	for _, step := range report.Plan {
		mark := "○"
		switch step.Status {
		case "completed":
			mark = "✓"
		case "in_progress":
			mark = "▶"
		}
		lines = append(lines, mark+" "+step.Step)
	}
	if report.Summary != "" {
		lines = append(lines, "\n"+report.Summary)
	}
	for _, request := range report.Pending {
		if request.Answerable {
			lines = append(lines, "/answer "+id+" "+request.ID+" <answer>")
		} else {
			lines = append(lines, e.i18n.Tf(MsgDesktopAnswerUnknown, request.ID))
		}
	}
	return strings.Join(lines, "\n"), nil
}
