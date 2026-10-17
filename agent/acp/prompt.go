package acp

import (
	"encoding/base64"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// buildPromptBlocks builds the ACP session/prompt content array.
// When nativeImages is false, images are expected to already be referenced
// in prompt text (path fallback) and only a text block is returned.
func buildPromptBlocks(prompt string, images []core.ImageAttachment, nativeImages bool) []any {
	if !nativeImages || len(images) == 0 {
		return []any{map[string]any{"type": "text", "text": prompt}}
	}
	var blocks []any
	if prompt != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": prompt})
	}
	for _, img := range images {
		if len(img.Data) == 0 {
			continue
		}
		mime := strings.TrimSpace(img.MimeType)
		if mime == "" {
			mime = "image/png"
		}
		blocks = append(blocks, map[string]any{
			"type":     "image",
			"mimeType": mime,
			"data":     base64.StdEncoding.EncodeToString(img.Data),
		})
	}
	if len(blocks) == 0 {
		return []any{map[string]any{"type": "text", "text": prompt}}
	}
	return blocks
}
