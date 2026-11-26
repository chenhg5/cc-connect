package claudecode

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.AutoMemoryProvider = (*Agent)(nil)

// AutoMemoryDir returns the Claude Code project memory directory for this agent.
func (a *Agent) AutoMemoryDir() string {
	const configKey = "CLAUDE_CONFIG_DIR"
	const disableKey = "CLAUDE_CODE_DISABLE_AUTO_MEMORY"

	a.mu.RLock()
	isolation := a.spawnOpts.IsolationMode()
	workDir := a.workDir
	configEnv := append([]string(nil), a.configEnv...)
	sessionEnv := append([]string(nil), a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		// Read only the settings we need. providerEnvLocked also manages a proxy.
		env := a.providers[a.activeIdx].Env
		configEnv = append(configEnv, configKey+"="+env[configKey], disableKey+"="+env[disableKey])
	}
	a.mu.RUnlock()
	if isolation || strings.TrimSpace(workDir) == "" {
		return ""
	}

	env := append(configEnv, sessionEnv...)
	disabled := autoMemoryEnv(env, disableKey)
	if disabled == "1" || strings.EqualFold(disabled, "true") {
		return ""
	}
	configHome := autoMemoryEnv(env, configKey)
	if configHome == "" {
		configHome = claudeConfigHomeDir()
	}
	if configHome == "" {
		return ""
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}
	var resolvedWorkDir string
	if resolved, err := filepath.EvalSymlinks(absWorkDir); err == nil && resolved != absWorkDir {
		resolvedWorkDir = resolved
	}
	roots := resolveMainRepoRoot(absWorkDir)
	candidates := append([]string{absWorkDir, resolvedWorkDir}, roots...)
	if resolvedWorkDir != "" {
		resolvedRoots := resolveMainRepoRoot(resolvedWorkDir)
		candidates = append(candidates, resolvedRoots...)
		if len(roots) == 0 {
			roots = resolvedRoots
		}
	}
	canonical := absWorkDir
	if len(roots) != 0 {
		canonical = roots[len(roots)-1]
	}

	settings := readAutoMemorySettings(configHome, canonical, absWorkDir)
	if settings.AutoMemoryEnabled != nil && !*settings.AutoMemoryEnabled {
		return ""
	}
	if dir := settings.AutoMemoryDirectory; dir != "" {
		if dir == "~" || strings.HasPrefix(dir, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			dir = filepath.Join(home, strings.TrimPrefix(dir[1:], "/"))
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(absWorkDir, dir)
		}
		return filepath.Clean(dir)
	}
	return projectMemoryDir(configHome, canonical, uniqueSkillDirs(candidates))
}

func autoMemoryEnv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], key+"="); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(os.Getenv(key))
}

// resolveMainRepoRoot returns the worktree root followed by its main repository
// root when a linked worktree points to a shared Git directory.
func resolveMainRepoRoot(dir string) []string {
	root := findGitRoot(dir)
	if root == "" {
		return nil
	}
	roots := []string{root}
	dotGit := filepath.Join(root, ".git")
	if info, err := os.Stat(dotGit); err != nil || !info.Mode().IsRegular() {
		return roots
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return roots
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	gitDir = strings.TrimSpace(gitDir)
	if !ok || gitDir == "" {
		return roots
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	var commonDir string
	data, err = os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err == nil {
		commonDir = strings.TrimSpace(string(data))
		if commonDir != "" && !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	} else if os.IsNotExist(err) {
		if parent := filepath.Dir(gitDir); filepath.Base(parent) == "worktrees" {
			commonDir = filepath.Dir(parent)
		}
	}
	if commonDir != "" {
		if mainRoot := filepath.Dir(filepath.Clean(commonDir)); mainRoot != root {
			roots = append(roots, mainRoot)
		}
	}
	return roots
}

type autoMemorySettings struct {
	AutoMemoryEnabled   *bool  `json:"autoMemoryEnabled"`
	AutoMemoryDirectory string `json:"autoMemoryDirectory"`
}

func readAutoMemorySettings(configHome, projectRoot, workDir string) autoMemorySettings {
	var merged autoMemorySettings
	apply := func(path string, allowDirectory bool) {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return
		}
		var settings autoMemorySettings
		if err == nil {
			err = json.Unmarshal(stripJSONC(data), &settings)
		}
		if err != nil {
			slog.Warn("claudecode: skipping auto-memory settings", "path", path, "error", err)
			return
		}
		if settings.AutoMemoryEnabled != nil {
			merged.AutoMemoryEnabled = settings.AutoMemoryEnabled
		}
		if allowDirectory && settings.AutoMemoryDirectory != "" {
			merged.AutoMemoryDirectory = settings.AutoMemoryDirectory
		}
	}
	apply(filepath.Join(configHome, "settings.json"), true)
	apply(filepath.Join(configHome, "settings.local.json"), true)
	for _, dir := range uniqueSkillDirs([]string{projectRoot, workDir}) {
		apply(filepath.Join(dir, ".claude", "settings.json"), false)
		apply(filepath.Join(dir, ".claude", "settings.local.json"), true)
	}
	return merged
}

func projectMemoryDir(configHome, canonical string, candidates []string) string {
	base := filepath.Join(configHome, "projects")
	var keys, legacy []string
	slashes := strings.NewReplacer("/", "-", "\\", "-")
	for _, dir := range candidates {
		keys = append(keys, encodeClaudeProjectKey(dir))
		forwardKey := slashes.Replace(dir)
		key := strings.ReplaceAll(forwardKey, ":", "-")
		legacy = append(legacy,
			strings.ReplaceAll(dir, string(filepath.Separator), "-"),
			key, strings.ReplaceAll(key, "_", "-"), forwardKey,
		)
	}
	// Exact keys precede every legacy spelling. Remember the first existing
	// project, but prefer any existing memory directory over an empty project.
	var firstProjectMemory string
	for _, key := range uniqueSkillDirs(append(keys, legacy...)) {
		project := filepath.Join(base, key)
		memory := filepath.Join(project, "memory")
		if info, err := os.Stat(memory); err == nil && info.IsDir() {
			return memory
		}
		if firstProjectMemory == "" {
			if info, err := os.Stat(project); err == nil && info.IsDir() {
				firstProjectMemory = memory
			}
		}
	}
	if firstProjectMemory != "" {
		return firstProjectMemory
	}
	return filepath.Join(base, encodeClaudeProjectKey(canonical), "memory")
}
