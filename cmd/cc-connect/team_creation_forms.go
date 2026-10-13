package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func teamCreationForm(cfg *config.Config, team config.TeamConfig, bot, kind string, directories ...string) *core.Card {
	lang := core.NormalizeLanguageString(cfg.Language)
	i18n := core.NewI18n(lang)
	form := &core.CardForm{Language: lang, SubmitText: i18n.T(core.MsgFormCreate)}
	text := func(name string, label core.MsgKey, required bool) core.CardFormField {
		return core.CardFormField{Name: name, Label: i18n.T(label), Kind: "text", Required: required}
	}
	var card *core.Card
	if kind == "bot" {
		return botProfileForm(cfg, team, config.TeamMember{Role: "developer"}, bot, "bot-"+strings.ToLower(rand.Text()), false)

	} else {
		directory := ""
		if len(directories) > 0 {
			directory = directories[0]
		}
		form.Command = "/project _create project-" + strings.ToLower(rand.Text())
		form.BackAction = "nav:/help project"
		name := text("name", core.MsgFormProjectName, true)
		members := core.CardFormField{Name: "members", Label: i18n.T(core.MsgFormMembers), Kind: "multiselect"}
		for _, member := range team.Members {
			if member.Bot == team.Manager {
				continue
			}
			members.Options = append(members.Options, core.CardSelectOption{Text: memberDisplayName(member) + " · " + member.Role, Value: member.Bot})
			members.Defaults = append(members.Defaults, member.Bot)
		}
		dir := text("directories", core.MsgFormDirectory, true)
		dir.Defaults = []string{directory}
		form.Fields = []core.CardFormField{name, dir}
		if len(members.Options) > 0 {
			form.Fields = append(form.Fields, members)
		}
		goal := text("goal", core.MsgFormGoal, false)
		goal.Kind = "multiline"
		form.Fields = append(form.Fields, goal)
		managerName := team.Manager
		for _, member := range team.Members {
			if member.Bot == team.Manager {
				managerName = memberDisplayName(member)
			}
		}
		card = core.NewCard().Title(i18n.T(core.MsgFormProjectTitle), "blue").Markdown(i18n.Tf(core.MsgFormProjectHint, managerName)).Build()
	}
	card.Form = form
	return card
}

// The same handlers process both textual commands and form submissions. No
// command string is assembled from user input, so spaces/newlines remain data.
func validatedTeamFormValues(card *core.Card, submitted map[string][]string) (map[string][]string, error) {
	raw := map[string]any{}
	for _, field := range card.Form.Fields {
		if v, ok := submitted[field.Name]; ok {
			if field.Kind == "multiselect" {
				raw[field.Name] = v
			} else if len(v) == 1 {
				raw[field.Name] = v[0]
			} else if len(v) == 0 {
				raw[field.Name] = ""
			} else {
				raw[field.Name] = v
			}
		}
	}
	return card.Form.ValidateValues(raw)
}

func creationFormArgs(card *core.Card, submitted map[string][]string, manager string) ([]string, error) {
	values, err := validatedTeamFormValues(card, submitted)
	if err != nil {
		return nil, err
	}
	one := func(name string) string {
		if len(values[name]) > 0 {
			return values[name][0]
		}
		return ""
	}
	if strings.HasPrefix(card.Form.Command, "/team bot create ") {
		return []string{"bot", "create", strings.TrimPrefix(card.Form.Command, "/team bot create "), one("role"), one("backend"), one("prompt")}, nil
	}
	id, ok := strings.CutPrefix(card.Form.Command, "/project _create ")
	if !ok || !core.ValidBotName(id) {
		return nil, fmt.Errorf("%s", core.NewI18n(card.Form.Language).T(core.MsgFormExpired))
	}
	if !filepath.IsAbs(one("directories")) {
		i18n := core.NewI18n(card.Form.Language)
		return nil, fmt.Errorf("%s", i18n.Tf(core.MsgFormInvalidField, i18n.T(core.MsgFormDirectory)))
	}
	bots := append([]string{manager}, values["members"]...)
	// The display name is separate from the server-owned ID and command args.
	return []string{"create", id, one("directories"), strings.Join(bots, ","), one("goal"), one("name")}, nil
}

func projectCreationWorkDir(ctx context.Context, cfg *config.Config, bot string) (string, error) {
	if dir := core.ProjectCommandWorkDir(ctx); dir != "" {
		return dir, nil
	}
	// Direct invocations outside Engine use the configured directory.
	for _, project := range cfg.RuntimeProjects() {
		if project.Name != bot {
			continue
		}
		effective, err := cfg.EffectiveProject(project)
		if err != nil {
			return "", err
		}
		if dir, ok := effective.Agent.Options["work_dir"].(string); ok && dir != "" {
			return filepath.Abs(dir)
		}
		if effective.BaseDir != "" {
			return filepath.Abs(effective.BaseDir)
		}
	}
	return os.Getwd()
}

func replyTeamForm(ctx context.Context, p core.Platform, msg *core.Message, card *core.Card, fallback core.MsgKey) (string, error) {
	if sender, ok := p.(core.FormCardSender); ok {
		return "", sender.ReplyForm(ctx, msg, card)
	}
	return core.NewI18n(card.Form.Language).T(fallback), nil
}

// A failed operation gets a fresh form reference with the original values.
// Successful submissions stay consumed, and retries still use existing IDs.
func retryTeamForm(ctx context.Context, p core.Platform, msg *core.Message, card *core.Card, operationErr error) {
	if operationErr == nil || msg.FormValues == nil {
		return
	}
	sender, ok := p.(core.FormCardSender)
	if !ok {
		return
	}
	applyTeamFormDefaults(card, msg.FormValues)
	if err := sender.ReplyForm(ctx, msg, card); err != nil {
		slog.Warn("team form retry could not be sent", "error", err)
	}
}

func applyTeamFormDefaults(card *core.Card, values map[string][]string) {
	for i := range card.Form.Fields {
		field := &card.Form.Fields[i]
		if values, ok := values[field.Name]; ok {
			field.Defaults = append([]string(nil), values...)
			if field.Kind == "select" || field.Kind == "multiselect" {
				field.Defaults = nil
				for _, value := range values {
					for _, option := range field.Options {
						if value == option.Value {
							field.Defaults = append(field.Defaults, value)
						}
					}
				}
			}
		}
	}
}
