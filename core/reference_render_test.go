package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTransformLocalReferences_DisabledWithoutNormalizeAgents(t *testing.T) {
	cfg := ReferenceRenderCfg{
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "basename",
		MarkerStyle:     "none",
		EnclosureStyle:  "none",
	}
	input := "See /root/code/demo/src/app.ts:42"
	got := TransformLocalReferences(input, cfg, "codex", "feishu", "/root/code/demo")
	if got != input {
		t.Fatalf("TransformLocalReferences() = %q, want unchanged %q", got, input)
	}
}

func TestTransformLocalReferences_UsesAllScopes(t *testing.T) {
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"all"},
		RenderPlatforms: []string{"all"},
		DisplayPath:     "basename",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	got := TransformLocalReferences("See /root/code/demo/src/app.ts:42", cfg, "codex", "feishu", "/root/code/demo")
	if !strings.Contains(got, "📄 `app.ts:42`") {
		t.Fatalf("TransformLocalReferences() = %q, want rendered basename reference", got)
	}
}

func TestTransformLocalReferences_PreservesWebMarkdownLinks(t *testing.T) {
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "basename",
		MarkerStyle:     "none",
		EnclosureStyle:  "none",
	}
	input := "Docs: [OpenAI](https://openai.com/) and [app.ts](/root/code/demo/src/app.ts#L42)"
	got := TransformLocalReferences(input, cfg, "codex", "feishu", "/root/code/demo")
	if !strings.Contains(got, "[OpenAI](https://openai.com/)") {
		t.Fatalf("TransformLocalReferences() = %q, want web link preserved", got)
	}
	if !strings.Contains(got, "app.ts#L42") {
		t.Fatalf("TransformLocalReferences() = %q, want local hash-line reference rendered", got)
	}
}

func TestTransformLocalReferences_PreservesInlineCodePathRange(t *testing.T) {
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"claudecode"},
		RenderPlatforms: []string{"weixin"},
		DisplayPath:     "dirname_basename",
		MarkerStyle:     "ascii",
		EnclosureStyle:  "code",
	}
	got := TransformLocalReferences("Inspect `/root/.claude/settings.json:5-10` next.", cfg, "claudecode", "weixin", "/root")
	want := "[FILE] `.claude/settings.json:5-10`"
	if !strings.Contains(got, want) {
		t.Fatalf("TransformLocalReferences() = %q, want substring %q", got, want)
	}
}

func TestTransformLocalReferences_PreservesWebMarkdownLinksAfterInlineCodeReference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TransformLocalReferences path handling assumes Unix separators")
	}
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"claudecode"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	input := "`/root/code/.claude/settings.json:5-10`\n[OpenAI](https://openai.com/)"
	got := TransformLocalReferences(input, cfg, "claudecode", "feishu", "/root/code")
	want := "📄 `.claude/settings.json:5-10`\n[OpenAI](https://openai.com/)"
	if got != want {
		t.Fatalf("TransformLocalReferences() = %q, want %q", got, want)
	}
}

func TestTransformLocalReferences_SmartDisplayFallsBackOnBasenameCollision(t *testing.T) {
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "smart",
		MarkerStyle:     "none",
		EnclosureStyle:  "none",
	}
	input := "Compare /root/code/demo/src/app.ts and /root/code/demo/tests/app.ts"
	got := TransformLocalReferences(input, cfg, "codex", "feishu", "/root/code/demo")
	if !strings.Contains(got, "src/app.ts") || !strings.Contains(got, "tests/app.ts") {
		t.Fatalf("TransformLocalReferences() = %q, want dirname+basename for both colliding refs", got)
	}
}

func TestTransformLocalReferences_RelativeDisplayUsesWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TransformLocalReferences path handling assumes Unix separators")
	}
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	got := TransformLocalReferences("Look at /root/code/demo/src/app.ts:42:7", cfg, "codex", "feishu", "/root/code/demo")
	want := "📄 `src/app.ts:42:7`"
	if !strings.Contains(got, want) {
		t.Fatalf("TransformLocalReferences() = %q, want substring %q", got, want)
	}
}

func TestTransformLocalReferences_RelativeInputIsNotSplitByAbsoluteMatcher(t *testing.T) {
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	input := "See lean-steward/src/lean_topo_steward/prompting/instructions/global_instructions.py:42"
	got := TransformLocalReferences(input, cfg, "codex", "feishu", "/root/code")
	want := "See 📄 `lean-steward/src/lean_topo_steward/prompting/instructions/global_instructions.py:42`"
	if got != want {
		t.Fatalf("TransformLocalReferences() = %q, want %q", got, want)
	}
}

func TestTransformLocalReferences_ChineseListSeparatorsDoNotMergeCandidates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TransformLocalReferences path handling assumes Unix separators")
	}
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "demo-repo", "README")
	profileDir := filepath.Join(workspace, "demo-repo", "src", "components", "profile")
	profileExtDir := filepath.Join(workspace, "demo-repo", "src", "components", "profile.ts")
	specDir := filepath.Join(workspace, "demo-repo", "docs", "spec.v1")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(file dir) error: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("readme"), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	for _, dir := range []string{profileDir, profileExtDir, specDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error: %v", dir, err)
		}
	}

	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"claudecode"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	input := "第 1 步：正在处理路径 demo-repo/README、" + profileDir + "、" + profileExtDir + "、" + specDir + "。"
	got := TransformLocalReferences(input, cfg, "claudecode", "feishu", workspace)
	want := "第 1 步：正在处理路径 📄 `demo-repo/README`、📁 `demo-repo/src/components/profile/`、📁 `demo-repo/src/components/profile.ts/`、📁 `demo-repo/docs/spec.v1/`。"
	if got != want {
		t.Fatalf("TransformLocalReferences() = %q, want %q", got, want)
	}
}

func TestTransformLocalReferences_ExistingDirectoryWithoutTrailingSlashIsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TransformLocalReferences path handling assumes Unix separators")
	}
	workspace := t.TempDir()
	dirPath := filepath.Join(workspace, "demo-repo", "src", "components")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}

	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	got := TransformLocalReferences("Dir "+dirPath, cfg, "codex", "feishu", workspace)
	want := "Dir 📁 `demo-repo/src/components/`"
	if got != want {
		t.Fatalf("TransformLocalReferences() = %q, want %q", got, want)
	}
}

func TestTransformLocalReferences_WorkspaceRootDisplaysAsRelativeRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TransformLocalReferences path handling assumes Unix separators")
	}
	workspace := t.TempDir()
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	got := TransformLocalReferences("Root "+workspace, cfg, "codex", "feishu", workspace)
	want := "Root 📁 `./`"
	if got != want {
		t.Fatalf("TransformLocalReferences() = %q, want %q", got, want)
	}
}

func TestTransformLocalReferences_UnknownNoExtPathKeepsNoMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TransformLocalReferences path handling assumes Unix separators")
	}
	workspace := t.TempDir()
	unknown := filepath.Join(workspace, "mysterypath")
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "relative",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	got := TransformLocalReferences("Unknown "+unknown, cfg, "codex", "feishu", workspace)
	want := "Unknown `mysterypath`"
	if got != want {
		t.Fatalf("TransformLocalReferences() = %q, want %q", got, want)
	}
}

func TestTransformLocalReferences_PreservesSlashCommandsAndStillRendersAbsolutePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TransformLocalReferences path handling assumes Unix separators")
	}
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"codex"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "basename",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	input := strings.Join([]string{
		"/feedback update notices should use admin_from",
		"/show /tmp/output.log",
		"Run `/new investigate the failure` and then `/upgrade`.",
		"Custom /deploy+prod now; Skill `/release_notes summarize`.",
		"Inspect /Users/name/project/file.go:42 and /tmp/output.log next.",
	}, "\n")

	got := TransformLocalReferences(input, cfg, "codex", "feishu", "/Users/name/project")

	for _, command := range []string{
		"/feedback update notices should use admin_from",
		"/show /tmp/output.log",
		"`/new investigate the failure`",
		"`/upgrade`",
		"/deploy+prod now",
		"`/release_notes summarize`",
	} {
		if !strings.Contains(got, command) {
			t.Errorf("TransformLocalReferences() lost slash command %q: %q", command, got)
		}
	}
	for _, renderedPath := range []string{"📄 `file.go:42`", "📄 `output.log`"} {
		if !strings.Contains(got, renderedPath) {
			t.Errorf("TransformLocalReferences() did not render real path %q: %q", renderedPath, got)
		}
	}
}

func TestParseUserLocalReference_StillAcceptsSingleSegmentAbsolutePath(t *testing.T) {
	ref, err := parseUserLocalReference("/tmp", "/")
	if err != nil {
		t.Fatalf("parseUserLocalReference(/tmp) error = %v", err)
	}
	if ref.pathOriginal != "/tmp" {
		t.Fatalf("parseUserLocalReference(/tmp) path = %q", ref.pathOriginal)
	}
}

func TestTransformLocalReferences_PreservesNumbersAndDates(t *testing.T) {
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"all"},
		RenderPlatforms: []string{"all"},
		DisplayPath:     "smart",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	const table = "\n| 日期 | A | B | C | D | 合计 |\n| --- | --- | --- | --- | --- | --- |\n| 9/28 | 23.34 | 0.20 | 59.8 | 5.5 | 170,851.78 |"
	cases := []struct{ name, input, want string }{
		{"bare", "23.34 0.20 59.8 5.5 9/28 170,851.78", "23.34 0.20 59.8 5.5 9/28 170,851.78"},
		{"inline_code", "`23.34` `0.20` `9/28` `170,851.78`", "`23.34` `0.20` `9/28` `170,851.78`"},
		{"table_after_file", "参考 `report.md`" + table, "参考 📄 `report.md`" + table},
		{"signed_and_percent", "-23.34 +0.20 59.8% -5.5% 1.25e-3", "-23.34 +0.20 59.8% -5.5% 1.25e-3"},
		{"dates", "9/28 2026/9/28 9/28/2026", "9/28 2026/9/28 9/28/2026"},
		{"sentence", "Amount 23.34. Date 9/28.", "Amount 23.34. Date 9/28."},
		{"compact_table", "|9/28|23.34|170,851.78|", "|9/28|23.34|170,851.78|"},
	}
	for _, agent := range []string{"claudecode", "codex"} {
		for _, platform := range []string{"feishu", "weixin"} {
			for _, tc := range cases {
				t.Run(agent+"/"+platform+"/"+tc.name, func(t *testing.T) {
					if got := TransformLocalReferences(tc.input, cfg, agent, platform, t.TempDir()); got != tc.want {
						t.Fatalf("rendered = %q, want %q", got, tc.want)
					}
				})
			}
		}
	}
}

func TestTransformLocalReferences_KeepsDistinctReferencesAcrossCodeSpans(t *testing.T) {
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"claudecode"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "basename",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	cases := []struct{ name, input, want string }{
		{"files", "`a.go` b.go c.go d.go", "📄 `a.go` 📄 `b.go` 📄 `c.go` 📄 `d.go`"},
		{"urls", "`a.go` https://example.com/one https://example.com/two", "📄 `a.go` https://example.com/one https://example.com/two"},
		{"markdown_links", "`a.go` [One](https://example.com/one) [Two](https://example.com/two)", "📄 `a.go` [One](https://example.com/one) [Two](https://example.com/two)"},
		{"ordinary_code_boundary", "a.go `text` b.go c.go", "📄 `a.go` `text` 📄 `b.go` 📄 `c.go`"},
		{"multiple_boundaries", "`a.go` b.go c.go `d.go` e.go f.go", "📄 `a.go` 📄 `b.go` 📄 `c.go` 📄 `d.go` 📄 `e.go` 📄 `f.go`"},
		{"mixed", "`a.go` [One](https://example.com/one) b.go https://example.com/two c.go", "📄 `a.go` [One](https://example.com/one) 📄 `b.go` https://example.com/two 📄 `c.go`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TransformLocalReferences(tc.input, cfg, "claudecode", "feishu", t.TempDir()); got != tc.want {
				t.Fatalf("rendered = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTransformLocalReferences_PreservesExplicitNumericFileReferences(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "23.34"), []byte("numeric filename"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := ReferenceRenderCfg{
		NormalizeAgents: []string{"claudecode"},
		RenderPlatforms: []string{"feishu"},
		DisplayPath:     "basename",
		MarkerStyle:     "emoji",
		EnclosureStyle:  "code",
	}
	input := "23.34 `23.34` ./23.34 `./23.34` [file](23.34)"
	want := "23.34 `23.34` 📄 `23.34` 📄 `23.34` 📄 `23.34`"
	if got := TransformLocalReferences(input, cfg, "claudecode", "feishu", workspace); got != want {
		t.Fatalf("rendered = %q, want %q", got, want)
	}
	if ref, err := parseUserLocalReference("23.34", workspace); err != nil || ref.pathOriginal != "23.34" {
		t.Fatalf("explicit user reference = %#v, %v", ref, err)
	}
}
