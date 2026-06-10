package webex

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("webex", New)
}

// replyContext carries what Reply/Send need to target a Webex room.
type replyContext struct {
	roomID    string
	messageID string
	personID  string
}

// Platform is the Webex adapter implementing core.Platform.
type Platform struct {
	token     string
	allowFrom []string // lowercased email allowlist; empty = allow all

	client webexClient

	mu               sync.RWMutex
	handler          core.MessageHandler
	lifecycleHandler core.PlatformLifecycleHandler
	cancel           context.CancelFunc
	stopping         bool
	selfID           string // bot's own personId
	deviceURL        string // for cleanup on Stop()
}

// New constructs a Webex platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("webex: token is required")
	}
	rawAllow, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("webex", rawAllow)

	return &Platform{
		token:     token,
		allowFrom: parseAllowFrom(rawAllow),
		client:    newHTTPClient(token),
	}, nil
}

func (p *Platform) Name() string { return "webex" }

// parseAllowFrom splits and lowercases a comma-separated email list.
func parseAllowFrom(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, e := range strings.Split(raw, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, strings.ToLower(e))
		}
	}
	return out
}

// isAllowed reports whether an email may use the bot.
// Empty allowlist permits everyone (a startup warning was already logged).
func (p *Platform) isAllowed(email string) bool {
	if len(p.allowFrom) == 0 {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for _, a := range p.allowFrom {
		if a == email {
			return true
		}
	}
	return false
}

var sparkMentionRe = regexp.MustCompile(`(?s)<spark-mention[^>]*>.*?</spark-mention>`)

// stripMention removes Webex <spark-mention> tags and trims the result.
func stripMention(text string) string {
	return strings.TrimSpace(sparkMentionRe.ReplaceAllString(text, ""))
}

// isMentioned reports whether the bot's selfID appears in mentionedPeople.
func (p *Platform) isMentioned(m *message) bool {
	for _, id := range m.MentionedPeople {
		if id == p.selfID {
			return true
		}
	}
	return false
}

// shouldProcess applies the gate: allowlist + group-mention requirement.
func (p *Platform) shouldProcess(m *message) bool {
	if !p.isAllowed(m.PersonEmail) {
		return false
	}
	if m.RoomType == "group" && !p.isMentioned(m) {
		return false
	}
	return true
}

// buildMessage converts a fetched Webex message into a core.Message,
// downloading any attachments and stripping group @mentions.
func (p *Platform) buildMessage(ctx context.Context, m *message) *core.Message {
	content := m.Text
	if m.RoomType == "group" {
		content = stripMention(content)
	}

	cm := &core.Message{
		SessionKey: fmt.Sprintf("webex:%s:%s", m.RoomID, m.PersonID),
		Platform:   "webex",
		MessageID:  m.ID,
		ChannelID:  m.RoomID,
		ChannelKey: m.RoomID,
		UserID:     m.PersonEmail,
		UserName:   m.PersonEmail,
		Content:    content,
		ReplyCtx:   replyContext{roomID: m.RoomID, messageID: m.ID, personID: m.PersonID},
	}

	for _, url := range m.Files {
		f, err := p.client.DownloadFile(ctx, url)
		if err != nil {
			slog.Error("webex: download file failed", "error", err)
			continue
		}
		if strings.HasPrefix(f.MimeType, "image/") {
			cm.Images = append(cm.Images, core.ImageAttachment{
				MimeType: f.MimeType, Data: f.Data, FileName: f.FileName,
			})
		} else {
			cm.Files = append(cm.Files, core.FileAttachment{
				MimeType: f.MimeType, Data: f.Data, FileName: f.FileName,
			})
		}
	}
	return cm
}

func (p *Platform) messageHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *Platform) isStopping() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopping
}

// NOTE: The following methods are temporary placeholders so *Platform satisfies
// core.Platform. They are fully implemented in later tasks (Start/Stop in the
// WebSocket task, Reply/Send in the senders task) and will be replaced there.

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	p.handler = handler
	p.mu.Unlock()
	return nil
}

func (p *Platform) Stop() error { return nil }
