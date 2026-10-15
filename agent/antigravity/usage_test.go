package antigravity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Shape of a real retrieveUserQuotaSummary response (2026-10-15, trimmed).
const sampleQuotaSummary = `{
  "groups": [
    {"displayName": "Gemini Models", "description": "Models within this group: Gemini Flash, Gemini Pro",
     "buckets": [
       {"bucketId": "gemini-weekly", "window": "weekly", "resetTime": "2026-10-19T08:24:25Z", "remainingFraction": 0.92373693},
       {"bucketId": "gemini-5h", "window": "5h", "resetTime": "2026-10-15T08:04:24Z", "remainingFraction": 0.9401266}
     ]},
    {"displayName": "Claude and GPT models", "description": "Models within this group: Claude Opus, Claude Sonnet, GPT-OSS",
     "buckets": [
       {"bucketId": "3p-weekly", "window": "weekly", "resetTime": "2026-10-22T05:13:42Z", "remainingFraction": 1},
       {"bucketId": "3p-5h", "window": "5h", "resetTime": "2026-10-15T10:13:42Z", "remainingFraction": 0.5}
     ]}
  ]
}`

func decodeSample(t *testing.T) quotaSummary {
	t.Helper()
	var p quotaSummary
	if err := json.Unmarshal([]byte(sampleQuotaSummary), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMapQuotaSummary_GeminiModelFirst(t *testing.T) {
	now := time.Date(2026, 10, 15, 5, 0, 0, 0, time.UTC)
	r := mapQuotaSummary(decodeSample(t), "gemini-3.8-flash-high", now)
	if len(r.Buckets) != 2 || r.Buckets[0].Name != "Gemini Models" {
		t.Fatalf("buckets = %+v", r.Buckets)
	}
	ws := r.Buckets[0].Windows
	if ws[0].WindowSeconds != 7*24*3600 || ws[0].UsedPercent != 8 {
		t.Errorf("weekly = %+v, want 604800s used 8%%", ws[0])
	}
	if ws[1].WindowSeconds != 5*3600 || ws[1].UsedPercent != 6 {
		t.Errorf("5h = %+v, want 18000s used 6%%", ws[1])
	}
	if ws[1].ResetAfterSeconds != int((3*time.Hour + 4*time.Minute + 24*time.Second).Seconds()) {
		t.Errorf("5h reset after = %d", ws[1].ResetAfterSeconds)
	}
}

func TestMapQuotaSummary_ThirdPartyModelFirst(t *testing.T) {
	r := mapQuotaSummary(decodeSample(t), "claude-sonnet-4-6", time.Now())
	if r.Buckets[0].Name != "Claude and GPT models" {
		t.Fatalf("first bucket = %q", r.Buckets[0].Name)
	}
}

func TestModelFromArgs(t *testing.T) {
	if got := modelFromArgs([]string{"--model", "gemini-3.8-flash-high"}); got != "gemini-3.8-flash-high" {
		t.Errorf("got %q", got)
	}
	if got := modelFromArgs([]string{"--model=claude-opus-4-6-thinking"}); got != "claude-opus-4-6-thinking" {
		t.Errorf("got %q", got)
	}
}

func TestScanFileForPattern_SplitAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	secret := "GOCSPX-" + strings.Repeat("a", googleClientSecretBody)
	// Place the secret so it straddles the 4MiB read boundary.
	data := make([]byte, 4<<20+100)
	copy(data[4<<20-10:], secret)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := scanFileForPattern(path, googleClientSecretRe, googleClientSecretLen)
	if err != nil || len(got) != 1 || got[0] != secret {
		t.Fatalf("got %v, %v", got, err)
	}
}

func fakeIDToken(aud string) string {
	payload, _ := json.Marshal(map[string]string{"aud": aud})
	return "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
}

// End to end: expired file token -> refresh with the secret found in the
// "binary" (first candidate wrong, second right) -> quota call with UA.
func TestGetUsage_RefreshesTokenAndFetchesQuota(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(filepath.Join(cliDir, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cliDir, "cache", "default_project_id.txt"), []byte("proj-1\n"), 0o600)
	tokFile, _ := json.Marshal(map[string]any{
		"token":    map[string]string{"access_token": "old", "refresh_token": "rt", "expiry": "2026-10-13T18:53:14.98139+08:00"},
		"id_token": fakeIDToken("client-123.apps.googleusercontent.com"),
	})
	os.WriteFile(filepath.Join(cliDir, "antigravity-oauth-token"), tokFile, 0o600)

	bad := "GOCSPX-" + strings.Repeat("b", googleClientSecretBody)
	good := "GOCSPX-" + strings.Repeat("g", googleClientSecretBody)
	bin := filepath.Join(home, "agy")
	os.WriteFile(bin, []byte("junk "+bad+" junk "+good+" junk"), 0o700)

	var quotaCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			if r.Form.Get("client_id") != "client-123.apps.googleusercontent.com" || r.Form.Get("refresh_token") != "rt" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			if r.Form.Get("client_secret") != good {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"access_token":"fresh","expires_in":3599}`)
		case "/quota":
			quotaCalls.Add(1)
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("Authorization") != "Bearer fresh" || r.Header.Get("User-Agent") != antigravityUserAgent || !strings.Contains(string(body), `"proj-1"`) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			io.WriteString(w, sampleQuotaSummary)
		}
	}))
	defer srv.Close()

	a := &Agent{cmd: "agy", model: "gemini-3.8-flash-high", activeIdx: -1}
	a.quota.homeDir = home
	a.quota.binaryPath = bin
	a.quota.tokenURL = srv.URL + "/token"
	a.quota.quotaURL = srv.URL + "/quota"

	r, err := a.GetUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Provider != "antigravity" || r.Buckets[0].Name != "Gemini Models" {
		t.Fatalf("report = %+v", r)
	}
	// Second call answers from cache and refreshes in the background,
	// reusing the in-memory token (the token server would reject "old").
	if _, err := a.GetUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for quotaCalls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if quotaCalls.Load() != 2 {
		t.Errorf("quota calls = %d, want 2", quotaCalls.Load())
	}
	if a.quota.secret != good {
		t.Errorf("remembered secret = %q", a.quota.secret)
	}
}

func TestGetUsage_ProviderModeSkipsQuota(t *testing.T) {
	a := &Agent{cmd: "agy", providers: []core.ProviderConfig{{Name: "relay"}}, activeIdx: 0}
	if r, err := a.GetUsage(context.Background()); err == nil || r != nil {
		t.Fatalf("got %+v, %v; want error in provider mode", r, err)
	}
}

func TestGoogleClientSecretRegexShape(t *testing.T) {
	re := regexp.MustCompile(`^` + googleClientSecretRe.String() + `$`)
	if !re.MatchString("GOCSPX-" + strings.Repeat("Z", googleClientSecretBody)) {
		t.Fatal("regex rejects a well-formed secret")
	}
}

// Once a report is cached, GetUsage must answer at once and refresh in the
// background, so only the cold start ever waits on the network.
func TestGetUsage_WarmCacheReturnsImmediately(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(home, ".gemini", "antigravity-cli")
	os.MkdirAll(filepath.Join(cliDir, "cache"), 0o700)
	os.WriteFile(filepath.Join(cliDir, "cache", "default_project_id.txt"), []byte("p"), 0o600)
	tokFile, _ := json.Marshal(map[string]any{
		"token": map[string]string{"access_token": "live", "expiry": time.Now().Add(time.Hour).Format(time.RFC3339Nano)},
	})
	os.WriteFile(filepath.Join(cliDir, "antigravity-oauth-token"), tokFile, 0o600)

	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			io.WriteString(w, sampleQuotaSummary) // 5h remaining 0.9401266
			return
		}
		<-release // later fetches are slow
		io.WriteString(w, strings.Replace(sampleQuotaSummary, "0.9401266", "0.5", 1))
	}))
	defer srv.Close()
	defer close(release)

	a := &Agent{cmd: "agy", model: "gemini-3.8-flash-high", activeIdx: -1}
	a.quota.homeDir = home
	a.quota.quotaURL = srv.URL

	if _, err := a.GetUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	r, err := a.GetUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("warm call waited %v for the network", d)
	}
	if got := r.Buckets[0].Windows[1].UsedPercent; got != 6 {
		t.Errorf("warm call 5h used = %d, want cached 6", got)
	}
	release <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _ = a.GetUsage(context.Background())
		if r.Buckets[0].Windows[1].UsedPercent == 50 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background refresh never landed: %+v", r.Buckets[0].Windows)
}
