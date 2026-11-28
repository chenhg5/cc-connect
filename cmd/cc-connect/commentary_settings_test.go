package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type commentaryReloadSession struct {
	stubMainAgentSession
	events chan core.Event
}

func (s *commentaryReloadSession) Events() <-chan core.Event { return s.events }

func (s *commentaryReloadSession) Send(string, string, []core.ImageAttachment, []core.FileAttachment) error {
	s.events <- core.Event{Type: core.EventCommentary, Content: "Working. "}
	s.events <- core.Event{Type: core.EventToolUse, ToolName: "shell"}
	s.events <- core.Event{Type: core.EventText, Content: "Done."}
	s.events <- core.Event{Type: core.EventResult, Done: true}
	return nil
}

type commentaryReloadAgent struct {
	stubMainAgent
	session *commentaryReloadSession
}

func (a *commentaryReloadAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return a.session, nil
}

type commentaryReloadPlatform struct {
	sent chan string
}

func (p *commentaryReloadPlatform) Name() string                    { return "test" }
func (p *commentaryReloadPlatform) Start(core.MessageHandler) error { return nil }
func (p *commentaryReloadPlatform) Stop() error                     { return nil }
func (p *commentaryReloadPlatform) Send(_ context.Context, _ any, text string) error {
	p.sent <- text
	return nil
}
func (p *commentaryReloadPlatform) Reply(ctx context.Context, replyCtx any, text string) error {
	return p.Send(ctx, replyCtx, text)
}

// Protect production callback mapping and reloadConfig, not just an equivalent
// test callback. Only agent and messaging transport boundaries are simulated.
func TestManagementCommentary_ProductionSaveReloadDelivery(t *testing.T) {
	oldPath := config.ConfigPath
	t.Cleanup(func() { config.ConfigPath = oldPath })
	t.Setenv("HOME", t.TempDir())
	for _, global := range []string{"absent", "false", "true"} {
		for _, project := range []string{"absent", "false", "true"} {
			t.Run(global+"/project="+project, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.toml")
				raw := "[display]\nmode = \"quiet\"\nreply_footer = false\nshow_context_indicator = false\n"
				raw += "[[projects]]\nname = \"test\"\n[projects.agent]\ntype = \"stub-main\"\n[[projects.platforms]]\ntype = \"test\"\n"
				if project != "absent" {
					raw += "[projects.display]\ncommentary_messages = " + project + "\n"
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				config.ConfigPath = path
				updates := map[string]any{}
				if global != "absent" {
					updates["commentary_messages"] = global == "true"
				}
				if err := saveGlobalSettings(updates); err != nil {
					t.Fatal(err)
				}
				s := &commentaryReloadSession{events: make(chan core.Event, 4)}
				a := &commentaryReloadAgent{session: s}
				p := &commentaryReloadPlatform{sent: make(chan string, 8)}
				e := core.NewEngine("test", a, []core.Platform{p}, "", core.LangEnglish)
				t.Cleanup(func() { _ = e.Stop() })
				result, err := reloadConfig(path, "test", e)
				if err != nil || !result.DisplayUpdated {
					t.Fatalf("reloadConfig: result=%+v err=%v", result, err)
				}
				wantStandalone := global != "false"
				if project != "absent" {
					wantStandalone = project == "true"
				}
				e.ReceiveMessage(p, &core.Message{SessionKey: "test:chat:user", Platform: "test", UserID: "user", Content: "Check work", ReplyCtx: "ctx"})

				var messages []string
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				for {
					select {
					case text := <-p.sent:
						messages = append(messages, text)
						if !strings.Contains(text, "Done.") {
							continue
						}
						if wantStandalone {
							if len(messages) != 2 || messages[0] != "Working." || text != "Done." {
								t.Fatalf("standalone messages = %q", messages)
							}
						} else if len(messages) != 1 || text != "Done." {
							t.Fatalf("opt-out messages = %q", messages)
						}
						return
					case <-timer.C:
						t.Fatalf("no final answer after save/reload: %q", messages)
					}
				}
			})
		}
	}
}
