package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

func TestTransportCallRoundTrip(t *testing.T) {
	rResp, wMockResp := io.Pipe()
	rReq, wTrOut := io.Pipe()

	tr := newTransport(rResp, wTrOut, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.readLoop(ctx)

	go func() {
		defer wMockResp.Close()
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req map[string]any
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			id := req["id"]
			line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":1}}`+"\n", id)
			if _, err := io.WriteString(wMockResp, line); err != nil {
				return
			}
		}
	}()

	res, err := tr.call(ctx, "initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatal(err)
	}
	if got.ProtocolVersion != 1 {
		t.Fatalf("protocolVersion = %d", got.ProtocolVersion)
	}
	cancel()
	_ = wTrOut.Close()
}

func TestJSONIDKey(t *testing.T) {
	if jsonIDKey(json.RawMessage(`42`)) != "42" {
		t.Fatalf("numeric id")
	}
	if jsonIDKey(json.RawMessage(`"x"`)) != "x" {
		t.Fatalf("string id")
	}
	if !isJSONRPCIDNullOrAbsent(json.RawMessage(`null`)) {
		t.Fatalf("null id")
	}
	if !isJSONRPCIDNullOrAbsent(json.RawMessage(nil)) {
		t.Fatalf("absent id")
	}
}
