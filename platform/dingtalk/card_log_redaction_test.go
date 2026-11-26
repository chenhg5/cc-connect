package dingtalk

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAICardFailuresOmitResponseSecrets(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	for _, tc := range []struct {
		name string
		run  func(*Platform) error
	}{
		{"create", func(p *Platform) error {
			_, err := p.createAICard(context.Background(), replyContext{})
			return err
		}},
		{"stream", func(p *Platform) error {
			card := &aiCard{platform: p, outTrackId: "track", done: make(chan struct{})}
			return card.doStream(context.Background(), "content", false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(`{"code":"denied","message":"not allowed","secret":"` + marker + `"}`)), Header: make(http.Header)}, nil
			})}
			p := &Platform{httpClient: client, accessToken: "test", tokenExpiry: time.Now().Add(time.Hour)}
			err := tc.run(p)
			if err == nil || !strings.Contains(err.Error(), "502") {
				t.Fatalf("card error = %v, want HTTP status", err)
			}
			if strings.Contains(err.Error(), marker) || strings.Contains(logs.String(), marker) {
				t.Fatal("DingTalk card error exposed response secret")
			}
		})
	}
}

func TestAICardResponseReadIsBounded(t *testing.T) {
	reader := &countingReader{remaining: 1 << 20}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(reader), Header: make(http.Header)}, nil
	})}
	p := &Platform{httpClient: client, accessToken: "test", tokenExpiry: time.Now().Add(time.Hour)}
	_, _ = p.createAICard(context.Background(), replyContext{})
	if reader.read > 8<<10 {
		t.Fatalf("read %d response bytes, want at most 8 KiB", reader.read)
	}
}

func TestAICardDeliveryErrorKeepsSafeMessage(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"result":{"cardInstanceId":"card","deliverResults":[{"success":false,"errorMsg":"quota exceeded ` + marker + `"}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p := &Platform{httpClient: client, accessToken: "test", clientSecret: marker, tokenExpiry: time.Now().Add(time.Hour)}
	_, err := p.createAICard(context.Background(), replyContext{})
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") || !strings.Contains(logs.String(), "quota exceeded") {
		t.Fatalf("safe diagnostic was lost: error=%v logs=%q", err, logs.String())
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(logs.String(), marker) {
		t.Fatal("delivery error exposed a credential")
	}
}

type countingReader struct {
	remaining int
	read      int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}
