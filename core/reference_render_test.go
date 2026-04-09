package core

import (
	"os"
	"path/filepath"
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
