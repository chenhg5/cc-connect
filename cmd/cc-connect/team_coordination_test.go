package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type roleTestPlatform struct {
	mu       sync.Mutex
	private  []string
	adds     int
	present  bool
	appID    string
	checks   []string
	checkErr error
	stopped  bool
}

func (*roleTestPlatform) Name() string                    { return "feishu" }
func (*roleTestPlatform) Start(core.MessageHandler) error { return nil }
func (p *roleTestPlatform) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	return nil
}
func (*roleTestPlatform) Reply(context.Context, any, string) error {
	return fmt.Errorf("unexpected public registration message")
}
func (*roleTestPlatform) Send(context.Context, any, string) error {
	return fmt.Errorf("unexpected public registration message")
}
func (p *roleTestPlatform) SendPrivateUser(_ context.Context, user, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.private = append(p.private, user+":"+text)
	return nil
}
func (*roleTestPlatform) CreateProjectGroup(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("must reuse group")
}
func (p *roleTestPlatform) EnsureProjectGroupBots(context.Context, string, []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.adds++
	return nil
}
func (p *roleTestPlatform) ProjectGroupHasBot(_ context.Context, _ string, app string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks = append(p.checks, app)
	if p.appID != app {
		return false, fmt.Errorf("membership check must use the bot's own application")
	}
	return p.present, p.checkErr
}
func (*roleTestPlatform) ProjectGroupLink(string) string { return "" }

func TestTeamCoordination_EnsureRoleAuthorizationThenJoinAndReuse(t *testing.T) {
	s, c, team, dir := projectFixture(t)
	c.Language = "zh"
	team.AutoCoordinate = true
	c.Teams = []config.TeamConfig{team}
	for i := range c.Projects {
		c.Projects[i].BotOwnerID = "ou_owner"
		c.Projects[i].AdminFrom = "ou_owner"
		c.Projects[i].Agent.Options = map[string]any{"work_dir": dir}
		c.Projects[i].Platforms[0].Options["app_secret"] = "test-secret"
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	previous := config.ConfigPath
	config.ConfigPath = s.path
	t.Cleanup(func() { config.ConfigPath = previous })
	project := core.TeamProject{ID: "project", Name: "挂载验证项目", Team: "default", Platform: "feishu", ChatID: "oc_project", Manager: "manager", ManagerApp: "cli_manager", Creator: "ou_owner", Bots: []string{"manager", "dev"}, BotApps: []string{"cli_manager", "cli_dev"}, Directories: []string{dir}, State: "ready"}
	if err := s.store.Put(project); err != nil {
		t.Fatal(err)
	}
	starts := make(chan botStartRequest, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.creator = newChatBotCreator(s.path, s.dataDir, ctx, starts)
	s.creator.permissionCheck = func(context.Context, registrationFlowResult, string) rolePermissionReport {
		return rolePermissionReport{Checked: true}
	}
	s.creator.markStarted("manager")
	s.creator.markStarted("dev")
	var registrations atomic.Int32
	authorized := make(chan struct{})
	s.creator.register = func(opts registrationFlowOptions) (*registrationFlowResult, error) {
		registrations.Add(1)
		if err := opts.OnVerification("https://example.invalid/private-grant"); err != nil {
			return nil, err
		}
		select {
		case <-authorized:
		case <-opts.Context.Done():
			return nil, opts.Context.Err()
		}
		return &registrationFlowResult{AppID: "cli_tester", AppSecret: "never-output", OwnerOpenID: "ou_new_owner", Platform: "feishu"}, nil
	}
	s.creator.readBotName = func(context.Context, registrationFlowResult) (string, error) { return "Tester", nil }
	p := &roleTestPlatform{present: true, appID: "cli_manager"}
	s.groupPlatformFactory = func(_ string, opts map[string]any) (core.Platform, error) {
		app, _ := opts["app_id"].(string)
		if app != "cli_tester" {
			return nil, fmt.Errorf("wrong role identity %q", app)
		}
		return &roleTestPlatform{present: true, appID: app}, nil
	}
	for _, bad := range []string{"", " \n\t", strings.Repeat("字", 2001)} {
		if _, err := s.ensureRole(ctx, p, project, "tester", "codex", bad); err == nil || registrations.Load() != 0 {
			t.Fatal("new role without a valid reason must not start registration")
		}
	}
	result, err := s.ensureRole(ctx, p, project, "tester", "codex", "验证动态挂载与卸载，交付回归测试报告")
	if err != nil || !strings.HasPrefix(result, "awaiting_authorization") {
		t.Fatalf("unexpected result: %s %v", result, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ready := len(p.private) > 0
		p.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	grant := strings.Join(p.private, "\n")
	p.mu.Unlock()
	for _, expected := range []string{"挂载验证项目", "测试者", "Codex", "验证动态挂载与卸载，交付回归测试报告", "private-grant"} {
		if !strings.Contains(grant, expected) {
			t.Fatalf("missing context before authorization: %q in %s", expected, grant)
		}
	}
	second, err := s.ensureRole(ctx, p, project, "tester", "codex", "验证动态挂载与卸载，交付回归测试报告")
	if err != nil || !strings.HasPrefix(second, "awaiting_authorization") || registrations.Load() != 1 {
		t.Fatal("concurrent request duplicated identity")
	}
	all, _ := s.store.All()
	if len(all["project"].Bots) != 2 {
		t.Fatal("unapproved identity was added to project")
	}
	close(authorized)
	select {
	case req := <-starts:
		fresh, err := config.Load(s.path)
		if err != nil {
			t.Fatal(err)
		}
		_, m := fresh.TeamForBot(req.name)
		if m == nil || m.Role != "tester" {
			t.Fatal("started before role configuration")
		}
		req.done <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("role did not start")
	}
	var ready bool
	for time.Now().Before(deadline) {
		all, _ = s.store.All()
		if len(all["project"].Bots) == 3 {
			ready = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("authorized role did not join existing group")
	}
	// Reuse the new member without another grant or group.
	result, err = s.ensureRole(ctx, p, all["project"], "tester", "codex", "")
	if err != nil || !strings.HasPrefix(result, "ready:") || registrations.Load() != 1 {
		t.Fatalf("reuse failed: %s %v", result, err)
	}
	p.mu.Lock()
	notices := strings.Join(p.private, "\n")
	p.mu.Unlock()
	if !strings.Contains(notices, "ou_owner:https://") && !strings.Contains(notices, "private-grant") {
		t.Fatal("authorization not delivered privately")
	}
	if strings.Contains(notices, "never-output") {
		t.Fatal("credential leaked")
	}
	for _, expected := range []string{"挂载验证项目", "测试者", "Codex", "加入", "权限", "验证动态挂载与卸载，交付回归测试报告"} {
		if !strings.Contains(notices, expected) {
			t.Fatalf("role authorization omitted %q: %s", expected, notices)
		}
	}
}

func TestTeamCoordination_UnconfirmedJoinDoesNotPublishMembership(t *testing.T) {
	s, c, _, dir := projectFixture(t)
	project := core.TeamProject{ID: "project", Platform: "feishu", ChatID: "oc_project", Manager: "manager", ManagerApp: "cli_manager", Bots: []string{"manager"}, Directories: []string{dir}, State: "ready"}
	if err := s.store.Put(project); err != nil {
		t.Fatal(err)
	}
	p := &roleTestPlatform{appID: "cli_manager"}
	if err := s.attachRole(context.Background(), c, p, project, "dev"); err == nil {
		t.Fatal("accepted unconfirmed bot")
	}
	all, _ := s.store.All()
	if len(all["project"].Bots) != 1 {
		t.Fatal("failed admission published a member")
	}
}

func TestTeamCoordination_AttachRoleChecksOwnIdentityBeforePublishing(t *testing.T) {
	for _, scenario := range []string{"ready", "absent", "api_error", "client_error"} {
		t.Run(scenario, func(t *testing.T) {
			s, cfg, _, dir := projectFixture(t)
			project := core.TeamProject{ID: "project", Platform: "feishu", ChatID: "oc_project", Manager: "manager", ManagerApp: "cli_manager", Bots: []string{"manager"}, BotApps: []string{"cli_manager"}, Directories: []string{dir}, State: "ready"}
			if err := s.store.Put(project); err != nil {
				t.Fatal(err)
			}
			manager := &roleTestPlatform{appID: "cli_manager", present: true}
			member := &roleTestPlatform{appID: "cli_dev", present: scenario != "absent"}
			if scenario == "api_error" {
				member.checkErr = fmt.Errorf("verification unavailable")
			}
			s.groupPlatformFactory = func(platform string, options map[string]any) (core.Platform, error) {
				if platform != "feishu" || options["app_id"] != "cli_dev" {
					return nil, fmt.Errorf("wrong identity for member verification")
				}
				if scenario == "client_error" {
					return nil, fmt.Errorf("client unavailable")
				}
				return member, nil
			}
			err := s.attachRole(context.Background(), cfg, manager, project, "dev")
			if (err == nil) != (scenario == "ready") {
				t.Fatalf("admission result: %v", err)
			}
			if err != nil && (!strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "cli_dev")) {
				t.Fatalf("membership error omitted the affected role identity: %v", err)
			}
			if strings.Join(manager.checks, ",") != "cli_manager" || manager.adds != 1 {
				t.Fatal("manager must admit members and only verify itself")
			}
			if scenario != "client_error" && (strings.Join(member.checks, ",") != "cli_dev" || !member.stopped || member.adds != 0) {
				t.Fatal("member must verify itself, release client, and never invite")
			}
			all, err := s.store.All()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "ready" {
				if strings.Join(all["project"].Bots, ",") != "manager,dev" || strings.Join(all["project"].BotApps, ",") != "cli_manager,cli_dev" {
					t.Fatal("confirmed identity not registered")
				}
				return
			}
			if len(all["project"].Bots) != 1 || len(all["project"].BotApps) != 1 {
				t.Fatal("failed membership confirmation published the role")
			}
			// A partial admission can be retried using the same existing role.
			member.present, member.checkErr = true, nil
			s.groupPlatformFactory = func(string, map[string]any) (core.Platform, error) { return member, nil }
			for n := 0; n < 2; n++ {
				if err := s.attachRole(context.Background(), cfg, manager, project, "dev"); err != nil {
					t.Fatal(err)
				}
			}
			all, _ = s.store.All()
			if len(all["project"].Bots) != 2 || len(all["project"].BotApps) != 2 {
				t.Fatal("recovery duplicated existing role")
			}
		})
	}
}
