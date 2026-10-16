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
	var families []catalogFamily
	if a != nil {
		a.mu.RLock()
		families = append([]catalogFamily(nil), a.families...)
		a.mu.RUnlock()
	}
	res := resolveFuzzy(input, families, optionValues)
	if !res.ok {
		return "", ""
	}
	return res.value, res.effort
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
