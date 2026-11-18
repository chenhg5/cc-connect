package antigravity

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type commentaryPlatform struct {
	sent  chan string
	cards chan struct{}
}

func (p *commentaryPlatform) Name() string                    { return "test" }
func (p *commentaryPlatform) Start(core.MessageHandler) error { return nil }
func (p *commentaryPlatform) Stop() error                     { return nil }
func (p *commentaryPlatform) Reply(ctx context.Context, replyCtx any, text string) error {
	return p.Send(ctx, replyCtx, text)
}
func (p *commentaryPlatform) Send(_ context.Context, _ any, text string) error {
	p.sent <- text
	return nil
}
func (p *commentaryPlatform) ProgressStyle() string             { return "card" }
func (p *commentaryPlatform) SupportsProgressCardPayload() bool { return true }
func (p *commentaryPlatform) SendPreviewStart(_ context.Context, _ any, _ string) (any, error) {
	select {
	case p.cards <- struct{}{}:
	default:
	}
	return "progress-card", nil
}
func (p *commentaryPlatform) UpdateMessage(context.Context, any, string) error { return nil }

// Exercise the real adapter, subprocess reader and Engine, mocking only AGY
// and the messaging transport. Hold the CLI before its final answer so a
// progress-card edit cannot accidentally satisfy the notification assertion.
func TestAntigravityCommentary_ReachesChatBeforeTurnCompletion(t *testing.T) {
	if os.Getenv("CC_AGY_COMMENTARY_HELPER") == "1" {
		_, _ = io.WriteString(os.Stdout, `{"event":"init","conversation_id":"commentary-session"}`+"\n"+
			`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"Checking "}}`+"\n"+
			`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"the build"}}`+"\n"+
			`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command"}}`+"\n")
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("CC_AGY_COMMENTARY_RELEASE")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(2)
			}
			time.Sleep(time.Millisecond)
		}
		_, _ = io.WriteString(os.Stdout, `{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command"}}`+"\n"+
			`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"agent_response","text_delta":"Build passed"}}`+"\n")
		if os.Getenv("CC_AGY_COMMENTARY_EOF") != "1" {
			_, _ = io.WriteString(os.Stdout, `{"event":"result","result":{"status":"SUCCESS","response":"Build passed"}}`+"\n")
		}
		os.Exit(0)
	}
	t.Setenv("HOME", t.TempDir())
	for _, eof := range []string{"0", "1"} {
		t.Run("eof="+eof, func(t *testing.T) {
			workDir := t.TempDir()
			release := filepath.Join(workDir, "release")
			a := &Agent{workDir: workDir, mode: "yolo", cmd: os.Args[0], activeIdx: -1,
				cliExtraArgs: []string{"-test.run=^TestAntigravityCommentary_ReachesChatBeforeTurnCompletion$", "--"},
				configEnv:    []string{"CC_AGY_COMMENTARY_HELPER=1", "CC_AGY_COMMENTARY_RELEASE=" + release, "CC_AGY_COMMENTARY_EOF=" + eof}}
			p := &commentaryPlatform{sent: make(chan string, 32), cards: make(chan struct{}, 8)}
			e := core.NewEngine("commentary", a, []core.Platform{p}, "", core.LangEnglish)
			enabled := true
			e.SetDisplayConfig(core.DisplayCfg{Mode: "full", CardMode: "legacy", ToolMessages: true, CommentaryMessages: &enabled})
			e.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
			t.Cleanup(func() {
				if err := e.Stop(); err != nil {
					t.Error(err)
				}
			})
			e.ReceiveMessage(p, &core.Message{SessionKey: "test:commentary:user", Platform: "test", UserID: "user", Content: "Check the build", ReplyCtx: "ctx"})
			select {
			case text := <-p.sent:
				if text != "Checking the build" {
					t.Fatalf("first standalone message = %q", text)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no standalone commentary before task completion")
			}
			select {
			case <-p.cards:
			case <-time.After(3 * time.Second):
				t.Fatal("tool progress did not create a card")
			}
			select {
			case text := <-p.sent:
				t.Fatalf("unexpected message while CLI held: %q", text)
			default:
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case text := <-p.sent:
				if !strings.Contains(text, "Build passed") || strings.Contains(text, "Checking the build") {
					t.Fatalf("final answer = %q", text)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("final answer missing")
			}
		})
	}
}

// Use the same non-nil, config-resolved display value as startup/reload. The
// real adapter and subprocess reader must retain narration when opt-in is off.
func TestAntigravityCommentary_ProductionConfigPreservesNarration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, mode := range []string{"full", "quiet"} {
		for _, eof := range []string{"0", "1"} {
			for _, tc := range []struct {
				name    string
				global  string
				project string
				enabled bool
			}{
				{name: "absent"},
				{name: "false", global: "false"},
				{name: "true", global: "true", enabled: true},
				{name: "project-opt-in", global: "false", project: "true", enabled: true},
				{name: "project-opt-out", global: "true", project: "false"},
			} {
				t.Run(mode+"/eof="+eof+"/"+tc.name, func(t *testing.T) {
					workDir := t.TempDir()
					raw := "[display]\nmode = \"" + mode + "\"\nreply_footer = false\nshow_context_indicator = false\n"
					if tc.global != "" {
						raw += "commentary_messages = " + tc.global + "\n"
					}
					raw += "[[projects]]\nname = \"commentary\"\n[projects.agent]\ntype = \"antigravity\"\n[[projects.platforms]]\ntype = \"test\"\n"
					if tc.project != "" {
						raw += "[projects.display]\ncommentary_messages = " + tc.project + "\n"
					}
					path := filepath.Join(workDir, "config.toml")
					if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
						t.Fatal(err)
					}
					cfg, err := config.Load(path)
					if err != nil {
						t.Fatal(err)
					}
					project := &cfg.Projects[0]
					commentary := config.EffectiveCommentaryMessages(cfg, project)
					if commentary != tc.enabled {
						t.Fatalf("resolved opt-in = %v, want %v", commentary, tc.enabled)
					}
					resolvedMode, thinking, tools, thinkingLen, toolLen, showContext, footer, hideFooter := config.EffectiveDisplay(cfg, project)
					release := filepath.Join(workDir, "release")
					a := &Agent{workDir: workDir, mode: "yolo", cmd: os.Args[0], activeIdx: -1,
						cliExtraArgs: []string{"-test.run=^TestAntigravityCommentary_ReachesChatBeforeTurnCompletion$", "--"},
						configEnv:    []string{"CC_AGY_COMMENTARY_HELPER=1", "CC_AGY_COMMENTARY_RELEASE=" + release, "CC_AGY_COMMENTARY_EOF=" + eof}}
					p := &commentaryPlatform{sent: make(chan string, 32), cards: make(chan struct{}, 8)}
					e := core.NewEngine(project.Name, a, []core.Platform{p}, "", core.LangEnglish)
					t.Cleanup(func() { _ = e.Stop() })
					e.SetDisplayConfig(core.DisplayCfg{
						Mode: resolvedMode, CardMode: config.EffectiveCardMode(cfg, project),
						ThinkingMessages: thinking, ToolMessages: tools,
						ThinkingMaxLen: thinkingLen, ToolMaxLen: toolLen,
						CommentaryMessages: &commentary, HideAgentFooter: hideFooter,
					})
					e.SetReplyFooterEnabled(footer)
					e.SetShowContextIndicator(showContext)
					e.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
					e.ReceiveMessage(p, &core.Message{SessionKey: "test:commentary:user", Platform: "test", UserID: "user", Content: "Check the build", ReplyCtx: "ctx"})

					var messages []string
					// Legacy full mode flushes pre-tool text before its progress card.
					// Quiet mode retains it in the answer unless explicitly opted in.
					if commentary || mode == "full" {
						select {
						case text := <-p.sent:
							if text != "Checking the build" {
								t.Fatalf("pre-tool narration = %q", text)
							}
							messages = append(messages, text)
						case <-time.After(5 * time.Second):
							t.Fatal("pre-tool narration disappeared before completion")
						}
					}
					if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
						t.Fatal(err)
					}
					select {
					case text := <-p.sent:
						if !strings.Contains(text, "Build passed") {
							t.Fatalf("expected final answer, got %q", text)
						}
						messages = append(messages, text)
						wantNarrationInAnswer := mode == "quiet" && !commentary
						if strings.Contains(text, "Checking the build") != wantNarrationInAnswer {
							t.Fatalf("final answer did not honor legacy/opt-in display: %q", text)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("final answer missing")
					}
					wantNarrationCount := 1
					if strings.Count(strings.Join(messages, "\n"), "Checking the build") != wantNarrationCount {
						t.Fatalf("narration lost or duplicated: %q", messages)
					}
				})
			}
		}
	}
}
