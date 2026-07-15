package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	workspaceDependenciesToolName        = "load_workspace_dependencies"
	workspaceDependenciesToolDescription = "Locate the configured bundled workspace dependency runtime paths for this local Codex thread, including Node.js, Python, and useful libraries for working with spreadsheets, slide decks, Word documents, and PDFs. This is read-only and takes no arguments."
)

type workspaceDependenciesConfig struct {
	Enabled     bool
	RuntimeRoot string
}

type appServerDynamicToolCallParams struct {
	ThreadID  string  `json:"threadId"`
	TurnID    string  `json:"turnId"`
	CallID    string  `json:"callId"`
	Namespace *string `json:"namespace"`
	Tool      string  `json:"tool"`
	Arguments any     `json:"arguments"`
}

type workspaceDependenciesManifest struct {
	BundleVersion       string `json:"bundleVersion"`
	ArtifactToolVersion string `json:"artifactToolVersion"`
}

func workspaceDependenciesDynamicTools() []map[string]any {
	return []map[string]any{{
		"type":        "function",
		"name":        workspaceDependenciesToolName,
		"description": workspaceDependenciesToolDescription,
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}}
}

func isWorkspaceDependenciesTool(namespace *string, tool string) bool {
	if tool != workspaceDependenciesToolName {
		return false
	}
	if namespace == nil || strings.TrimSpace(*namespace) == "" {
		return true
	}
	// Desktop-created threads persist this tool in the codex_app namespace.
	return *namespace == "codex_app"
}

func loadWorkspaceDependencies(configuredRoot string) (string, error) {
	root, err := workspaceDependenciesRuntimeRoot(configuredRoot)
	if err != nil {
		return "", err
	}

	manifestPath := filepath.Join(root, "runtime.json")
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", manifestPath, err)
	}
	var manifest workspaceDependenciesManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return "", fmt.Errorf("decode %s: %w", manifestPath, err)
	}
	if strings.TrimSpace(manifest.BundleVersion) == "" {
		return "", fmt.Errorf("%s has no bundleVersion", manifestPath)
	}

	deps := filepath.Join(root, "dependencies")
	node, err := firstExistingPath(
		filepath.Join(deps, "node", "bin", "node"),
		filepath.Join(deps, "node", "node.exe"),
	)
	if err != nil {
		return "", fmt.Errorf("locate bundled Node.js: %w", err)
	}
	nodeModules, err := requireExistingPath(filepath.Join(deps, "node", "node_modules"))
	if err != nil {
		return "", fmt.Errorf("locate bundled Node.js packages: %w", err)
	}
	_, err = requireExistingPath(filepath.Join(nodeModules, "@oai", "artifact-tool"))
	if err != nil {
		return "", fmt.Errorf("locate @oai/artifact-tool: %w", err)
	}
	python, err := firstExistingPath(
		filepath.Join(deps, "python", "bin", "python3"),
		filepath.Join(deps, "python", "bin", "python"),
		filepath.Join(deps, "python", "python.exe"),
	)
	if err != nil {
		return "", fmt.Errorf("locate bundled Python: %w", err)
	}
	pythonPackages, err := requireExistingPath(filepath.Join(deps, "python"))
	if err != nil {
		return "", fmt.Errorf("locate bundled Python packages: %w", err)
	}
	overrideBin, err := requireExistingPath(filepath.Join(deps, "bin", "override"))
	if err != nil {
		return "", fmt.Errorf("locate override binaries: %w", err)
	}
	fallbackBin, err := requireExistingPath(filepath.Join(deps, "bin", "fallback"))
	if err != nil {
		return "", fmt.Errorf("locate fallback binaries: %w", err)
	}
	git, err := firstExistingPath(filepath.Join(fallbackBin, "git"), filepath.Join(fallbackBin, "git.exe"))
	if err != nil {
		return "", fmt.Errorf("locate bundled Git: %w", err)
	}
	pnpm, err := firstExistingPath(filepath.Join(fallbackBin, "pnpm"), filepath.Join(fallbackBin, "pnpm.cmd"), filepath.Join(fallbackBin, "pnpm.exe"))
	if err != nil {
		return "", fmt.Errorf("locate bundled pnpm: %w", err)
	}

	artifactVersion := strings.TrimSpace(manifest.ArtifactToolVersion)
	if artifactVersion == "" {
		artifactVersion = "unknown"
	}
	return fmt.Sprintf(`Workspace dependencies are available for this cc-connect Codex thread.

### Workspace Dependencies
Use these bundled paths for sheets, slides, documents, PDFs, images, or browser automation:
- Bundle version: %s
- @oai/artifact-tool version: %s
- Runtime validation: required paths are present
- Git executable: %s
- Node.js executable: %s
- Node.js packages: %s
- pnpm executable: %s
- Python executable: %s
- Python packages: %s
- Override binaries: %s
- Fallback binaries: %s`,
		quotePath(manifest.BundleVersion),
		quotePath(artifactVersion),
		quotePath(git),
		quotePath(node),
		quotePath(nodeModules),
		quotePath(pnpm),
		quotePath(python),
		quotePath(pythonPackages),
		quotePath(overrideBin),
		quotePath(fallbackBin),
	), nil
}

func workspaceDependenciesRuntimeRoot(configuredRoot string) (string, error) {
	if root := strings.TrimSpace(configuredRoot); root != "" {
		return filepath.Abs(root)
	}
	if root := strings.TrimSpace(os.Getenv("CODEX_PRIMARY_RUNTIME")); root != "" {
		return filepath.Abs(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".cache", "codex-runtimes", "codex-primary-runtime"), nil
}

func firstExistingPath(paths ...string) (string, error) {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("none of %s exists", strings.Join(paths, ", "))
}

func requireExistingPath(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return path, nil
}

func quotePath(value string) string {
	return "`" + value + "`"
}
