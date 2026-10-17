package devin

import "strings"

// defaultModeAliases maps Claude-style and legacy Devin config values
// onto the mode ids `devin acp` actually advertises. "normal" is the
// value shipped in existing production configs.
func defaultModeAliases() map[string]string {
	return map[string]string{
		"default":           "accept-edits",
		"normal":            "accept-edits",
		"code":              "accept-edits",
		"accept-edits":      "accept-edits",
		"acceptEdits":       "accept-edits",
		"auto":              "accept-edits",
		"edit":              "accept-edits",
		"plan":              "plan",
		"ask":               "ask",
		"readonly":          "ask",
		"bypass":            "bypass",
		"bypassPermissions": "bypass",
		"yolo":              "bypass",
		"dangerous":         "bypass",
		"full-auto":         "bypass",
	}
}

// mergeModeAliases starts from the defaults and lets every user entry
// replace the default with the same key, ignoring case.
func mergeModeAliases(user any) map[string]string {
	out := defaultModeAliases()
	for key, value := range aliasMap(user) {
		overlayModeAlias(out, key, value)
	}
	return out
}

func aliasMap(v any) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for key, value := range m {
			s, ok := value.(string)
			if ok {
				out[key] = s
			}
		}
		return out
	default:
		return nil
	}
}

func overlayModeAlias(dst map[string]string, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	for existing := range dst {
		if strings.EqualFold(existing, key) {
			delete(dst, existing)
		}
	}
	dst[key] = value
}
