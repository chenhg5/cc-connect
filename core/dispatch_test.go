package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestDispatchStoreRecordsResultProvenanceForTargetOnly(t *testing.T) {
	store := newDispatchStore(t.TempDir())
	if err := store.upsert(DispatchExpectation{Letter: "L-0430", Thread: "thread", To: "dev-pro"}); err != nil {
		t.Fatal(err)
	}
	if err := store.recordResultProvenance("L-0430", "secretary-seat", "wrong-session", `F:\wrong.jsonl`); err != nil {
		t.Fatal(err)
	}
	if id, path := store.resultProvenance("L-0430"); id != "" || path != "" {
		t.Fatalf("non-target provenance = (%q, %q), want empty", id, path)
	}
	if err := store.recordResultProvenance("L-0430", "dev-pro", "producer-session", `F:\producer.jsonl`); err != nil {
		t.Fatal(err)
	}
	if id, path := store.resultProvenance("L-0430"); id != "producer-session" || path != `F:\producer.jsonl` {
		t.Fatalf("provenance = (%q, %q)", id, path)
	}
}

func TestParseDispatchBlockRequiresStandaloneCompleteCommand(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantReq dispatchRequest
		wantOk  bool
		wantErr bool
	}{
		{
			name:    "normal explanation mentions dispatch marker",
			content: "A [DISPATCH] command requires all four fields; this is only an explanation.",
			wantReq: dispatchRequest{},
			wantOk:  false,
			wantErr: false,
		},
		{
			name:    "incomplete template is ordinary text",
			content: "[DISPATCH]\nTo: dev-pro\nLetter: L-0154\nThread: topology-reframe",
			wantReq: dispatchRequest{},
			wantOk:  false,
			wantErr: false,
		},
		{
			name:    "fenced example with explanation is ordinary text",
			content: "Use this format after approval:\n```\n[DISPATCH]\nTo: dev-pro\nLetter: L-0154\nThread: topology-reframe\nPath: F:\\nexus\\docs\\archive\\threads\\topology-reframe\\L-0154.query.md\n```",
			wantReq: dispatchRequest{},
			wantOk:  false,
			wantErr: false,
		},
		{
			name:    "quoted example is ordinary text",
			content: "> [DISPATCH]\n> To: dev-pro\n> Letter: L-0154\n> Thread: topology-reframe\n> Path: F:\\nexus\\docs\\archive\\threads\\topology-reframe\\L-0154.query.md",
			wantReq: dispatchRequest{},
			wantOk:  false,
			wantErr: false,
		},
		{
			name:    "natural language preamble is ordinary text",
			content: "Dispatch this now:\n[DISPATCH]\nTo: dev-pro\nLetter: L-0154\nThread: topology-reframe\nPath: F:\\nexus\\docs\\archive\\threads\\topology-reframe\\L-0154.query.md",
			wantReq: dispatchRequest{},
			wantOk:  false,
			wantErr: false,
		},
		{
			name:    "standalone complete command",
			content: "[DISPATCH]\nTo: dev-pro\nLetter: L-0154\nThread: topology-reframe\nPath: F:\\nexus\\docs\\archive\\threads\\topology-reframe\\L-0154.query.md",
			wantReq: dispatchRequest{
				To:     "dev-pro",
				Letter: "L-0154",
				Thread: "topology-reframe",
				Path:   `F:\nexus\docs\archive\threads\topology-reframe\L-0154.query.md`,
			},
			wantOk:  true,
			wantErr: false,
		},
		{
			name:    "standalone fenced command",
			content: "```\n[DISPATCH]\nTo: dev-pro\nLetter: L-0154\nThread: topology-reframe\nPath: F:\\nexus\\docs\\archive\\threads\\topology-reframe\\L-0154.query.md\n```",
			wantReq: dispatchRequest{
				To:     "dev-pro",
				Letter: "L-0154",
				Thread: "topology-reframe",
				Path:   `F:\nexus\docs\archive\threads\topology-reframe\L-0154.query.md`,
			},
			wantOk:  true,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, ok, err := parseDispatchBlock(tt.content)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDispatchBlock() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if ok != tt.wantOk {
				t.Fatalf("parseDispatchBlock() ok = %v, wantOk = %v", ok, tt.wantOk)
			}
			if ok && !tt.wantErr {
				if req.To != tt.wantReq.To || req.Letter != tt.wantReq.Letter ||
					req.Thread != tt.wantReq.Thread || req.Path != tt.wantReq.Path {
					t.Fatalf("unexpected request: %+v, want: %+v", req, tt.wantReq)
				}
			}
		})
	}
}

func TestMaybeHandleDispatchBlockRejectsWrongSourceProject(t *testing.T) {
	e := NewEngine("other-project", &stubAgent{}, nil, "", LangEnglish)
	e.configureDispatch(DispatchConfig{Enabled: true, SourceProject: "secretary-seat"})

	handled, replacement := e.maybeHandleDispatchBlock(nil, "telegram:chat:user", `[DISPATCH]
To: dev-pro
Letter: L-0154
Thread: topology-reframe
Path: F:\nexus\docs\archive\threads\topology-reframe\L-0154.query.md`)
	if !handled {
		t.Fatal("complete command from a non-source project was not handled")
	}
	if !strings.Contains(replacement, "not allowed to emit") {
		t.Fatalf("replacement = %q, want source-project rejection", replacement)
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
	if !dispatchResultReady(DispatchExpectation{Letter: "L-0130", Thread: "topology-reframe", ResultPath: resultPath, IndexPath: indexPath}) {
		t.Fatal("dispatchResultReady() = false after result exists (INDEX row should not be required)")
	}
}

func TestDispatchResultReadyIsDir(t *testing.T) {
	root := t.TempDir()
	resultPath := filepath.Join(root, "L-0999.result.md")
	indexPath := filepath.Join(root, "INDEX.md")

	exp := DispatchExpectation{
		Letter:     "L-0999",
		Thread:     "test-thread",
		ResultPath: resultPath,
		IndexPath:  indexPath,
	}

	if dispatchResultReady(exp) {
		t.Fatal("dispatchResultReady() = true before result.md exists")
	}

	// Create resultPath as a directory instead of a file
	if err := os.Mkdir(resultPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if dispatchResultReady(exp) {
		t.Fatal("dispatchResultReady() = true when resultPath is a directory")
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

func TestIsPrivateTelegramSessionKey(t *testing.T) {
	tests := []struct {
		sessionKey string
		want       bool
	}{
		{"telegram:-1003917051393:7664413698", false},   // group (negative chat ID)
		{"telegram:-1003917051393:7664413698:0", false}, // group with thread
		{"telegram:7664413698:7664413698", true},        // DM (positive chat ID == user ID)
		{"telegram:7664413698", true},                   // bare DM chat ID
		{"feishu:some-chat", false},                     // non-telegram platform
		{"telegram:not-a-number:1", false},              // malformed
	}
	for _, tt := range tests {
		if got := isPrivateTelegramSessionKey(tt.sessionKey); got != tt.want {
			t.Errorf("isPrivateTelegramSessionKey(%q) = %v, want %v", tt.sessionKey, got, tt.want)
		}
	}
}

func TestResolveDashboardSessionKey(t *testing.T) {
	e := NewEngine("secretary-seat", &stubAgent{}, nil, "", LangEnglish)

	// DynamicDashboard disabled (default): always static, even from a DM source.
	e.configureDispatch(DispatchConfig{
		Enabled:             true,
		DashboardSessionKey: "telegram:-1003917051393:7664413698",
	})
	if got := e.resolveDashboardSessionKey("telegram:7664413698:7664413698"); got != "telegram:-1003917051393:7664413698" {
		t.Errorf("static mode: got %q, want General group", got)
	}

	// DynamicDashboard enabled + DM source: routes to the DM.
	e.dispatchConfig.DynamicDashboard = true
	dmKey := "telegram:7664413698:7664413698"
	if got := e.resolveDashboardSessionKey(dmKey); got != dmKey {
		t.Errorf("dynamic mode from DM: got %q, want %q", got, dmKey)
	}

	// DynamicDashboard enabled + group source: still static (default fleet
	// behavior for General dispatch must not change, L-0429).
	groupKey := "telegram:-1003917051393:7664413698:0"
	if got := e.resolveDashboardSessionKey(groupKey); got != "telegram:-1003917051393:7664413698" {
		t.Errorf("dynamic mode from group: got %q, want static General group", got)
	}
}

func TestExecuteDispatch_DynamicDashboardRoutesToPrivateChatViaVirtualTopic(t *testing.T) {
	root := t.TempDir()
	threadDir := filepath.Join(root, "threads", "cc-connect-notification-boundary")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(threadDir, "L-0429.query.md")
	query := `---
ID: L-0429
Thread: cc-connect-notification-boundary
Type: QUERY
---

## Query
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}

	createTopicCalls := 0
	p := &mockTaskTopicPlatform{
		stubMediaPlatform: stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}},
		createTaskTopicFunc: func(ctx context.Context, dashboardSessionKey, title, content string) (*TaskTopic, error) {
			createTopicCalls++
			return nil, fmt.Errorf("forum topics are not supported in private chats")
		},
		reconstructFunc: func(sessionKey string) (any, error) {
			return "reconstructed-ctx:" + sessionKey, nil
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
		DashboardSessionKey: "telegram:-1003917051393:7664413698", // General group (unused: DM wins)
		DynamicDashboard:    true,
		PollInterval:        1 * time.Second,
	})

	dmSessionKey := "telegram:7664413698:7664413698"
	req := dispatchRequest{
		To:     "dev-pro",
		Letter: "L-0429",
		Thread: "cc-connect-notification-boundary",
		Path:   queryPath,
	}

	receipt, err := sourceEngine.executeDispatch(p, dmSessionKey, req)
	if err != nil {
		t.Fatalf("executeDispatch failed: %v", err)
	}
	if receipt != "✅ Dispatched L-0429 to dev-pro" {
		t.Errorf("unexpected receipt: %q", receipt)
	}
	// The DM has no forum support at all, so CreateTaskTopic must never be
	// attempted — the private-chat shortcut skips straight to the virtual
	// topic path (L-0429).
	if createTopicCalls != 0 {
		t.Fatalf("expected CreateTaskTopic to be skipped for a private chat, got %d calls", createTopicCalls)
	}

	open, err := sourceEngine.dispatchStore.listOpen()
	if err != nil {
		t.Fatalf("listOpen failed: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open expectation, got %d", len(open))
	}
	exp := open[0]
	if exp.DashboardSessionKey != dmSessionKey {
		t.Errorf("expected DashboardSessionKey routed to DM %q, got %q", dmSessionKey, exp.DashboardSessionKey)
	}
	if exp.TopicSessionKey != "telegram:7664413698:0429:7664413698" {
		t.Errorf("unexpected virtual topic session key: %q", exp.TopicSessionKey)
	}
}

type topicIsolationTestAgent struct {
	stubAgent
}

func (a *topicIsolationTestAgent) Name() string {
	return "topic-isolation-test-agent"
}

func (a *topicIsolationTestAgent) GetWorkDir() string {
	return "/mock/parent/workdir"
}

func TestExecuteDispatch_TopicIsolationWithoutWorkspacePattern(t *testing.T) {
	root := t.TempDir()
	threadDir := filepath.Join(root, "threads", "advisory-seat-isolation")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(threadDir, "L-0318.query.md")
	query := `---
ID: L-0318
Thread: advisory-seat-isolation
Type: QUERY
---

## Query
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}

	workDirChan := make(chan string, 1)
	RegisterAgent("topic-isolation-test-agent", func(opts map[string]any) (Agent, error) {
		if wd, ok := opts["work_dir"].(string); ok {
			workDirChan <- wd
		}
		return &topicIsolationTestAgent{}, nil
	})

	createTopicCalls := 0
	p := &mockTaskTopicPlatform{
		stubMediaPlatform: stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}},
		createTaskTopicFunc: func(ctx context.Context, dashboardSessionKey, title, content string) (*TaskTopic, error) {
			createTopicCalls++
			return &TaskTopic{
				SessionKey: "telegram:-1003917051393:3180:7664413698",
				ReplyCtx:   "mock-reply-ctx",
				ThreadID:   "3180",
				Name:       "letter-L-0318",
			}, nil
		},
		reconstructFunc: func(sessionKey string) (any, error) {
			return "reconstructed-ctx", nil
		},
	}

	targetEngine := NewEngine("researcher-seat", &topicIsolationTestAgent{}, []Platform{p}, "", LangEnglish)
	targetEngine.SetDispatchTopicIsolation(true)

	sourceEngine := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sourceEngine.dataDir = root
	sourceEngine.relayManager = NewRelayManager(root)
	sourceEngine.relayManager.RegisterEngine("researcher-seat", targetEngine)
	sourceEngine.relayManager.RegisterEngine("secretary-seat", sourceEngine)

	sourceEngine.configureDispatch(DispatchConfig{
		Enabled:             true,
		SourceProject:       "secretary-seat",
		DashboardSessionKey: "telegram:-1003917051393:7664413698",
		PollInterval:        1 * time.Second,
	})

	req := dispatchRequest{
		To:     "researcher-seat",
		Letter: "L-0318",
		Thread: "advisory-seat-isolation",
		Path:   queryPath,
	}

	receipt, err := sourceEngine.executeDispatch(p, "telegram:-1003917051393:7664413698", req)
	if err != nil {
		t.Fatalf("executeDispatch failed: %v", err)
	}
	if receipt != "✅ Dispatched L-0318 to researcher-seat in Topic #3180" {
		t.Errorf("unexpected receipt: %q", receipt)
	}
	if createTopicCalls != 1 {
		t.Fatalf("expected CreateTaskTopic to be called once, got %d", createTopicCalls)
	}

	open, err := sourceEngine.dispatchStore.listOpen()
	if err != nil {
		t.Fatalf("listOpen failed: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open expectation, got %d", len(open))
	}
	exp := open[0]
	if exp.TopicID != "3180" || exp.TopicSessionKey != "telegram:-1003917051393:3180:7664413698" {
		t.Errorf("unexpected topic expectation: %+v", exp)
	}

	select {
	case gotWorkDir := <-workDirChan:
		if gotWorkDir != "/mock/parent/workdir" {
			t.Errorf("got work_dir %q, want %q", gotWorkDir, "/mock/parent/workdir")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected workspace agent to be created with inherited parent workdir")
	}
}

type orderingTestAgent struct {
	stubAgent
}

func (a *orderingTestAgent) Name() string {
	return "ordering-test-agent"
}

func TestExecuteDispatch_LedgerOrdering(t *testing.T) {
	root := t.TempDir()
	threadDir := filepath.Join(root, "threads", "topology-reframe")
	if err := os.MkdirAll(threadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(threadDir, "L-0275.query.md")
	query := `---
ID: L-0275
Thread: topology-reframe
Type: QUERY
---

## Query
`
	if err := os.WriteFile(queryPath, []byte(query), 0o644); err != nil {
		t.Fatal(err)
	}

	workDirChan := make(chan string, 1)
	RegisterAgent("ordering-test-agent", func(opts map[string]any) (Agent, error) {
		if wd, ok := opts["work_dir"].(string); ok {
			workDirChan <- wd
		}
		return &orderingTestAgent{}, nil
	})

	p := &mockTaskTopicPlatform{
		stubMediaPlatform: stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}},
		createTaskTopicFunc: func(ctx context.Context, dashboardSessionKey, title, content string) (*TaskTopic, error) {
			return &TaskTopic{
				SessionKey: "telegram:-1003917051393:1234:7664413698",
				ReplyCtx:   "mock-reply-ctx",
				ThreadID:   "1234",
				Name:       "letter-L-0275",
			}, nil
		},
		reconstructFunc: func(sessionKey string) (any, error) {
			return "reconstructed-ctx", nil
		},
	}

	ta := &orderingTestAgent{}
	targetEngine := NewEngine("dev-pro", ta, []Platform{p}, "", LangEnglish)
	targetEngine.SetMultiWorkspace(root, filepath.Join(root, "bindings.json"))
	targetEngine.SetWorkspacePattern(filepath.Join(root, "worktrees", "letter-{{LETTER_ID}}"))
	targetEngine.dataDir = root

	sourceEngine := NewEngine("secretary-seat", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sourceEngine.dataDir = root
	sourceEngine.relayManager = NewRelayManager(root)
	sourceEngine.relayManager.RegisterEngine("dev-pro", targetEngine)
	sourceEngine.relayManager.RegisterEngine("secretary-seat", sourceEngine)

	sourceEngine.configureDispatch(DispatchConfig{
		Enabled:             true,
		SourceProject:       "secretary-seat",
		DashboardSessionKey: "telegram:-1003917051393:7664413698",
		PollInterval:        1 * time.Second,
	})

	req := dispatchRequest{
		To:     "dev-pro",
		Letter: "L-0275",
		Thread: "topology-reframe",
		Path:   queryPath,
	}

	_, err := sourceEngine.executeDispatch(p, "telegram:-1003917051393:7664413698", req)
	if err != nil {
		t.Fatalf("executeDispatch failed: %v", err)
	}

	select {
	case gotWorkDir := <-workDirChan:
		wantWorkDir := filepath.Join(root, "worktrees", "letter-L-0275")
		if gotWorkDir != wantWorkDir {
			t.Errorf("expected workspace work_dir %q, got %q (ledger resolution failed/fallback used)", wantWorkDir, gotWorkDir)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for workspace agent creation")
	}
}
