//go:build blackbox

// This file registers all agent factories by importing the agent packages.
// Without these blank imports, core.CreateAgent would return "unknown agent".
// Each import triggers the package's init() function which calls core.RegisterAgent.
package helper

import (
	_ "github.com/chenhg5/cc-connect/agent/claudecode"
	_ "github.com/chenhg5/cc-connect/agent/codex"
	_ "github.com/chenhg5/cc-connect/agent/cursor"
	_ "github.com/chenhg5/cc-connect/agent/gemini"
	_ "github.com/chenhg5/cc-connect/agent/opencode"
	_ "github.com/chenhg5/cc-connect/agent/qoder"
)
