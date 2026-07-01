package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const relayTimeout = 60 * time.Second

// Default safety limits to prevent runaway relay loops (e.g. when an agent
// misinterprets a normal reply as a relay command, or when two agents
// volley a "relay" instruction back and forth).
const (
	defaultRelayBurstWindow = time.Minute
	defaultRelayBurstMax    = 10
)

const (
	RelayVisibilityFull    = "full"
	RelayVisibilitySummary = "summary"
	RelayVisibilityNone    = "none"
)

// RelayBinding represents a bot-to-bot relay binding in a group chat.
type RelayBinding struct {
	Platform string            `json:"platform"`
	ChatID   string            `json:"chat_id"`
	Bots     map[string]string `json:"bots"` // project name → bot display name
}

// RelayManager coordinates bot-to-bot message relay across engines.
type RelayManager struct {
	mu         sync.RWMutex
	engines    map[string]*Engine       // project name → engine (runtime only)
	bindings   map[string]*RelayBinding // chatID → binding
	storePath  string                   // empty = no persistence
	timeout    time.Duration
	visibility string

	// Per-source rate limit: tracks recent Send() times keyed by
	// "<chatID>::<from>". Drops a request when the window already contains
	// burstMax entries. Defends against agent self-relayed loops and
	// LLM-misinterpreted relay instructions. Window/max are tunable via
	// SetBurstLimit; zero burstMax disables the check.
	burstWindow time.Duration
	burstMax    int
	burstSeen   map[string][]time.Time
}

func NewRelayManager(dataDir string) *RelayManager {
	rm := &RelayManager{
		engines:     make(map[string]*Engine),
		bindings:    make(map[string]*RelayBinding),
		timeout:     relayTimeout,
		visibility:  RelayVisibilityFull,
		burstWindow: defaultRelayBurstWindow,
		burstMax:    defaultRelayBurstMax,
		burstSeen:   make(map[string][]time.Time),
	}
	if dataDir != "" {
		rm.storePath = filepath.Join(dataDir, "relay_bindings.json")
		rm.load()
	}
	return rm
}

// SetBurstLimit overrides the per-source rate limit. window is the rolling
// window; maxPerWindow is the cap. Pass maxPerWindow=0 to disable the check.
func (rm *RelayManager) SetBurstLimit(window time.Duration, maxPerWindow int) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if window < 0 {
		window = 0
	}
	if maxPerWindow < 0 {
		maxPerWindow = 0
	}
	rm.burstWindow = window
	rm.burstMax = maxPerWindow
}

// checkBurst records this relay attempt and returns an error if the rolling
// per-source budget is exhausted. Must be called with rm.mu NOT held.
func (rm *RelayManager) checkBurst(chatID, from string, now time.Time) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.burstMax <= 0 || rm.burstWindow <= 0 {
		return nil
	}
	key := chatID + "::" + from
	cutoff := now.Add(-rm.burstWindow)
	hits := rm.burstSeen[key]
	// Prune entries older than the window.
	pruned := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) >= rm.burstMax {
		// Keep the buffer so subsequent calls in the same window keep failing.
		rm.burstSeen[key] = pruned
		return fmt.Errorf(
			"relay: rate limit exceeded — %s sent %d relays in the last %s; "+
				"this usually means a loop (agents replying with relay commands) "+
				"or a misinterpreted message",
			from, len(pruned), rm.burstWindow)
	}
	rm.burstSeen[key] = append(pruned, now)
	return nil
}

func (rm *RelayManager) RegisterEngine(name string, e *Engine) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.engines[name] = e
}

// SetTimeout overrides the relay response timeout. Set to 0 to disable it.
func (rm *RelayManager) SetTimeout(d time.Duration) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if d < 0 {
		d = 0
	}
	rm.timeout = d
}

// SetVisibility controls whether relay request/response visibility messages are
// echoed into the source group chat. The relay transport still returns the
// target response to the caller regardless of this setting.
func (rm *RelayManager) SetVisibility(mode string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.visibility = normalizeRelayVisibility(mode)
}

// Bind establishes a relay binding between bots in a group chat.
// If a binding already exists, it will be replaced.
func (rm *RelayManager) Bind(platform, chatID string, bots map[string]string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.bindings[chatID] = &RelayBinding{
		Platform: platform,
		ChatID:   chatID,
		Bots:     bots,
	}
	slog.Info("relay: binding created", "chat_id", chatID, "bots", bots)
	rm.saveLocked()
}

// AddToBind adds a project to an existing binding, or creates a new one.
func (rm *RelayManager) AddToBind(platform, chatID, projectName string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	binding := rm.bindings[chatID]
	if binding == nil {
		binding = &RelayBinding{
			Platform: platform,
			ChatID:   chatID,
			Bots:     make(map[string]string),
		}
		rm.bindings[chatID] = binding
	}

	binding.Bots[projectName] = projectName
	slog.Info("relay: project added to binding", "chat_id", chatID, "project", projectName, "bots", binding.Bots)
	rm.saveLocked()
}

// RemoveFromBind removes a project from an existing binding.
// Returns true if the project was removed, false if not found.
func (rm *RelayManager) RemoveFromBind(chatID, projectName string) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	binding := rm.bindings[chatID]
	if binding == nil {
		return false
	}

	if _, exists := binding.Bots[projectName]; exists {
		delete(binding.Bots, projectName)
		slog.Info("relay: project removed from binding", "chat_id", chatID, "project", projectName, "remaining", binding.Bots)

		if len(binding.Bots) == 0 {
			delete(rm.bindings, chatID)
			slog.Info("relay: binding removed (no bots left)", "chat_id", chatID)
		}
		rm.saveLocked()
		return true
	}
	return false
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
	rm.saveLocked()
}

// HasEngine checks if a project engine is registered.
func (rm *RelayManager) HasEngine(name string) bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	_, ok := rm.engines[name]
	return ok
}

// Engine returns a registered project engine by name.
func (rm *RelayManager) Engine(name string) *Engine {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.engines[name]
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

	// Loop-defense: reject when the source has burst past the per-window budget.
	// Runs before any binding/engine resolution so loops can't waste lookups.
	if err := rm.checkBurst(chatID, req.From, time.Now()); err != nil {
		slog.Warn("relay: burst rejected", "from", req.From, "to", req.To, "chat_id", chatID, "error", err)
		return nil, err
	}

	rm.mu.RLock()
	binding := rm.bindings[chatID]
	targetEngine := rm.engines[req.To]
	sourceEngine := rm.engines[req.From]
	visibility := rm.visibility
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

	// Post the forwarded message to the group chat for visibility.
	groupSessionKey := platform + ":" + chatID + ":relay"
	if sourceEngine != nil && visibility != RelayVisibilityNone {
		label := relayVisibilityRequestLabel(visibility, fromName, toName, req.Message)
		rm.sendToGroup(ctx, sourceEngine, platform, groupSessionKey, label)
	}

	// Execute relay: inject message into target engine and collect response
	relayCtx, cancel := rm.relayContext(ctx)
	defer cancel()

	response, err := targetEngine.HandleRelay(relayCtx, req.From, req.SessionKey, req.Message)
	if err != nil {
		var notification string
		isTimeout := false
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "deadline exceeded") {
			isTimeout = true
			notification = fmt.Sprintf("%s appears to be hung (session locked beyond timeout). You can decide to wait, retry, or escalate.", toName)
		} else {
			notification = fmt.Sprintf("%s process has crashed. It will be restarted on next message, but context was lost.", toName)
		}

		if sourceEngine != nil && strings.TrimSpace(req.SessionKey) != "" {
			go func() {
				time.Sleep(100 * time.Millisecond)
				_ = sourceEngine.InjectRelayHandback(context.Background(), platform, req.SessionKey, toName, notification)
			}()
		}

		if isTimeout {
			return nil, fmt.Errorf("relay execution timed out. The target bot %q might be busy, hung, or working slowly. You can query its daemon-internal status by running: cc-connect status --project %s", req.To, req.To)
		}
		return nil, fmt.Errorf("relay: %w", err)
	}

	if sourceEngine != nil {
		if err := sourceEngine.InjectRelayHandback(ctx, platform, req.SessionKey, toName, response); err != nil {
			slog.Warn("relay: source handback injection failed",
				"from", req.From,
				"to", req.To,
				"session_key", req.SessionKey,
				"error", err,
			)
		}
	}

	// Post the response to the group chat for visibility. Source delivery no
	// longer depends on Telegram self-delivering bot-originated @mentions; the
	// source engine was updated directly above. The @mention remains useful for
	// human-readable chat context.
	if targetEngine != nil && visibility != RelayVisibilityNone {
		label := relayVisibilityResponseLabel(visibility, toName, response)
		if sourceEngine != nil {
			if srcUsername := sourceEngine.BotUsernameForPlatform(platform); srcUsername != "" {
				label = "@" + srcUsername + " " + label
			}
		}
		slog.Info("relay: posting response visibility", "to", toName, "response_len", len(response))
		rm.sendToGroup(ctx, targetEngine, platform, groupSessionKey, label)
	}

	return &RelayResponse{Response: response}, nil
}

// sendToGroup sends a message to the group chat for visibility.
func (rm *RelayManager) sendToGroup(ctx context.Context, e *Engine, platform, sessionKey, content string) {
	slog.Info("relay: sendToGroup", "engine", e.name, "platform", platform, "session_key", sessionKey, "content_len", len(content))
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
			slog.Warn("relay: failed to reconstruct reply ctx", "error", err)
			continue
		}
		if err := p.Send(ctx, rctx, content); err != nil {
			slog.Warn("relay: failed to send group message", "error", err)
		}
		return
	}
}

func truncateRelay(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}

func normalizeRelayVisibility(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case RelayVisibilityNone:
		return RelayVisibilityNone
	case RelayVisibilitySummary:
		return RelayVisibilitySummary
	case "", RelayVisibilityFull:
		return RelayVisibilityFull
	default:
		slog.Warn("relay: unknown visibility mode, falling back to full", "mode", mode,
			"valid_values", []string{RelayVisibilityNone, RelayVisibilitySummary, RelayVisibilityFull})
		return RelayVisibilityFull
	}
}

func relayVisibilityRequestLabel(mode, fromName, toName, message string) string {
	if normalizeRelayVisibility(mode) == RelayVisibilitySummary {
		return fmt.Sprintf("[%s → %s] relay request sent", fromName, toName)
	}
	return fmt.Sprintf("[%s → %s] %s", fromName, toName, message)
}

func relayVisibilityResponseLabel(mode, toName, response string) string {
	if normalizeRelayVisibility(mode) == RelayVisibilitySummary {
		return fmt.Sprintf("[%s] relay response ready (%d chars)", toName, len([]rune(response)))
	}
	return fmt.Sprintf("[%s] %s", toName, truncateRelay(response, 2000))
}

func (rm *RelayManager) relayContext(ctx context.Context) (context.Context, context.CancelFunc) {
	rm.mu.RLock()
	timeout := rm.timeout
	rm.mu.RUnlock()
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func parseSessionKeyParts(sessionKey string) (platform, chatID string, err error) {
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid session key format: %q", sessionKey)
	}

	if parts[0] == "relay" {
		// Relay session format:
		// "relay:sourceProject:platformName:chatID" or "relay:sourceProject:platformName:chatID:threadID"
		if len(parts) >= 4 {
			return parts[0], strings.Join(parts[2:], ":"), nil
		}
		if len(parts) == 3 {
			return parts[0], parts[2], nil
		}
		return "", "", fmt.Errorf("invalid relay session key format: %q", sessionKey)
	}

	// Normal session format:
	// For "platform:chatID:threadID:userID" (4 parts):
	if len(parts) == 4 && parts[0] == "telegram" {
		return parts[0], parts[1] + ":" + parts[2], nil
	}

	// For "platform:t:chatID:threadID:userID" (5 parts, type tag present):
	if len(parts) == 5 && len(parts[1]) == 1 {
		return parts[0], parts[2] + ":" + parts[3], nil
	}

	// Default fallback: return platform name and parts[1] (base chat ID)
	return parts[0], parts[1], nil
}

// ── Persistence ─────────────────────────────────────────────

// saveLocked persists bindings to disk. Caller must hold rm.mu (read or write).
func (rm *RelayManager) saveLocked() {
	if rm.storePath == "" {
		return
	}
	data, err := json.MarshalIndent(rm.bindings, "", "  ")
	if err != nil {
		slog.Error("relay: failed to marshal bindings", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(rm.storePath), 0o755); err != nil {
		slog.Error("relay: failed to create dir", "error", err)
		return
	}
	if err := AtomicWriteFile(rm.storePath, data, 0o644); err != nil {
		slog.Error("relay: failed to write bindings", "path", rm.storePath, "error", err)
	}
}

func (rm *RelayManager) load() {
	if rm.storePath == "" {
		return
	}
	data, err := os.ReadFile(rm.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("relay: failed to read bindings", "path", rm.storePath, "error", err)
		}
		return
	}
	var bindings map[string]*RelayBinding
	if err := json.Unmarshal(data, &bindings); err != nil {
		slog.Error("relay: failed to unmarshal bindings", "path", rm.storePath, "error", err)
		return
	}
	if bindings != nil {
		rm.bindings = bindings
		slog.Info("relay: loaded bindings", "count", len(bindings))
	}
}
