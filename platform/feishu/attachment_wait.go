package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// attachment_wait (issue #2186) changes what happens to attachment-only
// messages that #1099 admits into an active thread without @bot.
//
//	"off"     — default; dispatch them immediately, unchanged #1099 behaviour.
//	"mention" — stage them per (thread, sender) and hand them over with the
//	            same sender's next @bot message in that thread. Staged items
//	            expire after attachment_wait_secs and are then dropped.
//
// "next" (release on the sender's next message of any kind) is the shape
// proposed in #2135 and is reserved for it; it is rejected at startup until
// implemented so a config written for it does not silently behave as "off".
const (
	attachmentWaitOff     = "off"
	attachmentWaitMention = "mention"

	defaultAttachmentWaitSecs = 1800
	maxAttachmentWaitSecs     = 86400

	// maxStagedAttachments bounds one (thread, sender) stage. Only metadata is
	// kept until release, so this guards against unbounded growth in a topic
	// where someone uploads many files and never mentions the bot.
	maxStagedAttachments = 20
)

func parseAttachmentWaitOptions(name string, opts map[string]any) (string, time.Duration, error) {
	mode := attachmentWaitOff
	if raw, ok := opts["attachment_wait"]; ok {
		s, isString := raw.(string)
		if !isString {
			return "", 0, fmt.Errorf("%s: attachment_wait must be a string, got %T", name, raw)
		}
		switch v := strings.ToLower(strings.TrimSpace(s)); v {
		case "", attachmentWaitOff:
			mode = attachmentWaitOff
		case attachmentWaitMention:
			mode = attachmentWaitMention
		case "next":
			return "", 0, fmt.Errorf(`%s: attachment_wait = "next" is not implemented yet (see #2135); use "off" or "mention"`, name)
		default:
			return "", 0, fmt.Errorf(`%s: attachment_wait must be "off" or "mention", got %q`, name, s)
		}
	}
	secs := int64(defaultAttachmentWaitSecs)
	if raw, ok := opts["attachment_wait_secs"]; ok {
		v, err := coerceMilliseconds(raw) // generic numeric coercion; the unit here is seconds
		if err != nil || v <= 0 || v > maxAttachmentWaitSecs {
			return "", 0, fmt.Errorf("%s: attachment_wait_secs must be an integer from 1 to %d", name, maxAttachmentWaitSecs)
		}
		secs = v
	}
	return mode, time.Duration(secs) * time.Second, nil
}

// stagedAttachment is the metadata of one unmentioned attachment message.
// The bytes are fetched only on release, so expired items cost no download.
type stagedAttachment struct {
	messageID string
	msgType   string
	content   string
}

type attachmentStage struct {
	items []stagedAttachment
	timer *time.Timer
}

func (p *Platform) attachmentWaitMentionEnabled() bool {
	return p.attachmentWait == attachmentWaitMention
}

func (p *Platform) attachmentWaitWindow() time.Duration {
	if p.attachmentWaitTTL > 0 {
		return p.attachmentWaitTTL
	}
	return defaultAttachmentWaitSecs * time.Second
}

// canStageAttachment reports whether an attachment type is staged in
// "mention" mode. audio is excluded: a voice message is itself the
// instruction, and core.Message carries a single Audio field, so it keeps the
// current #1099 dispatch.
func canStageAttachment(msgType string) bool {
	switch msgType {
	case "image", "file", "media":
		return true
	}
	return false
}

func attachmentStageKey(sessionKey, userID string) string {
	return sessionKey + "\x00" + userID
}

// stageAttachment records an unmentioned attachment for later release. It
// runs synchronously in onMessage so a following @bot message from the same
// sender is guaranteed to see it.
func (p *Platform) stageAttachment(sessionKey, userID string, item stagedAttachment) {
	key := attachmentStageKey(sessionKey, userID)
	p.attachmentStageMu.Lock()
	defer p.attachmentStageMu.Unlock()
	if p.attachmentStages == nil {
		p.attachmentStages = make(map[string]*attachmentStage)
	}
	stage := p.attachmentStages[key]
	if stage == nil {
		stage = &attachmentStage{}
		p.attachmentStages[key] = stage
	}
	if stage.timer != nil {
		stage.timer.Stop()
	}
	stage.items = append(stage.items, item)
	if n := len(stage.items); n > maxStagedAttachments {
		slog.Warn(p.tag()+": staged attachment limit reached, dropping oldest",
			"session_key", sessionKey, "dropped_message_id", stage.items[0].messageID)
		stage.items = append([]stagedAttachment(nil), stage.items[n-maxStagedAttachments:]...)
	}
	ref := stage
	stage.timer = time.AfterFunc(p.attachmentWaitWindow(), func() {
		p.expireAttachmentStage(key, ref)
	})
	slog.Info(p.tag()+": staged attachment until next @bot message",
		"session_key", sessionKey, "message_id", item.messageID, "msg_type", item.msgType,
		"staged", len(stage.items), "wait", p.attachmentWaitWindow())
}

// expireAttachmentStage drops the stage iff it has not been superseded,
// taken or cleared since the timer was armed.
func (p *Platform) expireAttachmentStage(key string, ref *attachmentStage) {
	p.attachmentStageMu.Lock()
	current, ok := p.attachmentStages[key]
	if !ok || current != ref {
		p.attachmentStageMu.Unlock()
		return
	}
	delete(p.attachmentStages, key)
	p.attachmentStageMu.Unlock()
	slog.Info(p.tag()+": staged attachments expired without @bot message", "count", len(ref.items))
}

// takeStagedAttachments removes and returns the sender's staged attachments
// for this thread. Called synchronously in onMessage for the releasing @bot
// message.
func (p *Platform) takeStagedAttachments(sessionKey, userID string) []stagedAttachment {
	key := attachmentStageKey(sessionKey, userID)
	p.attachmentStageMu.Lock()
	defer p.attachmentStageMu.Unlock()
	stage := p.attachmentStages[key]
	if stage == nil {
		return nil
	}
	delete(p.attachmentStages, key)
	if stage.timer != nil {
		stage.timer.Stop()
	}
	return stage.items
}

func (p *Platform) clearStagedAttachments(sessionKey, userID string) {
	if items := p.takeStagedAttachments(sessionKey, userID); len(items) > 0 {
		slog.Info(p.tag()+": staged attachments cleared by /new", "session_key", sessionKey, "count", len(items))
	}
}

// dropAllStagedAttachments is called from Stop(). Staged items are dropped
// rather than dispatched: they never had an instruction, and dispatching them
// alone is exactly the behaviour "mention" mode exists to avoid.
func (p *Platform) dropAllStagedAttachments() {
	p.attachmentStageMu.Lock()
	pending := p.attachmentStages
	p.attachmentStages = nil
	p.attachmentStageMu.Unlock()
	for _, stage := range pending {
		if stage.timer != nil {
			stage.timer.Stop()
		}
	}
}

// mentionedCommand returns the lower-cased slash command of an @bot text
// message, or "" when the message is not a command.
func (p *Platform) mentionedCommand(msgType, content string, mentions []*larkim.MentionEvent) string {
	if msgType != "text" {
		return ""
	}
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return ""
	}
	fields := strings.Fields(stripMentions(body.Text, mentions, p.getBotOpenID()))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	return strings.ToLower(fields[0])
}

// downloadStagedAttachments fetches the bytes of released attachments.
// Recalled items are skipped. A failed download is reported as a note so the
// agent knows something the user referred to is missing.
func (p *Platform) downloadStagedAttachments(_ context.Context, items []stagedAttachment) ([]core.ImageAttachment, []core.FileAttachment, []string) {
	var images []core.ImageAttachment
	var files []core.FileAttachment
	var notes []string
	for _, item := range items {
		if p.isMessageRecalled(item.messageID) {
			slog.Debug(p.tag()+": recalled staged attachment skipped", "message_id", item.messageID)
			continue
		}
		var body struct {
			ImageKey string `json:"image_key"`
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		if err := json.Unmarshal([]byte(item.content), &body); err != nil {
			slog.Error(p.tag()+": failed to parse staged attachment", "message_id", item.messageID, "error", err)
			continue
		}
		switch item.msgType {
		case "image":
			data, mimeType, err := p.downloadImage(item.messageID, body.ImageKey)
			if err != nil {
				slog.Error(p.tag()+": download staged image failed", "message_id", item.messageID, "error", err)
				notes = append(notes, "[an earlier image could not be downloaded]")
				continue
			}
			images = append(images, core.ImageAttachment{MimeType: mimeType, Data: data})
		case "file", "media":
			name := body.FileName
			if name == "" && item.msgType == "media" {
				name = "video.mp4"
			}
			data, err := p.downloadResource(item.messageID, body.FileKey, "file")
			if err != nil {
				slog.Error(p.tag()+": download staged file failed", "message_id", item.messageID, "error", err)
				notes = append(notes, fmt.Sprintf("[earlier file %q could not be downloaded]", name))
				continue
			}
			mimeType := http.DetectContentType(data)
			if item.msgType == "media" {
				mimeType = "video/mp4"
			}
			files = append(files, core.FileAttachment{MimeType: mimeType, Data: data, FileName: name})
		}
	}
	return images, files, notes
}
