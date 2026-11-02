package feishu

import (
	"encoding/json"
	"strings"
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
// text character by character.
func TestBuildRichCard_StreamingModeFlag(t *testing.T) {
	on := buildRichCard(core.CardStatusWorking, "", nil, "hello", true, "")
	off := buildRichCard(core.CardStatusWorking, "", nil, "hello", false, "")
	if !strings.Contains(on, `"streaming_mode":true`) {
		t.Error("streaming card should carry streaming_mode:true")
	}
	if !strings.Contains(off, `"streaming_mode":false`) {
		t.Error("non-streaming card should carry streaming_mode:false")
	}
}
