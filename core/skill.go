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
	mu   sync.RWMutex
	dirs []string
	// cached results; nil means not yet scanned
	cache []*Skill
}

func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{}
}

// SetDirs configures which directories to scan for skills.
func (r *SkillRegistry) SetDirs(dirs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs = dirs
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

	for _, dir := range r.dirs {
		for _, skill := range discoverSkillsInDir(dir, seen) {
			result = append(result, skill)
		}
	}

	r.cache = result
	return result
}

func discoverSkillsInDir(root string, seen map[string]bool) []*Skill {
	var result []*Skill
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}

		skillDir := filepath.Dir(path)
		if sameFilePath(skillDir, root) {
			return nil
		}

		skillName := filepath.Base(skillDir)
		if seen[strings.ToLower(skillName)] {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		skill := parseSkillMD(skillName, string(data), skillDir)
		if skill == nil {
			return nil
		}

		seen[strings.ToLower(skillName)] = true
		result = append(result, skill)
		slog.Debug("skill: discovered", "name", skillName, "dir", skillDir)
		return nil
	})
	if err != nil {
		return result
	}
	return result
}

func sameFilePath(a, b string) bool {
	aClean := filepath.Clean(a)
	bClean := filepath.Clean(b)
	return aClean == bClean
}

// Invalidate clears the cache so skills are re-scanned on next access.
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
