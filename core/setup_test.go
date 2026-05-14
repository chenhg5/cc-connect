package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleSetupWeixinPoll_InvalidAPIURL guards against a nil-pointer panic
// when the caller passes an api_url that net/url.Parse rejects (e.g.
// "://bad", "%zz", "http://[::1"). The handler used to do
//
//	u, _ := url.Parse(apiBase + "/")
//	u = u.JoinPath(...)   // panic when u is nil
//
// and rely on http.Server's per-request recover to turn the panic into an
// opaque 500. The sibling handleSetupWeixinBegin already validates the URL,
// so we expect the same 400 path here.
func TestHandleSetupWeixinPoll_InvalidAPIURL(t *testing.T) {
	m := &ManagementServer{}

	for _, apiURL := range []string{"://bad", "%zz", "http://[::1"} {
		t.Run(apiURL, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{
				"qr_key":  "abc",
				"api_url": apiURL,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/weixin/poll", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			m.handleSetupWeixinPoll(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d. body=%q", w.Code, http.StatusBadRequest, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "invalid api_url") {
				t.Errorf("body does not mention invalid api_url: %q", w.Body.String())
			}
		})
	}
}
