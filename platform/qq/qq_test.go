package qq

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// recordFixture is a record directory plus a directory outside it holding a
// file the platform must never read.
type recordFixture struct {
	root    string // the configured record_dirs entry
	outside string // a directory next to root
	secret  string // a regular file in outside
}

func newRecordFixture(t *testing.T) recordFixture {
	t.Helper()
	base := t.TempDir()
	f := recordFixture{
		root:    filepath.Join(base, "record"),
		outside: filepath.Join(base, "outside"),
	}
	f.secret = filepath.Join(f.outside, "secret")
	for _, d := range []string{f.root, f.outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.secret, []byte("private data"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestReadLocalRecord_InsideRoot(t *testing.T) {
	f := newRecordFixture(t)
	path := filepath.Join(f.root, "2026-09", "voice.amr")
	want := []byte("\x02#!SILK_V3 payload")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readLocalRecord(path, int64(len(want)), []string{f.root}, time.Second)
	if err != nil {
		t.Fatalf("readLocalRecord: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("readLocalRecord = %q, want %q", got, want)
	}
}

func TestReadLocalRecord_WaitsForDownload(t *testing.T) {
	f := newRecordFixture(t)
	path := filepath.Join(f.root, "voice.amr")
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
	got, err := readLocalRecord(path, int64(len(want)), []string{f.root}, 5*time.Second)
	if err != nil {
		t.Fatalf("readLocalRecord: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("readLocalRecord = %q, want %q", got, want)
	}
}

func TestReadLocalRecord_TimesOut(t *testing.T) {
	f := newRecordFixture(t)
	if _, err := readLocalRecord(filepath.Join(f.root, "missing.amr"), 10, []string{f.root}, 300*time.Millisecond); err == nil {
		t.Fatal("expected an error for a file that never appears")
	}
}

func TestReadLocalRecord_Rejects(t *testing.T) {
	f := newRecordFixture(t)
	symlink := func(target, link string) string {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	fileLink := symlink(f.secret, filepath.Join(f.root, "link.amr"))
	dirLink := symlink(f.outside, filepath.Join(f.root, "linkdir"))
	subdir := filepath.Join(f.root, "sub")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(f.root, "big.amr")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxRecordBytes+1); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		path  string
		roots []string
	}{
		{"no record_dirs", f.secret, nil},
		{"relative path", "record/voice.amr", []string{f.root}},
		{"absolute path outside root", f.secret, []string{f.root}},
		{"traversal out of root", f.root + "/../outside/secret", []string{f.root}},
		{"missing file outside root", filepath.Join(f.outside, "missing.amr"), []string{f.root}},
		{"root itself", f.root, []string{f.root}},
		{"symlink to outside file", fileLink, []string{f.root}},
		{"path through symlinked dir", filepath.Join(dirLink, "secret"), []string{f.root}},
		{"directory", subdir, []string{f.root}},
		{"larger than limit", big, []string{f.root}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			data, err := readLocalRecord(tt.path, 1, tt.roots, 5*time.Second)
			if err == nil {
				t.Fatalf("readLocalRecord(%q) = %q, want an error", tt.path, data)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("rejection took %s; it should not wait for the file", d)
			}
		})
	}
}

func TestReadRecord_FileSize(t *testing.T) {
	f := newRecordFixture(t)
	path := filepath.Join(f.root, "voice.amr")
	if err := os.WriteFile(path, []byte("#!AMR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Platform{recordDirs: []string{f.root}}
	if _, err := p.readRecord(path, 0); err == nil {
		t.Error("local voice file without file_size was read")
	}
	if _, err := p.readRecord(path, maxRecordBytes+1); err == nil {
		t.Error("voice file with file_size over the limit was read")
	}
	if _, err := p.readRecord(path, 6); err != nil {
		t.Errorf("readRecord: %v", err)
	}
}

func TestRecordFileSize(t *testing.T) {
	tests := []struct {
		in      any
		want    int64
		wantErr bool
	}{
		{nil, 0, false},
		{float64(1234), 1234, false},
		{float64(1200000), 1200000, false},
		{"5678", 5678, false},
		{float64(0), 0, true},
		{float64(-5), 0, true},
		{"-5", 0, true},
		{float64(1.5), 0, true},
		{"abc", 0, true},
		{"", 0, true},
		{true, 0, true},
	}
	for _, tt := range tests {
		got, err := recordFileSize(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("recordFileSize(%#v) = %d, %v; want %d, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseRecordDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	got, err := parseRecordDirs([]any{"/a/b/../c", "~/qq"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/a/c", filepath.Join(home, "qq")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("parseRecordDirs = %q, want %q", got, want)
	}
	for _, bad := range []any{"relative/dir", []any{1}, 42} {
		if _, err := parseRecordDirs(bad); err == nil {
			t.Errorf("parseRecordDirs(%#v): want an error", bad)
		}
	}
	if _, err := New(map[string]any{"record_dirs": "relative"}); err == nil {
		t.Error("New accepted a relative record_dirs entry")
	}
}

func TestDownloadRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/voice.amr":
			_, _ = w.Write([]byte("#!AMR\n"))
		case "/big":
			// No Content-Length, so the limit must come from the read.
			chunk := make([]byte, 1<<20)
			for written := 0; written <= maxRecordBytes; written += len(chunk) {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		case "/big-declared":
			w.Header().Set("Content-Length", strconv.Itoa(maxRecordBytes+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := downloadRecord(srv.URL + "/voice.amr")
	if err != nil {
		t.Fatalf("downloadRecord: %v", err)
	}
	if string(got) != "#!AMR\n" {
		t.Errorf("downloadRecord = %q", got)
	}
	for _, path := range []string{"/big", "/big-declared", "/missing"} {
		if _, err := downloadRecord(srv.URL + path); err == nil {
			t.Errorf("downloadRecord(%s): want an error", path)
		}
	}
	p := &Platform{}
	if _, err := p.readRecord(srv.URL+"/voice.amr", 0); err != nil {
		t.Errorf("readRecord over HTTP without file_size: %v", err)
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
