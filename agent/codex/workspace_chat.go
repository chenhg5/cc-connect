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
	"time"

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
	return core.Workspace{}, fmt.Errorf("codex: unknown workspace reference")
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
	url, workDir, codexHome, cliBin := a.appServerURL, a.workDir, a.codexHome, a.cmd
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
	control, err := newAppServerControl(cliBin, url, workDir, model, effort, mode, baseURL, provider, extraEnv, codexHome)
	if err != nil {
		return nil, err
	}
	a.control = control
	return control, nil
}

type nativeThreadWire struct {
	ID        string           `json:"id"`
	Cwd       string           `json:"cwd"`
	Name      string           `json:"name"`
	Preview   string           `json:"preview"`
	Status    json.RawMessage  `json:"status"`
	CreatedAt int64            `json:"createdAt"`
	UpdatedAt int64            `json:"updatedAt"`
	Turns     []nativeTurnWire `json:"turns"`
}

type nativeTurnWire struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	StartedAt   *int64            `json:"startedAt"`
	CompletedAt *int64            `json:"completedAt"`
	DurationMS  *int64            `json:"durationMs"`
	Error       json.RawMessage   `json:"error"`
	Items       []json.RawMessage `json:"items"`
}

func mapNativeThread(thread nativeThreadWire) core.NativeThreadDetail {
	detail := core.NativeThreadDetail{NativeThread: core.NativeThread{
		ID: thread.ID, Cwd: thread.Cwd, Name: thread.Name, Preview: thread.Preview, Status: thread.Status,
		CreatedAt: time.Unix(thread.CreatedAt, 0), UpdatedAt: time.Unix(thread.UpdatedAt, 0),
	}, Turns: make([]core.NativeTurn, 0, len(thread.Turns))}
	for _, turn := range thread.Turns {
		mapped := core.NativeTurn{ID: turn.ID, Status: turn.Status, DurationMS: turn.DurationMS, Error: turn.Error, Items: turn.Items}
		if turn.StartedAt != nil {
			value := time.Unix(*turn.StartedAt, 0)
			mapped.StartedAt = &value
		}
		if turn.CompletedAt != nil {
			value := time.Unix(*turn.CompletedAt, 0)
			mapped.CompletedAt = &value
		}
		detail.Turns = append(detail.Turns, mapped)
	}
	return detail
}

func (a *Agent) ListNativeThreads(ctx context.Context) ([]core.NativeThread, error) {
	control, err := a.appServerControl(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	var all []core.NativeThread
	var cursor any
	for {
		params := map[string]any{"cwd": workDir, "limit": 100, "sortKey": "updated_at", "sortDirection": "desc"}
		if cursor != nil {
			params["cursor"] = cursor
		}
		var response struct {
			Data       []nativeThreadWire `json:"data"`
			NextCursor *string            `json:"nextCursor"`
		}
		if err := control.request("thread/list", params, &response); err != nil {
			return nil, fmt.Errorf("codex: thread/list: %w", err)
		}
		for _, thread := range response.Data {
			mapped := mapNativeThread(thread).NativeThread
			if sameCodexPath(mapped.Cwd, workDir) {
				all = append(all, mapped)
			}
		}
		if response.NextCursor == nil || *response.NextCursor == "" {
			break
		}
		cursor = *response.NextCursor
	}
	return all, nil
}

func (a *Agent) ReadNativeThread(ctx context.Context, threadID string) (core.NativeThreadDetail, error) {
	control, err := a.appServerControl(ctx)
	if err != nil {
		return core.NativeThreadDetail{}, err
	}
	var response struct {
		Thread nativeThreadWire `json:"thread"`
	}
	if err := control.request("thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &response); err != nil {
		return core.NativeThreadDetail{}, fmt.Errorf("codex: thread/read: %w", err)
	}
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	if !sameCodexPath(response.Thread.Cwd, workDir) {
		return core.NativeThreadDetail{}, fmt.Errorf("codex: thread does not belong to work_dir")
	}
	return mapNativeThread(response.Thread), nil
}

func (a *Agent) StartNativeThread(ctx context.Context, name string) (core.NativeThread, error) {
	control, err := a.appServerControl(ctx)
	if err != nil {
		return core.NativeThread{}, err
	}
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	params := control.threadRequestParams()
	params["cwd"] = workDir
	var response struct {
		Thread nativeThreadWire `json:"thread"`
	}
	if err := control.request("thread/start", params, &response); err != nil {
		return core.NativeThread{}, fmt.Errorf("codex: thread/start: %w", err)
	}
	if response.Thread.ID == "" || !sameCodexPath(response.Thread.Cwd, workDir) {
		return core.NativeThread{}, fmt.Errorf("codex: thread/start returned invalid workspace thread")
	}
	if name = strings.TrimSpace(name); name != "" {
		if err := control.request("thread/name/set", map[string]any{"threadId": response.Thread.ID, "name": name}, nil); err != nil {
			return core.NativeThread{}, fmt.Errorf("codex: thread/name/set: %w", err)
		}
		response.Thread.Name = name
	}
	return mapNativeThread(response.Thread).NativeThread, nil
}
