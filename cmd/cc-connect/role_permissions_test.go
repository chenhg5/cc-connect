package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func TestRolePermissions_DefaultProfilesAndIdentity(t *testing.T) {
	for _, role := range core.AgentRoleTemplates() {
		scopes, err := rolePermissionScopes(role.ID)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, s := range scopes {
			key := s.Type + ":" + s.Name
			if seen[key] {
				t.Fatal("duplicate", key)
			}
			seen[key] = true
			if role.ID != "manager" && (s.Type == "user" || s.Name == "im:message.group_msg" || s.Name == "im:chat.members:write_only") {
				t.Fatal("worker received manager permissions", role.ID, s)
			}
		}
		for _, name := range []string{"im:message:send_as_bot", "cardkit:card:write", "im:message.group_at_msg.include_bot:readonly", "im:chat.members:read"} {
			if !seen["tenant:"+name] {
				t.Fatal("collaboration permission missing", role.ID, name)
			}
		}
		if role.ID == "manager" {
			for _, key := range []string{"tenant:im:chat:create", "tenant:im:chat.members:write_only", "tenant:im:message.group_msg", "user:im:feed_group_v1:read", "user:im:feed_group_v1:write", "user:offline_access"} {
				if !seen[key] {
					t.Fatal("missing manager default", key)
				}
			}
		}
	}
	if _, err := rolePermissionScopes("administrator-all"); err == nil {
		t.Fatal("unrecognized role accepted")
	}
}

func scopeTestClient(t *testing.T, scopes func() []roleScope, apply func(http.ResponseWriter, *http.Request)) (*rolePermissionClient, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["app_id"] != "cli_child" || body["app_secret"] != "test-secret" {
				t.Error("wrong application credentials")
			}
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"private-token"}`)
		case "/open-apis/application/v6/scopes":
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("not using own application token")
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"scopes": scopes()}})
		case "/open-apis/application/v6/scopes/apply":
			calls.Add(1)
			if r.Method != "POST" || r.Body != http.NoBody && r.ContentLength != 0 {
				t.Error("apply must not accept a fictitious scope selector")
			}
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("wrong application token")
			}
			if apply != nil {
				apply(w, r)
			} else {
				fmt.Fprint(w, `{"code":0}`)
			}
		default:
			t.Error("unexpected API path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return &rolePermissionClient{base: server.URL, http: server.Client(), dataDir: t.TempDir()}, calls
}
func roleTestIdentity() registrationFlowResult {
	return registrationFlowResult{AppID: "cli_child", AppSecret: "test-secret", Platform: "feishu"}
}
func grantedRole(role string) []roleScope {
	s, _ := rolePermissionScopes(role)
	for i := range s {
		s[i].Status = 1
	}
	return s
}

func TestRolePermissions_ApplyOnlyConfiguredMissingAndThrottleAcrossRestart(t *testing.T) {
	scopes := grantedRole("manager")
	scopes[0].Status = 0
	c, calls := scopeTestClient(t, func() []roleScope { return scopes }, nil)
	for attempt := 0; attempt < 2; attempt++ {
		restarted := *c
		report := restarted.ensure(context.Background(), roleTestIdentity(), "manager")
		if !report.Checked || !report.Submitted || len(report.Missing) != 1 {
			t.Fatalf("unexpected report: %+v", report)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate administrator request", calls.Load())
	}
	// An approved permission is never requested again, even with an old stamp.
	scopes[0].Status = 1
	if r := c.ensure(context.Background(), roleTestIdentity(), "manager"); len(r.Missing) != 0 || r.Submitted || !r.Checked {
		t.Fatal(r)
	}
	if calls.Load() != 1 {
		t.Fatal("requested granted permissions")
	}
	files, _ := filepath.Glob(filepath.Join(c.dataDir, "role-permissions", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	info, _ := os.Stat(files[0])
	b, _ := os.ReadFile(files[0])
	if info.Mode().Perm() != 0600 || strings.Contains(string(b), "private-token") || strings.Contains(string(b), "test-secret") {
		t.Fatal("unsafe request receipt")
	}
}

func TestRolePermissions_AbsentOrUnrelatedScopesUseRoleLinks(t *testing.T) {
	for _, extra := range []bool{false, true} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			scopes := grantedRole("developer")
			scopes = scopes[1:]
			if extra {
				scopes = append(scopes, roleScope{Name: "mail:user_mailbox.message:send", Type: "user", Status: 0})
				scopes[0].Status = 0
			}
			c, calls := scopeTestClient(t, func() []roleScope { return scopes }, nil)
			report := c.ensure(context.Background(), roleTestIdentity(), "developer")
			if !report.Checked || len(report.Missing) == 0 || report.Submitted || calls.Load() != 0 {
				t.Fatal(report, calls.Load())
			}
			links := permissionLinks(roleTestIdentity(), report.Missing)
			if strings.Contains(links, "mail") || strings.Contains(links, "test-secret") {
				t.Fatal("unrelated scope or secret included")
			}
		})
	}
}

func TestRolePermissions_UserGrantDoesNotSatisfyTenantAndLinksPreserveType(t *testing.T) {
	scopes := grantedRole("manager")
	scopes[0].Type = "user"
	scopes = scopes[:len(scopes)-1]
	c, _ := scopeTestClient(t, func() []roleScope { return scopes }, nil)
	r := c.ensure(context.Background(), roleTestIdentity(), "manager")
	if len(r.Missing) != 2 {
		t.Fatal("identity mixed", r)
	}
	id := roleTestIdentity()
	id.Platform = "lark"
	links := strings.Split(permissionLinks(id, r.Missing), "\n")
	if len(links) != 2 {
		t.Fatal(links)
	}
	for idx, kind := range []string{"tenant", "user"} {
		u, err := url.Parse(links[idx])
		if err != nil || u.Host != "open.larksuite.com" || u.Path != "/app/cli_child/auth" || u.Query().Get("token_type") != kind || u.Query().Get("q") == "" {
			t.Fatal("wrong application or identity in permission link", links)
		}
	}
}

func TestRolePermissions_ApplyFailureNotSuccessAndNoSecretLeak(t *testing.T) {
	scopes := grantedRole("tester")
	scopes[0].Status = 0
	c, calls := scopeTestClient(t, func() []roleScope { return scopes }, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":99991672,"msg":"test-secret private-token"}`)
	})
	for retry := 0; retry < 2; retry++ {
		report := c.ensure(context.Background(), roleTestIdentity(), "tester")
		if report.Submitted || len(report.Missing) != 1 || strings.Contains(report.Error, "secret") {
			t.Fatal(report)
		}
		var text []string
		permissionNotice(roleTestIdentity(), "tester", report, func(k core.MsgKey, a ...any) { text = append(text, core.NewI18n(core.LangEnglish).Tf(k, a...)) })
		if strings.Contains(strings.Join(text, "\n"), "test-secret") {
			t.Fatal("credential leak")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("timed-out/failed apply spammed admins")
	}
}

func TestRolePermissions_UnavailableScopeCheckRetainsFullRolePlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-secret"}`)
	}))
	defer server.Close()
	c := &rolePermissionClient{base: server.URL, http: server.Client(), dataDir: t.TempDir()}
	r := c.ensure(context.Background(), roleTestIdentity(), "manager")
	if r.Checked || len(r.Missing) < 3 || r.Error == "" {
		t.Fatal(r)
	}
}

func TestRolePermissions_CreatedRoleChecksAfterDurableSaveAndRetry(t *testing.T) {
	c, starts := botCreatorFixture(t)
	cfg, err := config.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Teams = []config.TeamConfig{{ID: "default", Manager: "supervisor", Members: []config.TeamMember{{Bot: "supervisor", Role: "manager"}}}}
	writeFormConfig(t, c.configPath, cfg)
	registered := 0
	c.register = func(registrationFlowOptions) (*registrationFlowResult, error) {
		registered++
		id := roleTestIdentity()
		id.OwnerOpenID = "ou_child_owner"
		return &id, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		r, err := c.prepareWithOptions(context.Background(), "supervisor", "parent-owner", "qa", false, func(core.MsgKey, ...any) {}, "tester")
		if err != nil {
			t.Fatal(err)
		}
		if r.TeamRole != "tester" {
			t.Fatal("role lost before permission check")
		}
		r.TeamBackend = "codex"
		if err := c.saveRecord(r); err != nil {
			t.Fatal(err)
		}
		checked := false
		c.permissionCheck = func(_ context.Context, id registrationFlowResult, role string) rolePermissionReport {
			saved, err := config.Load(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			if id.AppID != "cli_child" || role != "tester" || len(saved.Projects) != 2 {
				t.Error("permission setup before bot persisted or wrong role/identity")
			}
			checked = true
			return rolePermissionReport{Error: "scope_check_failed", Missing: []roleScope{{Name: "cardkit:card:write", Type: "tenant"}}}
		}
		done := make(chan []core.MsgKey, 1)
		go func() {
			var keys []core.MsgKey
			c.finish(context.Background(), r, func(k core.MsgKey, _ ...any) { keys = append(keys, k) })
			done <- keys
		}()
		if attempt == 0 {
			req := <-starts
			req.done <- nil
		}
		keys := <-done
		if !checked || !containsMsgKey(keys, core.MsgRolePermissionsUnknown) {
			t.Fatal("permission failure hidden", keys)
		}
	}
	if registered != 1 {
		t.Fatal("retry created another bot")
	}
	if _, err := c.prepareWithOptions(context.Background(), "supervisor", "parent-owner", "qa", false, func(core.MsgKey, ...any) {}, "manager"); err == nil {
		t.Fatal("retry changed scope role")
	}
}
func containsMsgKey(keys []core.MsgKey, key core.MsgKey) bool {
	for _, v := range keys {
		if v == key {
			return true
		}
	}
	return false
}

func TestRolePermissions_ManagerInitRunsAfterPersistence(t *testing.T) {
	spec := teamInitFixture(t, "codex")
	checks := 0
	err := teamInitCommandWithDeps([]string{"--config", spec.ConfigPath, "--work-dir", spec.WorkDir, "--no-start"}, fakeTeamRegistration, func(string) error { t.Fatal("unexpected start"); return nil }, &strings.Builder{}, func(_ context.Context, id registrationFlowResult, role, dataDir string) rolePermissionReport {
		checks++
		if _, err := os.Stat(spec.ConfigPath); err != nil {
			t.Fatal("credentials not persisted", err)
		}
		if role != "manager" || id.AppID != "cli_test_manager" || dataDir != spec.DataDir {
			t.Fatal("wrong manager identity/role")
		}
		return rolePermissionReport{Checked: true}
	})
	if err != nil || checks != 1 {
		t.Fatal(err, checks)
	}
}

func TestCUJ_RolePermissions_PrivateCheckRetryAndGranted(t *testing.T) {
	s, cfg, _, _ := projectFixture(t)
	cfg.Language = "en"
	writeFormConfig(t, s.path, cfg)
	p := &creationFormTestPlatform{}
	e := core.NewEngine("manager", &createdBotTestAgent{}, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	defer e.Stop()
	e.SetAdminFrom("owner")
	e.SetTeamProjects(s.store, s.handler("manager"))
	var checked []string
	s.permissionCheck = func(_ context.Context, id registrationFlowResult, role string) rolePermissionReport {
		checked = append(checked, id.AppID+":"+role)
		if len(checked) == 1 {
			return rolePermissionReport{Checked: true, Missing: []roleScope{{Name: "cardkit:card:write", Type: "tenant"}}}
		}
		return rolePermissionReport{Checked: true}
	}
	send := func(user, rc, command, want string) {
		p.mu.Lock()
		p.sent = nil
		p.mu.Unlock()
		e.ReceiveMessage(p, &core.Message{UserID: user, SessionKey: "feishu:" + rc + ":" + user, ReplyCtx: rc, Content: command})
		formWait(t, p, want)
	}
	send("owner", "group", "/team permissions dev", "private")
	send("stranger", "private", "/team permissions dev", "admin")
	send("owner", "private", "/team permissions dev", "cli_dev/auth")
	send("owner", "private", "/team permissions dev", "permissions are active")
	if !reflect.DeepEqual(checked, []string{"cli_dev:developer", "cli_dev:developer"}) {
		t.Fatal("authorization bypass or wrong app", checked)
	}
}
