package codex

import "github.com/chenhg5/cc-connect/core"

func (a *Agent) ConfigureProjectDirectories(primary string, additional []string) error {
	dirs, err := core.NormalizeProjectDirectories(primary, additional)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir, a.additionalDirs = dirs[0], append([]string{}, dirs[1:]...)
	return nil
}
