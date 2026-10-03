package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type desktopHistoryAgent struct{ stubAgent }

func (a *desktopHistoryAgent) ValidateDesktopThread(id string) error {
	if id != "thread" {
		return context.Canceled
	}
	return nil
}

func TestDesktopNotificationDoesNotSwitchAndScopesRoute(t *testing.T) {
	agent := &desktopHistoryAgent{}
	RegisterAgent("stub", func(map[string]any) (Agent, error) { return agent, nil })
	engine := NewEngine("example", agent, nil, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	api := &APIServer{engines: map[string]*Engine{"example": engine}}
	request := SendRequest{Project: "example", SessionKey: "test:owner", WorkDir: t.TempDir(), ReplyThreadID: "thread"}
	body, _ := json.Marshal(request)
	rec := httptest.NewRecorder()
	api.handleSend(rec, httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if engine.sendWorkDirForSession("test:owner") != "" {
		t.Fatal("notification switched mobile session")
	}
	if _, ok := engine.threadRoute("test:owner", "thread"); !ok {
		t.Fatal("not registered")
	}
	if _, ok := engine.threadRoute("test:other", "thread"); ok {
		t.Fatal("route leaked across destinations")
	}
	request.ReplyThreadID = "missing"
	body, _ = json.Marshal(request)
	rec = httptest.NewRecorder()
	api.handleSend(rec, httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatal("unknown thread accepted")
	}
	text, err := engine.desktopNotice(SendRequest{ReplyThreadID: "thread", DesktopEvent: "progress", Message: strings.Repeat("full", 2000)})
	if err != nil || !strings.Contains(text, strings.Repeat("full", 2000)) || !strings.Contains(text, "thread") {
		t.Fatal("notice lost content or thread ID")
	}
	if _, err := engine.desktopNotice(SendRequest{ReplyThreadID: "thread", DesktopEvent: "invalid"}); err == nil {
		t.Fatal("unknown event accepted")
	}
}
