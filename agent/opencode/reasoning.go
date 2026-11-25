package opencode

import (
	"encoding/json"
	"strings"
)

// opencodeReasoningEfforts is the ordered set of reasoning levels a /reasoning
// switch can pick. OpenCode maps each name to a model "variant"; a variant is a
// partial model config merged over the base model, so {"reasoningEffort": X}
// turns into the openai-compatible `reasoning_effort` request field.
var opencodeReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

// normalizeOpencodeReasoningEffort maps user/command input onto one of
// opencodeReasoningEfforts, returning "" for anything that is not a valid level
// (the caller keeps the previous value then).
func normalizeOpencodeReasoningEffort(raw string) string {
	effort := strings.ToLower(strings.TrimSpace(raw))
	switch effort {
	case "off", "disabled", "disable":
		effort = "none"
	case "min":
		effort = "minimal"
	case "med":
		effort = "medium"
	case "x-high", "extra-high", "extra_high":
		effort = "xhigh"
	}
	for _, valid := range opencodeReasoningEfforts {
		if effort == valid {
			return effort
		}
	}
	return ""
}

// hasConfiguredModelLocked reports whether the agent already names a model
// (either the project option or the active provider). When it does not,
// OpenCode picks its own default model and cc-connect cannot attach a variant
// to a model it does not know, so /reasoning is not offered.
func (a *Agent) hasConfiguredModelLocked() bool {
	if strings.TrimSpace(a.model) != "" {
		return true
	}
	return a.activeIdx >= 0 && a.activeIdx < len(a.providers) && strings.TrimSpace(a.providers[a.activeIdx].Model) != ""
}

// opencodeConfigContentKey is the environment variable OpenCode reads its
// inline JSON config from.
const opencodeConfigContentKey = "OPENCODE_CONFIG_CONTENT"

// injectReasoningVariants rewrites an `opencode serve` / `opencode run`
// environment so every model it exposes carries one variant per reasoning
// level. Passing a variant through the API (`model.variant`) or the CLI
// (`--variant`) then selects the level, which is exactly what a custom model
// (e.g. an openai-compatible one from aiapi) lacks out of the box — OpenCode
// only honours --variant for models that declare `variants`.
//
// The variants are added only when a reasoning level is selected (effort != "")
// and when OPENCODE_CONFIG_CONTENT holds a JSON object. cc-connect never
// invents a provider config, it only annotates an existing one. The returned
// slice is always a fresh copy, so the agent's configured env is not mutated.
func injectReasoningVariants(env []string, effort string) []string {
	if len(env) == 0 || effort == "" {
		return env
	}
	out := append([]string(nil), env...)

	idx := -1
	for i, kv := range out {
		if k, _, ok := strings.Cut(kv, "="); ok && k == opencodeConfigContentKey {
			idx = i
			break
		}
	}
	if idx < 0 {
		return out
	}

	key, raw, _ := strings.Cut(out[idx], "=")
	patched, ok := addReasoningVariants(raw)
	if !ok {
		return out
	}
	out[idx] = key + "=" + patched
	return out
}

// addReasoningVariants merges the reasoning variants into every model of every
// provider in an OpenCode config JSON document. Existing variants the operator
// configured are preserved; only the missing reasoning levels are added.
func addReasoningVariants(configJSON string) (string, bool) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil || cfg == nil {
		return "", false
	}

	providers, ok := cfg["provider"].(map[string]any)
	if !ok || len(providers) == 0 {
		return "", false
	}

	changed := false
	for _, provRaw := range providers {
		prov, ok := provRaw.(map[string]any)
		if !ok {
			continue
		}
		models, ok := prov["models"].(map[string]any)
		if !ok {
			continue
		}
		for _, modelRaw := range models {
			model, ok := modelRaw.(map[string]any)
			if !ok {
				continue
			}
			variants, _ := model["variants"].(map[string]any)
			if variants == nil {
				variants = map[string]any{}
			}
			added := false
			for _, effort := range opencodeReasoningEfforts {
				if _, exists := variants[effort]; exists {
					continue
				}
				variants[effort] = map[string]any{"reasoningEffort": effort}
				added = true
			}
			if added {
				model["variants"] = variants
				changed = true
			}
		}
	}
	if !changed {
		return "", false
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", false
	}
	return string(data), true
}
