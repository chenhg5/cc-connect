package core

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type ReferenceRenderCfg struct {
	NormalizeAgents []string
	RenderPlatforms []string
	DisplayPath     string
	MarkerStyle     string
	EnclosureStyle  string
}

type placeholderReplacement struct {
	placeholder string
	ref         *localReference
	keepText    string
}

var (
	reFenceBlock      = regexp.MustCompile("(?s)```.*?```")
	reInlineCodeSpan  = regexp.MustCompile("`([^`\n]+)`")
	reBareURL         = regexp.MustCompile(`https?://[^\s<>()]+`)
	reAbsOrFileRef    = regexp.MustCompile(`file:///[^\s` + "`" + `<>\[\](),，、;；。！？!?]+|/[^\s` + "`" + `<>\[\](),，、;；。！？!?]+`)
	reRelativeRef     = regexp.MustCompile(`(?:\.\.?/|[A-Za-z0-9_.-]+/)[^\s` + "`" + `<>\[\](),，、;；。！？!?]+`)
	reBasenameFileRef = regexp.MustCompile(`\b[A-Za-z0-9_.-]+\.[A-Za-z0-9_.-]+(?:#L\d+(?:C\d+)?|:\d+(?::\d+)?|:\d+-\d+)?\b`)
)

func DefaultReferenceRenderCfg() ReferenceRenderCfg {
	return ReferenceRenderCfg{
		DisplayPath:    "dirname_basename",
		MarkerStyle:    "emoji",
		EnclosureStyle: "code",
	}
}

func normalizeReferenceRenderCfg(cfg ReferenceRenderCfg) ReferenceRenderCfg {
	n := DefaultReferenceRenderCfg()
	if strings.TrimSpace(cfg.DisplayPath) != "" {
		n.DisplayPath = strings.ToLower(strings.TrimSpace(cfg.DisplayPath))
	}
	if strings.TrimSpace(cfg.MarkerStyle) != "" {
		n.MarkerStyle = strings.ToLower(strings.TrimSpace(cfg.MarkerStyle))
	}
	if strings.TrimSpace(cfg.EnclosureStyle) != "" {
		n.EnclosureStyle = strings.ToLower(strings.TrimSpace(cfg.EnclosureStyle))
	}
	n.NormalizeAgents = normalizeReferenceScope(cfg.NormalizeAgents, supportedReferenceNormalizeAgents)
	n.RenderPlatforms = normalizeReferenceScope(cfg.RenderPlatforms, supportedReferenceRenderPlatforms)
	return n
}

var supportedReferenceNormalizeAgents = []string{"codex", "claudecode"}
var supportedReferenceRenderPlatforms = []string{"feishu", "weixin"}

func normalizeReferenceScope(values []string, supported []string) []string {
	if len(values) == 0 {
		return nil
	}
	supportedSet := make(map[string]struct{}, len(supported))
	for _, v := range supported {
		supportedSet[v] = struct{}{}
	}
	hasAll := false
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		key := strings.ToLower(strings.TrimSpace(v))
		if key == "" {
			continue
		}
		if key == "all" {
			hasAll = true
			continue
		}
		if _, ok := supportedSet[key]; !ok {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if hasAll {
		return append([]string(nil), supported...)
	}
	return out
}

func (cfg ReferenceRenderCfg) renderEnabled(agentName, platformName string) bool {
	if len(cfg.NormalizeAgents) == 0 || len(cfg.RenderPlatforms) == 0 {
		return false
	}
	agentName = strings.ToLower(strings.TrimSpace(agentName))
	platformName = strings.ToLower(strings.TrimSpace(platformName))
	if !containsFolded(cfg.NormalizeAgents, agentName) {
		return false
	}
	return containsFolded(cfg.RenderPlatforms, platformName)
}

func containsFolded(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

func TransformLocalReferences(text string, cfg ReferenceRenderCfg, agentName, platformName, workspaceDir string) string {
	cfg = normalizeReferenceRenderCfg(cfg)
	if !cfg.renderEnabled(agentName, platformName) || strings.TrimSpace(text) == "" {
		return text
	}
	parts := splitWithMatches(text, reFenceBlock)
	var out strings.Builder
	for _, part := range parts {
		if part.matched {
			out.WriteString(part.text)
			continue
		}
		out.WriteString(transformTextOutsideFence(part.text, cfg, workspaceDir))
	}
	return out.String()
}

func transformTextOutsideFence(text string, cfg ReferenceRenderCfg, workspaceDir string) string {
	parts := splitWithMatches(text, reInlineCodeSpan)
	replacements := make([]placeholderReplacement, 0)
	var out strings.Builder
	for _, part := range parts {
		if !part.matched {
			transformed, reps := transformNonCodeText(part.text, cfg, workspaceDir)
			if len(replacements) > 0 && len(reps) > 0 {
				offset := len(replacements)
				for i := range reps {
					oldPlaceholder := reps[i].placeholder
					newPlaceholder := makeReferencePlaceholder(offset + i)
					transformed = strings.ReplaceAll(transformed, oldPlaceholder, newPlaceholder)
					reps[i].placeholder = newPlaceholder
				}
			}
			out.WriteString(transformed)
			replacements = append(replacements, reps...)
			continue
		}
		match := reInlineCodeSpan.FindStringSubmatch(part.text)
		if len(match) < 2 {
			out.WriteString(part.text)
			continue
		}
		ref, ok := parseLocalReference(match[1], workspaceDir)
		if !ok {
			out.WriteString(part.text)
			continue
		}
		placeholder := makeReferencePlaceholder(len(replacements))
		replacements = append(replacements, placeholderReplacement{placeholder: placeholder, ref: ref})
		out.WriteString(placeholder)
	}
	return replaceReferencePlaceholders(out.String(), replacements, cfg)
}

func transformNonCodeText(text string, cfg ReferenceRenderCfg, workspaceDir string) (string, []placeholderReplacement) {
	replacements := make([]placeholderReplacement, 0)
	text = replaceProtectedWebMarkdownLinks(text, &replacements)
	text = replaceProtectedLinks(text, reBareURL, &replacements)
	text = replaceMarkdownLinks(text, &replacements, workspaceDir)
	text = replaceLocalReferenceCandidates(text, reAbsOrFileRef, &replacements, workspaceDir)
	text = replaceLocalReferenceCandidates(text, reRelativeRef, &replacements, workspaceDir)
	text = replaceLocalReferenceCandidates(text, reBasenameFileRef, &replacements, workspaceDir)
	return text, replacements
}

func replaceProtectedLinks(text string, re *regexp.Regexp, replacements *[]placeholderReplacement) string {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		out.WriteString(text[last:m[0]])
		token := text[m[0]:m[1]]
		placeholder := makeReferencePlaceholder(len(*replacements))
		*replacements = append(*replacements, placeholderReplacement{placeholder: placeholder, keepText: token})
		out.WriteString(placeholder)
		last = m[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

func replaceProtectedWebMarkdownLinks(text string, replacements *[]placeholderReplacement) string {
	matches := reMarkdownLink.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		target := text[m[4]:m[5]]
		if !isWebURL(target) {
			continue
		}
		out.WriteString(text[last:m[0]])
		token := text[m[0]:m[1]]
		placeholder := makeReferencePlaceholder(len(*replacements))
		*replacements = append(*replacements, placeholderReplacement{placeholder: placeholder, keepText: token})
		out.WriteString(placeholder)
		last = m[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

func replaceMarkdownLinks(text string, replacements *[]placeholderReplacement, workspaceDir string) string {
	matches := reMarkdownLink.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		out.WriteString(text[last:m[0]])
		target := text[m[4]:m[5]]
		suffix := ""
		if m[6] >= 0 {
			suffix = text[m[6]:m[7]]
		}
		ref, ok := parseLocalReference(target+suffix, workspaceDir)
		if !ok {
			out.WriteString(text[m[0]:m[1]])
			last = m[1]
			continue
		}
		placeholder := makeReferencePlaceholder(len(*replacements))
		*replacements = append(*replacements, placeholderReplacement{placeholder: placeholder, ref: ref})
		out.WriteString(placeholder)
		last = m[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

func replaceLocalReferenceCandidates(text string, re *regexp.Regexp, replacements *[]placeholderReplacement, workspaceDir string) string {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		out.WriteString(text[last:m[0]])
		token := text[m[0]:m[1]]
		if re == reAbsOrFileRef && !isValidAbsoluteReferenceBoundary(text, m[0]) {
			out.WriteString(token)
			last = m[1]
			continue
		}
		if re == reRelativeRef && !isValidRelativeReferenceBoundary(text, m[0]) {
			out.WriteString(token)
			last = m[1]
			continue
		}
		ref, ok := parseLocalReference(token, workspaceDir)
		if !ok {
			out.WriteString(token)
			last = m[1]
			continue
		}
		placeholder := makeReferencePlaceholder(len(*replacements))
		*replacements = append(*replacements, placeholderReplacement{placeholder: placeholder, ref: ref})
		out.WriteString(placeholder)
		last = m[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

func isValidAbsoluteReferenceBoundary(text string, start int) bool {
	if start <= 0 {
		return true
	}
	prev, _ := utf8.DecodeLastRuneInString(text[:start])
	switch {
	case prev == ' ', prev == '\n', prev == '\t', prev == '\r':
		return true
	case strings.ContainsRune("([<{\"'`、，,;；。！？!?:：", prev):
		return true
	default:
		return false
	}
}

func isValidRelativeReferenceBoundary(text string, start int) bool {
	if start <= 0 {
		return true
	}
	prev, _ := utf8.DecodeLastRuneInString(text[:start])
	switch {
	case prev == ' ', prev == '\n', prev == '\t', prev == '\r':
		return true
	case strings.ContainsRune("([<{\"'`、，,;；。！？!?:：", prev):
		return true
	default:
		return false
	}
}

func replaceReferencePlaceholders(text string, replacements []placeholderReplacement, cfg ReferenceRenderCfg) string {
	if len(replacements) == 0 {
		return text
	}
	basenameCounts := make(map[string]int)
	for _, rep := range replacements {
		if rep.ref == nil {
			continue
		}
		base := refBaseName(rep.ref)
		if base != "" {
			basenameCounts[base]++
		}
	}
	sort.SliceStable(replacements, func(i, j int) bool {
		return len(replacements[i].placeholder) > len(replacements[j].placeholder)
	})
	for _, rep := range replacements {
		replacement := rep.keepText
		if rep.ref != nil {
			replacement = renderLocalReference(rep.ref, cfg, basenameCounts)
		}
		text = strings.ReplaceAll(text, rep.placeholder, replacement)
	}
	return text
}

type splitPart struct {
	text    string
	matched bool
}

func splitWithMatches(text string, re *regexp.Regexp) []splitPart {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return []splitPart{{text: text}}
	}
	parts := make([]splitPart, 0, len(matches)*2+1)
	last := 0
	for _, m := range matches {
		if m[0] > last {
			parts = append(parts, splitPart{text: text[last:m[0]]})
		}
		parts = append(parts, splitPart{text: text[m[0]:m[1]], matched: true})
		last = m[1]
	}
	if last < len(text) {
		parts = append(parts, splitPart{text: text[last:]})
	}
	return parts
}

func makeReferencePlaceholder(idx int) string {
	return fmt.Sprintf("\x00REF_%03d\x00", idx)
}

func refBaseName(ref *localReference) string {
	if ref == nil {
		return ""
	}
	p := referenceDisplaySource(ref, "basename")
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

func renderLocalReference(ref *localReference, cfg ReferenceRenderCfg, basenameCounts map[string]int) string {
	body := referenceDisplaySource(ref, cfg.DisplayPath)
	if cfg.DisplayPath == "smart" {
		base := refBaseName(ref)
		if basenameCounts[base] <= 1 {
			body = referenceDisplaySource(ref, "basename")
		} else {
			body = referenceDisplaySource(ref, "dirname_basename")
			if body == base {
				body = referenceDisplaySource(ref, "relative")
			}
		}
	}
	body += renderReferenceLocation(ref)
	body = applyReferenceEnclosure(cfg.EnclosureStyle, body)
	return applyReferenceMarker(cfg.MarkerStyle, ref.kind, body)
}

func referenceDisplaySource(ref *localReference, mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "absolute":
		if ref.pathAbs != "" {
			return appendDirSuffix(ref.pathAbs, ref.kind)
		}
		return appendDirSuffix(cleanDisplayPath(ref.pathOriginal), ref.kind)
	case "relative":
		if ref.pathRel == "." {
			if ref.kind == referenceKindDir {
				return "./"
			}
			return "."
		}
		if rel := sanitizeRelativeDisplay(ref.pathRel); rel != "" {
			return appendDirSuffix(rel, ref.kind)
		}
		if ref.isRelative {
			return appendDirSuffix(cleanDisplayPath(ref.pathOriginal), ref.kind)
		}
		if ref.pathAbs != "" {
			return appendDirSuffix(ref.pathAbs, ref.kind)
		}
		return appendDirSuffix(cleanDisplayPath(ref.pathOriginal), ref.kind)
	case "basename":
		return appendDirSuffix(pathTail(ref, 1), ref.kind)
	case "dirname_basename":
		return appendDirSuffix(pathTail(ref, 2), ref.kind)
	case "smart":
		return appendDirSuffix(pathTail(ref, 1), ref.kind)
	default:
		return appendDirSuffix(pathTail(ref, 2), ref.kind)
	}
}

func sanitizeRelativeDisplay(rel string) string {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

func pathTail(ref *localReference, segs int) string {
	source := sanitizeRelativeDisplay(ref.pathRel)
	if source == "" {
		if ref.isRelative {
			source = cleanDisplayPath(ref.pathOriginal)
		} else if ref.pathAbs != "" {
			source = filepath.ToSlash(ref.pathAbs)
		} else {
			source = cleanDisplayPath(ref.pathOriginal)
		}
	}
	source = strings.TrimSuffix(source, "/")
	parts := strings.Split(filepath.ToSlash(source), "/")
	if len(parts) == 0 {
		return source
	}
	if segs <= 0 || len(parts) <= segs {
		return source
	}
	return strings.Join(parts[len(parts)-segs:], "/")
}

func cleanDisplayPath(path string) string {
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "./")
	return strings.TrimSpace(path)
}

func appendDirSuffix(path string, kind referenceKind) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return path
	}
	if kind == referenceKindDir && !strings.HasSuffix(path, "/") {
		return path + "/"
	}
	return strings.TrimSuffix(path, "/")
}

func renderReferenceLocation(ref *localReference) string {
	switch ref.locationFormat {
	case referenceLocationColonLine:
		return fmt.Sprintf(":%d", ref.lineStart)
	case referenceLocationColonLineCol:
		return fmt.Sprintf(":%d:%d", ref.lineStart, ref.column)
	case referenceLocationColonRange:
		return fmt.Sprintf(":%d-%d", ref.lineStart, ref.lineEnd)
	case referenceLocationHashLine:
		return fmt.Sprintf("#L%d", ref.lineStart)
	case referenceLocationHashLineCol:
		return fmt.Sprintf("#L%dC%d", ref.lineStart, ref.column)
	default:
		return ""
	}
}

func applyReferenceMarker(style string, kind referenceKind, body string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "ascii":
		if kind == referenceKindDir {
			return "[DIR] " + body
		}
		if kind == referenceKindFile {
			return "[FILE] " + body
		}
		return body
	case "emoji":
		if kind == referenceKindDir {
			return "📁 " + body
		}
		if kind == referenceKindFile {
			return "📄 " + body
		}
		return body
	default:
		return body
	}
}

func applyReferenceEnclosure(style, body string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "bracket":
		return "[" + body + "]"
	case "angle":
		return "<" + body + ">"
	case "fullwidth":
		return "【" + body + "】"
	case "code":
		return "`" + body + "`"
	default:
		return body
	}
}
