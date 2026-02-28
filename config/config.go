package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Agent     AgentConfig      `toml:"agent"`
	Platforms []PlatformConfig `toml:"platforms"`
	Log       LogConfig        `toml:"log"`
}

type AgentConfig struct {
	Type    string         `toml:"type"`
	Options map[string]any `toml:"options"`
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

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Agent.Type == "" {
		return fmt.Errorf("config: agent.type is required")
	}
	if len(c.Platforms) == 0 {
		return fmt.Errorf("config: at least one platform must be configured")
	}
	for i, p := range c.Platforms {
		if p.Type == "" {
			return fmt.Errorf("config: platforms[%d].type is required", i)
		}
	}
	return nil
}
