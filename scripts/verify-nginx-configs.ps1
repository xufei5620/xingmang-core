param(
    [string]$Image = '',
    [ValidatePattern('^(?:|sha256:[0-9a-f]{64})$')]
    [string]$ExpectedImageID = ''
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$nginxCompatibilityFixtureImage = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
if ([string]::IsNullOrWhiteSpace($Image)) { $Image = $nginxCompatibilityFixtureImage }
if (-not [string]::IsNullOrWhiteSpace($ExpectedImageID)) {
    $observedImageID = (& docker image inspect $Image --format '{{.Id}}' 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $observedImageID -cne $ExpectedImageID) {
        throw "Nginx configuration verifier image is unavailable or stale: $Image"
    }
}
$tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$tempRoot = Join-Path $tempParent ('invoice-nginx-verify-' + [Guid]::NewGuid().ToString('N'))
$pki = Join-Path $tempRoot 'pki'
[IO.Directory]::CreateDirectory($tempRoot) | Out-Null
try {
    Push-Location (Join-Path $projectRoot 'backend')
    try {
        go run ./cmd/mtlsgen --out-dir $pki --server-name invoice-ingest.internal --clients sub2api-agent,newapi-agent | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'failed to generate disposable Nginx test PKI' }
    } finally {
        Pop-Location
    }

    $security = '/test/invoice-security-headers.conf'
    $common = '/test/invoice-common-headers.conf'
    $cert = '/test/pki/ingest_server_cert.pem'
    $key = '/test/pki/ingest_server_key.pem'
    $ca = '/test/pki/source_agent_ca.pem'
    $invoice = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\invoice.solov.cc.conf.template')
    if ($invoice -match '/www/server/panel/vhost/nginx/invoice-security-headers\.conf') {
        throw 'invoice security snippet would be loaded globally by the BT vhost glob'
    }
    $invoice = $invoice.Replace('/www/server/panel/vhost/nginx/proxy/invoice-security-headers.conf', $security).
        Replace('/www/server/panel/vhost/nginx/proxy/invoice-common-headers.conf', $common).
        Replace('/www/server/panel/vhost/cert/invoice.solov.cc/fullchain.pem', $cert).
        Replace('/www/server/panel/vhost/cert/invoice.solov.cc/privkey.pem', $key).
        Replace('/www/wwwlogs/invoice.solov.cc.log', '/dev/null').
        Replace('/www/wwwlogs/invoice.solov.cc.error.log', '/dev/stderr')
    $auth = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\auth.solov.cc.conf.template')
    $auth = $auth.Replace('/www/server/panel/vhost/cert/auth.solov.cc/fullchain.pem', $cert).
        Replace('/www/server/panel/vhost/cert/auth.solov.cc/privkey.pem', $key).
        Replace('/www/wwwlogs/auth.solov.cc.error.log', '/dev/stderr').
        Replace('/www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf', '/test/auth-admin.solov.cc.allow.conf').
        Replace('/www/server/panel/vhost/nginx/proxy/keycloak-proxy-headers.conf', '/test/keycloak-proxy-headers.conf')
    $authAdmin = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\auth-admin.solov.cc.conf.template')
    $authAdmin = $authAdmin.Replace('/www/server/panel/vhost/cert/auth-admin.solov.cc/fullchain.pem', $cert).
        Replace('/www/server/panel/vhost/cert/auth-admin.solov.cc/privkey.pem', $key).
        Replace('/www/wwwlogs/auth-admin.solov.cc.error.log', '/dev/stderr').
        Replace('/www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf', '/test/auth-admin.solov.cc.allow.conf').
        Replace('/www/server/panel/vhost/nginx/proxy/keycloak-proxy-headers.conf', '/test/keycloak-proxy-headers.conf')
    $httpContext = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\invoice-http-context.conf')
    $full = "events {}`nhttp {`n$httpContext`n$invoice`n$auth`n$authAdmin`n}`n"
    [IO.File]::WriteAllText((Join-Path $tempRoot 'public.conf'), $full, [Text.UTF8Encoding]::new($false))
    [IO.File]::Copy((Join-Path $projectRoot 'deploy\nginx\invoice-security-headers.conf'), (Join-Path $tempRoot 'invoice-security-headers.conf'))
    [IO.File]::Copy((Join-Path $projectRoot 'deploy\nginx\invoice-common-headers.conf'), (Join-Path $tempRoot 'invoice-common-headers.conf'))
    [IO.File]::Copy((Join-Path $projectRoot 'deploy\nginx\keycloak-proxy-headers.conf'), (Join-Path $tempRoot 'keycloak-proxy-headers.conf'))
    [IO.File]::Copy((Join-Path $projectRoot 'deploy\nginx\auth-admin.solov.cc.allow.conf.example'), (Join-Path $tempRoot 'auth-admin.solov.cc.allow.conf'))

    $ingest = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\ingest-mtls.conf')
    $ingest = $ingest.Replace('/run/secrets/ingest_server_cert', $cert).
        Replace('/run/secrets/ingest_server_key', $key).
        Replace('/run/secrets/source_agent_ca', $ca).
        Replace('proxy_pass http://$ingest_upstream:8088;', 'proxy_pass http://127.0.0.1:8088;').
        Replace('resolver 127.0.0.11 valid=10s ipv6=off;', '')
    [IO.File]::WriteAllText((Join-Path $tempRoot 'ingest.conf'), $ingest, [Text.UTF8Encoding]::new($false))

    # XM-INV-INGEST-RESOLVER：入口代理必须在**请求时**解析 api，不能在 worker
    # 启动时解析一次就缓存。2026-09-06 事故：api 重启后换了地址，nginx 仍然把
    # 每一批源数据 POST 给旧地址，27 分钟 502。静态 proxy_pass 会让顺序成为唯一
    # 保障，而任何一次重启、重排或 IPAM 重分配都能再次踩中。
    $ingestRaw = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\ingest-mtls.conf')
    if ($ingestRaw -notmatch 'resolver\s+127\.0\.0\.11\s') {
        throw 'ingest-mtls.conf must declare Docker embedded DNS as a resolver (request-time upstream resolution)'
    }
    if ($ingestRaw -match 'proxy_pass\s+http://api:') {
        throw 'ingest-mtls.conf must not name the api upstream literally in proxy_pass: a literal is resolved once at worker start and cached'
    }
    if ($ingestRaw -notmatch 'proxy_pass\s+http://\$') {
        throw 'ingest-mtls.conf must proxy_pass through a variable so nginx resolves the upstream per request'
    }

    foreach ($config in @('public.conf', 'ingest.conf')) {
        docker run --rm --network none `
            --mount "type=bind,source=$tempRoot,target=/test,readonly" `
            $Image nginx -t -c "/test/$config"
        if ($LASTEXITCODE -ne 0) { throw "Nginx validation failed for $config" }
    }

    if ($invoice -notmatch 'documents/upload\$[\s\S]*proxy_request_buffering off' -or
        $invoice -notmatch '/document\$[\s\S]*proxy_buffering off') {
        throw 'PDF edge routes do not explicitly disable disk-capable buffering'
    }
    $readinessMatch = [regex]::Match(
        $invoice,
        '(?s)location = /readyz\s*\{(?<body>.*?)\r?\n\s*\}'
    )
    $readinessBody = $readinessMatch.Groups['body'].Value
    if (-not $readinessMatch.Success -or
        $readinessBody -notmatch 'proxy_pass http://127\.0\.0\.1:58088;' -or
        $readinessBody -notmatch 'access_log off;') {
        throw 'public readiness route is missing, not exact, or can be masked by the SPA fallback'
    }
    $backchannelMatch = [regex]::Match(
        $invoice,
        '(?s)location = /api/v1/auth/backchannel-logout\s*\{(?<body>.*?)\r?\n\s*\}'
    )
    $backchannelBody = $backchannelMatch.Groups['body'].Value
    if (-not $backchannelMatch.Success -or
        $httpContext -notmatch 'limit_req_zone \$binary_remote_addr zone=invoice_backchannel:10m rate=300r/m;' -or
        $backchannelBody -notmatch 'limit_req zone=invoice_backchannel burst=100 nodelay;' -or
        $backchannelBody -notmatch 'limit_except POST \{ deny all; \}' -or
        $backchannelBody -notmatch 'client_max_body_size 256k' -or
        $backchannelBody -notmatch 'access_log off' -or
        $backchannelBody -match 'limit_req zone=invoice_auth') {
        throw 'OIDC back-channel logout edge route is not isolated, burst-safe, POST-only, bounded and log-free'
    }
    if ($auth -notmatch 'location = /admin \{ return 404; \}' -or
        $auth -notmatch 'location \^~ /admin/ \{ return 404; \}' -or
        $auth -notmatch 'location \^~ /realms/master/[\s\S]*auth-admin\.solov\.cc\.allow\.conf' -or
        $auth -notmatch 'location / \{ return 404; \}') {
        throw 'public Keycloak vhost does not fail closed for administration paths'
    }
    if ($authAdmin -notmatch 'server_name auth-admin\.solov\.cc' -or
        $authAdmin -notmatch 'proxy_pass http://127\.0\.0\.1:58181' -or
        $authAdmin -notmatch 'auth-admin\.solov\.cc\.allow\.conf') {
        throw 'Keycloak administration vhost is not isolated on its loopback port and allowlist'
    }
    $proxyHeaders = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\keycloak-proxy-headers.conf')
    foreach ($requiredHeader in @(
        'proxy_set_header X-Forwarded-For $remote_addr;',
        'proxy_set_header X-Forwarded-Proto https;',
        'proxy_set_header X-Forwarded-Host $host;',
        'proxy_set_header X-Forwarded-Port 443;',
        'proxy_set_header Forwarded "";'
    )) {
        if (-not $proxyHeaders.Contains($requiredHeader)) {
            throw "Keycloak proxy header policy is missing: $requiredHeader"
        }
    }
    $allowRules = @(
        Get-Content -LiteralPath (Join-Path $projectRoot 'deploy\nginx\auth-admin.solov.cc.allow.conf.example') |
            Where-Object { $_ -match '^\s*allow\s+' }
    )
    if ($allowRules.Count -lt 2) { throw 'admin and break-glass example routes are both required' }
    foreach ($rule in $allowRules) {
        if ($rule -notmatch '^\s*allow\s+([^/;]+)/(32|128);\s*$') {
            throw "Keycloak admin allowlist contains a non-host route: $rule"
        }
        $address = [Net.IPAddress]::Parse($Matches[1])
        $expectedPrefix = if ($address.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork) { '32' } else { '128' }
        if ($Matches[2] -ne $expectedPrefix) {
            throw "Keycloak admin allowlist prefix is not exact for its address family: $rule"
        }
    }
    $allowlistText = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\nginx\auth-admin.solov.cc.allow.conf.example')
    if ($allowlistText -notmatch '(?m)^\s*deny all;\s*$') {
        throw 'Keycloak admin allowlist is missing deny all'
    }
} finally {
    $resolved = [IO.Path]::GetFullPath($tempRoot)
    if ($resolved.StartsWith($tempParent, [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $resolved) -like 'invoice-nginx-verify-*' -and
        [IO.Directory]::Exists($resolved)) {
        [IO.Directory]::Delete($resolved, $true)
    }
}

Write-Host 'Public, split-admin OIDC and mTLS Nginx configurations passed syntax and security gates.'
