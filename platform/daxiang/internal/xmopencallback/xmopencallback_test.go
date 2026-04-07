package xmopencallback

import (
	"context"
	"testing"

	thrift "github.com/apache/thrift/lib/go/thrift"
)

// stubHandler records the last call made to EventCallback.
type stubHandler struct {
	calledEventType int32
	calledJSONEvent string
	returnErr       error
}

func (h *stubHandler) EventCallback(_ context.Context, eventType int32, jsonEvent string) (*EmptyResp, error) {
	h.calledEventType = eventType
	h.calledJSONEvent = jsonEvent
	if h.returnErr != nil {
		return nil, h.returnErr
	}
	return &EmptyResp{Status: &RespStatus{Code: 0, Msg: "ok"}}, nil
}

// makeProtocols returns an in-protocol pre-loaded with a Thrift CALL message
// for method `name` with the given seqID, and an empty out-protocol backed by
// its own buffer that the processor can write the reply into.
//
// The args struct written into the in-buffer follows the field layout expected
// by eventCallbackProcessorFunc: field 1 = i32 (eventType), field 2 = string (jsonEvent).
func makeProtocols(t *testing.T, name string, seqID int32, eventType int32, jsonEvent string) (in thrift.TProtocol, out thrift.TProtocol, outBuf *thrift.TMemoryBuffer) {
	t.Helper()

	inBuf := thrift.NewTMemoryBuffer()
	inProto := thrift.NewTBinaryProtocolTransport(inBuf)

	ctx := context.Background()

	// Write Thrift CALL message header
	if err := inProto.WriteMessageBegin(ctx, name, thrift.CALL, seqID); err != nil {
		t.Fatalf("WriteMessageBegin: %v", err)
	}
	// Write args struct
	if err := inProto.WriteStructBegin(ctx, "eventCallback_args"); err != nil {
		t.Fatalf("WriteStructBegin: %v", err)
	}
	// field 1: eventType i32
	if err := inProto.WriteFieldBegin(ctx, "eventType", thrift.I32, 1); err != nil {
		t.Fatalf("WriteFieldBegin 1: %v", err)
	}
	if err := inProto.WriteI32(ctx, eventType); err != nil {
		t.Fatalf("WriteI32: %v", err)
	}
	if err := inProto.WriteFieldEnd(ctx); err != nil {
		t.Fatalf("WriteFieldEnd 1: %v", err)
	}
	// field 2: jsonEvent string
	if err := inProto.WriteFieldBegin(ctx, "jsonEvent", thrift.STRING, 2); err != nil {
		t.Fatalf("WriteFieldBegin 2: %v", err)
	}
	if err := inProto.WriteString(ctx, jsonEvent); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := inProto.WriteFieldEnd(ctx); err != nil {
		t.Fatalf("WriteFieldEnd 2: %v", err)
	}
	// STOP field
	if err := inProto.WriteFieldStop(ctx); err != nil {
		t.Fatalf("WriteFieldStop: %v", err)
	}
	if err := inProto.WriteStructEnd(ctx); err != nil {
		t.Fatalf("WriteStructEnd: %v", err)
	}
	if err := inProto.WriteMessageEnd(ctx); err != nil {
		t.Fatalf("WriteMessageEnd: %v", err)
	}

	outBuf = thrift.NewTMemoryBuffer()
	out = thrift.NewTBinaryProtocolTransport(outBuf)

	return inProto, out, outBuf
}

func assertSuccessReply(t *testing.T, outBuf *thrift.TMemoryBuffer, wantSeqID int32) {
	t.Helper()

	ctx := context.Background()
	reply := thrift.NewTBinaryProtocolTransport(outBuf)

	name, messageType, seqID, err := reply.ReadMessageBegin(ctx)
	if err != nil {
		t.Fatalf("ReadMessageBegin: %v", err)
	}
	if name != "eventCallback" {
		t.Fatalf("reply method = %q, want eventCallback", name)
	}
	if messageType != thrift.REPLY {
		t.Fatalf("reply message type = %v, want REPLY", messageType)
	}
	if seqID != wantSeqID {
		t.Fatalf("reply seqID = %d, want %d", seqID, wantSeqID)
	}

	if _, err := reply.ReadStructBegin(ctx); err != nil {
		t.Fatalf("ReadStructBegin result: %v", err)
	}
	_, resultFieldType, resultFieldID, err := reply.ReadFieldBegin(ctx)
	if err != nil {
		t.Fatalf("ReadFieldBegin result: %v", err)
	}
	if resultFieldType != thrift.STRUCT || resultFieldID != 0 {
		t.Fatalf("result field = (type=%v, id=%d), want (type=%v, id=0)", resultFieldType, resultFieldID, thrift.STRUCT)
	}

	if _, err := reply.ReadStructBegin(ctx); err != nil {
		t.Fatalf("ReadStructBegin success: %v", err)
	}
	_, emptyRespFieldType, emptyRespFieldID, err := reply.ReadFieldBegin(ctx)
	if err != nil {
		t.Fatalf("ReadFieldBegin success struct: %v", err)
	}
	if emptyRespFieldType != thrift.STRUCT || emptyRespFieldID != 1 {
		t.Fatalf("success field = (type=%v, id=%d), want (type=%v, id=1)", emptyRespFieldType, emptyRespFieldID, thrift.STRUCT)
	}

	if _, err := reply.ReadStructBegin(ctx); err != nil {
		t.Fatalf("ReadStructBegin status: %v", err)
	}
	_, statusCodeType, statusCodeID, err := reply.ReadFieldBegin(ctx)
	if err != nil {
		t.Fatalf("ReadFieldBegin status code: %v", err)
	}
	if statusCodeType != thrift.I32 || statusCodeID != 1 {
		t.Fatalf("status code field = (type=%v, id=%d), want (type=%v, id=1)", statusCodeType, statusCodeID, thrift.I32)
	}
	statusCode, err := reply.ReadI32(ctx)
	if err != nil {
		t.Fatalf("ReadI32 status code: %v", err)
	}
	if statusCode != 0 {
		t.Fatalf("status code = %d, want 0", statusCode)
	}
	if err := reply.ReadFieldEnd(ctx); err != nil {
		t.Fatalf("ReadFieldEnd status code: %v", err)
	}

	_, statusMsgType, statusMsgID, err := reply.ReadFieldBegin(ctx)
	if err != nil {
		t.Fatalf("ReadFieldBegin status msg: %v", err)
	}
	if statusMsgType != thrift.STRING || statusMsgID != 2 {
		t.Fatalf("status msg field = (type=%v, id=%d), want (type=%v, id=2)", statusMsgType, statusMsgID, thrift.STRING)
	}
	statusMsg, err := reply.ReadString(ctx)
	if err != nil {
		t.Fatalf("ReadString status msg: %v", err)
	}
	if statusMsg != "ok" {
		t.Fatalf("status msg = %q, want ok", statusMsg)
	}
	if err := reply.ReadFieldEnd(ctx); err != nil {
		t.Fatalf("ReadFieldEnd status msg: %v", err)
	}

	_, statusStopType, statusStopID, err := reply.ReadFieldBegin(ctx)
	if err != nil {
		t.Fatalf("ReadFieldBegin status stop: %v", err)
	}
	if statusStopType != thrift.STOP || statusStopID != 0 {
		t.Fatalf("status terminator = (type=%v, id=%d), want STOP", statusStopType, statusStopID)
	}
	if err := reply.ReadStructEnd(ctx); err != nil {
		t.Fatalf("ReadStructEnd status: %v", err)
	}
	if err := reply.ReadFieldEnd(ctx); err != nil {
		t.Fatalf("ReadFieldEnd success field: %v", err)
	}

	_, successStopType, successStopID, err := reply.ReadFieldBegin(ctx)
	if err != nil {
		t.Fatalf("ReadFieldBegin success stop: %v", err)
	}
	if successStopType != thrift.STOP || successStopID != 0 {
		t.Fatalf("success terminator = (type=%v, id=%d), want STOP", successStopType, successStopID)
	}
	if err := reply.ReadStructEnd(ctx); err != nil {
		t.Fatalf("ReadStructEnd success: %v", err)
	}
	if err := reply.ReadFieldEnd(ctx); err != nil {
		t.Fatalf("ReadFieldEnd result field: %v", err)
	}

	_, resultStopType, resultStopID, err := reply.ReadFieldBegin(ctx)
	if err != nil {
		t.Fatalf("ReadFieldBegin result stop: %v", err)
	}
	if resultStopType != thrift.STOP || resultStopID != 0 {
		t.Fatalf("result terminator = (type=%v, id=%d), want STOP", resultStopType, resultStopID)
	}
	if err := reply.ReadStructEnd(ctx); err != nil {
		t.Fatalf("ReadStructEnd result: %v", err)
	}
	if err := reply.ReadMessageEnd(ctx); err != nil {
		t.Fatalf("ReadMessageEnd: %v", err)
	}
}

// makeUnknownProtocols is like makeProtocols but writes an empty args struct
// (no fields) so it can be used for unknown-method dispatch tests.
func makeUnknownProtocols(t *testing.T, name string, seqID int32) (in thrift.TProtocol, out thrift.TProtocol) {
	t.Helper()

	inBuf := thrift.NewTMemoryBuffer()
	inProto := thrift.NewTBinaryProtocolTransport(inBuf)

	ctx := context.Background()

	if err := inProto.WriteMessageBegin(ctx, name, thrift.CALL, seqID); err != nil {
		t.Fatalf("WriteMessageBegin: %v", err)
	}
	// Write a minimal struct so Skip(STRUCT) can consume it
	if err := inProto.WriteStructBegin(ctx, name+"_args"); err != nil {
		t.Fatalf("WriteStructBegin: %v", err)
	}
	if err := inProto.WriteFieldStop(ctx); err != nil {
		t.Fatalf("WriteFieldStop: %v", err)
	}
	if err := inProto.WriteStructEnd(ctx); err != nil {
		t.Fatalf("WriteStructEnd: %v", err)
	}
	if err := inProto.WriteMessageEnd(ctx); err != nil {
		t.Fatalf("WriteMessageEnd: %v", err)
	}

	outBuf := thrift.NewTMemoryBuffer()
	outProto := thrift.NewTBinaryProtocolTransport(outBuf)

	return inProto, outProto
}

// TestEventCallback_Dispatch verifies the happy path: the processor correctly
// reads args from the wire, calls the handler with the right values, and
// returns (true, nil).
func TestEventCallback_Dispatch(t *testing.T) {
	handler := &stubHandler{}
	proc := NewXmOpenCallbackServiceIProcessor(handler)

	wantEventType := int32(42)
	wantJSON := `{"msg":"hello"}`
	wantSeqID := int32(7)

	in, out, outBuf := makeProtocols(t, "eventCallback", wantSeqID, wantEventType, wantJSON)

	ok, ex := proc.Process(context.Background(), in, out)

	if !ok {
		t.Fatalf("Process returned ok=false, exception=%v", ex)
	}
	if ex != nil {
		t.Fatalf("Process returned unexpected exception: %v", ex)
	}
	if handler.calledEventType != wantEventType {
		t.Errorf("eventType: got %d, want %d", handler.calledEventType, wantEventType)
	}
	if handler.calledJSONEvent != wantJSON {
		t.Errorf("jsonEvent: got %q, want %q", handler.calledJSONEvent, wantJSON)
	}
	assertSuccessReply(t, outBuf, wantSeqID)
}

// TestUnknownMethod verifies that calling an unregistered method returns
// ok=false with nil exception (the processor writes a Thrift EXCEPTION reply
// and returns gracefully rather than crashing).
func TestUnknownMethod(t *testing.T) {
	handler := &stubHandler{}
	proc := NewXmOpenCallbackServiceIProcessor(handler)

	in, out := makeUnknownProtocols(t, "noSuchMethod", 1)

	ok, ex := proc.Process(context.Background(), in, out)

	// Per the production code, unknown-method returns (false, nil).
	if ok {
		t.Errorf("Process returned ok=true for unknown method; want false")
	}
	if ex != nil {
		t.Errorf("Process returned exception=%v for unknown method; want nil", ex)
	}
	// Handler must NOT have been called.
	if handler.calledJSONEvent != "" || handler.calledEventType != 0 {
		t.Errorf("handler was unexpectedly called for unknown method")
	}
}

// TestProcessorMap verifies the processor map is populated correctly after
// construction.
func TestProcessorMap(t *testing.T) {
	proc := NewXmOpenCallbackServiceIProcessor(&stubHandler{})
	m := proc.ProcessorMap()
	if _, ok := m["eventCallback"]; !ok {
		t.Error("processorMap missing 'eventCallback' entry")
	}
	if len(m) != 1 {
		t.Errorf("processorMap has %d entries; want exactly 1", len(m))
	}
}
