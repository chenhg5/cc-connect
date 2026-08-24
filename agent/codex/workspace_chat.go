package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type codexAppState struct {
	LocalProjects map[string]codexAppProject `json:"local-projects"`
	ProjectOrder  []string                   `json:"project-order"`
}

type codexAppProject struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	RootPaths []string `json:"rootPaths"`
}

func workspaceRef(projectID string, rootIndex int, rootPath string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + fmt.Sprint(rootIndex) + "\x00" + filepath.Clean(rootPath)))
	return "ws_" + hex.EncodeToString(sum[:16])
}

func (a *Agent) appStatePath() (string, error) {
	a.mu.RLock()
	explicit := strings.TrimSpace(a.codexHome)
	a.mu.RUnlock()
	home := resolveCodexHomeDir(explicit)
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("codex: cannot resolve CODEX_HOME")
	}
	return filepath.Join(home, ".codex-global-state.json"), nil
}

func (a *Agent) readWorkspaces() ([]core.Workspace, error) {
	path, err := a.appStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("codex: read app state %s: %w", path, err)
	}
	var state codexAppState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("codex: parse app state %s: %w", path, err)
	}
	if state.LocalProjects == nil || state.ProjectOrder == nil {
		return nil, fmt.Errorf("codex: app state must contain local-projects and project-order")
	}
	orderedIDs := append([]string(nil), state.ProjectOrder...)
	seen := make(map[string]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("codex: app state project-order contains duplicate %q", id)
		}
		seen[id] = struct{}{}
		if _, exists := state.LocalProjects[id]; !exists {
			return nil, fmt.Errorf("codex: app state project-order references missing project %q", id)
		}
	}
	var remaining []string
	for id := range state.LocalProjects {
		if _, exists := seen[id]; !exists {
			remaining = append(remaining, id)
		}
	}
	sort.Strings(remaining)
	orderedIDs = append(orderedIDs, remaining...)

	workspaces := make([]core.Workspace, 0, len(state.LocalProjects))
	order := 0
	for _, id := range orderedIDs {
		project := state.LocalProjects[id]
		if strings.TrimSpace(project.ID) == "" || project.ID != id {
			return nil, fmt.Errorf("codex: app project %q has invalid id %q", id, project.ID)
		}
		if strings.TrimSpace(project.Name) == "" || len(project.RootPaths) == 0 {
			return nil, fmt.Errorf("codex: app project %q must have name and rootPaths", id)
		}
		for rootIndex, rawRoot := range project.RootPaths {
			root := filepath.Clean(strings.TrimSpace(rawRoot))
			if !filepath.IsAbs(root) {
				return nil, fmt.Errorf("codex: app project %q root %q is not absolute", id, rawRoot)
			}
			workspace := core.Workspace{
				Ref: workspaceRef(id, rootIndex, root), ProjectID: id, ProjectName: project.Name,
				RootIndex: rootIndex, RootName: filepath.Base(root), RootPath: root, Available: true, Order: order,
			}
			order++
			info, statErr := os.Stat(root)
			switch {
			case statErr != nil:
				workspace.Available = false
				workspace.Error = statErr.Error()
			case !info.IsDir():
				workspace.Available = false
				workspace.Error = "path is not a directory"
			}
			workspaces = append(workspaces, workspace)
		}
	}
	return workspaces, nil
}

func (a *Agent) ListWorkspaces(ctx context.Context) ([]core.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.readWorkspaces()
}

func (a *Agent) ResolveWorkspace(ctx context.Context, ref string) (core.Workspace, error) {
	workspaces, err := a.ListWorkspaces(ctx)
	if err != nil {
		return core.Workspace{}, err
	}
	for _, workspace := range workspaces {
		if workspace.Ref == ref {
			return workspace, nil
		}
	}
	return core.Workspace{}, fmt.Errorf("codex: unknown workspace reference: %w", core.ErrWorkspaceNotFound)
}

func (a *Agent) ValidateWorkspaceAccess(ctx context.Context, workspace core.Workspace) error {
	_, _, err := a.validateNativeWorkspace(ctx, workspace)
	return err
}

func (a *Agent) ValidateNativeThreadAccess(ctx context.Context, workspace core.Workspace, thread core.NativeThread) error {
	_, cwd, err := a.validateNativeWorkspace(ctx, workspace)
	if err != nil {
		return err
	}
	threadCwd, err := canonicalNativePath(thread.Cwd)
	if err != nil || threadCwd != cwd || strings.TrimSpace(thread.ID) == "" {
		return fmt.Errorf("codex: native thread does not belong to workspace")
	}
	return nil
}

func (a *Agent) appServerControl(ctx context.Context) (*appServerSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.control != nil && a.control.Alive() {
		return a.control, nil
	}
	if a.control != nil {
		_ = a.control.Close()
		a.control = nil
	}
	a.mu.Lock()
	mode, model, effort := a.mode, a.model, a.reasoningEffort
	workDir, codexHome, cliBin := a.workDir, a.codexHome, a.cmd
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	var baseURL string
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
		baseURL = a.providers[a.activeIdx].BaseURL
	}
	provider, _, _, _ := a.activeProviderCodexConfig()
	backend := a.backend
	a.mu.Unlock()
	if backend != "app_server" {
		return nil, fmt.Errorf("codex: native threads require backend=app_server")
	}
	control, err := newAppServerControl(cliBin, workDir, model, effort, mode, baseURL, provider, extraEnv, codexHome)
	if err != nil {
		return nil, err
	}
	a.control = control
	return control, nil
}
