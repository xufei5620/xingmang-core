param(
    [Parameter(Mandatory)]
    [string]$KeycloakImage,
    [Parameter(Mandatory)]
    [ValidatePattern('^sha256:[0-9a-f]{64}$')]
    [string]$ExpectedKeycloakImageID
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$provisioner = Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh'
$postgresImage = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$toolsImage = 'alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b'
$expectedOutput = '{"status":"ok","realm":"solov","clients":4,"desktop_enabled":false}'
$suffix = [Guid]::NewGuid().ToString('N').Substring(0, 12)
$network = "invoice-kc-provision-net-$suffix"
$database = "invoice-kc-provision-db-$suffix"
$keycloak = "invoice-kc-provision-app-$suffix"
$tools = "invoice-kc-provision-tools-$suffix"
$secretVolume = "invoice-kc-provision-secret-$suffix"
$provisionToolsImage = "invoice-keycloak-provision-tools-test:$suffix"
$bootstrapUser = 'invoice-bootstrap-admin'
$bootstrapPassword = 'Fixture+Bootstrap/Password=2026_Only'
$databaseCreated = $false
$keycloakCreated = $false
$toolsCreated = $false
$networkCreated = $false
$volumeCreated = $false
$provisionToolsImageCreated = $false

function Invoke-Docker {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed`n$($output | Out-String)"
    }
    return @($output)
}

function Wait-Until {
    param(
        [scriptblock]$Condition,
        [int]$Seconds,
        [string]$Failure
    )
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 1000
    } while ([DateTime]::UtcNow -lt $deadline)
    throw $Failure
}

function Initialize-AdminToken {
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $request = @'
set -eu
tmp=/run/test-secrets/admin_token.tmp
header_tmp=/run/test-secrets/admin_auth_header.tmp
password_request=/run/test-secrets/admin_password_request.tmp
trap 'rm -f "$tmp" "$header_tmp" "$password_request"' EXIT HUP INT TERM
tr -d '\r\n' </run/test-secrets/bootstrap_password >"$password_request"
chmod 0400 "$password_request"
curl --fail --silent --show-error --request POST \
  --max-filesize 1048576 --max-time 10 --proto '=http' \
  --data-urlencode grant_type=password \
  --data-urlencode client_id=admin-cli \
  --data-urlencode "username=$BOOTSTRAP_USER" \
  --data-urlencode "password@$password_request" \
  http://keycloak:8080/realms/master/protocol/openid-connect/token \
  | jq -er .access_token >"$tmp"
test -s "$tmp"
test "$(wc -l <"$tmp")" -eq 1
token=$(cat "$tmp")
test "${#token}" -ge 64
test "${#token}" -le 262144
printf '%s' "$token" | grep -Eq '^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$'
printf 'Authorization: Bearer %s\n' "$token" >"$header_tmp"
unset token
chmod 0400 "$header_tmp"
mv -f "$header_tmp" /run/test-secrets/admin_auth_header
rm -f "$tmp"
rm -f "$password_request"
trap - EXIT HUP INT TERM
'@
    do {
        & docker exec --env "BOOTSTRAP_USER=$bootstrapUser" $tools sh -ec $request 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) { return }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'disposable Keycloak did not issue an admin token'
}

function Invoke-AdminGet {
    param([string]$Path)
    if ($Path -notmatch '^solov(?:[/?][A-Za-z0-9._~%?&=:+-]+)*$' -or
        $Path.Contains('..') -or $Path -match '(?i)%2f|%5c') {
        throw 'unsafe disposable Keycloak Admin REST path'
    }
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $request = @'
set -eu
curl --header @/run/test-secrets/admin_auth_header \
  --fail --silent --show-error --max-filesize 1048576 --max-time 10 --proto '=http' \
  "http://keycloak:8080/admin/realms/${ADMIN_PATH}"
'@
    do {
        $output = @(& docker exec --env "ADMIN_PATH=$Path" $tools sh -ec $request 2>$null)
        $exitCode = $LASTEXITCODE
        if ($exitCode -eq 0) {
            try {
                $jsonText = ($output | Out-String).Trim()
                if (-not ($jsonText.StartsWith('{', [StringComparison]::Ordinal) -or
                    $jsonText.StartsWith('[', [StringComparison]::Ordinal))) {
                    throw 'Admin GET JSON root is not an object or array'
                }
                return $jsonText | ConvertFrom-Json
            } catch {
                throw "Admin GET returned invalid JSON for $Path"
            }
        }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Admin GET failed for $Path"
}

function Invoke-AdminGetArray {
    param([string]$Path)
    $value = Invoke-AdminGet -Path $Path
    foreach ($item in @($value)) { Write-Output $item }
}

if (-not (Test-Path -LiteralPath $provisioner -PathType Leaf)) { throw 'Keycloak provisioner is missing' }
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Docker is required for the Keycloak provisioning integration test' }

$source = Get-Content -Raw -LiteralPath $provisioner
if ([regex]::Matches($source, 'admin_request\s+GET\s+"[^"\r\n]*client-secret').Count -ne 1 -or
    $source -notmatch 'clients/\$invoice_client_id/client-secret' -or
    $source -match 'clients/\$(?:sub2api|newapi)_client_id/client-secret' -or
    $source -notmatch '--data-urlencode\s+"password@\$BOOTSTRAP_PASSWORD_REQUEST_FILE"' -or
    $source -notmatch 'tr -d ''\\r\\n'' <"\$BOOTSTRAP_PASSWORD_FILE"' -or
    $source -notmatch 'mktemp -d /dev/shm/' -or
    $source -notmatch 'trap cleanup EXIT HUP INT TERM') {
    throw 'provisioner secret/token transport invariant drifted'
}

try {
    Invoke-Docker image inspect $KeycloakImage | Out-Null
    $observedKeycloakImageID = (Invoke-Docker image inspect $KeycloakImage --format '{{.Id}}' | Select-Object -First 1).ToString().Trim()
    if ($observedKeycloakImageID -cne $ExpectedKeycloakImageID) {
        throw 'Keycloak provisioning test image does not equal the release manifest image ID'
    }
    Invoke-Docker image inspect $postgresImage | Out-Null
    Invoke-Docker image inspect $toolsImage | Out-Null

    $toolsDockerfile = @"
FROM $toolsImage
RUN ok=0; for attempt in 1 2 3; do if timeout 120 apk add --no-cache bash curl jq openssl coreutils; then ok=1; break; fi; sleep 2; done; test "`$ok" = 1
"@
    $savedErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $buildOutput = $toolsDockerfile | & docker build --tag $provisionToolsImage - 2>&1
        $buildExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $savedErrorActionPreference
    }
    if ($buildExitCode -ne 0) { throw "cannot build disposable provisioning tools image`n$($buildOutput | Out-String)" }
    $provisionToolsImageCreated = $true

    # This is a disposable fixture containing only fixed test credentials. A
    # normal named bridge is required so the tools container can install its
    # pinned test-time packages; no database or management port is published.
    Invoke-Docker network create $network | Out-Null
    $networkCreated = $true
    Invoke-Docker volume create $secretVolume | Out-Null
    $volumeCreated = $true

    Invoke-Docker run --detach --name $database --network $network --network-alias postgres `
        --env POSTGRES_DB=keycloak --env POSTGRES_USER=keycloak_app `
        --env POSTGRES_HOST_AUTH_METHOD=trust $postgresImage | Out-Null
    $databaseCreated = $true
    Wait-Until -Seconds 60 -Failure 'disposable Keycloak PostgreSQL did not become ready' -Condition {
        & docker exec $database pg_isready -U keycloak_app -d keycloak 2>&1 | Out-Null
        return $LASTEXITCODE -eq 0
    }

    Invoke-Docker run --detach --name $keycloak --network $network --network-alias keycloak `
        --entrypoint /opt/keycloak/bin/kc.sh `
        --env 'KC_DB=postgres' `
        --env 'KC_DB_URL=jdbc:postgresql://postgres:5432/keycloak' `
        --env 'KC_DB_USERNAME=keycloak_app' --env 'KC_DB_PASSWORD=fixture-db-password' `
        --env 'KC_HOSTNAME=http://keycloak:8080' --env 'KC_HOSTNAME_ADMIN=http://keycloak:8080' `
        --env 'KC_HOSTNAME_STRICT=true' --env 'KC_HTTP_ENABLED=true' `
        --env 'KC_HEALTH_ENABLED=true' --env "KC_BOOTSTRAP_ADMIN_USERNAME=$bootstrapUser" `
        --env "KC_BOOTSTRAP_ADMIN_PASSWORD=$bootstrapPassword" `
        $KeycloakImage start --optimized | Out-Null
    $keycloakCreated = $true

    # The image above exists only for this disposable test and is removed in
    # finally. The production host provisioner remains a plain host script.
    Invoke-Docker run --detach --name $tools --network $network --volume "${projectRoot}:/workspace:ro" --volume "${secretVolume}:/out" `
        $provisionToolsImage sh -c 'mkdir -p /run/test-secrets && chmod 0700 /run/test-secrets /out && printf "%s\n" Fixture+Bootstrap/Password=2026_Only >/run/test-secrets/bootstrap_password && chmod 0400 /run/test-secrets/bootstrap_password && touch /run/tools-ready && exec sleep 600' | Out-Null
    $toolsCreated = $true
    Wait-Until -Seconds 120 -Failure 'disposable provisioning tools did not become ready' -Condition {
        & docker exec $tools test -f /run/tools-ready 2>&1 | Out-Null
        return $LASTEXITCODE -eq 0
    }
    $discoveryProbe = @'
curl --fail --silent --show-error \
  http://keycloak:8080/realms/master/.well-known/openid-configuration \
  | jq -e '.token_endpoint | type == "string" and length > 0'
'@
    Wait-Until -Seconds 180 -Failure 'disposable Keycloak 26.7.2 did not become ready' -Condition {
        & docker exec $tools sh -ec $discoveryProbe 2>&1 | Out-Null
        return $LASTEXITCODE -eq 0
    }
    $provisionOutput = & docker exec `
        --env 'KEYCLOAK_PROVISION_TEST_MODE=1' `
        --env 'KEYCLOAK_ADMIN_BASE_URL=http://keycloak:8080' `
        --env 'KEYCLOAK_PUBLIC_HOST=keycloak:8080' `
        --env 'KEYCLOAK_ADMIN_HOST=keycloak:8080' `
        --env "KEYCLOAK_BOOTSTRAP_USERNAME=$bootstrapUser" `
        --env 'KEYCLOAK_BOOTSTRAP_PASSWORD_FILE=/run/test-secrets/bootstrap_password' `
        --env 'INVOICE_OIDC_CLIENT_SECRET_FILE=/out/invoice_oidc_client_secret' `
        --env 'INVOICE_OIDC_CLIENT_SECRET_UID=10001' `
        --env 'INVOICE_OIDC_CLIENT_SECRET_GID=10001' `
        $tools bash /workspace/deploy/keycloak/provision-solov-realm.sh 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Keycloak provisioner failed`n$($provisionOutput | Out-String)" }
    $provisionLines = @($provisionOutput | ForEach-Object { $_.ToString().Trim() } | Where-Object { $_ -ne '' })
    if ($provisionLines.Count -ne 1 -or $provisionLines[0] -ne $expectedOutput) {
        throw "provisioner stdout exceeded the fixed allowlist: $($provisionLines -join ' | ')"
    }

    $secretStat = (Invoke-Docker exec $tools stat -c '%u:%g:%a:%s' /out/invoice_oidc_client_secret | Select-Object -First 1).ToString().Trim()
    if ($secretStat -notmatch '^10001:10001:400:(\d+)$' -or [int]$Matches[1] -lt 32 -or [int]$Matches[1] -gt 256) {
        throw "published invoice-web fixture secret permissions/size are unsafe: $secretStat"
    }

    Initialize-AdminToken
    $realm = Invoke-AdminGet -Path 'solov'
    if ($realm.realm -ne 'solov' -or -not $realm.enabled -or $realm.browserFlow -ne 'solov-browser-step-up' -or
        $realm.registrationAllowed -ne $false -or $realm.verifyEmail -ne $false -or
        $realm.attributes.'acr.loa.map' -ne '{"urn:solov:loa:2":2}' -or
        $realm.accessTokenLifespan -ne 300 -or -not $realm.bruteForceProtected) {
        throw 'provisioned realm baseline/LoA policy is incomplete'
    }

    $clients = @{}
    foreach ($clientId in @('invoice-web', 'sub2api', 'newapi', 'invoice-desktop')) {
        $matches = @(Invoke-AdminGetArray -Path "solov/clients?clientId=$clientId&search=true&first=0&max=2")
        $matches = @($matches | Where-Object { $_.clientId -eq $clientId })
        if ($matches.Count -ne 1) { throw "client lookup is not unique after provisioning: $clientId" }
        $clients[$clientId] = Invoke-AdminGet -Path "solov/clients/$($matches[0].id)"
    }
    if (@($clients['invoice-web'].redirectUris).Count -ne 1 -or $clients['invoice-web'].redirectUris[0] -ne 'https://invoice.solov.cc/api/v1/auth/callback' -or
        $clients['invoice-web'].attributes.'backchannel.logout.session.required' -ne 'true' -or
        $clients['sub2api'].attributes.'pkce.code.challenge.method' -ne 'S256' -or
        $clients['newapi'].attributes.PSObject.Properties.Name -contains 'pkce.code.challenge.method' -or
        $clients['invoice-desktop'].enabled -ne $false -or $clients['invoice-desktop'].publicClient -ne $true -or
        @($clients['invoice-desktop'].redirectUris).Count -ne 0) {
        throw 'provisioned client redirect/PKCE/logout contract is incomplete'
    }

    foreach ($clientId in $clients.Keys) {
        $clientUuid = $clients[$clientId].id
        $defaults = @(Invoke-AdminGetArray -Path "solov/clients/$clientUuid/default-client-scopes")
        $defaultNames = @($defaults | ForEach-Object { $_.name } | Sort-Object)
        $expectedDefaultNames = if ($clientId -eq 'invoice-web') {
            'basic,email,profile,solov-token-contract'
        } else {
            'email,profile,solov-token-contract'
        }
        if (($defaultNames -join ',') -ne $expectedDefaultNames) {
            throw "client has an unexpected default/offline scope set: $clientId"
        }
        $optional = @(Invoke-AdminGetArray -Path "solov/clients/$clientUuid/optional-client-scopes")
        $optionalNames = @($optional | ForEach-Object { $_.name } | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        if ($optionalNames.Count -ne 0) { throw "client retains optional/offline scopes: $clientId" }
        $scopeRoles = @(Invoke-AdminGetArray -Path "solov/clients/$clientUuid/scope-mappings/realm")
        $roleNames = @($scopeRoles | ForEach-Object { $_.name } | Sort-Object)
        $expectedRoles = if ($clientId -eq 'invoice-web') { 'invoice-admin,invoice-user' } else { 'invoice-user' }
        if (($roleNames -join ',') -ne $expectedRoles) { throw "client role scope is not least privilege: $clientId" }
    }

    $scopes = @(Invoke-AdminGetArray -Path 'solov/client-scopes')
    $basicScopes = @($scopes | Where-Object { $_.name -eq 'basic' })
    if ($basicScopes.Count -ne 1) { throw 'built-in basic scope lookup is not unique' }
    $basicMappers = @(Invoke-AdminGetArray -Path "solov/client-scopes/$($basicScopes[0].id)/protocol-mappers/models")
    $authTimeMappers = @($basicMappers | Where-Object { $_.name -eq 'auth_time' -and $_.protocolMapper -eq 'oidc-usersessionmodel-note-mapper' })
    if ($authTimeMappers.Count -ne 1 -or
        $authTimeMappers[0].config.'user.session.note' -ne 'AUTH_TIME' -or
        $authTimeMappers[0].config.'claim.name' -ne 'auth_time' -or
        $authTimeMappers[0].config.'jsonType.label' -ne 'long' -or
        $authTimeMappers[0].config.'id.token.claim' -ne 'true') {
        throw 'built-in basic scope does not provide the required ID-token auth_time contract'
    }
    $contractScopes = @($scopes | Where-Object { $_.name -eq 'solov-token-contract' })
    if ($contractScopes.Count -ne 1) { throw 'token contract scope lookup is not unique' }
    $mappers = @(Invoke-AdminGetArray -Path "solov/client-scopes/$($contractScopes[0].id)/protocol-mappers/models")
    $mapperIds = @($mappers | ForEach-Object { $_.protocolMapper } | Sort-Object)
    if (($mapperIds -join ',') -ne 'oidc-acr-mapper,oidc-amr-mapper,oidc-usermodel-realm-role-mapper') {
        throw 'ACR/AMR/roles protocol mapper set is incomplete'
    }
    $rolesMapper = @($mappers | Where-Object { $_.protocolMapper -eq 'oidc-usermodel-realm-role-mapper' })
    if ($rolesMapper.Count -ne 1 -or $rolesMapper[0].config.'claim.name' -ne 'roles' -or $rolesMapper[0].config.multivalued -ne 'true') {
        throw 'top-level string-array roles mapper is incomplete'
    }

    foreach ($flowAlias in @('solov-browser-step-up', 'solov-auth', 'solov-loa1', 'solov-loa2')) {
        $flowExecutions = @(Invoke-AdminGetArray -Path "solov/authentication/flows/$flowAlias/executions")
        switch ($flowAlias) {
            'solov-browser-step-up' {
                $direct = @($flowExecutions | Where-Object { $_.level -eq 0 })
                if ($direct.Count -ne 2 -or @($direct | Where-Object { $_.providerId -eq 'auth-cookie' -and $_.requirement -eq 'ALTERNATIVE' }).Count -ne 1) { throw 'top browser flow is invalid' }
            }
            'solov-auth' {
                $direct = @($flowExecutions | Where-Object { $_.level -eq 0 })
                if ($direct.Count -ne 2 -or @($direct | Where-Object { $_.requirement -eq 'CONDITIONAL' }).Count -ne 2) { throw 'LoA parent flow is invalid' }
            }
            'solov-loa1' {
                if ($flowExecutions.Count -ne 2 -or @($flowExecutions | Where-Object { $_.providerId -eq 'auth-username-password-form' -and $_.requirement -eq 'REQUIRED' }).Count -ne 1) { throw 'LoA1 flow is invalid' }
            }
            'solov-loa2' {
                if ($flowExecutions.Count -ne 2 -or @($flowExecutions | Where-Object { $_.providerId -eq 'auth-otp-form' -and $_.requirement -eq 'REQUIRED' }).Count -ne 1) { throw 'LoA2 flow is invalid' }
            }
        }
        foreach ($execution in @($flowExecutions | Where-Object { -not [string]::IsNullOrWhiteSpace($_.authenticationConfig) })) {
            $config = Invoke-AdminGet -Path "solov/authentication/config/$($execution.authenticationConfig)"
            if ($config.alias -notin @('solov-loa1-condition', 'solov-password-amr', 'solov-loa2-condition', 'solov-otp-amr')) {
                throw 'unexpected authentication execution configuration alias'
            }
            switch ($config.alias) {
                'solov-loa1-condition' {
                    if ($config.config.'loa-condition-level' -ne '1' -or $config.config.'loa-max-age' -ne '28800') { throw 'LoA1 condition configuration drifted' }
                }
                'solov-password-amr' {
                    if ($config.config.'default.reference.value' -ne 'pwd' -or $config.config.'default.reference.maxAge' -ne '28800') { throw 'password AMR reference configuration drifted' }
                }
                'solov-loa2-condition' {
                    if ($config.config.'loa-condition-level' -ne '2' -or $config.config.'loa-max-age' -ne '0') { throw 'LoA2 condition configuration drifted' }
                }
                'solov-otp-amr' {
                    if ($config.config.'default.reference.value' -ne 'otp' -or $config.config.'default.reference.maxAge' -ne '600') { throw 'OTP AMR reference configuration drifted' }
                }
            }
        }
    }

    $invoiceSecret = Invoke-AdminGet -Path "solov/clients/$($clients['invoice-web'].id)/client-secret"
    if ([string]::IsNullOrWhiteSpace($invoiceSecret.value)) { throw 'invoice-web client secret is unavailable after provisioning' }
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        $sha = $sha256.ComputeHash([Text.Encoding]::UTF8.GetBytes([string]$invoiceSecret.value))
    } finally {
        $sha256.Dispose()
    }
    $expectedSecretHash = ([BitConverter]::ToString($sha) -replace '-', '').ToLowerInvariant()
    $actualSecretHash = ((Invoke-Docker exec $tools sha256sum /out/invoice_oidc_client_secret | Select-Object -First 1).ToString() -split '\s+')[0].ToLowerInvariant()
    if ($actualSecretHash -ne $expectedSecretHash) { throw 'published invoice-web client secret does not match Keycloak' }

    # A fresh output path proves the realm-exists guard, independently from the
    # existing-secret guard. No second secret may be created.
    $savedErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $rerunOutput = & docker exec `
            --env 'KEYCLOAK_PROVISION_TEST_MODE=1' `
            --env 'KEYCLOAK_ADMIN_BASE_URL=http://keycloak:8080' `
            --env 'KEYCLOAK_PUBLIC_HOST=keycloak:8080' `
            --env 'KEYCLOAK_ADMIN_HOST=keycloak:8080' `
            --env "KEYCLOAK_BOOTSTRAP_USERNAME=$bootstrapUser" `
            --env 'KEYCLOAK_BOOTSTRAP_PASSWORD_FILE=/run/test-secrets/bootstrap_password' `
            --env 'INVOICE_OIDC_CLIENT_SECRET_FILE=/out/second_invoice_oidc_client_secret' `
            $tools bash /workspace/deploy/keycloak/provision-solov-realm.sh 2>&1
        $rerunExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $savedErrorActionPreference
    }
    if ($rerunExitCode -eq 0) { throw 'provisioner accepted an existing realm' }
    $rerunLines = @($rerunOutput | ForEach-Object { $_.ToString().Trim() } | Where-Object { $_ -ne '' })
    if ($rerunLines.Count -ne 1 -or $rerunLines[0] -ne 'provision-solov-realm: realm already exists: solov') {
        throw 'existing-realm failure output exceeded the fixed allowlist'
    }
    & docker exec $tools test ! -e /out/second_invoice_oidc_client_secret
    if ($LASTEXITCODE -ne 0) { throw 'existing-realm rejection wrote a second secret' }

    $keycloakLogs = (& docker logs $keycloak 2>&1 | Out-String)
    if ($keycloakLogs -match [regex]::Escape($bootstrapPassword) -or $keycloakLogs -match [regex]::Escape([string]$invoiceSecret.value)) {
        throw 'disposable runtime logs exposed authorization material'
    }
    if ($keycloakLogs -notmatch 'Keycloak 26\.7\.2 .* started') { throw 'exact Keycloak 26.7.2 startup evidence is missing' }

    Write-Host 'Disposable Keycloak 26.7.2 realm provisioning, output allowlist and one-secret publication passed.'
} finally {
    if ($toolsCreated) { & docker rm --force $tools 2>&1 | Out-Null }
    if ($keycloakCreated) { & docker rm --force $keycloak 2>&1 | Out-Null }
    if ($databaseCreated) { & docker rm --force $database 2>&1 | Out-Null }
    if ($networkCreated) { & docker network rm $network 2>&1 | Out-Null }
    if ($volumeCreated) { & docker volume rm --force $secretVolume 2>&1 | Out-Null }
    if ($provisionToolsImageCreated) { & docker image rm --force $provisionToolsImage 2>&1 | Out-Null }
}
