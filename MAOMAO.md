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
(@-mention, @-ing someone else, questions, pure acknowledgements, link bursts), then a local Qwen3.5-4B (MLX)
reads the probabilities of three options (~30 ms on M5 Max). Every decision is logged as JSONL for tuning
(`GATE_LOG`, default `~/.cc-connect/gate.jsonl`); model decisions also log `option_mass` (how much of the
full-vocabulary probability lands on A/B/C — ~0.9 normally, low means the prompt is off) and raw `logprobs`.
Model: `GATE_MODEL` (HF repo id or local path).

The model part lives in `contrib/gate/scorer.py`: a fixed prefix + variable input, one forward pass, read
single-token options. The prefix's KV / linear-attention state is computed once and copied per call, so
only the ~40-token suffix is recomputed (Qwen3.5's linear-attention state can't be trimmed, so this
"continue from a saved point" is the only way to reuse it). Tests: `uv run contrib/gate/test_scorer.py`
(cached vs. full recompute, cache not polluted across inputs, option/length validation).

## Build

```sh
go build -tags no_web -o cc-connect ./cmd/cc-connect   # no_web: skip the web UI (needs node)
```
