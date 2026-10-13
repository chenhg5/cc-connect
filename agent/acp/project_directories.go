package acp

import (
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"path/filepath"
	"strings"
)

func (a *Agent) SupportsProjectDirectories() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.projectDirectoryMode == "trae" || (a.projectDirectoryMode == "" && strings.EqualFold(strings.TrimSuffix(filepath.Base(a.cmd), ".exe"), "traecli"))
}

func (a *Agent) ConfigureProjectDirectories(primary string, additional []string) error {
	if len(additional) > 0 && !a.SupportsProjectDirectories() {
		return fmt.Errorf("ACP executable does not support additional project directories")
	}
	dirs, err := core.NormalizeProjectDirectories(primary, additional)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.workDir, a.additionalDirs = dirs[0], append([]string{}, dirs[1:]...)
	a.mu.Unlock()
	a.invalidateConfig()
	return nil
}
