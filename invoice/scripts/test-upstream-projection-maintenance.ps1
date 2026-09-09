[CmdletBinding()]
param(
    [string]$PostgresImage = 'postgres:16-alpine'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$reconcileContract = Join-Path $repositoryRoot 'contracts/projection-reconcile.postgresql.sql'
$preflightContract = Join-Path $repositoryRoot 'contracts/upstream-upgrade-preflight.postgresql.sql'
$quiescenceContract = Join-Path $repositoryRoot 'contracts/source-cutover-quiescence-preflight.postgresql.sql'
$preserveRolesScript = Join-Path $PSScriptRoot 'preserve-source-reader-roles.sh'
$linuxWrapper = Join-Path $PSScriptRoot 'invoke-upstream-projection-maintenance.sh'
$bridgeContracts = @(
    'newapi-source-projection-grants.postgresql.sql',
    'newapi-economic-projection-grants.postgresql.sql',
    'newapi-bridge-v4-rollback.postgresql.sql',
    'sub2api-source-projection-grants.postgresql.sql',
    'sub2api-economic-projection-grants.postgresql.sql',
    'sub2api-bridge-v4-rollback.postgresql.sql'
) | ForEach-Object { Join-Path $repositoryRoot "contracts/$_" }
$wrapper = Join-Path $PSScriptRoot 'invoke-upstream-projection-maintenance.ps1'

foreach ($path in @($reconcileContract, $preflightContract, $quiescenceContract, $wrapper, $linuxWrapper, $preserveRolesScript) + $bridgeContracts) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Missing maintenance artifact: $path"
    }
}

$preserveContract = Get-Content -LiteralPath $preserveRolesScript -Raw
foreach ($required in @(
    'umask 077', 'chmod 0600', '--set=ON_ERROR_STOP=1',
    'SCRAM-SHA-256', 'pg_authid', 'restore requires zero existing'
)) {
    if (-not $preserveContract.Contains($required)) {
        throw "Reader-role preservation script is missing safety contract: $required"
    }
}
$linuxWrapperContract = Get-Content -LiteralPath $linuxWrapper -Raw
if (-not $linuxWrapperContract.Contains('--set=ON_ERROR_STOP=1') -or
    -not $linuxWrapperContract.Contains('cutover-quiescence-preflight') -or
    -not $linuxWrapperContract.Contains('rollback-bridge')) {
    throw 'Linux production maintenance wrapper does not force reviewed execution modes/ON_ERROR_STOP.'
}
foreach ($path in $bridgeContracts) {
    $contract = Get-Content -LiteralPath $path -Raw
    if (-not $contract.Contains('cluster superuser that owns the current database') -or
        -not $contract.Contains('ON_ERROR_STOP=1')) {
        throw "Bridge contract is missing executor/wrapper guard: $path"
    }
}

$forbiddenSql = '(?im)\bCASCADE\b|\b(?:DELETE\s+FROM|INSERT\s+INTO|UPDATE|TRUNCATE|ALTER\s+TABLE|DROP\s+TABLE)\s+(?:public\.)?'
foreach ($path in @($reconcileContract, $preflightContract, $quiescenceContract)) {
    $content = Get-Content -LiteralPath $path -Raw
    if ($content -match $forbiddenSql) {
        throw "Forbidden upstream mutation token found in $path"
    }
}

$parseErrors = $null
[System.Management.Automation.Language.Parser]::ParseFile(
    $wrapper,
    [ref]$null,
    [ref]$parseErrors
) > $null
if ($parseErrors.Count -ne 0) {
    throw "PowerShell wrapper parse failed: $($parseErrors[0].Message)"
}

$docker = Get-Command docker -CommandType Application -ErrorAction Stop |
    Select-Object -First 1
$script:maintenanceContainerName = "invoice-projection-maintenance-test-$PID"
$containerStarted = $false

function Invoke-TestPsql {
    param(
        [Parameter(Mandatory = $true)][string]$Database,
        [string]$Sql,
        [string]$File,
        [string[]]$Variables = @(),
        [string]$User = 'postgres',
        [switch]$ExpectFailure,
        [switch]$TuplesOnly
    )

    if ([string]::IsNullOrWhiteSpace($script:maintenanceContainerName)) {
        throw 'maintenance fixture container name is unavailable'
    }
    $arguments = @('exec', $script:maintenanceContainerName, 'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', $User, '-d', $Database)
    if ($TuplesOnly) {
        $arguments += @('-A', '-t')
    }
    foreach ($variable in $Variables) {
        $arguments += @('-v', $variable)
    }
    if (-not [string]::IsNullOrEmpty($File)) {
        $arguments += @('-f', $File)
    } else {
        $arguments += @('-c', $Sql)
    }

    $output = & $script:docker.Source @arguments 2>&1
    $exitCode = $LASTEXITCODE
    if ($ExpectFailure) {
        if ($exitCode -eq 0) {
            throw "Expected psql failure in $Database, but command passed."
        }
    } elseif ($exitCode -ne 0) {
        throw "psql failed in ${Database}: $($output -join [Environment]::NewLine)"
    }
    return ($output -join [Environment]::NewLine).Trim()
}

function New-SourceFixture {
    param(
        [Parameter(Mandatory = $true)][ValidateSet('newapi', 'sub2api')][string]$Source,
        [switch]$CreateLegacyViews
    )

    $database = "${Source}_fixture"
    Invoke-TestPsql -Database postgres -Sql "CREATE DATABASE $database" > $null

    if ($Source -eq 'newapi') {
        $tableDefinitions = [ordered]@{
            options = 'key text PRIMARY KEY,value text'
            users = 'id bigint PRIMARY KEY,quota bigint,deleted_at timestamptz,email text,password text'
            logs = 'id bigint PRIMARY KEY,user_id bigint,created_at bigint,type int,quota bigint,content text,username text,token_name text,model_name text,ip text,request_id text,other text'
            checkins = 'id bigint PRIMARY KEY,user_id bigint,checkin_date text,quota_awarded bigint,created_at bigint'
            redemptions = 'id bigint PRIMARY KEY,user_id bigint,status int,quota bigint,redeemed_time bigint,used_user_id bigint,key text,name text,deleted_at timestamptz'
            top_ups = 'id bigint PRIMARY KEY,user_id bigint,amount bigint,money numeric(20,6),trade_no text,payment_method text,payment_provider text,create_time bigint,complete_time bigint,status text'
            subscription_orders = 'id bigint PRIMARY KEY,user_id bigint,plan_id bigint,money numeric(20,6),trade_no text,payment_method text,payment_provider text,status text,create_time bigint,complete_time bigint,provider_payload text'
            custom_oauth_providers = 'id bigint PRIMARY KEY,slug text,enabled boolean,well_known text,authorization_endpoint text,token_endpoint text,user_info_endpoint text,client_secret text'
            user_oauth_bindings = 'id bigint PRIMARY KEY,user_id bigint,provider_id bigint,provider_user_id text,created_at timestamptz,secret_value text'
        }
        $tables = @($tableDefinitions.Keys)
        $sentinelTable = 'top_ups'
        $seedStatements = @(
            "INSERT INTO public.top_ups(id) VALUES(424242)",
            "INSERT INTO public.options VALUES('QuotaPerUnit','500000'),('Price','1'),('TopupGroupRatio','{`"default`":1}'),('LogConsumeEnabled','true')"
        )
        $views = @(
            'invoice_newapi_cutover_contract_v3',
            'invoice_newapi_payments_projection_health_v3',
            'invoice_newapi_payments_projection_v3',
            'invoice_newapi_usage_projection_health_v3',
            'invoice_newapi_usage_projection_v3',
            'invoice_newapi_credits_projection_health_v3',
            'invoice_newapi_credits_projection_v3',
            'invoice_newapi_balance_projection_v3',
            'invoice_newapi_wallet_config_contract_v3',
            'invoice_newapi_oidc_binding_projection_v1',
            'invoice_newapi_oidc_provider_contract_v1'
        )
        $roles = @(
            'invoice_newapi_payments_reader',
            'invoice_newapi_identities_reader',
            'invoice_newapi_payments_v3_reader',
            'invoice_newapi_usage_reader',
            'invoice_newapi_credits_reader',
            'invoice_newapi_balances_reader'
        )
    } else {
        $tableDefinitions = [ordered]@{
            settings = 'key text PRIMARY KEY,value text,updated_at timestamptz'
            users = 'id bigint PRIMARY KEY,balance numeric(20,8),deleted_at timestamptz,email text,password_hash text'
            usage_logs = 'id bigint PRIMARY KEY,user_id bigint,billing_type smallint,actual_cost numeric(20,10),created_at timestamptz,content text,ip_address text'
            promo_code_usages = 'id bigint PRIMARY KEY,user_id bigint,bonus_amount numeric(20,8),used_at timestamptz'
            user_affiliate_ledger = 'id bigint PRIMARY KEY,user_id bigint,action text,amount numeric(20,8),created_at timestamptz'
            redeem_codes = 'id bigint PRIMARY KEY,code text,type text,value numeric(20,8),status text,used_by bigint,used_at timestamptz'
            payment_orders = 'id bigint PRIMARY KEY,user_id bigint,status text,order_type text,amount numeric(20,8),pay_amount numeric(20,8),fee_rate numeric(10,4),refund_amount numeric(20,8),completed_at timestamptz,refund_at timestamptz,created_at timestamptz,updated_at timestamptz,payment_type text,provider_key text,provider_snapshot jsonb,recharge_code text'
            auth_identities = 'id bigint PRIMARY KEY,user_id bigint,provider_type text,provider_key text,provider_subject text,verified_at timestamptz,issuer text,metadata jsonb NOT NULL DEFAULT ''{}''::jsonb,created_at timestamptz,updated_at timestamptz,secret_value text'
        }
        $tables = @($tableDefinitions.Keys)
        $sentinelTable = 'payment_orders'
        $seedStatements = @(
            'INSERT INTO public.payment_orders(id) VALUES(424242)',
            "INSERT INTO public.settings VALUES('BALANCE_RECHARGE_MULTIPLIER','1.00',now()),('RECHARGE_FEE_RATE','0.00',now())"
        )
        $views = @(
            'invoice_sub2api_cutover_contract_v3',
            'invoice_sub2api_payments_projection_health_v3',
            'invoice_sub2api_payments_projection_v3',
            'invoice_sub2api_usage_projection_health_v3',
            'invoice_sub2api_usage_projection_v3',
            'invoice_sub2api_credits_projection_health_v3',
            'invoice_sub2api_credits_projection_v3',
            'invoice_sub2api_balance_projection_v3',
            'invoice_sub2api_wallet_config_contract_v3',
            'invoice_sub2api_oidc_binding_projection_v1',
            'invoice_sub2api_payment_projection_health_v1',
            'invoice_sub2api_payment_projection_v1'
        )
        $roles = @(
            'invoice_sub2api_payments_reader',
            'invoice_sub2api_identities_reader',
            'invoice_sub2api_payments_v3_reader',
            'invoice_sub2api_usage_reader',
            'invoice_sub2api_credits_reader',
            'invoice_sub2api_balances_reader'
        )
    }

    $statements = [System.Collections.Generic.List[string]]::new()
    foreach ($table in $tableDefinitions.Keys) {
        $statements.Add("CREATE TABLE public.$table ($($tableDefinitions[$table]))")
    }
    foreach ($statement in $seedStatements) {
        $statements.Add($statement)
    }
    foreach ($role in $roles) {
        $statements.Add("CREATE ROLE $role LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2")
        $statements.Add("ALTER ROLE $role SET default_transaction_read_only=on")
        $statements.Add("GRANT CONNECT ON DATABASE $database TO $role")
        $statements.Add("GRANT USAGE ON SCHEMA public TO $role")
    }
    if ($CreateLegacyViews) {
        foreach ($view in $views) {
            $statements.Add("CREATE VIEW public.$view AS SELECT id FROM public.$sentinelTable")
        }
        $qualifiedViews = ($views | ForEach-Object { "public.$_" }) -join ','
        foreach ($role in $roles) {
            $statements.Add("GRANT SELECT ON $qualifiedViews TO $role")
        }
    }
    $statements.Add("REVOKE TEMPORARY ON DATABASE $database FROM PUBLIC")
    Invoke-TestPsql -Database $database -Sql (($statements -join ";`n") + ';') > $null

    return [pscustomobject]@{
        Database = $database
        Tables = $tables
        SentinelTable = $sentinelTable
        Views = $views
        Roles = $roles
    }
}

function Install-BridgeFixture {
    param(
        [Parameter(Mandatory = $true)][ValidateSet('newapi', 'sub2api')][string]$Source,
        [Parameter(Mandatory = $true)][string]$Database
    )

    $compatibilityReader = "invoice_${Source}_payments_reader"
    $loginReaders = @(
        "invoice_${Source}_identities_reader",
        "invoice_${Source}_payments_v3_reader",
        "invoice_${Source}_usage_reader",
        "invoice_${Source}_credits_reader",
        "invoice_${Source}_balances_reader"
    )
    $statements = [System.Collections.Generic.List[string]]::new()
    $statements.Add("CREATE ROLE $compatibilityReader NOLOGIN NOINHERIT CONNECTION LIMIT 0")
    foreach ($role in $loginReaders) {
        $statements.Add("CREATE ROLE $role LOGIN NOINHERIT CONNECTION LIMIT 2")
    }
    Invoke-TestPsql -Database $Database -Sql (($statements -join ";`n") + ';') > $null

    Invoke-TestPsql -Database $Database -File "/contracts/${Source}-source-projection-grants.postgresql.sql" > $null
    Invoke-TestPsql -Database $Database -File "/contracts/${Source}-economic-projection-grants.postgresql.sql" > $null
}

try {
    & $docker.Source run --name $script:maintenanceContainerName --detach --rm `
        --env POSTGRES_PASSWORD=test-only `
        --env POSTGRES_HOST_AUTH_METHOD=trust `
        $PostgresImage -c max_prepared_transactions=10 > $null
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to start disposable PostgreSQL container from $PostgresImage"
    }
    $containerStarted = $true

    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        & $docker.Source exec $script:maintenanceContainerName pg_isready -U postgres 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) {
            $ready = $true
            break
        }
        Start-Sleep -Milliseconds 500
    }
    if (-not $ready) {
        throw 'Disposable PostgreSQL did not become ready.'
    }

    & $docker.Source exec $script:maintenanceContainerName mkdir -p /contracts
    if ($LASTEXITCODE -ne 0) { throw 'Unable to create disposable contract directory.' }
    & $docker.Source cp $reconcileContract "$($script:maintenanceContainerName):/contracts/projection-reconcile.postgresql.sql"
    if ($LASTEXITCODE -ne 0) { throw 'Unable to copy reconcile contract.' }
    & $docker.Source cp $preflightContract "$($script:maintenanceContainerName):/contracts/upstream-upgrade-preflight.postgresql.sql"
    if ($LASTEXITCODE -ne 0) { throw 'Unable to copy preflight contract.' }
    & $docker.Source cp $quiescenceContract "$($script:maintenanceContainerName):/contracts/source-cutover-quiescence-preflight.postgresql.sql"
    if ($LASTEXITCODE -ne 0) { throw 'Unable to copy cutover quiescence contract.' }
    foreach ($contract in $bridgeContracts) {
        & $docker.Source cp $contract "$($script:maintenanceContainerName):/contracts/$([IO.Path]::GetFileName($contract))"
        if ($LASTEXITCODE -ne 0) { throw "Unable to copy bridge contract $contract" }
    }

    # New API reproduces the observed partial state: zero views and six roles.
    # Sub2API reproduces the observed full legacy state: twelve views and six roles.
    $newapi = New-SourceFixture -Source newapi
    $sub2api = New-SourceFixture -Source sub2api -CreateLegacyViews

    Invoke-TestPsql -Database postgres -Sql 'CREATE ROLE maintenance_non_super LOGIN' > $null
    Invoke-TestPsql -Database postgres -Sql 'CREATE DATABASE maintenance_non_super OWNER maintenance_non_super' > $null
    Invoke-TestPsql -Database maintenance_non_super -User maintenance_non_super `
        -File /contracts/source-cutover-quiescence-preflight.postgresql.sql `
        -Variables @('invoice_source=newapi') -ExpectFailure > $null
    Invoke-TestPsql -Database maintenance_non_super -User maintenance_non_super `
        -File /contracts/newapi-source-projection-grants.postgresql.sql `
        -ExpectFailure > $null

    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/source-cutover-quiescence-preflight.postgresql.sql `
        -Variables @('invoice_source=newapi') > $null

    # The contract detects but never terminates a connected source role.
    & $docker.Source exec --detach $script:maintenanceContainerName psql -X -U invoice_newapi_usage_reader `
        -d $newapi.Database -c 'SELECT pg_sleep(3)' > $null
    if ($LASTEXITCODE -ne 0) { throw 'Unable to start disposable active-session fixture.' }
    $sessionObserved = $false
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        $sessionCount = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql (
            "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() " +
            "AND usename='invoice_newapi_usage_reader'"
        )
        if ([int]$sessionCount -gt 0) {
            $sessionObserved = $true
            break
        }
        Start-Sleep -Milliseconds 100
    }
    if (-not $sessionObserved) { throw 'Active-session fixture was not observed.' }
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/source-cutover-quiescence-preflight.postgresql.sql `
        -Variables @('invoice_source=newapi') -ExpectFailure > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=0') `
        -ExpectFailure > $null
    do {
        Start-Sleep -Milliseconds 250
        $sessionCount = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql (
            "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() " +
            "AND usename='invoice_newapi_usage_reader'"
        )
    } while ([int]$sessionCount -gt 0)

    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/source-cutover-quiescence-preflight.postgresql.sql `
        -Variables @('invoice_source=newapi') > $null
    Invoke-TestPsql -Database $newapi.Database -Sql "BEGIN; PREPARE TRANSACTION 'invoice_cutover_probe'" > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/source-cutover-quiescence-preflight.postgresql.sql `
        -Variables @('invoice_source=newapi') -ExpectFailure > $null
    Invoke-TestPsql -Database $newapi.Database -Sql "ROLLBACK PREPARED 'invoice_cutover_probe'" > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/source-cutover-quiescence-preflight.postgresql.sql `
        -Variables @('invoice_source=newapi') > $null

    # Exact database/schema/view ACL semantics are audited with aclexplode;
    # pg_shdepend alone cannot distinguish CONNECT from CREATE or SELECT from
    # UPDATE/grant-option on the same object.
    Invoke-TestPsql -Database $newapi.Database -Sql (
        "GRANT CREATE ON DATABASE $($newapi.Database) TO invoice_newapi_usage_reader"
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=1') -ExpectFailure > $null
    $databaseCreatePreserved = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql (
        "SELECT has_database_privilege('invoice_newapi_usage_reader',current_database(),'CREATE')"
    )
    if ($databaseCreatePreserved -ne 't') { throw 'Database CREATE ACL was not fail-closed.' }
    Invoke-TestPsql -Database $newapi.Database -Sql (
        "REVOKE CREATE ON DATABASE $($newapi.Database) FROM invoice_newapi_usage_reader"
    ) > $null

    Invoke-TestPsql -Database $newapi.Database -Sql (
        "GRANT TEMPORARY ON DATABASE $($newapi.Database) TO invoice_newapi_usage_reader"
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=0') -ExpectFailure > $null
    Invoke-TestPsql -Database $newapi.Database -Sql (
        "REVOKE TEMPORARY ON DATABASE $($newapi.Database) FROM invoice_newapi_usage_reader"
    ) > $null

    Invoke-TestPsql -Database $newapi.Database -Sql (
        "GRANT CONNECT ON DATABASE $($newapi.Database) TO invoice_newapi_usage_reader WITH GRANT OPTION"
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=0') -ExpectFailure > $null
    $databaseGrantOption = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql (
        "SELECT count(*) FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) a " +
        "JOIN pg_roles r ON r.oid=a.grantee WHERE d.datname=current_database() " +
        "AND r.rolname='invoice_newapi_usage_reader' AND a.privilege_type='CONNECT' AND a.is_grantable"
    )
    if ($databaseGrantOption -ne '1') { throw 'Database CONNECT grant option fixture was not preserved.' }
    Invoke-TestPsql -Database $newapi.Database -Sql (
        "REVOKE GRANT OPTION FOR CONNECT ON DATABASE $($newapi.Database) FROM invoice_newapi_usage_reader"
    ) > $null

    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT CREATE ON SCHEMA public TO invoice_newapi_usage_reader'
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=1') -ExpectFailure > $null
    $schemaCreatePreserved = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql (
        "SELECT has_schema_privilege('invoice_newapi_usage_reader','public','CREATE')"
    )
    if ($schemaCreatePreserved -ne 't') { throw 'Schema CREATE ACL was not fail-closed.' }
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'REVOKE CREATE ON SCHEMA public FROM invoice_newapi_usage_reader'
    ) > $null

    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT USAGE ON SCHEMA public TO invoice_newapi_usage_reader WITH GRANT OPTION'
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=0') -ExpectFailure > $null
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'REVOKE GRANT OPTION FOR USAGE ON SCHEMA public FROM invoice_newapi_usage_reader'
    ) > $null

    $legacyView = $sub2api.Views[0]
    Invoke-TestPsql -Database $sub2api.Database -Sql (
        "GRANT UPDATE ON public.$legacyView TO invoice_sub2api_usage_reader"
    ) > $null
    Invoke-TestPsql -Database $sub2api.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=sub2api', 'reconcile_apply=1') -ExpectFailure > $null
    $viewUpdatePreserved = Invoke-TestPsql -Database $sub2api.Database -TuplesOnly -Sql (
        "SELECT has_table_privilege('invoice_sub2api_usage_reader','public.$legacyView','UPDATE')"
    )
    if ($viewUpdatePreserved -ne 't') { throw 'Legacy view UPDATE ACL was not fail-closed.' }
    Invoke-TestPsql -Database $sub2api.Database -Sql (
        "REVOKE UPDATE ON public.$legacyView FROM invoice_sub2api_usage_reader"
    ) > $null

    Invoke-TestPsql -Database $sub2api.Database -Sql (
        "GRANT SELECT ON public.$legacyView TO invoice_sub2api_usage_reader WITH GRANT OPTION"
    ) > $null
    Invoke-TestPsql -Database $sub2api.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=sub2api', 'reconcile_apply=0') -ExpectFailure > $null
    Invoke-TestPsql -Database $sub2api.Database -Sql (
        "REVOKE GRANT OPTION FOR SELECT ON public.$legacyView FROM invoice_sub2api_usage_reader"
    ) > $null

    # A raw upstream-table grant is not silently revoked. DROP ROLE refuses it,
    # and the enclosing transaction restores every earlier role/privilege step.
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT SELECT ON public.top_ups TO invoice_newapi_payments_reader'
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=1') `
        -ExpectFailure > $null
    $rollbackState = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql (
        "SELECT count(*) || '|' || " +
        "(SELECT CASE WHEN rolcanlogin THEN 'login' ELSE 'nologin' END " +
        " FROM pg_roles WHERE rolname='invoice_newapi_payments_reader') " +
        "FROM pg_roles WHERE rolname LIKE 'invoice!_newapi!_%' ESCAPE '!'"
    )
    if ($rollbackState -ne '6|login') {
        throw "Failed apply was not fully rolled back: $rollbackState"
    }
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'REVOKE SELECT ON public.top_ups FROM invoice_newapi_payments_reader'
    ) > $null

    Invoke-TestPsql -Database postgres -Sql 'CREATE ROLE upstream_sentinel_role NOLOGIN' > $null
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT SELECT(trade_no) ON public.top_ups TO upstream_sentinel_role'
    ) > $null
    Invoke-TestPsql -Database $sub2api.Database -Sql (
        'GRANT SELECT(secret_value) ON public.auth_identities TO upstream_sentinel_role'
    ) > $null

    # An additional raw column on the compatibility reader is not part of the
    # reviewed partial state. Audit must fail before changing the six roles,
    # and both the unknown ACL and an unrelated upstream ACL must survive.
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT SELECT(trade_no) ON public.top_ups TO invoice_newapi_payments_reader'
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=1') `
        -ExpectFailure > $null
    $extraColumnState = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql (
        "SELECT (SELECT count(*) FROM pg_roles WHERE rolname LIKE 'invoice!_newapi!_%' ESCAPE '!') || '|' || " +
        "has_column_privilege('invoice_newapi_payments_reader','public.top_ups','trade_no','SELECT') || '|' || " +
        "has_column_privilege('upstream_sentinel_role','public.top_ups','trade_no','SELECT')"
    )
    if ($extraColumnState -ne '6|true|true') {
        throw "Unexpected compatibility ACL was not fail-closed: $extraColumnState"
    }
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'REVOKE SELECT(trade_no) ON public.top_ups FROM invoice_newapi_payments_reader'
    ) > $null

    # The same allowed column on the wrong reader and WITH GRANT OPTION are
    # independently outside the exact production allowlist.
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT SELECT(id) ON public.top_ups TO invoice_newapi_identities_reader'
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=0') `
        -ExpectFailure > $null
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'REVOKE SELECT(id) ON public.top_ups FROM invoice_newapi_identities_reader'
    ) > $null
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT SELECT(id) ON public.top_ups TO invoice_newapi_payments_reader WITH GRANT OPTION'
    ) > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=0') `
        -ExpectFailure > $null
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'REVOKE SELECT(id) ON public.top_ups FROM invoice_newapi_payments_reader'
    ) > $null

    # Reproduce the exact production partial state left by the reviewed legacy
    # compatibility contract: zero views, six roles and only these nine
    # non-grantable SELECT column ACLs on top_ups. Reconciliation may revoke
    # this exact known residue, while the full-table grant above remains fatal.
    Invoke-TestPsql -Database $newapi.Database -Sql (
        'GRANT SELECT(id,user_id,amount,money,payment_method,payment_provider,' +
        'create_time,complete_time,status) ON public.top_ups TO invoice_newapi_payments_reader'
    ) > $null

    foreach ($fixture in @($newapi, $sub2api)) {
        $source = if ($fixture.Database.StartsWith('newapi')) { 'newapi' } else { 'sub2api' }

        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/projection-reconcile.postgresql.sql `
            -Variables @("invoice_source=$source", 'reconcile_apply=0') > $null

        $roleCountAfterAudit = Invoke-TestPsql -Database $fixture.Database -TuplesOnly -Sql (
            "SELECT count(*) FROM pg_roles WHERE rolname = ANY(ARRAY['" +
            ($fixture.Roles -join "','") + "'])"
        )
        if ([int]$roleCountAfterAudit -ne 6) {
            throw "$source audit changed role state."
        }

        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/projection-reconcile.postgresql.sql `
            -Variables @("invoice_source=$source", 'reconcile_apply=1') > $null

        $postState = Invoke-TestPsql -Database $fixture.Database -TuplesOnly -Sql (
            "SELECT " +
            "(SELECT count(*) FROM pg_roles WHERE rolname LIKE 'invoice!_${source}!_%' ESCAPE '!')," +
            "(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace " +
            " WHERE n.nspname='public' AND c.relname LIKE 'invoice!_${source}!_%' ESCAPE '!')," +
            "(SELECT count(*) FROM public.$($fixture.SentinelTable) WHERE id=424242)," +
            "EXISTS (SELECT 1 FROM pg_database d CROSS JOIN LATERAL " +
            "aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a " +
            "WHERE d.datname=current_database() AND a.grantee=0 " +
            "AND a.privilege_type IN ('TEMP','TEMPORARY'))"
        )
        if ($postState -ne '0|0|1|t') {
            throw "$source cleanup postcondition mismatch: $postState"
        }

        $sentinelState = if ($source -eq 'newapi') {
            Invoke-TestPsql -Database $fixture.Database -TuplesOnly -Sql (
                "SELECT has_column_privilege('upstream_sentinel_role','public.top_ups','trade_no','SELECT') || '|' || " +
                "pg_get_userbyid(relowner) FROM pg_class WHERE oid='public.top_ups'::regclass"
            )
        } else {
            Invoke-TestPsql -Database $fixture.Database -TuplesOnly -Sql (
                "SELECT has_column_privilege('upstream_sentinel_role','public.auth_identities','secret_value','SELECT') || '|' || " +
                "pg_get_userbyid(relowner) FROM pg_class WHERE oid='public.auth_identities'::regclass"
            )
        }
        if ($sentinelState -ne 'true|postgres') {
            throw "$source cleanup changed an unrelated upstream ACL or table owner: $sentinelState"
        }

        # Idempotence: the same apply must remain a successful no-op.
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/projection-reconcile.postgresql.sql `
            -Variables @("invoice_source=$source", 'reconcile_apply=1') > $null

        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=detached') > $null
    }

    Invoke-TestPsql -Database $newapi.Database -Sql (
        'REVOKE SELECT(trade_no) ON public.top_ups FROM upstream_sentinel_role'
    ) > $null
    Invoke-TestPsql -Database $sub2api.Database -Sql (
        'REVOKE SELECT(secret_value) ON public.auth_identities FROM upstream_sentinel_role'
    ) > $null
    Invoke-TestPsql -Database postgres -Sql 'DROP ROLE upstream_sentinel_role' > $null

    # Unknown invoice-prefixed roles are never silently removed.
    Invoke-TestPsql -Database $newapi.Database -Sql 'CREATE ROLE invoice_newapi_unreviewed NOLOGIN' > $null
    Invoke-TestPsql -Database $newapi.Database `
        -File /contracts/projection-reconcile.postgresql.sql `
        -Variables @('invoice_source=newapi', 'reconcile_apply=0') `
        -ExpectFailure > $null
    Invoke-TestPsql -Database $newapi.Database -Sql 'DROP ROLE invoice_newapi_unreviewed' > $null

    foreach ($fixture in @($newapi, $sub2api)) {
        $source = if ($fixture.Database.StartsWith('newapi')) { 'newapi' } else { 'sub2api' }
        $owner = "invoice_${source}_bridge_owner"
        $identityReader = "invoice_${source}_identities_reader"
        $paymentsFunction = "${source}_payments_v4"
        $extraColumnTable = if ($source -eq 'newapi') { 'top_ups' } else { 'auth_identities' }
        $extraColumn = if ($source -eq 'newapi') { 'trade_no' } else { 'secret_value' }

        Install-BridgeFixture -Source $source -Database $fixture.Database
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') > $null

        # PUBLIC schema USAGE may legitimately be present or absent. Neither
        # state may grant CREATE, and callers need only invoice_bridge USAGE.
        Invoke-TestPsql -Database $fixture.Database -Sql 'REVOKE USAGE ON SCHEMA public FROM PUBLIC' > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') > $null
        Invoke-TestPsql -Database $fixture.Database -Sql 'GRANT USAGE ON SCHEMA public TO PUBLIC' > $null

        Invoke-TestPsql -Database $fixture.Database -Sql "ALTER TABLE public.$($fixture.SentinelTable) ENABLE ROW LEVEL SECURITY" > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') -ExpectFailure > $null
        Invoke-TestPsql -Database $fixture.Database -Sql "ALTER TABLE public.$($fixture.SentinelTable) DISABLE ROW LEVEL SECURITY" > $null

        Invoke-TestPsql -Database $fixture.Database -Sql "GRANT SELECT(id) ON public.$($fixture.SentinelTable) TO $identityReader" > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') -ExpectFailure > $null
        Invoke-TestPsql -Database $fixture.Database -Sql "REVOKE SELECT(id) ON public.$($fixture.SentinelTable) FROM $identityReader" > $null

        Invoke-TestPsql -Database $fixture.Database -Sql "GRANT SELECT($extraColumn) ON public.$extraColumnTable TO $owner" > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') -ExpectFailure > $null
        Invoke-TestPsql -Database $fixture.Database -File "/contracts/${source}-source-projection-grants.postgresql.sql" > $null

        Invoke-TestPsql -Database $fixture.Database -Sql "ALTER TABLE public.$($fixture.SentinelTable) OWNER TO $owner" > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') -ExpectFailure > $null
        Invoke-TestPsql -Database $fixture.Database -Sql "ALTER TABLE public.$($fixture.SentinelTable) OWNER TO postgres" > $null
        Invoke-TestPsql -Database $fixture.Database -File "/contracts/${source}-source-projection-grants.postgresql.sql" > $null

        Invoke-TestPsql -Database $fixture.Database -Sql 'GRANT CREATE ON SCHEMA public TO PUBLIC' > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') -ExpectFailure > $null
        Invoke-TestPsql -Database $fixture.Database -Sql 'REVOKE CREATE ON SCHEMA public FROM PUBLIC' > $null

        $fakeBody = "CREATE OR REPLACE FUNCTION invoice_bridge.$paymentsFunction(operation text,request jsonb) " +
            "RETURNS SETOF jsonb LANGUAGE plpgsql SECURITY DEFINER VOLATILE " +
            "SET search_path=pg_catalog SET row_security=on AS `$bridge`$ " +
            "BEGIN RETURN NEXT '{}'::jsonb; RETURN; END `$bridge`$; " +
            "ALTER FUNCTION invoice_bridge.$paymentsFunction(text,jsonb) OWNER TO $owner"
        Invoke-TestPsql -Database $fixture.Database -Sql $fakeBody > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') -ExpectFailure > $null
        Invoke-TestPsql -Database $fixture.Database -File "/contracts/${source}-source-projection-grants.postgresql.sql" > $null
        Invoke-TestPsql -Database $fixture.Database `
            -File /contracts/upstream-upgrade-preflight.postgresql.sql `
            -Variables @("invoice_source=$source", 'boundary_state=bridge-v4') > $null
    }

    $unexpectedType = 'invoice_newapi_unexpected_enum'
    Invoke-TestPsql -Database $newapi.Database -Sql "CREATE TYPE public.$unexpectedType AS ENUM ('sentinel'); ALTER TYPE public.$unexpectedType OWNER TO invoice_newapi_identities_reader" > $null
    Invoke-TestPsql -Database $newapi.Database -File /contracts/newapi-bridge-v4-rollback.postgresql.sql -ExpectFailure > $null
    $typePreserved = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql "SELECT to_regtype('public.$unexpectedType') IS NOT NULL"
    if ($typePreserved -ne 't') { throw 'Bridge rollback deleted an unknown reader-owned enum.' }
    Invoke-TestPsql -Database $newapi.Database -Sql "ALTER TYPE public.$unexpectedType OWNER TO postgres; DROP TYPE public.$unexpectedType" > $null

    Invoke-TestPsql -Database $newapi.Database -Sql @'
CREATE FUNCTION public.invoice_unrelated_helper() RETURNS integer LANGUAGE sql AS 'SELECT 1';
REVOKE ALL ON FUNCTION public.invoice_unrelated_helper() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.invoice_unrelated_helper() TO invoice_newapi_identities_reader;
'@ > $null
    Invoke-TestPsql -Database $newapi.Database -File /contracts/newapi-bridge-v4-rollback.postgresql.sql -ExpectFailure > $null
    $helperPreserved = Invoke-TestPsql -Database $newapi.Database -TuplesOnly -Sql "SELECT to_regprocedure('public.invoice_unrelated_helper()') IS NOT NULL AND has_function_privilege('invoice_newapi_identities_reader','public.invoice_unrelated_helper()','EXECUTE')"
    if ($helperPreserved -ne 't') { throw 'Bridge rollback silently removed an unknown function ACL.' }
    Invoke-TestPsql -Database $newapi.Database -Sql 'REVOKE EXECUTE ON FUNCTION public.invoice_unrelated_helper() FROM invoice_newapi_identities_reader; DROP FUNCTION public.invoice_unrelated_helper()' > $null
    Invoke-TestPsql -Database $newapi.Database -File /contracts/newapi-bridge-v4-rollback.postgresql.sql > $null

    Write-Host 'Projection maintenance static, executor, quiescence, prepared-xact, session, rollback, dry-run, partial/full-state, idempotence, detached and adversarial bridge-v4 tests passed.'
} finally {
    if ($containerStarted) {
        & $docker.Source rm --force $script:maintenanceContainerName 2>&1 | Out-Null
    }
}
