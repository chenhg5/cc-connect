# maomao fork: fast/slow brain + mid-turn insert

This fork of [cc-connect](https://github.com/chenhg5/cc-connect) (based on v1.5.0) adds a **gate**
in front of the agent. The gate decides, per user message, what the slow brain (Claude Code) should do with it:

| action | agent idle | agent busy (turn in flight) |
|---|---|---|
| `silent` | buffered; delivered with the next message that goes through | same |
| `insert` | sent as a turn prefixed with "supplementary info — answer NO_REPLY if nothing to add" | **written into the running turn** (mid-turn injection) |
| `reply` | normal turn | **written into the running turn**; its final answer covers it |

Upstream cc-connect deliberately queues messages for busy sessions and runs each as a separate turn.
Claude Code's stream-json input actually folds a mid-turn user message into the current turn
(one `result`), the same as typing while it works — this fork uses that.

## Config (per project)

```toml
[[projects]]
gate_url = "http://127.0.0.1:9930/decide"   # empty = gate off (upstream behaviour)
gate_timeout_ms = 3000                        # on error/timeout the message is treated as "reply"
```

Gate protocol: `POST gate_url` with
`{"session_key","content","mentioned","busy","has_images","has_files","buffered":[...]}`,
answer `{"action":"reply|insert|silent","reason":"..."}`. Messages with attachments are never silenced;
slash commands and permission replies bypass the gate. Discord additionally sets `mentioned`
for @-mentions in `group_reply_all` guilds.

## Example gate

`contrib/gate/gate.py` (macOS / Apple Silicon, `uv run contrib/gate/gate.py`): deterministic rules first
(@-mention, questions, pure acknowledgements, link bursts), then a local Qwen3.5-4B (MLX) reads the
probabilities of three options (~30 ms). Every decision is logged as JSONL for tuning.

## Build

```sh
go build -tags no_web -o cc-connect ./cmd/cc-connect   # no_web: skip the web UI (needs node)
```
