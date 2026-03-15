package telegram

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMenuPageToKeyboard_RowsAndData(t *testing.T) {
	page := &core.MenuPage{
		Title: "Test Menu",
		Buttons: [][]core.ButtonOption{
			{{Text: "A", Data: "menu:cat:session"}, {Text: "B", Data: "menu:cat:ai"}},
			{{Text: "Back", Data: "menu:main"}},
		},
	}
	kb := menuPageToKeyboard(page)
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(kb.InlineKeyboard))
	}
	if len(kb.InlineKeyboard[0]) != 2 {
		t.Errorf("expected 2 buttons in row 0, got %d", len(kb.InlineKeyboard[0]))
	}
	btn := kb.InlineKeyboard[0][0]
	if btn.Text != "A" {
		t.Errorf("expected Text=A, got %q", btn.Text)
	}
	if btn.CallbackData == nil || *btn.CallbackData != "menu:cat:session" {
		t.Errorf("expected CallbackData=menu:cat:session")
	}
}

func TestMenuPageToKeyboard_TruncatesLongData(t *testing.T) {
	longData := "menu:sel:session:" + strings.Repeat("x", 50) // > 64 bytes
	page := &core.MenuPage{
		Buttons: [][]core.ButtonOption{{{Text: "X", Data: longData}}},
	}
	kb := menuPageToKeyboard(page)
	data := *kb.InlineKeyboard[0][0].CallbackData
	if len(data) > 64 {
		t.Errorf("callback data should be truncated to 64 bytes, got %d", len(data))
	}
}

func TestMenuMessageText_WithSubtitle(t *testing.T) {
	page := &core.MenuPage{Title: "<b>Title</b>", Subtitle: "sub & info"}
	text := menuMessageText(page)
	if !strings.Contains(text, "<b>Title</b>") {
		t.Error("title should be preserved")
	}
	if !strings.Contains(text, "&amp;") {
		t.Error("subtitle & should be escaped")
	}
}

// --- handleMenuCallback routing tests ---

func TestHandleMenuCallback_NilHandler(t *testing.T) {
	p := &Platform{} // menuHandler is nil
	// Should not panic
	p.handleMenuCallback("menu:cat:ai", 123, 0, "u1", "user", "chat", "sk")
}

func TestHandleMenuCallback_Noop(t *testing.T) {
	var called bool
	p := &Platform{}
	p.menuHandler = func(action, sessionKey string) *core.MenuPage {
		called = true
		return nil // noop: no SendMenuPage call
	}
	p.handleMenuCallback("menu:noop", 123, 0, "u1", "user", "chat", "sk")
	if !called {
		t.Error("expected menuHandler to be called for menu:noop")
	}
	// No panic = SendMenuPage was not called (nil page triggers early return)
}

func TestHandleMenuCallback_Exec_DispatchesCommand(t *testing.T) {
	var got *core.Message
	p := &Platform{}
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}
	// menuHandler returns nil for refreshMenuPage (noop after exec)
	p.menuHandler = func(action, _ string) *core.MenuPage { return nil }
	p.handleMenuCallback("menu:exec:stop", 123, 456, "u1", "user", "chat", "sk")
	if got == nil {
		t.Fatal("expected handler to be called with synthetic message")
	}
	if got.Content != "/stop" {
		t.Errorf("expected Content=/stop, got %q", got.Content)
	}
	if got.SessionKey != "sk" {
		t.Errorf("expected SessionKey=sk, got %q", got.SessionKey)
	}
}
