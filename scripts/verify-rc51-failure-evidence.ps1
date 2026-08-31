[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')

$anchorPath = Join-Path $projectRoot 'docs\RC51-FAILURE-EVIDENCE-SHA256SUMS.txt'
if (-not (Test-Path -LiteralPath $anchorPath -PathType Leaf)) {
    throw 'RC51 failure evidence anchor is missing'
}
$anchorText = Get-Content -Raw -LiteralPath $anchorPath
Assert-RC51FailureEvidenceAnchor -ProjectRoot $projectRoot -AnchorText $anchorText | Out-Null

Write-Host 'RC51 failure evidence file set and SHA-256 anchor passed.'
$global:LASTEXITCODE = 0
