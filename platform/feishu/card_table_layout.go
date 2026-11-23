package feishu

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Card 2.0 renders Markdown tables with equal-width columns, and a native
// table keeps the text wrapping of its first layout when percentage columns
// change width. Reply cards therefore draw each Markdown table one of two ways:
//
//   - Weighted column_set rows when every column still gets a readable width
//     on a narrow pane. The weights follow how much text each column holds,
//     cells re-wrap when the pane is resized, and very long cells fold behind
//     a "show the rest" panel so one cell does not make its row endless.
//   - Otherwise a native table: the column with the most text takes the width
//     left over (width auto) and the others get fixed pixel widths sized by
//     content. It scrolls sideways instead of squeezing columns into strips,
//     long cells scroll inside a capped row height, and on a wide pane the
//     auto column fills the card.

const (
	// Feishu accepts integer column weights from 1 to 5.
	cardTableMaxWeight = 5
	// One very long cell should not decide its column's share.
	cardTableCellWidthCap = 400
	// Headers count only up to this width, so a wordy header over short values
	// stays narrow and wraps.
	cardTableHeaderWidthCap = 12
	// Above this many components the card keeps native Markdown tables, which
	// cost one component each; Feishu rejects cards with too many components.
	cardTableMaxComponents = 180
	// Weighted rows are used only if, on a narrow desktop pane, every column
	// fits its content up to cardTableMinColumnChars Latin characters (eight
	// CJK characters) per line plus padding.
	cardTableReferenceWidthPx = 640
	cardTableMinColumnChars   = 16
	cardTableCharWidthPx      = 7
	cardTableCellPaddingPx    = 16
	// Native table columns: Feishu accepts 80 to 600 px; long-text columns stop
	// at cardTableMaxColumnPx so a row stays readable without scrolling far.
	cardTableMinColumnPx   = 80
	cardTableMaxColumnPx   = 360
	cardTableRowMaxHeight  = "240px"
	cardTableNativePageMax = 10
	// Weighted-row cells wider than cardTableFoldWidth (about 140 CJK
	// characters) show about cardTableFoldPreview and fold the rest.
	cardTableFoldWidth   = 280
	cardTableFoldPreview = 200
	cardTableFoldSlack   = 120
	cardTableFoldMinRest = 60
)

type cardTable struct {
	header []string
	align  []string
	rows   [][]string
}

// cardTableColumn is what the layout needs to know about one column.
type cardTableColumn struct {
	mean   float64 // mean cell width, each cell capped at cardTableCellWidthCap
	widest int     // widest cell or header
}

// buildCardBodyElements returns the body elements for a reply card: Markdown
// text as markdown elements and each Markdown table as weighted rows or a
// native table. When the rows would exceed the component budget, the content
// stays one markdown element with Markdown tables.
func buildCardBodyElements(content string) []map[string]any {
	plain := []map[string]any{{"tag": "markdown", "content": content}}
	matches := findMarkdownTablesOutsideCodeBlocks(content)
	if len(matches) == 0 {
		return plain
	}
	var elements []map[string]any
	components := 0
	appendText := func(text string) {
		text = strings.Trim(text, "\n")
		if strings.TrimSpace(text) == "" {
			return
		}
		elements = append(elements, map[string]any{"tag": "markdown", "content": text})
		components++
	}
	prev := 0
	for _, m := range matches {
		table, ok := parseCardTable(m.raw)
		if !ok {
			continue
		}
		appendText(content[prev:m.start])
		columns := cardTableColumns(table)
		if weights, ok := cardTableColumnWeights(columns); ok {
			el, n := buildCardTableRows(table, weights)
			elements = append(elements, el)
			components += n
		} else {
			elements = append(elements, buildNativeCardTable(table, cardTableColumnWidths(columns)))
			components++
		}
		prev = m.end
	}
	appendText(content[prev:])
	if len(elements) == 0 || components > cardTableMaxComponents {
		return plain
	}
	return elements
}

func cardTableColumns(t cardTable) []cardTableColumn {
	columns := make([]cardTableColumn, len(t.header))
	for c := range columns {
		headerWidth := cardTextWidth(t.header[c])
		col := cardTableColumn{mean: float64(min(headerWidth, cardTableHeaderWidthCap)), widest: headerWidth}
		if len(t.rows) > 0 {
			total := 0
			for _, r := range t.rows {
				w := cardTextWidth(r[c])
				total += min(w, cardTableCellWidthCap)
				col.widest = max(col.widest, w)
			}
			col.mean = max(col.mean, float64(total)/float64(len(t.rows)))
		}
		columns[c] = col
	}
	return columns
}

// cardTableColumnWeights gives each column a weight from 1 to 5 by the square
// root of its mean width, so short columns stay narrow and long-text columns
// get most of the width without pushing the rest to nothing. Columns too
// narrow for their content on a narrow pane are then widened; it reports false
// when the columns cannot all fit.
func cardTableColumnWeights(columns []cardTableColumn) ([]int, bool) {
	maxScore := 0.0
	for _, col := range columns {
		maxScore = max(maxScore, math.Sqrt(max(col.mean, 1)))
	}
	weights := make([]int, len(columns))
	total := 0
	for c, col := range columns {
		w := int(math.Round(cardTableMaxWeight * math.Sqrt(max(col.mean, 1)) / maxScore))
		weights[c] = min(max(w, 1), cardTableMaxWeight)
		total += weights[c]
	}
	for changed := true; changed; {
		changed = false
		for c, col := range columns {
			needPx := min(max(col.widest, 1), cardTableMinColumnChars)*cardTableCharWidthPx + cardTableCellPaddingPx
			if weights[c]*cardTableReferenceWidthPx >= needPx*total {
				continue
			}
			if weights[c] == cardTableMaxWeight {
				return nil, false
			}
			weights[c]++
			total++
			changed = true
		}
	}
	return weights, true
}

// cardTableColumnWidths sizes native table columns: the column with the most
// text is auto and fills the rest of the card; short columns fit their widest
// cell, and other long-text columns grow with the square root of their mean
// width up to cardTableMaxColumnPx.
func cardTableColumnWidths(columns []cardTableColumn) []string {
	widths := make([]string, len(columns))
	widest := 0
	for c, col := range columns {
		if col.mean > columns[widest].mean {
			widest = c
		}
		px := min(col.widest, cardTableMinColumnChars)*cardTableCharWidthPx + cardTableCellPaddingPx
		px = max(px, int(math.Sqrt(max(col.mean, 1))*24))
		widths[c] = fmt.Sprintf("%dpx", min(max(px, cardTableMinColumnPx), cardTableMaxColumnPx))
	}
	widths[widest] = "auto"
	return widths
}

// buildCardTableRows draws one table as a single outer column whose rows are
// column_sets with shared weights. The outer column removes the gaps between
// rows without changing the card's own vertical spacing. It returns the
// element and the number of components it uses.
func buildCardTableRows(t cardTable, weights []int) (map[string]any, int) {
	components := 2 // outer set and column
	row := func(cells []string, background string, header bool) map[string]any {
		columns := make([]map[string]any, len(cells))
		for i, cell := range cells {
			text := cell
			if header && text != "" && !strings.Contains(text, "**") {
				text = "**" + text + "**"
			}
			if strings.TrimSpace(text) == "" {
				text = " "
			}
			var elements []map[string]any
			if preview, rest, ok := foldCardCell(text); ok && !header {
				elements = []map[string]any{cardTableCellMarkdown(preview, t.align[i]), cardTableFoldPanel(rest, t.align[i])}
				components += 3
			} else {
				elements = []map[string]any{cardTableCellMarkdown(text, t.align[i])}
				components++
			}
			columns[i] = map[string]any{
				"tag":              "column",
				"width":            "weighted",
				"weight":           weights[i],
				"vertical_align":   "top",
				"vertical_spacing": "4px",
				"padding":          "8px",
				"elements":         elements,
			}
			components++
		}
		components++
		return map[string]any{
			"tag":                "column_set",
			"flex_mode":          "none",
			"horizontal_spacing": "0px",
			"background_style":   background,
			"columns":            columns,
		}
	}
	rows := []map[string]any{row(t.header, "grey-100", true)}
	for i, cells := range t.rows {
		background := "default"
		if i%2 == 0 {
			background = "grey-50"
		}
		rows = append(rows, row(cells, background, false))
	}
	return map[string]any{
		"tag":                "column_set",
		"flex_mode":          "none",
		"horizontal_spacing": "0px",
		"columns": []map[string]any{{
			"tag":              "column",
			"width":            "weighted",
			"weight":           1,
			"vertical_spacing": "0px",
			"elements":         rows,
		}},
	}, components
}

func cardTableCellMarkdown(text, align string) map[string]any {
	md := map[string]any{"tag": "markdown", "content": text}
	if align != "" {
		md["text_align"] = align
	}
	return md
}

func cardTableFoldPanel(rest, align string) map[string]any {
	return map[string]any{
		"tag":      "collapsible_panel",
		"expanded": false,
		"padding":  "0px",
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "markdown",
				"content": fmt.Sprintf("<font color='grey'>展开其余 %d 字</font>", utf8.RuneCountInString(rest)),
			},
			"icon": map[string]any{
				"tag":   "standard_icon",
				"token": "down-small-ccm_outlined",
				"color": "grey",
				"size":  "16px 16px",
			},
			"icon_position":       "follow_text",
			"icon_expanded_angle": -180,
		},
		"elements": []map[string]any{cardTableCellMarkdown(rest, align)},
	}
}

// buildNativeCardTable draws a table as a native table with the given column
// widths. Header cells are plain text there, so their Markdown is dropped.
func buildNativeCardTable(t cardTable, widths []string) map[string]any {
	columns := make([]map[string]any, len(t.header))
	for c, h := range t.header {
		col := map[string]any{
			"name":           fmt.Sprintf("c%d", c),
			"display_name":   cardPlainText(h),
			"data_type":      "markdown",
			"width":          widths[c],
			"vertical_align": "top",
		}
		if t.align[c] != "" {
			col["horizontal_align"] = t.align[c]
		}
		columns[c] = col
	}
	rows := make([]map[string]any, len(t.rows))
	for r, cells := range t.rows {
		row := make(map[string]any, len(cells))
		for c, cell := range cells {
			row[fmt.Sprintf("c%d", c)] = cell
		}
		rows[r] = row
	}
	return map[string]any{
		"tag":            "table",
		"columns":        columns,
		"rows":           rows,
		"page_size":      min(max(len(rows), 1), cardTableNativePageMax),
		"row_height":     "auto",
		"row_max_height": cardTableRowMaxHeight,
		"header_style": map[string]any{
			"bold":             true,
			"background_style": "grey",
		},
	}
}

// cardFoldProtectedPattern matches spans a fold must not cut: links and
// images, bare URLs, inline code, emphasis, and inline HTML-like tags.
var cardFoldProtectedPattern = regexp.MustCompile(
	"!?\\[[^\\]]*\\]\\([^)]*\\)" + // [text](url) and ![alt](src)
		"|<[a-zA-Z]+[^>]*>.*?</[a-zA-Z]+>" + // <font ...>...</font>, <at ...>...</at>
		"|https?://[^\\s<>()\\p{Han}\\x{3000}-\\x{303F}\\x{FF00}-\\x{FFEF}]+" + // bare URLs
		"|`[^`]*`" + // inline code
		"|\\*\\*.+?\\*\\*|~~.+?~~", // bold, strikethrough
)

// foldCardCell splits a long cell into a preview and the rest. It cuts only
// after a line break, punctuation or space that lies outside links, URLs,
// inline code and emphasis, close to cardTableFoldPreview; with no such place,
// or too little left to fold, it reports false and the cell stays whole.
func foldCardCell(text string) (string, string, bool) {
	if cardTextWidth(text) <= cardTableFoldWidth {
		return "", "", false
	}
	protected := cardFoldProtectedPattern.FindAllStringIndex(text, -1)
	inside := func(i int) bool {
		for _, span := range protected {
			if i > span[0] && i < span[1] {
				return true
			}
		}
		return false
	}
	best, bestScore := -1, math.MaxInt
	width := 0
	for i, r := range text {
		if isWideRune(r) {
			width += 2
		} else {
			width++
		}
		if width > cardTableFoldPreview+cardTableFoldSlack {
			break
		}
		end := i + utf8.RuneLen(r)
		if !strings.ContainsRune("\n。！？；，、.!?;, ", r) || inside(end) {
			continue
		}
		// ASCII punctuation ends a clause only before a space: not in 3.14 or a.b.
		if strings.ContainsRune(".!?;,", r) && end < len(text) && text[end] != ' ' && text[end] != '\n' {
			continue
		}
		// Prefer line breaks, then sentence ends, nearest the preview width.
		score := width - cardTableFoldPreview
		if score < 0 {
			score = -score
		}
		switch {
		case r == '\n':
			score -= 40
		case strings.ContainsRune("。！？；.!?;", r):
			score -= 20
		}
		if width >= cardTableFoldPreview/2 && score < bestScore {
			best, bestScore = end, score
		}
	}
	if best < 0 {
		return "", "", false
	}
	preview := strings.TrimRight(text[:best], " \n")
	rest := strings.TrimLeft(text[best:], " \n")
	if cardTextWidth(rest) < cardTableFoldMinRest {
		return "", "", false
	}
	return preview, rest, true
}

// cardPlainText drops link targets and paired Markdown markup; lone markers
// such as the underscore in task_id stay.
func cardPlainText(s string) string {
	s = cardTextLinkPattern.ReplaceAllString(s, "$1")
	s = cardPairedMarkupPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := cardPairedMarkupPattern.FindStringSubmatch(m)
		for _, g := range sub[1:] {
			if g != "" {
				return g
			}
		}
		return ""
	})
	return strings.TrimSpace(cardHTMLTagPattern.ReplaceAllString(s, ""))
}

var (
	cardTextLinkPattern   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	cardTextMarkupPattern = regexp.MustCompile("[*_`~]+|</?[a-zA-Z][^>]*>")
	// Bold, strikethrough, inline code, then single-asterisk emphasis.
	cardPairedMarkupPattern = regexp.MustCompile("\\*\\*(.+?)\\*\\*|~~(.+?)~~|`([^`]+)`|\\*([^*\\s][^*]*)\\*")
	cardHTMLTagPattern      = regexp.MustCompile("</?[a-zA-Z][^>]*>")
)

// cardTextWidth estimates the rendered width of a Markdown cell in Latin
// character units: wide (CJK) characters count two, link targets and markup
// count nothing.
func cardTextWidth(s string) int {
	s = cardTextLinkPattern.ReplaceAllString(s, "$1")
	s = cardTextMarkupPattern.ReplaceAllString(s, "")
	width := 0
	for _, r := range s {
		if isWideRune(r) {
			width += 2
		} else if !unicode.IsControl(r) {
			width++
		}
	}
	return width
}

func isWideRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) ||
		(r >= 0x3000 && r <= 0x303F) || // CJK punctuation
		(r >= 0xFF00 && r <= 0xFF60) || (r >= 0xFFE0 && r <= 0xFFE6) // full-width forms
}

var cardTableBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>`)

// parseCardTable parses a GFM table found by findMarkdownTablesOutsideCodeBlocks.
// Rows with extra cells keep them, joined into the last column, and short rows
// are padded, so no text is dropped.
func parseCardTable(raw string) (cardTable, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) < 2 {
		return cardTable{}, false
	}
	header := splitCardTableRow(lines[0])
	n := len(header)
	if n == 0 {
		return cardTable{}, false
	}
	t := cardTable{header: header, align: make([]string, n)}
	for i, spec := range splitCardTableRow(lines[1]) {
		if i >= n {
			break
		}
		left, right := strings.HasPrefix(spec, ":"), strings.HasSuffix(spec, ":")
		switch {
		case left && right:
			t.align[i] = "center"
		case right:
			t.align[i] = "right"
		}
	}
	for _, line := range lines[2:] {
		cells := splitCardTableRow(line)
		if len(cells) > n {
			cells = append(cells[:n-1], strings.Join(cells[n-1:], " | "))
		}
		for len(cells) < n {
			cells = append(cells, "")
		}
		t.rows = append(t.rows, cells)
	}
	return t, true
}

// splitCardTableRow splits a table row on unescaped pipes outside inline code.
func splitCardTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var cell strings.Builder
	inCode := false
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		switch {
		case r == '\\' && i+1 < len(line) && line[i+1] == '|':
			cell.WriteByte('|')
			i += 2
			continue
		case r == '`' && (inCode || strings.IndexByte(line[i+1:], '`') >= 0):
			// A backtick without a closing one is plain text, as in GFM.
			inCode = !inCode
		case r == '|' && !inCode:
			cells = append(cells, cardTableCellText(cell.String()))
			cell.Reset()
			i += size
			continue
		}
		cell.WriteRune(r)
		i += size
	}
	return append(cells, cardTableCellText(cell.String()))
}

func cardTableCellText(s string) string {
	return cardTableBreakPattern.ReplaceAllString(strings.TrimSpace(s), "\n")
}
