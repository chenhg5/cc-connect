package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/go-telegram/bot/models"
)

func TestTelegramRecovery_SkippedNativeOpenerNoticeAndLiveReplyUseSameTopic(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	date := int(cutoff.Add(-time.Hour).Unix())
	created := &models.Update{ID: 1, Message: topicCreated(10, date, 55)}
	opener := &models.Update{ID: 2, Message: topicMessage(41, date+1, 0)}
	opener.Message.Text = ""
	opener.Message.Document = &models.Document{FileID: "offline-document", FileName: "notes.txt"}
	live := &models.Update{ID: 3, Message: topicMessage(42, int(cutoff.Unix()), 55)}
	api := &downtimeAPI{updates: []*models.Update{created, opener, live}, pageSize: 1}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	var received []*core.Message
	p := &Platform{
		allowFrom: "7",
		downtime:  &downtimeDrain{cutoff: cutoff, groups: make(map[downtimeDestination]*downtimeSummary)},
		handler: func(platform core.Platform, msg *core.Message) {
			received = append(received, msg)
			if err := platform.Reply(context.Background(), msg.ReplyCtx, "live response"); err != nil {
				t.Error(err)
			}
		},
	}
	p.newBot = func(string, func(context.Context, *models.Update), *http.Client) (telegramBot, *models.User, func(context.Context), error) {
		return source, &models.User{ID: 99, Username: "bridge"}, func(ctx context.Context) {
			if api.confirmed != 3 || api.pollOffset != 2 || len(sender.sends) != 1 {
				t.Fatalf("incorrect recovery boundary: confirmed=%d poll=%d notices=%d", api.confirmed, api.pollOffset, len(sender.sends))
			}
			p.processUpdate(ctx, live)
		}, nil
	}
	if err := p.runConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 || received[0].SessionKey != "telegram:100:55:7" {
		t.Fatalf("wrong live session or backlog reached agent: %+v", received)
	}
	if sender.GetFileCallCount() != 0 || len(sender.sends) != 2 {
		t.Fatalf("downloaded skipped document or incorrect responses: downloads=%d sends=%d", sender.GetFileCallCount(), len(sender.sends))
	}
	for _, sent := range sender.sends {
		if sent.ChatID != int64(100) || sent.MessageThreadID != 55 {
			t.Fatalf("notice/reply escaped native topic: %+v", sent)
		}
	}
	if !strings.Contains(sender.sends[0].Text, "skipped 1 messages in this topic") || sender.sends[1].Text != "live response" {
		t.Fatalf("unexpected notice and reply: %+v", sender.sends)
	}
}

func TestTelegramRecovery_CreationBeforeCutoffRoutesLiveMediaOpener(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	created := &models.Update{ID: 10, Message: topicCreated(100, int(cutoff.Unix())-1, 55)}
	live := &models.Update{ID: 11, Message: topicMessage(200, int(cutoff.Unix()), 0)}
	live.Message.Text = ""
	live.Message.Photo = []models.PhotoSize{{FileID: "live-photo"}}
	api := &downtimeAPI{updates: []*models.Update{created, live}, pageSize: 100}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	var received []*core.Message
	p := &Platform{
		allowFrom: "7", bot: source,
		downtime:   &downtimeDrain{cutoff: cutoff, groups: make(map[downtimeDestination]*downtimeSummary)},
		httpClient: &http.Client{Transport: topicFileTransport{}},
		handler:    func(_ core.Platform, msg *core.Message) { received = append(received, msg) },
	}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if len(sender.sends) != 0 || sender.GetFileCallCount() != 0 || api.confirmed != 11 {
		t.Fatal("service event caused a notice or live opener was processed during drain")
	}
	p.processUpdate(context.Background(), live)
	if len(received) != 1 || received[0].SessionKey != "telegram:100:55:7" || len(received[0].Images) != 1 {
		t.Fatalf("live media opener lost its pre-cutoff topic: %+v", received)
	}
	if sender.GetFileCallCount() != 1 || received[0].ReplyCtx.(replyContext).threadID != 55 {
		t.Fatal("live photo was not downloaded and routed exactly once")
	}
}

func TestTelegramRecovery_AmbiguousSkippedOpenerGetsSummaryWithoutAgentTurn(t *testing.T) {
	cutoff := time.Now().UTC().Truncate(time.Second)
	date := int(cutoff.Add(-time.Hour).Unix())
	api := &downtimeAPI{updates: []*models.Update{
		{ID: 1, Message: topicCreated(10, date, 55)},
		{ID: 2, Message: topicCreated(20, date, 66)},
		{ID: 3, Message: topicMessage(30, date+1, 0)},
	}, pageSize: 100}
	sender := newDowntimeNoticeBot()
	source := api.bot(t, sender)
	p := &Platform{allowFrom: "7", bot: source, handler: func(core.Platform, *core.Message) { t.Error("backlog reached agent") }}
	if err := p.drainDowntime(context.Background(), source, source); err != nil {
		t.Fatal(err)
	}
	if len(sender.sends) != 1 || sender.sends[0].MessageThreadID != 0 || !strings.Contains(sender.sends[0].Text, "skipped 1 messages in this chat") {
		t.Fatalf("ambiguous backlog was lost or produced individual errors: %+v", sender.sends)
	}
}

func TestTelegramRecovery_AmbiguityNoticeUsesConfiguredLanguage(t *testing.T) {
	sender := newDowntimeNoticeBot()
	p := &Platform{allowFrom: "7", bot: sender, handler: func(core.Platform, *core.Message) { t.Error("ambiguous message reached agent") }}
	p.SetI18n(core.NewI18n(core.LangSpanish))
	date := int(time.Now().Unix())
	p.handleMessage(context.Background(), topicCreated(10, date, 55))
	p.handleMessage(context.Background(), topicCreated(20, date, 66))
	opener := topicMessage(30, date+1, 0)
	opener.From.LanguageCode = "ja"
	p.handleMessage(context.Background(), opener)
	if len(sender.sends) != 1 || !strings.Contains(sender.sends[0].Text, "No pude determinar") {
		t.Fatalf("configured language not used for topic notice: %+v", sender.sends)
	}
}
