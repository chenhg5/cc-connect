package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// AgentSessionSteerer adds user input to an exact, already-running turn.
// Send starts a turn; it must never be used as a fallback for this capability.
type AgentSessionSteerer interface {
	CurrentTurnID() string
	Steer(ctx context.Context, expectedTurnID, content string, images []ImageAttachment, files []FileAttachment) error
}

// AgentSessionSupplementPolicy lets one-shot backends reject legacy Send-based
// /ps injection without platform or agent names in the engine.
type AgentSessionSupplementPolicy interface{ SupportsLegacySupplement() bool }

var (
	ErrSteerNotActive = errors.New("steer target turn is no longer active")
	// ErrSteerOutcomeUnknown means input may have been accepted. Do not retry or
	// execute the same input from the queue without reconciling the outcome.
	ErrSteerOutcomeUnknown = errors.New("steer submission outcome is unknown")
)

// CardTaskAction preserves the authenticated callback identity independently
// of the opaque operation token carried in Action.
type CardTaskAction struct {
	Action, SessionKey, UserID, ChatID, MessageID string
	ReplyCtx                                      any
}

type CardTaskActionResult struct {
	Card      *Card
	Toast     string
	ToastType string
}

type CardTaskActionHandler func(context.Context, CardTaskAction) CardTaskActionResult

type CardTaskActionHandlerSetter interface {
	SetCardTaskActionHandler(CardTaskActionHandler)
}

func supplementCommandID(content string) string {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return ""
	}
	id := matchPrefix(strings.TrimPrefix(strings.ToLower(fields[0]), "/"), builtinCommands)
	if id == "steer" || id == "ps" {
		return id
	}
	return ""
}

// Preserve legacy agents' image-message routing while recognizing /ps and /btw
// as commands for native steering and one-shot sessions that explicitly opt out.
func (e *Engine) handlesImageSupplementCommand(content, interactiveKey string) bool {
	switch supplementCommandID(content) {
	case "steer":
		return true
	case "ps":
		e.interactiveMu.Lock()
		state := e.interactiveStates[interactiveKey]
		if state == nil {
			e.interactiveMu.Unlock()
			return true // No active session: report it instead of starting image work.
		}
		state.mu.Lock()
		e.interactiveMu.Unlock()
		defer state.mu.Unlock()
		if state.agentSession == nil {
			return true
		}
		if _, ok := state.agentSession.(AgentSessionSteerer); ok {
			return true
		}
		policy, ok := state.agentSession.(AgentSessionSupplementPolicy)
		return ok && !policy.SupportsLegacySupplement()
	default:
		return false
	}
}

type queuedTaskStatus uint8

const (
	taskQueued queuedTaskStatus = iota
	taskSubmitting
	taskAccepted
	taskCancelled
	taskUnknown
	taskFailed
	taskEnded
	taskExpired
)

func (s queuedTaskStatus) messageKey() MsgKey {
	switch s {
	case taskQueued:
		return MsgSteerQueued
	case taskSubmitting:
		return MsgSteerSubmitting
	case taskAccepted:
		return MsgSteerAccepted
	case taskCancelled:
		return MsgSteerCancelled
	case taskUnknown:
		return MsgSteerUnknown
	case taskFailed:
		return MsgSteerFailed
	case taskEnded:
		return MsgSteerEnded
	default:
		return MsgSteerExpired
	}
}

func (s queuedTaskStatus) terminal() bool {
	return s == taskAccepted || s == taskCancelled || s == taskUnknown || s == taskExpired
}

type queuedTaskAction struct {
	queued queuedMessage
	turnID string
	status queuedTaskStatus
}

func steerPreviewText(content string) string {
	text := html.EscapeString(truncateIf(content, 500))
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "#", "\\#", "!", "\\!", "|", "\\|").Replace(text)
}

func (e *Engine) steerCommandEnabled(userID string) bool {
	e.userRolesMu.RLock()
	defer e.userRolesMu.RUnlock()
	disabled := e.disabledCmds
	if e.userRoles != nil {
		if role := e.userRoles.ResolveRole(userID); role != nil {
			disabled = role.DisabledCmds
		}
	}
	return !disabled["steer"]
}

// lockQueueForDrain returns with state.mu locked. A pending steer request is
// resolved before dequeue so a rejected request retains its original FIFO slot.
// It does not block output streaming or /stop while waiting for the RPC.
func lockQueueForDrain(state *interactiveState) {
	for {
		state.mu.Lock()
		done := state.steerDone
		if done == nil {
			return
		}
		state.mu.Unlock()
		<-done
	}
}

func (e *Engine) queuedTaskCard(action *queuedTaskAction, token string) *Card {
	card := NewCard().UpdateAll(true).Title(e.i18n.T(action.status.messageKey()), "blue").Markdown(steerPreviewText(action.queued.content))
	switch action.status {
	case taskQueued:
		buttons := []CardButton{
			PrimaryBtn(e.i18n.T(MsgSteerButton), "steer:"+token),
			DefaultBtn(e.i18n.T(MsgSteerCancelButton), "unqueue:"+token),
		}
		for i := range buttons {
			buttons[i].Extra = map[string]string{"lang": string(e.i18n.CurrentLang())}
		}
		card.Buttons(buttons...).Note(e.i18n.T(MsgSteerQueueHint))
	}
	return card.Build()
}

// registerQueuedTaskLocked opts in only when both ends support typed actions
// and native steering. The map is session-local; old cards fail closed after
// session replacement or restart. Completed entries are bounded per session.
// future: Quote shortcuts, other platform buttons, default steering, and durable recovery are separate extensions.
func (e *Engine) registerQueuedTaskLocked(state *interactiveState, q *queuedMessage) *Card {
	if !e.steerCommandEnabled(q.userID) {
		return nil
	}
	if _, ok := q.platform.(CardTaskActionHandlerSetter); !ok {
		return nil
	}
	if !supportsCards(q.platform) || q.userID == "" || q.channelID == "" || state.turnUserID != q.userID {
		return nil
	}
	s, ok := state.agentSession.(AgentSessionSteerer)
	if !ok {
		return nil
	}
	turnID := s.CurrentTurnID()
	if turnID == "" {
		return nil
	}
	if state.queueActions == nil {
		state.queueActions = make(map[string]*queuedTaskAction)
	}
	if len(state.queueActions) >= 256 {
		for token, action := range state.queueActions {
			if action.status != taskSubmitting && taskQueueIndex(state, token) < 0 {
				delete(state.queueActions, token)
			}
		}
		if len(state.queueActions) >= 256 {
			return nil
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		slog.Error("queue action token generation failed", "error", err)
		return nil
	}
	q.actionToken = fmt.Sprintf("%x", random)
	// Receipts outlive the queued input. Retain only their routing metadata
	// and a cloned preview, never attachment buffers or the full prompt.
	action := &queuedTaskAction{queued: queuedMessage{
		content: strings.Clone(truncateIf(q.content, 500)), platform: q.platform,
		userID: q.userID, msgSessionKey: q.msgSessionKey, channelID: q.channelID,
	}, turnID: turnID, status: taskQueued}
	state.queueActions[q.actionToken] = action
	return e.queuedTaskCard(action, q.actionToken)
}

func taskQueueIndex(state *interactiveState, token string) int {
	for i, q := range state.pendingMessages {
		if q.actionToken == token {
			return i
		}
	}
	return -1
}

func (e *Engine) taskActionResult(action *queuedTaskAction, token string) CardTaskActionResult {
	typ := "info"
	if action.status == taskAccepted || action.status == taskCancelled {
		typ = "success"
	}
	if action.status == taskFailed || action.status == taskUnknown {
		typ = "error"
	}
	result := CardTaskActionResult{Toast: e.i18n.T(action.status.messageKey()), ToastType: typ}
	// A delayed nonterminal response could overwrite a newer accepted/cancelled
	// card. Keep the original queue card until an immutable outcome is known.
	if action.status.terminal() {
		result.Card = e.queuedTaskCard(action, token)
	}
	return result
}

func (e *Engine) handleTaskAction(p Platform, ctx context.Context, event CardTaskAction) CardTaskActionResult {
	invalid := CardTaskActionResult{Toast: e.i18n.T(MsgSteerExpired), ToastType: "error"}
	kind, token, ok := strings.Cut(event.Action, ":")
	if !ok || (kind != "steer" && kind != "unqueue") || event.UserID == "" || event.ChatID == "" {
		return invalid
	}
	if !e.steerCommandEnabled(event.UserID) {
		return invalid
	}
	iKey := e.interactiveKeyForSessionKey(event.SessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[iKey]
	if state == nil {
		e.interactiveMu.Unlock()
		return invalid
	}
	state.mu.Lock()
	e.interactiveMu.Unlock()
	action := state.queueActions[token]
	if state.stopped || action == nil || action.queued.platform != p || action.queued.userID != event.UserID || action.queued.msgSessionKey != event.SessionKey || action.queued.channelID != event.ChatID {
		state.mu.Unlock()
		return invalid
	}
	if action.status.terminal() || action.status == taskSubmitting {
		result := e.taskActionResult(action, token)
		state.mu.Unlock()
		return result
	}
	index := taskQueueIndex(state, token)
	if index < 0 {
		action.status = taskExpired
		result := e.taskActionResult(action, token)
		state.mu.Unlock()
		return result
	}
	if kind == "unqueue" {
		state.pendingMessages = slices.Delete(state.pendingMessages, index, index+1)
		action.status = taskCancelled
		result := e.taskActionResult(action, token)
		state.mu.Unlock()
		return result
	}
	s, supported := state.agentSession.(AgentSessionSteerer)
	if !supported || state.turnUserID != event.UserID {
		state.mu.Unlock()
		return invalid
	}
	if s.CurrentTurnID() != action.turnID {
		action.status = taskEnded
		result := e.taskActionResult(action, token)
		state.mu.Unlock()
		return result
	}
	if state.steerDone != nil {
		state.mu.Unlock()
		return CardTaskActionResult{Toast: e.i18n.T(MsgSteerBusy), ToastType: "info"}
	}
	done := make(chan struct{})
	state.steerDone = done
	action.status = taskSubmitting
	q := state.pendingMessages[index]
	session := state.turnSession
	state.mu.Unlock()

	prompt := e.buildSenderPrompt(q.content, q.userID, q.userName, q.msgPlatform, q.msgSessionKey, q.channelKey)
	submitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := s.Steer(submitCtx, action.turnID, prompt, q.images, q.files)
	cancel()
	state.mu.Lock()
	action.status = queuedSteerResultStatus(err)
	if err != nil && !errors.Is(err, ErrSteerOutcomeUnknown) && taskQueueIndex(state, token) < 0 {
		// A recall during submission already removed the input. A definite
		// rejection must not advertise a retryable item that no longer exists.
		action.status = taskCancelled
	}
	if err == nil || errors.Is(err, ErrSteerOutcomeUnknown) {
		if index := taskQueueIndex(state, token); index >= 0 {
			state.pendingMessages = slices.Delete(state.pendingMessages, index, index+1)
		}
	}
	state.steerDone = nil
	if err == nil && session != nil && !state.stopped {
		state.turnHistoryNext = session.insertHistory(state.turnHistoryNext, "user", q.content)
	}
	close(done)
	result := e.taskActionResult(action, token)
	state.mu.Unlock()
	if err != nil {
		slog.Warn("queue steer submission", "error", err, "message_id", q.messageID)
	}
	if err == nil {
		_, sessions := e.sessionContextForKey(q.msgSessionKey)
		if session != nil {
			sessions.Save()
		}
	}
	return result
}

func queuedSteerResultStatus(err error) queuedTaskStatus {
	switch {
	case err == nil:
		return taskAccepted
	case errors.Is(err, ErrSteerOutcomeUnknown):
		return taskUnknown
	case errors.Is(err, ErrSteerNotActive):
		return taskEnded
	default:
		return taskFailed
	}
}

func steerResultStatus(err error) MsgKey {
	switch {
	case err == nil:
		return MsgSteerAccepted
	case errors.Is(err, ErrSteerOutcomeUnknown):
		return MsgSteerUnknown
	case errors.Is(err, ErrSteerNotActive):
		return MsgSteerNoTurn
	default:
		return MsgSteerRejected
	}
}

func (e *Engine) cmdSteer(p Platform, msg *Message, args []string) {
	if !e.steerCommandEnabled(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerExpired))
		return
	}
	text := steerInputText(msg, args)
	if text == "" && len(msg.Images) == 0 && len(msg.Files) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerUsage))
		return
	}
	if msg.ExtraContent != "" {
		text = msg.ExtraContent + "\n" + text
	}
	iKey := e.interactiveKeyForSessionKey(msg.SessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[iKey]
	if state == nil {
		e.interactiveMu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerNoTurn))
		return
	}
	state.mu.Lock()
	e.interactiveMu.Unlock()
	s, supported := state.agentSession.(AgentSessionSteerer)
	if !supported {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerUnsupported))
		return
	}
	if state.stopped || state.platform != p || state.turnUserID == "" || state.turnUserID != msg.UserID {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerExpired))
		return
	}
	turnID := s.CurrentTurnID()
	if turnID == "" {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerNoTurn))
		return
	}
	if state.steerDone != nil {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSteerBusy))
		return
	}
	done := make(chan struct{})
	state.steerDone = done
	session := state.turnSession
	state.mu.Unlock()
	prompt := e.buildSenderPrompt(text, msg.UserID, msg.UserName, msg.Platform, msg.SessionKey, msg.ChannelKey)
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	err := s.Steer(ctx, turnID, prompt, msg.Images, msg.Files)
	cancel()
	state.mu.Lock()
	state.steerDone = nil
	if err == nil && session != nil && !state.stopped {
		state.turnHistoryNext = session.insertHistory(state.turnHistoryNext, "user", text)
	}
	close(done)
	state.mu.Unlock()
	if err != nil {
		slog.Warn("steer submission", "error", err, "message_id", msg.MessageID)
	}
	if err == nil {
		runMessageAccepted(msg)
		_, sessions := e.sessionContextForKey(msg.SessionKey)
		if session != nil {
			sessions.Save()
		}
	}
	e.reply(p, msg.ReplyCtx, e.i18n.T(steerResultStatus(err)))
}

// Command parsing may unquote arguments; preserve the original supplemental
// prose (including newlines/quotes) after stripping only the command token.
func steerInputText(msg *Message, args []string) string {
	raw := strings.TrimSpace(msg.Content)
	if msg.ExtraContent != "" {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, msg.ExtraContent))
	}
	fields := strings.Fields(raw)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		id := matchPrefix(strings.TrimPrefix(strings.ToLower(fields[0]), "/"), builtinCommands)
		if id == "steer" || id == "ps" {
			return strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
		}
	}
	return strings.TrimSpace(strings.Join(args, " "))
}
