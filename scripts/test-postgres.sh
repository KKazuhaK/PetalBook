#!/usr/bin/env bash
# Runs the Go tests against a throwaway PostgreSQL cluster.
#
# The state-database tests run on SQLite in the ordinary test run (make test,
# under the race detector). Here, with PETALBOOK_TEST_POSTGRES set, they run on
# PostgreSQL, each test in a schema of its own, and with
# PETALBOOK_TEST_POSTGRES_ONLY=1 the SQLite half is left out: running it again
# would only repeat make test. The race detector stays on: some tests run
# transactions at the same time only on PostgreSQL (SQLite has one writer), so
# make test never races them. checkptr is off for modernc.org packages, as in
# make test (see RACEFLAGS in the Makefile). Only the packages whose tests import
# internal/store/storetest (the one place that reads the variables) are run:
# the others never touch the database. When
# that variable is already set (CI) the database it names is used; otherwise
# this script makes a cluster in a temporary directory on a free port, runs the
# tests with the variable pointing at it, and stops and removes the cluster
# whatever happens. It needs initdb, pg_ctl and psql on PATH.
#
# Usage: scripts/test-postgres.sh [go test arguments]
# Default arguments: every package that uses the state database, PostgreSQL
# half only. With arguments given, both halves run unless the caller sets
# PETALBOOK_TEST_POSTGRES_ONLY=1.
set -euo pipefail

export GOWORK=off
cd "$(dirname "$0")/.."

# The import paths of the packages whose tests use storetest, one per line.
state_db_packages() {
  go list -f '{{.ImportPath}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./cmd/... ./internal/... |
    awk '{ for (i = 2; i <= NF; i++) if ($i ~ /\/internal\/store\/storetest$/) { print $1; next } }'
}

# With no arguments: every such package, PostgreSQL half only.
state_db_args() {
  pkgs="$(state_db_packages)"
  [ -n "$pkgs" ] || { echo "no package uses internal/store/storetest: has it moved?" >&2; exit 1; }
  echo "packages: $(echo "$pkgs" | tr '\n' ' ')" >&2
}

# A database given from outside (CI's service container) is used as it is.
if [ -n "${PETALBOOK_TEST_POSTGRES:-}" ]; then
  echo "PostgreSQL from PETALBOOK_TEST_POSTGRES" >&2
  if [ "$#" -eq 0 ]; then
    state_db_args
    export PETALBOOK_TEST_POSTGRES_ONLY=1
    # shellcheck disable=SC2086
    set -- -race -gcflags=modernc.org/...=-d=checkptr=0 -timeout 20m $pkgs
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
  state_db_args
  export PETALBOOK_TEST_POSTGRES_ONLY=1
  # shellcheck disable=SC2086
  set -- -race -gcflags=modernc.org/...=-d=checkptr=0 -timeout 20m $pkgs
fi
go test "$@"
