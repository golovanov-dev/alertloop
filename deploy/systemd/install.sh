#!/usr/bin/env bash
# Install AlertLoop as a systemd service from a prebuilt binary.
# Usage: sudo ./install.sh /path/to/alertloop
set -euo pipefail

BINARY="${1:-./alertloop}"
PREFIX="/usr/local/bin"
CONF_DIR="/etc/alertloop"
DATA_DIR="/var/lib/alertloop"
SERVICE_SRC="$(dirname "$0")/alertloop.service"
CONF_SRC="$(dirname "$0")/../../alertloop.example.yaml"

if [[ $EUID -ne 0 ]]; then
  echo "This installer must run as root (use sudo)." >&2
  exit 1
fi
if [[ ! -f "$BINARY" ]]; then
  echo "Binary not found: $BINARY" >&2
  echo "Usage: sudo ./install.sh /path/to/alertloop" >&2
  exit 1
fi

echo "==> Creating alertloop system user"
id alertloop &>/dev/null || useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin alertloop

echo "==> Installing binary to $PREFIX/alertloop"
install -m 0755 "$BINARY" "$PREFIX/alertloop"

echo "==> Creating directories"
install -d -o alertloop -g alertloop "$DATA_DIR"
install -d "$CONF_DIR"

if [[ ! -f "$CONF_DIR/alertloop.yaml" ]]; then
  echo "==> Installing example config to $CONF_DIR/alertloop.yaml (edit before starting)"
  install -m 0640 "$CONF_SRC" "$CONF_DIR/alertloop.yaml"
else
  echo "==> Keeping existing $CONF_DIR/alertloop.yaml"
fi

echo "==> Installing systemd unit"
install -m 0644 "$SERVICE_SRC" /etc/systemd/system/alertloop.service
systemctl daemon-reload

echo
echo "AlertLoop installed. Next steps:"
echo "  1. Edit $CONF_DIR/alertloop.yaml (set admin_token, api_keys, channels)."
echo "  2. systemctl enable --now alertloop"
echo "  3. Open http://localhost:8080/swagger"
