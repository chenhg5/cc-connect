package qq

import (
	"strings"
	"testing"
)

func TestFailedWebSocketConnectionDoesNotExposeConfiguredURL(t *testing.T) {
	const secret = "CONFIG-URL-SENTINEL"
	p, err := New(map[string]any{"ws_url": "ws://127.0.0.1:1/private-" + secret + "?access_token=" + secret})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Start(nil)
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("websocket connection error exposed configured URL")
	}
}
