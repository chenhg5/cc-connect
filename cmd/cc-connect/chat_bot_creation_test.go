package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type createdBotTestPlatform struct {
	handler  core.MessageHandler
	messages []string
	started  bool
}

func (p *createdBotTestPlatform) Name() string { return "feishu" }
func (p *createdBotTestPlatform) Start(h core.MessageHandler) error {
	p.handler = h
	p.started = true
	return nil
}
func (p *createdBotTestPlatform) Stop() error { return nil }
func (p *createdBotTestPlatform) Reply(_ context.Context, _ any, s string) error {
	p.messages = append(p.messages, s)
	return nil
}
func (p *createdBotTestPlatform) Send(ctx context.Context, r any, s string) error {
	return p.Reply(ctx, r, s)
}
func (p *createdBotTestPlatform) IsPrivateUserMessage(m *core.Message) bool {
	return m.ReplyCtx == "private"
}

type createdBotTestAgent struct{}

func (*createdBotTestAgent) Name() string { return "stub" }
func (*createdBotTestAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return nil, fmt.Errorf("unexpected model invocation")
}
func (*createdBotTestAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (*createdBotTestAgent) Stop() error { return nil }

func botCreatorFixture(t *testing.T) (*chatBotCreator, chan botStartRequest) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`data_dir = %q
[[projects]]
name = "supervisor"
admin_from = "parent-owner"
[projects.agent]
type = "acp"
[projects.agent.options]
cmd = "trae-cli"
args = ["acp", "serve"]
work_dir = %q
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_parent"
app_secret = "parent-secret"
`, dir, dir)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	previous := config.ConfigPath
	config.ConfigPath = path
	t.Cleanup(func() { config.ConfigPath = previous })
	ch := make(chan botStartRequest, 2)
	c := newChatBotCreator(path, dir, context.Background(), ch)
	c.permissionCheck = func(context.Context, registrationFlowResult, string) rolePermissionReport {
		return rolePermissionReport{Checked: true}
	}
	return c, ch
}

func TestChatBotCreation_FormRetryKeepsIdentityWithoutRequestedName(t *testing.T) {
	c, _ := botCreatorFixture(t)
	const id = "bot-automatic-reference"
	// A registration from the former name-input form remains resumable.
	legacy := map[string]any{
		"Version": 2, "Parent": "supervisor", "Initiator": "parent-owner", "Name": id,
		"RequestedDisplayName": "Old requested name",
		"Identity":             registrationFlowResult{AppID: "cli_child", AppSecret: "test-secret", OwnerOpenID: "ou_scanner", Platform: "feishu"},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(c.recordPath(id)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.recordPath(id), data, 0600); err != nil {
		t.Fatal(err)
	}
	c.register = func(registrationFlowOptions) (*registrationFlowResult, error) {
		t.Fatal("retry tried to create another app")
		return nil, nil
	}
	notify := func(core.MsgKey, ...any) { t.Fatal("retry requested new authorization") }
	r, err := c.prepareWithOptions(context.Background(), "supervisor", "parent-owner", id, true, notify)
	if err != nil || r.Name != id || !r.AutomaticID || r.Identity.AppID != "cli_child" {
		t.Fatalf("registration identity not preserved: %v", err)
	}
	if _, err := c.prepareWithOptions(context.Background(), "supervisor", "someone-else", id, true, notify); !errors.Is(err, errBotExists) {
		t.Fatal("retry lost its original owner binding")
	}
	if err := c.saveRecord(r); err != nil {
		t.Fatal(err)
	}
	r, err = c.prepare(context.Background(), "supervisor", "parent-owner", id, notify)
	if err != nil || !r.AutomaticID || r.Name != id {
		t.Fatal("saved registration lost its automatic identity")
	}
}

func TestChatBotCreation_AutomaticallyBindsScannerBeforeStartAndRetry(t *testing.T) {
	c, starts := botCreatorFixture(t)
	registrations := 0
	c.register = func(opts registrationFlowOptions) (*registrationFlowResult, error) {
		registrations++
		if err := opts.OnVerification("https://example.invalid/authorize"); err != nil {
			return nil, err
		}
		return &registrationFlowResult{AppID: "cli_child", AppSecret: "never-print-me", OwnerOpenID: "ou_child_scanner", Platform: "feishu"}, nil
	}
	parent := &createdBotTestPlatform{}
	notices := make(chan string, 10)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		c.handler("supervisor")(context.Background(), parent, "parent-owner", "developer", func(key core.MsgKey, args ...any) { notices <- core.NewI18n(core.LangEnglish).Tf(key, args...) })
	}()
	var req botStartRequest
	select {
	case req = <-starts:
	case <-time.After(2 * time.Second):
		t.Fatal("no dynamic start request")
	}
	childPlatform := &createdBotTestPlatform{}
	var child *core.Engine
	registered := false
	err := startCreatedBot(req, c.configPath, func(cfg *config.Config, p config.ProjectConfig) (*core.Engine, string, error) {
		if p.BotOwnerID != "ou_child_scanner" || p.AdminFrom != "ou_child_scanner" || p.BotOwnerClaimHash != "" {
			t.Fatal("bot activated before binding scanner")
		}
		child = core.NewEngine(p.Name, &createdBotTestAgent{}, []core.Platform{childPlatform}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
		child.SetBotOwnership(p.BotOwnerClaimHash, p.BotOwnerID, func(id string) error { return config.ClaimBotIdentity(p.Name, p.BotOwnerClaimHash, id) })
		return child, "", nil
	}, func(name string, _ *core.Engine, _ string) { registered = name == "developer" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Stop() })
	req.done <- nil
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("creation did not finish")
	}
	if !registered || !childPlatform.started {
		t.Fatal("new bot was not connected and registered")
	}
	data, err := os.ReadFile(c.recordPath("developer"))
	if err != nil {
		t.Fatal(err)
	}
	var record botCreationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(c.recordPath("developer"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("recovery credentials not private")
	}
	// An acknowledged or unacknowledged success can be replayed safely.
	c.handler("supervisor")(context.Background(), parent, "parent-owner", "developer", func(key core.MsgKey, args ...any) { notices <- core.NewI18n(core.LangEnglish).Tf(key, args...) })
	if registrations != 1 || len(starts) != 0 {
		t.Fatal("retry created another app or engine")
	}
	if record.Version != 2 || record.ClaimToken != "" {
		t.Fatal("unexpected manual pairing record")
	}
	childPlatform.handler(childPlatform, &core.Message{UserID: "parent-owner", SessionKey: "child:parent", Content: "/project list", ReplyCtx: "private"})
	if !strings.Contains(strings.Join(childPlatform.messages, "\n"), core.NewI18n(core.LangEnglish).T(core.MsgBotOwnerRequired)) {
		t.Fatal("parent app identity allowed in child")
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Projects[1].BotOwnerID != "ou_child_scanner" || cfg.Projects[1].AdminFrom != "ou_child_scanner" {
		t.Fatal("child did not bind its own application user ID")
	}
	close(notices)
	for msg := range notices {
		if strings.Contains(msg, "never-print-me") || strings.Contains(msg, "/new claim") {
			t.Fatal("reply leaked secret or requested manual pairing")
		}
	}
}

func TestChatBotCreation_ExistingNameNeverRegisters(t *testing.T) {
	c, _ := botCreatorFixture(t)
	c.register = func(registrationFlowOptions) (*registrationFlowResult, error) {
		t.Fatal("started duplicate registration")
		return nil, nil
	}
	_, err := c.prepare(context.Background(), "supervisor", "owner", "supervisor", func(core.MsgKey, ...any) {})
	if !errors.Is(err, errBotExists) {
		t.Fatalf("err=%v", err)
	}
}

func TestChatBotCreation_SavedCredentialsSurviveRestart(t *testing.T) {
	c, _ := botCreatorFixture(t)
	if err := os.MkdirAll(filepath.Dir(c.recordPath("reviewer")), 0700); err != nil {
		t.Fatal(err)
	}
	record := &botCreationRecord{Name: "reviewer", Parent: "supervisor", Initiator: "owner", ClaimToken: strings.Repeat("a", 64), Identity: registrationFlowResult{AppID: "cli_review", AppSecret: "secret", Platform: "feishu"}}
	if err := c.saveRecord(record); err != nil {
		t.Fatal(err)
	}
	c2 := newChatBotCreator(c.configPath, c.dataDir, context.Background(), make(chan botStartRequest))
	c2.register = func(registrationFlowOptions) (*registrationFlowResult, error) {
		t.Fatal("repeat authorization on recovery")
		return nil, nil
	}
	got, err := c2.prepare(context.Background(), "supervisor", "owner", "reviewer", func(core.MsgKey, ...any) {})
	if err != nil || got.Identity.AppID != "cli_review" {
		t.Fatalf("recovery: %v", err)
	}
	if _, err := c2.prepare(context.Background(), "supervisor", "another-owner", "reviewer", func(core.MsgKey, ...any) {}); !errors.Is(err, errBotExists) {
		t.Fatal("another user obtained recovery credentials")
	}
}

func TestChatRegistrationFlow_CallbackAndCancellation(t *testing.T) {
	for _, outcome := range []string{"success", "access_denied", "denied-with-credentials", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				switch r.Form.Get("action") {
				case "init":
					fmt.Fprint(w, `{"supported_auth_methods":["client_secret"]}`)
				case "begin":
					fmt.Fprint(w, `{"device_code":"private-device","verification_uri_complete":"https://example.invalid/verify","interval":1,"expire_in":600}`)
				case "poll":
					if outcome == "success" {
						fmt.Fprint(w, `{"client_id":"cli_new","client_secret":"private-secret"}`)
					} else if outcome == "denied-with-credentials" {
						fmt.Fprint(w, `{"error":"access_denied","client_id":"cli_new","client_secret":"private-secret","user_info":{"open_id":"ou_should_not_bind"}}`)
					} else {
						fmt.Fprint(w, `{"error":"access_denied"}`)
					}
				default:
					t.Error("unknown registration action")
				}
			}))
			defer server.Close()
			links := 0
			result, err := runRegistrationFlowWithClient(registrationFlowOptions{Context: ctx, OnVerification: func(link string) error {
				links++
				if link != "https://example.invalid/verify" {
					t.Error("link changed")
				}
				if outcome == "cancel" {
					cancel()
				}
				return nil
			}}, &registrationClient{baseURL: server.URL, http: server.Client()})
			if links != 1 {
				t.Fatal("verification link callback missing")
			}
			if outcome == "success" {
				if err != nil || result.AppID != "cli_new" {
					t.Fatalf("success: %v", err)
				}
			} else if err == nil {
				t.Fatal("failure returned success")
			}
		})
	}
}
