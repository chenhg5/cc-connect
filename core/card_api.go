package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// RawCardSender is an optional Platform interface for platforms that send a
// card authored by the caller in the platform's own format (a Feishu card
// JSON object, for example) and report the message it became. A non-empty
// uuid is the platform's send deduplication key (Feishu sends at most one
// message per uuid an hour).
type RawCardSender interface {
	SendRawCard(ctx context.Context, replyCtx any, card json.RawMessage, uuid string) (messageID string, err error)
}

// MaxCardUUIDLen is the longest /send card uuid, in characters (Feishu's
// limit on its message uuid).
const MaxCardUUIDLen = 50

// RawCardUpdater is an optional Platform interface for platforms that
// replace a card message they sent earlier, in place.
type RawCardUpdater interface {
	UpdateRawCard(ctx context.Context, messageID string, card json.RawMessage) error
}

// errCardTarget marks a /cards/update request whose platform cannot be
// determined (unknown name, or several card platforms and no session key).
var errCardTarget = errors.New("card platform")

// isJSONObject reports whether raw holds a JSON object.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var v map[string]any
	return json.Unmarshal(trimmed, &v) == nil
}

// SendCardToSession sends a caller-authored card into a session and returns
// its message id. The session is resolved like a text send: its live state,
// else a reply context rebuilt from the key. uuid, when set, deduplicates
// the send on the platform.
func (e *Engine) SendCardToSession(sessionKey string, card json.RawMessage, uuid string) (string, error) {
	_, p, replyCtx, err := e.resolveOutboundSessionTarget(sessionKey, false)
	if err != nil {
		return "", err
	}
	sender, ok := p.(RawCardSender)
	if !ok {
		return "", fmt.Errorf("platform %s cannot send cards: %w", p.Name(), ErrNotSupported)
	}
	if err := e.waitOutgoing(p); err != nil {
		return "", err
	}
	return sender.SendRawCard(e.ctx, replyCtx, card, uuid)
}

// hasCard reports whether a /send request carries a card (an explicit JSON
// null counts as none).
func (r SendRequest) hasCard() bool {
	trimmed := bytes.TrimSpace(r.Card)
	return len(trimmed) > 0 && string(trimmed) != "null"
}

// sendCard serves a /send request that carries a card.
func (s *APIServer) sendCard(w http.ResponseWriter, req SendRequest) {
	if !isJSONObject(req.Card) {
		http.Error(w, "card must be a JSON object", http.StatusBadRequest)
		return
	}
	if req.Message != "" || strings.TrimSpace(req.TTSText) != "" || len(req.Images) > 0 || len(req.Files) > 0 || len(req.Audios) > 0 || len(req.Videos) > 0 {
		http.Error(w, "card cannot be combined with message, tts_text or attachments", http.StatusBadRequest)
		return
	}
	if len(req.AtUsers) > 0 || req.AtAll {
		// A card is sent as authored; mentions belong inside the card.
		http.Error(w, "card cannot be combined with at_users or at_all", http.StatusBadRequest)
		return
	}
	if !validCardUUID(req.UUID) {
		http.Error(w, fmt.Sprintf("uuid must be at most %d characters", MaxCardUUIDLen), http.StatusBadRequest)
		return
	}
	engine, status, msg := s.resolveEngine(req.Project)
	if engine == nil {
		http.Error(w, msg, status)
		return
	}
	messageID, err := engine.SendCardToSession(req.SessionKey, req.Card, req.UUID)
	if err != nil {
		http.Error(w, err.Error(), cardErrorStatus(err))
		return
	}
	apiJSON(w, http.StatusOK, map[string]string{"status": "ok", "message_id": messageID})
}

// validCardUUID reports whether uuid fits MaxCardUUIDLen.
func validCardUUID(uuid string) bool {
	return utf8.RuneCountInString(uuid) <= MaxCardUUIDLen
}

// UpdateCard replaces the card message messageID in place. sessionKey, when
// set, picks the platform the way a send to that session does (see
// platformForSessionKey, so a workspace-prefixed key works too); otherwise
// the project must have exactly one platform that can update cards.
func (e *Engine) UpdateCard(sessionKey, messageID string, card json.RawMessage) error {
	platformName := ""
	if sessionKey != "" {
		p, _ := e.platformForSessionKey(sessionKey)
		if p == nil {
			return fmt.Errorf("%w for session key %q not found", errCardTarget, sessionKey)
		}
		platformName = p.Name()
	}
	var updater RawCardUpdater
	var target Platform
	var names []string
	for _, p := range e.platforms {
		if platformName != "" && p.Name() != platformName {
			continue
		}
		names = append(names, p.Name())
		if u, ok := p.(RawCardUpdater); ok {
			if updater != nil {
				if platformName != "" {
					// A session key names a platform, so it cannot tell
					// two platforms of the same name apart.
					return fmt.Errorf("%w: several platforms named %q can update cards and a session key cannot tell them apart", errCardTarget, platformName)
				}
				return fmt.Errorf("%w: several platforms can update cards; pass session_key to pick one", errCardTarget)
			}
			updater, target = u, p
		}
	}
	if updater == nil {
		return fmt.Errorf("platform %s cannot update cards: %w", strings.Join(names, ", "), ErrNotSupported)
	}
	if err := e.waitOutgoing(target); err != nil {
		return err
	}
	return updater.UpdateRawCard(e.ctx, messageID, card)
}

// CardUpdateRequest is the JSON body for POST /cards/update.
type CardUpdateRequest struct {
	Project string `json:"project,omitempty"`
	// SessionKey optionally names the session (and so the platform) the
	// card was sent to; needed only when a project has several card
	// platforms.
	SessionKey string          `json:"session_key,omitempty"`
	MessageID  string          `json:"message_id"`
	Card       json.RawMessage `json:"card"`
}

// cardErrorStatus maps a card send/update failure to an HTTP status: a
// platform without cards is 501, an undeterminable platform 400, anything
// else (the platform API failed) 500.
func cardErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotSupported):
		return http.StatusNotImplemented
	case errors.Is(err, errCardTarget):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (s *APIServer) handleCardsUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req CardUpdateRequest
	// Same body limit as /send, so a card that can be sent can be updated.
	if err := json.NewDecoder(io.LimitReader(r.Body, s.sendBodyLimit())).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.MessageID) == "" {
		http.Error(w, "message_id is required", http.StatusBadRequest)
		return
	}
	if !isJSONObject(req.Card) {
		http.Error(w, "card must be a JSON object", http.StatusBadRequest)
		return
	}
	engine, status, msg := s.resolveEngine(req.Project)
	if engine == nil {
		http.Error(w, msg, status)
		return
	}
	if err := engine.UpdateCard(req.SessionKey, strings.TrimSpace(req.MessageID), req.Card); err != nil {
		http.Error(w, err.Error(), cardErrorStatus(err))
		return
	}
	apiJSON(w, http.StatusOK, map[string]string{"status": "ok", "message_id": strings.TrimSpace(req.MessageID)})
}
