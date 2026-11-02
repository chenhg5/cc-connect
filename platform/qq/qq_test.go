package qq

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/gorilla/websocket"
)

func TestPlatform_Name(t *testing.T) {
	p := &Platform{}
	if got := p.Name(); got != "qq" {
		t.Errorf("Name() = %q, want %q", got, "qq")
	}
}

func TestNew_DefaultWSURL(t *testing.T) {
	p, err := New(map[string]any{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://127.0.0.1:3001" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://127.0.0.1:3001")
	}
}

func TestNew_CustomWSURL(t *testing.T) {
	p, err := New(map[string]any{
		"ws_url": "ws://example.com:8080",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://example.com:8080" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://example.com:8080")
	}
}

func TestNew_WithToken(t *testing.T) {
	p, err := New(map[string]any{
		"token": "my-secret-token",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.token != "my-secret-token" {
		t.Errorf("token = %q, want %q", platform.token, "my-secret-token")
	}
}

func TestNew_WithAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"allow_from": "user1,user2,*",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.allowFrom != "user1,user2,*" {
		t.Errorf("allowFrom = %q, want %q", platform.allowFrom, "user1,user2,*")
	}
}

func TestNew_ShareSessionInChannel(t *testing.T) {
	p, err := New(map[string]any{
		"share_session_in_channel": true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if !platform.shareSessionInChannel {
		t.Error("shareSessionInChannel = false, want true")
	}
}

// verify Platform implements core.Platform
var _ core.Platform = (*Platform)(nil)

// TestStart_FetchesSelfIDWithoutTimeout verifies that Start() completes
// promptly with selfID populated from the get_login_info OneBot API call.
// Regression for a bug where Start invoked callAPI BEFORE launching readLoop,
// so the API response had no consumer and callAPI always timed out after 15s
// — leaving selfID=0 and disabling the self-message filter in handleMessage.
func TestStart_FetchesSelfIDWithoutTimeout(t *testing.T) {
	const botUserID = 999999

	upgrader := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}
			if req["action"] == "get_login_info" {
				echo, _ := req["echo"].(string)
				resp := map[string]any{
					"status":  "ok",
					"retcode": 0,
					"echo":    echo,
					"data":    map[string]any{"user_id": botUserID, "nickname": "TestBot"},
				}
				raw, _ := json.Marshal(resp)
				_ = c.WriteMessage(websocket.TextMessage, raw)
			}
		}
	}))
	defer ts.Close()

	p := &Platform{
		wsURL: "ws" + strings.TrimPrefix(ts.URL, "http"),
	}

	done := make(chan error, 1)
	go func() {
		done <- p.Start(func(core.Platform, *core.Message) {})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = p.Stop()
		t.Fatal("Start did not complete within 5s; readLoop likely starts after callAPI, so get_login_info never gets a response")
	}
	defer p.Stop()

	if p.selfID != botUserID {
		t.Errorf("selfID = %d, want %d (self-message filter would be disabled)", p.selfID, botUserID)
	}
}

func TestNew_RequireMentionDefaultsTrue(t *testing.T) {
	p, err := New(map[string]any{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !p.(*Platform).requireMention {
		t.Error("requireMention should default to true")
	}
}

func TestNew_RequireMentionFalse(t *testing.T) {
	p, err := New(map[string]any{"require_mention": false})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if p.(*Platform).requireMention {
		t.Error("requireMention should be false when explicitly configured")
	}
}

func TestMessageMentionsSelf(t *testing.T) {
	p := &Platform{selfID: 12345}
	cases := []struct {
		name    string
		message any
		want    bool
	}{
		{"array at self", []any{map[string]any{"type": "at", "data": map[string]any{"qq": "12345"}}}, true},
		{"array at all", []any{map[string]any{"type": "at", "data": map[string]any{"qq": "all"}}}, true},
		{"array at other", []any{map[string]any{"type": "at", "data": map[string]any{"qq": "99999"}}}, false},
		{"array no at", []any{map[string]any{"type": "text", "data": map[string]any{"text": "hello"}}}, false},
		{"cq string at self", "[CQ:at,qq=12345] hello", true},
		{"cq string at all", "[CQ:at,qq=all] hello", true},
		{"cq string no at", "hello", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"message": tc.message}
			if got := p.messageMentionsSelf(payload); got != tc.want {
				t.Errorf("messageMentionsSelf() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMessageMentionsSelf_FailsOpenWithoutSelfID(t *testing.T) {
	p := &Platform{} // selfID unknown
	if !p.messageMentionsSelf(map[string]any{"message": []any{}}) {
		t.Error("should fail open (return true) when selfID is unknown")
	}
}
