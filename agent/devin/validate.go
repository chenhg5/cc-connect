package devin

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errEmptyCatalog is a sentinel so a successful empty list is not cached.
var errEmptyCatalog = errors.New("devin: models list returned no families")

// ValidateModel accepts a name unchanged when nothing is known yet.
// A resolved name that still carries an effort suffix is kept as typed,
// so the next session can turn that suffix into thought_level.
// A pure alias, slug, or fuzzy name without a suffix is rewritten to the
// canonical ACP option, or the family's preferred variant when no session
// has advertised options.
func (a *Agent) ValidateModel(ctx context.Context, model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("unknown Devin model %q", model)
	}
	if a == nil {
		return model, nil
	}
	_ = a.AvailableModels(ctx)
	options := a.sessionOptionValues()
	a.mu.RLock()
	families := append([]catalogFamily(nil), a.families...)
	a.mu.RUnlock()
	if len(families) == 0 && len(options) == 0 {
		return model, nil
	}
	res := resolveFuzzy(model, families, options)
	if res.ambiguous {
		return "", fmt.Errorf("unknown Devin model %q (ambiguous: %s)", model, joinMax(res.candidates, 8))
	}
	if !res.ok {
		msg := fmt.Sprintf("unknown Devin model %q", model)
		if hints := joinMax(familySuggestions(families), 8); hints != "" {
			msg += " (" + hints + ")"
		}
		return "", errors.New(msg)
	}
	if res.effort != "" {
		return model, nil
	}
	if res.value == "" {
		return model, nil
	}
	return res.value, nil
}

func (a *Agent) sessionOptionValues() []string {
	if a == nil || a.Agent == nil {
		return nil
	}
	live := a.SessionModelOptions()
	if len(live) == 0 {
		return nil
	}
	out := make([]string, 0, len(live))
	for _, opt := range live {
		name := strings.TrimSpace(opt.Name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}
