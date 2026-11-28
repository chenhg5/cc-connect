package core

import (
	"net"
	"testing"
)

func TestBridgeRestartKeepsAdmissionClosedUntilBindSucceeds(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	bs := NewBridgeServerInsecure(listener.Addr().(*net.TCPAddr).Port, "", "/bridge/ws", nil)
	bs.Stop()
	if err := bs.Start(); err == nil {
		bs.Stop()
		t.Fatal("expected the occupied port to prevent startup")
	}
	bs.mu.RLock()
	closed := bs.stopping
	bs.mu.RUnlock()
	if !closed || bs.Listening() || bs.LastError() == "" {
		t.Fatal("a failed restart must keep admissions closed and report the bind failure")
	}

	bs.port = 0
	if err := bs.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bs.Stop)
	bs.mu.RLock()
	closed = bs.stopping
	bs.mu.RUnlock()
	if closed || !bs.Listening() || bs.LastError() != "" {
		t.Fatal("successful restart should reopen admissions and clear the bind failure")
	}
}
