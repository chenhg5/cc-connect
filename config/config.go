package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ConfigPath stores the path to the config file for saving
var ConfigPath string

type Config struct {
	DataDir  string          `toml:"data_dir"` // session store directory, default ~/.cc-connect
	Projects []ProjectConfig `toml:"projects"`
	Log      LogConfig       `toml:"log"`
	Language string          `toml:"language"` // "en" or "zh", default is "en"
	Speech   SpeechConfig    `toml:"speech"`
}

// SpeechConfig configures speech-to-text for voice messages.
type SpeechConfig struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"` // "openai" | "groq"
	Language string `toml:"language"` // e.g. "zh", "en"; empty = auto-detect
	OpenAI   struct {
		APIKey  string `toml:"api_key"`
		BaseURL string `toml:"base_url"`
		Model   string `toml:"model"`
	} `toml:"openai"`
	Groq struct {
		APIKey string `toml:"api_key"`
		Model  string `toml:"model"`
	} `toml:"groq"`
}

// ProjectConfig binds one agent (with a specific work_dir) to one or more platforms.
type ProjectConfig struct {
	Name      string           `toml:"name"`
	Agent     AgentConfig      `toml:"agent"`
	Platforms []PlatformConfig `toml:"platforms"`
}

type AgentConfig struct {
	Type      string           `toml:"type"`
	Options   map[string]any   `toml:"options"`
	Providers []ProviderConfig `toml:"providers"`
}

type ProviderConfig struct {
	Name    string            `toml:"name"`
	APIKey  string            `toml:"api_key"`
	BaseURL string            `toml:"base_url,omitempty"`
	Model   string            `toml:"model,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
}

type PlatformConfig struct {
	Type    string         `toml:"type"`
	Options map[string]any `toml:"options"`
}

type LogConfig struct {
	Level string `toml:"level"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{
		Log: LogConfig{Level: "info"},
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.DataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.DataDir = filepath.Join(home, ".cc-connect")
		} else {
			cfg.DataDir = ".cc-connect"
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if len(c.Projects) == 0 {
		return fmt.Errorf("config: at least one [[projects]] entry is required")
	}
	for i, proj := range c.Projects {
		prefix := fmt.Sprintf("projects[%d]", i)
		if proj.Name == "" {
			return fmt.Errorf("config: %s.name is required", prefix)
		}
		if proj.Agent.Type == "" {
			return fmt.Errorf("config: %s.agent.type is required", prefix)
		}
		if len(proj.Platforms) == 0 {
			return fmt.Errorf("config: %s needs at least one [[projects.platforms]]", prefix)
		}
		for j, p := range proj.Platforms {
			if p.Type == "" {
				return fmt.Errorf("config: %s.platforms[%d].type is required", prefix, j)
			}
		}
	}
	return nil
}

// SaveActiveProvider persists the active provider name for a project.
func SaveActiveProvider(projectName, providerName string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			if cfg.Projects[i].Agent.Options == nil {
				cfg.Projects[i].Agent.Options = make(map[string]any)
			}
			cfg.Projects[i].Agent.Options["provider"] = providerName
			break
		}
	}
	return saveConfig(cfg)
}

// AddProviderToConfig adds a provider to a project's agent config and saves.
func AddProviderToConfig(projectName string, provider ProviderConfig) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	found := false
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			for _, existing := range cfg.Projects[i].Agent.Providers {
				if existing.Name == provider.Name {
					return fmt.Errorf("provider %q already exists in project %q", provider.Name, projectName)
				}
			}
			cfg.Projects[i].Agent.Providers = append(cfg.Projects[i].Agent.Providers, provider)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("project %q not found in config", projectName)
	}
	return saveConfig(cfg)
}

// RemoveProviderFromConfig removes a provider from a project's agent config and saves.
func RemoveProviderFromConfig(projectName, providerName string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	found := false
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			providers := cfg.Projects[i].Agent.Providers
			for j := range providers {
				if providers[j].Name == providerName {
					cfg.Projects[i].Agent.Providers = append(providers[:j], providers[j+1:]...)
					found = true
					break
				}
			}
			break
		}
	}
	if !found {
		return fmt.Errorf("provider %q not found in project %q", providerName, projectName)
	}
	return saveConfig(cfg)
}

func saveConfig(cfg *Config) error {
	f, err := os.Create(ConfigPath)
	if err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

// SaveLanguage saves the language setting to the config file.
func SaveLanguage(lang string) error {
	if ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	cfg.Language = lang
	return saveConfig(cfg)
}

// ListProjects returns project names from the config file.
func ListProjects() ([]string, error) {
	if ConfigPath == "" {
		return nil, fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var names []string
	for _, p := range cfg.Projects {
		names = append(names, p.Name)
	}
	return names, nil
}

// GetProjectProviders returns providers for a given project.
func GetProjectProviders(projectName string) ([]ProviderConfig, string, error) {
	if ConfigPath == "" {
		return nil, "", fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, "", fmt.Errorf("parse config: %w", err)
	}
	for _, p := range cfg.Projects {
		if p.Name == projectName {
			active, _ := p.Agent.Options["provider"].(string)
			return p.Agent.Providers, active, nil
		}
	}
	return nil, "", fmt.Errorf("project %q not found", projectName)
}
