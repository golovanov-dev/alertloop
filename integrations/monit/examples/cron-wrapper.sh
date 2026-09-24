#!/usr/bin/env bash
#
# cron-wrapper.sh - run a cron job and report whether it worked.
#
#   cron-wrapper.sh <job-name> <command> [args...]
#
# In /etc/cron.d, as root; the job itself as its own user, through runuser:
#   30 3 * * * root /usr/local/bin/cron-wrapper.sh daily-import /usr/sbin/runuser -u app -- /opt/app/bin/import.sh
#
# The wrapper runs as root because alertloop-send reads the API key from
# /etc/alertloop/monit.env, which only root can read. Run as another user, it
# cannot report anything; it then says so on stderr and in syslog.
#
# What it does:
#   1. runs the command, capturing its output
#   2. on FAILURE  - reports `firing` to AlertLoop with the last lines of output
#   3. on SUCCESS  - touches the freshness file and, when the previous run
#                    failed or is unknown, reports `resolved`, closing its
#                    incident
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
# One file per job with the result of its last run: `resolved` is sent only on
# the success that follows a failure, or when there is no record yet. Sent on
# every success, it would reach AlertLoop's spool each time AlertLoop is down,
# and a bounded spool full of empty recoveries drops the `firing` that matter.
STATE_DIR="${ALERTLOOP_CRON_STATE_DIR:-/var/lib/alertloop-monit/cron}"
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
state_file="${STATE_DIR}/${job_id}.last"

# warn <text>: to stderr, which ends up in a cron mail few servers deliver,
# and to syslog.
warn() {
  echo "cron-wrapper: $1" >&2
  if command -v logger >/dev/null 2>&1; then
    logger -t cron-wrapper -p user.err "$1" || true
  fi
}

# set_state <ok|failed>: record the result of this run, whole or not at all.
set_state() {
  { mkdir -p "$STATE_DIR" && printf '%s\n' "$1" > "$state_file.tmp.$$" \
    && mv -f "$state_file.tmp.$$" "$state_file"; } 2>/dev/null && return 0
  rm -f "$state_file.tmp.$$" 2>/dev/null
  return 1
}

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
    warn "could not touch ${HEARTBEAT_DIR}/${JOB_NAME}.ok"

  # Close the incident of a previous failure. Without a record - the first run
  # of this wrapper for the job, or a state directory this user cannot write -
  # send it too: a stray recovery costs a 204, a lost one leaves an incident
  # open. `ok` is recorded only once the recovery is sent or spooled (0 or 9);
  # otherwise the next success tries again.
  if [ "$(cat "$state_file" 2>/dev/null)" != ok ]; then
    send_rc=0
    "$SEND" --status resolved --dedupe-key "$dedupe_key" >/dev/null 2>&1 || send_rc=$?
    case "$send_rc" in
      0|9) set_state ok || true ;;
      *) warn "could not report the recovery for $JOB_NAME (exit $send_rc)" ;;
    esac
  fi
  exit 0
fi

# Record the failure before reporting it, so the next success resolves it.
if ! set_state failed; then
  rm -f "$state_file" 2>/dev/null
  warn "could not write $state_file; the next success reports resolved anyway"
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
  >/dev/null 2>&1 || warn "could not report the failure of $JOB_NAME (exit $?)"

# The job's own exit code, unchanged. A wrapper that swallows it would break
# every `&&` and every retry that depends on it.
exit "$rc"
