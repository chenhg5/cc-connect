package daxiangbridge

import "testing"

func TestPendingPermissions_ResolveApprove(t *testing.T) {
	pm := newPendingPermissions()
	ch := pm.register("perm_001", "req_001", "sess_001")
	go pm.resolve("perm_001", "approve")
	result := <-ch
	if result.decision != "approve" {
		t.Errorf("decision: got %q", result.decision)
	}
	if result.requestID != "req_001" {
		t.Errorf("requestID: got %q", result.requestID)
	}
}

func TestPendingPermissions_ResolveDeny(t *testing.T) {
	pm := newPendingPermissions()
	ch := pm.register("perm_002", "req_002", "sess_002")
	go pm.resolve("perm_002", "deny")
	result := <-ch
	if result.decision != "deny" {
		t.Errorf("decision: got %q", result.decision)
	}
}

func TestPendingPermissions_UnknownPermission_NoOp(t *testing.T) {
	pm := newPendingPermissions()
	pm.resolve("unknown", "approve") // must not panic
}
