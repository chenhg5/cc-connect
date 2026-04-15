package core

import (
	"os"
	"sort"
	"strings"
)

const (
	bridgeCapabilitiesSnapshotType  = "capabilities_snapshot"
	bridgeCapabilitiesSnapshotProto = "capabilities_snapshot_v1"
	bridgeCommandArgsModeText       = "text"
	bridgeCommandSourceBuiltin      = "builtin"
	bridgeCommandSourceCustom       = "custom"
)

// CurrentCommit is set by main at startup so bridge clients can inspect the
// host binary that produced a capability snapshot.
var CurrentCommit string

// CurrentBuildTime is set by main at startup so bridge clients can compare
// host snapshots without reverse-engineering git-describe version strings.
var CurrentBuildTime string

type bridgeCapabilitiesSnapshot struct {
	Type     string                      `json:"type"`
	Version  int                         `json:"v"`
	Host     bridgeCapabilitiesHost      `json:"host"`
	Projects []bridgeProjectCapabilities `json:"projects"`
}

type bridgeCapabilitiesHost struct {
	ID               string `json:"id"`
	Hostname         string `json:"hostname,omitempty"`
	CCConnectVersion string `json:"cc_connect_version,omitempty"`
	Commit           string `json:"commit,omitempty"`
	BuildTime        string `json:"build_time,omitempty"`
}

type bridgeProjectCapabilities struct {
	Project  string                   `json:"project"`
	Commands []bridgePublishedCommand `json:"commands"`
}

type bridgePublishedCommand struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	Source            string `json:"source"`
	RequiresWorkspace bool   `json:"requires_workspace"`
	ArgsMode          string `json:"args_mode"`
}

// GetBridgePublishedCommands returns the subset of commands that a bridge
// control-plane client can safely expose as slash commands. It intentionally
// excludes skills and other richer command models until the bridge protocol
// grows beyond the single free-form "args" text bucket.
func (e *Engine) GetBridgePublishedCommands() []bridgePublishedCommand {
	e.userRolesMu.RLock()
	disabledCmds := e.disabledCmds
	e.userRolesMu.RUnlock()

	seen := make(map[string]bool)
	var commands []bridgePublishedCommand

	for _, c := range builtinCommands {
		if len(c.names) == 0 || disabledCmds[c.id] {
			continue
		}
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		commands = append(commands, bridgePublishedCommand{
			Name:              c.id,
			Description:       e.i18n.T(MsgKey(c.id)),
			Source:            bridgeCommandSourceBuiltin,
			RequiresWorkspace: false,
			ArgsMode:          bridgeCommandArgsModeText,
		})
	}

	customCommands := e.commands.ListAll()
	sort.Slice(customCommands, func(i, j int) bool {
		return strings.ToLower(customCommands[i].Name) < strings.ToLower(customCommands[j].Name)
	})
	for _, c := range customCommands {
		lowerName := strings.ToLower(strings.TrimSpace(c.Name))
		if lowerName == "" || seen[lowerName] || disabledCmds[lowerName] {
			continue
		}
		seen[lowerName] = true

		desc := strings.TrimSpace(c.Description)
		if desc == "" {
			desc = "Custom command"
		}

		commands = append(commands, bridgePublishedCommand{
			Name:              c.Name,
			Description:       desc,
			Source:            bridgeCommandSourceCustom,
			RequiresWorkspace: false,
			ArgsMode:          bridgeCommandArgsModeText,
		})
	}

	return commands
}

func (bs *BridgeServer) buildCapabilitiesSnapshot() bridgeCapabilitiesSnapshot {
	hostName, _ := os.Hostname()
	projects := make([]bridgeProjectCapabilities, 0, len(bs.engines))

	bs.enginesMu.RLock()
	projectNames := make([]string, 0, len(bs.engines))
	for projectName := range bs.engines {
		projectNames = append(projectNames, projectName)
	}
	sort.Strings(projectNames)
	for _, projectName := range projectNames {
		ref := bs.engines[projectName]
		if ref == nil || ref.engine == nil {
			continue
		}
		projects = append(projects, bridgeProjectCapabilities{
			Project:  projectName,
			Commands: ref.engine.GetBridgePublishedCommands(),
		})
	}
	bs.enginesMu.RUnlock()

	return bridgeCapabilitiesSnapshot{
		Type:    bridgeCapabilitiesSnapshotType,
		Version: 1,
		Host: bridgeCapabilitiesHost{
			ID:               hostName,
			Hostname:         hostName,
			CCConnectVersion: CurrentVersion,
			Commit:           CurrentCommit,
			BuildTime:        CurrentBuildTime,
		},
		Projects: projects,
	}
}
