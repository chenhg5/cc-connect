package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type creationFormTestPlatform struct {
	projectCommandPlatform
	mu    sync.Mutex
	forms []*core.Card
	sent  []string
}

func (p *creationFormTestPlatform) ReplyForm(_ context.Context, _ *core.Message, card *core.Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forms = append(p.forms, card)
	p.sent = append(p.sent, card.RenderText())
	return nil
}
func (p *creationFormTestPlatform) Reply(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, text)
	return nil
}
func (p *creationFormTestPlatform) Send(ctx context.Context, rc any, text string) error {
	return p.Reply(ctx, rc, text)
}
func (p *creationFormTestPlatform) getSent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.sent, "\n")
}
func (p *creationFormTestPlatform) latestForm() *core.Card {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.forms) == 0 {
		return nil
	}
	return p.forms[len(p.forms)-1]
}
func formWait(t *testing.T, p *creationFormTestPlatform, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.getSent(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("want %q in %s", want, p.getSent())
}
func writeFormConfig(t *testing.T, path string, cfg *config.Config) {
	t.Helper()
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

// Full user journey with real handlers, Engine and persistence; only platform
// delivery/group creation and the agent process are replaced.
type directoryFormTestAgent struct {
	createdBotTestAgent
	dir string
}

func (a *directoryFormTestAgent) GetWorkDir() string { return a.dir }

func TestCUJ_TeamForms_ProjectCreateRetryAndList(t *testing.T) {
	s, c, _, dir := projectFixture(t)
	dir += " with spaces,中文"
	c.Language = "en"
	writeFormConfig(t, s.path, c)
	p := &creationFormTestPlatform{projectCommandPlatform: projectCommandPlatform{fakeProjectGroups: fakeProjectGroups{manager: true, failAdd: true}}}
	e := core.NewEngine("manager", &directoryFormTestAgent{dir: filepath.Dir(dir)}, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	t.Cleanup(func() { e.Stop() })
	e.SetAdminFrom("owner")
	e.SetTeamProjects(s.store, s.handler("manager"))
	e.SetProjectCommand(s.projectHandler("manager"))
	send := func(content string, values map[string][]string) {
		e.ReceiveMessage(p, &core.Message{UserID: "owner", SessionKey: "feishu:private:owner", Content: content, ReplyCtx: "private", FormValues: values})
	}
	send("/project create", nil)
	formWait(t, p, "Project directory (absolute path)")
	form := p.latestForm().Form
	internalID := strings.TrimPrefix(form.Command, "/project _create ")
	if !core.ValidBotName(internalID) || !strings.HasPrefix(internalID, "project-") || strings.Contains(p.getSent(), internalID) {
		t.Fatal("project ID was not generated privately")
	}
	if form.HasDraftActions() || len(form.Fields) != 4 {
		t.Fatal("project creation must stay on one complete form without directory navigation")
	}
	for _, field := range form.Fields {
		if field.Name == "id" {
			t.Fatal("internal ID is editable")
		}
		if field.Name == "directories" && (field.Kind != "text" || !field.Required || !reflect.DeepEqual(field.Defaults, []string{filepath.Dir(dir)})) {
			t.Fatalf("directory field did not default to the conversation work directory: %+v", field)
		}
	}
	const projectName = "商城 Web API"
	values := map[string][]string{"name": {projectName}, "id": {"forged-id"}, "directories": {"relative/path"}, "members": {"dev"}, "goal": {"Line 1\nReview \"quotes\" and $(data)"}}
	command := p.latestForm().Form.Command
	send(command, values)
	formWait(t, p, "Check the field: Project directory")
	if p.creates != 0 {
		t.Fatal("invalid directory created a project group")
	}
	for _, field := range p.latestForm().Form.Fields {
		if !reflect.DeepEqual(field.Defaults, values[field.Name]) {
			t.Fatalf("invalid path retry lost %s", field.Name)
		}
	}
	values["directories"] = []string{dir}
	send(command, values)
	formWait(t, p, "temporary add failure")
	retry := p.latestForm()
	if retry.Form.Command != command {
		t.Fatal("retry changed the create action")
	}
	for _, field := range retry.Form.Fields {
		if !reflect.DeepEqual(field.Defaults, values[field.Name]) {
			t.Fatalf("retry lost %s", field.Name)
		}
	}
	p.failAdd = false
	send(retry.Form.Command, values)
	formWait(t, p, "https://example.invalid/oc_project")
	send("/project list", nil)
	formWait(t, p, projectName+" · ready · oc_project")
	all, err := s.store.All()
	if err != nil || len(all) != 1 || p.creates != 1 || p.groupName != projectName || all[internalID].Name != projectName || all[internalID].Goal != values["goal"][0] || !reflect.DeepEqual(all[internalID].Directories, []string{dir}) || all[internalID].Bots[0] != "manager" {
		t.Fatalf("creation changed: %+v %v", all, err)
	}
	if strings.Contains(p.getSent(), internalID) || strings.Contains(p.getSent(), "forged-id") {
		t.Fatal("internal ID leaked into the form, creation result or list")
	}
}

func TestTeamCreationForm_ProjectDefaultsAndMembers(t *testing.T) {
	_, cfg, team, dir := projectFixture(t)
	cfg.Language = "en"
	card := teamCreationForm(cfg, team, "manager", "project", dir)
	values := map[string][]string{"name": {"商城 Web API"}, "id": {"forged-id"}}
	args, err := creationFormArgs(card, values, team.Manager)
	internalID := strings.TrimPrefix(card.Form.Command, "/project _create ")
	if err != nil || args[1] != internalID || args[2] != dir || args[3] != "manager,dev" || args[5] != "商城 Web API" {
		t.Fatalf("default roster: %v %v", args, err)
	}
	other := teamCreationForm(cfg, team, "manager", "project", dir)
	if other.Form.Command == card.Form.Command {
		t.Fatal("different creation forms reused an internal ID")
	}
	for _, invalidDir := range []string{"", "relative/path", "~/project"} {
		values["directories"] = []string{invalidDir}
		if _, err := creationFormArgs(card, values, team.Manager); err == nil {
			t.Fatalf("invalid absolute directory accepted: %q", invalidDir)
		}
	}
	values["directories"] = []string{dir + " with spaces,中文"}
	args, err = creationFormArgs(card, values, team.Manager)
	if err != nil || args[2] != values["directories"][0] {
		t.Fatalf("typed directory changed: %v %v", args, err)
	}
	values["members"] = []string{}
	args, err = creationFormArgs(card, values, team.Manager)
	if err != nil || args[3] != "manager" {
		t.Fatalf("empty selection must retain only manager: %v %v", args, err)
	}
	values["members"] = []string{"outsider"}
	if _, err = creationFormArgs(card, values, team.Manager); err == nil {
		t.Fatal("allowed a bot outside the team")
	}
	values["members"] = nil
	values["name"] = []string{"  "}
	if _, err = creationFormArgs(card, values, team.Manager); err == nil {
		t.Fatal("empty project name accepted")
	}
	values["name"] = []string{"../中文 / API"}
	args, err = creationFormArgs(card, values, team.Manager)
	if err != nil || args[1] != internalID || args[5] != values["name"][0] {
		t.Fatal("display name was treated as an internal ID or filesystem path")
	}
	card.Form.Command = "/project _create ../invalid"
	if _, err = creationFormArgs(card, values, team.Manager); err == nil {
		t.Fatal("malformed internal ID accepted")
	}
}

func TestTeamCreationForm_BotPrivateOnlyAndUsesRegistrationFlow(t *testing.T) {
	for _, readFails := range []bool{false, true} {
		label := "name available"
		if readFails {
			label = "name unavailable"
		}
		t.Run(label, func(t *testing.T) {
			c, starts := botCreatorFixture(t)
			cfg, err := config.Load(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Language = "en"
			cfg.Teams = []config.TeamConfig{{ID: "default", Manager: "supervisor", Members: []config.TeamMember{{Bot: "supervisor", Role: "manager"}}}}
			writeFormConfig(t, c.configPath, cfg)
			s := newTeamProjectService(c.configPath, c.dataDir)
			s.creator = c
			p := &creationFormTestPlatform{}
			msg := &core.Message{UserID: "parent-owner", SessionKey: "feishu:private:parent-owner", ReplyCtx: "private"}
			count := 0
			c.register = func(options registrationFlowOptions) (*registrationFlowResult, error) {
				count++
				if err := options.OnVerification("https://example.invalid/register"); err != nil {
					return nil, err
				}
				return &registrationFlowResult{AppID: "cli_child", AppSecret: "test-secret", OwnerOpenID: "ou_scanner", Platform: "feishu"}, nil
			}
			group := *msg
			group.ReplyCtx = "group"
			_, err = s.handler("supervisor")(context.Background(), p, &group, []string{"bot", "create"})
			if err == nil || p.latestForm() != nil || count != 0 {
				t.Fatal("group initiated bot registration/form")
			}
			_, err = s.handler("supervisor")(context.Background(), p, msg, []string{"bot", "create"})
			if err != nil || p.latestForm() == nil || count != 0 {
				t.Fatalf("opening form starts registration: %v", err)
			}
			formCommand := strings.Fields(p.latestForm().Form.Command)[1:]
			internalID := formCommand[2]
			if !core.ValidBotName(internalID) || strings.Contains(p.latestForm().RenderText(), internalID) {
				t.Fatal("internal ID is invalid or visible")
			}
			c.readBotName = func(context.Context, registrationFlowResult) (string, error) {
				if readFails {
					return "", errors.New("temporary lookup failure")
				}
				return "代码审查助手", nil
			}
			msg.FormValues = map[string][]string{"role": {"developer"}, "backend": {"codex"}, "prompt": {"Keep \"quotes\"\nand line breaks"}}
			done := make(chan error, 1)
			go func() {
				_, err := s.handler("supervisor")(context.Background(), p, msg, formCommand)
				done <- err
			}()
			select {
			case request := <-starts:
				if request.name != internalID {
					t.Fatal(request.name)
				}
				request.done <- nil
			case <-time.After(2 * time.Second):
				t.Fatal("registration did not reach startup")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			wantName := "代码审查助手"
			if readFails {
				wantName = "The Feishu name could not be verified"
			}
			if count != 1 || !strings.Contains(p.getSent(), "https://example.invalid/register") || !strings.Contains(p.getSent(), wantName) {
				t.Fatal("registration flow was bypassed")
			}
			if strings.Contains(p.getSent(), internalID) || strings.Contains(p.getSent(), "prefill") {
				t.Fatal("registration exposed an internal ID or asked for a redundant name")
			}
			saved, err := config.Load(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			expectedName := "代码审查助手"
			if readFails {
				expectedName = ""
			}
			_, member := saved.TeamForBot(internalID)
			if member == nil || member.DisplayName != expectedName || member.Role != "developer" || member.Prompt != msg.FormValues["prompt"][0] {
				t.Fatal("role/prompt lost")
			}
			found := false
			for _, bot := range saved.Projects {
				if bot.Name == internalID {
					found = true
					if bot.BotOwnerID != "ou_scanner" || bot.Agent.Type != "codex" {
						t.Fatal("owner/backend lost")
					}
				}
			}
			if !found {
				t.Fatal("new bot missing")
			}

		})
	}
}

func TestTeamCreationForm_UnsupportedPlatformKeepsTextUsage(t *testing.T) {
	s, c, _, _ := projectFixture(t)
	c.Language = "en"
	writeFormConfig(t, s.path, c)
	p := &projectCommandPlatform{}
	text, err := s.projectHandler("manager")(context.Background(), p, &core.Message{UserID: "owner"}, []string{"create"})
	if err != nil || !strings.Contains(text, "/project create <id>") {
		t.Fatalf("text fallback: %s %v", text, err)
	}
}
