package feishu

import (
	"html"
	"strings"
)

// renderApprovalDiff turns Codex's file-change preview into compact file
// sections. The source stays in the card's text fallback for other platforms.
func renderApprovalDiff(fenced string) string {
	preview := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(fenced, "```diff\n"), "\n```"))
	var sections []string
	for _, section := range strings.Split(preview, "\n\n") {
		lines := strings.Split(strings.TrimSpace(section), "\n")
		kind, path, ok := strings.Cut(lines[0], " ")
		if !ok || path == "" || !isFileChangeKind(kind) {
			if len(sections) == 0 {
				return fenced
			}
			sections = append(sections, diffInlineCode(section))
			continue
		}
		var out []string
		out = append(out, "📄 "+diffInlineCode(path)+"  <text_tag color='blue'>"+kind+"</text_tag>")
		for _, line := range lines[1:] {
			switch {
			case strings.HasPrefix(line, "@@"):
				out = append(out, "<text_tag color='blue'>@@</text_tag> "+diffInlineCode(strings.TrimPrefix(line, "@@")))
			case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
				out = append(out, diffInlineCode(line))
			case strings.HasPrefix(line, "+"):
				out = append(out, "<text_tag color='green'>+</text_tag> "+diffInlineCode(line[1:]))
			case strings.HasPrefix(line, "-"):
				out = append(out, "<text_tag color='red'>−</text_tag> "+diffInlineCode(line[1:]))
			default:
				out = append(out, diffInlineCode(line))
			}
		}
		sections = append(sections, strings.Join(out, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func isFileChangeKind(kind string) bool {
	switch kind {
	case "add", "delete", "update", "move", "rename", "copy":
		return true
	}
	return false
}

func diffInlineCode(text string) string {
	for n := 1; n <= 5; n++ {
		fence := strings.Repeat("`", n)
		if !strings.Contains(text, fence) {
			return fence + text + fence
		}
	}
	return html.EscapeString(text)
}
