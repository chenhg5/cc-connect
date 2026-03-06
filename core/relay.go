package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const relayTimeout = 120 * time.Second

// RelayBinding represents a bot-to-bot relay binding in a group chat.
type RelayBinding struct {
	Platform string            // e.g. "feishu"
	ChatID   string            // group chat ID
	Bots     map[string]string // project name → bot display name
}

// RelayManager coordinates bot-to-bot message relay across engines.
type RelayManager struct {
	mu       sync.RWMutex
	engines  map[string]*Engine  // project name → engine
	bindings map[string]*RelayBinding // chatID → binding
}

func NewRelayManager() *RelayManager {
	return &RelayManager{
		engines:  make(map[string]*Engine),
		bindings: make(map[string]*RelayBinding),
	}
}

func (rm *RelayManager) RegisterEngine(name string, e *Engine) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.engines[name] = e
}

// Bind establishes a relay binding between two bots in a group chat.
func (rm *RelayManager) Bind(platform, chatID string, bots map[string]string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.bindings[chatID] = &RelayBinding{
		Platform: platform,
		ChatID:   chatID,
		Bots:     bots,
	}
	slog.Info("relay: binding created", "chat_id", chatID, "bots", bots)
}

// GetBinding returns the binding for a chat, or nil if none.
func (rm *RelayManager) GetBinding(chatID string) *RelayBinding {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.bindings[chatID]
}

// Unbind removes the relay binding for a chat.
func (rm *RelayManager) Unbind(chatID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.bindings, chatID)
	slog.Info("relay: binding removed", "chat_id", chatID)
}

// HasEngine checks if a project engine is registered.
func (rm *RelayManager) HasEngine(name string) bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	_, ok := rm.engines[name]
	return ok
}

// ListEngineNames returns all registered engine names.
func (rm *RelayManager) ListEngineNames() []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	names := make([]string, 0, len(rm.engines))
	for n := range rm.engines {
		names = append(names, n)
	}
	return names
}

// ListBoundBots returns the other bots bound in the same chat as the given project.
func (rm *RelayManager) ListBoundBots(chatID, selfProject string) map[string]string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	b := rm.bindings[chatID]
	if b == nil {
		return nil
	}
	others := make(map[string]string)
	for proj, name := range b.Bots {
		if proj != selfProject {
			others[proj] = name
		}
	}
	return others
}

// RelayRequest is the payload for a relay send.
type RelayRequest struct {
	From       string `json:"from"`        // source project name
	To         string `json:"to"`          // target project name
	SessionKey string `json:"session_key"` // source session key (contains platform + chatID)
	Message    string `json:"message"`
}

// RelayResponse is the result of a relay send.
type RelayResponse struct {
	Response string `json:"response"`
}

// Send delivers a message from one bot to another and returns the response.
func (rm *RelayManager) Send(ctx context.Context, req RelayRequest) (*RelayResponse, error) {
	platform, chatID, err := parseSessionKeyParts(req.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("relay: invalid session key: %w", err)
	}

	rm.mu.RLock()
	binding := rm.bindings[chatID]
	targetEngine := rm.engines[req.To]
	sourceEngine := rm.engines[req.From]
	rm.mu.RUnlock()

	if binding == nil {
		return nil, fmt.Errorf("relay: no binding for this chat. Use /bind <project> first")
	}
	if _, ok := binding.Bots[req.To]; !ok {
		var bound []string
		for proj := range binding.Bots {
			if proj != req.From {
				bound = append(bound, proj)
			}
		}
		return nil, fmt.Errorf("relay: project %q is not bound in this chat. Available targets: %s (use the exact name)", req.To, strings.Join(bound, ", "))
	}
	if targetEngine == nil {
		return nil, fmt.Errorf("relay: target engine %q not found (is the project running?)", req.To)
	}

	fromName := req.From
	if binding.Bots[req.From] != "" {
		fromName = binding.Bots[req.From]
	}
	toName := req.To
	if binding.Bots[req.To] != "" {
		toName = binding.Bots[req.To]
	}

	// Post the forwarded message to the group chat for visibility
	groupSessionKey := platform + ":" + chatID + ":relay"
	if sourceEngine != nil {
		label := fmt.Sprintf("[%s → %s] %s", fromName, toName, req.Message)
		rm.sendToGroup(ctx, sourceEngine, platform, groupSessionKey, label)
	}

	// Execute relay: inject message into target engine and collect response
	relayCtx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()

	response, err := targetEngine.HandleRelay(relayCtx, req.From, chatID, req.Message)
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}

	// Post the response to the group chat for visibility
	if targetEngine != nil {
		label := fmt.Sprintf("[%s] %s", toName, truncateRelay(response, 2000))
		rm.sendToGroup(ctx, targetEngine, platform, groupSessionKey, label)
	}

	return &RelayResponse{Response: response}, nil
}

// sendToGroup sends a message to the group chat for visibility.
func (rm *RelayManager) sendToGroup(ctx context.Context, e *Engine, platform, sessionKey, content string) {
	for _, p := range e.platforms {
		if p.Name() != platform {
			continue
		}
		rc, ok := p.(ReplyContextReconstructor)
		if !ok {
			continue
		}
		rctx, err := rc.ReconstructReplyCtx(sessionKey)
		if err != nil {
			slog.Debug("relay: failed to reconstruct reply ctx", "error", err)
			continue
		}
		if err := p.Send(ctx, rctx, content); err != nil {
			slog.Debug("relay: failed to send group message", "error", err)
		}
		return
	}
}

func truncateRelay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

func parseSessionKeyParts(sessionKey string) (platform, chatID string, err error) {
	// Format: "platform:chatID:userID"
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid session key format: %q", sessionKey)
	}
	return parts[0], parts[1], nil
}
