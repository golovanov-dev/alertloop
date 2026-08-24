#!/usr/bin/env bash
#
# check-worker-heartbeat.sh - is a worker still getting work done?
#
#   check-worker-heartbeat.sh <heartbeat-file> <max-age-seconds>
#
# Exits 0 if the file was touched within max-age-seconds, non-zero otherwise.
# Monit runs it as a `check program` and reacts to the exit code; see
# conf.d/worker-heartbeat.conf.
#
# Why a file: a worker's PID proves nothing. A worker deadlocked on a lock, or
# looping on a message it can never process, keeps its process alive forever.
# What proves it is working is that it recently finished something.
#
# The worker's side is one line at the end of each unit of work:
#
#   touch /var/lib/app-heartbeats/payment-worker
#
# Do it AFTER the work succeeds, not at the top of the loop - a worker that
# touches the file and then hangs looks perfectly healthy.
#
# Exit codes: 0 fresh, 1 stale, 2 missing, 3 bad usage. Monit only distinguishes
# zero from non-zero, but a human running this by hand gets to see which.
set -euo pipefail

file="${1:-}"
max_age="${2:-300}"

if [ -z "$file" ]; then
  echo "usage: check-worker-heartbeat.sh <heartbeat-file> <max-age-seconds>" >&2
  exit 3
fi
case "$max_age" in
  ''|*[!0-9]*) echo "max-age-seconds must be a whole number, got: $max_age" >&2; exit 3 ;;
esac

if [ ! -e "$file" ]; then
  # Missing is not the same as stale, and the difference matters when you are
  # reading the alert at 3am: it usually means the worker has never run since
  # this check was installed, or the path is wrong.
  echo "heartbeat missing: $file"
  exit 2
fi

now="$(date +%s)"
# GNU stat first, then BSD/macOS. Nothing else is worth carrying.
mtime="$(stat -c %Y "$file" 2>/dev/null || stat -f %m "$file" 2>/dev/null || echo '')"
if [ -z "$mtime" ]; then
  echo "cannot read the modification time of $file"
  exit 3
fi

age=$(( now - mtime ))
if [ "$age" -gt "$max_age" ]; then
  # One short line: Monit puts it in MONIT_DESCRIPTION, which becomes the
  # message of the AlertLoop incident. It must be readable on a phone and must
  # not contain a database password or a connection string.
  echo "heartbeat is ${age}s old, limit ${max_age}s"
  exit 1
fi

echo "heartbeat is ${age}s old, within ${max_age}s"
exit 0
