#!/usr/bin/env bash
# Runs the Go tests against a throwaway PostgreSQL cluster.
#
# The state-database tests always run on SQLite; with PETALBOOK_TEST_POSTGRES
# set they run on PostgreSQL as well, each test in a schema of its own. When
# that variable is already set (CI) the database it names is used; otherwise
# this script makes a cluster in a temporary directory on a free port, runs the
# tests with the variable pointing at it, and stops and removes the cluster
# whatever happens. It needs initdb, pg_ctl and psql on PATH.
#
# Usage: scripts/test-postgres.sh [go test arguments]
# Default arguments: -race ./cmd/... ./internal/...
set -euo pipefail

export GOWORK=off
cd "$(dirname "$0")/.."

# A database given from outside (CI's service container) is used as it is.
if [ -n "${PETALBOOK_TEST_POSTGRES:-}" ]; then
  echo "PostgreSQL from PETALBOOK_TEST_POSTGRES" >&2
  if [ "$#" -eq 0 ]; then
    set -- -race ./cmd/... ./internal/...
  fi
  exec go test "$@"
fi

for tool in initdb pg_ctl psql; do
  command -v "$tool" >/dev/null || { echo "$tool is not on PATH; install PostgreSQL" >&2; exit 2; }
done


work="$(mktemp -d "${TMPDIR:-/tmp}/petalbook-pg.XXXXXX")"
started=""
cleanup() {
  if [ -n "$started" ]; then
    pg_ctl -D "$work/data" -m immediate -w stop >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

# A port nothing is listening on. Another process may still grab it before
# the cluster starts; then pg_ctl fails and so does this script, loudly.
port=""
for _ in $(seq 1 100); do
  p=$((20000 + RANDOM % 30000))
  if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
    port="$p"
    break
  fi
done
[ -n "$port" ] || { echo "could not find a free port" >&2; exit 1; }

# LC_ALL: on some systems postgres refuses to start without a valid locale.
LC_ALL=C initdb -D "$work/data" -U petalbook --auth=trust --locale=C -E UTF8 --no-sync >"$work/initdb.log" 2>&1 \
  || { cat "$work/initdb.log" >&2; exit 1; }
# Local TCP only, no socket file (its path has a length limit), and no fsync:
# the cluster is thrown away.
cat >>"$work/data/postgresql.conf" <<CONF
listen_addresses = '127.0.0.1'
port = $port
unix_socket_directories = ''
fsync = off
synchronous_commit = off
full_page_writes = off
max_connections = 300
CONF
started=1
LC_ALL=C pg_ctl -D "$work/data" -l "$work/postgres.log" -w start >"$work/pg_ctl.log" 2>&1 \
  || { cat "$work/pg_ctl.log" "$work/postgres.log" >&2; exit 1; }

psql -h 127.0.0.1 -p "$port" -U petalbook -d postgres -v ON_ERROR_STOP=1 -q -c 'CREATE DATABASE petalbook_test' >/dev/null

export PETALBOOK_TEST_POSTGRES="postgres://petalbook@127.0.0.1:$port/petalbook_test?sslmode=disable"
echo "PostgreSQL $(postgres --version 2>/dev/null | awk '{print $3}') on port $port" >&2

if [ "$#" -eq 0 ]; then
  set -- -race ./cmd/... ./internal/...
fi
go test "$@"
