---
name: file-watcher
description: "Install a systemd file watcher that triggers a cc-connect agent when files appear in a directory. Creates the watch script, systemd unit, and starts the service."
---

# File Watcher Setup

Install a file watcher that monitors a directory and triggers a cc-connect agent session when matching files appear. Uses `inotifywait` + systemd for reliable, persistent watching.

## When to use

- Setting up automated completion processing pipelines
- Triggering agent work when CI drops artifacts
- Monitoring directories for new data files, reports, or configs

## Prerequisites

Check before proceeding:

```bash
which inotifywait    # Must be installed (apt install inotify-tools)
which cc-connect     # Must be in PATH or use absolute path
systemctl --version  # systemd required
```

If `inotifywait` is missing, install it:
```bash
sudo apt install -y inotify-tools
```

## Required Information

Gather these from the user before creating anything:

| Parameter | Description | Example |
|-----------|-------------|---------|
| `WATCH_DIR` | Directory to monitor | `/home/leigh/workspace/data-worklog/data/completions/` |
| `FILE_PATTERN` | Glob pattern for matching files | `*.md` |
| `CC_PROJECT` | cc-connect project name to target | `data-worklog-PM` |
| `CC_DATA_DIR` | cc-connect data directory | `~/.cc-connect` |
| `SERVICE_NAME` | systemd unit name | `completion-watcher` |
| `PROMPT_TEMPLATE` | Message template (`${FILE}` is replaced) | `New completion file: ${FILE}. Read and process it.` |

## Installation Steps

### 1. Create the watch script

Write to `/opt/${SERVICE_NAME}/watch.sh`:

```bash
#!/bin/bash
WATCH_DIR="${WATCH_DIR}"
CC_CONNECT="${CC_CONNECT_PATH}"

inotifywait -m -e create -e moved_to --format '%f' "$WATCH_DIR" | while read FILE; do
  [[ "$FILE" == ${FILE_PATTERN} ]] || continue
  "$CC_CONNECT" send --as-prompt -p ${CC_PROJECT} --data-dir ${CC_DATA_DIR} \
    -m "${PROMPT_TEMPLATE}"
done
```

This requires `sudo` to write to `/opt/`. Make the script executable:
```bash
sudo mkdir -p /opt/${SERVICE_NAME}
sudo tee /opt/${SERVICE_NAME}/watch.sh > /dev/null <<'SCRIPT'
... (script content)
SCRIPT
sudo chmod +x /opt/${SERVICE_NAME}/watch.sh
```

### 2. Create the systemd unit

Write to `/etc/systemd/system/${SERVICE_NAME}.service`:

```ini
[Unit]
Description=${SERVICE_NAME} file watcher
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/${SERVICE_NAME}/watch.sh
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

Install with:
```bash
sudo tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<'UNIT'
... (unit content)
UNIT
```

### 3. Enable and start

```bash
sudo systemctl daemon-reload
sudo systemctl enable ${SERVICE_NAME}.service
sudo systemctl start ${SERVICE_NAME}.service
```

### 4. Verify

```bash
systemctl status ${SERVICE_NAME}.service   # Should be active (running)
journalctl -u ${SERVICE_NAME}.service -f   # Follow logs
```

Drop a test file to confirm:
```bash
echo "# Test" > ${WATCH_DIR}/TEST-watcher-verify.md
# Check journal for send output
journalctl -u ${SERVICE_NAME}.service --since "10 seconds ago"
# Clean up
rm ${WATCH_DIR}/TEST-watcher-verify.md
```

## Important Notes

- The watch script uses `--as-prompt` so the message is injected as an inbound prompt the agent will process and respond to
- If no active session exists when a file arrives, the send will fail — the agent must be running
- If the session is busy, `--as-prompt` retries with 2-second backoff for up to 60 seconds
- The `inotifywait` events `create` and `moved_to` cover both new files and files moved into the directory
- The service runs as root by default; add `User=` to the unit if you need it to run as a specific user

## Updating an Existing Watcher

To modify the watch script or prompt template:

```bash
sudo vim /opt/${SERVICE_NAME}/watch.sh    # or use tee
sudo systemctl restart ${SERVICE_NAME}.service
```

## Uninstalling

```bash
sudo systemctl stop ${SERVICE_NAME}.service
sudo systemctl disable ${SERVICE_NAME}.service
sudo rm /etc/systemd/system/${SERVICE_NAME}.service
sudo rm -rf /opt/${SERVICE_NAME}
sudo systemctl daemon-reload
```
