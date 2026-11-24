package feishu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Use the real platform's key and reply-context reconstruction, replacing
// only the network boundary so these tests never send a Feishu message.
type scheduledRoutePlatform struct {
	base    *Platform
	mu      sync.Mutex
	replies map[string][]string
}

func (p *scheduledRoutePlatform) Name() string                  { return p.base.Name() }
func (*scheduledRoutePlatform) Start(core.MessageHandler) error { return nil }
func (*scheduledRoutePlatform) Stop() error                     { return nil }
func (p *scheduledRoutePlatform) ChannelKeyFromSessionKey(key string) (string, bool) {
	return p.base.ChannelKeyFromSessionKey(key)
}
func (p *scheduledRoutePlatform) ReconstructReplyCtx(key string) (any, error) {
	return p.base.ReconstructReplyCtx(key)
}
func (p *scheduledRoutePlatform) Reply(_ context.Context, ctx any, text string) error {
	rc := ctx.(replyContext)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies[rc.messageID] = append(p.replies[rc.messageID], text)
	return nil
}
func (p *scheduledRoutePlatform) Send(ctx context.Context, replyCtx any, text string) error {
	return p.Reply(ctx, replyCtx, text)
}

type scheduledRouteAgent struct{ name, workDir string }

func (a *scheduledRouteAgent) Name() string { return a.name }
func (*scheduledRouteAgent) Stop() error    { return nil }
func (*scheduledRouteAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *scheduledRouteAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return &scheduledRouteSession{events: make(chan core.Event, 1), reply: "executed-in=" + a.workDir}, nil
}

type scheduledRouteSession struct {
	events chan core.Event
	reply  string
}

func (s *scheduledRouteSession) Send(string, string, []core.ImageAttachment, []core.FileAttachment) error {
	s.events <- core.Event{Type: core.EventResult, Content: s.reply, Done: true}
	return nil
}
func (*scheduledRouteSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *scheduledRouteSession) Events() <-chan core.Event                           { return s.events }
func (*scheduledRouteSession) CurrentSessionID() string                              { return "scheduled-session" }
func (*scheduledRouteSession) Alive() bool                                           { return true }
func (*scheduledRouteSession) Close() error                                          { return nil }

func TestScheduledJobs_FeishuTopicExecutionMatchesReplyDestination(t *testing.T) {
	for _, platformName := range []string{"feishu", "lark"} {
		for _, scope := range []string{"root", "thread"} {
			for _, kind := range []string{"timer", "cron"} {
				t.Run(platformName+"/"+scope+"/"+kind, func(t *testing.T) {
					base := t.TempDir()
					bindingsPath := filepath.Join(base, "bindings.json")
					bindings := core.NewWorkspaceBindingManager(bindingsPath)
					workspaces := make(map[string]string)
					for _, topic := range []string{"a", "b"} {
						dir := filepath.Join(base, topic)
						if err := os.Mkdir(dir, 0o755); err != nil {
							t.Fatal(err)
						}
						// Match workspace routing's canonical native filesystem path.
						dir, err := filepath.EvalSymlinks(dir)
						if err != nil {
							t.Fatal(err)
						}
						workspaces[topic] = dir
						bindings.Bind("project:test", platformName+":room:topic:"+topic, topic, dir)
					}
					name := "scheduled-feishu-" + t.Name()
					core.RegisterAgent(name, func(opts map[string]any) (core.Agent, error) {
						return &scheduledRouteAgent{name: name, workDir: opts["work_dir"].(string)}, nil
					})
					p := &scheduledRoutePlatform{base: &Platform{platformName: platformName, threadIsolation: true}, replies: make(map[string][]string)}
					e := core.NewEngine("test", &scheduledRouteAgent{name: name, workDir: "wrong-global-workspace"}, []core.Platform{p}, filepath.Join(base, "sessions.json"), core.LangEnglish)
					e.SetMultiWorkspace(base, bindingsPath)
					t.Cleanup(func() { _ = e.Stop() })
					for _, topic := range []string{"a", "b"} {
						key := platformName + ":room:" + scope + ":" + topic
						var err error
						if kind == "timer" {
							err = e.ExecuteTimerJob(&core.TimerJob{ID: topic, Project: "test", SessionKey: key, Prompt: "check progress"})
						} else {
							err = e.ExecuteCronJob(&core.CronJob{ID: topic, Project: "test", SessionKey: key, Prompt: "check progress"})
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					p.mu.Lock()
					defer p.mu.Unlock()
					for topic, dir := range workspaces {
						text := strings.Join(p.replies[topic], "\n")
						if !strings.Contains(text, "executed-in="+dir) || strings.Contains(text, "wrong-global-workspace") {
							t.Fatalf("topic %s received a check from the wrong workspace: %s", topic, text)
						}
						for other, otherDir := range workspaces {
							if other != topic && strings.Contains(text, "executed-in="+otherDir) {
								t.Fatalf("topic %s received topic %s's check: %s", topic, other, text)
							}
						}
					}
				})
			}
		}
	}
}
