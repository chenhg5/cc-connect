#!/usr/bin/env bash
# restart.sh — Safely restart cc-connect, even when called from within cc-connect.
#
# Uses systemd-run to schedule the actual restart as a transient unit, fully
# detached from the calling process tree. This means cc-connect can die
# (as expected) and the restart still completes.
#
# Usage:
#   ./restart.sh              # restart only, keeping the current binary
#   ./restart.sh --deploy     # build, backup current binary, install, restart,
#                             # health-check, rollback on failure.
#
# --deploy contract:
#   - The script owns the build. Callers do NOT copy a binary into place
#     themselves — that caused a past incident where `cp broken $BINARY`
#     then running --deploy backed up the broken file as "known-good" AND
#     installed the broken file, leaving both the live binary and the
#     rollback target corrupted at the same time.
#   - Build happens in the FOREGROUND so compilation errors are surfaced
#     to the caller immediately. $BINARY is never touched if the build fails.
#   - The backup step in the detached phase captures $BINARY BEFORE the
#     new binary replaces it — the backup is the rollback TARGET, not the
#     thing we are rolling forward TO.
#   - Install uses `install -m 0755` so mode is explicit every time.
#     `cp` over an existing file preserves the destination's mode, which
#     can silently strip the execute bit if the file was ever touched
#     to 0664. `install` creates a fresh file with the mode we specify.
#
# Ownership notes:
#   - $BINARY and $BACKUP must be writable by the invoking user. If they
#     end up owned by a non-leigh user (e.g. after a partseeker-coder
#     session operated in this directory), fix ownership first:
#       sudo chown leigh:partseeker-dev cc-connect cc-connect.known-good
#     The script cannot fix this itself — it has no sudo.

set -euo pipefail

SERVICE="cc-connect.service"
INSTALL_DIR="/home/leigh/workspace/cc-connect"
BINARY="$INSTALL_DIR/cc-connect"
STAGED="$INSTALL_DIR/cc-connect.new"
BACKUP="$INSTALL_DIR/cc-connect.known-good"
HEALTH_GRACE=15

log() { echo "[restart] $(date '+%H:%M:%S') $*"; }

# ---------------------------------------------------------------------------
# Mode: simple restart (default)
# ---------------------------------------------------------------------------
if [[ "${1:-}" != "--deploy" ]]; then
    log "Scheduling detached restart via systemd-run..."
    # Sleep 2s to let the calling Claude session finish sending its response,
    # then restart the service. Runs as a transient systemd unit so it survives
    # cc-connect dying. --collect makes systemd garbage-collect the transient
    # unit after it exits so a lingering failed/completed unit never block
    # the next invocation with "Unit already loaded or has a fragment file".
    #
    # After restart, wait 10s for service to stabilize, then notify all active
    # sessions so agents can resume or close out.
    restart_script=$(cat <<'RESTART_EOF'
sleep 2
systemctl --user reset-failed cc-connect.service 2>/dev/null || true
systemctl --user restart cc-connect.service

# Wait for service to come up and stabilize
sleep 10

# Notify all active sessions about the restart
CC_CONNECT="/home/leigh/workspace/cc-connect/cc-connect"
SOCKET="/home/leigh/.cc-connect/run/api.sock"
if [[ -x "$CC_CONNECT" && -S "$SOCKET" ]]; then
    # Query active sessions via API (returns JSON array)
    sessions=$(curl -s --unix-socket "$SOCKET" http://unix/sessions 2>/dev/null || echo "[]")
    # Parse each session and send notification
    echo "$sessions" | jq -c '.[]' 2>/dev/null | while read -r session; do
        project=$(echo "$session" | jq -r '.project')
        session_key=$(echo "$session" | jq -r '.session_key')
        if [[ -n "$project" && -n "$session_key" && "$project" != "null" && "$session_key" != "null" ]]; then
            "$CC_CONNECT" send --as-prompt --session "$session_key" --project "$project" \
                -m "cc-connect has restarted. Are we finished here or is there more to do? If finished, please close out the thread." \
                2>/dev/null || true
        fi
    done
fi
RESTART_EOF
)
    systemd-run --user --collect --unit=cc-connect-restart --description="cc-connect restart" \
        bash -c "$restart_script"
    log "Restart scheduled. Service will bounce in ~2 seconds."
    log "Active sessions will be notified after ~12 seconds."
    log "Check status: systemctl --user status $SERVICE"
    exit 0
fi

# ---------------------------------------------------------------------------
# Mode: deploy — build, backup, install, restart, health check, rollback
# ---------------------------------------------------------------------------

# Step 1–3 run in the foreground so build/verify errors are visible to the
# caller and the live $BINARY is never touched if they fail.

log "Building staged binary at $STAGED..."
cd "$INSTALL_DIR"
if ! go build -o "$STAGED" ./cmd/cc-connect; then
    log "ERROR: 'go build ./cmd/cc-connect' failed. Not touching $BINARY."
    rm -f "$STAGED"
    exit 1
fi

log "Verifying staged binary is a real ELF executable..."
if ! file "$STAGED" | grep -q 'ELF.*executable'; then
    log "ERROR: $STAGED is not an ELF executable. 'file' output:"
    file "$STAGED" || true
    rm -f "$STAGED"
    exit 1
fi
if ! [[ -x "$STAGED" ]]; then
    # Shouldn't happen — go build sets 0755 — but guard anyway.
    log "ERROR: $STAGED does not have execute bit set."
    ls -la "$STAGED" || true
    rm -f "$STAGED"
    exit 1
fi
log "Staged binary OK ($(stat -c %s "$STAGED") bytes, $(stat -c %A "$STAGED"))."

# Step 4+ runs detached via systemd-run so the calling session dying does
# not interrupt the restart.
deploy_script=$(cat <<'DEPLOY_EOF'
set -euo pipefail
SERVICE="cc-connect.service"
INSTALL_DIR="/home/leigh/workspace/cc-connect"
BINARY="$INSTALL_DIR/cc-connect"
STAGED="$INSTALL_DIR/cc-connect.new"
BACKUP="$INSTALL_DIR/cc-connect.known-good"
HEALTH_GRACE=15

log() { echo "[deploy] $(date '+%H:%M:%S') $*"; }

sleep 2  # let the calling session finish sending its "scheduled" message

log "Starting deploy..."

# Snapshot the OLD (currently-running) $BINARY as $BACKUP before anything
# modifies it. This is the correct backup-order: the backup captures the
# thing we want to roll BACK TO, not the thing we are rolling FORWARD TO.
# If this step is skipped, rollback has nothing real to restore from.
if [[ -f "$BINARY" ]]; then
    log "Snapshotting current $BINARY to $BACKUP (rollback target)."
    install -m 0755 "$BINARY" "$BACKUP"
fi

log "Stopping service..."
systemctl --user stop "$SERVICE" 2>/dev/null || true
sleep 1

# Install the staged binary with an explicit mode. `install -m 0755` creates
# a fresh file with the mode we specify, so it cannot silently inherit 0664
# from whatever was at $BINARY before.
log "Installing staged binary -> $BINARY (mode 0755)..."
install -m 0755 "$STAGED" "$BINARY"
rm -f "$STAGED"

log "Clearing any failed state and starting service..."
systemctl --user reset-failed "$SERVICE" 2>/dev/null || true
systemctl --user start "$SERVICE"

# Braces deliberately omitted on $HEALTH_GRACE below. systemd-run
# pre-substitutes brace-form variable references in ExecStart lines from
# the unit's own environment before exec, and since HEALTH_GRACE is only
# set inside the bash -c script (not in systemd's env), the brace form
# would render to empty before bash ever sees it. The unbraced form is
# passed through verbatim and expanded by bash at runtime.
# (See systemd.service "Command lines" section for the full substitution
# rules; note that comments inside the bash script are still scanned by
# the substitution pass, which is why this comment avoids literal braces.)
log "Health check: waiting $HEALTH_GRACE seconds..."
sleep "$HEALTH_GRACE"

if systemctl --user is-active "$SERVICE" &>/dev/null; then
    log "New binary is healthy. Deploy complete."
    exit 0
fi

log "New binary crashed within $HEALTH_GRACE seconds. Journal tail:"
journalctl --user -u "$SERVICE" -n 20 --no-pager || true

if [[ -f "$BACKUP" ]]; then
    log "Rolling back to known-good binary..."
    install -m 0755 "$BACKUP" "$BINARY"
    systemctl --user reset-failed "$SERVICE" 2>/dev/null || true
    systemctl --user start "$SERVICE"
    sleep 3
    if systemctl --user is-active "$SERVICE" &>/dev/null; then
        log "Rollback successful."
        exit 1
    else
        log "CRITICAL: Rollback also failed. Journal tail:"
        journalctl --user -u "$SERVICE" -n 20 --no-pager || true
        exit 2
    fi
else
    log "CRITICAL: No known-good backup. Service is down."
    exit 2
fi
DEPLOY_EOF
)

log "Scheduling detached deploy via systemd-run..."
# --collect is critical: without it, a failed or completed cc-connect-deploy
# transient unit lingers in systemd's view until an explicit reset-failed,
# and the next ./restart.sh --deploy invocation then fails with "Unit
# cc-connect-deploy.service was already loaded or has a fragment file".
# --collect makes systemd drop the unit after it exits, regardless of
# exit status.
systemd-run --user --collect --unit=cc-connect-deploy --description="cc-connect deploy" \
    bash -c "$deploy_script"
log "Deploy scheduled. It will run in ~2 seconds."
log "Monitor: journalctl --user -u cc-connect-deploy -f"
exit 0
