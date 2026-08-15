# Official CC Connect migration compatibility

`cc-connect-next migrate` validates compatibility before creating or changing any target. The default `--source-version auto` does not execute a binary from daemon metadata; it validates the actual TOML schema, the same semantic requirements as normal startup, the configured Agent/platform registry, persistent-path inventory, source identity, and access metadata. Use an exact version only when provenance is known, for example `--source-version v1.5.0-beta.3`; unsupported explicit releases fail closed.

| Official source | Persistent layout | Configuration behavior | Status |
|---|---|---|---|
| v1.4.1 | Covered | Covered | Supported |
| v1.5.0-beta.1 | Same known layout | Covered when every configured plugin exists in the Next build | Supported with preflight |
| v1.5.0-beta.2 | Same known layout | Covered when every configured plugin exists in the Next build | Supported with preflight |
| v1.5.0-beta.3 | Same known layout | `hide_agent_footer`, Feishu `mention_map`, topic workspace isolation, quoted-file retrieval, topic bootstrap, and relay visibility are supported. Yuanbao and `agent_session_idle_timeout_mins` are rejected until implemented. | Supported with preflight |

Compatibility is deliberately configuration-specific. A release row does not mean an unavailable platform is silently removed or a new setting is ignored. If source TOML contains a field the current build cannot honor, fails normal startup validation (for example, an invalid display mode or a missing Agent/platform), or names a plugin unavailable in the current build, migration reports the exact incompatibility and writes nothing.

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
