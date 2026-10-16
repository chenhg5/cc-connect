package acp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// toolCallUpdateParams builds the session/update envelope mapSessionUpdate expects.
func toolCallUpdateParams(t *testing.T, update map[string]any) json.RawMessage {
	t.Helper()
	update["sessionUpdate"] = "tool_call_update"
	raw, err := json.Marshal(map[string]any{"sessionId": "s1", "update": update})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func contentText(text string) map[string]any {
	return map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}}
}

func contentImage(mimeType, data string) map[string]any {
	return map[string]any{"type": "content", "content": map[string]any{
		"type": "image", "data": data, "mimeType": mimeType,
	}}
}

// DeepSeek Harness commits a rendered picture inside the tool result rather than
// the assistant message, wrapped one level deeper than the ACP canonical form:
//
//	{"type":"content","content":{"type":"image","data":"<base64>","mimeType":...}}
//
// The old struct only declared Text, so the bytes were dropped and the user got
// prose claiming an image had been sent. This pins the decode.
func TestMapToolCallUpdate_ReadImageCarriesBytes(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3, 4}
	params := toolCallUpdateParams(t, map[string]any{
		"toolCallId": "call_1",
		"title":      "read_image",
		"status":     "completed",
		"content": []any{
			contentText("<path>/tmp/x.png</path>\n<type>image</type>"),
			contentImage("image/png", base64.StdEncoding.EncodeToString(png)),
		},
	})

	evs := mapSessionUpdate("", params)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(evs), evs)
	}
	if evs[0].Type != core.EventToolResult {
		t.Fatalf("Type = %v, want EventToolResult", evs[0].Type)
	}
	if len(evs[0].Images) != 1 {
		t.Fatalf("Images = %d, want 1 (the bytes must survive the mapping)", len(evs[0].Images))
	}
	got := evs[0].Images[0]
	if got.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", got.MimeType)
	}
	if string(got.Data) != string(png) {
		t.Errorf("Data = %v, want the original bytes %v", got.Data, png)
	}
}

// An image-only terminal result must still produce an event: the text is empty
// by design, and dropping it would lose the picture entirely.
func TestMapToolCallUpdate_ImageOnlyResultSurvives(t *testing.T) {
	params := toolCallUpdateParams(t, map[string]any{
		"toolCallId": "c",
		"title":      "read_image",
		"status":     "completed",
		"content": []any{
			contentImage("image/png", base64.StdEncoding.EncodeToString([]byte{1, 2, 3})),
		},
	})
	evs := mapSessionUpdate("", params)
	if len(evs) != 1 || len(evs[0].Images) != 1 {
		t.Fatalf("got %+v, want one event carrying one image", evs)
	}
}

// A real DeepSeek Harness tool_call_update omits `title` entirely, so any
// admission rule keyed on the tool name drops every image. This payload shape
// (no title, status completed, image in the second block) is what the wire
// actually carries.
func TestMapToolCallUpdate_ImageWithoutTitleIsStillCarried(t *testing.T) {
	params := toolCallUpdateParams(t, map[string]any{
		"toolCallId": "call_00_Og3cNZtPOdFCTLm4x2nI0594",
		"status":     "completed",
		"content": []any{
			contentText("<path>/tmp/x.png</path>\n<type>image</type>"),
			contentImage("image/png", base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 1, 2, 3})),
		},
	})
	evs := mapSessionUpdate("", params)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if len(evs[0].Images) != 1 {
		t.Fatalf("Images = %d, want 1 (admission must not depend on the tool title)", len(evs[0].Images))
	}
}

// An in-progress update carries a partial result; images are only committed on
// the terminal update, so a non-terminal frame must not send anything yet.
func TestMapToolCallUpdate_InProgressDoesNotCarryImages(t *testing.T) {
	params := toolCallUpdateParams(t, map[string]any{
		"toolCallId": "c",
		"title":      "read_image",
		"status":     "in_progress",
		"content": []any{
			contentImage("image/png", base64.StdEncoding.EncodeToString([]byte{1, 2, 3})),
		},
	})
	evs := mapSessionUpdate("", params)
	for _, ev := range evs {
		if len(ev.Images) != 0 {
			t.Fatalf("in-progress frame carried %d images, want 0", len(ev.Images))
		}
	}
}

func TestExtractToolCallImages_RejectsUndecodableAndUnsupported(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte{9, 9})
	blocks := func(mimeType, data string) []acpContentBlock {
		var b acpContentBlock
		b.Type = "content"
		b.Content.Type = "image"
		b.Content.MimeType = mimeType
		b.Content.Data = data
		return []acpContentBlock{b}
	}
	cases := []struct {
		name string
		in   []acpContentBlock
		want int
	}{
		{"valid", blocks("image/png", png), 1},
		{"case-insensitive mime", blocks("IMAGE/PNG", png), 1},
		{"unsupported mime", blocks("image/bmp", png), 0},
		{"not base64", blocks("image/png", "!!!not-base64!!!"), 0},
		{"empty data", blocks("image/png", ""), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractToolCallImages(tc.in)
			if len(got) != tc.want {
				t.Fatalf("got %d images, want %d", len(got), tc.want)
			}
			if len(got) == 1 && got[0].MimeType != "image/png" {
				t.Errorf("MimeType = %q, want normalized image/png", got[0].MimeType)
			}
		})
	}
}

// The tests above compare hand-made byte slices, which cannot show that what
// arrives is still a usable picture. This one carries a real PNG through the
// literal wire JSON — base64 in the text, decoded by the mapper, re-decoded by
// the receiver — and asserts the delivered bytes are still a valid image of the
// original size.
func TestMapToolCallUpdate_RealPNGSurvivesTheWireJSON(t *testing.T) {
	var buf bytes.Buffer
	src := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for i := range src.Pix { // deterministic, non-trivial content
		src.Pix[i] = uint8(i * 7)
	}
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("encode fixture png: %v", err)
	}

	// A literal JSON document rather than a marshalled Go map, so the exact
	// wire shape is pinned: the doubly-nested content block, the
	// <path>/<type> preamble in the text block, and no `title` at all.
	raw := fmt.Sprintf(`{
		"sessionId": "s-wire",
		"update": {
			"sessionUpdate": "tool_call_update",
			"toolCallId": "call_wire_1",
			"status": "completed",
			"content": [
				{"type": "content", "content": {"type": "text", "text": "<path>./render.png</path>\n<type>image</type>"}},
				{"type": "content", "content": {"type": "image", "data": %q, "mimeType": "image/png"}}
			]
		}
	}`, base64.StdEncoding.EncodeToString(buf.Bytes()))

	evs := mapSessionUpdate("", json.RawMessage(raw))
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(evs), evs)
	}
	if evs[0].Type != core.EventToolResult {
		t.Fatalf("Type = %v, want EventToolResult", evs[0].Type)
	}
	if len(evs[0].Images) != 1 {
		t.Fatalf("Images = %d, want 1", len(evs[0].Images))
	}
	if got := evs[0].Images[0].MimeType; got != "image/png" {
		t.Errorf("MimeType = %q, want image/png", got)
	}
	decoded, err := png.Decode(bytes.NewReader(evs[0].Images[0].Data))
	if err != nil {
		t.Fatalf("delivered bytes are not a decodable PNG: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != 4 || b.Dy() != 3 {
		t.Errorf("decoded bounds = %v, want 4x3", b)
	}
}
