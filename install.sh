#!/usr/bin/env bash
# rempkg installer: builds the Go binary and installs it.
#
# Usage:
#   ./install.sh
#   PREFIX=/usr/local ./install.sh
#   BIN_DIR=~/.local/bin ./install.sh
#
# Env:
#   PREFIX   install prefix (default: /usr/local)
#   BIN_DIR  override binary directory (default: $PREFIX/bin)
#   DATA_DIR tracking dir (default: ~/rempkg of the invoking user,
#            even when this script is run with sudo)

set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BIN_DIR="${BIN_DIR:-$PREFIX/bin}"
# Resolve the invoking user's home even under sudo.
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  _owner_home="$(getent passwd "$SUDO_USER" | cut -d: -f6 || true)"
  [ -n "$_owner_home" ] || _owner_home="/home/$SUDO_USER"
else
  _owner_home="$HOME"
fi
DATA_DIR="${DATA_DIR:-$_owner_home/rempkg}"

cd "$(dirname "$0")"

if ! command -v go >/dev/null 2>&1; then
  echo "error: go toolchain not found. Install Go >= 1.21 first." >&2
  exit 1
fi

echo "==> building rempkg..."
go vet ./...
go build -trimpath -ldflags="-s -w" -o rempkg .

echo "==> installing to $BIN_DIR/rempkg..."
mkdir -p "$BIN_DIR"
if [ -w "$BIN_DIR" ]; then
  install -m 0755 rempkg "$BIN_DIR/rempkg"
else
  sudo install -m 0755 rempkg "$BIN_DIR/rempkg"
fi

echo "==> ensuring tracking dir $DATA_DIR..."
mkdir -p "$DATA_DIR"
[ -f "$DATA_DIR/packages.jsonc" ] || printf '// rempkg tracked packages.\n[]\n' > "$DATA_DIR/packages.jsonc"
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  chown -R "$SUDO_USER" "$DATA_DIR" 2>/dev/null || true
fi

echo "==> done. Try: rempkg --help"
