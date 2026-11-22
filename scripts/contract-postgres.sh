#!/usr/bin/env bash
# Runs the API contract tests against an accounts-mode server whose state
# database is PostgreSQL, so PostgreSQL is tested through real HTTP like SQLite.
#
# With PETALBOOK_TEST_POSTGRES set (CI's service container) the server it names
# is used; otherwise a throwaway cluster is made, as scripts/test-postgres.sh
# does. Either way the run gets a database of its own, made fresh and dropped
# afterwards, so it never meets leftovers of another run. The URL goes to the
# server in PETALBOOK_DATABASE, not on a command line, and is never printed.
#
# Usage: scripts/contract-postgres.sh [path-to-binary]
set -euo pipefail

cd "$(dirname "$0")/.."

bin="${1:-./petalbook}"
dbname="petalbook_contract_$$_$RANDOM"
admin_url=""
created=""

# shellcheck source=scripts/postgres-cluster.sh
. scripts/postgres-cluster.sh

cleanup() {
  if [ -n "$created" ] && [ -n "$admin_url" ]; then
    psql "$admin_url" -v ON_ERROR_STOP=1 -q -c "DROP DATABASE IF EXISTS $dbname WITH (FORCE)" >/dev/null 2>&1 || true
  fi
  pg_cluster_stop
}
trap cleanup EXIT

if [ -n "${PETALBOOK_TEST_POSTGRES:-}" ]; then
  command -v psql >/dev/null || { echo "psql is not on PATH; install the PostgreSQL client" >&2; exit 2; }
  admin_url="$PETALBOOK_TEST_POSTGRES"
  echo "PostgreSQL from PETALBOOK_TEST_POSTGRES" >&2
else
  pg_cluster_start
  admin_url="postgres://petalbook@127.0.0.1:$PG_PORT/postgres?sslmode=disable"
  echo "PostgreSQL $(postgres --version 2>/dev/null | awk '{print $3}') on port $PG_PORT" >&2
fi

psql "$admin_url" -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE $dbname" >/dev/null
created=1

# The same URL with the database name replaced, query string kept.
base="${admin_url%%\?*}"
query=""
case "$admin_url" in *\?*) query="?${admin_url#*\?}" ;; esac
PETALBOOK_DATABASE="${base%/*}/$dbname$query"
export PETALBOOK_DATABASE

PETALBOOK_CONTRACT_ACCOUNTS_ONLY=1 bash scripts/contract.sh "$bin"
