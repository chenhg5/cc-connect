package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Thread attachment backfill.
//
// Feishu file and image messages cannot carry an @mention. In a group where
// the mention filter is active they are therefore dropped before download,
// and a user who uploads a file into a thread and then asks "@bot analyse the
// file above" used to reach the agent without the file. When an explicit
// @bot message is accepted inside a thread-isolated session, list the thread
// and forward the files and images the same user posted since their previous
// @bot message.
//
// The window is derived from the thread itself, not from process memory, so
// it survives restarts and events that never reached this process. Built-in
// commands ("/ps ...") do not close the window: they never reach the agent.
// Only the trigger's own sender is considered, the same privacy rule as the
// quoted-file path (issue #1560).

const (
	threadBackfillMaxAttachments = 10
	threadBackfillMaxPages       = 4
	threadBackfillPageSize       = 50
)

// threadBackfillScopeHint names the scope Feishu requires to list a group
// thread ("need scope: im:message.group_msg"); the downloads themselves work
// with the ordinary message read scope.
const threadBackfillScopeHint = "im:message.group_msg (read all messages in groups)"

var mentionPlaceholder = regexp.MustCompile(`@_user_\d+|@_all`)

type threadListItem struct {
	MessageID  string `json:"message_id"`
	MsgType    string `json:"msg_type"`
	CreateTime string `json:"create_time"`
	Deleted    bool   `json:"deleted"`
	Sender     struct {
		ID         string `json:"id"`
		SenderType string `json:"sender_type"`
	} `json:"sender"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Mentions []*larkim.Mention `json:"mentions"`
}

type threadListResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		HasMore   bool             `json:"has_more"`
		PageToken string           `json:"page_token"`
		Items     []threadListItem `json:"items"`
	} `json:"data"`
}

type threadAttachmentMeta struct {
	messageID string
	msgType   string // "file" or "image"
	key       string
	fileName  string
}

type threadBackfill struct {
	files   []core.FileAttachment
	images  []core.ImageAttachment
	notices []string
}

// isEngineCommandText reports whether text, after removing mention
// placeholders, is a built-in command the engine consumes without a turn.
// Skills and custom or unknown slash commands still reach the agent.
func isEngineCommandText(text string) bool {
	return core.IsBuiltinCommand(mentionPlaceholder.ReplaceAllString(text, ""))
}

func isSlashText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(mentionPlaceholder.ReplaceAllString(text, "")), "/")
}

func (p *Platform) mentionsBot(mentions []*larkim.Mention) bool {
	botOpenID := p.getBotOpenID()
	for _, m := range mentions {
		if m == nil || m.Id == nil || *m.Id == "" {
			continue
		}
		if *m.Id == botOpenID || (p.appID != "" && *m.Id == p.appID) {
			return true
		}
	}
	return false
}

// threadItemText returns the plain text of a text or post message.
func threadItemText(item threadListItem) string {
	switch item.MsgType {
	case "text":
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(item.Body.Content), &body) == nil {
			return body.Text
		}
	case "post":
		return extractPostPlainText(item.Body.Content)
	}
	return ""
}

func threadItemAttachment(item threadListItem) (threadAttachmentMeta, bool) {
	var body struct {
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
		ImageKey string `json:"image_key"`
	}
	if json.Unmarshal([]byte(item.Body.Content), &body) != nil {
		return threadAttachmentMeta{}, false
	}
	switch item.MsgType {
	case "file":
		if body.FileKey == "" {
			return threadAttachmentMeta{}, false
		}
		return threadAttachmentMeta{messageID: item.MessageID, msgType: "file", key: body.FileKey, fileName: body.FileName}, true
	case "image":
		if body.ImageKey == "" {
			return threadAttachmentMeta{}, false
		}
		return threadAttachmentMeta{messageID: item.MessageID, msgType: "image", key: body.ImageKey}, true
	}
	return threadAttachmentMeta{}, false
}

// listThreadAttachments walks the thread newest-first and returns the file
// and image messages userID posted after their previous non-command @bot
// message, oldest first.
func (p *Platform) listThreadAttachments(ctx context.Context, threadID, currentID, userID string, currentMs int64, skip map[string]bool) ([]threadAttachmentMeta, error) {
	var found []threadAttachmentMeta
	pageToken := ""
walk:
	for page := 0; page < threadBackfillMaxPages; page++ {
		q := url.Values{}
		q.Set("container_id_type", "thread")
		q.Set("container_id", threadID)
		q.Set("sort_type", "ByCreateTimeDesc")
		q.Set("page_size", strconv.Itoa(threadBackfillPageSize))
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		apiResp, err := p.client.Get(ctx, "/open-apis/im/v1/messages?"+q.Encode(), nil, larkcore.AccessTokenTypeTenant)
		if err != nil {
			return nil, fmt.Errorf("list thread messages: %w", err)
		}
		var resp threadListResponse
		if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil {
			return nil, fmt.Errorf("list thread messages: parse response: %w", err)
		}
		if resp.Code != 0 {
			return nil, &threadListError{code: resp.Code, msg: resp.Msg}
		}
		for _, item := range resp.Data.Items {
			if item.MessageID == currentID || item.Deleted {
				continue
			}
			if ms, err := strconv.ParseInt(item.CreateTime, 10, 64); err == nil && currentMs > 0 && ms > currentMs {
				continue
			}
			if item.Sender.SenderType != "user" || item.Sender.ID != userID {
				continue
			}
			if p.mentionsBot(item.Mentions) && !isEngineCommandText(threadItemText(item)) {
				break walk
			}
			if skip[item.MessageID] {
				continue
			}
			if meta, ok := threadItemAttachment(item); ok {
				found = append(found, meta)
				if len(found) >= threadBackfillMaxAttachments {
					break walk
				}
			}
		}
		if !resp.Data.HasMore || resp.Data.PageToken == "" {
			break
		}
		pageToken = resp.Data.PageToken
	}
	for i, j := 0, len(found)-1; i < j; i, j = i+1, j-1 {
		found[i], found[j] = found[j], found[i]
	}
	return found, nil
}

type threadListError struct {
	code int
	msg  string
}

func (e *threadListError) Error() string {
	return fmt.Sprintf("list thread messages: code=%d msg=%s", e.code, e.msg)
}

// denied reports whether Feishu refused the call for lack of a scope or of
// chat membership, rather than a transient failure.
func (e *threadListError) denied() bool {
	switch e.code {
	case 99991672, 99991679, 230027, 230002, 230006:
		return true
	}
	return false
}

// backfillThreadAttachments downloads the thread attachments for an accepted
// trigger. rctx.backfillThreadID is set by onMessage only for explicit @bot
// messages in thread-isolated group sessions. text is the trigger's text;
// built-in commands skip the backfill. skip holds message IDs already forwarded
// through the quote path.
func (p *Platform) backfillThreadAttachments(ctx context.Context, rctx replyContext, currentID, userID, text string, currentMs int64, skip map[string]bool) threadBackfill {
	var out threadBackfill
	threadID := rctx.backfillThreadID
	if threadID == "" || userID == "" || isEngineCommandText(text) {
		return out
	}
	// The engine only resolves a slash message as a skill or custom command
	// when it carries no images, so hand images to such a message as files.
	imagesAsFiles := isSlashText(text)
	i18n := core.NewI18n(core.DetectLanguage(text))
	metas, err := p.listThreadAttachments(ctx, threadID, currentID, userID, currentMs, skip)
	if err != nil {
		var listErr *threadListError
		if errors.As(err, &listErr) && listErr.denied() {
			if _, told := p.threadBackfillDenied.LoadOrStore(threadID, struct{}{}); told {
				slog.Debug(p.tag()+": thread attachment backfill denied", "thread_id", threadID, "error", err)
				return out
			}
			slog.Warn(p.tag()+": thread attachment backfill denied; earlier files in this thread are not forwarded",
				"thread_id", threadID, "message_id", currentID, "error", err, "required_scopes", threadBackfillScopeHint)
			out.notices = append(out.notices, fmt.Sprintf(
				"[Earlier files in this thread could not be read: %v. The Feishu app needs %s, and the bot must be in the chat.]",
				err, threadBackfillScopeHint))
			return out
		}
		slog.Warn(p.tag()+": thread attachment backfill failed", "thread_id", threadID, "message_id", currentID, "error", err)
		out.notices = append(out.notices, fmt.Sprintf("[Earlier files in this thread could not be read: %v]", clipThreadNotice(err.Error(), 300)))
		return out
	}
	if len(metas) == 0 {
		slog.Debug(p.tag()+": no thread attachments to backfill", "thread_id", threadID, "message_id", currentID)
		return out
	}
	var forwarded []string
	for _, m := range metas {
		switch m.msgType {
		case "file":
			name := m.fileName
			if name == "" {
				name = "attachment"
			}
			data, err := p.downloadResource(m.messageID, m.key, "file")
			if err != nil {
				slog.Error(p.tag()+": download thread file failed",
					"thread_id", threadID, "message_id", m.messageID, "file_name", name, "error", err)
				out.notices = append(out.notices, i18n.Tf(core.MsgAttachmentUnavailable,
					fmt.Sprintf("%s (message %s, download failed: %s)", name, m.messageID, clipThreadNotice(err.Error(), 200))))
				continue
			}
			out.files = append(out.files, core.FileAttachment{FileName: name, Data: data, MimeType: http.DetectContentType(data)})
		case "image":
			data, mimeType, err := p.downloadImage(m.messageID, m.key)
			if err != nil {
				slog.Error(p.tag()+": download thread image failed",
					"thread_id", threadID, "message_id", m.messageID, "error", err)
				out.notices = append(out.notices, i18n.Tf(core.MsgAttachmentUnavailable,
					fmt.Sprintf("image (message %s, download failed: %s)", m.messageID, clipThreadNotice(err.Error(), 200))))
				continue
			}
			if imagesAsFiles {
				out.files = append(out.files, core.FileAttachment{FileName: threadImageFileName(m.messageID, mimeType), Data: data, MimeType: mimeType})
				break
			}
			out.images = append(out.images, core.ImageAttachment{MimeType: mimeType, Data: data})
		}
		forwarded = append(forwarded, m.messageID)
	}
	slog.Info(p.tag()+": backfilled thread attachments",
		"thread_id", threadID, "message_id", currentID,
		"files", len(out.files), "images", len(out.images), "failed", len(metas)-len(forwarded),
		"source_message_ids", strings.Join(forwarded, ","))
	return out
}

// quotedMessageIDs lists the messages the quote path already forwarded, so
// the backfill does not attach them twice.
func quotedMessageIDs(parentID string, metas []quotedFileMeta) map[string]bool {
	ids := map[string]bool{}
	if parentID != "" {
		ids[parentID] = true
	}
	for _, m := range metas {
		ids[m.messageID] = true
	}
	return ids
}

// clipThreadNotice keeps an error short enough for a notice in the prompt.
func clipThreadNotice(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func threadImageFileName(messageID, mimeType string) string {
	ext := ".png"
	switch mimeType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	return "image_" + messageID + ext
}
