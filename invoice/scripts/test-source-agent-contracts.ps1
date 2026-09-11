param([string]$ProjectRoot = (Split-Path $PSScriptRoot -Parent), [string]$EvidenceRoot)
$ErrorActionPreference = 'Stop'
if (-not $EvidenceRoot) { $EvidenceRoot = Join-Path ([IO.Path]::GetTempPath()) ('source-schema-test-' + [guid]::NewGuid().ToString('N')) }
$fixture = Join-Path $EvidenceRoot 'fixture'
if (Test-Path -LiteralPath $fixture) { throw 'schema fixture must be new' }
$null = New-Item -ItemType Directory -Path (Join-Path $fixture 'contracts/examples') -Force
$examples = @('source-agent-batch.mock.json', 'source-agent-batch.v2.newapi.json', 'source-agent-batch.v3.sub2api-usage.json')
foreach ($version in 1..3) {
    Copy-Item -LiteralPath (Join-Path $ProjectRoot "contracts/source-agent-batch.v$version.schema.json") -Destination (Join-Path $fixture 'contracts')
    Copy-Item -LiteralPath (Join-Path $ProjectRoot ('contracts/examples/' + $examples[$version - 1])) -Destination (Join-Path $fixture 'contracts/examples')
}
$validator = Join-Path $PSScriptRoot 'verify-source-agent-contracts.ps1'
$pwsh = (Get-Process -Id $PID).Path
function Assert-SchemaExit([string]$Name, [bool]$Reject) {
    $output = & $pwsh -NoProfile -File $validator -ProjectRoot $fixture 2>&1
    $code = $LASTEXITCODE
    $output | Set-Content -LiteralPath (Join-Path $EvidenceRoot "$Name.log")
    if (($Reject -and ($code -ne 1 -or ($output -join "`n") -notmatch 'source-agent v[123] example does not match|unsupported external reference')) -or (-not $Reject -and $code -ne 0)) {
        throw "$Name expected rejection=$Reject, got exit=$code"
    }
}
Assert-SchemaExit 'valid-examples' $false
$cases = @(
    @{Name='v1-type'; Version=1; Break={param($s) $s.properties.agent_version.type='integer'}},
    @{Name='v2-required'; Version=2; Break={param($s) $s.required += 'missing_contract_field'}},
    @{Name='v3-event'; Version=3; Break={param($s) $s.'$defs'.usageRecord.allOf[1].properties.entity_type.const='usage_event_BROKEN'}},
    @{Name='v3-reference'; Version=3; Break={param($s) $s.allOf[0].then.properties.records.items.'$ref'='#/$defs/missing'}},
    @{Name='external-reference'; Version=3; Break={param($s) $s.allOf[0].then.properties.records.items.'$ref'='https://schema.invalid/never-fetch'}}
)
foreach ($case in $cases) {
    $path = Join-Path $fixture "contracts/source-agent-batch.v$($case.Version).schema.json"
    $fixed = [IO.File]::ReadAllBytes($path)
    try {
        $schema = [Text.Encoding]::UTF8.GetString($fixed) | ConvertFrom-Json -AsHashtable
        & $case.Break $schema
        [IO.File]::WriteAllText($path, ($schema | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
        Assert-SchemaExit $case.Name $true
    } finally { [IO.File]::WriteAllBytes($path, $fixed) }
}
Assert-SchemaExit 'restored-examples' $false
Write-Host "source-agent schema regression passed; evidence=$EvidenceRoot"
