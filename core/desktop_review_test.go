package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func (a *desktopHistoryAgent) ValidateDesktopThreadAt(id, _ string) error {
	return a.ValidateDesktopThread(id)
}

func desktopRegistration(t *testing.T, api *APIServer, session string, expiry int64) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"project": "example", "session_key": session, "work_dir": t.TempDir(), "reply_thread_id": "thread", "desktop_route_expires_at": expiry})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	api.handleSend(rec, httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body)))
	return rec
}

func TestDesktopNotificationValidatesWithoutCreatingWorkspace(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			var agent Agent = &stubAgent{}
			if supported {
				agent = &desktopHistoryAgent{}
			}
			e := NewEngine("example", agent, nil, "", LangEnglish)
			rec := desktopRegistration(t, &APIServer{engines: map[string]*Engine{"example": e}}, "test:owner", 0)
			want := http.StatusConflict
			if supported {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatal(rec.Code, rec.Body.String())
			}
			if e.workspacePool != nil {
				t.Fatal("notification materialized a workspace agent")
			}
		})
	}
}

func TestDesktopNotificationRejectsStaleOrUnboundedExpiry(t *testing.T) {
	e := NewEngine("example", &desktopHistoryAgent{}, nil, "", LangEnglish)
	api := &APIServer{engines: map[string]*Engine{"example": e}}
	for _, expiry := range []int64{time.Now().Add(-time.Minute).Unix(), time.Now().Add(48 * time.Hour).Unix()} {
		if rec := desktopRegistration(t, api, "test:owner", expiry); rec.Code != http.StatusConflict {
			t.Fatal("invalid expiry accepted", expiry, rec.Code)
		}
	}
	if _, ok := e.threadRoute("test:owner", "thread"); ok {
		t.Fatal("invalid expiry granted access")
	}
}

func TestDesktopNotificationExpiresAndPrunesRoutes(t *testing.T) {
	e := NewEngine("example", &desktopHistoryAgent{}, nil, "", LangEnglish)
	api := &APIServer{engines: map[string]*Engine{"example": e}}
	expiry := time.Now().Unix() + 2
	for _, session := range []string{"test:owner", "test:other"} {
		if rec := desktopRegistration(t, api, session, expiry); rec.Code != http.StatusOK {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if _, ok := e.threadRoute(session, "thread"); !ok {
			t.Fatal("live route rejected")
		}
	}
	time.Sleep(time.Until(time.Unix(expiry, 0).Add(20 * time.Millisecond)))
	if _, ok := e.threadRoute("test:owner", "thread"); ok {
		t.Fatal("expired route still grants access")
	}
	e.desktopThreads.Range(func(_, _ any) bool {
		t.Error("expired route was not pruned")
		return true
	})
}
