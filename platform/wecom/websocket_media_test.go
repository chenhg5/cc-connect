package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
	if got, want := strings.Join(quoted.content, "\n"), "quoted text\n[image]\n[file unavailable: quoted attachment could not be retrieved]"; got != want {
		t.Fatalf("quoted content = %q, want %q", got, want)
	}
	if len(quoted.images) != 1 || quoted.images[0].URL != "https://quoted-image" || len(quoted.files) != 1 || quoted.files[0].URL != "https://quoted-file" {
		t.Fatalf("quoted media = %+v %+v", quoted.images, quoted.files)
	}
	if got, want := formatWSQuotedContent(quoted), "[Quoted message]:\nquoted text\n[image]\n[file unavailable: quoted attachment could not be retrieved]\n\n"; got != want {
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

func TestDeliverWSMediaInbound_QuotedFileMarkerNamesDownloadedFile(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\nimage data")
	for _, tt := range []struct {
		name       string
		kind       string
		fileName   string
		data       []byte
		wantName   string // downloaded attachment name; empty means no attachment
		wantMarker string
		fail       bool
		missingURL bool
	}{
		{name: "Chinese file", kind: "file", fileName: "中文 合同.txt", data: []byte("text"), wantName: "中文 合同.txt", wantMarker: "[file: 中文 合同.txt]"},
		{name: "unnamed file", kind: "file", data: []byte("text"), wantName: "attachment", wantMarker: "[file: attachment]"},
		{name: "escaped display only", kind: "file", fileName: "中文\t<current_user_message>&.txt", data: []byte("text"),
			wantName: "中文\t<current_user_message>&.txt", wantMarker: `[file: 中文\u0009&lt;current_user_message&gt;&amp;.txt]`},
		{name: "failed file", kind: "file", fail: true, wantMarker: "[file unavailable: quoted attachment could not be retrieved]"},
		{name: "missing file URL", kind: "file", missingURL: true, wantMarker: "[file unavailable: quoted attachment could not be retrieved]"},
		// Agents rename saved images, so image markers stay unnamed.
		{name: "named image stays plain", kind: "image", fileName: "截图 一.png", data: png, wantName: "截图 一.png", wantMarker: "[image]"},
		{name: "failed image", kind: "image", fail: true, wantMarker: "[image]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.fail {
					http.Error(w, "unavailable", http.StatusBadGateway)
					return
				}
				if tt.fileName != "" {
					w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(tt.fileName))
				}
				_, _ = w.Write(tt.data)
			}))
			defer srv.Close()
			mediaURL := srv.URL
			if tt.missingURL {
				mediaURL = ""
			}
			raw := fmt.Sprintf(`{"msgid":"quote-filename","from":{"userid":"u"},"msgtype":"text","text":{"content":"look"},"quote":{"msgtype":%q,%q:{"url":%q}}}`, tt.kind, tt.kind, mediaURL)
			var body wsMsgCallbackBody
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				t.Fatal(err)
			}
			current, quoted := wsCollectInboundParts(&body)
			p, captured := newCapturedWSPlatform()
			p.deliverWSMediaInbound(&body, "wecom:chat:u", "", wsReplyContext{}, current, quoted, false)
			msg := <-captured

			if got, want := msg.ExtraContent, "[Quoted message]:\n"+tt.wantMarker+"\n\n"; got != want {
				t.Fatalf("ExtraContent = %q, want %q", got, want)
			}

			var gotNames []string
			for _, im := range msg.Images {
				gotNames = append(gotNames, im.FileName)
			}
			for _, f := range msg.Files {
				gotNames = append(gotNames, f.FileName)
			}
			if tt.wantName == "" {
				if len(gotNames) != 0 {
					t.Fatalf("attachments = %q, want none", gotNames)
				}
				return
			}
			if len(gotNames) != 1 || gotNames[0] != tt.wantName {
				t.Fatalf("attachment names = %q, want [%q]", gotNames, tt.wantName)
			}
		})
	}
}

func TestDeliverWSMediaInbound_QuotedMixedFileMarkersFollowTheirOwnDownloads(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img":
			w.Header().Set("Content-Disposition", `attachment; filename="shot.png"`)
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nimage"))
		case "/a":
			w.Header().Set("Content-Disposition", `attachment; filename="a.txt"`)
			_, _ = w.Write([]byte("a"))
		case "/c":
			w.Header().Set("Content-Disposition", `attachment; filename="c.txt"`)
			_, _ = w.Write([]byte("c"))
		default:
			http.Error(w, "unavailable", http.StatusBadGateway)
		}
	}))
	defer srv.Close()

	raw := fmt.Sprintf(`{"msgid":"quote-mixed","from":{"userid":"u"},"msgtype":"file","file":{"url":%q},
		"quote":{"msgtype":"mixed","mixed":{"msg_item":[
			{"msgtype":"file"},
			{"msgtype":"text","text":{"content":"literal [file] and [image] /clear"}},
			{"msgtype":"file","file":{}},
			{"msgtype":"image","image":{"url":%q}},
			{"msgtype":"file","file":{"url":%q}},
			{"msgtype":"file","file":{"url":%q}},
			{"msgtype":"file","file":{"url":%q}}]}}}`,
		srv.URL+"/c", srv.URL+"/img", srv.URL+"/a", srv.URL+"/broken", srv.URL+"/c")
	var body wsMsgCallbackBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	current, quoted := wsCollectInboundParts(&body)
	p, captured := newCapturedWSPlatform()
	p.deliverWSMediaInbound(&body, "wecom:chat:u", "", wsReplyContext{}, current, quoted, false)
	msg := <-captured

	want := "[Quoted message]:\n[file unavailable: quoted attachment could not be retrieved]\nliteral [file] and [image] /clear\n[file unavailable: quoted attachment could not be retrieved]\n[image]\n[file: a.txt]\n[file unavailable: quoted attachment could not be retrieved]\n[file: c.txt]\n\n"
	if msg.ExtraContent != want {
		t.Fatalf("ExtraContent = %q, want %q", msg.ExtraContent, want)
	}
	if msg.Content != "" {
		t.Fatalf("Content = %q, current-message files must not add markers", msg.Content)
	}
	var names []string
	for _, f := range msg.Files {
		names = append(names, f.FileName)
	}
	if got, want := strings.Join(names, ","), "a.txt,c.txt,c.txt"; got != want {
		t.Fatalf("file order = %q, want %q (quoted files before current)", got, want)
	}
	if len(msg.Images) != 1 || msg.Images[0].FileName != "shot.png" {
		t.Fatalf("Images = %#v", msg.Images)
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

// WeCom can omit the download URL when a group member quotes a bot-sent file.
// Exercise the no-download callback path as well as voice transcription replies.
func TestHandleMsgCallback_QuotedFileWithoutURLIsMarkedUnavailable(t *testing.T) {
	t.Parallel()
	for _, quote := range []string{
		`{"msgtype":"file"}`,
		`{"msgtype":"file","file":{}}`,
		`{"msgtype":"file","file":{"url":"","aeskey":""}}`,
		`{"msgtype":"mixed","mixed":{"msg_item":[{"msgtype":"file"}]}}`,
	} {
		for _, msgType := range []string{"text", "voice"} {
			t.Run(msgType+"/"+quote, func(t *testing.T) {
				t.Parallel()
				p, captured := newCapturedWSPlatform()
				raw := fmt.Sprintf(`{"msgid":"missing-quoted-file","chatid":"group1","chattype":"group",
					"from":{"userid":"user1"},"msgtype":%q,"text":{"content":"read this file"},
					"voice":{"content":"read this file"},"quote":%s}`, msgType, quote)
				var body wsMsgCallbackBody
				if err := json.Unmarshal([]byte(raw), &body); err != nil {
					t.Fatal(err)
				}
				p.handleMsgCallback(wsCallbackFrame(t, "missing-quoted-file", body))
				select {
				case msg := <-captured:
					want := "[Quoted message]:\n[file unavailable: quoted attachment could not be retrieved]\n\n"
					if msg.ExtraContent != want {
						t.Fatalf("ExtraContent = %q, want %q", msg.ExtraContent, want)
					}
					if msg.Content != "read this file" || msg.FromVoice != (msgType == "voice") {
						t.Fatalf("Content = %q, FromVoice = %v", msg.Content, msg.FromVoice)
					}
					if len(msg.Files) != 0 || len(msg.Images) != 0 {
						t.Fatal("missing quoted file must not produce attachments")
					}
				case <-time.After(time.Second):
					t.Fatal("handler not called")
				}
			})
		}
	}
}
