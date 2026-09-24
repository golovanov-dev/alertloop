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
#   /usr/local/bin/check-pm2.sh               helper for PM2 checks
#   /usr/local/bin/cron-wrapper.sh            helper for cron jobs
#   /etc/alertloop/monit.env                  configuration (0600, root)
#   /var/lib/alertloop-monit/spool            events not sent yet (0700, root)
#   /etc/alertloop/monit-examples/            example Monit rules, ONLY if you ask
#
# What it does NOT do: enable any check. Every example names a pidfile, a port,
# or a threshold that belongs to your machine and not to this repository. An
# installer that switched them all on would page you about a PostgreSQL you do
# not run. The examples go to a directory Monit does not read; you copy the
# ones you want into Monit's conf.d and edit them there.
set -euo pipefail

BIN_DIR="${BIN_DIR:-/usr/local/bin}"
CONF_DIR="${CONF_DIR:-/etc/alertloop}"
ENV_FILE="$CONF_DIR/monit.env"
SPOOL_DIR="${SPOOL_DIR:-/var/lib/alertloop-monit/spool}"
EXAMPLES_DIR="${EXAMPLES_DIR:-$CONF_DIR/monit-examples}"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

WITH_EXAMPLES=0
MONIT_CONF_DIR=""

usage() {
  cat <<'USAGE'
usage: sudo ./install.sh [--with-examples] [--monit-conf-dir DIR]

  --with-examples       also copy the example Monit rules to
                        /etc/alertloop/monit-examples, which Monit does not
                        read; nothing takes effect until you copy one into
                        Monit's conf.d.
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
# Everything alertloop-send refuses to run without. Checked before anything is
# installed: an adapter installed without them exits 2 on every event, and
# Monit's alerts are lost with nothing but a log line to say so.
missing=""
for cmd in curl jq flock; do
  command -v "$cmd" >/dev/null 2>&1 || missing="$missing $cmd"
done
if [ -n "$missing" ]; then
  echo "Missing required commands:$missing (flock comes with util-linux)" >&2
  echo "Nothing was installed." >&2
  echo >&2
  echo "  Debian/Ubuntu:   apt-get install -y curl jq util-linux" >&2
  echo "  RHEL/Alma/Rocky: dnf install -y curl jq util-linux" >&2
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
install -m 0755 "$SRC/examples/check-pm2.sh"               "$BIN_DIR/check-pm2.sh"

# --- configuration ---------------------------------------------------------
install -d -m 0755 "$CONF_DIR"
if [ -f "$ENV_FILE" ]; then
  # Never overwrite: this file holds the API key, and a re-run of the installer
  # must not be the thing that silently breaks a working install.
  echo "==> Keeping existing $ENV_FILE"
  # Settings added since that file was written. All optional: without a line
  # the default applies, so nothing has to change for the upgrade to work.
  new_keys=""
  while IFS= read -r key; do
    grep -q "^$key=" "$ENV_FILE" || new_keys="$new_keys $key"
  done < <(sed -n 's/^\(ALERTLOOP_[A-Z_]*\)=.*/\1/p' "$SRC/alertloop.env.example")
  if [ -n "$new_keys" ]; then
    echo "    Optional settings this version reads that the file does not set:"
    echo "     $new_keys"
    echo "    Their defaults apply (see $SRC/alertloop.env.example). To change one,"
    echo "    add its line once."
  fi
else
  echo "==> Creating $ENV_FILE"
  install -m 0600 -o root -g root "$SRC/alertloop.env.example" "$ENV_FILE"
fi

# Correct the mode even on an existing file: the adapter warns about a loose one
# every time it runs, and an alert channel that prints a warning on every event
# trains people to ignore its output.
chmod 0600 "$ENV_FILE"
chown root:root "$ENV_FILE" 2>/dev/null || true

# --- spool -----------------------------------------------------------------
# Owned by root, as Monit runs the adapter as root. A different
# ALERTLOOP_SPOOL_DIR in monit.env is created by the adapter on its first run.
echo "==> Creating $SPOOL_DIR"
install -d -m 0700 -o root -g root "$SPOOL_DIR"
chmod 0700 "$SPOOL_DIR"

# --- Monit rules -----------------------------------------------------------
if [ -z "$MONIT_CONF_DIR" ]; then
  for d in /etc/monit/conf.d /etc/monit.d /usr/local/etc/monit.d; do
    if [ -d "$d" ]; then MONIT_CONF_DIR="$d"; break; fi
  done
fi

# Before 0.8.0 the examples were copied into conf.d as *.conf.disabled. Monit
# on Debian and Ubuntu includes conf.d/* - every file, suffix or not - so those
# copies were live checks. Take ours out: only the names this integration
# ships, removed when unchanged, moved to the examples directory otherwise.
removed_disabled=0
if [ -n "$MONIT_CONF_DIR" ] && [ -d "$MONIT_CONF_DIR" ]; then
  for f in "$SRC"/conf.d/*.conf; do
    old="$MONIT_CONF_DIR/alertloop-$(basename "$f").disabled"
    [ -f "$old" ] || continue
    if [ "$removed_disabled" -eq 0 ]; then
      echo "==> Removing example rules an earlier version put in $MONIT_CONF_DIR"
      echo "    (Monit loads *.conf.disabled there as well; they were active checks)"
    fi
    removed_disabled=$((removed_disabled + 1))
    if cmp -s "$old" "$f"; then
      rm -f "$old"
      echo "    removed $old"
    else
      install -d -m 0755 "$EXAMPLES_DIR"
      mv -f "$old" "$EXAMPLES_DIR/"
      echo "    moved $old to $EXAMPLES_DIR/ (it differs from the shipped example)"
    fi
  done
fi

if [ "$WITH_EXAMPLES" -eq 1 ]; then
  echo "==> Copying example rules to $EXAMPLES_DIR (Monit does not read it)"
  install -d -m 0755 "$EXAMPLES_DIR"
  copied=0
  for f in "$SRC"/conf.d/*.conf; do
    install -m 0644 "$f" "$EXAMPLES_DIR/alertloop-$(basename "$f")"
    copied=$((copied + 1))
  done
  echo "    $copied rules. None is active. To enable one, copy it into"
  echo "    Monit's conf.d, edit it there, validate and reload:"
  echo "      sudo install -m 0600 $EXAMPLES_DIR/alertloop-postgresql.conf \\"
  echo "              ${MONIT_CONF_DIR:-/etc/monit/conf.d}/"
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
    if [ "$removed_disabled" -gt 0 ]; then
      echo "    Until then Monit keeps running the $removed_disabled example rule(s) removed above."
    fi
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
         - key: "<output of: openssl rand -hex 32>"
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

  5. Enable the checks you actually want: copy them from $EXAMPLES_DIR
     (install.sh --with-examples) into Monit's conf.d and edit them there
     (see README.md), then:

       sudo monit -t && sudo monit reload

STEPS
