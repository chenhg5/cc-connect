package feishu

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestOnMessageAtAllWithoutBotMentionIgnored(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		if id == "om_card" {
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub, map[string]any{
		"respond_to_at_everyone_and_here": true,
	})
	p.botOpenID = "ou_bot"
	p.markThread("om_card")

	handled := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg.SessionKey
	}

	ev := rootTestEvent("om_quote", "om_card", "", "@_all 看一下这条")
	ev.Event.Message.Mentions = nil
	if err := p.onMessage(context.Background(), ev); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case key := <-handled:
		t.Fatalf("@all must not count as bot mention, got key %q", key)
	case <-time.After(150 * time.Millisecond):
	}
	if n := atomic.LoadInt32(&stub.calls); n != 0 {
		t.Fatalf("lookups = %d, want 0", n)
	}
}

func TestOnMessageMentionGateByChatShape(t *testing.T) {
	botMention := []*larkim.MentionEvent{{
		Id: &larkim.UserId{OpenId: stringPtr("ou_bot")},
	}}
	otherMention := []*larkim.MentionEvent{{
		Id: &larkim.UserId{OpenId: stringPtr("ou_other")},
	}}

	tests := []struct {
		name     string
		threadID string
		mentions []*larkim.MentionEvent
		markIDs  []string
		want     bool
		wantKey  string
	}{
		{
			name:     "large group quote of bot card without bot mention",
			mentions: otherMention,
			markIDs:  []string{"om_card"},
			want:     false,
		},
		{
			name:    "large group quote of bot card without any mention",
			markIDs: []string{"om_card"},
			want:    false,
		},
		{
			name:     "large group explicit bot mention",
			mentions: botMention,
			markIDs:  []string{"om_card"},
			want:     true,
			wantKey:  "feishu:oc_test:root:om_card",
		},
		{
			name:     "topic without mention",
			threadID: "omt_topic",
			markIDs:  []string{"omt_topic"},
			want:     true,
			wantKey:  "feishu:oc_test:root:om_card",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &rootStub{rootOf: func(id string) (string, bool) {
				if id == "om_card" {
					return "", true
				}
				return "", false
			}}
			p := newRootTestPlatform(t, stub)
			p.botOpenID = "ou_bot"
			for _, id := range tt.markIDs {
				p.markThread(id)
			}

			handled := make(chan string, 1)
			p.handler = func(_ core.Platform, msg *core.Message) {
				handled <- msg.SessionKey
			}

			ev := rootTestEvent("om_quote", "om_card", tt.threadID, "看一下这条")
			ev.Event.Message.Mentions = tt.mentions
			if err := p.onMessage(context.Background(), ev); err != nil {
				t.Fatalf("onMessage() error = %v", err)
			}

			if !tt.want {
				select {
				case key := <-handled:
					t.Fatalf("message must require @, got key %q", key)
				case <-time.After(150 * time.Millisecond):
				}
				if n := atomic.LoadInt32(&stub.calls); n != 0 {
					t.Fatalf("lookups = %d, want 0", n)
				}
				return
			}

			select {
			case key := <-handled:
				if key != tt.wantKey {
					t.Fatalf("session key = %q, want %q", key, tt.wantKey)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("message was not dispatched")
			}
			if n := atomic.LoadInt32(&stub.calls); n != 1 {
				t.Fatalf("lookups = %d, want 1", n)
			}
		})
	}
}
