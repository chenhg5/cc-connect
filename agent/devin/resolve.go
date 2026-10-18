package devin

import (
	"strings"
	"unicode"
)

// fuzzyResult is one pass of model-name resolution.
// ambiguous candidates are display labels (slugs) for the error text.
type fuzzyResult struct {
	value      string
	effort     string
	ok         bool
	ambiguous  bool
	candidates []string
}

// resolveFuzzy normalizes input, strips a trailing effort suffix, then
// matches exact option/variant ids, exact family names, then a
// hyphen-token run. The first stage with exactly one hit wins.
// opus5.5 normalizes to "opus5-5", which is not the token run "opus-5-5",
// so it does not match Claude Opus 5.5.
func resolveFuzzy(input string, families []catalogFamily, options []string) fuzzyResult {
	norm := normalizeModelKey(input)
	if norm == "" {
		return fuzzyResult{}
	}
	base, suffix := stripEffortSuffix(norm)
	if base == "" {
		base = norm
	}
	effort := strings.TrimPrefix(suffix, "-")
	if cands := exactIDCandidates(norm, base, families, options, suffix); len(cands) > 0 {
		return finishCandidates(cands, effort)
	}
	if cands := exactFamilyCandidates(base, families, options, suffix); len(cands) > 0 {
		return finishCandidates(cands, effort)
	}
	return finishCandidates(tokenCandidates(base, families, options, suffix), effort)
}

func finishCandidates(cands []modelCandidate, effort string) fuzzyResult {
	uniq := uniqueCandidates(cands)
	if len(uniq) == 1 {
		return fuzzyResult{value: uniq[0].value, effort: effort, ok: true}
	}
	if len(uniq) > 1 {
		labels := make([]string, 0, len(uniq))
		for _, c := range uniq {
			labels = append(labels, c.label)
		}
		return fuzzyResult{ambiguous: true, candidates: labels}
	}
	return fuzzyResult{}
}

type modelCandidate struct {
	value string
	label string
}

func exactIDCandidates(norm, base string, families []catalogFamily, options []string, suffix string) []modelCandidate {
	var out []modelCandidate
	for _, opt := range options {
		key := normalizeModelKey(opt)
		if key == norm || key == base {
			out = append(out, modelCandidate{value: opt, label: opt})
		}
	}
	for _, fam := range families {
		for _, variant := range fam.Variants {
			key := normalizeModelKey(variant)
			if key != norm && key != base {
				continue
			}
			value := canonicalForFamily(fam, options, suffix)
			if value == "" {
				continue
			}
			out = append(out, modelCandidate{value: value, label: familyLabel(fam)})
		}
	}
	return out
}

func exactFamilyCandidates(base string, families []catalogFamily, options []string, suffix string) []modelCandidate {
	var out []modelCandidate
	for _, fam := range families {
		if !familyNameIs(fam, base) {
			continue
		}
		value := canonicalForFamily(fam, options, suffix)
		if value == "" {
			continue
		}
		out = append(out, modelCandidate{value: value, label: familyLabel(fam)})
	}
	return out
}

func tokenCandidates(base string, families []catalogFamily, options []string, suffix string) []modelCandidate {
	if base == "" {
		return nil
	}
	var out []modelCandidate
	claimed := map[string]struct{}{}
	for _, fam := range families {
		if fusionExcluded(fam, base) {
			continue
		}
		if !familyTokenMatch(fam, base) {
			continue
		}
		value := canonicalForFamily(fam, options, suffix)
		if value == "" {
			continue
		}
		for _, variant := range fam.Variants {
			b, _ := stripEffortSuffix(normalizeModelKey(variant))
			if b != "" {
				claimed[b] = struct{}{}
			}
		}
		out = append(out, modelCandidate{value: value, label: familyLabel(fam)})
	}
	for _, opt := range options {
		optBase, _ := stripEffortSuffix(normalizeModelKey(opt))
		if _, taken := claimed[optBase]; taken {
			continue
		}
		if fusionTextExcluded(optBase, base) || !tokenRun(base, optBase) {
			continue
		}
		out = append(out, modelCandidate{value: opt, label: opt})
	}
	return out
}

func familyNameIs(fam catalogFamily, base string) bool {
	if normalizeModelKey(fam.Slug) == base || normalizeModelKey(fam.UID) == base || normalizeModelKey(fam.Label) == base {
		return true
	}
	for _, alias := range fam.Aliases {
		if normalizeModelKey(alias) == base {
			return true
		}
	}
	return false
}

func familyTokenMatch(fam catalogFamily, base string) bool {
	fields := []string{fam.Slug, fam.UID, fam.Label}
	fields = append(fields, fam.Aliases...)
	for _, variant := range fam.Variants {
		b, _ := stripEffortSuffix(normalizeModelKey(variant))
		fields = append(fields, b)
	}
	for _, field := range fields {
		key := normalizeModelKey(field)
		if tokenRun(base, key) {
			return true
		}
	}
	return false
}

func fusionExcluded(fam catalogFamily, input string) bool {
	if strings.HasPrefix(input, "fusion") {
		return false
	}
	for _, field := range []string{fam.Slug, fam.UID} {
		if isFusionKey(normalizeModelKey(field)) {
			return true
		}
	}
	return false
}

func fusionTextExcluded(candidate, input string) bool {
	if strings.HasPrefix(input, "fusion") {
		return false
	}
	return isFusionKey(candidate)
}

// isFusionKey matches the Fusion family itself ("fusion") and its composite
// variant ids ("fusion-<primary>-sidekick-<secondary>").
func isFusionKey(key string) bool {
	return key == "fusion" || strings.HasPrefix(key, "fusion-")
}

func tokenRun(input, candidate string) bool {
	if input == "" || candidate == "" {
		return false
	}
	in := strings.Split(input, "-")
	cand := strings.Split(candidate, "-")
	if len(in) > len(cand) {
		return false
	}
	for i := 0; i+len(in) <= len(cand); i++ {
		match := true
		for j := range in {
			if cand[i+j] != in[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func canonicalForFamily(fam catalogFamily, options []string, suffix string) string {
	if len(options) == 0 {
		if fam.Preferred != "" {
			return fam.Preferred
		}
		if len(fam.Variants) > 0 {
			return fam.Variants[0]
		}
		return ""
	}
	bases := map[string]struct{}{}
	for _, variant := range fam.Variants {
		b, _ := stripEffortSuffix(normalizeModelKey(variant))
		if b != "" {
			bases[b] = struct{}{}
		}
	}
	var hits []string
	for _, opt := range options {
		b, _ := stripEffortSuffix(normalizeModelKey(opt))
		if _, ok := bases[b]; ok {
			hits = append(hits, opt)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return pickMatchedOption(hits, suffix)
}

// Families with an empty/blank label are intentionally rendered by slug, then uid.
func familyLabel(fam catalogFamily) string {
	if s := strings.TrimSpace(fam.Slug); s != "" {
		return s
	}
	if s := strings.TrimSpace(fam.UID); s != "" {
		return s
	}
	return strings.TrimSpace(fam.Label)
}

func uniqueCandidates(in []modelCandidate) []modelCandidate {
	var out []modelCandidate
	seen := map[string]struct{}{}
	for _, c := range in {
		if c.value == "" {
			continue
		}
		if _, ok := seen[c.value]; ok {
			continue
		}
		seen[c.value] = struct{}{}
		out = append(out, c)
	}
	return out
}

func normalizeModelKey(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '.' || r == '_' || unicode.IsSpace(r) || r == '-' {
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
			continue
		}
		b.WriteRune(r)
		prevDash = false
	}
	return strings.Trim(b.String(), "-")
}

func familySuggestions(families []catalogFamily) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, fam := range families {
		name := ""
		if len(fam.Aliases) > 0 {
			name = strings.TrimSpace(fam.Aliases[0])
		}
		if name == "" {
			name = strings.TrimSpace(fam.Slug)
		}
		if name == "" {
			name = strings.TrimSpace(fam.UID)
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func joinMax(items []string, n int) string {
	if len(items) > n {
		items = items[:n]
	}
	return strings.Join(items, ", ")
}
