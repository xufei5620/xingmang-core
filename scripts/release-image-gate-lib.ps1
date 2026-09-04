Set-StrictMode -Version Latest

function Get-ComposeOptionalBoolean {
    param(
        [Parameter(Mandatory)]$ComposeObject,
        [Parameter(Mandatory)][string]$PropertyName
    )

    $property = $ComposeObject.PSObject.Properties[$PropertyName]
    if ($null -eq $property) {
        return $false
    }
    if ($property.Value -isnot [bool]) {
        throw "Compose property $PropertyName must be a Boolean when present"
    }
    return $property.Value
}

function Get-ComposeOptionalString {
    param(
        [Parameter(Mandatory)]$ComposeObject,
        [Parameter(Mandatory)][string]$PropertyName
    )

    $property = $ComposeObject.PSObject.Properties[$PropertyName]
    if ($null -eq $property) {
        return $null
    }
    if ($null -eq $property.Value -or $property.Value -isnot [string]) {
        throw "Compose property $PropertyName must be a non-null String when present"
    }
    return $property.Value
}

function Get-RequiredImageId {
    param([Parameter(Mandatory)][string]$Reference)

    $id = (& docker image inspect $Reference --format '{{.Id}}' 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $id -notmatch '^sha256:[0-9a-f]{64}$') {
        throw "cannot resolve an immutable image ID for $Reference"
    }
    return $id
}

function ConvertFrom-TrivyDatabaseTimestamp {
    param([Parameter(Mandatory)][string]$Timestamp)

    if ($Timestamp -notmatch '^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})(?:\.(\d+))? ([+-]\d{2})(\d{2}) UTC$') {
        throw "unrecognized Trivy database timestamp: $Timestamp"
    }
    $fraction = ([string]$Matches[2] + '0000000').Substring(0, 7)
    $normalized = "$($Matches[1]).$fraction $($Matches[3]):$($Matches[4])"
    return [DateTimeOffset]::ParseExact($normalized, 'yyyy-MM-dd HH:mm:ss.fffffff zzz', [Globalization.CultureInfo]::InvariantCulture)
}

# --- release-gate live-volume Trivy download TMPDIR fix ----------------------
#
# scripts/release-image-gate.ps1 runs Trivy's own `--download-db-only`/
# `--download-java-db-only` against the shared, live Trivy cache volume at
# the start of every release, whenever that cache is stale. Trivy 0.74
# downloads into $TMPDIR (defaulting to the container's own /tmp) and only
# then moves the result into --cache-dir; when --cache-dir is a mounted
# Docker volume (as it always is here) that move crosses filesystems and can
# silently fail, leaving no db/java-db directory behind while still logging
# success -- found by the team lead while reseeding the real shared cache
# volume during the XM-INV-TRIVY-REFRESH-FIX investigation (see
# docs/handoffs/XM-INV-TRIVY-REFRESH-FIX.md, "Other bugs found" #2), which
# explicitly flagged this exact gap in release-image-gate.ps1 as a follow-up
# rather than fix it there (out of that branch's scope). scripts/refresh-
# trivy-cache-lib.ps1's own Invoke-TrivyCacheFreshnessSelfCheck already works
# around this for its own (separate, disposable staging-volume) Trivy
# invocations by pointing TMPDIR at a directory already on the same volume;
# the functions below give release-image-gate.ps1's real, live-volume
# download steps the same fix.

# Assert-SafeTrivyCacheContainerDirectory guards the one container path these
# functions accept before it is ever embedded into a shell command (mkdir/rm)
# or a docker -e value -- mirrors refresh-trivy-cache-lib.ps1's own Assert-
# SafeTrivyCacheSubPath / Assert-SafeOciDigestForShell "validate before
# shelling out" convention.
function Assert-SafeTrivyCacheContainerDirectory {
    param([Parameter(Mandatory)][string]$ContainerDirectory)
    if ($ContainerDirectory -notmatch '^/[0-9A-Za-z._/-]+$') {
        throw "unsafe Trivy cache container directory: $ContainerDirectory"
    }
}

# Get-TrivyCacheDownloadArguments builds the docker "run" argument list for
# release-image-gate.ps1's own Trivy database-download invocations
# (--download-db-only / --download-java-db-only): identical to that script's
# own Get-TrivyArguments (--rm, docker socket mount, cache volume mount,
# Trivy image, then Command) but also sets TMPDIR to a directory inside the
# mounted cache volume, so a real download triggered by a stale cache never
# crosses filesystems on its final move into --cache-dir. The caller is
# responsible for the directory at TMPDIR actually existing on the volume
# before this runs (New-TrivyCacheTmpDirectoryDockerArguments below) and for
# removing it afterward (New-TrivyCacheTmpDirectoryCleanupDockerArguments).
function Get-TrivyCacheDownloadArguments {
    param(
        [Parameter(Mandatory)][string[]]$Command,
        [Parameter(Mandatory)][string]$Volume,
        [Parameter(Mandatory)][string]$TrivyImage,
        [Parameter(Mandatory)][string]$ContainerCacheDirectory
    )
    Assert-SafeTrivyCacheContainerDirectory -ContainerDirectory $ContainerCacheDirectory | Out-Null
    return @(
        'run', '--rm',
        '-e', "TMPDIR=$ContainerCacheDirectory/tmp",
        '-v', '/var/run/docker.sock:/var/run/docker.sock',
        '-v', "${Volume}:${ContainerCacheDirectory}",
        $TrivyImage
    ) + $Command
}

# New-TrivyCacheTmpDirectoryDockerArguments / New-TrivyCacheTmpDirectoryCleanupDockerArguments
# build the docker "run" argument lists release-image-gate.ps1 uses to
# create, respectively remove, the TMPDIR directory Get-
# TrivyCacheDownloadArguments points Trivy's downloader at -- directly
# against the live cache volume (there is no staging clone here, unlike
# refresh-trivy-cache-lib.ps1's own Copy-TrivyCacheVolumeToVolume, which
# creates this same "tmp" directory only as a side effect of cloning into a
# disposable volume). Reuses the already-pulled Trivy tool image itself
# (Alpine-based, with a real /bin/sh) with its entrypoint overridden to a
# shell -- the same "--entrypoint override for a utility shell command
# against an already-pulled image" pattern release-image-gate.ps1 already
# uses for its Keycloak runtime-pruning proof -- rather than pulling a
# second image just to run mkdir/rm against the volume.
function New-TrivyCacheTmpDirectoryDockerArguments {
    param(
        [Parameter(Mandatory)][string]$Volume,
        [Parameter(Mandatory)][string]$TrivyImage,
        [Parameter(Mandatory)][string]$ContainerCacheDirectory
    )
    Assert-SafeTrivyCacheContainerDirectory -ContainerDirectory $ContainerCacheDirectory | Out-Null
    return @(
        'run', '--rm', '--entrypoint', 'sh',
        '-v', "${Volume}:${ContainerCacheDirectory}",
        $TrivyImage,
        '-c', "mkdir -p '$ContainerCacheDirectory/tmp'"
    )
}

function New-TrivyCacheTmpDirectoryCleanupDockerArguments {
    param(
        [Parameter(Mandatory)][string]$Volume,
        [Parameter(Mandatory)][string]$TrivyImage,
        [Parameter(Mandatory)][string]$ContainerCacheDirectory
    )
    Assert-SafeTrivyCacheContainerDirectory -ContainerDirectory $ContainerCacheDirectory | Out-Null
    return @(
        'run', '--rm', '--entrypoint', 'sh',
        '-v', "${Volume}:${ContainerCacheDirectory}",
        $TrivyImage,
        '-c', "rm -rf '$ContainerCacheDirectory/tmp'"
    )
}

function Assert-KeycloakDockerfileLiteralBasePins {
    param(
        [Parameter(Mandatory)][string]$DockerfileText,
        [Parameter(Mandatory)][string]$ExpectedBaseReference
    )

    if ($ExpectedBaseReference -notmatch '^quay\.io/keycloak/keycloak:26\.7\.2@sha256:[0-9a-f]{64}$') {
        throw 'expected Keycloak base reference is not the reviewed literal digest pin'
    }
    $normalized = $DockerfileText.Replace("`r`n", "`n").Replace("`r", "`n")
    $instructionLines = @(
        $normalized -split "`n" |
            ForEach-Object { $_.TrimStart() }
    )
    $fromLines = @($instructionLines | Where-Object { $_ -match '(?i)^FROM[\t\v\f\r ]+' })
    if ($fromLines.Count -ne 2 -or
        $fromLines[0] -cne "FROM $ExpectedBaseReference AS builder" -or
        $fromLines[1] -cne "FROM $ExpectedBaseReference") {
        throw 'Keycloak Dockerfile must contain exactly two literal reviewed FROM digest lines'
    }
    if (@($instructionLines | Where-Object { $_ -match '(?i)^ARG[\t\v\f\r ]+[^\r\n]*(?:KEYCLOAK|BASE_IMAGE)' }).Count -gt 0 -or
        @($fromLines | Where-Object { $_ -match '\$' }).Count -gt 0) {
        throw 'Keycloak Dockerfile cannot expose an ARG or variable FROM base override'
    }
    return $true
}

function Get-TrivyFindingSummary {
    param([Parameter(Mandatory)]$Report)

    if ([string]$Report.SchemaVersion -ne '2') {
        throw 'unsupported Trivy JSON schema; expected SchemaVersion 2'
    }
    if ([string]$Report.Metadata.ImageID -notmatch '^sha256:[0-9a-f]{64}$') {
        throw 'Trivy report has no valid immutable Metadata.ImageID'
    }

    if ($null -eq $Report.PSObject.Properties['Results'] -or @($Report.Results).Count -eq 0) {
        throw 'Trivy report contains no scan results and cannot prove a clean image'
    }
    $findings = @()
    foreach ($result in @($Report.Results)) {
        if ($null -eq $result.PSObject.Properties['Vulnerabilities']) { continue }
        foreach ($vulnerability in @($result.Vulnerabilities)) {
            if ($null -eq $vulnerability) { continue }
            $severity = ([string]$vulnerability.Severity).ToUpperInvariant()
            if ($severity -notin @('HIGH', 'CRITICAL')) {
                throw "unexpected severity '$severity' in HIGH/CRITICAL-only Trivy report"
            }
            $status = ''
            if ($null -ne $vulnerability.PSObject.Properties['Status']) { $status = [string]$vulnerability.Status }
            $fixedVersion = ''
            if ($null -ne $vulnerability.PSObject.Properties['FixedVersion']) { $fixedVersion = [string]$vulnerability.FixedVersion }
            $findings += [pscustomobject]@{
                Target           = [string]$result.Target
                Class            = [string]$result.Class
                Type             = [string]$result.Type
                VulnerabilityID  = [string]$vulnerability.VulnerabilityID
                PackageName      = [string]$vulnerability.PkgName
                InstalledVersion = [string]$vulnerability.InstalledVersion
                FixedVersion     = $fixedVersion
                Severity         = $severity
                Status           = $status
            }
        }
    }

    return [pscustomobject]@{
        ImageID = [string]$Report.Metadata.ImageID
        High = @($findings | Where-Object Severity -eq 'HIGH').Count
        Critical = @($findings | Where-Object Severity -eq 'CRITICAL').Count
        Total = $findings.Count
        Findings = $findings
    }
}

function Assert-TrivyReportBinding {
    param(
        [Parameter(Mandatory)]$Report,
        [Parameter(Mandatory)][string]$ExpectedImageId
    )

    $summary = Get-TrivyFindingSummary -Report $Report
    if ($summary.ImageID -cne $ExpectedImageId) {
        throw "stale Trivy report: report ImageID $($summary.ImageID) does not equal $ExpectedImageId"
    }
    return $summary
}

function Assert-CycloneDxBinding {
    param(
        [Parameter(Mandatory)]$Bom,
        [Parameter(Mandatory)][string]$ExpectedImageId
    )

    if ([string]$Bom.bomFormat -cne 'CycloneDX') {
        throw 'SBOM is not CycloneDX'
    }
    if ([string]$Bom.specVersion -cne '1.7') {
        throw "CycloneDX spec version $($Bom.specVersion) is not the reviewed 1.7 format"
    }
    if ([string]$Bom.serialNumber -notmatch '^urn:uuid:[0-9a-fA-F-]{36}$') {
        throw 'CycloneDX SBOM has no valid serialNumber'
    }

    $ids = @(
        $Bom.metadata.component.properties |
            Where-Object { [string]$_.name -ceq 'aquasecurity:trivy:ImageID' } |
            ForEach-Object { [string]$_.value } |
            Sort-Object -Unique
    )
    if ($ids.Count -ne 1 -or $ids[0] -cne $ExpectedImageId) {
        throw "stale CycloneDX SBOM: embedded ImageID '$($ids -join ',')' does not equal $ExpectedImageId"
    }
    if ([string]$Bom.metadata.component.'bom-ref' -notmatch [regex]::Escape("@$ExpectedImageId")) {
        throw 'CycloneDX root component bom-ref is not bound to the expected image ID'
    }
    return $ids[0]
}

function Assert-PostgresGosuFindingScope {
    param(
        [Parameter(Mandatory)][string]$ImageReference,
        [Parameter(Mandatory)][string]$ImageId,
        [Parameter(Mandatory)]$Summary,
        [Parameter(Mandatory)][string]$ExpectedReference,
        [Parameter(Mandatory)][string]$ExpectedImageId
    )

    if ($ImageReference -cne $ExpectedReference -or $ImageId -cne $ExpectedImageId) {
        throw 'PostgreSQL exception is restricted to the exact reviewed reference and image ID'
    }
    if ($Summary.Total -lt 1) {
        throw 'PostgreSQL exception cannot be used when there are no findings to review'
    }
    $reviewedNoFixTuples = @()
    foreach ($finding in @($Summary.Findings)) {
        if (-not [string]::IsNullOrEmpty([string]$finding.FixedVersion)) {
            throw "PostgreSQL exception cannot cover fixable finding $($finding.VulnerabilityID)@$($finding.FixedVersion)"
        }
        if ([string]$finding.Status -cne 'affected') {
            throw "PostgreSQL exception cannot cover finding with unreviewed no-fix status '$($finding.Status)'"
        }
        $matchingTuple = @($reviewedNoFixTuples | Where-Object {
            [string]$_.Target -ceq [string]$finding.Target -and
            [string]$_.Class -ceq [string]$finding.Class -and
            [string]$_.Type -ceq [string]$finding.Type -and
            [string]$_.VulnerabilityID -ceq [string]$finding.VulnerabilityID -and
            [string]$_.PackageName -ceq [string]$finding.PackageName -and
            [string]$_.InstalledVersion -ceq [string]$finding.InstalledVersion -and
            [string]$_.FixedVersion -ceq [string]$finding.FixedVersion -and
            [string]$_.Severity -ceq [string]$finding.Severity -and
            [string]$_.Status -ceq [string]$finding.Status
        })
        if ($matchingTuple.Count -ne 1) {
            throw "PostgreSQL exception has no approved RC91 no-fix tuple for $($finding.Target):$($finding.VulnerabilityID)"
        }
    }
    return $true
}

function Assert-KeycloakVendorRejectedCveScope {
    param(
        [Parameter(Mandatory)][string]$ImageReference,
        [Parameter(Mandatory)][string]$ImageId,
        [Parameter(Mandatory)][string]$BaseReference,
        [Parameter(Mandatory)]$Summary,
        [Parameter(Mandatory)][string]$ExpectedBaseReference
    )

    if ($BaseReference -cne $ExpectedBaseReference -or
        $ImageId -notmatch '^sha256:[0-9a-f]{64}$' -or
        $Summary.ImageID -cne $ImageId) {
        throw 'Keycloak vendor-rejection exception is bound to the exact base and current derived image ID'
    }
    if ($Summary.Total -ne 1 -or $Summary.High -ne 1 -or $Summary.Critical -ne 0) {
        throw 'Keycloak vendor-rejection exception covers exactly one HIGH finding'
    }
    $finding = @($Summary.Findings)[0]
    $expectedTarget = "$ImageReference (redhat 9.8)"
    if ([string]$finding.Target -cne $expectedTarget -or
        [string]$finding.Class -cne 'os-pkgs' -or
        [string]$finding.Type -cne 'redhat' -or
        [string]$finding.VulnerabilityID -cne 'CVE-2026-22020' -or
        [string]$finding.PackageName -cne 'java-21-openjdk-headless' -or
        [string]$finding.InstalledVersion -cne '1:21.0.12.1.1-1.2.el9' -or
        -not [string]::IsNullOrEmpty([string]$finding.FixedVersion) -or
        [string]$finding.Severity -cne 'HIGH' -or
        [string]$finding.Status -cne 'affected') {
        throw 'Keycloak finding is outside the exact CVE/package/version/status vendor-rejection scope'
    }
    return $true
}

function Assert-ExceptionReviewContract {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Rationale,
        [Parameter(Mandatory)][AllowEmptyString()][string]$ReviewedAt,
        [Parameter(Mandatory)][AllowEmptyString()][string]$ReviewDueAt,
        [Parameter(Mandatory)][AllowEmptyString()][string]$CurrentTime
    )

    if ([string]::IsNullOrWhiteSpace($Rationale)) {
        throw 'exception review requires a non-empty rationale'
    }

    $format = "yyyy-MM-dd'T'HH:mm:ss'Z'"
    $styles = [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal
    $reviewed = [DateTimeOffset]::MinValue
    $due = [DateTimeOffset]::MinValue
    $current = [DateTimeOffset]::MinValue
    if (-not [DateTimeOffset]::TryParseExact($ReviewedAt, $format, [Globalization.CultureInfo]::InvariantCulture, $styles, [ref]$reviewed)) {
        throw 'exception review has an invalid reviewedAt timestamp'
    }
    if (-not [DateTimeOffset]::TryParseExact($ReviewDueAt, $format, [Globalization.CultureInfo]::InvariantCulture, $styles, [ref]$due)) {
        throw 'exception review has an invalid reviewDueAt timestamp'
    }
    if (-not [DateTimeOffset]::TryParseExact($CurrentTime, $format, [Globalization.CultureInfo]::InvariantCulture, $styles, [ref]$current)) {
        throw 'exception review has an invalid current timestamp'
    }
    if ($due -le $reviewed) {
        throw 'exception review due timestamp must be after reviewedAt'
    }
    if ($current -lt $reviewed) {
        throw 'exception review current timestamp is before reviewedAt'
    }
    if ($current -ge $due) {
        throw 'exception review has expired'
    }
    return $true
}

function Get-RequiredExactProperty {
    param(
        [Parameter(Mandatory)]$InputObject,
        [Parameter(Mandatory)][string]$PropertyName,
        [Parameter(Mandatory)][string]$Context
    )

    if ($null -eq $InputObject) {
        throw "strict transfer requires exact property $PropertyName on $Context"
    }
    $matchingProperties = @($InputObject.PSObject.Properties | Where-Object {
        [string]$_.Name -ceq $PropertyName
    })
    if ($matchingProperties.Count -ne 1) {
        throw "strict transfer requires exact property $PropertyName on $Context"
    }
    return $matchingProperties[0]
}

function Assert-ExactReleaseVulnerabilityPolicy {
    param([Parameter(Mandatory)]$Policy)

    $severitiesProperty = Get-RequiredExactProperty -InputObject $Policy -PropertyName 'trivySeverities' -Context 'manifest policy'
    if ($severitiesProperty.Value -isnot [System.Array] -or
        $severitiesProperty.Value.Count -ne 2 -or
        $severitiesProperty.Value[0] -isnot [string] -or
        $severitiesProperty.Value[1] -isnot [string] -or
        [string]$severitiesProperty.Value[0] -cne 'HIGH' -or
        [string]$severitiesProperty.Value[1] -cne 'CRITICAL') {
        throw 'release manifest policy.trivySeverities must be the exact string array HIGH,CRITICAL'
    }
    $ignoreUnfixedProperty = Get-RequiredExactProperty -InputObject $Policy -PropertyName 'ignoreUnfixed' -Context 'manifest policy'
    if ($ignoreUnfixedProperty.Value -isnot [bool] -or $ignoreUnfixedProperty.Value) {
        throw 'release manifest policy.ignoreUnfixed=false must be a Boolean'
    }
    $ignoredVulnerabilitiesProperty = Get-RequiredExactProperty -InputObject $Policy -PropertyName 'ignoredVulnerabilities' -Context 'manifest policy'
    if ($ignoredVulnerabilitiesProperty.Value -isnot [System.Array] -or
        $ignoredVulnerabilitiesProperty.Value.Count -ne 0) {
        throw 'release manifest policy.ignoredVulnerabilities must be an empty array'
    }
    $trivyExecutionProperty = Get-RequiredExactProperty -InputObject $Policy -PropertyName 'trivyExecution' -Context 'manifest policy'
    if ($trivyExecutionProperty.Value -isnot [string] -or
        [string]$trivyExecutionProperty.Value -cne 'serial') {
        throw 'release manifest policy.trivyExecution must be the exact string serial'
    }
    $staleImageArtifactsProperty = Get-RequiredExactProperty -InputObject $Policy -PropertyName 'staleImageArtifacts' -Context 'manifest policy'
    if ($staleImageArtifactsProperty.Value -isnot [string] -or
        [string]$staleImageArtifactsProperty.Value -cne 'fail') {
        throw 'release manifest policy.staleImageArtifacts must be the exact string fail'
    }
    $postgresExceptionProperty = Get-RequiredExactProperty -InputObject $Policy -PropertyName 'postgresException' -Context 'manifest policy'
    if ($null -ne $postgresExceptionProperty.Value) {
        throw 'release manifest policy.postgresException must be null'
    }
    return $true
}

function Assert-ExactReleaseManifestPolicy {
    param([Parameter(Mandatory)]$Manifest)

    $policyProperty = Get-RequiredExactProperty -InputObject $Manifest -PropertyName 'policy' -Context 'manifest'
    Assert-ExactReleaseVulnerabilityPolicy -Policy $policyProperty.Value | Out-Null
    return $true
}

function Assert-JsonHasNoDuplicateProperties {
    param([Parameter(Mandatory)][string]$JsonText)

    $document = $null
    try {
        $document = [System.Text.Json.JsonDocument]::Parse($JsonText)

        function Assert-JsonElementHasNoDuplicateProperties {
            param(
                [Parameter(Mandatory)][System.Text.Json.JsonElement]$Element,
                [Parameter(Mandatory)][string]$Path
            )

            switch ($Element.ValueKind) {
                ([System.Text.Json.JsonValueKind]::Object) {
                    $exactNames = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
                    $caseFoldedNames = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
                    foreach ($property in $Element.EnumerateObject()) {
                        if (-not $exactNames.Add($property.Name) -or
                            -not $caseFoldedNames.Add($property.Name)) {
                            throw "duplicate JSON property at ${Path}: $($property.Name)"
                        }
                        Assert-JsonElementHasNoDuplicateProperties -Element $property.Value -Path "${Path}.$($property.Name)"
                    }
                }
                ([System.Text.Json.JsonValueKind]::Array) {
                    $index = 0
                    foreach ($item in $Element.EnumerateArray()) {
                        Assert-JsonElementHasNoDuplicateProperties -Element $item -Path "${Path}[$index]"
                        $index++
                    }
                }
            }
        }

        Assert-JsonElementHasNoDuplicateProperties -Element $document.RootElement -Path '$'
    } catch [System.Text.Json.JsonException] {
        throw "release manifest is not valid strict JSON: $($_.Exception.Message)"
    } finally {
        if ($null -ne $document) { $document.Dispose() }
    }
    return $true
}

function Assert-ReleasePowerShellVersion {
    param([Parameter(Mandatory)][version]$Version)

    if ($Version -lt [version]'7.5.0') {
        throw "release tooling requires PowerShell 7.5 or newer for ConvertFrom-Json -DateKind String; got $Version"
    }
    return $true
}

function Assert-ReleasePowerShellRuntime {
    Assert-ReleasePowerShellVersion -Version $PSVersionTable.PSVersion | Out-Null
    return $true
}

function ConvertFrom-ReleaseJson {
    param([Parameter(Mandatory)][string]$JsonText)

    Assert-ReleasePowerShellRuntime | Out-Null
    Assert-JsonHasNoDuplicateProperties -JsonText $JsonText | Out-Null
    return $JsonText | ConvertFrom-Json -DateKind String -ErrorAction Stop
}

function Assert-FailedReleaseEvidenceAnchor {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$AnchorText,
        [Parameter(Mandatory)][string]$ReleaseName,
        [Parameter(Mandatory)][int[]]$AllowedExactNumbers,
        [Parameter(Mandatory)][string]$CandidateLabel
    )

    $allowedNumbers = [Collections.Generic.HashSet[int]]::new()
    foreach ($number in $AllowedExactNumbers) {
        if ($number -lt 1 -or -not $allowedNumbers.Add($number)) {
            throw "$CandidateLabel failure evidence has an invalid exact directory contract"
        }
    }
    $expectedDirectoryNames = @($AllowedExactNumbers | Sort-Object | ForEach-Object { "$ReleaseName-exact$_" })
    $releaseRoot = Join-Path $ProjectRoot 'release'
    if (-not (Test-Path -LiteralPath $releaseRoot -PathType Container)) {
        throw "$CandidateLabel failure evidence release root is missing"
    }
    $actualCandidateDirectories = @(Get-ChildItem -LiteralPath $releaseRoot -Directory -Force | Where-Object {
        $_.Name.StartsWith("$ReleaseName-exact", [StringComparison]::Ordinal)
    })
    if ($actualCandidateDirectories.Count -ne $expectedDirectoryNames.Count) {
        throw "$CandidateLabel failure evidence exact directory namespace drifted"
    }
    foreach ($directoryName in $expectedDirectoryNames) {
        $matches = @($actualCandidateDirectories | Where-Object { $_.Name -ceq $directoryName })
        if ($matches.Count -ne 1) {
            throw "$CandidateLabel failure evidence exact directory namespace drifted"
        }
        if (($matches[0].Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "$CandidateLabel failure evidence exact root cannot be a reparse point: $directoryName"
        }
        Assert-NoReleaseReparsePoints -ReleaseDirectory $matches[0].FullName | Out-Null
    }

    $anchoredFiles = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::Ordinal)
    $escapedReleaseName = [regex]::Escape($ReleaseName)
    $allowedExactPattern = ($AllowedExactNumbers | Sort-Object | ForEach-Object { [string]$_ }) -join '|'
    foreach ($line in @($AnchorText -split '\r?\n')) {
        if ([string]::IsNullOrWhiteSpace($line) -or $line.StartsWith('#', [StringComparison]::Ordinal)) { continue }
        $match = [regex]::Match($line, "^(?<hash>[0-9a-f]{64})  (?<path>release/$escapedReleaseName-exact(?:$allowedExactPattern)/[^\r\n]+)$")
        if (-not $match.Success -or
            -not $anchoredFiles.TryAdd($match.Groups['path'].Value, $match.Groups['hash'].Value)) {
            throw "$CandidateLabel failure evidence anchor has an invalid or duplicate entry: $line"
        }
    }
    if ($anchoredFiles.Count -eq 0) { throw "$CandidateLabel failure evidence anchor is empty" }

    $actualFiles = @()
    foreach ($directoryName in $expectedDirectoryNames) {
        $directory = Join-Path $ProjectRoot "release\$directoryName"
        $actualFiles += Get-ChildItem -LiteralPath $directory -File -Recurse -Force
    }
    if ($actualFiles.Count -ne $anchoredFiles.Count) {
        throw "$CandidateLabel failure evidence file set drifted from the tracked anchor"
    }
    foreach ($file in $actualFiles) {
        $relativePath = [IO.Path]::GetRelativePath($ProjectRoot, $file.FullName).Replace('\', '/')
        if (-not $anchoredFiles.ContainsKey($relativePath)) {
            throw "$CandidateLabel failure evidence contains an unanchored file: $relativePath"
        }
        $actualHash = Get-FileSha256Lower -Path $file.FullName
        if ($actualHash -cne $anchoredFiles[$relativePath]) {
            throw "$CandidateLabel failure evidence hash drifted: $relativePath"
        }
    }
    return $true
}

function Assert-RC49FailureEvidenceAnchor {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$AnchorText
    )
    return Assert-FailedReleaseEvidenceAnchor `
        -ProjectRoot $ProjectRoot `
        -AnchorText $AnchorText `
        -ReleaseName '0.1.0-rc49' `
        -AllowedExactNumbers @(1, 2, 3) `
        -CandidateLabel 'RC49'
}

function Assert-RC50FailureEvidenceAnchor {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$AnchorText
    )
    return Assert-FailedReleaseEvidenceAnchor `
        -ProjectRoot $ProjectRoot `
        -AnchorText $AnchorText `
        -ReleaseName '0.1.0-rc50' `
        -AllowedExactNumbers @(1) `
        -CandidateLabel 'RC50'
}

function Assert-RC51FailureEvidenceAnchor {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$AnchorText
    )
    return Assert-FailedReleaseEvidenceAnchor `
        -ProjectRoot $ProjectRoot `
        -AnchorText $AnchorText `
        -ReleaseName '0.1.0-rc51' `
        -AllowedExactNumbers @(1) `
        -CandidateLabel 'RC51'
}

function Assert-RC52FailureEvidenceAnchor {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$AnchorText
    )
    return Assert-FailedReleaseEvidenceAnchor `
        -ProjectRoot $ProjectRoot `
        -AnchorText $AnchorText `
        -ReleaseName '0.1.0-rc52' `
        -AllowedExactNumbers @(1) `
        -CandidateLabel 'RC52'
}

function Get-ReleaseGitProvenance {
    param([Parameter(Mandatory)][string]$RepositoryRoot)

    $headLines = @(& git -C $RepositoryRoot rev-parse --verify HEAD 2>$null)
    $headExitCode = $LASTEXITCODE
    $headOutput = ($headLines | Out-String).Trim()

    $statusLines = @(& git -C $RepositoryRoot status --porcelain=v1 2>$null)
    $statusExitCode = $LASTEXITCODE
    if ($statusExitCode -ne 0) {
        throw "git status failed with exit $statusExitCode while capturing release provenance"
    }

    $gitHead = $null
    if ($headExitCode -eq 0 -and $headOutput -match '^[0-9a-f]{40}$') {
        $gitHead = $headOutput
    }
    return [pscustomobject]@{
        GitHead = $gitHead
        GitDirty = [bool]($statusLines.Count -ne 0)
    }
}

function Get-ReleaseGateBlockedExitCode {
    param(
        [Parameter(Mandatory)]$ApplicationStatus,
        [Parameter(Mandatory)]$ProductionStatus,
        [Parameter(Mandatory)]$Reasons
    )

    if ($ApplicationStatus -is [string] -and
        [string]$ApplicationStatus -ceq 'passed' -and
        $ProductionStatus -is [string] -and
        [string]$ProductionStatus -ceq 'blocked' -and
        $Reasons -is [System.Array] -and
        $Reasons.Count -eq 1 -and
        $Reasons[0] -is [string] -and
        [string]$Reasons[0] -ceq 'idp_self_hosted_pending_canary') {
        return 42
    }
    return 1
}

function Assert-StrictReleaseDirectoryName {
    param(
        [Parameter(Mandatory)][string]$ReleaseDirectory,
        [Parameter(Mandatory)][string]$ExpectedReleaseName
    )

    $leafName = [IO.Path]::GetFileName([IO.Path]::TrimEndingDirectorySeparator($ReleaseDirectory))
    $expectedPattern = '^' + [regex]::Escape($ExpectedReleaseName) + '-exact(?:[1-9]|[1-9][0-9])$'
    if ($leafName -cnotmatch $expectedPattern) {
        throw "strict transfer release directory must be an exact $ExpectedReleaseName exactN leaf"
    }
    return $true
}

function Get-StrictSignedReleaseTagRef {
    param([Parameter(Mandatory)][string]$SignedReleaseTag)

    if ($SignedReleaseTag -cne 'v0.1.0-rc91-signed') {
        throw 'strict transfer requires the exact signed RC91 tag v0.1.0-rc91-signed'
    }
    return 'refs/tags/v0.1.0-rc91-signed'
}

function Assert-TransferReadyManifest {
    param(
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$ExpectedGitHead
    )

    if ($ExpectedGitHead -notmatch '^[0-9a-fA-F]{40}$') {
        throw 'signed tag commit is not a 40-hex Git commit'
    }
    try {
        $releaseNameProperty = Get-RequiredExactProperty -InputObject $Manifest -PropertyName 'releaseName' -Context 'manifest'
    } catch {
        throw 'strict transfer requires exact property releaseName; manifest releaseName=0.1.0-rc91 is mandatory'
    }
    if ($releaseNameProperty.Value -isnot [string] -or
        [string]$releaseNameProperty.Value -cne '0.1.0-rc91') {
        throw 'strict transfer requires manifest releaseName=0.1.0-rc91'
    }

    $expectedImageReferences = [ordered]@{
        api = 'invoice-system-api:0.1.0-rc91'
        'pdf-scanner' = 'invoice-system-pdf-scanner:0.1.0-rc91'
        tools = 'invoice-system-tools:0.1.0-rc91'
        web = 'invoice-system-web:0.1.0-rc91'
        'source-agent' = 'invoice-source-agent:0.1.0-rc91'
        'postgres-runtime' = 'invoice-postgres:0.1.0-rc91'
        'clamav-runtime' = 'invoice-clamav:0.1.0-rc91'
        'ingest-proxy' = 'invoice-ingest-proxy:0.1.0-rc91'
        keycloak = 'invoice-keycloak:0.1.0-rc91'
    }
    $imagesProperty = Get-RequiredExactProperty -InputObject $Manifest -PropertyName 'images' -Context 'manifest'
    if ($imagesProperty.Value -isnot [System.Array] -or
        $imagesProperty.Value.Count -ne $expectedImageReferences.Count) {
        throw 'strict transfer requires the exact RC91 image inventory'
    }
    foreach ($expectedImage in $expectedImageReferences.GetEnumerator()) {
        $matchingRecords = @()
        foreach ($imageRecord in $imagesProperty.Value) {
            $nameProperty = Get-RequiredExactProperty -InputObject $imageRecord -PropertyName 'name' -Context 'manifest image record'
            if ($nameProperty.Value -is [string] -and
                [string]$nameProperty.Value -ceq [string]$expectedImage.Key) {
                $matchingRecords += $imageRecord
            }
        }
        if ($matchingRecords.Count -ne 1) {
            throw 'strict transfer requires the exact RC91 image inventory'
        }
        $referenceProperty = Get-RequiredExactProperty -InputObject $matchingRecords[0] -PropertyName 'reference' -Context "manifest image $($expectedImage.Key)"
        if ($referenceProperty.Value -isnot [string] -or
            [string]$referenceProperty.Value -cne [string]$expectedImage.Value) {
            throw 'strict transfer requires the exact RC91 image inventory'
        }
    }

    $sourceProperty = Get-RequiredExactProperty -InputObject $Manifest -PropertyName 'source' -Context 'manifest'
    if ($null -eq $sourceProperty.Value) {
        throw 'strict transfer requires manifest source provenance'
    }
    $source = $sourceProperty.Value
    $gitDirtyProperty = Get-RequiredExactProperty -InputObject $source -PropertyName 'gitDirty' -Context 'manifest source'
    if ($gitDirtyProperty.Value -isnot [bool] -or $gitDirtyProperty.Value) {
        throw 'strict transfer requires source.gitDirty=false as a Boolean'
    }
    $gitHeadProperty = Get-RequiredExactProperty -InputObject $source -PropertyName 'gitHead' -Context 'manifest source'
    if ($gitHeadProperty.Value -isnot [string] -or
        [string]$gitHeadProperty.Value -notmatch '^[0-9a-fA-F]{40}$') {
        throw 'strict transfer requires source.gitHead to be a 40-hex string commit'
    }
    $gitHead = [string]$gitHeadProperty.Value
    if (-not [string]::Equals($gitHead, $ExpectedGitHead, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'strict transfer source.gitHead does not equal the supplied signed tag commit'
    }

    $decisionsProperty = Get-RequiredExactProperty -InputObject $Manifest -PropertyName 'decisions' -Context 'manifest'
    if ($null -eq $decisionsProperty.Value) {
        throw 'strict transfer requires manifest release decisions'
    }
    $decisions = $decisionsProperty.Value
    $applicationImageGateProperty = Get-RequiredExactProperty -InputObject $decisions -PropertyName 'applicationImageGate' -Context 'manifest decisions'
    if ($applicationImageGateProperty.Value -isnot [string] -or
        [string]$applicationImageGateProperty.Value -cne 'passed') {
        throw 'strict transfer requires applicationImageGate=passed as a string'
    }
    $productionLaunchProperty = Get-RequiredExactProperty -InputObject $decisions -PropertyName 'productionLaunch' -Context 'manifest decisions'
    if ($productionLaunchProperty.Value -isnot [string] -or
        [string]$productionLaunchProperty.Value -cne 'blocked') {
        throw 'strict transfer requires productionLaunch=blocked as a string pending the real canary'
    }
    $reasonsProperty = Get-RequiredExactProperty -InputObject $decisions -PropertyName 'reasons' -Context 'manifest decisions'
    if ($reasonsProperty.Value -isnot [System.Array] -or
        $reasonsProperty.Value.Count -ne 1 -or
        $reasonsProperty.Value[0] -isnot [string] -or
        $reasonsProperty.Value[0] -cne 'idp_self_hosted_pending_canary') {
        throw 'strict transfer requires the exact pending-canary reason idp_self_hosted_pending_canary and no others'
    }
    return $true
}

function Assert-GovulncheckBinaryProof {
    param(
        [Parameter(Mandatory)][string]$ProofText,
        [Parameter(Mandatory)][string]$VersionText
    )

    if ($VersionText -notmatch '(?m)^Scanner:\s+govulncheck@v1\.7\.0\s*$') {
        throw 'binary proof was not produced by exact govulncheck v1.7.0'
    }
    if ($VersionText -notmatch '(?m)^DB updated:\s+\S.+$') {
        throw 'govulncheck proof has no vulnerability database timestamp'
    }
    $dbMatch = [regex]::Match($VersionText, '(?m)^DB updated:\s+(.+?)\s*$')
    $dbUpdated = ConvertFrom-TrivyDatabaseTimestamp -Timestamp $dbMatch.Groups[1].Value
    $dbAge = [DateTimeOffset]::UtcNow - $dbUpdated.ToUniversalTime()
    # vuln.go.dev can legitimately retain the same authoritative timestamp
    # across a weekend. The command above still performs a live database read;
    # allow four days while retaining a strict upper bound and exact zero-called
    # vulnerability proof. Trivy's primary vulnerability DB remains at 48h.
    if ($dbAge -lt [TimeSpan]::FromMinutes(-5) -or $dbAge -gt [TimeSpan]::FromHours(96)) {
        throw 'govulncheck vulnerability database is outside the 96-hour release window'
    }
    if ($ProofText -notmatch '(?m)^No vulnerabilities found\.\s*$' -or
        $ProofText -notmatch '(?m)^Your code is affected by 0 vulnerabilities\.\s*$') {
        throw 'govulncheck binary proof does not establish zero called vulnerabilities'
    }
    return $true
}

function Get-IdpGateDecision {
    param(
        [Parameter(Mandatory)]
        [ValidateSet('keycloak', 'none', 'external-managed')]
        [string]$Mode,
        [bool]$KeycloakImageApproved = $false
    )

    switch ($Mode) {
        'keycloak' {
            if ($KeycloakImageApproved) {
                return [pscustomobject]@{
                    Status = 'image_approved_pending_canary'
                    ProductionCanary = 'pending'
                    BlockReason = 'idp_self_hosted_pending_canary'
                    Note = 'The exact self-hosted Keycloak image passed the image policy; production OIDC/MFA/RP-logout/back-channel-logout canary evidence is still pending.'
                }
            }
            return [pscustomobject]@{
                Status = 'image_rejected'
                ProductionCanary = 'not_satisfied'
                BlockReason = 'self_hosted_keycloak_image_failed'
                Note = 'The exact self-hosted Keycloak image did not pass the zero-HIGH/CRITICAL image policy.'
            }
        }
        'external-managed' {
            return [pscustomobject]@{
                Status = 'external_pending_canary'
                ProductionCanary = 'pending'
                BlockReason = 'idp_external_pending_canary'
                Note = 'No IdP image was silently skipped: the external managed IdP is explicitly pending a production OIDC/MFA/logout canary.'
            }
        }
        'none' {
            return [pscustomobject]@{
                Status = 'not_included'
                ProductionCanary = 'not_satisfied'
                BlockReason = 'idp_not_included'
                Note = 'No IdP is included; this is an application-image-only result and cannot approve production launch.'
            }
        }
    }
}

function Get-CommonReleaseImageTag {
    param(
        [Parameter(Mandatory)][object[]]$ImageRecords,
        [Parameter(Mandatory)][ValidateSet('keycloak', 'external-managed', 'none')][string]$IdPMode
    )

    $releaseRepositories = [ordered]@{
        api = 'invoice-system-api'
        'pdf-scanner' = 'invoice-system-pdf-scanner'
        tools = 'invoice-system-tools'
        web = 'invoice-system-web'
        'source-agent' = 'invoice-source-agent'
        'postgres-runtime' = 'invoice-postgres'
        'clamav-runtime' = 'invoice-clamav'
        'ingest-proxy' = 'invoice-ingest-proxy'
    }
    if ($IdPMode -ceq 'keycloak') { $releaseRepositories.keycloak = 'invoice-keycloak' }

    $releaseTags = @()
    foreach ($entry in $releaseRepositories.GetEnumerator()) {
        $record = @($ImageRecords | Where-Object { [string]$_.name -ceq [string]$entry.Key })
        if ($record.Count -ne 1 -or
            [string]$record[0].reference -notmatch "^$([regex]::Escape($entry.Value)):(?<tag>[0-9A-Za-z_][0-9A-Za-z_.-]{0,127})$") {
            throw "release manifest has an invalid local image reference for $($entry.Key)"
        }
        $releaseTags += $Matches.tag
    }
    $uniqueReleaseTags = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($releaseTag in $releaseTags) { $null = $uniqueReleaseTags.Add([string]$releaseTag) }
    if ($uniqueReleaseTags.Count -ne 1) {
        throw 'all locally built release images do not share one exact release image tag'
    }
    return [string]$releaseTags[0]
}

function Assert-LinuxAmd64Platform {
    param([Parameter(Mandatory)][string]$Platform)

    if ($Platform -cne 'linux/amd64') {
        throw "release image platform must be exactly linux/amd64, got $Platform"
    }
    return $true
}

function Test-OrdinalStringEqual {
    param(
        [AllowNull()]$Actual,
        [AllowNull()]$Expected
    )
    return [string]::Equals([string]$Actual, [string]$Expected, [StringComparison]::Ordinal)
}

function Get-FileSha256Lower {
    param([Parameter(Mandatory)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-ReleaseRelativePath {
    param(
        [Parameter(Mandatory)][string]$BasePath,
        [Parameter(Mandatory)][string]$Path
    )
    return ([IO.Path]::GetRelativePath($BasePath, $Path) -replace '\\', '/')
}

function Resolve-ReleaseArtifactPath {
    param(
        [Parameter(Mandatory)][string]$ReleaseDirectory,
        [Parameter(Mandatory)][string]$RelativePath
    )

    if ([string]::IsNullOrWhiteSpace($RelativePath) -or
        [IO.Path]::IsPathFullyQualified($RelativePath) -or
        $RelativePath.Contains('\') -or
        $RelativePath.Contains(':') -or
        ($RelativePath -split '/') -contains '..' -or
        ($RelativePath -split '/') -contains '.') {
        throw "unsafe release artifact path: $RelativePath"
    }
    $root = [IO.Path]::GetFullPath($ReleaseDirectory)
    $path = [IO.Path]::GetFullPath((Join-Path $root ($RelativePath -replace '/', [IO.Path]::DirectorySeparatorChar)))
    $prefix = $root.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "release artifact path escapes release directory: $RelativePath"
    }
    return $path
}

function Assert-NoReleaseReparsePoints {
    param([Parameter(Mandatory)][string]$ReleaseDirectory)

    $rootItem = Get-Item -LiteralPath $ReleaseDirectory -Force
    if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "release directory contains a symlink/reparse point: $($rootItem.FullName)"
    }
    $pendingDirectories = [Collections.Generic.Stack[IO.DirectoryInfo]]::new()
    $pendingDirectories.Push([IO.DirectoryInfo]$rootItem)
    while ($pendingDirectories.Count -gt 0) {
        $directory = $pendingDirectories.Pop()
        foreach ($child in Get-ChildItem -LiteralPath $directory.FullName -Force) {
            if (($child.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "release directory contains a symlink/reparse point: $($child.FullName)"
            }
            if ($child.PSIsContainer) {
                $pendingDirectories.Push([IO.DirectoryInfo]$child)
            }
        }
    }
    return $true
}

function Get-ContextFingerprint {
    param(
        [Parameter(Mandatory)][string]$Root,
        [string[]]$ExcludedDirectoryNames = @()
    )

    $fullRoot = [IO.Path]::GetFullPath($Root)
    $records = [Collections.Generic.List[string]]::new()
    foreach ($file in Get-ChildItem -LiteralPath $fullRoot -Recurse -File -Force) {
        $relative = Get-ReleaseRelativePath -BasePath $fullRoot -Path $file.FullName
        $segments = $relative -split '/'
        if (@($segments | Where-Object { $_ -in $ExcludedDirectoryNames }).Count -ne 0) { continue }
        if ($file.Name -like '*.log') { continue }
        $records.Add("$(Get-FileSha256Lower -Path $file.FullName)  $relative")
    }
    $canonical = (@($records | Sort-Object) -join "`n") + "`n"
    $bytes = [Text.Encoding]::UTF8.GetBytes($canonical)
    return ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes))).ToLowerInvariant()
}

function Write-Sha256Sums {
    param([Parameter(Mandatory)][string]$ReleaseDirectory)

    $fullRoot = [IO.Path]::GetFullPath($ReleaseDirectory)
    Assert-NoReleaseReparsePoints -ReleaseDirectory $fullRoot | Out-Null
    $sumPath = Join-Path $fullRoot 'SHA256SUMS'
    $lines = @(
        Get-ChildItem -LiteralPath $fullRoot -Recurse -File |
            Where-Object FullName -ne $sumPath |
            ForEach-Object {
                "$(Get-FileSha256Lower -Path $_.FullName)  $(Get-ReleaseRelativePath -BasePath $fullRoot -Path $_.FullName)"
            } |
            Sort-Object
    )
    [IO.File]::WriteAllText($sumPath, (($lines -join "`n") + "`n"), [Text.UTF8Encoding]::new($false))
    return $sumPath
}

function Assert-Sha256Sums {
    param([Parameter(Mandatory)][string]$ReleaseDirectory)

    $fullRoot = [IO.Path]::GetFullPath($ReleaseDirectory)
    Assert-NoReleaseReparsePoints -ReleaseDirectory $fullRoot | Out-Null
    $sumPath = Join-Path $fullRoot 'SHA256SUMS'
    if (-not (Test-Path -LiteralPath $sumPath -PathType Leaf)) {
        throw 'SHA256SUMS is missing'
    }
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($line in Get-Content -LiteralPath $sumPath) {
        if ($line -notmatch '^([0-9a-f]{64})  ([^\r\n]+)$') {
            throw "malformed SHA256SUMS line: $line"
        }
        $expected = $Matches[1]
        $relative = $Matches[2]
        if (-not $seen.Add($relative)) { throw "duplicate SHA256SUMS path: $relative" }
        $path = Resolve-ReleaseArtifactPath -ReleaseDirectory $fullRoot -RelativePath $relative
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "SHA256SUMS path is missing: $relative" }
        $actual = Get-FileSha256Lower -Path $path
        if ($actual -cne $expected) { throw "checksum mismatch for $relative" }
    }

    $actualFiles = @(
        Get-ChildItem -LiteralPath $fullRoot -Recurse -File |
            Where-Object FullName -ne $sumPath |
            ForEach-Object { Get-ReleaseRelativePath -BasePath $fullRoot -Path $_.FullName }
    )
    if ($seen.Count -ne $actualFiles.Count) {
        throw 'SHA256SUMS does not cover every generated release artifact'
    }
    foreach ($relative in $actualFiles) {
        if (-not $seen.Contains($relative)) { throw "uncovered release artifact: $relative" }
    }
    return $true
}

function Get-RequiredNonNegativeIntegralJsonNumber {
    param(
        [Parameter(Mandatory)]$JsonObject,
        [Parameter(Mandatory)][string]$PropertyName,
        [Parameter(Mandatory)][string]$RecordName
    )

    $property = $JsonObject.PSObject.Properties[$PropertyName]
    if ($null -eq $property -or $null -eq $property.Value) {
        throw "manifest vulnerability $PropertyName is missing or null for $RecordName"
    }
    if ($property.Value -isnot [long]) {
        throw "manifest vulnerability $PropertyName must be an integral JSON number for $RecordName"
    }
    if ($property.Value -lt 0) {
        throw "manifest vulnerability $PropertyName cannot be negative for $RecordName"
    }
    return $property.Value
}

function Assert-GeneratedArtifactBinding {
    param(
        [Parameter(Mandatory)][string]$ReleaseDirectory,
        [Parameter(Mandatory)]$ImageRecord
    )

    $reportPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $ReleaseDirectory -RelativePath ([string]$ImageRecord.vulnerabilityReport.path)
    $sbomPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $ReleaseDirectory -RelativePath ([string]$ImageRecord.sbom.path)
    if ((Get-FileSha256Lower -Path $reportPath) -cne [string]$ImageRecord.vulnerabilityReport.sha256) {
        throw "manifest vulnerability report hash is stale for $($ImageRecord.name)"
    }
    if ((Get-FileSha256Lower -Path $sbomPath) -cne [string]$ImageRecord.sbom.sha256) {
        throw "manifest SBOM hash is stale for $($ImageRecord.name)"
    }
    $report = ConvertFrom-ReleaseJson -JsonText (Get-Content -Raw -LiteralPath $reportPath)
    $bom = ConvertFrom-ReleaseJson -JsonText (Get-Content -Raw -LiteralPath $sbomPath)
    $summary = Assert-TrivyReportBinding -Report $report -ExpectedImageId ([string]$ImageRecord.imageId)
    Assert-CycloneDxBinding -Bom $bom -ExpectedImageId ([string]$ImageRecord.imageId) | Out-Null
    $vulnerabilitiesProperty = $ImageRecord.PSObject.Properties['vulnerabilities']
    if ($null -eq $vulnerabilitiesProperty -or $null -eq $vulnerabilitiesProperty.Value) {
        throw "manifest vulnerability counters are missing for $($ImageRecord.name)"
    }
    $vulnerabilities = $vulnerabilitiesProperty.Value
    $high = Get-RequiredNonNegativeIntegralJsonNumber -JsonObject $vulnerabilities -PropertyName 'high' -RecordName ([string]$ImageRecord.name)
    $critical = Get-RequiredNonNegativeIntegralJsonNumber -JsonObject $vulnerabilities -PropertyName 'critical' -RecordName ([string]$ImageRecord.name)
    $total = Get-RequiredNonNegativeIntegralJsonNumber -JsonObject $vulnerabilities -PropertyName 'total' -RecordName ([string]$ImageRecord.name)
    if ($summary.Total -ne ($summary.High + $summary.Critical) -or
        $total -ne ($high + $critical) -or
        $summary.Total -ne $total -or
        $summary.High -ne $high -or
        $summary.Critical -ne $critical) {
        throw "manifest vulnerability counts are stale for $($ImageRecord.name)"
    }
    return $true
}
