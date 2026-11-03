package slack

import (
	"context"
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
		{in: "https://relay.example.com", want: "https://relay.example.com/"},
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

func TestRedactUserInfo(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:p@[":                  "https://REDACTED@[",
		"https://u:p@h/a@b":              "https://REDACTED@b",
		"https://relay.example.com/api/": "https://relay.example.com/api/",
		"u:p@h":                          "REDACTED@h",
	} {
		if got, _ := redactUserInfo(in); got != want {
			t.Errorf("redactUserInfo(%q) = %q, want %q", in, got, want)
		}
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
