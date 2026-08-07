#!/bin/sh
set -eu
DUALMEM_ENV_FILE="${DUALMEM_ENV_FILE:-$HOME/.config/dualmem/env}"
if [ -r "$DUALMEM_ENV_FILE" ]; then
  set -a
  . "$DUALMEM_ENV_FILE"
  set +a
fi
DUALMEM_BIN="${DUALMEM_BIN:-$HOME/go/bin/dualmem}"
exec "$DUALMEM_BIN" "$@"
