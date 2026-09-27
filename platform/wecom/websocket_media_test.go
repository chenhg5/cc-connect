package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseContentDispositionFilename(t *testing.T) {
	t.Parallel()
	got := parseContentDispositionFilename(`attachment; filename="doc.pdf"`)
	if got != "doc.pdf" {
		t.Fatalf("got %q", got)
	}
	got = parseContentDispositionFilename(`attachment; filename*=UTF-8''%E4%B8%AD.txt`)
	if got != "中.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestWsCollectInboundParts_fileAndQuote(t *testing.T) {
	t.Parallel()
	raw := `{
		"msgid": "1",
		"aibotid": "bot",
		"chatid": "c1",
		"chattype": "single",
		"from": {"userid": "u1"},
		"msgtype": "file",
		"file": {"url": "https://example.com/f", "aeskey": "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo="}
	}`
	var body wsMsgCallbackBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	current, quoted := wsCollectInboundParts(&body)
	if len(current.content) != 0 || len(current.images) != 0 || len(current.files) != 1 || current.files[0].URL != "https://example.com/f" || len(quoted.content) != 0 {
		t.Fatalf("current=%+v quoted=%+v", current, quoted)
	}
}

func TestWsCollectInboundParts_mixed(t *testing.T) {
	t.Parallel()
	raw := `{
		"msgid": "2",
		"aibotid": "bot",
		"chattype": "group",
		"chatid": "g1",
		"from": {"userid": "u1"},
		"msgtype": "mixed",
		"mixed": {
			"msg_item": [
				{"msgtype": "text", "text": {"content": "see"}},
				{"msgtype": "image", "image": {"url": "https://i", "aeskey": "k"}}
			]
		}
	}`
	var body wsMsgCallbackBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	current, quoted := wsCollectInboundParts(&body)
	if len(current.content) != 1 || current.content[0] != "see" || len(current.images) != 1 || current.images[0].URL != "https://i" || len(current.files) != 0 || len(quoted.content) != 0 {
		t.Fatalf("current=%+v quoted=%+v", current, quoted)
	}
}

func TestWsCollectInboundParts_fileWithNonEmptyMixedUsesTopLevelFile(t *testing.T) {
	t.Parallel()
	raw := `{
		"msgid": "3",
		"aibotid": "bot",
		"chattype": "single",
		"from": {"userid": "u1"},
		"msgtype": "file",
		"mixed": {
			"msg_item": [
				{"msgtype": "text", "text": {"content": "  "}}
			]
		},
		"file": {"url": "https://example.com/doc.pdf", "aeskey": "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo="}
	}`
	var body wsMsgCallbackBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	current, quoted := wsCollectInboundParts(&body)
	if len(current.files) != 1 || current.files[0].URL != "https://example.com/doc.pdf" || len(current.images) != 0 || len(quoted.content) != 0 {
		t.Fatalf("current=%+v quoted=%+v", current, quoted)
	}
}

func TestWsCollectInboundParts_mixedContainsFile(t *testing.T) {
	t.Parallel()
	raw := `{
		"msgid": "4",
		"aibotid": "bot",
		"chattype": "group",
		"chatid": "g1",
		"from": {"userid": "u1"},
		"msgtype": "mixed",
		"mixed": {
			"msg_item": [
				{"msgtype": "text", "text": {"content": "see file"}},
				{"msgtype": "file", "file": {"url": "https://f", "aeskey": "k"}}
			]
		}
	}`
	var body wsMsgCallbackBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	current, quoted := wsCollectInboundParts(&body)
	if len(current.content) != 1 || len(current.images) != 0 || len(current.files) != 1 || current.files[0].URL != "https://f" || len(quoted.content) != 0 {
		t.Fatalf("current=%+v quoted=%+v", current, quoted)
	}
}

func TestWsCollectInboundParts_SeparatesQuotedMixedContent(t *testing.T) {
	t.Parallel()
	raw := `{
		"msgid": "5",
		"msgtype": "mixed",
		"mixed": {"msg_item": [
			{"msgtype": "text", "text": {"content": "new instruction"}},
			{"msgtype": "file", "file": {"url": "https://current-file", "aeskey": "current-key"}}
		]},
		"quote": {"msgtype": "mixed", "mixed": {"msg_item": [
			{"msgtype": "text", "text": {"content": "quoted text"}},
			{"msgtype": "image", "image": {"url": "https://quoted-image", "aeskey": "image-key"}},
			{"msgtype": "file", "file": {"url": "https://quoted-file", "aeskey": "file-key"}}
		]}}
	}`
	var body wsMsgCallbackBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}

	current, quoted := wsCollectInboundParts(&body)
	if got, want := strings.Join(current.content, "\n"), "new instruction"; got != want {
		t.Fatalf("current content = %q, want %q", got, want)
	}
	if len(current.files) != 1 || current.files[0].URL != "https://current-file" {
		t.Fatalf("current files = %+v", current.files)
	}
	if got, want := strings.Join(quoted.content, "\n"), "quoted text\n[image]\n[file]"; got != want {
		t.Fatalf("quoted content = %q, want %q", got, want)
	}
	if len(quoted.images) != 1 || quoted.images[0].URL != "https://quoted-image" || len(quoted.files) != 1 || quoted.files[0].URL != "https://quoted-file" {
		t.Fatalf("quoted media = %+v %+v", quoted.images, quoted.files)
	}
	if got, want := formatWSQuotedContent(quoted), "[Quoted message]:\nquoted text\n[image]\n[file]\n\n"; got != want {
		t.Fatalf("quoted context = %q, want %q", got, want)
	}
}

func TestDeliverWSMediaInbound_QuotedAttachmentsPrecedeCurrentAttachments(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/quoted":
			w.Header().Set("Content-Disposition", `attachment; filename="quoted.png"`)
			_, _ = w.Write([]byte("quoted image"))
		case "/current":
			w.Header().Set("Content-Disposition", `attachment; filename="current.png"`)
			_, _ = w.Write([]byte("current image"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p, captured := newCapturedWSPlatform()
	body := &wsMsgCallbackBody{MsgID: "quoted-media", AibotID: "bot"}
	p.deliverWSMediaInbound(
		body,
		"wecom:chat:user",
		"",
		wsReplyContext{},
		wsInboundParts{content: []string{"inspect this"}, images: []wsMediaRef{{URL: srv.URL + "/current"}}},
		wsInboundParts{content: []string{"[image]"}, images: []wsMediaRef{{URL: srv.URL + "/quoted"}}},
		false,
	)

	msg := <-captured
	if msg.Content != "inspect this" {
		t.Fatalf("Content = %q", msg.Content)
	}
	if got, want := msg.ExtraContent, "[Quoted message]:\n[image]\n\n"; got != want {
		t.Fatalf("ExtraContent = %q, want %q", got, want)
	}
	if len(msg.Images) != 2 || string(msg.Images[0].Data) != "quoted image" || string(msg.Images[1].Data) != "current image" {
		t.Fatalf("image order = %#v", msg.Images)
	}
}

func TestDeliverWSMediaInbound_QuotedDownloadFailureKeepsContextMarker(t *testing.T) {
	t.Parallel()
	p, captured := newCapturedWSPlatform()
	body := &wsMsgCallbackBody{MsgID: "quoted-media-failure"}
	p.deliverWSMediaInbound(
		body,
		"wecom:chat:user",
		"",
		wsReplyContext{},
		wsInboundParts{content: []string{"what is this?"}},
		wsInboundParts{content: []string{"[image]"}, images: []wsMediaRef{{URL: "http://127.0.0.1:1/unavailable"}}},
		false,
	)

	msg := <-captured
	if got, want := msg.ExtraContent, "[Quoted message]:\n[image]\n\n"; got != want {
		t.Fatalf("ExtraContent = %q, want %q", got, want)
	}
	if len(msg.Images) != 0 {
		t.Fatalf("Images = %#v, want no downloaded images", msg.Images)
	}
}

func TestDecodeWeComAESKey_URLSafeUnpadded(t *testing.T) {
	t.Parallel()
	want := make([]byte, 32)
	for i := range want {
		want[i] = byte(i + 1)
	}
	std := base64.StdEncoding.EncodeToString(want)
	us := strings.ReplaceAll(std, "+", "-")
	us = strings.ReplaceAll(us, "/", "_")
	us = strings.TrimRight(us, "=")

	got, err := decodeWeComAESKey(us)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 32 {
		t.Fatalf("len=%d", len(got))
	}
	got = got[:32]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d: got %d want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeWeComAESKey_hex64(t *testing.T) {
	t.Parallel()
	want := bytes.Repeat([]byte{0xab}, 32)
	hexStr := hex.EncodeToString(want)
	got, err := decodeWeComAESKey(hexStr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
}

func TestWecomDecryptFile_AES256CBC(t *testing.T) {
	t.Parallel()
	// 32-byte key; IV = first 16 bytes (WeCom scheme)
	key32 := []byte("0123456789abcdef0123456789abcdef")
	aesKeyB64 := base64.StdEncoding.EncodeToString(key32)
	plain := []byte("hello-wecom")

	padded := pkcs7PadBlock(plain, aes.BlockSize)
	block, _ := aes.NewCipher(key32)
	iv := key32[:aes.BlockSize]
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	out, err := wecomDecryptFile(ct, aesKeyB64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatalf("got %q want %q", out, plain)
	}
}

func pkcs7PadBlock(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	if pad == 0 {
		pad = blockSize
	}
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}
