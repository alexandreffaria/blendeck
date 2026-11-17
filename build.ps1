# Builds synergy.exe (a background app: no console, an icon in the
# notification area, logging to synergy.log) and dist/synergy-addon.zip.
param([string]$Version = "dev")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o synergy.exe ./cmd/synergy
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
python -I scripts/build_addon.py
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "Built synergy.exe and dist/synergy-addon.zip"
