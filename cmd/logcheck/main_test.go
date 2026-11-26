package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRejectsDirectPayloadsAndAllowsSafeMetadata(t *testing.T) {
	root := t.TempDir()
	source := `package fixture
import ("fmt"; "log/slog")
func f(body []byte, raw []byte, content string, payload string) {
 slog.Info("response", "body", body)
 slog.Debug("response", slog.Any("raw", raw))
 _ = fmt.Errorf("upstream: %s", string(body))
	_ = fmt.Errorf("direct: %s", body)
 _ = fmt.Errorf("data: " + content)
 slog.Warn("response", "formatted", fmt.Sprintf("%s", payload))
 slog.Info("response", "bytes", len(body), "status", 500)
 slog.Info("response", "source", safeHost(raw))
 _ = fmt.Errorf("wrong type %T", payload)
}
func safeHost([]byte) string { return "example.com" }
`
	if err := os.WriteFile(filepath.Join(root, "fixture.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	findings, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 6 {
		t.Fatalf("findings = %+v, want six unsafe uses", findings)
	}
	for _, f := range findings {
		if !strings.Contains(f.file, "fixture.go") || f.line < 1 {
			t.Fatalf("bad finding: %+v", f)
		}
	}
}

func TestScanRejectsConcatenatedLogPayload(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.go"), []byte(`package fixture
import "log"
func f(text string) { log.Printf("message: %s", text) }
`), 0600); err != nil {
		t.Fatal(err)
	}
	findings, err := scan(root)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings = %+v, err = %v", findings, err)
	}
}
