package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type splitCardFixture struct {
	requests               []map[string]any
	files                  map[string]string
	mu                     sync.Mutex
	entities, messages     map[string]string
	sequences              map[string]int
	creates                int
	failUpdate, failCreate bool
	failCreateAt           int
	updates                int
	beforeUpdate           func()
}

func newSplitCardFixture(t *testing.T, entity bool, extra ...map[string]any) (*interactivePlatform, *splitCardFixture) {
	t.Helper()
	f := &splitCardFixture{entities: map[string]string{}, messages: map[string]string{}, sequences: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/files") && r.Method == "POST" {
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Error(err)
				return
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			f.mu.Lock()
			if f.files == nil {
				f.files = map[string]string{}
			}
			f.files[header.Filename] = string(data)
			f.mu.Unlock()
			fmt.Fprint(w, `{"code":0,"data":{"file_key":"file_complete"}}`)
			return
		}
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/messages/") {
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"chat_id":"oc_group","message_position":"42"}]}}`)
			return
		}
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/messages/") {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.failUpdate {
				fmt.Fprint(w, `{"code":230011,"msg":"message cannot be deleted"}`)
				return
			}
			delete(f.messages, path.Base(r.URL.Path))
			fmt.Fprint(w, `{"code":0}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != "POST" && f.beforeUpdate != nil {
			f.beforeUpdate()
		}
		if strings.Contains(r.URL.Path, "cardkit") {
			if r.Method == "POST" {
				if !entity {
					fmt.Fprint(w, `{"code":99991672,"msg":"no cardkit scope"}`)
					return
				}
				id := fmt.Sprintf("card_%d", len(f.entities)+1)
				f.entities[id] = body["data"].(string)
				fmt.Fprintf(w, `{"code":0,"data":{"card_id":%q}}`, id)
			} else {
				if f.failUpdate {
					fmt.Fprint(w, `{"code":230011,"msg":"message cannot be updated"}`)
					return
				}
				id := path.Base(r.URL.Path)
				sequence := int(body["sequence"].(float64))
				if sequence <= f.sequences[id] {
					fmt.Fprint(w, `{"code":300309,"msg":"sequence must increase"}`)
					return
				}
				f.sequences[id] = sequence
				f.entities[id] = body["card"].(map[string]any)["data"].(string)
				f.updates++
				fmt.Fprint(w, `{"code":0}`)
			}
			return
		}
		if r.Method == "POST" {
			f.creates++
			captured := map[string]any{"path": r.URL.Path}
			for k, v := range body {
				captured[k] = v
			}
			f.requests = append(f.requests, captured)
			if f.failCreate || f.creates == f.failCreateAt {
				fmt.Fprint(w, `{"code":230011,"msg":"cannot create message"}`)
				return
			}
			id := fmt.Sprintf("om_%d", f.creates)
			f.messages[id] = body["content"].(string)
			fmt.Fprintf(w, `{"code":0,"data":{"message_id":%q}}`, id)
		} else {
			if f.failUpdate {
				fmt.Fprint(w, `{"code":230011,"msg":"message cannot be updated"}`)
				return
			}
			f.messages[path.Base(r.URL.Path)] = body["content"].(string)
			f.updates++
			fmt.Fprint(w, `{"code":0}`)
		}
	}))
	t.Cleanup(server.Close)
	opts := map[string]any{"app_id": "cli_split_" + t.Name(), "app_secret": "test", "separate_final_card": true, "enable_feishu_card": true}
	for _, fields := range extra {
		for key, value := range fields {
			opts[key] = value
		}
	}
	platform, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	p := platform.(*interactivePlatform)
	p.client = lark.NewClient(p.appID, "test", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()))
	p.SetCardLanguageProvider(func() core.Language { return core.LangChinese })
	return p, f
}

func (f *splitCardFixture) shown(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	text := f.messages[id]
	var ref struct {
		Type string `json:"type"`
		Data struct {
			CardID string `json:"card_id"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(text), &ref) == nil && ref.Type == "card" {
		return f.entities[ref.Data.CardID]
	}
	return text
}

func TestQueryCard_SeparateAnswerAndIndependentPagination(t *testing.T) {
	for _, entity := range []bool{true, false} {
		t.Run(fmt.Sprint(entity), func(t *testing.T) {
			p, f := newSplitCardFixture(t, entity)
			ctx := context.Background()
			sc, err := createLegacyStreamingCard(p, ctx, replyContext{chatID: "oc_test", sessionKey: "feishu:oc_test:owner"})
			if err != nil {
				t.Fatal(err)
			}
			c := sc.(*queryCard)
			if !c.SeparateFinalAnswer() || !p.SingleQueryCardEnabled() {
				t.Fatal("split mode did not enable aggregate process card")
			}
			process := "PROCESS_FIRST\n" + strings.Repeat("process line\n", 500) + "PROCESS_LAST"
			answer := "ANSWER_FIRST\n" + strings.Repeat("answer line\n", 500) + "ANSWER_LAST"
			if err := c.Update(ctx, process); err != nil {
				t.Fatal(err)
			}
			if err := c.SetPrompt(ctx, core.NewCard().Title("Approve", "orange").Markdown("APPROVAL_PROMPT").Build()); err != nil {
				t.Fatal(err)
			}
			p.pageQueryCard("query_page:"+c.id+":latest", "oc_test", "om_1")
			if err := c.FinalizeWithAnswer(ctx, process, answer); err != nil {
				t.Fatal(err)
			}
			first, second := f.shown("om_1"), f.shown("om_2")
			if !strings.Contains(first, "执行进度") || !strings.Contains(first, "PROCESS_LAST") || strings.Contains(first, "ANSWER_") || strings.Contains(first, "APPROVAL_PROMPT") {
				t.Fatalf("bad process card: %s", first)
			}
			if !strings.Contains(second, "结论") || !strings.Contains(second, "ANSWER_FIRST") || strings.Contains(second, "PROCESS_") {
				t.Fatalf("answer did not start on its first page: %s", second)
			}
			p.queryCardsMu.Lock()
			var a *queryCard
			for _, card := range p.queryCards {
				if card.answer {
					a = card
				}
			}
			p.queryCardsMu.Unlock()
			if a == nil {
				t.Fatal("answer pagination not registered")
			}
			p.pageQueryCard("query_page:"+a.id+":latest", "oc_test", "om_2")
			if !strings.Contains(f.shown("om_2"), "ANSWER_LAST") || f.shown("om_1") != first {
				t.Fatal("answer pagination changed process card")
			}
			p.pageQueryCard("query_page:"+c.id+":0", "oc_test", "om_1")
			if !strings.Contains(f.shown("om_1"), "PROCESS_FIRST") || !strings.Contains(f.shown("om_2"), "ANSWER_LAST") {
				t.Fatal("process pagination changed answer card")
			}
			if err := c.FinalizeWithAnswer(ctx, "wrong", "duplicate"); err != nil {
				t.Fatal(err)
			}
			if err := c.Update(ctx, "late update"); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			creates := f.creates
			f.mu.Unlock()
			if creates != 2 || strings.Contains(f.shown("om_1"), "late update") {
				t.Fatal("duplicate completion or late update modified finished cards")
			}
		})
	}
}

func TestQueryCard_SeparateAnswerFailuresAndSilentCompletion(t *testing.T) {
	for _, mode := range []string{"process_update_failed", "answer_create_failed", "silent", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			p, f := newSplitCardFixture(t, false)
			ctx := context.Background()
			sc, err := p.CreateStreamingCard(ctx, replyContext{chatID: "oc_test"})
			if err != nil {
				t.Fatal(err)
			}
			c := sc.(*queryCardStream)
			answer := "CONCLUSION"
			if mode == "stopped" {
				if err := c.Finish(ctx, "STOPPED", core.CardStatusDone); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "silent" {
				answer = ""
			}
			f.mu.Lock()
			f.failUpdate = mode == "process_update_failed"
			f.failCreate = mode == "answer_create_failed"
			f.mu.Unlock()
			err = c.FinalizeWithAnswer(ctx, "PROCESS", answer)
			if (mode == "process_update_failed" || mode == "answer_create_failed") != (err != nil) {
				t.Fatalf("unexpected error: %v", err)
			}
			first, second := f.shown("om_1"), f.shown("om_2")
			switch mode {
			case "process_update_failed":
				if !strings.Contains(second, "CONCLUSION") {
					t.Fatal("process update failure withheld answer")
				}
			case "answer_create_failed":
				if !strings.Contains(first, "CONCLUSION") || second != "" {
					t.Fatal("failed answer not preserved on original card")
				}
			case "silent", "stopped":
				if second != "" || strings.Contains(first, "CONCLUSION") {
					t.Fatal("silent/stopped query sent a conclusion")
				}
			}
		})
	}
}
