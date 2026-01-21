#!/bin/bash
# Script to collect PGO (Profile-Guided Optimization) profiles for Go builds
# This script runs unit tests to generate CPU profiles for PGO optimization

set -euo pipefail

PROFILE_DIR="${PROFILE_DIR:-profiles}"
MERGED_PROFILE="${MERGED_PROFILE:-default.pgo}"
GO_TAGS="${GO_TAGS:-go_json}"

echo "🔍 Collecting PGO profiles..."
echo "Profile directory: ${PROFILE_DIR}"
echo "Merged profile: ${MERGED_PROFILE}"

# Create profile directory
mkdir -p "${PROFILE_DIR}"

# Clean old profiles
rm -f "${PROFILE_DIR}"/*.prof

# Collect profile from unit tests
echo "📊 Running unit tests with CPU profiling..."

# Clean test cache to ensure tests actually run
echo "Cleaning test cache..."
go clean -testcache

# Get all packages with tests (including main package if it has tests)
PACKAGES=$(go list ./... 2>/dev/null | grep -v '/vendor/')

PACKAGE_COUNT=$(echo "${PACKAGES}" | wc -l)
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
    go test \
        -tags "${GO_TAGS}" \
        -cpuprofile="${PROFILE_PATH}" \
        -count=1 \
        "${pkg}" 2>/dev/null || true

    # Check if profile was created
    if [ -f "${PROFILE_PATH}" ]; then
        # Get file size (Linux uses -c, macOS uses -f)
        if SIZE=$(stat -c%s "${PROFILE_PATH}" 2>/dev/null); then
            : # Linux stat succeeded
        elif SIZE=$(stat -f%z "${PROFILE_PATH}" 2>/dev/null); then
            : # macOS stat succeeded
        else
            SIZE=0
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
PROFILE_COUNT=$(find "${PROFILE_DIR}" -name "*.prof" -type f -size +0 2>/dev/null | wc -l)
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
go tool pprof -proto -output "${MERGED_PROFILE}" "${PROFILE_DIR}"/*.prof 2>/dev/null || {
    echo "⚠️  Profile merge failed, using first available profile as fallback"
    FIRST_PROFILE=$(find "${PROFILE_DIR}" -name "*.prof" -type f -size +0 | head -n 1)
    if [ -n "${FIRST_PROFILE}" ]; then
        cp "${FIRST_PROFILE}" "${MERGED_PROFILE}"
    else
        echo "Creating minimal profile for build compatibility..."
        echo "PGO profile placeholder" > "${MERGED_PROFILE}"
        exit 0
    fi
}

# Verify the merged profile
if [ -f "${MERGED_PROFILE}" ] && [ -s "${MERGED_PROFILE}" ]; then
    PROFILE_SIZE=$(du -h "${MERGED_PROFILE}" | cut -f1)
    echo "✅ PGO profile ready: ${MERGED_PROFILE} (${PROFILE_SIZE})"

    # Show profile statistics
    echo "📈 Profile statistics:"
    go tool pprof -top -nodecount=5 "${MERGED_PROFILE}" 2>/dev/null || true
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
    PROFILE_FILES=$(find "${PROFILE_DIR}" -name "*.prof" -type f 2>/dev/null)
    for prof_file in ${PROFILE_FILES}; do
        rm -f "${prof_file}"
        CLEANUP_COUNT=$((CLEANUP_COUNT + 1))
    done

    # Remove profile directory if empty
    if [ -z "$(ls -A "${PROFILE_DIR}" 2>/dev/null)" ]; then
        rmdir "${PROFILE_DIR}" 2>/dev/null || true
    fi
fi

if [ "${CLEANUP_COUNT}" -gt 0 ]; then
    echo "✅ Cleaned up ${CLEANUP_COUNT} temporary file(s)"
fi
