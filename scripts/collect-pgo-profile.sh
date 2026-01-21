#!/bin/bash
# Script to collect PGO (Profile-Guided Optimization) profiles for Go builds
# This script runs unit tests and performance benchmarks to generate CPU profiles
# that will be used to optimize the final binary build.

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

# Collect profile from unit tests with coverage
# Run tests with CPU profiling enabled
echo "📊 Running unit tests with CPU profiling..."
go test -tags "${GO_TAGS}" \
    -cpuprofile="${PROFILE_DIR}/cpu_test.prof" \
    -run=. \
    -count=1 \
    ./internal/... || {
    echo "⚠️  Some tests failed, but continuing with profile collection"
}

# Collect profiles from performance benchmarks
# Run key benchmarks that represent real-world usage patterns
echo "📊 Running performance benchmarks with CPU profiling..."

# Run benchmarks for critical paths (limit time to avoid excessive duration)
go test -tags "${GO_TAGS}" \
    -bench=. \
    -benchtime=2s \
    -cpuprofile="${PROFILE_DIR}/cpu_bench.prof" \
    -run=^$ \
    ./internal/keypool \
    ./internal/services \
    ./internal/utils \
    ./internal/channel \
    ./internal/proxy \
    ./internal/encryption \
    2>/dev/null || {
    echo "⚠️  Some benchmarks failed, but continuing with profile collection"
}

# Count collected profiles
PROFILE_COUNT=$(find "${PROFILE_DIR}" -name "*.prof" -type f | wc -l)
echo "✅ Collected ${PROFILE_COUNT} profile(s)"

if [ "${PROFILE_COUNT}" -eq 0 ]; then
    echo "❌ No profiles collected, cannot proceed"
    exit 1
fi

# Merge profiles using go tool pprof
# Go 1.21+ supports merging multiple profiles
echo "🔄 Merging profiles into ${MERGED_PROFILE}..."

# Use go tool pprof to merge profiles
# The -proto flag outputs in protobuf format which is what Go compiler expects
go tool pprof -proto "${PROFILE_DIR}"/*.prof > "${MERGED_PROFILE}" 2>/dev/null || {
    echo "⚠️  Profile merge failed, using first profile as fallback"
    cp "${PROFILE_DIR}/cpu_test.prof" "${MERGED_PROFILE}" 2>/dev/null || {
        cp "${PROFILE_DIR}/cpu_bench.prof" "${MERGED_PROFILE}" 2>/dev/null || {
            echo "❌ Failed to create merged profile"
            exit 1
        }
    }
}

# Verify the merged profile
if [ -f "${MERGED_PROFILE}" ] && [ -s "${MERGED_PROFILE}" ]; then
    PROFILE_SIZE=$(du -h "${MERGED_PROFILE}" | cut -f1)
    echo "✅ PGO profile ready: ${MERGED_PROFILE} (${PROFILE_SIZE})"

    # Show profile statistics
    echo "📈 Profile statistics:"
    go tool pprof -top -nodecount=5 "${MERGED_PROFILE}" 2>/dev/null || true
else
    echo "❌ Failed to create valid merged profile"
    exit 1
fi

echo "✅ Profile collection complete!"
