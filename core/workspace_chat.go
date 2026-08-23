package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrWorkspaceChatNotConfigured = errors.New("workspace chat is not configured")
	ErrWorkspaceNotFound          = errors.New("workspace not found")
	ErrNativeThreadNotFound       = errors.New("native thread not found")
	ErrWorkspaceTurnNotRunning    = errors.New("workspace turn is not running")
	errWorkspaceMenuInvalidIndex  = errors.New("workspace menu index is invalid")
	errWorkspacePositiveNumber    = errors.New("workspace positive number is invalid")
	errWorkspacePageOutOfRange    = errors.New("workspace menu page is out of range")
	errWorkspaceMenuItemNotFound  = errors.New("workspace menu item is not found")
)

type ConversationScope string

const (
	ConversationScopeDirect ConversationScope = "direct"
	ConversationScopeGroup  ConversationScope = "group"
)

// Workspace 是目录源签发的可选根目录。Ref 是不透明标识，客户端不能用 cwd 代替。
type Workspace struct {
	Ref         string `json:"ref"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	RootIndex   int    `json:"root_index"`
	RootName    string `json:"root_name"`
	RootPath    string `json:"root_path"`
	Available   bool   `json:"available"`
	Error       string `json:"error,omitempty"`
	Order       int    `json:"order"`
}

type NativeThread struct {
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	Name      string          `json:"name,omitempty"`
	Preview   string          `json:"preview,omitempty"`
	Status    json.RawMessage `json:"status,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Items 保留后端原始 JSON，新增 item 类型不会在传输中丢失。
type NativeTurn struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	DurationMS  *int64            `json:"duration_ms,omitempty"`
	Error       json.RawMessage   `json:"error,omitempty"`
	Items       []json.RawMessage `json:"items"`
}

type NativeThreadDetail struct {
	NativeThread
	Turns []NativeTurn `json:"turns"`
}

type WorkspaceCatalogProvider interface {
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	ResolveWorkspace(ctx context.Context, ref string) (Workspace, error)
}

// NativeThreadProvider 由具备后端原生会话能力的工作区 Agent 实现。
type NativeThreadProvider interface {
	ListNativeThreads(ctx context.Context) ([]NativeThread, error)
	ReadNativeThread(ctx context.Context, threadID string) (NativeThreadDetail, error)
	StartNativeThread(ctx context.Context, name string) (NativeThread, error)
}

type WorkspaceChatSelection struct {
	ClientID     string    `json:"client_id"`
	WorkspaceRef string    `json:"workspace_ref"`
	ThreadID     string    `json:"thread_id"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type WorkspaceChatMenuSnapshot struct {
	ClientID  string    `json:"client_id"`
	Kind      string    `json:"kind"`
	Revision  string    `json:"revision"`
	ItemIDs   []string  `json:"item_ids"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WorkspaceChatTurnRecord struct {
	RequestID    string    `json:"request_id"`
	ClientID     string    `json:"client_id"`
	WorkspaceRef string    `json:"workspace_ref"`
	ThreadID     string    `json:"thread_id"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type WorkspaceChatRepository interface {
	GetSelection(ctx context.Context, clientID string) (*WorkspaceChatSelection, error)
	PutSelection(ctx context.Context, selection WorkspaceChatSelection) error
	DeleteSelection(ctx context.Context, clientID string) error
	GetMenu(ctx context.Context, clientID, kind string) (*WorkspaceChatMenuSnapshot, error)
	PutMenu(ctx context.Context, snapshot WorkspaceChatMenuSnapshot) error
	BeginTurn(ctx context.Context, record WorkspaceChatTurnRecord) error
	FinishTurn(ctx context.Context, requestID, status, errorMessage string) error
	Close() error
}

type WorkspaceChatEvent struct {
	Type         string          `json:"type"`
	RequestID    string          `json:"request_id,omitempty"`
	ClientID     string          `json:"client_id,omitempty"`
	WorkspaceRef string          `json:"workspace_ref,omitempty"`
	ThreadID     string          `json:"thread_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Error        string          `json:"error,omitempty"`
	OccurredAt   time.Time       `json:"occurred_at"`
}

// WorkspaceAgentEventSink 允许平台适配器接收未经展示层压缩的 Agent 事件。
// Engine 只依赖该通用能力，具体传输协议由实现方决定。
type WorkspaceAgentEventSink interface {
	PublishWorkspaceAgentEvent(Event)
}

type workspaceChatRuntime struct{ sessions *SessionManager }
type workspaceChatThreadQueue struct {
	jobs chan workspaceChatJob
	mu   sync.Mutex
	// activeCancel 只由该 thread 的 worker 设置；Cancel 通过它标记权威运行状态。
	activeRequest string
	activeCancel  context.CancelFunc
}
type workspaceChatJob struct {
	ctx          context.Context
	cancel       context.CancelFunc
	clientID     string
	requestID    string
	workspace    Workspace
	threadID     string
	agent        Agent
	platform     Platform
	message      Message
	runtime      *workspaceChatRuntime
	completionCh chan error
}

// WorkspaceChatService 是 thread FIFO、选择持久化、事件分发和取消的唯一生命周期所有者。
type WorkspaceChatService struct {
	engine     *Engine
	repo       WorkspaceChatRepository
	transports map[string]struct{}
	ctx        context.Context
	cancel     context.CancelFunc

	runtimeMu sync.Mutex
	runtimes  map[string]*workspaceChatRuntime
	queues    map[string]*workspaceChatThreadQueue
	closed    bool
	closeCh   chan struct{}
	workers   sync.WaitGroup

	subMu       sync.RWMutex
	subscribers map[string]map[uint64]chan WorkspaceChatEvent
	nextSubID   uint64
}

func NewWorkspaceChatService(engine *Engine, repo WorkspaceChatRepository, transports []string) (*WorkspaceChatService, error) {
	if engine == nil || repo == nil {
		return nil, fmt.Errorf("workspace chat: engine and repository are required")
	}
	if _, ok := engine.agent.(WorkspaceCatalogProvider); !ok {
		return nil, fmt.Errorf("workspace chat: template agent does not provide a workspace catalog")
	}
	allowed := make(map[string]struct{}, len(transports))
	for _, transport := range transports {
		if transport = strings.ToLower(strings.TrimSpace(transport)); transport != "" {
			allowed[transport] = struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkspaceChatService{
		engine: engine, repo: repo, transports: allowed,
		runtimes: make(map[string]*workspaceChatRuntime), queues: make(map[string]*workspaceChatThreadQueue),
		subscribers: make(map[string]map[uint64]chan WorkspaceChatEvent), closeCh: make(chan struct{}),
		ctx: ctx, cancel: cancel,
	}, nil
}

func (s *WorkspaceChatService) Close() error {
	s.runtimeMu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
		close(s.closeCh)
	}
	threadIDs := make([]string, 0, len(s.queues))
	for threadID := range s.queues {
		threadIDs = append(threadIDs, threadID)
	}
	s.runtimeMu.Unlock()
	for _, threadID := range threadIDs {
		s.engine.stopInteractiveSessionSilently(workspaceChatRuntimeKey(threadID))
	}
	s.workers.Wait()
	s.subMu.Lock()
	for _, subscribers := range s.subscribers {
		for _, ch := range subscribers {
			close(ch)
		}
	}
	s.subscribers = make(map[string]map[uint64]chan WorkspaceChatEvent)
	s.subMu.Unlock()
	return s.repo.Close()
}

func (s *WorkspaceChatService) TransportEnabled(name string) bool {
	_, ok := s.transports[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func (s *WorkspaceChatService) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	catalog, ok := s.engine.agent.(WorkspaceCatalogProvider)
	if !ok {
		return nil, ErrWorkspaceChatNotConfigured
	}
	items, err := catalog.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("workspace chat: list workspaces: %w", err)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	return items, nil
}

func (s *WorkspaceChatService) resolveWorkspace(ctx context.Context, ref string) (Workspace, error) {
	catalog, ok := s.engine.agent.(WorkspaceCatalogProvider)
	if !ok {
		return Workspace{}, ErrWorkspaceChatNotConfigured
	}
	workspace, err := catalog.ResolveWorkspace(ctx, strings.TrimSpace(ref))
	if err != nil {
		return Workspace{}, fmt.Errorf("%w: %v", ErrWorkspaceNotFound, err)
	}
	if !workspace.Available {
		return Workspace{}, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, workspace.Error)
	}
	return workspace, nil
}

func (s *WorkspaceChatService) workspaceBackend(ctx context.Context, ref string) (Workspace, Agent, NativeThreadProvider, error) {
	workspace, err := s.resolveWorkspace(ctx, ref)
	if err != nil {
		return Workspace{}, nil, nil, err
	}
	agent, _, err := s.engine.getOrCreateWorkspaceAgent(workspace.RootPath)
	if err != nil {
		return Workspace{}, nil, nil, fmt.Errorf("workspace chat: create workspace agent: %w", err)
	}
	backend, ok := agent.(NativeThreadProvider)
	if !ok {
		return Workspace{}, nil, nil, fmt.Errorf("workspace chat: workspace agent does not provide native threads")
	}
	return workspace, agent, backend, nil
}

func (s *WorkspaceChatService) ListThreads(ctx context.Context, workspaceRef string) ([]NativeThread, error) {
	workspace, _, backend, err := s.workspaceBackend(ctx, workspaceRef)
	if err != nil {
		return nil, err
	}
	threads, err := backend.ListNativeThreads(ctx)
	if err != nil {
		return nil, fmt.Errorf("workspace chat: list native threads: %w", err)
	}
	filtered := make([]NativeThread, 0, len(threads))
	for _, thread := range threads {
		if sameWorkspacePath(thread.Cwd, workspace.RootPath) {
			filtered = append(filtered, thread)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt) })
	return filtered, nil
}

func (s *WorkspaceChatService) ReadThread(ctx context.Context, workspaceRef, threadID string) (NativeThreadDetail, error) {
	workspace, _, backend, err := s.workspaceBackend(ctx, workspaceRef)
	if err != nil {
		return NativeThreadDetail{}, err
	}
	detail, err := backend.ReadNativeThread(ctx, strings.TrimSpace(threadID))
	if err != nil {
		return NativeThreadDetail{}, fmt.Errorf("workspace chat: read native thread: %w", err)
	}
	if !sameWorkspacePath(detail.Cwd, workspace.RootPath) {
		return NativeThreadDetail{}, fmt.Errorf("%w: thread does not belong to workspace", ErrNativeThreadNotFound)
	}
	return detail, nil
}

func (s *WorkspaceChatService) StartThread(ctx context.Context, clientID, workspaceRef, name string) (NativeThread, error) {
	workspace, _, backend, err := s.workspaceBackend(ctx, workspaceRef)
	if err != nil {
		return NativeThread{}, err
	}
	thread, err := backend.StartNativeThread(ctx, strings.TrimSpace(name))
	if err != nil {
		return NativeThread{}, fmt.Errorf("workspace chat: start native thread: %w", err)
	}
	if !sameWorkspacePath(thread.Cwd, workspace.RootPath) {
		return NativeThread{}, fmt.Errorf("workspace chat: backend created thread in unexpected cwd %q", thread.Cwd)
	}
	selection := WorkspaceChatSelection{ClientID: clientID, WorkspaceRef: workspace.Ref, ThreadID: thread.ID, UpdatedAt: time.Now()}
	if err := s.repo.PutSelection(ctx, selection); err != nil {
		return NativeThread{}, fmt.Errorf("workspace chat: persist new thread selection: %w", err)
	}
	return thread, nil
}

func (s *WorkspaceChatService) SelectWorkspace(ctx context.Context, clientID, workspaceRef string) (WorkspaceChatSelection, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatSelection{}, err
	}
	threads, err := s.ListThreads(ctx, workspace.Ref)
	if err != nil {
		return WorkspaceChatSelection{}, err
	}
	if len(threads) == 0 {
		thread, err := s.StartThread(ctx, clientID, workspace.Ref, "")
		if err != nil {
			return WorkspaceChatSelection{}, err
		}
		threads = []NativeThread{thread}
	}
	selection := WorkspaceChatSelection{ClientID: clientID, WorkspaceRef: workspace.Ref, ThreadID: threads[0].ID, UpdatedAt: time.Now()}
	if err := s.repo.PutSelection(ctx, selection); err != nil {
		return WorkspaceChatSelection{}, fmt.Errorf("workspace chat: persist workspace selection: %w", err)
	}
	return selection, nil
}

func (s *WorkspaceChatService) SelectThread(ctx context.Context, clientID, workspaceRef, threadID string) (WorkspaceChatSelection, error) {
	workspace, err := s.resolveWorkspace(ctx, workspaceRef)
	if err != nil {
		return WorkspaceChatSelection{}, err
	}
	if _, err := s.ReadThread(ctx, workspace.Ref, threadID); err != nil {
		return WorkspaceChatSelection{}, err
	}
	selection := WorkspaceChatSelection{ClientID: clientID, WorkspaceRef: workspace.Ref, ThreadID: threadID, UpdatedAt: time.Now()}
	if err := s.repo.PutSelection(ctx, selection); err != nil {
		return WorkspaceChatSelection{}, fmt.Errorf("workspace chat: persist thread selection: %w", err)
	}
	return selection, nil
}

func (s *WorkspaceChatService) Selection(ctx context.Context, clientID string) (*WorkspaceChatSelection, error) {
	selection, err := s.repo.GetSelection(ctx, clientID)
	if err != nil || selection == nil {
		return selection, err
	}
	if _, err := s.ReadThread(ctx, selection.WorkspaceRef, selection.ThreadID); err != nil {
		if deleteErr := s.repo.DeleteSelection(ctx, clientID); deleteErr != nil {
			return nil, fmt.Errorf("workspace chat: stale selection: %v; clear selection: %w", err, deleteErr)
		}
		return nil, nil
	}
	return selection, nil
}

func (s *WorkspaceChatService) runtime(workspaceRef string) *workspaceChatRuntime {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	runtime := s.runtimes[workspaceRef]
	if runtime == nil {
		runtime = &workspaceChatRuntime{sessions: NewSessionManager("")}
		s.runtimes[workspaceRef] = runtime
	}
	return runtime
}

func workspaceChatRuntimeKey(threadID string) string {
	sum := sha256.Sum256([]byte(threadID))
	return "workspace-chat:" + hex.EncodeToString(sum[:12])
}

func (s *WorkspaceChatService) queue(threadID string) (*workspaceChatThreadQueue, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.closed {
		return nil, context.Canceled
	}
	queue := s.queues[threadID]
	if queue == nil {
		queue = &workspaceChatThreadQueue{jobs: make(chan workspaceChatJob, 64)}
		s.queues[threadID] = queue
		s.workers.Add(1)
		go s.runQueue(threadID, queue)
	}
	return queue, nil
}

// Send 在 SQLite 提交 queued 后才入队；completionCh 可选，用于企业微信等待最终回复。
func (s *WorkspaceChatService) Send(ctx context.Context, clientID, requestID, workspaceRef, threadID string, p Platform, msg *Message, completionCh chan error) error {
	if msg == nil {
		return fmt.Errorf("workspace chat: message is required")
	}
	workspace, agent, _, err := s.workspaceBackend(ctx, workspaceRef)
	if err != nil {
		return err
	}
	if _, err := s.ReadThread(ctx, workspace.Ref, threadID); err != nil {
		return err
	}
	if requestID == "" {
		requestID = fmt.Sprintf("turn-%d", time.Now().UnixNano())
	}
	now := time.Now()
	record := WorkspaceChatTurnRecord{RequestID: requestID, ClientID: clientID, WorkspaceRef: workspace.Ref, ThreadID: threadID, Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := s.repo.BeginTurn(ctx, record); err != nil {
		return fmt.Errorf("workspace chat: persist turn: %w", err)
	}
	queue, err := s.queue(threadID)
	if err != nil {
		_ = s.repo.FinishTurn(context.Background(), requestID, "failed", err.Error())
		return err
	}
	jobCtx, cancel := context.WithCancel(s.ctx)
	job := workspaceChatJob{ctx: jobCtx, cancel: cancel, clientID: clientID, requestID: requestID, workspace: workspace, threadID: threadID, agent: agent, platform: p, message: *msg, runtime: s.runtime(workspace.Ref), completionCh: completionCh}
	select {
	case queue.jobs <- job:
		s.publish(WorkspaceChatEvent{Type: "turn_queued", RequestID: requestID, ClientID: clientID, WorkspaceRef: workspace.Ref, ThreadID: threadID, OccurredAt: time.Now()})
		return nil
	case <-ctx.Done():
		cancel()
		_ = s.repo.FinishTurn(context.Background(), requestID, "cancelled", ctx.Err().Error())
		return ctx.Err()
	case <-s.closeCh:
		cancel()
		_ = s.repo.FinishTurn(context.Background(), requestID, "cancelled", context.Canceled.Error())
		return context.Canceled
	}
}

func (s *WorkspaceChatService) runQueue(threadID string, queue *workspaceChatThreadQueue) {
	defer s.workers.Done()
	defer func() {
		s.runtimeMu.Lock()
		if s.queues[threadID] == queue {
			delete(s.queues, threadID)
		}
		s.runtimeMu.Unlock()
	}()
	for {
		select {
		case job := <-queue.jobs:
			s.runJob(job)
		case <-s.closeCh:
			for {
				select {
				case job := <-queue.jobs:
					s.finishJob(job, "cancelled", context.Canceled)
				default:
					return
				}
			}
		}
	}
}

func (s *WorkspaceChatService) runJob(job workspaceChatJob) {
	queue, queueErr := s.queue(job.threadID)
	if queueErr != nil {
		s.finishJob(job, "cancelled", queueErr)
		return
	}
	queue.mu.Lock()
	queue.activeRequest = job.requestID
	queue.activeCancel = job.cancel
	queue.mu.Unlock()
	defer func() {
		queue.mu.Lock()
		if queue.activeRequest == job.requestID {
			queue.activeRequest = ""
			queue.activeCancel = nil
		}
		queue.mu.Unlock()
	}()
	if err := job.ctx.Err(); err != nil {
		s.finishJob(job, "cancelled", err)
		return
	}
	if err := s.repo.FinishTurn(context.Background(), job.requestID, "in_progress", ""); err != nil {
		s.finishJob(job, "failed", err)
		return
	}
	s.publish(WorkspaceChatEvent{Type: "turn_started", RequestID: job.requestID, ClientID: job.clientID, WorkspaceRef: job.workspace.Ref, ThreadID: job.threadID, OccurredAt: time.Now()})
	wrapped := &workspaceChatPlatform{base: job.platform, service: s, clientID: job.clientID, requestID: job.requestID, workspaceRef: job.workspace.Ref, threadID: job.threadID}
	job.message.SessionKey = workspaceChatRuntimeKey(job.threadID)
	job.message.Platform = wrapped.Name()
	err := s.engine.runWorkspaceChatMessage(wrapped, &job.message, job.agent, job.runtime.sessions, job.workspace.RootPath, job.threadID)
	if ctxErr := job.ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	if err == nil {
		err = wrapped.agentError()
	}
	status := "completed"
	if errors.Is(err, context.Canceled) {
		status = "cancelled"
	} else if err != nil {
		status = "failed"
	}
	s.finishJob(job, status, err)
}

func (s *WorkspaceChatService) finishJob(job workspaceChatJob, status string, runErr error) {
	if job.cancel != nil {
		job.cancel()
	}
	errorMessage := ""
	if runErr != nil {
		errorMessage = runErr.Error()
	}
	if err := s.repo.FinishTurn(context.Background(), job.requestID, status, errorMessage); err != nil && runErr == nil {
		runErr, errorMessage, status = err, err.Error(), "failed"
	}
	s.publish(WorkspaceChatEvent{Type: "turn_" + status, RequestID: job.requestID, ClientID: job.clientID, WorkspaceRef: job.workspace.Ref, ThreadID: job.threadID, Error: errorMessage, OccurredAt: time.Now()})
	if job.completionCh != nil {
		select {
		case job.completionCh <- runErr:
		default:
		}
	}
}

func (s *WorkspaceChatService) Cancel(ctx context.Context, workspaceRef, threadID string) error {
	if _, err := s.ReadThread(ctx, workspaceRef, threadID); err != nil {
		return err
	}
	s.runtimeMu.Lock()
	queue := s.queues[threadID]
	s.runtimeMu.Unlock()
	active := false
	if queue != nil {
		queue.mu.Lock()
		if queue.activeCancel != nil {
			s.publish(WorkspaceChatEvent{Type: "turn_cancel_requested", WorkspaceRef: workspaceRef, ThreadID: threadID, OccurredAt: time.Now()})
			queue.activeCancel()
			active = true
		}
		queue.mu.Unlock()
	}
	stopped := s.engine.stopInteractiveSessionSilently(workspaceChatRuntimeKey(threadID))
	if !active && !stopped {
		return ErrWorkspaceTurnNotRunning
	}
	if !active {
		s.publish(WorkspaceChatEvent{Type: "turn_cancel_requested", WorkspaceRef: workspaceRef, ThreadID: threadID, OccurredAt: time.Now()})
	}
	return nil
}

func (s *WorkspaceChatService) activeRequestID(threadID string) string {
	s.runtimeMu.Lock()
	queue := s.queues[threadID]
	s.runtimeMu.Unlock()
	if queue == nil {
		return ""
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.activeRequest
}

func (s *WorkspaceChatService) RespondApproval(ctx context.Context, workspaceRef, threadID, decision string) error {
	if _, err := s.ReadThread(ctx, workspaceRef, threadID); err != nil {
		return err
	}
	decision = strings.ToLower(strings.TrimSpace(decision))
	switch decision {
	case "allow":
		decision = "allow"
	case "allow_all":
		decision = "allow all"
	case "deny":
		decision = "deny"
	default:
		return fmt.Errorf("workspace chat: invalid approval decision %q", decision)
	}
	runtimeKey := workspaceChatRuntimeKey(threadID)
	if _, pending := s.engine.lookupPending(runtimeKey); pending == nil {
		return fmt.Errorf("workspace chat: no pending approval")
	}
	msg := &Message{SessionKey: runtimeKey, Content: decision, IsPermissionResponse: true}
	p := &workspaceChatPlatform{service: s, workspaceRef: workspaceRef, threadID: threadID}
	if !s.engine.handlePendingPermission(p, msg, decision, runtimeKey) {
		return fmt.Errorf("workspace chat: no pending approval")
	}
	return nil
}

func (s *WorkspaceChatService) Subscribe(threadID string) (<-chan WorkspaceChatEvent, func()) {
	ch := make(chan WorkspaceChatEvent, 128)
	s.subMu.Lock()
	s.nextSubID++
	id := s.nextSubID
	if s.subscribers[threadID] == nil {
		s.subscribers[threadID] = make(map[uint64]chan WorkspaceChatEvent)
	}
	s.subscribers[threadID][id] = ch
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		if subscribers := s.subscribers[threadID]; subscribers != nil {
			if existing := subscribers[id]; existing != nil {
				delete(subscribers, id)
				close(existing)
			}
			if len(subscribers) == 0 {
				delete(s.subscribers, threadID)
			}
		}
		s.subMu.Unlock()
	}
}

func (s *WorkspaceChatService) publish(event WorkspaceChatEvent) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for _, ch := range s.subscribers[event.ThreadID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *WorkspaceChatService) SaveMenu(ctx context.Context, clientID, kind string, itemIDs []string) error {
	hash := sha256.Sum256([]byte(strings.Join(itemIDs, "\x00")))
	return s.repo.PutMenu(ctx, WorkspaceChatMenuSnapshot{ClientID: clientID, Kind: kind, Revision: hex.EncodeToString(hash[:]), ItemIDs: append([]string(nil), itemIDs...), UpdatedAt: time.Now()})
}

func (s *WorkspaceChatService) MenuItem(ctx context.Context, clientID, kind string, index int) (string, error) {
	menu, err := s.repo.GetMenu(ctx, clientID, kind)
	if err != nil {
		return "", err
	}
	if menu == nil || index < 1 || index > len(menu.ItemIDs) {
		return "", fmt.Errorf("%w: %s", errWorkspaceMenuItemNotFound, kind)
	}
	return menu.ItemIDs[index-1], nil
}

func sameWorkspacePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
}

func ParseWorkspaceMenuIndex(raw string) (int, error) {
	index, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || index < 1 {
		return 0, fmt.Errorf("%w: %q", errWorkspaceMenuInvalidIndex, raw)
	}
	return index, nil
}

const workspaceChatMenuPageSize = 10

// HandleIncoming 将已启用消息传输的单聊路由到统一工作区服务。
// 返回 true 表示消息已消费，调用方不应再进入旧平台 Session 流程。
func (s *WorkspaceChatService) HandleIncoming(p Platform, msg *Message) bool {
	if p == nil || msg == nil || !s.TransportEnabled(msg.Platform) {
		return false
	}
	if msg.Scope == ConversationScopeGroup {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatGroupUnsupported))
		return true
	}
	clientID := strings.ToLower(strings.TrimSpace(msg.Platform)) + ":user:" + strings.TrimSpace(msg.UserID)
	if strings.TrimSpace(msg.UserID) == "" {
		s.replyWorkspaceChatError(p, msg, fmt.Errorf("workspace chat: user id is required"))
		return true
	}
	content := strings.TrimSpace(msg.Content)
	command, argument := splitWorkspaceChatCommand(content)
	ctx := s.ctx

	switch command {
	case "/projects":
		page, err := parseWorkspaceChatPositiveInt(argument, 1)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return true
		}
		text, err := s.renderWorkspaceMenu(ctx, clientID, page)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		} else {
			s.replyWorkspaceChat(p, msg, text)
		}
		return true

	case "/project":
		index, err := ParseWorkspaceMenuIndex(argument)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return true
		}
		workspaceRef, err := s.MenuItem(ctx, clientID, "projects", index)
		if err == nil {
			var selection WorkspaceChatSelection
			selection, err = s.SelectWorkspace(ctx, clientID, workspaceRef)
			if err == nil {
				workspace, resolveErr := s.resolveWorkspace(ctx, selection.WorkspaceRef)
				if resolveErr != nil {
					err = resolveErr
				} else {
					s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatProjectSelected, workspaceDisplayName(workspace)))
				}
			}
		}
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		}
		return true

	case "/threads":
		page, err := parseWorkspaceChatPositiveInt(argument, 1)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return true
		}
		text, err := s.renderThreadMenu(ctx, clientID, page)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		} else {
			s.replyWorkspaceChat(p, msg, text)
		}
		return true

	case "/switch":
		index, err := ParseWorkspaceMenuIndex(argument)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return true
		}
		selection, err := s.Selection(ctx, clientID)
		if err == nil && selection == nil {
			s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNoSelection))
			return true
		}
		if err == nil {
			var threadID string
			threadID, err = s.MenuItem(ctx, clientID, "threads", index)
			if err == nil {
				_, err = s.SelectThread(ctx, clientID, selection.WorkspaceRef, threadID)
				if err == nil {
					detail, readErr := s.ReadThread(ctx, selection.WorkspaceRef, threadID)
					if readErr != nil {
						err = readErr
					} else {
						s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatThreadSelected, threadDisplayName(detail.NativeThread)))
					}
				}
			}
		}
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		}
		return true

	case "/new":
		selection, err := s.Selection(ctx, clientID)
		if err == nil && selection == nil {
			s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNoSelection))
			return true
		}
		if err == nil {
			var thread NativeThread
			thread, err = s.StartThread(ctx, clientID, selection.WorkspaceRef, argument)
			if err == nil {
				s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatNewThread, threadDisplayName(thread)))
			}
		}
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		}
		return true

	case "/current":
		text, err := s.renderCurrentSelection(ctx, clientID)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		} else {
			s.replyWorkspaceChat(p, msg, text)
		}
		return true

	case "/history":
		count, err := parseWorkspaceChatPositiveInt(argument, 10)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return true
		}
		if count > 50 {
			count = 50
		}
		text, err := s.renderNativeHistory(ctx, clientID, count)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		} else {
			s.replyWorkspaceChat(p, msg, text)
		}
		return true

	case "/cancel":
		selection, err := s.Selection(ctx, clientID)
		if err == nil && selection == nil {
			s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNoSelection))
			return true
		}
		if err == nil {
			err = s.Cancel(ctx, selection.WorkspaceRef, selection.ThreadID)
		}
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
		} else {
			s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatCancelled))
		}
		return true

	case "/help":
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatUsage))
		return true
	}

	selection, err := s.Selection(ctx, clientID)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return true
	}
	if selection == nil {
		text, listErr := s.renderWorkspaceMenu(ctx, clientID, 1)
		if listErr != nil {
			s.replyWorkspaceChatError(p, msg, listErr)
		} else {
			s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNoSelection)+"\n\n"+text)
		}
		return true
	}
	runtimeKey := workspaceChatRuntimeKey(selection.ThreadID)
	if _, pending := s.engine.lookupPending(runtimeKey); pending != nil {
		permissionMessage := *msg
		permissionMessage.SessionKey = runtimeKey
		wrapped := &workspaceChatPlatform{
			base: p, service: s, clientID: clientID,
			requestID:    s.activeRequestID(selection.ThreadID),
			workspaceRef: selection.WorkspaceRef, threadID: selection.ThreadID,
		}
		if s.engine.handlePendingPermission(wrapped, &permissionMessage, content, runtimeKey) {
			return true
		}
	}
	if err := s.Send(ctx, clientID, msg.MessageID, selection.WorkspaceRef, selection.ThreadID, p, msg, nil); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
	}
	return true
}

func splitWorkspaceChatCommand(content string) (string, string) {
	fields := strings.Fields(content)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", ""
	}
	command := strings.ToLower(fields[0])
	return command, strings.TrimSpace(strings.TrimPrefix(content, fields[0]))
}

func parseWorkspaceChatPositiveInt(raw string, defaultValue int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%w: %q", errWorkspacePositiveNumber, raw)
	}
	return value, nil
}

func workspaceDisplayName(workspace Workspace) string {
	if workspace.RootName == "" || workspace.RootName == workspace.ProjectName {
		return workspace.ProjectName
	}
	return workspace.ProjectName + " / " + workspace.RootName
}

func threadDisplayName(thread NativeThread) string {
	if strings.TrimSpace(thread.Name) != "" {
		return thread.Name
	}
	if strings.TrimSpace(thread.Preview) != "" {
		return truncateWorkspaceChatText(thread.Preview, 48)
	}
	if len(thread.ID) > 12 {
		return thread.ID[:12]
	}
	return thread.ID
}

func workspaceChatPageBounds(total, page int) (int, int, error) {
	start := (page - 1) * workspaceChatMenuPageSize
	if start < 0 || start >= total && total > 0 {
		return 0, 0, fmt.Errorf("%w: %d", errWorkspacePageOutOfRange, page)
	}
	end := start + workspaceChatMenuPageSize
	if end > total {
		end = total
	}
	return start, end, nil
}

func (s *WorkspaceChatService) renderWorkspaceMenu(ctx context.Context, clientID string, page int) (string, error) {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return "", err
	}
	if len(workspaces) == 0 {
		return s.engine.i18n.T(MsgWorkspaceChatProjectsEmpty), nil
	}
	start, end, err := workspaceChatPageBounds(len(workspaces), page)
	if err != nil {
		return "", err
	}
	pageItems := workspaces[start:end]
	refs := make([]string, 0, len(pageItems))
	var b strings.Builder
	b.WriteString(s.engine.i18n.Tf(MsgWorkspaceChatProjectsTitle, page))
	for index, workspace := range pageItems {
		refs = append(refs, workspace.Ref)
		fmt.Fprintf(&b, "\n%d. %s", index+1, workspaceDisplayName(workspace))
		if workspace.Available {
			fmt.Fprintf(&b, "\n   `%s`", workspace.RootPath)
		} else {
			fmt.Fprintf(&b, "\n   %s: %s", s.engine.i18n.T(MsgWorkspaceChatUnavailable), workspace.Error)
		}
	}
	if err := s.SaveMenu(ctx, clientID, "projects", refs); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (s *WorkspaceChatService) renderThreadMenu(ctx context.Context, clientID string, page int) (string, error) {
	selection, err := s.Selection(ctx, clientID)
	if err != nil {
		return "", err
	}
	if selection == nil {
		return s.engine.i18n.T(MsgWorkspaceChatNoSelection), nil
	}
	threads, err := s.ListThreads(ctx, selection.WorkspaceRef)
	if err != nil {
		return "", err
	}
	if len(threads) == 0 {
		return s.engine.i18n.T(MsgWorkspaceChatThreadsEmpty), nil
	}
	start, end, err := workspaceChatPageBounds(len(threads), page)
	if err != nil {
		return "", err
	}
	pageItems := threads[start:end]
	ids := make([]string, 0, len(pageItems))
	var b strings.Builder
	b.WriteString(s.engine.i18n.Tf(MsgWorkspaceChatThreadsTitle, page))
	for index, thread := range pageItems {
		ids = append(ids, thread.ID)
		fmt.Fprintf(&b, "\n%d. %s\n   %s", index+1, threadDisplayName(thread), thread.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
	if err := s.SaveMenu(ctx, clientID, "threads", ids); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (s *WorkspaceChatService) renderCurrentSelection(ctx context.Context, clientID string) (string, error) {
	selection, err := s.Selection(ctx, clientID)
	if err != nil {
		return "", err
	}
	if selection == nil {
		return s.engine.i18n.T(MsgWorkspaceChatNoSelection), nil
	}
	workspace, err := s.resolveWorkspace(ctx, selection.WorkspaceRef)
	if err != nil {
		return "", err
	}
	thread, err := s.ReadThread(ctx, selection.WorkspaceRef, selection.ThreadID)
	if err != nil {
		return "", err
	}
	return s.engine.i18n.Tf(MsgWorkspaceChatCurrent, workspaceDisplayName(workspace), threadDisplayName(thread.NativeThread)), nil
}

func (s *WorkspaceChatService) renderNativeHistory(ctx context.Context, clientID string, count int) (string, error) {
	selection, err := s.Selection(ctx, clientID)
	if err != nil {
		return "", err
	}
	if selection == nil {
		return s.engine.i18n.T(MsgWorkspaceChatNoSelection), nil
	}
	detail, err := s.ReadThread(ctx, selection.WorkspaceRef, selection.ThreadID)
	if err != nil {
		return "", err
	}
	entries := nativeHistoryEntries(detail)
	if len(entries) == 0 {
		return s.engine.i18n.T(MsgWorkspaceChatHistoryEmpty), nil
	}
	if len(entries) > count {
		entries = entries[len(entries)-count:]
	}
	var b strings.Builder
	b.WriteString(s.engine.i18n.T(MsgWorkspaceChatHistoryTitle))
	for _, entry := range entries {
		role := MsgWorkspaceChatRoleAssistant
		if entry.role == "user" {
			role = MsgWorkspaceChatRoleUser
		}
		fmt.Fprintf(&b, "\n\n**%s**\n%s", s.engine.i18n.T(role), truncateWorkspaceChatText(entry.content, 1500))
	}
	return b.String(), nil
}

type workspaceChatHistoryEntry struct{ role, content string }

func nativeHistoryEntries(detail NativeThreadDetail) []workspaceChatHistoryEntry {
	var entries []workspaceChatHistoryEntry
	for _, turn := range detail.Turns {
		for _, raw := range turn.Items {
			var item map[string]any
			if json.Unmarshal(raw, &item) != nil {
				continue
			}
			itemType, _ := item["type"].(string)
			var role, content string
			switch itemType {
			case "userMessage":
				role, content = "user", workspaceChatItemText(item)
			case "agentMessage":
				role, content = "assistant", workspaceChatItemText(item)
			}
			if strings.TrimSpace(content) != "" {
				entries = append(entries, workspaceChatHistoryEntry{role: role, content: content})
			}
		}
	}
	return entries
}

func workspaceChatItemText(item map[string]any) string {
	if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
		return text
	}
	if content, ok := item["content"].([]any); ok {
		var parts []string
		for _, value := range content {
			if block, ok := value.(map[string]any); ok {
				if text, _ := block["text"].(string); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func truncateWorkspaceChatText(value string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "..."
}

func (s *WorkspaceChatService) replyWorkspaceChat(p Platform, msg *Message, content string) {
	if err := p.Reply(s.ctx, msg.ReplyCtx, content); err != nil {
		slog.Error("workspace chat reply failed", "platform", p.Name(), "user_id", msg.UserID, "error", err)
	}
}

func (s *WorkspaceChatService) replyWorkspaceChatError(p Platform, msg *Message, err error) {
	switch {
	case errors.Is(err, errWorkspaceMenuInvalidIndex):
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatInvalidIndex))
		return
	case errors.Is(err, errWorkspacePositiveNumber):
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatInvalidNumber))
		return
	case errors.Is(err, errWorkspacePageOutOfRange):
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatPageOutOfRange))
		return
	case errors.Is(err, errWorkspaceMenuItemNotFound):
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatMenuExpired))
		return
	case errors.Is(err, ErrWorkspaceTurnNotRunning):
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatTurnNotRunning))
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgError, err))
}

type workspaceChatPlatform struct {
	base         Platform
	service      *WorkspaceChatService
	clientID     string
	requestID    string
	workspaceRef string
	threadID     string
	errMu        sync.Mutex
	lastAgentErr error
}

func (p *workspaceChatPlatform) Name() string {
	if p.base == nil {
		return "workspace-web"
	}
	return p.base.Name()
}
func (p *workspaceChatPlatform) Start(MessageHandler) error { return nil }
func (p *workspaceChatPlatform) Stop() error                { return nil }

func (p *workspaceChatPlatform) emit(eventType, content string) {
	p.service.publish(WorkspaceChatEvent{Type: eventType, RequestID: p.requestID, ClientID: p.clientID, WorkspaceRef: p.workspaceRef, ThreadID: p.threadID, Content: content, OccurredAt: time.Now()})
}
func (p *workspaceChatPlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	p.emit("message", content)
	if p.base == nil {
		return nil
	}
	return p.base.Reply(ctx, replyCtx, content)
}
func (p *workspaceChatPlatform) Send(ctx context.Context, replyCtx any, content string) error {
	p.emit("message", content)
	if p.base == nil {
		return nil
	}
	return p.base.Send(ctx, replyCtx, content)
}
func (p *workspaceChatPlatform) SendImage(ctx context.Context, replyCtx any, image ImageAttachment) error {
	sender, ok := p.base.(ImageSender)
	if !ok {
		return ErrNotSupported
	}
	return sender.SendImage(ctx, replyCtx, image)
}
func (p *workspaceChatPlatform) SendFile(ctx context.Context, replyCtx any, file FileAttachment) error {
	sender, ok := p.base.(FileSender)
	if !ok {
		return ErrNotSupported
	}
	return sender.SendFile(ctx, replyCtx, file)
}
func (p *workspaceChatPlatform) UpdateMessage(ctx context.Context, replyCtx any, content string) error {
	p.emit("message_update", content)
	if updater, ok := p.base.(MessageUpdater); ok {
		return updater.UpdateMessage(ctx, replyCtx, content)
	}
	return nil
}
func (p *workspaceChatPlatform) StartTyping(ctx context.Context, replyCtx any) func() {
	p.emit("typing_start", "")
	var stopBase func()
	if typing, ok := p.base.(TypingIndicator); ok {
		stopBase = typing.StartTyping(ctx, replyCtx)
	}
	return func() {
		if stopBase != nil {
			stopBase()
		}
		p.emit("typing_stop", "")
	}
}

func (p *workspaceChatPlatform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]ButtonOption) error {
	payload, err := json.Marshal(map[string]any{"content": content, "buttons": buttons})
	if err != nil {
		return err
	}
	p.service.publish(WorkspaceChatEvent{
		Type: "approval_requested", RequestID: p.requestID, ClientID: p.clientID,
		WorkspaceRef: p.workspaceRef, ThreadID: p.threadID, Content: content,
		Payload: payload, OccurredAt: time.Now(),
	})
	if p.base == nil {
		return nil
	}
	if sender, ok := p.base.(InlineButtonSender); ok {
		return sender.SendWithButtons(ctx, replyCtx, content, buttons)
	}
	return p.base.Send(ctx, replyCtx, content)
}

func (p *workspaceChatPlatform) PublishWorkspaceAgentEvent(event Event) {
	if event.Type == EventError && event.Error != nil {
		p.errMu.Lock()
		p.lastAgentErr = event.Error
		p.errMu.Unlock()
	}
	payload, err := json.Marshal(map[string]any{
		"event_type": event.Type, "content": event.Content,
		"tool_name": event.ToolName, "tool_input": event.ToolInput,
		"tool_result": event.ToolResult, "tool_status": event.ToolStatus,
		"request_id": event.RequestID, "done": event.Done,
		"error": workspaceChatEventError(event.Error),
	})
	if err != nil {
		p.emit("agent_event_error", err.Error())
		return
	}
	p.service.publish(WorkspaceChatEvent{
		Type: "agent_event", RequestID: p.requestID, ClientID: p.clientID,
		WorkspaceRef: p.workspaceRef, ThreadID: p.threadID, Payload: payload, OccurredAt: time.Now(),
	})
}

func (p *workspaceChatPlatform) agentError() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.lastAgentErr
}

func workspaceChatEventError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
