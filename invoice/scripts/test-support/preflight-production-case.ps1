param([Parameter(Mandatory)][string]$TargetScript, [Parameter(Mandatory)][string]$Case)
$ErrorActionPreference = 'Stop'
$global:PreflightFixtureCase = $Case
$global:PreflightFixtureCalls = 0
trap { Write-Host ('Fixture observed error: ' + $_.Exception.Message); exit 1 }

# These are the only external boundaries the copied production script uses.
# Unexpected requests stop here; no native SSH, Docker or HTTP is reachable.
function global:Invoke-RestMethod {
    param([string]$Uri, [int]$TimeoutSec)
    if ($Uri -ne 'https://api.cloudflare.com/client/v4/ips') { throw 'unexpected fixture HTTP request' }
    [pscustomobject]@{ success=$true; result=[pscustomobject]@{ ipv4_cidrs=@('203.0.113.0/24'); ipv6_cidrs=@() } }
}
function global:ssh {
    $command = [string]$args[-1]
    $global:PreflightFixtureCalls++
    $global:LASTEXITCODE = 0
    if ($command -like 'docker ps --format*') {
        $image = 'fixture/new-api:v1.0.0-rc.25'
        $status = 'Up'
        switch ($global:PreflightFixtureCase) {
            'version-prefix' { $image = 'fixture/new-api:v1.0.0-rc.250' }
            'version-wrong' { $image = 'fixture/new-api:v0.0.0' }
            'version-digest' { $image += '@sha256:' + ('a' * 64) }
            'version-registry-port' { $image = 'registry.example:5000/new-api:v1.0.0-rc.25' }
            'version-status' { $image = 'fixture/new-api:v0.0.0'; $status = 'Up v1.0.0-rc.25' }
            'version-no-tag' { $image = 'fixture/v1.0.0-rc.25' }
            'version-case' { $image = 'fixture/new-api:V1.0.0-RC.25' }
            'version-bad-digest' { $image += '@sha256:invalid' }
        }
        return @('sub2api-mig|fixture/sub2api:0.1.179|Up|', 'sub2api-mig-postgres|fixture/postgres:18|Up|',
            ("new-api|$image|$status|"), 'postgres|fixture/postgres:18|Up|')
    }
    if ($command -eq 'cat /www/server/panel/vhost/nginx/0.cloudflare.conf') {
        return @('set_real_ip_from 203.0.113.0/24;', 'real_ip_header CF-Connecting-IP;', 'real_ip_recursive on;')
    }
    if ($command -eq 'docker exec sub2api-mig /app/sub2api -version') { return 'Sub2API 0.1.179' }
    if ($command -in @('df -P / /var/lib/docker', 'LC_ALL=C df -P / /var/lib/docker')) {
        $header = 'Filesystem 1024-blocks Used Available Capacity Mounted on'
        $row = '/dev/fixture 100000 1000 99000 1% /'
        switch ($global:PreflightFixtureCase) {
            'df-empty' { return @() }
            'df-one-row' { return @($header, $row) }
            'df-header' { return @('unrecognized header', $row, $row) }
            'df-malformed' { return @($header, 'unparseable-row', $row) }
            'df-number' { return @($header, '/dev/fixture invalid 1000 99000 1% /', $row) }
            'df-percentage' { return @($header, '/dev/fixture 100000 1000 99000 1 /', $row) }
            'df-mount' { return @($header, '/dev/fixture 100000 1000 99000 1% relative', $row) }
            'df-full' { return @($header, '/dev/fixture 100000 80000 20000 80% /', $row) }
            'df-79' { return @($header, '/dev/fixture 100000 79000 21000 79% /', $row) }
            default { return @($header, $row, $row) }
        }
    }
    if ($command -eq 'timedatectl show -p NTPSynchronized --value') { return 'yes' }
    if ($command -eq 'ss -H -ltn') { return @() }
    if ($command -like 'docker network inspect --format*') {
        switch ($global:PreflightFixtureCase) {
            'net-empty' { return @() }
            'net-malformed' { return 'unparseable-network-row' }
            'net-name' { return '|false|172.17.0.0/16,' }
            'net-internal' { return 'bridge|unknown|172.17.0.0/16,' }
            'net-cidr' { return 'bridge|false|not-a-cidr,' }
            'net-prefix' { return 'bridge|false|172.17.0.0/33,' }
            'net-ipv6-prefix' { return 'ipv6-only|false|fd00::/129,' }
            'net-empty-entry' { return 'bridge|false|172.17.0.0/16,,' }
            'net-overlap' { return 'unexpected|false|172.30.239.0/28,' }
            'net-builtins' { return @('bridge|false|172.17.0.0/16,', 'host|false|', 'none|false|') }
            'net-ipv6' { return @('ipv6-only|false|fd00:1234::/64,', 'dual|false|172.17.0.0/16,fd00:abcd::/64,') }
            'net-approved' { return 'invoice-system-prod_invoice_db|true|172.30.240.0/28,' }
            default { return 'bridge|false|172.17.0.0/16,' }
        }
    }
    throw 'Unexpected mocked SSH request; no native command can be reached.'
}
& $TargetScript -HostAlias fixture-host
Write-Host ('Mock external requests=' + $global:PreflightFixtureCalls)
exit 0
