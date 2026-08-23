package wecom

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestWorkspaceChatTransportOnlyWebSocketOptsIn(t *testing.T) {
	var platform core.Platform = &WSPlatform{}
	source, ok := platform.(core.WorkspaceChatTransportSource)
	if !ok {
		t.Fatal("WSPlatform does not expose the workspace-chat transport capability")
	}
	if got := source.WorkspaceChatTransport(); got != "wecom" {
		t.Fatalf("WSPlatform workspace transport = %q", got)
	}
	var webhook core.Platform = &Platform{}
	if _, ok := webhook.(core.WorkspaceChatTransportSource); ok {
		t.Fatal("Webhook Platform must remain in the platform-session product domain")
	}
}
