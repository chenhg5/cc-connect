package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDispatchBlockStrict(t *testing.T) {
	req, ok, err := parseDispatchBlock(`[DISPATCH]
To: dev-swift
Letter: L-0130
Thread: topology-reframe
Path: F:\nexus\docs\archive\threads\topology-reframe\L-0130.query.md`)
	if err != nil {
		t.Fatalf("parseDispatchBlock() error = %v", err)
	}
	if !ok {
		t.Fatal("parseDispatchBlock() ok = false, want true")
	}
	if req.To != "dev-swift" || req.Letter != "L-0130" || req.Thread != "topology-reframe" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestValidateDispatchArchiveAndResultDetection(t *testing.T) {
	root := t.TempDir()
	threadDir := filepath.Join(root, "threads", "topology-reframe")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(threadDir, "L-0130.query.md")
	query := `---
ID: L-0130
Thread: topology-reframe
Type: QUERY
---

## Query
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}

	resultPath, indexPath, err := validateDispatchArchive(dispatchRequest{
		To:     "dev-swift",
		Letter: "L-0130",
		Thread: "topology-reframe",
		Path:   queryPath,
	})
	if err != nil {
		t.Fatalf("validateDispatchArchive() error = %v", err)
	}
	if resultPath != filepath.Join(threadDir, "L-0130.result.md") {
		t.Fatalf("resultPath = %q", resultPath)
	}
	if indexPath != filepath.Join(root, "INDEX.md") {
		t.Fatalf("indexPath = %q", indexPath)
	}
	if dispatchResultReady(DispatchExpectation{Letter: "L-0130", Thread: "topology-reframe", ResultPath: resultPath, IndexPath: indexPath}) {
		t.Fatal("dispatchResultReady() = true before result exists")
	}
	if err := os.WriteFile(resultPath, []byte("result"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("| L-0130 | RESULT | topology-reframe | L-0128 | Done | 07-03 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !dispatchResultReady(DispatchExpectation{Letter: "L-0130", Thread: "topology-reframe", ResultPath: resultPath, IndexPath: indexPath}) {
		t.Fatal("dispatchResultReady() = false after result and INDEX row")
	}
}
