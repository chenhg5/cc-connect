package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestAnonymousCardRendersLocalizedCountAndStreamingAnswer(t *testing.T) {
	p := &interactivePlatform{}
	for _, lang := range []core.Language{core.LangEnglish, core.LangChinese, core.LangTraditionalChinese, core.LangJapanese, core.LangSpanish} {
		t.Run(string(lang), func(t *testing.T) {
			copy := core.NewI18n(lang)
			for _, status := range []core.CardStatus{core.CardStatusThinking, core.CardStatusWorking, core.CardStatusDone, core.CardStatusError} {
				answer := "Answer with `code` and /workspace/main.go"
				raw := p.BuildAnonymousRichCard(status, 2, answer, true, lang)
				var card map[string]any
				if err := json.Unmarshal([]byte(raw), &card); err != nil {
					t.Fatal(err)
				}
				if card["schema"] != "2.0" || !strings.Contains(raw, richCardMainTextElementID) || !strings.Contains(raw, answer) {
					t.Fatalf("invalid card or changed answer: %s", raw)
				}
				active := status == core.CardStatusThinking || status == core.CardStatusWorking
				if card["config"].(map[string]any)["streaming_mode"] != active {
					t.Fatalf("incorrect streaming mode: %s", raw)
				}
				if active && !strings.Contains(raw, copy.Tf(core.MsgAnonymousProgressTools, 2)) {
					t.Fatalf("missing localized count: %s", raw)
				}
				for _, forbidden := range []string{"collapsible_panel", "Reasoning", "workdir", "status_footer"} {
					if strings.Contains(raw, forbidden) {
						t.Fatalf("unwanted progress detail %q in %s", forbidden, raw)
					}
				}
			}
		})
	}
}

func TestAnonymousCardOnlyAdvertisedWhenInteractiveCardsEnabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		p, err := New(map[string]any{
			"app_id": "test-app", "app_secret": "test-secret", "enable_feishu_card": enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, supported := p.(core.AnonymousRichCardSupporter)
		if supported != enabled {
			t.Fatalf("anonymous capability = %v with enable_feishu_card=%v", supported, enabled)
		}
	}
}

func TestAnonymousCardUsesOneQuotedCardKitEntity(t *testing.T) {
	type request struct {
		method, path string
		body         map[string]any
	}
	var mu sync.Mutex
	var requests []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		body := map[string]any{}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &body); err != nil {
				t.Error(err)
			}
		}
		mu.Lock()
		requests = append(requests, request{r.Method, r.URL.Path, body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = io.WriteString(w, `{"code":0,"expire":7200,"tenant_access_token":"test-token"}`)
		case "/open-apis/cardkit/v1/cards":
			_, _ = io.WriteString(w, `{"code":0,"data":{"card_id":"test-card"}}`)
		case "/open-apis/im/v1/messages/trigger/reply":
			_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"answer"}}`)
		default:
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
		}
	}))
	defer srv.Close()
	client := lark.NewClient("test-app", "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
	p := &interactivePlatform{Platform: &Platform{platformName: "feishu", domain: srv.URL, appID: "test-app", appSecret: "test-secret", useInteractiveCard: true, client: client, replayClient: client}}
	ctx := context.Background()
	handle, err := p.SendPreviewStart(ctx, replyContext{messageID: "trigger", chatID: "chat", sessionKey: "feishu:chat:user"}, p.BuildAnonymousRichCard(core.CardStatusThinking, 0, "", true, core.LangEnglish))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateMessage(ctx, handle, p.BuildAnonymousRichCard(core.CardStatusWorking, 2, "", true, core.LangEnglish)); err != nil {
		t.Fatal(err)
	}
	if err := p.StreamRichCardText(ctx, handle, "Answer in progress"); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateMessage(ctx, handle, p.BuildAnonymousRichCard(core.CardStatusDone, 2, "Final answer", false, core.LangEnglish)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	captured := append([]request(nil), requests...)
	mu.Unlock()
	creates, replies, updates, streams := 0, 0, 0, 0
	lastSequence := float64(0)
	for _, req := range captured {
		switch {
		case req.path == "/open-apis/cardkit/v1/cards":
			creates++
			data, _ := req.body["data"].(string)
			if !strings.Contains(data, "Tool calls: 0") || strings.Contains(data, "collapsible_panel") {
				t.Fatalf("initial payload: %v", req.body)
			}
		case req.path == "/open-apis/im/v1/messages/trigger/reply":
			replies++
			content, _ := req.body["content"].(string)
			if !strings.Contains(content, "test-card") {
				t.Fatalf("reply did not reference card entity: %v", req.body)
			}
		case strings.HasPrefix(req.path, "/open-apis/cardkit/v1/cards/test-card"):
			if req.method != http.MethodPut {
				t.Fatalf("unexpected card method: %s", req.method)
			}
			sequence, _ := req.body["sequence"].(float64)
			if sequence <= lastSequence {
				t.Fatalf("non-monotonic sequence: %v", req.body)
			}
			lastSequence = sequence
			if strings.Contains(req.path, "/elements/"+richCardMainTextElementID+"/content") {
				streams++
				if req.body["content"] != "Answer in progress" {
					t.Fatalf("stream content: %v", req.body)
				}
			} else {
				updates++
			}
		case strings.Contains(req.path, "/im/v1/messages"):
			t.Fatalf("unexpected extra message: %s", req.path)
		}
	}
	if creates != 1 || replies != 1 || updates != 2 || streams != 1 {
		t.Fatalf("creates=%d replies=%d updates=%d streams=%d", creates, replies, updates, streams)
	}
}
