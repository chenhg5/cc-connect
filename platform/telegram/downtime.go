package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// pendingUpdateSource is implemented by the production polling bot. Keeping
// this separate from telegramBot lets send-only bots remain useful in tests.
type pendingUpdateSource interface {
	pendingUpdates(context.Context, int64) ([]*models.Update, error)
	setPollingOffset(int64)
}

type startupBot struct {
	telegramBot
	token     string
	client    *http.Client
	setOffset func(int64)
}

func (b *startupBot) setPollingOffset(lastID int64) { b.setOffset(lastID) }

func (b *startupBot) pendingUpdates(ctx context.Context, offset int64) ([]*models.Update, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&limit=100&timeout=0", b.token, offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: prepare pending updates: %s", core.RedactToken(err.Error(), b.token))
	}
	client := b.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: read pending updates: %s", core.RedactToken(err.Error(), b.token))
	}
	defer resp.Body.Close()
	var result struct {
		OK        bool             `json:"ok"`
		ErrorCode int              `json:"error_code"`
		Updates   []*models.Update `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("telegram: decode pending updates: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return nil, fmt.Errorf("telegram: pending updates rejected (HTTP %d, API %d)", resp.StatusCode, result.ErrorCode)
	}
	return result.Updates, nil
}

type downtimeDestination struct {
	chatID   int64
	threadID int
}

type downtimeSummary struct {
	count     int
	earliest  time.Time
	noticeID  int
	attempted bool
}

// Telegram retains pending updates for at most 24 hours and may choose a new
// random update ID after a week of inactivity. Expiring old cursors before
// acknowledgement avoids swallowing unseen updates after that reset.
const telegramUpdateRetention = 24 * time.Hour

type updateCursor struct {
	nextOffset int64
	updatedAt  time.Time
}

// The cursor, cutoff and summaries survive connection retries in memory. A
// failed request may already have acknowledged the preceding page on Telegram;
// resetting the summaries on retry would lose those counts.
type downtimeDrain struct {
	mu           sync.Mutex
	cutoff       time.Time
	cursor       updateCursor
	lastUpdateID int64
	complete     atomic.Bool
	groups       map[downtimeDestination]*downtimeSummary
}

func (p *Platform) SetI18n(i18n *core.I18n) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.i18n = i18n
}

func (p *Platform) noticeI18n() *core.I18n {
	p.mu.RLock()
	i18n := p.i18n
	p.mu.RUnlock()
	if i18n == nil {
		return core.NewI18n(core.LangEnglish)
	}
	return i18n
}

func (p *Platform) drainDowntime(ctx context.Context, source pendingUpdateSource, bot telegramBot) error {
	p.mu.Lock()
	drain := p.downtime
	if drain == nil || drain.complete.Load() {
		// Telegram timestamps have second precision. Messages in the current
		// second are live, avoiding accidental discard at the startup boundary.
		drain = &downtimeDrain{
			cutoff: time.Now().UTC().Truncate(time.Second), cursor: p.processedCursor,
			groups: make(map[downtimeDestination]*downtimeSummary),
		}
		p.downtime = drain
	}
	p.mu.Unlock()
	drain.mu.Lock()
	defer drain.mu.Unlock()
	if err := p.collectDowntime(ctx, source, drain); err != nil {
		return err
	}
	drain.complete.Store(true)
	drain.lastUpdateID = drain.cursor.nextOffset - 1
	source.setPollingOffset(drain.lastUpdateID)
	for _, dest := range sortedDowntimeDestinations(drain.groups) {
		p.sendDowntimeNotice(ctx, bot, dest, drain.groups[dest])
	}
	return ctx.Err()
}

func (p *Platform) collectDowntime(ctx context.Context, source pendingUpdateSource, drain *downtimeDrain) error {
	if time.Since(drain.cursor.updatedAt) >= telegramUpdateRetention {
		// Keep an incomplete drain's counts and cutoff, but its expired
		// acknowledgement prefix cannot still be waiting on Telegram.
		drain.cursor = updateCursor{}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		updates, err := source.pendingUpdates(ctx, drain.cursor.nextOffset)
		if err != nil {
			return err
		}
		// Acknowledge only the prefix already counted or processed before a
		// reconnect; completed live inputs must not appear in skipped notices.
		// Stop before a live update, leaving it on Telegram for normal polling.
		if len(updates) == 0 {
			return nil
		}
		advanced := false
		for _, update := range updates {
			if update == nil || update.ID < drain.cursor.nextOffset {
				continue
			}
			if msg := update.Message; msg != nil && int64(msg.Date) >= drain.cutoff.Unix() {
				if advanced {
					// Confirm only the skipped prefix. The returned live updates
					// remain unacknowledged and will be read by the poller.
					_, err := source.pendingUpdates(ctx, drain.cursor.nextOffset)
					return err
				}
				return nil
			}
			p.countDowntimeMessage(drain, update.Message)
			drain.cursor = updateCursor{nextOffset: update.ID + 1, updatedAt: time.Now()}
			advanced = true
		}
		if !advanced {
			return fmt.Errorf("telegram: pending updates did not advance the cursor")
		}
	}
}

// Routing topic-created service events is useful during the drain even though
// they are not prompts. This optional internal capability also allows the
// native topic-opener routing change to be maintained independently.
type downtimeTopicRouter interface {
	routeTopicMessage(*models.Message) (service, ambiguous bool)
}

func (p *Platform) countDowntimeMessage(drain *downtimeDrain, msg *models.Message) (downtimeDestination, *downtimeSummary) {
	if msg == nil || msg.From == nil || msg.From.IsBot || !core.AllowList(p.allowFrom, strconv.FormatInt(msg.From.ID, 10)) {
		return downtimeDestination{}, nil
	}
	if router, ok := any(p).(downtimeTopicRouter); ok {
		if service, _ := router.routeTopicMessage(msg); service {
			return downtimeDestination{}, nil
		}
	}
	// Count only message types accepted by handleMessage, without downloading
	// attachments, acknowledging buttons or invoking the agent.
	if msg.Text == "" && len(msg.Photo) == 0 && msg.Document == nil && msg.Voice == nil && msg.Audio == nil && msg.Location == nil {
		return downtimeDestination{}, nil
	}
	isGroup := msg.Chat.Type == models.ChatTypeGroup || msg.Chat.Type == models.ChatTypeSupergroup
	if isGroup && !p.groupReplyAll && !p.isDirectedAtBot(msg) {
		return downtimeDestination{}, nil
	}
	dest := downtimeDestination{chatID: msg.Chat.ID}
	if msg.Chat.IsForum || !isGroup {
		dest.threadID = msg.MessageThreadID
	}
	summary := drain.groups[dest]
	date := time.Unix(int64(msg.Date), 0).UTC()
	if summary == nil {
		summary = &downtimeSummary{earliest: date}
		drain.groups[dest] = summary
	}
	summary.count++
	if date.Before(summary.earliest) {
		summary.earliest = date
	}
	return dest, summary
}

func sortedDowntimeDestinations(groups map[downtimeDestination]*downtimeSummary) []downtimeDestination {
	destinations := make([]downtimeDestination, 0, len(groups))
	for dest := range groups {
		destinations = append(destinations, dest)
	}
	sort.Slice(destinations, func(i, j int) bool {
		if destinations[i].chatID != destinations[j].chatID {
			return destinations[i].chatID < destinations[j].chatID
		}
		return destinations[i].threadID < destinations[j].threadID
	})
	return destinations
}

func (p *Platform) sendDowntimeNotice(ctx context.Context, bot telegramBot, dest downtimeDestination, summary *downtimeSummary) {
	key := core.MsgDowntimeMessagesSkippedChat
	if dest.threadID != 0 {
		key = core.MsgDowntimeMessagesSkippedTopic
	}
	text := p.noticeI18n().Tf(key, summary.count, summary.earliest.UTC().Format("2006-01-02 15:04:05 UTC"))
	var err error
	if summary.noticeID != 0 {
		_, err = bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{ChatID: dest.chatID, MessageID: summary.noticeID, Text: text})
	} else if !summary.attempted {
		// An ambiguous network failure may mean Telegram accepted the send.
		// Do not retry that send on reconnect and flood the chat with copies.
		summary.attempted = true
		var notice *models.Message
		notice, err = bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: dest.chatID, MessageThreadID: dest.threadID, Text: text})
		if err == nil && notice != nil {
			summary.noticeID = notice.ID
		}
	}
	if err != nil {
		slog.Warn("telegram: downtime notice failed", "chat", dest.chatID, "thread", dest.threadID, "error", core.RedactToken(err.Error(), p.token))
	}
}

// Send dates need not follow update order. If an old message follows the first
// live update, discard it here and update the original summary in place. This
// preserves live updates without relying on an in-memory replay buffer.
func (p *Platform) discardLateDowntimeUpdate(ctx context.Context, update *models.Update) bool {
	p.mu.RLock()
	drain, bot := p.downtime, p.bot
	p.mu.RUnlock()
	if drain == nil || bot == nil {
		return false
	}
	drain.mu.Lock()
	defer drain.mu.Unlock()
	if !drain.complete.Load() {
		return false
	}
	if msg := update.Message; msg != nil && int64(msg.Date) < drain.cutoff.Unix() {
		// Deduplicate only the old backlog. Telegram may choose a lower random
		// update ID after a week without updates, so a lifetime high-water mark
		// must not reject live messages or callbacks.
		if update.ID <= drain.lastUpdateID {
			return true
		}
		drain.lastUpdateID = update.ID
		if dest, summary := p.countDowntimeMessage(drain, msg); summary != nil {
			p.sendDowntimeNotice(ctx, bot, dest, summary)
		}
		return true
	}
	return false
}
