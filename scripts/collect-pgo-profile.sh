#!/bin/bash
# Script to collect PGO (Profile-Guided Optimization) profiles for Go builds
# This script runs unit tests to generate CPU profiles for PGO optimization

# Note: GitHub Actions runs bash with -e -o pipefail by default
# We need to be very careful with error handling

set -uo pipefail

PROFILE_DIR="${PROFILE_DIR:-profiles}"
MERGED_PROFILE="${MERGED_PROFILE:-default.pgo}"
GO_TAGS="${GO_TAGS:-go_json}"

echo "🔍 Collecting PGO profiles..."
echo "Profile directory: ${PROFILE_DIR}"
echo "Merged profile: ${MERGED_PROFILE}"

# Create profile directory
mkdir -p "${PROFILE_DIR}"

# Clean old profiles
rm -f "${PROFILE_DIR}"/*.prof 2>/dev/null || true

# Collect profile from unit tests
echo "📊 Running unit tests with CPU profiling..."

# Clean test cache to ensure tests actually run
echo "Cleaning test cache..."
set +e
go clean -testcache >/dev/null 2>&1
CLEAN_EXIT=$?
set -e
if [ ${CLEAN_EXIT} -ne 0 ]; then
    echo "⚠️  Failed to clean test cache, continuing..."
fi

# Collect benchmark profiles for hot paths
echo "🔥 Running benchmarks for hot paths..."

# Run benchmarks for each package separately (cpuprofile requires single package)
# Focus on proxy/forwarding hot paths: keypool selection, encryption, buffer pool, JSON processing
declare -a BENCH_PACKAGES=(
    "./internal/keypool:^Benchmark(SelectKey|RealisticWorkload)"
    "./internal/encryption:^Benchmark(Encrypt|Decrypt|Hash)"
    "./internal/utils:^Benchmark(BufferPool|TieredBufferPooling|JSONEncoder|WeightedRandomSelect|ApplyModelMapping|RealisticWorkload)"
)
BENCH_INDEX=0

for pkg_pattern in "${BENCH_PACKAGES[@]}"; do
    BENCH_INDEX=$((BENCH_INDEX + 1))
    PKG="${pkg_pattern%%:*}"
    PATTERN="${pkg_pattern##*:}"
    BENCHMARK_PROFILE="${PROFILE_DIR}/cpu_bench_${BENCH_INDEX}.prof"

    echo "  Running benchmarks for ${PKG}..."

    set +e
    go test \
        -tags "${GO_TAGS}" \
        -bench="${PATTERN}" \
        -benchtime=2s \
        -cpuprofile="${BENCHMARK_PROFILE}" \
        -run=^$ \
        "${PKG}" >/dev/null 2>&1
    BENCH_EXIT=$?
    set -e

    if [ ${BENCH_EXIT} -eq 0 ] && [ -f "${BENCHMARK_PROFILE}" ]; then
        SIZE=$(stat -c%s "${BENCHMARK_PROFILE}" 2>/dev/null || stat -f%z "${BENCHMARK_PROFILE}" 2>/dev/null || echo "0")
        if [ "${SIZE}" -gt 0 ]; then
            echo "    ✓ Benchmark profile created: ${SIZE} bytes"
        else
            echo "    ✗ Empty benchmark profile, removing"
            rm -f "${BENCHMARK_PROFILE}"
        fi
    else
        echo "    ⚠️  No benchmarks found or profiling failed"
        rm -f "${BENCHMARK_PROFILE}" 2>/dev/null || true
    fi
done

# Get all packages with tests (only project packages, not dependencies)
echo "Listing packages..."

# Get module name from go.mod to filter project packages
# This prevents testing third-party dependencies which pollute PGO profiles
MODULE_NAME=$(go list -m 2>/dev/null || echo "gpt-load")
echo "Module name: ${MODULE_NAME}"

# Use a temporary file to avoid pipefail issues
# Only list project packages (exclude main package to avoid web/dist dependency)
TEMP_PACKAGES=$(mktemp)
set +e
# Redirect stderr to /dev/null to avoid download messages
# Use -e flag to continue on errors
go list -e ./internal/... 2>/dev/null > "${TEMP_PACKAGES}"
LIST_EXIT=$?
set -e

if [ ${LIST_EXIT} -eq 0 ]; then
    # Filter: only keep lines that start with module name (project packages)
    # This excludes third-party dependencies, vendor packages, and download messages
    # Use grep -F for fixed-string matching to avoid regex metacharacter issues
    PACKAGES=$(grep -F "${MODULE_NAME}/" "${TEMP_PACKAGES}" 2>/dev/null | grep -v '/vendor/' || echo "")
else
    echo "⚠️  go list failed, trying to continue..."
    PACKAGES=""
fi
rm -f "${TEMP_PACKAGES}"

if [ -z "${PACKAGES}" ]; then
    echo "⚠️  No packages found, creating minimal profile..."
    echo "PGO profile placeholder" > "${MERGED_PROFILE}"
    exit 0
fi

PACKAGE_COUNT=$(echo "${PACKAGES}" | wc -l | tr -d ' ')
echo "Found ${PACKAGE_COUNT} packages"

# Run tests for each package individually (required for -cpuprofile)
PROFILE_INDEX=0
PACKAGES_WITH_TESTS=0
PACKAGES_WITHOUT_TESTS=0

for pkg in ${PACKAGES}; do
    PROFILE_INDEX=$((PROFILE_INDEX + 1))
    PROFILE_PATH="${PROFILE_DIR}/cpu_test_${PROFILE_INDEX}.prof"

    echo "Testing ${pkg}..."
    echo "  Profile: ${PROFILE_PATH}"

    # Use -tags to match build environment (go_json for high-performance JSON)
    # Capture both stdout and stderr, but don't fail on test failures
    set +e  # Temporarily disable exit on error
    go test \
        -tags "${GO_TAGS}" \
        -cpuprofile="${PROFILE_PATH}" \
        -count=1 \
        "${pkg}" >/dev/null 2>&1
    TEST_EXIT_CODE=$?
    set -e  # Re-enable exit on error

    if [ ${TEST_EXIT_CODE} -eq 0 ]; then
        echo "  ✓ Tests passed"
    else
        echo "  ⚠️  Tests failed or no tests found (exit code: ${TEST_EXIT_CODE}), but profile may still be generated"
    fi

    # Check if profile was created
    if [ -f "${PROFILE_PATH}" ]; then
        # Get file size (Linux uses -c, macOS uses -f)
        SIZE=0
        if command -v stat >/dev/null 2>&1; then
            # Try Linux stat first (GitHub Actions uses Linux)
            SIZE=$(stat -c%s "${PROFILE_PATH}" 2>/dev/null || stat -f%z "${PROFILE_PATH}" 2>/dev/null || echo "0")
        fi

        if [ "${SIZE}" -gt 0 ]; then
            echo "  ✓ Profile created: ${SIZE} bytes"
            PACKAGES_WITH_TESTS=$((PACKAGES_WITH_TESTS + 1))
        else
            echo "  ✗ Empty profile, removing"
            rm -f "${PROFILE_PATH}"
            PACKAGES_WITHOUT_TESTS=$((PACKAGES_WITHOUT_TESTS + 1))
        fi
    else
        echo "  ✗ No profile created (no tests)"
        PACKAGES_WITHOUT_TESTS=$((PACKAGES_WITHOUT_TESTS + 1))
    fi
done

echo ""
echo "Summary:"
echo "  Packages with tests: ${PACKAGES_WITH_TESTS}"
echo "  Packages without tests: ${PACKAGES_WITHOUT_TESTS}"

# Count collected profiles
PROFILE_COUNT=0
if [ -d "${PROFILE_DIR}" ]; then
    PROFILE_COUNT=$(find "${PROFILE_DIR}" -name "*.prof" -type f -size +0 2>/dev/null | wc -l | tr -d ' ')
fi
echo "✅ Collected ${PROFILE_COUNT} profile(s)"

if [ "${PROFILE_COUNT}" -eq 0 ]; then
    echo "⚠️  No profiles collected from tests"
    echo "Creating minimal profile for build compatibility..."
    echo "PGO profile placeholder" > "${MERGED_PROFILE}"
    exit 0
fi

# Merge profiles using go tool pprof
echo "🔄 Merging profiles into ${MERGED_PROFILE}..."

# Use go tool pprof to merge profiles - redirect output to file directly
set +e  # Temporarily disable exit on error
go tool pprof -proto -output "${MERGED_PROFILE}" "${PROFILE_DIR}"/*.prof 2>/dev/null
MERGE_EXIT_CODE=$?
set -e  # Re-enable exit on error

if [ ${MERGE_EXIT_CODE} -ne 0 ]; then
    echo "⚠️  Profile merge failed, using first available profile as fallback"
    FIRST_PROFILE=$(find "${PROFILE_DIR}" -name "*.prof" -type f -size +0 2>/dev/null | head -n 1 || echo "")
    if [ -n "${FIRST_PROFILE}" ] && [ -f "${FIRST_PROFILE}" ]; then
        cp "${FIRST_PROFILE}" "${MERGED_PROFILE}"
    else
        echo "Creating minimal profile for build compatibility..."
        echo "PGO profile placeholder" > "${MERGED_PROFILE}"
        exit 0
    fi
fi

# Verify the merged profile
if [ -f "${MERGED_PROFILE}" ] && [ -s "${MERGED_PROFILE}" ]; then
    PROFILE_SIZE=$(du -h "${MERGED_PROFILE}" 2>/dev/null | cut -f1 || echo "unknown")
    echo "✅ PGO profile ready: ${MERGED_PROFILE} (${PROFILE_SIZE})"

    # Show profile statistics
    echo "📈 Profile statistics:"
    set +e  # Temporarily disable exit on error
    go tool pprof -top -nodecount=5 "${MERGED_PROFILE}" 2>/dev/null
    set -e  # Re-enable exit on error
else
    echo "Creating minimal profile for build compatibility..."
    echo "PGO profile placeholder" > "${MERGED_PROFILE}"
fi

echo "✅ Profile collection complete!"

# Clean up temporary files
echo "🧹 Cleaning up temporary files..."
CLEANUP_COUNT=0

# Remove test binaries
for test_binary in *.test *.test.exe; do
    if [ -f "${test_binary}" ]; then
        rm -f "${test_binary}"
        CLEANUP_COUNT=$((CLEANUP_COUNT + 1))
    fi
done

# Remove coverage files
if [ -f "coverage" ]; then
    rm -f "coverage"
    CLEANUP_COUNT=$((CLEANUP_COUNT + 1))
fi
if [ -f "coverage.out" ]; then
    rm -f "coverage.out"
    CLEANUP_COUNT=$((CLEANUP_COUNT + 1))
fi

# Remove individual profile files (keep only merged profile)
if [ -d "${PROFILE_DIR}" ]; then
    PROFILE_FILES=$(find "${PROFILE_DIR}" -name "*.prof" -type f 2>/dev/null || echo "")
    if [ -n "${PROFILE_FILES}" ]; then
        for prof_file in ${PROFILE_FILES}; do
            if [ -f "${prof_file}" ]; then
                rm -f "${prof_file}"
                CLEANUP_COUNT=$((CLEANUP_COUNT + 1))
            fi
        done
    fi

    # Remove profile directory if empty
    if [ -d "${PROFILE_DIR}" ]; then
        REMAINING=$(ls -A "${PROFILE_DIR}" 2>/dev/null || echo "")
        if [ -z "${REMAINING}" ]; then
            rmdir "${PROFILE_DIR}" 2>/dev/null || true
        fi
    fi
fi

if [ "${CLEANUP_COUNT}" -gt 0 ]; then
    echo "✅ Cleaned up ${CLEANUP_COUNT} temporary file(s)"
fi
