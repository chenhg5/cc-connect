package grok

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

type grokConfig struct {
	Skills struct {
		Paths    []string `toml:"paths"`
		Ignore   []string `toml:"ignore"`
		Disabled []string `toml:"disabled"`
	} `toml:"skills"`
	Compat struct {
		Claude struct {
			Skills *bool `toml:"skills"`
		} `toml:"claude"`
		Cursor struct {
			Skills *bool `toml:"skills"`
		} `toml:"cursor"`
	} `toml:"compat"`
}

func grokCapabilityDirs(workDir string, effectiveEnv []string, kind string) []string {
	workDir = normalizeGrokCapabilityWorkDir(workDir)
	processEnv := grokProcessEnv(effectiveEnv)
	grokHome := resolveGrokHome(effectiveEnv, workDir)
	return grokCapabilityDirsWithConfig(workDir, processEnv, grokHome, kind, readGrokConfig(grokHome))
}

func normalizeGrokCapabilityWorkDir(workDir string) string {
	if absWorkDir, err := filepath.Abs(workDir); err == nil {
		workDir = absWorkDir
	}
	workDir = filepath.Clean(workDir)
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		return filepath.Clean(resolved)
	}
	return workDir
}

func grokCapabilityDirsWithConfig(workDir string, processEnv []string, grokHome, kind string, config grokConfig) []string {
	claudeEnabled, cursorEnabled := grokCompatSkills(processEnv, config)

	var dirs []string
	for _, level := range grokProjectLevels(workDir) {
		dirs = append(dirs,
			filepath.Join(level, ".grok", kind),
			filepath.Join(level, ".agents", kind),
		)
		if claudeEnabled {
			dirs = append(dirs, filepath.Join(level, ".claude", kind))
		}
		if cursorEnabled {
			dirs = append(dirs, filepath.Join(level, ".cursor", kind))
		}
	}

	if grokHome != "" {
		dirs = append(dirs, filepath.Join(grokHome, kind))
	}
	if userHome := grokUserHome(processEnv); userHome != "" {
		userHome = resolveGrokPath(userHome, workDir)
		dirs = append(dirs, filepath.Join(userHome, ".agents", kind))
		if claudeEnabled {
			dirs = append(dirs, filepath.Join(userHome, ".claude", kind))
		}
		if cursorEnabled {
			dirs = append(dirs, filepath.Join(userHome, ".cursor", kind))
		}
	}
	return uniqueGrokDirs(dirs)
}

func grokProjectLevels(workDir string) []string {
	repoRoot := grokGitRoot(workDir)
	if repoRoot == "" {
		return []string{workDir}
	}

	var levels []string
	for current := workDir; ; current = filepath.Dir(current) {
		levels = append(levels, current)
		if current == repoRoot {
			return levels
		}
		parent := filepath.Dir(current)
		if parent == current {
			return levels
		}
	}
}

func grokGitRoot(start string) string {
	for current := filepath.Clean(start); ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
	}
}

func readGrokConfig(grokHome string) grokConfig {
	var config grokConfig
	if grokHome == "" {
		return config
	}
	if _, err := toml.DecodeFile(filepath.Join(grokHome, "config.toml"), &config); err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("grok: config could not be read", "error", err)
		}
		return grokConfig{}
	}
	return config
}

func grokCompatSkills(processEnv []string, config grokConfig) (bool, bool) {
	claudeEnabled := true
	cursorEnabled := true
	if config.Compat.Claude.Skills != nil {
		claudeEnabled = *config.Compat.Claude.Skills
	}
	if config.Compat.Cursor.Skills != nil {
		cursorEnabled = *config.Compat.Cursor.Skills
	}
	claudeEnabled = grokCompatEnvBool(processEnv, "GROK_CLAUDE_SKILLS_ENABLED", claudeEnabled)
	cursorEnabled = grokCompatEnvBool(processEnv, "GROK_CURSOR_SKILLS_ENABLED", cursorEnabled)
	return claudeEnabled, cursorEnabled
}

func grokCompatEnvBool(env []string, name string, fallback bool) bool {
	raw, ok := grokEnvValue(env, name)
	if !ok {
		return fallback
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

func grokSkillDiscoveryConfig(workDir string, effectiveEnv []string) core.SkillDiscoveryConfig {
	workDir = normalizeGrokCapabilityWorkDir(workDir)
	processEnv := grokProcessEnv(effectiveEnv)
	grokHome := resolveGrokHome(effectiveEnv, workDir)
	config := readGrokConfig(grokHome)
	return core.SkillDiscoveryConfig{
		Dirs:          grokCapabilityDirsWithConfig(workDir, processEnv, grokHome, "skills", config),
		Paths:         resolveGrokConfiguredPaths(config.Skills.Paths, processEnv, workDir),
		IgnorePaths:   resolveGrokConfiguredPaths(config.Skills.Ignore, processEnv, workDir),
		DisabledNames: append([]string(nil), config.Skills.Disabled...),
	}
}

func resolveGrokConfiguredPaths(paths, processEnv []string, workDir string) []string {
	userHome := ""
	if finalHome, ok := grokEnvValue(processEnv, "HOME"); ok {
		userHome = strings.TrimSpace(finalHome)
	} else {
		userHome = grokUserHome(processEnv)
	}
	if userHome != "" {
		userHome = resolveGrokPath(userHome, workDir)
	}

	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		switch {
		case path == "~":
			if userHome == "" {
				continue
			}
			path = userHome
		case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`):
			if userHome == "" {
				continue
			}
			path = filepath.Join(userHome, filepath.FromSlash(strings.ReplaceAll(path[2:], `\`, "/")))
		default:
			path = resolveGrokPath(path, workDir)
		}
		if canonical, err := filepath.EvalSymlinks(path); err == nil {
			path = canonical
		}
		resolved = append(resolved, filepath.Clean(path))
	}
	return uniqueGrokDirs(resolved)
}

func uniqueGrokDirs(paths []string) []string {
	dirs := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		path = filepath.Clean(path)
		duplicate := false
		for _, existing := range dirs {
			if path == existing || sameGrokWorkDir(path, existing) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dirs = append(dirs, path)
		}
	}
	return dirs
}
