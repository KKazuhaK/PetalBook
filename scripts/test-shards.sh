#!/usr/bin/env bash
# Splits the tests of one Go package into shards that run on machines of their
# own, and checks that the shards are exactly the tests of the package.
#
# A shard is a set of top-level test names (Test, Example and Fuzz functions;
# the subtests of a test go with it), passed to go test as -run '^(A|B|...)$'.
# The split needs no list to be kept: a test goes to the shard of the hash of
# its name, so a new test lands somewhere by itself and no other test moves. The
# few tests that take far longer than the rest are named in HEAVY_FILE and are
# dealt out in turn first, so that two of them never share a shard by chance.
#
# Usage:
#   scripts/test-shards.sh names PKG            every test of PKG, one per line
#   scripts/test-shards.sh list  PKG N K        the tests of shard K of N (1..N)
#   scripts/test-shards.sh run   PKG N K [ARG]  go test -race of shard K, with the
#                                               arguments after K added
#   scripts/test-shards.sh check PKG N          fails unless every test of PKG is
#                                               in exactly one of the N shards
#
# The race detector is on for names and check as well, so that the test binary
# they build is the one the run uses (it is built once and cached).
set -euo pipefail

export GOWORK=off
cd "$(dirname "$0")/.."
heavy_file="${HEAVY_FILE:-scripts/test-shards.heavy}"
timeout="${GOTEST_TIMEOUT:-30m}"

die() { echo "test-shards: $*" >&2; exit 2; }

# tests: keeps the lines of go test -list that are test names, whatever
# characters (a letter that is not ASCII is allowed in a Go name) they have; the
# rest is the "ok ..." line and any notice.
tests() { grep -E '^(Test|Example|Fuzz)' || true; }

# names PKG: the tests go test would run, one per line, sorted.
names() {
  local out
  out="$(go test -race -list '.' "$1")" || { echo "$out" >&2; die "go test -list $1 failed"; }
  tests <<<"$out" | LC_ALL=C sort -u
}

# assign N: reads names on stdin and writes "K name", K in 1..N.
assign() {
  local n="$1" heavy=""
  [ -r "$heavy_file" ] && heavy="$(grep -vE '^[[:space:]]*(#|$)' "$heavy_file" || true)"
  LC_ALL=C awk -v n="$n" -v heavy="$heavy" '
    BEGIN {
      for (i = 0; i < 256; i++) ord[sprintf("%c", i)] = i
      nh = split(heavy, h, "\n")
      for (i = 1; i <= nh; i++) if (h[i] != "") pinned[h[i]] = 1
    }
    { all[NR] = $0 }
    END {
      # The heavy tests first, in the order of the file, one shard after another.
      turn = 0
      for (i = 1; i <= nh; i++) {
        if (h[i] == "") continue
        for (j = 1; j <= NR; j++) if (all[j] == h[i]) { print (turn % n) + 1, all[j]; turn++ }
      }
      for (j = 1; j <= NR; j++) {
        if (all[j] in pinned) continue
        v = 5381
        for (c = 1; c <= length(all[j]); c++) v = (v * 33 + ord[substr(all[j], c, 1)]) % 4294967291
        print (v % n) + 1, all[j]
      }
    }'
}

# regex FILE K: the -run expression of shard K, from the output of assign.
regex() {
  local list
  # Names are escaped, so that no character of one is read as part of the expression.
  list="$(awk -v k="$2" '$1 == k { print $2 }' "$1" | sed 's/[][\\.*+?(){}|^$]/\\&/g' | paste -sd'|' -)"
  printf '^(%s)$' "$list"
}

# need_shard N K: N and K are numbers and shard K of N exists.
need_shard() {
  case "$1" in '' | *[!0-9]*) die "N and K must be numbers" ;; esac
  case "$2" in '' | *[!0-9]*) die "N and K must be numbers" ;; esac
  [ "$1" -ge 1 ] && [ "$2" -ge 1 ] && [ "$2" -le "$1" ] || die "shard $2 of $1 does not exist"
}

cmd="${1:-}"
case "$cmd" in
  names)
    [ "$#" -eq 2 ] || die "usage: names PKG"
    names "$2"
    ;;
  list)
    [ "$#" -eq 4 ] || die "usage: list PKG N K"
    need_shard "$3" "$4"
    names "$2" | assign "$3" | awk -v k="$4" '$1 == k { print $2 }'
    ;;
  run)
    [ "$#" -ge 4 ] || die "usage: run PKG N K [go test arguments]"
    need_shard "$3" "$4"
    pkg="$2" n="$3" k="$4"
    shift 4
    work="$(mktemp -d "${TMPDIR:-/tmp}/test-shards.XXXXXX")"
    trap 'rm -rf "$work"' EXIT
    names "$pkg" | assign "$n" >"$work/assign"
    re="$(regex "$work/assign" "$k")"
    [ "$re" != '^()$' ] || { echo "shard $k of $n has no tests" >&2; exit 1; }
    echo "shard $k of $n of $pkg: $(tr -cd '|' <<<"$re" | wc -c | tr -d ' ') + 1 tests"
    # Not exec: the trap removes the work folder when go test is done.
    go test -race -timeout "$timeout" -run "$re" "$@" "$pkg"
    ;;
  check)
    [ "$#" -eq 3 ] || die "usage: check PKG N"
    pkg="$2" n="$3"
    need_shard "$n" 1
    work="$(mktemp -d "${TMPDIR:-/tmp}/test-shards.XXXXXX")"
    trap 'rm -rf "$work"' EXIT
    names "$pkg" >"$work/all"
    [ -s "$work/all" ] || die "$pkg has no tests: has it moved?"
    assign "$n" <"$work/all" >"$work/assign"
    # Ask go test itself which tests each shard's -run expression selects, so
    # that the expression is what is checked and not only the assignment.
    : >"$work/seen"
    for k in $(seq 1 "$n"); do
      re="$(regex "$work/assign" "$k")"
      [ "$re" = '^()$' ] && continue
      out="$(go test -race -list "$re" "$pkg")" || { echo "$out" >&2; die "go test -list $pkg failed"; }
      tests <<<"$out" >>"$work/seen"
    done
    LC_ALL=C sort "$work/seen" >"$work/seen.sorted"
    twice="$(uniq -d "$work/seen.sorted")"
    missing="$(LC_ALL=C comm -23 "$work/all" <(uniq "$work/seen.sorted"))"
    extra="$(LC_ALL=C comm -13 "$work/all" <(uniq "$work/seen.sorted"))"
    status=0
    [ -z "$twice" ] || { echo "in more than one shard:" >&2; echo "$twice" >&2; status=1; }
    [ -z "$missing" ] || { echo "in no shard:" >&2; echo "$missing" >&2; status=1; }
    [ -z "$extra" ] || { echo "selected but not a test of the package:" >&2; echo "$extra" >&2; status=1; }
    if [ "$status" -eq 0 ]; then
      echo "$pkg: $(wc -l <"$work/all" | tr -d ' ') tests, each in exactly one of $n shards"
      for k in $(seq 1 "$n"); do
        echo "  shard $k: $(awk -v k="$k" '$1 == k' "$work/assign" | wc -l | tr -d ' ')"
      done
    fi
    exit "$status"
    ;;
  *)
    die "usage: $0 names|list|run|check ..."
    ;;
esac
