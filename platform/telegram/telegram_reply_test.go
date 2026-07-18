package telegram

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestEnrichReplyContent_EmptyWhenNoReply(t *testing.T) {
	got := enrichReplyContent(&models.Message{Text: "hello"})
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestEnrichReplyContent_FullTextFallback(t *testing.T) {
	got := enrichReplyContent(&models.Message{
		Text: "ok",
		ReplyToMessage: &models.Message{
			Text: "secretary said this",
			From: &models.User{Username: "SecretaryBot", FirstName: "Sec"},
		},
	})
	want := "[Reply to @SecretaryBot]: secretary said this"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEnrichReplyContent_PrefersQuoteOverFullText(t *testing.T) {
	got := enrichReplyContent(&models.Message{
		Text: "fix that",
		Quote: &models.TextQuote{
			Text:     "just this clause",
			Position: 12,
			IsManual: true,
		},
		ReplyToMessage: &models.Message{
			Text: "long message with just this clause buried inside",
			From: &models.User{Username: "SecretaryBot", FirstName: "Sec"},
		},
	})
	if !strings.Contains(got, "just this clause") {
		t.Fatalf("got %q, want quote fragment", got)
	}
	if strings.Contains(got, "long message") {
		t.Fatalf("got %q, full body should not appear when quote is set", got)
	}
	if !strings.HasPrefix(got, "[Reply to @SecretaryBot]:") {
		t.Fatalf("got %q, want Reply to prefix", got)
	}
}

func TestEnrichReplyContent_StickerMarker(t *testing.T) {
	got := enrichReplyContent(&models.Message{
		Text: "lol",
		ReplyToMessage: &models.Message{
			Sticker: &models.Sticker{Emoji: "😀"},
			From:    &models.User{Username: "alice", FirstName: "Alice"},
		},
	})
	want := "[Reply to @alice]: [Sticker]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEnrichReplyContent_QuotePlusImage(t *testing.T) {
	got := enrichReplyContent(&models.Message{
		Text:  "what is this",
		Quote: &models.TextQuote{Text: "caption bit"},
		ReplyToMessage: &models.Message{
			Caption: "full caption with caption bit here",
			Photo:   []models.PhotoSize{{FileID: "x"}},
			From:    &models.User{FirstName: "Bob"},
		},
	})
	if !strings.Contains(got, "caption bit") {
		t.Fatalf("got %q, want quote text", got)
	}
	if !strings.Contains(got, "[Image]") {
		t.Fatalf("got %q, want [Image] marker", got)
	}
	if strings.Contains(got, "full caption") {
		t.Fatalf("got %q, full caption should not appear when quote is set", got)
	}
}
