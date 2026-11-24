package wpsxiezuo

import (
	"encoding/json"
	"testing"
)

// WPS clients send style-bearing rich text elements with a `style_content`
// field. Before the fix this parsed to an empty string and the message was
// silently dropped in handleRichTextMessage.
func TestParseWPSRichTextStyleContent(t *testing.T) {
	raw := json.RawMessage(`{
		"rich_text": {
			"elements": [{
				"type": "nl",
				"elements": [{
					"type": "text",
					"style_content": {
						"style": {"bold": true, "color": "", "italic": false},
						"text": "加粗"
					}
				}, {
					"type": "text",
					"style_content": {
						"style": {"bold": false, "color": "", "italic": false},
						"text": "普通"
					}
				}]
			}]
		}
	}`)

	text, _, _, err := parseWPSRichText(raw)
	if err != nil {
		t.Fatalf("parseWPSRichText() error = %v", err)
	}
	if want := "加粗普通"; text != want {
		t.Fatalf("parseWPSRichText() = %q, want %q", text, want)
	}
}

// The legacy style_text_content field must keep working.
func TestParseWPSRichTextLegacyStyleTextContent(t *testing.T) {
	raw := json.RawMessage(`{
		"rich_text": {
			"elements": [{
				"type": "nl",
				"elements": [{
					"type": "text",
					"style_text_content": {"text": "旧字段"}
				}]
			}]
		}
	}`)

	text, _, _, err := parseWPSRichText(raw)
	if err != nil {
		t.Fatalf("parseWPSRichText() error = %v", err)
	}
	if want := "旧字段"; text != want {
		t.Fatalf("parseWPSRichText() = %q, want %q", text, want)
	}
}
