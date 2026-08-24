#!/bin/sh
set -eu

control_dir="/var/lib/cc-connect/control"
app_dir="/var/lib/cc-connect/app"
run_dir="/run/cc-connect"
setup_token_file="$control_dir/setup-token"

mkdir -p "$control_dir" "$app_dir" "$run_dir"
chmod 0700 "$control_dir"
chmod 0750 "$app_dir" "$run_dir"

if [ ! -e "$control_dir/control.db" ] && [ ! -e "$setup_token_file" ]; then
  umask 077
  token="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
  [ "${#token}" -eq 48 ] || { echo "无法生成一次性设置 Token" >&2; exit 1; }
  printf '%s\n' "$token" > "$setup_token_file"
  printf '一次性设置 Token: %s\n' "$token"
fi

exec /usr/local/bin/cc-connect-control "$@"
