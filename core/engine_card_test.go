package core

import (
	"strings"
	"testing"
)

// TestBuildCardContent_CompactSummary verifies the streaming card renders
// thinking and tool entries as compact summaries (one-line excerpt + tool
// name/count) instead of dumping full intermediate process inline. The
// final answer stays full-length.
func TestBuildCardContent_CompactSummary(t *testing.T) {
	thinking := "第一步：检查 ACR 镜像 tag。\n第二步：比对 manifest。\n第三步：写补丁脚本。\n第四步：验证收敛。"
	tools := []cardToolEntry{
		{Index: 1, Name: "read", Input: "/var/folders/.../nacos_dump.sh"},
		{Index: 2, Name: "bash", Input: "ls -la /var/folders/.../opencode/ | head -30"},
		{Index: 3, Name: "bash", Input: "rg -n NACOS_USER ..."},
	}
	answer := "测试 56 全绿，dist 已重建。\n\n**两个提示都源自 render-validate.sh。**"

	got := buildCardContent(thinking, tools, answer)

	// Summary lines present, full tool inputs absent.
	if !strings.Contains(got, "💭 **思考**") {
		t.Errorf("missing thinking header:\n%s", got)
	}
	if !strings.Contains(got, "🔧 **工具 (3)**: read, bash") {
		t.Errorf("missing tool summary line, want deduped names:\n%s", got)
	}
	// Thinking is collapsed to a single line — the excerpt itself has no
	// newline (the header line and the blank separator are expected).
	start := strings.Index(got, "💭 **思考**")
	toolStart := strings.Index(got, "🔧 **工具")
	if start < 0 || toolStart < 0 || toolStart <= start {
		t.Fatalf("unexpected card layout:\n%s", got)
	}
	excerpt := got[start+len("💭 **思考**") : toolStart]
	lines := strings.Split(strings.TrimSpace(excerpt), "\n")
	if len(lines) != 1 {
		t.Errorf("thinking excerpt should be one line, got %d lines:\n%q", len(lines), excerpt)
	}
	// Full tool inputs must not leak into the card.
	if strings.Contains(got, "nacos_dump.sh") || strings.Contains(got, "ls -la") {
		t.Errorf("tool input leaked into card:\n%s", got)
	}
	// Final answer preserved in full.
	if !strings.Contains(got, "测试 56 全绿") || !strings.Contains(got, "render-validate.sh") {
		t.Errorf("final answer not preserved:\n%s", got)
	}
}

// TestBuildCardContent_NoThinkingNoTools verifies the card is just the
// answer when there is no intermediate process.
func TestBuildCardContent_NoThinkingNoTools(t *testing.T) {
	got := buildCardContent("", nil, "就一句话。")
	if got != "就一句话。" {
		t.Errorf("got %q, want plain answer", got)
	}
}

// TestBuildCardContent_DeduplicatesToolNames verifies repeated tool calls
// collapse to unique names in the summary line.
func TestBuildCardContent_DeduplicatesToolNames(t *testing.T) {
	tools := []cardToolEntry{
		{Index: 1, Name: "bash", Input: "a"},
		{Index: 2, Name: "bash", Input: "b"},
		{Index: 3, Name: "read", Input: "c"},
	}
	got := buildCardContent("", tools, "完成")
	if !strings.Contains(got, "🔧 **工具 (3)**: bash, read") {
		t.Errorf("got %q, want deduped summary", got)
	}
	// Inputs never appear.
	if strings.Contains(got, "```") {
		t.Errorf("tool input leaked: %q", got)
	}
}

// TestCompactOneLine verifies multi-line blocks collapse to one capped line.
func TestCompactOneLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  简单  ", "简单"},
		{"第一行\n第二行\n第三行", "第一行 第二行 第三行"},
	}
	for _, c := range cases {
		if got := compactOneLine(c.in); got != c.want {
			t.Errorf("compactOneLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("字", 300)
	got := compactOneLine(long)
	if len([]rune(got)) != 121 { // 120 + ellipsis
		t.Errorf("compactOneLine long = %d runes, want 121", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("compactOneLine long missing ellipsis: %q", got)
	}
}

// TestStripAnswerEchoes pins the rule that keeps the final answer out of the
// process panel.
//
// The card folds every EventText chunk into the foldable thinking panel, and
// for every adapter except opencode the assistant text IS the answer — so the
// reply used to render inside the "思考" panel and then again as the separate
// answer message (observed live on a claudecode project). Only a trailing run
// of entries that reproduces the END of the answer is dropped.
func TestStripAnswerEchoes(t *testing.T) {
	tests := []struct {
		name      string
		stepTexts []string
		answer    string
		want      int // expected len(kept)
	}{
		{
			name:      "answer-only turn loses the echoed answer",
			stepTexts: []string{"这是最终答复。"},
			answer:    "这是最终答复。",
			want:      0,
		},
		{
			name:      "narration before the answer stays in the panel",
			stepTexts: []string{"先看一下代码。", "这是最终答复。"},
			answer:    "这是最终答复。",
			want:      1,
		},
		{
			name:      "chunked answer echo is dropped as one trailing run",
			stepTexts: []string{"先看一下代码。", "第一段。", "第二段。"},
			answer:    "第一段。\n第二段。",
			want:      1,
		},
		{
			name:      "opencode-style step narration is untouched",
			stepTexts: []string{"先看一下代码。", "再核对调用方。"},
			answer:    "结论：上游重复计费。",
			want:      2,
		},
		{
			name:      "empty answer leaves the panel alone",
			stepTexts: []string{"先看一下代码。"},
			answer:    "",
			want:      1,
		},
		{
			name:      "trailing answer duplicate is dropped, earlier note kept",
			stepTexts: []string{"[ps] 补充：顺便看下日志。", "结论：上游重复计费。"},
			answer:    "结论：上游重复计费。",
			want:      1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripAnswerEchoes(tc.stepTexts, tc.answer)
			if len(got) != tc.want {
				t.Fatalf("stripAnswerEchoes() kept %d entries (%q), want %d", len(got), got, tc.want)
			}
			if tc.want > 0 && got[0] != tc.stepTexts[0] {
				t.Errorf("stripAnswerEchoes() dropped from the wrong end: %q", got)
			}
		})
	}
}

// TestBuildStreamingCardPayload verifies the streaming-card payload encodes
// thinking and tool entries as typed panels plus the final answer body, and
// that the transport string parses back into the structured payload.
func TestBuildStreamingCardPayload(t *testing.T) {
	thinking := "检查 ACR 镜像 tag。\n比对 manifest。\n写补丁脚本。"
	tools := []cardToolEntry{
		{Index: 1, Name: "read", Input: "/tmp/nacos_dump.sh"},
		{Index: 2, Name: "bash", Input: "ls -la /tmp/opencode/"},
	}
	answer := "测试 56 全绿。\n\n**两个提示都源自 render-validate.sh。**"

	got := BuildStreamingCardPayload(thinking, []string{"第一步进展"}, tools, answer, "opencode", LangChinese, ProgressCardStateRunning)
	if !strings.HasPrefix(got, ProgressCardPayloadPrefix) {
		t.Fatalf("payload missing prefix: %q", got)
	}
	payload, ok := ParseProgressCardPayload(got)
	if !ok {
		t.Fatalf("payload did not parse back")
	}
	if payload.Agent != "opencode" || payload.Lang != string(LangChinese) {
		t.Errorf("payload meta = %q/%q", payload.Agent, payload.Lang)
	}
	if payload.State != ProgressCardStateRunning {
		t.Errorf("payload state = %q, want running", payload.State)
	}
	if payload.Answer != answer {
		t.Errorf("payload answer = %q, want %q", payload.Answer, answer)
	}
	if len(payload.Items) != 4 { // 1 thinking + 1 step + 2 tools
		t.Fatalf("payload items = %d, want 3", len(payload.Items))
	}
	if payload.Items[0].Kind != ProgressEntryThinking || payload.Items[0].Text != thinking {
		t.Errorf("item0 = %+v, want thinking entry", payload.Items[0])
	}
	if payload.Items[1].Kind != ProgressEntryThinking || payload.Items[1].Text != "第一步进展" {
		t.Errorf("item1 = %+v, want step thinking entry", payload.Items[1])
	}
	if payload.Items[2].Kind != ProgressEntryToolUse || payload.Items[2].Tool != "read" {
		t.Errorf("item2 = %+v, want tool entry read", payload.Items[2])
	}
	if payload.Items[3].Tool != "bash" {
		t.Errorf("item3 = %+v, want tool entry bash", payload.Items[3])
	}
}

// TestBuildStreamingCardPayload_EmptyItems verifies the payload is still
// transportable when there is no intermediate process — the answer-only card.
func TestBuildStreamingCardPayload_EmptyItems(t *testing.T) {
	got := BuildStreamingCardPayload("", nil, nil, "就一句话。", "opencode", LangChinese, ProgressCardStateCompleted)
	payload, ok := ParseProgressCardPayload(got)
	if !ok {
		t.Fatalf("payload did not parse back")
	}
	if len(payload.Items) != 0 {
		t.Errorf("payload items = %d, want 0", len(payload.Items))
	}
	if payload.Answer != "就一句话。" {
		t.Errorf("payload answer = %q", payload.Answer)
	}
	if payload.State != ProgressCardStateCompleted {
		t.Errorf("payload state = %q, want completed", payload.State)
	}
}

// TestBuildStreamingCardPayload_DedupLatestThinking verifies the latest
// thinking (also carried in stepTexts by the engine) is not double-counted
// in the panel — the "思考 (N)" count must reflect distinct steps.
func TestBuildStreamingCardPayload_DedupLatestThinking(t *testing.T) {
	thinking := "第三步：写补丁"
	steps := []string{"第一步：检查", "第二步：比对", "第三步：写补丁"}
	got := BuildStreamingCardPayload(thinking, steps, nil, "答复", "opencode", LangChinese, ProgressCardStateRunning)
	payload, ok := ParseProgressCardPayload(got)
	if !ok {
		t.Fatalf("payload did not parse back")
	}
	if len(payload.Items) != 3 { // 3 distinct steps, no duplicate of the latest
		t.Errorf("panel items = %d, want 3 (latest thinking deduped)", len(payload.Items))
	}
}
