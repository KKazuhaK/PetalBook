#!/usr/bin/env bash
# Runs make targets in lanes, and says how long each took.
#
# The targets of one lane run one after the other and stop at the first that
# fails; the lanes of a phase run side by side. A failed lane does not stop the
# others, so one run shows everything that is wrong. The output of a target is
# printed, whole, when it ends, so lines from two targets never mix. Phases
# (separated by --) run one after the other, and a phase that failed ends the
# run. The exit status is 0 only when every target passed.
#
# Usage: scripts/lanes.sh LANE... [-- LANE...]
#   LANE is NAME='target target ...', or a single target name.
#
#   scripts/lanes.sh go='vet test' web vuln -- release
set -uo pipefail

cd "$(dirname "$0")/.."
make_cmd="${MAKE:-make}"

work="$(mktemp -d "${TMPDIR:-/tmp}/petalbook-lanes.XXXXXX")"
trap 'rm -rf "$work"' EXIT
: >"$work/summary"

# One writer at a time, so output of two targets does not interleave. (mkdir is
# atomic everywhere; flock is not on macOS.)
lock() { until mkdir "$work/lock" 2>/dev/null; do sleep 0.05; done; }
unlock() { rmdir "$work/lock"; }

# run_lane PHASE LANE TARGETS...
run_lane() {
  local lane="$1"
  shift
  local target start end secs status failed=""
  for target in "$@"; do
    if [ -n "$failed" ]; then
      printf '%s\t%s\t-\tskipped\n' "$lane" "$target" >>"$work/summary.$lane"
      continue
    fi
    start=$(date +%s)
    "$make_cmd" --no-print-directory "$target" >"$work/out.$lane.$target" 2>&1
    status=$?
    end=$(date +%s)
    secs=$((end - start))
    lock
    if [ "$status" -eq 0 ]; then
      printf '=== [%s] %s: ok, %ds\n' "$lane" "$target" "$secs"
    else
      printf '=== [%s] %s: FAILED (exit %d), %ds\n' "$lane" "$target" "$status" "$secs"
    fi
    cat "$work/out.$lane.$target"
    unlock
    if [ "$status" -eq 0 ]; then
      printf '%s\t%s\t%d\tok\n' "$lane" "$target" "$secs" >>"$work/summary.$lane"
    else
      printf '%s\t%s\t%d\tFAILED\n' "$lane" "$target" "$secs" >>"$work/summary.$lane"
      failed=1
    fi
  done
  [ -z "$failed" ]
}

overall=0
began=$(date +%s)
phase_lanes=()

run_phase() {
  local spec name targets pids=() ok=0 lane
  phase_lanes=()
  for spec in "$@"; do
    case "$spec" in
      *=*) name="${spec%%=*}" targets="${spec#*=}" ;;
      *) name="$spec" targets="$spec" ;;
    esac
    phase_lanes+=("$name")
    # shellcheck disable=SC2086
    run_lane "$name" $targets &
    pids+=($!)
  done
  for pid in "${pids[@]}"; do
    wait "$pid" || ok=1
  done
  for lane in "${phase_lanes[@]}"; do
    cat "$work/summary.$lane" >>"$work/summary" 2>/dev/null
  done
  return "$ok"
}

skip_phase() {
  local spec name targets target
  for spec in "$@"; do
    case "$spec" in
      *=*) name="${spec%%=*}" targets="${spec#*=}" ;;
      *) name="$spec" targets="$spec" ;;
    esac
    for target in $targets; do
      printf '%s\t%s\t-\tskipped\n' "$name" "$target" >>"$work/summary"
    done
  done
}

# Run the phases in order: the arguments up to each "--" are one phase.
stopped=""
phase=()
for arg in "$@" --; do
  if [ "$arg" != "--" ]; then
    phase+=("$arg")
    continue
  fi
  [ "${#phase[@]}" -gt 0 ] || continue
  if [ -n "$stopped" ]; then
    skip_phase "${phase[@]}"
  elif ! run_phase "${phase[@]}"; then
    overall=1
    stopped=1
  fi
  phase=()
done

finished=$(date +%s)
echo
echo "---------------------------------------------------------------"
printf '%-14s %-18s %8s  %s\n' lane target seconds result
while IFS=$'\t' read -r lane target secs result; do
  printf '%-14s %-18s %8s  %s\n' "$lane" "$target" "$secs" "$result"
done <"$work/summary"
printf '%-14s %-18s %8d  %s\n' "wall clock" "" "$((finished - began))" "$([ "$overall" -eq 0 ] && echo ok || echo FAILED)"
exit "$overall"
