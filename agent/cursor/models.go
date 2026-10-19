package cursor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// cursorModelsTimeout bounds `agent models`. Tests may shorten it.
var cursorModelsTimeout = 10 * time.Second

// cursorEffortLevels are suffix variants Cursor advertises.
// This agent launches those ids. Bracket forms are a separate CLI feature for
// some families; Grok rejects grok-4.7[effort=high], so we do not emit them.
var cursorEffortLevels = []string{
	"none", "minimal", "low", "medium", "high", "xhigh", "extra-high", "max",
}

// effortStripOrder lists suffixes longest-first so extra-high/xhigh win over high.
var effortStripOrder = []string{
	"extra-high", "minimal", "medium", "xhigh", "none", "high", "low", "max",
}

type cursorModelID struct {
	base          string
	thinking      bool
	thinkingAfter bool // true for claude-4.6-opus-high-thinking
	fast          bool
	effort        string
}

func normalizeCursorEffort(raw string) string {
	effort := strings.ToLower(strings.TrimSpace(raw))
	for _, known := range cursorEffortLevels {
		if effort == known {
			return effort
		}
	}
	return ""
}

func stripSuffixFold(name, suffix string) (string, bool) {
	if len(name) <= len(suffix) || !strings.HasSuffix(strings.ToLower(name), suffix) {
		return name, false
	}
	return name[:len(name)-len(suffix)], true
}

func parseCursorModel(name string) cursorModelID {
	name = strings.TrimSpace(name)
	id := cursorModelID{base: name}
	if rest, ok := stripSuffixFold(name, "-fast"); ok {
		id.fast = true
		name = rest
	}
	// Effort may sit before -thinking (claude-4.6-opus-high-thinking)
	// or after it (claude-opus-4-7-thinking-high).
	if rest, ok := stripSuffixFold(name, "-thinking"); ok {
		id.thinking = true
		id.thinkingAfter = true
		name = rest
	}
	for _, eff := range effortStripOrder {
		if rest, ok := stripSuffixFold(name, "-"+eff); ok {
			id.effort = eff
			name = rest
			break
		}
	}
	if !id.thinking {
		if rest, ok := stripSuffixFold(name, "-thinking"); ok {
			id.thinking = true
			name = rest
		}
	}
	id.base = name
	return id
}

func sameCursorFamily(a, b cursorModelID) bool {
	return strings.EqualFold(a.base, b.base) && a.thinking == b.thinking && a.fast == b.fast
}

func (a *Agent) baseModelLocked() string {
	if a.modelPinned {
		return a.model
	}
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

// launchModel is the id passed to `agent --model` and returned by GetModel.
func (a *Agent) launchModel(ctx context.Context) string {
	a.mu.RLock()
	base := strings.TrimSpace(a.baseModelLocked())
	effort := a.reasoningEffort
	a.mu.RUnlock()
	if base == "" || effort == "" || strings.EqualFold(base, "auto") {
		return base
	}
	return applyCursorEffort(base, effort, a.modelCatalog(ctx))
}

func applyCursorEffort(model, effort string, catalog []core.ModelOption) string {
	effort = normalizeCursorEffort(effort)
	if effort == "" || strings.EqualFold(strings.TrimSpace(model), "auto") {
		return model
	}
	if name, ok := familyVariant(catalog, model, effort); ok {
		return name
	}
	if name, ok := catalogLookup(catalog, model); ok {
		return name
	}
	return model
}

// familyVariant picks an advertised id in the same base/thinking/fast family.
// It does not invent a suffix order the catalog did not list.
func familyVariant(catalog []core.ModelOption, model, effort string) (string, bool) {
	cur := parseCursorModel(model)
	var sameOrder, other string
	for _, opt := range catalog {
		id := parseCursorModel(opt.Name)
		if !sameCursorFamily(cur, id) || id.effort != effort {
			continue
		}
		if id.thinkingAfter == cur.thinkingAfter && sameOrder == "" {
			sameOrder = opt.Name
		} else if other == "" {
			other = opt.Name
		}
	}
	if sameOrder != "" {
		return sameOrder, true
	}
	if other != "" {
		return other, true
	}
	return "", false
}

func (a *Agent) SetReasoningEffort(effort string) {
	normalized := normalizeCursorEffort(effort)
	if normalized == "" && strings.TrimSpace(effort) != "" {
		slog.Warn("cursor: ignoring unknown reasoning_effort", "value", effort)
		return
	}
	if normalized != "" {
		ok := false
		for _, known := range a.AvailableReasoningEfforts() {
			if known == normalized {
				ok = true
				break
			}
		}
		if !ok {
			slog.Warn("cursor: ignoring unsupported reasoning_effort", "value", normalized)
			return
		}
	}
	a.mu.Lock()
	a.reasoningEffort = normalized
	a.mu.Unlock()
	slog.Info("cursor: reasoning effort changed", "effort", normalized)
}

func (a *Agent) GetReasoningEffort() string {
	launch := strings.TrimSpace(a.launchModel(context.Background()))
	if launch == "" || strings.EqualFold(launch, "auto") {
		return ""
	}
	return parseCursorModel(launch).effort
}

func (a *Agent) AvailableReasoningEfforts() []string {
	a.mu.RLock()
	base := strings.TrimSpace(a.baseModelLocked())
	a.mu.RUnlock()
	if base == "" || strings.EqualFold(base, "auto") {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cursorModelsTimeout)
	defer cancel()
	return effortsForModel(base, a.modelCatalog(ctx))
}

func effortsForModel(model string, catalog []core.ModelOption) []string {
	if strings.TrimSpace(model) == "" || strings.EqualFold(model, "auto") {
		return nil
	}
	cur := parseCursorModel(model)
	have := map[string]bool{}
	for _, opt := range catalog {
		id := parseCursorModel(opt.Name)
		if sameCursorFamily(cur, id) && id.effort != "" {
			have[id.effort] = true
		}
	}
	var out []string
	for _, eff := range cursorEffortLevels {
		if have[eff] {
			out = append(out, eff)
		}
	}
	return out
}

func (a *Agent) ValidateModel(ctx context.Context, model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("cursor: unknown model %q", model)
	}
	catalog := a.AvailableModels(ctx)
	if len(catalog) == 0 {
		return model, nil
	}
	for _, m := range catalog {
		if m.Name == model {
			return m.Name, nil
		}
	}
	if matches := matchFold(catalog, func(m core.ModelOption) bool {
		return strings.EqualFold(m.Name, model)
	}); len(matches) == 1 {
		return matches[0].Name, nil
	} else if len(matches) > 1 {
		return "", ambiguousModelError(model, matches)
	}
	if matches := matchFold(catalog, func(m core.ModelOption) bool {
		if m.Alias != "" && strings.EqualFold(m.Alias, model) {
			return true
		}
		return m.Desc != "" && strings.EqualFold(m.Desc, model)
	}); len(matches) == 1 {
		return matches[0].Name, nil
	} else if len(matches) > 1 {
		return "", ambiguousModelError(model, matches)
	}
	lower := strings.ToLower(model)
	if matches := matchFold(catalog, func(m core.ModelOption) bool {
		return strings.HasPrefix(strings.ToLower(m.Name), lower)
	}); len(matches) == 1 {
		return matches[0].Name, nil
	} else if len(matches) > 1 {
		return "", ambiguousModelError(model, matches)
	}
	return "", fmt.Errorf("cursor: unknown model %q", model)
}

func matchFold(catalog []core.ModelOption, pred func(core.ModelOption) bool) []core.ModelOption {
	var out []core.ModelOption
	for _, m := range catalog {
		if pred(m) {
			out = append(out, m)
		}
	}
	return out
}

func ambiguousModelError(model string, matches []core.ModelOption) error {
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	if len(names) > 8 {
		names = append(names[:8], "...")
	}
	return fmt.Errorf("cursor: ambiguous model %q: %s", model, strings.Join(names, ", "))
}

func catalogLookup(catalog []core.ModelOption, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, m := range catalog {
		if m.Name == name {
			return m.Name, true
		}
	}
	for _, m := range catalog {
		if strings.EqualFold(m.Name, name) {
			return m.Name, true
		}
	}
	return "", false
}

func (a *Agent) bumpModelsGenLocked() {
	a.modelsGen++
	a.modelsCache = nil
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	a.mu.RLock()
	configured := core.GetProviderModels(a.providers, a.activeIdx)
	gen := a.modelsGen
	a.mu.RUnlock()
	if len(configured) > 0 {
		a.rememberModels(gen, configured)
		return configured
	}
	models, gen, current := a.fetchAdvertisedModels(ctx)
	if current && len(models) > 0 && a.rememberModels(gen, models) {
		return models
	}
	a.mu.RLock()
	cached := append([]core.ModelOption(nil), a.modelsCache...)
	curModel := strings.TrimSpace(a.baseModelLocked())
	a.mu.RUnlock()
	if len(cached) > 0 {
		return ensureAutoAndCurrent(cached, curModel)
	}
	if !current {
		return ensureAutoAndCurrent(nil, curModel)
	}
	return ensureAutoAndCurrent(cursorFallbackModels(), curModel)
}

func (a *Agent) modelCatalog(ctx context.Context) []core.ModelOption {
	if models := a.configuredModels(); len(models) > 0 {
		return models
	}
	a.mu.RLock()
	cached := append([]core.ModelOption(nil), a.modelsCache...)
	a.mu.RUnlock()
	if len(cached) > 0 {
		return cached
	}
	models, gen, current := a.fetchAdvertisedModels(ctx)
	if !current {
		a.mu.RLock()
		cached = append([]core.ModelOption(nil), a.modelsCache...)
		a.mu.RUnlock()
		return cached
	}
	return a.publishFetchedModels(gen, models)
}

// publishFetchedModels installs a fetch only when gen is still current.
// A rejected publish returns the catalog that belongs to the new generation.
func (a *Agent) publishFetchedModels(gen uint64, models []core.ModelOption) []core.ModelOption {
	if len(models) == 0 {
		return nil
	}
	if a.rememberModels(gen, models) {
		return models
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]core.ModelOption(nil), a.modelsCache...)
}

func (a *Agent) rememberModels(gen uint64, models []core.ModelOption) bool {
	cp := append([]core.ModelOption(nil), models...)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.modelsGen != gen {
		return false
	}
	a.modelsCache = cp
	return true
}

func (a *Agent) fetchAdvertisedModels(ctx context.Context) ([]core.ModelOption, uint64, bool) {
	a.mu.RLock()
	gen := a.modelsGen
	cmd := a.cmd
	extraArgs := append([]string{}, a.cliExtraArgs...)
	workDir := a.workDir
	extraEnv := core.MergeEnv(a.configEnv, a.providerEnvLocked())
	extraEnv = core.MergeEnv(extraEnv, a.sessionEnv)
	a.mu.RUnlock()
	models := runAgentModels(ctx, cmd, extraArgs, workDir, extraEnv)
	a.mu.RLock()
	still := a.modelsGen == gen
	a.mu.RUnlock()
	if !still {
		return nil, gen, false
	}
	return models, gen, true
}

// fetchModelsFromAgentCLI runs `agent models` and parses the output.
// Output format: "model-id - Display Name  (current)" or "model-id - Display Name".
func fetchModelsFromAgentCLI(ctx context.Context, cmd string, extraEnv []string) []core.ModelOption {
	return runAgentModels(ctx, cmd, nil, "", extraEnv)
}

func runAgentModels(ctx context.Context, cmd string, extraArgs []string, workDir string, extraEnv []string) []core.ModelOption {
	if strings.TrimSpace(cmd) == "" {
		cmd = "agent"
	}
	ctx, cancel := boundModelsContext(ctx)
	defer cancel()
	args := append(append([]string{}, extraArgs...), "models")
	c := exec.CommandContext(ctx, cmd, args...)
	if workDir != "" {
		c.Dir = workDir
	}
	c.Env = core.MergeEnv(os.Environ(), extraEnv)
	c.WaitDelay = 200 * time.Millisecond
	c.Cancel = func() error {
		return c.Process.Kill()
	}
	out, err := c.Output()
	if err != nil {
		slog.Debug("cursor: agent models failed", "error", err)
		return nil
	}
	return parseAgentModelsOutput(string(out))
}

func boundModelsContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= cursorModelsTimeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, cursorModelsTimeout)
}

func parseAgentModelsOutput(out string) []core.ModelOption {
	var models []core.ModelOption
	seen := make(map[string]struct{})
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "Available models" || strings.HasPrefix(line, "Tip:") {
			continue
		}
		idx := strings.Index(line, " - ")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		desc := strings.TrimSpace(line[idx+3:])
		if name == "" {
			continue
		}
		desc = stripModelStatusMarkers(desc)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, core.ModelOption{Name: name, Desc: desc})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

func stripModelStatusMarkers(desc string) string {
	for {
		desc = strings.TrimSpace(desc)
		open := strings.LastIndex(desc, " (")
		if open < 0 || !strings.HasSuffix(desc, ")") {
			return desc
		}
		inner := strings.ToLower(desc[open+2 : len(desc)-1])
		inner = strings.ReplaceAll(inner, " ", "")
		switch inner {
		case "current", "default", "current,default", "default,current":
			desc = desc[:open]
		default:
			return strings.TrimSpace(desc)
		}
	}
}

func cursorFallbackModels() []core.ModelOption {
	return []core.ModelOption{{Name: "auto", Desc: "Auto"}}
}

func ensureAutoAndCurrent(models []core.ModelOption, current string) []core.ModelOption {
	out := append([]core.ModelOption(nil), models...)
	if _, ok := catalogLookup(out, "auto"); !ok {
		out = append([]core.ModelOption{{Name: "auto", Desc: "Auto"}}, out...)
	}
	current = strings.TrimSpace(current)
	if current != "" {
		if _, ok := catalogLookup(out, current); !ok {
			out = append(out, core.ModelOption{Name: current, Desc: "Current"})
		}
	}
	return out
}
