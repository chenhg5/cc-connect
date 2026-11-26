# Blackbox tests

## Release smoke (run before every release)

```bash
make test-release-smoke                                   # current tree
CC_BLACKBOX_BINARY=dist/cc-connect-<ver>-<os>-<arch> \
CC_BLACKBOX_EXPECT_VERSION=<ver> make test-release-smoke  # the artifact you will publish
```

Needs Go, plus the Claude Code (`claude`) and Codex (`codex`) CLIs on PATH. No
model key and no IM account are needed: the agents talk to a scripted fake
model, and the test drives cc-connect through its bridge like a chat platform.
It takes about 3–4 minutes with `-race`. On macOS, point `TMPDIR` at a
directory without symlinks (for example `~/.cache/qa-tmp`).

The run is **fail-closed**: a missing agent CLI fails it instead of skipping
(`CC_BLACKBOX_REQUIRE_AGENTS`, default `claudecode,codex`). With
`CC_BLACKBOX_EXPECT_VERSION` set, `/version` must report that version, which
proves the artifact itself was tested. The release flow runs this as its
`release-smoke` gate before publishing; a failure holds the release.

**A PASS means** that, for Claude Code and Codex, on the real binary and the
in-process engine, and across platform capability profiles (text-only,
buttons, cards, streaming previews):

| Area | Checked |
| --- | --- |
| Startup | the binary starts from a generated config, the bridge accepts an adapter and rejects a wrong token, `/version` is the release version |
| Conversation | a message reaches the model and the answer reaches the user; the next turn carries the previous one; long answers are previewed and end complete; images reach the model |
| Tools and permissions | a tool call runs and its output reaches the model; a permission prompt appears (buttons where the platform has them, text otherwise), allow runs the tool, deny keeps it from running |
| Sessions | `/new` separates conversations, `/list` shows them, `/switch` restores the right context; `/stop` ends a running turn and the next message resumes the session; a restarted binary still lists and resumes the session |
| Users | two users' conversations never reach each other's model |
| Failures | a model/provider error is shown to the user, and the next message works |
| Commands and cards | `/help`, `/status`, `/list` answer on every profile; card buttons click through; text-only platforms never receive buttons |

**A PASS does not cover**: real IM platforms (Feishu, Telegram, Slack, …:
their APIs, rendering and limits; see the release flow's channel smoke),
answers from a real model (`TestSmoke_AgentTurn` runs only with credentials),
agents other than Claude Code and Codex, and how cards look.

## Running blackbox tests

Blackbox tests drive cc-connect the way a user does: inject a chat message,
assert on what the platform receives. The agent side is a real agent CLI.

```bash
go test -tags blackbox -count=1 ./tests/blackbox/smoke/   # no model credentials needed
go test -tags blackbox -count=1 ./tests/blackbox/p0/      # needs model credentials
```

## Fake model

`Options{FakeModel: true}` points the agent at `tests/blackbox/fakemodel`, a
scripted stand-in for the model API. The real agent CLI and the real
cc-connect run; only the model is fake, so turns are deterministic and need
no key. One server speaks both wire formats:

| Agent | Wire format | Provider wiring |
| --- | --- | --- |
| `claudecode` | Anthropic Messages (`/v1/messages`) | `base_url` = fake |
| `codex` | OpenAI Responses (`/v1/responses`) | `base_url` = fake + `/v1`, `wire_api = "responses"`, private `codex_home` per test |

The script is the message text:

| Message | Fake model does |
| --- | --- |
| `FAKE:reply <text>` | answers `<text>` |
| `FAKE:slow <n> <word>` | streams `<word>` n times, 300 ms apart |
| `FAKE:tool Bash <command>` | calls the shell tool (`Bash` / Codex `exec_command`); after the tool result, answers `TOOL_DONE: <output>` (or `TOOL_ERROR: …`) |
| `FAKE:escalate <command>` | like `FAKE:tool Bash`, but asks to run outside the sandbox; this is what makes Codex request approval (use `AgentOptions: {"backend": "app_server", "mode": "suggest"}`) |
| `FAKE:recall` | answers `HISTORY: ` plus the text of earlier turns |
| anything else | answers `ECHO: <first line>` |

`env.FakeModel.TurnRequests()` returns what the CLI actually sent (user
text, tool results, image blocks), so tests can assert on the model side as
well.

## Backends

`CC_BLACKBOX_BACKEND` selects what runs cc-connect:

| Backend | What runs | What it covers |
| --- | --- | --- |
| `inprocess` (default) | `core.Engine` inside the test process | engine, commands, sessions, agent adapter |
| `binary` | the real `cc-connect` process, reached through its bridge WebSocket (`docs/bridge-protocol.md`) | everything above plus config loading, startup, the bridge transport and the agent subprocess as deployed |

The binary backend builds `./cmd/cc-connect` once per test run with
`-tags no_web`, or uses `CC_BLACKBOX_BINARY` (for example a release artifact
in `dist/`). Each `Env` gets its own `HOME`, data dir, config, random ports
and bridge token; the process log tail is printed when a test fails.

Tests that call `NewEnvWithSetup` or touch `env.Engine` need the in-process
engine and skip on the binary backend (`env.RequireInProcess()`).

## Capability profiles

cc-connect renders a turn differently depending on which optional platform
interfaces exist (buttons, cards, streaming preview). Real platforms fall
into a few profiles, so testing the profiles covers the platform-side
behaviour of core without one test per real platform.
`CC_BLACKBOX_PROFILE` (or `Options.Profile`) selects one:

| Profile | Capabilities | Similar real platforms |
| --- | --- | --- |
| `legacy` (default) | cards, buttons, images, files — the historical mock | — |
| `text` | text only | LINE |
| `buttons` | inline buttons | QQ Bot |
| `streaming` | preview start / update / delete | Slack, Matrix |
| `card` | cards with navigation, buttons | — |
| `full` | cards, buttons, streaming preview, images, files | Feishu, Telegram |

In-process, the profile picks a wrapper type that exposes only those
interfaces; on the binary backend it becomes the bridge `register`
capability list.

## Clicking buttons

`env.Click("perm:allow", timeout)` or
`env.Platform.InjectCardAction(user, chat, action)` simulate a button click
with the bridge contract: `perm:allow|deny|allow_all` answer a permission
prompt, `cmd:/x` runs a command, `nav:`/`act:` go through the card
navigation handler. `SentMessage.ButtonValues()` lists the callbacks
offered by a message, whether from inline buttons or card elements.
