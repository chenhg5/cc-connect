package teams

import (
	"encoding/json"
	"strings"
)

// activity is the subset of a Bot Framework Activity the connector consumes.
// See https://learn.microsoft.com/azure/bot-service/rest-api/bot-framework-rest-connector-api-reference.
type activity struct {
	Type         string              `json:"type"`
	ID           string              `json:"id"`
	Text         string              `json:"text"`
	ServiceURL   string              `json:"serviceUrl"`
	From         channelAccount      `json:"from"`
	Recipient    channelAccount      `json:"recipient"`
	Conversation conversationAccount `json:"conversation"`
	Entities     []entity            `json:"entities"`
	Attachments  []inboundAttachment `json:"attachments"`
	Value        json.RawMessage     `json:"value"`
}

// fileDownloadInfoContentType is the attachment contentType Teams uses for a file
// a user sends the bot in a 1:1 chat. Its `content` is a fileDownloadInfo.
const fileDownloadInfoContentType = "application/vnd.microsoft.teams.file.download.info"

// inboundAttachment is an attachment on an *inbound* activity. It is distinct
// from the outbound `attachment` card type (connector.go): inbound payloads are
// kept as raw JSON so typed contents like FileDownloadInfo can be decoded lazily.
type inboundAttachment struct {
	ContentType string          `json:"contentType"`
	ContentURL  string          `json:"contentUrl"`
	Content     json.RawMessage `json:"content"`
	Name        string          `json:"name"`
}

// fileDownloadInfo is the `content` of a FileDownloadInfo attachment. downloadUrl
// is a pre-authenticated link (no bearer token required); fileType is the file
// extension without a dot (e.g. "docx").
type fileDownloadInfo struct {
	DownloadURL string `json:"downloadUrl"`
	FileType    string `json:"fileType"`
	UniqueID    string `json:"uniqueId"`
}

// isFileDownload reports whether the attachment is a 1:1 FileDownloadInfo.
func (att inboundAttachment) isFileDownload() bool {
	return strings.EqualFold(att.ContentType, fileDownloadInfoContentType)
}

// downloadInfo decodes a FileDownloadInfo attachment's content. The bool is false
// when the attachment is not a file download or its content is unparseable.
func (att inboundAttachment) downloadInfo() (fileDownloadInfo, bool) {
	if !att.isFileDownload() || len(att.Content) == 0 {
		return fileDownloadInfo{}, false
	}
	var info fileDownloadInfo
	if err := json.Unmarshal(att.Content, &info); err != nil {
		return fileDownloadInfo{}, false
	}
	return info, true
}

// isImage reports whether the attachment is an inline image (contentType image/*).
func (att inboundAttachment) isImage() bool {
	return strings.HasPrefix(strings.ToLower(att.ContentType), "image/")
}

// isPersonal reports whether the activity is a 1:1 (personal) chat. Attachment
// handling is gated to this context: channel/group files require Microsoft Graph
// and are out of scope.
func (a *activity) isPersonal() bool {
	return strings.EqualFold(a.Conversation.ConversationType, "personal")
}

// hasProcessableAttachment reports whether the activity carries at least one
// attachment this connector actually handles (a file download or an inline
// image). Keying the dispatch gate on this — rather than a raw attachment count —
// avoids dispatching an empty-content turn for a message whose only attachment is
// an unhandled type (e.g. a link preview) with no text.
func (a *activity) hasProcessableAttachment() bool {
	for _, att := range a.Attachments {
		if att.isFileDownload() || att.isImage() {
			return true
		}
	}
	return false
}

type channelAccount struct {
	ID          string `json:"id"`
	AADObjectID string `json:"aadObjectId"`
	Name        string `json:"name"`
}

type conversationAccount struct {
	ID               string `json:"id"`
	ConversationType string `json:"conversationType"`
	Name             string `json:"name"`
}

// entity is a Bot Framework entity; only mention entities are interpreted.
type entity struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Mentioned channelAccount `json:"mentioned"`
}

func parseActivity(body []byte) (*activity, error) {
	var a activity
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// cleanText returns the message text with bot @mention markup removed. Teams
// includes the mention's display text inline (e.g. "<at>bot</at> hi"); each
// mention entity carries the exact Text span to strip.
func (a *activity) cleanText() string {
	text := a.Text
	for _, e := range a.Entities {
		if strings.EqualFold(e.Type, "mention") && e.Text != "" {
			text = strings.ReplaceAll(text, e.Text, "")
		}
	}
	return strings.TrimSpace(text)
}

// mentionsBot reports whether the activity @mentions this bot (by the bot's app
// ID, which equals the activity recipient ID).
func (a *activity) mentionsBot(botID string) bool {
	for _, e := range a.Entities {
		if strings.EqualFold(e.Type, "mention") &&
			(e.Mentioned.ID == botID || (a.Recipient.ID != "" && e.Mentioned.ID == a.Recipient.ID)) {
			return true
		}
	}
	return false
}

// hasMention reports whether the activity carries any @mention entity (targeting
// the bot or anyone else). Used by the engaged-thread follow filter to ignore
// messages addressed to other participants.
func (a *activity) hasMention() bool {
	for _, e := range a.Entities {
		if strings.EqualFold(e.Type, "mention") {
			return true
		}
	}
	return false
}

// cardAction returns the action string from a card submit (Action.Submit), or
// "" when the activity is not a card action. Teams delivers submits as a message
// activity carrying `value` and (usually) no text.
func (a *activity) cardAction() string {
	if len(a.Value) == 0 {
		return ""
	}
	var v map[string]any
	if err := json.Unmarshal(a.Value, &v); err != nil {
		return ""
	}
	for _, key := range []string{"action", "cmd"} {
		if s, ok := v[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// cardActionReply maps a card Action.Submit value to the message content the
// engine's interactive-prompt resolvers expect. Permission actions become the
// canonical keyword text — notably so "perm:allow_all" is not mis-read as a
// one-time allow by the engine's substring/token matcher. AskUserQuestion
// actions ("askq:q:o") and anything else pass through verbatim (the engine
// parses the askq: prefix directly).
func cardActionReply(action string) string {
	switch action {
	case "perm:allow":
		return "allow"
	case "perm:deny":
		return "deny"
	case "perm:allow_all":
		return "allow all"
	default:
		return action
	}
}
