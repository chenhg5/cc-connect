package qq

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestReadRecord_LocalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.amr")
	want := []byte("\x02#!SILK_V3 payload")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRecord(path, int64(len(want)))
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("readRecord = %q, want %q", got, want)
	}
}

func TestReadRecord_WaitsForDownload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.amr")
	want := []byte("\x02#!SILK_V3 payload")
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := os.WriteFile(path, want[:4], 0o600); err != nil {
			t.Error(err)
		}
		time.Sleep(200 * time.Millisecond)
		if err := os.WriteFile(path, want, 0o600); err != nil {
			t.Error(err)
		}
	}()
	got, err := readRecord(path, int64(len(want)))
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("readRecord = %q, want %q", got, want)
	}
}

func TestReadRecord_TimesOut(t *testing.T) {
	old := recordWaitTimeout
	recordWaitTimeout = 300 * time.Millisecond
	defer func() { recordWaitTimeout = old }()
	if _, err := readRecord(filepath.Join(t.TempDir(), "missing.amr"), 10); err == nil {
		t.Fatal("expected an error for a file that never appears")
	}
}

func TestReadRecord_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#!AMR\n"))
	}))
	defer srv.Close()
	got, err := readRecord(srv.URL+"/voice.amr", 0)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if string(got) != "#!AMR\n" {
		t.Errorf("readRecord = %q", got)
	}
}

func TestRecordFormat(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"tencent silk", "\x02#!SILK_V3", "silk"},
		{"plain silk", "#!SILK_V3", "silk"},
		{"amr", "#!AMR\n", "amr"},
		{"mp3 with id3", "ID3\x04", "mp3"},
		{"mp3 frame sync", "\xff\xfb\x90", "mp3"},
	}
	for _, tt := range tests {
		if got := recordFormat([]byte(tt.data)); got != tt.want {
			t.Errorf("%s: recordFormat = %q, want %q", tt.name, got, tt.want)
		}
	}
}
