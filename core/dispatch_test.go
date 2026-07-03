package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

type mockTaskTopicPlatform struct {
	stubMediaPlatform
	createTaskTopicFunc func(ctx context.Context, dashboardSessionKey, title, content string) (*TaskTopic, error)
	reconstructFunc     func(sessionKey string) (any, error)
}

func (m *mockTaskTopicPlatform) CreateTaskTopic(ctx context.Context, dashboardSessionKey, title, content string) (*TaskTopic, error) {
	if m.createTaskTopicFunc != nil {
		return m.createTaskTopicFunc(ctx, dashboardSessionKey, title, content)
	}
	return nil, nil
}

func (m *mockTaskTopicPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	if m.reconstructFunc != nil {
		return m.reconstructFunc(sessionKey)
	}
	return nil, nil
}

func TestExecuteDispatchFallback(t *testing.T) {
	root := t.TempDir()
	threadDir := filepath.Join(root, "threads", "topology-reframe")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(threadDir, "L-0154.query.md")
	query := `---
ID: L-0154
Thread: topology-reframe
Type: QUERY
---

## Query
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &mockTaskTopicPlatform{
		stubMediaPlatform: stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}},
		createTaskTopicFunc: func(ctx context.Context, dashboardSessionKey, title, content string) (*TaskTopic, error) {
			return nil, fmt.Errorf("not enough rights to create a topic")
		},
		reconstructFunc: func(sessionKey string) (any, error) {
			return "reconstructed-ctx", nil
		},
	}

	targetEngine := NewEngine("dev-pro", &stubAgent{}, []Platform{p}, "", LangEnglish)
	targetEngine.SetWorkspacePattern(filepath.Join(root, "worktrees", "task-{{THREAD_ID}}"))

	sourceEngine := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sourceEngine.dataDir = root
	sourceEngine.relayManager = NewRelayManager(root)
	sourceEngine.relayManager.RegisterEngine("dev-pro", targetEngine)
	sourceEngine.relayManager.RegisterEngine("secretary-seat", sourceEngine)

	sourceEngine.configureDispatch(DispatchConfig{
		Enabled:             true,
		SourceProject:       "secretary-seat",
		DashboardSessionKey: "telegram:-1003917051393:7664413698:0",
		PollInterval:        1 * time.Second,
	})

	req := dispatchRequest{
		To:     "dev-pro",
		Letter: "L-0154",
		Thread: "topology-reframe",
		Path:   queryPath,
	}

	receipt, err := sourceEngine.executeDispatch(p, "telegram:-1003917051393:7664413698:0", req)
	if err != nil {
		t.Fatalf("executeDispatch failed: %v", err)
	}

	if receipt != "✅ Dispatched L-0154 to dev-pro" {
		t.Errorf("unexpected receipt: %q", receipt)
	}

	open, err := sourceEngine.dispatchStore.listOpen()
	if err != nil {
		t.Fatalf("listOpen failed: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open expectation, got %d", len(open))
	}
	exp := open[0]
	if exp.Letter != "L-0154" || exp.TopicID != "" || exp.TopicSessionKey != "telegram:-1003917051393:0154:0" {
		t.Errorf("unexpected expectation: %+v", exp)
	}
}
