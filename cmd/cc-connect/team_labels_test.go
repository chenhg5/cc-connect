package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type labelTestPlatform struct {
	creationFormTestPlatform
	labelMu                                sync.Mutex
	authorized, fail                       bool
	labelCreates, labelAdds, logins, opens int
	labels                                 []core.PersonalLabel
	added, recipient                       string
}

func (p *labelTestPlatform) PersonalLabelsAuthorized(string) bool {
	p.labelMu.Lock()
	defer p.labelMu.Unlock()
	return p.authorized
}
func (p *labelTestPlatform) AuthorizePersonalLabels(context.Context, string, func(string) error) error {
	p.labelMu.Lock()
	defer p.labelMu.Unlock()
	p.logins++
	p.authorized = true
	return nil
}
func (p *labelTestPlatform) ListPersonalLabels(context.Context, string) ([]core.PersonalLabel, error) {
	p.labelMu.Lock()
	defer p.labelMu.Unlock()
	return append([]core.PersonalLabel{}, p.labels...), nil
}
func (p *labelTestPlatform) CreatePersonalLabel(_ context.Context, _ string, name string) (core.PersonalLabel, error) {
	p.labelMu.Lock()
	defer p.labelMu.Unlock()
	p.labelCreates++
	g := core.PersonalLabel{ID: fmt.Sprintf("ofg_%d", p.labelCreates), Name: name, Type: "normal"}
	p.labels = append(p.labels, g)
	return g, nil
}
func (p *labelTestPlatform) AddPersonalLabel(_ context.Context, _, chat, id string) error {
	p.labelMu.Lock()
	defer p.labelMu.Unlock()
	p.labelAdds++
	if p.fail {
		return &core.PersonalLabelError{Code: "add_failed"}
	}
	p.added = chat + ":" + id
	return nil
}
func (p *labelTestPlatform) OpenLabelConversation(_ context.Context, user, _, _ string) (string, error) {
	p.labelMu.Lock()
	defer p.labelMu.Unlock()
	p.opens++
	p.recipient = user
	return "oc_bot", nil
}
func (p *labelTestPlatform) IsPrivateUserMessage(msg *core.Message) bool {
	return msg.ReplyCtx == "private"
}
func (p *labelTestPlatform) setFail(v bool) { p.labelMu.Lock(); defer p.labelMu.Unlock(); p.fail = v }
func labelServiceFixture(t *testing.T) (*teamProjectService, *config.Config, config.TeamConfig, *labelTestPlatform) {
	s, c, team, _ := projectFixture(t)
	c.Language = "en"
	c.Projects[0].BotOwnerID = "owner"
	c.Projects[1].BotOwnerID = "new-app-owner"
	writeFormConfig(t, s.path, c)
	p := &labelTestPlatform{authorized: true}
	p.manager = true
	s.labelPlatformFactory = func(string, map[string]any) (core.Platform, error) { return p, nil }
	return s, c, team, p
}
func TestCUJ_TeamLabels_DefaultOverrideFailureAndRestartRetry(t *testing.T) {
	s, c, _, p := labelServiceFixture(t)
	e := core.NewEngine("manager", &createdBotTestAgent{}, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	t.Cleanup(func() { e.Stop() })
	e.SetAdminFrom("owner")
	e.SetTeamProjects(s.store, s.handler("manager"))
	e.SetProjectCommand(s.projectHandler("manager"))
	send := func(command string, values map[string][]string) {
		e.ReceiveMessage(p, &core.Message{UserID: "owner", SessionKey: "feishu:private:owner", ReplyCtx: "private", Content: command, FormValues: values})
	}
	send("/team labels", nil)
	formWait(t, &p.creationFormTestPlatform, "Label settings")
	send("/team labels save", map[string][]string{"new_bot_labels": {"Bots"}, "new_project_labels": {"Tasks"}})
	formWait(t, &p.creationFormTestPlatform, "Default labels saved")
	send("/project create", nil)
	formWait(t, &p.creationFormTestPlatform, "Labels for this creation")
	card := p.latestForm()
	selected := false
	for _, f := range card.Form.Fields {
		if f.Name == "labels" {
			selected = len(f.Defaults) == 1 && f.Defaults[0] == "new:Tasks"
		}
	}
	if !selected {
		t.Fatal("default not populated")
	}
	p.setFail(true)
	send(card.Form.Command, map[string][]string{"name": {"Task One"}, "directories": {t.TempDir()}, "labels": {}, "new_labels": {"Custom"}})
	formWait(t, &p.creationFormTestPlatform, "Creation is preserved")
	all, _ := s.store.All()
	if len(all) != 1 {
		t.Fatal("creation missing")
	}
	for _, v := range all {
		if v.State != "ready" {
			t.Fatal("label failure broke group")
		}
	}
	// Recreate the service to verify persisted IDs and targets, without creating a second group.
	s2 := newTeamProjectService(s.path, s.dataDir)
	p.setFail(false)
	_, err := s2.handler("manager")(context.Background(), p, &core.Message{UserID: "owner", ReplyCtx: "private"}, []string{"labels", "retry"})
	if err != nil {
		t.Fatal(err)
	}
	p.labelMu.Lock()
	creates, added := p.labelCreates, p.added
	p.labelMu.Unlock()
	if creates != 1 || !strings.HasSuffix(added, ":ofg_1") {
		t.Fatal("retry created duplicate label or failed", creates, added)
	}
	if p.creates != 1 {
		t.Fatal("duplicate group")
	}
	path, _ := s.labelPath(c, "manager", "owner")
	state, _ := readTeamLabels(path)
	for _, job := range state.Jobs {
		if !job.Done || job.Labels[0].Name != "Custom" {
			t.Fatal("wrong selection", job)
		}
	}
	if state.Project[0].Name != "Tasks" {
		t.Fatal("override changed defaults")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("preferences not private")
	}
}
func TestTeamLabels_ExplicitEmptySelectionAndIdentityIsolation(t *testing.T) {
	s, c, team, p := labelServiceFixture(t)
	path, _ := s.labelPath(c, "manager", "owner")
	saveTeamLabels(path, teamLabelState{Bot: []core.PersonalLabel{{Name: "Bots"}}, Project: []core.PersonalLabel{{Name: "Tasks"}}})
	card := teamCreationForm(c, team, "manager", "project", t.TempDir())
	msg := &core.Message{UserID: "owner", ReplyCtx: "private"}
	if err := s.creationLabels(p, msg, c, "manager", "project", card); err != nil {
		t.Fatal(err)
	}
	ctx, err := labelCreationContext(context.Background(), card, map[string][]string{"name": {"One"}, "labels": {}, "new_labels": {}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.prepareLabelJob(ctx, c, "manager", "owner", "project", "one"); err != nil {
		t.Fatal(err)
	}
	state, _ := readTeamLabels(path)
	if job := state.Jobs["project:one"]; !job.Done || len(job.Labels) > 0 {
		t.Fatal("empty reset to defaults")
	}
	if _, err = s.labelPath(c, "manager", "stranger"); err == nil {
		t.Fatal("shared personal preferences")
	}
	answer, err := s.handler("manager")(context.Background(), p, &core.Message{UserID: "owner", ReplyCtx: "group"}, []string{"labels", "authorize"})
	if err != nil || !strings.Contains(answer, "private") || p.logins != 0 {
		t.Fatal("OAuth initiated in group")
	}
}
func TestTeamLabels_BotUsesItsOwnOwnerAndIndependentRetry(t *testing.T) {
	s, c, _, p := labelServiceFixture(t)
	path, _ := s.labelPath(c, "manager", "owner")
	saveTeamLabels(path, teamLabelState{Bot: []core.PersonalLabel{{Name: "Bots"}}})
	if err := s.prepareBotLabels(context.Background(), "manager", "owner", "dev"); err != nil {
		t.Fatal(err)
	}
	p.setFail(true)
	s.labelCreatedBot(context.Background(), &botCreationRecord{Parent: "manager", Initiator: "owner", Name: "dev"}, func(core.MsgKey, ...any) {})
	p.setFail(false)
	text := s.finishLabelJob(context.Background(), p, c, "manager", "owner", "bot", "dev")
	if !strings.Contains(text, "Labels added") {
		t.Fatal(text)
	}
	if p.opens != 1 || p.recipient != "new-app-owner" || p.added != "oc_bot:ofg_1" || p.labelCreates != 1 {
		t.Fatal("bot opened twice, wrong owner, or duplicate label", p.opens, p.recipient, p.added)
	}
}
func TestTeamLabels_MissingAuthorizationLeavesCreationReady(t *testing.T) {
	s, c, team, p := labelServiceFixture(t)
	p.authorized = false
	path, _ := s.labelPath(c, "manager", "owner")
	saveTeamLabels(path, teamLabelState{Project: []core.PersonalLabel{{Name: "Tasks"}}})
	_, err := s.create(context.Background(), c, team, p, "feishu", "owner", "first", []string{t.TempDir()}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	text := s.finishLabelJob(context.Background(), p, c, "manager", "owner", "project", "first")
	if !strings.Contains(text, "authorization") || p.creates != 1 || p.labelCreates != 0 {
		t.Fatal(text)
	}
	// A forged selection that the issued form did not offer must fail validation.
	card := teamCreationForm(c, team, "manager", "bot")
	s.creationLabels(p, &core.Message{UserID: "owner"}, c, "manager", "bot", card)
	card.Form.Fields = append(card.Form.Fields, core.CardFormField{Name: "labels", Label: "Labels", Kind: "multiselect", Options: []core.CardSelectOption{{Text: "Allowed", Value: "ofg_allowed"}}})
	if _, err := labelCreationContext(context.Background(), card, map[string][]string{"labels": {"ofg_forged"}}); err == nil {
		t.Fatal("forged group accepted")
	}
}
func TestTeamLabels_TransientListFailureIsNotEmptySuccess(t *testing.T) {
	i := core.NewI18n(core.LangEnglish)
	if out := labelErrorText(i, errors.New("secret-data")); strings.Contains(out, "secret-data") || !strings.Contains(out, "storage_error") {
		t.Fatal(out)
	}
}
