package runtimeprotocol

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeRejectsContractMismatchAndUnknownFields(t *testing.T) {
	_, err := Decode([]byte(`{"contract_hash":"old","device_id":"d1","connection_generation":1,"sequence":1,"method":"catalog/list"}`))
	if !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("Decode() error = %v, want contract mismatch", err)
	}

	raw := `{"contract_hash":"` + ContractHash + `","device_id":"d1","connection_generation":1,"sequence":1,"method":"catalog/list","cwd":"/tmp"}`
	_, err = Decode([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), `unknown field "cwd"`) {
		t.Fatalf("Decode() error = %v, want strict unknown field rejection", err)
	}
}

func TestSequenceGuardRejectsGapAndResetsOnGeneration(t *testing.T) {
	guard := &SequenceGuard{}
	base := Envelope{ContractHash: ContractHash, DeviceID: "d1", ConnectionGeneration: 1, Sequence: 1, Method: MethodHeartbeat}
	if err := guard.Accept(base); err != nil {
		t.Fatalf("Accept(first) error = %v", err)
	}
	base.Sequence = 3
	if err := guard.Accept(base); err == nil || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("Accept(gap) error = %v", err)
	}
	base.ConnectionGeneration = 2
	base.Sequence = 1
	if err := guard.Accept(base); err != nil {
		t.Fatalf("Accept(new generation) error = %v", err)
	}
}

func TestDecodePayloadRejectsPathsOutsideDeclaredType(t *testing.T) {
	envelope := Envelope{Method: MethodTurnStart, Payload: []byte(`{"text":"hello","local_path":"/tmp/a"}`)}
	_, err := DecodePayload[struct {
		Text string `json:"text"`
	}](envelope)
	if err == nil || !strings.Contains(err.Error(), `unknown field "local_path"`) {
		t.Fatalf("DecodePayload() error = %v", err)
	}
}
