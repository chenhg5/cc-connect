# Blackbox tests

Blackbox tests drive cc-connect the way a user does: inject a chat message,
assert on what the platform receives. The agent side is a real agent CLI.

```bash
go test -tags blackbox -count=1 ./tests/blackbox/smoke/   # no model credentials needed
go test -tags blackbox -count=1 ./tests/blackbox/p0/      # needs model credentials
```

## Fake model

`Options{FakeModel: true}` points the agent at `tests/blackbox/fakemodel`, a
scripted stand-in for the Anthropic Messages API. The real Claude Code CLI
and the real cc-connect run; only the model is fake, so turns are
deterministic and need no key. The script is the message text:

| Message | Fake model does |
| --- | --- |
| `FAKE:reply <text>` | answers `<text>` |
| `FAKE:slow <n> <word>` | streams `<word>` n times, 300 ms apart |
| `FAKE:tool Bash <command>` | calls the Bash tool; after the tool result, answers `TOOL_DONE: <output>` (or `TOOL_ERROR: …`) |
| `FAKE:recall` | answers `HISTORY: ` plus the text of earlier turns |
| anything else | answers `ECHO: <first line>` |

`env.FakeModel.TurnRequests()` returns what the CLI actually sent (user
text, tool results, image blocks), so tests can assert on the model side as
well. Only `claudecode` is supported so far (Anthropic wire format).

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
