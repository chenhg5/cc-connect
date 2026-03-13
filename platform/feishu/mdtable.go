package feishu

import (
	"fmt"
	"strings"
)

// mdTableToCardElements splits Markdown content into card elements,
// converting Markdown tables into native schema 2.0 table components
// with page_size for reliable pagination.
//
// Non-table content is kept as markdown elements. Code blocks are
// preserved as-is (tables inside code blocks are not converted).
func mdTableToCardElements(content string) []any {
	lines := strings.Split(content, "\n")
	var elements []any
	var textBuf strings.Builder
	inCodeBlock := false

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Track code blocks — don't parse tables inside them.
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			textBuf.WriteString(line)
			textBuf.WriteByte('\n')
			i++
			continue
		}
		if inCodeBlock {
			textBuf.WriteString(line)
			textBuf.WriteByte('\n')
			i++
			continue
		}

		// Detect table start: header row followed by separator row.
		if isTableRow(trimmed) && i+1 < len(lines) && isTableSeparator(strings.TrimSpace(lines[i+1])) {
			// Flush accumulated text.
			if text := strings.TrimSpace(textBuf.String()); text != "" {
				elements = append(elements, map[string]any{"tag": "markdown", "content": text})
				textBuf.Reset()
			}

			headerLine := trimmed
			i += 2 // skip header + separator

			// Collect data rows.
			var dataRows []string
			for i < len(lines) {
				rowTrimmed := strings.TrimSpace(lines[i])
				if !isTableRow(rowTrimmed) {
					break
				}
				dataRows = append(dataRows, rowTrimmed)
				i++
			}

			if tableEl := buildNativeTable(headerLine, dataRows); tableEl != nil {
				elements = append(elements, tableEl)
			}
			continue
		}

		textBuf.WriteString(line)
		textBuf.WriteByte('\n')
		i++
	}

	// Flush remaining text.
	if text := strings.TrimSpace(textBuf.String()); text != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": text})
	}

	return elements
}

// isTableRow returns true if the line looks like a pipe-delimited table row.
func isTableRow(line string) bool {
	return len(line) > 1 && line[0] == '|' && line[len(line)-1] == '|'
}

// isTableSeparator returns true if the line is a Markdown table separator
// (e.g. "|---|---|" or "| :---: | ---: |").
func isTableSeparator(line string) bool {
	if !isTableRow(line) {
		return false
	}
	inner := line[1 : len(line)-1]
	hasDash := false
	for _, cell := range strings.Split(inner, "|") {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			continue
		}
		for _, ch := range cell {
			if ch != '-' && ch != ':' && ch != ' ' {
				return false
			}
		}
		// Must contain at least one dash.
		if strings.Contains(cell, "-") {
			hasDash = true
		} else {
			return false
		}
	}
	return hasDash
}

// mdLinkRe is declared in feishu.go — reused here for link detection.

const defaultTablePageSize = 5

// buildNativeTable converts a parsed Markdown table into a Feishu schema 2.0
// native table component with page_size.
func buildNativeTable(headerLine string, dataRows []string) map[string]any {
	headers := parsePipeCells(headerLine)
	if len(headers) == 0 {
		return nil
	}

	// Use indexed column names to avoid key collisions from header text.
	colNames := make([]string, len(headers))
	for i := range headers {
		colNames[i] = fmt.Sprintf("c%d", i)
	}

	// Parse data rows.
	rows := make([]map[string]any, 0, len(dataRows))
	for _, rowLine := range dataRows {
		cells := parsePipeCells(rowLine)
		row := make(map[string]any, len(headers))
		for j := range headers {
			if j < len(cells) {
				row[colNames[j]] = cells[j]
			} else {
				row[colNames[j]] = ""
			}
		}
		rows = append(rows, row)
	}

	// Build columns; auto-detect lark_md for columns containing Markdown links.
	columns := make([]map[string]any, 0, len(headers))
	for i, header := range headers {
		dataType := "text"
		for _, row := range rows {
			if val, ok := row[colNames[i]].(string); ok && mdLinkRe.MatchString(val) {
				dataType = "lark_md"
				break
			}
		}
		columns = append(columns, map[string]any{
			"name":         colNames[i],
			"display_name": header,
			"data_type":    dataType,
			"width":        "auto",
		})
	}

	return map[string]any{
		"tag":       "table",
		"columns":   columns,
		"rows":      rows,
		"page_size": defaultTablePageSize,
	}
}

// parsePipeCells splits a pipe-delimited row into trimmed cell values.
// Leading and trailing pipes are removed first.
func parsePipeCells(line string) []string {
	inner := line
	if len(inner) > 0 && inner[0] == '|' {
		inner = inner[1:]
	}
	if len(inner) > 0 && inner[len(inner)-1] == '|' {
		inner = inner[:len(inner)-1]
	}
	parts := strings.Split(inner, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}
