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

# shellcheck source=scripts/postgres-cluster.sh
. scripts/postgres-cluster.sh
trap pg_cluster_stop EXIT
pg_cluster_start
pg_cluster_create_database petalbook_test

export PETALBOOK_TEST_POSTGRES="postgres://petalbook@127.0.0.1:$PG_PORT/petalbook_test?sslmode=disable"
echo "PostgreSQL $(postgres --version 2>/dev/null | awk '{print $3}') on port $PG_PORT" >&2

if [ "$#" -eq 0 ]; then
  set -- -race ./cmd/... ./internal/...
fi
go test "$@"
