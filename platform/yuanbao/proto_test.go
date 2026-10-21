package yuanbao

import (
	"encoding/hex"
	"testing"
)

func TestVarintRoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 255, 256, 65535, 1 << 20, 1 << 31, 1 << 63}
	for _, v := range cases {
		b := &buffer{}
		b.writeVarint(v)
		got, n := decodeVarint(b.bytes())
		if n == 0 {
			t.Errorf("decodeVarint(%d) returned 0", v)
			continue
		}
		if got != v {
			t.Errorf("round-trip: input=%d, got=%d", v, got)
		}
	}
}

func TestVarintOverflow(t *testing.T) {
	v := uint64(1<<63 | 0x7FFFFFFFFFFFFFFF)
	b := &buffer{}
	b.writeVarint(v)
	got, n := decodeVarint(b.bytes())
	if n == 0 {
		t.Fatal("decodeVarint overflow returned 0")
	}
	if got != v {
		t.Errorf("overflow: got %d, want %d", got, v)
	}
}

func TestEncodeDecodeConnMsg(t *testing.T) {
	data := []byte("hello world")
	encoded := encodeConnMsgFull(1, "test_cmd", 42, "msg-001", "test_module", data, true)
	msg, err := decodeConnMsg(encoded)
	if err != nil {
		t.Fatalf("decodeConnMsg: %v", err)
	}
	if msg.head.cmdType != 1 {
		t.Errorf("cmdType: want 1, got %d", msg.head.cmdType)
	}
	if msg.head.cmd != "test_cmd" {
		t.Errorf("cmd: got %q", msg.head.cmd)
	}
	if msg.head.seqNo != 42 {
		t.Errorf("seqNo: want 42, got %d", msg.head.seqNo)
	}
	if msg.head.msgID != "msg-001" {
		t.Errorf("msgID: got %q", msg.head.msgID)
	}
	if string(msg.data) != "hello world" {
		t.Errorf("data: got %q", string(msg.data))
	}
}

func TestConnMsgWithoutData(t *testing.T) {
	encoded := encodeConnMsgFull(1, "test", 1, "id", "m", nil, false)
	msg, err := decodeConnMsg(encoded)
	if err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if msg.head.cmdType != 1 {
		t.Errorf("cmdType: want 0, got %d", msg.head.cmdType)
	}
}

func TestAuthBindEncode(t *testing.T) {
	encoded := encodeAuthBind("bot-123", "bot", "token-abc", "auth-001", "", "", "", "")
	msg, err := decodeConnMsg(encoded)
	if err != nil {
		t.Fatalf("decode auth bind: %v", err)
	}
	if msg.head.cmd != "auth-bind" {
		t.Errorf("cmd: want 'auth-bind', got %q", msg.head.cmd)
	}
	if msg.head.module != "conn_access" {
		t.Errorf("module: want 'conn_access', got %q", msg.head.module)
	}
}

func TestDecodeAuthBindRsp(t *testing.T) {
	b := &buffer{}
	b.writeVarintField(1, 0)
	b.writeStringField(2, "ok")
	b.writeStringField(3, "conn-001")
	rsp := decodeAuthBindRsp(b.bytes())
	if rsp.code != 0 {
		t.Errorf("code: want 0, got %d", rsp.code)
	}
	if rsp.connectID != "conn-001" {
		t.Errorf("connectID: got %q", rsp.connectID)
	}
}

func TestAuthBindRspError(t *testing.T) {
	b := &buffer{}
	b.writeVarintField(1, 4001)
	rsp := decodeAuthBindRsp(b.bytes())
	if rsp.code != 4001 {
		t.Errorf("code: want 4001, got %d", rsp.code)
	}
}

func TestPingEncode(t *testing.T) {
	encoded := encodePing("ping-001")
	msg, err := decodeConnMsg(encoded)
	if err != nil {
		t.Fatalf("decode ping: %v", err)
	}
	if msg.head.cmd != "ping" {
		t.Errorf("cmd: got %q", msg.head.cmd)
	}
}

func TestPushAck(t *testing.T) {
	h := connHead{cmdType: cmdTypePush, cmd: "inbound_message", seqNo: 100, msgID: "push-001", module: bizPkg}
	encoded := encodePushAck(h)
	msg, err := decodeConnMsg(encoded)
	if err != nil {
		t.Fatalf("decode push ack: %v", err)
	}
	if msg.head.cmdType != cmdTypePushAck {
		t.Errorf("cmdType: want %d, got %d", cmdTypePushAck, msg.head.cmdType)
	}
}

func TestEncodeDecodeTextBody(t *testing.T) {
	body := encodeTextBody("hello世界")
	fields, err := parseFields(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	msgType := getString(fields, 1)
	if msgType != "TIMTextElem" {
		t.Errorf("msgType: got %q", msgType)
	}
	content := getBytes(fields, 2)
	cf, _ := parseFields(content)
	text := getString(cf, 1)
	if text != "hello世界" {
		t.Errorf("text: got %q", text)
	}
}

func TestEncodeC2CMessage(t *testing.T) {
	body := [][]byte{encodeTextBody("你好")}
	encoded := encodeSendC2CMessage("user-001", body, "bot-123", "", 0, "", "")
	msg, err := decodeConnMsg(encoded)
	if err != nil {
		t.Fatalf("decode c2c: %v", err)
	}
	if msg.head.cmd != "send_c2c_message" {
		t.Errorf("cmd: got %q", msg.head.cmd)
	}
}

func TestEncodeGroupMessage(t *testing.T) {
	body := [][]byte{encodeTextBody("群消息")}
	encoded := encodeSendGroupMessage("group-001", body, "bot-123", "", "", "")
	msg, err := decodeConnMsg(encoded)
	if err != nil {
		t.Fatalf("decode group: %v", err)
	}
	if msg.head.cmd != "send_group_message" {
		t.Errorf("cmd: got %q", msg.head.cmd)
	}
}

func TestDecodeInboundPush(t *testing.T) {
	cb := &buffer{}
	cb.writeStringField(1, "hello")
	eb := &buffer{}
	eb.writeStringField(1, "TIMTextElem")
	eb.writeBytesField(2, cb.bytes())
	pb := &buffer{}
	pb.writeStringField(1, "C2CCallback")
	pb.writeStringField(2, "from-user")
	pb.writeStringField(3, "bot")
	pb.writeStringField(4, "User1")
	pb.writeStringField(12, "msg-001")
	pb.writeBytesField(13, eb.bytes())

	push := decodeInboundPush(pb.bytes())
	if push == nil {
		t.Fatal("nil push")
	}
	if push.fromAccount != "from-user" {
		t.Errorf("fromAccount: got %q", push.fromAccount)
	}
	if len(push.msgBody) != 1 || push.msgBody[0].msgType != "TIMTextElem" {
		t.Errorf("msgBody: got %d elements", len(push.msgBody))
	}
}

func TestDecodeInboundPushGroup(t *testing.T) {
	cb := &buffer{}
	cb.writeStringField(1, "群消息")
	eb := &buffer{}
	eb.writeStringField(1, "TIMTextElem")
	eb.writeBytesField(2, cb.bytes())
	pb := &buffer{}
	pb.writeStringField(2, "from-user")
	pb.writeStringField(4, "User1")
	pb.writeStringField(6, "grp-001")
	pb.writeStringField(7, "测试群")
	pb.writeStringField(12, "msg-002")
	pb.writeBytesField(13, eb.bytes())

	push := decodeInboundPush(pb.bytes())
	if push == nil || push.groupCode != "grp-001" {
		t.Errorf("groupCode: got %q", push.groupCode)
	}
}

func TestInboundText(t *testing.T) {
	push := &inboundPush{msgBody: []msgBodyElement{
		{msgType: "TIMTextElem", msgContent: map[string]interface{}{"text": "你好"}},
		{msgType: "TIMImageElem", msgContent: map[string]interface{}{"originalUrl": "http://img"}},
	}}
	text := inboundText(push)
	if text == "" {
		t.Fatal("empty text")
	}
}

func TestInboundTextEmpty(t *testing.T) {
	if text := inboundText(&inboundPush{}); text != "" {
		t.Errorf("expected empty, got %q", text)
	}
}

func TestHeartbeatEncoding(t *testing.T) {
	f := encodeSendPrivateHeartbeat("bot-123", "user-001", 1)
	m, _ := decodeConnMsg(f)
	if m.head.cmd != "send_private_heartbeat" {
		t.Errorf("cmd: got %q", m.head.cmd)
	}
}

func TestGroupHeartbeatEncoding(t *testing.T) {
	f := encodeSendGroupHeartbeat("bot-123", "group-001", 1)
	m, _ := decodeConnMsg(f)
	if m.head.cmd != "send_group_heartbeat" {
		t.Errorf("cmd: got %q", m.head.cmd)
	}
}

func TestSeqNoIncrement(t *testing.T) {
	old := seqCounter
	a, b, c := nextSeqNo(), nextSeqNo(), nextSeqNo()
	if b != a+1 || c != b+1 {
		t.Errorf("not monotonic: %d %d %d", a, b, c)
	}
	seqCounter = old
}

func TestSignature(t *testing.T) {
	nonce := "abc123"
	ts := "2026-01-01T00:00:00+08:00"
	sig := computeSignature(nonce, ts, "key", "secret")
	decoded, _ := hex.DecodeString(sig)
	if len(decoded) != 32 {
		t.Errorf("sig len: want 32, got %d", len(decoded))
	}
}

func TestSignatureDeterministic(t *testing.T) {
	a := computeSignature("n1", "t1", "k1", "s1")
	b := computeSignature("n1", "t1", "k1", "s1")
	if a != b {
		t.Error("not deterministic")
	}
}

func TestSignatureDifferent(t *testing.T) {
	a := computeSignature("n1", "t1", "k1", "s1")
	b := computeSignature("n2", "t1", "k1", "s1")
	if a == b {
		t.Error("should differ")
	}
}

func TestParseRepeatedFields(t *testing.T) {
	b := &buffer{}
	b.writeStringField(1, "a")
	b.writeStringField(2, "x")
	b.writeStringField(1, "b")
	fields, _ := parseFields(b.bytes())
	if len(fields) != 3 {
		t.Fatalf("want 3 fields, got %d", len(fields))
	}
	vals := getRepeatedBytes(fields, 1)
	if len(vals) != 2 {
		t.Errorf("want 2 repeated, got %d", len(vals))
	}
}

func TestJSONInboundPush(t *testing.T) {
	p := &Platform{}
	push := p.decodePush([]byte(`{"callback_command":"C2CCallback","from_account":"user-001","msg_body":[{"msg_type":"TIMTextElem","msg_content":{"text":"你好"}}]}`))
	if push == nil || push.fromAccount != "user-001" || len(push.msgBody) != 1 {
		t.Fatal("json push parse failed")
	}
}

func TestJSONPascalCase(t *testing.T) {
	p := &Platform{}
	push := p.decodePush([]byte(`{"From_Account":"user","MsgBody":[{"MsgType":"TIMTextElem","MsgContent":{"text":"hi"}}]}`))
	if push == nil || push.fromAccount != "user" {
		t.Fatal("pascal case parse failed")
	}
}

func TestMultipleMsgBodies(t *testing.T) {
	body := [][]byte{encodeTextBody("a"), encodeTextBody("b")}
	encoded := encodeSendC2CMessage("user-001", body, "bot-123", "multi-001", 0, "", "")
	msg, _ := decodeConnMsg(encoded)
	if msg.head.msgID != "multi-001" {
		t.Errorf("msgID: got %q", msg.head.msgID)
	}
}

func TestParseInvalid(t *testing.T) {
	_, err := parseFields([]byte{0xFF, 0xFF, 0xFF})
	if err == nil {
		t.Error("expected error")
	}
}

func TestDecodeNil(t *testing.T) {
	msg, err := decodeConnMsg(nil)
	if err == nil || msg != nil {
		t.Error("expected error for nil")
	}
}
