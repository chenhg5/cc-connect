package wecom

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWeComAPIURL_DefaultBase(t *testing.T) {
	p := &Platform{}
	got := p.wecomAPIURL("/cgi-bin/gettoken", url.Values{
		"corpid":     []string{"ww-test"},
		"corpsecret": []string{"sec-test"},
	})
	want := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww-test&corpsecret=sec-test"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestWeComAPIURL_CustomBase(t *testing.T) {
	p := &Platform{apiBaseURL: "https://wecom.internal.example.com/"}
	got := p.wecomAPIURL("/cgi-bin/message/send", url.Values{
		"access_token": []string{"tok"},
	})
	want := "https://wecom.internal.example.com/cgi-bin/message/send?access_token=tok"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestNew_DefaultAPIBaseURL(t *testing.T) {
	pf, err := New(map[string]any{
		"corp_id":          "ww_test",
		"corp_secret":      "sec_test",
		"agent_id":         "1000002",
		"callback_token":   "cb_token",
		"callback_aes_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	p, ok := pf.(*Platform)
	if !ok {
		t.Fatalf("platform type = %T, want *wecom.Platform", pf)
	}
	if p.apiBaseURL != defaultAPIBaseURL {
		t.Fatalf("apiBaseURL = %q, want %q", p.apiBaseURL, defaultAPIBaseURL)
	}
}

func TestNew_CustomAPIBaseURL_TrimTrailingSlash(t *testing.T) {
	pf, err := New(map[string]any{
		"corp_id":          "ww_test",
		"corp_secret":      "sec_test",
		"agent_id":         "1000002",
		"callback_token":   "cb_token",
		"callback_aes_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"api_base_url":     "https://wecom.internal.example.com/",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	p, ok := pf.(*Platform)
	if !ok {
		t.Fatalf("platform type = %T, want *wecom.Platform", pf)
	}
	if p.apiBaseURL != "https://wecom.internal.example.com" {
		t.Fatalf("apiBaseURL = %q, want %q", p.apiBaseURL, "https://wecom.internal.example.com")
	}
}

type recordingRoundTripper struct {
	mu    sync.Mutex
	bodys [][]byte
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	rt.bodys = append(rt.bodys, append([]byte(nil), body...))
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)),
	}, nil
}

func (rt *recordingRoundTripper) LastBody() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.bodys) == 0 {
		return ""
	}
	return string(rt.bodys[len(rt.bodys)-1])
}

func TestReply_StripsANSISequences(t *testing.T) {
	tests := []struct {
		name           string
		enableMarkdown bool
		wantMsgType    string
		wantContent    string
	}{
		{name: "markdown", enableMarkdown: true, wantMsgType: `"msgtype":"markdown"`, wantContent: `status **failed**`},
		{name: "text", enableMarkdown: false, wantMsgType: `"msgtype":"text"`, wantContent: `status failed`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &recordingRoundTripper{}
			p := &Platform{
				agentID:        "1000001",
				enableMarkdown: tt.enableMarkdown,
				apiClient:      &http.Client{Transport: rt},
				tokenCache: tokenCache{
					token:     "token",
					expiresAt: time.Now().Add(time.Hour),
				},
			}

			err := p.Reply(context.Background(), replyContext{userID: "alice"}, "status **\x1b[31mfailed\x1b[0m**")
			if err != nil {
				t.Fatalf("Reply() error = %v", err)
			}

			body := rt.LastBody()
			if body == "" {
				t.Fatal("Reply() did not send request body")
			}
			if strings.Contains(body, "\u001b") || bytes.Contains([]byte(body), []byte{0x1b}) {
				t.Fatalf("Reply() leaked ANSI sequence: %q", body)
			}
			if !strings.Contains(body, tt.wantMsgType) {
				t.Fatalf("Reply() body = %q, want msg type %q", body, tt.wantMsgType)
			}
			if !strings.Contains(body, tt.wantContent) {
				t.Fatalf("Reply() body = %q, want content %q", body, tt.wantContent)
			}
		})
	}
}

