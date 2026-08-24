package core

import "testing"

func TestWorkspaceChatVerifiedAttachmentsRemainOpaqueMediaInputs(t *testing.T) {
	service := &WorkspaceChatService{i18n: NewI18n(LangEnglish)}
	inputs, err := service.validateNativeInputs([]NativeUserInput{
		{Type: "file", Data: []byte("report"), FileName: "report.pdf"},
		{Type: "audio", Data: []byte("voice"), FileName: "voice.amr"},
		{Type: "image", Data: []byte("image"), FileName: "image.png"},
	}, true)
	if err != nil {
		t.Fatalf("validateNativeInputs() error = %v", err)
	}
	if len(inputs) != 3 {
		t.Fatalf("validateNativeInputs() length = %d, want 3", len(inputs))
	}
	for index, inputType := range []string{"file", "audio", "image"} {
		if inputs[index].Type != inputType || len(inputs[index].Data) == 0 || inputs[index].LocalPath != "" || inputs[index].AttachmentRef != "" {
			t.Fatalf("verified input %d = %#v", index, inputs[index])
		}
	}
}

func TestWorkspaceChatBrowserCannotSubmitAttachmentPaths(t *testing.T) {
	service := &WorkspaceChatService{i18n: NewI18n(LangEnglish)}
	for _, inputType := range []string{"audio", "file", "image"} {
		for _, input := range []NativeUserInput{
			{Type: inputType, LocalPath: "/server/private"},
			{Type: inputType, AttachmentRef: "att_forged"},
		} {
			if _, err := service.validateNativeInputs([]NativeUserInput{input}, false); err == nil {
				t.Fatalf("validateNativeInputs(%q) accepted untrusted media %#v", inputType, input)
			}
		}
	}
}
