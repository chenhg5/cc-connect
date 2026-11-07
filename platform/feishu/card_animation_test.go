package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/chenhg5/cc-connect/core"
)

// TestNewPlatform_CardAnimationDefaultsOn pins the default: rich cards keep the
// native typewriter animation unless the operator opts out.
func TestNewPlatform_CardAnimationDefaultsOn(t *testing.T) {
	p, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
		"app_id":     "cli_test",
		"app_secret": "secret",
	})
	if err != nil {
		t.Fatalf("newPlatform error: %v", err)
	}
	fp := extractBasePlatform(p)
	if fp == nil {
		t.Fatal("expected *Platform or *interactivePlatform")
	}
	if !fp.cardAnimation {
		t.Error("card_animation should default to true, but it is false")
	}
}

// TestNewPlatform_CardAnimationOption covers the opt-out switch.
func TestNewPlatform_CardAnimationOption(t *testing.T) {
	p, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
		"app_id":            "cli_test",
		"app_secret":        "secret",
		"cc_card_animation": false,
	})
	if err != nil {
		t.Fatalf("newPlatform error: %v", err)
	}
	fp := extractBasePlatform(p)
	if fp == nil {
		t.Fatal("expected *Platform or *interactivePlatform")
	}
	if fp.cardAnimation {
		t.Error("cc_card_animation=false should disable the typewriter animation")
	}
}

// TestDisableCardStreamingMode checks the helper that rewrites the card config.
func TestDisableCardStreamingMode(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"minimal", `{"schema":"2.0","config":{"streaming_mode":true},"body":{"elements":[]}}`},
		{"with other config keys", `{"schema":"2.0","config":{"streaming_mode":true,"update_multi":true,"enable_forward_interaction":true},"body":{"elements":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := disableCardStreamingMode(tc.in)
			var card map[string]any
			if err := json.Unmarshal([]byte(out), &card); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}
			cfg, ok := card["config"].(map[string]any)
			if !ok {
				t.Fatal("config block missing from output")
			}
			if cfg["streaming_mode"] != false {
				t.Errorf("streaming_mode = %v, want false", cfg["streaming_mode"])
			}
			// The rest of the card must survive the rewrite.
			if card["schema"] != "2.0" {
				t.Errorf("schema = %v, want 2.0", card["schema"])
			}
			if _, ok := card["body"]; !ok {
				t.Error("body block was dropped")
			}
		})
	}
}

// TestDisableCardStreamingMode_PreservesOtherConfigKeys makes sure we do not
// clobber sibling config keys (update_multi, enable_forward_interaction).
func TestDisableCardStreamingMode_PreservesOtherConfigKeys(t *testing.T) {
	in := `{"schema":"2.0","config":{"streaming_mode":true,"update_multi":true,"enable_forward_interaction":true},"body":{"elements":[]}}`
	out := disableCardStreamingMode(in)
	var card map[string]any
	if err := json.Unmarshal([]byte(out), &card); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	cfg := card["config"].(map[string]any)
	if cfg["update_multi"] != true {
		t.Error("update_multi was dropped")
	}
	if cfg["enable_forward_interaction"] != true {
		t.Error("enable_forward_interaction was dropped")
	}
}

// TestDisableCardStreamingMode_InvalidJSONIsPassThrough checks the best-effort
// contract: malformed JSON must come back untouched rather than panic.
func TestDisableCardStreamingMode_InvalidJSONIsPassThrough(t *testing.T) {
	const broken = `{"schema":"2.0",`
	if got := disableCardStreamingMode(broken); got != broken {
		t.Errorf("broken JSON should pass through unchanged, got %q", got)
	}
}

// TestDisableCardStreamingMode_NoConfigBlock covers a card without a config
// block — nothing to change, so it must pass through unchanged.
func TestDisableCardStreamingMode_NoConfigBlock(t *testing.T) {
	const noCfg = `{"schema":"2.0","body":{"elements":[]}}`
	if got := disableCardStreamingMode(noCfg); got != noCfg {
		t.Errorf("card without config should pass through unchanged, got %q", got)
	}
}

// TestBuildRichCard_StreamingModeFlag documents that the card JSON carries the
// streaming_mode flag the Feishu client uses to decide whether to animate the
// text character by character, and that the platform-level switch overrides
// whatever the caller asked for.
func TestBuildRichCard_StreamingModeFlag(t *testing.T) {
	on := (&Platform{platformName: "feishu", cardAnimation: true}).
		BuildRichCard(core.CardStatusWorking, "", nil, "hello", true, "")
	off := (&Platform{platformName: "feishu", cardAnimation: false}).
		BuildRichCard(core.CardStatusWorking, "", nil, "hello", true, "")
	if !strings.Contains(on, `"streaming_mode":true`) {
		t.Error("animation on: streaming card should carry streaming_mode:true")
	}
	if !strings.Contains(off, `"streaming_mode":false`) {
		t.Error("animation off: mid-turn card must carry streaming_mode:false")
	}
}

// TestCardAnimationOff_WirePayloads pins the whole turn on the wire: the card
// is created inline with config.streaming_mode=false, no cardkit entity is
// created, and the first mid-turn Patch still carries streaming_mode=false.
func TestCardAnimationOff_WirePayloads(t *testing.T) {
	const appID = "cli_card_anim_off"
	const appSecret = "secret"

	var (
		mu          sync.Mutex
		createBody  string
		patchBody   string
		cardkitHits int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/cardkit/v1/cards":
			mu.Lock()
			cardkitHits++
			mu.Unlock()
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"card_id": "card_x"}})
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			createBody = string(body)
			mu.Unlock()
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_card_1"}})
		case strings.Contains(r.URL.Path, "/messages/") && r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			patchBody = string(body)
			mu.Unlock()
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "ok"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		cardAnimation:      false,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}

	ctx := context.Background()
	// The engine builds every mid-turn frame with streaming=true.
	initial := p.BuildRichCard(core.CardStatusWorking, "", nil, "", true, "")
	handle, err := p.SendPreviewStart(ctx, replyContext{chatID: "oc_chat"}, initial)
	if err != nil {
		t.Fatalf("SendPreviewStart() error = %v", err)
	}

	mu.Lock()
	if cardkitHits != 0 {
		mu.Unlock()
		t.Fatalf("cardkit entity created %d times, want 0 when animation is off", cardkitHits)
	}
	if !strings.Contains(createBody, `\"streaming_mode\":false`) {
		mu.Unlock()
		t.Fatalf("created card must carry streaming_mode:false, body = %s", createBody)
	}
	mu.Unlock()

	update := p.BuildRichCard(core.CardStatusWorking, "", nil, "partial answer", true, "")
	if err := p.UpdateMessage(ctx, handle, update); err != nil {
		t.Fatalf("UpdateMessage() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(patchBody, `\"streaming_mode\":false`) {
		t.Fatalf("first mid-turn Patch must keep streaming_mode:false (regression), body = %s", patchBody)
	}
}

// TestCardAnimationOn_WirePayloads is the control: with the switch on (the
// default), the same Patch path must still carry streaming_mode:true.
func TestCardAnimationOn_WirePayloads(t *testing.T) {
	const appID = "cli_card_anim_on"
	const appSecret = "secret"

	var (
		mu        sync.Mutex
		patchBody string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case strings.Contains(r.URL.Path, "/messages/") && r.Method == http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			patchBody = string(body)
			mu.Unlock()
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "ok"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		cardAnimation:      true,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}

	handle := &feishuPreviewHandle{messageID: "om_card_on", chatID: "oc_chat"}
	update := p.BuildRichCard(core.CardStatusWorking, "", nil, "partial answer", true, "")
	if err := p.UpdateMessage(context.Background(), handle, update); err != nil {
		t.Fatalf("UpdateMessage() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(patchBody, `\"streaming_mode\":true`) {
		t.Fatalf("animation on: Patch must carry streaming_mode:true, body = %s", patchBody)
	}
}

// TestPatchMessageRateLimitPropagates: a 230020 on the inline Patch path must be
// returned to the caller. The engine's final flush only falls back to Send when
// UpdateMessage reports an error, so swallowing it would silently drop the answer.
func TestPatchMessageRateLimitPropagates(t *testing.T) {
	const appID = "cli_patch_ratelimit"
	const appSecret = "secret"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case strings.Contains(r.URL.Path, "/messages/") && r.Method == http.MethodPatch:
			writeJSON(t, w, map[string]any{"code": 230020, "msg": "too many requests"})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "ok"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:       "feishu",
		domain:             srv.URL,
		appID:              appID,
		appSecret:          appSecret,
		useInteractiveCard: true,
		cardAnimation:      true,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
	}

	handle := &feishuPreviewHandle{messageID: "om_card_rl", chatID: "oc_chat"}
	card := p.BuildRichCard(core.CardStatusWorking, "", nil, "partial", true, "")
	err := p.UpdateMessage(context.Background(), handle, card)
	if err == nil {
		t.Fatal("rate-limited Patch must return an error so the final flush can fall back to Send")
	}
	if !errors.Is(err, errFeishuCardRateLimited) {
		t.Fatalf("error should classify as errFeishuCardRateLimited, got %v", err)
	}
}

// TestNewPlatform_RejectsNonBoolCardAnimation: a non-bool value is a config
// mistake, not a silent "animation off".
func TestNewPlatform_RejectsNonBoolCardAnimation(t *testing.T) {
	if _, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
		"app_id":            "cli_test",
		"app_secret":        "secret",
		"cc_card_animation": "false",
	}); err == nil {
		t.Fatal("non-bool cc_card_animation should be rejected")
	}
}
