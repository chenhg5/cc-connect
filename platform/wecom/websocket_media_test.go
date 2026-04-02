package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	texts, imgs, files := wsCollectInboundParts(&body)
	if len(texts) != 0 || len(imgs) != 0 || len(files) != 1 || files[0].URL != "https://example.com/f" {
		t.Fatalf("files=%v texts=%v imgs=%v", files, texts, imgs)
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
	texts, imgs, files := wsCollectInboundParts(&body)
	if len(texts) != 1 || texts[0] != "see" || len(imgs) != 1 || imgs[0].URL != "https://i" || len(files) != 0 {
		t.Fatalf("texts=%v imgs=%v files=%v", texts, imgs, files)
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
	texts, imgs, files := wsCollectInboundParts(&body)
	if len(files) != 1 || files[0].URL != "https://example.com/doc.pdf" || len(imgs) != 0 {
		t.Fatalf("texts=%v imgs=%v files=%v", texts, imgs, files)
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
	texts, imgs, files := wsCollectInboundParts(&body)
	if len(texts) != 1 || len(imgs) != 0 || len(files) != 1 || files[0].URL != "https://f" {
		t.Fatalf("texts=%v imgs=%v files=%v", texts, imgs, files)
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
