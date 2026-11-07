package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestSendTrackedCard_UpdatesWithoutButtonCallback(t *testing.T) {
	for _, scenario := range []string{"chat", "thread", "missing-id"} {
		t.Run(scenario, func(t *testing.T) {
			var sent atomic.Int32
			patches := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(r.URL.Path, "/tenant_access_token/"):
					writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "tracked-card-token"})
				case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages"):
					sent.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if scenario == "thread" {
						if !strings.HasSuffix(r.URL.Path, "/om_user/reply") || body["reply_in_thread"] != true {
							t.Errorf("tracked card lost its thread: %s %#v", r.URL.Path, body)
						}
					} else if r.URL.Path != "/open-apis/im/v1/messages" {
						t.Errorf("unexpected tracked card route: %s", r.URL.Path)
					}
					id := "om_choice"
					if scenario == "missing-id" {
						id = ""
					}
					writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": id}})
				case r.Method == http.MethodPatch && r.URL.Path == "/open-apis/im/v1/messages/om_choice":
					var body struct {
						Content string `json:"content"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					patches <- body.Content
					writeJSON(t, w, map[string]any{"code": 0})
				default:
					t.Errorf("unexpected tracked card request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			platform, err := New(map[string]any{"app_id": "cli_tracked_" + scenario, "app_secret": "test", "thread_isolation": true})
			if err != nil {
				t.Fatal(err)
			}
			p := platform.(*interactivePlatform)
			p.domain = srv.URL
			p.client = lark.NewClient(p.appID, p.appSecret, lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
			key := "feishu:oc_chat:ou_author"
			if scenario == "thread" {
				key = "feishu:oc_chat:root:om_thread"
			}
			initial := core.NewCard().Markdown("queued task").Buttons(core.PrimaryBtn("Add now", "cmd:/ps --queued one")).Build()
			receipt, err := p.SendTrackedCard(context.Background(), replyContext{messageID: "om_user", chatID: "oc_chat", sessionKey: key}, initial)
			if err != nil || sent.Load() != 1 {
				t.Fatalf("tracked send failed or duplicated: err=%v sends=%d", err, sent.Load())
			}
			if scenario == "missing-id" {
				if receipt != nil {
					t.Fatal("missing bot message ID returned an unsafe update context")
				}
				return
			}
			p.cardActionMsgIDs = map[string]string{key: "om_unrelated"}
			resolved := core.NewCard().Title("Processing queued message", "green").Markdown("queued task").Build()
			updated, err := p.UpdateActionCard(context.Background(), receipt, resolved)
			if !updated || err != nil {
				t.Fatalf("unclicked card update failed: updated=%v err=%v", updated, err)
			}
			select {
			case content := <-patches:
				if !strings.Contains(content, "Processing queued message") || !strings.Contains(content, "queued task") || strings.Contains(content, `"tag":"button"`) {
					t.Fatalf("automatic update retained its button or lost the message: %s", content)
				}
			default:
				t.Fatal("card was not updated without a click")
			}
		})
	}
}

func TestUpdateActionCard_ConcurrentChoicesPatchTheirOwnMessage(t *testing.T) {
	type patch struct {
		path    string
		content string
	}
	patches := make(chan patch, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "test-action-token"})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			patches <- patch{path: r.URL.Path, content: body.Content}
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	platform, err := New(map[string]any{"app_id": "cli_action_update", "app_secret": "test"})
	if err != nil {
		t.Fatal(err)
	}
	p := platform.(*interactivePlatform)
	p.domain = srv.URL
	p.client = lark.NewClient(p.appID, p.appSecret, lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
	const key = "feishu:oc_chat:ou_author"
	// Another callback in the same session must not redirect either update.
	p.cardActionMsgIDs = map[string]string{key: "om_unrelated_navigation"}
	card := core.NewCard().Title("Kept queued", "blue").Markdown("Original message").Build()
	for _, ctx := range []any{
		nil,
		replyContext{messageID: "om_original_user_message", sessionKey: key},
		replyContext{cardAction: true, sessionKey: key},
	} {
		updated, err := p.UpdateActionCard(context.Background(), ctx, card)
		if updated || err != nil {
			t.Fatalf("ordinary or incomplete context must not be patched: updated=%v err=%v", updated, err)
		}
	}
	var wg sync.WaitGroup
	for _, id := range []string{"om_choice_one", "om_choice_two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := replyContext{messageID: id, sessionKey: key, chatID: "oc_chat", cardAction: true}
			status := core.NewCard().Title("Choice handled", "green").Markdown(id).Build()
			updated, err := p.UpdateActionCard(context.Background(), ctx, status)
			if !updated || err != nil {
				t.Errorf("update %s: updated=%v err=%v", id, updated, err)
			}
		}()
	}
	wg.Wait()
	close(patches)
	seen := make(map[string]bool)
	for update := range patches {
		id := strings.TrimPrefix(update.path, "/open-apis/im/v1/messages/")
		if !strings.Contains(update.content, id) || strings.Contains(update.content, `"tag":"button"`) || strings.Contains(update.content, `"tag":"action"`) {
			t.Errorf("card update lost its message or still has buttons: %s", update.content)
		}
		seen[id] = true
	}
	if len(seen) != 2 || !seen["om_choice_one"] || !seen["om_choice_two"] {
		t.Fatalf("updates patched the wrong cards: %v", seen)
	}
}
