# Builds blendeck.exe (a background app: no console, an icon in the
# notification area, logging to blendeck.log) and dist/blendeck-addon.zip.
param([string]$Version = "dev")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o blendeck.exe ./cmd/blendeck
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
python -I scripts/build_addon.py
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "Built blendeck.exe and dist/blendeck-addon.zip"
