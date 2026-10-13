package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Existing CLI-backed agents keep their current command behavior. Only agents
// with session-advertised configuration opt into discovery and acknowledged RPCs.
func (e *Engine) runtimeConfigProblem(agent Agent, category string, sessionKeys ...string) string {
	configurable, ok := agent.(RuntimeConfigurable)
	if !ok {
		return ""
	}
	if len(sessionKeys) > 0 && sessionKeys[0] != "" {
		e.injectSessionEnv(agent, sessionKeys[0])
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	if err := configurable.PrepareRuntimeConfig(ctx); err != nil {
		return e.i18n.Tf(MsgAgentConfigFailed, err)
	}
	switch category {
	case "model":
		if switcher, ok := agent.(ModelSwitcher); !ok || len(switcher.AvailableModels(ctx)) == 0 {
			return e.i18n.T(MsgModelNotSupported)
		}
	case "thought_level":
		if switcher, ok := agent.(ReasoningEffortSwitcher); !ok || len(switcher.AvailableReasoningEfforts()) == 0 {
			return e.i18n.T(MsgReasoningNotSupported)
		}
	case "mode":
		if switcher, ok := agent.(ModeSwitcher); !ok || len(switcher.PermissionModes()) == 0 {
			return e.i18n.T(MsgModeNotSupported)
		}
	}
	return ""
}
func (e *Engine) setRuntimeConfig(agent Agent, category, value string) error {
	configurable, ok := agent.(RuntimeConfigurable)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	defer cancel()
	return configurable.SetRuntimeConfig(ctx, category, value)
}
func (e *Engine) switchRuntimeModel(agent Agent, target string, persist bool) (string, error) {
	switcher := agent.(ModelSwitcher)
	old := switcher.GetModel()
	effort := ""
	if r, ok := agent.(ReasoningEffortSwitcher); ok {
		effort = r.GetReasoningEffort()
	}
	if err := e.setRuntimeConfig(agent, "model", target); err != nil {
		return "", err
	}
	if persist && e.modelSaveFunc != nil {
		if err := e.modelSaveFunc(target); err != nil {
			rollbackErr := e.setRuntimeConfig(agent, "model", old)
			if rollbackErr == nil && effort != "" {
				rollbackErr = e.setRuntimeConfig(agent, "thought_level", effort)
			}
			if rollbackErr != nil {
				slog.Error("runtime model rollback failed", "error", rollbackErr)
				return "", fmt.Errorf("save model: %w (restore previous setting: %v)", err, rollbackErr)
			}
			return "", fmt.Errorf("save model: %w", err)
		}
	}
	return switcher.GetModel(), nil
}

// Return an error card for failed selections; do not silently redraw the old
// value or discard a conversation that has not actually changed configuration.
func (e *Engine) handleRuntimeConfigCard(agent Agent, cmd, args, sessionKey string) *Card {
	category, title := "thought_level", MsgCardTitleReasoning
	if cmd == "/mode" {
		category, title = "mode", MsgCardTitleMode
	}
	if problem := e.runtimeConfigProblem(agent, category, sessionKey); problem != "" {
		return e.simpleCard(e.i18n.T(title), "red", problem)
	}
	target := strings.TrimSpace(args)
	if category == "thought_level" {
		efforts := agent.(ReasoningEffortSwitcher).AvailableReasoningEfforts()
		if idx, err := strconv.Atoi(target); err == nil && idx >= 1 && idx <= len(efforts) {
			target = efforts[idx-1]
		}
	}
	if err := e.setRuntimeConfig(agent, category, target); err != nil {
		return e.simpleCard(e.i18n.T(title), "red", e.i18n.Tf(MsgAgentConfigFailed, err))
	}
	e.cleanupInteractiveState(e.interactiveKeyForSessionKey(sessionKey))
	if cmd == "/mode" {
		return e.renderModeCardForAgent(agent, sessionKey)
	}
	return e.renderReasoningCardForAgent(agent, sessionKey)
}

func (e *Engine) injectSessionEnv(agent Agent, ccKey string) {
	if inj, ok := agent.(SessionEnvInjector); ok {
		envVars := []string{
			"CC_PROJECT=" + e.name,
			"CC_SESSION_KEY=" + ccKey,
		}
		e.dataDirMu.RLock()
		dataDir := e.dataDir
		e.dataDirMu.RUnlock()
		if dataDir != "" {
			envVars = append(envVars, "CC_DATA_DIR="+dataDir)
		}
		if exePath, err := os.Executable(); err == nil {
			binDir := filepath.Dir(exePath)
			if curPath := os.Getenv("PATH"); curPath != "" {
				envVars = append(envVars, "PATH="+binDir+string(filepath.ListSeparator)+curPath)
			} else {
				envVars = append(envVars, "PATH="+binDir)
			}
		}
		inj.SetSessionEnv(envVars)
	}

}
