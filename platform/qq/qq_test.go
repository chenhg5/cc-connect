package qq

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
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

// --- group_reply_all (#1475) -------------------------------------------------

const testBotSelfID = 888888

func newTestHandler(t *testing.T) (core.MessageHandler, *atomic.Int32) {
	t.Helper()
	var called atomic.Int32
	return func(_ core.Platform, _ *core.Message) {
		called.Add(1)
	}, &called
}

// buildGroupPayload builds a minimal OneBot v11 group message payload for
// handleMessage() tests. messageSegments / rawMessage let callers exercise
// the JSON segment and CQ-code forms independently.
func buildGroupPayload(userID, groupID, messageID int64, messageSegments []any, rawMessage string) map[string]any {
	return map[string]any{
		"post_type":    "message",
		"message_type": "group",
		"user_id":      userID,
		"group_id":     groupID,
		"message_id":   messageID,
		"time":         float64(time.Now().Unix()),
		"message":      messageSegments,
		"raw_message":  rawMessage,
		"sender":       map[string]any{"nickname": "alice"},
	}
}

func TestHandle_GroupMessage_NotMentionedWithGroupReplyAllFalse_Skipped(t *testing.T) {
	handler, called := newTestHandler(t)
	p := &Platform{
		selfID:        testBotSelfID,
		groupReplyAll: false,
		handler:       handler,
	}

	payload := buildGroupPayload(
		111111, 222222, 333333,
		[]any{map[string]any{"type": "text", "data": map[string]any{"text": "hello group"}}},
		"hello group",
	)
	p.handleMessage(payload)

	if got := called.Load(); got != 0 {
		t.Errorf("handler called %d times, want 0 (un-mentioned group msg must be skipped)", got)
	}
}

func TestHandle_GroupMessage_MentionedResponds(t *testing.T) {
	handler, called := newTestHandler(t)
	p := &Platform{
		selfID:        testBotSelfID,
		groupReplyAll: false,
		handler:       handler,
	}

	payload := buildGroupPayload(
		111111, 222222, 444444,
		[]any{
			map[string]any{"type": "at", "data": map[string]any{"qq": strconv.FormatInt(testBotSelfID, 10)}},
			map[string]any{"type": "text", "data": map[string]any{"text": " hi bot"}},
		},
		"[CQ:at,qq="+strconv.FormatInt(testBotSelfID, 10)+"] hi bot",
	)
	p.handleMessage(payload)

	if got := called.Load(); got != 1 {
		t.Errorf("handler called %d times, want 1 (@-mentioned group msg must respond)", got)
	}
}

func TestHandle_GroupMessage_WithGroupReplyAllTrue_RespondsAll(t *testing.T) {
	handler, called := newTestHandler(t)
	p := &Platform{
		selfID:        testBotSelfID,
		groupReplyAll: true,
		handler:       handler,
	}

	// No @-mention in payload, but group_reply_all=true → respond to all
	// (regression guard for legacy behavior).
	payload := buildGroupPayload(
		111111, 222222, 555555,
		[]any{map[string]any{"type": "text", "data": map[string]any{"text": "no mention here"}}},
		"no mention here",
	)
	p.handleMessage(payload)

	if got := called.Load(); got != 1 {
		t.Errorf("handler called %d times, want 1 (group_reply_all=true must respond to all)", got)
	}
}

func TestHandle_GroupMessage_PrivateMessageUnaffectedByGroupReplyAll(t *testing.T) {
	// Private messages (message_type="private") must never be filtered by
	// the group_reply_all logic. They have no @-mention concept.
	handler, called := newTestHandler(t)
	p := &Platform{
		selfID:        testBotSelfID,
		groupReplyAll: false,
		handler:       handler,
	}

	payload := map[string]any{
		"post_type":    "message",
		"message_type": "private",
		"user_id":      111111,
		"message_id":   666666,
		"time":         float64(time.Now().Unix()),
		"message":      []any{map[string]any{"type": "text", "data": map[string]any{"text": "hi"}}},
		"raw_message":  "hi",
		"sender":       map[string]any{"nickname": "alice"},
	}
	p.handleMessage(payload)

	if got := called.Load(); got != 1 {
		t.Errorf("handler called %d times, want 1 (private msg must always pass)", got)
	}
}

func TestNew_GroupReplyAllConfigDefault(t *testing.T) {
	p, err := New(map[string]any{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	platform := p.(*Platform)
	if platform.groupReplyAll {
		t.Error("groupReplyAll default = true, want false (must match Feishu/Discord/Telegram default)")
	}
}

func TestNew_GroupReplyAllConfigExplicitTrue(t *testing.T) {
	p, err := New(map[string]any{"group_reply_all": true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	platform := p.(*Platform)
	if !platform.groupReplyAll {
		t.Error("groupReplyAll = false, want true (explicit config)")
	}
}

func TestIsMentioned(t *testing.T) {
	p := &Platform{selfID: testBotSelfID}
	cases := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{
			"json_segment_at_string",
			map[string]any{
				"message": []any{
					map[string]any{"type": "at", "data": map[string]any{"qq": strconv.FormatInt(testBotSelfID, 10)}},
				},
			},
			true,
		},
		{
			"json_segment_at_other_id",
			map[string]any{
				"message": []any{
					map[string]any{"type": "at", "data": map[string]any{"qq": "999999"}},
				},
			},
			false,
		},
		{
			"cq_code_at_bracketed",
			map[string]any{
				"raw_message": "[CQ:at,qq=" + strconv.FormatInt(testBotSelfID, 10) + "] hello",
			},
			true,
		},
		{
			"cq_code_at_with_name",
			map[string]any{
				"raw_message": "[CQ:at,qq=" + strconv.FormatInt(testBotSelfID, 10) + ",name=bot] hello",
			},
			true,
		},
		{
			"no_at_plain_text",
			map[string]any{
				"message":     []any{map[string]any{"type": "text", "data": map[string]any{"text": "hi"}}},
				"raw_message": "hi",
			},
			false,
		},
		{
			"different_id_partial_no_false_positive",
			// selfID=888888; a message mentioning 8888 (different digit count) must not match.
			map[string]any{
				"raw_message": "[CQ:at,qq=8888] hi",
			},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.isMentioned(c.payload); got != c.want {
				t.Errorf("isMentioned = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIsMentioned_SelfIDZeroReturnsTrue(t *testing.T) {
	// When selfID isn't known yet (e.g. before get_login_info), don't filter —
	// match old behavior to avoid dropping messages during reconnect.
	p := &Platform{selfID: 0}
	payload := map[string]any{
		"message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "hi"}}},
	}
	if !p.isMentioned(payload) {
		t.Error("isMentioned should return true when selfID=0 (graceful fallback)")
	}
}
