package feishu

import (
	"encoding/json"
	"testing"
)

func TestIsTableRow(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"| a | b |", true},
		{"|a|b|", true},
		{"| single |", true},
		{"|", false},       // too short
		{"", false},        // empty
		{"no pipes", false}, // no pipes
		{"| only left", false},
		{"only right |", false},
	}
	for _, tt := range tests {
		if got := isTableRow(tt.line); got != tt.want {
			t.Errorf("isTableRow(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestIsTableSeparator(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"|---|---|", true},
		{"| --- | --- |", true},
		{"| :--- | :---: | ---: |", true},
		{"| :---: |", true},
		{"| data | row |", false},   // has letters
		{"|---|", true},              // single column
		{"| - |", true},             // minimal dash
		{"| |", false},              // empty cell, no dash
		{"| :: |", false},           // no dash
	}
	for _, tt := range tests {
		if got := isTableSeparator(tt.line); got != tt.want {
			t.Errorf("isTableSeparator(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestParsePipeCells(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{"| a | b | c |", []string{"a", "b", "c"}},
		{"|a|b|", []string{"a", "b"}},
		{"| hello world | 123 |", []string{"hello world", "123"}},
	}
	for _, tt := range tests {
		got := parsePipeCells(tt.line)
		if len(got) != len(tt.want) {
			t.Errorf("parsePipeCells(%q) len = %d, want %d", tt.line, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parsePipeCells(%q)[%d] = %q, want %q", tt.line, i, got[i], tt.want[i])
			}
		}
	}
}

func TestMdTableToCardElements_SimpleTable(t *testing.T) {
	md := "| Name | Score |\n| --- | --- |\n| Alice | 90 |\n| Bob | 85 |"
	elements := mdTableToCardElements(md)

	if len(elements) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elements))
	}

	el, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatal("element is not map[string]any")
	}
	if el["tag"] != "table" {
		t.Errorf("tag = %v, want table", el["tag"])
	}
	if el["page_size"] != defaultTablePageSize {
		t.Errorf("page_size = %v, want %d", el["page_size"], defaultTablePageSize)
	}

	cols := el["columns"].([]map[string]any)
	if len(cols) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(cols))
	}
	if cols[0]["display_name"] != "Name" {
		t.Errorf("col[0].display_name = %v, want Name", cols[0]["display_name"])
	}
	if cols[0]["data_type"] != "text" {
		t.Errorf("col[0].data_type = %v, want text", cols[0]["data_type"])
	}

	rows := el["rows"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0]["c0"] != "Alice" {
		t.Errorf("rows[0][c0] = %v, want Alice", rows[0]["c0"])
	}
}

func TestMdTableToCardElements_WithLinks(t *testing.T) {
	md := "| Key | Title |\n|---|---|\n| [D1-1](http://jira/D1-1) | Fix bug |"
	elements := mdTableToCardElements(md)

	el := elements[0].(map[string]any)
	cols := el["columns"].([]map[string]any)

	if cols[0]["data_type"] != "lark_md" {
		t.Errorf("link column data_type = %v, want lark_md", cols[0]["data_type"])
	}
	if cols[1]["data_type"] != "text" {
		t.Errorf("text column data_type = %v, want text", cols[1]["data_type"])
	}
}

func TestMdTableToCardElements_MixedContent(t *testing.T) {
	md := "**Title**\n\nSome intro.\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\nFooter text."
	elements := mdTableToCardElements(md)

	if len(elements) != 3 {
		t.Fatalf("expected 3 elements, got %d: %+v", len(elements), elements)
	}

	// First element: markdown before table.
	e0 := elements[0].(map[string]any)
	if e0["tag"] != "markdown" {
		t.Errorf("elements[0] tag = %v, want markdown", e0["tag"])
	}

	// Second element: native table.
	e1 := elements[1].(map[string]any)
	if e1["tag"] != "table" {
		t.Errorf("elements[1] tag = %v, want table", e1["tag"])
	}

	// Third element: markdown after table.
	e2 := elements[2].(map[string]any)
	if e2["tag"] != "markdown" {
		t.Errorf("elements[2] tag = %v, want markdown", e2["tag"])
	}
}

func TestMdTableToCardElements_CodeBlockPreserved(t *testing.T) {
	md := "```\n| not | a | table |\n|---|---|---|\n| just | code | block |\n```"
	elements := mdTableToCardElements(md)

	if len(elements) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elements))
	}
	el := elements[0].(map[string]any)
	if el["tag"] != "markdown" {
		t.Errorf("code block table should stay as markdown, got tag = %v", el["tag"])
	}
}

func TestMdTableToCardElements_NoTable(t *testing.T) {
	md := "Just some **bold** text.\n\nAnd a paragraph."
	elements := mdTableToCardElements(md)

	if len(elements) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elements))
	}
	el := elements[0].(map[string]any)
	if el["tag"] != "markdown" {
		t.Errorf("tag = %v, want markdown", el["tag"])
	}
	if el["content"] != md {
		t.Errorf("content mismatch")
	}
}

func TestMdTableToCardElements_HeaderOnlyTable(t *testing.T) {
	md := "| A | B |\n|---|---|"
	elements := mdTableToCardElements(md)

	if len(elements) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elements))
	}
	el := elements[0].(map[string]any)
	if el["tag"] != "table" {
		t.Errorf("tag = %v, want table", el["tag"])
	}
	rows := el["rows"].([]map[string]any)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestBuildCardJSON_WithTable(t *testing.T) {
	content := "**Results**\n\n| X | Y |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |"
	result := buildCardJSON(content)

	var card map[string]any
	if err := json.Unmarshal([]byte(result), &card); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if card["schema"] != "2.0" {
		t.Errorf("schema = %v, want 2.0", card["schema"])
	}

	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(elements))
	}

	// First: markdown, second: table.
	e0 := elements[0].(map[string]any)
	if e0["tag"] != "markdown" {
		t.Errorf("elements[0] tag = %v, want markdown", e0["tag"])
	}
	e1 := elements[1].(map[string]any)
	if e1["tag"] != "table" {
		t.Errorf("elements[1] tag = %v, want table", e1["tag"])
	}
	if e1["page_size"].(float64) != float64(defaultTablePageSize) {
		t.Errorf("page_size = %v, want %d", e1["page_size"], defaultTablePageSize)
	}
}
