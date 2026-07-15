# cc-connect (Nexus build)

Private, independent build of the cc-connect daemon: it bridges instant-messaging platforms
(Telegram, Feishu, Slack, …) to AI coding CLIs (Claude Code, Codex, Copilot, OpenCode, …),
extended with the Nexus orchestration layer — seats, dispatch, letter archive, worktree
isolation and persona injection.

**Not distributed.** This build is compiled from source and deployed manually. Self-update is
disabled and nothing here fetches from any upstream repository at runtime.

## Origin and attribution

Forked from [chenhg5/cc-connect](https://github.com/chenhg5/cc-connect), which contributes the
IM ↔ CLI streaming bridge that this build rests on. Upstream declares the MIT license in its
README; it ships no LICENSE file, so none is vendored here.

The fork relationship was severed under L-0407: the module path is
`github.com/JayGarland/cc-connect`, there is no `upstream` remote, and no runtime path reaches
upstream. Independence is a maintenance decision, not a claim over upstream's work.

## Documentation

Documentation lives in the Nexus repository, not here:

| Topic | Location |
|---|---|
| What this is, and the catalog of what Nexus added | `F:\nexus\docs\cc-connect\README.md`, `FEATURES.md` |
| Independence protocol and migration record | `F:\nexus\docs\cc-connect\INDEPENDENCE-PROTOCOL.md` |
| Deployment and restart procedure | `F:\nexus\HANDOFF.md` |
| Topology, seats, relay mappings | `F:\nexus\docs\boss\BOSS_HANDBOOK.md` |

## Build

```
go build ./...       # needs web/dist; build the web assets first
go test ./core/...
```

Changes go through a worktree and a feature branch — never directly onto `main`.
