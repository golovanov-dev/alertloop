#!/usr/bin/env bash
# Upgrade test: write a database with an OLD AlertLoop release, then start the
# CURRENT build against that same database and check the data is still there and
# still correct.
#
# The in-process migration test (internal/storage/upgrade_test.go) checks the
# migration chain against the 0001 SQL checked in here. This script checks
# something that test cannot: that the schema the old release ACTUALLY wrote is
# the one we think it wrote. It builds the old binary from its git tag rather
# than downloading a release asset, so it runs anywhere the repository does.
#
# Usage:
#   scripts/upgrade-test.sh
#   FROM_TAG=v0.2.0 scripts/upgrade-test.sh
#   ALERTLOOP_TEST_POSTGRES_DSN=postgres://... scripts/upgrade-test.sh
set -euo pipefail

FROM_TAG="${FROM_TAG:-v0.1.0}"
OLD_PORT="${OLD_PORT:-18081}"
NEW_PORT="${NEW_PORT:-18082}"
TOKEN="upgrade-test-admin-token"
GOEXE="$(go env GOEXE)"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
worktree="$work/old-src"
old_bin="$work/alertloop-old$GOEXE"
new_bin="$work/alertloop-new$GOEXE"
server_pid=""

cleanup() {
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null || true
  fi
  git -C "$repo_root" worktree remove --force "$worktree" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

fail() { echo "UPGRADE TEST FAILED: $*" >&2; exit 1; }

# native_path renders a path the way the Go binary will read it. Under Git Bash
# / MSYS, a POSIX path is rewritten on its way into a native process's
# environment but NOT when it is written into a config file, so the two halves
# of this test would otherwise disagree about where the database is.
native_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi
}

# wait_for_health polls until the server answers or gives up. A server that
# never comes up must fail here with its log, not 30 seconds later with a
# confusing curl error.
wait_for_health() {
  local port="$1" log="$2" i
  for i in $(seq 1 60); do
    if curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then return 0; fi
    if ! kill -0 "$server_pid" 2>/dev/null; then
      echo "--- server exited early; log follows ---" >&2
      cat "$log" >&2
      fail "server on port $port exited during startup"
    fi
    sleep 0.5
  done
  echo "--- server never became healthy; log follows ---" >&2
  cat "$log" >&2
  fail "server on port $port did not become healthy"
}

stop_server() {
  [ -n "$server_pid" ] || return 0
  kill "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
  server_pid=""
}

echo "==> Building $FROM_TAG"
git -C "$repo_root" worktree add --detach -q "$worktree" "$FROM_TAG"
(cd "$worktree" && CGO_ENABLED=0 go build -o "$old_bin" ./cmd/alertloop)

echo "==> Building the current tree"
(cd "$repo_root" && CGO_ENABLED=0 go build -o "$new_bin" ./cmd/alertloop)

# run_scenario <label> <old-env-driver> <old-env-dsn> <new-config-driver> <new-config-dsn>
run_scenario() {
  local label="$1" driver="$2" dsn="$3"
  echo
  echo "=== $label: $FROM_TAG -> current ==="

  # --- the old release writes the database --------------------------------
  # The old release was configured through the environment. Those variables are
  # scoped to this one command: the current binary refuses to start while a
  # leftover one is set, and that refusal is deliberate.
  local old_log="$work/old-$label.log"
  env ALERTLOOP_ADDR=":$OLD_PORT" \
      ALERTLOOP_DB_DRIVER="$driver" \
      ALERTLOOP_DB_DSN="$dsn" \
      ALERTLOOP_ADMIN_TOKEN="$TOKEN" \
      "$old_bin" all >"$old_log" 2>&1 &
  server_pid=$!
  wait_for_health "$OLD_PORT" "$old_log"

  echo "--> ingesting events with $FROM_TAG"
  local i
  for i in 1 2 3; do
    curl -fsS -X POST "http://127.0.0.1:$OLD_PORT/v1/events" \
      -H "X-API-Key: $TOKEN" -H 'Content-Type: application/json' \
      -d "{\"type\":\"incident\",\"severity\":\"error\",\"source\":\"upgrade-test\",
           \"category\":\"billing\",\"message\":\"written by $FROM_TAG #$i\",
           \"dedupe_key\":\"upgrade:$label:$i\",
           \"payload\":{\"n\":$i,\"quote\":\"it's\",\"brace\":\"a}b\"}}" \
      >/dev/null || fail "$FROM_TAG rejected the ingest"
  done

  # Close one of them, so the upgrade has a resolved row to backfill.
  local resolve_id
  resolve_id="$(curl -fsS -H "X-API-Key: $TOKEN" \
    "http://127.0.0.1:$OLD_PORT/v1/events?limit=1" | jq -r '.items[0].id')"
  if [ -z "$resolve_id" ] || [ "$resolve_id" = "null" ]; then
    fail "could not read an event id from $FROM_TAG"
  fi
  curl -fsS -X POST -H "X-API-Key: $TOKEN" \
    "http://127.0.0.1:$OLD_PORT/v1/events/$resolve_id/resolve" >/dev/null \
    || fail "$FROM_TAG rejected the resolve action"

  local before
  before="$(curl -fsS -H "X-API-Key: $TOKEN" \
    "http://127.0.0.1:$OLD_PORT/v1/events?limit=100" | jq '.items | length')"
  [ "$before" = "3" ] || fail "$FROM_TAG stored $before events, expected 3"
  stop_server

  # --- the current build takes over ---------------------------------------
  local cfg="$work/alertloop-$label.yaml" new_log="$work/new-$label.log"
  cat >"$cfg" <<YAML
addr: ":$NEW_PORT"
admin_token: "$TOKEN"
database:
  driver: "$driver"
  dsn: "$dsn"
YAML

  "$new_bin" --config "$cfg" all >"$new_log" 2>&1 &
  server_pid=$!
  wait_for_health "$NEW_PORT" "$new_log"

  echo "--> reading the same database with the current build"
  local body
  body="$(curl -fsS -H "X-API-Key: $TOKEN" "http://127.0.0.1:$NEW_PORT/v1/events?limit=100")"

  local after
  after="$(echo "$body" | jq '.items | length')"
  [ "$after" = "$before" ] || fail "$before events before the upgrade, $after after"

  echo "$body" | jq -e --arg tag "$FROM_TAG" \
    '[.items[] | select(.message | startswith("written by " + $tag))] | length == 3' >/dev/null \
    || fail "event messages did not survive the upgrade"

  # ASCII only here on purpose: what a Windows console does to a non-ASCII
  # command line has nothing to do with whether the upgrade preserved the data.
  # Unicode payload round-tripping is covered by the Go storage tests, which do
  # not pass through a shell.
  echo "$body" | jq -e '[.items[] | select(.payload.quote == "it'"'"'s" and .payload.brace == "a}b")] | length == 3' >/dev/null \
    || fail "event payloads did not survive the upgrade"

  echo "$body" | jq -e '[.items[] | select(.category == "billing")] | length == 3' >/dev/null \
    || fail "event columns did not survive the upgrade"

  # The 0.4.0 columns are present and backfilled on rows the old release wrote.
  echo "$body" | jq -e '[.items[] | select(.last_seen_at != null and .last_seen_at != "")] | length == 3' >/dev/null \
    || fail "last_seen_at was not backfilled on upgraded rows"

  echo "$body" | jq -e '[.items[] | select(.state == "resolved" and .resolved_at != null)] | length == 1' >/dev/null \
    || fail "resolved_at was not backfilled on the resolved row"

  # And the upgraded database supports the new lifecycle end to end: the
  # resolved incident's key is free, and an open one still deduplicates.
  local code
  code="$(curl -fsS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$NEW_PORT/v1/events" \
    -H "X-API-Key: $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"status\":\"firing\",\"type\":\"incident\",\"severity\":\"critical\",
         \"source\":\"upgrade-test\",\"message\":\"post-upgrade firing\",
         \"dedupe_key\":\"upgrade:$label:1\"}")"
  [ "$code" = "200" ] || [ "$code" = "201" ] || fail "post-upgrade ingest returned HTTP $code"

  code="$(curl -fsS -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$NEW_PORT/v1/events" \
    -H "X-API-Key: $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"status\":\"resolved\",\"dedupe_key\":\"upgrade:$label:1\"}")"
  [ "$code" = "200" ] || fail "post-upgrade resolve returned HTTP $code"

  stop_server
  echo "--> $label OK: $after events survived, lifecycle works on the upgraded schema"
}

sqlite_db="$(native_path "$work/upgrade.db")"
run_scenario "sqlite" "sqlite" "$sqlite_db"

if [ -n "${ALERTLOOP_TEST_POSTGRES_DSN:-}" ]; then
  run_scenario "postgres" "postgres" "$ALERTLOOP_TEST_POSTGRES_DSN"
else
  echo
  echo "==> Skipping PostgreSQL: ALERTLOOP_TEST_POSTGRES_DSN is not set"
fi

echo
echo "UPGRADE TEST PASSED ($FROM_TAG -> current)"
