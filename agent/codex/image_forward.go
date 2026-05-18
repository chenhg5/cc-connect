package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// generateImageForwardURL is the HTTP endpoint of the generate-image service.
var generateImageForwardURL = "http://localhost:3120/feishu/forward"

// setGenerateImageForwardURL overrides the forward URL (used in tests).
func setGenerateImageForwardURL(url string) {
	generateImageForwardURL = url
}

// imageSessionTimeout defines how long an active image generation session lasts
// before being automatically cleaned up.
// 30 minutes: image generation takes 3-5 min, plus user needs time to review
// and select variants.
const imageSessionTimeout = 30 * time.Minute

// imageForwardPayload is the JSON payload sent to the generate-image service.
type imageForwardPayload struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id"`
	Content   string `json:"content"`
	SenderID  string `json:"sender_id"`
	MsgType   string `json:"msg_type"`
	ImageURL  string `json:"image_url"`
}

// imageSessionKey uniquely identifies an active image generation session.
type imageSessionKey struct {
	ChatID   string
	SenderID string
}

// imageSessionEntry tracks the last interaction time for an active session.
type imageSessionEntry struct {
	LastActive time.Time
}

// imageSessionTracker manages active image generation sessions.
// It is safe for concurrent use.
type imageSessionTracker struct {
	mu       sync.Mutex
	sessions map[imageSessionKey]*imageSessionEntry
}

var globalImageSessions = &imageSessionTracker{
	sessions: make(map[imageSessionKey]*imageSessionEntry),
}

func init() {
	// Start background cleanup goroutine for expired image sessions.
	go globalImageSessions.cleanupLoop()
}

// markActive records that a user in a chat has an active image generation session.
func (t *imageSessionTracker) markActive(chatID, senderID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := imageSessionKey{ChatID: chatID, SenderID: senderID}
	t.sessions[key] = &imageSessionEntry{LastActive: time.Now()}
	slog.Debug("image_session: markActive",
		"chat_id", chatID,
		"sender_id", senderID,
	)
}

// isActive checks whether a user in a chat has an active image generation session.
func (t *imageSessionTracker) isActive(chatID, senderID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := imageSessionKey{ChatID: chatID, SenderID: senderID}
	entry, ok := t.sessions[key]
	if !ok {
		slog.Debug("image_session: isActive miss (no entry)",
			"chat_id", chatID,
			"sender_id", senderID,
		)
		return false
	}
	elapsed := time.Since(entry.LastActive)
	if elapsed > imageSessionTimeout {
		delete(t.sessions, key)
		slog.Debug("image_session: isActive miss (expired)",
			"chat_id", chatID,
			"sender_id", senderID,
			"elapsed", elapsed.String(),
		)
		return false
	}
	slog.Debug("image_session: isActive hit",
		"chat_id", chatID,
		"sender_id", senderID,
		"elapsed", elapsed.String(),
	)
	return true
}

// cleanupLoop periodically removes expired sessions.
func (t *imageSessionTracker) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		t.mu.Lock()
		now := time.Now()
		for key, entry := range t.sessions {
			if now.Sub(entry.LastActive) > imageSessionTimeout {
				delete(t.sessions, key)
			}
		}
		t.mu.Unlock()
	}
}

// isImageGenerationIntent checks whether the given message text indicates an
// image generation intent. Returns true if the message should be forwarded to
// the generate-image service instead of being processed by Codex CLI.
func isImageGenerationIntent(text string, chatID, senderID string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}

	trimmed = stripLeadingMention(trimmed)
	if trimmed == "" {
		return false
	}

	// Direct generate-image commands and common scene names.
	lowerTrimmed := strings.ToLower(trimmed)
	if isCodexCommandIntent(lowerTrimmed) {
		return false
	}
	directCommands := []string{
		"app-icon", "app icon", "app图标", "app 图标", "应用图标",
		"launch-screen", "launch screen", "启动页",
		"screenshot-polish", "screenshot polish", "截图优化",
		"general",
	}
	for _, cmd := range directCommands {
		if strings.Contains(lowerTrimmed, cmd) {
			return true
		}
	}

	// Chinese image generation patterns
	if strings.Contains(trimmed, "生图") || strings.Contains(trimmed, "产图") || strings.Contains(trimmed, "出图") {
		return true
	}
	if strings.HasPrefix(trimmed, "生成") && (strings.Contains(trimmed, "图") || strings.Contains(lowerTrimmed, "icon") || strings.Contains(trimmed, "头像") || strings.Contains(lowerTrimmed, "logo")) {
		return true
	}
	if strings.HasPrefix(trimmed, "生成一个") || strings.HasPrefix(trimmed, "生成一张") || strings.HasPrefix(trimmed, "生成个") {
		return true
	}
	if isChineseNaturalLanguageImageRequest(trimmed, lowerTrimmed) {
		return true
	}
	if strings.Contains(trimmed, "画一") {
		return true
	}

	// English image generation patterns
	englishImageIntents := []string{"generate image", "create image", "draw ", "make image"}
	for _, phrase := range englishImageIntents {
		if strings.Contains(lowerTrimmed, phrase) {
			return true
		}
	}

	// Follow-up commands for active image sessions
	if globalImageSessions.isActive(chatID, senderID) {
		if isImageFollowUpCommand(trimmed) {
			return true
		}
	}

	return false
}

func stripLeadingMention(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "@") {
		return trimmed
	}
	fields := strings.Fields(trimmed)
	if len(fields) <= 1 {
		return ""
	}
	return strings.TrimSpace(strings.Join(fields[1:], " "))
}

func isCodexCommandIntent(lowerText string) bool {
	commandPrefixes := []string{
		"gh-",
		"/gh",
		"git ",
	}
	for _, prefix := range commandPrefixes {
		if strings.HasPrefix(lowerText, prefix) {
			return true
		}
	}
	return false
}

func isChineseNaturalLanguageImageRequest(text, lowerText string) bool {
	if !strings.Contains(text, "生成") {
		return false
	}
	imageTargets := []string{"图", "图片", "头像", "logo", "icon"}
	hasImageTarget := false
	for _, target := range imageTargets {
		if strings.Contains(lowerText, target) || strings.Contains(text, target) {
			hasImageTarget = true
			break
		}
	}
	if !hasImageTarget {
		return false
	}

	requestPrefixes := []string{"给", "为", "帮", "请", "我要", "想要", "需要", "做", "画", "来"}
	for _, prefix := range requestPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// isImageFollowUpCommand checks if the message is a follow-up command for an
// active image generation session (e.g. selection numbers, retry, help).
func isImageFollowUpCommand(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}

	// Single digit selection (1-9) — covers all variant selections
	if utf8.RuneCountInString(trimmed) == 1 {
		r := []rune(trimmed)[0]
		if r >= '1' && r <= '9' {
			return true
		}
	}

	// Known follow-up commands
	lower := strings.ToLower(trimmed)
	followUps := []string{"retry", "details", "help", "/new", "upscale", "variation", "redo", "重试", "放大", "变体"}
	for _, cmd := range followUps {
		if lower == cmd {
			return true
		}
	}

	// "upscale 2", "variation 3" style commands
	if strings.HasPrefix(lower, "upscale ") || strings.HasPrefix(lower, "variation ") {
		return true
	}

	return false
}

// isImageMessage returns true if the message contains only images (image-to-image scenario).
func isImageMessage(prompt string, hasImages bool) bool {
	return hasImages
}

// forwardToGenerateImage sends the message payload to the generate-image service.
// It runs asynchronously and returns immediately. If the service is unreachable,
// it returns an error so the caller can fallback to Codex CLI.
func forwardToGenerateImage(payload imageForwardPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("image_forward: marshal payload: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(generateImageForwardURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("image_forward: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("image_forward: service returned status %d", resp.StatusCode)
	}

	slog.Info("image_forward: successfully forwarded to generate-image",
		"chat_id", payload.ChatID,
		"msg_type", payload.MsgType,
		"content_len", len(payload.Content),
	)
	return nil
}

// parseSessionKey extracts chatID and senderID from a CC_SESSION_KEY value.
// Expected formats:
//   - "feishu:{chatID}:{userID}"
//   - "lark:{chatID}:{userID}"
//   - "feishu:{chatID}:root:{rootID}" (thread isolation)
//   - "feishu:{chatID}" (share_session_in_channel mode)
//
// Returns empty strings if the key cannot be parsed.
func parseSessionKey(sessionKey string) (chatID, senderID string) {
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 2 {
		return "", ""
	}
	// parts[0] = platform (feishu/lark)
	// parts[1] = chatID
	chatID = parts[1]
	if len(parts) == 2 {
		// share_session_in_channel mode: "feishu:{chatID}"
		senderID = ""
	} else if parts[2] == "root" {
		// Thread isolation mode: "feishu:{chatID}:root:{rootID}"
		senderID = ""
	} else {
		// Normal mode: "feishu:{chatID}:{userID}"
		senderID = parts[2]
	}
	return chatID, senderID
}

// tryForwardImageRequest attempts to intercept and forward an image generation
// request. It returns true if the message was forwarded (caller should NOT send
// to Codex CLI), or false if the message should proceed normally.
//
// Parameters:
//   - prompt: the user message text
//   - hasImages: whether the message includes image attachments
//   - imagePaths: local paths of staged images (from stageImages)
//   - extraEnv: session environment variables (contains CC_SESSION_KEY)
//   - messageID: the platform message ID for reply context
//   - chatID: explicit chat ID (takes priority over CC_SESSION_KEY parsing)
//   - senderID: explicit sender ID (takes priority over CC_SESSION_KEY parsing)
func tryForwardImageRequest(prompt string, hasImages bool, imagePaths []string, extraEnv []string, messageID string, chatID string, senderID string) bool {
	// Resolve chatID/senderID: prefer explicit params, fall back to CC_SESSION_KEY in extraEnv.
	if chatID == "" {
		sessionKey := getenvFromList(extraEnv, "CC_SESSION_KEY")
		if sessionKey == "" {
			return false
		}
		// Only handle feishu/lark platforms
		if !strings.HasPrefix(sessionKey, "feishu:") && !strings.HasPrefix(sessionKey, "lark:") {
			return false
		}
		chatID, senderID = parseSessionKey(sessionKey)
	}

	if chatID == "" {
		slog.Debug("image_forward: chatID is empty after resolution")
		return false
	}

	slog.Debug("image_forward: evaluating request",
		"chat_id", chatID,
		"sender_id", senderID,
		"prompt", prompt,
		"has_images", hasImages,
	)

	// Check intent: explicit text intent or image-only message (img2img)
	shouldForward := false
	if isImageGenerationIntent(prompt, chatID, senderID) {
		shouldForward = true
		slog.Debug("image_forward: matched as image generation intent")
	} else if isImageMessage(prompt, hasImages) {
		shouldForward = true
		slog.Debug("image_forward: matched as image message (img2img)")
	}

	if !shouldForward {
		slog.Debug("image_forward: not an image request, skipping",
			"chat_id", chatID,
			"prompt", prompt,
		)
		return false
	}

	// Build payload
	msgType := "text"
	imageURL := ""
	if hasImages && len(imagePaths) > 0 {
		msgType = "image"
		imageURL = imagePaths[0] // primary image path
	}

	payload := imageForwardPayload{
		MessageID: messageID,
		ChatID:    chatID,
		Content:   prompt,
		SenderID:  senderID,
		MsgType:   msgType,
		ImageURL:  imageURL,
	}

	// Attempt forward — if service is down, fallback to Codex CLI
	if err := forwardToGenerateImage(payload); err != nil {
		slog.Warn("image_forward: forwarding failed, falling back to Codex CLI",
			"error", err,
			"chat_id", chatID,
			"content_len", len(prompt),
		)
		return false
	}

	// Mark session as active for follow-up commands
	globalImageSessions.markActive(chatID, senderID)
	return true
}
