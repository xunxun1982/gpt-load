# PowerShell script to collect PGO profiles for Windows builds
# This script runs unit tests and performance benchmarks to generate CPU profiles

$ErrorActionPreference = "Stop"

$PROFILE_DIR = if ($env:PROFILE_DIR) { $env:PROFILE_DIR } else { "profiles" }
$MERGED_PROFILE = if ($env:MERGED_PROFILE) { $env:MERGED_PROFILE } else { "default.pgo" }
$GO_TAGS = if ($env:GO_TAGS) { $env:GO_TAGS } else { "go_json" }

Write-Host "🔍 Collecting PGO profiles..." -ForegroundColor Cyan
Write-Host "Profile directory: $PROFILE_DIR"
Write-Host "Merged profile: $MERGED_PROFILE"

# Create profile directory
New-Item -ItemType Directory -Force -Path $PROFILE_DIR | Out-Null

# Clean old profiles
Get-ChildItem -Path $PROFILE_DIR -Filter "*.prof" -ErrorAction SilentlyContinue | Remove-Item -Force

# Collect profile from unit tests
Write-Host "📊 Running unit tests with CPU profiling..." -ForegroundColor Cyan

# Get all packages with tests
$packages = go list -tags $GO_TAGS ./internal/... 2>$null | Where-Object { $_ -notmatch '/vendor/' }

# Run tests for each package individually (required for -cpuprofile)
$profileIndex = 0
foreach ($pkg in $packages) {
    $profileIndex++
    $testProfile = Join-Path $PROFILE_DIR "cpu_test_$profileIndex.prof"
    try {
        go test -tags $GO_TAGS `
            -cpuprofile=$testProfile `
            -run=. `
            -count=1 `
            $pkg 2>$null
    } catch {
        # Continue on error
    }
}

# Collect profiles from performance benchmarks
Write-Host "📊 Running performance benchmarks with CPU profiling..." -ForegroundColor Cyan

$benchPackages = @(
    "./internal/keypool",
    "./internal/services",
    "./internal/utils",
    "./internal/channel",
    "./internal/proxy",
    "./internal/encryption"
)

$benchIndex = 0
foreach ($pkg in $benchPackages) {
    $benchIndex++
    $benchProfile = Join-Path $PROFILE_DIR "cpu_bench_$benchIndex.prof"
    try {
        go test -tags $GO_TAGS `
            -bench=. `
            -benchtime=2s `
            -cpuprofile=$benchProfile `
            -run='^$' `
            $pkg 2>$null
    } catch {
        # Continue on error
    }
}

# Count collected profiles
$profiles = Get-ChildItem -Path $PROFILE_DIR -Filter "*.prof" -ErrorAction SilentlyContinue
$profileCount = $profiles.Count
Write-Host "✅ Collected $profileCount profile(s)" -ForegroundColor Green

if ($profileCount -eq 0) {
    Write-Host "❌ No profiles collected, cannot proceed" -ForegroundColor Red
    exit 1
}

# Merge profiles
Write-Host "🔄 Merging profiles into $MERGED_PROFILE..." -ForegroundColor Cyan

$profilePaths = $profiles | ForEach-Object { $_.FullName }
try {
    # Use go tool pprof to merge profiles
    $profileArgs = @("-proto") + $profilePaths
    & go tool pprof $profileArgs 2>$null | Set-Content -Path $MERGED_PROFILE -Encoding Byte
} catch {
    Write-Host "⚠️  Profile merge failed, using first available profile as fallback" -ForegroundColor Yellow
    $firstProfile = $profiles | Select-Object -First 1
    if ($firstProfile) {
        Copy-Item $firstProfile.FullName $MERGED_PROFILE
    } else {
        Write-Host "❌ Failed to create merged profile" -ForegroundColor Red
        exit 1
    }
}

# Verify the merged profile
if ((Test-Path $MERGED_PROFILE) -and ((Get-Item $MERGED_PROFILE).Length -gt 0)) {
    $profileSize = (Get-Item $MERGED_PROFILE).Length
    $profileSizeKB = [math]::Round($profileSize / 1KB, 2)
    Write-Host "✅ PGO profile ready: $MERGED_PROFILE ($profileSizeKB KB)" -ForegroundColor Green

    # Show profile statistics
    Write-Host "📈 Profile statistics:" -ForegroundColor Cyan
    try {
        go tool pprof -top -nodecount=5 $MERGED_PROFILE 2>$null
    } catch {
        # Ignore errors in statistics display
    }
} else {
    Write-Host "❌ Failed to create valid merged profile" -ForegroundColor Red
    exit 1
}

Write-Host "✅ Profile collection complete!" -ForegroundColor Green
