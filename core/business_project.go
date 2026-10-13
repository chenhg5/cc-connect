package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ProjectDirectory struct {
	ID   string `toml:"id" json:"id"`
	Path string `toml:"path" json:"path"`
}

// BusinessProject is independent of the execution backend and chat channel.
type BusinessProject struct {
	ID               string             `toml:"id" json:"id"`
	Name             string             `toml:"name" json:"name"`
	Goal             string             `toml:"goal,omitempty" json:"goal,omitempty"`
	Rules            string             `toml:"rules,omitempty" json:"rules,omitempty"`
	BotIDs           []string           `toml:"bot_ids" json:"bot_ids"`
	PrimaryDirectory string             `toml:"primary_directory" json:"primary_directory"`
	Directories      []ProjectDirectory `toml:"directories" json:"directories"`
}

func (p BusinessProject) Validate() error {
	if !ValidBotName(p.ID) {
		return fmt.Errorf("invalid business project id %q", p.ID)
	}
	if len(p.BotIDs) == 0 {
		return fmt.Errorf("project %q requires bot_ids", p.ID)
	}
	seen := map[string]bool{}
	for _, id := range p.BotIDs {
		if !ValidBotName(id) || seen[id] {
			return fmt.Errorf("project %q has invalid or duplicate bot id", p.ID)
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	primary := false
	for _, d := range p.Directories {
		if !ValidBotName(d.ID) || seen[d.ID] || !filepath.IsAbs(d.Path) {
			return fmt.Errorf("project %q requires unique directory IDs and absolute paths", p.ID)
		}
		seen[d.ID] = true
		if d.ID == p.PrimaryDirectory {
			primary = true
		}
	}
	if !primary {
		return fmt.Errorf("project %q requires a primary_directory from directories", p.ID)
	}
	return nil
}

func (p BusinessProject) WorkDir() string {
	for _, d := range p.Directories {
		if d.ID == p.PrimaryDirectory {
			return d.Path
		}
	}
	return ""
}

// ProjectDirectoryConfigurer is optional: unsupported adapters must reject
// multiple roots instead of silently dropping directories or broadening access.
type ProjectDirectoryConfigurer interface {
	ConfigureProjectDirectories(primary string, additional []string) error
}

// Protocol adapters may implement the interface for only some executables.
func SupportsProjectDirectories(a Agent) bool {
	if _, ok := a.(ProjectDirectoryConfigurer); !ok {
		return false
	}
	if c, ok := a.(interface{ SupportsProjectDirectories() bool }); ok {
		return c.SupportsProjectDirectories()
	}
	return true
}

// NormalizeProjectDirectories validates every root before changing any state.
// Resolve aliases and deduplicate, preserving the primary root and input order.
func NormalizeProjectDirectories(primary string, additional []string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	for _, dir := range append([]string{primary}, additional...) {
		if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "\x00\r\n") {
			return nil, fmt.Errorf("project directory must be an absolute path: %q", dir)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
		stat, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		if !stat.IsDir() {
			return nil, fmt.Errorf("project path is not a directory: %s", dir)
		}
		if !seen[resolved] {
			dirs = append(dirs, resolved)
			seen[resolved] = true
		}
	}
	return dirs, nil
}

func ProjectAdditionalDirectoriesOption(opts map[string]any) ([]string, error) {
	switch v := opts["additional_dirs"].(type) {
	case nil:
		return nil, nil
	case []string:
		if v == nil {
			return nil, nil
		}
		return append([]string{}, v...), nil
	case []any:
		out := make([]string, len(v))
		for i, item := range v {
			var ok bool
			out[i], ok = item.(string)
			if !ok {
				return nil, fmt.Errorf("additional_dirs must contain only paths")
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("additional_dirs must be an array of paths")
	}
}
