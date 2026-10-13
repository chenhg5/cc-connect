package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func teamRoleOptions(i18n *core.I18n) []core.CardSelectOption {
	var options []core.CardSelectOption
	for _, option := range []struct {
		id    string
		label core.MsgKey
	}{
		{"manager", core.MsgFormRoleManager}, {"planner", core.MsgFormRolePlanner},
		{"developer", core.MsgFormRoleDeveloper}, {"tester", core.MsgFormRoleTester}, {"reviewer", core.MsgFormRoleReviewer},
	} {
		options = append(options, core.CardSelectOption{Text: i18n.T(option.label), Value: option.id})
	}
	return options
}

func teamMemberForm(cfg *config.Config, team config.TeamConfig, member config.TeamMember) *core.Card {
	return botProfileForm(cfg, team, member, member.Bot, member.Bot, true)
}

// Both entry points use the same fields, order, backend options and validation.
func botProfileForm(cfg *config.Config, team config.TeamConfig, member config.TeamMember, runtime, id string, editing bool) *core.Card {
	lang := core.NormalizeLanguageString(cfg.Language)
	i18n := core.NewI18n(lang)
	role := core.CardFormField{Name: "role", Label: i18n.T(core.MsgFormRole), Kind: "select", Required: true, Defaults: []string{member.Role}, Options: teamRoleOptions(i18n)}
	if editing && member.Bot == team.Manager {
		role.Options = []core.CardSelectOption{{Text: i18n.T(core.MsgFormRoleManager), Value: "manager"}}
	}
	backend := core.CardFormField{Name: "backend", Label: i18n.T(core.MsgFormBackend), Kind: "select", Required: true, Defaults: []string{teamBotBackend(cfg, runtime)}, Options: []core.CardSelectOption{{Text: "Codex", Value: "codex"}, {Text: "Claude Code", Value: "claudecode"}, {Text: "Trae", Value: "trae"}}}
	// Keep an existing native backend selectable without replacing it on save.
	if backend.Defaults[0] != "codex" && backend.Defaults[0] != "claudecode" && backend.Defaults[0] != "trae" {
		if editing {
			backend.Options = append(backend.Options, core.CardSelectOption{Text: backend.Defaults[0], Value: backend.Defaults[0]})
		} else {
			backend.Defaults = []string{"codex"}
		}
	}
	form := &core.CardForm{Language: lang, Command: "/team bot create " + id, SubmitText: i18n.T(core.MsgFormCreate)}
	title, hint := i18n.T(core.MsgFormBotTitle), i18n.T(core.MsgFormBotHint)
	if editing {
		form.Command = "/team members " + id
		form.SubmitText = i18n.T(core.MsgFormModify)
		title = i18n.T(core.MsgBotProfileEditTitle)
		hint = i18n.T(core.MsgBotProfileEditHint)
	}
	form.Fields = []core.CardFormField{role, backend, {Name: "prompt", Label: i18n.T(core.MsgFormPrompt), Kind: "multiline", Defaults: []string{member.Prompt}}}
	form.BackAction = core.CardReturnAction(form.Command)
	card := core.NewCard().Title(title, "blue").Build()
	if editing {
		// The platform name is display-only, outside the submitted form fields.
		name := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]").Replace(memberDisplayName(member))
		card.Elements = append(card.Elements, core.CardMarkdown{Content: i18n.T(core.MsgFormBotName) + ": " + name})
	}
	card.Elements = append(card.Elements, core.CardMarkdown{Content: hint})
	if editing {
		card.Elements = append(card.Elements, core.CardActions{Buttons: []core.CardButton{core.DefaultBtn(i18n.T("team permissions"), "cmd:/team permissions "+id)}})
	}
	if !editing {
		card.Elements = append(card.Elements, core.CardNote{Text: i18n.T(core.MsgRolePermissionsCreationHint)})
	}
	card.Form = form
	return card
}

func teamBotBackend(cfg *config.Config, name string) string {
	for _, p := range cfg.Projects {
		if p.Name != name {
			continue
		}
		if p.Agent.Type == "acp" && p.Agent.Options["cmd"] == "traecli" {
			return "trae"
		}
		if p.Agent.Type != "" {
			return p.Agent.Type
		}
	}
	return "codex"
}

func memberDisplayName(member config.TeamMember) string {
	if member.DisplayName != "" {
		return member.DisplayName
	}
	if strings.HasPrefix(member.Bot, "bot-") {
		return "Bot"
	}
	return member.Bot // legacy configurations keep their previous label until refreshed
}

func (s *teamProjectService) members(ctx context.Context, p core.Platform, msg *core.Message, cfg *config.Config, team config.TeamConfig, bot string, args []string) (answer string, resultErr error) {
	i18n := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
	if len(args) == 0 {
		namesFresh := s.refreshTeamNames(ctx, cfg, &team, "")
		card := core.NewCard().Title(i18n.T(core.MsgTeamMembersTitle), "blue")
		_, canEdit := p.(core.FormCardSender)
		for _, member := range team.Members {
			line := memberDisplayName(member) + " · " + member.Role + " · " + teamBotBackend(cfg, member.Bot)
			if canEdit && bot == team.Manager {
				card.ListItem(line, i18n.T(core.MsgTeamMemberEdit), "cmd:/team members "+member.Bot)
			} else {
				card.Markdown(line)
			}
		}
		if !namesFresh {
			card.Note(i18n.T(core.MsgBotNameReadFailed))
		}
		card.Divider().Buttons(core.DefaultBtn(i18n.T(core.MsgCardBack), "nav:/help team"))
		panel := card.Build()
		panel.Reusable = true
		if sender, ok := p.(core.CardSender); ok {
			return "", sender.ReplyCard(ctx, msg.ReplyCtx, panel)
		}
		return panel.RenderText(), nil
	}
	if bot != team.Manager {
		return "", fmt.Errorf("%s", i18n.T(core.MsgTeamManagerOnly))
	}
	if len(args) != 1 {
		return i18n.T(core.MsgTeamChatUsage), nil
	}
	for _, member := range team.Members {
		if member.Bot != args[0] {
			continue
		}
		namesFresh := s.refreshTeamNames(ctx, cfg, &team, member.Bot)
		for _, current := range team.Members {
			if current.Bot == member.Bot {
				member = current
			}
		}
		card := teamMemberForm(cfg, team, member)
		if !namesFresh {
			card.Elements = append(card.Elements, core.CardNote{Text: i18n.T(core.MsgBotNameReadFailed)})
		}
		if msg.FormValues == nil {
			return replyTeamForm(ctx, p, msg, card, core.MsgTeamMemberFormUnsupported)
		}
		defer func() { retryTeamForm(ctx, p, msg, card, resultErr) }()
		values, err := validatedTeamFormValues(card, msg.FormValues)
		if err != nil {
			return "", err
		}
		if err := config.ConfigureTeamBotProfile(bot, member.Bot, values["role"][0], values["backend"][0], strings.Join(values["prompt"], ""), member.DisplayName); err != nil {
			return "", err
		}
		return i18n.T(core.MsgTeamMemberSaved), nil
	}
	return "", fmt.Errorf("%s", i18n.T(core.MsgTeamMemberNotFound))
}
