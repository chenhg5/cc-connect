package webex

import (
	"context"
	"testing"
)

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(map[string]any{}); err == nil {
		t.Fatal("expected error when token is missing")
	}
}

func TestNewParsesAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"token":      "abc",
		"allow_from": "A@x.com, b@x.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wp := p.(*Platform)
	if len(wp.allowFrom) != 2 {
		t.Fatalf("expected 2 allowed emails, got %d", len(wp.allowFrom))
	}
}

// stubClient implements webexClient for tests.
type stubClient struct {
	me          *person
	dev         *device
	msg         *message
	file        *downloadedFile
	posted      []postedMsg
	postedFiles []string
	deletedURL  string
}

type postedMsg struct {
	roomID, parentID, markdown string
}

func (s *stubClient) GetMe(context.Context) (*person, error)        { return s.me, nil }
func (s *stubClient) CreateDevice(context.Context) (*device, error) { return s.dev, nil }
func (s *stubClient) DeleteDevice(_ context.Context, url string) error {
	s.deletedURL = url
	return nil
}
func (s *stubClient) GetMessage(context.Context, string) (*message, error) { return s.msg, nil }
func (s *stubClient) DownloadFile(context.Context, string) (*downloadedFile, error) {
	return s.file, nil
}
func (s *stubClient) PostMessage(_ context.Context, roomID, parentID, markdown string) error {
	s.posted = append(s.posted, postedMsg{roomID, parentID, markdown})
	return nil
}
func (s *stubClient) PostFile(_ context.Context, roomID string, f *downloadedFile) error {
	s.postedFiles = append(s.postedFiles, roomID)
	return nil
}
