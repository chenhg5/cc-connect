package codex

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Runtime caches are explicitly configured separately from source directories,
// so selecting a project cannot remove access needed by integration tools.
// These options never change the selected approval or sandbox mode.
func sandboxAccessOptions(opts map[string]any) ([]string, *bool, error) {
	roots, err := core.ProjectAdditionalDirectoriesOption(map[string]any{"additional_dirs": opts["sandbox_writable_roots"]})
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox_writable_roots: %w", err)
	}
	for i, root := range roots {
		if !filepath.IsAbs(root) || strings.ContainsAny(root, "\x00\r\n") {
			return nil, nil, fmt.Errorf("sandbox_writable_roots requires absolute paths")
		}
		roots[i] = filepath.Clean(root)
	}
	var network *bool
	if value, present := opts["sandbox_network_access"]; present {
		allowed, ok := value.(bool)
		if !ok {
			return nil, nil, fmt.Errorf("sandbox_network_access must be a boolean")
		}
		network = &allowed
	}
	return roots, network, nil
}

func mergeSandboxRoots(project, runtime []string) []string {
	if project == nil && runtime == nil {
		return nil
	}
	result := append([]string{}, project...)
	for _, root := range runtime {
		found := false
		for _, existing := range result {
			if existing == root {
				found = true
				break
			}
		}
		if !found {
			result = append(result, root)
		}
	}
	return result
}
