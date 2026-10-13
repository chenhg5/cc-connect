package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type memberFormTestPlatform struct {
	creationFormTestPlatform
	cards []*core.Card
}

func (p *memberFormTestPlatform) ReplyCard(_ context.Context, _ any, card *core.Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cards = append(p.cards, card)
	p.sent = append(p.sent, card.RenderText())
	return nil
}
func (p *memberFormTestPlatform) SendCard(ctx context.Context, rc any, card *core.Card) error {
	return p.ReplyCard(ctx, rc, card)
}
func (p *memberFormTestPlatform) latestCard() *core.Card {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cards[len(p.cards)-1]
}
func (p *memberFormTestPlatform) clearSent() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = nil
}
func memberFormsFixture(t *testing.T) (*teamProjectService, *config.Config) {
	t.Helper()
	s, cfg, _, _ := projectFixture(t)
	cfg.Language = "en"
	cfg.Teams[0].Members[1].Prompt = "Original identity"
	cfg.Projects[1].Agent.Options = map[string]any{"model": "keep-model", "work_dir": t.TempDir()}
	writeFormConfig(t, s.path, cfg)
	old := config.ConfigPath
	config.ConfigPath = s.path
	t.Cleanup(func() { config.ConfigPath = old })
	return s, cfg
}

// The actual roster button opens the form, the submitted identity is persisted,
// and reopening it shows what was saved. No external bot/app is created.
func TestCUJ_TeamForms_MembersEditAndReopen(t *testing.T) {
	s, _ := memberFormsFixture(t)
	p := &memberFormTestPlatform{}
	e := core.NewEngine("manager", &createdBotTestAgent{}, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	t.Cleanup(func() { e.Stop() })
	e.SetAdminFrom("owner")
	e.SetTeamProjects(s.store, s.handler("manager"))
	send := func(command string, values map[string][]string, want string) {
		p.clearSent()
		e.ReceiveMessage(p, &core.Message{UserID: "owner", SessionKey: "feishu:private:owner", Content: command, ReplyCtx: "private", FormValues: values})
		formWait(t, &p.creationFormTestPlatform, want)
	}
	before, err := config.Load(s.path)
	if err != nil {
		t.Fatal(err)
	}
	send("/team members", nil, "dev · developer")
	var edit string
	for _, element := range p.latestCard().Elements {
		if row, ok := element.(core.CardListItem); ok && strings.HasPrefix(row.Text, "dev · developer ·") {
			edit = strings.TrimPrefix(row.BtnValue, "cmd:")
		}
	}
	if edit == "" {
		t.Fatal("Members has no identity edit button")
	}
	send(edit, nil, "Original identity")
	if !strings.Contains(p.getSent(), "service restart") || !strings.Contains(p.getSent(), "across its projects") {
		t.Fatal("form must explain when and where the change takes effect")
	}
	form := p.latestForm().Form
	prompt := "Review code\nKeep literal $(data) and /team member set"
	// Extra values cannot select another member; keeping the backend preserves its options.
	values := map[string][]string{"role": {"reviewer"}, "prompt": {prompt}, "bot": {"manager"}, "id": {"another-id"}, "name": {"forged name"}, "backend": {"codex"}}
	send(form.Command, values, "Bot settings saved. Restart the service")
	send("/team members", nil, "dev · reviewer")
	send(edit, nil, prompt)
	if !strings.Contains(p.getSent(), "Bot name in Feishu (read-only): dev") || strings.Contains(p.getSent(), "forged name") || strings.Contains(p.getSent(), "another-id") {
		t.Fatal("name or internal ID was editable")
	}
	if !strings.Contains(p.getSent(), "Role: reviewer") {
		t.Fatal("reopened form did not prefill the saved role")
	}
	after, err := config.Load(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Projects, after.Projects) || !reflect.DeepEqual(before.Teams[0].Members[0], after.Teams[0].Members[0]) {
		t.Fatal("identity edit changed bot runtime/backend or the manager")
	}
	if after.Teams[0].Members[1].Prompt != prompt {
		t.Fatal("identity instructions lost structured text")
	}
	// Clearing an optional prompt must remove it, rather than restore the default.
	send(edit, map[string][]string{"role": {"reviewer"}, "prompt": {""}}, "Bot settings saved.")
	send(edit, nil, "Modify bot")
	if strings.Contains(p.getSent(), prompt) {
		t.Fatal("cleared instructions were restored")
	}
}

func TestTeamMemberForms_RejectInvalidEditsAndRemovedCommand(t *testing.T) {
	s, cfg := memberFormsFixture(t)
	p := &memberFormTestPlatform{}
	before, _ := os.ReadFile(s.path)
	for _, tc := range []struct {
		name, bot string
		args      []string
		values    map[string][]string
		wantError bool
	}{
		{"removed set", "manager", []string{"member", "set", "dev", "tester", "changed"}, nil, false},
		{"non-manager", "dev", []string{"members", "dev"}, map[string][]string{"role": {"tester"}}, true},
		{"unknown member", "manager", []string{"members", "outsider"}, map[string][]string{"role": {"tester"}}, true},
		{"manager demotion", "manager", []string{"members", "manager"}, map[string][]string{"role": {"developer"}}, true},
		{"invalid role", "manager", []string{"members", "dev"}, map[string][]string{"role": {"invalid"}}, true},
		{"invalid backend", "manager", []string{"members", "dev"}, map[string][]string{"backend": {"evil"}}, true},
		{"opening is read-only", "manager", []string{"members", "dev"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer, err := s.handler(tc.bot)(context.Background(), p, &core.Message{UserID: "owner", FormValues: tc.values}, tc.args)
			if (err != nil) != tc.wantError {
				t.Fatalf("answer %q error %v", answer, err)
			}
			if tc.name == "removed set" && (strings.Contains(answer, "member set") || !strings.Contains(answer, "/team members")) {
				t.Fatalf("old command still exposed: %q", answer)
			}
			after, _ := os.ReadFile(s.path)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected edit/opening/removed command mutated config")
			}
		})
	}
	// A manager's instructions are editable, while its role stays fixed.
	form := teamMemberForm(cfg, cfg.Teams[0], cfg.Teams[0].Members[0])
	if len(form.Form.Fields[0].Options) != 1 || form.Form.Fields[0].Defaults[0] != "manager" {
		t.Fatal("manager role is not fixed")
	}
	_, err := s.handler("manager")(context.Background(), p, &core.Message{FormValues: map[string][]string{"role": {"manager"}, "prompt": {"Coordinate reviews"}}}, []string{"members", "manager"})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := config.Load(s.path)
	if after.Teams[0].Members[0].Prompt != "Coordinate reviews" {
		t.Fatal("manager instructions not saved")
	}
}

func TestTeamMemberForms_ReadOnlyAndUnsupportedPlatforms(t *testing.T) {
	s, _ := memberFormsFixture(t)
	p := &memberFormTestPlatform{}
	_, err := s.handler("dev")(context.Background(), p, &core.Message{}, []string{"members"})
	if err != nil {
		t.Fatal(err)
	}
	for _, element := range p.latestCard().Elements {
		if _, ok := element.(core.CardListItem); ok {
			t.Fatal("non-manager roster has an edit button")
		}
	}
	if !strings.Contains(p.getSent(), "dev · developer") {
		t.Fatal("read-only roster missing")
	}
	plain := &projectCommandPlatform{}
	text, err := s.handler("manager")(context.Background(), plain, &core.Message{}, []string{"members", "dev"})
	if err != nil || !strings.Contains(text, "does not support identity forms") {
		t.Fatalf("unsupported fallback: %q %v", text, err)
	}
}
