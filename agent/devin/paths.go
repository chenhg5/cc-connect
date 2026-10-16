package devin

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectMemoryFile is <work_dir>/AGENTS.md. Devin has no native system
// prompt channel, so the engine writes relay instructions here.
func (a *Agent) ProjectMemoryFile() string {
	return filepath.Join(a.absWorkDir(), "AGENTS.md")
}

// GlobalMemoryFile is $XDG_CONFIG_HOME/devin/AGENTS.md. The agent env
// option wins over the process environment; otherwise ~/.config.
func (a *Agent) GlobalMemoryFile() string {
	home := a.configHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "devin", "AGENTS.md")
}

// SkillDirs returns the project and global skill directories that exist.
// Project dirs are under the absolute work dir: .devin/skills,
// .agents/skills, .windsurf/skills. Global dirs are
// $XDG_CONFIG_HOME/devin/skills and ~/.agents/skills.
func (a *Agent) SkillDirs() []string {
	root := a.absWorkDir()
	candidates := []string{
		filepath.Join(root, ".devin", "skills"),
		filepath.Join(root, ".agents", "skills"),
		filepath.Join(root, ".windsurf", "skills"),
	}
	if home := a.configHome(); home != "" {
		candidates = append(candidates, filepath.Join(home, "devin", "skills"))
	}
	if user, err := os.UserHomeDir(); err == nil && user != "" {
		candidates = append(candidates, filepath.Join(user, ".agents", "skills"))
	}
	return existingDirs(candidates)
}

func (a *Agent) absWorkDir() string {
	dir := ""
	if a != nil && a.Agent != nil {
		dir = a.GetWorkDir()
	}
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		return resolved
	}
	return abs
}

func (a *Agent) configHome() string {
	if v := strings.TrimSpace(a.envValue("XDG_CONFIG_HOME")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" {
		return v
	}
	user, err := os.UserHomeDir()
	if err != nil || user == "" {
		return ""
	}
	return filepath.Join(user, ".config")
}

func (a *Agent) envValue(key string) string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.env[key]
}

func existingDirs(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	var out []string
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}
