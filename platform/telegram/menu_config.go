package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// MenuConfig holds per-chat customization for the /menu panel.
type MenuConfig struct {
	mu      sync.RWMutex
	chatID  int64
	dataDir string

	Pinned          []string          `json:"pinned,omitempty"`           // pinned command names (max 4)
	HiddenCats      []string          `json:"hidden_cats,omitempty"`      // hidden category keys
	CustomCmds      []string          `json:"custom_cmds,omitempty"`      // custom commands to show
	CmdDescriptions map[string]string `json:"cmd_descriptions,omitempty"` // command → description overrides
}

// menuConfigPath returns the JSON file path for a given chatID.
func menuConfigPath(dataDir string, chatID int64) string {
	return filepath.Join(dataDir, fmt.Sprintf("menu_config_%d.json", chatID))
}

// LoadMenuConfig loads (or creates empty) config for the given chatID.
func LoadMenuConfig(chatID int64, dataDir string) *MenuConfig {
	cfg := &MenuConfig{chatID: chatID, dataDir: dataDir}
	path := menuConfigPath(dataDir, chatID)
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg // file not found = default config
	}
	_ = json.Unmarshal(data, cfg)
	return cfg
}

// Save writes the config to disk.
func (c *MenuConfig) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.dataDir, 0700); err != nil {
		return fmt.Errorf("menu_config: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("menu_config: marshal: %w", err)
	}
	path := menuConfigPath(c.dataDir, c.chatID)
	return os.WriteFile(path, data, 0600)
}

// IsCatHidden returns true if the category key should be hidden.
func (c *MenuConfig) IsCatHidden(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, h := range c.HiddenCats {
		if h == key {
			return true
		}
	}
	return false
}

// ToggleHiddenCat toggles visibility of a category. "session" is always visible.
// Returns the new hidden state.
func (c *MenuConfig) ToggleHiddenCat(key string) bool {
	if key == "session" {
		return false // session cannot be hidden
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, h := range c.HiddenCats {
		if h == key {
			c.HiddenCats = append(c.HiddenCats[:i], c.HiddenCats[i+1:]...)
			return false // now visible
		}
	}
	c.HiddenCats = append(c.HiddenCats, key)
	return true // now hidden
}

// TogglePinned pins or unpins a command. Max 4 pinned commands.
// Returns (isPinned, error).
func (c *MenuConfig) TogglePinned(cmd string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Pinned {
		if p == cmd {
			c.Pinned = append(c.Pinned[:i], c.Pinned[i+1:]...)
			return false, nil
		}
	}
	if len(c.Pinned) >= 4 {
		return false, fmt.Errorf("max 4 pinned commands")
	}
	c.Pinned = append(c.Pinned, cmd)
	return true, nil
}

// SetCmdDescription overrides the Telegram command list description for cmd.
func (c *MenuConfig) SetCmdDescription(cmd, desc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.CmdDescriptions == nil {
		c.CmdDescriptions = make(map[string]string)
	}
	c.CmdDescriptions[cmd] = desc
}

// Reset clears all customization.
func (c *MenuConfig) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Pinned = nil
	c.HiddenCats = nil
	c.CustomCmds = nil
	c.CmdDescriptions = nil
}
