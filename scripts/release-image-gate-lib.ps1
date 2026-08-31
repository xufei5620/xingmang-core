Set-StrictMode -Version Latest

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
            throw "PostgreSQL exception has no approved RC49 no-fix tuple for $($finding.Target):$($finding.VulnerabilityID)"
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
    if ($current -gt $due) {
        throw 'exception review has expired'
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

    $reparsePoints = @(
        Get-ChildItem -LiteralPath $ReleaseDirectory -Recurse -Force |
            Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 }
    )
    if ($reparsePoints.Count -ne 0) {
        throw "release directory contains a symlink/reparse point: $($reparsePoints[0].FullName)"
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
    $report = Get-Content -Raw -LiteralPath $reportPath | ConvertFrom-Json
    $bom = Get-Content -Raw -LiteralPath $sbomPath | ConvertFrom-Json
    $summary = Assert-TrivyReportBinding -Report $report -ExpectedImageId ([string]$ImageRecord.imageId)
    Assert-CycloneDxBinding -Bom $bom -ExpectedImageId ([string]$ImageRecord.imageId) | Out-Null
    if ($summary.High -ne [int]$ImageRecord.vulnerabilities.high -or
        $summary.Critical -ne [int]$ImageRecord.vulnerabilities.critical) {
        throw "manifest vulnerability counts are stale for $($ImageRecord.name)"
    }
    return $true
}
