package core

import (
	"fmt"
	"strconv"
	"strings"
)

type progressSelection struct {
	usePrevious bool
	single      *int
	rangeStart  int
	rangeEnd    int
}

func parseProgressSelection(args []string) (progressSelection, error) {
	sel := progressSelection{}
	if len(args) == 0 {
		return sel, nil
	}
	if strings.EqualFold(args[0], "prev") {
		sel.usePrevious = true
		args = args[1:]
	}
	if len(args) == 0 {
		return sel, nil
	}
	if len(args) > 1 {
		return sel, fmt.Errorf("usage")
	}
	token := strings.TrimSpace(args[0])
	if token == "" {
		return sel, nil
	}
	if strings.Contains(token, ":") {
		parts := strings.SplitN(token, ":", 2)
		start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil || start <= 0 || end <= 0 || start > end {
			return sel, fmt.Errorf("range")
		}
		sel.rangeStart = start
		sel.rangeEnd = end
		return sel, nil
	}
	n, err := strconv.Atoi(token)
	if err != nil || n <= 0 {
		return sel, fmt.Errorf("selector")
	}
	sel.single = &n
	return sel, nil
}

func (e *Engine) cmdProgress(p Platform, msg *Message, args []string) {
	_, sessions, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	selection, err := parseProgressSelection(args)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProgressUsage))
		return
	}

	s := sessions.GetOrCreateActive(msg.SessionKey)
	timelines := s.GetEventTimelines()
	if len(timelines) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProgressEmpty))
		return
	}

	timelineIdx := len(timelines) - 1
	if selection.usePrevious {
		timelineIdx--
	}
	if timelineIdx < 0 || timelineIdx >= len(timelines) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProgressNoPrevious))
		return
	}

	timeline := timelines[timelineIdx]
	if len(timeline.Events) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProgressEmpty))
		return
	}

	events := timeline.Events
	if selection.single != nil {
		if *selection.single > len(events) {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgProgressEventNotFound, *selection.single))
			return
		}
		events = events[*selection.single-1 : *selection.single]
	} else if selection.rangeStart > 0 {
		if selection.rangeStart > len(events) {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgProgressRangeNotFound, selection.rangeStart, selection.rangeEnd))
			return
		}
		end := selection.rangeEnd
		if end > len(events) {
			end = len(events)
		}
		events = events[selection.rangeStart-1 : end]
	}

	items := buildProgressItemsFromTimeline(events, e.display)
	if len(items) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProgressEmpty))
		return
	}
	if progressStyleFor(p, msg.ReplyCtx, e.display) == progressStyleCard {
		if cap, ok := p.(ProgressCardPayloadSupport); ok && cap.SupportsProgressCardPayload() {
			payload := BuildProgressCardPayloadV2(items, false, normalizeProgressAgentLabel(e.agent.Name()), e.i18n.CurrentLang(), ProgressCardStateCompleted)
			if payload != "" {
				e.reply(p, msg.ReplyCtx, payload)
				return
			}
		}
	}

	e.reply(p, msg.ReplyCtx, e.renderProgressItemsText(items, selection.usePrevious))
}

func (e *Engine) renderProgressItemsText(items []ProgressCardEntry, previous bool) string {
	var sb strings.Builder
	if previous {
		sb.WriteString(e.i18n.Tf(MsgProgressHeaderPrevious, len(items), len(items)))
	} else {
		sb.WriteString(e.i18n.Tf(MsgProgressHeaderLatest, len(items), len(items)))
	}
	sb.WriteString("\n\n")
	for i, item := range items {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(e.progressItemHeader(item))
		if body := e.progressItemBody(item); body != "" {
			sb.WriteString("\n")
			sb.WriteString(body)
		}
	}
	return sb.String()
}

func (e *Engine) progressItemHeader(item ProgressCardEntry) string {
	switch item.Kind {
	case ProgressEntryThinking:
		return e.i18n.T(MsgProgressEventThinking)
	case ProgressEntryToolUse:
		head := e.i18n.T(MsgProgressEventToolUse)
		if strings.TrimSpace(item.Tool) != "" {
			head += " · " + strings.TrimSpace(item.Tool)
		}
		return head
	case ProgressEntryToolResult:
		head := e.i18n.T(MsgProgressEventToolResult)
		if strings.TrimSpace(item.Tool) != "" {
			head += " · " + strings.TrimSpace(item.Tool)
		}
		if item.ExitCode != nil {
			head += fmt.Sprintf(" · %s %d", e.i18n.T(MsgToolResultFmtExit), *item.ExitCode)
		}
		return head
	case ProgressEntryError:
		return e.i18n.T(MsgProgressEventError)
	default:
		return e.i18n.T(MsgProgressEventInfo)
	}
}

func (e *Engine) progressItemBody(item ProgressCardEntry) string {
	switch item.Kind {
	case ProgressEntryThinking, ProgressEntryError, ProgressEntryInfo:
		return strings.TrimSpace(item.Text)
	case ProgressEntryToolUse:
		if strings.TrimSpace(item.ToolInput) == "" {
			return ""
		}
		return fencedProgressBody(item.Tool, item.ToolInput)
	case ProgressEntryToolResult:
		lines := make([]string, 0, 3)
		if strings.TrimSpace(item.ToolInput) != "" {
			lines = append(lines, fencedProgressBody(item.Tool, item.ToolInput))
		}
		if strings.TrimSpace(item.ToolResult) != "" {
			lines = append(lines, fencedProgressBody("", item.ToolResult))
		} else if strings.TrimSpace(item.Status) != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", e.i18n.T(MsgToolResultFmtStatus), strings.TrimSpace(item.Status)))
		}
		return strings.Join(lines, "\n")
	default:
		return strings.TrimSpace(item.Text)
	}
}

func fencedProgressBody(toolName, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lang := toolCodeLang(toolName, text)
	if lang != "" {
		return "```" + lang + "\n" + text + "\n```"
	}
	return "```\n" + text + "\n```"
}
