#!/usr/bin/env bash
#
# Tests for alertloop-send and alertloop-monit.
#
#   integrations/monit/test/run-tests.sh
#
# Needs bash, curl, jq, and python3 (for the mock AlertLoop). No test framework:
# a shell script that tests a shell script should not need one installed first.
#
# What is covered: argument validation, payload construction and escaping,
# truncation, the exit-code contract, retry policy, and the one property that
# has to hold no matter what - the API key never appears in the output.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SEND="$HERE/../alertloop-send"
GLUE="$HERE/../alertloop-monit"
PORT="${TEST_PORT:-18711}"
WORK="$(mktemp -d)"
REQUESTS="$WORK/requests.jsonl"
CONTROL="$WORK/control"
CONFIG="$WORK/monit.env"
SERVER_PID=""
API_KEY="super-secret-test-key-do-not-log"

pass=0
fail=0

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

ok()   { pass=$((pass + 1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail + 1)); printf '  FAIL %s\n' "$1"; [ $# -gt 1 ] && printf '       %s\n' "$2"; }

# --- assertions ------------------------------------------------------------
assert_eq() {
  local what="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then ok "$what"; else bad "$what" "got '$got', want '$want'"; fi
}

assert_contains() {
  local what="$1" haystack="$2" needle="$3"
  case "$haystack" in
    *"$needle"*) ok "$what" ;;
    *) bad "$what" "'$needle' not found in: ${haystack:0:200}" ;;
  esac
}

assert_not_contains() {
  local what="$1" haystack="$2" needle="$3"
  case "$haystack" in
    *"$needle"*) bad "$what" "'$needle' WAS found in: ${haystack:0:200}" ;;
    *) ok "$what" ;;
  esac
}

# --- harness ---------------------------------------------------------------
reset() {
  : > "$REQUESTS"
  printf '200\n' > "$CONTROL"
}

# run <args...> -> sets RC, OUT (stdout), ERR (stderr)
run() {
  local out_file="$WORK/stdout" err_file="$WORK/stderr"
  ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$CONFIG" "$@" >"$out_file" 2>"$err_file"
  RC=$?
  OUT="$(cat "$out_file")"
  ERR="$(cat "$err_file")"
}

run_glue() {
  local out_file="$WORK/stdout" err_file="$WORK/stderr"
  ALERTLOOP_SKIP_PERM_CHECK=1 ALERTLOOP_SEND="$SEND" ALERTLOOP_CONFIG_FILE="$CONFIG" \
    "$GLUE" "$@" >"$out_file" 2>"$err_file"
  RC=$?
  OUT="$(cat "$out_file")"
  ERR="$(cat "$err_file")"
}

# last_body -> the JSON body of the most recent request
last_body() { tail -n 1 "$REQUESTS" | jq -r '.body'; }
request_count() { wc -l < "$REQUESTS" | tr -d ' '; }

firing_args=(
  --status firing --severity critical --event-type service_unavailable
  --host server-01 --service postgresql
  --dedupe-key server-01:postgresql:availability
  --title "PostgreSQL unavailable"
  --message "does not respond on port 5432"
)

# --- setup -----------------------------------------------------------------
for cmd in curl jq; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "missing dependency: $cmd" >&2; exit 1; }
done

# Pick an interpreter that actually RUNS, not merely one that is on PATH:
# a `python3` that exists but exits non-zero (a Microsoft Store stub, a broken
# symlink) would otherwise leave the mock server dead and every network test
# failing with a connection error that says nothing about the real cause.
PY=""
for candidate in python3 python; do
  if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c 'import sys; sys.exit(0)' >/dev/null 2>&1; then
    PY="$candidate"
    break
  fi
done
if [ -z "$PY" ]; then
  echo "missing dependency: a working python3 (needed only for the mock AlertLoop)" >&2
  exit 1
fi

cat > "$CONFIG" <<EOF
ALERTLOOP_URL=http://127.0.0.1:$PORT
ALERTLOOP_API_KEY=$API_KEY
ALERTLOOP_HOST=server-01
ALERTLOOP_RETRY_COUNT=2
ALERTLOOP_RETRY_DELAY_SECONDS=1
ALERTLOOP_TIMEOUT_SECONDS=3
ALERTLOOP_CONNECT_TIMEOUT_SECONDS=1
ALERTLOOP_SPOOL_DIR=$WORK/not-a-dir/spool
EOF
chmod 600 "$CONFIG" 2>/dev/null || true
# The tests above the spool section check the codes of an adapter whose spool
# cannot be used: the directory sits under a file, which not even root can fix.
: > "$WORK/not-a-dir"

reset
"$PY" "$HERE/mock-alertloop.py" "$PORT" "$REQUESTS" "$CONTROL" &
SERVER_PID=$!
ready=0
for _ in $(seq 1 40); do
  if curl -fsS -m 1 -X POST -d '{}' "http://127.0.0.1:$PORT/v1/events" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
if [ "$ready" -ne 1 ]; then
  # Fail loudly here. Without this the suite reports dozens of unrelated
  # failures and buries the one fact that matters.
  echo "the mock AlertLoop never came up on port $PORT; aborting" >&2
  exit 1
fi
reset

echo "alertloop-send tests"

# --- argument validation ---------------------------------------------------
echo "-- argument validation"
run --status firing --severity critical --dedupe-key k --title t
assert_eq "missing --message is rejected" "$RC" 2

run --status firing --severity critical --dedupe-key k --message m
assert_eq "missing --title is rejected" "$RC" 2

run --status firing --severity critical --title t --message m
assert_eq "missing --dedupe-key is rejected" "$RC" 2

run --dedupe-key k --title t --message m
assert_eq "missing --status is rejected" "$RC" 2

run --status flapping --dedupe-key k --title t --message m
assert_eq "unknown status is rejected" "$RC" 2
assert_contains "unknown status is explained" "$ERR" "invalid_status"

run --status firing --severity catastrophic --dedupe-key k --title t --message m
assert_eq "unknown severity is rejected" "$RC" 2

run --status resolved
assert_eq "resolve without a dedupe_key is rejected" "$RC" 2

run --status firing --dedupe-key k --title t --message m --label novalue
assert_eq "a label without = is rejected" "$RC" 2

run --status firing --dedupe-key k --title t --message m --wat
assert_eq "an unknown argument is rejected" "$RC" 2

run --status firing --dedupe-key k --title t --message m --api-key leak-me
assert_eq "an API key on the command line is refused" "$RC" 2
assert_not_contains "the refused key is not echoed back" "$ERR" "leak-me"

# --- configuration ---------------------------------------------------------
echo "-- configuration"
ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$WORK/nope.env" --status resolved --dedupe-key k >/dev/null 2>"$WORK/stderr"
assert_eq "a missing config file is rejected" "$?" 2
assert_contains "the missing config file is named" "$(cat "$WORK/stderr")" "config_not_found"

cat > "$WORK/insecure.env" <<EOF
ALERTLOOP_URL=http://alerts.example.com
ALERTLOOP_API_KEY=$API_KEY
EOF
ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$WORK/insecure.env" --status resolved --dedupe-key k >/dev/null 2>"$WORK/stderr"
assert_eq "plain HTTP to a remote host is refused" "$?" 2
assert_contains "the refusal explains why" "$(cat "$WORK/stderr")" "insecure_url"

cat > "$WORK/nokey.env" <<EOF
ALERTLOOP_URL=http://127.0.0.1:$PORT
EOF
ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$WORK/nokey.env" --status resolved --dedupe-key k >/dev/null 2>"$WORK/stderr"
assert_eq "a config without an API key is rejected" "$?" 2

cat > "$WORK/evil.env" <<EOF
ALERTLOOP_URL=http://127.0.0.1:$PORT
ALERTLOOP_API_KEY=$API_KEY
touch $WORK/PWNED
EOF
reset
ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$WORK/evil.env" --status resolved --dedupe-key k >/dev/null 2>&1
if [ -e "$WORK/PWNED" ]; then
  bad "the config file is parsed, not sourced" "a command in the config file executed"
else
  ok "the config file is parsed, not sourced"
fi

# --- payload ---------------------------------------------------------------
echo "-- payload"
reset
run "${firing_args[@]}"
assert_eq "a valid firing report is accepted" "$RC" 0
body="$(last_body)"
assert_eq "status is firing"      "$(echo "$body" | jq -r '.status')"      "firing"
assert_eq "type is incident"      "$(echo "$body" | jq -r '.type')"        "incident"
assert_eq "severity is carried"   "$(echo "$body" | jq -r '.severity')"    "critical"
assert_eq "source defaults to monit" "$(echo "$body" | jq -r '.source')"   "monit"
assert_eq "event type becomes the category" "$(echo "$body" | jq -r '.category')" "service_unavailable"
assert_eq "dedupe_key is carried"  "$(echo "$body" | jq -r '.dedupe_key')" "server-01:postgresql:availability"
assert_eq "host is a label"        "$(echo "$body" | jq -r '.payload.labels.host')" "server-01"
assert_eq "service is a label"     "$(echo "$body" | jq -r '.payload.labels.service')" "postgresql"
assert_eq "monitor is a label"     "$(echo "$body" | jq -r '.payload.labels.monitor')" "monit"
assert_eq "the title is preserved" "$(echo "$body" | jq -r '.payload.title')" "PostgreSQL unavailable"
assert_contains "occurred_at is RFC3339" "$(echo "$body" | jq -r '.payload.occurred_at')" "T"

headers="$(tail -n 1 "$REQUESTS" | jq -r '.headers | keys | join(",")')"
assert_contains "the API key is sent as a header" "$headers" "x-api-key"
assert_eq "the key value is correct" "$(tail -n 1 "$REQUESTS" | jq -r '.headers["x-api-key"]')" "$API_KEY"
assert_contains "the content type is JSON" "$(tail -n 1 "$REQUESTS" | jq -r '.headers["content-type"]')" "application/json"
assert_contains "the user agent identifies the adapter" "$(tail -n 1 "$REQUESTS" | jq -r '.headers["user-agent"]')" "alertloop-send"

echo "-- escaping"
reset
run --status firing --severity warning --dedupe-key "esc:test" \
  --title 'quote " brace } backslash \ end' \
  --message 'line one
line two with "quotes" and {braces} and a tab	here'
assert_eq "a message full of JSON metacharacters is accepted" "$RC" 0
body="$(last_body)"
if echo "$body" | jq -e . >/dev/null 2>&1; then
  ok "the payload is still valid JSON"
else
  bad "the payload is still valid JSON" "$body"
fi
assert_contains "quotes survive" "$(echo "$body" | jq -r '.payload.title')" 'quote " brace } backslash \ end'
assert_contains "newlines survive" "$(echo "$body" | jq -r '.payload.detail')" 'line one'

reset
run --status firing --severity warning --dedupe-key "inject:test" \
  --title 'x' --message '","dedupe_key":"hijacked","x":"'
body="$(last_body)"
assert_eq "a message cannot hijack another field" "$(echo "$body" | jq -r '.dedupe_key')" "inject:test"

echo "-- normalisation"
reset
run --status firing --severity warning --dedupe-key "spaces and/slashes:ok" --title t --message m
assert_eq "a dedupe_key is sanitised to identifier characters" \
  "$(last_body | jq -r '.dedupe_key')" "spaces-and/slashes:ok"

reset
long_title="$(printf 'A%.0s' $(seq 1 500))"
run --status firing --severity warning --dedupe-key "trunc:test" --title "$long_title" --message m
title_len="$(last_body | jq -r '.payload.title' | wc -c | tr -d ' ')"
if [ "$title_len" -le 210 ]; then
  ok "an over-long title is truncated"
else
  bad "an over-long title is truncated" "length was $title_len"
fi

reset
run --status firing --severity warning --dedupe-key "sev:test" --severity warn --title t --message m
assert_eq "the 'warn' spelling is normalised" "$(last_body | jq -r '.severity')" "warning"
reset
run --status firing --dedupe-key "sev:test2" --severity error --title t --message m
assert_eq "the 'error' spelling maps to critical" "$(last_body | jq -r '.severity')" "critical"

echo "-- labels and metadata"
reset
run --status firing --severity info --dedupe-key "lbl:test" --title t --message m \
  --label env=production --label region=eu --metadata monit_event="Connection failed"
body="$(last_body)"
assert_eq "a custom label is included" "$(echo "$body" | jq -r '.payload.labels.env')" "production"
assert_eq "a second custom label is included" "$(echo "$body" | jq -r '.payload.labels.region')" "eu"
assert_eq "metadata lands in the payload" "$(echo "$body" | jq -r '.payload.monit_event')" "Connection failed"

echo "-- resolve"
reset
run --status resolved --dedupe-key "server-01:postgresql:availability"
assert_eq "a resolve carrying only a dedupe_key is accepted" "$RC" 0
body="$(last_body)"
assert_eq "resolve sends status=resolved" "$(echo "$body" | jq -r '.status')" "resolved"
assert_eq "resolve sends the key" "$(echo "$body" | jq -r '.dedupe_key')" "server-01:postgresql:availability"
assert_eq "resolve sends nothing else" "$(echo "$body" | jq -r 'keys | join(",")')" "dedupe_key,status"

echo "-- dry run"
reset
run "${firing_args[@]}" --dry-run
assert_eq "dry-run exits zero" "$RC" 0
assert_eq "dry-run sends no request" "$(request_count)" "0"
if echo "$OUT" | jq -e . >/dev/null 2>&1; then
  ok "dry-run prints the payload as JSON"
else
  bad "dry-run prints the payload as JSON" "$OUT"
fi
assert_not_contains "dry-run does not print the API key" "$OUT$ERR" "$API_KEY"

echo "-- exit codes and retries"
reset
printf '400\n' > "$CONTROL"
run "${firing_args[@]}"
assert_eq "HTTP 400 exits 4" "$RC" 4
assert_eq "HTTP 400 is not retried" "$(request_count)" "1"

reset
printf '401\n' > "$CONTROL"
run "${firing_args[@]}"
assert_eq "HTTP 401 exits 4" "$RC" 4
assert_eq "HTTP 401 is not retried" "$(request_count)" "1"

reset
printf '500\n' > "$CONTROL"
run "${firing_args[@]}"
assert_eq "a persistent HTTP 500 exits 5" "$RC" 5
assert_eq "HTTP 500 is retried to the configured limit" "$(request_count)" "3"

reset
printf '429\n' > "$CONTROL"
run "${firing_args[@]}"
assert_eq "HTTP 429 is retried" "$(request_count)" "3"

reset
printf '500\n500\n200\n' > "$CONTROL"
run "${firing_args[@]}"
assert_eq "a retry that eventually succeeds exits zero" "$RC" 0
assert_eq "it stopped as soon as it succeeded" "$(request_count)" "3"

reset
printf '204\n' > "$CONTROL"
run --status resolved --dedupe-key "nothing:to:resolve"
assert_eq "HTTP 204 is a success" "$RC" 0

echo "-- unreachable AlertLoop"
cat > "$WORK/closed.env" <<EOF
ALERTLOOP_URL=http://127.0.0.1:1
ALERTLOOP_SPOOL_DIR=$WORK/not-a-dir/spool
ALERTLOOP_API_KEY=$API_KEY
ALERTLOOP_RETRY_COUNT=1
ALERTLOOP_RETRY_DELAY_SECONDS=1
ALERTLOOP_CONNECT_TIMEOUT_SECONDS=1
ALERTLOOP_TIMEOUT_SECONDS=2
EOF
ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$WORK/closed.env" --status resolved --dedupe-key k >/dev/null 2>"$WORK/stderr"
rc=$?
if [ "$rc" = "6" ] || [ "$rc" = "7" ]; then
  ok "a refused connection exits 6 or 7 (got $rc)"
else
  bad "a refused connection exits 6 or 7" "got $rc"
fi
assert_not_contains "the API key is not in the failure log" "$(cat "$WORK/stderr")" "$API_KEY"

echo "-- the API key never leaks"
reset
run "${firing_args[@]}"
assert_not_contains "not on stdout" "$OUT" "$API_KEY"
assert_not_contains "not on stderr" "$ERR" "$API_KEY"
reset
printf '500\n' > "$CONTROL"
run "${firing_args[@]}"
assert_not_contains "not in a retry log" "$ERR" "$API_KEY"
reset
printf '400\n' > "$CONTROL"
run "${firing_args[@]}"
assert_not_contains "not in a 4xx log" "$ERR" "$API_KEY"

echo
echo "alertloop-monit tests"
reset
export MONIT_SERVICE="postgresql" MONIT_HOST="server-01"
export MONIT_EVENT="Connection failed"
export MONIT_DESCRIPTION="failed protocol test [PGSQL] at [127.0.0.1]:5432"
export MONIT_DATE="Sat, 22 Aug 2026 18:00:00"

run_glue firing critical availability
assert_eq "the glue sends a firing report" "$RC" 0
body="$(last_body)"
assert_eq "the key is host:service:check" "$(echo "$body" | jq -r '.dedupe_key')" "server-01:postgresql:availability"
assert_contains "the title carries Monit's event" "$(echo "$body" | jq -r '.payload.title')" "Connection failed"
assert_contains "the message carries Monit's description" "$(echo "$body" | jq -r '.payload.detail')" "PGSQL"
assert_eq "availability maps to service_unavailable" "$(echo "$body" | jq -r '.category')" "service_unavailable"

reset
run_glue firing warning memory
assert_eq "memory maps to resource_limit" "$(last_body | jq -r '.category')" "resource_limit"
assert_eq "the key uses the check name" "$(last_body | jq -r '.dedupe_key')" "server-01:postgresql:memory"
assert_eq "severity is carried through" "$(last_body | jq -r '.severity')" "warning"

reset
run_glue resolved availability
assert_eq "the glue resolves" "$RC" 0
assert_eq "resolve carries the same key" "$(last_body | jq -r '.dedupe_key')" "server-01:postgresql:availability"
assert_eq "resolve sends status=resolved" "$(last_body | jq -r '.status')" "resolved"

reset
run_glue resolved critical availability
assert_eq "resolve tolerates a severity argument" "$(last_body | jq -r '.dedupe_key')" "server-01:postgresql:availability"

reset
MONIT_SERVICE="my service/with junk" run_glue firing critical availability
assert_eq "a service name with spaces becomes a stable key" \
  "$(last_body | jq -r '.dedupe_key')" "server-01:my-service-with-junk:availability"

run_glue bogus
assert_eq "an unknown status is rejected" "$RC" 2

# The property the whole dedupe design rests on: the key must not move between
# cycles. Same check, different Monit message and time - same key.
reset
MONIT_EVENT="Connection failed" MONIT_DATE="Sat, 22 Aug 2026 18:00:00" run_glue firing critical availability
key1="$(last_body | jq -r '.dedupe_key')"
reset
MONIT_EVENT="Connection timed out" MONIT_DATE="Sat, 22 Aug 2026 19:30:00" run_glue firing critical availability
key2="$(last_body | jq -r '.dedupe_key')"
assert_eq "the key is stable across cycles" "$key1" "$key2"

echo "-- spool"
SPOOL="$WORK/spool"
SPOOL_CONFIG="$WORK/spool.env"
cat > "$SPOOL_CONFIG" <<EOF
ALERTLOOP_URL=http://127.0.0.1:$PORT
ALERTLOOP_API_KEY=$API_KEY
ALERTLOOP_RETRY_COUNT=0
ALERTLOOP_TIMEOUT_SECONDS=3
ALERTLOOP_CONNECT_TIMEOUT_SECONDS=1
ALERTLOOP_SPOOL_DIR=$SPOOL
ALERTLOOP_SPOOL_MAX=3
EOF
spool_run() {
  ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$SPOOL_CONFIG" "$@" >"$WORK/stdout" 2>"$WORK/stderr"
  RC=$?
  ERR="$(cat "$WORK/stderr")"
}
fire() { spool_run --status firing --dedupe-key "$1" --title t --message m; }
spooled() { find "$SPOOL" -maxdepth 1 -name '*.json' 2>/dev/null | wc -l | tr -d ' '; }
spooled_keys() { for f in "$SPOOL"/*.json; do jq -r .dedupe_key "$f"; done | tr '\n' ' '; }
sent_keys() { jq -r '.body | fromjson | .dedupe_key' "$REQUESTS" | tr '\n' ' '; }

sed "s#^ALERTLOOP_URL=.*#ALERTLOOP_URL=http://127.0.0.1:1#" "$SPOOL_CONFIG" > "$WORK/spool-closed.env"
ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$WORK/spool-closed.env" --status firing --dedupe-key down:1 --title t --message m 2>/dev/null
assert_eq "an unreachable AlertLoop spools the event and exits 9" "$?" 9
assert_eq "the spooled event is on disk" "$(spooled_keys)" "down:1 "
assert_eq "the spool directory is private" "$(stat -c '%a' "$SPOOL")" "700"
assert_not_contains "the spool does not hold the API key" "$(cat "$SPOOL"/*.json)" "$API_KEY"

reset
printf '500\n' > "$CONTROL"
fire order:2
: > "$REQUESTS"
spool_run --status resolved --dedupe-key order:1
assert_eq "a resolve after an unsent alert is spooled too" "$RC" 9
assert_eq "an event is not sent past older unsent ones" "$(request_count)" "1"
printf '200\n' > "$CONTROL"
: > "$REQUESTS"
spool_run --flush
assert_eq "--flush exits 0 once the spool is empty" "$RC" 0
assert_eq "--flush sends oldest first" "$(sent_keys)" "down:1 order:2 order:1 "
assert_eq "--flush empties the spool" "$(spooled)" "0"

printf '500\n' > "$CONTROL"
fire next:1
printf '200\n' > "$CONTROL"
: > "$REQUESTS"
fire next:2
assert_eq "the next run sends the spool first, then its own event" "$(sent_keys)" "next:1 next:2 "
assert_eq "and exits 0" "$RC" 0

reset
printf '400\n' > "$CONTROL"
fire rejected:1
assert_eq "a 4xx is not spooled" "$RC$(spooled)" "40"

printf '500\n' > "$CONTROL"
for k in full:1 full:2 full:3 full:4; do fire "$k"; done
assert_eq "a full spool drops the oldest" "$(spooled_keys)" "full:2 full:3 full:4 "
assert_contains "the drop is logged" "$ERR" "status=dropped dedupe_key=full:1"
rejected_keys() { for f in "$SPOOL"/rejected/*.json; do [ -e "$f" ] && jq -r .dedupe_key "$f"; done | tr '\n' ' '; }
printf '400\n' > "$CONTROL"
spool_run --flush
assert_eq "a spooled event AlertLoop rejects leaves the queue; --flush exits 4 while it waits in rejected/" "$RC$(spooled)" "40"
assert_eq "and is kept in rejected/, not deleted" "$(rejected_keys)" "full:2 full:3 full:4 "
assert_contains "the move is logged" "$ERR" "status=rejected dedupe_key=full:2 file=$SPOOL/rejected/"
assert_eq "rejected/ is private" "$(stat -c '%a' "$SPOOL/rejected")" "700"
rm -f "$SPOOL"/rejected/*.json

# Only an event refused for what it is moves to rejected/, and the events behind
# it still go out. A refused key or a wrong URL is this host's to fix: the event
# stays and --flush says so with 4.
for code in 413 422 403:source_not_allowed; do
  printf '500\n' > "$CONTROL"
  fire "bad-body:$code"
  fire "behind:$code"
  printf '%s\n200\n' "$code" > "$CONTROL"
  : > "$REQUESTS"
  spool_run --flush
  assert_eq "a spooled event answered $code moves to rejected/, --flush exits 4 while it waits" "$RC $(spooled) $(rejected_keys)" "4 0 bad-body:$code "
  assert_eq "the event behind it answered $code is sent" "$(sent_keys)" "bad-body:$code behind:$code "
  rm -f "$SPOOL"/rejected/*.json
done
# A rejected event moved back into the spool goes out on the next run.
printf '500\n' > "$CONTROL"
fire back:1
printf '422\n' > "$CONTROL"
spool_run --flush
mv "$SPOOL"/rejected/*.json "$SPOOL"/
printf '200\n' > "$CONTROL"
: > "$REQUESTS"
spool_run --flush
assert_eq "a rejected event moved back is sent by --flush" "$RC $(sent_keys)$(spooled)" "0 back:1 0"

# 409 is invalid_transition, which the API answers with "retry": it is spooled,
# not refused, both when sent directly and when sent from the spool.
printf '409\n' > "$CONTROL"
fire conflict:1
assert_eq "a 409 is spooled and exits 9" "$RC $(spooled_keys)" "9 conflict:1 "
spool_run --flush
assert_eq "--flush answered 409 keeps the event and exits 9" "$RC $(spooled_keys)$(rejected_keys)" "9 conflict:1 "
printf '200\n' > "$CONTROL"
spool_run --flush
assert_eq "the 409 event goes out once AlertLoop takes it" "$RC $(spooled)" "0 0"
printf '500\n' > "$CONTROL"
fire key:1
for code in 401 403:forbidden 404; do
  printf '%s\n' "$code" > "$CONTROL"
  spool_run --flush
  assert_eq "--flush answered $code keeps the event and exits 4" "$RC $(spooled_keys)" "4 key:1 "
done
assert_contains "the kept event is logged" "$ERR" "status=kept_in_spool dedupe_key=key:1 http_status=404"
fire key:2
assert_eq "a new event behind a refused spool is kept and exits 4" "$RC $(spooled_keys)" "4 key:1 key:2 "
printf '200\n' > "$CONTROL"
: > "$REQUESTS"
spool_run --flush
assert_eq "once the key is fixed the kept events go out in order" "$RC $(sent_keys)" "0 key:1 key:2 "

# A repeat of the last spooled event for a key (same status and severity) adds
# nothing: a stream of identical events must not push older ones out of the
# bounded spool.
printf '500\n' > "$CONTROL"
fire same:1
fire same:1
spool_run --status resolved --dedupe-key same:1
spool_run --status resolved --dedupe-key same:1
assert_eq "an identical event is not spooled twice" "$RC $(spooled_keys)" "9 same:1 same:1 "
assert_eq "firing then resolved are both kept" \
  "$(for f in "$SPOOL"/*.json; do jq -r .status "$f"; done | tr '\n' ' ')" "firing resolved "
fire same:1
assert_eq "a firing after the resolved is kept" "$(spooled)" "3"
printf '200\n' > "$CONTROL"
spool_run --flush
printf '500\n' > "$CONTROL"
spool_run --status firing --severity warning --dedupe-key disk:1 --title t --message m
spool_run --status firing --severity critical --dedupe-key disk:1 --title t --message m
assert_eq "a firing with another severity is kept" \
  "$(for f in "$SPOOL"/*.json; do jq -r .severity "$f"; done | tr '\n' ' ')" "warning critical "
printf '200\n' > "$CONTROL"
spool_run --flush

echo "-- cron-wrapper"
cron_run() {
  ALERTLOOP_SKIP_PERM_CHECK=1 ALERTLOOP_CONFIG_FILE="$SPOOL_CONFIG" ALERTLOOP_SEND="$SEND" \
    ALERTLOOP_HEARTBEAT_DIR="$WORK/heartbeats" ALERTLOOP_CRON_STATE_DIR="$WORK/cron-state" \
    ALERTLOOP_HOST_OVERRIDE=server-01 \
    "$HERE/../examples/cron-wrapper.sh" nightly "$@" >/dev/null 2>"$WORK/stderr"
  RC=$?
}
statuses() { jq -r '.body | fromjson | .status' "$REQUESTS" | tr '\n' ' '; }
reset
cron_run true
assert_eq "the first run of a job reports resolved once" "$RC $(statuses)" "0 resolved "
: > "$REQUESTS"
cron_run true
assert_eq "a success after a success reports nothing" "$RC $(request_count)" "0 0"
cron_run false
assert_eq "a failure reports firing and keeps the job's exit code" "$RC $(statuses)" "1 firing "
cron_run true
assert_eq "the success after it reports resolved" "$RC $(statuses)" "0 firing resolved "
cron_run true
assert_eq "the next success reports nothing" "$(statuses)" "firing resolved "
printf '500\n' > "$CONTROL"
cron_run false
cron_run true
cron_run true
printf '200\n' > "$CONTROL"
: > "$REQUESTS"
spool_run --flush
assert_eq "a spooled recovery is not repeated by the next success" "$(statuses)" "firing resolved "

printf '500\n' > "$CONTROL"
sed -i 's/^ALERTLOOP_SPOOL_MAX=.*/ALERTLOOP_SPOOL_MAX=10/' "$SPOOL_CONFIG"
pids=()
for k in 1 2 3 4 5 6; do
  ALERTLOOP_SKIP_PERM_CHECK=1 "$SEND" --config "$SPOOL_CONFIG" --status firing --dedupe-key "par:$k" --title t --message m 2>/dev/null &
  pids+=("$!")
done
codes=""
for p in "${pids[@]}"; do wait "$p"; codes="$codes$?"; done
assert_eq "parallel runs all spool" "$codes" "999999"
assert_eq "parallel runs lose no event" "$(spooled_keys | tr ' ' '\n' | sort | tr '\n' ' ')" "par:1 par:2 par:3 par:4 par:5 par:6 "
printf '200\n' > "$CONTROL"
: > "$REQUESTS"
spool_run --flush
assert_eq "the parallel events are all sent once" "$RC $(request_count)" "0 6"

echo "-- unreadable config"
# Run as a user who cannot read monit.env (a cron job run as the app user): the
# error must reach syslog, not only a cron mail nobody gets. A stub logger
# records what would go to syslog.
UNREAD="$(mktemp -d)"
chmod 755 "$UNREAD"
mkdir "$UNREAD/bin"
printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "%s/syslog"\n' "$UNREAD" > "$UNREAD/bin/logger"
chmod 755 "$UNREAD/bin/logger"
: > "$UNREAD/syslog"
chmod 666 "$UNREAD/syslog"
cp "$SEND" "$UNREAD/alertloop-send"
cp "$HERE/../examples/cron-wrapper.sh" "$UNREAD/cron-wrapper.sh"
cp "$SPOOL_CONFIG" "$UNREAD/monit.env"
chmod 755 "$UNREAD/alertloop-send" "$UNREAD/cron-wrapper.sh"
as_user=()
if [ "$(id -u)" -eq 0 ]; then
  chmod 600 "$UNREAD/monit.env"
  as_user=(runuser -u nobody --)
else
  chmod 000 "$UNREAD/monit.env"
fi
${as_user[@]+"${as_user[@]}"} env PATH="$UNREAD/bin:$PATH" "$UNREAD/alertloop-send" --config "$UNREAD/monit.env" \
  --status resolved --dedupe-key k 2>"$UNREAD/stderr"
assert_eq "an unreadable config exits 2" "$?" 2
assert_contains "the error is on stderr" "$(cat "$UNREAD/stderr")" "config_not_readable:$UNREAD/monit.env"
assert_contains "and in syslog" "$(cat "$UNREAD/syslog")" "-t alertloop-send -p user.err status=failed error=config_not_readable"
: > "$UNREAD/syslog"
${as_user[@]+"${as_user[@]}"} env PATH="$UNREAD/bin:$PATH" ALERTLOOP_SEND="$UNREAD/alertloop-send" \
  ALERTLOOP_CONFIG_FILE="$UNREAD/monit.env" ALERTLOOP_HEARTBEAT_DIR="$UNREAD/hb" \
  ALERTLOOP_CRON_STATE_DIR="$UNREAD/state" "$UNREAD/cron-wrapper.sh" nightly false >/dev/null 2>"$UNREAD/stderr"
assert_eq "the wrapper keeps the job's exit code" "$?" 1
assert_contains "the wrapper run by that user leaves the adapter's error in syslog" "$(cat "$UNREAD/syslog")" "config_not_readable"
assert_contains "and its own" "$(cat "$UNREAD/syslog")" "-t cron-wrapper -p user.err could not report the failure of nightly (exit 2)"
rm -rf "$UNREAD"

echo
echo "-------------------------------------"
printf 'passed: %d   failed: %d\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
