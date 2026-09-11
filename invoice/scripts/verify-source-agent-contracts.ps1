param([string]$ProjectRoot = (Split-Path $PSScriptRoot -Parent))
$ErrorActionPreference = 'Stop'

# Bundle the three reviewed documents in memory. Their relative references
# become local JSON pointers, so validation never fetches a schema over HTTP.
# Published files, enum values and required/type constraints stay unchanged.
function ConvertTo-LocalSourceSchemaRefs($Node, [int]$Version) {
    if ($Node -is [System.Collections.IDictionary]) {
        $Node.Remove('$id') | Out-Null
        foreach ($key in @($Node.Keys)) {
            if ($key -eq '$ref') {
                $reference = [string]$Node[$key]
                if ($reference.StartsWith('#', [StringComparison]::Ordinal)) {
                    $Node[$key] = '#/$defs/source-v' + $Version + $reference.Substring(1)
                } elseif ($reference -match '^source-agent-batch\.v([123])\.schema\.json(#.*)?$') {
                    $fragment = [string]$Matches[2]
                    $Node[$key] = '#/$defs/source-v' + $Matches[1] + $fragment.TrimStart('#')
                } else {
                    throw "source schema contains unsupported external reference: $reference"
                }
            } else {
                ConvertTo-LocalSourceSchemaRefs $Node[$key] $Version
            }
        }
    } elseif ($Node -is [System.Collections.IList]) {
        foreach ($item in $Node) { ConvertTo-LocalSourceSchemaRefs $item $Version }
    }
}

$definitions = @{}
foreach ($version in 1..3) {
    $schema = Get-Content -LiteralPath (Join-Path $ProjectRoot "contracts/source-agent-batch.v$version.schema.json") -Raw | ConvertFrom-Json -AsHashtable
    ConvertTo-LocalSourceSchemaRefs $schema $version
    $definitions["source-v$version"] = $schema
}
$examples = @('source-agent-batch.mock.json', 'source-agent-batch.v2.newapi.json', 'source-agent-batch.v3.sub2api-usage.json')
foreach ($version in 1..3) {
    $bundle = @{'$schema' = 'https://json-schema.org/draft/2020-12/schema'; '$defs' = $definitions; '$ref' = "#/`$defs/source-v$version"} | ConvertTo-Json -Depth 100 -Compress
    $example = Join-Path $ProjectRoot ('contracts/examples/' + $examples[$version - 1])
    try {
        $valid = Test-Json -LiteralPath $example -Schema $bundle -ErrorAction Stop
    } catch {
        throw "source-agent v$version example does not match its JSON Schema: $($_.Exception.Message)"
    }
    if (-not $valid) { throw "source-agent v$version example does not match its JSON Schema" }
}
Write-Host 'source-agent v1/v2/v3 JSON Schema examples passed (offline references)'
