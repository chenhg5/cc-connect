package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.SkillCatalogProvider = (*Agent)(nil)

// ListSkills returns the authoritative enabled skill catalog for the current
// workspace. It takes precedence over SkillDirs directory discovery; errors
// must not fall back to scanning directories that can contain disabled skills.
//
// Native OpenCode semantics honored (see https://opencode.ai/docs/skills/,
// https://opencode.ai/docs/permissions/):
//   - permission.skill patterns (allow/deny/ask, last match wins, agent
//     override takes precedence). Only "allow" skills are returned; "deny"
//     and "ask" are hidden, matching native behavior where deny hides the
//     skill and ask requires interactive approval cc-connect cannot provide.
//   - tools.skill=false (legacy) disables the skill tool entirely.
//   - OPENCODE_DISABLE_CLAUDE_CODE_SKILLS disables .claude/skills locations.
//   - SKILL.md must carry valid frontmatter (name+description, name matches
//     directory, opencode name regex, description 1-1024). Malformed skills
//     are skipped and never injected.
//   - Unreadable/invalid opencode.json fails closed with an error so the
//     engine shows a resolution error instead of falling back to directories.
//
// Only allowed skills have their SKILL.md body loaded. Denied/asked skills
// are skipped by directory name before any file read, so their instructions
// are never injected into the agent prompt.
func (a *Agent) ListSkills(ctx context.Context) ([]*core.Skill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	workDir := a.workDir
	agentName := a.agentName
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	a.mu.RUnlock()

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("opencode: skills work directory: %w", err)
	}
	env := opencodeEnvLookup(extraEnv)

	policy, err := resolveOpencodeSkillPolicy(absDir, agentName, env)
	if err != nil {
		return nil, err
	}
	if policy.disabled {
		return []*core.Skill{}, nil
	}

	dirs := opencodeSkillDirsFiltered(absDir, env)
	seen := make(map[string]struct{})
	out := make([]*core.Skill, 0)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !isOpencodeSkillSubdir(dir, entry) {
				continue
			}
			name := entry.Name()
			key := strings.ToLower(name)
			if _, dup := seen[key]; dup {
				continue
			}
			// Permission check by directory name before any file I/O so
			// deny/ask skills are never read, let alone injected.
			if policy.decision(name) != "allow" {
				continue
			}
			skillPath := filepath.Join(dir, name, "SKILL.md")
			skill, err := loadOpencodeSkillFile(skillPath, name)
			if err != nil {
				// Malformed skill: skip, never inject. A single bad file
				// must not break the whole catalog.
				continue
			}
			seen[key] = struct{}{}
			out = append(out, skill)
		}
	}
	if out == nil {
		out = []*core.Skill{}
	}
	return out, nil
}

func isOpencodeSkillSubdir(parent string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.IsDir()
}

var opencodeSkillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// loadOpencodeSkillFile reads one allowed skill and enforces native frontmatter
// rules: name+description required, name matches directory, name regex,
// description 1-1024 chars, non-empty body. Returns error for malformed files.
func loadOpencodeSkillFile(skillPath, dirName string) (*core.Skill, error) {
	skill, err := core.LoadSkillFile(skillPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, err
	}
	front := parseOpencodeFrontmatter(string(data))
	name := strings.TrimSpace(front["name"])
	desc := strings.TrimSpace(front["description"])
	if name == "" || desc == "" {
		return nil, fmt.Errorf("opencode: skill %q missing name/description", dirName)
	}
	if name != dirName {
		return nil, fmt.Errorf("opencode: skill name %q must match directory %q", name, dirName)
	}
	if len(name) < 1 || len(name) > 64 || !opencodeSkillNameRe.MatchString(name) {
		return nil, fmt.Errorf("opencode: invalid skill name %q", name)
	}
	if len([]rune(desc)) < 1 || len([]rune(desc)) > 1024 {
		return nil, fmt.Errorf("opencode: invalid skill description for %q", name)
	}
	if strings.TrimSpace(skill.Prompt) == "" {
		return nil, fmt.Errorf("opencode: empty instructions for %q", name)
	}
	// Preserve native name/description from frontmatter.
	skill.Name = name
	skill.Description = desc
	return skill, nil
}

func parseOpencodeFrontmatter(raw string) map[string]string {
	// Intentionally minimal YAML: single-line "key: value" only.
	// Folded/block scalars (e.g. "description: >") are not supported;
	// OpenCode does not document them for SKILL.md frontmatter.
	content := strings.TrimSpace(raw)
	out := map[string]string{}
	if !strings.HasPrefix(content, "---") {
		return out
	}
	rest := content[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return out
	}
	block := rest[:endIdx]
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if key == "name" || key == "description" {
			out[key] = val
		}
	}
	return out
}

// -- policy --

type opencodeSkillRule struct {
	pattern string
	action  string // allow|deny|ask
}

type opencodeSkillPolicy struct {
	disabled    bool
	globalRules []opencodeSkillRule
	agentRules  []opencodeSkillRule
}

func (p opencodeSkillPolicy) decision(skill string) string {
	if p.disabled {
		return "deny"
	}
	if act, ok := matchOpencodeRules(p.agentRules, skill); ok {
		return act
	}
	if act, ok := matchOpencodeRules(p.globalRules, skill); ok {
		return act
	}
	return "allow"
}

func matchOpencodeRules(rules []opencodeSkillRule, skill string) (string, bool) {
	matched := false
	action := ""
	for _, r := range rules {
		ok, err := path.Match(r.pattern, skill)
		if err != nil {
			continue
		}
		if ok {
			matched = true
			action = r.action
		}
	}
	return action, matched
}

type opencodeEnvFunc func(key string) (string, bool)

func opencodeEnvLookup(extra []string) opencodeEnvFunc {
	over := map[string]string{}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			over[k] = v
		}
	}
	return func(key string) (string, bool) {
		if v, ok := over[key]; ok {
			return v, true
		}
		return os.LookupEnv(key)
	}
}

func opencodeEnvTruthy(env opencodeEnvFunc, key string) bool {
	v, ok := env(key)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func opencodeSkillDirsFiltered(workDir string, env opencodeEnvFunc) []string {
	dirs := opencodeSkillDirs(workDir)
	if env != nil && opencodeEnvTruthy(env, "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS") {
		filtered := dirs[:0]
		for _, d := range dirs {
			if isClaudeCompatSkillDir(d) {
				continue
			}
			filtered = append(filtered, d)
		}
		dirs = filtered
	}
	return dirs
}

func isClaudeCompatSkillDir(dir string) bool {
	clean := filepath.ToSlash(filepath.Clean(dir))
	return strings.Contains(clean, "/.claude/skills") || strings.HasSuffix(clean, ".claude/skills")
}

// resolveOpencodeSkillPolicy loads and merges opencode.json configs.
// Load order (later wins): global, OPENCODE_CONFIG, project walk-up, inline.
// Any existing-but-unreadable or invalid config fails closed with an error.
func resolveOpencodeSkillPolicy(workDir, agentName string, env opencodeEnvFunc) (opencodeSkillPolicy, error) {
	var policy opencodeSkillPolicy
	home, _ := os.UserHomeDir()

	sources := make([]json.RawMessage, 0, 4)
	if home != "" {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			data, err := readOpencodeConfigFile(filepath.Join(home, ".config", "opencode", name))
			if err != nil {
				return policy, err
			}
			if data != nil {
				sources = append(sources, data)
				break
			}
		}
	}
	if cfgPath, ok := env("OPENCODE_CONFIG"); ok && strings.TrimSpace(cfgPath) != "" {
		data, err := readOpencodeConfigFile(strings.TrimSpace(cfgPath))
		if err != nil {
			return policy, err
		}
		if data == nil {
			return policy, fmt.Errorf("opencode: OPENCODE_CONFIG not found: %s", strings.TrimSpace(cfgPath))
		}
		sources = append(sources, data)
	}
	if projPath, err := findOpencodeConfigInWorkDir(workDir); err != nil {
		return policy, err
	} else if projPath != "" {
		data, err := readOpencodeConfigFile(projPath)
		if err != nil {
			return policy, err
		}
		if data != nil {
			sources = append(sources, data)
		}
	}
	if inline, ok := env("OPENCODE_CONFIG_CONTENT"); ok && strings.TrimSpace(inline) != "" {
		data, err := parseOpencodeJSONC(strings.TrimSpace(inline))
		if err != nil {
			return policy, fmt.Errorf("opencode: invalid OPENCODE_CONFIG_CONTENT: %w", err)
		}
		sources = append(sources, data)
	}

	disabledSet := false
	disabled := false
	// Loop order is intentional: sources are appended global → custom →
	// project → inline, so later sources override earlier ones.
	for _, src := range sources {
		cfg, err := parseOpencodeConfig(src)
		if err != nil {
			return policy, err
		}
		policy.globalRules = append(policy.globalRules, cfg.globalRules...)
		policy.agentRules = append(policy.agentRules, cfg.agentRules(agentName)...)
		if cfg.toolsSkillSet {
			disabled = !cfg.toolsSkill
			disabledSet = true
		}
		if cfg.agentToolsSkillSet(agentName) {
			disabled = !cfg.agentToolsSkill(agentName)
			disabledSet = true
		}
	}
	if disabledSet {
		policy.disabled = disabled
	}
	return policy, nil
}

func findOpencodeConfigInWorkDir(workDir string) (string, error) {
	current := filepath.Clean(workDir)
	if current == "" || current == "." {
		// If the cwd cannot be resolved either, the caller gets ("", nil)
		// and policy resolution silently uses the global config only.
		// That is intentional: a relative workDir without a resolvable
		// cwd has no project scope to load.
		if abs, err := filepath.Abs(workDir); err == nil {
			current = abs
		} else {
			return "", fmt.Errorf("opencode: skills work directory: %w", err)
		}
	}
	stopAt := findOpencodeProjectRoot(current)
	for {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			candidate := filepath.Join(current, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			} else if !os.IsNotExist(err) {
				return "", fmt.Errorf("opencode: stat config %s: %w", candidate, err)
			}
		}
		if stopAt != "" && sameOpencodePath(current, stopAt) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", nil
}

func readOpencodeConfigFile(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opencode: read config %s: %w", path, err)
	}
	parsed, err := parseOpencodeJSONC(string(data))
	if err != nil {
		return nil, fmt.Errorf("opencode: invalid config %s: %w", path, err)
	}
	return parsed, nil
}

func parseOpencodeJSONC(raw string) (json.RawMessage, error) {
	stripped := stripOpencodeJSONComments(raw)
	var v json.RawMessage
	if err := json.Unmarshal([]byte(stripped), &v); err != nil {
		return nil, err
	}
	return v, nil
}

func stripOpencodeJSONComments(s string) string {
	var b strings.Builder
	inStr := false
	escaped := false
	i := 0
	for i < len(s) {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && (s[i] != '*' || s[i+1] != '/') {
				i++
			}
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

type opencodeFileConfig struct {
	globalRules   []opencodeSkillRule
	agentRulesMap map[string][]opencodeSkillRule
	toolsSkill    bool
	toolsSkillSet bool
	agentTools    map[string]bool
}

func (c opencodeFileConfig) agentRules(agent string) []opencodeSkillRule {
	if agent == "" || c.agentRulesMap == nil {
		return nil
	}
	return c.agentRulesMap[agent]
}

func (c opencodeFileConfig) agentToolsSkillSet(agent string) bool {
	if agent == "" || c.agentTools == nil {
		return false
	}
	_, ok := c.agentTools[agent]
	return ok
}

func (c opencodeFileConfig) agentToolsSkill(agent string) bool {
	return c.agentTools[agent]
}

func parseOpencodeConfig(raw json.RawMessage) (opencodeFileConfig, error) {
	var root map[string]json.RawMessage
	var cfg opencodeFileConfig
	cfg.agentRulesMap = map[string][]opencodeSkillRule{}
	cfg.agentTools = map[string]bool{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return cfg, fmt.Errorf("opencode: invalid config JSON: %w", err)
	}
	if v, ok := root["permission"]; ok {
		rules, err := parseOpencodeSkillPermission(v)
		if err != nil {
			return cfg, err
		}
		cfg.globalRules = rules
	}
	if v, ok := root["tools"]; ok {
		enabled, set := parseOpencodeToolsSkill(v)
		cfg.toolsSkill = enabled
		cfg.toolsSkillSet = set
	}
	if v, ok := root["agent"]; ok {
		var agents map[string]json.RawMessage
		if err := json.Unmarshal(v, &agents); err != nil {
			return cfg, fmt.Errorf("opencode: invalid agent config: %w", err)
		}
		for name, adata := range agents {
			var aroot map[string]json.RawMessage
			if err := json.Unmarshal(adata, &aroot); err != nil {
				return cfg, fmt.Errorf("opencode: invalid agent %q config: %w", name, err)
			}
			if pv, ok := aroot["permission"]; ok {
				rules, err := parseOpencodeSkillPermission(pv)
				if err != nil {
					return cfg, err
				}
				// Agent permission block may itself be {"skill": ...} or a
				// bare skill rule set; parseOpencodeSkillPermission already
				// handles both shapes.
				cfg.agentRulesMap[name] = append(cfg.agentRulesMap[name], rules...)
			}
			if tv, ok := aroot["tools"]; ok {
				enabled, set := parseOpencodeToolsSkill(tv)
				if set {
					cfg.agentTools[name] = enabled
				}
			}
		}
	}
	return cfg, nil
}

// parseOpencodeSkillPermission extracts skill rules from a permission value.
// Shapes (see https://opencode.ai/docs/permissions/):
//   - "allow"|"ask"|"deny" (applies to skill)
//   - {"skill": "allow"|{pattern: action}, "*": action, ...}
//   - {"pattern": action, ...} (agent-level permission block)
func parseOpencodeSkillPermission(raw json.RawMessage) ([]opencodeSkillRule, error) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		act := normalizeOpencodeAction(asString)
		if act == "" {
			return nil, fmt.Errorf("opencode: invalid permission action %q", asString)
		}
		return []opencodeSkillRule{{pattern: "*", action: act}}, nil
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return nil, fmt.Errorf("opencode: invalid permission config: %w", err)
	}
	// Prefer the skill-specific key when present.
	if skillRaw, ok := asMap["skill"]; ok {
		var skillStr string
		if err := json.Unmarshal(skillRaw, &skillStr); err == nil {
			act := normalizeOpencodeAction(skillStr)
			if act == "" {
				return nil, fmt.Errorf("opencode: invalid permission.skill action %q", skillStr)
			}
			return []opencodeSkillRule{{pattern: "*", action: act}}, nil
		}
		var skillMap map[string]string
		if err := json.Unmarshal(skillRaw, &skillMap); err != nil {
			return nil, fmt.Errorf("opencode: invalid permission.skill config")
		}
		return orderedOpencodeRules(skillRaw)
	}
	// Agent-level block: {"skill": ..., ...} already handled above when the
	// map itself contains skill patterns? No: agent permission blocks use
	// tool names as keys, so a bare {"internal-*": "allow"} is ambiguous.
	// Only honor "*" (global default for all tools) and "skill" here; other
	// tool keys (bash/edit/...) must not affect skills.
	if starRaw, ok := asMap["*"]; ok {
		var starStr string
		if err := json.Unmarshal(starRaw, &starStr); err == nil {
			act := normalizeOpencodeAction(starStr)
			if act == "" {
				return nil, fmt.Errorf("opencode: invalid permission action %q", starStr)
			}
			return []opencodeSkillRule{{pattern: "*", action: act}}, nil
		}
	}
	return nil, nil
}

func orderedOpencodeRules(raw json.RawMessage) ([]opencodeSkillRule, error) {
	// Preserve file order for last-match-wins: decode keys in order.
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("opencode: invalid permission.skill config")
	}
	var rules []opencodeSkillRule
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("opencode: invalid permission.skill config")
		}
		pattern, _ := ktok.(string)
		var action string
		if err := dec.Decode(&action); err != nil {
			return nil, fmt.Errorf("opencode: invalid permission.skill action for %q", pattern)
		}
		act := normalizeOpencodeAction(action)
		if act == "" {
			return nil, fmt.Errorf("opencode: invalid permission.skill action %q", action)
		}
		rules = append(rules, opencodeSkillRule{pattern: pattern, action: act})
	}
	return rules, nil
}

func normalizeOpencodeAction(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return "allow"
	case "deny":
		return "deny"
	case "ask":
		return "ask"
	default:
		return ""
	}
}

func parseOpencodeToolsSkill(raw json.RawMessage) (enabled bool, set bool) {
	var asBool bool
	if err := json.Unmarshal(raw, &asBool); err == nil {
		return asBool, false
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return true, false
	}
	if v, ok := asMap["skill"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			return b, true
		}
	}
	return true, false
}
