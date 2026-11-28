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
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Thread attachment backfill.
//
// Feishu file and image messages cannot carry an @mention. With
// attachment_wait = "mention" (#2186), in-memory staging captures uploads
// inside an already-engaged thread while the process is running, but files
// uploaded before bot engagement or before a restart are not in process
// memory. When an explicit @bot message is accepted inside a thread-isolated
// session with attachment_wait = "mention", list the thread and forward the
// files and images the same user posted since their previous non-command
// @bot message (or /new), deduplicating any attachments already forwarded by
// the quote or in-memory staging paths.
//
// The window is derived from the thread itself, not from process memory, so
// it survives restarts and events that never reached this process. Built-in
// commands other than /new (e.g. "/ps ...") do not close the window: they
// never reach the agent. Only the trigger's own sender is considered, the
// same privacy rule as the quoted-file path (issue #1560).

const (
	threadBackfillMaxAttachments = 10
	threadBackfillMaxPages       = 4
	threadBackfillPageSize       = 50

	// A thread told about a denied listing is told again after this long, in
	// case the operator has granted the scope meanwhile. At most
	// threadBackfillDeniedMax threads are remembered.
	threadBackfillDeniedTTL = 24 * time.Hour
	threadBackfillDeniedMax = 1024
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

// threadWindowLimit tells why listThreadAttachments stopped before reaching
// the sender's previous @bot message.
type threadWindowLimit int

const (
	threadWindowComplete      threadWindowLimit = iota
	threadWindowAttachmentCap                   // more than threadBackfillMaxAttachments in the window
	threadWindowPageCap                         // threadBackfillMaxPages pages read, boundary not reached
)

// deniedThreads is a small bounded TTL set of threads already told that
// listing them is denied.
type deniedThreads struct {
	mu sync.Mutex
	at map[string]time.Time
}

// firstNotice reports whether threadID should be told now, and records it.
func (d *deniedThreads) firstNotice(threadID string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := d.at[threadID]; ok && now.Sub(at) < threadBackfillDeniedTTL {
		return false
	}
	if d.at == nil {
		d.at = map[string]time.Time{}
	}
	if len(d.at) >= threadBackfillDeniedMax {
		oldestID, oldest := "", now
		for id, at := range d.at {
			if now.Sub(at) >= threadBackfillDeniedTTL {
				delete(d.at, id)
				continue
			}
			if at.Before(oldest) {
				oldestID, oldest = id, at
			}
		}
		if len(d.at) >= threadBackfillDeniedMax && oldestID != "" {
			delete(d.at, oldestID)
		}
	}
	d.at[threadID] = now
	return true
}

// isEngineCommandText reports whether text, after removing mention
// placeholders, is a built-in command the engine consumes without a turn.
// Skills and custom or unknown slash commands still reach the agent.
func isEngineCommandText(text string) bool {
	return core.IsBuiltinCommand(strings.TrimSpace(mentionPlaceholder.ReplaceAllString(text, "")))
}

// isNewCommandText reports whether text invokes /new, which starts a fresh
// session and therefore closes the attachment backfill window (matching
// attachment_wait = "mention" clearing staged attachments on /new).
func isNewCommandText(text string) bool {
	fields := strings.Fields(mentionPlaceholder.ReplaceAllString(text, ""))
	return len(fields) > 0 && strings.EqualFold(fields[0], "/new")
}

// isSlashText reports whether text looks like a slash command the engine may
// resolve as a skill or custom command. A pasted path ("/etc/x.conf") does
// not: command names contain no further "/".
func isSlashText(text string) bool {
	fields := strings.Fields(mentionPlaceholder.ReplaceAllString(text, ""))
	return len(fields) > 0 && strings.HasPrefix(fields[0], "/") && !strings.Contains(fields[0][1:], "/")
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
// message, oldest first. When a cap stops the walk early, the newest
// attachments are kept (they are the ones closest to the instruction) and
// limit says which cap was hit.
func (p *Platform) listThreadAttachments(ctx context.Context, threadID, currentID, userID string, currentMs int64, skip map[string]bool) ([]threadAttachmentMeta, threadWindowLimit, error) {
	if p.client == nil {
		return nil, threadWindowComplete, nil
	}
	var found []threadAttachmentMeta
	limit := threadWindowComplete
	pageToken := ""
	page := 0
walk:
	for ; page < threadBackfillMaxPages; page++ {
		q := url.Values{}
		q.Set("container_id_type", "thread")
		q.Set("container_id", threadID)
		q.Set("sort_type", "ByCreateTimeDesc")
		q.Set("page_size", strconv.Itoa(threadBackfillPageSize))
		// Sender and mention ids are compared with the bot's and the
		// trigger's open_id; pin the id type rather than rely on the default.
		q.Set("user_id_type", "open_id")
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		apiResp, err := p.client.Get(ctx, "/open-apis/im/v1/messages?"+q.Encode(), nil, larkcore.AccessTokenTypeTenant)
		if err != nil {
			return nil, limit, fmt.Errorf("list thread messages: %w", err)
		}
		var resp threadListResponse
		if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil {
			return nil, limit, fmt.Errorf("list thread messages: parse response: %w", err)
		}
		if resp.Code != 0 {
			return nil, limit, &threadListError{code: resp.Code, msg: resp.Msg}
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
			if p.mentionsBot(item.Mentions) {
				text := threadItemText(item)
				if !isEngineCommandText(text) || isNewCommandText(text) {
					break walk
				}
			}
			if skip[item.MessageID] {
				continue
			}
			if meta, ok := threadItemAttachment(item); ok {
				if len(found) >= threadBackfillMaxAttachments {
					limit = threadWindowAttachmentCap
					break walk
				}
				found = append(found, meta)
			}
		}
		if !resp.Data.HasMore || resp.Data.PageToken == "" {
			break
		}
		pageToken = resp.Data.PageToken
	}
	if page == threadBackfillMaxPages {
		limit = threadWindowPageCap
	}
	for i, j := 0, len(found)-1; i < j; i, j = i+1, j-1 {
		found[i], found[j] = found[j], found[i]
	}
	return found, limit, nil
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
	metas, limit, err := p.listThreadAttachments(ctx, threadID, currentID, userID, currentMs, skip)
	if err != nil {
		var listErr *threadListError
		if errors.As(err, &listErr) && listErr.denied() {
			if !p.threadBackfillDenied.firstNotice(threadID, time.Now()) {
				slog.Debug(p.tag()+": thread attachment backfill denied", "thread_id", threadID, "error", err)
				return out
			}
			// The scope and membership details are for the operator: log them,
			// and keep the text that reaches the chat short.
			slog.Warn(p.tag()+": thread attachment backfill denied; earlier files in this thread are not forwarded",
				"thread_id", threadID, "message_id", currentID, "error", err,
				"required_scopes", threadBackfillScopeHint, "hint", "the bot must also be a member of the chat")
			out.notices = append(out.notices, fmt.Sprintf(
				"[Earlier files in this thread could not be read: the bot is not permitted to list this thread (Feishu code %d).]",
				listErr.code))
			return out
		}
		slog.Warn(p.tag()+": thread attachment backfill failed", "thread_id", threadID, "message_id", currentID, "error", err)
		out.notices = append(out.notices, fmt.Sprintf("[Earlier files in this thread could not be read: %v]", clipThreadNotice(err.Error(), 300)))
		return out
	}
	switch limit {
	case threadWindowAttachmentCap:
		slog.Warn(p.tag()+": thread attachment backfill truncated; older files in the window are not forwarded",
			"thread_id", threadID, "message_id", currentID, "max_attachments", threadBackfillMaxAttachments)
		out.notices = append(out.notices, fmt.Sprintf(
			"[Only the %d most recent files and images this user posted in the thread are attached; older ones are not.]",
			threadBackfillMaxAttachments))
	case threadWindowPageCap:
		slog.Warn(p.tag()+": thread attachment backfill truncated; the window extends past the listed messages",
			"thread_id", threadID, "message_id", currentID, "max_messages", threadBackfillMaxPages*threadBackfillPageSize)
		out.notices = append(out.notices, fmt.Sprintf(
			"[Only files and images among the latest %d messages of this thread are attached; older ones are not.]",
			threadBackfillMaxPages*threadBackfillPageSize))
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

// forwardedAttachmentIDs lists the messages already forwarded by the quote or
// in-memory attachment_wait staging paths, so the backfill does not attach
// them twice. The parent itself is not skipped unconditionally: in an engaged
// thread the quote path is not taken (quoted is empty), and a reply to the
// sender's own file must still get that file through the backfill. Approved
// quoted files and staged attachments are skipped by message ID, and the
// parent is skipped when the quote path forwarded its images.
func forwardedAttachmentIDs(parentID string, quoted quotedMessage, approved []quotedFileMeta, staged []stagedAttachment) map[string]bool {
	ids := map[string]bool{}
	if parentID != "" && len(quoted.images) > 0 {
		ids[parentID] = true
	}
	for _, m := range approved {
		ids[m.messageID] = true
	}
	for _, s := range staged {
		if s.messageID != "" {
			ids[s.messageID] = true
		}
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
