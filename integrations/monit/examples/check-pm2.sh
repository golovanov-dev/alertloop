#!/usr/bin/env bash
#
# check-pm2.sh - is a PM2-managed application healthy?
#
#   check-pm2.sh <pm2-user> <app-name> [pm2-binary]
#
# <pm2-user> is the user PM2 ALREADY runs as on this machine. You do not create
# an account for this check, and on a small server the answer is usually `root`.
# Find it:
#
#   systemctl list-units --type=service | grep -i pm2   # -> pm2-root.service
#   ps -eo user:20,args | grep '[P]M2.*God Daemon'      # -> user, and PM2_HOME
#   ls -ld /root/.pm2 /home/*/.pm2
#
# It is whoever runs your deploy script, because that is who ran `pm2 start`.
#
# Exits 0 if the app is online and PM2 has not restarted it since the last run.
# Monit runs it as a `check program` and reacts to the exit code; see
# conf.d/pm2.conf.
#
# Why this exists, when you already have an HTTP check: PM2 restarts a crashing
# app in seconds. If it crashes and comes back faster than the HTTP check's
# `for 3 cycles` window - 90 seconds at Monit's default cycle - the HTTP check
# never fires, and an app losing every request it receives reads as healthy.
# `max_memory_restart` is the same story without even a crash: PM2 kills and
# revives the process silently, forever, and nothing anywhere says so. The
# restart counter is the only place either one shows up.
#
# THE TRAP, and it catches everyone exactly once: PM2 keeps one daemon per user,
# under $HOME/.pm2, and `pm2 jlist` only ever sees the daemon of whoever asks.
# Monit runs as root, so a check that just calls `pm2` sees root's daemon - and
# if your apps run as someone else, that daemon is empty and the check reads it
# as "everything is down". Hence the mandatory <pm2-user> argument, and hence
# PM2_HOME being set explicitly below rather than left to $HOME: under Monit,
# root's HOME is not always what you would expect, and `//.pm2` is not a useful
# place to look for a process list.
#
# THE SECOND TRAP, if you installed Node with nvm: pm2 then lives in
# ~/.nvm/versions/node/<version>/bin, which is put on PATH by ~/.bashrc - and a
# login shell started by `su -s /bin/sh -` reads ~/.profile, not ~/.bashrc. So
# `pm2` may not be found even though it works fine when you log in yourself.
# Find the real path once and pass it as the third argument:
#
#   su - <user> -c 'command -v pm2'
#
# Exit codes: 0 healthy, 1 not online or unknown to PM2, 2 restarted since the
# last run, 3 PM2 unreachable or bad usage. Monit only distinguishes zero from
# non-zero - but it records this script's OUTPUT and puts it in the alert, so
# the incident message says which of the three it was.
set -uo pipefail

STATE_DIR="${ALERTLOOP_STATE_DIR:-/var/lib/alertloop-monit}"

user="${1:-}"
app="${2:-}"
pm2_bin="${3:-pm2}"

if [ -z "$user" ] || [ -z "$app" ]; then
  echo "usage: check-pm2.sh <pm2-user> <app-name> [pm2-binary]" >&2
  exit 3
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is not installed"
  exit 3
fi

home="$(getent passwd "$user" 2>/dev/null | cut -d: -f6)"
if [ -z "$home" ]; then
  echo "no such user: $user"
  exit 3
fi

pm2_home="${ALERTLOOP_PM2_HOME:-$home/.pm2}"
if [ ! -d "$pm2_home" ]; then
  # Said plainly, because this is the mistake people actually make: the user is
  # wrong, not missing. Nobody needs to create an account for this check.
  echo "no PM2 home at $pm2_home - PM2 does not run as '$user' on this machine"
  exit 3
fi

# Skip the su when Monit already runs as the right user - which is the common
# case, because PM2 on a small server usually runs as root. `-` for a login
# shell otherwise, and `-s /bin/sh` because a deploy user's login shell is often
# /usr/sbin/nologin, which would fail for a reason unrelated to the app.
if [ "$(id -un 2>/dev/null)" = "$user" ]; then
  raw="$(PM2_HOME="$pm2_home" "$pm2_bin" jlist 2>/dev/null)"
else
  raw="$(su -s /bin/sh - "$user" -c "PM2_HOME='$pm2_home' $pm2_bin jlist" 2>/dev/null)"
fi

if [ -z "$raw" ]; then
  echo "'$pm2_bin jlist' produced nothing as $user (PM2_HOME=$pm2_home) - missing pm2 binary, or dead PM2 daemon"
  exit 3
fi

# PM2 sometimes prints an update notice before the JSON. `jlist` emits its array
# on a single line, so take the first line that starts one.
json="$(printf '%s\n' "$raw" | grep -m1 '^\[')"
if [ -z "$json" ] || ! printf '%s' "$json" | jq -e . >/dev/null 2>&1; then
  echo "'$pm2_bin jlist' as $user returned no usable JSON"
  exit 3
fi

status="$(printf '%s' "$json" | jq -r --arg n "$app" \
  'map(select(.name == $n)) | if length == 0 then "missing" else (.[0].pm2_env.status // "unknown") end')"

if [ "$status" = "missing" ]; then
  echo "$app: not in PM2's process list for user $user"
  exit 1
fi

if [ "$status" != "online" ]; then
  echo "$app: PM2 status is '$status'"
  exit 1
fi

restarts="$(printf '%s' "$json" | jq -r --arg n "$app" \
  'map(select(.name == $n)) | (.[0].pm2_env.restart_time // 0)')"
case "$restarts" in
  ''|*[!0-9]*) echo "$app: online, but PM2 reported no usable restart counter"; exit 3 ;;
esac

state_id="$(printf '%s' "${user}-${app}" | tr -c 'A-Za-z0-9._-' '-' | tr -s '-')"
state_file="$STATE_DIR/pm2-${state_id}.restarts"

mkdir -p "$STATE_DIR" 2>/dev/null || true
previous=""
[ -f "$state_file" ] && previous="$(cat "$state_file" 2>/dev/null)"
printf '%s\n' "$restarts" > "$state_file" 2>/dev/null || true

# The first run seeds the file and reports healthy. A check that alerts the
# moment it is installed teaches you to ignore it on day one.
case "$previous" in
  ''|*[!0-9]*)
    echo "$app: online (restart counter $restarts recorded, first run)"
    exit 0
    ;;
esac

# PM2 resets the counter on `pm2 reset` and when its daemon restarts, so a DROP
# is not an error. Only growth is.
if [ "$restarts" -gt "$previous" ]; then
  echo "$app: online, but PM2 restarted it $((restarts - previous)) time(s) since the last check (total $restarts)"
  exit 2
fi

echo "$app: online, no restarts since the last check (total $restarts)"
exit 0
