package daxiangbridge

import (
	"encoding/json"
	"testing"
)

func TestBuildFinalReplyFrame(t *testing.T) {
	frame := buildFinalReplyFrame("req_001", "sess_001", "这是回复")
	if frame.Type != FrameTypeAgentReplyFinal {
		t.Errorf("type: got %q", frame.Type)
	}
	var p AgentReplyPayload
	json.Unmarshal(frame.Payload, &p)
	if p.Text != "这是回复" {
		t.Errorf("text: got %q", p.Text)
	}
}

func TestBuildDeltaFrame(t *testing.T) {
	frame := buildDeltaFrame("req_001", "sess_001", 5, "hello")
	var p AgentDeltaPayload
	json.Unmarshal(frame.Payload, &p)
	if p.Seq != 5 {
		t.Errorf("seq: got %d", p.Seq)
	}
}

func TestBuildPermissionRequestFrame(t *testing.T) {
	frame := buildPermissionRequestFrame("req_001", "sess_001", "perm_001", "Bash", "git status", "check repo")
	if frame.Type != FrameTypeAgentPermissionRequest {
		t.Errorf("type: got %q", frame.Type)
	}
	var p AgentPermissionRequestPayload
	json.Unmarshal(frame.Payload, &p)
	if p.PermissionID != "perm_001" {
		t.Errorf("permissionId: got %q", p.PermissionID)
	}
}
