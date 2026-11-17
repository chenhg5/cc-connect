package kimi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Current Kimi Code records cwd, while older versions used workDir.
func TestParseKimiSessionDir_FiltersCurrentCwd(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	for _, tc := range []struct {
		name    string
		state   map[string]any
		visible bool
	}{
		{"current match", map[string]any{"cwd": workspace}, true},
		{"current other", map[string]any{"cwd": other}, false},
		{"legacy match", map[string]any{"workDir": workspace}, true},
		{"legacy other", map[string]any{"workDir": other}, false},
		{"cwd takes precedence", map[string]any{"cwd": other, "workDir": workspace}, false},
		{"cwd wins matching", map[string]any{"cwd": workspace, "workDir": other}, true},
		{"empty cwd falls back", map[string]any{"cwd": "", "workDir": other}, false},
		{"no directory legacy", map[string]any{"custom_title": "legacy"}, true},
		{"normalized cwd", map[string]any{"cwd": workspace + "/."}, true},
		{"archived", map[string]any{"cwd": workspace, "archived": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			data, err := json.Marshal(tc.state)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), data, 0600))
			info := parseKimiSessionDir(dir, workspace)
			if tc.visible {
				require.NotNil(t, info)
			} else {
				require.Nil(t, info)
			}
		})
	}
	t.Run("malformed metadata", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0600))
		require.Nil(t, parseKimiSessionDir(dir, workspace))
	})
}
