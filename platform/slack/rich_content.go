package slack

// Slack delivers native tables, forwarded/shared messages and other structured
// content in "blocks" / "attachments", not in "text". Forwarding only "text"
// means the agent never sees a table pasted into Slack or a message shared into
// the conversation. This file renders that structured content as plain text /
// Markdown so it can be appended to the message content.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

const richContentLogLimit = 4000

// extractRichContent parses the raw inner event JSON and returns a text
// rendering of tables / non-text blocks and attachments. The message's own
// rich_text blocks are skipped because they duplicate the "text" field.
func extractRichContent(raw *json.RawMessage, msgID string) string {
	if raw == nil || len(*raw) == 0 {
		return ""
	}
	var ev struct {
		Blocks      []map[string]any `json:"blocks"`
		Attachments []map[string]any `json:"attachments"`
	}
	if err := json.Unmarshal(*raw, &ev); err != nil {
		slog.Debug("slack: parse rich content", "error", err)
		return ""
	}

	hasExtraBlocks := false
	for _, b := range ev.Blocks {
		if str(b["type"]) != "rich_text" {
			hasExtraBlocks = true
			break
		}
	}
	if !hasExtraBlocks && len(ev.Attachments) == 0 {
		return ""
	}

	// Only pay for re-marshalling the payload when debug logging is on.
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		if dump, err := json.Marshal(map[string]any{"blocks": ev.Blocks, "attachments": ev.Attachments}); err == nil {
			s := string(dump)
			if len(s) > richContentLogLimit {
				s = s[:richContentLogLimit] + "...(truncated)"
			}
			slog.Debug("slack: rich content received", "msg_id", msgID,
				"blocks", len(ev.Blocks), "attachments", len(ev.Attachments), "raw", s)
		}
	}

	var parts []string
	if body := strings.TrimSpace(renderBlocks(ev.Blocks, true)); body != "" {
		parts = append(parts, body)
	}
	for i, att := range ev.Attachments {
		if body := strings.TrimSpace(renderAttachment(att)); body != "" {
			label := fmt.Sprintf("[Attachment %d", i+1)
			if author := str(att["author_name"]); author != "" {
				label += " from " + author
			}
			parts = append(parts, label+"]\n"+body)
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderAttachment(att map[string]any) string {
	var parts []string
	for _, k := range []string{"pretext", "title"} {
		if v := str(att[k]); v != "" {
			parts = append(parts, v)
		}
	}
	rendered := false
	if mbs, ok := att["message_blocks"].([]any); ok {
		for _, mb := range mbs {
			msg, _ := asMap(mb)["message"].(map[string]any)
			if body := strings.TrimSpace(renderBlocks(toMaps(msg["blocks"]), false)); body != "" {
				parts = append(parts, body)
				rendered = true
			}
		}
	}
	if !rendered {
		if body := strings.TrimSpace(renderBlocks(toMaps(att["blocks"]), false)); body != "" {
			parts = append(parts, body)
			rendered = true
		}
	}
	if !rendered {
		if v := str(att["text"]); v != "" {
			parts = append(parts, v)
			rendered = true
		}
	}
	if fields, ok := att["fields"].([]any); ok {
		for _, f := range fields {
			fm := asMap(f)
			if t, v := str(fm["title"]), str(fm["value"]); t != "" || v != "" {
				parts = append(parts, strings.TrimSpace(t+": "+v))
				rendered = true
			}
		}
	}
	if !rendered {
		if v := str(att["fallback"]); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "\n")
}

func renderBlocks(blocks []map[string]any, skipRichText bool) string {
	var out []string
	for _, b := range blocks {
		var s string
		switch str(b["type"]) {
		case "rich_text":
			if skipRichText {
				continue
			}
			s = richTextPlain(b["elements"])
		case "table":
			s = renderTable(b["rows"])
		case "section":
			var ps []string
			if t := str(asMap(b["text"])["text"]); t != "" {
				ps = append(ps, t)
			}
			if fields, ok := b["fields"].([]any); ok {
				for _, f := range fields {
					if t := str(asMap(f)["text"]); t != "" {
						ps = append(ps, t)
					}
				}
			}
			s = strings.Join(ps, "\n")
		case "header":
			s = str(asMap(b["text"])["text"])
		case "markdown":
			s = str(b["text"])
		case "context":
			var ps []string
			if els, ok := b["elements"].([]any); ok {
				for _, e := range els {
					if t := str(asMap(e)["text"]); t != "" {
						ps = append(ps, t)
					}
				}
			}
			s = strings.Join(ps, " ")
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

func renderTable(rowsAny any) string {
	rows, ok := rowsAny.([]any)
	if !ok || len(rows) == 0 {
		return ""
	}
	var lines []string
	for i, r := range rows {
		cells, _ := r.([]any)
		vals := make([]string, len(cells))
		for j, c := range cells {
			v := cellText(asMap(c))
			v = strings.ReplaceAll(v, "\n", " ")
			vals[j] = strings.ReplaceAll(v, "|", `\|`)
		}
		lines = append(lines, "| "+strings.Join(vals, " | ")+" |")
		if i == 0 {
			seps := make([]string, len(cells))
			for j := range seps {
				seps[j] = "---"
			}
			lines = append(lines, "| "+strings.Join(seps, " | ")+" |")
		}
	}
	return strings.Join(lines, "\n")
}

func cellText(c map[string]any) string {
	switch str(c["type"]) {
	case "rich_text":
		return strings.TrimSpace(richTextPlain(c["elements"]))
	default:
		return strings.TrimSpace(str(c["text"]))
	}
}

// richTextPlain flattens Slack rich_text elements into plain text.
func richTextPlain(elementsAny any) string {
	els, ok := elementsAny.([]any)
	if !ok {
		return ""
	}
	var out []string
	for _, e := range els {
		em := asMap(e)
		switch str(em["type"]) {
		case "rich_text_section", "rich_text_quote":
			out = append(out, inlinePlain(em["elements"]))
		case "rich_text_preformatted":
			out = append(out, "```\n"+inlinePlain(em["elements"])+"\n```")
		case "rich_text_list":
			if items, ok := em["elements"].([]any); ok {
				for _, it := range items {
					out = append(out, "- "+inlinePlain(asMap(it)["elements"]))
				}
			}
		default:
			out = append(out, inlinePlain([]any{e}))
		}
	}
	return strings.Join(out, "\n")
}

func inlinePlain(elementsAny any) string {
	els, ok := elementsAny.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, e := range els {
		em := asMap(e)
		switch str(em["type"]) {
		case "text":
			b.WriteString(str(em["text"]))
		case "link":
			if t := str(em["text"]); t != "" {
				b.WriteString(t + " (" + str(em["url"]) + ")")
			} else {
				b.WriteString(str(em["url"]))
			}
		case "user":
			b.WriteString("<@" + str(em["user_id"]) + ">")
		case "channel":
			b.WriteString("<#" + str(em["channel_id"]) + ">")
		case "emoji":
			b.WriteString(":" + str(em["name"]) + ":")
		case "broadcast":
			b.WriteString("@" + str(em["range"]))
		default:
			if t := str(em["text"]); t != "" {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func toMaps(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		if m := asMap(x); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// appendRichContent joins the plain text content with the rendered rich content.
func appendRichContent(content, extra string) string {
	if extra == "" {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return extra
	}
	return content + "\n\n" + extra
}
