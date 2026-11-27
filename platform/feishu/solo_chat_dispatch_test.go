package feishu

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestSoloChatDispatch_UnmentionedGate(t *testing.T) {
	for _, name := range []string{"option off", "unknown bot", "mentions others", "not solo"} {
		t.Run(name, func(t *testing.T) {
			messages := make(chan *core.Message, 1)
			p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
			p.soloChatReplyAll = name != "option off"
			if name == "unknown bot" {
				p.botOpenID = ""
				p.groupFilterDegraded = true
			}
			calls := 0
			p.soloChatFetch = func(context.Context, string) (*larkim.GetChatResp, error) {
				calls++
				return soloLookupResponse(t, `{"code":0,"data":{"user_count":"2","bot_count":"1"}}`), nil
			}
			event := makeGroupHistoryEvent(t, "om_gate", "ou_alice", "text", `{"text":"hello"}`, "oc_main", "", false)
			if name == "mentions others" {
				event.Event.Message.Mentions = []*larkim.MentionEvent{{Key: stringPtr("@_other"), Id: &larkim.UserId{OpenId: stringPtr("ou_other")}}}
			}
			if err := p.onMessage(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			assertNoGroupHistoryMessage(t, messages)
			wantCalls := 0
			if name == "not solo" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("lookups=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestSoloChatDispatch_AdmittedTopicAndReplyAreTriggers(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(map[bool]string{false: "topic", true: "reply"}[reply], func(t *testing.T) {
			messages := make(chan *core.Message, 1)
			p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
			p.soloChatReplyAll = true
			p.soloChatFetch = func(context.Context, string) (*larkim.GetChatResp, error) {
				return soloLookupResponse(t, `{"code":0,"data":{"user_count":"1","bot_count":"1"}}`), nil
			}
			event := makeGroupHistoryEvent(t, "om_solo", "ou_alice", "text", `{"text":"hello"}`, "oc_main", "", false)
			if reply {
				p.threadIsolation = true
				event.Event.Message.RootId = stringPtr("om_root")
				event.Event.Message.ThreadId = stringPtr("omt_thread")
				event.Event.Message.ParentId = stringPtr("om_parent")
				// An already engaged isolated thread needs no quoted-message
				// bootstrap. Keep this test entirely off the SDK/HTTP path.
				key := p.makeSessionKey(event.Event.Message, "oc_main", "ou_alice")
				if !p.markThreadSessionActive(key) {
					t.Fatal("reply fixture did not activate an isolated thread")
				}
			}
			if err := p.onMessage(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			got := awaitGroupHistoryMessage(t, messages)
			if got.Content == "" {
				t.Fatal("admitted message lost content")
			}
			history := p.snapshotGroupHistory(p.groupHistoryScope(event.Event.Message, "oc_main"))
			if len(history.entries) != 0 {
				t.Fatal("admitted solo message was recorded as overheard history")
			}
		})
	}
}

func TestSoloChatDispatch_MentionWaitStagesBeforeSolo(t *testing.T) {
	for _, mode := range []string{"active", "expired", "missing"} {
		t.Run(mode, func(t *testing.T) {
			messages := make(chan *core.Message, 2)
			p := newGroupHistoryTestPlatform(func(_ core.Platform, msg *core.Message) { messages <- msg })
			p.soloChatReplyAll = true
			p.threadIsolation = true
			p.attachmentWait = attachmentWaitMention
			p.attachmentWaitTTL = time.Hour
			t.Cleanup(p.dropAllStagedAttachments)
			calls := 0
			p.soloChatFetch = func(context.Context, string) (*larkim.GetChatResp, error) {
				calls++
				return soloLookupResponse(t, `{"code":0,"data":{"user_count":"1","bot_count":"1"}}`), nil
			}
			file := makeGroupHistoryEvent(t, "om_staged", "ou_alice", "file",
				`{"file_key":"fixture_file","file_name":"note.txt"}`, "oc_main", "", false)
			file.Event.Message.RootId = stringPtr("om_root")
			file.Event.Message.ThreadId = stringPtr("omt_thread")
			key := p.makeSessionKey(file.Event.Message, "oc_main", "ou_alice")
			last := time.Now().Add(-59 * time.Minute)
			switch mode {
			case "active":
				p.activeThreadSessions.Store(key, last)
			case "expired":
				p.activeThreadSessions.Store(key, time.Now().Add(-2*time.Hour))
			}
			if err := p.onMessage(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			assertNoGroupHistoryMessage(t, messages)
			if calls != 0 {
				t.Fatalf("attachment used solo membership lookup: %d", calls)
			}
			if mode != "active" {
				if got := p.takeStagedAttachments(key, "ou_alice"); len(got) != 0 {
					t.Fatalf("inactive attachment staged: %#v", got)
				}
				return
			}
			plain := makeGroupHistoryEvent(t, "om_solo_plain", "ou_alice", "text",
				`{"text":"plain instruction"}`, "oc_main", "", false)
			plain.Event.Message.RootId = stringPtr("om_root")
			plain.Event.Message.ThreadId = stringPtr("omt_thread")
			if err := p.onMessage(context.Background(), plain); err != nil {
				t.Fatal(err)
			}
			if got := awaitGroupHistoryMessage(t, messages); got.Content != "plain instruction" {
				t.Fatalf("unexpected solo dispatch: %q", got.Content)
			}
			assertNoGroupHistoryMessage(t, messages)
			if stamp, ok := p.activeThreadSessions.Load(key); !ok || stamp != last {
				t.Fatalf("solo admission changed the attachment activity timestamp: %v", stamp)
			}
			if got := p.takeStagedAttachments(key, "ou_other"); len(got) != 0 {
				t.Fatalf("different sender found staged attachment: %#v", got)
			}
			if got := p.takeStagedAttachments(key, "ou_alice"); len(got) != 1 || got[0].messageID != "om_staged" {
				t.Fatalf("solo admission consumed or duplicated the staged file: %#v", got)
			}
		})
	}
}

func TestSoloChatDispatch_QuoteClaimDoesNotActivateAttachmentWait(t *testing.T) {
	p := &Platform{threadIsolation: true, attachmentWait: attachmentWaitMention}
	key := "feishu:oc_main:root:om_root"
	claims := make(chan bool, 2)
	for range 2 {
		go func() { claims <- p.claimThreadQuoteBootstrap(key) }()
	}
	if first, second := <-claims, <-claims; first == second {
		t.Fatalf("want exactly one initial quote claim; got %t and %t", first, second)
	}
	if p.isActiveThreadSession(key) {
		t.Fatal("quote-only state granted attachment eligibility")
	}
	p.attachmentWait = attachmentWaitOff
	if p.isActiveThreadSession(key) {
		t.Fatal("quote-only state granted eligibility after mode change")
	}
	p.attachmentWait = attachmentWaitMention
	if p.markThreadSessionActive(key) || !p.isActiveThreadSession(key) {
		t.Fatal("legacy activation did not promote quote-only state without duplicate bootstrap")
	}
}
