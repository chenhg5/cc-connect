package droid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func loadDroidCustomModels() []core.ModelOption {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	settingsPath := filepath.Join(homeDir, ".factory", "settings.json")
	return loadDroidCustomModelsFromFile(settingsPath)
}

func loadDroidCustomModelsFromFile(path string) []core.ModelOption {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var cfg struct {
		CustomModels []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			Model       string `json:"model"`
		} `json:"customModels"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil
	}

	models := make([]core.ModelOption, 0, len(cfg.CustomModels))
	for _, m := range cfg.CustomModels {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		desc := strings.TrimSpace(m.DisplayName)
		if desc == "" {
			desc = strings.TrimSpace(m.Model)
		}
		models = append(models, core.ModelOption{Name: id, Desc: desc})
	}
	return models
}
