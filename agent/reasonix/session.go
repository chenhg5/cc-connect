package reasonix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// ── Wire types (mirrors reasonix/internal/serve/wire.go) ──────────

type wireEvent struct {
	Kind      string          `json:"kind"`
	Text      string          `json:"text,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	Level     string          `json:"level,omitempty"`
	Tool      *wireTool       `json:"tool,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	Approval  *wireApproval   `json:"approval,omitempty"`
	Ask       *wireAsk        `json:"ask,omitempty"`
	Err       string          `json:"err,omitempty"`
}

type wireTool struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Args     string `json:"args,omitempty"`
	Output   string `json:"output,omitempty"`
	Err      string `json:"err,omitempty"`
	ReadOnly bool   `json:"readOnly"`
}

type wireApproval struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Subject string `json:"subject"`
}

type wireAsk struct {
	ID        string           `json:"id"`
	Questions []wireAskQuestion `json:"questions"`
}

type wireAskQuestion struct {
	ID      string          `json:"id"`
	Header  string          `json:"header,omitempty"`
	Prompt  string          `json:"prompt"`
	Options []wireAskOption `json:"options"`
	Multi   bool            `json:"multi,omitempty"`
}

type wireAskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// ── Session ──────────────────────────────────────────────────────

// reasonixSession manages a single conversation session with reasonix serve.
// It maintains one persistent SSE connection and turns are driven by Send().
type reasonixSession struct {
	serveURL  string
	workDir   string
	sessionID string
	mode      string

	ctx    context.Context
	cancel context.CancelFunc

	events   chan core.Event
	alive    atomic.Bool
	closeOnce sync.Once

	// SSE reader state
	mu           sync.Mutex
	sseClient    *http.Client
	sseCancel    context.CancelFunc
	readLoopDone chan struct{}

	// Turn synchronization
	turnDone      chan struct{} // signaled when turn_done event received
	errTurn       error        // error from turn_done, read after turnDone
	inTurn        atomic.Bool  // true while a turn is in progress

	// Pending approval tracking
	pendingApprovalID string

	// Reconnect tracking
	maxReconnects int
	reconnectCount int

	// Thinking accumulator — buffers incremental reasoning chunks
	thinkingBuf strings.Builder
}

func newSession(ctx context.Context, serveURL, workDir, sessionID, mode string) (*reasonixSession, error) {
	ctx, cancel := context.WithCancel(ctx)

	s := &reasonixSession{
		serveURL:     serveURL,
		workDir:      workDir,
		sessionID:    sessionID,
		mode:         mode,
		ctx:          ctx,
		cancel:       cancel,
		events:       make(chan core.Event, 128),
		turnDone:     make(chan struct{}, 1),
		readLoopDone: make(chan struct{}),
		maxReconnects: 5,
		sseClient: &http.Client{
			Timeout: 0, // no timeout for SSE
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
	s.alive.Store(true)

	// Start fresh session on reasonix if sessionID is empty or 'new'
	if sessionID == "" || sessionID == "new" {
		if err := s.httpPost("/new", nil); err != nil {
			cancel()
			return nil, fmt.Errorf("reasonix: new session: %w", err)
		}
		slog.Info("reasonix: created new session")
	}

	// Start SSE reader in background
	readerCtx, readerCancel := context.WithCancel(ctx)
	s.sseCancel = readerCancel
	go s.readLoop(readerCtx)

	return s, nil
}

// ── core.AgentSession implementation ─────────────────────────────

func (s *reasonixSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("reasonix: session is closed")
	}

	// Save images/files to disk and append references
	if len(images) > 0 {
		prompt = prompt + "\n\n[Attached images: " + formatImages(images) + "]"
	}
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}

	// Handle special commands locally
	if strings.TrimSpace(prompt) == "/compact" {
		return s.httpPost("/compact", nil)
	}

	// Submit to reasonix
	s.inTurn.Store(true)
	body := map[string]string{"input": prompt}
	if err := s.httpPost("/submit", body); err != nil {
		s.inTurn.Store(false)
		return fmt.Errorf("reasonix: submit: %w", err)
	}

	// Wait for turn to complete
	<-s.turnDone

	s.mu.Lock()
	err := s.errTurn
	s.errTurn = nil
	s.mu.Unlock()

	return err
}

func (s *reasonixSession) RespondPermission(requestID string, result core.PermissionResult) error {
	body := map[string]any{
		"id":      requestID,
		"allow":   result.Behavior == "allow",
		"session": false,
	}
	return s.httpPost("/approve", body)
}

func (s *reasonixSession) Events() <-chan core.Event {
	return s.events
}

func (s *reasonixSession) CurrentSessionID() string {
	// We don't track reasonix's internal session ID; use cc-connect's session ID.
	return s.sessionID
}

func (s *reasonixSession) Alive() bool {
	return s.alive.Load()
}

func (s *reasonixSession) Close() error {
	s.closeOnce.Do(func() {
		s.alive.Store(false)
		s.sseCancel()
		<-s.readLoopDone
		s.cancel()
		slog.Info("reasonix: session closed")
	})
	return nil
}

// ── SSE read loop ────────────────────────────────────────────────

// readLoop maintains a persistent SSE connection to reasonix serve.
// If the connection drops, it automatically retries with exponential backoff.
func (s *reasonixSession) readLoop(ctx context.Context) {
	defer close(s.readLoopDone)

	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			s.alive.Store(false)
			return
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", s.serveURL+"/events", nil)
		if err != nil {
			slog.Error("reasonix: create SSE request failed", "error", err)
			return
		}

		resp, err := s.sseClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				s.alive.Store(false)
				return
			}
			slog.Warn("reasonix: SSE connect failed, retrying", "error", err, "backoff", backoff)
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("SSE connect: %w (reconnecting)", err)})
			goto retryWait
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			slog.Warn("reasonix: SSE unexpected status, retrying", "status", resp.StatusCode, "backoff", backoff)
			goto retryWait
		}

		slog.Info("reasonix: SSE connected")
		s.alive.Store(true)
		backoff = 1 * time.Second  // reset backoff on successful connection
		s.reconnectCount = 0       // reset reconnect count on successful connection
		s.readSSE(ctx, resp.Body)
		_ = resp.Body.Close()

		// Connection dropped; if context is still alive, reconnect
		if ctx.Err() != nil {
			s.alive.Store(false)
			return
		}
		// If a turn is in progress, unblock Send() with a reconnect error
		if s.inTurn.Load() {
			s.mu.Lock()
			s.errTurn = fmt.Errorf("reasonix: SSE disconnected during turn")
			s.mu.Unlock()
			s.emit(core.Event{Type: core.EventResult, Done: true, Error: fmt.Errorf("SSE disconnected, reconnecting")})
			s.inTurn.Store(false)
			s.turnDone <- struct{}{}
		}
		slog.Warn("reasonix: SSE connection lost, reconnecting", "backoff", backoff)

	retryWait:
		s.reconnectCount++
		if s.reconnectCount >= s.maxReconnects {
			slog.Error("reasonix: SSE reconnect limit reached, closing session", "max", s.maxReconnects)
			s.alive.Store(false)
			if s.inTurn.Load() {
				s.inTurn.Store(false)
				s.turnDone <- struct{}{}
			}
			return
		}
		select {
		case <-ctx.Done():
			s.alive.Store(false)
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

func (s *reasonixSession) readSSE(ctx context.Context, r io.Reader) {
	br := bufio.NewReaderSize(r, 65536)
	var dataBuf bytes.Buffer

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := br.ReadString('\n')
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				slog.Warn("reasonix: SSE read error", "error", err)
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if strings.HasPrefix(line, "data: ") {
			dataBuf.WriteString(strings.TrimPrefix(line, "data: "))
		} else if line == "" && dataBuf.Len() > 0 {
			// Empty line = end of one SSE event
			s.dispatchEvent(dataBuf.Bytes())
			dataBuf.Reset()
		}
	}
}

func (s *reasonixSession) dispatchEvent(data []byte) {
	var we wireEvent
	if err := json.Unmarshal(data, &we); err != nil {
		slog.Debug("reasonix: SSE parse error", "error", err)
		return
	}

	if s.ctx.Err() != nil {
		return
	}

	switch we.Kind {
	case "turn_started":
		// No-op

	case "reasoning":
		// Accumulate reasoning chunks; flush at the next meaningful event
		s.mu.Lock()
		s.thinkingBuf.WriteString(we.Reasoning)
		s.mu.Unlock()

	case "phase":
		// Phase events are high-level status ("正在思考", "正在编辑文件")
		// Flush accumulated reasoning first, then emit the phase
		s.flushThinking()
		text := we.Reasoning
		if text == "" {
			text = we.Text
		}
		s.emit(core.Event{Type: core.EventThinking, Content: text})

	case "text", "message":
		s.flushThinking()
		s.emit(core.Event{Type: core.EventText, Content: we.Text})

	case "tool_dispatch":
		s.flushThinking()
		s.emit(core.Event{
			Type:     core.EventToolUse,
			ToolName: we.Tool.Name,
			Content:  we.Tool.Args,
		})

	case "tool_result":
		status := "completed"
		if we.Tool.Err != "" {
			status = "failed"
		}
		s.emit(core.Event{
			Type:       core.EventToolResult,
			ToolName:   we.Tool.Name,
			ToolResult: we.Tool.Output,
			ToolStatus: status,
		})

	case "approval_request":
		s.flushThinking()
		s.mu.Lock()
		s.pendingApprovalID = we.Approval.ID
		s.mu.Unlock()
		s.emit(core.Event{
			Type:      core.EventPermissionRequest,
			RequestID: we.Approval.ID,
			Content:   we.Approval.Subject,
			ToolName:  we.Approval.Tool,
		})

	case "ask_request":
		s.flushThinking()
		var qs []core.UserQuestion
		for _, q := range we.Ask.Questions {
			opts := make([]core.UserQuestionOption, len(q.Options))
			for i, o := range q.Options {
				opts[i] = core.UserQuestionOption{Label: o.Label, Description: o.Description}
			}
			qs = append(qs, core.UserQuestion{
				Question:    q.Prompt,
				Header:      q.Header,
				Options:     opts,
				MultiSelect: q.Multi,
			})
		}
		s.emit(core.Event{
			Type:      core.EventPermissionRequest,
			RequestID: we.Ask.ID,
			Questions: qs,
		})

	case "turn_done":
		s.flushThinking()
		s.inTurn.Store(false)
		s.mu.Lock()
		if we.Err != "" {
			s.errTurn = fmt.Errorf("%s", we.Err)
		}
		errTurn := s.errTurn
		s.mu.Unlock()

		s.emit(core.Event{Type: core.EventResult, Done: true, Error: errTurn})
		s.turnDone <- struct{}{}

	case "notice":
		s.flushThinking()
		s.emit(core.Event{Type: core.EventText, Content: "[Notice] " + we.Text})

	case "usage", "compaction_started", "compaction_done":
		// Informational, no user-visible event needed

	default:
		slog.Debug("reasonix: unhandled event", "kind", we.Kind)
	}
}

// ── Helpers ──────────────────────────────────────────────────────

// emit sends an event to the engine, handling backpressure and context cancellation.
func (s *reasonixSession) emit(evt core.Event) {
	select {
	case s.events <- evt:
	case <-s.ctx.Done():
	}
}

// flushThinking emits accumulated reasoning text as a single EventThinking
// and resets the buffer. Safe to call multiple times — no-op when buffer is empty.
func (s *reasonixSession) flushThinking() {
	s.mu.Lock()
	text := s.thinkingBuf.String()
	s.thinkingBuf.Reset()
	s.mu.Unlock()

	if text == "" {
		return
	}
	s.emit(core.Event{Type: core.EventThinking, Content: text})
}

// httpPost sends a JSON POST request to the reasonix serve endpoint.
func (s *reasonixSession) httpPost(path string, body any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("reasonix: marshal body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(s.ctx, "POST", s.serveURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("reasonix: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reasonix: POST %s: %w", path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("reasonix: POST close body", "path", path, "error", err)
		}
	}()

	if resp.StatusCode >= 400 {
		// Include response body (first 512 bytes) in error for debugging.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("reasonix: POST %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return nil
}

// formatImages builds a comma-separated list of image filenames for inclusion
// in the prompt. Reasons adopts the standard cc-connect file-save pattern so
// the actual image bytes land on disk (via core.SaveFilesToDisk); this list
// gives reasonix serve a human-readable hint about which images were attached.
func formatImages(images []core.ImageAttachment) string {
	names := make([]string, len(images))
	for i, img := range images {
		if img.FileName != "" {
			names[i] = img.FileName
		} else {
			names[i] = fmt.Sprintf("image_%d", i)
		}
	}
	return strings.Join(names, ", ")
}

// Static interface assertion — ensure reasonixSession satisfies core.AgentSession.
var _ core.AgentSession = (*reasonixSession)(nil)
