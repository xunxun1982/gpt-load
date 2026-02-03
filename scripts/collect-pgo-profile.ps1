# PowerShell script to collect PGO profiles for Windows builds
# This script runs unit tests to generate CPU profiles for PGO optimization

$ErrorActionPreference = "Continue"  # Continue on errors to collect as many profiles as possible

$PROFILE_DIR = if ($env:PROFILE_DIR) { $env:PROFILE_DIR } else { "profiles" }
$MERGED_PROFILE = if ($env:MERGED_PROFILE) { $env:MERGED_PROFILE } else { "default.pgo" }
$GO_TAGS = if ($env:GO_TAGS) { $env:GO_TAGS } else { "go_json" }

Write-Host "🔍 Collecting PGO profiles..." -ForegroundColor Cyan
Write-Host "Profile directory: $PROFILE_DIR"
Write-Host "Merged profile: $MERGED_PROFILE"

# Create profile directory
New-Item -ItemType Directory -Force -Path $PROFILE_DIR | Out-Null

# Clean old profiles
Get-ChildItem -Path $PROFILE_DIR -Filter "*.prof" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue

# Collect profile from unit tests
Write-Host "📊 Running unit tests with CPU profiling..." -ForegroundColor Cyan

# Clean test cache to ensure tests actually run
Write-Host "Cleaning test cache..." -ForegroundColor Gray
try {
    go clean -testcache 2>&1 | Out-Null
} catch {
    Write-Host "⚠️  Failed to clean test cache, continuing..." -ForegroundColor Yellow
}

# Collect benchmark profiles for hot paths
Write-Host "🔥 Running benchmarks for hot paths..." -ForegroundColor Cyan

# Run benchmarks for each package separately (cpuprofile requires single package)
# Focus on proxy/forwarding hot paths: keypool selection, encryption, buffer pool, JSON processing
$benchPackages = @(
    @{Path="./internal/keypool"; Pattern="^Benchmark(SelectKey|RealisticWorkload)"},
    @{Path="./internal/encryption"; Pattern="^Benchmark(Encrypt|Decrypt|Hash)"},
    @{Path="./internal/utils"; Pattern="^Benchmark(BufferPool|TieredBufferPooling|JSONEncoder|WeightedRandomSelect|ApplyModelMapping|RealisticWorkload)"}
)
$benchIndex = 0

foreach ($pkg in $benchPackages) {
    $benchIndex++
    $benchmarkProfile = Join-Path (Get-Location) (Join-Path $PROFILE_DIR "cpu_bench_$benchIndex.prof")

    Write-Host "  Running benchmarks for $($pkg.Path)..." -ForegroundColor Gray

    try {
        go test `
            -tags $GO_TAGS `
            -bench="$($pkg.Pattern)" `
            -benchtime=2s `
            -cpuprofile="$benchmarkProfile" `
            -run='^$' `
            $pkg.Path 2>&1 | Out-Null
        $benchExitCode = $LASTEXITCODE

        if ($benchExitCode -eq 0 -and (Test-Path $benchmarkProfile)) {
            $size = (Get-Item $benchmarkProfile).Length
            if ($size -gt 0) {
                Write-Host "    ✓ Benchmark profile created: $size bytes" -ForegroundColor Green
            } else {
                Write-Host "    ✗ Empty benchmark profile, removing" -ForegroundColor Yellow
                Remove-Item $benchmarkProfile -Force -ErrorAction SilentlyContinue
            }
        } else {
            Write-Host "    ⚠️  No benchmarks found or profiling failed" -ForegroundColor Yellow
            Remove-Item $benchmarkProfile -Force -ErrorAction SilentlyContinue
        }
    } catch {
        Write-Host "    ⚠️  Benchmark execution failed: $_" -ForegroundColor Yellow
        Remove-Item $benchmarkProfile -Force -ErrorAction SilentlyContinue
    }
}

# Get all packages with tests (only project packages, not dependencies)
Write-Host "Listing packages..." -ForegroundColor Gray

# Get module name from go.mod to filter project packages
# This prevents testing third-party dependencies which pollute PGO profiles
try {
    # Use -f to format output and 2>$null to discard stderr warnings
    $moduleName = (go list -m -f '{{.Path}}' 2>$null | Select-Object -First 1).Trim()
    if ([string]::IsNullOrEmpty($moduleName) -or $LASTEXITCODE -ne 0) {
        $moduleName = "gpt-load"
    }
} catch {
    $moduleName = "gpt-load"
}
Write-Host "Module name: $moduleName" -ForegroundColor Gray

try {
    # Use -e flag to continue on errors, redirect stderr to suppress download messages
    # Filter output to only include project packages (starting with module name)
    $packagesRaw = go list -e ./internal/... 2>$null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "⚠️  go list failed, trying to continue..." -ForegroundColor Yellow
        $packages = @()
    } else {
        # Filter: only keep lines that start with module name (project packages)
        # This excludes third-party dependencies, vendor packages, and download messages
        $packages = $packagesRaw | Where-Object {
            $_ -match "^$([regex]::Escape($moduleName))/" -and $_ -notmatch '/vendor/'
        }
    }
} catch {
    Write-Host "⚠️  go list failed: $_" -ForegroundColor Yellow
    $packages = @()
}

if ($packages.Count -eq 0) {
    Write-Host "⚠️  No packages found, creating minimal profile..." -ForegroundColor Yellow
    "PGO profile placeholder" | Set-Content -Path $MERGED_PROFILE
    exit 0
}

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
    try {
        $result = go test -tags $GO_TAGS -cpuprofile="$testProfile" -count=1 $pkg 2>&1
        $testExitCode = $LASTEXITCODE

        if ($testExitCode -eq 0) {
            Write-Host "  ✓ Tests passed" -ForegroundColor Green
        } else {
            Write-Host "  ⚠️  Tests failed or no tests found (exit code: $testExitCode), but profile may still be generated" -ForegroundColor Yellow
        }
    } catch {
        Write-Host "  ⚠️  Test execution failed: $_" -ForegroundColor Yellow
    }

    # Check if profile was created
    if (Test-Path $testProfile) {
        $size = (Get-Item $testProfile).Length
        if ($size -gt 0) {
            Write-Host "  ✓ Profile created: $size bytes" -ForegroundColor Green
            $packagesWithTests++
        } else {
            Write-Host "  ✗ Empty profile, removing" -ForegroundColor Yellow
            Remove-Item $testProfile -Force -ErrorAction SilentlyContinue
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
$profileCount = if ($profiles) { $profiles.Count } else { 0 }
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
    $mergeExitCode = $LASTEXITCODE

    if ($mergeExitCode -ne 0 -or -not (Test-Path $MERGED_PROFILE) -or (Get-Item $MERGED_PROFILE).Length -eq 0) {
        throw "Merge failed with exit code $mergeExitCode. Output: $mergeOutput"
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
Get-ChildItem -Path . -File -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '\.test(\.exe)?$' } | ForEach-Object {
    Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue
    $cleanupCount++
}

# Remove coverage files
if (Test-Path "coverage") {
    Remove-Item "coverage" -Force -ErrorAction SilentlyContinue
    $cleanupCount++
}
if (Test-Path "coverage.out") {
    Remove-Item "coverage.out" -Force -ErrorAction SilentlyContinue
    $cleanupCount++
}

# Remove individual profile files (keep only merged profile)
if (Test-Path $PROFILE_DIR) {
    $profileFiles = Get-ChildItem -Path $PROFILE_DIR -Filter "*.prof" -ErrorAction SilentlyContinue
    if ($profileFiles) {
        $profileFiles | ForEach-Object {
            Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue
            $cleanupCount++
        }
    }

    # Remove profile directory if empty
    $remainingFiles = Get-ChildItem -Path $PROFILE_DIR -ErrorAction SilentlyContinue
    if (-not $remainingFiles -or $remainingFiles.Count -eq 0) {
        Remove-Item $PROFILE_DIR -Force -ErrorAction SilentlyContinue
    }
}

if ($cleanupCount -gt 0) {
    Write-Host "✅ Cleaned up $cleanupCount temporary file(s)" -ForegroundColor Green
}
