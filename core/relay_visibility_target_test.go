package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type relayVisibilityTargetStub struct {
	stubPlatformEngine
	key string
	ok  bool
}

func (p *relayVisibilityTargetStub) RelayGroupVisibilityKey(callerSessionKey string) (string, bool) {
	return p.key, p.ok
}

func TestResolveGroupVisibilityKeyUsesPlatformThreadTarget(t *testing.T) {
	rm := NewRelayManager("")
	platform := &relayVisibilityTargetStub{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		key:                "feishu:oc_chat:root:om_root",
		ok:                 true,
	}
	engine := NewEngine("source", &stubAgent{}, []Platform{platform}, "", LangEnglish)

	got := rm.resolveGroupVisibilityKey("feishu", "oc_chat", "feishu:oc_chat:root:om_root", engine)
	if got != "feishu:oc_chat:root:om_root" {
		t.Fatalf("resolveGroupVisibilityKey() = %q, want thread-scoped key", got)
	}
}

func TestResolveGroupVisibilityKeyFallsBackForUnsupportedPlatform(t *testing.T) {
	rm := NewRelayManager("")
	platform := &relayVisibilityTargetStub{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		ok:                 false,
	}
	engine := NewEngine("source", &stubAgent{}, []Platform{platform}, "", LangEnglish)

	got := rm.resolveGroupVisibilityKey("feishu", "oc_chat", "feishu:oc_chat:root:om_root", engine)
	if got != "feishu:oc_chat:relay" {
		t.Fatalf("resolveGroupVisibilityKey() = %q, want legacy fallback", got)
	}
}

type relayVisibilitySenderStub struct {
	stubPlatformEngine
	sessionKey string
	sendErr    error
}

func (p *relayVisibilitySenderStub) SendRelayGroupVisibility(_ context.Context, sessionKey, content string) error {
	p.mu.Lock()
	p.sessionKey = sessionKey
	p.sent = append(p.sent, content)
	p.mu.Unlock()
	return p.sendErr
}

func TestSendToGroupWarnsWhenPlatformVisibilitySendFails(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	rm := NewRelayManager("")
	platform := &relayVisibilitySenderStub{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		sendErr:            errors.New("topic reply unavailable"),
	}
	engine := NewEngine("source", &stubAgent{}, []Platform{platform}, "", LangEnglish)

	rm.sendToGroup(context.Background(), engine, "feishu", "feishu:oc_chat:root:om_root", "relay visible")

	logOutput := buf.String()
	for _, want := range []string{
		"relay: failed to send platform group visibility message",
		`"platform":"feishu"`,
		`"session_key":"feishu:oc_chat:root:om_root"`,
		"topic reply unavailable",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("warning log %q does not contain %q", logOutput, want)
		}
	}
}

func TestSendToGroupPrefersPlatformVisibilitySender(t *testing.T) {
	rm := NewRelayManager("")
	platform := &relayVisibilitySenderStub{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	engine := NewEngine("source", &stubAgent{}, []Platform{platform}, "", LangEnglish)

	rm.sendToGroup(context.Background(), engine, "feishu", "feishu:oc_chat:root:om_root", "relay visible")

	if platform.sessionKey != "feishu:oc_chat:root:om_root" {
		t.Fatalf("visibility session key = %q, want topic-scoped key", platform.sessionKey)
	}
	if got := platform.getSent(); len(got) != 1 || got[0] != "relay visible" {
		t.Fatalf("visibility send = %#v", got)
	}
}
