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

`POST /send` accepts `reply_thread_id` and optional `desktop_event` (`completed`, `progress`, or `request`). Supply `work_dir` as runtime routing metadata. A request containing a thread ID but no message registers its route without sending. A nonexistent thread or a thread outside the registered desktop workspace fails registration. Request events also require `desktop_request_id`. The existing owner-only socket permission remains the trust boundary.

## Verification

```sh
python3 scripts/codex-desktop/test_bridge.py
go test ./core -run 'TestDesktopNotification|TestHandleSend'
```

## Continue or answer through the existing desktop owner

Opt in on the Codex adapter with a helper command array. Replace only the generic checkout placeholder:

```toml
[projects.agent.options]
desktop_helper = ["python3", "<absolute-checkout>/scripts/codex-desktop/desktop.py"]
# Optional: use the same private state directory configured for the watcher.
# desktop_state_dir = "<private-state-directory>"
```

Send `/reply UUID --queue <instruction>` to wait for the current task to finish, or `/reply UUID --now <instruction>` to insert a follow-up immediately. Without a flag, `/reply UUID <instruction>` defaults to `--queue`. Queue jobs persist in the companion's private state directory and survive watcher restarts; keep the watcher running. They do not replace the desktop's own queue and are displayed in the desktop conversation only when dispatched. In immediate mode an active turn uses the native steer operation, and an idle conversation starts another turn with inherited settings.

The platform message ID produces a stable desktop message ID. A receipt is written before dispatch. Redeliveries and unknown acknowledgements do not replay the mutation, and a rejected steer never falls back to start-turn. Pending approvals and unconfirmed desktop submissions block queued dispatch. These checks remove the known active-turn start path that can leave a duplicate optimistic bubble; real desktop UI end-to-end verification is still required. The private IPC protocol does not expose an atomic cross-device claim, so no universal exactly-once guarantee is made. Commands use only routes notified to the exact messaging destination. They retain normal command disable/role policies and do not switch the mobile session. If the owner is unavailable, the command fails; it never resumes a second CLI writer.

Pending desktop approvals, structured questions and asynchronous question cards are also forwarded in full. Use `/answer UUID REQUEST <answer>`. Approvals accept `approve` or `deny` for this request only. A single question accepts plain text; multiple questions require a JSON object keyed by every question ID. MCP form requests accept a JSON object or `deny`. Authentication/URL and other unsupported requests must be handled on the desktop.

Before sending an answer, the helper refreshes the owner's live state and rejects a request already handled on the desktop. A private receipt is persisted before mutation. An explicit rejection permits a later retry; a transport/decode error keeps acceptance marked unknown and suppresses replay. Check the desktop after an unknown acknowledgement. The desktop has no atomic cross-device answer claim: a truly simultaneous desktop/mobile submission can still race. The bridge does not claim exactly-once cross-device answering or require both devices to answer.

Additional checks:

```sh
python3 scripts/codex-desktop/test_replies.py
python3 scripts/codex-desktop/test_answers.py
python3 scripts/codex-desktop/test_native_answer.py
go test ./core ./agent/codex -run 'TestCUJ_B12_DesktopCommands|TestDesktopHelper|TestValidateDesktopThread'
```

## Read progress without a model call

Send `/progress UUID` from the same notified messaging destination. It reads the existing desktop owner snapshot and shows the latest turn's public completed message, plan, elapsed time and pending request IDs. It excludes nonpublic channels and unclassified saved messages. A question with unknown answer acceptance stays pending, suppresses ETA, and shows a desktop-check warning instead of a replay command. It does not start, steer or resume a turn. If the owner is offline, the response explicitly uses saved history; saved task-started records never prove an active turn is still running.

An ETA is shown only for a live running turn with no unanswered requests, a known start time, and a plan with some completed and some remaining steps. It divides observed elapsed time equally among completed steps and labels the result a rough estimate. Unequal step durations, newly changed plans and blockers can make it inaccurate. With insufficient evidence, completed/failed/interrupted turns or saved history, ETA is unknown. Turn completion does not establish that every implementation step is finished.

```sh
python3 scripts/codex-desktop/test_progress.py
go test ./core -run 'TestCUJ_B13_DesktopProgress'
```
