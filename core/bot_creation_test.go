package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type botCreationTestPlatform struct{ *stubPlatformEngine }

func (p *botCreationTestPlatform) IsPrivateUserMessage(msg *Message) bool {
	return msg.ReplyCtx == "private-user"
}

func TestBotCreation_OwnershipGateConsumesSecretBeforeAgent(t *testing.T) {
	platform := &botCreationTestPlatform{&stubPlatformEngine{n: "test"}}
	agent := &cujAgent{}
	e := NewEngine("child", agent, []Platform{platform}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { e.Stop() })
	token := "secret-for-child"
	hash := sha256.Sum256([]byte(token))
	saved := ""
	e.SetBotOwnership(hex.EncodeToString(hash[:]), "", func(userID string) error { saved = userID; return nil })
	send := func(user, content, ctx string) {
		e.ReceiveMessage(platform, &Message{SessionKey: "test:" + user, UserID: user, Content: content, ReplyCtx: ctx})
	}
	send("stranger", "run arbitrary code", "private-user")
	send("owner", "/new claim "+token, "group-user")
	send("owner", "/new claim wrong", "private-user")
	if saved != "" {
		t.Fatal("invalid or group claim saved owner")
	}
	send("ou_this_app_owner", "/new claim "+token, "private-user")
	if saved != "ou_this_app_owner" || !e.isAdmin(saved) {
		t.Fatal("did not bind new-app owner")
	}
	send("stranger", "run arbitrary code", "private-user")
	for _, out := range platform.getSent() {
		if strings.Contains(out, token) {
			t.Fatal("echoed claim secret")
		}
	}
	if len(agent.sessions) != 0 {
		t.Fatal("ownership requests reached model")
	}
	if !strings.Contains(strings.Join(platform.getSent(), "\n"), e.i18n.T(MsgBotClaimed)) {
		t.Fatal("user did not see successful claim")
	}
}

func TestBotCreation_TranslationsComplete(t *testing.T) {
	for key, entries := range messages {
		if !strings.HasPrefix(string(key), "bot_") {
			continue
		}
		for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
			if entries[lang] == "" {
				t.Errorf("missing %s %s", key, lang)
			}
		}
	}
}

func TestBotCreation_ValidNames(t *testing.T) {
	for _, name := range []string{"../oops", "", "review/dev", "cancel", "Developer", strings.Repeat("a", 49)} {
		if ValidBotName(name) {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"developer", "qa-1", "code_review"} {
		if !ValidBotName(name) {
			t.Errorf("rejected %q", name)
		}
	}
}

// Helpers also used by the CUJ: the external provisioning service is the only
// mocked boundary; command routing, admin checks and cancellation are real.
func newBotCreationCUJ(t *testing.T) (*Engine, *botCreationTestPlatform, *atomic.Int32, chan struct{}) {
	t.Helper()
	p := &botCreationTestPlatform{&stubPlatformEngine{n: "test"}}
	e := NewEngine("parent", &cujAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	e.SetAdminFrom("owner")
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	calls := &atomic.Int32{}
	finished := make(chan struct{}, 2)
	e.SetBotCreationFunc(func(ctx context.Context, _ Platform, user, name string, notify func(MsgKey, ...any)) {
		calls.Add(1)
		notify(MsgBotAuthorize, name, "https://example.invalid/register")
		<-ctx.Done()
		finished <- struct{}{}
	})
	t.Cleanup(func() { e.Stop() })
	return e, p, calls, finished
}

func awaitBotText(t *testing.T, p *botCreationTestPlatform, text string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing %q in %v", text, p.getSent())
}
