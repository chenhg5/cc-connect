package devin

import "strings"

// devinEfforts is the reasoning-effort set used until a session
// advertises its own thought_level choices.
var devinEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// effortSuffixes is longest-first so "-xhigh" is not read as "-high".
var effortSuffixes = []string{"-medium", "-xhigh", "-high", "-max", "-low"}

// resolveModelValue maps a user model onto a value present in the
// session's model option list. The returned effort is the suffix the
// input carried ("high"), empty when the input had none. It does not
// rewrite the model id: Devin only accepts advertised option values,
// and thought_level is set separately.
func (a *Agent) resolveModelValue(input string, optionValues []string) (string, string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	if value, effort, ok := matchOptionByBase(input, optionValues); ok {
		return value, effort
	}
	fam, ok := a.lookupCatalogFamily(input)
	if !ok {
		base, _ := stripEffortSuffix(input)
		if !strings.EqualFold(base, input) {
			fam, ok = a.lookupCatalogFamily(base)
		}
	}
	if !ok {
		return "", ""
	}
	matches := matchFamilyOptions(fam, optionValues)
	if len(matches) == 0 {
		return "", ""
	}
	_, suffix := stripEffortSuffix(input)
	return pickMatchedOption(matches, suffix), strings.TrimPrefix(suffix, "-")
}

func matchOptionByBase(input string, options []string) (string, string, bool) {
	base, suffix := stripEffortSuffix(input)
	var matches []string
	for _, opt := range options {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		optBase, _ := stripEffortSuffix(opt)
		if strings.EqualFold(optBase, base) {
			matches = append(matches, opt)
		}
	}
	if len(matches) == 0 {
		return "", "", false
	}
	return pickMatchedOption(matches, suffix), strings.TrimPrefix(suffix, "-"), true
}

func matchFamilyOptions(fam catalogFamily, options []string) []string {
	var matches []string
	seen := make(map[string]struct{})
	for _, variant := range fam.Variants {
		value, _, ok := matchOptionByBase(variant, options)
		if !ok {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		matches = append(matches, value)
	}
	return matches
}

func pickMatchedOption(matches []string, suffix string) string {
	suffix = strings.ToLower(suffix)
	if suffix != "" {
		for _, m := range matches {
			if strings.HasSuffix(strings.ToLower(m), suffix) {
				return m
			}
		}
	}
	for _, m := range matches {
		if strings.HasSuffix(strings.ToLower(m), "-medium") {
			return m
		}
	}
	return matches[0]
}

func stripEffortSuffix(model string) (base, suffix string) {
	lower := strings.ToLower(model)
	for _, suf := range effortSuffixes {
		if strings.HasSuffix(lower, suf) && len(model) > len(suf) {
			return model[:len(model)-len(suf)], suf
		}
	}
	return model, ""
}

func (a *Agent) lookupCatalogFamily(input string) (catalogFamily, bool) {
	input = strings.TrimSpace(input)
	if input == "" || a == nil {
		return catalogFamily{}, false
	}
	a.mu.RLock()
	families := a.families
	a.mu.RUnlock()
	for _, fam := range families {
		if familyMatches(fam, input) {
			return fam, true
		}
	}
	return catalogFamily{}, false
}

func familyMatches(fam catalogFamily, input string) bool {
	if strings.EqualFold(fam.Slug, input) || strings.EqualFold(fam.UID, input) || strings.EqualFold(fam.Label, input) {
		return true
	}
	for _, alias := range fam.Aliases {
		if strings.EqualFold(strings.TrimSpace(alias), input) {
			return true
		}
	}
	for _, variant := range fam.Variants {
		if strings.EqualFold(strings.TrimSpace(variant), input) {
			return true
		}
	}
	return false
}
