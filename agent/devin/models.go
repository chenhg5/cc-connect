package devin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	modelsListTimeout    = 20 * time.Second
	modelsPrewarmTimeout = 60 * time.Second
	modelsCacheTTL       = 10 * time.Minute
)

type devinFamily struct {
	FamilyLabel string         `json:"family_label"`
	FamilyUID   string         `json:"family_uid"`
	Slug        string         `json:"slug"`
	Aliases     []string       `json:"aliases"`
	Variants    []devinVariant `json:"variants"`
}

// catalogFamily is the cached models-list entry used to resolve slugs,
// aliases, and labels onto advertised ACP option values.
type catalogFamily struct {
	Label    string
	UID      string
	Slug     string
	Aliases  []string
	Variants []string
}

type devinVariant struct {
	ModelUID string `json:"model_uid"`
	Label    string `json:"label"`
}

// AvailableModels asks `devin models list --format json` (or models_cmd)
// and caches a successful result for 10 minutes. The binary is `devin`
// on PATH unless models_cmd is set; the ACP wrapper cmd is not used.
// On failure the embedded ACP list is returned: last session's
// configOptions, then the configured "models" option.
func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	if a != nil && a.Agent != nil {
		if live := a.Agent.SessionModelOptions(); len(live) > 0 {
			return a.annotateLiveModels(live)
		}
	}
	if cached, ok := a.cachedModels(); ok {
		return cached
	}
	if done := a.prewarmInFlight(); done != nil {
		if !waitPrewarm(ctx, done) {
			return a.fallbackModels(ctx)
		}
		if cached, ok := a.cachedModels(); ok {
			return cached
		}
		return a.fallbackModels(ctx)
	}
	opts, err := a.fetchModels(ctx, modelsListTimeout)
	if err != nil {
		slog.Warn("devin: models list failed", "error", err)
		return a.fallbackModels(ctx)
	}
	return opts
}

// startModelsPrewarm runs models list once, in the background, for up
// to a minute. Later StartSession calls do not start another fetch.
func (a *Agent) startModelsPrewarm() {
	a.prewarmOnce.Do(func() {
		done := make(chan struct{})
		a.mu.Lock()
		a.prewarmDone = done
		a.mu.Unlock()
		go a.runModelsPrewarm(done)
	})
}

func (a *Agent) runModelsPrewarm(done chan struct{}) {
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), modelsPrewarmTimeout)
	defer cancel()
	if _, err := a.fetchModels(ctx, modelsPrewarmTimeout); err != nil {
		slog.Debug("devin: models prewarm failed", "error", err)
	}
}

func (a *Agent) prewarmInFlight() <-chan struct{} {
	a.mu.RLock()
	done := a.prewarmDone
	a.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
		return done
	}
}

func waitPrewarm(ctx context.Context, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
}

func (a *Agent) fetchModels(ctx context.Context, timeout time.Duration) ([]core.ModelOption, error) {
	if cached, ok := a.cachedModels(); ok {
		return cached, nil
	}
	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := a.modelsRunner()(listCtx, a.modelsBinary(), a.modelsEnv())
	if err != nil {
		return nil, err
	}
	opts, families, err := parseDevinModels(out)
	if err != nil {
		return nil, err
	}
	a.storeModels(opts, families)
	return cloneModels(opts), nil
}

// annotateLiveModels keeps the session's settable model ids and adds
// the catalog slug as Alias when a family is already cached. It does
// not fetch the catalog.
func (a *Agent) annotateLiveModels(opts []core.ModelOption) []core.ModelOption {
	out := cloneModels(opts)
	a.mu.RLock()
	families := a.families
	a.mu.RUnlock()
	if len(families) == 0 {
		return out
	}
	used := make(map[string]struct{})
	for i := range out {
		fam, ok := familyForModel(out[i].Name, families)
		if !ok {
			continue
		}
		alias := uniqueAlias(fam.Slug, used)
		if alias != "" {
			out[i].Alias = alias
		}
	}
	return out
}

func familyForModel(model string, families []catalogFamily) (catalogFamily, bool) {
	base, _ := stripEffortSuffix(model)
	for _, fam := range families {
		for _, variant := range fam.Variants {
			variantBase, _ := stripEffortSuffix(variant)
			if strings.EqualFold(variant, model) || strings.EqualFold(variantBase, base) {
				return fam, true
			}
		}
	}
	return catalogFamily{}, false
}

func (a *Agent) fallbackModels(ctx context.Context) []core.ModelOption {
	if a == nil || a.Agent == nil {
		return nil
	}
	return a.Agent.AvailableModels(ctx)
}

func (a *Agent) modelsRunner() ModelsRunner {
	a.mu.RLock()
	run := a.modelsRun
	a.mu.RUnlock()
	if run == nil {
		return runDevinModelsList
	}
	return run
}

func (a *Agent) modelsBinary() string {
	a.mu.RLock()
	name := strings.TrimSpace(a.modelsCmd)
	a.mu.RUnlock()
	if name == "" {
		name = "devin"
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return name
}

func (a *Agent) modelsEnv() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var extra []string
	for k, v := range a.env {
		extra = append(extra, k+"="+v)
	}
	return core.MergeEnv(os.Environ(), extra)
}

func (a *Agent) cachedModels() ([]core.ModelOption, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.modelsCache) == 0 || time.Since(a.modelsCachedAt) >= modelsCacheTTL {
		return nil, false
	}
	return cloneModels(a.modelsCache), true
}

func (a *Agent) storeModels(opts []core.ModelOption, families []catalogFamily) {
	a.mu.Lock()
	a.modelsCache = cloneModels(opts)
	a.modelsCachedAt = time.Now()
	a.families = append([]catalogFamily(nil), families...)
	a.mu.Unlock()
}

func runDevinModelsList(ctx context.Context, binary string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, "models", "list", "--format", "json")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("devin: models list: %w", err)
	}
	return out, nil
}

func parseDevinModels(raw []byte) ([]core.ModelOption, []catalogFamily, error) {
	var doc struct {
		Families []devinFamily `json:"families"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("devin: parse models json: %w", err)
	}
	usedAlias := make(map[string]struct{})
	var opts []core.ModelOption
	var families []catalogFamily
	for _, family := range doc.Families {
		fam, ok := catalogFromFamily(family)
		if ok {
			families = append(families, fam)
		}
		opt, ok := familyOption(family)
		if !ok {
			continue
		}
		opt.Alias = uniqueAlias(opt.Alias, usedAlias)
		opts = append(opts, opt)
	}
	return opts, families, nil
}

func catalogFromFamily(family devinFamily) (catalogFamily, bool) {
	var variants []string
	for _, variant := range family.Variants {
		id := strings.TrimSpace(variant.ModelUID)
		if id == "" {
			continue
		}
		variants = append(variants, id)
	}
	if len(variants) == 0 {
		return catalogFamily{}, false
	}
	var aliases []string
	for _, alias := range family.Aliases {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			aliases = append(aliases, alias)
		}
	}
	return catalogFamily{
		Label:    strings.TrimSpace(family.FamilyLabel),
		UID:      strings.TrimSpace(family.FamilyUID),
		Slug:     strings.TrimSpace(family.Slug),
		Aliases:  aliases,
		Variants: variants,
	}, true
}

func uniqueAlias(alias string, used map[string]struct{}) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ""
	}
	if _, taken := used[alias]; taken {
		return ""
	}
	used[alias] = struct{}{}
	return alias
}

func familyOption(family devinFamily) (core.ModelOption, bool) {
	name := preferredVariant(family.Variants)
	if name == "" {
		return core.ModelOption{}, false
	}
	desc := strings.TrimSpace(family.FamilyLabel)
	if desc == "" {
		desc = strings.TrimSpace(family.FamilyUID)
	}
	return core.ModelOption{Name: name, Desc: desc, Alias: strings.TrimSpace(family.Slug)}, true
}

// preferredVariant picks the variant whose uid ends in "-medium",
// otherwise the first non-empty uid.
func preferredVariant(variants []devinVariant) string {
	first := ""
	for _, variant := range variants {
		id := strings.TrimSpace(variant.ModelUID)
		if id == "" {
			continue
		}
		if first == "" {
			first = id
		}
		if strings.HasSuffix(strings.ToLower(id), "-medium") {
			return id
		}
	}
	return first
}

func cloneModels(in []core.ModelOption) []core.ModelOption {
	if len(in) == 0 {
		return nil
	}
	return append([]core.ModelOption(nil), in...)
}
