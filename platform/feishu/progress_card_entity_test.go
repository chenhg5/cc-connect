package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// clientCard stands in for a progress card shown in the Feishu client. It
// applies batch_update actions like the card entity API does (failing on an
// unknown element_id, as Feishu answers 300313) and keeps each panel's open
// state the way the client does: a panel starts as its JSON says when it is
// mounted, and only a reader's click changes it afterwards.
type clientCard struct {
	t        *testing.T
	elements []any
	open     map[string]bool
}

func mountClientCard(t *testing.T, cardJSON string) *clientCard {
	t.Helper()
	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card JSON is invalid: %v\n%s", err, cardJSON)
	}
	c := &clientCard{t: t, elements: card["body"].(map[string]any)["elements"].([]any), open: map[string]bool{}}
	c.mount(c.elements)
	return c
}

func (c *clientCard) mount(elements []any) {
	for _, raw := range elements {
		el := raw.(map[string]any)
		if el["tag"] == "collapsible_panel" {
			c.open[el["element_id"].(string)] = el["expanded"] == true
		}
		if children, ok := el["elements"].([]any); ok {
			c.mount(children)
		}
	}
}

// find returns the element holding id in its "elements" (nil for the card
// body) and id's index there.
func (c *clientCard) find(parent map[string]any, list []any, id string) (map[string]any, int, bool) {
	for i, raw := range list {
		el := raw.(map[string]any)
		if el["element_id"] == id {
			return parent, i, true
		}
		if children, ok := el["elements"].([]any); ok {
			if owner, j, found := c.find(el, children, id); found {
				return owner, j, true
			}
		}
	}
	return nil, -1, false
}

func (c *clientCard) list(owner map[string]any) []any {
	if owner == nil {
		return c.elements
	}
	children, _ := owner["elements"].([]any)
	return children
}

func (c *clientCard) setList(owner map[string]any, list []any) {
	if owner == nil {
		c.elements = list
	} else {
		owner["elements"] = list
	}
}

func (c *clientCard) mustFind(id string) (map[string]any, int) {
	c.t.Helper()
	owner, i, ok := c.find(nil, c.elements, id)
	if !ok {
		c.t.Fatalf("batch_update targets unknown element %q", id)
	}
	return owner, i
}

func toAnyList(t *testing.T, v any) []any {
	t.Helper()
	b, _ := json.Marshal(v)
	var out []any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func toAnyMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(v)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (c *clientCard) apply(actions []map[string]any) {
	c.t.Helper()
	for _, action := range actions {
		params := toAnyMap(c.t, action["params"])
		switch action["action"] {
		case "add_elements":
			added := toAnyList(c.t, params["elements"])
			owner, i := c.mustFind(params["target_element_id"].(string))
			list := c.list(owner)
			switch params["type"] {
			case "insert_after":
				out := append(append(append([]any{}, list[:i+1]...), added...), list[i+1:]...)
				c.setList(owner, out)
			case "append":
				panel := list[i].(map[string]any)
				children, _ := panel["elements"].([]any)
				panel["elements"] = append(children, added...)
			default:
				c.t.Fatalf("unexpected add_elements type %v", params["type"])
			}
			c.mount(added)
		case "delete_elements":
			for _, id := range toAnyList(c.t, params["element_ids"]) {
				owner, i := c.mustFind(id.(string))
				list := c.list(owner)
				c.setList(owner, append(append([]any{}, list[:i]...), list[i+1:]...))
				delete(c.open, id.(string))
			}
		case "update_element":
			id := params["element_id"].(string)
			owner, i := c.mustFind(id)
			el := params["element"].(map[string]any)
			if el["tag"] == "collapsible_panel" {
				c.t.Fatalf("update_element re-sends panel %q, which closes it on the client", id)
			}
			c.list(owner)[i] = el
		case "partial_update_element":
			id := params["element_id"].(string)
			owner, i := c.mustFind(id)
			partial := params["partial_element"].(map[string]any)
			if _, ok := partial["expanded"]; ok {
				c.t.Fatalf("partial update of %q overrides the reader's expanded state", id)
			}
			el := c.list(owner)[i].(map[string]any)
			for k, v := range partial {
				el[k] = v
			}
		case "partial_update_setting":
		default:
			c.t.Fatalf("unexpected action %v", action["action"])
		}
	}
}

// content is what the card shows, without ids and initial states.
func cardContent(t *testing.T, elements []any) []any {
	t.Helper()
	out := make([]any, 0, len(elements))
	for _, raw := range elements {
		el := toAnyMap(t, raw)
		delete(el, "element_id")
		delete(el, "expanded")
		if children, ok := el["elements"].([]any); ok {
			el["elements"] = cardContent(t, children)
		}
		out = append(out, el)
	}
	return out
}

func freshContent(t *testing.T, payload *core.ProgressCardPayload) []any {
	t.Helper()
	_, cardJSON := newProgressCard(payload)
	return cardContent(t, mountClientCard(t, cardJSON).elements)
}

func progressPayload(t *testing.T, state core.ProgressCardState, truncated bool, items ...core.ProgressCardEntry) *core.ProgressCardPayload {
	t.Helper()
	payload, ok := core.ParseProgressCardPayload(core.BuildProgressCardPayloadV2(items, truncated, "Codex", core.LangChinese, state))
	if !ok {
		t.Fatal("progress payload did not parse")
	}
	return payload
}

// A reader opens the Tools panel; the turn keeps refreshing the card: more
// tool calls, a tool result landing on its call, a new lane appearing, old
// rows dropping off the front, completion. Tools stays open, the other panels
// stay collapsed, and every refresh shows exactly what a full redraw would.
func TestProgressCardEntity_OpenPanelSurvivesRefreshes(t *testing.T) {
	exitCode := 0
	think := core.ProgressCardEntry{Kind: core.ProgressEntryThinking, Text: "Inspecting the repo"}
	pwd := core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}
	pwdOut := core.ProgressCardEntry{Kind: core.ProgressEntryToolResult, Tool: "Bash", Text: "/tmp/project", ExitCode: &exitCode}
	ls := core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "ls"}
	lsOut := core.ProgressCardEntry{Kind: core.ProgressEntryToolResult, Tool: "Bash", Text: "main.go", ExitCode: &exitCode}
	think2 := core.ProgressCardEntry{Kind: core.ProgressEntryThinking, Text: "Planning the change"}
	compacted := core.ProgressCardEntry{Kind: core.ProgressEntryInfo, Text: "Context compacted"}
	running := core.ProgressCardStateRunning

	model, cardJSON := newProgressCard(progressPayload(t, running, false, think, pwd))
	card := mountClientCard(t, cardJSON)
	for id, open := range card.open {
		if open {
			t.Fatalf("panel %q starts expanded, want collapsed on first render", id)
		}
	}
	card.open["pc_t"] = true // the reader opens Tools

	// A tool result lands on its call while a later call is already shown.
	pwdDone := pwd
	pwdDone.Text = "pwd -P"
	refreshes := []*core.ProgressCardPayload{
		progressPayload(t, running, false, think, pwd, ls),
		progressPayload(t, running, false, think, pwdDone, ls),
		progressPayload(t, running, false, think, pwd, pwdOut),
		progressPayload(t, running, false, think, pwd, pwdOut, ls),
		progressPayload(t, running, false, think, pwd, pwdOut, ls, lsOut),
		progressPayload(t, running, false, think, pwd, pwdOut, ls, lsOut, think2),
		progressPayload(t, running, false, think, pwd, pwdOut, ls, lsOut, think2, compacted),
		progressPayload(t, running, true, pwdOut, ls, lsOut, think2, compacted),
		progressPayload(t, core.ProgressCardStateCompleted, true, pwdOut, ls, lsOut, think2, compacted),
	}
	for i, payload := range refreshes {
		actions, next := progressCardUpdate(model, payload)
		if len(actions) == 0 {
			t.Fatalf("refresh %d produced no update", i)
		}
		card.apply(actions)
		model = next
		if !card.open["pc_t"] {
			t.Fatalf("refresh %d closed the Tools panel the reader opened", i)
		}
		for _, id := range []string{"pc_r", "pc_u"} {
			if open, ok := card.open[id]; ok && open {
				t.Fatalf("refresh %d opened panel %q the reader never opened", i, id)
			}
		}
		if got, want := cardContent(t, card.elements), freshContent(t, payload); !reflect.DeepEqual(got, want) {
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(want)
			t.Fatalf("refresh %d shows\n%s\nwant\n%s", i, gb, wb)
		}
	}
	if _, ok := card.open["pc_u"]; !ok {
		t.Fatal("the Updates panel never appeared")
	}

	// The reader closes Tools again; it stays closed.
	card.open["pc_t"] = false
	actions, _ := progressCardUpdate(model, progressPayload(t, core.ProgressCardStateCompleted, true, ls, lsOut, think2, compacted))
	card.apply(actions)
	if card.open["pc_t"] {
		t.Fatal("a refresh re-opened the Tools panel the reader closed")
	}
}

func TestProgressCardEntity_UnchangedRefreshSendsNothing(t *testing.T) {
	payload := progressPayload(t, core.ProgressCardStateRunning, false,
		core.ProgressCardEntry{Kind: core.ProgressEntryThinking, Text: "planning"})
	model, _ := newProgressCard(payload)
	if actions, _ := progressCardUpdate(model, payload); len(actions) != 0 {
		t.Fatalf("unchanged refresh sent %d actions: %#v", len(actions), actions)
	}
}

type cardkitCall struct {
	method, path string
	body         map[string]any
}

// cardkitStub is an Open API stub for progress cards: it creates card_1, sends
// om_progress, and answers each batch_update with the next queued response
// (HTTP status, code), then with success.
type cardkitStub struct {
	mu      sync.Mutex
	calls   []cardkitCall
	batches [][2]int
}

func (s *cardkitStub) queueBatch(status, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, [2]int{status, code})
}

func (s *cardkitStub) take() []cardkitCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := s.calls
	s.calls = nil
	return calls
}

func newCardkitStubPlatform(t *testing.T) (*Platform, *cardkitStub) {
	t.Helper()
	stub := &cardkitStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		stub.mu.Lock()
		stub.calls = append(stub.calls, cardkitCall{r.Method, r.URL.Path, body})
		next := [2]int{http.StatusOK, 0}
		if strings.HasSuffix(r.URL.Path, "/batch_update") && len(stub.batches) > 0 {
			next, stub.batches = stub.batches[0], stub.batches[1:]
		}
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "ok", "expire": 7200, "tenant_access_token": "t-test"})
		case r.URL.Path == "/open-apis/cardkit/v1/cards" && r.Method == http.MethodPost:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"card_id": "card_1"}})
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodPost:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"message_id": "om_progress"}})
		case strings.HasSuffix(r.URL.Path, "/batch_update"):
			w.WriteHeader(next[0])
			writeJSON(t, w, map[string]any{"code": next[1], "msg": "batch result"})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "ok"})
		}
	}))
	t.Cleanup(srv.Close)
	return &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              "cli_test",
		appSecret:          "secret",
		useInteractiveCard: true,
		client:             lark.NewClient("cli_test", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient: lark.NewClient("cli_test", "secret", lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}, stub
}

func progressContent(items ...core.ProgressCardEntry) string {
	return core.BuildProgressCardPayloadV2(items, false, "Codex", core.LangChinese, core.ProgressCardStateRunning)
}

var (
	stubThink = core.ProgressCardEntry{Kind: core.ProgressEntryThinking, Text: "planning"}
	stubPwd   = core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd"}
	stubLs    = core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "ls"}
)

func batchCalls(t *testing.T, calls []cardkitCall) []cardkitCall {
	t.Helper()
	var batches []cardkitCall
	for _, call := range calls {
		switch {
		case call.method == http.MethodPatch && strings.HasPrefix(call.path, "/open-apis/im/v1/messages/"):
			t.Fatalf("progress refresh replaced the whole card with Im.Message.Patch")
		case call.method == http.MethodPut && call.path == "/open-apis/cardkit/v1/cards/card_1":
			t.Fatalf("progress refresh replaced the whole card entity")
		case call.path == "/open-apis/cardkit/v1/cards/card_1/batch_update":
			batches = append(batches, call)
		}
	}
	return batches
}

func replacedCard(calls []cardkitCall) bool {
	for _, call := range calls {
		if call.method == http.MethodPut && call.path == "/open-apis/cardkit/v1/cards/card_1" {
			return true
		}
	}
	return false
}

// SendPreviewStart sends a progress card as a card entity and UpdateMessage
// refreshes it with batch_update, never by replacing the whole card.
func TestProgressCardEntity_RefreshesInPlace(t *testing.T) {
	p, stub := newCardkitStubPlatform(t)
	ctx := context.Background()
	handle, err := p.SendPreviewStart(ctx, replyContext{chatID: "oc_chat"}, progressContent(stubThink))
	if err != nil {
		t.Fatalf("SendPreviewStart() error = %v", err)
	}
	stub.take()
	for _, content := range []string{progressContent(stubThink, stubPwd), progressContent(stubThink, stubPwd, stubLs)} {
		if err := p.UpdateMessage(ctx, handle, content); err != nil {
			t.Fatalf("UpdateMessage() error = %v", err)
		}
	}
	batches := batchCalls(t, stub.take())
	if len(batches) != 2 {
		t.Fatalf("batch_update calls = %d, want 2", len(batches))
	}
	if batches[0].body["sequence"].(float64) >= batches[1].body["sequence"].(float64) {
		t.Fatalf("batch_update sequence must increase: %v then %v", batches[0].body["sequence"], batches[1].body["sequence"])
	}

	// Feishu rejecting the actions (HTTP 400 with a code, as cardkit answers)
	// means the card no longer matches what cc-connect thinks it shows:
	// replace it so it stays right, and later refreshes go back to batches.
	// 300301 is what a batch that took effect but timed out on the client
	// gets when its rows are added again.
	content := []core.ProgressCardEntry{stubThink, stubPwd, stubLs}
	for _, code := range []int{300313, 300301} {
		content = append(content, stubPwd)
		stub.queueBatch(http.StatusBadRequest, code)
		if err := p.UpdateMessage(ctx, handle, progressContent(content...)); err != nil {
			t.Fatalf("UpdateMessage() after batch rejected with %d error = %v", code, err)
		}
		if !replacedCard(stub.take()) {
			t.Fatalf("batch_update rejected with HTTP 400 code %d did not fall back to a full card update", code)
		}
		content = append(content, stubLs)
		if err := p.UpdateMessage(ctx, handle, progressContent(content...)); err != nil {
			t.Fatalf("UpdateMessage() after the fallback error = %v", err)
		}
		if batches := batchCalls(t, stub.take()); len(batches) != 1 {
			t.Fatalf("refresh after the fallback sent %d batch_update calls, want 1", len(batches))
		}
	}
}

// A transient failure is retried with the same sequence and uuid; a failure
// that is not Feishu rejecting the actions is returned for core to retry, and
// the next refresh picks up from the same card. Neither replaces the card,
// which would close the panels the reader opened.
func TestProgressCardEntity_FailuresDoNotReplaceCard(t *testing.T) {
	p, stub := newCardkitStubPlatform(t)
	ctx := context.Background()
	handle, err := p.SendPreviewStart(ctx, replyContext{chatID: "oc_chat"}, progressContent(stubThink))
	if err != nil {
		t.Fatalf("SendPreviewStart() error = %v", err)
	}
	stub.take()

	stub.queueBatch(http.StatusOK, 2200) // Feishu internal error: transient
	if err := p.UpdateMessage(ctx, handle, progressContent(stubThink, stubPwd)); err != nil {
		t.Fatalf("UpdateMessage() after a transient failure error = %v", err)
	}
	batches := batchCalls(t, stub.take())
	if len(batches) != 2 {
		t.Fatalf("batch_update calls = %d, want the failed one and its retry", len(batches))
	}
	if !reflect.DeepEqual(batches[0].body, batches[1].body) {
		t.Fatalf("retry must resend the same sequence and uuid:\n%v\n%v", batches[0].body, batches[1].body)
	}
	if batches[0].body["uuid"] == nil {
		t.Fatal("batch_update carries no uuid, so a retry could apply twice")
	}

	for _, failure := range []struct {
		name         string
		status, code int
	}{
		{"server error", http.StatusInternalServerError, 0},
		{"rate limited", http.StatusOK, 230020},
	} {
		stub.queueBatch(failure.status, failure.code)
		if err := p.UpdateMessage(ctx, handle, progressContent(stubThink, stubPwd, stubLs)); err == nil {
			t.Fatalf("%s: UpdateMessage() = nil, want the error for core to retry", failure.name)
		}
		if calls := stub.take(); replacedCard(calls) {
			t.Fatalf("%s: replaced the whole card", failure.name)
		}
		// core retries with the same content; the diff still starts from the
		// card as last shown, so it adds the ls row.
		if err := p.UpdateMessage(ctx, handle, progressContent(stubThink, stubPwd, stubLs)); err != nil {
			t.Fatalf("%s: retry UpdateMessage() error = %v", failure.name, err)
		}
		batches := batchCalls(t, stub.take())
		if len(batches) != 1 || !strings.Contains(batches[0].body["actions"].(string), "add_elements") {
			t.Fatalf("%s: retry did not resend the missing row: %#v", failure.name, batches)
		}
		// Undo for the next case: the card goes back to two rows.
		if err := p.UpdateMessage(ctx, handle, progressContent(stubThink, stubPwd)); err != nil {
			t.Fatalf("%s: reset UpdateMessage() error = %v", failure.name, err)
		}
		stub.take()
	}
}
