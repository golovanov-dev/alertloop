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
ENV_FILE="$CONF_DIR/alertloop.env"

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

# One previous copy of the binary and the unit, for going back after an upgrade
# (OPERATIONS.md, "Upgrades and downgrades"). Re-running with the same files
# keeps the copy of the older version.
keep_previous() {
  if [[ -f "$2" ]] && ! cmp -s "$1" "$2"; then
    cp -p "$2" "$2.prev"
    echo "    previous version kept as $2.prev"
  fi
}

echo "==> Installing binary to $PREFIX/alertloop"
keep_previous "$BINARY" "$PREFIX/alertloop"
install -m 0755 "$BINARY" "$PREFIX/alertloop"

echo "==> Creating directories"
install -d -o alertloop -g alertloop "$DATA_DIR"
install -d "$CONF_DIR"

if [[ ! -f "$CONF_DIR/alertloop.yaml" ]]; then
  echo "==> Installing example config to $CONF_DIR/alertloop.yaml (edit before starting)"
  # Owned by the service user: the unit runs as User=alertloop, so a root-owned
  # 0640 config would fail to start with "permission denied".
  install -m 0640 -o alertloop -g alertloop "$CONF_SRC" "$CONF_DIR/alertloop.yaml"
else
  echo "==> Keeping existing $CONF_DIR/alertloop.yaml"
fi

# The config references ${ALERTLOOP_ADMIN_TOKEN} with no fallback, so the
# service cannot start until the variable exists. Generating it here is the
# difference between an install that works and one that stops with an error the
# operator has to go and read about. The token is never printed: it would land
# in shell history and in the terminal scrollback of whoever ran the installer.
if [[ ! -f "$ENV_FILE" ]]; then
  echo "==> Generating an admin token in $ENV_FILE"
  if command -v openssl >/dev/null 2>&1; then
    token="$(openssl rand -hex 32)"
  else
    token="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  fi
  umask 077
  printf '# Secrets for AlertLoop. Read by systemd, referenced from alertloop.yaml.\n' > "$ENV_FILE"
  printf 'ALERTLOOP_ADMIN_TOKEN=%s\n' "$token" >> "$ENV_FILE"
  chown alertloop:alertloop "$ENV_FILE"
  chmod 0640 "$ENV_FILE"
  unset token
else
  echo "==> Keeping existing $ENV_FILE"
fi

echo "==> Installing systemd unit"
keep_previous "$SERVICE_SRC" /etc/systemd/system/alertloop.service
install -m 0644 "$SERVICE_SRC" /etc/systemd/system/alertloop.service
systemctl daemon-reload

echo
echo "AlertLoop installed. Next steps:"
echo "  1. Edit $CONF_DIR/alertloop.yaml (api_keys, channels, routing)."
echo "     The admin token is already set, in $ENV_FILE — read it with:"
echo "       sudo grep ALERTLOOP_ADMIN_TOKEN $ENV_FILE"
echo "  2. systemctl enable --now alertloop"
echo "  3. Open http://localhost:8080/swagger"
