package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/go-telegram/bot/models"
)

func topicMessage(id, date, threadID int) *models.Message {
	return &models.Message{
		ID: id, Date: date, MessageThreadID: threadID, Text: "hello",
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		From: &models.User{ID: 7, Username: "alice"},
	}
}

func topicCreated(id, date, threadID int) *models.Message {
	msg := topicMessage(id, date, threadID)
	msg.Text = ""
	msg.ForumTopicCreated = &models.ForumTopicCreated{Name: "New conversation"}
	return msg
}

type topicFileTransport struct{}

func (topicFileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Request: req,
		Body: io.NopCloser(strings.NewReader("attachment")),
	}, nil
}

func TestProcessUpdate_PrivateTopicOpenerRoutesToCreatedTopic(t *testing.T) {
	cases := []struct {
		name  string
		set   func(*models.Message)
		check func(*testing.T, *core.Message)
	}{
		{"text", func(*models.Message) {}, func(t *testing.T, msg *core.Message) {
			if msg.Content != "hello" {
				t.Fatalf("content = %q", msg.Content)
			}
		}},
		{"photo", func(msg *models.Message) {
			msg.Text = ""
			msg.Photo = []models.PhotoSize{{FileID: "photo"}}
		}, func(t *testing.T, msg *core.Message) {
			if len(msg.Images) != 1 {
				t.Fatal("photo opener not delivered")
			}
		}},
		{"document", func(msg *models.Message) {
			msg.Text = ""
			msg.Document = &models.Document{FileID: "document", FileName: "test.txt"}
		}, func(t *testing.T, msg *core.Message) {
			if len(msg.Files) != 1 {
				t.Fatal("document opener not delivered")
			}
		}},
		{"voice", func(msg *models.Message) {
			msg.Text = ""
			msg.Voice = &models.Voice{FileID: "voice", MimeType: "audio/ogg"}
		}, func(t *testing.T, msg *core.Message) {
			if msg.Audio == nil {
				t.Fatal("voice opener not delivered")
			}
		}},
		{"audio", func(msg *models.Message) {
			msg.Text = ""
			msg.Audio = &models.Audio{FileID: "audio", MimeType: "audio/mpeg"}
		}, func(t *testing.T, msg *core.Message) {
			if msg.Audio == nil {
				t.Fatal("audio opener not delivered")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var handled []*core.Message
			p := &Platform{allowFrom: "7", bot: newStubTelegramBot(),
				httpClient: &http.Client{Transport: topicFileTransport{}},
				handler:    func(_ core.Platform, msg *core.Message) { handled = append(handled, msg) },
			}
			date := int(time.Now().Unix())
			p.processUpdate(context.Background(), &models.Update{Message: topicCreated(10, date, 55)})
			opener := topicMessage(41, date+1, 0) // Other chats/replies can occupy IDs 11..40.
			tc.set(opener)
			p.processUpdate(context.Background(), &models.Update{Message: opener})
			if len(handled) != 1 {
				t.Fatalf("got %d agent messages, want only opener", len(handled))
			}
			got := handled[0]
			if got.SessionKey != "telegram:100:55:7" || got.ChannelKey != "100:55" {
				t.Fatalf("opener routed to %q / %q", got.SessionKey, got.ChannelKey)
			}
			if rc := got.ReplyCtx.(replyContext); rc.threadID != 55 || rc.messageID != 41 {
				t.Fatalf("reply context = %+v", rc)
			}
			tc.check(t, got)
			p.processUpdate(context.Background(), &models.Update{Message: topicMessage(42, date+2, 55)})
			if len(handled) != 2 || handled[1].SessionKey != got.SessionKey {
				t.Fatal("explicit follow-up did not continue opener's session")
			}
		})
	}
}

func TestPrivateTopicRouting_IsolatesCreatorsAndChats(t *testing.T) {
	p := &Platform{allowFrom: "7,8"}
	p.routeTopicMessage(topicCreated(10, 100, 55))
	otherChat := topicCreated(11, 100, 66)
	otherChat.Chat.ID = 200
	p.routeTopicMessage(otherChat)
	otherUser := topicMessage(12, 101, 0)
	otherUser.From.ID = 8
	p.routeTopicMessage(otherUser)
	if otherUser.MessageThreadID != 0 {
		t.Fatal("different sender adopted creator's topic")
	}

	for _, tc := range []struct {
		chat       int64
		id, thread int
	}{{100, 13, 55}, {200, 19, 66}} {
		msg := topicMessage(tc.id, 101, 0)
		msg.Chat.ID = tc.chat
		p.routeTopicMessage(msg)
		if msg.MessageThreadID != tc.thread {
			t.Fatalf("chat %d routed to %d", tc.chat, msg.MessageThreadID)
		}
	}
	p.routeTopicMessage(topicCreated(20, 102, 77))
	msg := topicMessage(21, 102, 0)
	p.routeTopicMessage(msg)
	if msg.MessageThreadID != 77 {
		t.Fatal("second conversation reused earlier topic")
	}
}

func TestPrivateTopicRouting_RejectsUntrustedCreation(t *testing.T) {
	cases := []struct {
		name  string
		alter func(*models.Message)
	}{
		{"bot", func(msg *models.Message) { msg.From.IsBot = true }},
		{"unauthorized", func(msg *models.Message) { msg.From.ID = 99 }},
		{"missing sender", func(msg *models.Message) { msg.From = nil }},
		{"group", func(msg *models.Message) { msg.Chat.Type = models.ChatTypeSupergroup; msg.Chat.IsForum = true }},
		{"no thread", func(msg *models.Message) { msg.MessageThreadID = 0 }},
		{"no timestamp", func(msg *models.Message) { msg.Date = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Platform{allowFrom: "7"}
			created := topicCreated(10, 100, 55)
			tc.alter(created)
			if service, _ := p.routeTopicMessage(created); !service {
				t.Fatal("creation service event not consumed")
			}
			opener := topicMessage(11, 101, 0)
			p.routeTopicMessage(opener)
			if opener.MessageThreadID != 0 {
				t.Fatal("invalid creation affected routing")
			}
		})
	}
}

func TestPrivateTopicRouting_ExplicitIDsAndSendOrder(t *testing.T) {
	p := &Platform{}
	p.routeTopicMessage(topicCreated(10, 100, 55))
	for _, earlier := range []*models.Message{topicMessage(9, 100, 0), topicMessage(12, 99, 0)} {
		p.routeTopicMessage(earlier)
		if earlier.MessageThreadID != 0 {
			t.Fatal("earlier message adopted newer topic")
		}
	}
	opener := topicMessage(20, 101, 0)
	p.routeTopicMessage(opener)
	if opener.MessageThreadID != 55 {
		t.Fatal("earlier messages consumed pending opener")
	}
	explicit := topicMessage(21, 102, 88)
	p.routeTopicMessage(explicit)
	if explicit.MessageThreadID != 88 {
		t.Fatal("explicit thread ID changed")
	}
	p.routeTopicMessage(topicCreated(10, 100, 55)) // Replayed service event must not reopen fallback.
	lobby := topicMessage(22, 102, 0)
	p.routeTopicMessage(lobby)
	if lobby.MessageThreadID != 0 {
		t.Fatal("fallback continued after explicit thread")
	}
}

func TestPrivateTopicRouting_SplitOpenerUsesSendTime(t *testing.T) {
	var routing privateTopicRouting
	now := time.Now()
	routing.route(topicCreated(10, 100, 55), now)
	for i := 1; i <= 3; i++ {
		part := topicMessage(10+i, 100+4*i, 0)
		// Handler delay exceeds follow window; Telegram send gap remains four seconds.
		routing.route(part, now.Add(time.Duration(i)*time.Minute))
		if part.MessageThreadID != 55 {
			t.Fatalf("split part %d lost topic", i)
		}
	}
	later := topicMessage(14, 118, 0)
	routing.route(later, now.Add(4*time.Minute))
	if later.MessageThreadID != 0 {
		t.Fatal("stale topic hint captured later lobby message")
	}
}

func TestPrivateTopicRouting_AlbumRetainsExplicitTopic(t *testing.T) {
	p := &Platform{}
	p.routeTopicMessage(topicCreated(10, 100, 55))
	first := topicMessage(11, 101, 55)
	first.MediaGroupID = "album-a"
	p.routeTopicMessage(first)
	p.routeTopicMessage(topicCreated(12, 101, 66))
	oldAlbumPart := topicMessage(13, 102, 0)
	oldAlbumPart.MediaGroupID = "album-a"
	p.routeTopicMessage(oldAlbumPart)
	if oldAlbumPart.MessageThreadID != 55 {
		t.Fatal("old album part adopted the newly created topic")
	}
	secondAlbum := topicMessage(14, 102, 0)
	secondAlbum.MediaGroupID = "album-b"
	p.routeTopicMessage(secondAlbum)
	if secondAlbum.MessageThreadID != 66 {
		t.Fatal("second album did not adopt its new topic")
	}
	for i, tc := range []struct {
		album  string
		thread int
	}{{"album-a", 55}, {"album-b", 66}} {
		part := topicMessage(15+i, 102, 0)
		part.MediaGroupID = tc.album
		p.routeTopicMessage(part)
		if part.MessageThreadID != tc.thread {
			t.Fatalf("album %s routed to %d", tc.album, part.MessageThreadID)
		}
	}
	later := topicMessage(17, 108, 0)
	later.MediaGroupID = "album-a"
	p.routeTopicMessage(later)
	if later.MessageThreadID != 0 {
		t.Fatal("expired album hint was reused")
	}
}

func TestProcessUpdate_PrivateTopicCreationBeforeStartupCutoff(t *testing.T) {
	var handled []*core.Message
	p := &Platform{handler: func(_ core.Platform, msg *core.Message) { handled = append(handled, msg) }}
	date := int(core.StartTime.Unix()) - 3
	p.processUpdate(context.Background(), &models.Update{Message: topicCreated(10, date, 55)})
	p.processUpdate(context.Background(), &models.Update{Message: topicMessage(11, date+4, 0)})
	if len(handled) != 1 || handled[0].SessionKey != "telegram:100:55:7" {
		t.Fatal("old service event did not route the live opener")
	}
}

func TestProcessUpdate_PrivateTopicAmbiguousOpenersAreNotDispatched(t *testing.T) {
	var handled []*core.Message
	bot := newStubTelegramBot()
	p := &Platform{bot: bot,
		handler: func(_ core.Platform, msg *core.Message) { handled = append(handled, msg) },
	}
	date := int(time.Now().Unix())
	for _, msg := range []*models.Message{
		topicCreated(10, date, 55), topicCreated(11, date, 66),
		topicMessage(12, date+1, 0), topicMessage(13, date+2, 0),
	} {
		p.processUpdate(context.Background(), &models.Update{Message: msg})
	}
	if len(handled) != 0 {
		t.Fatal("ambiguous openers reached an agent session")
	}
	if bot.SendMessageCallCount() != 2 {
		t.Fatal("missing resend-in-topic notices")
	}
	p.processUpdate(context.Background(), &models.Update{Message: topicMessage(14, date+3, 66)})
	if len(handled) != 1 || handled[0].SessionKey != "telegram:100:66:7" {
		t.Fatal("explicit resend did not route to the intended topic")
	}
	p.processUpdate(context.Background(), &models.Update{Message: topicCreated(15, date+4, 77)})
	p.processUpdate(context.Background(), &models.Update{Message: topicMessage(16, date+4, 0)})
	if len(handled) != 2 || handled[1].SessionKey != "telegram:100:77:7" {
		t.Fatal("a later unambiguous topic failed to adopt its opener")
	}
}

func TestProcessUpdate_PrivateTopicAmbiguityExpiresAtSendTimeWindow(t *testing.T) {
	var handled []*core.Message
	bot := newStubTelegramBot()
	p := &Platform{bot: bot,
		handler: func(_ core.Platform, msg *core.Message) { handled = append(handled, msg) },
	}
	date := int(time.Now().Unix())
	for _, msg := range []*models.Message{
		topicCreated(10, date, 55), topicCreated(11, date+1, 66),
		topicMessage(12, date+2, 0), topicMessage(13, date+6, 0),
	} {
		p.processUpdate(context.Background(), &models.Update{Message: msg})
	}
	if len(handled) != 0 || bot.SendMessageCallCount() != 2 {
		t.Fatal("openers within the ambiguity window must request an explicit topic")
	}
	// Rejected prompts must not extend the window beyond the last creation.
	// These arrive immediately after the rejected prompts, so wall-clock expiry
	// cannot clear the ambiguity on their behalf.
	for i, sentAt := range []int{date + 7, date + 8, date + 60} {
		p.processUpdate(context.Background(), &models.Update{Message: topicMessage(14+i, sentAt, 0)})
		if len(handled) != i+1 || handled[i].SessionKey != "telegram:100:7" {
			t.Fatalf("unrelated later message %d did not reach the general-chat session", i)
		}
	}
	if bot.SendMessageCallCount() != 2 {
		t.Fatal("expired ambiguity kept sending resend notices")
	}
	p.processUpdate(context.Background(), &models.Update{Message: topicCreated(17, date+61, 77)})
	p.processUpdate(context.Background(), &models.Update{Message: topicMessage(18, date+62, 0)})
	if len(handled) != 4 || handled[3].SessionKey != "telegram:100:77:7" {
		t.Fatal("expired ambiguity prevented adopting a later topic")
	}
}

func TestPrivateTopicRouting_StateIsBoundedAndExpires(t *testing.T) {
	var routing privateTopicRouting
	now := time.Now()
	for i := 0; i <= privateTopicRouteLimit; i++ {
		msg := topicCreated(i+1, 100, i+1)
		msg.Chat.ID = int64(i + 1)
		routing.route(msg, now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(routing.routes) != privateTopicRouteLimit {
		t.Fatalf("stored %d routes", len(routing.routes))
	}
	if _, exists := routing.routes[privateTopicRouteKey{chatID: 1, userID: 7}]; exists {
		t.Fatal("oldest route not evicted")
	}
	later := topicMessage(privateTopicRouteLimit+2, 101, 0)
	later.Chat.ID = int64(privateTopicRouteLimit + 1)
	routing.route(later, now.Add(privateTopicRouteTTL+time.Second))
	if later.MessageThreadID != 0 || len(routing.routes) != 1 {
		t.Fatal("expired state retained")
	}
}

func TestPrivateTopicRouting_ConcurrentChats(t *testing.T) {
	p := &Platform{}
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for i := 1; i <= cap(errs); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			created, opener := topicCreated(1, 100, i), topicMessage(3, 101, 0)
			created.Chat.ID, opener.Chat.ID = int64(i), int64(i)
			p.routeTopicMessage(created)
			p.routeTopicMessage(opener)
			if opener.MessageThreadID != i {
				errs <- fmt.Sprintf("chat %d routed to %d", i, opener.MessageThreadID)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
