#!/bin/bash
# check-coverage.sh - Verify code coverage meets per-package thresholds
#
# Usage: ./hack/check-coverage.sh <coverage-profile>
#
# Reads thresholds from .coverage-thresholds.yaml and checks the coverage
# profile against them. Exits with 1 if any threshold is violated or if
# new packages are found that are not listed in the thresholds file.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
THRESHOLDS_FILE="${REPO_ROOT}/.coverage-thresholds.yaml"
COVERAGE_FILE="${1:?Usage: $0 <coverage-profile>}"

if [[ ! -f "${COVERAGE_FILE}" ]]; then
    echo "ERROR: Coverage profile not found: ${COVERAGE_FILE}"
    exit 1
fi

if [[ ! -f "${THRESHOLDS_FILE}" ]]; then
    echo "ERROR: Thresholds file not found: ${THRESHOLDS_FILE}"
    exit 1
fi

# Module path prefix to strip from coverage output
MODULE="github.com/openshift-kni/oran-o2ims"

# Parse the overall threshold
OVERALL_THRESHOLD=$(grep '^overall:' "${THRESHOLDS_FILE}" | awk '{print $2}')

# Get overall coverage from the profile
OVERALL_COVERAGE=$(go tool cover -func="${COVERAGE_FILE}" | grep '^total:' | awk '{gsub(/%/,""); print $NF}')

echo "=== Code Coverage Report ==="
echo ""

# Collect per-package coverage from the profile
declare -A pkg_coverage
while IFS= read -r line; do
    # Each line from go tool cover -func is: file:line func coverage%
    # We want to aggregate by package
    file=$(echo "${line}" | awk '{print $1}')
    cov=$(echo "${line}" | awk '{gsub(/%/,""); print $NF}')

    # Extract package path (strip module prefix and filename)
    pkg="${file#"${MODULE}"/}"
    pkg="${pkg%/*}"

    if [[ -n "${pkg}" && "${pkg}" != "total:" ]]; then
        # Accumulate for averaging
        if [[ -z "${pkg_coverage[${pkg}]+x}" ]]; then
            pkg_coverage[${pkg}]="${cov}"
        else
            pkg_coverage[${pkg}]="${pkg_coverage[${pkg}]} ${cov}"
        fi
    fi
done < <(go tool cover -func="${COVERAGE_FILE}" | grep -v '^total:')

# Calculate per-package averages
declare -A pkg_avg
for pkg in "${!pkg_coverage[@]}"; do
    values="${pkg_coverage[${pkg}]}"
    sum=0
    count=0
    for v in ${values}; do
        sum=$(echo "${sum} + ${v}" | bc)
        count=$((count + 1))
    done
    avg=$(echo "scale=1; ${sum} / ${count}" | bc)
    pkg_avg[${pkg}]="${avg}"
done

# Collect known packages from thresholds file
declare -A known_packages
in_pkgs=false
while IFS= read -r line; do
    [[ "${line}" =~ ^[[:space:]]*# ]] && continue
    [[ -z "${line}" ]] && continue
    if [[ "${line}" == "packages:" ]]; then
        in_pkgs=true
        continue
    fi
    if ${in_pkgs}; then
        kpkg=$(echo "${line}" | sed 's/^[[:space:]]*//' | cut -d: -f1)
        known_packages[${kpkg}]=1
    fi
done < "${THRESHOLDS_FILE}"

# Check thresholds
FAILURES=0

# Print header
printf "%-65s %8s %8s %s\n" "Package" "Coverage" "Min" "Status"
printf "%-65s %8s %8s %s\n" "-------" "--------" "---" "------"

# Read per-package thresholds and check
in_packages=false
while IFS= read -r line; do
    # Skip comments and empty lines
    [[ "${line}" =~ ^[[:space:]]*# ]] && continue
    [[ -z "${line}" ]] && continue
    [[ "${line}" =~ ^overall: ]] && continue

    if [[ "${line}" == "packages:" ]]; then
        in_packages=true
        continue
    fi

    if ${in_packages}; then
        # Parse "  package/path: threshold"
        pkg=$(echo "${line}" | sed 's/^[[:space:]]*//' | cut -d: -f1)
        threshold=$(echo "${line}" | cut -d: -f2 | tr -d ' ')

        actual="${pkg_avg[${pkg}]:-N/A}"

        if [[ "${actual}" == "N/A" ]]; then
            printf "%-65s %7s%% %7s%% %s\n" "${pkg}" "${actual}" "${threshold}" "⚠ NOT FOUND"
            continue
        fi

        # Compare (using bc for float comparison)
        if (( $(echo "${actual} < ${threshold}" | bc -l) )); then
            printf "%-65s %7s%% %7s%% %s\n" "${pkg}" "${actual}" "${threshold}" "✗ FAIL"
            FAILURES=$((FAILURES + 1))
        else
            printf "%-65s %7s%% %7s%% %s\n" "${pkg}" "${actual}" "${threshold}" "✓ OK"
        fi
    fi
done < "${THRESHOLDS_FILE}"

# Check for packages in coverage profile that are not in thresholds file
for pkg in "${!pkg_avg[@]}"; do
    if [[ -z "${known_packages[${pkg}]+x}" ]]; then
        printf "%-65s %7s%%          %s\n" "${pkg}" "${pkg_avg[${pkg}]}" "✗ NOT IN THRESHOLDS"
        FAILURES=$((FAILURES + 1))
    fi
done

echo ""
printf "%-65s %7s%% %7s%% " "OVERALL" "${OVERALL_COVERAGE}" "${OVERALL_THRESHOLD}"
if (( $(echo "${OVERALL_COVERAGE} < ${OVERALL_THRESHOLD}" | bc -l) )); then
    echo "✗ FAIL"
    FAILURES=$((FAILURES + 1))
else
    echo "✓ OK"
fi

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
    echo "FAILED: ${FAILURES} coverage threshold(s) violated"
    exit 1
else
    echo "PASSED: All coverage thresholds met"
fi
