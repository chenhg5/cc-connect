package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigureProjectDirectories is called on a fresh project runtime before any
// session starts. Existing wrapper arguments remain intact.
func (a *Agent) ConfigureProjectDirectories(primary string, additional []string) error {
	for _, dir := range append([]string{primary}, additional...) {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("project directory must be absolute")
		}
		stat, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if !stat.IsDir() {
			return fmt.Errorf("project path is not a directory: %s", dir)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = primary
	for _, dir := range additional {
		a.cliExtraArgs = append(a.cliExtraArgs, "--add-dir", dir)
	}
	return nil
}
