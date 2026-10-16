package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// A user image must travel as an inline ACP image block when the agent
// advertised `promptCapabilities.image`; otherwise it silently degrades to a
// bare file path and the model loses the pixels. This regression covers the
// capability gate and the mimeType/empty-data guards that decide which images
// can make that trip.
func TestSplitInlineImages_CapabilityGatesImageBlocks(t *testing.T) {
	png := core.ImageAttachment{MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}

	t.Run("capable agent gets an inline block", func(t *testing.T) {
		inline, fallback := splitInlineImages([]core.ImageAttachment{png}, true)
		if len(inline) != 1 {
			t.Fatalf("inline = %d, want 1", len(inline))
		}
		if len(fallback) != 0 {
			t.Fatalf("fallback = %d, want 0", len(fallback))
		}
	})

	t.Run("incapable agent keeps the path fallback", func(t *testing.T) {
		inline, fallback := splitInlineImages([]core.ImageAttachment{png}, false)
		if len(inline) != 0 {
			t.Fatalf("inline = %d, want 0", len(inline))
		}
		if len(fallback) != 1 {
			t.Fatalf("fallback = %d, want 1", len(fallback))
		}
	})
}

func TestSplitInlineImages_SupportedMimeTypes(t *testing.T) {
	data := []byte{1, 2, 3}
	for _, mt := range []string{"image/png", "image/jpeg", "image/webp", "image/gif", "IMAGE/PNG", " image/jpeg "} {
		inline, fallback := splitInlineImages([]core.ImageAttachment{{MimeType: mt, Data: data}}, true)
		if len(inline) != 1 || len(fallback) != 0 {
			t.Errorf("mimeType %q: inline=%d fallback=%d, want 1/0", mt, len(inline), len(fallback))
		}
	}
}

// An image whose mimeType the ACP image block schema cannot express, or whose
// bytes are missing, must degrade to a path instead of failing the whole
// prompt: one bad attachment must not reject the text and the other images.
func TestSplitInlineImages_DowngradesUndecodableImages(t *testing.T) {
	cases := []struct {
		name string
		img  core.ImageAttachment
	}{
		{"unsupported mimeType", core.ImageAttachment{MimeType: "image/bmp", Data: []byte{1}}},
		{"missing mimeType", core.ImageAttachment{MimeType: "", Data: []byte{1}}},
		{"empty data", core.ImageAttachment{MimeType: "image/png", Data: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inline, fallback := splitInlineImages([]core.ImageAttachment{tc.img}, true)
			if len(inline) != 0 {
				t.Errorf("inline = %d, want 0", len(inline))
			}
			if len(fallback) != 1 {
				t.Errorf("fallback = %d, want 1", len(fallback))
			}
		})
	}
}

func TestSplitInlineImages_MixedBatchKeepsOrder(t *testing.T) {
	ok1 := core.ImageAttachment{MimeType: "image/png", Data: []byte{1}}
	bad := core.ImageAttachment{MimeType: "image/bmp", Data: []byte{2}}
	ok2 := core.ImageAttachment{MimeType: "image/jpeg", Data: []byte{3}}

	inline, fallback := splitInlineImages([]core.ImageAttachment{ok1, bad, ok2}, true)
	if len(inline) != 2 || inline[0].Data[0] != 1 || inline[1].Data[0] != 3 {
		t.Fatalf("inline = %+v, want first and third in order", inline)
	}
	if len(fallback) != 1 || fallback[0].Data[0] != 2 {
		t.Fatalf("fallback = %+v, want the middle image", fallback)
	}
}

func TestSplitInlineImages_NoImages(t *testing.T) {
	if inline, fallback := splitInlineImages(nil, true); inline != nil || fallback != nil {
		t.Fatalf("inline=%v fallback=%v, want nil/nil", inline, fallback)
	}
}

// The wire shape is what the agent actually reads, so assert the encoded
// block rather than the partition alone.
func TestSplitInlineImages_Base64RoundTripMatchesBytes(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0x00}
	inline, _ := splitInlineImages([]core.ImageAttachment{{MimeType: "image/png", Data: raw}}, true)
	if len(inline) != 1 {
		t.Fatalf("inline = %d, want 1", len(inline))
	}
	block := map[string]any{
		"type":     "image",
		"mimeType": normalizeImageMimeType(inline[0].MimeType),
		"data":     base64.StdEncoding.EncodeToString(inline[0].Data),
	}
	if block["type"] != "image" || block["mimeType"] != "image/png" {
		t.Fatalf("block = %+v", block)
	}
	decoded, err := base64.StdEncoding.DecodeString(block["data"].(string))
	if err != nil {
		t.Fatalf("decode block data: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("decoded = %v, want %v", decoded, raw)
	}
}

// A mimeType that only differs by case or padding would pass a case-insensitive
// check and then be rejected by an agent that requires the exact lowercase
// canonical, failing the whole prompt. What is validated must be what is sent.
func TestNormalizeImageMimeType_MatchesValidatedForm(t *testing.T) {
	cases := map[string]string{
		"image/png":    "image/png",
		"IMAGE/PNG":    "image/png",
		" image/jpeg ": "image/jpeg",
		"Image/WebP":   "image/webp",
		"":             "",
	}
	for in, want := range cases {
		if got := normalizeImageMimeType(in); got != want {
			t.Errorf("normalizeImageMimeType(%q) = %q, want %q", in, got, want)
		}
	}

	// The normalized value must be the one that passes validation, so a
	// passthrough of the raw value can never disagree with the guard.
	for _, raw := range []string{"IMAGE/PNG", " Image/Png "} {
		inline, fallback := splitInlineImages([]core.ImageAttachment{{MimeType: raw, Data: []byte{1}}}, true)
		if len(inline) != 1 || len(fallback) != 0 {
			t.Fatalf("raw %q: inline=%d fallback=%d, want 1/0", raw, len(inline), len(fallback))
		}
		if got := normalizeImageMimeType(inline[0].MimeType); got != "image/png" {
			t.Fatalf("raw %q normalizes to %q, want image/png", raw, got)
		}
	}
}

// End-to-end within the package: drive the real Send path and assert what
// actually crosses the wire, since that is what the agent reads. The
// partition tests above can all pass while Send still emits a text-only
// prompt, which is exactly the bug being fixed.
func TestSend_EmitsInlineImageBlockWhenAdvertised(t *testing.T) {
	pipeR, wReq := io.Pipe()
	rResp, mockW := io.Pipe()
	tr := newTransport(rResp, wReq, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.readLoop(ctx)

	requests := make(chan map[string]any, 4)
	go func() {
		sc := bufio.NewScanner(pipeR)
		for sc.Scan() {
			var req map[string]any
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			requests <- req
			// Answer session/prompt so Send can complete.
			_, _ = fmt.Fprintf(mockW, `{"jsonrpc":"2.0","id":%v,"result":{"stopReason":"end_turn"}}`+"\n", req["id"])
		}
	}()

	s := &acpSession{
		workDir: t.TempDir(),
		events:  make(chan core.Event, 8),
		ctx:     ctx,
		tr:      tr,
	}
	s.alive.Store(true)
	s.setACPSessionID("session-under-test")

	imgData := []byte{0x89, 'P', 'N', 'G', 0x01, 0x02}
	s.imagePromptsEnabled.Store(true)
	if err := s.Send("what is in this picture?", "msg-1", []core.ImageAttachment{
		{MimeType: "image/png", Data: imgData},
	}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case req := <-requests:
		if req["method"] != "session/prompt" {
			t.Fatalf("method = %v, want session/prompt", req["method"])
		}
		params, _ := req["params"].(map[string]any)
		blocks, _ := params["prompt"].([]any)
		if len(blocks) != 2 {
			t.Fatalf("prompt blocks = %d, want text + image", len(blocks))
		}
		text, _ := blocks[0].(map[string]any)
		if text["type"] != "text" || text["text"] != "what is in this picture?" {
			t.Fatalf("block[0] = %+v, want the prompt text", text)
		}
		image, _ := blocks[1].(map[string]any)
		if image["type"] != "image" || image["mimeType"] != "image/png" {
			t.Fatalf("block[1] = %+v, want an image block", image)
		}
		if image["data"] != base64.StdEncoding.EncodeToString(imgData) {
			t.Fatalf("block[1].data = %v, want base64 of the pixels", image["data"])
		}
		// A path-based fallback would have smuggled the file location into
		// the text block; an inline block must not need it.
		if txt, _ := text["text"].(string); strings.Contains(txt, "saved locally") {
			t.Fatalf("text block leaked the path fallback: %q", txt)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no session/prompt reached the wire")
	}
}

// The legacy path must survive: an agent that does not advertise image
// support still receives the file location as text, as before the fix.
func TestSend_FallsBackToPathWhenNotAdvertised(t *testing.T) {
	pipeR, wReq := io.Pipe()
	rResp, mockW := io.Pipe()
	tr := newTransport(rResp, wReq, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.readLoop(ctx)

	requests := make(chan map[string]any, 4)
	go func() {
		sc := bufio.NewScanner(pipeR)
		for sc.Scan() {
			var req map[string]any
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			requests <- req
			_, _ = fmt.Fprintf(mockW, `{"jsonrpc":"2.0","id":%v,"result":{"stopReason":"end_turn"}}`+"\n", req["id"])
		}
	}()

	s := &acpSession{workDir: t.TempDir(), events: make(chan core.Event, 8), ctx: ctx, tr: tr}
	s.alive.Store(true)
	s.setACPSessionID("session-under-test")

	s.imagePromptsEnabled.Store(false)
	if err := s.Send("look at this", "msg-2", []core.ImageAttachment{
		{MimeType: "image/png", Data: []byte{1, 2, 3}},
	}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case req := <-requests:
		params, _ := req["params"].(map[string]any)
		blocks, _ := params["prompt"].([]any)
		if len(blocks) != 1 {
			t.Fatalf("prompt blocks = %d, want one text block", len(blocks))
		}
		text, _ := blocks[0].(map[string]any)
		body, _ := text["text"].(string)
		if !strings.Contains(body, "saved locally") {
			t.Fatalf("text block = %q, want the path fallback note", body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no session/prompt reached the wire")
	}
}
