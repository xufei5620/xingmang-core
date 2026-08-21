param(
    [string]$HostAlias = 'fiberstate',
    [int[]]$RequiredLoopbackPorts = @(58088, 58090, 58180, 58181),
    [string]$InvoiceEdgeSubnet = '172.30.239.0/28',
    [string]$InvoiceDBSubnet = '172.30.240.0/28',
    [string]$InvoiceAppSubnet = '172.30.241.0/28',
    [string]$ClamAVEgressSubnet = '172.30.242.0/28',
    [string]$OIDCPreflightEgressSubnet = '172.30.243.0/28',
    [string]$ProxySubnet = '172.30.250.0/28',
    [string]$ProxyGatewayIP = '172.30.250.1',
    [string]$TrustedProxyCIDR = '172.30.250.1/32',
    [string]$IngestSubnet = '172.30.251.0/28',
    [string]$IngestDynamicRange = '172.30.251.0/29',
    [string]$IngestProxyIP = '172.30.251.14',
    [string]$IngestProxyCIDR = '172.30.251.14/32',
    [string]$Sub2ProjectionSubnet = '172.30.252.0/28',
    [string]$NewAPIProjectionSubnet = '172.30.253.0/28',
    [string]$KeycloakDBSubnet = '172.30.244.0/28',
    [string]$KeycloakEdgeSubnet = '172.30.254.0/29',
    [string]$KeycloakEdgeGateway = '172.30.254.1',
    [string]$KeycloakProxyTrustedCIDR = '172.30.254.1/32',
    [string]$ExpectedSub2Version = '0.1.179',
    [string]$ExpectedNewAPIImageVersion = 'v1.0.0-rc.25'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot

function Get-IPv4CIDRBounds([string]$CIDR) {
    if ($CIDR -notmatch '^([^/]+)/(\d{1,2})$') { throw "invalid IPv4 CIDR: $CIDR" }
    $address = [System.Net.IPAddress]::Parse($Matches[1])
    $prefix = [int]$Matches[2]
    $bytes = $address.GetAddressBytes()
    if ($bytes.Count -ne 4 -or $prefix -lt 0 -or $prefix -gt 32) { throw "invalid IPv4 CIDR: $CIDR" }
    $value = [uint64]$bytes[0] * 16777216 + [uint64]$bytes[1] * 65536 + [uint64]$bytes[2] * 256 + [uint64]$bytes[3]
    $size = [uint64][Math]::Pow(2, 32 - $prefix)
    $start = [uint64]([Math]::Floor($value / $size) * $size)
    [pscustomobject]@{ Start = $start; End = $start + $size - 1; Prefix = $prefix }
}

function Get-IPv4Value([string]$Address) {
    $bytes = [System.Net.IPAddress]::Parse($Address).GetAddressBytes()
    if ($bytes.Count -ne 4) { throw "invalid IPv4 address: $Address" }
    return [uint64]$bytes[0] * 16777216 + [uint64]$bytes[1] * 65536 + [uint64]$bytes[2] * 256 + [uint64]$bytes[3]
}

$ingestBounds = Get-IPv4CIDRBounds $IngestSubnet
$proxyBounds = Get-IPv4CIDRBounds $ProxySubnet
$invoiceEdgeBounds = Get-IPv4CIDRBounds $InvoiceEdgeSubnet
$invoiceDBBounds = Get-IPv4CIDRBounds $InvoiceDBSubnet
$invoiceAppBounds = Get-IPv4CIDRBounds $InvoiceAppSubnet
$clamavEgressBounds = Get-IPv4CIDRBounds $ClamAVEgressSubnet
$oidcPreflightEgressBounds = Get-IPv4CIDRBounds $OIDCPreflightEgressSubnet
$dynamicBounds = Get-IPv4CIDRBounds $IngestDynamicRange
$sub2ProjectionBounds = Get-IPv4CIDRBounds $Sub2ProjectionSubnet
$newAPIProjectionBounds = Get-IPv4CIDRBounds $NewAPIProjectionSubnet
$keycloakDBBounds = Get-IPv4CIDRBounds $KeycloakDBSubnet
$keycloakEdgeBounds = Get-IPv4CIDRBounds $KeycloakEdgeSubnet
$proxyValue = Get-IPv4Value $IngestProxyIP
$invoiceProxyGatewayValue = Get-IPv4Value $ProxyGatewayIP
$keycloakGatewayValue = Get-IPv4Value $KeycloakEdgeGateway
if ($dynamicBounds.Start -lt $ingestBounds.Start -or $dynamicBounds.End -gt $ingestBounds.End) {
    throw 'ingestion dynamic range is outside the ingestion subnet'
}
if ($proxyValue -le $ingestBounds.Start -or $proxyValue -ge $ingestBounds.End) {
    throw 'ingest proxy IP is not a usable address inside the ingestion subnet'
}
if ($proxyValue -ge $dynamicBounds.Start -and $proxyValue -le $dynamicBounds.End) {
    throw 'ingest proxy IP overlaps the dynamic container range'
}
if ($IngestProxyCIDR -ne "$IngestProxyIP/32") {
    throw 'ingest proxy trust CIDR must be the exact proxy /32'
}
if ($invoiceProxyGatewayValue -le $proxyBounds.Start -or $invoiceProxyGatewayValue -ge $proxyBounds.End) {
    throw 'invoice proxy gateway is not a usable address inside the invoice proxy subnet'
}
if ($TrustedProxyCIDR -ne "$ProxyGatewayIP/32") {
    throw 'invoice API trusted proxy must be the exact invoice proxy gateway /32'
}
if ($keycloakGatewayValue -le $keycloakEdgeBounds.Start -or $keycloakGatewayValue -ge $keycloakEdgeBounds.End) {
    throw 'Keycloak edge gateway is not a usable address inside the Keycloak edge subnet'
}
if ($KeycloakProxyTrustedCIDR -ne "$KeycloakEdgeGateway/32") {
    throw 'Keycloak proxy trust must be the exact Keycloak edge gateway /32'
}
$plannedNetworks = @(
    [pscustomobject]@{ Label = 'invoice edge'; CIDR = $InvoiceEdgeSubnet; Bounds = $invoiceEdgeBounds; ExistingName = 'invoice-system-prod_invoice_edge'; Internal = $false },
    [pscustomobject]@{ Label = 'invoice database'; CIDR = $InvoiceDBSubnet; Bounds = $invoiceDBBounds; ExistingName = 'invoice-system-prod_invoice_db'; Internal = $true },
    [pscustomobject]@{ Label = 'invoice application'; CIDR = $InvoiceAppSubnet; Bounds = $invoiceAppBounds; ExistingName = 'invoice-system-prod_invoice_app'; Internal = $true },
    [pscustomobject]@{ Label = 'ClamAV egress'; CIDR = $ClamAVEgressSubnet; Bounds = $clamavEgressBounds; ExistingName = 'invoice-system-prod_clamav_egress'; Internal = $false },
    [pscustomobject]@{ Label = 'OIDC preflight egress'; CIDR = $OIDCPreflightEgressSubnet; Bounds = $oidcPreflightEgressBounds; ExistingName = 'invoice-system-prod_oidc_preflight_egress'; Internal = $false },
    [pscustomobject]@{ Label = 'Keycloak database'; CIDR = $KeycloakDBSubnet; Bounds = $keycloakDBBounds; ExistingName = 'invoice-keycloak-prod_keycloak_db'; Internal = $true },
    [pscustomobject]@{ Label = 'proxy'; CIDR = $ProxySubnet; Bounds = $proxyBounds; ExistingName = 'invoice-system-prod_invoice_proxy'; Internal = $false },
    [pscustomobject]@{ Label = 'ingest'; CIDR = $IngestSubnet; Bounds = $ingestBounds; ExistingName = 'invoice-system-ingest'; Internal = $true },
    [pscustomobject]@{ Label = 'sub2 projection'; CIDR = $Sub2ProjectionSubnet; Bounds = $sub2ProjectionBounds; ExistingName = 'invoice-sub2api-projection'; Internal = $true },
    [pscustomobject]@{ Label = 'New API projection'; CIDR = $NewAPIProjectionSubnet; Bounds = $newAPIProjectionBounds; ExistingName = 'invoice-newapi-projection'; Internal = $true },
    [pscustomobject]@{ Label = 'Keycloak edge'; CIDR = $KeycloakEdgeSubnet; Bounds = $keycloakEdgeBounds; ExistingName = 'invoice-keycloak-prod_keycloak_edge'; Internal = $false }
)
for ($left = 0; $left -lt $plannedNetworks.Count; $left++) {
    for ($right = $left + 1; $right -lt $plannedNetworks.Count; $right++) {
        $a = $plannedNetworks[$left]
        $b = $plannedNetworks[$right]
        if ($a.Bounds.Start -le $b.Bounds.End -and $b.Bounds.Start -le $a.Bounds.End) {
            throw "planned $($a.Label) and $($b.Label) subnets overlap"
        }
    }
}

& (Join-Path $PSScriptRoot 'check-upstream-integrity.ps1')
if ($LASTEXITCODE -ne 0) { throw 'upstream integrity gate failed' }

$containers = @(ssh -o BatchMode=yes -o ConnectTimeout=12 $HostAlias "docker ps --format '{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}'")
if ($LASTEXITCODE -ne 0) { throw "cannot inspect $HostAlias" }
foreach ($required in @('sub2api-mig', 'sub2api-mig-postgres', 'new-api', 'postgres')) {
    if (-not ($containers | Where-Object { $_ -like "$required|*" })) {
        throw "required current container is missing: $required"
    }
}

$cloudflare = Invoke-RestMethod -Uri 'https://api.cloudflare.com/client/v4/ips' -TimeoutSec 20
if (-not $cloudflare.success) { throw 'cannot retrieve the authoritative Cloudflare IP list' }
$expectedCloudflareCIDRs = @($cloudflare.result.ipv4_cidrs) + @($cloudflare.result.ipv6_cidrs) | Sort-Object -Unique
$realIPConfig = @(ssh -o BatchMode=yes $HostAlias "cat /www/server/panel/vhost/nginx/0.cloudflare.conf")
if ($LASTEXITCODE -ne 0) { throw 'cannot inspect host Cloudflare real-IP policy' }
$actualCloudflareCIDRs = @(
    $realIPConfig |
        ForEach-Object { if ($_ -match '^\s*set_real_ip_from\s+([^;]+);') { $Matches[1].Trim() } } |
        Where-Object { $_ } |
        Sort-Object -Unique
)
$cidrDifference = @(Compare-Object -ReferenceObject $expectedCloudflareCIDRs -DifferenceObject $actualCloudflareCIDRs)
if ($cidrDifference.Count -ne 0 -or
    -not ($realIPConfig -match '^\s*real_ip_header\s+CF-Connecting-IP;') -or
    -not ($realIPConfig -match '^\s*real_ip_recursive\s+on;')) {
    throw 'host Cloudflare real-IP policy is stale or incomplete'
}

$sub2Version = (ssh -o BatchMode=yes $HostAlias "docker exec sub2api-mig /app/sub2api -version" 2>&1 | Out-String).Trim()
$sub2VersionMatch = [regex]::Match($sub2Version, 'Sub2API\s+([0-9.]+)')
if ($LASTEXITCODE -ne 0 -or -not $sub2VersionMatch.Success) {
    throw 'cannot verify the running Sub2API binary version'
}
$observedSub2Version = $sub2VersionMatch.Groups[1].Value
if ($observedSub2Version -ne $ExpectedSub2Version) {
    throw "Sub2API runtime drift: expected $ExpectedSub2Version, got $observedSub2Version"
}
$newAPIContainer = $containers | Where-Object { $_ -like 'new-api|*' } | Select-Object -First 1
if (-not $newAPIContainer -or $newAPIContainer -notmatch [regex]::Escape($ExpectedNewAPIImageVersion)) {
    throw "New API image drift: expected $ExpectedNewAPIImageVersion"
}

$disk = @(ssh -o BatchMode=yes $HostAlias "df -P / /var/lib/docker")
if ($LASTEXITCODE -ne 0) { throw 'cannot inspect host disk capacity' }
$diskLines = @($disk | Select-Object -Skip 1)
foreach ($line in $diskLines) {
    $fields = @(($line -split '\s+') | Where-Object { $_ })
    if ($fields.Count -ge 5 -and [int]($fields[4].TrimEnd('%')) -ge 80) {
        throw "host filesystem is above 80% use: $line"
    }
}

$ntpSynchronized = (ssh -o BatchMode=yes $HostAlias "timedatectl show -p NTPSynchronized --value" | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $ntpSynchronized -ne 'yes') {
    throw 'host clock is not NTP synchronized; signed source/OIDC time checks would be unsafe'
}

$listeners = @(ssh -o BatchMode=yes $HostAlias "ss -H -ltn")
if ($LASTEXITCODE -ne 0) { throw 'cannot inspect host listening ports' }
foreach ($port in $RequiredLoopbackPorts) {
    if ($listeners | Where-Object { $_ -match "[:.]$port\s" }) {
        throw "planned loopback port is already in use: $port"
    }
}

$networks = @(ssh -o BatchMode=yes $HostAlias "docker network inspect --format '{{.Name}}|{{.Internal}}|{{range .IPAM.Config}}{{.Subnet}},{{end}}' `$(docker network ls -q)")
if ($LASTEXITCODE -ne 0) { throw 'cannot inspect Docker networks' }
foreach ($line in $networks) {
    $parts = $line -split '\|', 3
    if ($parts.Count -ne 3) { continue }
    $name = $parts[0]
    $isInternal = $parts[1] -eq 'true'
    foreach ($existingCIDR in ($parts[2] -split ',' | Where-Object { $_ })) {
        if ($existingCIDR -notmatch '^\d+\.\d+\.\d+\.\d+/\d+$') { continue }
        $existing = Get-IPv4CIDRBounds $existingCIDR
        foreach ($planned in $plannedNetworks) {
            $overlaps = $existing.Start -le $planned.Bounds.End -and $planned.Bounds.Start -le $existing.End
            if (-not $overlaps) { continue }
            $approvedExisting = $name -eq $planned.ExistingName -and
                $existingCIDR -eq $planned.CIDR -and
                $isInternal -eq $planned.Internal
            if (-not $approvedExisting) {
                throw "planned $($planned.Label) subnet overlaps Docker network $name ($existingCIDR)"
            }
        }
    }
}

Write-Host "Read-only production preflight passed for $HostAlias"
Write-Host "Running Sub2API: $sub2Version"
Write-Host 'Existing Docker network subnets (all planned ranges were checked for overlap and exact internal mode):'
$networks | ForEach-Object { Write-Host "  $_" }
Write-Host 'No production state was changed.'
