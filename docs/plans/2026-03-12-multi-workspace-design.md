# Multi-Workspace Feature Design

## Overview

Enable a single cc-connect bot (one Slack token) to serve multiple workspaces, with the channel determining which Claude Code working directory and session to use.

## Config

```toml
[[projects]]
name = "claude"
mode = "multi-workspace"
base_dir = "~/workspace"

[projects.agent]
type = "claudecode"
permission_mode = "yolo"

[[projects.platforms]]
type = "slack"
bot_token = "xoxb-..."
app_token = "xapp-..."
```

- `mode = "multi-workspace"` enables the feature. Omitting or `"single"` preserves current behavior.
- `base_dir` is the parent directory where workspaces live. Replaces `work_dir` on the agent.
- Agent config has no `work_dir` — resolved per-channel at runtime.

## Workspace Resolution Flow

When a message arrives in a channel:

1. **Check bindings** — look up `workspace_bindings.json` for an existing channel-to-workspace mapping.
2. **Convention match** — if no binding, check if `<base_dir>/<channel-name>/` exists. If yes, auto-bind and confirm:
   > "Found `~/workspace/model-profiler` matching this channel. Binding workspace and starting session... Ready."
3. **Ask for repo** — if no match, reply:
   > "No workspace found for this channel. What repo should I clone?"
   User provides URL, bot confirms:
   > "I'll clone `org/repo` to `~/workspace/repo-name` and bind to this channel. OK?"
4. **Clone and bind** — on confirmation, clone the repo, save the binding, spawn agent subprocess. Explicit feedback throughout:
   > "Cloning `github.com/org/repo` to `~/workspace/repo-name`..."
   > "Clone complete. Binding workspace to this channel... Ready."

### Binding Storage

Persisted in `~/.cc-connect/workspace_bindings.json`:

```json
{
  "project:claude": {
    "C0AKYKUF75K": {
      "channel_name": "model-profiler",
      "workspace": "/home/leigh/workspace/model-profiler",
      "bound_at": "2026-03-12T10:00:00Z"
    }
  }
}
```

## Agent Subprocess Management

Engine maintains `workspaceAgents map[string]*workspaceState` keyed by workspace path. Each `workspaceState` holds the agent subprocess, its SessionManager, and a `lastActivity` timestamp.

### Lifecycle

1. **Spawn on first message** — start a Claude Code subprocess with `work_dir` set to the resolved workspace.
2. **Resume on subsequent messages** — reuse the running subprocess with saved session ID.
3. **Idle reap** — background goroutine checks `lastActivity` every minute. Subprocesses idle >15 minutes are stopped. Session ID is preserved so the next message transparently restarts.
4. **Graceful shutdown** — on bot shutdown, stop all subprocesses cleanly.

### Session Management

Each workspace gets its own SessionManager instance with a separate JSON file (same naming scheme as today: `project_hash.json`). Named sessions within a workspace work exactly as they do now.

## Message Routing Changes

In `Engine.handleMessage`, the multi-workspace path inserts before the existing flow:

1. **Extract channel ID** from the message's session key (`slack:channelID:userID`).
2. **Resolve workspace** — look up binding, convention match, or trigger init flow.
3. **If no workspace resolved** (init flow in progress) — handle the init conversation directly, don't forward to any agent.
4. **If workspace resolved** — get or spawn the agent subprocess for that workspace, then continue with existing message processing.

`interactiveStates` gets keyed by workspace+sessionKey (rather than just sessionKey) so the same user in different channels hits different agent processes.

### New Commands

- `/workspace` — show current channel's bound workspace
- `/workspace init <url>` — clone and bind
- `/workspace unbind` — remove binding
- `/workspace list` — show all bindings

Existing commands (`/sessions`, `/model`, etc.) work per-workspace.

## Error Handling & Edge Cases

- **Unbound channel, bot mentioned** — bot asks for repo URL. No agent forwarding until binding is established.
- **Clone fails** (bad URL, auth, disk) — bot reports error and asks user to try again. No partial binding saved.
- **Workspace directory deleted externally** — on next message, bot detects missing directory, removes the binding, re-enters init flow: "Workspace `~/workspace/foo` no longer exists. What repo should I clone?"
- **Agent subprocess crashes** — restart on next message using saved session ID (same as current behavior).
- **Bot in unwanted channel** — without binding or matching directory, it just asks for a repo. User can ignore or remove the bot.

## Architecture: Approach 1 (Engine-level multiplexing)

The Engine itself handles multi-workspace routing. No new meta-engine or wrapper layers. The multi-workspace logic is gated behind the `mode` config field, so single-workspace projects are completely unaffected.
