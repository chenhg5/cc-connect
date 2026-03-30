package wecom

import (
	"encoding/xml"
	"testing"
)

func TestWecomInboundFileMime(t *testing.T) {
	t.Parallel()
	if got := wecomInboundFileMime("report.pdf", []byte("%PDF-1.4")); got != "application/pdf" {
		t.Fatalf("pdf by extension: got %q", got)
	}
	if got := wecomInboundFileMime("unknown.bin", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); got != "image/png" {
		t.Fatalf("png by magic: got %q", got)
	}
	if got := wecomInboundFileMime("weird", []byte{0x00, 0x01, 0x02}); got != "application/octet-stream" {
		t.Fatalf("fallback: got %q", got)
	}
}

func TestXMLMessageFile(t *testing.T) {
	t.Parallel()
	raw := `<xml>
<ToUserName><![CDATA[to]]></ToUserName>
<FromUserName><![CDATA[from]]></FromUserName>
<CreateTime>123</CreateTime>
<MsgType><![CDATA[file]]></MsgType>
<MediaId><![CDATA[mid]]></MediaId>
<FileName><![CDATA[../dir/doc.pdf]]></FileName>
<MsgId>999</MsgId>
<AgentID>1</AgentID>
</xml>`
	var msg xmlMessage
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.MsgType != "file" || msg.MediaId != "mid" || msg.FileName != "../dir/doc.pdf" {
		t.Fatalf("parsed: %+v", msg)
	}
	if msg.MsgId != 999 {
		t.Fatalf("MsgId = %d", msg.MsgId)
	}
}
