# Web control-plane deployment

Signed Releases contain Linux amd64/arm64 control, server, and deploy-host artifacts plus macOS amd64/arm64 Runtime artifacts. Installation and updates verify the GitHub OIDC/Sigstore manifest identity and every SHA-256; unsigned artifacts are rejected.

Linux can use either the native systemd lane or Docker Compose. The two lanes must not share one state directory. The formal container install downloads one complete signed Release and runs:

```bash
sudo ./release/bootstrap-container.sh --release-dir ./release
```

This installs only `cc-connect-deploy-host.service` on the host. The executor is fixed to the `shusfun/cc-connect` Release identity, `ghcr.io/shusfun/cc-connect`, Compose project `cc-connect`, and service `cc-connect`. `compose.yaml` is the executor's fixed runtime input, not a standalone production entry point that bypasses the host executor.

The first log prints the one-time setup token. Compose binds only `127.0.0.1:9820`, runs as UID/GID 10001 with a read-only root filesystem, and bind-mounts `/var/lib/cc-connect-docker/control` and `/var/lib/cc-connect-docker/app`. Starting with the next tag that contains this change, Releases publish signed multi-architecture images and deploy-host artifacts; the already-published `v0.1.0` does not contain them.

Inside the container, control remains the only owner of server and never mounts the Docker Socket. It reaches the host executor through a read-only Unix Socket mount. Web update and rollback remain available: control owns activity checks, Runtime coordination, and the database backup, while the host executor owns only control-container replacement and watchdog rollback to the previous signed digest. The macOS Runtime is still installed with launchd and is never containerized.

Download one complete signed tag on Linux, then run the bundled bootstrap:

```bash
gh release download v0.1.0 --repo shusfun/cc-connect --dir release-v0.1.0
sudo ./release-v0.1.0/bootstrap.sh --release-dir ./release-v0.1.0
```

It creates release slots under `/opt/cc-connect/releases`, control state under `/var/lib/cc-connect/control`, app state under `/var/lib/cc-connect/app`, and private sockets under `/run/cc-connect`. systemd manages only control; control exclusively supervises server.

The first start listens only on `127.0.0.1:9820` and prints a one-time setup token. Use SSH forwarding and complete the six Web steps: create the administrator, save the public HTTPS origin, pair Runtime, validate Codex and at least one project, optionally configure WeCom WebSocket, then atomically generate configuration and start server. Apply the Release's `openresty-1panel.conf` (repository copy: [1Panel/OpenResty template](../deploy/openresty-1panel.conf)) to an existing HTTPS site.

Create a pairing code in the setup wizard and install Runtime on macOS:

```bash
curl -fsSL https://cc.example.com/runtime/v1/install.sh -o cc-connect-runtime-install.sh
sh cc-connect-runtime-install.sh --server https://cc.example.com --code <code> --tag v0.1.0
```

The Ed25519 private key stays in macOS Keychain. Runtime connects outbound over TLS/WebSocket and reads local Codex App state, so no VPN or inbound tunnel is required. Additional devices can be paired, renamed, revoked, and inspected through persistent connection logs in Operations. Catalog changes update only opaque project metadata; paths and conversation bodies never leave the Mac through catalog sync.

For the native systemd lane, updates and rollbacks are manual Web operations. control blocks while Turns, interactions, or realtime are active; backs up `control.db`; stages online Runtime devices; switches a candidate slot; and confirms only after candidate health checks. Each activated Runtime keeps `pending-activation.json` until the candidate control reconnects it and sends `runtime/update/confirm`; lack of confirmation triggers the Runtime watchdog rollback. systemd `ExecStopPost` restores the previous server slot and database if candidate startup fails. Update, rollback, and restart share one execution slot, and rollback is limited to the previous successful release.

Use the Web operations page for service and deployment logs. For SSH diagnosis, use `systemctl status cc-connect-control.service` and `journalctl -u cc-connect-control.service`; do not manually rewrite release links or delete activation backups.
