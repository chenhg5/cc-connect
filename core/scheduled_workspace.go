package core

import (
	"fmt"
	"os"
	"strings"
)

// Scheduled jobs persist a session key, not the platform's Message.ChannelKey.
// Ask the platform to recover that opaque scope before resolving its workspace;
// parsing only a chat ID would discard topic-specific sessions and providers.
func (e *Engine) scheduledWorkspaceContext(p Platform, sessionKey, explicitWorkDir string) (Agent, *SessionManager, string, error) {
	if explicitWorkDir != "" {
		workspace := normalizeWorkspacePath(explicitWorkDir)
		agent, sessions, err := e.getOrCreateWorkspaceAgent(workspace)
		return agent, sessions, workspace, err
	}
	if !e.multiWorkspace {
		return e.agent, e.sessions, "", nil
	}
	channelKey := e.extractWorkspaceChannelKey(sessionKey)
	if channelKey == "" {
		return e.agent, e.sessions, "", nil
	}

	workspace := ""
	if e.workspaceBindings != nil {
		if binding, _ := e.workspaceBindings.LookupEffective("project:"+e.name, channelKey); binding != nil {
			workspace = normalizeWorkspacePath(binding.Workspace)
			// A timer must not delete an unavailable binding, then run a later
			// check in the global workspace. Preserve the binding and fail.
			if e.runAsUser() == "" {
				info, err := os.Stat(workspace)
				if err != nil {
					return nil, nil, "", fmt.Errorf("scheduled workspace %q: %w", workspace, err)
				}
				if !info.IsDir() {
					return nil, nil, "", fmt.Errorf("scheduled workspace %q is not a directory", workspace)
				}
			}
		}
	}
	if workspace == "" {
		channelID := strings.TrimPrefix(channelKey, p.Name()+":")
		var err error
		workspace, _, err = e.resolveWorkspace(p, channelID)
		if err != nil {
			return nil, nil, "", err
		}
	}
	if workspace == "" {
		return e.agent, e.sessions, "", nil
	}
	// Match interactive routing, including a topic's /dir override. Errors
	// propagate instead of silently selecting another agent or session store.
	agent, sessions, _, effectiveDir, err := e.workspaceContext(workspace, sessionKey)
	return agent, sessions, effectiveDir, err
}
