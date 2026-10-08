package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Shared runtimes keep one event consumer across both local and externally
// initiated turns. They never enter the subprocess foreground/unsolicited
// handoff, which drains queued events and auto-denies background approvals.
func (e *Engine) startSharedReader(state *interactiveState, as SharedAgentSession, session *Session, sessions *SessionManager, key string) {
	state.mu.Lock()
	if state.unsolicitedCancel != nil {
		state.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	state.unsolicitedCancel = cancel
	state.unsolicitedDone = done
	state.eventsNeedResync = false
	replayEvents := make(chan Event, 128)
	state.sharedReplayEvents = replayEvents
	state.mu.Unlock()
	activity := e.sharedWorkspaceActivity(state, sessions)
	activity.observe(as.RuntimeState(), Event{})
	go func() {
		defer close(done)
		defer cancel()
		defer activity.end()
		external := map[string]*sharedForeground{}
		tails := map[string]*sharedToolTail{}
		var completedOrder []string
		var tailTimer *time.Timer
		var tailOutputCh <-chan time.Time
		defer func() {
			if tailTimer != nil {
				tailTimer.Stop()
			}
			for _, presentation := range external {
				presentation.cancel()
			}
		}()
		for {
			var event Event
			var ok bool
			var replayed bool
			select {
			case <-ctx.Done():
				return
			case <-tailOutputCh:
				tailOutputCh = nil
				for _, tail := range tails {
					if tail.pending != nil {
						event := *tail.pending
						tail.pending = nil
						tail.lastUpdate = time.Now()
						e.renderSharedToolTail(state, tail, event)
					}
				}
				continue
			case event = <-replayEvents:
				ok, replayed = true, true
			case event, ok = <-as.Events():
			}
			if !ok {
				state.mu.Lock()
				f := state.sharedForeground
				state.mu.Unlock()
				if f != nil {
					f.route(Event{Type: EventError, Error: fmt.Errorf("shared observer disconnected")})
				}
				for _, presentation := range external {
					presentation.route(Event{Type: EventError, Error: fmt.Errorf("shared observer disconnected")})
				}
				return
			}
			if ctx.Err() != nil {
				return
			}
			if !replayed {
				activity.observe(as.RuntimeState(), event)
			}
			if event.Type == EventToolOutput && event.TurnID == "" {
				event.TurnID = as.RuntimeState().TurnID
			}
			if event.Type == EventNotice {
				e.showSharedNotice(state, event.Notice)
				continue
			}
			if event.Type == EventPermissionRequest || event.Type == EventPermissionResolved || event.Type == EventRuntimeStatus {
				e.handleSharedEvent(state, as, session, sessions, event)
				if event.Type != EventRuntimeStatus {
					state.mu.Lock()
					f := state.sharedForeground
					state.mu.Unlock()
					if f != nil {
						f.route(event)
					}
					for _, presentation := range external {
						presentation.route(event)
					}
				}
				continue
			}
			if event.TurnID != "" {
				tail := tails[event.TurnID]
				if tail == nil {
					tail = &sharedToolTail{}
					tails[event.TurnID] = tail
				}
				if !replayed {
					tail.observe(event, e.display.ToolMaxLen)
				}
				if tail.done && !isSharedToolEvent(event) && (!replayed || event.Type != EventResult) {
					// A late start/text/completion cannot resurrect a completed turn,
					// regardless of replay/live channel selection order.
					continue
				}
				if event.Type == EventResult && event.Done && !tail.done {
					tail.done = true
					completedOrder = append(completedOrder, event.TurnID)
					if len(completedOrder) > 256 {
						delete(tails, completedOrder[0])
						completedOrder = completedOrder[1:]
					}
				} else if isSharedToolEvent(event) && (tail.done || (external[event.TurnID] == nil && as.RuntimeState().TurnID != event.TurnID)) {
					// Old commands can outlive their model turn. They only update
					// tool presentation, never start a task with interrupt timers.
					e.showSharedToolTail(state, tail, event)
					if tail.pending != nil && tailOutputCh == nil {
						if tailTimer == nil {
							tailTimer = time.NewTimer(500 * time.Millisecond)
						} else {
							tailTimer.Reset(500 * time.Millisecond)
						}
						tailOutputCh = tailTimer.C
					}
					continue
				}
			}
			if event.Type == EventText && event.Metadata["delivery"] == "async" && len(event.Questions) > 0 {
				e.showSharedAsyncQuestions(state, as, event)
			}
			state.mu.Lock()
			f := state.sharedForeground
			state.mu.Unlock()
			if f != nil && f.route(event) {
				continue
			}
			if event.Type == EventUserMessage {
				if event.Content != "" {
					session.AddHistory("user", event.Content)
					sessions.Save()
				}
				continue
			}
			if event.TurnID != "" {
				presentation := external[event.TurnID]
				if presentation != nil && presentation.ctx.Err() != nil {
					delete(external, event.TurnID)
					presentation = nil
				}
				if presentation == nil && event.Type != EventResult && event.Type != EventError {
					presentation = e.startSharedExternalPresentation(state, as, session, sessions, key, event.TurnID)
					external[event.TurnID] = presentation
				}
				if presentation != nil && presentation.route(event) {
					if (event.Type == EventResult && event.Done) || event.Type == EventError {
						delete(external, event.TurnID)
					}
					continue
				}
			}
			if event.Type == EventToolOutput {
				// Completed tool results carry aggregated output. Do not
				// send raw transport deltas as standalone assistant replies.
				continue
			}
			e.handleSharedEvent(state, as, session, sessions, event)
		}
	}()
}

func (e *Engine) handleSharedEvent(state *interactiveState, as SharedAgentSession, session *Session, sessions *SessionManager, event Event) {
	state.mu.Lock()
	p, reply, sessionKey := state.platform, state.replyCtx, state.sharedSessionKey
	state.mu.Unlock()
	switch event.Type {
	case EventText:
		phase, _ := event.Metadata["phase"].(string)
		if phase == "commentary" && event.Content != "" {
			e.send(p, reply, event.Content)
		}
	case EventThinking:
		if e.display.ThinkingMessages && event.Content != "" {
			e.send(p, reply, event.Content)
		}
	case EventToolUse:
		if e.display.ToolMessages {
			e.send(p, reply, "🔧 "+event.ToolName+"\n"+truncateIf(event.ToolInput, e.display.ToolMaxLen))
		}
	case EventToolResult:
		if e.display.ToolMessages {
			text := e.formatToolResultEventFallback(event.ToolName, event.ToolResult, event.ToolStatus, event.ToolExitCode, event.ToolSuccess)
			if text != "" {
				e.send(p, reply, text)
			}
		}
	case EventPermissionRequest:
		e.addSharedPending(state, event)
	case EventPermissionResolved:
		e.resolveSharedPending(state, event.RequestID)
	case EventResult:
		status, _ := event.Metadata["turn_status"].(string)
		if status == "interrupted" {
			e.send(p, reply, e.sharedInterruptedMessage(as))
		}
		if event.Content != "" {
			text := event.Content
			session.AddHistory("assistant", event.Content)
			sessions.Save()
			if isSilentReply(text) {
				e.noteUserTurnCompleted(state)
				return
			}
			if stripped, ok := stripTrailingSilent(text); ok {
				text = stripped
			}
			if strings.TrimSpace(text) == "" {
				e.noteUserTurnCompleted(state)
				return
			}
			e.hooks.Emit(HookEvent{Event: HookEventMessageSent, SessionKey: sessionKey, Platform: p.Name(), Content: text})
			if footer := e.buildReplyFooter(state.agent, as, "", ""); footer != "" {
				text += "\n\n" + footer
			}
			for _, chunk := range SplitMessageCodeFenceAware(text, maxPlatformMessageLen) {
				e.send(p, reply, chunk)
			}
		}
		e.noteUserTurnCompleted(state)
	case EventRuntimeStatus:
		switch event.Content {
		case "reconnecting":
			e.send(p, reply, e.i18n.T(MsgSharedReconnecting))
		case "connected":
			e.send(p, reply, e.i18n.T(MsgSharedReconnected))
		}
	case EventError:
		if event.Error != nil {
			e.hooks.Emit(HookEvent{Event: HookEventError, SessionKey: sessionKey, Platform: p.Name(), Error: event.Error.Error()})
			e.send(p, reply, e.i18n.Tf(MsgError, event.Error))
		}
	}
}

func (e *Engine) addSharedPending(state *interactiveState, event Event) {
	state.mu.Lock()
	if state.sharedRequests == nil {
		state.sharedRequests = map[string]Event{}
	}
	state.sharedRequests[event.RequestID] = event
	state.mu.Unlock()
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		slog.Error("generate shared request token", "error", err)
		return
	}
	pending := &pendingPermission{RequestID: event.RequestID, ActionToken: base64.RawURLEncoding.EncodeToString(nonce), Decisions: event.Decisions, DecisionDetails: event.DecisionDetails, ToolName: event.ToolName, ToolInput: event.ToolInputRaw, InputPreview: event.ToolInput, Questions: event.Questions, Resolved: make(chan struct{})}
	state.mu.Lock()
	if _, ok := state.platform.(CardMessageUpdater); ok && len(event.Questions) == 0 {
		// Initialize before publishing pending; the pointer stays immutable.
		pending.card = &permissionCardState{}
	}
	if state.pending != nil && state.pending.RequestID == event.RequestID {
		state.mu.Unlock()
		return
	}
	for _, queued := range state.sharedPending {
		if queued.RequestID == event.RequestID {
			state.mu.Unlock()
			return
		}
	}
	show := state.pending == nil
	if show {
		state.pending = pending
	} else {
		state.sharedPending = append(state.sharedPending, pending)
	}
	p, sessionKey := state.platform, state.sharedSessionKey
	state.mu.Unlock()
	e.hooks.Emit(HookEvent{Event: HookEventPermissionRequested, SessionKey: sessionKey, Platform: p.Name(), Content: event.ToolInput})
	if show {
		e.sendSharedPrompt(state, pending)
	}
}

func (e *Engine) resolveSharedPending(state *interactiveState, id string) {
	state.mu.Lock()
	delete(state.sharedRequests, id)
	var next *pendingPermission
	var resolved *pendingPermission
	if state.pending != nil && state.pending.RequestID == id {
		resolved = state.pending
		state.pending.resolve()
		state.pending = nil
		if len(state.sharedPending) > 0 {
			next = state.sharedPending[0]
			state.sharedPending = state.sharedPending[1:]
			state.pending = next
		}
	}
	kept := state.sharedPending[:0]
	for _, pending := range state.sharedPending {
		if pending.RequestID == id {
			pending.resolve()
		} else {
			kept = append(kept, pending)
		}
	}
	state.sharedPending = kept
	f := state.sharedForeground
	state.mu.Unlock()
	if f != nil {
		f.route(Event{Type: EventPermissionResolved, RequestID: id})
	}
	if resolved != nil {
		e.resolveSharedPermissionCard(resolved)
	}
	if next != nil {
		e.sendSharedPrompt(state, next)
	}
}

func (e *Engine) sendSharedPrompt(state *interactiveState, pending *pendingPermission) {
	original := pending
	state.mu.Lock()
	p, reply := state.platform, state.replyCtx
	snapshot := pendingPermission{
		ActionToken:     pending.ActionToken,
		ToolName:        pending.ToolName,
		InputPreview:    pending.InputPreview,
		ToolInput:       pending.ToolInput,
		Decisions:       append([]string(nil), pending.Decisions...),
		DecisionDetails: pending.DecisionDetails,
		Questions:       append([]UserQuestion(nil), pending.Questions...),
		CurrentQuestion: pending.CurrentQuestion,
	}
	state.mu.Unlock()
	pending = &snapshot
	if len(pending.Questions) > 0 {
		e.sendSharedQuestionPrompt(p, reply, pending)
		return
	}
	text := e.sharedPermissionBody(pending)
	buttons := []ButtonOption{}
	var cardButtons []CardButton
	for _, decision := range pending.Decisions {
		label, ok := e.sharedDecisionLabel(decision)
		if !ok {
			continue
		}
		action := "cmd:/decision " + pending.ActionToken + " " + decision
		buttons = append(buttons, ButtonOption{Text: label, Data: action})
		cardButtons = append(cardButtons, CardButton{Text: label, Value: action, Type: "default"})
	}
	if bs, ok := p.(InlineButtonSender); ok && len(buttons) > 0 {
		if err := bs.SendWithButtons(e.ctx, reply, text, [][]ButtonOption{buttons}); err == nil {
			return
		} else {
			slog.Warn("shared permission buttons", "error", err)
		}
	}
	if supportsCards(p) {
		card := NewCard().Title(e.i18n.T(MsgPermCardTitle), "orange").Markdown(text).Buttons(cardButtons...).Note(e.i18n.Tf(MsgSharedDecisionHint, strings.Join(pending.Decisions, " / "))).Build()
		e.sendSharedPermissionCard(p, reply, original, card)
		return
	}
	e.send(p, reply, text+"\n"+e.i18n.Tf(MsgSharedDecisionHint, strings.Join(pending.Decisions, " / ")))
}

func (e *Engine) sendSharedQuestionPrompt(p Platform, reply any, pending *pendingPermission) {
	index := pending.CurrentQuestion
	if index < 0 || index >= len(pending.Questions) {
		return
	}
	q := pending.Questions[index]
	text := fmt.Sprintf("(%d/%d) %s", index+1, len(pending.Questions), q.Question)
	buttons := []ButtonOption{}
	cardButtons := []CardButton{}
	for i, opt := range q.Options {
		text += fmt.Sprintf("\n%d. %s", i+1, opt.Label)
		if opt.Description != "" {
			text += " — " + opt.Description
		}
		action := fmt.Sprintf("cmd:/answer %s %d %d", pending.ActionToken, index, i+1)
		buttons = append(buttons, ButtonOption{Text: opt.Label, Data: action})
		cardButtons = append(cardButtons, CardButton{Text: opt.Label, Value: action, Type: "default"})
	}
	text += "\n" + e.i18n.T(MsgSharedAnswerHint)
	skip := ButtonOption{Text: e.i18n.T(MsgSharedSkip), Data: "cmd:/skip " + pending.ActionToken}
	buttons = append(buttons, skip)
	cardButtons = append(cardButtons, CardButton{Text: skip.Text, Value: skip.Data, Type: "default"})
	if bs, ok := p.(InlineButtonSender); ok {
		rows := [][]ButtonOption{}
		for _, button := range buttons {
			rows = append(rows, []ButtonOption{button})
		}
		if err := bs.SendWithButtons(e.ctx, reply, text, rows); err == nil {
			return
		} else {
			slog.Warn("shared question buttons", "error", err)
		}
	}
	if supportsCards(p) {
		card := NewCard().Title(e.i18n.T(MsgAskQuestionTitle), "blue").Markdown(text)
		for _, button := range cardButtons {
			card.Buttons(button)
		}
		e.sendWithCard(p, reply, card.Build())
		return
	}
	e.send(p, reply, text)
}

func (e *Engine) handleSharedPending(p Platform, msg *Message, content, key string) bool {
	state, pending := e.lookupPending(key)
	if pending == nil {
		if requestState, _ := e.sharedRequestForSource(msg.SessionKey, ""); requestState != nil {
			state = requestState
			state.mu.Lock()
			pending = state.pending
			state.mu.Unlock()
		}
	}
	if state == nil {
		return false
	}
	state.mu.Lock()
	_, shared := state.agentSession.(SharedAgentSession)
	state.mu.Unlock()
	if !shared || pending == nil {
		return false
	}
	if msg.IsPermissionResponse {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
		return true
	}
	if len(pending.Questions) > 0 {
		e.answerSharedQuestion(p, msg, state, pending, "", -1, content)
		return true
	}
	decision := ""
	lower := strings.ToLower(strings.TrimSpace(content))
	for _, choice := range pending.Decisions {
		if strings.EqualFold(strings.TrimSpace(content), choice) {
			decision = choice
			break
		}
	}
	if decision == "" {
		if lower == "cancel" || lower == "取消" {
			decision = "cancel"
		} else if isAllowResponse(lower) && !isApproveAllResponse(lower) {
			decision = "allow"
		} else if isDenyResponse(lower) {
			decision = "deny"
		}
	}
	e.respondSharedDecision(p, msg, state, pending, decision)
	return true
}

func (e *Engine) respondSharedDecision(p Platform, msg *Message, state *interactiveState, pending *pendingPermission, decision string) {
	offered := false
	for _, choice := range pending.Decisions {
		if choice == decision {
			offered = true
		}
	}
	if !offered {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgSharedDecisionHint, strings.Join(pending.Decisions, " / ")))
		return
	}
	state.mu.Lock()
	as := state.agentSession
	current := state.pending == pending && len(pending.Questions) == 0
	state.mu.Unlock()
	if as == nil || !current {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
		return
	}
	if err := as.RespondPermission(pending.RequestID, PermissionResult{Behavior: decision}); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	e.updateSharedPermissionCard(pending, decision)
	e.resolveSharedPending(state, pending.RequestID)
	if !pending.card.wasUpdated() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedResponseSent))
	}
}

func (e *Engine) answerSharedQuestion(p Platform, msg *Message, state *interactiveState, pending *pendingPermission, token string, index int, content string) {
	state.mu.Lock()
	if state.pending != pending || (token != "" && token != pending.ActionToken) || (index >= 0 && index != pending.CurrentQuestion) {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
		return
	}
	current := pending.CurrentQuestion
	if current < 0 || current >= len(pending.Questions) {
		state.mu.Unlock()
		return
	}
	q := pending.Questions[current]
	answer := strings.TrimSpace(content)
	if answer == "" {
		state.mu.Unlock()
		return
	}
	if choice, err := strconv.Atoi(answer); err == nil && choice >= 1 && choice <= len(q.Options) {
		answer = q.Options[choice-1].Label
	}
	if !q.IsOther && len(q.Options) > 0 {
		matched := false
		for _, opt := range q.Options {
			if answer == opt.Label {
				matched = true
			}
		}
		if !matched {
			state.mu.Unlock()
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedAnswerHint))
			return
		}
	}
	if pending.Answers == nil {
		pending.Answers = map[int]string{}
	}
	pending.Answers[current] = answer
	if current+1 < len(pending.Questions) {
		pending.CurrentQuestion++
		state.mu.Unlock()
		e.sendSharedPrompt(state, pending)
		return
	}
	answers := map[string]any{}
	for i, q := range pending.Questions {
		answers[q.ID] = []string{pending.Answers[i]}
	}
	as := state.agentSession
	state.mu.Unlock()
	if as == nil {
		return
	}
	if err := as.RespondPermission(pending.RequestID, PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": answers}}); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	e.resolveSharedPending(state, pending.RequestID)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedResponseSent))
}
