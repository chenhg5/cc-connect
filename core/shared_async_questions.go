package core

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// Async questions are advisory input to an active turn, never pending approvals.
// Tokens live on the attached observer, so detach/switch invalidates old cards.
type sharedAsyncQuestion struct {
	turnID, itemID, threadID string
	question                 UserQuestion
	responding, answered     bool
}

func (e *Engine) showSharedAsyncQuestions(state *interactiveState, as SharedAgentSession, event Event) {
	if full, ok := event.Metadata["message_text"].(string); ok {
		event.Content = full
	}
	runtime := as.RuntimeState()
	if event.ItemID == "" || event.TurnID == "" || runtime.TurnID != event.TurnID {
		return
	}
	state.mu.Lock()
	if state.sharedAsyncQuestions == nil {
		state.sharedAsyncQuestions = map[string]*sharedAsyncQuestion{}
	}
	for token, q := range state.sharedAsyncQuestions {
		if q.turnID != event.TurnID {
			delete(state.sharedAsyncQuestions, token)
		} else if q.itemID == event.ItemID {
			state.mu.Unlock()
			return
		}
	}
	// Bound memory without silently replacing still visible, actionable cards.
	if len(state.sharedAsyncQuestions)+len(event.Questions) > 64 {
		state.mu.Unlock()
		slog.Warn("shared async question limit reached")
		return
	}
	p, reply := state.platform, state.replyCtx
	type prompt struct {
		token string
		q     UserQuestion
	}
	var prompts []prompt
	for _, q := range event.Questions {
		nonce := make([]byte, 12)
		if _, err := rand.Read(nonce); err != nil {
			state.mu.Unlock()
			slog.Error("shared async question token", "error", err)
			return
		}
		token := base64.RawURLEncoding.EncodeToString(nonce)
		state.sharedAsyncQuestions[token] = &sharedAsyncQuestion{turnID: event.TurnID, itemID: event.ItemID, threadID: runtime.SessionID, question: q}
		prompts = append(prompts, prompt{token, q})
	}
	state.mu.Unlock()
	for index, prompt := range prompts {
		command := "/async-answer " + prompt.token + " text"
		body := prompt.q.Question + "\n\n" + e.i18n.Tf(MsgSharedAsyncHint, command)
		if index == 0 && event.Content != "" && event.Content != prompt.q.Question {
			body = event.Content + "\n\n" + body
		}
		card := NewCard().Title(e.i18n.T(MsgSharedAsyncTitle), "blue").Markdown(body)
		var rows [][]ButtonOption
		for i, opt := range prompt.q.Options {
			action := fmt.Sprintf("cmd:/async-answer %s option %d", prompt.token, i+1)
			card.Buttons(CardButton{Text: opt.Label, Value: action, Type: "default"})
			rows = append(rows, []ButtonOption{{Text: opt.Label, Data: action}})
			body += fmt.Sprintf("\n%d. %s", i+1, opt.Label)
		}
		if supportsCards(p) {
			e.sendWithCard(p, reply, card.Build())
			continue
		}
		if sender, ok := p.(InlineButtonSender); ok && len(rows) > 0 {
			if err := sender.SendWithButtons(e.ctx, reply, body, rows); err == nil {
				continue
			} else {
				slog.Warn("shared async question buttons", "error", err)
			}
		}
		e.send(p, reply, e.i18n.T(MsgSharedAsyncTitle)+"\n"+body)
	}
}

func (e *Engine) answerSharedAsyncQuestion(p Platform, msg *Message, state *interactiveState, as SharedAgentSession, args []string) {
	stale := func() { e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest)) }
	if len(args) < 3 || len(msg.Images) > 0 || len(msg.Files) > 0 {
		stale()
		return
	}
	runtime := as.RuntimeState()
	state.mu.Lock()
	q := state.sharedAsyncQuestions[args[0]]
	if q == nil || q.responding || q.answered || q.turnID != runtime.TurnID || q.threadID != runtime.SessionID || !runtime.Connected {
		state.mu.Unlock()
		stale()
		return
	}
	answer := strings.TrimSpace(strings.Join(args[2:], " "))
	switch args[1] {
	case "option":
		index, err := strconv.Atoi(answer)
		if err != nil || index < 1 || index > len(q.question.Options) {
			state.mu.Unlock()
			stale()
			return
		}
		answer = q.question.Options[index-1].Label
	case "text":
	default:
		state.mu.Unlock()
		stale()
		return
	}
	if answer == "" {
		state.mu.Unlock()
		stale()
		return
	}
	steerer, ok := as.(AgentSessionSteerer)
	if !ok {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedUnsupported))
		return
	}
	q.responding = true
	turnID, prompt := q.turnID, q.question.Question+": "+answer
	state.mu.Unlock()
	err := steerer.Steer(turnID, prompt)
	state.mu.Lock()
	q.responding = false
	if err == nil {
		q.answered = true
	}
	state.mu.Unlock()
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedSteerAccepted))
}
