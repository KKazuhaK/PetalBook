#!/usr/bin/env bash
# Runs the web app's API contract tests against the real server.
#
# The web tests normally run against an in-memory mock. A mock can drift from
# the server it stands in for, so this starts the built binary on an empty
# temporary space and runs the same assertions through real HTTP.
#
# Usage: scripts/contract.sh [path-to-binary]
set -euo pipefail

bin="${1:-./petalbook}"
[ -x "$bin" ] || { echo "no binary at $bin; run 'make build' first" >&2; exit 2; }

work="$(mktemp -d)"
port="${PETALBOOK_CONTRACT_PORT:-18080}"
log="$work/server.log"
mkdir "$work/space"

# A name only this run knows. Without it, a server already listening on the
# port (a stale build, someone else's instance) would answer the readiness
# check and be tested in place of the binary under test.
name="contract-$$-$RANDOM"

"$bin" serve --space "$work/space" --state-dir "$work/state" --name "$name" --listen "127.0.0.1:$port" >"$log" 2>&1 &
pid=$!
cleanup() {
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

ready=""
for _ in $(seq 1 50); do
  if ! kill -0 "$pid" 2>/dev/null; then
    break
  fi
  if curl -fsS "http://127.0.0.1:$port/api/meta" 2>/dev/null | grep -q "\"$name\""; then
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

PETALBOOK_API="http://127.0.0.1:$port" npm --prefix web run --silent test:contract
