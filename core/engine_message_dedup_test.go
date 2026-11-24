package core

import (
	"testing"
	"time"
)

func TestEngineMessageIDDedupDropsRetransmission(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDedupConfig(true, time.Minute)
	msg := &Message{Platform: "test", SessionKey: "test:user", UserID: "user", MessageID: "delivery-1", Content: "/help", ReplyCtx: "ctx"}
	e.handleMessage(p, msg)
	first := len(p.getSent())
	if first == 0 {
		t.Fatal("first delivery produced no reply")
	}
	e.handleMessage(p, msg)
	if got := len(p.getSent()); got != first {
		t.Fatalf("retransmission produced %d replies; want %d", got, first)
	}
}

func TestEngineMessageIDDedupScopedToSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDedupConfig(true, time.Minute)
	for _, session := range []string{"test:user-a", "test:user-b"} {
		e.handleMessage(p, &Message{Platform: "test", SessionKey: session, UserID: session, MessageID: "same-id", Content: "/help", ReplyCtx: "ctx"})
	}
	if got := len(p.getSent()); got != 2 {
		t.Fatalf("different sessions produced %d replies; want 2", got)
	}
}

func TestEngineMessageIDDedupCanDisable(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDedupConfig(false, time.Minute)
	msg := &Message{Platform: "test", SessionKey: "test:user", UserID: "user", MessageID: "delivery-1", Content: "/help", ReplyCtx: "ctx"}
	e.handleMessage(p, msg)
	e.handleMessage(p, msg)
	if got := len(p.getSent()); got != 2 {
		t.Fatalf("disabled dedup produced %d replies; want 2", got)
	}
}

func TestEngineMessageIDDedupSkipsEmptyID(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDedupConfig(true, time.Minute)
	msg := &Message{Platform: "test", SessionKey: "test:user", UserID: "user", Content: "/help", ReplyCtx: "ctx"}
	e.handleMessage(p, msg)
	e.handleMessage(p, msg)
	if got := len(p.getSent()); got != 2 {
		t.Fatalf("empty ID produced %d replies; want 2", got)
	}
}
