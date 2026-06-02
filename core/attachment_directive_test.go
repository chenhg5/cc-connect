package core

import (
	"path/filepath"
	"testing"
)

func TestExtractAttachmentDirectives_StripsStrictBlock(t *testing.T) {
	input := "Here is the image.\n\n```cc-connect-attachments\nimage: /tmp/chart.png\nfile: /tmp/report.pdf\n```\n\nDone."

	clean, attachments, err := extractAttachmentDirectives(input)
	if err != nil {
		t.Fatalf("extractAttachmentDirectives returned error: %v", err)
	}
	if clean != "Here is the image.\n\nDone." {
		t.Fatalf("clean = %q", clean)
	}
	if len(attachments) != 2 {
		t.Fatalf("attachments len = %d, want 2", len(attachments))
	}
	if attachments[0].kind != "image" || attachments[0].path != "/tmp/chart.png" {
		t.Fatalf("first attachment = %#v", attachments[0])
	}
	if attachments[1].kind != "file" || attachments[1].path != "/tmp/report.pdf" {
		t.Fatalf("second attachment = %#v", attachments[1])
	}
}

func TestExtractAttachmentDirectives_IgnoresNormalMarkdownLinks(t *testing.T) {
	input := "See [image](/tmp/chart.png), but do not send it automatically."

	clean, attachments, err := extractAttachmentDirectives(input)
	if err != nil {
		t.Fatalf("extractAttachmentDirectives returned error: %v", err)
	}
	if clean != input {
		t.Fatalf("clean = %q, want original", clean)
	}
	if len(attachments) != 0 {
		t.Fatalf("attachments = %#v, want none", attachments)
	}
}

func TestBuildAttachmentFromDirective_RequiresAbsoluteExistingFile(t *testing.T) {
	_, err := buildAttachmentFromDirective(outboundAttachmentDirective{kind: "image", path: "relative.png"})
	if err == nil {
		t.Fatal("expected relative path to fail")
	}

	missing := filepath.Join(t.TempDir(), "missing.png")
	_, err = buildAttachmentFromDirective(outboundAttachmentDirective{kind: "image", path: missing})
	if err == nil {
		t.Fatal("expected missing file to fail")
	}
}
