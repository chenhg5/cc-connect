package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIShutdownRequiresPostAndAnOwnerHandler(t *testing.T) {
	api := &APIServer{}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := httptest.NewRecorder()
		api.handleShutdown(response, httptest.NewRequest(method, "/shutdown", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, response.Code)
		}
	}
	response := httptest.NewRecorder()
	api.handleShutdown(response, httptest.NewRequest(http.MethodPost, "/shutdown", nil))
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("shutdown without an owner status = %d, want 501", response.Code)
	}
}

func TestAPIShutdownRespondsBeforeNotifyingOwnerOnce(t *testing.T) {
	api := &APIServer{}
	var calls atomic.Int32
	responses := make(chan *httptest.ResponseRecorder, 1)
	observed := make(chan bool, 1)
	api.SetShutdownHandler(func() {
		calls.Add(1)
		response := <-responses
		var result map[string]string
		err := json.Unmarshal(response.Body.Bytes(), &result)
		observed <- response.Code == http.StatusAccepted && response.Flushed && err == nil && result["status"] == "accepted"
	})
	first := httptest.NewRecorder()
	responses <- first
	api.handleShutdown(first, httptest.NewRequest(http.MethodPost, "/shutdown", nil))
	select {
	case ok := <-observed:
		if !ok {
			t.Fatal("owner notified before the accepted response was flushed")
		}
	case <-time.After(time.Second):
		t.Fatal("owner was not notified")
	}
	second := httptest.NewRecorder()
	api.handleShutdown(second, httptest.NewRequest(http.MethodPost, "/shutdown", nil))
	if second.Code != http.StatusAccepted || calls.Load() != 1 {
		t.Fatalf("repeat status/calls = %d/%d, want 202/1", second.Code, calls.Load())
	}
}
