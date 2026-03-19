package fake

import (
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestMessage creates a basic test message with sensible defaults.
func TestMessage() *core.Message {
	return &core.Message{
		SessionKey: "test:channel:user",
		Platform:   "test",
		MessageID:  "msg-test-001",
		UserID:     "user-test",
		UserName:   "Test User",
		ChatName:   "Test Channel",
		Content:    "Hello, world",
		ReplyCtx:   nil,
	}
}

// TestMessageWithContent creates a message with specific content.
func TestMessageWithContent(content string) *core.Message {
	msg := TestMessage()
	msg.Content = content
	return msg
}

// TestMessageWithSession creates a message with a specific session key.
func TestMessageWithSession(sessionKey string) *core.Message {
	msg := TestMessage()
	msg.SessionKey = sessionKey
	return msg
}

// TestMessageWithImages creates a message with images.
func TestMessageWithImages(images []core.ImageAttachment) *core.Message {
	msg := TestMessage()
	msg.Images = images
	return msg
}

// TestMessageWithFiles creates a message with files.
func TestMessageWithFiles(files []core.FileAttachment) *core.Message {
	msg := TestMessage()
	msg.Files = files
	return msg
}

// TestMessageWithAudio creates a message with audio.
func TestMessageWithAudio(audio *core.AudioAttachment) *core.Message {
	msg := TestMessage()
	msg.Audio = audio
	return msg
}

// TestMessageFromVoice creates a message that originated from voice.
func TestMessageFromVoice(content string) *core.Message {
	msg := TestMessageWithContent(content)
	msg.FromVoice = true
	return msg
}

// TestLongMessage creates a message with a very long content for truncation testing.
func TestLongMessage(length int) *core.Message {
	return &core.Message{
		SessionKey: "test:channel:user",
		Platform:   "test",
		MessageID:  "msg-test-long",
		UserID:    "user-test",
		Content:    strings.Repeat("x", length),
	}
}

// TestSpecialCharsMessage creates a message with special characters for security testing.
func TestSpecialCharsMessage() *core.Message {
	return &core.Message{
		SessionKey: "test:channel:user",
		Platform:   "test",
		MessageID:  "msg-test-special",
		UserID:    "user-test",
		Content:    "Hello! 🎉 <script>alert('xss')</script> & \"quotes\"",
	}
}

// TestImageAttachment creates a test image attachment.
func TestImageAttachment(mimeType, filename string, data []byte) core.ImageAttachment {
	return core.ImageAttachment{
		MimeType: mimeType,
		Data:     data,
		FileName: filename,
	}
}

// TestFileAttachment creates a test file attachment.
func TestFileAttachment(mimeType, filename string, data []byte) core.FileAttachment {
	return core.FileAttachment{
		MimeType: mimeType,
		Data:     data,
		FileName: filename,
	}
}

// TestAudioAttachment creates a test audio attachment.
func TestAudioAttachment(mimeType, format string, data []byte, duration int) *core.AudioAttachment {
	return &core.AudioAttachment{
		MimeType: mimeType,
		Data:     data,
		Format:   format,
		Duration: duration,
	}
}

// TestEvent creates a test event.
func TestEvent(eventType core.EventType, content string) core.Event {
	return core.Event{
		Type:    eventType,
		Content: content,
		Done:    eventType == core.EventResult || eventType == core.EventError,
	}
}

// TestTextEvent creates a text event.
func TestTextEvent(content string) core.Event {
	return TestEvent(core.EventText, content)
}

// TestResultEvent creates a result event.
func TestResultEvent(content string) core.Event {
	return TestEvent(core.EventResult, content)
}

// TestErrorEvent creates an error event.
func TestErrorEvent(err error) core.Event {
	return core.Event{
		Type:  core.EventError,
		Done:  true,
		Error: err,
	}
}

// TestPermissionRequestEvent creates a permission request event.
func TestPermissionRequestEvent(requestID, toolName, toolInput string) core.Event {
	return core.Event{
		Type:      core.EventPermissionRequest,
		ToolName:  toolName,
		ToolInput: toolInput,
		RequestID: requestID,
	}
}

// TestToolUseEvent creates a tool use event.
func TestToolUseEvent(toolName, toolInput string) core.Event {
	return core.Event{
		Type:      core.EventToolUse,
		ToolName:  toolName,
		ToolInput: toolInput,
	}
}

// TestThinkingEvent creates a thinking event.
func TestThinkingEvent(content string) core.Event {
	return TestEvent(core.EventThinking, content)
}

// TestHistoryEntry creates a test history entry.
func TestHistoryEntry(role, content string) core.HistoryEntry {
	return core.HistoryEntry{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}
}

// TestAgentSessionInfo creates a test agent session info.
func TestAgentSessionInfo(id, summary string, messageCount int) core.AgentSessionInfo {
	return core.AgentSessionInfo{
		ID:           id,
		Summary:      summary,
		MessageCount: messageCount,
		ModifiedAt:   time.Now(),
	}
}

// TestPermissionResult creates a test permission result.
func TestPermissionResultAllow() core.PermissionResult {
	return core.PermissionResult{
		Behavior: "allow",
	}
}

func TestPermissionResultDeny(message string) core.PermissionResult {
	return core.PermissionResult{
		Behavior: "deny",
		Message:  message,
	}
}

// TestProviderConfig creates a test provider config.
func TestProviderConfig(name, apiKey, baseURL, model string) core.ProviderConfig {
	return core.ProviderConfig{
		Name:    name,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}
}

// TestModelOption creates a test model option.
func TestModelOption(name, desc, alias string) core.ModelOption {
	return core.ModelOption{
		Name:  name,
		Desc:  desc,
		Alias: alias,
	}
}

// BatchTestMessages creates multiple test messages for batch testing.
func BatchTestMessages(count int) []*core.Message {
	messages := make([]*core.Message, count)
	for i := 0; i < count; i++ {
		messages[i] = TestMessageWithContent(fmt.Sprintf("Test message %d", i))
	}
	return messages
}
