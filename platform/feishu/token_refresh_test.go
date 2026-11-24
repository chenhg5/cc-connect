package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFetchFreshTenantAccessToken_SingleFlight(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":                0,
			"msg":                 "ok",
			"tenant_access_token": "t-single",
			"expire":              7200,
		})
	}))
	defer srv.Close()

	p := &Platform{appID: "cli_test", appSecret: "secret", domain: srv.URL}

	const callers = 10
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := p.fetchFreshTenantAccessToken(context.Background())
			if err != nil || token != "t-single" {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Fatalf("token endpoint hits = %d, want 1 (single-flight)", got)
	}
	if got := failures.Load(); got != 0 {
		t.Fatalf("%d/%d callers got an error or wrong token", got, callers)
	}
}
