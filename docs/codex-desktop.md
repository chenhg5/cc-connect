# Optional Codex Desktop notifications

This experimental companion observes existing root desktop conversations. It never starts a Codex CLI writer or calls a model. It uses Python 3 standard-library modules and the desktop application's private local IPC protocol on macOS. Keep the desktop application running; application updates can require a protocol update. This does not replace the `exec` or `app_server` backends.

## Enable explicitly

Use a private state directory through `CC_CONNECT_DESKTOP_STATE_DIR` if the default user-scoped directory is unsuitable. Configure one existing messaging destination, including a DingTalk Stream bot:

```sh
python3 scripts/codex-desktop/notify.py configure --project example --session-key '<session-key>'
python3 scripts/codex-desktop/notify.py watch
```

For DingTalk, first configure the existing platform adapter as described in [the DingTalk guide](dingtalk.md). A generic platform configuration is:

```toml
[[projects.platforms]]
type = "dingtalk"
[projects.platforms.options]
client_id = "<app-key>"
client_secret = "<app-secret>"
```

Use the session key reported for your DingTalk conversation. This companion reuses the existing DingTalk delivery and long-message splitting; it does not upload local files or require another bot implementation.

Get the destination session key from cc-connect's existing session API. `--api-socket` can select the owner-only local API socket when the cc-connect data directory differs from its default. Each state directory belongs to one project/destination; use another state directory for another destination.

For completion notifications, configure Codex's native callback, replacing the script placeholder with the absolute path to this checkout:

```toml
notify = ["python3", "<absolute-checkout>/scripts/codex-desktop/notify.py", "notify"]
```

Both the desktop application's callback environment and the watcher must use the same `CC_CONNECT_DESKTOP_STATE_DIR` and `CODEX_HOME` overrides, when set. Restart the desktop application after changing its callback configuration so already-open conversations read the change. Run the watcher under your preferred user service manager if it should survive terminal closure; no OS-specific service configuration is installed automatically.

## Behavior

- Completion callbacks include the desktop thread ID and the full final text.
- Complete public `commentary` messages forward as progress updates without waiting for the entire turn. Partial text, hidden reasoning, tool output and child-agent conversations are excluded.
- No old completed commentary before initial configuration is replayed. The initial enabled timestamp persists across watcher restarts; delivered IDs remain deduplicated.
- Notifications register the thread only for their exact destination and do not switch that destination's active conversation/workspace.
- Outbound jobs remain on disk until sending succeeds. A failed job does not stop unrelated jobs. `ret=-2` suspends automatic sending for one hour while retaining the queue; the delay is a local retry policy, not a guaranteed platform recovery time.
- The platform's existing long-message splitting is reused. Transport failures after partial delivery may still cause duplicates on retry; no end-to-end exactly-once delivery is claimed.

The companion forwards already-generated messages; model output during the original task still uses its normal tokens. There is no periodic model prompt or strict ten-minute scheduler. Public message text can itself contain sensitive content, so enable forwarding only to a destination you trust. No files are uploaded by this companion.

## Local API extension

`POST /send` accepts `reply_thread_id` and optional `desktop_event` (`completed` or `progress`). Supply `work_dir` as runtime routing metadata. A request containing a thread ID but no message registers its route without sending. A nonexistent thread fails registration. The existing owner-only socket permission remains the trust boundary.

## Verification

```sh
python3 scripts/codex-desktop/test_bridge.py
go test ./core -run 'TestDesktopNotification|TestHandleSend'
```
