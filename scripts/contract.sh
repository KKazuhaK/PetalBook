#!/usr/bin/env bash
# Runs the web app's API contract tests against the real server, twice: once
# against a single-user server and once against an accounts-mode server.
#
# The web tests normally run against an in-memory mock. A mock can drift from
# the server it stands in for, so this starts the built binary on empty
# temporary data and runs the same assertions through real HTTP.
#
#   single-user: PETALBOOK_API is the server's address.
#   accounts:    the same, plus PETALBOOK_CONTRACT_USER and
#                PETALBOOK_CONTRACT_PASSWORD of an administrator made with
#                "petalbook user add"; the tests sign in with them and use the
#                first space from GET /api/spaces.
#
# Usage: scripts/contract.sh [path-to-binary]
set -euo pipefail

bin="${1:-./petalbook}"
[ -x "$bin" ] || { echo "no binary at $bin; run 'make build' first" >&2; exit 2; }

work="$(mktemp -d)"
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

echo "contract tests: single-user server" >&2
mkdir "$work/space"
name="contract-$$-$RANDOM" # a space name only this run knows
log="$work/single.log"
"$bin" serve --space "$work/space" --state-dir "$work/state" --name "$name" --listen "127.0.0.1:$port" >"$log" 2>&1 &
pid=$!
wait_ready "$log" sh -c "curl -fsS 'http://127.0.0.1:$port/api/meta' | grep -q '\"$name\"'"
PETALBOOK_API="http://127.0.0.1:$port" npm --prefix web run --silent test:contract
stop_server

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
wait_ready "$log" sh -c "curl -fsS -X POST -H 'Content-Type: application/json' -d '{\"username\":\"$user\",\"password\":\"$password\"}' 'http://127.0.0.1:$port/api/login' | grep -q '\"$user\"'"
PETALBOOK_API="http://127.0.0.1:$port" PETALBOOK_CONTRACT_USER="$user" PETALBOOK_CONTRACT_PASSWORD="$password" \
  npm --prefix web run --silent test:contract
