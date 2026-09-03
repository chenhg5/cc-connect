package matrix

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/chenhg5/cc-connect/core"
)

// statusIcon maps a card status to an emoji prefix for plain-text rendering.
func statusIcon(status core.CardStatus) string {
	switch status {
	case core.CardStatusThinking:
		return "🤔"
	case core.CardStatusWorking:
		return "⚙️"
	case core.CardStatusDone:
		return "✅"
	case core.CardStatusError:
		return "❌"
	}
	return "ℹ️"
}

// statusTitle returns a human title when the engine did not provide one.
func statusTitle(status core.CardStatus, title string) string {
	if title != "" {
		return title
	}
	switch status {
	case core.CardStatusThinking:
		return "Думаю…"
	case core.CardStatusWorking:
		return "Работаю…"
	case core.CardStatusDone:
		return "Готово"
	case core.CardStatusError:
		return "Ошибка"
	}
	return ""
}

// BuildRichCard renders the engine's streaming card as plain markdown text.
// Matrix has no native card widget: status, tool steps and the live body are
// composed into a single message that is later edited in place (m.replace).
func (p *Platform) BuildRichCard(status core.CardStatus, title string, steps []core.ToolStep, markdown string, streaming bool, statusFooter string) string {
	var b strings.Builder
	b.WriteString(statusIcon(status))
	if t := statusTitle(status, title); t != "" {
		b.WriteString(" **" + t + "**")
	}
	for _, st := range steps {
		mark := "▸"
		if st.Done {
			mark = "✓"
		}
		name := st.Name
		if st.Kind == core.ToolStepKindThinking {
			name = "думаю"
		}
		line := fmt.Sprintf("\n- %s `%s`", mark, name)
		if sm := strings.TrimSpace(st.Summary); sm != "" {
			line += ": " + firstLine(sm)
		}
		b.WriteString(line)
	}
	if md := strings.TrimSpace(markdown); md != "" {
		b.WriteString("\n\n---\n\n")
		b.WriteString(md)
	}
	if sf := strings.TrimSpace(statusFooter); sf != "" {
		b.WriteString("\n\n---\n")
		b.WriteString(sf)
	}
	if streaming {
		b.WriteString(" …")
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

// SendPreviewStart sends the initial preview ("thinking") message and returns
// a handle used by UpdateMessage to edit it in place while the turn streams.
func (p *Platform) SendPreviewStart(ctx context.Context, replyCtx any, content string) (any, error) {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("matrix: invalid reply context type %T", replyCtx)
	}
	parsed := format.RenderMarkdown(content, true, false)
	parsed.Body = content
	applyThreadRelation(&parsed.RelatesTo, rc)

	evtID, err := p.sendRoomEvent(ctx, rc.roomID, event.EventMessage, &parsed)
	if err != nil {
		return nil, err
	}
	p.rememberSent(rc, evtID)
	return replyContext{
		roomID:     rc.roomID,
		messageID:  evtID,
		threadID:   rc.threadID,
		sessionKey: rc.sessionKey,
	}, nil
}

// KeepPreviewOnFinish makes the engine replace the preview/thinking message
// with the final answer via UpdateMessage instead of sending a new one.
func (p *Platform) KeepPreviewOnFinish() bool { return true }
