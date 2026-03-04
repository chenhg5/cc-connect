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
func (r *SkillRegistry) Resolve(name string) *Skill {
	lower := strings.ToLower(name)
	for _, s := range r.ListAll() {
		if strings.ToLower(s.Name) == lower {
			return s
		}
	}
	return nil
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
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillName := entry.Name()
			if seen[strings.ToLower(skillName)] {
				continue
			}

			mdPath := filepath.Join(dir, skillName, "SKILL.md")
			data, err := os.ReadFile(mdPath)
			if err != nil {
				continue
			}

			skill := parseSkillMD(skillName, string(data), dir)
			if skill == nil {
				continue
			}

			seen[strings.ToLower(skillName)] = true
			result = append(result, skill)
			slog.Debug("skill: discovered", "name", skillName, "dir", dir)
		}
	}

	r.cache = result
	return result
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
// Handles quoted and unquoted values. Does not support nested structures.
func parseFrontmatter(block string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
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
