package qqbot

import "testing"

func TestMessageImageAttachments_NormalizesAndDeduplicatesURLs(t *testing.T) {
	quoteType := msgTypeQuote
	got := messageImageAttachments(
		[]attachment{{ContentType: "image/png", URL: " https://example.com/image.png "}},
		&messageReference{Message: &quotedMessage{Attachments: []attachment{
			{ContentType: "image/png", URL: "example.com/image.png"},
			{ContentType: "image/png", URL: ""},
		}}},
		&quoteType,
		[]msgElement{{Attachments: []attachment{{ContentType: "image/png", URL: "https://example.com/image.png"}}}},
	)
	if len(got) != 1 || got[0].URL != "https://example.com/image.png" {
		t.Fatalf("images = %#v, want one normalized image URL", got)
	}
}

func TestMessageImageAttachments_DoesNotExpandQuotedFiles(t *testing.T) {
	quoteType := msgTypeQuote
	files := []attachment{{ContentType: "application/pdf", URL: "https://example.com/file.pdf"}}
	got := messageImageAttachments(nil,
		&messageReference{Message: &quotedMessage{Attachments: files}},
		&quoteType, []msgElement{{Attachments: files}},
	)
	if len(got) != 0 {
		t.Fatalf("quoted non-image attachments = %#v, want none", got)
	}
}
