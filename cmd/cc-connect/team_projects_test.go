package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type fakeProjectGroups struct {
	creates, adds int
	failAdd       bool
	manager       bool
	uuid          string
	groupName     string
}

func (p *fakeProjectGroups) CreateProjectGroup(_ context.Context, name string, _ string, uuid string) (string, error) {
	p.creates++
	p.uuid = uuid
	p.groupName = name
	return "oc_project", nil
}

func TestTeamProject_DisplayNamePersistsAcrossRetry(t *testing.T) {
	s, c, team, dir := projectFixture(t)
	p := &fakeProjectGroups{failAdd: true, manager: true}
	const id, name = "project-internal", "商城 Web API"
	_, err := s.create(context.Background(), c, team, p, "feishu", "owner", id, []string{dir}, []string{"dev"}, "goal", name)
	if err == nil || p.groupName != name {
		t.Fatalf("group name does not match display name: %q %v", p.groupName, err)
	}
	s = newTeamProjectService(s.path, s.dataDir)
	p.failAdd = false
	_, err = s.create(context.Background(), c, team, p, "feishu", "owner", id, []string{dir}, []string{"dev"}, "goal", "另一个项目")
	if err == nil || p.creates != 1 {
		t.Fatal("retry changed the project name after the group was created")
	}
	v, err := s.create(context.Background(), c, team, p, "feishu", "owner", id, []string{dir}, []string{"dev"}, "goal", name)
	if err != nil || v.ID != id || v.Name != name || v.State != "ready" || p.creates != 1 {
		t.Fatalf("retry lost name/ID or created another group: %+v %v", v, err)
	}
}

func TestTeamProject_CreateRetryCannotReopenEndedProject(t *testing.T) {
	s, c, team, dir := projectFixture(t)
	p := &fakeProjectGroups{manager: true}
	v, err := s.create(context.Background(), c, team, p, "feishu", "owner", "closed", []string{dir}, []string{"dev"}, "goal")
	if err != nil {
		t.Fatal(err)
	}
	v.State, v.EndedBy, v.EndedAt = "ended", "owner", "2026-10-06T12:00:00Z"
	if err := s.store.Put(*v); err != nil {
		t.Fatal(err)
	}
	adds := p.adds
	if _, err := s.create(context.Background(), c, team, p, "feishu", "owner", "closed", []string{dir}, []string{"dev"}, "goal"); err == nil {
		t.Fatal("create retry reopened project")
	}
	all, _ := s.store.All()
	if all["closed"].State != "ended" || p.adds != adds || p.creates != 1 {
		t.Fatal("ended group was modified")
	}
}
func (p *fakeProjectGroups) EnsureProjectGroupBots(_ context.Context, _ string, apps []string) error {
	p.adds++
	if len(apps) < 1 || apps[0] != "cli_manager" {
		return errors.New("manager missing")
	}
	if p.failAdd {
		return errors.New("temporary add failure")
	}
	return nil
}
func (p *fakeProjectGroups) ProjectGroupHasBot(context.Context, string, string) (bool, error) {
	return p.manager, nil
}
func (p *fakeProjectGroups) ProjectGroupLink(id string) string {
	return "https://example.invalid/" + id
}
func projectFixture(t *testing.T) (*teamProjectService, *config.Config, config.TeamConfig, string) {
	dir := t.TempDir()
	s := newTeamProjectService(filepath.Join(dir, "config.toml"), dir)
	team := config.TeamConfig{ID: "default", Manager: "manager", Members: []config.TeamMember{{Bot: "manager", Role: "manager"}, {Bot: "dev", Role: "developer"}}}
	c := &config.Config{DataDir: dir, Teams: []config.TeamConfig{team}}
	for _, name := range []string{"manager", "dev"} {
		c.Projects = append(c.Projects, config.ProjectConfig{Name: name, Agent: config.AgentConfig{Type: "codex"}, Platforms: []config.PlatformConfig{{Type: "feishu", Options: map[string]any{"app_id": "cli_" + name}}}})
	}
	return s, c, team, filepath.Join(dir, "repo")
}
func TestTeamProject_CreateRetryKeepsGroupAndIncludesManager(t *testing.T) {
	s, c, team, dir := projectFixture(t)
	p := &fakeProjectGroups{failAdd: true, manager: true}
	_, err := s.create(context.Background(), c, team, p, "feishu", "ou_owner", "shop", []string{dir}, []string{"dev"}, "goal")
	if err == nil {
		t.Fatal("expected add failure")
	}
	all, _ := s.store.All()
	if all["shop"].ChatID != "oc_project" || all["shop"].State != "adding_members" {
		t.Fatal("group receipt lost")
	}
	p.failAdd = false
	s = newTeamProjectService(s.path, s.dataDir)
	result, err := s.create(context.Background(), c, team, p, "feishu", "ou_owner", "shop", []string{dir}, []string{"dev"}, "goal")
	if err != nil {
		t.Fatal(err)
	}
	if p.creates != 1 || p.uuid == "" || result.State != "ready" || result.Bots[0] != "manager" {
		t.Fatal("duplicate group or missing manager")
	}
	_, err = s.create(context.Background(), c, team, p, "feishu", "ou_other", "shop", []string{dir}, []string{"dev"}, "goal")
	if err == nil || p.creates != 1 {
		t.Fatal("another user claimed existing group")
	}
}
func TestTeamProject_ManagerRequiredAndUnsupportedRootsFailBeforeGroup(t *testing.T) {
	s, c, team, dir := projectFixture(t)
	p := &fakeProjectGroups{}
	_, err := s.create(context.Background(), c, team, p, "feishu", "ou_owner", "shop", []string{dir}, []string{"dev"}, "")
	if err == nil {
		t.Fatal("accepted missing manager")
	}
	all, _ := s.store.All()
	if all["shop"].State == "ready" {
		t.Fatal("reported incomplete group as ready")
	}
	// Use an actual unsupported backend; Codex now supports multiple roots.
	core.RegisterAgent("unsupported-project-test", func(map[string]any) (core.Agent, error) { return &stubMainAgent{}, nil })
	c.Projects[1].Agent.Type = "unsupported-project-test"
	_, err = s.create(context.Background(), c, team, p, "feishu", "ou_owner", "other", []string{dir, dir}, []string{"dev"}, "")
	if err == nil || p.creates != 1 {
		t.Fatal("multi-root check happened after external action")
	}
}

var _ core.ProjectGroupCreator = (*fakeProjectGroups)(nil)

type projectCommandPlatform struct {
	createdBotTestPlatform
	fakeProjectGroups
}

func TestProjectCommands_SeparateRosterFromProjectCreation(t *testing.T) {
	s, cfg, _, dir := projectFixture(t)
	cfg.Language = "en"
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	p := &projectCommandPlatform{fakeProjectGroups: fakeProjectGroups{manager: true}}
	msg := &core.Message{UserID: "ou_owner"}
	roster := s.handler("manager")
	projects := s.projectHandler("manager")
	text, err := roster(context.Background(), p, msg, nil)
	if err != nil || !strings.Contains(text, "/team members") || strings.Contains(text, "/project create") {
		t.Fatalf("Team help mixed with project commands: %q %v", text, err)
	}
	text, err = projects(context.Background(), p, msg, nil)
	if err != nil || !strings.Contains(text, "/project create") || strings.Contains(text, "/team bot create") {
		t.Fatalf("Project help mixed with bot commands: %q %v", text, err)
	}
	args := []string{"create", "shop", dir, "dev", "Deliver", "shop"}
	text, err = s.projectHandler("dev")(context.Background(), p, msg, args)
	if err == nil || p.creates != 0 {
		t.Fatalf("non-manager created group: %q %v", text, err)
	}
	for i := 0; i < 2; i++ {
		text, err = projects(context.Background(), p, msg, args)
		if err != nil || !strings.Contains(text, "https://example.invalid/oc_project") {
			t.Fatalf("project create/retry: %q %v", text, err)
		}
	}
	records, err := s.store.All()
	if err != nil || p.creates != 1 || records["shop"].Goal != "Deliver shop" {
		t.Fatalf("new command changed project creation semantics: %+v %v", records, err)
	}
	text, err = projects(context.Background(), p, msg, []string{"list"})
	if err != nil || !strings.Contains(text, "shop · ready · oc_project") {
		t.Fatalf("project missing: %q %v", text, err)
	}
	text, err = roster(context.Background(), p, msg, []string{"members"})
	if err != nil || !strings.Contains(text, "dev · developer") || strings.Contains(text, "shop") {
		t.Fatalf("Team roster contains projects: %q %v", text, err)
	}
}

func TestProjectCLI_HelpIsRegistered(t *testing.T) {
	if !runTopLevelCommand([]string{"project", "--help"}) {
		t.Fatal("project command not registered")
	}
}
