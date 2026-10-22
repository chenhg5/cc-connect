package discord

import "strings"

// wrapTablesInCodeBlocks detects markdown tables (contiguous pipe-delimited
// lines that include a separator row like |---|---|) outside code blocks, and
// wraps them with ``` so Discord renders them in monospace.
func wrapTablesInCodeBlocks(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	inCodeBlock := false

	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])

		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			result = append(result, lines[i])
			i++
			continue
		}

		if inCodeBlock {
			result = append(result, lines[i])
			i++
			continue
		}

		if isPipeRow(trimmed) {
			tableStart := i
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if !isPipeRow(t) {
					break
				}
				i++
			}
			tableLines := lines[tableStart:i]
			if hasTableSeparator(tableLines) {
				result = append(result, "```")
				result = append(result, tableLines...)
				result = append(result, "```")
			} else {
				result = append(result, tableLines...)
			}
			continue
		}

		result = append(result, lines[i])
		i++
	}

	return strings.Join(result, "\n")
}

func isPipeRow(trimmed string) bool {
	return len(trimmed) >= 3 &&
		strings.HasPrefix(trimmed, "|") &&
		strings.HasSuffix(trimmed, "|")
}

// hasTableSeparator checks if any line in the block looks like | --- | --- |.
func hasTableSeparator(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		inner := strings.Trim(trimmed, "|")
		cells := strings.Split(inner, "|")
		allDash := true
		for _, cell := range cells {
			c := strings.TrimSpace(cell)
			c = strings.TrimLeft(c, ":")
			c = strings.TrimRight(c, ":")
			if len(c) == 0 || strings.Trim(c, "-") != "" {
				allDash = false
				break
			}
		}
		if allDash && len(cells) >= 1 {
			return true
		}
	}
	return false
}
