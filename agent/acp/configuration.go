package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.RuntimeConfigurable = (*Agent)(nil)
var _ core.ModelSwitcher = (*Agent)(nil)
var _ core.ReasoningEffortSwitcher = (*Agent)(nil)

// Configuration is advertised by session/new, not initialize. Keep one lazy,
// prompt-free connection per Agent and give it to the next fresh conversation.
// This avoids spawning another empty session for every menu or selector click.
// configSessionMu owns this connection; conversation connections are owned by
// the engine and are never modified while handling another chat's settings.
func (a *Agent) prepareConfigLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.configStopped {
		return fmt.Errorf("acp: agent is stopped")
	}
	if a.configSession != nil && !a.configSession.Alive() {
		_ = a.configSession.Close()
		a.configSession = nil
	}
	if a.configSession != nil {
		a.publishConfigLocked()
	}
	cfg := a.sessionConfig("")
	if a.configSession == nil {
		cfg.handshakeContext = ctx
		s, err := newACPSession(context.Background(), cfg)
		if err != nil {
			return err
		}
		a.configSession = s
	} else if err := a.configSession.applyPreferences(ctx, cfg.initialModel, cfg.initialReasoning); err != nil {
		return err
	}
	a.publishConfigLocked()
	return nil
}
func (a *Agent) PrepareRuntimeConfig(ctx context.Context) error {
	a.configSessionMu.Lock()
	defer a.configSessionMu.Unlock()
	return a.prepareConfigLocked(ctx)
}
func (a *Agent) SetRuntimeConfig(ctx context.Context, category, value string) error {
	a.configSessionMu.Lock()
	defer a.configSessionMu.Unlock()
	if err := a.prepareConfigLocked(ctx); err != nil {
		return err
	}
	if err := a.configSession.setConfig(ctx, category, value); err != nil {
		// A timed-out server may still have applied the change. Reconnect with the
		// last acknowledged preferences before accepting another operation.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = a.configSession.Close()
			a.configSession = nil
		}
		return err
	}
	a.mu.Lock()
	switch category {
	case "model":
		a.model = value
		option, _ := a.configSession.option("thought_level")
		a.reasoning = option.CurrentValue // model changes can remove/reset this option
	case "thought_level":
		a.reasoning = value
	case "mode":
		a.mode = value
	}
	a.mu.Unlock()
	a.publishConfigLocked()
	return nil
}
func (a *Agent) publishConfigLocked() {
	options := map[string]configOption{}
	for _, category := range []string{"model", "thought_level", "mode"} {
		if option, ok := a.configSession.option(category); ok {
			options[category] = option
		}
	}
	a.catalogMu.Lock()
	previous := a.catalog
	a.catalog = options
	a.catalogMu.Unlock()
	// Adopt changes announced by this reserved connection (including removal of
	// dependent controls), while preserving explicit setters until they are sent.
	if previous != nil {
		a.mu.Lock()
		if previous["model"].CurrentValue != options["model"].CurrentValue {
			a.model = options["model"].CurrentValue
		}
		if previous["thought_level"].CurrentValue != options["thought_level"].CurrentValue {
			a.reasoning = options["thought_level"].CurrentValue
		}
		if previous["mode"].CurrentValue != options["mode"].CurrentValue {
			a.mode = options["mode"].CurrentValue
		}
		a.mu.Unlock()
	}
	mode := options["mode"]
	block := acpModesBlock{CurrentModeID: mode.CurrentValue}
	for _, o := range mode.Options {
		block.AvailableModes = append(block.AvailableModes, acpModeInfo{ID: o.Value, Name: o.Name, Description: o.Description})
	}
	a.reportModes(block)
}
func (a *Agent) configOption(category string) configOption {
	// Pick up asynchronous config_option_update notifications too.
	a.configSessionMu.Lock()
	defer a.configSessionMu.Unlock()
	if a.configSession != nil && a.configSession.Alive() {
		a.publishConfigLocked()
	}
	a.catalogMu.RLock()
	defer a.catalogMu.RUnlock()
	return a.catalog[category]
}
func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.model != model {
		a.reasoning = ""
	}
	a.model = model
}
func (a *Agent) GetModel() string {
	observed := a.configOption("model").CurrentValue
	a.mu.RLock()
	value := a.model
	a.mu.RUnlock()
	if value != "" {
		return value
	}
	return observed
}
func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	if err := a.PrepareRuntimeConfig(ctx); err != nil {
		slog.Warn("acp: discover models", "error", err)
		return nil
	}
	var out []core.ModelOption
	for _, o := range a.configOption("model").Options {
		out = append(out, core.ModelOption{Name: o.Value, Desc: o.Name})
	}
	return out
}
func (a *Agent) SetReasoningEffort(value string) { a.mu.Lock(); a.reasoning = value; a.mu.Unlock() }
func (a *Agent) GetReasoningEffort() string {
	observed := a.configOption("thought_level").CurrentValue
	a.mu.RLock()
	value := a.reasoning
	a.mu.RUnlock()
	if value != "" {
		return value
	}
	return observed
}
func (a *Agent) AvailableReasoningEfforts() []string {
	var out []string
	for _, o := range a.configOption("thought_level").Options {
		out = append(out, o.Value)
	}
	return out
}
func (a *Agent) invalidateConfig() {
	a.configSessionMu.Lock()
	defer a.configSessionMu.Unlock()
	if a.configSession != nil {
		_ = a.configSession.Close()
		a.configSession = nil
	}
	a.catalogMu.Lock()
	a.catalog = nil
	a.catalogMu.Unlock()
}
