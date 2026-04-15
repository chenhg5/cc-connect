package discord

import "strings"

// wrapTablesInCodeBlocks detects markdown tables (contiguous lines starting and
// ending with "|") that are not already inside a code block, and wraps them
// with ``` so Discord renders them in monospace with proper alignment.
func wrapTablesInCodeBlocks(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	inCodeBlock := false
	inTable := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if inTable {
				result = append(result, "```")
				inTable = false
			}
			inCodeBlock = !inCodeBlock
			result = append(result, line)
			continue
		}

		if inCodeBlock {
			result = append(result, line)
			continue
		}

		isTableLine := len(trimmed) >= 3 &&
			strings.HasPrefix(trimmed, "|") &&
			strings.HasSuffix(trimmed, "|")

		if isTableLine && !inTable {
			result = append(result, "```")
			inTable = true
		} else if !isTableLine && inTable {
			result = append(result, "```")
			inTable = false
		}

		result = append(result, line)
	}

	if inTable {
		result = append(result, "```")
	}

	return strings.Join(result, "\n")
}
