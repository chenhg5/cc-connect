package cloudweb

import "testing"

// mustNew builds a Platform from opts and fails the test if construction
// errors. Used by transport tests that only exercise the success path.
func mustNew(t *testing.T, opts map[string]any) *Platform {
	t.Helper()
	pAny, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return pAny.(*Platform)
}
