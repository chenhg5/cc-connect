#!/usr/bin/env bash
# deploy.sh — safe binary deployment for cc-connect
#
# Builds the new binary, installs it, health-checks the start, and
# auto-rolls back to the previous known-good binary if the new one
# crashes within the grace period.
#
# Usage: ./deploy.sh [--skip-build]
#   --skip-build  skip make build, just deploy whatever ./cc-connect is now

set -euo pipefail

INSTALL_DIR="/home/leigh/workspace/cc-connect"
BINARY="$INSTALL_DIR/cc-connect"
BACKUP="$INSTALL_DIR/cc-connect.known-good"
SERVICE="cc-connect.service"
HEALTH_GRACE=15  # seconds the new binary must stay alive to be considered healthy

log() { echo "[deploy] $(date '+%H:%M:%S') $*"; }

# --- Build ---
if [[ "${1:-}" != "--skip-build" ]]; then
    log "Building new binary..."
    cd "$INSTALL_DIR"
    make build
    log "Build complete."
else
    log "Skipping build (--skip-build)."
fi

# --- Backup current known-good ---
if [[ -f "$BACKUP" ]]; then
    log "Existing known-good backup: $(stat -c '%Y %s' "$BACKUP" | awk '{print strftime("%Y-%m-%d %H:%M:%S", $1), int($2/1024)"K"}')"
fi

# Only back up if the currently running binary is healthy (service is active)
if systemctl --user is-active "$SERVICE" &>/dev/null; then
    log "Service is running — backing up current binary as known-good."
    cp "$BINARY" "$BACKUP"
else
    log "Service is not running — skipping backup (current binary may be bad)."
    if [[ ! -f "$BACKUP" ]]; then
        log "WARNING: No known-good backup exists. Proceeding without safety net."
    fi
fi

# --- Deploy ---
log "Stopping service..."
systemctl --user stop "$SERVICE" 2>/dev/null || true
sleep 1

log "Starting service with new binary..."
systemctl --user start "$SERVICE"

# --- Health check ---
log "Health check: waiting ${HEALTH_GRACE}s for new binary to prove stable..."
sleep "$HEALTH_GRACE"

if systemctl --user is-active "$SERVICE" &>/dev/null; then
    log "✓ New binary is healthy. Updating known-good backup."
    cp "$BINARY" "$BACKUP"
    log "Deploy complete."
    exit 0
fi

# --- Rollback ---
log "✗ New binary crashed within ${HEALTH_GRACE}s."

if [[ -f "$BACKUP" ]]; then
    log "Rolling back to known-good binary..."
    cp "$BACKUP" "$BINARY"
    systemctl --user reset-failed "$SERVICE" 2>/dev/null || true
    systemctl --user start "$SERVICE"
    sleep 3

    if systemctl --user is-active "$SERVICE" &>/dev/null; then
        log "✓ Rollback successful — running known-good binary."
        exit 1  # non-zero: deploy failed but service is alive
    else
        log "✗ CRITICAL: Rollback also failed. Service is down."
        log "  Manual recovery: check journalctl --user -u $SERVICE"
        exit 2
    fi
else
    log "✗ CRITICAL: No known-good backup to roll back to. Service is down."
    log "  Manual recovery: fix the binary, then: systemctl --user start $SERVICE"
    exit 2
fi
