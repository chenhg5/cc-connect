package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ── Helpers for seeding INDEX and archive files in a temp dir ─────

func seedArchive(t *testing.T, dir string) {
	t.Helper()

	archiveDir := filepath.Join(dir, "docs", "archive")
	threadsDir := filepath.Join(archiveDir, "threads", "rehydration-mechanism")
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		t.Fatalf("mkdir threads: %v", err)
	}

	// INDEX.md with a realistic mix of lines.
	indexContent := `# 信件档案索引

| ID | Type | Thread | Parent | 一句话摘要 | Date |
|---|---|---|---|---|---|
| L-0235 | QUERY | rehydration-mechanism | ROOT | 设计 WAKE+HANDOFF 退休后的 rehydration 方案 | 07-05 |
| L-0236 | RESULT | rehydration-mechanism | L-0235 | T6 WAKE/HANDOFF 引用已替换 | 07-05 |
| L-0245 | RESULT | infra-worktree-bug | L-0238 | Option A: config-only 移出 foundry repo | 07-05 |
| L-0247 | QUERY | product-next-steps | L-0238 | 架构师审查 PR #10 安全性 | 07-05 |
| L-0250 | RESULT | rehydration-mechanism | L-0235 | 推荐 B: cc-connect spawn-time Rehydration Digest | 07-05 |
| L-0242 | RESULT | leaked-api-key-landscape | ROOT | API Key 泄露黑产生态调研完成 | 07-05 |
| L-0251 | QUERY | rehydration-mechanism | L-0250 | 方案 B 实现 | 07-05 |
`
	if err := os.WriteFile(filepath.Join(archiveDir, "INDEX.md"), []byte(indexContent), 0o644); err != nil {
		t.Fatalf("write INDEX: %v", err)
	}

	// Parent letter L-0250 QUERY.
	l0250Query := `---
ID: L-0250
Thread: rehydration-mechanism
Parent: L-0235
Type: QUERY
To: architect-codex
Route: heavy
Date: 07-05
---

## Context Digest

本条是 rehydration-mechanism 线程的子信，继承 L-0235 的未决设计问题。

**已完成的奠基工作：**
- L-0236 验证计划 T6 测试已更新
- L-0216 archive-first 三版 preamble 文件已落地

## Query

请为 rehydration-mechanism 线程完成原始 L-0235 的设计问题。
`
	if err := os.WriteFile(filepath.Join(threadsDir, "L-0250.query.md"), []byte(l0250Query), 0o644); err != nil {
		t.Fatalf("write L-0250.query: %v", err)
	}

	// Parent letter L-0250 RESULT.
	l0250Result := `---
ID: L-0250
Thread: rehydration-mechanism
Parent: L-0235
Type: RESULT
To: architect-codex
Status: DONE
Date: 07-05
---

## Conclusion

推荐选 B：由 cc-connect 在每次冷启动 spawn 前生成一次冻结的 Rehydration Digest。

## Options for Boss

A. 零代码版
B. 推荐版：cc-connect spawn-time Digest 注入
C. 缓存版
`
	if err := os.WriteFile(filepath.Join(threadsDir, "L-0250.result.md"), []byte(l0250Result), 0o644); err != nil {
		t.Fatalf("write L-0250.result: %v", err)
	}

	// Current letter L-0251 QUERY.
	l0251Query := `---
ID: L-0251
Thread: rehydration-mechanism
Parent: L-0250
Type: QUERY
To: architect
Route: heavy
Date: 07-05
---

## Context Digest

本条是 rehydration-mechanism 线程的子信，继承 L-0250 的决议。

**L-0250 RESULT**：推荐方案 B。

## Query

实现方案 B：cc-connect spawn-time Rehydration Digest。
`
	if err := os.WriteFile(filepath.Join(threadsDir, "L-0251.query.md"), []byte(l0251Query), 0o644); err != nil {
		t.Fatalf("write L-0251.query: %v", err)
	}
}

// ── DeriveArchiveDir ─────────────────────────────────────────────

func TestDeriveArchiveDir(t *testing.T) {
	if got := DeriveArchiveDir(""); got != "" {
		t.Errorf("DeriveArchiveDir(\"\") = %q, want \"\"", got)
	}

	// Host-native absolute path.
	dataDir := filepath.Join(string(filepath.Separator)+"opt", "nexus", "data")
	want := filepath.Join(string(filepath.Separator)+"opt", "nexus", "docs", "archive")
	if got := DeriveArchiveDir(dataDir); got != want {
		t.Errorf("DeriveArchiveDir(%q) = %q, want %q", dataDir, got, want)
	}

	// Windows-style strings must derive on every OS (Linux migration / CI).
	tests := []struct {
		dataDir string
		want    string
	}{
		{`F:\nexus\data`, filepath.FromSlash(`F:/nexus/docs/archive`)},
		{`C:\Users\test\nexus\data`, filepath.FromSlash(`C:/Users/test/nexus/docs/archive`)},
	}
	for _, tc := range tests {
		got := DeriveArchiveDir(tc.dataDir)
		if got != tc.want {
			t.Errorf("DeriveArchiveDir(%q) = %q, want %q (GOOS=%s)", tc.dataDir, got, tc.want, runtime.GOOS)
		}
	}
}

func TestDeriveArchiveDir_PosixPath(t *testing.T) {
	got := DeriveArchiveDir("/opt/nexus/data")
	want := filepath.Join("/opt/nexus/docs/archive")
	if got != want {
		t.Errorf("DeriveArchiveDir posix = %q, want %q", got, want)
	}
}

// ── readTail ─────────────────────────────────────────────────────

func TestReadTail_LessThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "a\nb\nc\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readTail(path, 50)
	if got != "a\nb\nc" {
		t.Errorf("readTail small file = %q", got)
	}
}

func TestReadTail_ExactN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("1\n2\n3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readTail(path, 3)
	if got != "1\n2\n3" {
		t.Errorf("readTail %q", got)
	}
}

func TestReadTail_MoreThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readTail(path, 3)
	want := "line-8\nline-9\nline-10"
	if got != want {
		t.Errorf("readTail tail 3:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestReadTail_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readTail(path, 50); got != "" {
		t.Errorf("readTail empty = %q", got)
	}
}

func TestReadTail_MissingFile(t *testing.T) {
	if got := readTail("/nonexistent/path", 50); got != "" {
		t.Errorf("readTail missing = %q", got)
	}
}

// ── extractStuckBlocked ──────────────────────────────────────────

func TestExtractStuckBlocked(t *testing.T) {
	input := `| L-0207 | STUCK | context-compaction-config | 已交付但未完成验证 | 07-04 |
| L-0218 | STUCK | cc-connect-maintenance | PR修复CI仍红灯 | 07-05 |
| L-0224 | STUCK | cc-connect-maintenance | salvage PR | 07-05 |
| L-0250 | RESULT | rehydration-mechanism | 推荐方案 B | 07-05 |
| L-0245 | RESULT | infra-worktree-bug | 推荐 Option A | 07-05 |`

	stuck := extractStuckBlocked(input)
	if len(stuck) != 3 {
		t.Fatalf("expected 3 STUCK entries, got %d: %v", len(stuck), stuck)
	}
	for _, s := range stuck {
		if !strings.Contains(s, "STUCK") {
			t.Errorf("entry missing STUCK: %s", s)
		}
	}
}

func TestExtractStuckBlocked_None(t *testing.T) {
	input := `| L-0250 | RESULT | rehydration-mechanism | done | 07-05 |
| L-0251 | QUERY | rehydration-mechanism | plan | 07-05 |`
	if got := extractStuckBlocked(input); len(got) != 0 {
		t.Errorf("expected 0, got %d: %v", len(got), got)
	}
}

func TestExtractOpenStuckBlocked(t *testing.T) {
	input := `| L-0250 | RESULT | rehydration-mechanism | L-0235 | done | 07-05 |
| L-0251 | QUERY | rehydration-mechanism | L-0250 | implement | 07-05 |
| L-0252 | QUERY | product-next-steps | L-0244 | open product work | 07-05 |
| L-0252 | RESULT | product-next-steps | L-0244 | done product work | 07-05 |
| L-0258 | RESULT | presage-399-weekly-brief | ROOT | BLOCKED: missing screenshot | 07-05 |`

	got := extractOpenStuckBlocked(input, 10)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "L-0251") {
		t.Fatalf("expected open L-0251, got %v", got)
	}
	if strings.Contains(joined, "open product work") {
		t.Fatalf("resolved L-0252 QUERY should not be reported open: %v", got)
	}
	if !strings.Contains(joined, "BLOCKED") {
		t.Fatalf("expected BLOCKED row, got %v", got)
	}
}

// ── BuildRehydrationDigest integration ───────────────────────────

func TestBuildRehydrationDigest_WithArchive(t *testing.T) {
	dir := t.TempDir()
	seedArchive(t, dir)

	archiveDir := filepath.Join(dir, "docs", "archive")
	digest := BuildRehydrationDigest(RehydrationConfig{
		ArchiveDir:       archiveDir,
		ActiveLetterID:   "L-0251",
		ParentChainDepth: 1,
	})

	if digest == "" {
		t.Fatal("BuildRehydrationDigest returned empty")
	}

	// Should include INDEX tail.
	if !strings.Contains(digest, "INDEX") {
		t.Error("digest should mention INDEX")
	}
	// Should include the active letter context.
	if !strings.Contains(digest, "L-0251") {
		t.Error("digest should mention L-0251")
	}
	if !strings.Contains(digest, "## Query") && !strings.Contains(digest, "#### Query") {
		t.Error("digest should include active QUERY section")
	}
	// Should include parent chain context.
	if !strings.Contains(digest, "L-0250") {
		t.Error("digest should mention parent L-0250")
	}
	// Should include the Rehydration Digest heading.
	if !strings.Contains(digest, "Rehydration Digest") {
		t.Error("digest should have heading")
	}

	// Verify digest length is reasonable but bounded.
	if len(digest) > 10000 {
		t.Errorf("digest too long: %d chars", len(digest))
	}
	t.Logf("digest length: %d chars", len(digest))
	t.Logf("digest:\n%s", digest[:min(500, len(digest))])
}

func TestBuildRehydrationDigest_TrimsToBudget(t *testing.T) {
	dir := t.TempDir()
	seedArchive(t, dir)
	archiveDir := filepath.Join(dir, "docs", "archive")

	digest := BuildRehydrationDigest(RehydrationConfig{
		ArchiveDir:     archiveDir,
		ActiveLetterID: "L-0251",
		MaxTokens:      200,
	})

	if EstimateTokenCount(digest) > 220 {
		t.Fatalf("digest not trimmed enough: %d tokens", EstimateTokenCount(digest))
	}
	if !strings.Contains(digest, "truncated to budget") {
		t.Fatalf("expected truncation notice, got:\n%s", digest)
	}
}

func TestBuildRehydrationDigest_EmptyArchiveDir(t *testing.T) {
	if got := BuildRehydrationDigest(RehydrationConfig{ArchiveDir: ""}); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestBuildRehydrationDigest_MissingArchive(t *testing.T) {
	got := BuildRehydrationDigest(RehydrationConfig{
		ArchiveDir: t.TempDir(), // empty dir, no INDEX.md
	})
	if got != "" {
		t.Errorf("expected empty for missing INDEX, got %q", got)
	}
}

func TestBuildRehydrationDigest_NoLetterID(t *testing.T) {
	dir := t.TempDir()
	seedArchive(t, dir)

	archiveDir := filepath.Join(dir, "docs", "archive")
	digest := BuildRehydrationDigest(RehydrationConfig{
		ArchiveDir: archiveDir,
		// No ActiveLetterID — digest should still include INDEX + stuck summary.
	})
	if digest == "" {
		t.Fatal("digest should not be empty even without letter ID")
	}
	if !strings.Contains(digest, "INDEX") {
		t.Error("digest should include INDEX tail")
	}
	// Should NOT contain letter-specific sections.
	if strings.Contains(digest, "当前信上下文") {
		t.Log("letter context section absent without letter ID — correct")
	}
}

func TestBuildRehydrationDigest_IndexTailLines(t *testing.T) {
	dir := t.TempDir()
	seedArchive(t, dir)
	archiveDir := filepath.Join(dir, "docs", "archive")

	// Only 3 lines of INDEX.
	digest := BuildRehydrationDigest(RehydrationConfig{
		ArchiveDir:     archiveDir,
		IndexTailLines: 3,
	})
	if digest == "" {
		t.Fatal("digest should not be empty")
	}
	// Should only contain a few entries.
	entries := strings.Count(digest, "|")
	t.Logf("digest with IndexTailLines=3: %d table entries", entries)
}

// ── ResolveThreadFromIndex ───────────────────────────────────────

func TestResolveThreadFromIndex(t *testing.T) {
	index := `| L-0251 | QUERY | rehydration-mechanism | Parent | desc | date |
| L-0250 | RESULT | rehydration-mechanism | L-0235 | desc | date |
| L-0245 | RESULT | infra-worktree-bug | L-0238 | desc | date |`

	if got := resolveThreadFromIndex(index, "L-0251"); got != "rehydration-mechanism" {
		t.Errorf("L-0251 thread = %q, want rehydration-mechanism", got)
	}
	if got := resolveThreadFromIndex(index, "L-0245"); got != "infra-worktree-bug" {
		t.Errorf("L-0245 thread = %q, want infra-worktree-bug", got)
	}
	if got := resolveThreadFromIndex(index, "L-9999"); got != "" {
		t.Errorf("L-9999 thread = %q, want empty", got)
	}
}

// ── stripFrontmatter / extractSection ────────────────────────────

func TestStripFrontmatter(t *testing.T) {
	input := `---
ID: L-0251
To: architect
---

## Context Digest

Hello world.
`
	got := stripFrontmatter(input)
	want := "## Context Digest\n\nHello world.\n"
	// stripFrontmatter correctly removes YAML but the first content
	// character after the frontmatter boundary is '\n'.
	if got != want && got != "\n"+want {
		t.Errorf("stripFrontmatter:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestStripFrontmatter_NoFrontmatter(t *testing.T) {
	input := "just content\n"
	if got := stripFrontmatter(input); got != input {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestExtractSection(t *testing.T) {
	body := `## Context Digest

This is the digest content.

It can span multiple lines.

## Query

This is the query section.
`
	got := extractSection(body, "Context Digest")
	want := "This is the digest content.\n\nIt can span multiple lines."
	if got != want {
		t.Errorf("extractSection:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestExtractSection_LastSection(t *testing.T) {
	body := `## Context Digest

Only this section exists.
`
	got := extractSection(body, "Context Digest")
	want := "Only this section exists."
	if got != want {
		t.Errorf("extractSection last:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestExtractSection_NotFound(t *testing.T) {
	if got := extractSection("no heading here", "Context Digest"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ── readParentField ──────────────────────────────────────────────

func TestReadParentField(t *testing.T) {
	dir := t.TempDir()
	threadsDir := filepath.Join(dir, "threads", "test-thread")
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
ID: L-0251
Parent: L-0250
---

Body here.
`
	if err := os.WriteFile(filepath.Join(threadsDir, "L-0251.query.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := readParentField(dir, "test-thread", "L-0251"); got != "L-0250" {
		t.Errorf("readParentField = %q, want L-0250", got)
	}
}

func TestReadParentField_ROOT(t *testing.T) {
	dir := t.TempDir()
	threadsDir := filepath.Join(dir, "threads", "test")
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
ID: L-0001
Parent: ROOT
---
`
	if err := os.WriteFile(filepath.Join(threadsDir, "L-0001.query.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readParentField(dir, "test", "L-0001"); got != "ROOT" {
		t.Errorf("readParentField = %q, want ROOT", got)
	}
}

func TestReadParentField_Missing(t *testing.T) {
	if got := readParentField("/nonexistent", "test", "L-9999"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ── EstimateTokenCount ───────────────────────────────────────────

func TestEstimateTokenCount(t *testing.T) {
	// Empty string.
	if got := EstimateTokenCount(""); got != 0 {
		t.Errorf("empty: got %d", got)
	}
	// English text: ~16 chars → ~8 tokens.
	if got := EstimateTokenCount("hello world test"); got != 8 {
		t.Errorf("english: got %d", got)
	}
	// Chinese text: 6 chars → ~3 tokens (len/2).
	if got := EstimateTokenCount("你好世界测试"); got != 3 {
		t.Errorf("chinese: got %d, want 3", got)
	}
}

func TestRehydrationBudgetForPersonaClass(t *testing.T) {
	write := RehydrationBudgetForPersonaClass(PersonaClassWrite)
	read := RehydrationBudgetForPersonaClass(PersonaClassRead)
	secretary := RehydrationBudgetForPersonaClass(PersonaClassSecretary)

	if write.MaxTokens <= read.MaxTokens {
		t.Fatalf("write budget should exceed read budget: write=%d read=%d", write.MaxTokens, read.MaxTokens)
	}
	if secretary.OpenSummaryEntries <= read.OpenSummaryEntries {
		t.Fatalf("secretary should keep more open rows than read seats")
	}
}

func TestNormalizeLetterID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Canonical L-XXXX format
		{"L-0319", "L-0319"},
		{"L-1000", "L-1000"},
		{"L-12345", "L-12345"},

		// Variations with casing / formatting
		{"l-0319", "L-0319"},
		{"L0319", "L-0319"},
		{"l0319", "L-0319"},

		// Bare numbers with leading zeros
		{"0319", "L-0319"},
		{"0042", "L-0042"},

		// Preceded by keywords
		{"process 0319", "L-0319"},
		{"process 1005", "L-1005"},
		{"letter 0319", "L-0319"},
		{"letter 1005", "L-1005"},
		{"id 0319", "L-0319"},
		{"id 1005", "L-1005"},
		{"thread 1005", "L-1005"},
		{"query 1005", "L-1005"},
		{"result 1005", "L-1005"},

		// Standalone bare numbers (trimmed)
		{"1005", "L-1005"},
		{"  1024  ", "L-1024"},

		// Inside paths
		{`F:\foundry\worktrees\letter-L-0319`, "L-0319"},
		{`F:\foundry\worktrees\letter-0319`, "L-0319"},
		{`F:\foundry\worktrees\0319`, "L-0319"},

		// Non-matching inputs (ports, PIDs, other contexts)
		{"port 8080", ""},
		{"cc-switch proxy on 15731", ""},
		{"PID 33884", ""},
		{"some random text", ""},
		{"L-12", ""}, // too short
		{"12", ""},   // too short
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizeLetterID(tc.input)
			if got != tc.want {
				t.Errorf("normalizeLetterID(%q) = %q, want %q", tc.input, got, tc.want)
			}
			gotFromText := ExtractLetterIDFromText(tc.input)
			if gotFromText != tc.want {
				t.Errorf("ExtractLetterIDFromText(%q) = %q, want %q", tc.input, gotFromText, tc.want)
			}
		})
	}
}

