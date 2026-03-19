package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// CustomCommand represents a registered slash command (from config or agent command files).
type CustomCommand struct {
	Name        string // command name without leading "/"
	Description string
	Prompt      string // template with {{1}}, {{2}}, {{2*}}, {{args}} placeholders
	Exec        string // shell command to execute (mutually exclusive with Prompt)
	WorkDir     string // optional: working directory for exec command
	Source      string // "config" or "agent" (for display)
}

// CommandRegistry holds all available custom commands and resolves agent command files.
type CommandRegistry struct {
	mu        sync.RWMutex
	commands  map[string]*CustomCommand // from config.toml or runtime add
	agentDirs []string                  // directories to scan for *.md command files
}

func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]*CustomCommand),
	}
}

// Add registers a custom command.
func (r *CommandRegistry) Add(name, description, prompt, exec, workDir, source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands[strings.ToLower(name)] = &CustomCommand{
		Name:        name,
		Description: description,
		Prompt:      prompt,
		Exec:        exec,
		WorkDir:     workDir,
		Source:      source,
	}
}

// ClearSource removes all commands from a given source (e.g. "config").
func (r *CommandRegistry) ClearSource(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, c := range r.commands {
		if c.Source == source {
			delete(r.commands, k)
		}
	}
}

// Remove deletes a config-defined custom command by name. Returns false if not found.
func (r *CommandRegistry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	lower := strings.ToLower(name)
	if _, ok := r.commands[lower]; ok {
		delete(r.commands, lower)
		return true
	}
	return false
}

// SetAgentDirs sets the directories to scan for agent command files.
func (r *CommandRegistry) SetAgentDirs(dirs []string) {
	r.agentDirs = dirs
}

// Resolve looks up a command by name. Config commands take priority, then
// agent command directories are scanned for a matching .md file.
// Hyphens and underscores are treated as equivalent so that Telegram-sanitized
// names (e.g. "my_cmd") match original command names ("my-cmd").
func (r *CommandRegistry) Resolve(name string) (*CustomCommand, bool) {
	lower := strings.ToLower(name)
	norm := normalizeCommandName(name)

	r.mu.RLock()
	// Exact match first
	if c, ok := r.commands[lower]; ok {
		r.mu.RUnlock()
		return c, true
	}
	// Normalized match (hyphen ↔ underscore)
	for key, c := range r.commands {
		if normalizeCommandName(key) == norm {
			r.mu.RUnlock()
			return c, true
		}
	}
	r.mu.RUnlock()

	// Scan agent command directories; try both original name and hyphenated variant
	candidates := []string{name}
	if alt := strings.ReplaceAll(name, "_", "-"); alt != name {
		candidates = append(candidates, alt)
	}
	for _, dir := range r.agentDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		for _, candidate := range candidates {
			mdPath := filepath.Join(dir, candidate+".md")
			absPath, err := filepath.Abs(mdPath)
			if err != nil || !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
				continue
			}
			data, err := os.ReadFile(mdPath)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			slog.Debug("command: loaded agent command file", "path", mdPath)
			return &CustomCommand{
				Name:   candidate,
				Prompt: content,
				Source: "agent",
			}, true
		}
	}

	return nil, false
}

// ListAll returns all registered commands (config + agent command files).
func (r *CommandRegistry) ListAll() []*CustomCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var result []*CustomCommand

	for _, c := range r.commands {
		result = append(result, c)
		seen[strings.ToLower(c.Name)] = true
	}

	for _, dir := range r.agentDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".md")
			if seen[strings.ToLower(name)] {
				continue
			}
			seen[strings.ToLower(name)] = true

			desc := ""
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err == nil {
				first, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
				if len([]rune(first)) > 60 {
					first = string([]rune(first)[:60]) + "..."
				}
				desc = first
			}

			result = append(result, &CustomCommand{
				Name:        name,
				Description: desc,
				Source:      "agent",
			})
		}
	}

	return result
}

// placeholderRe matches {{1}}, {{2*}}, {{args}}, and variants with defaults like {{1:foo}}.
var placeholderRe = regexp.MustCompile(`\{\{(\d+\*?|args)(:[^}]*)?\}\}`)

// ExpandPrompt replaces template placeholders with the provided arguments.
//
// Supported placeholders:
//   - {{1}}, {{2}}, ...       — positional argument (1-based)
//   - {{1:default}}           — positional with default value if arg not provided
//   - {{2*}}                  — argument N and everything after it
//   - {{2*:default}}          — same, with default
//   - {{args}}                — all arguments joined by space
//   - {{args:default}}        — all arguments, with default if none provided
//
// If the template has no placeholders, arguments are appended to the end.
func ExpandPrompt(template string, args []string) string {
	if !placeholderRe.MatchString(template) {
		if len(args) > 0 {
			return template + "\n\n" + strings.Join(args, " ")
		}
		return template
	}

	result := placeholderRe.ReplaceAllStringFunc(template, func(match string) string {
		inner := match[2 : len(match)-2] // strip {{ and }}
		key, defaultVal, hasDefault := strings.Cut(inner, ":")

		if key == "args" {
			if len(args) > 0 {
				return strings.Join(args, " ")
			}
			if hasDefault {
				return defaultVal
			}
			return ""
		}
		if strings.HasSuffix(key, "*") {
			idx := 0
			_, _ = fmt.Sscanf(key, "%d", &idx)
			if idx >= 1 && idx-1 < len(args) {
				return strings.Join(args[idx-1:], " ")
			}
			if hasDefault {
				return defaultVal
			}
			return ""
		}
		idx := 0
		_, _ = fmt.Sscanf(key, "%d", &idx)
		if idx >= 1 && idx-1 < len(args) {
			return args[idx-1]
		}
		if hasDefault {
			return defaultVal
		}
		return ""
	})

	return result
}
