package pi

import "testing"

func TestPiSession_SupportsMidTurnSteering(t *testing.T) {
	if (&piSession{rpc: true}).SupportsMidTurnSteering() != true {
		t.Fatal("RPC mode supports streamingBehavior=steer")
	}
	if (&piSession{rpc: false}).SupportsMidTurnSteering() != false {
		t.Fatal("JSON one-shot mode cannot steer")
	}
}
