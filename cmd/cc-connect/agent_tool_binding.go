package main

import (
	"os"
	"strings"
)

// Only an environment explicitly scoped by this bridge belongs to an owned
// subprocess. A shared daemon can inherit unrelated CC_* variables at startup.
func agentToolLegacyEnv(name string) string {
	if strings.TrimSpace(os.Getenv("CODEX_THREAD_ID")) != "" && os.Getenv("CC_CONNECT_SESSION_ENV") != "1" {
		return ""
	}
	return os.Getenv(name)
}

// Explicit destinations and scoped stdio environments retain their routing.
// Otherwise Codex's native thread ID is metadata for the local API only.
func agentToolSessionID(sessionKey string) string {
	if sessionKey != "" || os.Getenv("CC_CONNECT_SESSION_ENV") == "1" {
		return ""
	}
	return strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
}
