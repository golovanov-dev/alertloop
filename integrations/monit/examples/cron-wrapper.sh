#!/usr/bin/env bash
#
# cron-wrapper.sh - run a cron job and report whether it worked.
#
#   cron-wrapper.sh <job-name> <command> [args...]
#
# In the crontab:
#   30 3 * * * app /usr/local/bin/cron-wrapper.sh daily-import /opt/app/bin/import.sh
#
# What it does:
#   1. runs the command, capturing its output
#   2. on FAILURE  - reports `firing` to AlertLoop with the last lines of output
#   3. on SUCCESS  - touches the freshness file and reports `resolved`, closing
#                    an incident from a previous failed run
#   4. exits with the job's OWN exit code, so cron's mail and any surrounding
#      tooling behave exactly as they did before
#
# The freshness file is the other half of the story. This wrapper can only speak
# when the job RUNS. It cannot tell you the job never started - because nothing
# started to tell you. conf.d/cron-freshness.conf watches the file's age and
# catches that case. Use both.
set -uo pipefail

JOB_NAME="${1:-}"
shift || true

SEND="${ALERTLOOP_SEND:-/usr/local/bin/alertloop-send}"
HEARTBEAT_DIR="${ALERTLOOP_HEARTBEAT_DIR:-/var/lib/app-heartbeats}"
# How much of the job's output to include. Enough to see the error, not so much
# that a job looping on stack traces posts a megabyte.
OUTPUT_LINES="${ALERTLOOP_OUTPUT_LINES:-20}"

if [ -z "$JOB_NAME" ] || [ $# -eq 0 ]; then
  echo "usage: cron-wrapper.sh <job-name> <command> [args...]" >&2
  exit 2
fi

host="${ALERTLOOP_HOST_OVERRIDE:-$(hostname -s 2>/dev/null || hostname 2>/dev/null || echo unknown)}"
sanitize() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '-' | tr -s '-'; }
host_id="$(sanitize "$host")"
job_id="$(sanitize "$JOB_NAME")"
dedupe_key="${host_id}:cron:${job_id}:exit-code"

output_file="$(mktemp)"
# shellcheck disable=SC2064  # expand now: $output_file must not change later
trap "rm -f '$output_file'" EXIT

started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
start_epoch="$(date +%s)"

# Run the job, capturing its output, then replay it on stdout so cron's own
# mail still contains it and nothing an operator relied on disappears.
#
# Written this way rather than with `tee` in a process substitution: the shell
# does not wait for a process substitution to finish, so `tail` below could read
# the file before `tee` had flushed and the alert would carry no output at all.
# Cron output is not read live, so buffering it costs nothing.
"$@" > "$output_file" 2>&1
rc=$?
cat "$output_file"

duration=$(( $(date +%s) - start_epoch ))

if [ "$rc" -eq 0 ]; then
  # Record the success first. If AlertLoop is unreachable, the freshness file is
  # still correct and cron-freshness.conf will not raise a false alarm.
  mkdir -p "$HEARTBEAT_DIR" 2>/dev/null || true
  touch "${HEARTBEAT_DIR}/${JOB_NAME}.ok" 2>/dev/null || \
    echo "cron-wrapper: could not touch ${HEARTBEAT_DIR}/${JOB_NAME}.ok" >&2

  # Close an incident from a previous failure. When there is none this is a
  # no-op that AlertLoop answers with 204, so it is safe to send every time.
  "$SEND" --status resolved --dedupe-key "$dedupe_key" >/dev/null 2>&1 || \
    echo "cron-wrapper: could not report the recovery for $JOB_NAME" >&2
  exit 0
fi

# Failure. The job's output is the most useful thing in the alert, and also the
# most likely place for a connection string to appear - keep it short and do not
# add anything the job did not print itself.
tail_output="$(tail -n "$OUTPUT_LINES" "$output_file" 2>/dev/null || echo '(no output captured)')"

"$SEND" \
  --status firing \
  --severity critical \
  --event-type job_failed \
  --host "$host_id" \
  --service "cron-${job_id}" \
  --dedupe-key "$dedupe_key" \
  --title "Cron job failed: ${JOB_NAME}" \
  --message "exit code ${rc} after ${duration}s
${tail_output}" \
  --label "job=${job_id}" \
  --metadata "exit_code=${rc}" \
  --metadata "duration_seconds=${duration}" \
  --metadata "started_at=${started}" \
  >/dev/null 2>&1 || echo "cron-wrapper: could not report the failure of $JOB_NAME" >&2

# The job's own exit code, unchanged. A wrapper that swallows it would break
# every `&&` and every retry that depends on it.
exit "$rc"
