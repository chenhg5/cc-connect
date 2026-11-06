package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (e *Engine) cmdCurrentPermissionMode(p Platform, msg *Message, key string, args []string) {
	e.replySettingsCard(p, msg, e.currentPermissionModeCard(key, args))
}

func (e *Engine) currentPermissionModeCard(key string, args []string) *Card {
	failure := func(text string) *Card {
		return NewCard().Title(e.i18n.T(MsgCurrentModeTitle), "indigo").Markdown(text).Buttons(DefaultBtn(e.i18n.T(MsgModeDefaultsButton), "nav:/mode")).Build()
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	var session AgentSession
	if state != nil {
		state.mu.Lock()
		session = state.agentSession
		state.mu.Unlock()
	}
	if session == nil || !session.Alive() {
		return failure(e.i18n.T(MsgSharedNotAttached))
	}
	switcher, ok := session.(SessionPermissionSwitcher)
	reader, readable := session.(AgentRuntimeSettingsReader)
	if !ok || !readable {
		return failure(e.i18n.T(MsgModeNotSupported))
	}
	modes := switcher.CurrentPermissionModes()
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	changed := len(args) > 0
	if changed {
		if err := e.applyCurrentPermissionChange(ctx, session, switcher, modes, args); err != nil {
			if errors.Is(err, ErrAgentTurnBusy) {
				return failure(e.i18n.T(MsgCurrentModeBusy))
			}
			return failure(e.i18n.Tf(MsgError, err))
		}
	}
	settings, err := reader.ReadRuntimeSettings(ctx)
	if err != nil {
		key := MsgDefaultSettingsReadFailed
		if changed {
			key = MsgCurrentModeReadFailed
		}
		return failure(e.i18n.Tf(key, err))
	}
	var approvals []PermissionModeInfo
	if sw, ok := session.(SessionApprovalSwitcher); ok {
		approvals = sw.CurrentApprovalModes()
	}
	return e.renderCurrentPermissionModeCard(modes, approvals, settings, changed)
}

func (e *Engine) renderCurrentPermissionModeCard(modes, approvals []PermissionModeInfo, settings AgentRuntimeSettings, changed bool) *Card {
	var sb strings.Builder
	if changed {
		sb.WriteString(e.i18n.T(MsgCurrentModeSaved) + "\n")
	}
	value := func(s string) string {
		if s == "" {
			return e.i18n.T(MsgDefaultSettingsUnknown)
		}
		return s
	}
	sb.WriteString(e.i18n.Tf(MsgCurrentModeState, value(settings.PermissionProfile), value(settings.ApprovalPolicy), value(settings.ApprovalsReviewer), value(settings.Sandbox)))
	var options []CardSelectOption
	for _, mode := range modes {
		label := mode.Name
		if e.i18n.IsZhLike() && mode.NameZh != "" {
			label = mode.NameZh
		}
		if mode.NameKey != "" {
			label = e.i18n.T(mode.NameKey)
		}
		fmt.Fprintf(&sb, "\n%s: %s", mode.Key, label)
		options = append(options, CardSelectOption{Text: label, Value: "act:/mode current " + mode.Key})
	}
	sb.WriteString("\n" + e.i18n.T(MsgCurrentModeUsage))
	card := NewCard().Title(e.i18n.T(MsgCurrentModeTitle), "indigo").Markdown(sb.String()).
		Select(e.i18n.T(MsgPermissionPresets), options, "")
	if len(approvals) > 0 {
		var approvalOptions []CardSelectOption
		selected := ""
		for _, mode := range approvals {
			action := "act:/mode current approval " + mode.Key
			approvalOptions = append(approvalOptions, CardSelectOption{Text: e.i18n.T(mode.NameKey), Value: action})
			if settings.ApprovalPolicy == "on-request" && ((mode.Key == "request" && settings.ApprovalsReviewer == "user") || (mode.Key == "auto-review" && settings.ApprovalsReviewer == "auto_review")) {
				selected = action
			}
		}
		card.Markdown(e.i18n.T(MsgApprovalOnlyHint)).Select(e.i18n.T(MsgApprovalOnlyTitle), approvalOptions, selected).Note(e.i18n.T(MsgApprovalOnlyUsage))
	}
	return card.Buttons(DefaultBtn(e.i18n.T(MsgModeDefaultsButton), "nav:/mode")).Build()
}

func (e *Engine) applyCurrentPermissionChange(ctx context.Context, session AgentSession, switcher SessionPermissionSwitcher, modes []PermissionModeInfo, args []string) error {
	if strings.EqualFold(args[0], "approval") {
		sw, ok := session.(SessionApprovalSwitcher)
		if !ok {
			return fmt.Errorf("%s", e.i18n.T(MsgModeNotSupported))
		}
		if len(args) == 2 {
			target := strings.ToLower(args[1])
			for _, mode := range sw.CurrentApprovalModes() {
				if mode.Key == target {
					return sw.SetCurrentApprovalMode(ctx, target)
				}
			}
		}
		return fmt.Errorf("%s", e.i18n.T(MsgApprovalOnlyUsage))
	}
	if len(args) == 1 {
		target := strings.ToLower(args[0])
		for _, mode := range modes {
			if mode.Key == target {
				return switcher.SetCurrentPermissionMode(ctx, target)
			}
		}
	}
	return fmt.Errorf("%s\n%s", e.i18n.T(MsgCurrentModeUsage), e.modeUsageText(modes))
}
