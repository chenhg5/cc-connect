package slack

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeAPIURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "https://slack-gov.com/api/", want: "https://slack-gov.com/api/"},
		{in: "https://slack-gov.com/api", want: "https://slack-gov.com/api/"},
		{in: " https://relay.example.com/slack/api ", want: "https://relay.example.com/slack/api/"},
		{in: "http://127.0.0.1:8080/api", want: "http://127.0.0.1:8080/api/"},
		{in: "https://relay.example.com", want: "https://relay.example.com/api/"},
		{in: "https://slack-gov.com/", want: "https://slack-gov.com/api/"},
		{in: "https://relay.example.com/slack/", wantErr: true},
		{in: "https://slack-gov.com/apis/", wantErr: true},
		{in: "https://slack-gov.com/api/v2/", wantErr: true},
		// A bare trailing "?" would turn every SDK method into a query string.
		{in: "https://relay.example.com/api/?", wantErr: true},
		{in: "https://relay.example.com/api?", wantErr: true},
		{in: "ftp://slack.com/api/", wantErr: true},
		{in: "slack-gov.com/api/", wantErr: true},
		{in: "https:///api/", wantErr: true},
		{in: "https://slack-gov.com/api/?x=1", wantErr: true},
		{in: "https://slack-gov.com/api/#frag", wantErr: true},
		{in: "://bad", wantErr: true},
		{in: "https://user:secret@relay.example.com/api/", wantErr: true},
	}
	for _, c := range cases {
		got, err := normalizeAPIURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeAPIURL(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeAPIURL(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeAPIURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeAPIURL_MalformedCredentialsNotEchoed(t *testing.T) {
	for _, in := range []string{
		"https://user:sup3rsecret@[",
		"https://user:sup3rsecret@host:notaport/api/",
		"https://user:sup3rsecret%zz@relay.example.com/api/",
		"https://sup3rsecret@[::1/api/",
		"https://user:sup3r/secret@host:notaport/api/",
		// No "@": the parser reads the secret as a port, so it isn't user
		// info, but it is still what the operator meant as a password.
		"https://user:sup3rsecret",
		"https://user:sup3rsecret:notaport",
		"https://user:sup3rsecret:notaport/api/",
	} {
		_, err := normalizeAPIURL(in)
		if err == nil {
			t.Errorf("normalizeAPIURL(%q): want error", in)
			continue
		}
		if strings.Contains(err.Error(), "sup3r") {
			t.Errorf("normalizeAPIURL(%q): error echoes the credentials: %v", in, err)
		}
	}
}

// TestNormalizeAPIURL_NoErrorPathEchoesSecrets feeds every validation branch
// a value that fails there while carrying a secret, and checks the secret
// never reaches the error.
func TestNormalizeAPIURL_NoErrorPathEchoesSecrets(t *testing.T) {
	for _, in := range []string{
		"token:sup3rsecret@relay.example.com",               // opaque, fails the scheme check
		"token:sup3r?secret@relay.example.com",              // "?" inside the secret
		"ftp://relay.example.com/api/?token=sup3rsecret",    // scheme check, secret in query
		"https:///api/?token=sup3rsecret",                   // no host, secret in query
		"https://relay.example.com/api/?token=sup3rsecret",  // query check
		"https://relay.example.com/api/#sup3rsecret",        // fragment check
		"https://user:sup3rsecret@relay.example.com/api/",   // user info check
		"https://user:sup3rsecret",                          // parse error, no "@"
		"https://relay.example.com/sup3rsecret",             // path check, secret in path
		"ftp://relay.example.com/sup3rsecret/api/",          // scheme check, secret before /api/
		"https://host:badport/sup3rsecret",                  // parse error, secret in path
		"https://relay.example.com/sup3rsecret/api/?x=1",    // query check, secret before /api/
		"https://relay.example.com/api/@sup3rsecret%zz",     // parse error, "@" in the path (#2247 review)
		"https://u:p@relay.example.com/api/@sup3rsecret%zz", // parse error, "@" in both
	} {
		_, err := normalizeAPIURL(in)
		if err == nil {
			t.Errorf("normalizeAPIURL(%q): want error", in)
			continue
		}
		if strings.Contains(err.Error(), "sup3r") {
			t.Errorf("normalizeAPIURL(%q): error echoes the secret: %v", in, err)
		}
	}
}

func TestLogSafe(t *testing.T) {
	for in, want := range map[string]string{
		"https://relay.example.com/api/?token=x": "https://REDACTED/api/?<omitted>",
		"https://relay.example.com/api/#frag":    "https://REDACTED/api/?<omitted>",
		"token:secret@relay.example.com":         "REDACTED",
		"token:sec?ret@relay.example.com":        "REDACTED?<omitted>",
		"https://u:p@h/api/":                     "https://REDACTED/api/",
		"ftp://slack-gov.com/api/":               "ftp://REDACTED/api/",
	} {
		if got := logSafe(in); got != want {
			t.Errorf("logSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeAPIURL_ErrorsShowHost checks the other half of the rule: for a
// value that parsed, the host stays visible so the operator can see a typo.
func TestNormalizeAPIURL_ErrorsShowHost(t *testing.T) {
	for _, in := range []string{
		"ftp://relay.example.com/api/",
		"https://relay.example.com/api/?x=1",
		"https://relay.example.com/wrong/",
	} {
		_, err := normalizeAPIURL(in)
		if err == nil || !strings.Contains(err.Error(), "relay.example.com") {
			t.Errorf("normalizeAPIURL(%q): err = %v, want it to show the host", in, err)
		}
	}
}

func TestSafePath(t *testing.T) {
	for in, want := range map[string]string{
		"":             "",
		"/":            "/",
		"/api":         "/api",
		"/api/":        "/api/",
		"/slack/api/":  "/<omitted>/api/",
		"/sup3r/api":   "/<omitted>/api/",
		"/sup3rsecret": "/<path omitted>",
		"/api/v2/":     "/<path omitted>",
	} {
		if got := safePath(in); got != want {
			t.Errorf("safePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNew_StartupLogHidesPath(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)
	_, err := New(map[string]any{"bot_token": "xoxb-test", "app_token": "xapp-test",
		"base_url": "https://relay.example.com/sup3rsecret/api/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logged := buf.String()
	if strings.Contains(logged, "sup3r") {
		t.Errorf("startup log echoes the path secret: %q", logged)
	}
	if !strings.Contains(logged, "relay.example.com") {
		t.Errorf("startup log should still show the host: %q", logged)
	}
}

func TestRedactAuthority(t *testing.T) {
	// Only the authority (scheme to the next "/") is masked; the path is left
	// for safePath, so an "@" never decides where the mask ends.
	for in, want := range map[string]string{
		"https://u:p@[":                     "https://REDACTED",
		"https://u:p@h/a@b":                 "https://REDACTED/a@b",
		"u:p@h":                             "REDACTED",
		"https://user:sup3rsecret":          "https://REDACTED",
		"https://host:notaport/api/":        "https://REDACTED/api/",
		"https://relay.example.com/api/%zz": "https://REDACTED/api/%zz",
		"%zz":                               "REDACTED",
	} {
		if got := redactAuthority(in); got != want {
			t.Errorf("redactAuthority(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsKnownSlackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"slack.com":               true,
		"slack-gov.com":           true,
		"SLACK-GOV.COM":           true,
		"slack-gov.com.":          true,
		"api.slack.com":           true,
		"slack.com.example.test":  false,
		"evilslack.com":           false,
		"slack.corp.example.com":  false,
		"relay.example.com":       false,
		"slack-gov.com.evil.test": false,
	} {
		if got := isKnownSlackHost(host); got != want {
			t.Errorf("isKnownSlackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// captureWarnings runs fn with the default slog logger writing to a buffer
// and returns what was logged at WARN or above.
func captureWarnings(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

func TestNormalizeAPIURL_HostWarnings(t *testing.T) {
	cases := []struct {
		in       string
		wantWarn string // substring expected in the log; "" means no warning
	}{
		{in: "https://slack-gov.com/api/", wantWarn: ""},
		{in: "https://api.slack.com/api/", wantWarn: ""},
		{in: "http://127.0.0.1:8080/api/", wantWarn: ""},
		{in: "https://localhost/api/", wantWarn: ""},
		{in: "https://slack.com.example.test/api/", wantWarn: "not a Slack domain"},
		{in: "https://relay.example.com/slack/api/", wantWarn: "not a Slack domain"},
		{in: "http://relay.example.com/api/", wantWarn: "plain http"},
	}
	for _, c := range cases {
		var err error
		logged := captureWarnings(t, func() { _, err = normalizeAPIURL(c.in) })
		if err != nil {
			t.Errorf("normalizeAPIURL(%q): unexpected error %v", c.in, err)
			continue
		}
		if c.wantWarn == "" && logged != "" {
			t.Errorf("normalizeAPIURL(%q): unexpected warning %q", c.in, logged)
		}
		if c.wantWarn != "" && !strings.Contains(logged, c.wantWarn) {
			t.Errorf("normalizeAPIURL(%q): log %q, want a warning containing %q", c.in, logged, c.wantWarn)
		}
	}
}

func TestAPIURLOption(t *testing.T) {
	for _, v := range []any{42, []string{"https://slack-gov.com/api/"}, map[string]any{"url": "x"}, true} {
		_, err := apiURLOption(map[string]any{"base_url": v})
		if err == nil || !strings.Contains(err.Error(), "must be a string") {
			t.Errorf("apiURLOption(base_url=%#v): err = %v, want a type error", v, err)
		}
	}
	for _, opts := range []map[string]any{{}, {"base_url": nil}, {"base_url": ""}} {
		got, err := apiURLOption(opts)
		if err != nil || got != "" {
			t.Errorf("apiURLOption(%v) = %q, %v; want SDK default", opts, got, err)
		}
	}
	got, err := apiURLOption(map[string]any{"base_url": "https://slack-gov.com"})
	if err != nil || got != "https://slack-gov.com/api/" {
		t.Errorf("apiURLOption(bare host) = %q, %v; want https://slack-gov.com/api/", got, err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":   true,
		"127.0.0.1":   true,
		"::1":         true,
		"10.0.0.5":    false,
		"example.com": false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestNew_BaseURL(t *testing.T) {
	base := map[string]any{"bot_token": "xoxb-test", "app_token": "xapp-test"}

	p, err := New(base)
	if err != nil {
		t.Fatalf("New without base_url: %v", err)
	}
	if got := p.(*Platform).apiURL; got != "" {
		t.Errorf("apiURL without base_url = %q, want empty (SDK default)", got)
	}

	withURL := map[string]any{"bot_token": "xoxb-test", "app_token": "xapp-test",
		"base_url": "https://slack-gov.com/api"}
	p, err = New(withURL)
	if err != nil {
		t.Fatalf("New with base_url: %v", err)
	}
	if got := p.(*Platform).apiURL; got != "https://slack-gov.com/api/" {
		t.Errorf("apiURL = %q, want %q", got, "https://slack-gov.com/api/")
	}

	bad := map[string]any{"bot_token": "xoxb-test", "app_token": "xapp-test",
		"base_url": "ftp://slack-gov.com/api/"}
	if _, err := New(bad); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("New with invalid base_url: err = %v, want a base_url error", err)
	}

	wrongType := map[string]any{"bot_token": "xoxb-test", "app_token": "xapp-test",
		"base_url": 42}
	if _, err := New(wrongType); err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Errorf("New with non-string base_url: err = %v, want a type error", err)
	}

	creds := map[string]any{"bot_token": "xoxb-test", "app_token": "xapp-test",
		"base_url": "https://user:secret@relay.example.com/api/"}
	if _, err := New(creds); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("New with credentials in base_url: err = %v, want an error that does not echo them", err)
	}
}

// TestNewClient_UsesBaseURL checks that both the Web API and the Socket Mode
// handshake (apps.connections.open) go to base_url, so one option covers both.
func TestNewClient_UsesBaseURL(t *testing.T) {
	var gotAuthTest, gotConnOpen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth.test":
			// The SDK sends the bot token for auth.test as a form field.
			gotAuthTest = r.FormValue("token")
			_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
		case "/api/apps.connections.open":
			gotConnOpen = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"ok":true,"url":"wss://relay.example.com/link"}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	apiURL, err := normalizeAPIURL(srv.URL + "/api")
	if err != nil {
		t.Fatalf("normalizeAPIURL: %v", err)
	}
	p := &Platform{botToken: "xoxb-test", appToken: "xapp-test", apiURL: apiURL}
	client := p.newClient()

	if _, err := client.AuthTestContext(context.Background()); err != nil {
		t.Fatalf("AuthTest via base_url: %v", err)
	}
	if gotAuthTest != "xoxb-test" {
		t.Errorf("auth.test token = %q, want bot token", gotAuthTest)
	}

	_, wsURL, err := client.StartSocketModeContext(context.Background())
	if err != nil {
		t.Fatalf("apps.connections.open via base_url: %v", err)
	}
	if gotConnOpen != "Bearer xapp-test" {
		t.Errorf("apps.connections.open Authorization = %q, want app token", gotConnOpen)
	}
	if wsURL != "wss://relay.example.com/link" {
		t.Errorf("websocket URL = %q, want the one returned by base_url", wsURL)
	}
}
