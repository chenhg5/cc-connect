package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type downtimeNoticeBot struct {
	*stubTelegramBot
	noticeMu sync.Mutex
	sends    []tgbot.SendMessageParams
	edits    []tgbot.EditMessageTextParams
}

func newDowntimeNoticeBot() *downtimeNoticeBot {
	return &downtimeNoticeBot{stubTelegramBot: newStubTelegramBot()}
}

func (b *downtimeNoticeBot) SendMessage(ctx context.Context, params *tgbot.SendMessageParams) (*models.Message, error) {
	b.noticeMu.Lock()
	defer b.noticeMu.Unlock()
	b.sends = append(b.sends, *params)
	return &models.Message{ID: 100 + len(b.sends)}, b.sendErr
}

func (b *downtimeNoticeBot) EditMessageText(ctx context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error) {
	b.noticeMu.Lock()
	defer b.noticeMu.Unlock()
	b.edits = append(b.edits, *params)
	return &models.Message{ID: params.MessageID}, b.sendErr
}

type downtimeRoundTrip func(*http.Request) (*http.Response, error)

func (f downtimeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Simulate Telegram's confirmation semantics, including the case where a
// request confirms a page but its response is lost before reaching the caller.
type downtimeAPI struct {
	updates      []*models.Update
	pageSize     int
	offsets      []int64
	confirmed    int64
	failOffset   int64
	failuresLeft int
	pollOffset   int64
}

func (api *downtimeAPI) bot(t *testing.T, sender telegramBot) *startupBot {
	t.Helper()
	return &startupBot{telegramBot: sender, token: "test-token", setOffset: func(id int64) { api.pollOffset = id }, client: &http.Client{
		Transport: downtimeRoundTrip(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/bottest-token/getUpdates" || req.URL.Query().Get("timeout") != "0" || req.URL.Query().Get("limit") != "100" {
				t.Fatalf("unexpected API request: %s", req.URL.Redacted())
			}
			offset, err := strconv.ParseInt(req.URL.Query().Get("offset"), 10, 64)
			if err != nil || offset < 0 {
				t.Fatalf("invalid offset %q", req.URL.Query().Get("offset"))
			}
			api.offsets = append(api.offsets, offset)
			if offset > api.confirmed {
				api.confirmed = offset
			}
			if offset == api.failOffset && api.failuresLeft > 0 {
				api.failuresLeft--
				return nil, errors.New("simulated response loss")
			}
			var result []*models.Update
			for _, update := range api.updates {
				if update.ID >= api.confirmed {
					result = append(result, update)
					if len(result) == api.pageSize {
						break
					}
				}
			}
			body, err := json.Marshal(map[string]any{"ok": true, "result": result})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
		}),
	}}
}

func downtimeMessage(id int64, date int, chat int64, thread int) *models.Update {
	return &models.Update{ID: id, Message: &models.Message{ID: int(id), Date: date, Chat: models.Chat{ID: chat, Type: models.ChatTypePrivate}, MessageThreadID: thread, From: &models.User{ID: 7}, Text: "pending prompt"}}
}

func TestDrainDowntime_CountsAllPagesWithoutInvokingAgentOrDownloadingMedia(t *testing.T) {
	date := int(time.Now().Add(-time.Hour).Unix())
	updates := []*models.Update{
		downtimeMessage(1, date+5, 10, 21), downtimeMessage(2, date, 10, 21),
		downtimeMessage(3, date+10, 10, 22), downtimeMessage(4, date+20, 11, 0),
		downtimeMessage(5, date+21, 10, 21), downtimeMessage(6, date+22, 10, 21),
		downtimeMessage(7, date+23, 10, 21), downtimeMessage(8, date+24, 10, 21),
		downtimeMessage(9, date+25, 10, 21), downtimeMessage(10, date+26, 10, 21),
		{ID: 11, CallbackQuery: &models.CallbackQuery{ID: "stale-click"}},
	}
	updates[1].Message.Text = ""
	updates[1].Message.Photo = []models.PhotoSize{{FileID: "photo"}}
	updates[4].Message.Text = ""
	updates[4].Message.Voice = &models.Voice{FileID: "voice"}
	updates[5].Message.Text = ""
	updates[5].Message.Audio = &models.Audio{FileID: "audio"}
	updates[6].Message.Text = ""
	updates[6].Message.Document = &models.Document{FileID: "document"}
	updates[7].Message.From.ID = 8 // unauthorized
	updates[8].Message.From.IsBot = true
	updates[9].Message.Text = ""
	updates[9].Message.ForumTopicCreated = &models.ForumTopicCreated{Name: "service event"}
	api := &downtimeAPI{updates: updates, pageSize: 2}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	p := &Platform{allowFrom: "7", bot: source, handler: func(core.Platform, *core.Message) { t.Error("backlog dispatched to agent") }}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if got, want := api.offsets, []int64{0, 3, 5, 7, 9, 11, 12}; !reflect.DeepEqual(got, want) {
		t.Fatalf("offsets = %v, want %v", got, want)
	}
	if api.pollOffset != 11 || api.confirmed != 12 {
		t.Fatalf("poll offset=%d confirmed=%d", api.pollOffset, api.confirmed)
	}
	if len(sender.sends) != 3 {
		t.Fatalf("notices = %d, want 3", len(sender.sends))
	}
	first := sender.sends[0]
	if first.ChatID != int64(10) || first.MessageThreadID != 21 || !strings.Contains(first.Text, "skipped 5 messages in this topic") || !strings.Contains(first.Text, time.Unix(int64(date), 0).UTC().Format("2006-01-02 15:04:05 UTC")) || !strings.Contains(first.Text, "Please resend") {
		t.Fatalf("unexpected notice: %+v", first)
	}
	if sender.GetFileCallCount() != 0 || sender.answerCallbackCalls != 0 {
		t.Fatal("drain downloaded a file or handled a callback")
	}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if len(sender.sends) != 3 {
		t.Fatal("reconnect repeated notices for acknowledged messages")
	}
}

func TestDrainDowntime_PreservesLiveMessagesAndEditsNoticeForLateOldMessage(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	old := downtimeMessage(10, int(cutoff.Unix())-20, 10, 21)
	live := downtimeMessage(11, int(cutoff.Unix()), 10, 21)
	lateOld := downtimeMessage(12, int(cutoff.Unix())-30, 10, 21)
	api := &downtimeAPI{updates: []*models.Update{old, live, lateOld}, pageSize: 100}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	var received []string
	p := &Platform{allowFrom: "7", bot: source, downtime: &downtimeDrain{cutoff: cutoff, groups: make(map[downtimeDestination]*downtimeSummary)}, handler: func(_ core.Platform, msg *core.Message) { received = append(received, msg.Content) }}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if api.confirmed != 11 || api.pollOffset != 10 || len(sender.sends) != 1 {
		t.Fatalf("live input acknowledged or wrong notice count: confirmed=%d offset=%d sends=%d", api.confirmed, api.pollOffset, len(sender.sends))
	}
	p.processUpdate(context.Background(), live)
	p.processUpdate(context.Background(), lateOld)
	p.processUpdate(context.Background(), lateOld) // replay must not count twice
	if len(received) != 1 || len(sender.sends) != 1 || len(sender.edits) != 1 {
		t.Fatalf("received=%v sends=%d edits=%d", received, len(sender.sends), len(sender.edits))
	}
	if !strings.Contains(sender.edits[0].Text, "skipped 2 messages") || sender.edits[0].MessageID != 101 || !strings.Contains(sender.edits[0].Text, time.Unix(int64(lateOld.Message.Date), 0).UTC().Format("2006-01-02 15:04:05 UTC")) {
		t.Fatalf("wrong updated notice: %+v", sender.edits[0])
	}
}

func TestDrainDowntime_RetryRetainsAcknowledgedCountsAndCutoff(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	api := &downtimeAPI{updates: []*models.Update{downtimeMessage(1, int(cutoff.Unix())-20, 10, 0), downtimeMessage(2, int(cutoff.Unix()), 10, 0)}, pageSize: 100, failOffset: 2, failuresLeft: 1}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	p := &Platform{allowFrom: "7", bot: source, downtime: &downtimeDrain{cutoff: cutoff, groups: make(map[downtimeDestination]*downtimeSummary)}}
	if err := p.drainDowntime(context.Background(), source, source); err == nil {
		t.Fatal("expected simulated network error")
	}
	if len(sender.sends) != 0 || api.confirmed != 2 {
		t.Fatal("notice sent before acknowledgement completed")
	}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if !p.downtime.cutoff.Equal(cutoff) || len(sender.sends) != 1 || !strings.Contains(sender.sends[0].Text, "skipped 1 messages") || api.pollOffset != 1 {
		t.Fatalf("retry lost state: cutoff=%v notices=%+v offset=%d", p.downtime.cutoff, sender.sends, api.pollOffset)
	}
}

func TestDrainDowntime_CompletedCursorIncludesNonPromptUpdates(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	unauthorized := downtimeMessage(100, int(now.Unix()), 10, 0)
	unauthorized.Message.From.ID = 8
	for name, processed := range map[string]*models.Update{
		"service":      {ID: 100, Message: topicCreated(100, int(now.Unix()), 55)},
		"callback":     {ID: 100, CallbackQuery: &models.CallbackQuery{ID: "consumed"}},
		"unauthorized": unauthorized,
		"unsupported":  {ID: 100},
	} {
		t.Run(name, func(t *testing.T) {
			missed := downtimeMessage(101, int(now.Unix())-1, 10, 0)
			api := &downtimeAPI{updates: []*models.Update{processed, missed}, pageSize: 100}
			sender := newDowntimeNoticeBot()
			source := api.bot(t, sender)
			p := &Platform{allowFrom: "7", bot: source, handler: func(core.Platform, *core.Message) { t.Error("non-prompt update dispatched") }}
			p.processUpdate(context.Background(), processed)
			if err := p.drainDowntime(context.Background(), source, source); err != nil {
				t.Fatal(err)
			}
			if len(api.offsets) == 0 || api.offsets[0] != 101 {
				t.Fatalf("recovery did not acknowledge the completed prefix: %v", api.offsets)
			}
			if len(sender.sends) != 1 || !strings.Contains(sender.sends[0].Text, "skipped 1 messages") {
				t.Fatalf("completed update inflated downtime notice: %+v", sender.sends)
			}
		})
	}
}

func TestDrainDowntime_CompletedCursorUsesLatestIDAfterReset(t *testing.T) {
	sender := newDowntimeNoticeBot()
	missed := downtimeMessage(11, int(time.Now().Unix())-1, 10, 0)
	api := &downtimeAPI{updates: []*models.Update{missed}, pageSize: 100}
	source := api.bot(t, sender)
	p := &Platform{bot: source}
	p.processUpdate(context.Background(), &models.Update{ID: 1000})
	p.processUpdate(context.Background(), &models.Update{ID: 10})
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if len(api.offsets) == 0 || api.offsets[0] != 11 || len(sender.sends) != 1 {
		t.Fatalf("recovery reused a pre-reset high-water mark: offsets=%v notices=%+v", api.offsets, sender.sends)
	}
}

func TestDrainDowntime_ExpiresCursorsBeforeAcknowledgingNewUpdateIDs(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprintf("incomplete=%v", incomplete), func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			live := downtimeMessage(10, int(now.Unix())+1, 10, 0)
			api := &downtimeAPI{updates: []*models.Update{live}, pageSize: 100}
			sender := newDowntimeNoticeBot()
			source := api.bot(t, sender)
			var received int
			p := &Platform{bot: source, handler: func(core.Platform, *core.Message) { received++ }}
			stale := updateCursor{nextOffset: 1001, updatedAt: now.Add(-telegramUpdateRetention)}
			if incomplete {
				p.downtime = &downtimeDrain{
					cutoff: now.Add(-8 * 24 * time.Hour), cursor: stale,
					groups: map[downtimeDestination]*downtimeSummary{{chatID: 10}: {count: 2, earliest: now.Add(-9 * 24 * time.Hour)}},
				}
			} else {
				p.processedCursor = stale
			}
			if err := p.drainDowntime(context.Background(), source, source); err != nil {
				t.Fatal(err)
			}
			if len(api.offsets) != 1 || api.offsets[0] != 0 || api.confirmed != 0 {
				t.Fatalf("stale cursor acknowledged unseen lower IDs: %v confirmed=%d", api.offsets, api.confirmed)
			}
			p.processUpdate(context.Background(), live)
			if received != 1 {
				t.Fatal("live update after cursor expiry was not processed")
			}
			if incomplete {
				if len(sender.sends) != 1 || !strings.Contains(sender.sends[0].Text, "skipped 2 messages") || !p.downtime.cutoff.Equal(now.Add(-8*24*time.Hour)) {
					t.Fatalf("cursor expiry lost in-progress summary or cutoff: %+v", sender.sends)
				}
			} else if len(sender.sends) != 0 {
				t.Fatalf("live update was counted as downtime: %+v", sender.sends)
			}
		})
	}
}

func TestDiscardLateDowntimeUpdate_PreservesLiveTrafficAfterUpdateIDReset(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	drain := &downtimeDrain{cutoff: cutoff, lastUpdateID: 1000, groups: make(map[downtimeDestination]*downtimeSummary)}
	drain.complete.Store(true)
	sender := newDowntimeNoticeBot()
	var received int
	p := &Platform{bot: sender, downtime: drain, handler: func(core.Platform, *core.Message) { received++ }}
	// Telegram randomizes the next update ID after a week without updates;
	// a fresh message can therefore have a lower ID than the drained backlog.
	p.processUpdate(context.Background(), downtimeMessage(10, int(cutoff.Unix()), 10, 0))
	if received != 1 {
		t.Fatal("fresh message after update ID reset was discarded")
	}
	callback := &models.Update{ID: 11, CallbackQuery: &models.CallbackQuery{ID: "new-click"}}
	if p.discardLateDowntimeUpdate(context.Background(), callback) {
		t.Fatal("fresh callback after update ID reset was discarded")
	}
	// Live traffic must not move the backlog high-water mark in either
	// direction; only old messages participate in backlog deduplication.
	p.processUpdate(context.Background(), downtimeMessage(2000, int(cutoff.Unix()), 10, 0))
	if drain.lastUpdateID != 1000 {
		t.Fatalf("live traffic changed backlog high-water mark to %d", drain.lastUpdateID)
	}
	old := downtimeMessage(1001, int(cutoff.Unix())-20, 10, 0)
	p.processUpdate(context.Background(), old)
	p.processUpdate(context.Background(), old)
	if received != 2 || len(sender.sends) != 1 || len(sender.edits) != 0 {
		t.Fatalf("received=%d notices=%d edits=%d; expected only live inputs and one old-message notice", received, len(sender.sends), len(sender.edits))
	}
	if !strings.Contains(sender.sends[0].Text, "skipped 1 messages") || drain.lastUpdateID != 1001 {
		t.Fatalf("old-message count or dedup state is wrong: notice=%q high-water=%d", sender.sends[0].Text, drain.lastUpdateID)
	}
}

func TestDrainDowntime_NoticesOnlyEligibleGroupMessages(t *testing.T) {
	date := int(time.Now().Add(-time.Hour).Unix())
	mentioned := downtimeMessage(1, date, -10, 90)
	mentioned.Message.Chat.Type = models.ChatTypeSupergroup
	mentioned.Message.Text = "@mybot help"
	mentioned.Message.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 0, Length: 6}}
	unaddressed := downtimeMessage(2, date, -10, 90)
	unaddressed.Message.Chat.Type = models.ChatTypeSupergroup
	api := &downtimeAPI{updates: []*models.Update{mentioned, unaddressed}, pageSize: 100}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	p := &Platform{allowFrom: "7", bot: source, selfUser: &models.User{ID: 99, Username: "mybot"}}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if len(sender.sends) != 1 || sender.sends[0].MessageThreadID != 0 || !strings.Contains(sender.sends[0].Text, "skipped 1 messages in this chat") {
		t.Fatalf("unexpected notices: %+v", sender.sends)
	}
}

func TestDrainDowntime_SendFailureDoesNotRepeatNoticeOrBlockLiveInput(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	api := &downtimeAPI{updates: []*models.Update{downtimeMessage(1, int(cutoff.Unix())-20, 10, 0)}, pageSize: 100}
	sender := newDowntimeNoticeBot()
	sender.sendErr = errors.New("ambiguous transport failure")
	source := api.bot(t, sender)
	var received int
	p := &Platform{bot: source, handler: func(core.Platform, *core.Message) { received++ }}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	p.processUpdate(context.Background(), downtimeMessage(2, int(cutoff.Unix())-10, 10, 0))
	p.processUpdate(context.Background(), downtimeMessage(3, int(cutoff.Unix()), 10, 0))
	if len(sender.sends) != 1 || received != 1 {
		t.Fatalf("sends=%d received=%d", len(sender.sends), received)
	}
}

func TestDrainDowntime_CancelledDoesNotReadOrSend(t *testing.T) {
	api := &downtimeAPI{pageSize: 100}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	p := &Platform{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.drainDowntime(ctx, source, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if len(api.offsets) != 0 || len(sender.sends) != 0 {
		t.Fatal("cancelled drain performed IO")
	}
}

func TestPendingUpdates_RejectsHTTPAndAPIErrorsAndRedactsToken(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		failure    error
	}{
		{name: "HTTP", status: 503, body: `{"ok":false,"error_code":503}`},
		{name: "API", status: 200, body: `{"ok":false,"error_code":401}`},
		{name: "malformed", status: 200, body: "not JSON"},
		{name: "transport", failure: errors.New("network failure at /botsecret-token/getUpdates")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &startupBot{token: "secret-token", client: &http.Client{Transport: downtimeRoundTrip(func(*http.Request) (*http.Response, error) {
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			_, err := b.pendingUpdates(context.Background(), 0)
			if err == nil || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDowntimeNotices_UseConfiguredLanguageAndNumericUTCTimestamps(t *testing.T) {
	for _, lang := range []core.Language{core.LangEnglish, core.LangChinese, core.LangTraditionalChinese, core.LangJapanese, core.LangSpanish} {
		t.Run(string(lang), func(t *testing.T) {
			p := &Platform{}
			p.SetI18n(core.NewI18n(lang))
			sender := newDowntimeNoticeBot()
			earliest := time.Date(2026, time.October, 26, 10, 11, 12, 0, time.UTC)
			p.sendDowntimeNotice(context.Background(), sender, downtimeDestination{chatID: 1}, &downtimeSummary{count: 3, earliest: earliest})
			text := sender.sends[0].Text
			// English month names and "at" must not leak into translated notices.
			if strings.Contains(text, "%!") || !strings.Contains(text, "3") || !strings.Contains(text, "2026-10-26 10:11:12 UTC") {
				t.Fatalf("invalid localized notice: %s", text)
			}
			if lang != core.LangEnglish && strings.HasPrefix(text, "Sorry") {
				t.Fatalf("notice not localized: %s", text)
			}
		})
	}
}

func TestRunConnection_DrainsBeforeReadyAndPolling(t *testing.T) {
	api := &downtimeAPI{updates: []*models.Update{downtimeMessage(1, int(time.Now().Add(-time.Hour).Unix()), 1, 0)}, pageSize: 100}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	var events []string
	p := &Platform{newBot: func(string, func(context.Context, *models.Update), *http.Client) (telegramBot, *models.User, func(context.Context), error) {
		return source, &models.User{ID: 42}, func(context.Context) {
			if api.confirmed != 2 || len(sender.sends) != 1 {
				t.Error("polling began before drain completed")
			}
			events = append(events, "poll")
		}, nil
	}}
	p.SetLifecycleHandler(testLifecycleHandler{onReady: func(core.Platform) {
		if api.confirmed != 2 || len(sender.sends) != 1 {
			t.Error("ready signalled before drain completed")
		}
		events = append(events, "ready")
	}})
	if err := p.runConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(events) != "[ready poll]" {
		t.Fatalf("events=%v", events)
	}
}
