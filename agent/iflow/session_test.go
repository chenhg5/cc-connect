package iflow

import (
	"os"
	"testing"
)

func TestReadExecutionInfoSessionID(t *testing.T) {
	f, err := os.CreateTemp("", "iflow-exec-info-*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())

	if _, err := f.WriteString(`{"session-id":"session-123"}`); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()

	sid, err := readExecutionInfoSessionID(f.Name())
	if err != nil {
		t.Fatalf("readExecutionInfoSessionID: %v", err)
	}
	if sid != "session-123" {
		t.Fatalf("session id = %q, want session-123", sid)
	}
}

func TestExtractSessionIDFromExecutionInfo(t *testing.T) {
	stderr := `<Execution Info>
{
  "session-id": "session-abc",
  "conversation-id": "cid"
}
</Execution Info>`
	if got := extractSessionIDFromExecutionInfo(stderr); got != "session-abc" {
		t.Fatalf("extractSessionIDFromExecutionInfo = %q", got)
	}
}

func TestIsIFlowAPIFailure(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"Error when talking to iFlow API", true},
		{"Attempt 1 failed. Retrying with backoff... Error: generate data error: fetch failed", true},
		{"normal log only", false},
	}

	for _, tc := range cases {
		if got := isIFlowAPIFailure(tc.in); got != tc.want {
			t.Fatalf("isIFlowAPIFailure(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSummarizeIFlowError(t *testing.T) {
	err := summarizeIFlowError("Error when talking to iFlow API\n\n<Execution Info>", nil)
	if err == nil || err.Error() != "Error when talking to iFlow API" {
		t.Fatalf("unexpected error summary: %v", err)
	}
}
