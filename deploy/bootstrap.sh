#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "用法: sudo ./bootstrap.sh --release-dir <已下载的 Release 目录> [--cosign <路径>]" >&2
}

release_dir=""
cosign_bin="${COSIGN_BIN:-cosign}"
bootstrap_root="${CC_CONNECT_BOOTSTRAP_ROOT:-}"
bootstrap_testing="${CC_CONNECT_BOOTSTRAP_TESTING:-0}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --release-dir) release_dir="${2:-}"; shift 2 ;;
    --cosign) cosign_bin="${2:-}"; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done

if [ -n "$bootstrap_root" ]; then
  if [ "$bootstrap_testing" != "1" ] || [[ "$bootstrap_root" != /* ]] || [ "$bootstrap_root" = "/" ]; then
    echo "CC_CONNECT_BOOTSTRAP_ROOT 只允许隔离测试使用绝对非根目录" >&2
    exit 2
  fi
elif [ "$(id -u)" -ne 0 ]; then
  usage
  exit 2
fi
if [ -z "$release_dir" ]; then
  usage
  exit 2
fi
required_commands=("$cosign_bin" jq sha256sum tar openssl systemctl)
if [ -z "$bootstrap_root" ]; then required_commands+=(useradd); fi
for command in "${required_commands[@]}"; do
  command -v "$command" >/dev/null 2>&1 || { echo "缺少必需命令: $command" >&2; exit 1; }
done

release_dir="$(cd "$release_dir" && pwd)"
manifest="$release_dir/manifest.json"
bundle="$release_dir/manifest.bundle"
test -f "$manifest" && test -f "$bundle" || { echo "Release 目录缺少 manifest.json 或 manifest.bundle" >&2; exit 1; }

repository="$(jq -er '.repository' "$manifest")"
workflow="$(jq -er '.workflow' "$manifest")"
tag="$(jq -er '.tag' "$manifest")"
test "$repository" = "shusfun/cc-connect" || { echo "拒绝非个人 fork Release: $repository" >&2; exit 1; }
test "$workflow" = ".github/workflows/release.yml" || { echo "拒绝未知 Release workflow: $workflow" >&2; exit 1; }
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || { echo "无效 tag: $tag" >&2; exit 1; }

"$cosign_bin" verify-blob \
  --bundle "$bundle" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/shusfun/cc-connect/.github/workflows/release.yml@refs/tags/$tag" \
  "$manifest" >/dev/null

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "不支持的 Linux 架构: $(uname -m)" >&2; exit 1 ;;
esac

verify_artifact() {
  component="$1"
  name="$(jq -er --arg component "$component" --arg arch "$arch" '.artifacts[] | select(.component == $component and .os == "linux" and .arch == $arch) | .name' "$manifest")"
  expected="$(jq -er --arg name "$name" '.artifacts[] | select(.name == $name) | .sha256' "$manifest")"
  test -f "$release_dir/$name" || { echo "缺少制品: $name" >&2; exit 1; }
  printf '%s  %s\n' "$expected" "$release_dir/$name" | sha256sum -c - >/dev/null
  printf '%s\n' "$name"
}

control_archive="$(verify_artifact control)"
server_archive="$(verify_artifact server)"

if [ -z "$bootstrap_root" ] && ! id cc-connect >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/cc-connect --create-home --shell /usr/sbin/nologin cc-connect
fi
owner="cc-connect"
group="cc-connect"
if [ -n "$bootstrap_root" ]; then owner="$(id -un)"; group="$(id -gn)"; fi
opt_dir="$bootstrap_root/opt/cc-connect"
control_dir="$bootstrap_root/var/lib/cc-connect/control"
app_dir="$bootstrap_root/var/lib/cc-connect/app"
helper_dir="$bootstrap_root/usr/libexec/cc-connect"
unit_dir="$bootstrap_root/etc/systemd/system"
install -d -o "$owner" -g "$group" -m 0750 "$opt_dir"
install -d -o "$owner" -g "$group" -m 0750 "$opt_dir/releases"
install -d -o "$owner" -g "$group" -m 0700 "$control_dir"
install -d -o "$owner" -g "$group" -m 0750 "$app_dir"
slot="$opt_dir/releases/$tag"
if [ ! -d "$slot" ]; then
  install -d -o "$owner" -g "$group" -m 0755 "$slot"
  tar -xzf "$release_dir/$control_archive" -C "$slot"
  tar -xzf "$release_dir/$server_archive" -C "$slot"
  chmod 0755 "$slot/cc-connect-control" "$slot/cc-connect-server"
  install -m 0644 "$manifest" "$slot/manifest.json"
  install -m 0644 "$bundle" "$slot/manifest.bundle"
fi
test -x "$slot/cc-connect-control" && test -x "$slot/cc-connect-server" || { echo "版本槽不完整: $slot" >&2; exit 1; }
test "$(jq -er '.tag' "$slot/manifest.json")" = "$tag" || { echo "现有版本槽 manifest 不匹配" >&2; exit 1; }
ln -sfn "$slot" "$opt_dir/current"
install -d -o "$owner" -g "$group" -m 0755 "$helper_dir"
install -o "$owner" -g "$group" -m 0755 "$slot/cc-connect-control" "$helper_dir/activation-helper"

setup_token=""
if [ ! -f "$control_dir/control.db" ]; then
  if [ -f "$control_dir/setup-token" ]; then
    setup_token="$(cat "$control_dir/setup-token")"
  else
    setup_token="$(openssl rand -hex 24)"
    umask 077
    printf '%s\n' "$setup_token" > "$control_dir/setup-token"
    chown "$owner:$group" "$control_dir/setup-token"
  fi
fi

install -d -o "$owner" -g "$group" -m 0755 "$unit_dir"
cat > "$unit_dir/cc-connect-control.service" <<'UNIT'
[Unit]
Description=cc-connect control plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=cc-connect
Group=cc-connect
RuntimeDirectory=cc-connect
RuntimeDirectoryMode=0750
ExecStart=/opt/cc-connect/current/cc-connect-control --listen 127.0.0.1:9820 --control-dir /var/lib/cc-connect/control --app-dir /var/lib/cc-connect/app --run-dir /run/cc-connect --server-binary /opt/cc-connect/current/cc-connect-server --setup-token-file /var/lib/cc-connect/control/setup-token
Restart=always
RestartSec=3
ExecStopPost=/usr/libexec/cc-connect/activation-helper recover-activation --record /var/lib/cc-connect/control/activation.json --releases-dir /opt/cc-connect/releases --current-link /opt/cc-connect/current --control-database /var/lib/cc-connect/control/control.db
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/cc-connect /run/cc-connect /opt/cc-connect
UMask=0027

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now cc-connect-control.service

echo "cc-connect control 已仅监听 127.0.0.1:9820。"
if [ -n "$setup_token" ]; then
  echo "一次性设置 Token: $setup_token"
else
  echo "control.db 已存在；未生成新的设置 Token。"
fi
echo "首次设置前请建立 SSH 转发: ssh -L 9820:127.0.0.1:9820 <server>"
echo "然后访问: http://127.0.0.1:9820/setup"
