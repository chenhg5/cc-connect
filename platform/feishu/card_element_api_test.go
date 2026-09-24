package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// TestPatchCardElementTitleBodyUsesPartialElement pins the request body of the
// cardkit "update element properties" API
// (PATCH /open-apis/cardkit/v1/cards/{card_id}/elements/{element_id}).
//
// Regression (observed live on 2026-09-24): the property payload was sent in a
// field named `element`, while the API requires `partial_element`. Every call
// was rejected with HTTP 400, so the "思考 (N) / 工具 (N)" counts on the live
// progress card stayed frozen at their card-creation values and the daemon
// logged "patch … panel title failed (title stays stale)" on each step. The
// pre-fix code fails this test (no `partial_element` in the body).
func TestPatchCardElementTitleBodyUsesPartialElement(t *testing.T) {
	const appID = "cli_patch_element"
	const appSecret = "secret-patch-element"

	var (
		patchMethod string
		patchPath   string
		patchBody   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "t-patch",
			})
			return
		}
		patchMethod = r.Method
		patchPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&patchBody); err != nil {
			t.Errorf("decode patch body: %v", err)
		}
		writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	h := &feishuPreviewHandle{messageID: "om_card", chatID: "oc_chat", cardID: "card_1"}
	if err := p.patchCardElementTitle(context.Background(), h, progressPanelThinkingElementID, "思考 (3)"); err != nil {
		t.Fatalf("patchCardElementTitle() error = %v", err)
	}

	if patchMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", patchMethod)
	}
	wantPath := "/open-apis/cardkit/v1/cards/card_1/elements/" + progressPanelThinkingElementID
	if patchPath != wantPath {
		t.Errorf("path = %q, want %q", patchPath, wantPath)
	}

	raw, ok := patchBody["partial_element"]
	if !ok {
		t.Fatalf("body = %v, want a partial_element field: the API rejects the request with HTTP 400 without it", patchBody)
	}
	if _, legacy := patchBody["element"]; legacy {
		t.Errorf("body still carries the retired `element` field: %v", patchBody)
	}
	if _, ok := patchBody["sequence"]; !ok {
		t.Errorf("body = %v, want a sequence field", patchBody)
	}

	serialized, ok := raw.(string)
	if !ok {
		t.Fatalf("partial_element type = %T, want a JSON-serialized string", raw)
	}
	var partial struct {
		Header struct {
			Title struct {
				Content string `json:"content"`
			} `json:"title"`
		} `json:"header"`
	}
	if err := json.Unmarshal([]byte(serialized), &partial); err != nil {
		t.Fatalf("partial_element is not valid JSON (%q): %v", serialized, err)
	}
	if partial.Header.Title.Content != "思考 (3)" {
		t.Errorf("patched title = %q, want %q", partial.Header.Title.Content, "思考 (3)")
	}
}

// TestBuildPatchElementTitleBodyMentionsNoExpandedState guards the reason the
// panel title is patched through the element API at all: the request carries
// only header.title, never the panel's `expanded` property, so the client
// keeps whatever expand/collapse state the user chose.
func TestBuildPatchElementTitleBodyMentionsNoExpandedState(t *testing.T) {
	body, err := buildPatchElementTitleBody("工具 (4)", 7)
	if err != nil {
		t.Fatalf("buildPatchElementTitleBody() error = %v", err)
	}
	serialized, _ := body["partial_element"].(string)
	var partial map[string]any
	if err := json.Unmarshal([]byte(serialized), &partial); err != nil {
		t.Fatalf("partial_element is not valid JSON (%q): %v", serialized, err)
	}
	if _, ok := partial["expanded"]; ok {
		t.Errorf("partial_element = %v, must not carry `expanded` (it would override the user's panel state)", partial)
	}
	if len(partial) != 1 {
		t.Errorf("partial_element keys = %v, want only header", partial)
	}
	if seq, ok := body["sequence"].(int); !ok || seq != 7 {
		t.Errorf("sequence = %v, want 7", body["sequence"])
	}
}
