#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  YOUZONE_ACCESS_TOKEN=... \
  YOUZONE_TENANT_ID=... \
  YOUZONE_RECEIVER_ROBOT_ID=... \
  YOUZONE_SENDER_ROBOT_ID=... \
  scripts/e2e-youzone-codex.sh

Runs a black-box YOUZONE -> cc-connect -> Codex -> YOUZONE test using two
YOUZONE robots:

  sender robot   -> sends the nonce prompt via YOUZONE sendMessage
  receiver robot -> cc-connect listens via getWss + WebSocket/xmpp

Required environment (no defaults — point these at resources you own; the
script sends a live test prompt to YOUZONE_SENDER_ROBOT_ID):
  YOUZONE_ACCESS_TOKEN             BIP/YouZone yht_access_token
  YOUZONE_TENANT_ID                tenant id
  YOUZONE_RECEIVER_ROBOT_ID        robot id cc-connect listens on (getWss + WebSocket)
  YOUZONE_SENDER_ROBOT_ID          robot id the nonce prompt is sent from

Optional environment:
  YOUZONE_BASE_URL                 default: https://c2.yonyoucloud.com
  YOUZONE_API_PREFIX               default: /yonbip-ec-link
  YOUZONE_E2E_TIMEOUT_SEC          default: 240
  YOUZONE_E2E_WORK_DIR             default: temp empty work dir
  CODEX_BIN                        default: codex

Exit codes:
  0  success
  2  missing prerequisite/config
  20 receiver did not get sender robot message
  21 Codex did not start/complete
  22 expected nonce was not observed in Codex output
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 2
  fi
}

require_cmd go
require_cmd node
require_cmd "${CODEX_BIN:-codex}"

require_env() {
  if [[ -z "${!1:-}" ]]; then
    echo "$1 is required (no default — set it to a resource you own)" >&2
    exit 2
  fi
}

# No defaults for tenant/robot ids: this script sends a live prompt via
# YOUZONE_SENDER_ROBOT_ID, so it must never fall back to someone else's robots.
require_env YOUZONE_ACCESS_TOKEN
require_env YOUZONE_TENANT_ID
require_env YOUZONE_RECEIVER_ROBOT_ID
require_env YOUZONE_SENDER_ROBOT_ID

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_URL="${YOUZONE_BASE_URL:-https://c2.yonyoucloud.com}"
API_PREFIX="${YOUZONE_API_PREFIX:-/yonbip-ec-link}"
TENANT_ID="$YOUZONE_TENANT_ID"
RECEIVER_ROBOT_ID="$YOUZONE_RECEIVER_ROBOT_ID"
SENDER_ROBOT_ID="$YOUZONE_SENDER_ROBOT_ID"
TIMEOUT_SEC="${YOUZONE_E2E_TIMEOUT_SEC:-240}"
CODEX_BIN="${CODEX_BIN:-codex}"

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
NONCE="YOUZONE_CODEX_E2E_${RUN_ID}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cc-connect-youzone-e2e.XXXXXX")"
CONFIG_PATH="$TMP_DIR/config.toml"
DATA_DIR="$TMP_DIR/data"
WORK_DIR="${YOUZONE_E2E_WORK_DIR:-$TMP_DIR/work}"
LOG_PATH="$TMP_DIR/cc-connect.log"
PID=""

cleanup() {
  if [[ -n "$PID" ]] && kill -0 "$PID" >/dev/null 2>&1; then
    kill "$PID" >/dev/null 2>&1 || true
    wait "$PID" >/dev/null 2>&1 || true
  fi
  if [[ "${YOUZONE_E2E_KEEP_TMP:-0}" != "1" ]]; then
    rm -rf "$TMP_DIR"
  else
    echo "kept temp dir: $TMP_DIR" >&2
  fi
}
trap cleanup EXIT

mkdir -p "$DATA_DIR" "$WORK_DIR"

cat >"$CONFIG_PATH" <<EOF
data_dir = "$DATA_DIR"
language = "zh"

[log]
level = "debug"

[display]
mode = "quiet"
thinking_messages = false
tool_messages = false

[[projects]]
name = "youzone-codex-e2e"
reply_footer = false
show_context_indicator = false
disabled_commands = ["restart", "upgrade", "shell"]

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "$WORK_DIR"
mode = "suggest"
reasoning_effort = "low"
cli_path = "$CODEX_BIN"

[[projects.platforms]]
type = "youzone"

[projects.platforms.options]
base_url = "$BASE_URL"
api_prefix = "$API_PREFIX"
robot_id = "$RECEIVER_ROBOT_ID"
access_token = "\${YOUZONE_ACCESS_TOKEN}"
tenant_id = "$TENANT_ID"
allow_from = "*"
websocket_protocols = "xmpp"
heartbeat_mode = "xmpp-whitespace"
ping_interval = "25s"
reconnect_delays = "1s,3s,10s,30s"
log_inbound_raw = true
EOF

echo "starting cc-connect receiver robot=$RECEIVER_ROBOT_ID"
(
  cd "$ROOT_DIR"
  YOUZONE_ACCESS_TOKEN="$YOUZONE_ACCESS_TOKEN" \
    go run -tags no_web ./cmd/cc-connect --config "$CONFIG_PATH" --force
) >"$LOG_PATH" 2>&1 &
PID="$!"

wait_log() {
  local pattern="$1"
  local timeout="$2"
  local start now
  start="$(date +%s)"
  while true; do
    if grep -Fq "$pattern" "$LOG_PATH" 2>/dev/null; then
      return 0
    fi
    now="$(date +%s)"
    if (( now - start >= timeout )); then
      return 1
    fi
    if ! kill -0 "$PID" >/dev/null 2>&1; then
      echo "cc-connect exited before pattern: $pattern" >&2
      tail -n 120 "$LOG_PATH" >&2 || true
      return 1
    fi
    sleep 1
  done
}

wait_log "youzone: websocket connected" 60 || {
  echo "receiver websocket did not connect" >&2
  tail -n 120 "$LOG_PATH" >&2 || true
  exit 2
}

echo "receiver connected; sending nonce from sender robot=$SENDER_ROBOT_ID nonce=$NONCE"
YOUZONE_E2E_BASE_URL="$BASE_URL" \
YOUZONE_E2E_API_PREFIX="$API_PREFIX" \
YOUZONE_E2E_TENANT_ID="$TENANT_ID" \
YOUZONE_E2E_SENDER_ROBOT_ID="$SENDER_ROBOT_ID" \
YOUZONE_E2E_NONCE="$NONCE" \
node <<'NODE'
const baseUrl = process.env.YOUZONE_E2E_BASE_URL;
const apiPrefix = process.env.YOUZONE_E2E_API_PREFIX;
const tenantId = process.env.YOUZONE_E2E_TENANT_ID;
const robotId = process.env.YOUZONE_E2E_SENDER_ROBOT_ID;
const nonce = process.env.YOUZONE_E2E_NONCE;
const token = process.env.YOUZONE_ACCESS_TOKEN;
const prompt = `请只回复 ${nonce}，不要输出其他内容。`;

(async () => {
  const response = await fetch(`${baseUrl}${apiPrefix}/claw-robot/client/sendMessage`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'cookie': `yht_access_token=${token}; tenantid=${tenantId}`,
      'origin': baseUrl,
      'referer': `${baseUrl}/`,
      'user-agent': 'cc-connect-youzone-e2e/0.1'
    },
    body: JSON.stringify({
      id: robotId,
      robotId,
      content: prompt,
      contentType: 2
    })
  });
  const text = await response.text();
  let payload = null;
  try { payload = JSON.parse(text); } catch {}
  if (!response.ok || (payload && typeof payload.code === 'number' && payload.code !== 200)) {
    console.error(`sendMessage failed HTTP ${response.status}: ${text.slice(0, 500)}`);
    process.exit(1);
  }
  console.log(text.slice(0, 500));
})();
NODE

if ! wait_log "message received\" platform=youzone" "$TIMEOUT_SEC"; then
  echo "receiver did not get sender robot message within ${TIMEOUT_SEC}s" >&2
  echo "This usually means YOUZONE does not route robot-initiated sendMessage from sender robot to receiver robot." >&2
  tail -n 160 "$LOG_PATH" >&2 || true
  exit 20
fi

if ! wait_log "codexSession: launching" 90; then
  echo "Codex did not launch" >&2
  tail -n 160 "$LOG_PATH" >&2 || true
  exit 21
fi

if ! wait_log "turn complete" "$TIMEOUT_SEC"; then
  echo "Codex turn did not complete" >&2
  tail -n 200 "$LOG_PATH" >&2 || true
  exit 21
fi

if ! grep -Fq "$NONCE" "$LOG_PATH"; then
  echo "expected nonce was not observed in cc-connect/Codex logs: $NONCE" >&2
  tail -n 200 "$LOG_PATH" >&2 || true
  exit 22
fi

if grep -Fq "send message failed" "$LOG_PATH"; then
  echo "YOUZONE reply send failed" >&2
  tail -n 200 "$LOG_PATH" >&2 || true
  exit 22
fi

echo "YOUZONE black-box E2E passed: nonce=$NONCE"
