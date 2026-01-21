# PowerShell script to collect PGO profiles for Windows builds
# This script runs unit tests to generate CPU profiles for PGO optimization

$ErrorActionPreference = "Continue"  # Changed to Continue to see errors

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

# Clean test cache to ensure tests actually run
Write-Host "Cleaning test cache..." -ForegroundColor Gray
go clean -testcache

# Get all packages with tests
$packages = go list ./internal/...

Write-Host "Found $($packages.Count) packages" -ForegroundColor Gray

# Run tests for each package individually (required for -cpuprofile)
$profileIndex = 0
$packagesWithTests = 0
$packagesWithoutTests = 0

foreach ($pkg in $packages) {
    $profileIndex++
    # Use absolute path for profile file
    $testProfile = Join-Path (Get-Location) (Join-Path $PROFILE_DIR "cpu_test_$profileIndex.prof")

    Write-Host "Testing $pkg..." -ForegroundColor Gray
    Write-Host "  Profile: $testProfile" -ForegroundColor DarkGray

    # Run test with -count=1 to avoid caching
    # Use -tags to match build environment (go_json for high-performance JSON)
    $result = go test -tags $GO_TAGS -cpuprofile="$testProfile" -count=1 $pkg 2>&1

    # Check if profile was created
    if (Test-Path $testProfile) {
        $size = (Get-Item $testProfile).Length
        if ($size -gt 0) {
            Write-Host "  ✓ Profile created: $size bytes" -ForegroundColor Green
            $packagesWithTests++
        } else {
            Write-Host "  ✗ Empty profile, removing" -ForegroundColor Yellow
            Remove-Item $testProfile -Force
            $packagesWithoutTests++
        }
    } else {
        Write-Host "  ✗ No profile created (no tests)" -ForegroundColor DarkGray
        $packagesWithoutTests++
    }
}

Write-Host "`nSummary:" -ForegroundColor Cyan
Write-Host "  Packages with tests: $packagesWithTests" -ForegroundColor Green
Write-Host "  Packages without tests: $packagesWithoutTests" -ForegroundColor Gray

# Count collected profiles
$profiles = Get-ChildItem -Path $PROFILE_DIR -Filter "*.prof" -ErrorAction SilentlyContinue | Where-Object { $_.Length -gt 0 }
$profileCount = $profiles.Count
Write-Host "✅ Collected $profileCount profile(s)" -ForegroundColor Green

if ($profileCount -eq 0) {
    Write-Host "⚠️  No profiles collected from tests" -ForegroundColor Yellow
    Write-Host "Creating minimal profile for build compatibility..." -ForegroundColor Yellow
    "PGO profile placeholder" | Set-Content -Path $MERGED_PROFILE
    Write-Host "✅ Profile collection complete!" -ForegroundColor Green
    exit 0
}

# Merge profiles
Write-Host "🔄 Merging profiles into $MERGED_PROFILE..." -ForegroundColor Cyan

$profilePaths = $profiles | ForEach-Object { $_.FullName }
try {
    # Use go tool pprof to merge profiles - redirect output to file directly
    $profileArgs = @("-proto", "-output", $MERGED_PROFILE) + $profilePaths
    $mergeOutput = & go tool pprof $profileArgs 2>&1

    if (-not (Test-Path $MERGED_PROFILE) -or (Get-Item $MERGED_PROFILE).Length -eq 0) {
        throw "Merge produced empty file. Output: $mergeOutput"
    }
} catch {
    Write-Host "⚠️  Profile merge failed: $_" -ForegroundColor Yellow
    Write-Host "Using first available profile as fallback..." -ForegroundColor Yellow
    $firstProfile = $profiles | Select-Object -First 1
    if ($firstProfile) {
        Copy-Item $firstProfile.FullName $MERGED_PROFILE -Force
    } else {
        Write-Host "Creating minimal profile for build compatibility..." -ForegroundColor Yellow
        "PGO profile placeholder" | Set-Content -Path $MERGED_PROFILE
        Write-Host "✅ Profile collection complete!" -ForegroundColor Green
        exit 0
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
        go tool pprof -top -nodecount=5 $MERGED_PROFILE 2>&1 | Out-Host
    } catch {
        # Ignore errors in statistics display
    }
} else {
    Write-Host "Creating minimal profile for build compatibility..." -ForegroundColor Yellow
    "PGO profile placeholder" | Set-Content -Path $MERGED_PROFILE
}

Write-Host "✅ Profile collection complete!" -ForegroundColor Green

# Clean up temporary files
Write-Host "🧹 Cleaning up temporary files..." -ForegroundColor Cyan
$cleanupCount = 0

# Remove test binaries (both .test and .test.exe)
Get-ChildItem -Path . -File | Where-Object { $_.Name -match '\.test(\.exe)?$' } | ForEach-Object {
    Remove-Item $_.FullName -Force
    $cleanupCount++
}

# Remove individual profile files (keep only merged profile)
if (Test-Path $PROFILE_DIR) {
    $profileFiles = Get-ChildItem -Path $PROFILE_DIR -Filter "*.prof" -ErrorAction SilentlyContinue
    $profileFiles | ForEach-Object {
        Remove-Item $_.FullName -Force
        $cleanupCount++
    }

    # Remove profile directory if empty
    if ((Get-ChildItem -Path $PROFILE_DIR -ErrorAction SilentlyContinue).Count -eq 0) {
        Remove-Item $PROFILE_DIR -Force -ErrorAction SilentlyContinue
    }
}

if ($cleanupCount -gt 0) {
    Write-Host "✅ Cleaned up $cleanupCount temporary file(s)" -ForegroundColor Green
}
