package codex

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Ask the same CLI that runs turns, so its provider, profile, visibility rules,
// and bundled catalog determine the chooser instead of a potentially stale cache.
// This only requests metadata; it never starts a thread or a model turn.
func (a *Agent) loadModelsFromCLI(ctx context.Context) ([]core.ModelOption, error) {
	a.mu.RLock()
	command, workDir, codexHome := a.cmd, a.workDir, a.codexHome
	args := append([]string(nil), a.cliExtraArgs...)
	env := append([]string(nil), a.configEnv...)
	env = append(env, a.providerEnvLocked()...)
	env = append(env, a.sessionEnv...)
	provider, _, _, _ := a.activeProviderCodexConfig()
	var baseURL string
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		baseURL = a.providers[a.activeIdx].BaseURL
	}
	a.mu.RUnlock()
	if provider != "" {
		args = append(args, "-c", fmt.Sprintf("model_provider=%q", provider))
	}
	if baseURL != "" {
		args = append(args, "-c", fmt.Sprintf("openai_base_url=%q", baseURL))
	}
	if codexHome != "" {
		env = append(env, "CODEX_HOME="+codexHome)
	}
	bin, err := resolveCodexExecutable(command)
	if err != nil {
		return nil, fmt.Errorf("model list resolve CLI: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append(args, "app-server")...)
	prepareCmdForKill(cmd)
	cmd.Cancel = func() error { return forceKillCmd(cmd) }
	cmd.Dir = workDir
	cmd.Env = core.MergeEnv(os.Environ(), env)
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("model list stdin: %w", err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("model list stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("model list start app-server: %w", err)
	}
	defer func() {
		_ = forceKillCmd(cmd)
		_ = cmd.Wait()
	}()
	stopClose := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopClose()
	reader := bufio.NewReader(stdout)
	if err := rpcRequestOverIO(stdin, reader, 1, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name": "cc-connect-model-list", "version": "0.1.0",
		},
	}, nil); err != nil {
		return nil, err
	}
	if err := rpcNotifyOverIO(stdin, "initialized", map[string]any{}); err != nil {
		return nil, err
	}
	return readNativeModelPages(stdin, reader)
}

func readNativeModelPages(stdin io.Writer, reader *bufio.Reader) ([]core.ModelOption, error) {
	var models []core.ModelOption
	seenModels, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for id := int64(2); ; id++ {
		params := map[string]any{"includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data []struct {
				Model       string `json:"model"`
				ID          string `json:"id"`
				Description string `json:"description"`
				Hidden      bool   `json:"hidden"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := rpcRequestOverIO(stdin, reader, id, "model/list", params, &page); err != nil {
			return nil, err
		}
		for _, model := range page.Data {
			name := strings.TrimSpace(model.Model)
			if name == "" {
				name = strings.TrimSpace(model.ID)
			}
			if name == "" || model.Hidden || seenModels[name] {
				continue
			}
			seenModels[name] = true
			models = append(models, core.ModelOption{Name: name, Desc: strings.TrimSpace(model.Description)})
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return models, nil
		}
		cursor = *page.NextCursor
		if seenCursors[cursor] {
			return nil, fmt.Errorf("model list repeated pagination cursor")
		}
		seenCursors[cursor] = true
	}
}
