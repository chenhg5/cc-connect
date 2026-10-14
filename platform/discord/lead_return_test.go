package discord

import "testing"

func TestReturnToLeadPrependsOncePerTrigger(t *testing.T) {
	p := &Platform{}
	rc := replyContext{channelID: "c", messageID: "m1", agent: true, lead: "L"}
	if got := p.returnToLead(rc, "已完成，交橘验收"); got != "<@L> 已完成，交橘验收" {
		t.Fatalf("first post: %q", got)
	}
	if got := p.returnToLead(rc, "第二段"); got != "第二段" {
		t.Fatalf("second post of same turn must not ping again: %q", got)
	}
	rc2 := rc
	rc2.messageID = "m2"
	if got := p.returnToLead(rc2, "新的活"); got != "<@L> 新的活" {
		t.Fatalf("new dispatch: %q", got)
	}
}

func TestReturnToLeadSkips(t *testing.T) {
	p := &Platform{}
	cases := []struct {
		name string
		rc   replyContext
		in   string
	}{
		{"not agent output", replyContext{messageID: "a", lead: "L"}, "↪ 已插入"},
		{"not dispatched by lead", replyContext{messageID: "b", agent: true}, "hi"},
		{"already mentions lead", replyContext{messageID: "c", agent: true, lead: "L"}, "<@L> 好了"},
		{"already mentions lead (nick form)", replyContext{messageID: "d", agent: true, lead: "L"}, "<@!L> 好了"},
		{"empty", replyContext{messageID: "e", agent: true, lead: "L"}, "  "},
	}
	for _, c := range cases {
		if got := p.returnToLead(c.rc, c.in); got != c.in {
			t.Errorf("%s: got %q", c.name, got)
		}
	}
	// a turn that already @-ed the lead does not get a second ping later
	if got := p.returnToLead(replyContext{messageID: "c", agent: true, lead: "L"}, "补充"); got != "补充" {
		t.Errorf("after explicit @: %q", got)
	}
}
