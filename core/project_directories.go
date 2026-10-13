package core

import (
	"fmt"
	"strconv"
	"strings"
)

func (e *Engine) projectDirectoriesForm(p TeamProject) *Card {
	card := NewCard().Title(e.i18n.T(MsgProjectEditDirectory), "blue").Markdown(e.i18n.T(MsgProjectDirectoriesHelp)).Note(e.i18n.T(MsgProjectDirectoryScope)).Build()
	card.Reusable = true
	card.Form = &CardForm{
		Command: fmt.Sprintf("/project _directories %s %d", p.ID, p.ContextRevision), Language: e.i18n.CurrentLang(),
		SubmitText: e.i18n.T(MsgFormSave), BackAction: "cmd:/project settings",
		Fields: []CardFormField{
			{Name: "primary", Label: e.i18n.T(MsgProjectPrimaryDirectory), Kind: "text", Required: true, Defaults: []string{p.Directories[0]}},
			{Name: "additional", Label: e.i18n.T(MsgProjectAdditionalDirectories), Kind: "multiline", Defaults: []string{strings.Join(p.Directories[1:], "\n")}},
		},
	}
	return card
}

func (e *Engine) replyProjectDirectoriesForm(platform Platform, msg *Message, card *Card) {
	if sender, ok := platform.(FormCardSender); ok {
		if err := sender.ReplyForm(e.ctx, msg, card); err != nil {
			e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		}
		return
	}
	e.reply(platform, msg.ReplyCtx, card.RenderText()+"\n"+e.i18n.T(MsgProjectSettingsUsage))
}

func (e *Engine) handleProjectDirectories(platform Platform, msg *Message, p TeamProject, args []string) bool {
	card := e.projectDirectoriesForm(p)
	if args[0] != "_directories" && len(args) == 1 {
		e.replyProjectDirectoriesForm(platform, msg, card)
		return true
	}
	dirs := append([]string(nil), p.Directories...)
	revision := p.ContextRevision
	var err error
	if args[0] == "_directories" {
		var submitted uint64
		if len(args) == 3 {
			submitted, err = strconv.ParseUint(args[2], 10, 64)
		}
		if len(args) != 3 || args[1] != p.ID || err != nil || submitted != revision || msg.FormValues == nil {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgFormExpired))
			return true
		}
		raw := map[string]any{}
		for _, field := range card.Form.Fields {
			v := msg.FormValues[field.Name]
			switch len(v) {
			case 0:
				raw[field.Name] = ""
			case 1:
				raw[field.Name] = v[0]
			default:
				raw[field.Name] = v
			}
		}
		var values map[string][]string
		values, err = card.Form.ValidateValues(raw)
		if err == nil {
			dirs = []string{values["primary"][0]}
			if len(values["additional"]) > 0 {
				for _, line := range strings.Split(values["additional"][0], "\n") {
					if dir := strings.TrimSpace(line); dir != "" {
						dirs = append(dirs, dir)
					}
				}
			}
		}
	} else if len(args) >= 2 {
		path := strings.Join(args[2:], " ")
		switch args[1] {
		case "add":
			if path == "" {
				err = fmt.Errorf("%s", e.i18n.T(MsgProjectDirectoriesHelp))
			} else {
				dirs = append(dirs, path)
			}
		case "remove":
			index := -1
			for i, dir := range dirs {
				if normalizeWorkspacePath(path) == normalizeWorkspacePath(dir) {
					index = i
					break
				}
			}
			if index <= 0 {
				err = fmt.Errorf("%s", e.i18n.T(MsgProjectDirectoriesHelp))
			} else {
				dirs = append(dirs[:index], dirs[index+1:]...)
			}
		case "clear":
			if len(args) != 2 {
				err = fmt.Errorf("%s", e.i18n.T(MsgProjectDirectoriesHelp))
			} else {
				dirs = dirs[:1]
			}
		default:
			err = fmt.Errorf("%s", e.i18n.T(MsgProjectDirectoriesHelp))
		}
	}
	if err == nil {
		err = e.updateProjectSettingsWithDirectories(p.ID, "directories", "", dirs, &revision)
	}
	if err != nil {
		if msg.FormValues != nil {
			for i := range card.Form.Fields {
				if v, ok := msg.FormValues[card.Form.Fields[i].Name]; ok && len(v) <= 1 {
					card.Form.Fields[i].Defaults = v
				}
			}
			card.Elements = append(card.Elements, CardMarkdown{Content: e.i18n.Tf(MsgError, err)})
			e.replyProjectDirectoriesForm(platform, msg, card)
		} else {
			e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		}
		return true
	}
	updated, err := e.teamProject(msg.SessionKey)
	if err != nil || updated == nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectGroupRequired))
		return true
	}
	card = e.renderProjectSettings(*updated)
	card.Elements = append(card.Elements, CardNote{Text: e.i18n.T(MsgProjectSettingsSaved)})
	e.replyWithCard(platform, msg.ReplyCtx, card)
	return true
}
