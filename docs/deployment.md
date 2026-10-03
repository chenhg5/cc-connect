# Deploying cc-connect: Docker, nohup, tmux, screen

> **Issue:** [#1719](https://github.com/chenhg5/cc-connect/issues/1719) —
> "在 docker 中运行无法使用 daemon,导致 cron 无法工作"
> (cron jobs cannot find the daemon socket when running inside Docker).

This guide covers three lightweight ways to keep cc-connect running 24/7
without `systemd` (which is unavailable inside most containers), and the
single most common failure mode for each: a missing or unreachable Unix
socket under `~/.cc-connect/run/api.sock`.

If you only need a one-shot test, skip to the bottom of the page — the
`docker run` recipe is the shortest path to a working setup.

## TL;DR — diagnose first

When `cc-connect cron list` (or any other client subcommand) reports:

```
Error: cc-connect is not running.
  Tried socket: <path>/run/api.sock
  Other candidate data_dirs: ...
  --config used: <path>
```

…the daemon either crashed, was never started, or is using a different
`data_dir` than the client. Run these on the host (or inside the
container) to triage:

```bash
# 1. Is the daemon process alive?
pgrep -af 'cc-connect' || echo "no daemon process"

# 2. Does the socket file exist?
ls -l /var/lib/cc-connect/run/api.sock 2>&1 || true

# 3. If you started the daemon with --config, does the client know?
cc-connect --config /etc/cc-connect.toml cron list \
  --config /etc/cc-connect.toml

# 4. Last-resort recovery from a stale PID lock after a crash:
cc-connect --config /etc/cc-connect.toml --force
```

The `--config` flag is what Issue #1719 introduced on the client side:
the daemon used to expose only `--data-dir`, so a deploy that pinned a
custom `data_dir` inside `config.toml` was unreachable from cron jobs.

## 1. systemd (Linux hosts)

If you have systemd on the host, the project ships a service unit:

```bash
cc-connect daemon install      # writes /etc/systemd/system/cc-connect.service
sudo systemctl enable --now cc-connect
sudo systemctl status cc-connect
cc-connect daemon logs -f      # tail the journal
```

The unit reads `~/.cc-connect/config.toml` by default. To pin a custom
location:

```ini
# /etc/systemd/system/cc-connect.service.d/override.conf
[Service]
ExecStart=
ExecStart=/usr/local/bin/cc-connect --config /etc/cc-connect.toml
```

Then `sudo systemctl daemon-reload && sudo systemctl restart cc-connect`.

Cron jobs scheduled via `cc-connect cron add` fire inside the daemon,
so they need no extra wiring. System cron jobs (`crontab -e`) that
invoke `cc-connect cron list` need either `CC_DATA_DIR` exported in the
cron environment or `--config /etc/cc-connect.toml` on every invocation:

```cron
CC_DATA_DIR=/var/lib/cc-connect
*/5 * * * * /usr/local/bin/cc-connect cron list >> /var/log/cc-connect.log 2>&1
```

## 2. Docker

Docker is the most common place where Issue #1719 bit users. The
problem: the daemon starts, the socket binds on the container's
overlay filesystem, then `cc-connect cron` from the host (or from a
sidecar container) cannot see that socket because the path lives inside
the container.

There are two correct architectures:

### 2a. Single container (daemon + cron)

Put both the daemon and your cron jobs in the **same container**, mount
a persistent volume for `data_dir`, and start the daemon in the
background. The simplest reliable form:

```dockerfile
FROM ghcr.io/chenhg5/cc-connect:latest
RUN apk add --no-cache tzdata  # optional; many distros already have it
COPY cc-connect.toml /etc/cc-connect/config.toml
ENV CC_DATA_DIR=/var/lib/cc-connect
VOLUME ["/var/lib/cc-connect"]
EXPOSE 9820  # web admin
ENTRYPOINT ["cc-connect", "--config", "/etc/cc-connect/config.toml"]
CMD []
```

```bash
docker run -d \
  --name cc-connect \
  --restart unless-stopped \
  -v cc-connect-data:/var/lib/cc-connect \
  -v /etc/cc-connect:/etc/cc-connect:ro \
  ghcr.io/chenhg5/cc-connect:latest
```

Because the daemon and `cc-connect cron ...` invocations share the same
container, they share the same `/var/lib/cc-connect/run/api.sock` and
the resolution is automatic. From inside the container:

```bash
docker exec cc-connect cc-connect cron list
```

### 2b. Sidecar / multi-container

If you keep the daemon in one container and run cron jobs in another
(or on the host), you must share the socket somehow:

* **Bind-mount the data_dir volume** into the cron container. The socket
  path is `<data_dir>/run/api.sock`; mounting the whole directory means
  both containers see the same file.
* **Match UID/GID** on both containers. The socket is created with
  mode `0600`, so the cron container must run as the same UID that
  owns `data_dir`. A common failure: `cron` container runs as UID 0,
  daemon container as UID 1000, and you get EACCES. The fix is
  `user: "1000:1000"` on both `--user` flags.

```yaml
# docker-compose.yml
services:
  cc-connect:
    image: ghcr.io/chenhg5/cc-connect:latest
    command: ["--config", "/etc/cc-connect/config.toml"]
    volumes:
      - cc-data:/var/lib/cc-connect
      - ./cc-connect.toml:/etc/cc-connect/config.toml:ro
    user: "1000:1000"
    restart: unless-stopped

  cron-runner:
    image: ghcr.io/chenhg5/cc-connect:latest
    # any cron-like job that calls cc-connect cron / send / relay
    command: >
      sh -c "while true; do
        cc-connect --config /etc/cc-connect/config.toml cron list || true;
        sleep 300;
      done"
    volumes:
      - cc-data:/var/lib/cc-connect   # SAME volume as the daemon
      - ./cc-connect.toml:/etc/cc-connect/config.toml:ro
    user: "1000:1000"                  # MUST match the daemon container
    depends_on:
      - cc-connect

volumes:
  cc-data: {}
```

### 2c. Cron inside Docker

There are three working patterns; pick whichever fits your toolchain.

**(i) Use cc-connect's own scheduler (recommended).**

```bash
# Inside the container, schedule it via the daemon's API:
docker exec cc-connect cc-connect cron add \
  --cron "0 6 * * *" \
  --prompt "Summarize GitHub trending repos" \
  --desc "Daily Trending"
```

The daemon then fires the job in-process, with no cron daemon required.

**(ii) Use a `cron` image and call `cc-connect cron ...`.**

```dockerfile
FROM alpine:3.20
RUN apk add --no-cache curl tzdata dcron
COPY crontab /etc/crontabs/root
CMD ["crond", "-f", "-d", "0"]
```

```cron
# crontab — CC_DATA_DIR must match the daemon container's
CC_DATA_DIR=/var/lib/cc-connect
*/5 * * * * cc-connect --config /etc/cc-connect.toml cron exec abc123
```

Mount the same `cc-data` volume on this container, or schedule it
inside the same container as the daemon (pattern 2a).

**(iii) Use `supercronic` (a single-binary cron).**

```dockerfile
FROM ghcr.io/chenhg5/cc-connect:latest
COPY schedule.cron /etc/cc-connect/schedule.cron
USER 1000:1000
ENTRYPOINT []
CMD ["/usr/local/bin/supercronic", "/etc/cc-connect/schedule.cron"]
```

Same `data_dir` mount rules apply.

## 3. nohup / tmux / screen

For a single Linux box without systemd, the three classic options are
all fine. The only rule that matters is: pick one, and stick to it, so
the PID lock file in `<config_dir>/.<config_base>.lock` doesn't fight
itself.

### nohup

```bash
mkdir -p /var/log/cc-connect /var/lib/cc-connect
nohup cc-connect --config /etc/cc-connect/config.toml \
  >> /var/log/cc-connect/stdout.log 2>&1 &
echo $! > /var/run/cc-connect.pid

# Stop:
kill "$(cat /var/run/cc-connect.pid)"
# Or, if the PID file is stale:
cc-connect --config /etc/cc-connect/config.toml --force
```

### tmux

```bash
tmux new-session -d -s cc-connect \
  'cc-connect --config /etc/cc-connect/config.toml 2>&1 | tee -a /var/log/cc-connect/stdout.log'

# Watch it live:
tmux attach -t cc-connect

# Stop:
tmux kill-session -t cc-connect
```

### screen

```bash
screen -dmS cc-connect \
  cc-connect --config /etc/cc-connect/config.toml 2>&1 | tee -a /var/log/cc-connect/stdout.log

# Attach:
screen -r cc-connect

# Stop:
screen -S cc-connect -X quit
```

In all three cases, cron jobs (host `cron`, `crond`, or `supercronic`)
that call `cc-connect cron ...` need either `CC_DATA_DIR` exported or
the same `--config` flag on both server and client. See Issue #1719
for the underlying bug history.

## 4. Verification checklist

After deploying, run these four commands. Each one must succeed before
you trust the setup:

```bash
# 1. Daemon process is alive and holds the socket.
pgrep -af cc-connect
ls -l "$(cc-connect --config /etc/cc-connect.toml config path | xargs dirname)/run/api.sock"

# 2. Client can talk to the daemon.
cc-connect --config /etc/cc-connect/config.toml cron list

# 3. Add a one-shot test job and watch it fire.
cc-connect --config /etc/cc-connect/config.toml cron add \
  --cron "* * * * *" \
  --exec "echo hello-from-cron >> /tmp/cc-connect-test.log" \
  --desc "1-minute smoke test"

# 4. After the job fires:
tail -f /tmp/cc-connect-test.log   # should show "hello-from-cron"
cc-connect --config /etc/cc-connect/config.toml cron del <id-from-step-3>
```

If any step fails, the "diagnose first" section at the top covers the
common causes (PID lock vs socket mismatch, wrong `data_dir`, missing
volume mount, UID mismatch).

## 5. Migration from a stuck state

If your existing deployment is in the broken state from Issue #1719 —
the daemon PID is gone but the lock file remains, or the daemon is
running but cron jobs can't find the socket — the cleanest recovery:

```bash
# 1. Stop anything that might still be running.
pkill -f 'cc-connect' || true

# 2. Remove the stale PID lock.
rm -f /etc/cc-connect/.config.toml.lock   # or wherever your config lives

# 3. Decide on a single data_dir and bake it into config.toml:
echo 'data_dir = "/var/lib/cc-connect"' >> /etc/cc-connect/config.toml

# 4. Restart the daemon, then verify the client can reach it.
cc-connect --config /etc/cc-connect/config.toml --force
cc-connect --config /etc/cc-connect/config.toml cron list
```

After Issue #1719, the client and server agree on `data_dir` whenever
they agree on `--config`, so step 4 should not require any env-var
plumbing in normal cron entries.
