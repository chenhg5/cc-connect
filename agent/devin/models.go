package devin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	// modelsFetchTimeout is the background catalog budget. Callers never
	// cancel it; a cold `devin models list` can take well over a minute.
	modelsFetchTimeout = 150 * time.Second
	modelsCacheTTL     = 6 * time.Hour
)

// modelsRunnerDefault runs when an agent has no injected runner.
// Tests replace it so New's prewarm never executes the real devin binary.
var modelsRunnerDefault ModelsRunner = runDevinModelsList

// modelsCacheDir overrides os.UserCacheDir()/cc-connect. Tests set a temp dir.
var modelsCacheDir string

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
	Label     string
	UID       string
	Slug      string
	Aliases   []string
	Variants  []string
	Preferred string
}

type devinVariant struct {
	ModelUID string `json:"model_uid"`
	Label    string `json:"label"`
}

// AvailableModels returns the cached Devin catalog when one exists,
// including a stale disk cache. A refresh runs in the background and
// is not awaited. With no catalog, the caller waits only until ctx
// ends, then the latest ACP model options are used, then the
// configured "models" list.
func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	if a == nil {
		return nil
	}
	a.loadDiskIfEmpty()
	if opts, ok := a.cachedModelsFresh(); ok {
		return opts
	}
	if opts, ok := a.cachedModelsAny(); ok {
		a.refreshCatalog()
		return opts
	}
	done := a.refreshCatalog()
	if waitFetch(ctx, done) {
		if opts, ok := a.cachedModelsAny(); ok {
			return opts
		}
	}
	return a.fallbackModels(ctx)
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
	return a.annotateLiveModels(a.Agent.AvailableModels(ctx))
}

// SetModelsRunner is the supported injection point for wrappers and tests.
// The runner is stored under a.mu, the same lock modelsRunner reads.
func (a *Agent) SetModelsRunner(run ModelsRunner) {
	a.mu.Lock()
	a.modelsRun = run
	a.mu.Unlock()
}

func (a *Agent) modelsRunner() ModelsRunner {
	a.mu.RLock()
	run := a.modelsRun
	a.mu.RUnlock()
	if run != nil {
		return run
	}
	if modelsRunnerDefault != nil {
		return modelsRunnerDefault
	}
	return runDevinModelsList
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

func (a *Agent) cachedModelsFresh() ([]core.ModelOption, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.modelsCache) == 0 || time.Since(a.modelsCachedAt) >= modelsCacheTTL {
		return nil, false
	}
	return cloneModels(a.modelsCache), true
}

func (a *Agent) cachedModelsAny() ([]core.ModelOption, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.modelsCache) == 0 {
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
		Label:     strings.TrimSpace(family.FamilyLabel),
		UID:       strings.TrimSpace(family.FamilyUID),
		Slug:      strings.TrimSpace(family.Slug),
		Aliases:   aliases,
		Variants:  variants,
		Preferred: preferredVariant(family.Variants),
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
