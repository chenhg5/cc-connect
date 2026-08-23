# Unified workspace chat

Unified workspace chat connects the main Web chat and WeCom direct messages to the same Codex App projects and native Codex threads. Codex threads are authoritative for conversation content; `data_dir/workspace_chat.db` stores only client selection, numbered-menu snapshots, and Turn delivery state.

## Configuration

```toml
[management]
enabled = true
port = 9820
token = "your-management-token"

[workspace_chat]
enabled = true
template_project = "codex-template"
transports = ["web", "wecom"]

[[projects]]
name = "codex-template"
work_dir = "/path/to/a/default/project"
[projects.agent]
type = "codex"
[projects.agent.options]
backend = "app_server"
```

The template must exist and use the Codex `app_server` backend. At least one of `web` and `wecom` is required; `web` also requires the Management API. Invalid configuration fails startup explicitly.

`/chat` lists the projects in `CODEX_HOME/.codex-global-state.json`, expands multi-root projects, restores the latest native thread, and creates one immediately when none exists. The URL contains both `workspaceRef` and `threadId`; bare `/chat` restores the SQLite selection for `web:admin`. Legacy `session_key` conversations remain under `/platform-sessions` and are not imported.

WeCom uses its WebSocket long connection, with no public callback, VPN, or tunnel. Direct-message selection is stored as `wecom:user:{userId}`; group chats are rejected explicitly. See [WeCom setup](wecom.md#统一工作区对话) for commands.

Each thread has one FIFO worker shared by Web and WeCom. `WorkspaceChatService` owns queueing, approval, cancellation, shutdown, and state publication. On restart, unfinished SQLite Turn records become `interrupted`; Codex remains authoritative for full Turn history.
