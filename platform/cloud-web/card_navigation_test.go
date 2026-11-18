package cloudweb

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestCardNavigationUsesCommandIdentity(t *testing.T) {
	// No transport or SDK client: this callback must stay entirely in memory.
	p := &Platform{}
	var gotAction, gotKey, gotUserID string
	p.SetUserAwareCardNavigationHandler(func(action, sessionKey, userID string) *core.Card {
		gotAction, gotKey, gotUserID = action, sessionKey, userID
		return nil
	})
	raw := []byte(`{"type":"card_action","session_key":"cloud_web:shared","action":"nav:/help"}`)
	p.handleCardAction(raw)
	if gotAction != "nav:/help" || gotKey != "cloud_web:shared" || gotUserID != "system" {
		t.Fatalf("navigation = (%q, %q, %q), want (nav:/help, cloud_web:shared, system)", gotAction, gotKey, gotUserID)
	}
	gotAction, gotKey = "", ""
	p.SetCardNavigationHandler(func(action, sessionKey string) *core.Card {
		gotAction, gotKey = action, sessionKey
		return nil
	})
	p.handleCardAction(raw)
	if gotAction != "nav:/help" || gotKey != "cloud_web:shared" {
		t.Fatalf("legacy navigation = (%q, %q)", gotAction, gotKey)
	}
}
