# Run from GoLand terminal in miar5-8:
# powershell -ExecutionPolicy Bypass -File .\practice8\run_tests.ps1
# Keep practice7 Docker containers running.
$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$pr7 = Join-Path $root 'practice7'

if (-not (Test-Path (Join-Path $root 'go.mod'))) {
    throw 'Go module was not found at miar5-8/go.mod'
}

Write-Host '========== UNIT TESTS (practice7) ==========' -ForegroundColor Cyan
Push-Location $pr7
try {
    go test -v -count=1 ./internal/app
    if ($LASTEXITCODE -ne 0) { throw 'Unit tests failed.' }
} finally {
    Pop-Location
}

Write-Host "`n========== INTEGRATION TESTS (practice8) ==========" -ForegroundColor Cyan
Push-Location $root
try {
    go test -v -count=1 ./practice8
    if ($LASTEXITCODE -ne 0) { throw 'Integration tests failed.' }
} finally {
    Pop-Location
}

Write-Host "`nSUCCESS: unit and integration tests passed." -ForegroundColor Green
