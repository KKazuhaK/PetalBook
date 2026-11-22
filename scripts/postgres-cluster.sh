# Sourced by scripts/test-postgres.sh and scripts/contract-postgres.sh: makes a
# throwaway PostgreSQL cluster in a temporary directory, on a free local port.
# It needs initdb, pg_ctl and psql on PATH.
#
#   pg_cluster_start                 sets PG_PORT; the cluster user is petalbook, no password
#   pg_cluster_create_database NAME  makes an empty database in it
#   pg_cluster_stop                  stops the cluster and removes its files; safe to call
#                                    any time, so a caller puts it in its EXIT trap
#
# The caller owns "set -euo pipefail" and the trap.

PG_WORK=""
PG_STARTED=""
PG_PORT=""

pg_cluster_stop() {
  if [ -n "$PG_STARTED" ]; then
    pg_ctl -D "$PG_WORK/data" -m immediate -w stop >/dev/null 2>&1 || true
    PG_STARTED=""
  fi
  if [ -n "$PG_WORK" ]; then
    rm -rf "$PG_WORK"
    PG_WORK=""
  fi
}

pg_cluster_start() {
  local tool p
  for tool in initdb pg_ctl psql; do
    command -v "$tool" >/dev/null || { echo "$tool is not on PATH; install PostgreSQL" >&2; exit 2; }
  done

  PG_WORK="$(mktemp -d "${TMPDIR:-/tmp}/petalbook-pg.XXXXXX")"

  # A port nothing is listening on. Another process may still grab it before
  # the cluster starts; then pg_ctl fails and so does the caller, loudly.
  PG_PORT=""
  for _ in $(seq 1 100); do
    p=$((20000 + RANDOM % 30000))
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
      PG_PORT="$p"
      break
    fi
  done
  [ -n "$PG_PORT" ] || { echo "could not find a free port" >&2; exit 1; }

  # LC_ALL: on some systems postgres refuses to start without a valid locale.
  LC_ALL=C initdb -D "$PG_WORK/data" -U petalbook --auth=trust --locale=C -E UTF8 --no-sync >"$PG_WORK/initdb.log" 2>&1 \
    || { cat "$PG_WORK/initdb.log" >&2; exit 1; }
  # Local TCP only, no socket file (its path has a length limit), and no fsync:
  # the cluster is thrown away.
  cat >>"$PG_WORK/data/postgresql.conf" <<CONF
listen_addresses = '127.0.0.1'
port = $PG_PORT
unix_socket_directories = ''
fsync = off
synchronous_commit = off
full_page_writes = off
max_connections = 300
CONF
  PG_STARTED=1
  LC_ALL=C pg_ctl -D "$PG_WORK/data" -l "$PG_WORK/postgres.log" -w start >"$PG_WORK/pg_ctl.log" 2>&1 \
    || { cat "$PG_WORK/pg_ctl.log" "$PG_WORK/postgres.log" >&2; exit 1; }
}

pg_cluster_create_database() {
  psql -h 127.0.0.1 -p "$PG_PORT" -U petalbook -d postgres -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE $1" >/dev/null
}
