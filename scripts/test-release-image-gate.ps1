$ErrorActionPreference = 'Stop'

$gateSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'release-image-gate.ps1')
if ([regex]::Matches($gateSource, "'--timeout', '15m'").Count -lt 4) {
    throw 'release image gate does not apply the reviewed 15-minute Trivy timeout to database updates, scans and SBOM generation'
}
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')

$composeInternalFixtures = @(
    [pscustomobject]@{ Label = 'absent internal defaults false'; Network = [pscustomobject]@{}; Expected = $false },
    [pscustomobject]@{ Label = 'true internal is preserved'; Network = [pscustomobject]@{ internal = $true }; Expected = $true },
    [pscustomobject]@{ Label = 'false internal is preserved'; Network = [pscustomobject]@{ internal = $false }; Expected = $false }
)
foreach ($fixture in $composeInternalFixtures) {
    $actual = Get-ComposeOptionalBoolean -ComposeObject $fixture.Network -PropertyName 'internal'
    if ($actual -ne $fixture.Expected) {
        throw "Compose internal fixture failed: $($fixture.Label)"
    }
}
foreach ($invalidValue in @($null, 'false', [long]0, [pscustomobject]@{})) {
    $rejected = $false
    try {
        Get-ComposeOptionalBoolean -ComposeObject ([pscustomobject]@{ internal = $invalidValue }) -PropertyName 'internal' | Out-Null
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw "Compose internal fixture accepted invalid $($invalidValue.GetType().FullName) value"
    }
}
$verifySource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify.ps1')
if ([regex]::Matches($verifySource, 'Get-ComposeOptionalBoolean\s+-ComposeObject').Count -ne 5 -or
    $verifySource -cmatch '(?m)\$[A-Za-z_][A-Za-z0-9_.]*\.internal\b') {
    throw 'verify.ps1 does not use the optional Compose Boolean helper for every network internal check'
}

$composeOptionalStringFixtures = @(
    [pscustomobject]@{ Label = 'absent string defaults null'; Environment = [pscustomobject]@{}; Expected = $null },
    [pscustomobject]@{ Label = 'string is preserved'; Environment = [pscustomobject]@{ SOURCE_RECONCILE_FILE = '/state/reconcile.json' }; Expected = '/state/reconcile.json' },
    [pscustomobject]@{ Label = 'empty string is preserved for caller validation'; Environment = [pscustomobject]@{ SOURCE_RECONCILE_FILE = '' }; Expected = '' }
)
foreach ($fixture in $composeOptionalStringFixtures) {
    $actual = Get-ComposeOptionalString -ComposeObject $fixture.Environment -PropertyName 'SOURCE_RECONCILE_FILE'
    if (($null -eq $fixture.Expected -and $null -ne $actual) -or
        ($null -ne $fixture.Expected -and $actual -cne $fixture.Expected)) {
        throw "Compose optional string fixture failed: $($fixture.Label)"
    }
}
foreach ($invalidValue in @($null, $true, [long]0, [pscustomobject]@{})) {
    $rejected = $false
    try {
        Get-ComposeOptionalString -ComposeObject ([pscustomobject]@{ SOURCE_RECONCILE_FILE = $invalidValue }) -PropertyName 'SOURCE_RECONCILE_FILE' | Out-Null
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw 'Compose optional string fixture accepted an invalid value'
    }
}
if ([regex]::Matches($verifySource, 'Get-ComposeOptionalString\s+-ComposeObject[^\r\n]+-PropertyName ''SOURCE_RECONCILE_FILE''').Count -ne 2 -or
    $verifySource -cmatch '(?m)\$[A-Za-z_][A-Za-z0-9_.]*\.environment\.SOURCE_RECONCILE_FILE\b') {
    throw 'verify.ps1 does not use the optional Compose string helper for both SOURCE_RECONCILE_FILE checks'
}
$verifyTokens = $null
$verifyParseErrors = $null
$verifyAst = [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'verify.ps1'), [ref]$verifyTokens, [ref]$verifyParseErrors)
$ordinalComparisonFunction = $verifyAst.Find({
    param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq 'Test-OrdinalStringEqual'
}, $true)
if ($null -eq $ordinalComparisonFunction) {
    throw 'verify.ps1 no longer defines the reviewed ordinal string comparison helper'
}
. ([scriptblock]::Create($ordinalComparisonFunction.Extent.Text))
$caseMismatchedReconcileFile = Get-ComposeOptionalString -ComposeObject ([pscustomobject]@{ SOURCE_RECONCILE_FILE = '/STATE/reconcile.json' }) -PropertyName 'SOURCE_RECONCILE_FILE'
if (Test-OrdinalStringEqual -Actual $caseMismatchedReconcileFile -Expected '/state/reconcile.json') {
    throw 'identity SOURCE_RECONCILE_FILE case-mismatch fixture was accepted'
}
$requiredIdentityReconcileComparison = '-not (Test-OrdinalStringEqual -Actual (Get-ComposeOptionalString -ComposeObject $renderedSources.services.$service.environment -PropertyName ''SOURCE_RECONCILE_FILE'') -Expected ''/state/reconcile.json'')'
if (-not $verifySource.Contains($requiredIdentityReconcileComparison, [StringComparison]::Ordinal)) {
    throw 'identity SOURCE_RECONCILE_FILE check does not use ordinal equality'
}

$task5AMutationFailures = [Collections.Generic.List[string]]::new()

function Test-Task5ARequirement {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$Label
    )

    try {
        & $Action
    } catch {
        $task5AMutationFailures.Add("${Label}: $($_.Exception.Message)")
    }
}

function Test-Task5AMutationRejected {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$Label
    )

    $rejected = $false
    try { & $Action } catch { $rejected = $true }
    if (-not $rejected) {
        $task5AMutationFailures.Add("${Label}: mutation was accepted")
    }
}

function Assert-ThrowsLike {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$ExpectedMessagePattern,
        [Parameter(Mandatory)][string]$FailureMessage
    )

    $caught = $null
    try { & $Action } catch { $caught = $_ }
    if ($null -eq $caught -or $caught.Exception.Message -notmatch $ExpectedMessagePattern) {
        $actual = if ($null -eq $caught) { '<no exception>' } else { $caught.Exception.Message }
        throw "$FailureMessage (actual: $actual)"
    }
}

$gitProvenance = Get-ReleaseGitProvenance -RepositoryRoot $projectRoot
if ($gitProvenance.GitHead -notmatch '^[0-9a-f]{40}$' -or
    $gitProvenance.GitDirty -isnot [bool]) {
    throw 'release Git provenance helper did not return an exact commit and Boolean dirty state'
}
$invalidGitIndexDirectory = Join-Path ([IO.Path]::GetTempPath()) ('invoice-invalid-git-index-' + [guid]::NewGuid().ToString('N'))
$previousGitIndexFile = $env:GIT_INDEX_FILE
try {
    $null = New-Item -ItemType Directory -Path $invalidGitIndexDirectory
    $env:GIT_INDEX_FILE = $invalidGitIndexDirectory
    Assert-ThrowsLike `
        -Action { Get-ReleaseGitProvenance -RepositoryRoot $projectRoot | Out-Null } `
        -ExpectedMessagePattern 'git status.*exit' `
        -FailureMessage 'release Git provenance accepted a failed native git status as gitDirty=false'
} finally {
    if ($null -eq $previousGitIndexFile) {
        Remove-Item Env:GIT_INDEX_FILE -ErrorAction SilentlyContinue
    } else {
        $env:GIT_INDEX_FILE = $previousGitIndexFile
    }
    if (Test-Path -LiteralPath $invalidGitIndexDirectory) {
        Remove-Item -LiteralPath $invalidGitIndexDirectory -Recurse -Force
    }
}
if (-not $gateSource.Contains('$gitProvenance = Get-ReleaseGitProvenance -RepositoryRoot $projectRoot', [StringComparison]::Ordinal) -or
    $gateSource -match '(?m)& git (?:-C \$projectRoot )?status --porcelain') {
    throw 'release gate bypasses the native-exit-checked Git provenance helper'
}

$pendingCanaryExitCode = Get-ReleaseGateBlockedExitCode `
    -ApplicationStatus 'passed' `
    -ProductionStatus 'blocked' `
    -Reasons @('idp_self_hosted_pending_canary')
if ($pendingCanaryExitCode -ne 42) {
    throw "expected pending-canary gate exit 42, got $pendingCanaryExitCode"
}
foreach ($blockedExitMutation in @(
    [pscustomobject]@{ Label = 'failed application gate'; Application = 'failed'; Production = 'blocked'; Reasons = @('idp_self_hosted_pending_canary') },
    [pscustomobject]@{ Label = 'approved production'; Application = 'passed'; Production = 'approved'; Reasons = @('idp_self_hosted_pending_canary') },
    [pscustomobject]@{ Label = 'extra reason'; Application = 'passed'; Production = 'blocked'; Reasons = @('idp_self_hosted_pending_canary', 'extra') },
    [pscustomobject]@{ Label = 'scalar reason'; Application = 'passed'; Production = 'blocked'; Reasons = 'idp_self_hosted_pending_canary' }
)) {
    $actualExit = Get-ReleaseGateBlockedExitCode `
        -ApplicationStatus $blockedExitMutation.Application `
        -ProductionStatus $blockedExitMutation.Production `
        -Reasons $blockedExitMutation.Reasons
    if ($actualExit -ne 1) {
        throw "release gate assigned non-failure exit $actualExit to $($blockedExitMutation.Label)"
    }
}
if (-not $gateSource.Contains('Get-ReleaseGateBlockedExitCode', [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('exit 42', [StringComparison]::Ordinal)) {
    throw 'release image gate does not reserve exit 42 for the sole pending-canary state'
}

foreach ($validStrictDirectory in @('release\0.1.0-rc71-exact1', 'release\0.1.0-rc71-exact99')) {
    Assert-StrictReleaseDirectoryName -ReleaseDirectory $validStrictDirectory -ExpectedReleaseName '0.1.0-rc71' | Out-Null
}
foreach ($invalidStrictDirectory in @(
    'release\0.1.0-rc52-exact1',
    'release\0.1.0-RC71-exact1',
    'release\0.1.0-rc71-exact0',
    'release\0.1.0-rc71-exact100',
    'release\rc71-exact1'
)) {
    Assert-ThrowsLike `
        -Action { Assert-StrictReleaseDirectoryName -ReleaseDirectory $invalidStrictDirectory -ExpectedReleaseName '0.1.0-rc71' | Out-Null } `
        -ExpectedMessagePattern 'strict transfer release directory' `
        -FailureMessage "strict transfer accepted invalid release directory $invalidStrictDirectory"
}

$validStrictManifestJson = '{"releaseName":"0.1.0-rc71","images":[{"name":"api","reference":"invoice-system-api:0.1.0-rc71"}]}'
Assert-JsonHasNoDuplicateProperties -JsonText $validStrictManifestJson | Out-Null
foreach ($duplicateJsonFixture in @(
    [pscustomobject]@{ Label = 'same-case top-level releaseName'; Json = '{"releaseName":"0.1.0-rc52","releaseName":"0.1.0-rc71"}' },
    [pscustomobject]@{ Label = 'case-drifted top-level releaseName'; Json = '{"releaseName":"0.1.0-rc52","ReleaseName":"0.1.0-rc71"}' },
    [pscustomobject]@{ Label = 'same-case nested image reference'; Json = '{"images":[{"reference":"invoice-system-api:0.1.0-rc52","reference":"invoice-system-api:0.1.0-rc71"}]}' },
    [pscustomobject]@{ Label = 'case-drifted nested image reference'; Json = '{"images":[{"reference":"invoice-system-api:0.1.0-rc52","Reference":"invoice-system-api:0.1.0-rc71"}]}' }
)) {
    Assert-ThrowsLike `
        -Action { Assert-JsonHasNoDuplicateProperties -JsonText $duplicateJsonFixture.Json | Out-Null } `
        -ExpectedMessagePattern 'duplicate JSON property' `
        -FailureMessage "strict JSON parser accepted $($duplicateJsonFixture.Label)"
}

Assert-ReleasePowerShellVersion -Version ([version]'7.5.0') | Out-Null
Assert-ThrowsLike `
    -Action { Assert-ReleasePowerShellVersion -Version ([version]'7.4.99') | Out-Null } `
    -ExpectedMessagePattern 'PowerShell 7\.5' `
    -FailureMessage 'release tooling accepted PowerShell below the DateKind String boundary'
$isoDateJson = '{"reviewedAt":"2026-08-31T00:00:00Z","reviewDueAt":"2026-09-30T00:00:00Z"}'
$isoDateRecord = ConvertFrom-ReleaseJson -JsonText $isoDateJson
if ($isoDateRecord.reviewedAt -isnot [string] -or
    $isoDateRecord.reviewDueAt -isnot [string] -or
    [string]$isoDateRecord.reviewedAt -cne '2026-08-31T00:00:00Z' -or
    [string]$isoDateRecord.reviewDueAt -cne '2026-09-30T00:00:00Z') {
    throw 'release JSON parser did not preserve reviewedAt/reviewDueAt as exact ISO strings'
}
Assert-ThrowsLike `
    -Action { ConvertFrom-ReleaseJson -JsonText '{"reviewedAt":"old","reviewedAt":"2026-08-31T00:00:00Z"}' | Out-Null } `
    -ExpectedMessagePattern 'duplicate JSON property' `
    -FailureMessage 'release JSON parser bypassed the raw duplicate-property guard'

$strictVulnerabilityPolicy = [pscustomobject]@{
    trivySeverities = @('HIGH', 'CRITICAL')
    ignoreUnfixed = $false
    ignoredVulnerabilities = @()
    trivyExecution = 'serial'
    staleImageArtifacts = 'fail'
    postgresException = $null
}
Assert-ExactReleaseVulnerabilityPolicy -Policy $strictVulnerabilityPolicy | Out-Null
$strictPolicyManifest = [pscustomobject]@{ policy = $strictVulnerabilityPolicy }
Assert-ExactReleaseManifestPolicy -Manifest $strictPolicyManifest | Out-Null
$caseDriftedPolicyManifest = $strictPolicyManifest | ConvertTo-Json -Depth 10 | ConvertFrom-Json
$caseDriftedPolicy = $caseDriftedPolicyManifest.policy
$caseDriftedPolicyManifest.PSObject.Properties.Remove('policy')
$caseDriftedPolicyManifest | Add-Member -NotePropertyName 'Policy' -NotePropertyValue $caseDriftedPolicy
Assert-ThrowsLike `
    -Action { Assert-ExactReleaseManifestPolicy -Manifest $caseDriftedPolicyManifest | Out-Null } `
    -ExpectedMessagePattern 'exact property policy' `
    -FailureMessage 'strict vulnerability policy accepted case-drifted top-level Policy'
foreach ($policyMutation in @(
    [pscustomobject]@{ Label = 'scalar trivySeverities'; Expected = 'trivySeverities'; Mutate = { param($policy) $policy.trivySeverities = 'HIGH,CRITICAL' } },
    [pscustomobject]@{ Label = 'numeric ignoreUnfixed'; Expected = 'ignoreUnfixed=false.*Boolean'; Mutate = { param($policy) $policy.ignoreUnfixed = [long]0 } },
    [pscustomobject]@{ Label = 'string ignoreUnfixed'; Expected = 'ignoreUnfixed=false.*Boolean'; Mutate = { param($policy) $policy.ignoreUnfixed = 'false' } },
    [pscustomobject]@{ Label = 'true ignoreUnfixed'; Expected = 'ignoreUnfixed=false.*Boolean'; Mutate = { param($policy) $policy.ignoreUnfixed = $true } }
)) {
    $mutatedPolicy = $strictVulnerabilityPolicy | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    & $policyMutation.Mutate $mutatedPolicy
    Assert-ThrowsLike `
        -Action { Assert-ExactReleaseVulnerabilityPolicy -Policy $mutatedPolicy | Out-Null } `
        -ExpectedMessagePattern $policyMutation.Expected `
        -FailureMessage "strict vulnerability policy accepted $($policyMutation.Label)"
}

function Get-DockerfileStageBody {
    param(
        [Parameter(Mandatory)][string]$DockerfileText,
        [Parameter(Mandatory)][string]$StageName
    )

    $stagePattern = [regex]::Escape($StageName)
    $match = [regex]::Match($DockerfileText, "(?ms)^FROM\b[^\r\n]*\s+AS\s+$stagePattern\s*\r?\n(?<body>.*?)(?=^FROM\b|\z)")
    if (-not $match.Success) {
        throw "Dockerfile is missing named stage $StageName"
    }
    return $match.Groups['body'].Value
}

function Assert-DockerfileGlobalArgBeforeFirstFrom {
    param(
        [Parameter(Mandatory)][string]$DockerfileText,
        [Parameter(Mandatory)][string]$ArgumentName,
        [Parameter(Mandatory)][string]$ExpectedValue
    )

    $normalized = ConvertTo-NormalizedLfText -Text $DockerfileText
    $firstFrom = [regex]::Match($normalized, '(?im)^[ \t]*FROM(?:[ \t]|$)')
    if (-not $firstFrom.Success) {
        throw 'Dockerfile does not contain a FROM instruction'
    }

    $argumentPattern = '(?im)^[ \t]*ARG[ \t]+' + [regex]::Escape($ArgumentName) + '=(?<value>[^\n]*?)[ \t]*$'
    $argumentMatches = [regex]::Matches($normalized, $argumentPattern)
    if ($argumentMatches.Count -ne 1 -or
        $argumentMatches[0].Index -ge $firstFrom.Index -or
        $argumentMatches[0].Groups['value'].Value -cne $ExpectedValue) {
        throw "Dockerfile ARG $ArgumentName must be declared exactly once with the reviewed value before the first FROM"
    }
}

function Assert-BackendRuntimeOpenSslPins {
    param(
        [Parameter(Mandatory)][string]$DockerfileText,
        [Parameter(Mandatory)][string]$FixedPackages
    )

    foreach ($stageName in @('api-base', 'scanner-base')) {
        if (-not (Get-DockerfileStageBody -DockerfileText $DockerfileText -StageName $stageName).Contains($FixedPackages)) {
            throw "backend Dockerfile stage $stageName does not pin both fixed OpenSSL packages"
        }
    }
    foreach ($inheritedStage in @(
        'FROM api-base AS tools',
        'FROM api-base AS api',
        'FROM scanner-base AS pdf-policy-gate',
        'FROM scanner-base AS scanner'
    )) {
        if (-not $DockerfileText.Contains($inheritedStage)) {
            throw "backend Dockerfile no longer preserves the intended inherited runtime stage: $inheritedStage"
        }
    }
}

function ConvertTo-NormalizedLfText {
    param([Parameter(Mandatory)][string]$Text)

    return $Text.Replace("`r`n", "`n").Replace("`r", "`n")
}

function Assert-ExactNormalizedText {
    param(
        [Parameter(Mandatory)][string]$Actual,
        [Parameter(Mandatory)][string]$Expected,
        [Parameter(Mandatory)][string]$Label
    )

    if ((ConvertTo-NormalizedLfText -Text $Actual) -cne (ConvertTo-NormalizedLfText -Text $Expected)) {
        throw "$Label must match the exact reviewed contents without extra lines, requirements, sums, or replace directives"
    }
}

function Get-ComposeServiceBlock {
    param(
        [Parameter(Mandatory)][string]$ComposeText,
        [Parameter(Mandatory)][string]$Service
    )

    $servicePattern = [regex]::Escape($Service)
    $match = [regex]::Match($ComposeText, "(?ms)^  ${servicePattern}:\r?\n.*?(?=^  [A-Za-z0-9_-]+:\s*$|^\S|\z)")
    if (-not $match.Success) {
        throw "Compose service $Service is missing"
    }
    return $match.Value
}

function Assert-ComposeServiceImage {
    param(
        [Parameter(Mandatory)][string]$ComposeText,
        [Parameter(Mandatory)][string]$Service,
        [Parameter(Mandatory)][string]$ExpectedImage
    )

    $serviceBlock = Get-ComposeServiceBlock -ComposeText $ComposeText -Service $Service
    $imageMatches = [regex]::Matches($serviceBlock, '(?m)^    image:\s*(?<image>[^\r\n]+)\s*$')
    $pullPolicyMatches = [regex]::Matches($serviceBlock, '(?m)^    pull_policy:\s*(?<policy>[^\s#]+)\s*$')
    if ($imageMatches.Count -ne 1 -or $imageMatches[0].Groups['image'].Value -cne $ExpectedImage -or
        $pullPolicyMatches.Count -ne 1 -or $pullPolicyMatches[0].Groups['policy'].Value -cne 'never') {
        throw "Compose service $Service must use exactly $ExpectedImage with pull_policy: never"
    }
}

$idpCompose = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.idp.yml')
$artifactVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1')
$sourceVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify.ps1')
$readinessIndexGate = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-source-readiness-index-operator.ps1')
$keycloakProvisioningVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
if (-not $idpCompose.Contains('    image: invoice-keycloak:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}') -or
    $idpCompose.Contains('    image: invoice-keycloak:26.7.2') -or
    -not $artifactVerifier.Contains('Get-CommonReleaseImageTag -ImageRecords $records -IdPMode $idpMode') -or
    -not $artifactVerifier.Contains('production Keycloak Compose does not use the manifest-bound release image tag') -or
    -not $sourceVerifier.Contains("Test-OrdinalStringEqual -Actual `$idpBaseObject.services.keycloak.image -Expected 'invoice-keycloak:verification-build'") -or
    -not $sourceVerifier.Contains("Test-OrdinalStringEqual -Actual `$idpBaseObject.services.keycloak.pull_policy -Expected 'never'")) {
    throw 'Keycloak production Compose can escape the common manifest-bound release image tag'
}
if (-not $sourceVerifier.Contains("& (Join-Path `$PSScriptRoot 'verify-source-readiness-index-operator.ps1')", [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('readinessIndexOperatorGateSha256 = Get-FileSha256Lower', [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('readinessIndexOperatorSha256 = Get-FileSha256Lower', [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('readinessIndexVerifierSha256 = Get-FileSha256Lower', [StringComparison]::Ordinal) -or
    -not $artifactVerifier.Contains('manifest.tools.readinessIndexOperatorGateSha256', [StringComparison]::Ordinal) -or
    -not $artifactVerifier.Contains('manifest.tools.readinessIndexOperatorSha256', [StringComparison]::Ordinal) -or
    -not $artifactVerifier.Contains('manifest.tools.readinessIndexVerifierSha256', [StringComparison]::Ordinal) -or
    -not $readinessIndexGate.Contains('readiness query execution exceeded the 2000ms hard limit', [StringComparison]::Ordinal)) {
    throw 'RC39 readiness-index operator/verifier escaped the source or release artifact gates'
}
if ($keycloakProvisioningVerifier -notmatch 'tr -d ''\\r\\n''' -or
    $keycloakProvisioningVerifier -notmatch 'test "\$\(wc -l <"\$tmp"\)" -eq 1' -or
    $keycloakProvisioningVerifier -notmatch '/run/test-secrets/admin_auth_header' -or
    $keycloakProvisioningVerifier -notmatch 'curl --header @/run/test-secrets/admin_auth_header' -or
    $keycloakProvisioningVerifier -notmatch 'http://keycloak:8080/admin/realms/' -or
    $keycloakProvisioningVerifier -notmatch 'http://keycloak:8080/realms/master/\.well-known/openid-configuration' -or
    $keycloakProvisioningVerifier -notmatch 'rm -f "\$password_request"' -or
    $keycloakProvisioningVerifier -notmatch '\^\[A-Za-z0-9_-\]\+\\\.\[A-Za-z0-9_-\]\+\\\.\[A-Za-z0-9_-\]\+\$' -or
    $keycloakProvisioningVerifier -notmatch 'test "\$\{#token\}" -ge 64' -or
    $keycloakProvisioningVerifier -notmatch 'test "\$\{#token\}" -le 262144' -or
    $keycloakProvisioningVerifier -notmatch '\$Path\.Contains\(''\.\.''' -or
    $keycloakProvisioningVerifier -notmatch '\(\?i\)%2f\|%5c' -or
    $keycloakProvisioningVerifier -notmatch 'Admin GET JSON root is not an object or array' -or
    $keycloakProvisioningVerifier -match 'http://127\.0\.0\.1:\$Port/admin/realms/' -or
    $keycloakProvisioningVerifier -match '--publish') {
    throw 'Keycloak provisioning verification can expose credentials or depend on Docker host-port NAT for Admin REST'
}

$validFixtureJWT = ('a' * 32) + '.' + ('b' * 32) + '.' + ('c' * 32)
$multilineFixtureJWT = $validFixtureJWT + "`n" + 'header = "X-Probe: injected"'
$fixtureJWTPattern = '^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$'
if ($validFixtureJWT -cnotmatch $fixtureJWTPattern -or
    @($validFixtureJWT -split "`n").Count -ne 1 -or
    @($multilineFixtureJWT -split "`n").Count -eq 1) {
    throw 'Keycloak fixture JWT single-line validation contract is incomplete'
}

if (-not (Test-OrdinalStringEqual -Actual 'invoice-keycloak:verification-build' -Expected 'invoice-keycloak:verification-build') -or
    (Test-OrdinalStringEqual -Actual 'invoice-keycloak:Verification-build' -Expected 'invoice-keycloak:verification-build') -or
    (Test-OrdinalStringEqual -Actual 'Never' -Expected 'never')) {
    throw 'production image or pull-policy comparison is not ordinal and case-sensitive'
}

$matchingTagRecords = @(
    [pscustomobject]@{ name = 'api'; reference = 'invoice-system-api:fixture' }
    [pscustomobject]@{ name = 'pdf-scanner'; reference = 'invoice-system-pdf-scanner:fixture' }
    [pscustomobject]@{ name = 'tools'; reference = 'invoice-system-tools:fixture' }
    [pscustomobject]@{ name = 'web'; reference = 'invoice-system-web:fixture' }
    [pscustomobject]@{ name = 'source-agent'; reference = 'invoice-source-agent:fixture' }
    [pscustomobject]@{ name = 'postgres-runtime'; reference = 'invoice-postgres:fixture' }
    [pscustomobject]@{ name = 'clamav-runtime'; reference = 'invoice-clamav:fixture' }
    [pscustomobject]@{ name = 'ingest-proxy'; reference = 'invoice-ingest-proxy:fixture' }
)
if ((Get-CommonReleaseImageTag -ImageRecords $matchingTagRecords -IdPMode external-managed) -cne 'fixture' -or
    (Get-CommonReleaseImageTag -ImageRecords $matchingTagRecords -IdPMode none) -cne 'fixture') {
    throw 'non-Keycloak IdP modes no longer accept the eight common locally built release images'
}
$matchingTagRecords[-1].reference = 'invoice-ingest-proxy:different'
$rejected = $false
try { Get-CommonReleaseImageTag -ImageRecords $matchingTagRecords -IdPMode none | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'mismatched derived ingest-proxy release image tag was accepted' }
$matchingTagRecords[-1].reference = 'invoice-ingest-proxy:fixture'
$keycloakRecords = @($matchingTagRecords) + [pscustomobject]@{ name = 'keycloak'; reference = 'invoice-keycloak:fixture' }
if ((Get-CommonReleaseImageTag -ImageRecords $keycloakRecords -IdPMode keycloak) -cne 'fixture') {
    throw 'matching Keycloak release image tag was rejected'
}
$keycloakRecords[-1].reference = 'invoice-keycloak:different'
$rejected = $false
try { Get-CommonReleaseImageTag -ImageRecords $keycloakRecords -IdPMode keycloak | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'mismatched Keycloak release image tag was accepted' }
$keycloakRecords[-1].reference = 'invoice-keycloak:Fixture'
$rejected = $false
try { Get-CommonReleaseImageTag -ImageRecords $keycloakRecords -IdPMode keycloak | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'case-mismatched Keycloak release image tag was accepted' }

$productionRunbookLines = Get-Content -LiteralPath (Join-Path $projectRoot 'docs\PRODUCTION-RUNBOOK.md')
foreach ($line in $productionRunbookLines) {
    if ($line -match '\bup -d\b' -and $line -notmatch '--no-build') {
        throw "production Compose command can rebuild outside the release gate: $line"
    }
    if ($line -match '\brun --rm\b' -and $line -notmatch '^docker run\b' -and $line -notmatch '--pull never') {
        throw "production Compose one-shot command can pull outside the release gate: $line"
    }
}
$productionComposeText = @(
    Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.prod.yml')
    Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.sources.yml')
    Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.idp.yml')
) -join "`n"
if ($productionComposeText -match '(?m)^\s+build:\s*$') {
    throw 'production Compose retains a local build directive outside the release gate'
}

$backendDockerfile = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'backend\Dockerfile')
$webDockerfile = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'web\Dockerfile')
$keycloakDockerfile = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\keycloak\Dockerfile')
$postgresDockerfilePath = Join-Path $projectRoot 'deploy\postgres\Dockerfile'
$postgresGoModPath = Join-Path $projectRoot 'deploy\postgres\gosu-build\go.mod'
$postgresGoSumPath = Join-Path $projectRoot 'deploy\postgres\gosu-build\go.sum'
$clamavDockerfilePath = Join-Path $projectRoot 'deploy\clamav\Dockerfile'
$ingestDockerfilePath = Join-Path $projectRoot 'deploy\ingest-proxy\Dockerfile'
if (-not (Test-Path -LiteralPath $postgresDockerfilePath) -or
    -not (Test-Path -LiteralPath $postgresGoModPath) -or
    -not (Test-Path -LiteralPath $postgresGoSumPath) -or
    -not (Test-Path -LiteralPath $clamavDockerfilePath) -or
    -not (Test-Path -LiteralPath $ingestDockerfilePath)) {
    throw 'RC71 derived PostgreSQL, ClamAV, and ingest runtime image sources are missing'
}

$postgresDockerfile = Get-Content -Raw -LiteralPath $postgresDockerfilePath
$postgresGoMod = Get-Content -Raw -LiteralPath $postgresGoModPath
$postgresGoSum = Get-Content -Raw -LiteralPath $postgresGoSumPath
$clamavDockerfile = Get-Content -Raw -LiteralPath $clamavDockerfilePath
$ingestDockerfile = Get-Content -Raw -LiteralPath $ingestDockerfilePath
$fixedAlpinePackages = 'libcrypto3=3.5.8-r0 libssl3=3.5.8-r0'
$goBuilderReference = 'golang:1.25.13-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946'
$postgresBaseReference = 'postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$clamavBaseReference = 'clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8'
$nginxBaseReference = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
$keycloakBaseReference = 'quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
$gosuModuleVersion = 'v0.0.0-20250923190938-6456aaa0f3c8'
$gosuExpectedVersion = '1.19 (go1.25.13 on linux/amd64; gc)'

function Assert-Task2MutationRejected {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$Message
    )

    $rejected = $false
    try { & $Action } catch { $rejected = $true }
    if (-not $rejected) { throw $Message }
}

foreach ($requiredTask2Helper in @(
    'Assert-BackendRuntimeOpenSslPins',
    'Assert-DockerfileGlobalArgBeforeFirstFrom',
    'Assert-ExactNormalizedText',
    'Assert-ComposeServiceImage'
)) {
    if (-not (Get-Command -Name $requiredTask2Helper -ErrorAction SilentlyContinue)) {
        throw "RC71 focused static checker is missing: $requiredTask2Helper"
    }
}

$apiBaseStart = $backendDockerfile.IndexOf('FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS api-base', [StringComparison]::Ordinal)
$scannerBaseStart = $backendDockerfile.IndexOf('FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS scanner-base', [StringComparison]::Ordinal)
if ($apiBaseStart -lt 0 -or $scannerBaseStart -le $apiBaseStart) {
    throw 'RC71 backend fixture does not contain the named runtime stages'
}
$backendWithoutApiBaseOpenSslPins = $backendDockerfile.Substring(0, $apiBaseStart) +
    $backendDockerfile.Substring($apiBaseStart, $scannerBaseStart - $apiBaseStart).Replace($fixedAlpinePackages, '') +
    $backendDockerfile.Substring($scannerBaseStart)
Assert-Task2MutationRejected -Action {
    Assert-BackendRuntimeOpenSslPins -DockerfileText $backendWithoutApiBaseOpenSslPins -FixedPackages $fixedAlpinePackages
} -Message 'backend api-base OpenSSL pin removal was accepted because scanner-base still contained the pins'

$postgresGlobalArgFixture = "ARG POSTGRES_BASE_IMAGE=$postgresBaseReference`nFROM scratch AS gosu-build`nFROM " + '${POSTGRES_BASE_IMAGE}' + "`n"
Assert-DockerfileGlobalArgBeforeFirstFrom `
    -DockerfileText $postgresGlobalArgFixture `
    -ArgumentName 'POSTGRES_BASE_IMAGE' `
    -ExpectedValue $postgresBaseReference
$postgresStageScopedArgFixture = "FROM scratch AS gosu-build`nARG POSTGRES_BASE_IMAGE=$postgresBaseReference`nFROM " + '${POSTGRES_BASE_IMAGE}' + "`n"
Assert-Task2MutationRejected -Action {
    Assert-DockerfileGlobalArgBeforeFirstFrom `
        -DockerfileText $postgresStageScopedArgFixture `
        -ArgumentName 'POSTGRES_BASE_IMAGE' `
        -ExpectedValue $postgresBaseReference
} -Message 'PostgreSQL base image ARG declared after the first FROM was accepted as globally scoped'
$postgresLowercaseFromStageArgFixture = "from scratch AS gosu-build`nARG POSTGRES_BASE_IMAGE=$postgresBaseReference`nFROM " + '${POSTGRES_BASE_IMAGE}' + "`n"
Assert-Task2MutationRejected -Action {
    Assert-DockerfileGlobalArgBeforeFirstFrom `
        -DockerfileText $postgresLowercaseFromStageArgFixture `
        -ArgumentName 'POSTGRES_BASE_IMAGE' `
        -ExpectedValue $postgresBaseReference
} -Message 'PostgreSQL base image ARG after a lowercase first FROM was accepted as globally scoped'
Assert-DockerfileGlobalArgBeforeFirstFrom `
    -DockerfileText $postgresDockerfile `
    -ArgumentName 'POSTGRES_BASE_IMAGE' `
    -ExpectedValue $postgresBaseReference

$expectedGosuGoMod = @'
module invoice.local/gosu-build

go 1.25.0

require github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8

require (
	github.com/moby/sys/user v0.1.0 // indirect
	golang.org/x/sys v0.1.0 // indirect
)
'@ + "`n"
$expectedGosuGoSum = @'
github.com/moby/sys/user v0.1.0 h1:WmZ93f5Ux6het5iituh9x2zAG7NFY9Aqi49jjE1PaQg=
github.com/moby/sys/user v0.1.0/go.mod h1:fKJhFOnsCN6xZ5gSfbM6zaHGgDJMrqt9/reuj4T7MmU=
github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8 h1:HIpXk5mGBQGfOqcaBbRT4Vnss8NPICnMGlD5xTlPBdQ=
github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8/go.mod h1:SwhRwWsO6iqXZN9CpIaU9CnOrUqpWDINW16KaaSqnrU=
golang.org/x/sys v0.1.0 h1:kunALQeHf1/185U1i0GOB/fy1IPRDDpuoOOqRReG57U=
golang.org/x/sys v0.1.0/go.mod h1:oPkhp1MJrh7nUepCBck5+mAzfO9JrbApNNgaTdGDITg=
'@ + "`n"
Assert-Task2MutationRejected -Action {
    Assert-ExactNormalizedText -Actual ($postgresGoMod + "`nreplace github.com/tianon/gosu => ./unexpected") -Expected $expectedGosuGoMod -Label 'gosu go.mod'
} -Message 'gosu go.mod accepted an extra replace directive'
Assert-Task2MutationRejected -Action {
    Assert-ExactNormalizedText -Actual ($postgresGoSum + "`nexample.invalid/extra v0.0.0 h1:unexpected=") -Expected $expectedGosuGoSum -Label 'gosu go.sum'
} -Message 'gosu go.sum accepted an extra checksum'

$productionCompose = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.prod.yml')
$localPostgresImage = 'invoice-postgres:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}'
$localClamavImage = 'invoice-clamav:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}'
$localIngestImage = 'invoice-ingest-proxy:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}'
$swappedProductionCompose = $productionCompose -replace '(?m)(^  postgres:\r?\n    image: )invoice-postgres:\$\{INVOICE_IMAGE_TAG:\?set the exact reviewed invoice release tag\}', ('${1}' + $localClamavImage)
$swappedProductionCompose = $swappedProductionCompose -replace '(?m)(^  clamav:\r?\n    image: )invoice-clamav:\$\{INVOICE_IMAGE_TAG:\?set the exact reviewed invoice release tag\}', ('${1}' + $localPostgresImage)
Assert-Task2MutationRejected -Action {
    Assert-ComposeServiceImage -ComposeText $swappedProductionCompose -Service 'postgres' -ExpectedImage $localPostgresImage
} -Message 'Compose image-count checker accepted a postgres/clamav service-image swap'

Assert-BackendRuntimeOpenSslPins -DockerfileText $backendDockerfile -FixedPackages $fixedAlpinePackages
if (-not $webDockerfile.Contains($fixedAlpinePackages) -or
    -not $postgresDockerfile.Contains($fixedAlpinePackages) -or
    -not $clamavDockerfile.Contains($fixedAlpinePackages) -or
    -not $ingestDockerfile.Contains($fixedAlpinePackages)) {
    throw 'RC71 runtime Alpine stages do not pin both fixed OpenSSL packages'
}
if (-not $postgresDockerfile.Contains($goBuilderReference) -or
    -not $postgresDockerfile.Contains($postgresBaseReference) -or
    -not $postgresDockerfile.Contains('CGO_ENABLED=0') -or
    -not $postgresDockerfile.Contains('-mod=readonly') -or
    -not $postgresDockerfile.Contains('-trimpath') -or
    -not $postgresDockerfile.Contains('-buildvcs=false') -or
    -not $postgresDockerfile.Contains($gosuExpectedVersion) -or
    -not $postgresDockerfile.Contains('test "$TARGETOS" = "linux"') -or
    -not $postgresDockerfile.Contains('test "$TARGETARCH" = "amd64"') -or
    $postgresDockerfile.Contains('go mod download -mod=readonly')) {
    throw 'RC71 PostgreSQL gosu rebuild is not pinned, platform-gated, and read-only at build time'
}
Assert-ExactNormalizedText -Actual $postgresGoMod -Expected $expectedGosuGoMod -Label 'RC71 PostgreSQL gosu go.mod'
Assert-ExactNormalizedText -Actual $postgresGoSum -Expected $expectedGosuGoSum -Label 'RC71 PostgreSQL gosu go.sum'
if (-not $clamavDockerfile.Contains($clamavBaseReference) -or
    -not $clamavDockerfile.Contains('ClamAV 1.4.5') -or
    -not $ingestDockerfile.Contains($nginxBaseReference) -or
    -not $ingestDockerfile.Contains('nginx/1.30.4')) {
    throw 'RC71 PostgreSQL, ClamAV, or ingest runtime is not pinned to the reviewed base'
}
Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $keycloakDockerfile -ExpectedBaseReference $keycloakBaseReference | Out-Null

$keycloakLiteralPinFixture = "FROM $keycloakBaseReference AS builder`nFROM $keycloakBaseReference`n"
Test-Task5ARequirement -Label 'literal Keycloak FROM pins with permitted leading whitespace' -Action {
    $leadingWhitespaceFixture = "  FROM $keycloakBaseReference AS builder`n`tFROM $keycloakBaseReference`n"
    Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $leadingWhitespaceFixture -ExpectedBaseReference $keycloakBaseReference | Out-Null
}
foreach ($unicodeWhitespaceFixture in @(
    [pscustomobject]@{ Label = 'vertical tab'; Prefix = [string][char]0x000B },
    [pscustomobject]@{ Label = 'form feed'; Prefix = [string][char]0x000C },
    [pscustomobject]@{ Label = 'non-breaking space'; Prefix = [string][char]0x00A0 }
)) {
    Test-Task5ARequirement -Label "literal Keycloak FROM pins with $($unicodeWhitespaceFixture.Label) leading whitespace" -Action {
        $unicodeLeadingWhitespaceFixture =
            $unicodeWhitespaceFixture.Prefix + "FROM $keycloakBaseReference AS builder`n" +
            $unicodeWhitespaceFixture.Prefix + "FROM $keycloakBaseReference`n"
        Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $unicodeLeadingWhitespaceFixture -ExpectedBaseReference $keycloakBaseReference | Out-Null
    }
}
foreach ($dockerfileMutation in @(
    [pscustomobject]@{
        Label = 'indented mixed-case third FROM'
        Text = $keycloakLiteralPinFixture + "  fRoM scratch AS bypass`n"
    },
    [pscustomobject]@{
        Label = 'indented lowercase Keycloak base ARG'
        Text = "  arg KEYCLOAK_BASE_IMAGE=$keycloakBaseReference`n" + $keycloakLiteralPinFixture
    },
    [pscustomobject]@{
        Label = 'indented variable FROM'
        Text = $keycloakLiteralPinFixture + '  FROM ${KEYCLOAK_BASE_IMAGE} AS bypass' + "`n"
    },
    [pscustomobject]@{
        Label = 'vertical-tab-prefixed mixed-case third FROM'
        Text = $keycloakLiteralPinFixture + [string][char]0x000B + "fRoM scratch AS bypass`n"
    },
    [pscustomobject]@{
        Label = 'form-feed-prefixed mixed-case third FROM'
        Text = $keycloakLiteralPinFixture + [string][char]0x000C + "fRoM scratch AS bypass`n"
    },
    [pscustomobject]@{
        Label = 'NBSP-prefixed mixed-case third FROM'
        Text = $keycloakLiteralPinFixture + [string][char]0x00A0 + "fRoM scratch AS bypass`n"
    },
    [pscustomobject]@{
        Label = 'vertical-tab-separated mixed-case third FROM'
        Text = $keycloakLiteralPinFixture + 'fRoM' + [string][char]0x000B + "scratch AS bypass`n"
    },
    [pscustomobject]@{
        Label = 'form-feed-separated lowercase Keycloak base ARG'
        Text = 'arg' + [string][char]0x000C + "KEYCLOAK_BASE_IMAGE=$keycloakBaseReference`n" + $keycloakLiteralPinFixture
    }
)) {
    Test-Task5AMutationRejected -Label $dockerfileMutation.Label -Action {
        Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $dockerfileMutation.Text -ExpectedBaseReference $keycloakBaseReference | Out-Null
    }
}

$verifySource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify.ps1')
if (-not $verifySource.Contains('Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $keycloakDockerfile -ExpectedBaseReference $expectedKeycloakBase', [StringComparison]::Ordinal) -or
    $verifySource -match 'KEYCLOAK_BASE_IMAGE') {
    throw 'verify.ps1 does not enforce the literal-only two-stage Keycloak base pin contract'
}

$productionCompose = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.prod.yml')
if ($productionComposeText -match '(?m)^\s+image:\s+postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2\s*$' -or
    $productionComposeText -match '(?m)^\s+image:\s+clamav/clamav:1\.4\.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8\s*$' -or
    $productionComposeText -match '(?m)^\s+image:\s+nginx:1\.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46\s*$') {
    throw 'RC71 production Compose retains external PostgreSQL, ClamAV, or ingest runtime image references'
}
Assert-ComposeServiceImage -ComposeText $productionCompose -Service 'postgres' -ExpectedImage $localPostgresImage
Assert-ComposeServiceImage -ComposeText $productionCompose -Service 'permissions' -ExpectedImage $localPostgresImage
Assert-ComposeServiceImage -ComposeText $idpCompose -Service 'keycloak-postgres' -ExpectedImage $localPostgresImage
Assert-ComposeServiceImage -ComposeText $productionCompose -Service 'clamav' -ExpectedImage $localClamavImage
Assert-ComposeServiceImage -ComposeText $productionCompose -Service 'ingest-proxy' -ExpectedImage $localIngestImage

$fixtureRoot = Join-Path $PSScriptRoot 'fixtures\release-image-gate'
$imageID = 'sha256:' + ('a' * 64)
$trivyTimestamp = ConvertFrom-TrivyDatabaseTimestamp -Timestamp '2026-08-20 19:46:52.822388238 +0000 UTC'
if ($trivyTimestamp.ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ss.fffffffZ') -cne '2026-08-20T19:46:52.8223882Z') {
    throw 'Trivy Go-style nanosecond database timestamp was not parsed deterministically'
}
$goodReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-vulnerability-report.json') | ConvertFrom-Json
$goodBom = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-sbom.json') | ConvertFrom-Json

$summary = Assert-TrivyReportBinding -Report $goodReport -ExpectedImageId $imageID
if ($summary.Total -ne 0 -or $summary.High -ne 0 -or $summary.Critical -ne 0) {
    throw 'zero-finding fixture was not interpreted as clean'
}
Assert-CycloneDxBinding -Bom $goodBom -ExpectedImageId $imageID | Out-Null

$badReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-vulnerability-report.json') | ConvertFrom-Json
$badReport.Metadata.ImageID = 'sha256:' + ('b' * 64)
$rejected = $false
try { Assert-TrivyReportBinding -Report $badReport -ExpectedImageId $imageID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'stale Trivy ImageID fixture was accepted' }

$badBom = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-sbom.json') | ConvertFrom-Json
$badBom.metadata.component.properties[0].value = 'sha256:' + ('b' * 64)
$rejected = $false
try { Assert-CycloneDxBinding -Bom $badBom -ExpectedImageId $imageID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'stale CycloneDX ImageID fixture was accepted' }

$postgresReference = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$postgresID = 'sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$postgresReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'postgres-gosu-vulnerability-report.json') | ConvertFrom-Json
$postgresSummary = Assert-TrivyReportBinding -Report $postgresReport -ExpectedImageId $postgresID
Assert-ThrowsLike `
    -Action { Assert-PostgresGosuFindingScope -ImageReference $postgresReference -ImageId $postgresID -Summary $postgresSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } `
    -ExpectedMessagePattern 'cannot cover fixable finding' `
    -FailureMessage 'PostgreSQL exception did not reject the RC48 gosu finding because it has a fixed version'

$postgresUnapprovedNoFixReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'postgres-gosu-vulnerability-report.json') | ConvertFrom-Json
$postgresUnapprovedNoFixReport.Results[0].Vulnerabilities[0].FixedVersion = ''
$postgresUnapprovedNoFixReport.Results[0].Vulnerabilities[0] | Add-Member -NotePropertyName Status -NotePropertyValue 'affected'
$postgresUnapprovedNoFixSummary = Get-TrivyFindingSummary -Report $postgresUnapprovedNoFixReport
Assert-ThrowsLike `
    -Action { Assert-PostgresGosuFindingScope -ImageReference $postgresReference -ImageId $postgresID -Summary $postgresUnapprovedNoFixSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } `
    -ExpectedMessagePattern 'no approved RC71 no-fix tuple' `
    -FailureMessage 'PostgreSQL exception did not fail closed with an empty approved no-fix tuple set'

$postgresUnreviewedStatusReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'postgres-gosu-vulnerability-report.json') | ConvertFrom-Json
$postgresUnreviewedStatusReport.Results[0].Vulnerabilities[0].FixedVersion = ''
$postgresUnreviewedStatusReport.Results[0].Vulnerabilities[0] | Add-Member -NotePropertyName Status -NotePropertyValue 'not_affected'
$postgresUnreviewedStatusSummary = Get-TrivyFindingSummary -Report $postgresUnreviewedStatusReport
Assert-ThrowsLike `
    -Action { Assert-PostgresGosuFindingScope -ImageReference $postgresReference -ImageId $postgresID -Summary $postgresUnreviewedStatusSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } `
    -ExpectedMessagePattern 'unreviewed no-fix status' `
    -FailureMessage 'PostgreSQL exception accepted an unreviewed no-fix status'

$rejected = $false
try { Assert-PostgresGosuFindingScope -ImageReference 'postgres:latest' -ImageId $postgresID -Summary $postgresSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'PostgreSQL exception generalized beyond exact digest' }

$keycloakBase = 'quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
$keycloakID = 'sha256:' + ('c' * 64)
$keycloakReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'keycloak-vendor-rejected-cve-report.json') | ConvertFrom-Json
$keycloakSummary = Assert-TrivyReportBinding -Report $keycloakReport -ExpectedImageId $keycloakID
Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference $keycloakBase -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null

function Assert-KeycloakFixtureRejected {
    param(
        [Parameter(Mandatory)][scriptblock]$Mutate,
        [Parameter(Mandatory)][string]$Message
    )

    $report = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'keycloak-vendor-rejected-cve-report.json') | ConvertFrom-Json
    & $Mutate $report
    $summary = Get-TrivyFindingSummary -Report $report
    $rejected = $false
    try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference $keycloakBase -Summary $summary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw $Message }
}

$rejected = $false
try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId ('sha256:' + ('d' * 64)) -BaseReference $keycloakBase -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'Keycloak vendor-rejection exception generalized beyond the derived image ID' }
$rejected = $false
try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference 'quay.io/keycloak/keycloak:latest' -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'Keycloak vendor-rejection exception generalized beyond exact base digest' }
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].VulnerabilityID = 'CVE-DRIFT' } -Message 'Keycloak vendor-rejection exception generalized beyond exact CVE'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].PkgName = 'different-package' } -Message 'Keycloak vendor-rejection exception generalized beyond exact package'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].InstalledVersion = '1:21.0.12.0.8-1.2.el9' } -Message 'Keycloak vendor-rejection exception generalized beyond exact installed version'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].FixedVersion = '1:21.0.12.2.1-1.2.el9' } -Message 'Keycloak vendor-rejection exception generalized beyond fixed-version drift'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].Severity = 'CRITICAL' } -Message 'Keycloak vendor-rejection exception generalized beyond exact severity'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].Status = 'not_affected' } -Message 'Keycloak vendor-rejection exception generalized beyond exact status'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Class = 'lang-pkgs' } -Message 'Keycloak vendor-rejection exception generalized beyond exact class'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Type = 'library' } -Message 'Keycloak vendor-rejection exception generalized beyond exact type'

$reviewedAt = '2026-08-31T00:00:00Z'
$reviewDueAt = '2026-09-30T00:00:00Z'
$reviewNow = '2026-08-31T12:00:00Z'
$reviewRationale = 'Red Hat rejected the CVE for the retained OpenJDK system-libpng runtime.'
Assert-ExceptionReviewContract -Rationale $reviewRationale -ReviewedAt $reviewedAt -ReviewDueAt $reviewDueAt -CurrentTime $reviewNow | Out-Null

function Assert-ExceptionReviewRejected {
    param(
        [string]$Rationale = $reviewRationale,
        [string]$ReviewedAt = $reviewedAt,
        [string]$ReviewDueAt = $reviewDueAt,
        [string]$CurrentTime = $reviewNow,
        [Parameter(Mandatory)][string]$Message
    )

    $rejected = $false
    try { Assert-ExceptionReviewContract -Rationale $Rationale -ReviewedAt $ReviewedAt -ReviewDueAt $ReviewDueAt -CurrentTime $CurrentTime | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw $Message }
}

Assert-ExceptionReviewRejected -Rationale '' -Message 'exception review accepted missing rationale'
Assert-ExceptionReviewRejected -ReviewedAt '' -Message 'exception review accepted missing reviewedAt'
Assert-ExceptionReviewRejected -ReviewDueAt '' -Message 'exception review accepted missing reviewDueAt'
Assert-ExceptionReviewRejected -ReviewedAt '2026/08/31 00:00:00' -Message 'exception review accepted invalid reviewedAt timestamp'
Assert-ExceptionReviewRejected -ReviewDueAt 'not-a-timestamp' -Message 'exception review accepted invalid reviewDueAt timestamp'
Assert-ExceptionReviewRejected -ReviewDueAt $reviewedAt -Message 'exception review accepted due-at equal to reviewed-at'
Assert-ExceptionReviewRejected -ReviewDueAt '2026-08-31T00:00:01Z' -CurrentTime $reviewNow -Message 'exception review accepted an expired due date'
Assert-ExceptionReviewRejected -CurrentTime $reviewDueAt -Message 'exception review remained valid at the exact due timestamp'
Assert-ExceptionReviewRejected -CurrentTime '2026-08-30T23:59:59Z' -Message 'exception review accepted a current time before the review window'

$fixtureDBTime = [DateTimeOffset]::UtcNow.ToString('yyyy-MM-dd HH:mm:ss +0000') + ' UTC'
$versionProof = "Go: go1.25.7`nScanner: govulncheck@v1.7.0`nDB: https://vuln.go.dev`nDB updated: $fixtureDBTime`n"
$binaryProof = "=== Symbol Results ===`n`nNo vulnerabilities found.`n`nYour code is affected by 0 vulnerabilities.`n"
Assert-GovulncheckBinaryProof -ProofText $binaryProof -VersionText $versionProof | Out-Null
$weekendDBTime = [DateTimeOffset]::UtcNow.AddHours(-95).ToString('yyyy-MM-dd HH:mm:ss +0000') + ' UTC'
$weekendVersionProof = $versionProof -replace '(?m)^DB updated:.+$', "DB updated: $weekendDBTime"
Assert-GovulncheckBinaryProof -ProofText $binaryProof -VersionText $weekendVersionProof | Out-Null
$staleDBTime = [DateTimeOffset]::UtcNow.AddHours(-97).ToString('yyyy-MM-dd HH:mm:ss +0000') + ' UTC'
$staleVersionProof = $versionProof -replace '(?m)^DB updated:.+$', "DB updated: $staleDBTime"
$rejected = $false
try { Assert-GovulncheckBinaryProof -ProofText $binaryProof -VersionText $staleVersionProof | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'stale govulncheck database fixture exceeded the 96-hour release window' }
$rejected = $false
try { Assert-GovulncheckBinaryProof -ProofText ($binaryProof -replace '0 vulnerabilities', '1 vulnerability') -VersionText $versionProof | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'called-vulnerability govulncheck fixture was accepted' }

$rejected = $false
try { Resolve-ReleaseArtifactPath -ReleaseDirectory $projectRoot -RelativePath '../outside' | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'release artifact traversal fixture was accepted' }
$rejected = $false
try { Resolve-ReleaseArtifactPath -ReleaseDirectory $projectRoot -RelativePath 'C:/outside' | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'absolute release artifact fixture was accepted' }

$keycloakRejectedDecision = Get-IdpGateDecision -Mode keycloak -KeycloakImageApproved $false
$keycloakApprovedDecision = Get-IdpGateDecision -Mode keycloak -KeycloakImageApproved $true
$externalDecision = Get-IdpGateDecision -Mode external-managed
$noneDecision = Get-IdpGateDecision -Mode none
if ($keycloakRejectedDecision.Status -cne 'image_rejected' -or
    $keycloakRejectedDecision.BlockReason -cne 'self_hosted_keycloak_image_failed' -or
    $keycloakApprovedDecision.Status -cne 'image_approved_pending_canary' -or
    $keycloakApprovedDecision.BlockReason -cne 'idp_self_hosted_pending_canary' -or
    $externalDecision.Status -cne 'external_pending_canary' -or
    $externalDecision.ProductionCanary -cne 'pending' -or
    $noneDecision.Status -cne 'not_included') {
    throw 'IdP fail-closed decision fixture drifted'
}
$gateSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'release-image-gate.ps1')
$artifactVerifierSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1')
if ($gateSource -notmatch '(?s)\[string\]\s*\$IdPMode\s*=\s*''keycloak''' -or
    $gateSource -notmatch "Policy\s+'keycloak-26\.7\.2-exact-vendor-rejection'" -or
    $gateSource -notmatch [regex]::Escape('quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067')) {
    throw 'release image gate no longer defaults to exact Keycloak 26.7.2 scan mode'
}
if ($gateSource -match '(?m)verify\.ps1.*-SkipPostgres') {
    throw 'release image gate bypasses the full PostgreSQL verification gate'
}
if ($gateSource -notmatch "@\('build', '--pull', '--platform', 'linux/amd64', '--provenance=false'") {
    throw 'release image builds no longer pin linux/amd64 or disable nondeterministic BuildKit provenance wrappers'
}
Test-Task5ARequirement -Label 'generator re-parses manifest JSON before exact counter-type binding' -Action {
    if (-not $gateSource.Contains('$generatedManifest = ConvertFrom-ReleaseJson -JsonText (Get-Content -Raw -LiteralPath $manifestPath)', [StringComparison]::Ordinal) -or
        -not $gateSource.Contains('foreach ($record in @($generatedManifest.images)) {', [StringComparison]::Ordinal)) {
        throw 'release generator still validates pre-JSON PowerShell counter types'
    }
}
if ($artifactVerifierSource -match 'approved-by-exact-binary-exception' -or
    $artifactVerifierSource -notmatch '\[string\]\$postgres\[0\]\.policyStatus\s*-cne\s*''approved''' -or
    $artifactVerifierSource -notmatch '\$null\s*-ne\s*\$postgres\[0\]\.exception') {
    throw 'RC71 artifact verification still permits a PostgreSQL vulnerability exception'
}
Test-Task5ARequirement -Label 'artifact verifier avoids vulnerability counter coercion after exact JSON validation' -Action {
    if ($artifactVerifierSource -match '\[int\][^\r\n]*\.vulnerabilities\.(?:high|critical|total)' -or
        $artifactVerifierSource -notmatch '\$postgres\[0\]\.vulnerabilities\.total\s*-ne\s*0') {
        throw 'artifact verifier still casts/coerces a manifest vulnerability counter'
    }
}

$keycloakDefinitionLine = [regex]::Match($gateSource, '(?m)^\s*\$definitions\.Add\(\(New-ImageDefinition -Name ''keycloak''[^\r\n]+$')
if (-not $keycloakDefinitionLine.Success -or
    $keycloakDefinitionLine.Value -match 'BuildArguments|KEYCLOAK_BASE_IMAGE' -or
    $gateSource -match 'KEYCLOAK_BASE_IMAGE') {
    throw 'release image gate retains an override path for the literal-pinned Keycloak base image'
}

if ($artifactVerifierSource -notmatch '\[switch\]\$RequireTransferReady' -or
    $artifactVerifierSource -notmatch '\[string\]\$SignedReleaseTag' -or
    $artifactVerifierSource -notmatch '(?ms)if \(\$RequireTransferReady\) \{\s*Assert-TransferReadyManifest -Manifest \$manifest -ExpectedGitHead \$signedTagCommit\s*\| Out-Null\s*\}') {
    throw 'independent artifact verifier is missing the strict signed-tag transfer-ready source contract'
}
if (-not $artifactVerifierSource.Contains("Assert-StrictReleaseDirectoryName -ReleaseDirectory `$releaseRoot -ExpectedReleaseName '0.1.0-rc71'", [StringComparison]::Ordinal)) {
    throw 'independent artifact verifier does not bind strict transfer to an RC71 exactN directory leaf'
}
Test-Task5ARequirement -Label 'artifact verifier rejects raw duplicate JSON properties before object conversion' -Action {
    $rawManifestOffset = $artifactVerifierSource.IndexOf('$manifestJson = Get-Content -Raw -LiteralPath $manifestPath', [StringComparison]::Ordinal)
    $manifestConversionOffset = $artifactVerifierSource.IndexOf('$manifest = ConvertFrom-ReleaseJson -JsonText $manifestJson', [StringComparison]::Ordinal)
    if ($rawManifestOffset -lt 0 -or
        $manifestConversionOffset -le $rawManifestOffset) {
        throw 'independent verifier does not route raw manifest JSON through the strict release parser'
    }
}
if (-not $artifactVerifierSource.Contains('Assert-ExactReleaseManifestPolicy -Manifest $manifest | Out-Null', [StringComparison]::Ordinal)) {
    throw 'independent artifact verifier does not enforce exact vulnerability policy types and values'
}
Test-Task5ARequirement -Label 'fully qualified signed tag ref is used for every strict Git lookup' -Action {
    foreach ($requiredTagContract in @(
        '$signedTagRef = Get-StrictSignedReleaseTagRef -SignedReleaseTag $SignedReleaseTag',
        'git -C $projectRoot cat-file -t $signedTagRef',
        'git -C $projectRoot verify-tag $signedTagRef',
        '$peeledSignedTagRef = "$signedTagRef^{}"',
        'git -C $projectRoot rev-parse --verify $peeledSignedTagRef',
        'git -C $projectRoot cat-file -t $peeledSignedTagRef'
    )) {
        if (-not $artifactVerifierSource.Contains($requiredTagContract, [StringComparison]::Ordinal)) {
            throw "missing strict tag-ref contract: $requiredTagContract"
        }
    }
    if ($artifactVerifierSource -match 'git -C \$projectRoot (?:cat-file -t|verify-tag|rev-parse --verify) [^\r\n]*\$SignedReleaseTag') {
        throw 'strict Git lookup still accepts the unqualified SignedReleaseTag value'
    }
}
Test-Task5ARequirement -Label 'exact RC71 tag name maps to the fully qualified tag ref' -Action {
    $resolvedTagRef = Get-StrictSignedReleaseTagRef -SignedReleaseTag 'v0.1.0-rc71-signed'
    if ($resolvedTagRef -cne 'refs/tags/v0.1.0-rc71-signed') {
        throw "unexpected resolved tag ref: $resolvedTagRef"
    }
}
foreach ($invalidTagName in @(
    'v0.1.0-rc49-signed',
    'v0.1.0-rc50-signed',
    'v0.1.0-rc51-signed',
    'v0.1.0-rc52-signed',
    'v0.1.0-rc71-signed-sibling',
    'refs/heads/v0.1.0-rc71-signed',
    'refs/tags/v0.1.0-rc71-signed'
)) {
    Test-Task5AMutationRejected -Label "non-exact signed tag input $invalidTagName" -Action {
        Get-StrictSignedReleaseTagRef -SignedReleaseTag $invalidTagName | Out-Null
    }
}
$productionRunbook = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'docs\PRODUCTION-RUNBOOK.md')
Test-Task5ARequirement -Label 'production runbook invokes the exact RC71 strict transfer-ready verifier parameters' -Action {
    if (-not $productionRunbook.Contains('-RequireTransferReady', [StringComparison]::Ordinal) -or
        -not $productionRunbook.Contains('-SignedReleaseTag v0.1.0-rc71-signed', [StringComparison]::Ordinal) -or
        $productionRunbook -match '\bRC(?:32|38)\b') {
        throw 'production runbook does not invoke the exact strict transfer-ready verifier parameters'
    }
}

function Assert-RC71ProductionPrerequisites {
    param([Parameter(Mandatory)][string]$ReadinessText)

    $sectionStart = $ReadinessText.IndexOf('## Production prerequisites not yet performed', [StringComparison]::Ordinal)
    $sectionEnd = $ReadinessText.IndexOf('## Conservative V1 decisions requiring owner acknowledgement', [StringComparison]::Ordinal)
    if ($sectionStart -lt 0 -or $sectionEnd -le $sectionStart) {
        throw 'release readiness does not contain the bounded production prerequisites section'
    }
    $prerequisites = $ReadinessText.Substring($sectionStart, $sectionEnd - $sectionStart)
    foreach ($requiredRC71Instruction in @(
        'fresh RC71 image/SBOM/vulnerability',
        'same RC71 tag',
        'RC71 commit and signed release tag'
    )) {
        if (-not $prerequisites.Contains($requiredRC71Instruction, [StringComparison]::Ordinal)) {
            throw "release readiness production prerequisites are missing RC71 instruction: $requiredRC71Instruction"
        }
    }
    if ($prerequisites.Contains('RC32', [StringComparison]::Ordinal)) {
        throw 'release readiness production prerequisites retain an active RC32 instruction'
    }
}

$releaseReadiness = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'RELEASE-READINESS.md')
Assert-RC71ProductionPrerequisites -ReadinessText $releaseReadiness
foreach ($staleInstructionMutation in @(
    [pscustomobject]@{ Current = 'fresh RC71 image/SBOM/vulnerability'; Stale = 'fresh RC52 image/SBOM/vulnerability' },
    [pscustomobject]@{ Current = 'same RC71 tag'; Stale = 'same RC52 tag' },
    [pscustomobject]@{ Current = 'RC71 commit and signed release tag'; Stale = 'RC52 commit and signed release tag' }
)) {
    Test-Task5AMutationRejected -Label "stale readiness instruction $($staleInstructionMutation.Stale)" -Action {
        Assert-RC71ProductionPrerequisites -ReadinessText ($releaseReadiness.Replace(
            [string]$staleInstructionMutation.Current,
            [string]$staleInstructionMutation.Stale
        ))
    }
}

function Assert-RC71ImageScanReviewCurrentSection {
    param([Parameter(Mandatory)][string]$ReviewText)

    $sectionStart = $ReviewText.IndexOf('## Current RC71 remediation and review status (source/static only)', [StringComparison]::Ordinal)
    $sectionEnd = $ReviewText.IndexOf('## Historical RC1 approved-candidate snapshot (retired)', [StringComparison]::Ordinal)
    if ($sectionStart -lt 0 -or $sectionEnd -le $sectionStart) {
        throw 'image scan review does not contain a bounded current RC71 section'
    }
    $currentReview = $ReviewText.Substring($sectionStart, $sectionEnd - $sectionStart)
    foreach ($requiredRC71SecurityContract in @(
        'PostgreSQL has **no RC71 exception**',
        'The sole permitted RC71 exception',
        'sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067',
        'CVE-2026-22020',
        'java-21-openjdk-headless` / `1:21.0.12.1.1-1.2.el9',
        '`HIGH` / `affected`',
        '`HIGH,CRITICAL` with `ignoreUnfixed=false`'
    )) {
        if (-not $currentReview.Contains($requiredRC71SecurityContract, [StringComparison]::Ordinal)) {
            throw "current RC71 image scan review is missing exact security contract: $requiredRC71SecurityContract"
        }
    }
    foreach ($retiredSecurityContract in @(
        '21 HIGH and one CRITICAL',
        'sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec',
        '1:21.0.12.0.8-1.2.el9',
        'accepted, narrow binary-reachability exception'
    )) {
        if ($currentReview.Contains($retiredSecurityContract, [StringComparison]::Ordinal)) {
            throw "current RC71 image scan review includes retired security contract: $retiredSecurityContract"
        }
    }
}

$imageScanReview = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'docs\IMAGE-SCAN-REVIEW.md')
Assert-RC71ImageScanReviewCurrentSection -ReviewText $imageScanReview
Test-Task5AMutationRejected -Label 'retired Keycloak base injected into current RC71 scan section' -Action {
    Assert-RC71ImageScanReviewCurrentSection -ReviewText ($imageScanReview.Replace(
        'sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067',
        'sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec'
    ))
}

foreach ($supersededRC49Document in @(
    'docs\superpowers\plans\2026-08-31-invoice-rc49-image-security.md',
    'docs\superpowers\specs\2026-08-31-invoice-rc49-image-security-design.md',
    'docs\superpowers\plans\2026-08-31-invoice-rc50-release-recovery.md',
    'docs\superpowers\plans\2026-08-31-invoice-rc51-release-recovery.md',
    'docs\superpowers\plans\2026-08-31-invoice-rc52-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc53-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc54-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc55-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc56-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc57-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc58-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc59-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc60-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc61-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc62-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc63-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc64-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc65-release-recovery.md',
    'docs\superpowers\plans\2026-09-01-invoice-rc66-release-recovery.md',
    'docs\superpowers\plans\2026-09-02-invoice-rc67-release-recovery.md',
    'docs\superpowers\plans\2026-09-02-invoice-rc68-release-recovery.md',
    'docs\superpowers\plans\2026-09-02-invoice-rc69-release-recovery.md',
    'docs\superpowers\plans\2026-09-02-invoice-rc70-release-recovery.md'
)) {
    $documentPrefix = @(Get-Content -LiteralPath (Join-Path $projectRoot $supersededRC49Document) -TotalCount 8) -join "`n"
    if (-not $documentPrefix.Contains('**SUPERSEDED — DO NOT EXECUTE.**', [StringComparison]::Ordinal) -or
        -not $documentPrefix.Contains('docs/superpowers/plans/2026-09-02-invoice-rc71-release-recovery.md', [StringComparison]::Ordinal)) {
        throw "historical release document is still executable: $supersededRC49Document"
    }
}

$failureEvidenceVerifierSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-rc49-failure-evidence.ps1')
$failureEvidenceVerifier50Source = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-rc50-failure-evidence.ps1')
$failureEvidenceVerifier51Source = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-rc51-failure-evidence.ps1')
$failureEvidenceVerifier52Source = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-rc52-failure-evidence.ps1')
$rc71RecoveryPlan = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'docs\superpowers\plans\2026-09-02-invoice-rc71-release-recovery.md')
if (-not $failureEvidenceVerifierSource.Contains('Assert-RC49FailureEvidenceAnchor -ProjectRoot $projectRoot -AnchorText $anchorText', [StringComparison]::Ordinal) -or
    -not $failureEvidenceVerifier50Source.Contains('Assert-RC50FailureEvidenceAnchor -ProjectRoot $projectRoot -AnchorText $anchorText', [StringComparison]::Ordinal) -or
    -not $failureEvidenceVerifier51Source.Contains('Assert-RC51FailureEvidenceAnchor -ProjectRoot $projectRoot -AnchorText $anchorText', [StringComparison]::Ordinal) -or
    -not $failureEvidenceVerifier52Source.Contains('Assert-RC52FailureEvidenceAnchor -ProjectRoot $projectRoot -AnchorText $anchorText', [StringComparison]::Ordinal) -or
    -not $rc71RecoveryPlan.Contains('all four failure-evidence scripts', [StringComparison]::Ordinal) -or
    -not $productionRunbook.Contains('pwsh -NoProfile -File .\scripts\verify-rc49-failure-evidence.ps1', [StringComparison]::Ordinal) -or
    -not $productionRunbook.Contains('pwsh -NoProfile -File .\scripts\verify-rc50-failure-evidence.ps1', [StringComparison]::Ordinal) -or
    -not $productionRunbook.Contains('pwsh -NoProfile -File .\scripts\verify-rc51-failure-evidence.ps1', [StringComparison]::Ordinal) -or
    -not $productionRunbook.Contains('pwsh -NoProfile -File .\scripts\verify-rc52-failure-evidence.ps1', [StringComparison]::Ordinal)) {
    throw 'RC71 recovery flow does not invoke all four dedicated failure evidence anchor verifiers'
}
foreach ($requiredRC71RecoveryContract in @(
    'PowerShell 7.5+',
    'exit `42`',
    'ordinary and strict verifier exits `0` and `0`',
    'v0.1.0-rc71-signed',
    'without manually parsing manifest decisions'
)) {
    if (-not $rc71RecoveryPlan.Contains($requiredRC71RecoveryContract, [StringComparison]::Ordinal)) {
        throw "RC71 recovery plan is missing fail-closed contract: $requiredRC71RecoveryContract"
    }
}
if ($rc71RecoveryPlan -match '\[string\]\$rc71Manifest') {
    throw 'RC71 recovery plan manually coerces manifest decisions instead of trusting the verifiers'
}

$trackedFailureEvidenceAnchor = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'docs\RC49-FAILURE-EVIDENCE-SHA256SUMS.txt')
$anchoredPaths = @()
foreach ($line in @($trackedFailureEvidenceAnchor -split '\r?\n')) {
    if ([string]::IsNullOrWhiteSpace($line) -or $line.StartsWith('#', [StringComparison]::Ordinal)) { continue }
    $match = [regex]::Match($line, '^[0-9a-f]{64}  (?<path>release/0\.1\.0-rc49-exact[123]/[^\r\n]+)$')
    if (-not $match.Success) { throw "tracked RC49 failure evidence anchor has an invalid entry: $line" }
    $anchoredPaths += $match.Groups['path'].Value
}
$expectedAnchoredPaths = @(
    'release/0.1.0-rc49-exact1/logs/source-verification.log',
    'release/0.1.0-rc49-exact2/logs/source-verification.log',
    'release/0.1.0-rc49-exact2/logs/trivy-cache.log',
    'release/0.1.0-rc49-exact2/logs/trivy-db-update.log',
    'release/0.1.0-rc49-exact2/logs/trivy-tool-pull.log',
    'release/0.1.0-rc49-exact3/build/invoice-postgres.log',
    'release/0.1.0-rc49-exact3/build/invoice-source-agent.log',
    'release/0.1.0-rc49-exact3/build/invoice-system-api.log',
    'release/0.1.0-rc49-exact3/build/invoice-system-pdf-scanner.log',
    'release/0.1.0-rc49-exact3/build/invoice-system-tools.log',
    'release/0.1.0-rc49-exact3/build/invoice-system-web.log',
    'release/0.1.0-rc49-exact3/logs/source-verification.log',
    'release/0.1.0-rc49-exact3/logs/trivy-cache.log',
    'release/0.1.0-rc49-exact3/logs/trivy-db-update.log',
    'release/0.1.0-rc49-exact3/logs/trivy-java-db-update.log',
    'release/0.1.0-rc49-exact3/logs/trivy-tool-pull.log',
    'release/0.1.0-rc49-exact3/logs/trivy-version.log',
    'release/0.1.0-rc49-exact3/proof/trivy-version.txt'
)
if ($anchoredPaths.Count -ne $expectedAnchoredPaths.Count -or
    @($anchoredPaths | Sort-Object -Unique).Count -ne $anchoredPaths.Count) {
    throw 'tracked RC49 failure evidence anchor does not contain the exact 18-file inventory'
}
foreach ($expectedAnchoredPath in $expectedAnchoredPaths) {
    if ($anchoredPaths -cnotcontains $expectedAnchoredPath) {
        throw "tracked RC49 failure evidence anchor is missing exact path: $expectedAnchoredPath"
    }
}
$trackedRC50Anchor = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'docs\RC50-FAILURE-EVIDENCE-SHA256SUMS.txt')
$rc50AnchorEntries = @($trackedRC50Anchor -split '\r?\n' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) -and -not $_.StartsWith('#', [StringComparison]::Ordinal) })
if ($rc50AnchorEntries.Count -ne 1 -or
    $rc50AnchorEntries[0] -notmatch '^[0-9a-f]{64}  release/0\.1\.0-rc50-exact1/logs/source-verification\.log$') {
    throw 'tracked RC50 failure evidence anchor does not contain the exact one-file inventory'
}
$trackedRC51Anchor = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'docs\RC51-FAILURE-EVIDENCE-SHA256SUMS.txt')
$rc51AnchorEntries = @($trackedRC51Anchor -split '\r?\n' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) -and -not $_.StartsWith('#', [StringComparison]::Ordinal) })
$rc51AnchoredPaths = @()
foreach ($entry in $rc51AnchorEntries) {
    $match = [regex]::Match($entry, '^[0-9a-f]{64}  (?<path>release/0\.1\.0-rc51-exact1/[^\r\n]+)$')
    if (-not $match.Success) { throw "tracked RC51 failure evidence anchor has an invalid entry: $entry" }
    $rc51AnchoredPaths += $match.Groups['path'].Value
}
if ($rc51AnchorEntries.Count -ne 65 -or
    @($rc51AnchoredPaths | Sort-Object -Unique).Count -ne 65) {
    throw 'tracked RC51 failure evidence anchor does not contain the exact 65-file inventory'
}
$trackedRC52Anchor = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'docs\RC52-FAILURE-EVIDENCE-SHA256SUMS.txt')
$rc52AnchorEntries = @($trackedRC52Anchor -split '\r?\n' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) -and -not $_.StartsWith('#', [StringComparison]::Ordinal) })
if ($rc52AnchorEntries.Count -ne 1 -or
    $rc52AnchorEntries[0] -notmatch '^[0-9a-f]{64}  release/0\.1\.0-rc52-exact1/logs/source-verification\.log$') {
    throw 'tracked RC52 failure evidence anchor does not contain the exact one-file inventory'
}

$failureEvidenceFixtureRoot = Join-Path ([IO.Path]::GetTempPath()) ('invoice-rc49-failure-anchor-' + [guid]::NewGuid().ToString('N'))
try {
    $fixtureRelativePaths = @(
        'release/0.1.0-rc49-exact1/logs/first.log',
        'release/0.1.0-rc49-exact2/logs/second.log',
        'release/0.1.0-rc49-exact3/proof/third.txt'
    )
    $anchorLines = @()
    foreach ($relativePath in $fixtureRelativePaths) {
        $fixturePath = Join-Path $failureEvidenceFixtureRoot $relativePath
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $fixturePath) -Force
        [IO.File]::WriteAllText($fixturePath, "$relativePath`n", [Text.UTF8Encoding]::new($false))
        $anchorLines += "$(Get-FileSha256Lower -Path $fixturePath)  $relativePath"
    }
    $fixtureAnchor = ($anchorLines -join "`n") + "`n"
    Assert-RC49FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $fixtureAnchor | Out-Null

    $firstAnchoredHash = $anchorLines[0].Substring(0, 64)
    Assert-ThrowsLike `
        -Action {
            Assert-RC49FailureEvidenceAnchor `
                -ProjectRoot $failureEvidenceFixtureRoot `
                -AnchorText ($fixtureAnchor.Replace($firstAnchoredHash, ('0' * 64))) | Out-Null
        } `
        -ExpectedMessagePattern 'hash drifted' `
        -FailureMessage 'RC49 failure evidence hash mutation was accepted'

    $extraExactDirectory = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc49-exact4'
    $null = New-Item -ItemType Directory -Path $extraExactDirectory
    Assert-ThrowsLike `
        -Action { Assert-RC49FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $fixtureAnchor | Out-Null } `
        -ExpectedMessagePattern 'exact directory namespace' `
        -FailureMessage 'RC49 failure evidence accepted an unexpected exact4 namespace'
    Remove-Item -LiteralPath $extraExactDirectory -Force

    $exactOneRoot = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc49-exact1'
    $junctionTarget = Join-Path $failureEvidenceFixtureRoot 'rc49-exact1-junction-target'
    Move-Item -LiteralPath $exactOneRoot -Destination $junctionTarget
    $linkType = if ($IsWindows) { 'Junction' } else { 'SymbolicLink' }
    $null = New-Item -ItemType $linkType -Path $exactOneRoot -Target $junctionTarget
    Assert-ThrowsLike `
        -Action { Assert-RC49FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $fixtureAnchor | Out-Null } `
        -ExpectedMessagePattern 'reparse point' `
        -FailureMessage 'RC49 failure evidence accepted a reparse/junction exact root'
    Remove-Item -LiteralPath $exactOneRoot -Force
    Move-Item -LiteralPath $junctionTarget -Destination $exactOneRoot

    $nestedTarget = Join-Path $failureEvidenceFixtureRoot 'nested-junction-target'
    $nestedLink = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc49-exact3\nested-link'
    $null = New-Item -ItemType Directory -Path $nestedTarget
    $null = New-Item -ItemType $linkType -Path $nestedLink -Target $nestedTarget
    Assert-ThrowsLike `
        -Action { Assert-RC49FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $fixtureAnchor | Out-Null } `
        -ExpectedMessagePattern 'reparse point' `
        -FailureMessage 'RC49 failure evidence accepted a nested reparse/junction'
    Remove-Item -LiteralPath $nestedLink -Force
    Remove-Item -LiteralPath $nestedTarget -Force

    $extraEvidencePath = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc49-exact3\logs\extra.log'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $extraEvidencePath) -Force
    [IO.File]::WriteAllText($extraEvidencePath, "extra`n", [Text.UTF8Encoding]::new($false))
    Assert-ThrowsLike `
        -Action { Assert-RC49FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $fixtureAnchor | Out-Null } `
        -ExpectedMessagePattern 'file set drifted' `
        -FailureMessage 'RC49 failure evidence file-set mutation was accepted'

    $rc50FixturePath = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc50-exact1\logs\source-verification.log'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $rc50FixturePath) -Force
    [IO.File]::WriteAllText($rc50FixturePath, "rc50-failed`n", [Text.UTF8Encoding]::new($false))
    $rc50FixtureAnchor = "$(Get-FileSha256Lower -Path $rc50FixturePath)  release/0.1.0-rc50-exact1/logs/source-verification.log`n"
    Assert-RC50FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $rc50FixtureAnchor | Out-Null
    $rc50ExtraExact = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc50-exact2'
    $null = New-Item -ItemType Directory -Path $rc50ExtraExact
    Assert-ThrowsLike `
        -Action { Assert-RC50FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $rc50FixtureAnchor | Out-Null } `
        -ExpectedMessagePattern 'exact directory namespace' `
        -FailureMessage 'RC50 failure evidence accepted an unexpected exact2 namespace'

    $rc51FixturePath = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc51-exact1\release-manifest.json'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $rc51FixturePath) -Force
    [IO.File]::WriteAllText($rc51FixturePath, "{}`n", [Text.UTF8Encoding]::new($false))
    $rc51FixtureAnchor = "$(Get-FileSha256Lower -Path $rc51FixturePath)  release/0.1.0-rc51-exact1/release-manifest.json`n"
    Assert-RC51FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $rc51FixtureAnchor | Out-Null
    $rc51ExtraExact = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc51-exact2'
    $null = New-Item -ItemType Directory -Path $rc51ExtraExact
    Assert-ThrowsLike `
        -Action { Assert-RC51FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $rc51FixtureAnchor | Out-Null } `
        -ExpectedMessagePattern 'exact directory namespace' `
        -FailureMessage 'RC51 failure evidence accepted an unexpected exact2 namespace'

    $rc52FixturePath = Join-Path $failureEvidenceFixtureRoot 'release\0.1.0-rc52-exact1\logs\source-verification.log'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $rc52FixturePath) -Force
    [IO.File]::WriteAllText($rc52FixturePath, "rc52-host-nat-failed`n", [Text.UTF8Encoding]::new($false))
    $rc52FixtureAnchor = "$(Get-FileSha256Lower -Path $rc52FixturePath)  release/0.1.0-rc52-exact1/logs/source-verification.log`n"
    Assert-RC52FailureEvidenceAnchor -ProjectRoot $failureEvidenceFixtureRoot -AnchorText $rc52FixtureAnchor | Out-Null
} finally {
    if (Test-Path -LiteralPath $failureEvidenceFixtureRoot) {
        Remove-Item -LiteralPath $failureEvidenceFixtureRoot -Recurse -Force
    }
}

function Assert-RC71DerivedImageGateSource {
    param(
        [Parameter(Mandatory)][string]$Gate,
        [Parameter(Mandatory)][string]$Library,
        [Parameter(Mandatory)][string]$ArtifactVerifier
    )

    foreach ($requiredDefinition in @(
        'New-ImageDefinition -Name ''postgres-runtime'' -ArtifactName ''invoice-postgres'' -Reference "invoice-postgres:$ImageTag" -Kind ''built'' -Policy ''zero-findings'' -Context ''postgres'' -Dockerfile ''deploy/postgres/Dockerfile''',
        'New-ImageDefinition -Name ''clamav-runtime'' -ArtifactName ''invoice-clamav'' -Reference "invoice-clamav:$ImageTag" -Kind ''built'' -Policy ''zero-findings'' -Context ''clamav'' -Dockerfile ''deploy/clamav/Dockerfile''',
        'New-ImageDefinition -Name ''ingest-proxy'' -ArtifactName ''invoice-ingest-proxy'' -Reference "invoice-ingest-proxy:$ImageTag" -Kind ''built'' -Policy ''zero-findings'' -Context ''ingest-proxy'' -Dockerfile ''deploy/ingest-proxy/Dockerfile'''
    )) {
        if (-not $Gate.Contains($requiredDefinition, [StringComparison]::Ordinal)) {
            throw "RC71 release gate is missing locally built derived image definition: $requiredDefinition"
        }
    }
    foreach ($baseBinding in @(
        '-BaseReference $postgresBaseReference',
        '-BaseReference $clamavBaseReference',
        '-BaseReference $nginxBaseReference'
    )) {
        if (-not $Gate.Contains($baseBinding, [StringComparison]::Ordinal)) {
            throw "RC71 derived image definition is missing exact base binding: $baseBinding"
        }
    }
    foreach ($contextBinding in @(
        @{ Context = 'postgres'; Fingerprint = 'postgres' },
        @{ Context = 'clamav'; Fingerprint = 'clamav' },
        @{ Context = 'ingest-proxy'; Fingerprint = 'ingestProxy' }
    )) {
        if ($Gate -notmatch "(?m)^\s+$([regex]::Escape($contextBinding.Fingerprint)) = Get-ContextFingerprint" -or
            [regex]::Matches($Gate, "(?m)^\s+'?$([regex]::Escape($contextBinding.Context))'?\s*\{ Join-Path \`$projectRoot").Count -ne 1) {
            throw "RC71 release gate does not fingerprint and build exact context $($contextBinding.Context)"
        }
    }
    if ($Gate -notmatch "@\('build', '--pull', '--platform', 'linux/amd64', '--provenance=false'" -or
        -not $Gate.Contains('Assert-LinuxAmd64Platform -Platform "$($metadata.Os)/$($metadata.Architecture)"', [StringComparison]::Ordinal) -or
        -not $Library.Contains("function Assert-LinuxAmd64Platform", [StringComparison]::Ordinal)) {
        throw 'RC71 locally built images are not build-time and post-build gated to linux/amd64'
    }
    if ($Gate -match "exact-postgres-gosu-exception|approved-by-exact-binary-exception|postgres_exception_proof_failed" -or
        -not $Gate.Contains('postgresException = $null', [StringComparison]::Ordinal)) {
        throw 'RC71 release gate retains an active PostgreSQL exception/proof path'
    }
    foreach ($fingerprint in @('postgres', 'clamav', 'ingestProxy')) {
        if (-not $ArtifactVerifier.Contains("$fingerprint = Get-ContextFingerprint", [StringComparison]::Ordinal)) {
            throw "independent artifact verifier does not bind derived source fingerprint $fingerprint"
        }
    }
    if (-not $ArtifactVerifier.Contains("Assert-LinuxAmd64Platform -Platform ([string]`$record.platform)", [StringComparison]::Ordinal) -or
        -not $ArtifactVerifier.Contains("docker image inspect ([string]`$record.reference) --format '{{.Os}}/{{.Architecture}}'", [StringComparison]::Ordinal) -or
        -not $ArtifactVerifier.Contains('current image platform does not match the manifest-bound linux/amd64 platform', [StringComparison]::Ordinal) -or
        -not $ArtifactVerifier.Contains("[string]`$record.kind -cne 'built'", [StringComparison]::Ordinal) -or
        -not $ArtifactVerifier.Contains("[string]`$record.acquisition -cne 'built-from-source'", [StringComparison]::Ordinal)) {
        throw 'independent artifact verifier does not reject non-built or non-linux/amd64 RC71 image records'
    }
}

$gateLibrarySource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')
if ($gateSource -match '\bConvertFrom-Json\b' -or
    $artifactVerifierSource -match '\bConvertFrom-Json\b' -or
    [regex]::Matches($gateLibrarySource, '\bConvertFrom-Json\b').Count -ne 2 -or
    -not $gateLibrarySource.Contains('ConvertFrom-Json -DateKind String', [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('Assert-ReleasePowerShellRuntime | Out-Null', [StringComparison]::Ordinal) -or
    -not $artifactVerifierSource.Contains('Assert-ReleasePowerShellRuntime | Out-Null', [StringComparison]::Ordinal)) {
    throw 'production release JSON parsing bypasses the centralized DateKind String parser or PowerShell 7.5 boundary'
}
Assert-RC71DerivedImageGateSource -Gate $gateSource -Library $gateLibrarySource -ArtifactVerifier $artifactVerifierSource

$platformRejected = $false
try { Assert-LinuxAmd64Platform -Platform 'linux/arm64' | Out-Null } catch { $platformRejected = $true }
if (-not $platformRejected) { throw 'linux/arm64 release image fixture was accepted' }
Assert-LinuxAmd64Platform -Platform 'linux/amd64' | Out-Null

$bindingFixtureRoot = Join-Path ([IO.Path]::GetTempPath()) ('invoice-rc71-artifact-binding-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $bindingFixtureRoot | Out-Null
try {
    $bindingReportPath = Join-Path $bindingFixtureRoot 'report.json'
    $bindingSbomPath = Join-Path $bindingFixtureRoot 'sbom.json'
    Copy-Item -LiteralPath (Join-Path $fixtureRoot 'good-vulnerability-report.json') -Destination $bindingReportPath
    Copy-Item -LiteralPath (Join-Path $fixtureRoot 'good-sbom.json') -Destination $bindingSbomPath
    $bindingRecord = [pscustomobject]@{
        name = 'fixture'
        imageId = $imageID
        vulnerabilities = [pscustomobject]@{ high = 0; critical = 0; total = 0 }
        vulnerabilityReport = [pscustomobject]@{ path = 'report.json'; sha256 = Get-FileSha256Lower -Path $bindingReportPath }
        sbom = [pscustomobject]@{ path = 'sbom.json'; sha256 = Get-FileSha256Lower -Path $bindingSbomPath }
    } | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    Assert-GeneratedArtifactBinding -ReleaseDirectory $bindingFixtureRoot -ImageRecord $bindingRecord | Out-Null

    foreach ($counterName in @('high', 'critical', 'total')) {
        foreach ($counterMutation in @(
            [pscustomobject]@{ Label = 'null'; Value = $null },
            [pscustomobject]@{ Label = 'Boolean'; Value = $false },
            [pscustomobject]@{ Label = 'string'; Value = '0' },
            [pscustomobject]@{ Label = 'Decimal'; Value = [decimal]0 },
            [pscustomobject]@{ Label = 'Double'; Value = [double]0 },
            [pscustomobject]@{ Label = 'negative'; Value = [long]-1 }
        )) {
            $mutatedCounterRecord = $bindingRecord | ConvertTo-Json -Depth 10 | ConvertFrom-Json
            $mutatedCounterRecord.vulnerabilities.$counterName = $counterMutation.Value
            Test-Task5AMutationRejected -Label "vulnerabilities.$counterName $($counterMutation.Label) value" -Action {
                Assert-GeneratedArtifactBinding -ReleaseDirectory $bindingFixtureRoot -ImageRecord $mutatedCounterRecord | Out-Null
            }
        }

        $missingCounterRecord = $bindingRecord | ConvertTo-Json -Depth 10 | ConvertFrom-Json
        $missingCounterRecord.vulnerabilities.PSObject.Properties.Remove($counterName)
        Test-Task5AMutationRejected -Label "missing vulnerabilities.$counterName" -Action {
            Assert-GeneratedArtifactBinding -ReleaseDirectory $bindingFixtureRoot -ImageRecord $missingCounterRecord | Out-Null
        }

        $mismatchedCounterRecord = $bindingRecord | ConvertTo-Json -Depth 10 | ConvertFrom-Json
        $mismatchedCounterRecord.vulnerabilities.$counterName = [long]1
        Test-Task5AMutationRejected -Label "mismatched vulnerabilities.$counterName" -Action {
            Assert-GeneratedArtifactBinding -ReleaseDirectory $bindingFixtureRoot -ImageRecord $mismatchedCounterRecord | Out-Null
        }
    }

    $missingTotalRecord = $bindingRecord | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    $missingTotalRecord.vulnerabilities.PSObject.Properties.Remove('total')
    Assert-ThrowsLike `
        -Action { Assert-GeneratedArtifactBinding -ReleaseDirectory $bindingFixtureRoot -ImageRecord $missingTotalRecord | Out-Null } `
        -ExpectedMessagePattern 'manifest vulnerability total is missing' `
        -FailureMessage 'generated artifact binding did not explicitly reject a missing vulnerabilities.total property'

    $mismatchedTotalRecord = $bindingRecord | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    $mismatchedTotalRecord.vulnerabilities.total = [long]1
    Assert-ThrowsLike `
        -Action { Assert-GeneratedArtifactBinding -ReleaseDirectory $bindingFixtureRoot -ImageRecord $mismatchedTotalRecord | Out-Null } `
        -ExpectedMessagePattern 'manifest vulnerability counts are stale' `
        -FailureMessage 'generated artifact binding accepted vulnerabilities.total that disagrees with the Trivy summary'
} finally {
    Remove-Item -LiteralPath $bindingFixtureRoot -Recurse -Force
}

$transferReadyManifest = [pscustomobject]@{
    releaseName = '0.1.0-rc71'
    source = [pscustomobject]@{ gitDirty = $false; gitHead = '0123456789abcdef0123456789abcdef01234567' }
    images = @(
        [pscustomobject]@{ name = 'api'; reference = 'invoice-system-api:0.1.0-rc71' },
        [pscustomobject]@{ name = 'pdf-scanner'; reference = 'invoice-system-pdf-scanner:0.1.0-rc71' },
        [pscustomobject]@{ name = 'tools'; reference = 'invoice-system-tools:0.1.0-rc71' },
        [pscustomobject]@{ name = 'web'; reference = 'invoice-system-web:0.1.0-rc71' },
        [pscustomobject]@{ name = 'source-agent'; reference = 'invoice-source-agent:0.1.0-rc71' },
        [pscustomobject]@{ name = 'postgres-runtime'; reference = 'invoice-postgres:0.1.0-rc71' },
        [pscustomobject]@{ name = 'clamav-runtime'; reference = 'invoice-clamav:0.1.0-rc71' },
        [pscustomobject]@{ name = 'ingest-proxy'; reference = 'invoice-ingest-proxy:0.1.0-rc71' },
        [pscustomobject]@{ name = 'keycloak'; reference = 'invoice-keycloak:0.1.0-rc71' }
    )
    decisions = [pscustomobject]@{
        applicationImageGate = 'passed'
        productionLaunch = 'blocked'
        reasons = @('idp_self_hosted_pending_canary')
    }
}
Assert-TransferReadyManifest -Manifest $transferReadyManifest -ExpectedGitHead '0123456789abcdef0123456789abcdef01234567' | Out-Null

function Assert-TransferManifestMutationRejected {
    param(
        [Parameter(Mandatory)][scriptblock]$Mutate,
        [Parameter(Mandatory)][string]$ExpectedMessagePattern,
        [Parameter(Mandatory)][string]$FailureMessage
    )

    $manifestFixture = $transferReadyManifest | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    & $Mutate $manifestFixture
    Assert-ThrowsLike `
        -Action { Assert-TransferReadyManifest -Manifest $manifestFixture -ExpectedGitHead '0123456789abcdef0123456789abcdef01234567' | Out-Null } `
        -ExpectedMessagePattern $ExpectedMessagePattern `
        -FailureMessage $FailureMessage
}

Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.source.gitDirty = $true } -ExpectedMessagePattern 'source\.gitDirty=false' -FailureMessage 'strict transfer mode accepted a dirty source manifest'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.source.gitHead = 'not-a-commit' } -ExpectedMessagePattern '40-hex' -FailureMessage 'strict transfer mode accepted a malformed source commit'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.source.gitHead = '1123456789abcdef0123456789abcdef01234567' } -ExpectedMessagePattern 'signed tag commit' -FailureMessage 'strict transfer mode accepted a source commit different from the signed tag'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.decisions.applicationImageGate = 'failed' } -ExpectedMessagePattern 'applicationImageGate=passed' -FailureMessage 'strict transfer mode accepted a failed application image gate'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.decisions.productionLaunch = 'approved' } -ExpectedMessagePattern 'productionLaunch=blocked' -FailureMessage 'strict transfer mode accepted a non-blocked production launch decision'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.releaseName = '0.1.0-rc52' } -ExpectedMessagePattern 'releaseName=0\.1\.0-rc71' -FailureMessage 'strict transfer mode accepted the failed RC52 release name'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.releaseName = '0.1.0-rc71-sibling' } -ExpectedMessagePattern 'releaseName=0\.1\.0-rc71' -FailureMessage 'strict transfer mode accepted a sibling RC71 release name'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.releaseName = '0.1.0-RC71' } -ExpectedMessagePattern 'releaseName=0\.1\.0-rc71' -FailureMessage 'strict transfer mode accepted a case-drifted RC71 release name'
foreach ($invalidReleaseNameFixture in @(
    [pscustomobject]@{ Label = 'null'; Value = $null },
    [pscustomobject]@{ Label = 'Boolean'; Value = $false },
    [pscustomobject]@{ Label = 'singleton array'; Value = [string[]]@('0.1.0-rc71') }
)) {
    Assert-TransferManifestMutationRejected `
        -Mutate { param($manifest) $manifest.releaseName = $invalidReleaseNameFixture.Value } `
        -ExpectedMessagePattern 'releaseName=0\.1\.0-rc71' `
        -FailureMessage "strict transfer mode accepted $($invalidReleaseNameFixture.Label) releaseName"
}

Assert-TransferManifestMutationRejected `
    -Mutate {
        param($manifest)
        $manifest.PSObject.Properties.Remove('releaseName')
        $manifest | Add-Member -NotePropertyName 'ReleaseName' -NotePropertyValue '0.1.0-rc71'
    } `
    -ExpectedMessagePattern 'exact property releaseName' `
    -FailureMessage 'strict transfer mode accepted case-drifted ReleaseName'
Assert-TransferManifestMutationRejected `
    -Mutate {
        param($manifest)
        $manifest.images[0].PSObject.Properties.Remove('name')
        $manifest.images[0] | Add-Member -NotePropertyName 'Name' -NotePropertyValue 'api'
    } `
    -ExpectedMessagePattern 'exact property name' `
    -FailureMessage 'strict transfer mode accepted case-drifted image Name'
Assert-TransferManifestMutationRejected `
    -Mutate {
        param($manifest)
        $manifest.images[0].PSObject.Properties.Remove('reference')
        $manifest.images[0] | Add-Member -NotePropertyName 'Reference' -NotePropertyValue 'invoice-system-api:0.1.0-rc71'
    } `
    -ExpectedMessagePattern 'exact property reference' `
    -FailureMessage 'strict transfer mode accepted case-drifted image Reference'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.source.gitHead = @('0123456789abcdef0123456789abcdef01234567') } -ExpectedMessagePattern 'source\.gitHead.*string' -FailureMessage 'strict transfer mode coerced a singleton string array into source.gitHead'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.decisions.applicationImageGate = @('passed') } -ExpectedMessagePattern 'applicationImageGate=passed.*string' -FailureMessage 'strict transfer mode coerced a singleton string array into applicationImageGate'
Assert-TransferManifestMutationRejected -Mutate { param($manifest) $manifest.decisions.productionLaunch = @('blocked') } -ExpectedMessagePattern 'productionLaunch=blocked.*string' -FailureMessage 'strict transfer mode coerced a singleton string array into productionLaunch'

$missingReleaseNameManifest = $transferReadyManifest | ConvertTo-Json -Depth 10 | ConvertFrom-Json
$missingReleaseNameManifest.PSObject.Properties.Remove('releaseName')
Assert-ThrowsLike `
    -Action { Assert-TransferReadyManifest -Manifest $missingReleaseNameManifest -ExpectedGitHead '0123456789abcdef0123456789abcdef01234567' | Out-Null } `
    -ExpectedMessagePattern 'releaseName=0\.1\.0-rc71' `
    -FailureMessage 'strict transfer mode accepted a missing releaseName'

foreach ($inventoryMutation in @(
    [pscustomobject]@{
        Label = 'missing images property'
        Expected = 'exact property images'
        Mutate = { param($manifest) $manifest.PSObject.Properties.Remove('images') }
    },
    [pscustomobject]@{
        Label = 'case-drifted Images property'
        Expected = 'exact property images'
        Mutate = {
            param($manifest)
            $records = $manifest.images
            $manifest.PSObject.Properties.Remove('images')
            $manifest | Add-Member -NotePropertyName 'Images' -NotePropertyValue $records
        }
    },
    [pscustomobject]@{
        Label = 'non-array images value'
        Expected = 'exact RC71 image inventory'
        Mutate = { param($manifest) $manifest.images = 'invoice-system-api:0.1.0-rc71' }
    },
    [pscustomobject]@{
        Label = 'eight-image inventory'
        Expected = 'exact RC71 image inventory'
        Mutate = { param($manifest) $manifest.images = @($manifest.images | Select-Object -First 8) }
    },
    [pscustomobject]@{
        Label = 'ten-image inventory'
        Expected = 'exact RC71 image inventory'
        Mutate = {
            param($manifest)
            $manifest.images = @($manifest.images) + [pscustomobject]@{
                name = 'unexpected'
                reference = 'invoice-unexpected:0.1.0-rc71'
            }
        }
    },
    [pscustomobject]@{
        Label = 'duplicate image name'
        Expected = 'exact RC71 image inventory'
        Mutate = { param($manifest) $manifest.images[8].name = 'api' }
    },
    [pscustomobject]@{
        Label = 'missing image name'
        Expected = 'exact property name'
        Mutate = { param($manifest) $manifest.images[0].PSObject.Properties.Remove('name') }
    },
    [pscustomobject]@{
        Label = 'missing image reference'
        Expected = 'exact property reference'
        Mutate = { param($manifest) $manifest.images[0].PSObject.Properties.Remove('reference') }
    },
    [pscustomobject]@{
        Label = 'case-drifted image name value'
        Expected = 'exact RC71 image inventory'
        Mutate = { param($manifest) $manifest.images[0].name = 'API' }
    },
    [pscustomobject]@{
        Label = 'case-drifted image reference value'
        Expected = 'exact RC71 image inventory'
        Mutate = { param($manifest) $manifest.images[0].reference = 'Invoice-system-api:0.1.0-rc71' }
    }
)) {
    Assert-TransferManifestMutationRejected `
        -Mutate $inventoryMutation.Mutate `
        -ExpectedMessagePattern $inventoryMutation.Expected `
        -FailureMessage "strict transfer mode accepted $($inventoryMutation.Label)"
}

foreach ($imageStringField in @('name', 'reference')) {
    $validSingletonValue = if ($imageStringField -ceq 'name') { 'api' } else { 'invoice-system-api:0.1.0-rc71' }
    foreach ($invalidImageStringFixture in @(
        [pscustomobject]@{ Label = 'null'; Value = $null },
        [pscustomobject]@{ Label = 'Boolean'; Value = $false },
        [pscustomobject]@{ Label = 'singleton array'; Value = [string[]]@($validSingletonValue) }
    )) {
        Assert-TransferManifestMutationRejected `
            -Mutate { param($manifest) $manifest.images[0].$imageStringField = $invalidImageStringFixture.Value } `
            -ExpectedMessagePattern 'exact RC71 image inventory' `
            -FailureMessage "strict transfer mode accepted $($invalidImageStringFixture.Label) image $imageStringField"
    }
}

foreach ($expectedImage in @($transferReadyManifest.images)) {
    foreach ($wrongTag in @('0.1.0-rc52', '0.1.0-rc71-sibling')) {
        Assert-TransferManifestMutationRejected `
            -Mutate {
                param($manifest)
                $record = @($manifest.images | Where-Object { [string]$_.name -ceq [string]$expectedImage.name })
                $record[0].reference = ([string]$record[0].reference) -replace ':0\.1\.0-rc71$', ":$wrongTag"
            } `
            -ExpectedMessagePattern 'exact RC71 image inventory' `
            -FailureMessage "strict transfer mode accepted $($expectedImage.name) image tag $wrongTag"
    }
}

foreach ($transferMutation in @(
    [pscustomobject]@{
        Label = 'scalar string reasons'
        Mutate = { param($manifest) $manifest.decisions.reasons = 'idp_self_hosted_pending_canary' }
    },
    [pscustomobject]@{
        Label = 'null reasons'
        Mutate = { param($manifest) $manifest.decisions.reasons = $null }
    },
    [pscustomobject]@{
        Label = 'Boolean reasons'
        Mutate = { param($manifest) $manifest.decisions.reasons = $false }
    },
    [pscustomobject]@{
        Label = 'object reasons'
        Mutate = { param($manifest) $manifest.decisions.reasons = [pscustomobject]@{ reason = 'idp_self_hosted_pending_canary' } }
    },
    [pscustomobject]@{
        Label = 'empty reasons array'
        Mutate = { param($manifest) $manifest.decisions.reasons = @() }
    },
    [pscustomobject]@{
        Label = 'extra reasons array element'
        Mutate = { param($manifest) $manifest.decisions.reasons = @('idp_self_hosted_pending_canary', 'extra') }
    },
    [pscustomobject]@{
        Label = 'string source.gitDirty'
        Mutate = { param($manifest) $manifest.source.gitDirty = 'false' }
    },
    [pscustomobject]@{
        Label = 'numeric source.gitDirty'
        Mutate = { param($manifest) $manifest.source.gitDirty = [long]0 }
    },
    [pscustomobject]@{
        Label = 'null source.gitDirty'
        Mutate = { param($manifest) $manifest.source.gitDirty = $null }
    }
)) {
    $manifestFixture = $transferReadyManifest | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    & $transferMutation.Mutate $manifestFixture
    Test-Task5AMutationRejected -Label $transferMutation.Label -Action {
        Assert-TransferReadyManifest -Manifest $manifestFixture -ExpectedGitHead '0123456789abcdef0123456789abcdef01234567' | Out-Null
    }
}

function Assert-RC71RuntimeAndBackupBindings {
    param([Parameter(Mandatory)][string]$ProjectRoot)

    $runtimeVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')
    $provisioningVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
    $nginxVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-nginx-configs.ps1')
    $headerVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-web-security-headers.ps1')
    $backup = Get-Content -Raw -LiteralPath (Join-Path $ProjectRoot 'deploy\backup\backup.sh')
    $restore = Get-Content -Raw -LiteralPath (Join-Path $ProjectRoot 'deploy\backup\restore-drill.sh')

    function Assert-WebSecurityHeaderVerifierModeContract {
        param([Parameter(Mandatory)][string]$Source)

        if (-not $Source.Contains('$releaseBound = -not [string]::IsNullOrWhiteSpace($ExpectedImageID)', [StringComparison]::Ordinal) -or
            -not $Source.Contains('$mountArguments = @()', [StringComparison]::Ordinal)) {
            throw 'web header verifier does not distinguish release-bound and compatibility modes'
        }
        $compatibilityBranch = [regex]::Match(
            $Source,
            '(?ms)^if \(-not \$releaseBound\) \{\r?\n(?<body>.*?)^\}'
        )
        if (-not $compatibilityBranch.Success -or
            $compatibilityBranch.Groups['body'].Value -notmatch 'Resolve-Path.*web\\nginx\.conf' -or
            $compatibilityBranch.Groups['body'].Value -notmatch 'Resolve-Path.*web\\dist' -or
            $compatibilityBranch.Groups['body'].Value -notmatch 'type=bind,source=\$configPath,target=/etc/nginx/conf\.d/default\.conf,readonly' -or
            $compatibilityBranch.Groups['body'].Value -notmatch 'type=bind,source=\$distPath,target=/usr/share/nginx/html,readonly' -or
            [regex]::Matches($Source, 'type=bind,source=').Count -ne 2) {
            throw 'pre-build web header compatibility mode no longer owns exactly both host bind mounts'
        }
        if ($Source -notmatch '(?s)function Get-ReleaseBoundAssetRequestPath.*docker exec \$Container.*find /usr/share/nginx/html/assets.*return \$assetPath\.Substring' -or
            -not $Source.Contains('$assetRequestPath = Get-ReleaseBoundAssetRequestPath -Container $container', [StringComparison]::Ordinal)) {
            throw 'release-bound web header mode does not discover an asset from the running immutable image'
        }
        if (-not $Source.Contains("`$dockerRunArguments += `$mountArguments", [StringComparison]::Ordinal) -or
            $Source -match '(?s)docker run --detach --rm.*--mount') {
            throw 'release-bound web header mode can still include an unconditional host bind mount'
        }
        if ($Source -match "'--publish'" -or
            $Source.Contains('Invoke-WebRequest', [StringComparison]::Ordinal) -or
            $Source -match '(?m)^\s*\$binding\s*=\s*\(docker port ') {
            throw 'web header verification still depends on Docker Desktop host port publishing'
        }
        if ($Source -notmatch '(?s)function Invoke-ContainerWebHeaderProbe.*docker exec \$Container wget --spider -S -T 2 \$uri' -or
            $Source -notmatch '\$statusMatches\.Count -ne 1' -or
            -not $Source.Contains("`$statusMatches[0].Groups['status'].Value -cne '200'", [StringComparison]::Ordinal)) {
            throw 'web header verification does not require one container-local HTTP 200 response'
        }
        foreach ($exactHeaderContract in @(
            "Content-Security-Policy: `$ExpectedContentSecurityPolicy",
            "[string]`$ExpectedContentSecurityPolicy = `$approvedContentSecurityPolicy",
            "-Path '/admin' -ExpectedContentSecurityPolicy `$approvedAdminContentSecurityPolicy",
            "frame-ancestors https://console.solov.cc",
            'X-Content-Type-Options: nosniff',
            'Referrer-Policy: no-referrer'
        )) {
            if (-not $Source.Contains($exactHeaderContract, [StringComparison]::Ordinal)) {
                throw "web header verification does not bind the exact response header: $exactHeaderContract"
            }
        }
        foreach ($diagnosticContract in @(
            'last HTTP/exec error:',
            'docker inspect state:',
            'docker logs:'
        )) {
            if (-not $Source.Contains($diagnosticContract, [StringComparison]::Ordinal)) {
                throw "web header verification timeout omits required diagnostics: $diagnosticContract"
            }
        }
    }

    Assert-WebSecurityHeaderVerifierModeContract -Source $headerVerifier

    foreach ($binding in @(
        @{ Source = $runtimeVerifier; Required = '[string]$ExpectedPostgresImageID'; Label = 'Keycloak runtime PostgreSQL image ID' },
        @{ Source = $runtimeVerifier; Required = '[string]$ExpectedProbeImageID'; Label = 'Keycloak runtime probe image ID' },
        @{ Source = $provisioningVerifier; Required = '[string]$ExpectedPostgresImageID'; Label = 'Keycloak provisioning PostgreSQL image ID' },
        @{ Source = $nginxVerifier; Required = '[string]$ExpectedImageID'; Label = 'Nginx configuration image ID' },
        @{ Source = $headerVerifier; Required = '[string]$ExpectedImageID'; Label = 'web header image ID' }
    )) {
        if (-not $binding.Source.Contains($binding.Required, [StringComparison]::Ordinal)) {
            throw "RC71 verifier is missing expected ID binding: $($binding.Label)"
        }
    }
    foreach ($requiredGateArgument in @(
        '-PostgresImage ([string]$postgresRuntimeRecord.reference)',
        '-ExpectedPostgresImageID ([string]$postgresRuntimeRecord.imageId)',
        '-ProbeImage ([string]$ingestRuntimeRecord.reference)',
        '-ExpectedProbeImageID ([string]$ingestRuntimeRecord.imageId)',
        '-Image ([string]$ingestRuntimeRecord.reference) -ExpectedImageID ([string]$ingestRuntimeRecord.imageId)',
        '-Image ([string]$webRuntimeRecord.reference) -ExpectedImageID ([string]$webRuntimeRecord.imageId)'
    )) {
        if (-not $gateSource.Contains($requiredGateArgument, [StringComparison]::Ordinal)) {
            throw "post-build verifier is not passed an exact derived reference/ID: $requiredGateArgument"
        }
    }
    foreach ($proofBinding in @(
        'postgresReference = [string]$postgresRuntimeRecord.reference',
        'postgresImageId = [string]$postgresRuntimeRecord.imageId',
        'probeReference = [string]$ingestRuntimeRecord.reference',
        'probeImageId = [string]$ingestRuntimeRecord.imageId'
    )) {
        if (-not $gateSource.Contains($proofBinding, [StringComparison]::Ordinal) -or
            -not $artifactVerifierSource.Contains(".$($proofBinding.Split(' = ')[0])", [StringComparison]::Ordinal)) {
            throw "retained runtime proof is not independently bound to dependency: $proofBinding"
        }
    }
    if ($runtimeVerifier -match 'postgres:18(?:\.6)?-alpine@sha256' -or
        $provisioningVerifier -match 'postgres:18(?:\.6)?-alpine@sha256') {
        throw 'Keycloak runtime/provisioning verifier retains a direct external PostgreSQL runtime reference'
    }
    if ($backup -match 'nginx:1\.30-alpine@sha256' -or $restore -match 'postgres:18(?:\.6)?-alpine@sha256' -or
        -not $backup.Contains(': "${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}"', [StringComparison]::Ordinal) -or
        -not $backup.Contains('invoice_ingest_proxy_image="invoice-ingest-proxy:$INVOICE_IMAGE_TAG"', [StringComparison]::Ordinal) -or
        -not $backup.Contains('invoice_postgres_image="invoice-postgres:$INVOICE_IMAGE_TAG"', [StringComparison]::Ordinal) -or
        -not $restore.Contains('restore_postgres_image="invoice-postgres:$INVOICE_IMAGE_TAG"', [StringComparison]::Ordinal) -or
        -not $backup.Contains('docker image inspect "$invoice_ingest_proxy_image" "$invoice_postgres_image"', [StringComparison]::Ordinal) -or
        -not $restore.Contains('docker image inspect "$restore_postgres_image"', [StringComparison]::Ordinal) -or
        -not $backup.Contains('docker run --pull never', [StringComparison]::Ordinal) -or
        -not $restore.Contains('docker run --pull never', [StringComparison]::Ordinal)) {
        throw 'current backup/restore drill is not bound to existing no-pull RC71 derived images'
    }
}

Assert-RC71RuntimeAndBackupBindings -ProjectRoot $projectRoot

if ($task5AMutationFailures.Count -gt 0) {
    throw "Task 5A focused mutation failures:`n- $($task5AMutationFailures -join "`n- ")"
}

Write-Host 'Release image gate offline/static fixtures passed.'
$global:LASTEXITCODE = 0
