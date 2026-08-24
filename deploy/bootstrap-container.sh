#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "用法: sudo ./bootstrap-container.sh --release-dir <已下载的 Release 目录> [--cosign <路径>]" >&2
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
if [ -z "$release_dir" ]; then usage; exit 2; fi
required_commands=("$cosign_bin" jq sha256sum tar openssl systemctl docker)
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
"$cosign_bin" verify-blob --bundle "$bundle" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/shusfun/cc-connect/.github/workflows/release.yml@refs/tags/$tag" \
  "$manifest" >/dev/null

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "不支持的 Linux 架构: $(uname -m)" >&2; exit 1 ;;
esac
archive="$(jq -er --arg arch "$arch" '.artifacts[] | select(.component == "deployhost" and .os == "linux" and .arch == $arch) | .name' "$manifest")"
expected="$(jq -er --arg name "$archive" '.artifacts[] | select(.name == $name) | .sha256' "$manifest")"
test -f "$release_dir/$archive" || { echo "缺少制品: $archive" >&2; exit 1; }
printf '%s  %s\n' "$expected" "$release_dir/$archive" | sha256sum -c - >/dev/null

staging="$(mktemp -d)"
trap 'rm -rf "$staging"' EXIT
tar -xzf "$release_dir/$archive" -C "$staging"
test -x "$staging/cc-connect-deploy-host" || { echo "宿主执行器制品不完整" >&2; exit 1; }
test -f "$staging/compose.yaml" || { echo "宿主执行器制品缺少 compose.yaml" >&2; exit 1; }

opt_dir="$bootstrap_root/opt/cc-connect-docker"
state_root="$bootstrap_root/var/lib/cc-connect-docker"
control_dir="$state_root/control"
app_dir="$state_root/app"
deployer_dir="$state_root/deployer"
unit_dir="$bootstrap_root/etc/systemd/system"
owner=10001
group=10001
if [ -n "$bootstrap_root" ]; then owner="$(id -un)"; group="$(id -gn)"; fi
install -d -m 0755 "$opt_dir" "$unit_dir"
install -d -o "$owner" -g "$group" -m 0700 "$control_dir"
install -d -o "$owner" -g "$group" -m 0750 "$app_dir"
install -d -o root -g root -m 0700 "$deployer_dir" 2>/dev/null || install -d -m 0700 "$deployer_dir"
install -m 0755 "$staging/cc-connect-deploy-host" "$opt_dir/cc-connect-deploy-host"
install -m 0644 "$staging/compose.yaml" "$opt_dir/compose.yaml"

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

cat > "$unit_dir/cc-connect-deploy-host.service" <<UNIT
[Unit]
Description=cc-connect fixed-target container deployment host
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
Type=simple
User=root
Group=root
Environment=HOME=/var/lib/cc-connect-docker/deployer
RuntimeDirectory=cc-connect-deploy
RuntimeDirectoryMode=0750
RuntimeDirectoryPreserve=yes
ExecStart=/opt/cc-connect-docker/cc-connect-deploy-host --socket /run/cc-connect-deploy/host.sock --state /var/lib/cc-connect-docker/deployer/state.json --environment /var/lib/cc-connect-docker/deployment.env --control-database /var/lib/cc-connect-docker/control/control.db --compose-file /opt/cc-connect-docker/compose.yaml --project-directory /opt/cc-connect-docker --initial-tag $tag --client-uid 10001 --client-gid 10001
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/cc-connect-docker /run/cc-connect-deploy
UMask=0027

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable cc-connect-deploy-host.service
systemctl restart cc-connect-deploy-host.service
echo "cc-connect Docker 控制面将仅监听 127.0.0.1:9820，systemd 只管理宿主部署执行器。"
if [ -n "$setup_token" ]; then
  echo "一次性设置 Token: $setup_token"
else
  echo "control.db 已存在；未生成新的设置 Token。"
fi
echo "首次设置前请建立 SSH 转发: ssh -L 9820:127.0.0.1:9820 <server>"
echo "然后访问: http://127.0.0.1:9820/setup"
