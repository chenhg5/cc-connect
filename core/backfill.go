package core

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// maomao fork: channel backfill. The agent only ever sees the messages cc-connect
// hands it, so anything that never reached it (sent while cc-connect was down,
// silenced by the gate, dropped as empty, or said by a teammate) is invisible.
// Before each turn the engine re-reads the channel since the last message this
// session saw and prepends what the agent missed, in channel order.

// ChannelHistoryMessage is one message read back from a platform channel.
type ChannelHistoryMessage struct {
	ID     string
	Author string
	IsBot  bool
	IsSelf bool // sent by this bot
	// FromTurn: carries the agent's own text (maomao fork, AgentReplyMarker).
	// The agent has these in its session; any other post under this bot's
	// name (cron --exec output, relay answers, status lines) never went through it.
	FromTurn bool
	Content  string
	Time     time.Time
	// Attachments of the post (maomao fork): backfill downloads the images of the
	// posts it shows and hands them to the agent as images, files go to disk.
	Attachments []ChannelHistoryAttachment
}

// ChannelHistoryAttachment is one attachment of a ChannelHistoryMessage (maomao fork).
type ChannelHistoryAttachment struct {
	URL         string
	FileName    string
	ContentType string
	Width       int // > 0 with Height for images whose ContentType is empty
	Height      int
}

// isImage mirrors the Discord classifier for live messages (classifyAttachments).
func (a ChannelHistoryAttachment) isImage() bool {
	ct := strings.ToLower(a.ContentType)
	return strings.HasPrefix(ct, "image/") || ct == "" && a.Width > 0 && a.Height > 0
}

// AgentReplyMarker is implemented by platforms that track which posts carry
// the agent's own text (maomao fork). The engine marks the reply context of
// every send that delivers agent output; status lines, errors and command
// replies go out unmarked, so channel backfill still shows them to the agent.
type AgentReplyMarker interface {
	MarkAgentReply(replyCtx any) any
}

// FinalReplyMarker is implemented by platforms that treat a turn's final answer differently from
// its interim posts (maomao fork: a sub's final answer to lead-dispatched work pings the lead).
type FinalReplyMarker interface {
	MarkFinalReply(replyCtx any) any
}

// markFinalReply marks replyCtx as carrying the turn's final answer (and agent output).
func markFinalReply(p Platform, replyCtx any) any {
	replyCtx = markAgentReply(p, replyCtx)
	if m, ok := p.(FinalReplyMarker); ok {
		return m.MarkFinalReply(replyCtx)
	}
	return replyCtx
}

// markAgentReply marks replyCtx as carrying agent output when p supports it.
func markAgentReply(p Platform, replyCtx any) any {
	if m, ok := p.(AgentReplyMarker); ok {
		return m.MarkAgentReply(replyCtx)
	}
	return replyCtx
}

// ChannelHistoryReader is implemented by platforms that can read channel history.
// It returns up to limit messages posted before beforeID, oldest first.
type ChannelHistoryReader interface {
	ChannelHistory(channelID, beforeID string, limit int) ([]ChannelHistoryMessage, error)
}

const (
	backfillFetchLimit   = 50
	backfillFirstWindow  = 3 * time.Hour // no last-seen mark yet: how far back to look
	backfillMaxMsgRunes  = 2000          // a Discord post at most: a cron digest must not lose its second half
	backfillMaxTotalRune = 8000
)

// snowflakeAfter reports whether message ID a was posted after b (numeric IDs as strings).
func snowflakeAfter(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// channelBackfill returns the formatted messages the session has not seen yet
// (between its last-seen mark and msg) with the images they carried, and ok=false when the
// platform cannot read history or the read failed — callers then fall back to the gate buffer.
// It records on msg where the backfill started (see Message.BackfillFrom).
func (e *Engine) channelBackfill(p Platform, msg *Message, session *Session) (string, []ImageAttachment, bool) {
	if msg.MessageID == "" {
		return "", nil, false
	}
	from := session.GetLastSeenMsgID()
	back, images, _, ok := e.channelBackfillBefore(p, msg.ChannelID, msg.MessageID, session)
	if ok {
		msg.BackfillFrom, msg.BackfillRan = from, true
	}
	return back, images, ok
}

// cronChannelBackfill is channelBackfill for a scheduled turn, which has no triggering message:
// it reads up to the newest post in the channel and marks it seen. Without it a cron turn never
// sees what was just said in its channel (2026-10-15: the bots coming home at 19:00 could not
// hear each other, so nobody could answer what the one before had run into).
// maomao fork: it now edits msg itself, so the images of those posts go with the turn
// (2026-10-17: "他每次都要看到之前的消息").
func (e *Engine) cronChannelBackfill(p Platform, sessionKey string, session *Session, msg *Message) {
	from := session.GetLastSeenMsgID()
	back, images, newest, ok := e.channelBackfillBefore(p, extractChannelID(sessionKey), "", session)
	if !ok {
		return
	}
	msg.BackfillFrom, msg.BackfillRan = from, true
	if newest != "" {
		markSeen(session, &Message{MessageID: newest})
	}
	if back != "" {
		msg.Content = back + "\n\n（定时任务，回这条）\n" + msg.Content
	}
	msg.Images = append(images, msg.Images...)
}

// channelBackfillBefore formats the messages before beforeID ("" = up to the newest) that the
// session has not seen, with the images of the messages it shows; newest is the ID of the
// newest message read.
func (e *Engine) channelBackfillBefore(p Platform, channelID, beforeID string, session *Session) (back string, images []ImageAttachment, newest string, ok bool) {
	r, ok := p.(ChannelHistoryReader)
	if !ok || channelID == "" {
		return "", nil, "", false
	}
	msgs, err := r.ChannelHistory(channelID, beforeID, backfillFetchLimit)
	if err != nil {
		slog.Warn("backfill: read channel history failed", "channel", channelID, "error", err)
		return "", nil, "", false
	}
	if len(msgs) > 0 {
		newest = msgs[len(msgs)-1].ID
	}
	lastSeen := session.GetLastSeenMsgID()
	var lines []string
	var shown []ChannelHistoryMessage // the message behind each line
	truncated := len(msgs) == backfillFetchLimit
	for _, m := range msgs {
		if lastSeen != "" {
			if !snowflakeAfter(m.ID, lastSeen) {
				truncated = false
				continue
			}
		} else if time.Since(m.Time) > backfillFirstWindow {
			truncated = false
			continue
		}
		if m.FromTurn {
			// Only what the agent's own turns posted. Skipping every post under
			// this bot's name hid the 10:30 trending digest (a --exec cron job),
			// and "rsa" was then misread (2026-10-13).
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" && len(m.Attachments) == 0 {
			text = "（空消息或只有附件）"
		}
		if r := []rune(text); len(r) > backfillMaxMsgRunes {
			text = string(r[:backfillMaxMsgRunes]) + "…"
		}
		text += e.mediaNoteFor(m.ID) // maomao fork: a parsed video link carries its result path
		lines = append(lines, historyPrefix(m)+text)
		shown = append(shown, m)
	}
	if len(lines) == 0 {
		return "", nil, newest, true
	}
	// Keep the newest messages when over budget.
	total := 0
	start := len(lines)
	for start > 0 {
		n := len([]rune(lines[start-1]))
		if total+n > backfillMaxTotalRune {
			truncated = true
			break
		}
		total += n
		start--
	}
	// maomao fork: only the lines that are shown bring their attachments.
	for i := start; i < len(lines); i++ {
		note, imgs := e.backfillAttachments(shown[i])
		lines[i] += note
		images = append(images, imgs...)
	}
	head := "（频道里你上次看到之后的消息，按时间顺序；这是真实记录，先读它再回答）"
	if truncated {
		head = "（频道里你上次看到之后的消息，按时间顺序；更早的还有，没列出来，需要时去翻）"
	}
	return head + "\n" + strings.Join(lines[start:], "\n"), images, newest, true
}

// historyPrefix is the "[15:04] who: " head of a backfilled message.
func historyPrefix(m ChannelHistoryMessage) string {
	who := m.Author
	switch {
	case m.IsSelf:
		who += "（你这个账号，但不是你在会话里写的，如定时任务输出）"
	case m.IsBot:
		who += "（bot）"
	}
	return fmt.Sprintf("[%s] %s: ", m.Time.Local().Format("15:04"), who)
}

// backfillDownload fetches one attachment of a backfilled message (maomao fork). A variable so
// tests can count calls; it uses the shared HTTPClient and the per-attachment size limit of /send.
var backfillDownload = func(u string) ([]byte, error) {
	resp, err := HTTPClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, DefaultMaxAttachmentSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > DefaultMaxAttachmentSize {
		return nil, fmt.Errorf("larger than %d MB", DefaultMaxAttachmentSize>>20)
	}
	return data, nil
}

// backfillAttachments downloads the attachments of a backfilled message (maomao fork,
// 2026-10-17: a bot that was not called never saw the photos posted before it was @-ed).
// Images are returned to go with the turn; other files are saved where the agent saves
// live attachments (SaveFilesToDisk) and named by path. Failures are written into the note
// and never stop the turn.
func (e *Engine) backfillAttachments(m ChannelHistoryMessage) (string, []ImageAttachment) {
	var images []ImageAttachment
	var notes []string
	for _, a := range m.Attachments {
		data, err := backfillDownload(a.URL)
		if a.isImage() {
			if err != nil {
				slog.Warn("backfill: download image failed", "msg", m.ID, "file", a.FileName, "error", err)
				notes = append(notes, fmt.Sprintf("附图 %s 下载失败（%v），需要时让人重发", a.FileName, err))
				continue
			}
			images = append(images, ImageAttachment{MimeType: a.ContentType, Data: data, FileName: a.FileName})
			continue
		}
		if err != nil {
			slog.Warn("backfill: download file failed", "msg", m.ID, "file", a.FileName, "error", err)
			notes = append(notes, fmt.Sprintf("附件 %s 下载失败（%v）", a.FileName, err))
			continue
		}
		paths := SaveFilesToDisk(e.attachmentWorkDir(), m.ID, []FileAttachment{{MimeType: a.ContentType, Data: data, FileName: a.FileName}})
		if len(paths) == 0 {
			notes = append(notes, fmt.Sprintf("附件 %s 保存失败", a.FileName))
			continue
		}
		notes = append(notes, fmt.Sprintf("附件 %s 已存到 %s", a.FileName, paths[0]))
	}
	if len(images) > 0 {
		notes = append([]string{fmt.Sprintf("附图 %d 张（已随本条附上）", len(images))}, notes...)
	}
	if len(notes) == 0 {
		return "", nil
	}
	return "（" + strings.Join(notes, "；") + "）", images
}

// attachmentWorkDir is where the agent's live attachments go (its work_dir), so backfilled
// files land next to them under .cc-connect/attachments/.
func (e *Engine) attachmentWorkDir() string {
	if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok && wd.GetWorkDir() != "" {
		return wd.GetWorkDir()
	}
	dir, _ := os.Getwd()
	return dir
}

// markSeen records msg as the newest message this session has seen.
func markSeen(session *Session, msg *Message) {
	id := msg.MessageID
	if msg.SeenUpToID != "" && snowflakeAfter(msg.SeenUpToID, id) {
		id = msg.SeenUpToID
	}
	if id == "" {
		return
	}
	if cur := session.GetLastSeenMsgID(); cur == "" || snowflakeAfter(id, cur) {
		session.SetLastSeenMsgID(id)
	}
}

// DefaultImageFreshMB is the default of [projects.image_fresh] max_mb (maomao fork; moved here
// from cmd/cc-connect so core can use it too). It is an estimate, not a measured knee. Source
// (jingyu on the relay, 2026-10-16/17 rollouts): with 8.3 MB of images in the context each step
// took 90-130 s; with 3.4 MB, 8-20 s; with none, 16-25 s.
const DefaultImageFreshMB = 3.0

// imageContextBudget is the base64 size of images a context may hold before image_fresh
// starts a new one: the configured limit, or its default when image_fresh is off.
func (e *Engine) imageContextBudget() int {
	if e.imageFreshMaxBytes > 0 {
		return e.imageFreshMaxBytes
	}
	return int(DefaultImageFreshMB * 1e6)
}

// base64Len is the size of n bytes as base64, the unit image_fresh counts in.
func base64Len(n int) int { return (n + 2) / 3 * 4 }

// freshContextImages gives the first turn of a fresh agent context the recent channel images
// the old context held (maomao fork). A new context (image_fresh or cold-cache renewal, failed
// resume) starts with the handoff text only, and "since last seen" may be empty, so the bot
// forgot the photos it had just looked at (2026-10-17, user: "他每次都要看到之前的消息的呀，
// 就是可能这长中短，近期的一个消息"). It re-reads the posts before the ones this turn's backfill
// already covered, within backfillFirstWindow, and attaches their images newest first while
// the context stays under imageContextBudget (minus the images this turn already carries), so
// the images alone never trigger the next renewal. Text is not repeated: the handoff has it.
// Runs only for turns whose channel backfill ran, so it knows where that backfill started.
func (e *Engine) freshContextImages(p Platform, session *Session, msg *Message, already []ImageAttachment) (string, []ImageAttachment) {
	if !session.TakeFreshContext() || !msg.BackfillRan || msg.BackfillFrom == "" {
		return "", nil // no backfill, or it already covered the whole window
	}
	r, ok := p.(ChannelHistoryReader)
	channelID := msg.ChannelID
	if channelID == "" {
		channelID = extractChannelID(msg.SessionKey)
	}
	if !ok || channelID == "" {
		return "", nil
	}
	msgs, err := r.ChannelHistory(channelID, msg.MessageID, backfillFetchLimit)
	if err != nil {
		slog.Warn("fresh context: read channel history failed", "channel", channelID, "error", err)
		return "", nil
	}
	budget := e.imageContextBudget()
	for _, img := range already {
		budget -= base64Len(len(img.Data))
	}
	type picked struct {
		m    ChannelHistoryMessage
		imgs []ImageAttachment
	}
	var got []picked // newest first
	used, failed, full := 0, 0, false
	for i := len(msgs) - 1; i >= 0 && !full; i-- {
		m := msgs[i]
		if time.Since(m.Time) > backfillFirstWindow {
			break
		}
		if snowflakeAfter(m.ID, msg.BackfillFrom) {
			continue // this turn's backfill already brought it
		}
		var imgs []ImageAttachment
		for j := len(m.Attachments) - 1; j >= 0; j-- {
			a := m.Attachments[j]
			if !a.isImage() {
				continue
			}
			data, err := backfillDownload(a.URL)
			if err != nil {
				slog.Warn("fresh context: download image failed", "msg", m.ID, "file", a.FileName, "error", err)
				failed++
				continue
			}
			if used+base64Len(len(data)) >= budget {
				full = true
				break
			}
			used += base64Len(len(data))
			imgs = append([]ImageAttachment{{MimeType: a.ContentType, Data: data, FileName: a.FileName}}, imgs...)
		}
		if len(imgs) > 0 {
			got = append(got, picked{m, imgs})
		}
	}
	if len(got) == 0 && failed == 0 {
		return "", nil
	}
	var images []ImageAttachment
	var where []string
	for i := len(got) - 1; i >= 0; i-- {
		images = append(images, got[i].imgs...)
		where = append(where, fmt.Sprintf("%s%d 张", historyPrefix(got[i].m), len(got[i].imgs)))
	}
	note := fmt.Sprintf("（新会话：旧会话里看过的频道图片补回 %d 张，已随本条附上，按时间顺序：%s", len(images), strings.Join(where, "；"))
	if full {
		note += "；更早的图没附，太多会拖慢每一步"
	}
	if failed > 0 {
		note += fmt.Sprintf("；%d 张下载失败", failed)
	}
	slog.Info("fresh context: re-attached recent channel images", "session", msg.SessionKey, "images", len(images), "base64_bytes", used, "failed", failed)
	return note + "）", images
}
