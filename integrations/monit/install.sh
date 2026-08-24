#!/usr/bin/env bash
#
# Install the AlertLoop Monit integration.
#
#   sudo ./install.sh
#
# What it installs:
#   /usr/local/bin/alertloop-send             the adapter
#   /usr/local/bin/alertloop-monit            the Monit glue
#   /usr/local/bin/check-worker-heartbeat.sh  helper for worker checks
#   /usr/local/bin/cron-wrapper.sh            helper for cron jobs
#   /etc/alertloop/monit.env                  configuration (0600, root)
#   /etc/monit/conf.d/alertloop-*.conf        Monit rules, ONLY if you ask
#
# What it does NOT do: enable any check. Every example names a pidfile, a port,
# or a threshold that belongs to your machine and not to this repository. An
# installer that switched them all on would page you about a PostgreSQL you do
# not run. Copy the ones you want with --with-examples, then edit them.
set -euo pipefail

BIN_DIR="${BIN_DIR:-/usr/local/bin}"
CONF_DIR="${CONF_DIR:-/etc/alertloop}"
ENV_FILE="$CONF_DIR/monit.env"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

WITH_EXAMPLES=0
MONIT_CONF_DIR=""

usage() {
  cat <<'USAGE'
usage: sudo ./install.sh [--with-examples] [--monit-conf-dir DIR]

  --with-examples       also copy the example Monit rules, disabled, as
                        alertloop-*.conf.disabled. You rename them after
                        editing; nothing takes effect until you do.
  --monit-conf-dir DIR  where Monit reads its configuration from. Detected
                        automatically (/etc/monit/conf.d on Debian and Ubuntu,
                        /etc/monit.d on the RHEL family).
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --with-examples) WITH_EXAMPLES=1; shift ;;
    --monit-conf-dir) MONIT_CONF_DIR="${2-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "This installer must run as root (use sudo)." >&2
  exit 1
fi

# --- dependencies ----------------------------------------------------------
missing=""
for cmd in curl jq; do
  command -v "$cmd" >/dev/null 2>&1 || missing="$missing $cmd"
done
if [ -n "$missing" ]; then
  echo "Missing required commands:$missing" >&2
  echo >&2
  echo "  Debian/Ubuntu:  apt-get install -y curl jq" >&2
  echo "  RHEL/Alma/Rocky: dnf install -y curl jq" >&2
  exit 1
fi

if ! command -v monit >/dev/null 2>&1; then
  # Not fatal: installing the adapter before Monit is a legitimate order.
  echo "Note: monit is not installed. The adapter will still work; install Monit"
  echo "      before enabling any rules."
  echo "        Debian/Ubuntu:  apt-get install -y monit"
  echo "        RHEL family:    dnf install -y monit   (needs EPEL)"
fi

# --- binaries --------------------------------------------------------------
echo "==> Installing to $BIN_DIR"
install -m 0755 "$SRC/alertloop-send"  "$BIN_DIR/alertloop-send"
install -m 0755 "$SRC/alertloop-monit" "$BIN_DIR/alertloop-monit"
install -m 0755 "$SRC/examples/check-worker-heartbeat.sh" "$BIN_DIR/check-worker-heartbeat.sh"
install -m 0755 "$SRC/examples/cron-wrapper.sh"           "$BIN_DIR/cron-wrapper.sh"

# --- configuration ---------------------------------------------------------
install -d -m 0755 "$CONF_DIR"
if [ -f "$ENV_FILE" ]; then
  # Never overwrite: this file holds the API key, and a re-run of the installer
  # must not be the thing that silently breaks a working install.
  echo "==> Keeping existing $ENV_FILE"
else
  echo "==> Creating $ENV_FILE"
  install -m 0600 -o root -g root "$SRC/alertloop.env.example" "$ENV_FILE"
fi

# Correct the mode even on an existing file: the adapter warns about a loose one
# every time it runs, and an alert channel that prints a warning on every event
# trains people to ignore its output.
chmod 0600 "$ENV_FILE"
chown root:root "$ENV_FILE" 2>/dev/null || true

# --- Monit rules -----------------------------------------------------------
if [ -z "$MONIT_CONF_DIR" ]; then
  for d in /etc/monit/conf.d /etc/monit.d /usr/local/etc/monit.d; do
    if [ -d "$d" ]; then MONIT_CONF_DIR="$d"; break; fi
  done
fi

if [ "$WITH_EXAMPLES" -eq 1 ]; then
  if [ -z "$MONIT_CONF_DIR" ] || [ ! -d "$MONIT_CONF_DIR" ]; then
    echo "Could not find Monit's conf.d directory. Pass --monit-conf-dir DIR." >&2
    exit 1
  fi
  echo "==> Copying example rules to $MONIT_CONF_DIR (DISABLED)"
  for f in "$SRC"/conf.d/*.conf; do
    name="alertloop-$(basename "$f")"
    target="$MONIT_CONF_DIR/${name}.disabled"
    if [ -f "$MONIT_CONF_DIR/$name" ] || [ -f "$target" ]; then
      echo "    skip $name (already present)"
      continue
    fi
    install -m 0600 "$f" "$target"
    echo "    $target"
  done
  echo
  echo "    They are inert until you rename one, so a copy cannot page you about"
  echo "    a service this machine does not run:"
  echo "      sudo mv $MONIT_CONF_DIR/alertloop-postgresql.conf.disabled \\"
  echo "              $MONIT_CONF_DIR/alertloop-postgresql.conf"
fi

# --- validate --------------------------------------------------------------
# Never reload Monit on a configuration it rejects: a failed reload can leave
# monitoring off, and monitoring that is off is worse than monitoring that is
# noisy, because nothing tells you.
if command -v monit >/dev/null 2>&1; then
  echo "==> Checking the Monit configuration (monit -t)"
  if monit -t; then
    echo "    configuration is valid"
    echo
    echo "    Nothing was reloaded. Apply it when you are ready:"
    echo "      sudo monit reload"
  else
    echo
    echo "!! monit -t failed. NOT reloading Monit." >&2
    echo "!! Existing monitoring keeps running on the previous configuration." >&2
    exit 1
  fi
fi

# --- next steps ------------------------------------------------------------
cat <<STEPS

Installed.

Next:

  1. Create an AlertLoop API key with the 'ingest' scope for this server, in
     AlertLoop's config file:

       api_keys:
         - key: "\$(openssl rand -hex 32)"
           scope: ingest

  2. Put the URL and that key in $ENV_FILE
     (it is mode 0600 and owned by root; the key is never printed by anything
     in this integration).

  3. Check it without sending anything real:

       sudo alertloop-send --status firing --severity info \\
         --event-type test --dedupe-key "\$(hostname -s):install:test" \\
         --title "Install check" --message "from install.sh" --dry-run

  4. Send one real event and look for it in AlertLoop:

       sudo alertloop-send --status firing --severity info \\
         --event-type test --dedupe-key "\$(hostname -s):install:test" \\
         --title "Install check" --message "from install.sh"
       sudo alertloop-send --status resolved \\
         --dedupe-key "\$(hostname -s):install:test"

  5. Enable the checks you actually want (see README.md), then:

       sudo monit -t && sudo monit reload

STEPS
