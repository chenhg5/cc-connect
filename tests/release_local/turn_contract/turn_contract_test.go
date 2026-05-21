package turn_contract

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type turnRecord struct {
	prompt string
	images []core.ImageAttachment
	files  []core.FileAttachment
}

type turnAgent struct {
	session *turnSession
	model   string
	workDir string
}

func newTurnAgent() *turnAgent {
	return &turnAgent{session: newTurnSession()}
}

func (a *turnAgent) Name() string { return "turn-agent" }
func (a *turnAgent) GetModel() string {
	return strings.TrimSpace(a.model)
}
func (a *turnAgent) GetWorkDir() string {
	return strings.TrimSpace(a.workDir)
}

func (a *turnAgent) StartSession(_ context.Context, sessionID string) (core.AgentSession, error) {
	a.session.setID(sessionID)
	return a.session, nil
}

func (a *turnAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) { return nil, nil }
func (a *turnAgent) Stop() error                                                     { return a.session.Close() }

type turnSession struct {
	mu         sync.Mutex
	id         string
	alive      bool
	records    []turnRecord
	events     chan core.Event
	blockFirst bool
	blocked    bool
	result     core.Event
	permCalls  []permissionCall
}

type permissionCall struct {
	requestID string
	result    core.PermissionResult
}

func newTurnSession() *turnSession {
	return &turnSession{
		alive:  true,
		events: make(chan core.Event, 32),
		result: core.Event{Type: core.EventResult, Content: "turn ok", Done: true},
	}
}

func (s *turnSession) setID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
}

func (s *turnSession) setResult(event core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = event
}

func (s *turnSession) blockFirstResult() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockFirst = true
}

func (s *turnSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return errors.New("session closed")
	}
	s.records = append(s.records, turnRecord{
		prompt: prompt,
		images: append([]core.ImageAttachment(nil), images...),
		files:  append([]core.FileAttachment(nil), files...),
	})
	if s.blockFirst && len(s.records) == 1 {
		s.blocked = true
		return nil
	}
	s.events <- s.result
	return nil
}

func (s *turnSession) Events() <-chan core.Event { return s.events }
func (s *turnSession) RespondPermission(requestID string, result core.PermissionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permCalls = append(s.permCalls, permissionCall{requestID: requestID, result: result})
	return nil
}
func (s *turnSession) CurrentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
func (s *turnSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}
func (s *turnSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return nil
	}
	s.alive = false
	close(s.events)
	return nil
}

func (s *turnSession) releaseFirstResult(event core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.blocked {
		return
	}
	s.events <- event
	s.blocked = false
}

func (s *turnSession) emit(event core.Event) {
	s.events <- event
}

func (s *turnSession) permissionCalls() []permissionCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]permissionCall, len(s.permCalls))
	copy(out, s.permCalls)
	return out
}

func (s *turnSession) waitRecords(t *testing.T, n int) []turnRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.records) >= n {
			out := append([]turnRecord(nil), s.records...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("timeout waiting for %d agent sends, got %d: %#v", n, len(s.records), s.records)
	return nil
}

type turnPlatform struct {
	mu       sync.Mutex
	texts    []string
	images   []core.ImageAttachment
	files    []core.FileAttachment
	replyCtx []any
	buttons  [][][]core.ButtonOption
}

func (p *turnPlatform) Name() string { return "turn" }
func (p *turnPlatform) Start(core.MessageHandler) error {
	return nil
}
func (p *turnPlatform) Stop() error { return nil }
func (p *turnPlatform) Reply(_ context.Context, replyCtx any, content string) error {
	return p.Send(context.Background(), replyCtx, content)
}
func (p *turnPlatform) Send(_ context.Context, replyCtx any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts = append(p.texts, content)
	p.replyCtx = append(p.replyCtx, replyCtx)
	return nil
}
func (p *turnPlatform) SendWithButtons(_ context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts = append(p.texts, content)
	p.replyCtx = append(p.replyCtx, replyCtx)
	p.buttons = append(p.buttons, buttons)
	return nil
}
func (p *turnPlatform) SendImage(_ context.Context, replyCtx any, img core.ImageAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.images = append(p.images, img)
	p.replyCtx = append(p.replyCtx, replyCtx)
	return nil
}
func (p *turnPlatform) SendFile(_ context.Context, replyCtx any, file core.FileAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files = append(p.files, file)
	p.replyCtx = append(p.replyCtx, replyCtx)
	return nil
}

func (p *turnPlatform) snapshot() (texts []string, images []core.ImageAttachment, files []core.FileAttachment, replyCtx []any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...),
		append([]core.ImageAttachment(nil), p.images...),
		append([]core.FileAttachment(nil), p.files...),
		append([]any(nil), p.replyCtx...)
}

func (p *turnPlatform) waitTextContaining(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		texts, _, _, _ := p.snapshot()
		for _, text := range texts {
			if strings.Contains(text, substr) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, _, _, _ := p.snapshot()
	t.Fatalf("timeout waiting for text containing %q, got %#v", substr, texts)
}

func newTurnEngine(t *testing.T) (*core.Engine, *turnAgent, *turnPlatform) {
	t.Helper()
	agent := newTurnAgent()
	platform := &turnPlatform{}
	engine := core.NewEngine("release-turn", agent, []core.Platform{platform}, t.TempDir()+"/sessions.json", core.LangEnglish)
	t.Cleanup(func() {
		engine.Stop()
		_ = agent.Stop()
	})
	return engine, agent, platform
}

func turnMessage(content string) *core.Message {
	return &core.Message{
		SessionKey: "turn:chat-1:user-1",
		Platform:   "turn",
		UserID:     "user-1",
		UserName:   "tester",
		Content:    content,
		ReplyCtx:   "reply-ctx-1",
	}
}

func TestBasicUserTurnContractAcrossInputModalities(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		images     []core.ImageAttachment
		files      []core.FileAttachment
		wantPrompt string
	}{
		{name: "text", content: "plain request", wantPrompt: "plain request"},
		{name: "image_only", images: []core.ImageAttachment{{MimeType: "image/png", FileName: "chart.png", Data: []byte("png")}}},
		{name: "file_only", files: []core.FileAttachment{{MimeType: "text/plain", FileName: "notes.txt", Data: []byte("notes")}}},
		{
			name:       "text_image_file",
			content:    "inspect these",
			images:     []core.ImageAttachment{{MimeType: "image/jpeg", FileName: "photo.jpg", Data: []byte("jpg")}},
			files:      []core.FileAttachment{{MimeType: "application/pdf", FileName: "spec.pdf", Data: []byte("pdf")}},
			wantPrompt: "inspect these",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, agent, platform := newTurnEngine(t)
			agent.session.setResult(core.Event{Type: core.EventResult, Content: "final answer", InputTokens: 52000, Done: true})

			msg := turnMessage(tt.content)
			msg.Images = tt.images
			msg.Files = tt.files
			engine.ReceiveMessage(platform, msg)

			records := agent.session.waitRecords(t, 1)
			if tt.wantPrompt != "" && !strings.Contains(records[0].prompt, tt.wantPrompt) {
				t.Fatalf("prompt = %q, want %q", records[0].prompt, tt.wantPrompt)
			}
			if len(records[0].images) != len(tt.images) {
				t.Fatalf("images = %#v, want %#v", records[0].images, tt.images)
			}
			if len(records[0].files) != len(tt.files) {
				t.Fatalf("files = %#v, want %#v", records[0].files, tt.files)
			}

			platform.waitTextContaining(t, "final answer")
			texts, _, _, _ := platform.snapshot()
			if len(texts) != 1 {
				t.Fatalf("texts = %#v, want exactly one final reply", texts)
			}
			if strings.Count(texts[0], "[ctx:") != 1 {
				t.Fatalf("final reply = %q, want exactly one context indicator", texts[0])
			}
		})
	}
}

func TestSideChannelEchoContractAcrossOutboundModalities(t *testing.T) {
	tests := []struct {
		name   string
		images []core.ImageAttachment
		files  []core.FileAttachment
	}{
		{name: "text_only"},
		{name: "text_image", images: []core.ImageAttachment{{MimeType: "image/png", FileName: "chart.png", Data: []byte("png")}}},
		{name: "text_file", files: []core.FileAttachment{{MimeType: "text/plain", FileName: "report.txt", Data: []byte("report")}}},
		{
			name:   "text_image_file",
			images: []core.ImageAttachment{{MimeType: "image/png", FileName: "chart.png", Data: []byte("png")}},
			files:  []core.FileAttachment{{MimeType: "application/pdf", FileName: "report.pdf", Data: []byte("pdf")}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, agent, platform := newTurnEngine(t)
			agent.session.blockFirstResult()

			msg := turnMessage("start work")
			engine.ReceiveMessage(platform, msg)
			agent.session.waitRecords(t, 1)

			sideText := "delivery ready"
			if err := engine.SendToSessionWithAttachments(msg.SessionKey, sideText, tt.images, tt.files); err != nil {
				t.Fatalf("SendToSessionWithAttachments() error = %v", err)
			}
			agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: sideText, InputTokens: 52000, Done: true})
			assertStableSideChannelOnly(t, platform, sideText)
		})
	}
}

func TestSideChannelDifferentFinalContract(t *testing.T) {
	engine, agent, platform := newTurnEngine(t)
	agent.session.blockFirstResult()

	msg := turnMessage("start work")
	engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)

	sideText := "delivery ready"
	if err := engine.SendToSessionWithAttachments(msg.SessionKey, sideText, nil, nil); err != nil {
		t.Fatalf("SendToSessionWithAttachments() error = %v", err)
	}

	agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: "separate final answer", InputTokens: 52000, Done: true})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		texts, _, _, _ := platform.snapshot()
		if len(texts) >= 2 {
			if !containsText(texts, sideText) || !containsText(texts, "separate final answer") {
				t.Fatalf("texts = %#v, want side-channel and distinct final reply", texts)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, _, _, _ := platform.snapshot()
	t.Fatalf("texts = %#v, want side-channel plus distinct final reply", texts)
}

func TestThinkingAndToolEventsContract(t *testing.T) {
	engine, agent, platform := newTurnEngine(t)
	engine.SetDisplayConfig(core.DisplayCfg{
		Mode:             "full",
		ThinkingMessages: true,
		ToolMessages:     true,
		ThinkingMaxLen:   300,
		ToolMaxLen:       500,
	})
	agent.session.blockFirstResult()

	msg := turnMessage("run a tool")
	go engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)

	agent.session.emit(core.Event{Type: core.EventThinking, Content: "planning the command"})
	agent.session.emit(core.Event{Type: core.EventToolUse, ToolName: "Bash", ToolInput: "echo tool-output"})
	agent.session.emit(core.Event{Type: core.EventToolResult, ToolName: "Bash", ToolResult: "tool-output", ToolStatus: "completed"})
	agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: "final answer", InputTokens: 52000, Done: true})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		texts, _, _, _ := platform.snapshot()
		joined := strings.Join(texts, "\n")
		if strings.Contains(joined, "planning the command") &&
			strings.Contains(joined, "Bash") &&
			strings.Contains(joined, "tool-output") &&
			strings.Contains(joined, "final answer") {
			if countContaining(texts, "final answer") != 1 {
				t.Fatalf("texts = %#v, want exactly one final answer", texts)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, _, _, _ := platform.snapshot()
	t.Fatalf("texts = %#v, want thinking, tool use/result, and final answer", texts)
}

func TestHiddenToolEventsContractKeepsFinalAndHidesToolDetails(t *testing.T) {
	engine, agent, platform := newTurnEngine(t)
	engine.SetDisplayConfig(core.DisplayCfg{
		Mode:             "full",
		ThinkingMessages: true,
		ToolMessages:     false,
		ThinkingMaxLen:   300,
		ToolMaxLen:       500,
	})
	agent.session.blockFirstResult()

	msg := turnMessage("run a hidden tool")
	go engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)

	agent.session.emit(core.Event{Type: core.EventThinking, Content: "planning hidden work"})
	agent.session.emit(core.Event{Type: core.EventToolUse, ToolName: "Bash", ToolInput: "cat secret.txt"})
	agent.session.emit(core.Event{Type: core.EventToolResult, ToolName: "Bash", ToolResult: "secret-output", ToolStatus: "completed"})
	agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: "final answer", InputTokens: 52000, Done: true})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		texts, _, _, _ := platform.snapshot()
		joined := strings.Join(texts, "\n")
		if strings.Contains(joined, "final answer") {
			if strings.Contains(joined, "Bash") || strings.Contains(joined, "cat secret.txt") || strings.Contains(joined, "secret-output") {
				t.Fatalf("hidden tool details leaked to platform: %#v", texts)
			}
			if countContaining(texts, "final answer") != 1 {
				t.Fatalf("texts = %#v, want exactly one final answer", texts)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, _, _, _ := platform.snapshot()
	t.Fatalf("texts = %#v, want final answer even when tool messages are hidden", texts)
}

func TestPermissionInteractionContractWhileAgentSendIsBlocked(t *testing.T) {
	engine, agent, platform := newTurnEngine(t)
	agent.session.blockFirstResult()

	msg := turnMessage("write a file")
	go engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)

	agent.session.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    "req-write",
		ToolName:     "write_file",
		ToolInput:    "/tmp/contract.txt",
		ToolInputRaw: map[string]any{"path": "/tmp/contract.txt"},
	})
	platform.waitTextContaining(t, "write_file")

	engine.ReceiveMessage(platform, &core.Message{
		SessionKey: msg.SessionKey,
		Platform:   msg.Platform,
		UserID:     msg.UserID,
		UserName:   msg.UserName,
		Content:    "allow",
		ReplyCtx:   "reply-ctx-allow",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls := agent.session.permissionCalls()
		if len(calls) > 0 {
			if len(calls) != 1 {
				t.Fatalf("permission calls = %#v, want exactly one", calls)
			}
			if calls[0].requestID != "req-write" || calls[0].result.Behavior != "allow" {
				t.Fatalf("permission call = %#v, want allow for req-write", calls[0])
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(agent.session.permissionCalls()) != 1 {
		t.Fatalf("permission calls = %#v, want one allow response", agent.session.permissionCalls())
	}

	agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: "write complete", InputTokens: 52000, Done: true})
	platform.waitTextContaining(t, "write complete")

	records := agent.session.waitRecords(t, 1)
	if len(records) != 1 {
		t.Fatalf("agent sends = %#v, permission response should not start a second user turn", records)
	}
	texts, _, _, _ := platform.snapshot()
	if countContaining(texts, "write complete") != 1 {
		t.Fatalf("texts = %#v, want exactly one final write completion", texts)
	}
}

func TestStreamingPreviewFinalizationContractExposesDuplicateFinalSend(t *testing.T) {
	agent := newTurnAgent()
	platform := &previewLifecyclePlatform{}
	engine := core.NewEngine("release-preview", agent, []core.Platform{platform}, t.TempDir()+"/sessions.json", core.LangEnglish)
	t.Cleanup(func() {
		engine.Stop()
		_ = agent.Stop()
	})
	agent.session.blockFirstResult()

	msg := turnMessage("produce a long direct response")
	go engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)

	previewText := strings.Repeat("preview content ", 20)
	agent.session.emit(core.Event{Type: core.EventText, Content: previewText})
	platform.waitPreviewStarts(t, 1)

	agent.session.releaseFirstResult(core.Event{
		Type:        core.EventResult,
		Content:     previewText,
		InputTokens: 52000,
		Done:        true,
	})

	platform.waitPreviewUpdates(t, 1)
	texts, starts, updates, deletes := platform.snapshotPreviewLifecycle()
	if len(texts) != 0 || len(starts) != 1 || len(updates) == 0 || len(deletes) != 0 {
		t.Fatalf(
			"streaming preview finalization violated: normal final text was sent separately while preview remained active\ntexts=%#v\npreview_starts=%#v\npreview_updates=%#v\npreview_deletes=%#v",
			texts, starts, updates, deletes,
		)
	}
}

func TestStreamingPreviewConfigurationMatrix(t *testing.T) {
	tests := []struct {
		name        string
		cfg         core.StreamPreviewCfg
		wantPreview bool
	}{
		{
			name: "enabled_keeps_preview_in_place",
			cfg: core.StreamPreviewCfg{
				Enabled:       true,
				IntervalMs:    1,
				MinDeltaChars: 1,
				MaxChars:      5000,
			},
			wantPreview: true,
		},
		{
			name: "disabled_globally_sends_final_once",
			cfg: core.StreamPreviewCfg{
				Enabled:       false,
				IntervalMs:    1,
				MinDeltaChars: 1,
				MaxChars:      5000,
			},
		},
		{
			name: "disabled_for_platform_sends_final_once",
			cfg: core.StreamPreviewCfg{
				Enabled:           true,
				DisabledPlatforms: []string{"feishu"},
				IntervalMs:        1,
				MinDeltaChars:     1,
				MaxChars:          5000,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := newTurnAgent()
			platform := &previewLifecyclePlatform{}
			engine := core.NewEngine("release-preview-matrix", agent, []core.Platform{platform}, t.TempDir()+"/sessions.json", core.LangEnglish)
			engine.SetStreamPreviewCfg(tt.cfg)
			t.Cleanup(func() {
				engine.Stop()
				_ = agent.Stop()
			})
			agent.session.blockFirstResult()

			msg := turnMessage("streaming config matrix")
			go engine.ReceiveMessage(platform, msg)
			agent.session.waitRecords(t, 1)

			body := strings.Repeat("stream body ", 20)
			agent.session.emit(core.Event{Type: core.EventText, Content: body})
			agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: body, InputTokens: 52000, Done: true})

			if tt.wantPreview {
				platform.waitPreviewUpdates(t, 1)
			} else {
				platform.waitSentTexts(t, 1)
			}

			texts, starts, updates, deletes := platform.snapshotPreviewLifecycle()
			if tt.wantPreview {
				if len(texts) != 0 || len(starts) != 1 || len(updates) == 0 || len(deletes) != 0 {
					t.Fatalf("preview lifecycle = texts:%#v starts:%#v updates:%#v deletes:%#v, want in-place preview finalize", texts, starts, updates, deletes)
				}
				if !strings.Contains(updates[len(updates)-1], "[ctx:") {
					t.Fatalf("final preview update = %q, want context indicator", updates[len(updates)-1])
				}
				return
			}
			if len(starts) != 0 || len(updates) != 0 || len(deletes) != 0 {
				t.Fatalf("preview lifecycle = starts:%#v updates:%#v deletes:%#v, want no preview when disabled", starts, updates, deletes)
			}
			if len(texts) != 1 || !strings.Contains(texts[0], strings.TrimSpace(body)) || strings.Count(texts[0], "[ctx:") != 1 {
				t.Fatalf("texts = %#v, want one final send with context indicator", texts)
			}
		})
	}
}

func TestStreamingPreviewMaxCharsOnlyTruncatesIntermediatePreview(t *testing.T) {
	agent := newTurnAgent()
	platform := &previewLifecyclePlatform{}
	engine := core.NewEngine("release-preview-maxchars", agent, []core.Platform{platform}, t.TempDir()+"/sessions.json", core.LangEnglish)
	engine.SetStreamPreviewCfg(core.StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    1,
		MinDeltaChars: 1,
		MaxChars:      20,
	})
	t.Cleanup(func() {
		engine.Stop()
		_ = agent.Stop()
	})
	agent.session.blockFirstResult()

	msg := turnMessage("stream max chars")
	go engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)

	body := strings.Repeat("full final body ", 30)
	agent.session.emit(core.Event{Type: core.EventText, Content: body})
	platform.waitPreviewStarts(t, 1)
	agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: body, InputTokens: 52000, Done: true})
	platform.waitPreviewUpdates(t, 1)

	_, starts, updates, _ := platform.snapshotPreviewLifecycle()
	if !strings.Contains(starts[0], "…") {
		t.Fatalf("initial preview = %q, want max_chars truncation marker", starts[0])
	}
	final := updates[len(updates)-1]
	if !strings.Contains(final, strings.TrimSpace(body)) {
		t.Fatalf("final preview update = %q, want full untruncated final body", final)
	}
}

func TestReplyMetadataConfigurationMatrix(t *testing.T) {
	tests := []struct {
		name       string
		showCtx    bool
		showFooter bool
		want       []string
		forbid     []string
	}{
		{
			name:       "context_and_footer_on_share_one_line",
			showCtx:    true,
			showFooter: true,
			want:       []string{"answer", "*[ctx: ~14%] · glm-5.1 · …/tmp/release-agent*"},
		},
		{
			name:       "context_off_footer_on_keeps_model_footer",
			showCtx:    false,
			showFooter: true,
			want:       []string{"answer", "*glm-5.1 · …/tmp/release-agent*"},
			forbid:     []string{"[ctx:"},
		},
		{
			name:       "context_on_footer_off_uses_legacy_context_suffix",
			showCtx:    true,
			showFooter: false,
			want:       []string{"answer\n[ctx: ~14%]"},
			forbid:     []string{"glm-5.1", "/tmp/release-agent"},
		},
		{
			name:       "context_and_footer_off_plain_answer",
			showCtx:    false,
			showFooter: false,
			want:       []string{"answer"},
			forbid:     []string{"[ctx:", "glm-5.1", "/tmp/release-agent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, agent, platform := newTurnEngine(t)
			agent.model = "glm-5.1"
			agent.workDir = "/tmp/release-agent"
			engine.SetShowContextIndicator(tt.showCtx)
			engine.SetReplyFooterEnabled(tt.showFooter)
			agent.session.setResult(core.Event{Type: core.EventResult, Content: "answer", InputTokens: 28000, Done: true})

			engine.ReceiveMessage(platform, turnMessage("metadata matrix"))
			platform.waitTextContaining(t, "answer")

			texts, _, _, _ := platform.snapshot()
			if len(texts) != 1 {
				t.Fatalf("texts = %#v, want exactly one final reply", texts)
			}
			for _, want := range tt.want {
				if !strings.Contains(texts[0], want) {
					t.Fatalf("reply = %q, want contains %q", texts[0], want)
				}
			}
			for _, forbidden := range tt.forbid {
				if strings.Contains(texts[0], forbidden) {
					t.Fatalf("reply = %q, should not contain %q", texts[0], forbidden)
				}
			}
		})
	}
}

func TestLongFinalResponseKeepsMetadataOnceAtTail(t *testing.T) {
	engine, agent, platform := newTurnEngine(t)
	agent.model = "glm-5.1"
	agent.workDir = "/tmp/release-agent"
	engine.SetReplyFooterEnabled(true)

	body := strings.Repeat("long-response ", 420)
	agent.session.setResult(core.Event{Type: core.EventResult, Content: body, InputTokens: 28000, Done: true})

	engine.ReceiveMessage(platform, turnMessage("long final"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		texts, _, _, _ := platform.snapshot()
		if len(texts) >= 2 {
			joined := strings.Join(texts, "")
			if strings.Count(joined, "[ctx:") != 1 || strings.Count(joined, "glm-5.1") != 1 {
				t.Fatalf("chunks = %#v, want metadata exactly once", texts)
			}
			if !strings.Contains(texts[len(texts)-1], "[ctx: ~14%] · glm-5.1") {
				t.Fatalf("last chunk = %q, want metadata at tail", texts[len(texts)-1])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, _, _, _ := platform.snapshot()
	t.Fatalf("texts = %#v, want long response split into multiple chunks", texts)
}

func TestDisplayVisibilityConfigurationMatrix(t *testing.T) {
	tests := []struct {
		name         string
		thinking     bool
		tools        bool
		wantThinking bool
		wantTool     bool
	}{
		{name: "show_both", thinking: true, tools: true, wantThinking: true, wantTool: true},
		{name: "hide_thinking", thinking: false, tools: true, wantTool: true},
		{name: "hide_tools", thinking: true, tools: false, wantThinking: true},
		{name: "hide_both", thinking: false, tools: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, agent, platform := newTurnEngine(t)
			engine.SetDisplayConfig(core.DisplayCfg{
				Mode:             "full",
				CardMode:         "legacy",
				ThinkingMessages: tt.thinking,
				ToolMessages:     tt.tools,
				ThinkingMaxLen:   300,
				ToolMaxLen:       500,
			})
			agent.session.blockFirstResult()

			msg := turnMessage("visibility matrix")
			go engine.ReceiveMessage(platform, msg)
			agent.session.waitRecords(t, 1)

			agent.session.emit(core.Event{Type: core.EventThinking, Content: "matrix thinking"})
			agent.session.emit(core.Event{Type: core.EventToolUse, ToolName: "Bash", ToolInput: "echo visible"})
			agent.session.emit(core.Event{Type: core.EventToolResult, ToolName: "Bash", ToolResult: "visible output", ToolStatus: "completed"})
			agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: "matrix final", InputTokens: 52000, Done: true})
			platform.waitTextContaining(t, "matrix final")

			texts, _, _, _ := platform.snapshot()
			joined := strings.Join(texts, "\n")
			if got := strings.Contains(joined, "matrix thinking"); got != tt.wantThinking {
				t.Fatalf("thinking visibility = %v, want %v; texts=%#v", got, tt.wantThinking, texts)
			}
			hasTool := strings.Contains(joined, "Bash") || strings.Contains(joined, "echo visible") || strings.Contains(joined, "visible output")
			if hasTool != tt.wantTool {
				t.Fatalf("tool visibility = %v, want %v; texts=%#v", hasTool, tt.wantTool, texts)
			}
			if countContaining(texts, "matrix final") != 1 {
				t.Fatalf("texts=%#v, want exactly one final answer", texts)
			}
		})
	}
}

func TestRichCardModeKeepsToolStepsAndFinalMetadataInOneCard(t *testing.T) {
	agent := newTurnAgent()
	agent.model = "glm-5.1"
	agent.workDir = "/tmp/release-agent"
	platform := &richPreviewPlatform{}
	engine := core.NewEngine("release-rich-card", agent, []core.Platform{platform}, t.TempDir()+"/sessions.json", core.LangEnglish)
	engine.SetReplyFooterEnabled(true)
	engine.SetDisplayConfig(core.DisplayCfg{
		Mode:             "full",
		CardMode:         "rich",
		ThinkingMessages: true,
		ToolMessages:     true,
		ThinkingMaxLen:   300,
		ToolMaxLen:       500,
	})
	t.Cleanup(func() {
		engine.Stop()
		_ = agent.Stop()
	})
	agent.session.blockFirstResult()

	msg := turnMessage("rich card tool turn")
	go engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)

	agent.session.emit(core.Event{Type: core.EventThinking, Content: "rich thinking"})
	agent.session.emit(core.Event{Type: core.EventToolUse, ToolName: "Bash", ToolInput: "echo rich"})
	agent.session.emit(core.Event{Type: core.EventToolResult, ToolName: "Bash", ToolResult: "rich output", ToolStatus: "completed"})
	agent.session.releaseFirstResult(core.Event{Type: core.EventResult, Content: "rich final", InputTokens: 28000, Done: true})
	platform.waitPreviewUpdates(t, 3)

	texts, starts, updates, deletes := platform.snapshotPreviewLifecycle()
	if len(texts) != 0 || len(starts) != 1 || len(updates) == 0 || len(deletes) != 0 {
		t.Fatalf("rich lifecycle = texts:%#v starts:%#v updates:%#v deletes:%#v, want one editable rich card", texts, starts, updates, deletes)
	}
	final := updates[len(updates)-1]
	for _, want := range []string{"status=done", "step=Bash", "rich output", "markdown=rich final", "[ctx: ~14%] · glm-5.1"} {
		if !strings.Contains(final, want) {
			t.Fatalf("final rich card = %q, want contains %q", final, want)
		}
	}
}

type previewLifecyclePlatform struct {
	turnPlatform

	mu             sync.Mutex
	previewStarts  []string
	previewUpdates []string
	previewDeletes []any
}

func (p *previewLifecyclePlatform) Name() string { return "feishu" }

func (p *previewLifecyclePlatform) KeepPreviewOnFinish() bool { return true }

func (p *previewLifecyclePlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.previewStarts = append(p.previewStarts, content)
	return "preview-1", nil
}

func (p *previewLifecyclePlatform) UpdateMessage(_ context.Context, handle any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.previewUpdates = append(p.previewUpdates, content)
	return nil
}

func (p *previewLifecyclePlatform) DeletePreviewMessage(_ context.Context, handle any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.previewDeletes = append(p.previewDeletes, handle)
	return nil
}

func (p *previewLifecyclePlatform) waitPreviewStarts(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := len(p.previewStarts)
		p.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, starts, updates, deletes := p.snapshotPreviewLifecycle()
	t.Fatalf("timeout waiting for %d preview starts; starts=%#v updates=%#v deletes=%#v", n, starts, updates, deletes)
}

func (p *previewLifecyclePlatform) waitSentTexts(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		texts, _, _, _ := p.snapshotPreviewLifecycle()
		if len(texts) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, starts, updates, deletes := p.snapshotPreviewLifecycle()
	t.Fatalf("timeout waiting for %d final sends; texts=%#v starts=%#v updates=%#v deletes=%#v", n, texts, starts, updates, deletes)
}

func (p *previewLifecyclePlatform) waitPreviewUpdates(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, updates, _ := p.snapshotPreviewLifecycle()
		if len(updates) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, starts, updates, deletes := p.snapshotPreviewLifecycle()
	t.Fatalf("timeout waiting for %d preview updates; texts=%#v starts=%#v updates=%#v deletes=%#v", n, texts, starts, updates, deletes)
}

func (p *previewLifecyclePlatform) snapshotPreviewLifecycle() (texts []string, starts []string, updates []string, deletes []any) {
	texts, _, _, _ = p.turnPlatform.snapshot()
	p.mu.Lock()
	defer p.mu.Unlock()
	return texts,
		append([]string(nil), p.previewStarts...),
		append([]string(nil), p.previewUpdates...),
		append([]any(nil), p.previewDeletes...)
}

type richPreviewPlatform struct {
	previewLifecyclePlatform
}

func (p *richPreviewPlatform) BuildRichCard(status core.CardStatus, title string, steps []core.ToolStep, markdown string, streaming bool, elapsed time.Duration) string {
	var b strings.Builder
	b.WriteString("status=")
	b.WriteString(string(status))
	if streaming {
		b.WriteString(" streaming=true")
	}
	b.WriteString("\n")
	for _, step := range steps {
		b.WriteString("step=")
		b.WriteString(step.Name)
		b.WriteString(" summary=")
		b.WriteString(step.Summary)
		if step.Result != "" {
			b.WriteString(" result=")
			b.WriteString(step.Result)
		}
		b.WriteString("\n")
	}
	if markdown != "" {
		b.WriteString("markdown=")
		b.WriteString(markdown)
		b.WriteString("\n")
	}
	return b.String()
}

func assertStableSideChannelOnly(t *testing.T, platform *turnPlatform, sideText string) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	var lastTexts []string
	for time.Now().Before(deadline) {
		texts, _, _, _ := platform.snapshot()
		lastTexts = texts
		count := 0
		for _, text := range texts {
			if strings.Contains(text, sideText) {
				count++
			}
			if strings.Contains(text, "[ctx:") {
				t.Fatalf("unexpected context-only duplicate reply: %#v", texts)
			}
		}
		if count > 1 {
			t.Fatalf("texts = %#v, want no duplicate side-channel text", texts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	count := 0
	for _, text := range lastTexts {
		if strings.Contains(text, sideText) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("texts = %#v, want exactly one side-channel text", lastTexts)
	}
}

func countContaining(texts []string, substr string) int {
	count := 0
	for _, text := range texts {
		if strings.Contains(text, substr) {
			count++
		}
	}
	return count
}

func containsText(texts []string, substr string) bool {
	for _, text := range texts {
		if strings.Contains(text, substr) {
			return true
		}
	}
	return false
}
