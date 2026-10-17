package acp

import "strings"

// lookupModeAlias returns the canonical mode id for input when aliases
// contains a case-insensitive key. Empty when nothing matches.
func lookupModeAlias(aliases map[string]string, input string) string {
	input = strings.TrimSpace(input)
	if input == "" || len(aliases) == 0 {
		return ""
	}
	for key, value := range aliases {
		if strings.EqualFold(strings.TrimSpace(key), input) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// parseModeAliases accepts map[string]string or map[string]any of strings.
func parseModeAliases(v any) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		return cloneModeAliases(m)
	case map[string]any:
		out := make(map[string]string, len(m))
		for key, value := range m {
			s, ok := value.(string)
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			s = strings.TrimSpace(s)
			if key == "" || s == "" {
				continue
			}
			out[key] = s
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func cloneModeAliases(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// matchModeWithAliases resolves a user-typed mode. An alias target is
// returned even before the agent has advertised its mode list, so a
// configured alias such as "normal" is stored as the canonical id.
// A name that only matches the live list (no alias) still needs that list.
func matchModeWithAliases(input string, aliases map[string]string, modes []coreMode) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	aliased := lookupModeAlias(aliases, input)
	if id := matchModeList(aliased, modes); id != "" {
		return id
	}
	if aliased != "" {
		return aliased
	}
	return matchModeList(input, modes)
}

// coreMode is the small slice of a permission mode matchModeWithAliases needs.
type coreMode struct {
	Key  string
	Name string
}

func matchModeList(input string, modes []coreMode) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	for _, m := range modes {
		if strings.EqualFold(m.Key, input) || strings.EqualFold(m.Name, input) {
			return m.Key
		}
	}
	return ""
}
