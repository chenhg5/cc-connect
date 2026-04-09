package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildReferenceViewRequest_ModeSelection(t *testing.T) {
	ws := t.TempDir()
	file := filepath.Join(ws, "svc", "handler.go")
	dir := filepath.Join(ws, "docs", "spec.v1")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		raw  string
		mode referenceViewMode
	}{
		{name: "file", raw: file, mode: referenceViewFileHead},
		{name: "line", raw: file + ":12", mode: referenceViewContext},
		{name: "linecol", raw: file + ":12:2", mode: referenceViewContext},
		{name: "range", raw: file + ":8-17", mode: referenceViewRange},
		{name: "hash", raw: file + "#L12", mode: referenceViewContext},
		{name: "markdown", raw: "[handler.go](" + file + "#L12)", mode: referenceViewContext},
		{name: "dir", raw: dir, mode: referenceViewDir},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := buildReferenceViewRequest(tc.raw, ws)
			if err != nil {
				t.Fatalf("buildReferenceViewRequest error: %v", err)
			}
			if req.Mode != tc.mode {
				t.Fatalf("mode = %q, want %q", req.Mode, tc.mode)
			}
		})
	}
}

func TestBuildReferenceViewRequest_DirectoryWithLocationFails(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, "docs", "spec.v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := buildReferenceViewRequest(dir+":12", ws); err == nil {
		t.Fatal("expected error for directory location reference")
	}
}

func TestRenderReferenceView_FileHeadAndContext(t *testing.T) {
	ws := t.TempDir()
	file := filepath.Join(ws, "svc", "handler.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"package svc",
		"",
		"func one() {}",
		"func two() {}",
		"func three() {}",
	}, "\n")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	headReq, err := buildReferenceViewRequest(file, ws)
	if err != nil {
		t.Fatal(err)
	}
	head, err := renderReferenceView(headReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(head, "```go") {
		t.Fatalf("head output = %q, want go code fence", head)
	}
	if !strings.Contains(head, "package svc") {
		t.Fatalf("head output = %q, want file content", head)
	}

	ctxReq, err := buildReferenceViewRequest(file+":4", ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := renderReferenceView(ctxReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx, "func two() {}") {
		t.Fatalf("context output = %q, want nearby line", ctx)
	}
}

func TestRenderReferenceView_DirectoryList(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, "docs", "spec.v1")
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	req, err := buildReferenceViewRequest(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderReferenceView(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "📁 docs/spec.v1/") {
		t.Fatalf("directory output = %q, want title", out)
	}
	if !strings.Contains(out, "- alpha.md") || !strings.Contains(out, "- subdir/") {
		t.Fatalf("directory output = %q, want entries", out)
	}
}
