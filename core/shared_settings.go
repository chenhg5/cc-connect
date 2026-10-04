package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func defaultSettingsOnly(agent Agent) bool {
	capability, ok := agent.(DefaultSettingsAgent)
	return ok && capability.DefaultSettingsOnly()
}

func isSettingsCommand(command string) bool {
	switch strings.TrimPrefix(command, "/") {
	case "model", "reasoning", "mode", "provider":
		return true
	}
	return false
}

func (e *Engine) handleDefaultSettingsCommand(p Platform, msg *Message, command string, args []string) bool {
	agent, _, key, err := e.commandContext(p, msg)
	if err != nil || !defaultSettingsOnly(agent) {
		return false
	}
	// Provider catalog management still uses the existing config-only flow.
	if command == "provider" && len(args) > 0 {
		sub := matchSubCommand(strings.ToLower(args[0]), []string{"list", "add", "remove", "switch", "current", "clear", "reset", "none"})
		if sub == "add" || sub == "remove" || args[0] == "rm" || args[0] == "delete" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDefaultSettingsScope)+"\n"+e.i18n.T(MsgDefaultProviderHint))
			return false
		}
	}
	changed, err := e.applyDefaultSetting(agent, msg.SessionKey, key, command, args)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	card := e.renderDefaultSettingsCard(agent, msg.SessionKey, command)
	if changed {
		card.Elements = append([]CardElement{CardMarkdown{Content: e.i18n.T(MsgDefaultSettingsSaved)}}, card.Elements...)
	}
	if supportsCards(p) {
		e.replyWithCard(p, msg.ReplyCtx, card)
	} else {
		var buttons [][]ButtonOption
		for _, element := range card.Elements {
			if selectRow, ok := element.(CardSelect); ok {
				for _, option := range selectRow.Options {
					buttons = append(buttons, []ButtonOption{{Text: option.Text, Data: strings.Replace(option.Value, "act:", "cmd:", 1)}})
				}
			}
		}
		e.replyWithButtons(p, msg.ReplyCtx, card.RenderText(), buttons)
	}
	return true
}

// Text commands and in-place card actions share validation and persistence.
// This path never closes the observer or resets session IDs/history.
func (e *Engine) applyDefaultSetting(agent Agent, sessionKey, key, command string, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch command {
	case "model":
		return e.applyDefaultModel(agent, sessionKey, key, args)
	case "reasoning":
		return e.applyDefaultReasoning(agent, args)
	case "mode":
		return e.applyDefaultMode(agent, args)
	case "provider":
		return e.applyDefaultProvider(agent, args)
	}
	return false, nil
}

func (e *Engine) applyDefaultModel(agent Agent, sessionKey, key string, args []string) (bool, error) {
	switcher, ok := agent.(ModelSwitcher)
	if !ok {
		return false, fmt.Errorf("%s", e.i18n.T(MsgModelNotSupported))
	}
	target, valid := parseModelSwitchArgs(args)
	if !valid {
		return false, fmt.Errorf("%s", e.i18n.T(MsgModelUsage))
	}
	target = strings.TrimSpace(target)
	if modelSwitchNeedsLookup(target) {
		ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
		target = resolveModelSwitchTarget(target, switcher.AvailableModels(ctx))
		cancel()
	}
	resolved, err := e.switchModelOnAgent(agent, target, agent == e.agent)
	if err != nil {
		return false, err
	}
	e.persistWorkspaceModelOverride(key, sessionKey, agent, resolved)
	return true, nil
}

func (e *Engine) applyDefaultReasoning(agent Agent, args []string) (bool, error) {
	switcher, ok := agent.(ReasoningEffortSwitcher)
	if !ok {
		return false, fmt.Errorf("%s", e.i18n.T(MsgReasoningNotSupported))
	}
	efforts := switcher.AvailableReasoningEfforts()
	target := strings.ToLower(strings.TrimSpace(args[0]))
	if n, err := strconv.Atoi(target); err == nil && n >= 1 && n <= len(efforts) {
		target = efforts[n-1]
	}
	valid := false
	for _, effort := range efforts {
		if target == effort {
			valid = true
		}
	}
	if len(args) != 1 || !valid {
		return false, fmt.Errorf("%s", e.reasoningUsage(efforts))
	}
	switcher.SetReasoningEffort(target)
	return true, nil
}

func (e *Engine) applyDefaultMode(agent Agent, args []string) (bool, error) {
	switcher, ok := agent.(ModeSwitcher)
	if !ok {
		return false, fmt.Errorf("%s", e.i18n.T(MsgModeNotSupported))
	}
	target := strings.ToLower(args[0])
	valid := false
	for _, mode := range switcher.PermissionModes() {
		if mode.Key == target {
			valid = true
		}
	}
	if len(args) != 1 || !valid {
		return false, fmt.Errorf("%s", e.modeUsageText(switcher.PermissionModes()))
	}
	switcher.SetMode(target)
	return true, nil
}

func (e *Engine) applyDefaultProvider(agent Agent, args []string) (bool, error) {
	switcher, ok := agent.(ProviderSwitcher)
	if !ok {
		return false, fmt.Errorf("%s", e.i18n.T(MsgProviderNotSupported))
	}
	sub := matchSubCommand(strings.ToLower(args[0]), []string{"list", "switch", "current", "clear", "reset", "none"})
	if sub == "list" || sub == "current" {
		return false, nil
	}
	name := args[0]
	if sub == "switch" {
		if len(args) != 2 {
			return false, fmt.Errorf("%s", e.i18n.T(MsgProviderSwitchHint))
		}
		name = args[1]
	} else if len(args) != 1 {
		return false, fmt.Errorf("%s", e.i18n.T(MsgProviderSwitchHint))
	}
	if sub == "clear" || sub == "reset" || sub == "none" {
		name = ""
	}
	// Persist only the new-thread default, never bind it to the existing thread.
	found := name == ""
	for _, provider := range switcher.ListProviders() {
		if provider.Name == name {
			found = true
		}
	}
	if !found {
		return false, fmt.Errorf("%s", e.i18n.Tf(MsgProviderNotFound, name))
	}
	if agent == e.agent && e.providerSaveFunc != nil {
		if err := e.providerSaveFunc(name); err != nil {
			return false, fmt.Errorf("save provider: %w", err)
		}
	}
	if !switcher.SetActiveProvider(name) {
		return false, fmt.Errorf("%s", e.i18n.Tf(MsgProviderNotFound, name))
	}
	return true, nil
}

func (e *Engine) defaultSettingsSummary(agent Agent, sessionKey string) string {
	unknown := e.i18n.T(MsgDefaultSettingsUnknown)
	value := func(text string) string {
		if text == "" {
			return unknown
		}
		return text
	}
	var runtime AgentRuntimeSettings
	key := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	var reader AgentRuntimeSettingsReader
	if state != nil {
		state.mu.Lock()
		reader, _ = state.agentSession.(AgentRuntimeSettingsReader)
		state.mu.Unlock()
	}
	text := e.i18n.T(MsgDefaultSettingsNotAttached)
	if reader != nil {
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		settings, err := reader.ReadRuntimeSettings(ctx)
		cancel()
		if err != nil {
			text = e.i18n.Tf(MsgDefaultSettingsReadFailed, err)
		} else {
			runtime = settings
			text = e.i18n.Tf(MsgDefaultSettingsRuntime, value(runtime.Model), value(runtime.ReasoningEffort), value(runtime.Provider), value(runtime.ApprovalPolicy), value(runtime.Sandbox))
		}
	}
	model, effort, mode, provider := "", "", "", ""
	if sw, ok := agent.(ModelSwitcher); ok {
		model = sw.GetModel()
	}
	if sw, ok := agent.(ReasoningEffortSwitcher); ok {
		effort = sw.GetReasoningEffort()
	}
	if sw, ok := agent.(ModeSwitcher); ok {
		mode = sw.GetMode()
	}
	if sw, ok := agent.(ProviderSwitcher); ok {
		if p := sw.GetActiveProvider(); p != nil {
			provider = p.Name
			if p.Model != "" {
				model = p.Model
			}
		}
	}
	text += "\n\n" + e.i18n.Tf(MsgDefaultSettingsConfigured, value(model), value(effort), value(mode), value(provider)) + "\n\n" + e.i18n.T(MsgDefaultSettingsScope)
	return text
}

func (e *Engine) renderDefaultSettingsCard(agent Agent, sessionKey, command string) *Card {
	title := map[string]MsgKey{"model": MsgCardTitleModel, "reasoning": MsgCardTitleReasoning, "mode": MsgCardTitleMode, "provider": MsgCardTitleProvider}[command]
	card := NewCard().Title(e.i18n.T(title), "indigo").Markdown(e.defaultSettingsSummary(agent, sessionKey))
	opts := []CardSelectOption{}
	selected := ""
	usage := ""
	switch command {
	case "model":
		if sw, ok := agent.(ModelSwitcher); ok {
			ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
			models := sw.AvailableModels(ctx)
			cancel()
			current := sw.GetModel()
			if ps, ok := agent.(ProviderSwitcher); ok {
				if p := ps.GetActiveProvider(); p != nil && p.Model != "" {
					current = p.Model
				}
			}
			for i, m := range models {
				action := fmt.Sprintf("act:/model switch %d", i+1)
				opts = append(opts, CardSelectOption{Text: fmt.Sprintf("%d. %s", i+1, m.Name), Value: action})
				if m.Name == current {
					selected = action
				}
			}
			usage = e.i18n.T(MsgModelUsage)
		}
	case "reasoning":
		if sw, ok := agent.(ReasoningEffortSwitcher); ok {
			for i, effort := range sw.AvailableReasoningEfforts() {
				action := fmt.Sprintf("act:/reasoning %d", i+1)
				opts = append(opts, CardSelectOption{Text: fmt.Sprintf("%d. %s", i+1, effort), Value: action})
				if effort == sw.GetReasoningEffort() {
					selected = action
				}
			}
			usage = e.reasoningUsage(sw.AvailableReasoningEfforts())
		}
	case "mode":
		if sw, ok := agent.(ModeSwitcher); ok {
			for _, mode := range sw.PermissionModes() {
				label := mode.Name
				if e.i18n.IsZhLike() {
					label = mode.NameZh
				}
				action := "act:/mode " + mode.Key
				opts = append(opts, CardSelectOption{Text: label, Value: action})
				if mode.Key == sw.GetMode() {
					selected = action
				}
			}
			usage = e.modeUsageText(sw.PermissionModes())
		}
	case "provider":
		if sw, ok := agent.(ProviderSwitcher); ok {
			current := sw.GetActiveProvider()
			for _, p := range sw.ListProviders() {
				action := "act:/provider " + p.Name
				opts = append(opts, CardSelectOption{Text: p.Name, Value: action})
				if current != nil && current.Name == p.Name {
					selected = action
				}
			}
			opts = append(opts, CardSelectOption{Text: e.i18n.T(MsgProviderClearOption), Value: "act:/provider clear"})
		}
		card.Markdown(e.i18n.T(MsgDefaultProviderHint))
		usage = e.i18n.T(MsgProviderSwitchHint)
		card.Buttons(PrimaryBtn(e.i18n.T(MsgCardTitleProviderAdd), "nav:/provider/add"))
	}
	if len(opts) > 0 {
		card.Select(e.i18n.T(MsgDefaultSettingsSelect), opts, selected)
	}
	card.Buttons(e.cardBackButton()).Note(usage)
	return card.Build()
}

func (e *Engine) defaultSettingsCardAction(command, args, sessionKey string) *Card {
	agent, _ := e.sessionContextForKey(sessionKey)
	if !defaultSettingsOnly(agent) || !isSettingsCommand(command) {
		return nil
	}
	command = strings.TrimPrefix(command, "/")
	changed, err := e.applyDefaultSetting(agent, sessionKey, e.interactiveKeyForSessionKey(sessionKey), command, strings.Fields(args))
	card := e.renderDefaultSettingsCard(agent, sessionKey, command)
	if err != nil {
		card.Elements = append([]CardElement{CardMarkdown{Content: e.i18n.Tf(MsgError, err)}}, card.Elements...)
	} else if changed {
		card.Elements = append([]CardElement{CardMarkdown{Content: e.i18n.T(MsgDefaultSettingsSaved)}}, card.Elements...)
	}
	return card
}
