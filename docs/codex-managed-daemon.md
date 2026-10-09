# Codex managed daemon guide

[简体中文](codex-managed-daemon.zh-CN.md) · [Configuration template](../config.example.toml)

## 1. Shared sessions through a managed daemon

CC-Connect can connect to an existing Codex managed daemon through App Server. Clients using the same daemon and thread share live output, approvals, questions and execution state. CC-Connect does not start, stop, restart or update the daemon.

### Configuration

```toml
[projects.agent.options]
backend = "app_server"
app_server_transport = "managed_daemon"
daemon_attach_only = true
daemon_socket = "/path/to/app-server-control.sock"
daemon_reconnect_attempts = 3
daemon_enable_steer = true
daemon_busy_message_mode = "queue"
daemon_enable_interrupt = true
daemon_enable_approvals = true
daemon_enable_questions = true
daemon_enable_async_questions = true
daemon_enable_delete = true
```

### Options

| Option | Default | Meaning |
|---|---:|---|
| `backend` | `"exec"` | Set to `"app_server"` to enable App Server. |
| `app_server_transport` | `"stdio"` | Set to `"managed_daemon"` to connect to an existing daemon. |
| `daemon_attach_only` | `false` | When `true`, select an existing thread with `/attach` or `/switch` before chatting. New threads cannot be created automatically. |
| `daemon_socket` | Unset | Daemon control socket path. When omitted, CC-Connect uses read-only CLI discovery. |
| `daemon_reconnect_attempts` | `3` | Number of retries after disconnection. `0` disables all automatic retries; it does not skip the initial connection. |
| `daemon_enable_steer` | `true` | Allow `/steer` to inject text into the active turn. |
| `daemon_busy_message_mode` | `"queue"` | `queue` waits for the active turn to finish; `steer` inserts ordinary text into it. |
| `daemon_enable_interrupt` | `true` | Allow stopping the active turn. |
| `daemon_enable_approvals` | `true` | Receive and handle tool approvals in CC-Connect. Disable to leave them to another daemon client. |
| `daemon_enable_questions` | `true` | Receive blocking `requestUserInput` questions. |
| `daemon_enable_async_questions` | `true` | Receive advisory questions answered through steer. |
| `daemon_enable_delete` | `true` | Allow `/delete` to delete threads through the daemon. |

Managed mode rejects `env` and `app_server_url`. Configure credentials, providers and process environment in the daemon. Attached threads retain their own model, provider, approval and sandbox settings; `model`, `mode`, `reasoning_effort` and `provider` configure new threads only.

### Existing stdio configurations

Existing configurations need no migration:

```toml
[projects.agent.options]
backend = "app_server"
app_server_url = "stdio://"
```

Omitting `app_server_transport` keeps the existing stdio path, including the previous handling of `app_server_url`. Omitting `app_server_url` also defaults to stdio. Omitting `backend` still selects exec.

Only explicitly setting both `backend = "app_server"` and `app_server_transport = "managed_daemon"` enables managed mode. `daemon_*` options are valid only in that mode.

## 2. Shared-session chat commands

These commands require the corresponding shared-session capabilities. `<id>` can be a thread ID or its index in the current `/list` output.

| Command | Purpose |
|---|---|
| `/attach <id\|index>` | Attach to an existing thread and receive live output. |
| `/detach` | Disconnect this observer; the thread continues running. |
| `/steer <text>` | Inject text instructions into the active turn. |
| `/terminals` or `/terminals list` | List background terminals created by this thread. |
| `/terminals stop <id>` | Stop one background terminal. |
| `/terminals stop all` | Stop all background terminals belonging to this thread. |
| `/decision <action-token> <decision>` | Submit a decision offered by the approval prompt, such as `allow`, `allow_session`, `allow_similar`, `network_allow` or `cancel`. |
| `/answer <action-token> <question-index> <answer>` | Answer a blocking question using its prompt token; question indexes start at `0`. |
| `/skip <action-token>` | Skip the pending question identified by its prompt token. |
| `/async-answer <action-token> text <answer>` | Answer an advisory question with free text, or click an offered option. |

Existing `/list`, `/switch <id>`, `/history [n]`, `/stop` and `/delete <id>` commands remain available. `/stop` interrupts the active turn; `/terminals stop` stops background terminals. `/delete` applies to inactive threads and requires `daemon_enable_delete`.

Approval and question tokens prevent replies to requests already handled by another client. Text approvals also accept the decisions listed in the prompt. `cancel` means cancellation and is not converted into denial.

In multi-workspace mode, shared turns, including turns started by another client, and approval waits count as workspace activity. Completion or detach releases that activity so idle reaping does not interrupt ongoing work.

`/history [n]` reads newest-first turn pages and then returns the latest `n` displayable messages in chronological order. It fetches more pages when necessary; unlimited history is also paginated. A single oversized page can still exceed the 32 MiB WebSocket message limit.

## 3. Thinking visibility across clients

Reasoning events emitted by a shared thread can also appear in the connected chat, including events from turns started in a CLI or IDE. CC-Connect applies the existing `thinking_messages` display setting, which defaults to `true`; managed mode does not change that default.

To hide reasoning in every CC-Connect chat, use:

```toml
[display]
thinking_messages = false
```

To override only one project, place this table within that project's `[[projects]]` entry:

```toml
[projects.display]
thinking_messages = false
```

You can also use `/quiet` to hide thinking and tool progress while retaining assistant replies. These controls affect CC-Connect presentation only; they do not disable reasoning or change what other daemon clients receive. Consider who can read the connected chat when enabling thinking display.

## 4. Daemon lifecycle and long-running use

The commands below were checked against Codex CLI 0.161.0. Check `codex app-server daemon --help` for your installed version. General App Server protocol documentation is available in [OpenAI Docs](https://learn.chatgpt.com/docs/app-server).

Before starting CC-Connect, run these commands separately under the same OS account and `CODEX_HOME` used by your interactive Codex clients:

```sh
codex app-server daemon start
codex app-server daemon version
```

`start` starts the background daemon if needed. `version` reports status, versions and `socketPath` as JSON; this is also CC-Connect's passive discovery command. Use the reported socket path for `daemon_socket`, or omit the option to discover it. An eligible interactive Codex session may already have started the background server; check `version` first in that case.

When CC-Connect runs as a service, check discovery under the service account and environment rather than only in your terminal. If the daemon uses a custom `CODEX_HOME`, set the same value in the service environment or the agent's `codex_home` option. An explicit `daemon_socket` bypasses discovery, but the service account must still be able to access that socket.

For durable management on an SSH host, prefer Codex's built-in command:

```sh
codex app-server daemon bootstrap
```

`bootstrap` installs durable local management and changes the host's daemon setup. Run it as an explicit host setup step, rather than from CC-Connect startup. Local socket sharing does not require `--remote-control`. For systemd or launchd deployments, check the management installed by your Codex version and its login/boot behavior; a tmux session alone does not provide boot startup or process supervision.

If the daemon exits, CC-Connect retries the connection up to `daemon_reconnect_attempts` times, but does not restart the daemon. With `0`, the observer closes after disconnection without retrying. Restore the daemon separately, then attach again. `/detach` and stopping CC-Connect leave the daemon running.

Use `codex app-server daemon stop` when intentionally stopping the daemon. `restart` and `update` can interrupt work shared by other clients, so schedule them outside active turns. Configure credentials and providers before starting the daemon; clients share its startup environment.

## 5. Notes

1. Unsupported shared-session commands report that the capability is unavailable.
2. `daemon_attach_only = true` is useful for managing existing work; set it to `false` when CC-Connect should create new threads.
3. Busy mode applies to turns started by any connected client. Pending approval/question answers are handled first, and attachments cannot be injected through steer. `steer` requires `daemon_enable_steer = true`.
4. Keep the socket, credentials, provider and daemon environment consistent.
5. CC-Connect refuses to attach to the thread identified by its inherited `CODEX_THREAD_ID`. That is the Codex thread that launched the process, so refusing it prevents a self-control loop; it does not forbid attaching to other active threads.
