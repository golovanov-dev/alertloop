#!/usr/bin/env bash
#
# Remove the AlertLoop Monit integration.
#
#   sudo ./uninstall.sh                 remove the scripts and this integration's rules
#   sudo ./uninstall.sh --purge         also remove /etc/alertloop/monit.env (the API key)
#   sudo ./uninstall.sh --yes           do not ask
#
# What it will not do:
#   - remove Monit
#   - remove a rule file you edited, without asking
#   - remove the API key unless you pass --purge
#   - remove events still waiting in the spool
#   - reload Monit on a configuration that does not validate
set -euo pipefail

BIN_DIR="${BIN_DIR:-/usr/local/bin}"
CONF_DIR="${CONF_DIR:-/etc/alertloop}"
ENV_FILE="$CONF_DIR/monit.env"
SPOOL_DIR="/var/lib/alertloop-monit/spool"
EXAMPLES_DIR="${EXAMPLES_DIR:-$CONF_DIR/monit-examples}"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PURGE=0
ASSUME_YES=0
MONIT_CONF_DIR=""

while [ $# -gt 0 ]; do
  case "$1" in
    --purge) PURGE=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    --monit-conf-dir) MONIT_CONF_DIR="${2-}"; shift 2 ;;
    -h|--help) sed -n '3,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "This must run as root (use sudo)." >&2
  exit 1
fi

confirm() {
  [ "$ASSUME_YES" -eq 1 ] && return 0
  printf '%s [y/N] ' "$1"
  read -r reply < /dev/tty || return 1
  case "$reply" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# --- Monit rules -----------------------------------------------------------
if [ -z "$MONIT_CONF_DIR" ]; then
  for d in /etc/monit/conf.d /etc/monit.d /usr/local/etc/monit.d; do
    if [ -d "$d" ]; then MONIT_CONF_DIR="$d"; break; fi
  done
fi

# remove_rule <file>: remove a rule file this integration installed, if it
# still matches the shipped example. An edited rule is the operator's work, and
# deleting somebody's tuned thresholds without asking is not a thing an
# uninstaller gets to do. 0 when removed.
remove_rule() {
  local f="$1" base original
  base="$(basename "$f")"
  base="${base#alertloop-}"
  base="${base%.disabled}"
  original="$SRC/conf.d/$base"
  if ! { [ -f "$original" ] && cmp -s "$f" "$original"; }; then
    echo "    $f differs from the shipped example (you edited it)"
    if ! confirm "    Remove it anyway?"; then
      echo "    kept"
      return 1
    fi
  fi
  rm -f "$f"
  echo "    removed $f"
}

# In conf.d: the rules you enabled, and *.conf.disabled copies left by a
# version before 0.8.0 (Monit on Debian and Ubuntu loads those too).
removed_rules=0
if [ -n "$MONIT_CONF_DIR" ] && [ -d "$MONIT_CONF_DIR" ]; then
  echo "==> Looking for this integration's rules in $MONIT_CONF_DIR"
  for f in "$MONIT_CONF_DIR"/alertloop-*.conf "$MONIT_CONF_DIR"/alertloop-*.conf.disabled; do
    [ -e "$f" ] || continue
    if remove_rule "$f"; then removed_rules=$((removed_rules + 1)); fi
  done
fi

# The examples directory, which Monit does not read.
if [ -d "$EXAMPLES_DIR" ]; then
  echo "==> Removing the example rules in $EXAMPLES_DIR"
  for f in "$EXAMPLES_DIR"/alertloop-*.conf "$EXAMPLES_DIR"/alertloop-*.conf.disabled; do
    [ -e "$f" ] || continue
    remove_rule "$f" || true
  done
  rmdir "$EXAMPLES_DIR" 2>/dev/null && echo "    removed empty $EXAMPLES_DIR"
fi

# --- spool -----------------------------------------------------------------
if [ -f "$ENV_FILE" ]; then
  line="$(grep '^ALERTLOOP_SPOOL_DIR=' "$ENV_FILE" | tail -n 1 || true)"
  line="${line#ALERTLOOP_SPOOL_DIR=}"
  case "$line" in
    \"*\"|\'*\') line="${line:1:${#line}-2}" ;;
  esac
  SPOOL_DIR="${line:-$SPOOL_DIR}"
fi
spooled() { find "$SPOOL_DIR" -maxdepth 1 -name '*.json' 2>/dev/null | wc -l | tr -d ' '; }
if [ -d "$SPOOL_DIR" ]; then
  if [ "$(spooled)" -gt 0 ] && [ -x "$BIN_DIR/alertloop-send" ]; then
    echo "==> Sending the $(spooled) event(s) waiting in $SPOOL_DIR"
    "$BIN_DIR/alertloop-send" --flush || true
  fi
  left="$(spooled)"
  rejected="$({ find "$SPOOL_DIR/rejected" -maxdepth 1 -name '*.json' 2>/dev/null || true; } | wc -l | tr -d ' ')"
  if [ "$rejected" -gt 0 ]; then
    echo "!! $rejected event(s) AlertLoop refused stay in $SPOOL_DIR/rejected." >&2
  fi
  if [ "$left" -gt 0 ]; then
    echo "!! $left event(s) could not be sent and stay in $SPOOL_DIR." >&2
    echo "!! Each file is the JSON body of POST /v1/events; delete the directory when done with them." >&2
  else
    rm -f "$SPOOL_DIR/.lock"
    rmdir "$SPOOL_DIR/rejected" 2>/dev/null || true
    rmdir "$SPOOL_DIR" 2>/dev/null && echo "    removed empty $SPOOL_DIR"
    rmdir "$(dirname "$SPOOL_DIR")" 2>/dev/null || true
  fi
fi

# --- scripts ---------------------------------------------------------------
echo "==> Removing scripts from $BIN_DIR"
for f in alertloop-send alertloop-monit check-worker-heartbeat.sh cron-wrapper.sh check-pm2.sh; do
  if [ -e "$BIN_DIR/$f" ]; then
    rm -f "$BIN_DIR/$f"
    echo "    removed $BIN_DIR/$f"
  fi
done

# --- configuration ---------------------------------------------------------
if [ "$PURGE" -eq 1 ]; then
  if [ -f "$ENV_FILE" ]; then
    if confirm "Remove $ENV_FILE (it holds the API key)?"; then
      rm -f "$ENV_FILE"
      echo "    removed $ENV_FILE"
      # Only if empty: /etc/alertloop may also hold the server's own config.
      if rmdir "$CONF_DIR" 2>/dev/null; then
        echo "    removed empty $CONF_DIR"
      fi
    else
      echo "    kept $ENV_FILE"
    fi
  fi
else
  if [ -f "$ENV_FILE" ]; then
    echo "==> Keeping $ENV_FILE (pass --purge to remove it)"
    echo "    Revoke the API key in AlertLoop's own config file too: removing this"
    echo "    file does not invalidate the key."
  fi
fi

# --- validate and reload ---------------------------------------------------
if command -v monit >/dev/null 2>&1; then
  echo "==> Checking the Monit configuration (monit -t)"
  if monit -t; then
    if [ "$removed_rules" -gt 0 ]; then
      echo "    configuration is valid. Apply it with:  sudo monit reload"
    else
      echo "    configuration is valid; no rules were removed."
    fi
  else
    echo
    echo "!! monit -t failed. NOT reloading." >&2
    echo "!! A rule may still reference a script this uninstaller removed." >&2
    echo "!! Fix the configuration, then run: sudo monit -t && sudo monit reload" >&2
    exit 1
  fi
fi

echo
echo "Done."
