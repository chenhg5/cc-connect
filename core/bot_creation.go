package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// PrivateUserMessageDetector is an optional platform capability. Unknown
// contexts fail closed; registration links must never be sent to group chats.
type PrivateUserMessageDetector interface {
	IsPrivateUserMessage(*Message) bool
}

// BotCreationFunc is wired by the application layer, which owns configuration
// persistence and engine lifecycle. Neither credentials nor device codes enter
// the engine or the language model. notify reports localized progress messages.
type BotCreationFunc func(context.Context, Platform, string, string, func(MsgKey, ...any))

func (e *Engine) SetBotCreationFunc(fn BotCreationFunc) { e.botCreationFunc = fn }

// SetBotOwnership configures an unclaimed or already claimed new identity.
// Call before Start. Pairing uses a one-time secret delivered to its creator.
func (e *Engine) SetBotOwnership(hash, owner string, save func(string) error) {
	e.botOwnerHash, e.botOwnerID, e.botOwnerSave = hash, owner, save
}

func privateUserMessage(p Platform, msg *Message) bool {
	d, ok := p.(PrivateUserMessageDetector)
	return ok && msg.UserID != "" && d.IsPrivateUserMessage(msg)
}

func isBotCreationCommand(raw string) bool {
	parts := splitCommandArgs(raw)
	return len(parts) >= 2 && strings.EqualFold(parts[0], "/new") && strings.EqualFold(parts[1], "bot")
}

func (e *Engine) handleBotOwnership(p Platform, msg *Message) bool {
	e.botSetupMu.Lock()
	defer e.botSetupMu.Unlock()
	if e.botOwnerHash == "" {
		if strings.HasPrefix(strings.TrimSpace(msg.Content), "/new claim") {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotClaimInvalid))
			return true
		}
		if e.botOwnerID != "" && e.botOwnerID != msg.UserID {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotOwnerRequired))
			return true
		}
		return false
	}
	parts := strings.Fields(msg.Content)
	if !privateUserMessage(p, msg) || len(parts) != 3 || parts[0] != "/new" || parts[1] != "claim" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotUnclaimed))
		return true
	}
	hash := sha256.Sum256([]byte(parts[2]))
	want, err := hex.DecodeString(e.botOwnerHash)
	if err != nil || subtle.ConstantTimeCompare(hash[:], want) != 1 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotClaimInvalid))
		return true
	}
	if e.botOwnerSave == nil || e.botOwnerSave(msg.UserID) != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotSaveFailed))
		return true
	}
	e.botOwnerHash, e.botOwnerID = "", msg.UserID
	e.SetAdminFrom(msg.UserID)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotClaimed))
	return true
}

func (e *Engine) cmdNewBot(p Platform, msg *Message, args []string) {
	if !privateUserMessage(p, msg) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotPrivateOnly))
		return
	}
	if e.botCreationFunc == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotUnsupported))
		return
	}
	e.botSetupMu.Lock()
	if len(args) == 1 && args[0] == "cancel" {
		if e.botSetupCancel != nil && e.botSetupSession == msg.SessionKey {
			e.botSetupCancel()
		}
		e.botSetupMu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotCancelled))
		return
	}
	if len(args) != 1 || !ValidBotName(args[0]) {
		e.botSetupMu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotUsage))
		return
	}
	if e.botSetupCancel != nil {
		e.botSetupMu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgBotBusy))
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	e.botSetupCancel, e.botSetupSession = cancel, msg.SessionKey
	e.botSetupMu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.botSetupMu.Lock()
			e.botSetupCancel, e.botSetupSession = nil, ""
			e.botSetupMu.Unlock()
		}()
		e.botCreationFunc(ctx, p, msg.UserID, args[0], func(key MsgKey, values ...any) {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(key, values...))
		})
	}()
}

// ValidBotName is also used at the persistence boundary to exclude path traversal.
func ValidBotName(name string) bool {
	if len(name) == 0 || len(name) > 48 || name == "cancel" {
		return false
	}
	for i, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (i > 0 && (c == '-' || c == '_')) {
			continue
		}
		return false
	}
	return true
}
