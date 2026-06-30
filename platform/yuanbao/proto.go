package yuanbao

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	cmdTypeRequest  = 0
	cmdTypeResponse = 1
	cmdTypePush     = 2
	cmdTypePushAck  = 3

	cmdAuthBind   = "auth-bind"
	cmdPing       = "ping"
	cmdKickout    = "kickout"
	cmdUpdateMeta = "update-meta"

	moduleConnAccess = "conn_access"
	bizPkg           = "yuanbao_openclaw_proxy"
	instanceID       = 17
)

const (
	wtVarint = 0
	wt64Bit  = 1
	wtLen    = 2
	wt32Bit  = 5
)

type buffer struct{ data []byte }

func newBuffer(capacity int) *buffer {
	return &buffer{data: make([]byte, 0, capacity)}
}
func (b *buffer) bytes() []byte { return b.data }

func (b *buffer) writeVarint(v uint64) {
	for {
		bits := v & 0x7F
		v >>= 7
		if v != 0 {
			b.data = append(b.data, byte(bits)|0x80)
		} else {
			b.data = append(b.data, byte(bits))
			return
		}
	}
}

func (b *buffer) writeFieldNum(fieldNum int, wireType int) {
	b.writeVarint(uint64(fieldNum<<3 | wireType))
}

func (b *buffer) writeString(s string) {
	b.writeVarint(uint64(len(s)))
	b.data = append(b.data, []byte(s)...)
}

func (b *buffer) writeBytes(data []byte) {
	b.writeVarint(uint64(len(data)))
	b.data = append(b.data, data...)
}

func (b *buffer) writeVarintField(num int, val uint64) {
	b.writeFieldNum(num, wtVarint)
	b.writeVarint(val)
}

func (b *buffer) writeStringField(num int, val string) {
	if val == "" {
		return
	}
	b.writeFieldNum(num, wtLen)
	b.writeString(val)
}

func (b *buffer) writeBytesField(num int, val []byte) {
	if len(val) == 0 {
		return
	}
	b.writeFieldNum(num, wtLen)
	b.writeBytes(val)
}

func (b *buffer) writeHead(cmdType int, cmd string, seqNo int, msgID string, module string, needAck bool, status int) []byte {
	hb := newBuffer(64)
	if cmdType != 0 {
		hb.writeVarintField(1, uint64(cmdType))
	}
	if cmd != "" {
		hb.writeStringField(2, cmd)
	}
	if seqNo != 0 {
		hb.writeVarintField(3, uint64(seqNo))
	}
	if msgID != "" {
		hb.writeStringField(4, msgID)
	}
	if module != "" {
		hb.writeStringField(5, module)
	}
	if needAck {
		hb.writeVarintField(6, 1)
	}
	if status != 0 {
		hb.writeVarintField(10, uint64(status))
	}
	return hb.bytes()
}

func encodeConnMsgFull(cmdType int, cmd string, seqNo int, msgID string, module string, data []byte, needAck bool) []byte {
	b := newBuffer(128 + len(data))
	headBytes := (&buffer{}).writeHead(cmdType, cmd, seqNo, msgID, module, needAck, 0)
	b.writeBytesField(1, headBytes)
	if len(data) > 0 {
		b.writeBytesField(2, data)
	}
	return b.bytes()
}

type field struct {
	fieldNum int
	wireType int
	value    interface{}
}

func parseFields(data []byte) ([]field, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("yuanbao: empty data")
	}
	var fields []field
	pos := 0
	for pos < len(data) {
		tag, n := decodeVarint(data[pos:])
		if n == 0 {
			return nil, fmt.Errorf("yuanbao: parse varint tag failed at %d", pos)
		}
		pos += n
		fieldNum := int(tag >> 3)
		wt := int(tag & 0x07)
		switch wt {
		case wtVarint:
			val, n := decodeVarint(data[pos:])
			if n == 0 {
				return nil, fmt.Errorf("yuanbao: parse varint at %d", pos)
			}
			fields = append(fields, field{fieldNum, wt, val})
			pos += n
		case wtLen:
			ln, n := decodeVarint(data[pos:])
			if n == 0 {
				return nil, fmt.Errorf("yuanbao: parse length at %d", pos)
			}
			pos += n
			if pos+int(ln) > len(data) {
				return nil, fmt.Errorf("yuanbao: length %d exceeds data at %d", ln, pos)
			}
			val := make([]byte, ln)
			copy(val, data[pos:pos+int(ln)])
			fields = append(fields, field{fieldNum, wt, val})
			pos += int(ln)
		case wt64Bit:
			if pos+8 > len(data) {
				return nil, fmt.Errorf("yuanbao: 64-bit truncated at %d", pos)
			}
			fields = append(fields, field{fieldNum, wt, binary.LittleEndian.Uint64(data[pos : pos+8])})
			pos += 8
		case wt32Bit:
			if pos+4 > len(data) {
				return nil, fmt.Errorf("yuanbao: 32-bit truncated at %d", pos)
			}
			fields = append(fields, field{fieldNum, wt, uint64(binary.LittleEndian.Uint32(data[pos : pos+4]))})
			pos += 4
		default:
			return nil, fmt.Errorf("yuanbao: unknown wire type %d at %d", wt, pos)
		}
	}
	return fields, nil
}

func decodeVarint(data []byte) (uint64, int) {
	var result uint64
	var shift uint
	for i, b := range data {
		result |= uint64(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			return result, i + 1
		}
		if shift >= 64 {
			return 0, 0
		}
	}
	return 0, 0
}

func getString(fields []field, num int) string {
	for _, f := range fields {
		if f.fieldNum == num && f.wireType == wtLen {
			if b, ok := f.value.([]byte); ok {
				return string(b)
			}
		}
	}
	return ""
}

func getVarint(fields []field, num int) uint64 {
	for _, f := range fields {
		if f.fieldNum == num && f.wireType == wtVarint {
			if v, ok := f.value.(uint64); ok {
				return v
			}
		}
	}
	return 0
}

func getBytes(fields []field, num int) []byte {
	for _, f := range fields {
		if f.fieldNum == num && f.wireType == wtLen {
			if b, ok := f.value.([]byte); ok {
				return b
			}
		}
	}
	return nil
}

func getRepeatedBytes(fields []field, num int) [][]byte {
	var result [][]byte
	for _, f := range fields {
		if f.fieldNum == num && f.wireType == wtLen {
			if b, ok := f.value.([]byte); ok {
				result = append(result, b)
			}
		}
	}
	return result
}

type connHead struct {
	cmdType int
	cmd     string
	seqNo   int
	msgID   string
	module  string
	needAck bool
	status  int
}

type connMsg struct {
	head  connHead
	seqNo int
	data  []byte
}

func decodeConnMsg(raw []byte) (*connMsg, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("yuanbao: empty conn msg")
	}
	fields, err := parseFields(raw)
	if err != nil {
		return nil, err
	}
	headBytes := getBytes(fields, 1)
	payload := getBytes(fields, 2)
	var head connHead
	if len(headBytes) > 0 {
		hf, err := parseFields(headBytes)
		if err != nil {
			return nil, fmt.Errorf("yuanbao: parse head: %w", err)
		}
		head = connHead{
			cmdType: int(getVarint(hf, 1)),
			cmd:     getString(hf, 2),
			seqNo:   int(getVarint(hf, 3)),
			msgID:   getString(hf, 4),
			module:  getString(hf, 5),
			needAck: getVarint(hf, 6) != 0,
			status:  int(getVarint(hf, 10)),
		}
	}
	return &connMsg{head: head, seqNo: head.seqNo, data: payload}, nil
}

type authBindRsp struct {
	code      int
	message   string
	connectID string
}

func encodeAuthBind(uid, source, token, msgID, appVersion, operationSystem, botVersion, routeEnv string) []byte {
	ab := newBuffer(128)
	ab.writeStringField(1, uid)
	ab.writeStringField(2, source)
	ab.writeStringField(3, token)
	authBuf := ab.bytes()
	db := newBuffer(64)
	db.writeStringField(1, appVersion)
	db.writeStringField(2, operationSystem)
	db.writeStringField(10, fmt.Sprintf("%d", instanceID))
	db.writeStringField(24, botVersion)
	devBuf := db.bytes()
	rb := newBuffer(128 + len(authBuf) + len(devBuf))
	rb.writeStringField(1, "ybBot")
	rb.writeBytesField(2, authBuf)
	rb.writeBytesField(3, devBuf)
	rb.writeStringField(5, routeEnv)
	return encodeConnMsgFull(cmdTypeRequest, cmdAuthBind, nextSeqNo(), msgID, moduleConnAccess, rb.bytes(), false)
}

func decodeAuthBindRsp(data []byte) authBindRsp {
	fields, _ := parseFields(data)
	return authBindRsp{
		code:      int(getVarint(fields, 1)),
		message:   getString(fields, 2),
		connectID: getString(fields, 3),
	}
}

func encodePing(msgID string) []byte {
	return encodeConnMsgFull(cmdTypeRequest, cmdPing, nextSeqNo(), msgID, moduleConnAccess, nil, false)
}

func encodePushAck(h connHead) []byte {
	return encodeConnMsgFull(cmdTypePushAck, h.cmd, nextSeqNo(), h.msgID, h.module, nil, false)
}

type inboundPush struct {
	callbackCommand string
	fromAccount     string
	toAccount       string
	senderNickname  string
	groupID         string
	groupCode       string
	groupName       string
	msgSeq          int
	msgRandom       int
	msgTime         int
	msgKey          string
	msgID           string
	msgBody         []msgBodyElement
	cloudCustomData string
	botOwnerID      string
	clawMsgType     int
	traceID         string
}

type msgBodyElement struct {
	msgType    string
	msgContent map[string]interface{}
}

func decodeInboundPush(data []byte) *inboundPush {
	fields, err := parseFields(data)
	if err != nil {
		return nil
	}
	traceID := ""
	if logExtBytes := getBytes(fields, 20); len(logExtBytes) > 0 {
		if lf, err := parseFields(logExtBytes); err == nil {
			traceID = getString(lf, 1)
		}
	}
	var msgBody []msgBodyElement
	for _, elBytes := range getRepeatedBytes(fields, 13) {
		if el, err := decodeMsgBodyElement(elBytes); err == nil {
			msgBody = append(msgBody, el)
		}
	}
	return &inboundPush{
		callbackCommand: getString(fields, 1),
		fromAccount:     getString(fields, 2),
		toAccount:       getString(fields, 3),
		senderNickname:  getString(fields, 4),
		groupID:         getString(fields, 5),
		groupCode:       getString(fields, 6),
		groupName:       getString(fields, 7),
		msgSeq:          int(getVarint(fields, 8)),
		msgRandom:       int(getVarint(fields, 9)),
		msgTime:         int(getVarint(fields, 10)),
		msgKey:          getString(fields, 11),
		msgID:           getString(fields, 12),
		msgBody:         msgBody,
		cloudCustomData: getString(fields, 14),
		botOwnerID:      getString(fields, 16),
		clawMsgType:     int(getVarint(fields, 18)),
		traceID:         traceID,
	}
}

func decodeMsgBodyElement(data []byte) (msgBodyElement, error) {
	fields, err := parseFields(data)
	if err != nil {
		return msgBodyElement{}, err
	}
	elem := msgBodyElement{msgType: getString(fields, 1)}
	if contentBytes := getBytes(fields, 2); len(contentBytes) > 0 {
		if cf, err := parseFields(contentBytes); err == nil {
			elem.msgContent = decodeMsgContent(cf)
		}
	}
	return elem, nil
}

func decodeMsgContent(fields []field) map[string]interface{} {
	m := make(map[string]interface{})
	for _, f := range fields {
		switch f.fieldNum {
		case 1:
			if s, ok := f.value.([]byte); ok {
				m["text"] = string(s)
			}
		case 2:
			if s, ok := f.value.([]byte); ok {
				m["uuid"] = string(s)
			}
		case 3:
			if v, ok := f.value.(uint64); ok {
				m["imageFormat"] = int(v)
			}
		case 4:
			if s, ok := f.value.([]byte); ok {
				m["data"] = string(s)
			}
		case 5:
			if s, ok := f.value.([]byte); ok {
				m["desc"] = string(s)
			}
		case 10:
			if s, ok := f.value.([]byte); ok {
				m["url"] = string(s)
			}
		case 12:
			if s, ok := f.value.([]byte); ok {
				m["fileName"] = string(s)
			}
		case 15:
			if s, ok := f.value.([]byte); ok {
				m["originalUrl"] = string(s)
			}
		}
	}
	return m
}

func encodeTextBody(text string) []byte {
	cb := newBuffer(64)
	cb.writeStringField(1, text)
	contentBuf := cb.bytes()
	eb := newBuffer(64 + len(contentBuf))
	eb.writeStringField(1, "TIMTextElem")
	eb.writeBytesField(2, contentBuf)
	return eb.bytes()
}

func encodeSendC2CMessage(toAccount string, msgBody [][]byte, fromAccount string, msgID string, msgRandom int, groupCode string, traceID string) []byte {
	bb := newBuffer(256)
	reqID := msgID
	if msgID != "" {
		bb.writeStringField(1, msgID)
	}
	bb.writeStringField(2, toAccount)
	bb.writeStringField(3, fromAccount)
	if msgRandom != 0 {
		bb.writeVarintField(4, uint64(msgRandom))
	}
	for _, body := range msgBody {
		bb.writeBytesField(5, body)
	}
	bb.writeStringField(6, groupCode)
	if traceID != "" {
		logBytes := func() []byte { var lb buffer; lb.writeStringField(1, traceID); return lb.bytes() }()
		bb.writeBytesField(8, logBytes)
	}
	if reqID == "" {
		reqID = fmt.Sprintf("c2c_%d", nextSeqNo())
	}
	return encodeConnMsgFull(cmdTypeRequest, "send_c2c_message", nextSeqNo(), reqID, bizPkg, bb.bytes(), false)
}

func encodeSendGroupMessage(groupCode string, msgBody [][]byte, fromAccount string, msgID string, refMsgID string, traceID string) []byte {
	bb := newBuffer(256)
	reqID := msgID
	if msgID != "" {
		bb.writeStringField(1, msgID)
	}
	bb.writeStringField(2, groupCode)
	bb.writeStringField(3, fromAccount)
	for _, body := range msgBody {
		bb.writeBytesField(6, body)
	}
	bb.writeStringField(7, refMsgID)
	if traceID != "" {
		logBytes := func() []byte { var lb buffer; lb.writeStringField(1, traceID); return lb.bytes() }()
		bb.writeBytesField(9, logBytes)
	}
	if reqID == "" {
		reqID = fmt.Sprintf("grp_%d", nextSeqNo())
	}
	return encodeConnMsgFull(cmdTypeRequest, "send_group_message", nextSeqNo(), reqID, bizPkg, bb.bytes(), false)
}

func encodeSendPrivateHeartbeat(fromAccount, toAccount string, heartbeat int) []byte {
	bb := newBuffer(64)
	bb.writeStringField(1, fromAccount)
	bb.writeStringField(2, toAccount)
	bb.writeVarintField(3, uint64(heartbeat))
	return encodeConnMsgFull(cmdTypeRequest, "send_private_heartbeat", nextSeqNo(),
		fmt.Sprintf("hb_priv_%d", nextSeqNo()), bizPkg, bb.bytes(), false)
}

func encodeSendGroupHeartbeat(fromAccount, groupCode string, heartbeat int) []byte {
	bb := newBuffer(64)
	bb.writeStringField(1, fromAccount)
	bb.writeStringField(2, "")
	bb.writeStringField(3, groupCode)
	bb.writeVarintField(4, uint64(time.Now().UnixMilli()))
	bb.writeVarintField(5, uint64(heartbeat))
	return encodeConnMsgFull(cmdTypeRequest, "send_group_heartbeat", nextSeqNo(),
		fmt.Sprintf("hb_grp_%d", nextSeqNo()), bizPkg, bb.bytes(), false)
}

func inboundText(push *inboundPush) string {
	var parts []string
	for _, body := range push.msgBody {
		switch body.msgType {
		case "TIMTextElem":
			if text, ok := body.msgContent["text"]; ok {
				if s, ok := text.(string); ok {
					parts = append(parts, s)
				}
			}
		case "TIMImageElem":
			url := extractImageElemURL(body.msgContent)
			if url != "" {
				parts = append(parts, "[图片] "+url)
			} else {
				parts = append(parts, "[图片]")
			}
		case "TIMFileElem":
			fileName, _ := body.msgContent["file_name"].(string)
			if fileName == "" {
				fileName, _ = body.msgContent["fileName"].(string)
			}
			if fileName != "" {
				parts = append(parts, "[文件: "+fileName+"]")
			} else {
				parts = append(parts, "[文件]")
			}
		case "TIMFaceElem":
			parts = append(parts, "[表情]")
		case "TIMSoundElem":
			parts = append(parts, "[语音]")
		case "TIMCustomElem":
			if data, ok := body.msgContent["data"]; ok {
				if s, ok := data.(string); ok && s != "" {
					var parsed map[string]interface{}
					if err := json.Unmarshal([]byte(s), &parsed); err == nil {
						if text, _ := parsed["text"].(string); text != "" {
							parts = append(parts, text)
						} else {
							parts = append(parts, s)
						}
					} else {
						parts = append(parts, s)
					}
				}
			}
		default:
			if desc, ok := body.msgContent["desc"]; ok {
				if s, ok := desc.(string); ok {
					parts = append(parts, s)
				}
			}
		}
	}
	return strings.Join(parts, "")
}

// extractImageElemURL tries to extract an image URL from a TIMImageElem msgContent.
// Priority: image_info_array → url → originalUrl.
func extractImageElemURL(c map[string]interface{}) string {
	if infoArray, ok := c["image_info_array"].([]interface{}); ok {
		for _, item := range infoArray {
			if info, ok := item.(map[string]interface{}); ok {
				// type=1: original, 2: large, 3: thumbnail
				if u, _ := info["url"].(string); u != "" {
					return u
				}
			}
		}
	}
	if url, _ := c["url"].(string); url != "" {
		return url
	}
	if url, _ := c["originalUrl"].(string); url != "" {
		return url
	}
	return ""
}

type jsonInboundPush struct {
	CallbackCommand string          `json:"callback_command"`
	FromAccount     string          `json:"from_account"`
	ToAccount       string          `json:"to_account"`
	SenderNickname  string          `json:"sender_nickname"`
	GroupCode       string          `json:"group_code"`
	GroupName       string          `json:"group_name"`
	MsgID           string          `json:"msg_id"`
	MsgKey          string          `json:"msg_key"`
	MsgBody         json.RawMessage `json:"msg_body"`
	MsgTime         int             `json:"msg_time"`
}

var seqCounter uint32

func nextSeqNo() int {
	n := seqCounter
	seqCounter++
	return int(n)
}
