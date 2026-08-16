# Official CC Connect migration compatibility

`cc-connect-next migrate` validates compatibility before creating or changing any target. The default `--source-version auto` does not execute a binary from daemon metadata; it validates the actual TOML schema, the same semantic requirements as normal startup, the configured Agent/platform registry, persistent-path inventory, source identity, and access metadata. Use an exact version only when provenance is known, for example `--source-version v1.5.0-beta.3`; unsupported explicit releases fail closed.

| Official source | Persistent layout | Configuration behavior | Status |
|---|---|---|---|
| v1.4.1 | Covered | Covered | Supported |
| v1.5.0-beta.1 | Same known layout | Covered when every configured plugin exists in the Next build | Supported with preflight |
| v1.5.0-beta.2 | Same known layout | Covered when every configured plugin exists in the Next build | Supported with preflight |
| v1.5.0-beta.3 | Same known layout | `hide_agent_footer`, Feishu `mention_map` and `peer_bots`, topic workspace isolation, quoted-file retrieval, topic bootstrap, relay visibility, Weixin `burst_limit`/`burst_window_secs`, pi `thinking`, and the `cmd` array form are supported. The known rejections below fail preflight until implemented. | Supported with preflight |

Compatibility is deliberately configuration-specific. A release row does not mean an unavailable platform is silently removed or a new setting is ignored. If source TOML contains a field the current build cannot honor, fails normal startup validation (for example, an invalid display mode or a missing Agent/platform), or names a plugin unavailable in the current build, migration reports the exact incompatibility and writes nothing.

## Known rejections (fail preflight, nothing written)

- **Plugin types this build does not provide**: platforms `yuanbao`, `googlechat`, `tuitui`, `cloud_web`, `wps-agentspace`; agent `reasonix`. A source project configured with any of them is rejected by the registry preflight with the exact project and plugin name.
- **Gated settings implemented upstream but not here**: the `[[projects]]`-level `agent_session_idle_timeout_mins`, and the pi agent option `rpc` (official beta.x pi gained a persistent RPC transport; this build runs pi one-shot in json mode). An explicit `rpc = false` is behavior-neutral and passes.
- **Dynamic option tables are validated, not waved through**: the agent options `env` table migrates only for agent types that actually consume it (`devin` and `tmux` ignore env, so an env table on them is rejected as dead configuration). Feishu `mention_map` and `peer_bots` tables migrate only on `feishu`/`lark` platforms; `mention_map` additionally requires `resolve_mentions = true` and mention names without a leading `@` in this build, and the Feishu validator reports the exact violation otherwise. (Official CC Connect leaves a `mention_map` dormant when `resolve_mentions` is off; this build treats that as dead configuration and fails preflight with the fix.)

## Defaults that differ from official CC Connect

A migrated configuration keeps its bytes, so every setting it does not spell out follows this build's defaults. These defaults deliberately differ:

- **`card_mode` defaults to `rich`** (the privacy-first Feishu Card 2.0 answer-card contract) where official CC Connect defaults to `legacy` plain messages. A migrated config that never set `card_mode` changes rendering. Set `card_mode = "legacy"` globally under `[display]` or per project to keep the official look.
- **`data_dir` defaults to `~/.cc-connect-next`** instead of `~/.cc-connect`. Migration removes the ambiguity for migrated configs by pinning the rewritten `data_dir` to the migration target explicitly.
- **The chat surface defaults to the final answer only**: `thinking_messages`, `tool_messages`, `show_context_indicator`, and `reply_footer` all default to `false` (official: all shown). Set them to `true` explicitly to restore the official process output.
- **Feishu/Lark `done_emoji` defaults to `"Done"`** (official: no completion reaction). Set `done_emoji = "none"` to disable it.
- **`language` defaults to `"zh"`** (official: auto-detect from user messages). Set `language = "auto"` for the official detection behavior or pick a language explicitly.

Everything else — including Feishu `progress_style` (`legacy`), `reaction_emoji` (`OnIt`), the Weixin send quota (4 per 24h), and all timeout defaults — matches the official values.

The generated manifest is schema version 2 and records `source_version` as either the caller-supplied canonical release or `auto-layout-v1`, together with every copied file's source, target, size, and SHA-256.

## Recommended commands

```bash
cc-connect-next migrate --dry-run
cc-connect-next migrate
```

When the official release is known:

```bash
cc-connect-next migrate --source-version v1.5.0-beta.3 --dry-run
cc-connect-next migrate --source-version v1.5.0-beta.3
```

Before production cutover, stop the official runtime and repeat the same command with `--force` so state written after the earlier rehearsal is included. Official and Next may remain installed together, but must not establish concurrent Feishu connections with the same app credentials.
