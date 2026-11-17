# Builds consort.exe (a background app: no console, an icon in the
# notification area, logging to consort.log) and dist/consort-addon.zip.
param([string]$Version = "dev")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o consort.exe ./cmd/consort
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
python -I scripts/build_addon.py
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "Built consort.exe and dist/consort-addon.zip"
