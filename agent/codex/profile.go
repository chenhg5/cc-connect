package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// absoluteCodexHome pins the profile before a resumed turn changes directory.
// Explicit codex_home wins; otherwise use the subprocess's merged environment,
// including an empty CODEX_HOME that clears an inherited override.
func absoluteCodexHome(explicit string, extraEnv []string) (string, error) {
	home := strings.TrimSpace(explicit)
	if home == "" {
		env := core.MergeEnv(os.Environ(), extraEnv)
		home = getenvFromList(env, "CODEX_HOME")
		if home == "" {
			key := "HOME"
			switch runtime.GOOS {
			case "windows":
				key = "USERPROFILE"
			case "plan9":
				key = "home"
			}
			userHome := getenvFromList(env, key)
			if userHome == "" {
				return "", fmt.Errorf("codex: cannot resolve profile directory: %s is not set", key)
			}
			home = filepath.Join(userHome, ".codex")
		}
	}
	return filepath.Abs(home)
}
