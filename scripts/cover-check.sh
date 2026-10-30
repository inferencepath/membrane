#!/usr/bin/env bash
#
# cover-check.sh: enforce coverage thresholds.
#
#   * Overall statement coverage must be >= OVERALL_MIN (default 90%).
#   * Critical packages (the Membrane and the boundary types) must be at
#     CRITICAL_MIN (default 100%): internal/membrane/** and internal/types/**.
#
# Packages with no executable statements (pure type/interface declarations) are
# treated as satisfying the critical threshold.
#
# Usage: scripts/cover-check.sh [coverage-profile]
set -euo pipefail

PROFILE="${1:-coverage.out}"
OVERALL_MIN="${OVERALL_MIN:-90.0}"
CRITICAL_MIN="${CRITICAL_MIN:-100.0}"

if [[ ! -f "${PROFILE}" ]]; then
  echo "cover-check: coverage profile '${PROFILE}' not found" >&2
  echo "cover-check: run 'go test ./... -coverprofile=${PROFILE} -covermode=atomic' first" >&2
  exit 2
fi

fail=0

# --- Overall ----------------------------------------------------------------
overall="$(go tool cover -func="${PROFILE}" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')"
if [[ -z "${overall}" ]]; then
  echo "cover-check: could not determine overall coverage" >&2
  exit 2
fi

awk -v got="${overall}" -v min="${OVERALL_MIN}" 'BEGIN { exit !(got+0 >= min+0) }' || {
  echo "cover-check: FAIL overall coverage ${overall}% < ${OVERALL_MIN}%"
  fail=1
}
echo "cover-check: overall coverage ${overall}% (min ${OVERALL_MIN}%)"

# --- Critical packages ------------------------------------------------------
# Collect per-function coverage, then aggregate per critical file. A file with
# no statements never appears in `go tool cover -func`, which is fine (vacuously
# complete).
while IFS= read -r line; do
  file="$(echo "${line}" | awk '{print $1}')"
  pct="$(echo "${line}" | awk '{gsub(/%/,"",$NF); print $NF}')"
  case "${file}" in
    *github.com/inferencepath/membrane/internal/membrane/*|*github.com/inferencepath/membrane/internal/types/*)
      awk -v got="${pct}" -v min="${CRITICAL_MIN}" 'BEGIN { exit !(got+0 >= min+0) }' || {
        echo "cover-check: FAIL critical ${file} at ${pct}% < ${CRITICAL_MIN}%"
        fail=1
      }
      ;;
  esac
done < <(go tool cover -func="${PROFILE}" | grep -v '^total:')

if [[ "${fail}" -ne 0 ]]; then
  echo "cover-check: thresholds not met"
  exit 1
fi

echo "cover-check: OK (overall >= ${OVERALL_MIN}%, critical == ${CRITICAL_MIN}%)"
