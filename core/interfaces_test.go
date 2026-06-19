package core

import "testing"

type fakeHandle struct{ id string }

func (h *fakeHandle) MessageID() string { return h.id }

func TestMessageHandleIdentifierContract(t *testing.T) {
	var _ MessageHandleIdentifier = (*fakeHandle)(nil)

	h := &fakeHandle{id: "om_test"}
	var i any = h
	ident, ok := i.(MessageHandleIdentifier)
	if !ok {
		t.Fatalf("*fakeHandle should implement MessageHandleIdentifier")
	}
	if got := ident.MessageID(); got != "om_test" {
		t.Errorf("MessageID() = %q, want %q", got, "om_test")
	}
}