package feishu

import (
	"errors"
	"testing"

	"github.com/timmyagentic/cc-connect-next/core"
)

func TestPlatform_ConnectionErrorStartsClean(t *testing.T) {
	p := &Platform{appID: "cli_test", domain: "feishu.cn"}
	if err := p.ConnectionError(); err != nil {
		t.Fatalf("ConnectionError() = %v, want nil before any connection attempt", err)
	}
	var _ core.PlatformHealth = p
}

func TestPlatform_ConnectionErrorSurvivesUntilTheNextAttempt(t *testing.T) {
	p := &Platform{appID: "cli_test", domain: "feishu.cn"}

	// The long connection dies after Start already returned nil — the case
	// where the process reports "running" and delivers nothing.
	wsErr := errors.New("1000040346: app_id is invalid")
	p.recordConnectionError(wsErr)
	if got := p.ConnectionError(); !errors.Is(got, wsErr) {
		t.Fatalf("ConnectionError() = %v, want %v", got, wsErr)
	}

	p.recordConnectionError(nil)
	if got := p.ConnectionError(); got != nil {
		t.Fatalf("ConnectionError() = %v, want nil after a fresh attempt started", got)
	}
}

func TestPlatform_ConnectionErrorReachesEverySharerOfTheConnection(t *testing.T) {
	cleanup := func() {
		sharedWSMu.Lock()
		defer sharedWSMu.Unlock()
		for k := range sharedWSGroups {
			delete(sharedWSGroups, k)
		}
	}
	cleanup()
	defer cleanup()

	primary := &Platform{appID: "cli_shared", domain: "feishu.cn"}
	secondary := &Platform{appID: "cli_shared", domain: "feishu.cn"}
	group, isPrimary := registerSharedWS(primary)
	primary.sharedGroup = group
	if !isPrimary {
		t.Fatal("first platform should own the connection")
	}
	group, _ = registerSharedWS(secondary)
	secondary.sharedGroup = group

	// Only the primary runs the WebSocket, but every project sharing it lost
	// the same connection.
	wsErr := errors.New("1000040346: app_id is invalid")
	primary.recordConnectionError(wsErr)

	if got := secondary.ConnectionError(); !errors.Is(got, wsErr) {
		t.Fatalf("secondary ConnectionError() = %v, want %v", got, wsErr)
	}
}
