package core_test

import (
	"encoding/binary"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// RFC 6455 requires unpredictable client masks, including when application code
// seeds math/rand. Check actual wire bytes, rather than a module version string.
func TestWebSocketMasksDoNotFollowMathRandSeed(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",randseednop=0")
	masks := make(chan [4][4]byte, 1)
	errors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			errors <- err
			return
		}
		defer func() { _ = conn.Close() }()
		raw := conn.UnderlyingConn()
		_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
		var got [4][4]byte
		for i := range got {
			var frame [7]byte // Two header bytes, four mask bytes, one payload byte.
			if _, err := io.ReadFull(raw, frame[:]); err != nil {
				errors <- err
				return
			}
			if frame[0] != 0x81 || frame[1] != 0x81 {
				t.Errorf("unexpected masked one-byte text frame: %x", frame[:2])
				return
			}
			copy(got[i][:], frame[2:6])
		}
		masks <- got
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	// SA1019: the prediction only holds if the global source is deterministic.
	rand.Seed(1) //nolint:staticcheck
	prediction := rand.New(rand.NewSource(1))
	var predicted [4][4]byte
	for i := range predicted {
		binary.LittleEndian.PutUint32(predicted[i][:], prediction.Uint32())
		if err := conn.WriteMessage(websocket.TextMessage, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-masks:
		if got == predicted {
			t.Fatal("client masks follow the public math/rand seed; use cryptographic randomness")
		}
	case err := <-errors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading client masks")
	}
}
