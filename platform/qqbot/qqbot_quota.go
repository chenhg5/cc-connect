package qqbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
)

// QQ Bot API business error codes that matter for message delivery.
//
// The QQ Bot API answers most failures with a 2xx status and a non-zero
// "code" in the body, so the HTTP status alone does not say whether the
// message was delivered.
const (
	// qqCodePassiveReplyLimit is returned when the passive-reply quota of
	// the incoming event is exhausted. QQ allows only a handful of passive
	// replies per event (4 within 60 minutes for C2C), so a turn that
	// reports its progress in several messages burns through it — every
	// later message of that turn is then dropped (issue #2216).
	qqCodePassiveReplyLimit = 40034128
	// qqCodeMsgIDExpired is returned when the msg_id of the incoming event
	// is too old to reply to any more.
	qqCodeMsgIDExpired = 40034005
)

// qqAPIError is a QQ Bot business error: the HTTP call succeeded but the API
// answered with a non-zero business code.
type qqAPIError struct {
	Code    int
	Message string
}

func (e *qqAPIError) Error() string {
	return fmt.Sprintf("qqbot: api error %d: %s", e.Code, e.Message)
}

// checkQQResponseCode inspects the QQ business code in a response body.
//
// A 2xx response can still carry a failure code. Treating it as success
// meant the adapter saw "sent", the response held no message id, and the
// message was dropped without a word (issue #2216).
func checkQQResponseCode(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		// Not a JSON envelope: the caller's own decoding reports the problem.
		return nil
	}
	if envelope.Code == 0 {
		return nil
	}
	return &qqAPIError{Code: envelope.Code, Message: envelope.Message}
}

// isPassiveReplyLimit reports whether the error means the passive-reply
// channel of the incoming event cannot be used any more.
func isPassiveReplyLimit(err error) bool {
	var apiErr *qqAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == qqCodePassiveReplyLimit || apiErr.Code == qqCodeMsgIDExpired
}

// qqCodeOf returns the QQ business code of the error, or 0 when the error
// did not come from the API.
func qqCodeOf(err error) int {
	var apiErr *qqAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return 0
}

// sendWithActiveFallback performs a passive send and, when the passive-reply
// channel is exhausted, retries the same payload as an active message.
//
// The same endpoint without msg_id/msg_seq sends an active message, which
// does not consume the passive quota — so the rest of the turn still reaches
// the user. Active messages have their own limits and may need a permission
// the bot does not have; if that fails too, the returned error carries both
// failures and the caller logs the lost content.
func (p *Platform) sendWithActiveFallback(rctx *replyContext, send func(rctx *replyContext) error) error {
	err := send(rctx)
	if err == nil || !isPassiveReplyLimit(err) {
		return err
	}

	activeCtx := *rctx
	activeCtx.eventMsgID = ""
	if activeErr := send(&activeCtx); activeErr != nil {
		return fmt.Errorf("%w (active fallback failed: %v)", err, activeErr)
	}

	slog.Warn("qqbot: passive reply quota exhausted, resent as active message",
		"message_type", rctx.messageType,
		"code", qqCodeOf(err),
	)
	return nil
}

// maxLoggedChunkLen bounds how much of a dropped message is written to the
// error log.
const maxLoggedChunkLen = 500

// truncateChunk shortens a dropped chunk for the error log.
func truncateChunk(s string) string {
	runes := []rune(s)
	if len(runes) <= maxLoggedChunkLen {
		return s
	}
	return string(runes[:maxLoggedChunkLen]) + "...(truncated)"
}
