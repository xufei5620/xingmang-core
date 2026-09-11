[CmdletBinding()]
param(
    [string]$LibraryPath = (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1'),
    [string]$CaseId = '*',
    [string]$DockerfilePath = (Join-Path $PSScriptRoot '../deploy/keycloak/Dockerfile')
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
. $LibraryPath
$base='quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
$builder='registry.access.redhat.com/ubi9/openjdk-21@sha256:cc8a30e9181b0135e6657ca3b824d7b32e4c7f6a664769ef641d4f7031339564'
$legacy="FROM $base AS builder"+[char]10+"FROM $base"+[char]10
$rebuilt="FROM $builder AS source-build"+[char]10+$legacy
$failures=[Collections.Generic.List[string]]::new()
function Check-RebuildCase([string]$Id,[scriptblock]$Action){
    if($CaseId -ne '*' -and $CaseId -cne $Id){return}
    try{& $Action;Write-Host "PASS $Id"}catch{$failures.Add($Id+': '+$_.Exception.Message);Write-Host "FAIL $Id"}
}
function Require-Rejected([string]$Text){
    $rejected=$false
    try{Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $Text -ExpectedBaseReference $base|Out-Null}catch{$rejected=$true}
    if(-not $rejected){throw 'Untrusted source-build recipe was accepted.'}
}
Check-RebuildCase 'reviewed-source-stage-accepted' {
    Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $rebuilt -ExpectedBaseReference $base|Out-Null
}
Check-RebuildCase 'legacy-binary-only-layout-rejected' {Require-Rejected $legacy}
Check-RebuildCase 'unreviewed-source-builder-rejected' {Require-Rejected ($rebuilt.Replace($builder,'ubuntu:latest'))}
Check-RebuildCase 'source-builder-digest-drift-rejected' {Require-Rejected ($rebuilt.Replace('cc8a30e9181b0135e6657ca3b824d7b32e4c7f6a664769ef641d4f7031339564',('a'*64)))}
Check-RebuildCase 'source-builder-order-rejected' {Require-Rejected ($legacy+"FROM $builder AS source-build"+[char]10)}
Check-RebuildCase 'runtime-base-drift-rejected' {Require-Rejected ($rebuilt.Replace($base,'quay.io/keycloak/keycloak:26.7.3@sha256:'+('b'*64)))}
Check-RebuildCase 'source-build-libicu-pin-before-maven' {
    $text=[IO.File]::ReadAllText($DockerfilePath).Replace("`r`n","`n").Replace("`r","`n")
    $stages=[regex]::Matches($text,'(?m)^[\t ]*FROM[\t ]+')
    if($stages.Count -lt 2){throw 'Cannot locate the Keycloak source-build stage.'}
    $source=$text.Substring($stages[0].Index,$stages[1].Index-$stages[0].Index)
    $active=(($source -split "`n" | Where-Object {$_ -notmatch '^\s*#'}) -join "`n") -replace '\\\n[\t ]*',' '
    $pin='libicu-67.1-10.el9_6.x86_64'
    $install=[regex]::Match($active,'(?m)^[\t ]*RUN[\t ]+microdnf[\t ]+install[\t ]+-y[\t ]+([^&\r\n]+)')
    if(-not $install.Success -or ($install.Groups[1].Value.Trim() -split '\s+') -cnotcontains $pin){
        throw 'Keycloak source-build must install the exact libicu-67.1-10.el9_6.x86_64 prerequisite.'
    }
    $assertion=[regex]::Match($active,'(?m)(?:^[\t ]*RUN|&&)[\t ]+rpm[\t ]+-q[\t ]+'+[regex]::Escape($pin)+'(?=[\t ]*(?:&&|$))')
    $maven=[regex]::Match($active,'(?m)(?:^[\t ]*RUN|&&)[\t ]+mvn(?:[\t ]|$)')
    if(-not $assertion.Success -or -not $maven.Success -or $assertion.Index -le $install.Index -or $assertion.Index -ge $maven.Index){
        throw 'Keycloak source-build must assert the exact libicu package with rpm -q after installation and before Maven.'
    }
}

function Get-SourceRebuildLayoutFixture {
    [IO.File]::ReadAllText($DockerfilePath).Replace("`r`n","`n").Replace("`r","`n")
}

function Replace-SourceRebuildStageAnchor(
    [string]$Text,
    [ValidateSet('builder','final')][string]$Stage,
    [string]$Old,
    [AllowEmptyString()][string]$New
){
    $headers=[regex]::Matches($Text,'(?m)^FROM[\t ]+[^\n]+')
    if($headers.Count -ne 3 -or $headers[1].Value -notmatch '[\t ]AS[\t ]builder$' -or $headers[2].Value -match '[\t ]AS[\t ]'){
        throw 'Layout mutation requires exactly source-build, builder, and unnamed final stages.'
    }
    $index=if($Stage -eq 'builder'){1}else{2}
    $start=$headers[$index].Index
    $end=if($index -eq 1){$headers[2].Index}else{$Text.Length}
    $stageText=$Text.Substring($start,$end-$start)
    if([string]::IsNullOrEmpty($Old) -or [regex]::Matches($stageText,[regex]::Escape($Old)).Count -ne 1){
        throw ('Layout mutation anchor is not unique in '+$Stage+': '+$Old)
    }
    $mutated=$stageText.Replace($Old,$New)
    if($mutated -ceq $stageText){throw ('Layout mutation did not change '+$Stage)}
    $Text.Substring(0,$start)+$mutated+$Text.Substring($end)
}

function Require-SourceRebuildLayoutRejected([string]$Original,[string]$Mutant){
    # Resolve the new guard and accept the real fixture before the rejection
    # catch, so a missing/broken function cannot make a negative case green.
    Get-Command Assert-KeycloakSourceRebuildLayout -CommandType Function -ErrorAction Stop|Out-Null
    Assert-KeycloakSourceRebuildLayout -DockerfileText $Original|Out-Null
    if($Original -ceq $Mutant){throw 'Layout mutant is identical to its accepted baseline.'}
    $rejected=$false
    try{Assert-KeycloakSourceRebuildLayout -DockerfileText $Mutant|Out-Null}catch{$rejected=$true}
    if(-not $rejected){throw 'Unsafe source-rebuild runtime layout was accepted.'}
}

Check-RebuildCase 'source-rebuild-runtime-layout-accepted' {
    Get-Command Assert-KeycloakSourceRebuildLayout -CommandType Function -ErrorAction Stop|Out-Null
    Assert-KeycloakSourceRebuildLayout -DockerfileText (Get-SourceRebuildLayoutFixture)|Out-Null
}

$distributionCopies=@{
    builder='COPY --from=source-build --chown=1000:0 /rebuilt/keycloak/ /opt/keycloak/'
    final='COPY --from=builder --chown=1000:0 /opt/keycloak/ /opt/keycloak/'
}
$verifierCopy='COPY --chmod=755 source-build/verify-runtime.sh /usr/local/bin/verify-keycloak-source-build'
$mssqlAbsence="! find /opt/keycloak -type f -name 'com.microsoft.sqlserver.mssql-jdbc-*.jar' -print -quit | grep -q ."
foreach($stageName in @('builder','final')){
    $distributionCopy=$distributionCopies[$stageName]
    $wrongCopy=if($stageName -eq 'builder'){
        $distributionCopy.Replace('--from=source-build','--from=builder')
    }else{
        $distributionCopy.Replace('--from=builder','--from=source-build')
    }
    $layoutMutants=@(
        @{Name='old-root-delete-missing';Old="RUN rm -rf /opt/keycloak`n";New=''},
        @{Name='old-root-delete-after-copy';Old="RUN rm -rf /opt/keycloak`n$distributionCopy";New="$distributionCopy`nRUN rm -rf /opt/keycloak"},
        @{Name='root-user-missing';Old="USER root`n";New=''},
        @{Name='root-user-wrong';Old="USER root`n";New="USER 1000`n"},
        @{Name='root-user-after-delete';Old="USER root`nRUN rm -rf /opt/keycloak";New="RUN rm -rf /opt/keycloak`nUSER root"},
        @{Name='runtime-user-missing';Old="USER 1000`n";New=''},
        @{Name='runtime-user-root';Old="USER 1000`n";New="USER root`n"},
        @{Name='distribution-copy-missing';Old=$distributionCopy+"`n";New=''},
        @{Name='distribution-copy-wrong-source';Old=$distributionCopy;New=$wrongCopy},
        @{Name='cli-absence-missing';Old='test ! -e /opt/keycloak/bin/client';New='true'},
        @{Name='mssql-absence-missing';Old=$mssqlAbsence;New='true'},
        @{Name='verifier-copy-missing';Old=$verifierCopy+"`n";New=''},
        @{Name='verifier-execution-missing';Old='&& /usr/local/bin/verify-keycloak-source-build';New='&& true'}
    )
    foreach($mutation in $layoutMutants){
        Check-RebuildCase ('source-layout-'+$stageName+'-'+$mutation.Name+'-rejected') {
            $original=Get-SourceRebuildLayoutFixture
            $mutant=Replace-SourceRebuildStageAnchor -Text $original -Stage $stageName -Old $mutation.Old -New $mutation.New
            Require-SourceRebuildLayoutRejected -Original $original -Mutant $mutant
        }
    }
}

$builderOnlyMutants=@(
    @{Name='audit-copy-missing';Old="COPY --from=source-build --chown=1000:0 /audit/ /opt/keycloak/source-build-audit/`n";New=''},
    @{Name='cli-prune-missing';Old='rm -rf /opt/keycloak/bin/client';New='true'},
    @{Name='mssql-prune-missing';Old='rm -f /opt/keycloak/lib/lib/main/com.microsoft.sqlserver.mssql-jdbc-*.jar';New='true'},
    @{Name='keycloak-build-missing';Old='/opt/keycloak/bin/kc.sh build';New='true'}
)
foreach($mutation in $builderOnlyMutants){
    Check-RebuildCase ('source-layout-builder-'+$mutation.Name+'-rejected') {
        $original=Get-SourceRebuildLayoutFixture
        $mutant=Replace-SourceRebuildStageAnchor -Text $original -Stage builder -Old $mutation.Old -New $mutation.New
        Require-SourceRebuildLayoutRejected -Original $original -Mutant $mutant
    }
}
if($failures.Count){throw ('Keycloak source-build contracts failed: '+($failures -join '; '))}
Write-Host 'Keycloak source-build stage contracts passed.'
