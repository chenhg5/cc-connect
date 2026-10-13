package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// Exercise both CardKit entities and the inline fallback through the actual
// Feishu HTTP adapter. Every subsequent state must update the original message.
func TestQueryCard_ProgressApprovalFinalAndPagination(t *testing.T) {
	for _, entity := range []bool{true, false} {
		t.Run(fmt.Sprint(entity), func(t *testing.T) {
			var mu sync.Mutex
			var creates, updates int
			var latest string
			failUpdate := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				defer mu.Unlock()
				if r.Method == "POST" && strings.Contains(r.URL.Path, "cardkit") {
					if entity {
						fmt.Fprint(w, `{"code":0,"data":{"card_id":"card_query"}}`)
					} else {
						fmt.Fprint(w, `{"code":99991672,"msg":"no cardkit scope"}`)
					}
					return
				}
				if r.Method == "POST" {
					creates++
				} else {
					updates++
					if failUpdate {
						fmt.Fprint(w, `{"code":230011,"msg":"message cannot be updated"}`)
						return
					}
					if entity {
						latest = body["card"].(map[string]any)["data"].(string)
					} else {
						latest = body["content"].(string)
					}
				}
				fmt.Fprint(w, `{"code":0,"data":{"message_id":"om_query"}}`)
			}))
			t.Cleanup(server.Close)
			platform, err := New(map[string]any{"app_id": "cli_query_" + t.Name(), "app_secret": "test", "single_card_reply": true, "enable_feishu_card": true})
			if err != nil {
				t.Fatal(err)
			}
			p := platform.(*interactivePlatform)
			p.client = lark.NewClient(p.appID, "test", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()))
			p.userNameCache.Store("owner", "Owner")
			p.chatNameCache.Store("oc_private", "Private")
			ctx := context.Background()
			sc, err := createLegacyStreamingCard(p, ctx, replyContext{chatID: "oc_private", sessionKey: "feishu:oc_private:owner"})
			if err != nil {
				t.Fatal(err)
			}
			c := sc.(*queryCard)
			if err := c.Update(ctx, "Partial answer"); err != nil {
				t.Fatal(err)
			}
			prompt := core.NewCard().Title("Approve", "orange").Markdown("Run test").Build()
			prompt.Elements = append(prompt.Elements, core.CardActions{Buttons: []core.CardButton{{Text: "Allow", Value: "perm:allow", Extra: map[string]string{"query_card": "true", "permission_token": "request-one"}}}})
			if err := c.SetPrompt(ctx, prompt); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			shown := latest
			mu.Unlock()
			if !strings.Contains(shown, "Partial answer") || !strings.Contains(shown, "permission_token") {
				t.Fatalf("approval lost answer or controls: %s", shown)
			}
			received := make(chan *core.Message, 2)
			p.handler = func(_ core.Platform, m *core.Message) { received <- m }
			for _, action := range []string{"perm:allow", "askq:0:1"} {
				click := panelClick("om_query", action)
				click.Event.Action.Value["query_card"] = "true"
				click.Event.Action.Value["permission_token"] = "request-one"
				resp, err := p.onCardAction(click)
				if err != nil || (resp != nil && resp.Card != nil) {
					t.Fatalf("callback replaced query card: %v %#v", err, resp)
				}
				select {
				case m := <-received:
					if !m.IsPermissionResponse || m.PermissionToken != "request-one" {
						t.Fatalf("lost callback identity: %+v", m)
					}
				case <-time.After(time.Second):
					t.Fatal("callback was not dispatched")
				}
			}
			if err := c.SetNotice(ctx, "Approved"); err != nil {
				t.Fatal(err)
			}
			if err := c.SetPrompt(ctx, nil); err != nil {
				t.Fatal(err)
			}
			answer := "FIRST\n" + strings.Repeat("Long answer line\n", 1800) + "LAST"
			// Long output must remain readable before the turn has finished.
			if err := c.Update(ctx, answer); err != nil {
				t.Fatal(err)
			}
			if err := c.SetNotice(ctx, "Still working"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			runningFirstPage := latest
			mu.Unlock()
			if !strings.Contains(runningFirstPage, "FIRST") || !strings.Contains(runningFirstPage, "query_page:") {
				t.Fatal("long running card hid earlier output or omitted page controls")
			}
			pageCount := len(core.SplitMessageCodeFenceAware(answer, queryPageRunes))
			p.pageQueryCard(fmt.Sprintf("query_page:%s:%d", c.id, pageCount-1), "oc_private", "om_query")
			mu.Lock()
			runningLastPage := latest
			mu.Unlock()
			if !strings.Contains(runningLastPage, "LAST") {
				t.Fatal("cannot browse a running card")
			}
			p.pageQueryCard("query_page:"+c.id+":0", "oc_private", "om_query")
			answer += "\n" + strings.Repeat("More output\n", 1000) + "NEWEST"
			if err := c.Update(ctx, answer); err != nil {
				t.Fatal(err)
			}
			if err := c.SetPrompt(ctx, prompt); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			readingWhileApproval := latest
			mu.Unlock()
			if !strings.Contains(readingWhileApproval, "FIRST") || !strings.Contains(readingWhileApproval, "perm:allow") {
				t.Fatal("new output or approval replaced the page being read")
			}
			p.pageQueryCard("query_page:"+c.id+":latest", "oc_private", "om_query")
			mu.Lock()
			newestPage := latest
			mu.Unlock()
			if !strings.Contains(newestPage, "NEWEST") || !strings.Contains(newestPage, "perm:allow") {
				t.Fatal("latest-page navigation lost new output or approval controls")
			}
			if err := c.Finalize(ctx, answer); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			finalSelectedPage := latest
			mu.Unlock()
			if !strings.Contains(finalSelectedPage, "NEWEST") || strings.Contains(finalSelectedPage, "perm:allow") {
				t.Fatal("completion jumped away from the selected page or left approval controls")
			}
			p.pageQueryCard("query_page:"+c.id+":0", "oc_private", "om_query")
			mu.Lock()
			firstPage := latest
			mu.Unlock()
			if strings.Contains(firstPage, "perm:allow") || !strings.Contains(firstPage, "query_page:") || !strings.Contains(firstPage, "FIRST") {
				t.Fatalf("bad final page: %.300s", firstPage)
			}
			pageCount = len(core.SplitMessageCodeFenceAware(answer, queryPageRunes))
			p.pageQueryCard(fmt.Sprintf("query_page:%s:%d", c.id, pageCount-1), "oc_private", "om_query")
			mu.Lock()
			lastPage := latest
			before := updates
			mu.Unlock()
			if !strings.Contains(lastPage, "NEWEST") {
				t.Fatal("last page missing")
			}
			p.pageQueryCard("query_page:"+c.id+":0", "wrong-chat", "om_query")
			p.pageQueryCard("query_page:"+c.id+":0", "oc_private", "wrong-message")
			if err := c.Update(ctx, "late content"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if updates != before || creates != 1 {
				t.Errorf("new message or stale update: creates=%d updates=%d before=%d", creates, updates, before)
			}
			failUpdate = true
			mu.Unlock()
			p.pageQueryCard("query_page:"+c.id+":0", "oc_private", "om_query")
			if c.Failed() {
				t.Fatal("update error must not enable duplicate fallback messages")
			}
			mu.Lock()
			defer mu.Unlock()
			if creates != 1 {
				t.Fatalf("failure created replacement: %d", creates)
			}
		})
	}
}

func TestQueryCard_Disabled(t *testing.T) {
	p, _, _ := panelFixture(t)
	c, err := p.CreateStreamingCard(context.Background(), nil)
	if err != nil || c != nil || p.SingleQueryCardEnabled() {
		t.Fatal("legacy platform unexpectedly uses query cards")
	}
}

func TestQueryCard_RestartMissingPageShowsFeedback(t *testing.T) {
	p, _, _ := panelFixture(t)
	response, err := p.onCardAction(panelClick("om_old", "query_page:lost-on-restart:1"))
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Toast == nil || response.Toast.Content == "" {
		t.Fatal("expired pagination click was silently ignored")
	}
}

// Construct the pre-continuation layout to verify callbacks on already sent cards.
func createLegacyStreamingCard(p *interactivePlatform, ctx context.Context, rc replyContext) (core.StreamingCard, error) {
	return p.createQueryCard(ctx, rc, "", false)
}
