package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleSend_AllowsAttachmentOnly(t *testing.T) {
	engine := NewEngine("test", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}}
	reqBody := SendRequest{
		Project:    "test",
		SessionKey: "session-1",
		Images: []ImageAttachment{{
			MimeType: "image/png",
			Data:     []byte("img"),
			FileName: "chart.png",
		}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func apiPostJSON(t *testing.T, handler http.HandlerFunc, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func TestAPI_CronAdd_ValidatesEffort(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)
	agent := &effortRecordingAgent{
		session:         newResultAgentSession("ok"),
		availableEffort: []string{"low", "medium", "high", "max"},
	}
	engine := NewEngine("test-project", agent, nil, "", LangEnglish)
	defer engine.cancel()

	api := &APIServer{
		engines: map[string]*Engine{"test-project": engine},
		cron:    scheduler,
	}

	rr := apiPostJSON(t, api.handleCronAdd, "/cron/add", map[string]any{
		"project":     "test-project",
		"session_key": "user1",
		"cron_expr":   "0 9 * * *",
		"prompt":      "hello",
		"effort":      "turbo",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid reasoning effort") {
		t.Fatalf("body = %q, want invalid reasoning effort", rr.Body.String())
	}
}

func TestAPI_CronEdit_ValidatesEffortAndNormalizes(t *testing.T) {
	store, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(store)
	agent := &effortRecordingAgent{
		session:         newResultAgentSession("ok"),
		availableEffort: []string{"low", "medium", "high", "max"},
	}
	engine := NewEngine("test-project", agent, nil, "", LangEnglish)
	defer engine.cancel()

	api := &APIServer{
		engines: map[string]*Engine{"test-project": engine},
		cron:    scheduler,
	}

	if err := store.Add(&CronJob{
		ID:         "job1",
		Project:    "test-project",
		SessionKey: "user1",
		CronExpr:   "0 9 * * *",
		Prompt:     "hello",
		Enabled:    true,
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("store.Add() error = %v", err)
	}

	rr := apiPostJSON(t, api.handleCronEdit, "/cron/edit", map[string]any{
		"id":    "job1",
		"field": "effort",
		"value": "  MEDIUM  ",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	job := store.Get("job1")
	if job == nil || job.Effort != "medium" {
		t.Fatalf("job effort = %q, want medium", func() string {
			if job == nil {
				return "<nil>"
			}
			return job.Effort
		}())
	}
}
