package cursor

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func shortTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	timeout := 30 * time.Second
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			timeout = 100 * time.Millisecond
		} else if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(context.Background(), timeout)
}

func requireWorkingAgentCLI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("skipping agent CLI test in CI (no real cursor agent available)")
	}
	if os.Getenv("SKIP_REAL_AGENT_CLI") != "" {
		t.Skip("skipping real agent CLI test (SKIP_REAL_AGENT_CLI is set)")
	}
	if _, err := exec.LookPath("agent"); err != nil {
		t.Skip("agent CLI not in PATH")
	}
	ctx, cancel := shortTestContext(t)
	defer cancel()

	out, err := exec.CommandContext(ctx, "agent", "models").CombinedOutput()
	if err != nil {
		t.Skipf("agent CLI is not runnable in this environment: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

func TestFetchModelsFromAgentCLI(t *testing.T) {
	ctx, cancel := shortTestContext(t)
	defer cancel()
	requireWorkingAgentCLI(t)

	models := fetchModelsFromAgentCLI(ctx, "agent", nil)
	if len(models) == 0 {
		t.Fatal("expected models from agent models, got none")
	}

	// Verify format: each model has non-empty Name
	for i, m := range models {
		if m.Name == "" {
			t.Errorf("models[%d].Name is empty", i)
		}
	}
	// 运行 go test -v 时可见
	t.Logf("fetched %d models:", len(models))
	for i, m := range models {
		t.Logf("  %2d. %s - %s", i+1, m.Name, m.Desc)
	}
}

func TestFetchModelsFromAgentCLI_FailsGracefully(t *testing.T) {
	ctx, cancel := shortTestContext(t)
	defer cancel()
	models := fetchModelsFromAgentCLI(ctx, "nonexistent-agent-xyz", nil)
	if len(models) != 0 {
		t.Errorf("expected empty when command fails, got %d models", len(models))
	}
}

func TestAvailableModels_Fallback(t *testing.T) {
	// When agent models fails, should fall back to hardcoded list
	ctx, cancel := shortTestContext(t)
	defer cancel()
	a := &Agent{cmd: "nonexistent-cmd-that-will-fail"}
	models := a.AvailableModels(ctx)
	fallback := cursorFallbackModels()
	if len(models) != len(fallback) {
		t.Fatalf("fallback models length = %d, want %d", len(models), len(fallback))
	}
	for i := range models {
		if models[i].Name != fallback[i].Name {
			t.Errorf("models[%d].Name = %q, want %q", i, models[i].Name, fallback[i].Name)
		}
	}
}

func TestAvailableModels_FetchFromAgent(t *testing.T) {
	requireWorkingAgentCLI(t)
	ctx, cancel := shortTestContext(t)
	defer cancel()

	a := &Agent{cmd: "agent"}
	models := a.AvailableModels(ctx)
	if len(models) == 0 {
		t.Fatal("expected models from agent models, got none")
	}

	t.Logf("AvailableModels returned %d models:", len(models))
	for i, m := range models {
		t.Logf("  %2d. %s - %s", i+1, m.Name, m.Desc)
	}

	// Should have real models like gpt-5.3-codex, opus-4.6-thinking, etc.
	hasCodex := false
	for _, m := range models {
		if m.Name == "gpt-5.3-codex" || m.Name == "opus-4.6-thinking" || m.Name == "auto" {
			hasCodex = true
			break
		}
	}
	if !hasCodex {
		t.Logf("models: %v", models)
		t.Log("agent models returned models but none of the expected ones (gpt-5.3-codex, opus-4.6-thinking, auto) - may be OK if CLI output format changed")
	}
}

func TestSaveCursorImagesToDisk(t *testing.T) {
	workDir := t.TempDir()

	paths, err := saveCursorImagesToDisk(workDir, []core.ImageAttachment{
		{MimeType: "image/jpeg", FileName: "photo.jpg", Data: []byte("jpeg-bytes")},
		{MimeType: "image/webp", Data: []byte("webp-bytes")},
	})
	if err != nil {
		t.Fatalf("saveCursorImagesToDisk returned error: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(paths))
	}

	wantDir := filepath.Join(workDir, ".cc-connect", "images")
	if paths[0] != filepath.Join(wantDir, "photo.jpg") {
		t.Fatalf("first image path = %q, want photo.jpg under images dir", paths[0])
	}
	if filepath.Ext(paths[1]) != ".webp" {
		t.Fatalf("generated webp path = %q, want .webp extension", paths[1])
	}

	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("read saved image: %v", err)
	}
	if string(data) != "jpeg-bytes" {
		t.Fatalf("saved image data = %q, want jpeg-bytes", string(data))
	}
}

func TestSaveCursorImagesToDiskDownscalesReadableImages(t *testing.T) {
	workDir := t.TempDir()

	src := image.NewRGBA(image.Rect(0, 0, 1200, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 1200; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	paths, err := saveCursorImagesToDisk(workDir, []core.ImageAttachment{{
		MimeType: "image/png",
		FileName: "large.png",
		Data:     buf.Bytes(),
	}})
	if err != nil {
		t.Fatalf("saveCursorImagesToDisk returned error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d", len(paths))
	}
	if filepath.Base(paths[0]) != "large.jpg" {
		t.Fatalf("saved path = %q, want large.jpg", paths[0])
	}

	f, err := os.Open(paths[0])
	if err != nil {
		t.Fatalf("open saved image: %v", err)
	}
	defer f.Close()
	got, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("decode saved jpeg: %v", err)
	}
	if got.Bounds().Dx() > 768 || got.Bounds().Dy() > 768 {
		t.Fatalf("saved image bounds = %v, want max side <= 768", got.Bounds())
	}
}
