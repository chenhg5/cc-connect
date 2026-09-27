package codex

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/chenhg5/cc-connect/core"
)

// ListSkills asks the same CLI that executes turns for its enabled catalog.
// No thread or model turn is created. In particular, cached plugin packages
// and skill repositories are not evidence that their skills are enabled.
func (a *Agent) ListSkills(ctx context.Context) ([]*core.Skill, error) {
	a.mu.RLock()
	bin, workDir, codexHome := a.cmd, a.workDir, a.codexHome
	args := append([]string(nil), a.cliExtraArgs...)
	env := append([]string(nil), a.configEnv...)
	env = append(env, a.providerEnvLocked()...)
	env = append(env, a.sessionEnv...)
	a.mu.RUnlock()
	if bin == "" {
		bin = "codex"
	}
	if codexHome != "" {
		env = append(env, "CODEX_HOME="+codexHome)
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("codex: skills work directory: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, append(args, "app-server")...)
	cmd.Dir = absDir
	cmd.Env = core.MergeEnv(os.Environ(), env)
	prepareCmdForKill(cmd)
	cmd.Cancel = func() error { return forceKillCmd(cmd) }
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: skills stdin: %w", err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: skills stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex: skills app-server: %w", err)
	}
	defer func() {
		if err := forceKillCmd(cmd); err != nil {
			slog.Warn("codex: stop skills app-server", "error", err)
		}
		_ = cmd.Wait() // The metadata-only process is deliberately terminated.
	}()
	reader := bufio.NewReader(stdout)
	if err := rpcRequestOverIO(stdin, reader, 1, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "cc-connect-skills", "version": "1"},
	}, nil); err != nil {
		return nil, fmt.Errorf("codex: skills initialize: %w", err)
	}
	if err := rpcNotifyOverIO(stdin, "initialized", map[string]any{}); err != nil {
		return nil, fmt.Errorf("codex: skills initialized: %w", err)
	}
	var result codexSkillsResponse
	if err := rpcRequestOverIO(stdin, reader, 2, "skills/list", map[string]any{
		"cwds": []string{absDir}, "forceReload": true,
	}, &result); err != nil {
		return nil, fmt.Errorf("codex: list skills: %w", err)
	}
	return result.load(absDir)
}

type codexSkillsResponse struct {
	Data []struct {
		Cwd    string `json:"cwd"`
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Path        string `json:"path"`
			Enabled     bool   `json:"enabled"`
		} `json:"skills"`
		Errors []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"data"`
}

func (r codexSkillsResponse) load(workDir string) ([]*core.Skill, error) {
	for _, entry := range r.Data {
		if !sameCodexPath(entry.Cwd, workDir) {
			continue
		}
		if len(entry.Errors) > 0 {
			return nil, fmt.Errorf("codex: skill discovery reported %d error(s) in %s", len(entry.Errors), workDir)
		}
		skills := make([]*core.Skill, 0, len(entry.Skills))
		for _, item := range entry.Skills {
			if !item.Enabled {
				continue
			}
			if !filepath.IsAbs(item.Path) || item.Name == "" {
				return nil, fmt.Errorf("codex: invalid skill metadata in %s", workDir)
			}
			skill, err := core.LoadSkillFile(item.Path)
			if err != nil {
				return nil, fmt.Errorf("codex: load skill: %w", err)
			}
			// Preserve native plugin namespaces instead of merging unrelated
			// plugins whose skill directories happen to have the same name.
			skill.Name = item.Name
			skill.Description = item.Description
			skills = append(skills, skill)
		}
		return skills, nil
	}
	return nil, fmt.Errorf("codex: skills response missing workspace %s", workDir)
}
