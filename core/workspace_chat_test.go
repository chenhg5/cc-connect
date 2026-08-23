package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type workspaceChatMemoryRepository struct {
	mu         sync.Mutex
	selections map[string]WorkspaceChatSelection
	menus      map[string]WorkspaceChatMenuSnapshot
	turns      map[string]WorkspaceChatTurnRecord
	closed     bool
}

func newWorkspaceChatMemoryRepository() *workspaceChatMemoryRepository {
	return &workspaceChatMemoryRepository{
		selections: make(map[string]WorkspaceChatSelection), menus: make(map[string]WorkspaceChatMenuSnapshot), turns: make(map[string]WorkspaceChatTurnRecord),
	}
}

func (r *workspaceChatMemoryRepository) GetSelection(_ context.Context, clientID string) (*WorkspaceChatSelection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	selection, ok := r.selections[clientID]
	if !ok {
		return nil, nil
	}
	copy := selection
	return &copy, nil
}
func (r *workspaceChatMemoryRepository) PutSelection(_ context.Context, selection WorkspaceChatSelection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selections[selection.ClientID] = selection
	return nil
}
func (r *workspaceChatMemoryRepository) DeleteSelection(_ context.Context, clientID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.selections, clientID)
	return nil
}
func (r *workspaceChatMemoryRepository) GetMenu(_ context.Context, clientID, kind string) (*WorkspaceChatMenuSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	menu, ok := r.menus[clientID+"\x00"+kind]
	if !ok {
		return nil, nil
	}
	copy := menu
	copy.ItemIDs = append([]string(nil), menu.ItemIDs...)
	return &copy, nil
}
func (r *workspaceChatMemoryRepository) PutMenu(_ context.Context, menu WorkspaceChatMenuSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.menus[menu.ClientID+"\x00"+menu.Kind] = menu
	return nil
}
func (r *workspaceChatMemoryRepository) BeginTurn(_ context.Context, record WorkspaceChatTurnRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.turns[record.RequestID]; exists {
		return fmt.Errorf("duplicate request")
	}
	r.turns[record.RequestID] = record
	return nil
}
func (r *workspaceChatMemoryRepository) FinishTurn(_ context.Context, requestID, status, errorMessage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, exists := r.turns[requestID]
	if !exists {
		return fmt.Errorf("turn not found")
	}
	record.Status, record.Error, record.UpdatedAt = status, errorMessage, time.Now()
	r.turns[requestID] = record
	return nil
}
func (r *workspaceChatMemoryRepository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}
func (r *workspaceChatMemoryRepository) turnStatus(requestID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[requestID].Status
}

type workspaceChatTestAgent struct {
	mu         sync.Mutex
	workspaces []Workspace
	threads    map[string]NativeThreadDetail
	sessions   map[string]*workspaceChatTestSession
	started    chan string
	release    chan struct{}
	nextThread int
}

func (a *workspaceChatTestAgent) Name() string { return "workspace-test" }
func (a *workspaceChatTestAgent) StartSession(_ context.Context, threadID string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if session := a.sessions[threadID]; session != nil && session.Alive() {
		return session, nil
	}
	session := newWorkspaceChatTestSession(threadID, a.started, a.release)
	a.sessions[threadID] = session
	return session, nil
}
func (a *workspaceChatTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *workspaceChatTestAgent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, session := range a.sessions {
		_ = session.Close()
	}
	return nil
}
func (a *workspaceChatTestAgent) ListWorkspaces(context.Context) ([]Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Workspace(nil), a.workspaces...), nil
}
func (a *workspaceChatTestAgent) ResolveWorkspace(_ context.Context, ref string) (Workspace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, workspace := range a.workspaces {
		if workspace.Ref == ref {
			return workspace, nil
		}
	}
	return Workspace{}, ErrWorkspaceNotFound
}
func (a *workspaceChatTestAgent) ListNativeThreads(context.Context) ([]NativeThread, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	threads := make([]NativeThread, 0, len(a.threads))
	for _, detail := range a.threads {
		threads = append(threads, detail.NativeThread)
	}
	return threads, nil
}
func (a *workspaceChatTestAgent) ReadNativeThread(_ context.Context, threadID string) (NativeThreadDetail, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	detail, ok := a.threads[threadID]
	if !ok {
		return NativeThreadDetail{}, ErrNativeThreadNotFound
	}
	return detail, nil
}
func (a *workspaceChatTestAgent) StartNativeThread(_ context.Context, name string) (NativeThread, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextThread++
	workspace := a.workspaces[0]
	thread := NativeThread{ID: fmt.Sprintf("thread-new-%d", a.nextThread), Cwd: workspace.RootPath, Name: name, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	a.threads[thread.ID] = NativeThreadDetail{NativeThread: thread, Turns: []NativeTurn{}}
	return thread, nil
}

type workspaceChatTestSession struct {
	threadID string
	events   chan Event
	started  chan string
	release  chan struct{}
	closed   chan struct{}
	close    sync.Once
	mu       sync.Mutex
	images   int
	files    int
}

func newWorkspaceChatTestSession(threadID string, started chan string, release chan struct{}) *workspaceChatTestSession {
	return &workspaceChatTestSession{threadID: threadID, events: make(chan Event, 16), started: started, release: release, closed: make(chan struct{})}
}
func (s *workspaceChatTestSession) Send(prompt, _ string, images []ImageAttachment, files []FileAttachment) error {
	s.mu.Lock()
	s.images += len(images)
	s.files += len(files)
	s.mu.Unlock()
	s.started <- prompt
	select {
	case <-s.release:
	case <-s.closed:
		return context.Canceled
	}
	if prompt == "approve" {
		s.events <- Event{Type: EventPermissionRequest, RequestID: "approval-1", ToolName: "Bash", ToolInput: "echo test"}
		return nil
	}
	s.events <- Event{Type: EventText, Content: "reply:" + prompt}
	s.events <- Event{Type: EventResult, SessionID: s.threadID, Done: true}
	return nil
}
func (s *workspaceChatTestSession) RespondPermission(_ string, result PermissionResult) error {
	if result.Behavior != "allow" {
		return fmt.Errorf("unexpected behavior %s", result.Behavior)
	}
	s.events <- Event{Type: EventText, Content: "approved"}
	s.events <- Event{Type: EventResult, SessionID: s.threadID, Done: true}
	return nil
}
func (s *workspaceChatTestSession) Events() <-chan Event     { return s.events }
func (s *workspaceChatTestSession) CurrentSessionID() string { return s.threadID }
func (s *workspaceChatTestSession) Alive() bool {
	select {
	case <-s.closed:
		return false
	default:
		return true
	}
}
func (s *workspaceChatTestSession) Close() error {
	s.close.Do(func() { close(s.closed) })
	return nil
}

type workspaceChatTestPlatform struct {
	stubPlatformEngine
	images []ImageAttachment
	files  []FileAttachment
}

func (p *workspaceChatTestPlatform) SendImage(_ context.Context, _ any, image ImageAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.images = append(p.images, image)
	return nil
}
func (p *workspaceChatTestPlatform) SendFile(_ context.Context, _ any, file FileAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files = append(p.files, file)
	return nil
}

func newWorkspaceChatTestService(t *testing.T) (*WorkspaceChatService, *workspaceChatMemoryRepository, *workspaceChatTestAgent, *workspaceChatTestPlatform, Workspace) {
	t.Helper()
	root := t.TempDir()
	workspace := Workspace{Ref: "ws-one", ProjectID: "project-one", ProjectName: "Project One", RootName: "Project One", RootPath: root, Available: true, Order: 0}
	userItem, _ := json.Marshal(map[string]any{"type": "userMessage", "text": "hello"})
	agentItem, _ := json.Marshal(map[string]any{"type": "agentMessage", "text": "world"})
	thread := NativeThread{ID: "thread-1", Cwd: root, Name: "First", CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now()}
	agent := &workspaceChatTestAgent{
		workspaces: []Workspace{workspace},
		threads:    map[string]NativeThreadDetail{thread.ID: {NativeThread: thread, Turns: []NativeTurn{{ID: "history-1", Status: "completed", Items: []json.RawMessage{userItem, agentItem}}}}},
		sessions:   make(map[string]*workspaceChatTestSession), started: make(chan string, 16), release: make(chan struct{}, 16),
	}
	platform := &workspaceChatTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "wecom"}}
	engine := NewEngine("workspace", agent, []Platform{platform}, "", LangChinese)
	engine.workspacePool = newWorkspacePool(DefaultWorkspaceIdleTimeout)
	state := engine.workspacePool.GetOrCreate(root)
	state.agent = agent
	state.sessions = NewSessionManager("")
	repository := newWorkspaceChatMemoryRepository()
	service, err := NewWorkspaceChatService(engine, repository, []string{"web", "wecom"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.Close()
		_ = engine.Stop()
	})
	return service, repository, agent, platform, workspace
}

func TestCUJ_WorkspaceChatWeComSelectionHistoryAndGroupRejection(t *testing.T) {
	service, repository, agent, platform, _ := newWorkspaceChatTestService(t)
	message := func(content string, scope ConversationScope) *Message {
		return &Message{Platform: "wecom", UserID: "u1", MessageID: "m-" + content, Content: content, Scope: scope}
	}
	if !service.HandleIncoming(platform, message("hello before selection", ConversationScopeDirect)) {
		t.Fatal("unselected direct message was not consumed")
	}
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "请先用 /projects") || !strings.Contains(got, "Codex App 项目") {
		t.Fatalf("first response = %q", got)
	}
	platform.clearSent()
	service.HandleIncoming(platform, message("/project 1", ConversationScopeDirect))
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "已选择项目") {
		t.Fatalf("project selection response = %q", got)
	}
	selection, _ := repository.GetSelection(context.Background(), "wecom:user:u1")
	if selection == nil || selection.WorkspaceRef != "ws-one" || selection.ThreadID != "thread-1" {
		t.Fatalf("selection = %#v", selection)
	}
	platform.clearSent()
	service.HandleIncoming(platform, message("/history 10", ConversationScopeDirect))
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "**用户**") || !strings.Contains(got, "**助手**") || !strings.Contains(got, "world") {
		t.Fatalf("history response = %q", got)
	}
	platform.clearSent()
	service.HandleIncoming(platform, message("/new named", ConversationScopeDirect))
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "已创建新会话：named") {
		t.Fatalf("new thread response = %q", got)
	}
	platform.clearSent()
	service.HandleIncoming(platform, message("group message", ConversationScopeGroup))
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "不支持群聊工作区对话") {
		t.Fatalf("group response = %q", got)
	}
	select {
	case prompt := <-agent.started:
		t.Fatalf("group message reached agent: %q", prompt)
	default:
	}
}

func TestWorkspaceChatMenuSnapshotIsRevalidated(t *testing.T) {
	service, _, agent, platform, _ := newWorkspaceChatTestService(t)
	msg := &Message{Platform: "wecom", UserID: "u1", Content: "/projects", Scope: ConversationScopeDirect}
	service.HandleIncoming(platform, msg)
	agent.mu.Lock()
	agent.workspaces = nil
	agent.mu.Unlock()
	platform.clearSent()
	msg.Content = "/project 1"
	service.HandleIncoming(platform, msg)
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "错误") {
		t.Fatalf("stale snapshot response = %q", got)
	}
}

func TestWorkspaceChatThreadFIFOAttachmentsApprovalAndCancel(t *testing.T) {
	service, repository, agent, platform, workspace := newWorkspaceChatTestService(t)
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	first := &Message{Platform: "wecom", UserID: "u1", MessageID: "m1", Content: "first", Images: []ImageAttachment{{FileName: "one.png"}}, Files: []FileAttachment{{FileName: "one.txt"}}}
	second := &Message{Platform: "web", UserID: "web:admin", MessageID: "m2", Content: "second"}
	if err := service.Send(context.Background(), "wecom:user:u1", "request-1", workspace.Ref, "thread-1", platform, first, firstDone); err != nil {
		t.Fatal(err)
	}
	if got := <-agent.started; got != "first" {
		t.Fatalf("first prompt = %q", got)
	}
	if err := service.Send(context.Background(), "web:admin", "request-2", workspace.Ref, "thread-1", nil, second, secondDone); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-agent.started:
		t.Fatalf("second turn started before FIFO release: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	agent.release <- struct{}{}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := <-agent.started; got != "second" {
		t.Fatalf("second prompt = %q", got)
	}
	agent.release <- struct{}{}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if repository.turnStatus("request-1") != "completed" || repository.turnStatus("request-2") != "completed" {
		t.Fatalf("turn states = %q, %q", repository.turnStatus("request-1"), repository.turnStatus("request-2"))
	}
	agent.mu.Lock()
	session := agent.sessions["thread-1"]
	agent.mu.Unlock()
	session.mu.Lock()
	images, files := session.images, session.files
	session.mu.Unlock()
	if images != 1 || files != 1 {
		t.Fatalf("attachment counts = images:%d files:%d", images, files)
	}

	approvalDone := make(chan error, 1)
	if err := repository.PutSelection(context.Background(), WorkspaceChatSelection{
		ClientID: "wecom:user:u1", WorkspaceRef: workspace.Ref, ThreadID: "thread-1", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	platform.clearSent()
	if err := service.Send(context.Background(), "wecom:user:u1", "request-3", workspace.Ref, "thread-1", platform, &Message{Platform: "wecom", UserID: "u1", Content: "approve"}, approvalDone); err != nil {
		t.Fatal(err)
	}
	if got := <-agent.started; got != "approve" {
		t.Fatalf("approval prompt = %q", got)
	}
	agent.release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(strings.Join(platform.getSent(), "\n"), "权限请求") {
		if time.Now().After(deadline) {
			t.Fatal("approval prompt was not delivered to WeCom")
		}
		time.Sleep(10 * time.Millisecond)
	}
	service.HandleIncoming(platform, &Message{Platform: "wecom", UserID: "u1", MessageID: "approval-response", Content: "allow", Scope: ConversationScopeDirect})
	if err := <-approvalDone; err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(platform.getSent(), "\n"); !strings.Contains(got, "已允许") {
		t.Fatalf("approval response = %q", got)
	}

	cancelDone := make(chan error, 1)
	if err := service.Send(context.Background(), "web:admin", "request-4", workspace.Ref, "thread-1", nil, &Message{Platform: "web", Content: "blocked"}, cancelDone); err != nil {
		t.Fatal(err)
	}
	if got := <-agent.started; got != "blocked" {
		t.Fatalf("cancel prompt = %q", got)
	}
	cancelEvents, unsubscribe := service.Subscribe("thread-1")
	defer unsubscribe()
	if err := service.Cancel(context.Background(), workspace.Ref, "thread-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-cancelEvents:
		if event.Type != "turn_cancel_requested" {
			t.Fatalf("first cancel event = %q, want turn_cancel_requested", event.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel request event was not published")
	}
	if err := <-cancelDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel completion = %v", err)
	}
	if repository.turnStatus("request-4") != "cancelled" {
		t.Fatalf("cancelled turn state = %q", repository.turnStatus("request-4"))
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		select {
		case event := <-cancelEvents:
			if event.Type == "turn_cancelled" {
				return
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("terminal cancel event was not published")
		}
	}
}

func TestWorkspaceChatPlatformForwardsMediaCapabilities(t *testing.T) {
	service, _, _, platform, _ := newWorkspaceChatTestService(t)
	wrapper := &workspaceChatPlatform{base: platform, service: service, threadID: "thread-1"}
	if err := wrapper.SendImage(context.Background(), nil, ImageAttachment{FileName: "image.png"}); err != nil {
		t.Fatal(err)
	}
	if err := wrapper.SendFile(context.Background(), nil, FileAttachment{FileName: "file.txt"}); err != nil {
		t.Fatal(err)
	}
	if len(platform.images) != 1 || len(platform.files) != 1 {
		t.Fatalf("forwarded media = images:%d files:%d", len(platform.images), len(platform.files))
	}
	withoutBase := &workspaceChatPlatform{service: service, threadID: "thread-1"}
	if !errors.Is(withoutBase.SendImage(context.Background(), nil, ImageAttachment{}), ErrNotSupported) {
		t.Fatal("web image delivery did not report unsupported capability")
	}
}

func TestWorkspaceChatCloseCancelsActiveTurnAndDrainsQueue(t *testing.T) {
	service, repository, agent, _, workspace := newWorkspaceChatTestService(t)
	activeDone := make(chan error, 1)
	queuedDone := make(chan error, 1)
	if err := service.Send(context.Background(), "web:admin", "close-active", workspace.Ref, "thread-1", nil, &Message{Platform: "web", Content: "active"}, activeDone); err != nil {
		t.Fatal(err)
	}
	if got := <-agent.started; got != "active" {
		t.Fatalf("active prompt = %q", got)
	}
	if err := service.Send(context.Background(), "web:admin", "close-queued", workspace.Ref, "thread-1", nil, &Message{Platform: "web", Content: "queued"}, queuedDone); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WorkspaceChatService.Close did not stop the active turn")
	}
	if err := <-activeDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("active completion = %v", err)
	}
	if err := <-queuedDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued completion = %v", err)
	}
	if repository.turnStatus("close-active") != "cancelled" || repository.turnStatus("close-queued") != "cancelled" {
		t.Fatalf("close states = %q, %q", repository.turnStatus("close-active"), repository.turnStatus("close-queued"))
	}
}
