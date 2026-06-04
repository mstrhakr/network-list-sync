#!/bin/sh
set -eu

APP_BIN="/network-list-sync"

uid="${PUID:-}"
gid="${PGID:-}"
umask_value="${UMASK:-}"

if [ -n "$umask_value" ]; then
  if ! umask "$umask_value" 2>/dev/null; then
    echo "[entrypoint] invalid UMASK '$umask_value'; using current process umask" >&2
  fi
fi

if [ -n "$uid" ] || [ -n "$gid" ]; then
  uid="${uid:-65532}"
  gid="${gid:-65532}"

  if [ "$(id -u)" -eq 0 ]; then
    mkdir -p /data
    chown -R "${uid}:${gid}" /data 2>/dev/null || true
    exec su-exec "${uid}:${gid}" "$APP_BIN" "$@"
  fi
fi

exec "$APP_BIN" "$@"
