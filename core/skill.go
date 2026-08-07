package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Skill represents an agent skill discovered from a SKILL.md file.
type Skill struct {
	Name        string // skill name (= subdirectory name)
	DisplayName string // optional display name from frontmatter
	Description string // from frontmatter or first line of content
	Prompt      string // the instruction content (body after frontmatter)
	Source      string // directory path where this skill was found
}

// SkillRegistry discovers and caches agent skills from skill directories.
// Skills are project-level: each Engine has its own SkillRegistry.
type SkillRegistry struct {
	mu            sync.RWMutex
	dirs          []string
	paths         []string
	ignorePaths   []string
	disabledNames []string
	// cached results; nil means not yet scanned
	cache []*Skill
}

func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{}
}

// SetDirs configures which directories to scan for skills.
func (r *SkillRegistry) SetDirs(dirs []string) {
	r.SetDiscoveryConfig(SkillDiscoveryConfig{Dirs: dirs})
}

// SetDiscoveryConfig configures conventional roots, recursive paths, and
// filters for skill discovery.
func (r *SkillRegistry) SetDiscoveryConfig(config SkillDiscoveryConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs = append([]string(nil), config.Dirs...)
	r.paths = append([]string(nil), config.Paths...)
	r.ignorePaths = append([]string(nil), config.IgnorePaths...)
	r.disabledNames = append([]string(nil), config.DisabledNames...)
	r.cache = nil
}

// Resolve looks up a skill by name. Returns nil if not found.
// Hyphens and underscores are treated as equivalent so that Telegram-sanitized
// names (e.g. "calendar_scheduler") match original skill names ("calendar-scheduler").
func (r *SkillRegistry) Resolve(name string) *Skill {
	norm := normalizeCommandName(name)
	for _, s := range r.ListAll() {
		if normalizeCommandName(s.Name) == norm {
			return s
		}
	}
	return nil
}

// normalizeCommandName folds case and treats hyphens/underscores as equivalent.
func normalizeCommandName(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", "_"))
}

// ListAll returns all discovered skills. Results are cached after first scan.
func (r *SkillRegistry) ListAll() []*Skill {
	r.mu.RLock()
	if r.cache != nil {
		defer r.mu.RUnlock()
		return r.cache
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// double-check after acquiring write lock
	if r.cache != nil {
		return r.cache
	}

	var result []*Skill
	seen := make(map[string]bool)
	ignorePaths := make([]string, 0, len(r.ignorePaths))
	for _, path := range r.ignorePaths {
		if strings.TrimSpace(path) != "" {
			ignorePaths = append(ignorePaths, canonicalDiscoveryPath(path))
		}
	}
	disabled := make(map[string]bool, len(r.disabledNames))
	for _, name := range r.disabledNames {
		if name != "" {
			disabled[name] = true
		}
	}

	for _, dir := range r.dirs {
		result = append(result, discoverSkillsInDir(dir, seen, ignorePaths, disabled)...)
	}
	for _, path := range r.paths {
		result = append(result, discoverSkillsInPath(path, seen, ignorePaths, disabled)...)
	}

	r.cache = result
	return result
}

// discoverSkillsInDir scans a single skill root directory for immediate
// subdirectories that contain a SKILL.md file. Per the Claude Code CLI
// convention (issue #1304), only depth-1 layout is recognised:
//
//	<root>/<skill-name>/SKILL.md        — registered
//	<root>/<skill-name>/references/...  — asset, NOT registered
//
// Nested SKILL.md files inside a skill (e.g. example templates shipped
// alongside the real one) are treated as skill assets and ignored, so they
// cannot leak into platform command menus as phantom slash commands.
func discoverSkillsInDir(scanRoot string, seen map[string]bool, ignorePaths []string, disabled map[string]bool) []*Skill {
	entries, err := os.ReadDir(scanRoot)
	if err != nil {
		return nil
	}

	var result []*Skill
	for _, entry := range entries {
		if !isDiscoverableSubdir(scanRoot, entry) {
			continue
		}

		skillName := entry.Name()
		if seen[strings.ToLower(skillName)] {
			continue
		}

		skillDir := filepath.Join(scanRoot, skillName)
		skillFile := filepath.Join(skillDir, "SKILL.md")
		skill := discoverSkillFile(skillFile, seen, ignorePaths, disabled)
		if skill == nil {
			// No SKILL.md at the top of this subdir — and we deliberately
			// do NOT recurse, matching the Claude Code CLI rule.
			continue
		}
		result = append(result, skill)
	}

	return result
}

// discoverSkillsInPath recursively searches a configured directory, or reads
// a configured SKILL.md file directly.
func discoverSkillsInPath(scanPath string, seen map[string]bool, ignorePaths []string, disabled map[string]bool) []*Skill {
	info, err := os.Stat(scanPath)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		if filepath.Base(scanPath) != "SKILL.md" {
			return nil
		}
		if skill := discoverSkillFile(scanPath, seen, ignorePaths, disabled); skill != nil {
			return []*Skill{skill}
		}
		return nil
	}

	walkRoot := scanPath
	if resolved, err := filepath.EvalSymlinks(scanPath); err == nil {
		walkRoot = resolved
	}
	var result []*Skill
	_ = filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if discoveryPathIgnored(path, ignorePaths) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		if skill := discoverSkillFile(path, seen, ignorePaths, disabled); skill != nil {
			result = append(result, skill)
		}
		return nil
	})
	return result
}

func discoverSkillFile(skillFile string, seen map[string]bool, ignorePaths []string, disabled map[string]bool) *Skill {
	skillDir := filepath.Dir(skillFile)
	skillName := filepath.Base(skillDir)
	if seen[strings.ToLower(skillName)] {
		return nil
	}
	if discoveryPathIgnored(skillDir, ignorePaths) || discoveryPathIgnored(skillFile, ignorePaths) {
		return nil
	}

	data, err := os.ReadFile(skillFile)
	if err != nil {
		return nil
	}
	skill := parseSkillMD(skillName, string(data), skillDir)
	if skill == nil {
		return nil
	}
	if disabled[skill.Name] || disabled[skill.DisplayName] {
		return nil
	}

	seen[strings.ToLower(skillName)] = true
	slog.Debug("skill: discovered", "name", skillName, "dir", skillDir)
	return skill
}

func discoveryPathIgnored(path string, ignorePaths []string) bool {
	if len(ignorePaths) == 0 {
		return false
	}
	path = canonicalDiscoveryPath(path)
	for _, ignored := range ignorePaths {
		rel, err := filepath.Rel(ignored, path)
		if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))) {
			return true
		}
	}
	return false
}

func canonicalDiscoveryPath(path string) string {
	path = filepath.Clean(path)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path
}

// isDiscoverableSubdir reports whether entry should be considered as a
// potential skill subdirectory of parentDir. Accepts real directories and
// symlinks that resolve to directories.
func isDiscoverableSubdir(parentDir string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parentDir, entry.Name()))
	return err == nil && info.IsDir()
}

// Dirs returns the configured skill directories.
func (r *SkillRegistry) Dirs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.dirs...)
}

// Invalidate clears the cache so skills will be re-scanned on next access.
func (r *SkillRegistry) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = nil
}

// parseSkillMD parses a SKILL.md file with optional YAML frontmatter.
//
// Format:
//
//	---
//	description: Short description
//	name: Display Name
//	---
//	Prompt/instruction content here...
func parseSkillMD(skillName, raw, sourceDir string) *Skill {
	content := strings.TrimSpace(raw)
	if content == "" {
		return nil
	}

	var frontmatter map[string]string
	body := content

	if strings.HasPrefix(content, "---") {
		rest := content[3:]
		endIdx := strings.Index(rest, "\n---")
		if endIdx >= 0 {
			fmBlock := rest[:endIdx]
			body = strings.TrimSpace(rest[endIdx+4:])
			frontmatter = parseFrontmatter(fmBlock)
		}
	}

	if body == "" {
		return nil
	}

	description := ""
	displayName := ""
	if frontmatter != nil {
		description = frontmatter["description"]
		displayName = frontmatter["name"]
	}

	if description == "" {
		first, _, _ := strings.Cut(body, "\n")
		first = strings.TrimSpace(first)
		if len([]rune(first)) > 80 {
			first = string([]rune(first)[:80]) + "..."
		}
		description = first
	}

	return &Skill{
		Name:        skillName,
		DisplayName: displayName,
		Description: description,
		Prompt:      body,
		Source:      sourceDir,
	}
}

// parseFrontmatter extracts simple key: value pairs from a YAML-like block.
// Handles quoted values, and YAML block scalar indicators (>-, |-, >, |)
// by reading the following indented lines as the value.
func parseFrontmatter(block string) map[string]string {
	m := make(map[string]string)
	lines := strings.Split(block, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		// Handle YAML block scalar indicators: >-, |-, >, |
		if val == ">-" || val == "|-" || val == ">" || val == "|" {
			var blockLines []string
			for i+1 < len(lines) {
				next := lines[i+1]
				// Block continues while lines are indented (start with space/tab)
				if len(next) == 0 || (next[0] != ' ' && next[0] != '\t') {
					break
				}
				i++
				blockLines = append(blockLines, strings.TrimSpace(next))
			}
			val = strings.Join(blockLines, " ")
		}

		val = strings.Trim(val, `"'`)
		if key != "" {
			m[strings.ToLower(key)] = val
		}
	}
	return m
}

// BuildSkillInvocationPrompt constructs the message sent to the agent when
// a user invokes a skill. Instead of raw prompt expansion, we instruct the
// agent to execute the skill.
func BuildSkillInvocationPrompt(skill *Skill, args []string) string {
	var sb strings.Builder

	sb.WriteString("The user is asking you to execute the following skill.\n\n")

	name := skill.DisplayName
	if name == "" {
		name = skill.Name
	}
	fmt.Fprintf(&sb, "## Skill: %s\n", name)

	if skill.Description != "" {
		fmt.Fprintf(&sb, "## Description: %s\n", skill.Description)
	}

	sb.WriteString("\n## Skill Instructions:\n")
	sb.WriteString(skill.Prompt)

	if len(args) > 0 {
		sb.WriteString("\n\n## User Arguments:\n")
		sb.WriteString(strings.Join(args, " "))
	}

	sb.WriteString("\n\nPlease follow the skill instructions above to complete the task.")
	return sb.String()
}
