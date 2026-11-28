#!/usr/bin/env bash
# Runs the web app's API contract tests against the real server, twice: once
# against a single-user server and once against an accounts-mode server.
#
# The contract suite (web/src/api/contract.test.ts) runs only against the real
# server: this starts the built binary on empty temporary data and runs the
# assertions through real HTTP. The default web test run does not include it.
#
#   single-user: PETALBOOK_API is the server's address.
#   accounts:    the same, plus PETALBOOK_CONTRACT_USER and
#                PETALBOOK_CONTRACT_PASSWORD of an administrator made with
#                "petalbook user add"; the tests sign in with them and use the
#                first space from GET /api/spaces.
#
# With PETALBOOK_CONTRACT_ACCOUNTS_ONLY set only the accounts-mode run is made,
# and the servers (and "petalbook user add") take their state database from
# PETALBOOK_DATABASE when that is set; scripts/contract-postgres.sh uses this to
# run against PostgreSQL.
#
# Every JSON answer is also checked against its shape (keys and types, no
# values) in web/src/api/fixtures/recorded/shapes.json. With PETALBOOK_RECORD=1
# the file is made again from the answers of both runs instead of being checked.
#
# Usage: scripts/contract.sh [path-to-binary]
set -euo pipefail

bin="${1:-./petalbook}"
[ -x "$bin" ] || { echo "no binary at $bin; run 'make build' first" >&2; exit 2; }

work="$(mktemp -d)"
# A recording starts from nothing, so a shape that is no longer answered goes.
# The accounts-only run (contract-postgres.sh) does not record.
if [ -n "${PETALBOOK_CONTRACT_ACCOUNTS_ONLY:-}" ]; then
  unset PETALBOOK_RECORD
elif [ "${PETALBOOK_RECORD:-}" = 1 ]; then
  rm -f web/src/api/fixtures/recorded/shapes.json
fi
port="${PETALBOOK_CONTRACT_PORT:-18080}"
pid=""
cleanup() {
  stop_server
  rm -rf "$work"
}
trap cleanup EXIT

stop_server() {
  if [ -n "$pid" ]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    pid=""
  fi
}

# wait_ready LOG CHECK...: waits until CHECK (a command) succeeds while the
# server we started is still alive. A check that only asked "does something
# answer on the port" would be satisfied by a stale build or someone else's
# instance, and that would be tested in place of the binary under test; each
# check therefore asks for something only this run knows.
wait_ready() {
  local log="$1"
  shift
  local ready=""
  for _ in $(seq 1 50); do
    if ! kill -0 "$pid" 2>/dev/null; then
      break
    fi
    if "$@" 2>/dev/null; then
      ready=1
      break
    fi
    sleep 0.1
  done
  if [ -z "$ready" ]; then
    echo "the server under test is not the one answering on port $port (did it fail to start? is the port taken?):" >&2
    cat "$log" >&2
    exit 1
  fi
}

# --- single-user ---------------------------------------------------------------
if [ -z "${PETALBOOK_CONTRACT_ACCOUNTS_ONLY:-}" ]; then
  echo "contract tests: single-user server" >&2
  mkdir "$work/space"
  name="contract-$$-$RANDOM" # a space name only this run knows
  log="$work/single.log"
  "$bin" serve --space "$work/space" --state-dir "$work/state" --name "$name" --listen "127.0.0.1:$port" >"$log" 2>&1 &
  pid=$!
  wait_ready "$log" sh -c "curl -fsS 'http://127.0.0.1:$port/api/meta' | grep -q '\"$name\"'"
  PETALBOOK_API="http://127.0.0.1:$port" npm --prefix web run --silent test:contract
  stop_server
fi

# --- accounts ------------------------------------------------------------------

echo "contract tests: accounts-mode server" >&2
user="contract-$$-$RANDOM" # the administrator; only this run's data directory knows it
password="pw-$RANDOM-$RANDOM-$$-$RANDOM"
printf '%s\n' "$password" | "$bin" user add --data "$work/data" --username "$user" --admin >"$work/user-add.log" 2>&1 \
  || { echo "could not make the contract user:" >&2; cat "$work/user-add.log" >&2; exit 1; }
log="$work/accounts.log"
"$bin" serve --data "$work/data" --listen "127.0.0.1:$port" >"$log" 2>&1 &
pid=$!
# Signing in as the user made above proves this is the server on our data directory.
wait_ready "$log" sh -c "curl -fsS -X POST -H 'Content-Type: application/json' -d '{\"identifier\":\"$user\",\"password\":\"$password\"}' 'http://127.0.0.1:$port/api/login' | grep -q '\"$user\"'"
PETALBOOK_API="http://127.0.0.1:$port" PETALBOOK_CONTRACT_USER="$user" PETALBOOK_CONTRACT_PASSWORD="$password" \
  npm --prefix web run --silent test:contract
