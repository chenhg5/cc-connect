package feishu

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type testCardColumn struct {
	Tag      string           `json:"tag"`
	Weight   int              `json:"weight"`
	Elements []map[string]any `json:"elements"`
}

type testCardElement struct {
	Tag     string           `json:"tag"`
	Content string           `json:"content"`
	Columns []testCardColumn `json:"columns"`
}

type testReplyCard struct {
	Schema string `json:"schema"`
	Config struct {
		WidthMode string `json:"width_mode"`
	} `json:"config"`
	Body struct {
		Elements []testCardElement `json:"elements"`
	} `json:"body"`
}

func decodeTestReplyCard(t *testing.T, card string) testReplyCard {
	t.Helper()
	var c testReplyCard
	if err := json.Unmarshal([]byte(card), &c); err != nil {
		t.Fatalf("decode card: %v\n%s", err, card)
	}
	return c
}

// testTableRows returns the cell texts and weights of a table element.
func testTableRows(t *testing.T, el testCardElement) ([][]string, []int) {
	t.Helper()
	if el.Tag != "column_set" || len(el.Columns) != 1 {
		t.Fatalf("table element = %+v, want one outer column", el)
	}
	raw, _ := json.Marshal(el.Columns[0].Elements)
	var rows []testCardElement
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	var cells [][]string
	var weights []int
	for i, row := range rows {
		var texts []string
		for _, col := range row.Columns {
			texts = append(texts, col.Elements[0]["content"].(string))
			if i == 0 {
				weights = append(weights, col.Weight)
			}
		}
		cells = append(cells, texts)
	}
	return cells, weights
}

func TestBuildCardJSON_FillsWidthAndKeepsPlainMarkdown(t *testing.T) {
	const content = "Hello **world**\n\n- one\n- two"
	for name, card := range map[string]string{
		"plain":       buildCardJSON(content),
		"with footer": buildCardJSONWithStatusFooter(content, "Status"),
	} {
		t.Run(name, func(t *testing.T) {
			c := decodeTestReplyCard(t, card)
			if c.Schema != "2.0" || c.Config.WidthMode != "fill" {
				t.Fatalf("schema = %q, width_mode = %q; want 2.0 and fill", c.Schema, c.Config.WidthMode)
			}
			if strings.Contains(card, "wide_screen_mode") {
				t.Errorf("card still sets the legacy wide_screen_mode: %s", card)
			}
			first := c.Body.Elements[0]
			if first.Tag != "markdown" || first.Content != content {
				t.Errorf("first element = %+v, want the Markdown unchanged", first)
			}
		})
	}
}

func TestBuildCardJSON_DrawsTablesAsWeightedRows(t *testing.T) {
	long := strings.Repeat("这一列的说明文字很长，需要换行显示。", 6)
	content := "Before the table.\n\n" +
		"| ID | Status | Description |\n" +
		"| --- | :---: | --- |\n" +
		"| 1 | ok | " + long + " |\n" +
		"| 2 | failed | `a|b` and a\\|b<br>next line |\n" +
		"\nAfter the table."
	for name, card := range map[string]string{
		"plain":       buildCardJSON(content),
		"with footer": buildCardJSONWithStatusFooter(content, "Status"),
	} {
		t.Run(name, func(t *testing.T) {
			c := decodeTestReplyCard(t, card)
			if c.Config.WidthMode != "fill" {
				t.Fatalf("width_mode = %q, want fill", c.Config.WidthMode)
			}
			els := c.Body.Elements
			if len(els) < 3 || els[0].Content != "Before the table." || els[2].Content != "After the table." {
				t.Fatalf("elements = %+v, want text, table, text", els)
			}
			cells, weights := testTableRows(t, els[1])
			want := [][]string{
				{"**ID**", "**Status**", "**Description**"},
				{"1", "ok", long},
				{"2", "failed", "`a|b` and a|b\nnext line"},
			}
			if !reflect.DeepEqual(cells, want) {
				t.Errorf("cells = %q, want %q", cells, want)
			}
			if weights[2] != cardTableMaxWeight || weights[0] >= weights[2] || weights[1] >= weights[2] {
				t.Errorf("weights = %v, want the description column widest", weights)
			}
		})
	}
}

func TestCardTableColumnWeights(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want []int
	}{
		{"equal columns", "| a | b |\n| - | - |\n| xx | yy |", []int{5, 5}},
		{"short and long", "| ID | Note |\n| - | - |\n| 1 | " + strings.Repeat("long text ", 20) + " |", []int{1, 5}},
		{"one huge cell is capped", "| A | B |\n| - | - |\n| " + strings.Repeat("x", 5000) + " | " + strings.Repeat("y", 100) + " |\n| x | " + strings.Repeat("y", 100) + " |", []int{5, 4}},
		{"links count their text", "| Link | Name |\n| - | - |\n| [doc](https://example.com/" + strings.Repeat("p", 300) + ") | abc |", []int{5, 5}},
		{"header only", "| Short | A much longer header |\n| - | - |", []int{3, 5}},
		{"narrow label column is widened", "| 项目 | A | B | C | D |\n| - | - | - | - | - |\n| 中文长段落 | " + strings.Repeat("长文本", 60) + " | " + strings.Repeat("中等", 30) + " | " + strings.Repeat("中等", 30) + " | " + strings.Repeat("中等", 30) + " |", []int{3, 5, 4, 4, 4}},
		{"eight short columns", "| 日期 | 车型 | 版本 | 里程 | 接管 | 时长 | 城市 | 结论 |\n| - | - | - | - | - | - | - | - |\n| 11-08 | J6M | 1.8.1 | 132km | 2 | 3.1h | 苏州 | 通过 |", []int{5, 4, 5, 5, 4, 4, 4, 4}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			table, ok := parseCardTable(tt.raw)
			if !ok {
				t.Fatal("table not parsed")
			}
			if got, ok := cardTableColumnWeights(cardTableColumns(table)); !ok || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("weights = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildCardJSON_TooManyColumnsBecomeScrollingNativeTable(t *testing.T) {
	header := "|" + strings.Repeat(" 字段名称 |", 12)
	sep := "|" + strings.Repeat(" - |", 12)
	row := "|" + strings.Repeat(" 中文内容 |", 12)
	long := "| **长** | " + strings.Repeat("很长的说明文字。", 40) + " |" + strings.Repeat(" x |", 10)
	table, ok := parseCardTable(header + "\n" + sep + "\n" + row)
	if !ok {
		t.Fatal("table not parsed")
	}
	if weights, ok := cardTableColumnWeights(cardTableColumns(table)); ok {
		t.Errorf("weights = %v, want the 12-column table rejected", weights)
	}

	card := buildCardJSON("intro\n\n" + header + "\n" + sep + "\n" + row + "\n" + long + "\n")
	var c struct {
		Body struct {
			Elements []struct {
				Tag     string `json:"tag"`
				Columns []struct {
					DisplayName string `json:"display_name"`
					DataType    string `json:"data_type"`
					Width       string `json:"width"`
				} `json:"columns"`
				Rows         []map[string]string `json:"rows"`
				PageSize     int                 `json:"page_size"`
				RowHeight    string              `json:"row_height"`
				RowMaxHeight string              `json:"row_max_height"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(card), &c); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	els := c.Body.Elements
	if len(els) != 2 || els[0].Tag != "markdown" || els[1].Tag != "table" {
		t.Fatalf("elements = %+v, want intro then a native table", els)
	}
	tbl := els[1]
	if len(tbl.Columns) != 12 || tbl.Columns[0].DisplayName != "字段名称" || tbl.Columns[0].DataType != "markdown" {
		t.Errorf("columns = %+v", tbl.Columns)
	}
	var widths []string
	for _, col := range tbl.Columns {
		widths = append(widths, col.Width)
	}
	// The long-text column fills the card; the others fit their content.
	if want := []string{"80px", "auto", "80px", "80px", "80px", "80px", "80px", "80px", "80px", "80px", "80px", "80px"}; !reflect.DeepEqual(widths, want) {
		t.Errorf("widths = %q, want %q", widths, want)
	}
	if len(tbl.Rows) != 2 || tbl.Rows[1]["c0"] != "**长**" || tbl.PageSize != 2 || tbl.RowHeight != "auto" || tbl.RowMaxHeight != cardTableRowMaxHeight {
		t.Errorf("table = %+v", tbl)
	}
}

func TestFoldCardCell(t *testing.T) {
	sentence := "这是一句用于测试折叠位置的中文句子。"
	url := "https://example.com/very/long/path/that/must/stay/whole/" + strings.Repeat("segment-", 30) + "end"
	for _, tt := range []struct {
		name       string
		text       string
		wantFold   bool
		wantIntact []string
	}{
		{"short cell", sentence, false, nil},
		{"sentences", strings.Repeat(sentence, 20), true, nil},
		{"bare URL across the preview", strings.Repeat(sentence, 5) + "链接 " + url + " 之后。" + strings.Repeat(sentence, 6), true, []string{url}},
		{"Markdown link across the preview", strings.Repeat(sentence, 5) + "[链接, 说明. 文字](" + url + ")" + strings.Repeat(sentence, 6), true, []string{"[链接, 说明. 文字](" + url + ")"}},
		{"inline code across the preview", strings.Repeat(sentence, 5) + "`a, b. c; d " + strings.Repeat("x", 300) + "`" + strings.Repeat(sentence, 6), true, []string{"`a, b. c; d " + strings.Repeat("x", 300) + "`"}},
		{"no place to cut", strings.Repeat("长", 400), false, nil},
		{"decimal points are not sentence ends", strings.Repeat("版本1.2.3和3.14", 40), false, nil},
		{"little left to fold", strings.Repeat("长", 115) + "。" + strings.Repeat("长", 28), false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			preview, rest, ok := foldCardCell(tt.text)
			if ok != tt.wantFold {
				t.Fatalf("fold = %v, want %v (preview %d chars)", ok, tt.wantFold, len([]rune(preview)))
			}
			if !ok {
				return
			}
			if joined := preview + rest; strings.ReplaceAll(joined, " ", "") != strings.ReplaceAll(tt.text, " ", "") {
				t.Errorf("preview + rest lost text:\n%q\n%q", preview, rest)
			}
			if w := cardTextWidth(preview); w < cardTableFoldPreview/2 || w > cardTableFoldPreview+cardTableFoldSlack {
				t.Errorf("preview width = %d", w)
			}
			for _, span := range tt.wantIntact {
				if !strings.Contains(preview, span) && !strings.Contains(rest, span) {
					t.Errorf("%q was cut between preview and rest", span)
				}
			}
		})
	}
}

func TestBuildCardJSON_FoldsLongCells(t *testing.T) {
	long := strings.Repeat("这是一句用于测试折叠位置的中文句子。", 20)
	card := buildCardJSON("| 项 | 说明 |\n| - | - |\n| a | " + long + " |\n")
	c := decodeTestReplyCard(t, card)
	raw, _ := json.Marshal(c.Body.Elements[0].Columns[0].Elements)
	var rows []testCardElement
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	cell := rows[1].Columns[1].Elements
	if len(cell) != 2 || cell[0]["tag"] != "markdown" || cell[1]["tag"] != "collapsible_panel" {
		t.Fatalf("cell = %+v, want preview and fold panel", cell)
	}
	panel, _ := json.Marshal(cell[1])
	var p struct {
		Expanded bool `json:"expanded"`
		Elements []struct {
			Content string `json:"content"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(panel, &p); err != nil {
		t.Fatal(err)
	}
	if p.Expanded || cell[0]["content"].(string)+p.Elements[0].Content != long {
		t.Errorf("panel = %s, want folded rest of the cell", panel)
	}
}

func TestParseCardTable_KeepsEveryCell(t *testing.T) {
	table, ok := parseCardTable("| a | b |\n| --- | ---: |\n| 1 | 2 | 3 |\n| only |")
	if !ok {
		t.Fatal("table not parsed")
	}
	if want := [][]string{{"1", "2 | 3"}, {"only", ""}}; !reflect.DeepEqual(table.rows, want) {
		t.Errorf("rows = %q, want %q", table.rows, want)
	}
	if want := []string{"", "right"}; !reflect.DeepEqual(table.align, want) {
		t.Errorf("align = %q, want %q", table.align, want)
	}
}

func TestBuildCardJSON_LargeTablesStayNative(t *testing.T) {
	var b strings.Builder
	b.WriteString("| a | b | c | d | e |\n| - | - | - | - | - |\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "| %d | b | c | d | e |\n", i)
	}
	content := b.String()
	c := decodeTestReplyCard(t, buildCardJSON(content))
	if len(c.Body.Elements) != 1 || c.Body.Elements[0].Tag != "markdown" || !strings.Contains(c.Body.Elements[0].Content, "| 39 |") {
		t.Errorf("elements = %+v, want one markdown element with the native table", c.Body.Elements)
	}
}

func TestBuildCardJSON_OversizedRowsFallBackToNativeTable(t *testing.T) {
	cell := strings.Repeat("长", 1500)
	content := "intro\n\n| a | b |\n| - | - |\n| " + cell + " | " + cell + " |\n| " + cell + " | " + cell + " |\n| " + cell + " | " + cell + " |\n"
	card := buildCardJSON(content)
	c := decodeTestReplyCard(t, card)
	if len(c.Body.Elements) != 1 || c.Body.Elements[0].Tag != "markdown" {
		t.Errorf("elements = %d (first %q), want one markdown element", len(c.Body.Elements), c.Body.Elements[0].Tag)
	}
}

func TestBuildCardJSON_OversizedTableOnlyReplyFallsBack(t *testing.T) {
	cell := strings.Repeat("长", 130)
	var b strings.Builder
	b.WriteString("| a | b | c | d |\n| - | - | - | - |\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cell, cell, cell, cell)
	}
	content := b.String()
	if els := buildCardBodyElements(sanitizeCardMarkdownForCard(content)); len(els) != 1 || els[0]["tag"] != "column_set" {
		t.Fatalf("precondition: want the table alone drawn as rows, got %d elements", len(els))
	}
	card := buildCardJSON(content)
	c := decodeTestReplyCard(t, card)
	if len(card) > maxRichCardJSONBytes || len(c.Body.Elements) != 1 || c.Body.Elements[0].Tag != "markdown" {
		t.Errorf("card = %d bytes, first element %q; want the native Markdown table within the limit", len(card), c.Body.Elements[0].Tag)
	}
}

func TestCardPlainText(t *testing.T) {
	for in, want := range map[string]string{
		"task_id":                      "task_id",
		"p_99 *latency*":               "p_99 latency",
		"**Bold** and ~~old~~":         "Bold and old",
		"`file_name`":                  "file_name",
		"[Docs](https://example.com)":  "Docs",
		"<font color='red'>Red</font>": "Red",
		"a * b":                        "a * b",
	} {
		if got := cardPlainText(in); got != want {
			t.Errorf("cardPlainText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitCardTableRow_UnpairedBacktick(t *testing.T) {
	for in, want := range map[string][]string{
		"| don`t | x | y |": {"don`t", "x", "y"},
		"| `a|b` | c |":     {"`a|b`", "c"},
		"| `a | b` ` | c |": {"`a | b` `", "c"},
	} {
		if got := splitCardTableRow(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCardTableRow(%q) = %q, want %q", in, got, want)
		}
	}
}
