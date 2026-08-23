package core

import (
	"strings"
	"testing"
)

func TestWorkspaceChatVerifiedFileAndAudioBecomeTextInputs(t *testing.T) {
	service := &WorkspaceChatService{engine: &Engine{i18n: NewI18n(LangEnglish)}}
	inputs, err := service.validateNativeInputs([]NativeUserInput{
		{Type: "file", LocalPath: "/workspace/report.pdf"},
		{Type: "audio", LocalPath: "/workspace/voice.amr"},
		{Type: "image", LocalPath: "/workspace/image.png"},
	}, true)
	if err != nil {
		t.Fatalf("validateNativeInputs() error = %v", err)
	}
	if len(inputs) != 3 {
		t.Fatalf("validateNativeInputs() length = %d, want 3", len(inputs))
	}
	for index, path := range []string{"/workspace/report.pdf", "/workspace/voice.amr"} {
		if inputs[index].Type != "text" || !strings.Contains(inputs[index].Text, path) {
			t.Fatalf("converted input %d = %#v, want verified text reference to %s", index, inputs[index], path)
		}
		if inputs[index].LocalPath != "" {
			t.Fatalf("converted input %d leaked LocalPath = %q", index, inputs[index].LocalPath)
		}
	}
	if inputs[2].Type != "image" || inputs[2].LocalPath != "/workspace/image.png" {
		t.Fatalf("image input = %#v, want verified local image", inputs[2])
	}
}

func TestWorkspaceChatBrowserCannotSubmitAttachmentPaths(t *testing.T) {
	service := &WorkspaceChatService{engine: &Engine{i18n: NewI18n(LangEnglish)}}
	for _, inputType := range []string{"audio", "file", "image"} {
		_, err := service.validateNativeInputs([]NativeUserInput{{Type: inputType, LocalPath: "/server/private"}}, false)
		if err == nil {
			t.Fatalf("validateNativeInputs(%q) accepted an untrusted server path", inputType)
		}
	}
}
