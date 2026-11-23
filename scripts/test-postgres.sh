#!/usr/bin/env bash
# Runs the Go tests against a throwaway PostgreSQL cluster.
#
# The state-database tests always run on SQLite; with PETALBOOK_TEST_POSTGRES
# set they run on PostgreSQL as well, each test in a schema of its own. Only
# the packages whose tests import internal/store/storetest (the one place that
# reads the variable) are run: the others never touch the database. When
# that variable is already set (CI) the database it names is used; otherwise
# this script makes a cluster in a temporary directory on a free port, runs the
# tests with the variable pointing at it, and stops and removes the cluster
# whatever happens. It needs initdb, pg_ctl and psql on PATH.
#
# Usage: scripts/test-postgres.sh [go test arguments]
# Default arguments: -race and every package that uses the state database.
set -euo pipefail

export GOWORK=off
cd "$(dirname "$0")/.."

# The import paths of the packages whose tests use storetest, one per line.
state_db_packages() {
  go list -f '{{.ImportPath}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./cmd/... ./internal/... |
    awk '{ for (i = 2; i <= NF; i++) if ($i ~ /\/internal\/store\/storetest$/) { print $1; next } }'
}

# With no arguments: the race detector and every such package.
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
    # shellcheck disable=SC2086
    # -race and both databases per test: internal/api alone nears go test's default
    # 10 minutes on a busy machine, so the run gets 30.
    set -- -race -timeout 30m $pkgs
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
  # shellcheck disable=SC2086
  # -race and both databases per test: internal/api alone nears go test's default
  # 10 minutes on a busy machine, so the run gets 30.
  set -- -race -timeout 30m $pkgs
fi
go test "$@"
