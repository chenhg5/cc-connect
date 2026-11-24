package feishu

import (
	"testing"
	"time"
)

func TestTrackCardActionMsg_PrunesExpiredEntries(t *testing.T) {
	p := &Platform{
		cardActionMsgIDs: map[string]string{
			"old":   "om_old",
			"fresh": "om_fresh",
		},
		cardActionMsgAt: map[string]time.Time{
			"old":   time.Now().Add(-2 * cardActionMsgTTL),
			"fresh": time.Now(),
		},
	}

	p.trackCardActionMsg("new", "om_new")

	p.cardActionMsgMu.Lock()
	defer p.cardActionMsgMu.Unlock()
	if _, ok := p.cardActionMsgIDs["old"]; ok {
		t.Fatal("expired card action message was not pruned")
	}
	if p.cardActionMsgIDs["fresh"] != "om_fresh" {
		t.Fatal("fresh card action message was pruned")
	}
	if p.cardActionMsgIDs["new"] != "om_new" {
		t.Fatal("new card action message was not tracked")
	}
}

func TestTrackCardActionMsg_CapsMapSize(t *testing.T) {
	p := &Platform{
		cardActionMsgIDs: make(map[string]string, cardActionMsgMax),
		cardActionMsgAt:  make(map[string]time.Time, cardActionMsgMax),
	}
	now := time.Now()
	for i := 0; i < cardActionMsgMax; i++ {
		key := "session-" + string(rune(i))
		p.cardActionMsgIDs[key] = "om_" + key
		p.cardActionMsgAt[key] = now
	}

	p.trackCardActionMsg("latest", "om_latest")

	p.cardActionMsgMu.Lock()
	defer p.cardActionMsgMu.Unlock()
	if got := len(p.cardActionMsgIDs); got != cardActionMsgMax {
		t.Fatalf("tracked card messages = %d, want cap %d", got, cardActionMsgMax)
	}
	if p.cardActionMsgIDs["latest"] != "om_latest" {
		t.Fatal("latest card action message was not tracked")
	}
}
