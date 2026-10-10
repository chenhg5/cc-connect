package core

import (
	"testing"
)

// Images an agent produced inside a tool result must reach the platform on
// their own channel. Before this, the bytes were dropped in the ACP mapping and
// the user only saw prose claiming a picture had been sent.
func TestSendEventImages_DeliversToCapablePlatform(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	png := []byte{0x89, 'P', 'N', 'G'}
	e.sendEventImages(p, "ctx", []ImageAttachment{{MimeType: "image/png", Data: png}})

	if len(p.images) != 1 {
		t.Fatalf("SendImage calls = %d, want 1", len(p.images))
	}
	if p.images[0].MimeType != "image/png" || string(p.images[0].Data) != string(png) {
		t.Fatalf("delivered image = %+v, want the original bytes", p.images[0])
	}
}

func TestSendEventImages_NoImagesIsNoop(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	e.sendEventImages(p, "ctx", nil)
	e.sendEventImages(p, "ctx", []ImageAttachment{})

	if len(p.images) != 0 {
		t.Fatalf("SendImage calls = %d, want 0", len(p.images))
	}
}

// A text-only platform must be a silent no-op: the tool call itself succeeded,
// so a missing sender must not fail the turn or emit an error.
func TestSendEventImages_PlatformWithoutSenderIsNoop(t *testing.T) {
	p := &stubPlatformEngine{n: "text-only"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	e.sendEventImages(p, "ctx", []ImageAttachment{{MimeType: "image/png", Data: []byte{1}}})
}

func TestSendEventImages_DisabledByAttachmentSend(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAttachmentSendEnabled(false)

	e.sendEventImages(p, "ctx", []ImageAttachment{{MimeType: "image/png", Data: []byte{1}}})

	if len(p.images) != 0 {
		t.Fatalf("SendImage calls = %d, want 0 when attachment send-back is off", len(p.images))
	}
}
