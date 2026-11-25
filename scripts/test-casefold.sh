#!/usr/bin/env bash
# Runs the Go tests of the packages that touch file names on a case-folding
# tmpfs.
#
# Pages and attachments are files, and macOS and Windows treat "Notes.md" and
# "notes.md" as one file. A Linux tmpfs mounted with the casefold option, with
# the +F attribute on a directory, behaves the same way, so tests that assume
# a case-sensitive file system fail here, on Linux, instead of on a user's
# machine.
#
# This needs no root: "unshare -rm" makes a user and mount namespace in which
# the caller is root, so the tmpfs can be mounted and nothing outside the
# namespace changes. For the same reason, inside it the tests that need an
# unprivileged user (chmod 000 and the like) skip; they run in the normal
# suite. The test binaries are built first, outside the namespace, so only the
# tests themselves run as namespace root.
#
# With CASEFOLD_DIR set to a directory that already folds case (CI mounts an
# ext4 image made with -O casefold there, as root, which works on kernels
# whose tmpfs cannot), the tests run in it directly, as the calling user, and
# no namespace is made; the chmod tests then run as well.
#
# Usage: scripts/test-casefold.sh [flags for the test binaries, e.g. -test.run X]
# Exits 2 when this machine cannot do it: not Linux, user namespaces not
# allowed, a kernel whose tmpfs has no casefold, or a CASEFOLD_DIR that does
# not fold.
set -euo pipefail

export GOWORK=off
cd "$(dirname "$0")/.."

# Only the packages whose tests put files with names of their own on disk.
# Left out on purpose, because they cannot tell a case-folding file system from
# any other (about half of the time this run used to take):
#   internal/index   works on page paths as strings and opens one fixed file name
#                    (index.db); its stress and randomized tests are the slow ones
#   internal/spaces  names directories after page ids, which are upper case only
#                    (page.ValidID), and its tests push single-case file names
# Both run on every pull request in the normal suite.
pkgs="internal/gitstore internal/space internal/api"

if [ "$(uname -s)" != "Linux" ]; then
  echo "test-casefold needs Linux (a tmpfs with casefold)" >&2
  exit 2
fi
# folds DIR: whether DIR finds a file under another spelling of its name.
folds() {
  local p="$1/PetalBook-Probe"
  : >"$p" && [ -e "$1/petalbook-probe" ]
  local ok=$?
  rm -f "$p"
  return "$ok"
}

# The test binaries are linked side by side. They are built without the race
# detector: make test runs these packages under it already, and what this run
# adds is a file system that folds case, which the race detector does not
# care about (it made the run several times slower). The tests themselves run
# one package at a time, so that tests that depend on timing do not compete
# with those of another package.
build() {
  mkdir "$work/bin"
  local pids=() pid status=0
  for pkg in $pkgs; do
    go test -c -o "$work/bin/$(basename "$pkg").test" "./$pkg" &
    pids+=($!)
  done
  for pid in "${pids[@]}"; do
    wait "$pid" || status=1
  done
  return "$status"
}

work="$(mktemp -d "${TMPDIR:-/tmp}/petalbook-casefold.XXXXXX")"
trap 'rm -rf "$work"' EXIT

if [ -n "${CASEFOLD_DIR:-}" ]; then
  if ! folds "$CASEFOLD_DIR"; then
    echo "CASEFOLD_DIR=$CASEFOLD_DIR does not fold case" >&2
    exit 2
  fi
  build
  echo "tests in a case-folding directory ($CASEFOLD_DIR): $pkgs" >&2
  status=0
  for pkg in $pkgs; do
    echo "== $pkg" >&2
    (cd "$pkg" && TMPDIR="$CASEFOLD_DIR" "$work/bin/$(basename "$pkg").test" "$@") || status=1
  done
  exit "$status"
fi

for tool in unshare mount chattr; do
  command -v "$tool" >/dev/null || { echo "$tool is not on PATH" >&2; exit 2; }
done

# Probe first, so "this machine cannot" is told apart from "a test failed".
mkdir "$work/probe"
if ! unshare -rm true 2>/dev/null; then
  echo "unshare -rm is not allowed here: unprivileged user namespaces are disabled or restricted" >&2
  echo "(on Ubuntu 24.04: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)" >&2
  exit 2
fi
if ! unshare -rm sh -c 'mount -t tmpfs -o casefold,size=16M none "$1" && mkdir "$1/t" && chattr +F "$1/t" && : >"$1/t/Probe" && [ -e "$1/t/probe" ]' sh "$work/probe" 2>/dev/null; then
  echo "this kernel's tmpfs has no casefold support (Linux 6.13 or newer is needed)" >&2
  exit 2
fi

# Build every test binary outside the namespace.
build

# The tmpfs is mounted inside the namespace and goes away with it.
mkdir "$work/mnt"
echo "tests on a case-folding tmpfs: $pkgs" >&2
PKGS="$pkgs" unshare -rm sh -c '
  set -eu
  bin="$1"; dir="$2"; shift 2
  mount -t tmpfs -o casefold,size=4G none "$dir"
  mkdir "$dir/t"
  chattr +F "$dir/t"
  export TMPDIR="$dir/t"
  status=0
  for pkg in $PKGS; do
    echo "== $pkg" >&2
    (cd "$pkg" && "$bin/$(basename "$pkg").test" "$@") || status=1
  done
  exit "$status"
' sh "$work/bin" "$work/mnt" "$@"
