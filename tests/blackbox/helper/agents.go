//go:build blackbox

// This file registers all agent factories by importing the agent packages.
// Without these blank imports, core.CreateAgent would return "unknown agent".
// Each import triggers the package's init() function which calls core.RegisterAgent.
package helper

import (
	_ "github.com/timmyagentic/cc-connect-next/agent/claudecode"
	_ "github.com/timmyagentic/cc-connect-next/agent/codex"
	_ "github.com/timmyagentic/cc-connect-next/agent/cursor"
	_ "github.com/timmyagentic/cc-connect-next/agent/gemini"
	_ "github.com/timmyagentic/cc-connect-next/agent/opencode"
	_ "github.com/timmyagentic/cc-connect-next/agent/qoder"
)
