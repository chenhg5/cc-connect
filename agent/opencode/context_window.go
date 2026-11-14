package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OpenCode does not report the model's context window with its usage, so the
// footer used to divide the prompt size by a generic 200k and showed ~100% for
// any conversation past that — on a model with a 1M window. OpenCode keeps the
// models.dev catalogue it resolves models against in its cache directory; the
// window comes from there, keyed by the same "provider/model" id the session
// runs with.

// opencodeModelsCacheTTL bounds how often the catalogue is re-read; OpenCode
// refreshes the file itself in the background.
const opencodeModelsCacheTTL = 10 * time.Minute

var opencodeModelsCache struct {
	sync.Mutex
	path    string
	loaded  time.Time
	modTime time.Time
	windows map[string]int // "provider/model" -> context window
}

// opencodeModelsPath is OpenCode's models catalogue: $XDG_CACHE_HOME/opencode
// or ~/.cache/opencode, on every platform (OpenCode uses XDG paths on macOS too).
func opencodeModelsPath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "opencode", "models.json")
}

// parseOpencodeModelWindows reads the catalogue's provider -> models -> limit.context.
func parseOpencodeModelWindows(data []byte) map[string]int {
	var catalogue map[string]struct {
		Models map[string]struct {
			Limit struct {
				Context int `json:"context"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &catalogue); err != nil {
		return nil
	}
	windows := make(map[string]int)
	for provider, p := range catalogue {
		for model, m := range p.Models {
			if m.Limit.Context > 0 {
				windows[provider+"/"+model] = m.Limit.Context
			}
		}
	}
	return windows
}

// opencodeContextWindow returns the context window of a "provider/model" id, or
// 0 when it is unknown (no model configured, a custom provider, no catalogue) —
// the footer then keeps its generic estimate.
func opencodeContextWindow(model string) int {
	model = strings.TrimSpace(model)
	if model == "" || !strings.Contains(model, "/") {
		return 0
	}
	path := opencodeModelsPath()
	if path == "" {
		return 0
	}

	c := &opencodeModelsCache
	c.Lock()
	defer c.Unlock()
	if c.path != path || time.Since(c.loaded) > opencodeModelsCacheTTL {
		info, err := os.Stat(path)
		switch {
		case err != nil:
			c.windows = nil
		case c.path != path || !info.ModTime().Equal(c.modTime) || c.windows == nil:
			if data, rerr := os.ReadFile(path); rerr == nil {
				c.windows = parseOpencodeModelWindows(data)
				c.modTime = info.ModTime()
			}
		}
		c.path = path
		c.loaded = time.Now()
	}
	return c.windows[model]
}
