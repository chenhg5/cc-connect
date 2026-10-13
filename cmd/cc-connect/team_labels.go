package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type teamLabelState struct {
	Bot     []core.PersonalLabel     `json:"bot"`
	Project []core.PersonalLabel     `json:"project"`
	Catalog []core.PersonalLabel     `json:"catalog"`
	Jobs    map[string]*teamLabelJob `json:"jobs"`
}
type teamLabelJob struct {
	Kind, Resource, Name, Chat, UUID string
	Labels                           []core.PersonalLabel
	Done                             bool
	Error                            string
}

var teamLabelLocks sync.Map

func teamLabelLock(path string) *sync.Mutex {
	v, _ := teamLabelLocks.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func (s *teamProjectService) labelPath(cfg *config.Config, manager, user string) (string, error) {
	b, pc, err := teamBotPlatform(cfg, manager, "feishu")
	if err != nil {
		b, pc, err = teamBotPlatform(cfg, manager, "lark")
	}
	if err != nil {
		return "", err
	}
	if user == "" || (b.BotOwnerID != "" && b.BotOwnerID != user) {
		return "", &core.PersonalLabelError{Code: "wrong_user"}
	}
	app, _ := pc.Options["app_id"].(string)
	sum := sha256.Sum256([]byte(manager + "\x00" + pc.Type + "\x00" + app + "\x00" + user))
	return filepath.Join(s.dataDir, "team-labels", fmt.Sprintf("%x.json", sum)), nil
}
func readTeamLabels(path string) (teamLabelState, error) {
	state := teamLabelState{Jobs: map[string]*teamLabelJob{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return state, err
	}
	if state.Jobs == nil {
		state.Jobs = map[string]*teamLabelJob{}
	}
	return state, nil
}
func saveTeamLabels(path string, state teamLabelState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".labels-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}
func labelValue(g core.PersonalLabel) string {
	if g.ID != "" {
		return g.ID
	}
	return "new:" + g.Name
}
func labelOptions(groups ...[]core.PersonalLabel) []core.CardSelectOption {
	var out []core.CardSelectOption
	seen := map[string]bool{}
	for _, list := range groups {
		for _, g := range list {
			v := labelValue(g)
			if !seen[v] {
				out = append(out, core.CardSelectOption{Text: g.Name, Value: v})
				seen[v] = true
			}
		}
	}
	return out
}
func labelDefaults(list []core.PersonalLabel) []string {
	var out []string
	for _, g := range list {
		out = append(out, labelValue(g))
	}
	return out
}
func labelFields(i *core.I18n, name string, title core.MsgKey, defaults, catalog []core.PersonalLabel) []core.CardFormField {
	fields := []core.CardFormField{}
	options := labelOptions(catalog, defaults)
	if len(options) > 0 {
		fields = append(fields, core.CardFormField{Name: name, Label: i.T(title), Kind: "multiselect", Options: options, Defaults: labelDefaults(defaults)})
	}
	fields = append(fields, core.CardFormField{Name: "new_" + name, Label: i.T(title) + " · " + i.T(core.MsgLabelNew), Placeholder: i.T(core.MsgLabelNewHint), Kind: "multiline"})
	return fields
}
func chosenLabels(card *core.Card, values map[string][]string, name string) ([]core.PersonalLabel, error) {
	var result []core.PersonalLabel
	seen := map[string]bool{}
	add := func(g core.PersonalLabel) error {
		if utf8.RuneCountInString(g.Name) > 100 || strings.TrimSpace(g.Name) == "" || strings.ContainsAny(g.Name, "\x00\r\n") {
			return &core.PersonalLabelError{Code: "invalid_name"}
		}
		v := labelValue(g)
		if !seen[v] {
			result = append(result, g)
			seen[v] = true
		}
		if len(result) > 10 {
			return &core.PersonalLabelError{Code: "too_many_labels"}
		}
		return nil
	}
	for _, f := range card.Form.Fields {
		if f.Name == name {
			for _, v := range values[name] {
				for _, o := range f.Options {
					if v == o.Value {
						g := core.PersonalLabel{ID: v, Name: o.Text, Type: "normal"}
						if strings.HasPrefix(v, "new:") {
							g.ID = ""
						}
						if err := add(g); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	for _, text := range values["new_"+name] {
		for _, line := range strings.Split(text, "\n") {
			if n := strings.TrimSpace(line); n != "" {
				if err := add(core.PersonalLabel{Name: n, Type: "normal"}); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}
func labelErrorText(i *core.I18n, err error) string {
	var e *core.PersonalLabelError
	if errors.As(err, &e) {
		switch e.Code {
		case "authorization_required", "invalid_grant", "expired_token":
			return i.T(core.MsgLabelAuthNeeded)
		case "wrong_user":
			return i.T(core.MsgLabelWrongUser)
		case "busy":
			return i.T(core.MsgLabelBusy)
		}
		return i.Tf(core.MsgLabelFailed, e.Code)
	}
	return i.Tf(core.MsgLabelFailed, "storage_error")
}
func (s *teamProjectService) labels(ctx context.Context, p core.Platform, msg *core.Message, cfg *config.Config, team config.TeamConfig, args []string) (answer string, resultErr error) {
	i := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
	api, ok := p.(core.PersonalLabelManager)
	if !ok {
		return "", fmt.Errorf("%s", i.T(core.MsgLabelUnsupported))
	}
	// Personal preferences and OAuth links are only displayed in private chat.
	private, ok := p.(core.PrivateUserMessageDetector)
	if !ok || !private.IsPrivateUserMessage(msg) {
		return i.T(core.MsgLabelPrivate), nil
	}
	path, err := s.labelPath(cfg, team.Manager, msg.UserID)
	if err != nil {
		return labelErrorText(i, err), nil
	}
	action := ""
	if len(args) > 0 {
		action = args[0]
	}
	if action == "authorize" {
		err := api.AuthorizePersonalLabels(ctx, msg.UserID, func(link string) error { return p.Reply(ctx, msg.ReplyCtx, i.Tf(core.MsgLabelAuthorize, link)) })
		if err != nil {
			return labelErrorText(i, err), nil
		}
		return i.T(core.MsgLabelAuthorized), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lock := teamLabelLock(path)
	if !lock.TryLock() {
		return i.T(core.MsgLabelBusy), nil
	}
	defer lock.Unlock()
	release, err := core.LockPrivateFile(path)
	if err != nil {
		return labelErrorText(i, &core.PersonalLabelError{Code: "busy"}), nil
	}
	defer release()
	state, err := readTeamLabels(path)
	if err != nil {
		return labelErrorText(i, err), nil
	}
	if action == "retry" {
		// Stable order makes retries deterministic; completed assignments are skipped.
		keys := []string{}
		for id := range state.Jobs {
			keys = append(keys, id)
		}
		sort.Strings(keys)
		for _, id := range keys {
			job := state.Jobs[id]
			if job.Done {
				continue
			}
			err := s.applyLabelJob(ctx, p, msg.UserID, cfg, &state, job, path)
			if err != nil {
				job.Error = labelErrorText(i, err)
			} else {
				job.Error = ""
			}
			if e := saveTeamLabels(path, state); e != nil {
				return labelErrorText(i, e), nil
			}
		}
	} else if action != "" && action != "save" {
		return i.T(core.MsgLabelUsage), nil
	}
	// Only refresh on opening the page, so values submitted from an issued form
	// are validated against the catalog that actually generated that form.
	status := i.T(core.MsgLabelAuthNeeded)
	if api.PersonalLabelsAuthorized(msg.UserID) {
		status = i.T(core.MsgLabelAuthReady)
		if msg.FormValues == nil {
			catalog, e := api.ListPersonalLabels(ctx, msg.UserID)
			if e != nil {
				status = labelErrorText(i, e)
			} else {
				state.Catalog = catalog
				if e = saveTeamLabels(path, state); e != nil {
					return labelErrorText(i, e), nil
				}
			}
		}
	}
	card := core.NewCard().Title(i.T(core.MsgLabelTitle), "blue").Markdown(i.T(core.MsgLabelHint)).Markdown(status).
		Buttons(core.DefaultBtn(i.T(core.MsgLabelAuthButton), "cmd:/team labels authorize"), core.DefaultBtn(i.T(core.MsgLabelRetry), "cmd:/team labels retry")).Build()
	for _, job := range state.Jobs {
		if !job.Done {
			card.Elements = append(card.Elements, core.CardNote{Text: i.Tf(core.MsgLabelPending, labelJobName(job, i), job.Error)})
		}
	}
	card.Form = &core.CardForm{Command: "/team labels save", Language: i.CurrentLang(), SubmitText: i.T(core.MsgLabelSave), BackAction: "nav:/help team"}
	card.Form.Fields = append(labelFields(i, "bot_labels", core.MsgLabelBots, state.Bot, state.Catalog), labelFields(i, "project_labels", core.MsgLabelProjects, state.Project, state.Catalog)...)
	if msg.FormValues != nil && action == "save" {
		defer func() { retryTeamForm(ctx, p, msg, card, resultErr) }()
		values, e := validatedTeamFormValues(card, msg.FormValues)
		if e != nil {
			return "", e
		}
		state.Bot, e = chosenLabels(card, values, "bot_labels")
		if e != nil {
			return "", fmt.Errorf("%s", labelErrorText(i, e))
		}
		state.Project, e = chosenLabels(card, values, "project_labels")
		if e != nil {
			return "", fmt.Errorf("%s", labelErrorText(i, e))
		}
		if e = saveTeamLabels(path, state); e != nil {
			return labelErrorText(i, e), nil
		}
		return i.T(core.MsgLabelSaved), nil
	}
	return replyTeamForm(ctx, p, msg, card, core.MsgLabelUsage)
}

type labelSelectionKey struct{}

// A non-nil empty selection explicitly opts this creation out of auto-labeling.
func (s *teamProjectService) creationLabels(p core.Platform, msg *core.Message, cfg *config.Config, manager, kind string, card *core.Card) error {
	if _, ok := p.(core.PersonalLabelManager); !ok {
		return nil
	}
	path, err := s.labelPath(cfg, manager, msg.UserID)
	if err != nil {
		return err
	}
	lock := teamLabelLock(path)
	lock.Lock()
	defer lock.Unlock()
	release, err := core.LockPrivateFile(path)
	if err != nil {
		return err
	}
	defer release()
	state, err := readTeamLabels(path)
	if err != nil {
		return err
	}
	defaults := state.Bot
	if kind == "project" {
		defaults = state.Project
	}
	i := core.NewI18n(card.Form.Language)
	card.Form.Fields = append(card.Form.Fields, labelFields(i, "labels", core.MsgLabelSelection, defaults, state.Catalog)...)
	card.Elements = append(card.Elements, core.CardNote{Text: i.T(core.MsgLabelCreationHint)})
	return nil
}
func labelCreationContext(ctx context.Context, card *core.Card, raw map[string][]string) (context.Context, error) {
	exists := false
	for _, f := range card.Form.Fields {
		if f.Name == "new_labels" {
			exists = true
		}
	}
	if !exists {
		return ctx, nil
	}
	values, err := validatedTeamFormValues(card, raw)
	if err != nil {
		return ctx, err
	}
	selected, err := chosenLabels(card, values, "labels")
	if err != nil {
		return ctx, err
	}
	if selected == nil {
		selected = []core.PersonalLabel{}
	}
	return context.WithValue(ctx, labelSelectionKey{}, selected), nil
}
func (s *teamProjectService) prepareLabelJob(ctx context.Context, cfg *config.Config, manager, user, kind, resource string) error {
	path, err := s.labelPath(cfg, manager, user)
	if err != nil {
		return err
	}
	lock := teamLabelLock(path)
	lock.Lock()
	defer lock.Unlock()
	release, err := core.LockPrivateFile(path)
	if err != nil {
		return err
	}
	defer release()
	state, err := readTeamLabels(path)
	if err != nil {
		return err
	}
	key := kind + ":" + resource
	if state.Jobs[key] != nil {
		return nil
	} // first creation intent survives retries/default changes
	selection, ok := ctx.Value(labelSelectionKey{}).([]core.PersonalLabel)
	if !ok {
		selection = state.Bot
		if kind == "project" {
			selection = state.Project
		}
	}
	state.Jobs[key] = &teamLabelJob{Kind: kind, Resource: resource, Labels: append([]core.PersonalLabel(nil), selection...), UUID: core.GenerateToken(16), Done: len(selection) == 0}
	return saveTeamLabels(path, state)
}
func (s *teamProjectService) labelPlatform(cfg *config.Config, bot string) (core.Platform, error) {
	_, pc, err := teamBotPlatform(cfg, bot, "feishu")
	if err != nil {
		_, pc, err = teamBotPlatform(cfg, bot, "lark")
	}
	if err != nil {
		return nil, err
	}
	opts := map[string]any{}
	for k, v := range pc.Options {
		opts[k] = v
	}
	opts["cc_data_dir"] = s.dataDir
	if s.labelPlatformFactory != nil {
		return s.labelPlatformFactory(pc.Type, opts)
	}
	return core.CreatePlatform(pc.Type, opts)
}
func labelJobName(job *teamLabelJob, i *core.I18n) string {
	if job.Name != "" {
		return job.Name
	}
	if job.Kind == "project" {
		return i.T(core.MsgLabelProjects)
	}
	return i.T(core.MsgLabelBots)
}
func (s *teamProjectService) nameLabelJob(cfg *config.Config, job *teamLabelJob) {
	if job.Name != "" {
		return
	}
	if job.Kind == "project" {
		all, err := s.store.All()
		if err == nil {
			job.Name = all[job.Resource].Name
		}
		return
	}
	for _, team := range cfg.Teams {
		for _, member := range team.Members {
			if member.Bot == job.Resource {
				job.Name = memberDisplayName(member)
				return
			}
		}
	}
}
func (s *teamProjectService) applyLabelJob(ctx context.Context, p core.Platform, user string, cfg *config.Config, state *teamLabelState, job *teamLabelJob, path string) error {
	s.nameLabelJob(cfg, job)
	if job.Done {
		return nil
	}
	api, ok := p.(core.PersonalLabelManager)
	if !ok {
		return &core.PersonalLabelError{Code: "unavailable"}
	}
	if !api.PersonalLabelsAuthorized(user) {
		return &core.PersonalLabelError{Code: "authorization_required"}
	}
	if job.Chat == "" {
		if job.Kind == "project" {
			all, err := s.store.All()
			if err != nil {
				return err
			}
			v, ok := all[job.Resource]
			if !ok || v.State != "ready" || v.Creator != user {
				return &core.PersonalLabelError{Code: "creation_incomplete"}
			}
			job.Chat = v.ChatID
		} else {
			// Resolve the new bot through its own configured app-scoped owner identity.
			var bot config.ProjectConfig
			for _, b := range cfg.Projects {
				if b.Name == job.Resource {
					bot = b
				}
			}
			if bot.BotOwnerID == "" {
				return &core.PersonalLabelError{Code: "creation_incomplete"}
			}
			newPlatform, err := s.labelPlatform(cfg, job.Resource)
			if err != nil {
				return err
			}
			conversations, ok := newPlatform.(core.PersonalLabelConversation)
			if !ok {
				return &core.PersonalLabelError{Code: "unavailable"}
			}
			i := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
			job.Chat, err = conversations.OpenLabelConversation(ctx, bot.BotOwnerID, job.UUID, i.T(core.MsgLabelWelcome))
			if err != nil {
				return err
			}
		}
		if err := saveTeamLabels(path, *state); err != nil {
			return err
		}
	}
	var catalog []core.PersonalLabel
	for index, g := range job.Labels {
		if g.ID == "" {
			if catalog == nil {
				var err error
				catalog, err = api.ListPersonalLabels(ctx, user)
				if err != nil {
					return err
				}
			}
			matches := []core.PersonalLabel{}
			for _, candidate := range catalog {
				if candidate.Name == g.Name {
					matches = append(matches, candidate)
				}
			}
			if len(matches) > 1 {
				return &core.PersonalLabelError{Code: "ambiguous_name"}
			}
			if len(matches) == 1 {
				g = matches[0]
			} else {
				var err error
				g, err = api.CreatePersonalLabel(ctx, user, g.Name)
				if err != nil {
					return err
				}
				catalog = append(catalog, g)
			}
			job.Labels[index] = g
			// Promote a newly resolved name to its stable ID for future forms.
			for _, defaults := range [][]core.PersonalLabel{state.Bot, state.Project} {
				for j := range defaults {
					if defaults[j].ID == "" && defaults[j].Name == g.Name {
						defaults[j] = g
					}
				}
			}
			known := false
			for _, existing := range state.Catalog {
				if existing.ID == g.ID {
					known = true
				}
			}
			if !known {
				state.Catalog = append(state.Catalog, g)
			}
			if err := saveTeamLabels(path, *state); err != nil {
				return err
			}
		}
		if err := api.AddPersonalLabel(ctx, user, job.Chat, g.ID); err != nil {
			return err
		}
	}
	job.Done = true
	job.Error = ""
	return nil
}
func (s *teamProjectService) finishLabelJob(ctx context.Context, p core.Platform, cfg *config.Config, manager, user, kind, resource string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	i := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
	path, err := s.labelPath(cfg, manager, user)
	if err != nil {
		return labelErrorText(i, err)
	}
	lock := teamLabelLock(path)
	lock.Lock()
	defer lock.Unlock()
	release, err := core.LockPrivateFile(path)
	if err != nil {
		return labelErrorText(i, &core.PersonalLabelError{Code: "busy"})
	}
	defer release()
	state, err := readTeamLabels(path)
	if err != nil {
		return labelErrorText(i, err)
	}
	job := state.Jobs[kind+":"+resource]
	if job == nil || len(job.Labels) == 0 {
		return ""
	}
	if err := s.applyLabelJob(ctx, p, user, cfg, &state, job, path); err != nil {
		job.Error = labelErrorText(i, err)
	}
	if err = saveTeamLabels(path, state); err != nil {
		return labelErrorText(i, err)
	}
	if !job.Done {
		return i.Tf(core.MsgLabelPending, labelJobName(job, i), job.Error)
	}
	return i.T(core.MsgLabelApplied)
}
func (s *teamProjectService) prepareBotLabels(ctx context.Context, parent, user, name string) error {
	cfg, err := config.Load(s.path)
	if err != nil {
		return err
	}
	team, _ := cfg.TeamForBot(parent)
	if team == nil || team.Manager != parent {
		return nil
	}
	return s.prepareLabelJob(ctx, cfg, parent, user, "bot", name)
}
func (s *teamProjectService) labelCreatedBot(ctx context.Context, r *botCreationRecord, notify func(core.MsgKey, ...any)) {
	cfg, err := config.Load(s.path)
	if err != nil {
		notify(core.MsgLabelFailed, "storage_error")
		return
	}
	team, _ := cfg.TeamForBot(r.Parent)
	if team == nil || team.Manager != r.Parent {
		return
	}
	p, err := s.labelPlatform(cfg, r.Parent)
	if err != nil {
		notify(core.MsgLabelFailed, "platform_unavailable")
		return
	}
	text := s.finishLabelJob(ctx, p, cfg, r.Parent, r.Initiator, "bot", r.Name)
	if text != "" {
		notify(core.MsgLabelText, text)
	}
}
