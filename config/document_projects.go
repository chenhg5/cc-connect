package config

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// projectDocumentSpan includes the project's nested tables, but stops before
// the next project or an unrelated top-level section.
func projectDocumentSpan(tables []documentTable, total, index int) (int, int, error) {
	start := -1
	for _, table := range tables {
		if table.name == "projects" && table.project == index {
			start = table.start
			break
		}
	}
	if start < 0 {
		return 0, 0, fmt.Errorf("cannot locate project %d", index)
	}
	for _, table := range tables {
		if table.start > start && table.project != index {
			return start, table.start, nil
		}
	}
	return start, total, nil
}

func removeProjectBlock(raw string, index int) (string, error) {
	_, tables, err := scanConfigDocument(raw)
	if err != nil {
		return "", err
	}
	start, end, err := projectDocumentSpan(tables, len(raw), index)
	if err != nil {
		return "", err
	}
	return raw[:start] + raw[end:], nil
}

func serializeProjectBlock(project ProjectConfig) (string, error) {
	var out strings.Builder
	if err := toml.NewEncoder(&out).Encode(struct {
		Projects []ProjectConfig `toml:"projects"`
	}{Projects: []ProjectConfig{project}}); err != nil {
		return "", err
	}
	return out.String(), nil
}

func serializePlatformBlock(platform PlatformConfig) (string, error) {
	var out strings.Builder
	if err := toml.NewEncoder(&out).Encode(struct {
		Platforms []PlatformConfig `toml:"platforms"`
	}{Platforms: []PlatformConfig{platform}}); err != nil {
		return "", err
	}
	var block strings.Builder
	for line := range strings.SplitSeq(out.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[[platforms]]" || strings.HasPrefix(trimmed, "[platforms.") {
			line = strings.Replace(line, "[platforms", "[projects.platforms", 1)
		}
		if block.Len() > 0 {
			block.WriteByte('\n')
		}
		block.WriteString(line)
	}
	return block.String(), nil
}

func projectBlockNewlines(raw, block string) string {
	if documentNewline(raw) == "\r\n" {
		return strings.ReplaceAll(block, "\n", "\r\n")
	}
	return block
}

func insertPlatformBlock(raw string, project int, block string) (string, error) {
	_, tables, err := scanConfigDocument(raw)
	if err != nil {
		return "", err
	}
	_, end, err := projectDocumentSpan(tables, len(raw), project)
	if err != nil {
		return "", err
	}
	return insertDocument(raw, end, documentNewline(raw)+projectBlockNewlines(raw, block)), nil
}
