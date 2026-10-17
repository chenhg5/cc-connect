package acp

import (
	"context"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// SetModel stores the model id applied to future sessions.
// The running session is left unchanged; ModelSwitcher is next-session.
func (a *Agent) SetModel(model string) {
	model = strings.TrimSpace(model)
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	slog.Info("acp: model changed for future sessions", "model", model)
}

// GetModel returns the configured model. When nothing is configured it
// returns the model the latest session reported, so /model can show the
// id the agent is actually using. The running session still reports its
// own value via (*acpSession).GetModel.
func (a *Agent) GetModel() string {
	a.mu.RLock()
	model := strings.TrimSpace(a.model)
	a.mu.RUnlock()
	if model != "" {
		return model
	}
	return a.LastReportedModel()
}

// AvailableModels prefers options advertised by the latest session,
// then the configured "models" option, then an empty list.
func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	a.modelsMu.RLock()
	cached := append([]core.ModelOption(nil), a.cachedModelOptions...)
	a.modelsMu.RUnlock()
	if len(cached) > 0 {
		return cached
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.configuredModels) == 0 {
		return nil
	}
	return append([]core.ModelOption(nil), a.configuredModels...)
}

// parseConfiguredModels accepts the same shapes cc-connect already uses
// for a configured model list: []ModelOption, []string, or []any of
// strings / maps. Map keys follow provider config ("model", "alias")
// and the ACP select option ("value", "name", "description").
func parseConfiguredModels(v any) []core.ModelOption {
	switch x := v.(type) {
	case nil:
		return nil
	case []core.ModelOption:
		return append([]core.ModelOption(nil), x...)
	case []string:
		return modelOptionsFromStrings(x)
	case []any:
		var out []core.ModelOption
		for _, item := range x {
			if opt, ok := modelOptionFromAny(item); ok {
				out = append(out, opt)
			}
		}
		return out
	default:
		return nil
	}
}

func modelOptionsFromStrings(in []string) []core.ModelOption {
	var out []core.ModelOption
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, core.ModelOption{Name: s})
	}
	return out
}

func modelOptionFromAny(v any) (core.ModelOption, bool) {
	switch t := v.(type) {
	case string:
		t = strings.TrimSpace(t)
		if t == "" {
			return core.ModelOption{}, false
		}
		return core.ModelOption{Name: t}, true
	case core.ModelOption:
		if strings.TrimSpace(t.Name) == "" {
			return core.ModelOption{}, false
		}
		return t, true
	case map[string]any:
		return modelOptionFromMap(t)
	case map[string]string:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = val
		}
		return modelOptionFromMap(m)
	default:
		return core.ModelOption{}, false
	}
}

func modelOptionFromMap(m map[string]any) (core.ModelOption, bool) {
	id := firstMapString(m, "value", "model", "modelId")
	nameWasID := false
	if id == "" {
		id = firstMapString(m, "name")
		nameWasID = id != ""
	}
	if id == "" {
		return core.ModelOption{}, false
	}
	desc := firstMapString(m, "desc", "description")
	if desc == "" && !nameWasID {
		desc = firstMapString(m, "name")
	}
	if desc == id {
		desc = ""
	}
	return core.ModelOption{
		Name:  id,
		Desc:  desc,
		Alias: firstMapString(m, "alias"),
	}, true
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		s, ok := m[k].(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}
