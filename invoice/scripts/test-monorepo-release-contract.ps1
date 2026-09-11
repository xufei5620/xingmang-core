[CmdletBinding()]
param(
    [ValidateSet('All', 'PinnedRoot', 'GitProvenance')]
    [string]$Group = 'All',
    [string]$CaseId = '*',
    [string]$FixtureRoot = '',
    [string]$UpstreamScript = (Join-Path $PSScriptRoot 'check-upstream-integrity.ps1'),
    [string]$ProvenanceLibrary = (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$failures = [Collections.Generic.List[string]]::new()
$executedCases = 0
if ([string]::IsNullOrWhiteSpace($FixtureRoot)) {
    $FixtureRoot = Join-Path (Split-Path -Parent $PSScriptRoot) 'release/monorepo-test-fixtures'
}
$runRoot = Join-Path $FixtureRoot ([datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ') + '-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $runRoot
Write-Host "Monorepo fixture directory (retained): $runRoot"

function Invoke-FixtureGit {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string[]]$Arguments)
    $output = @(& git -C $Root @Arguments 2>&1)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "Fixture git $($Arguments -join ' ') failed with exit ${exitCode}: $($output -join ' ')"
    }
    return ($output -join "`n").Trim()
}

function New-MonorepoFixture {
    param([Parameter(Mandatory)][string]$Name, [switch]$Pinned, [switch]$Linked)
    $workspace = Join-Path $runRoot $Name
    $repo = Join-Path $workspace '01-core'
    $invoice = Join-Path $repo 'invoice'
    $platform = Join-Path $repo 'platform'
    $hooks = Join-Path $workspace 'empty-hooks'
    $null = New-Item -ItemType Directory -Path $invoice, $platform, $hooks
    Set-Content -LiteralPath (Join-Path $invoice 'tracked.txt') -Value 'invoice baseline'
    Set-Content -LiteralPath (Join-Path $platform 'tracked.txt') -Value 'platform baseline'
    $null = Invoke-FixtureGit -Root $repo -Arguments @('init', '--initial-branch=main')
    $null = Invoke-FixtureGit -Root $repo -Arguments @('add', '--', 'invoice', 'platform')
    $null = Invoke-FixtureGit -Root $repo -Arguments @(
        '-c', 'user.name=Monorepo contract fixture', '-c', 'user.email=fixture@example.invalid',
        '-c', 'commit.gpgsign=false', '-c', "core.hooksPath=$hooks", 'commit', '-m', 'fixture baseline'
    )
    $head = Invoke-FixtureGit -Root $repo -Arguments @('rev-parse', '--verify', 'HEAD')
    $pinnedRoot = Join-Path $workspace '06-upstream/pinned'
    if ($Pinned) { $null = New-Item -ItemType Directory -Path $pinnedRoot }
    if ($Linked) {
        $linkedRoot = Join-Path $workspace '09-wt/cutover'
        $null = Invoke-FixtureGit -Root $repo -Arguments @('worktree', 'add', '--detach', $linkedRoot, 'HEAD')
        $invoice = Join-Path $linkedRoot 'invoice'
    }
    return [pscustomobject]@{
        Workspace = $workspace; Repository = $repo; Invoice = $invoice
        Platform = $platform; PinnedRoot = $pinnedRoot; Head = $head
    }
}

function Invoke-MonorepoCase {
    param([Parameter(Mandatory)][string]$Id, [Parameter(Mandatory)][scriptblock]$Action)
    if ($CaseId -ne '*' -and $CaseId -cne $Id) { return }
    $script:executedCases++
    try {
        & $Action
        Write-Host "PASS $Id"
    } catch {
        $script:failures.Add("${Id}: $($_.Exception.Message)")
        Write-Host "FAIL ${Id}: $($_.Exception.Message)"
    }
}

function Assert-ContractThrows {
    param([Parameter(Mandatory)][scriptblock]$Action, [Parameter(Mandatory)][string]$Pattern)
    $caught = $null
    try { & $Action | Out-Null } catch { $caught = $_ }
    if ($null -eq $caught) { throw "Expected failure matching '$Pattern'; call succeeded" }
    if ($caught.Exception.Message -notmatch $Pattern) {
        throw "Expected failure matching '$Pattern'; got '$($caught.Exception.Message)'"
    }
}

function Assert-ExactDirectory {
    param([Parameter(Mandatory)]$Actual, [Parameter(Mandatory)][string]$Expected)
    if ($Actual -isnot [string] -or
        [IO.Path]::GetFullPath($Actual).TrimEnd([IO.Path]::DirectorySeparatorChar) -cne
        [IO.Path]::GetFullPath($Expected).TrimEnd([IO.Path]::DirectorySeparatorChar)) {
        throw "Expected exact pinned directory '$Expected'; got '$Actual'"
    }
}

function Assert-FixtureProvenance {
    param([Parameter(Mandatory)]$Fixture, [Parameter(Mandatory)][bool]$Dirty)
    $actual = Get-ReleaseGitProvenance -RepositoryRoot $Fixture.Invoice
    if ($actual.GitHead -isnot [string] -or $actual.GitHead -cnotmatch '^[0-9a-f]{40}$' -or
        $actual.GitHead -cne $Fixture.Head) {
        throw "Expected full monorepo HEAD '$($Fixture.Head)'; got '$($actual.GitHead)'"
    }
    if ($actual.GitDirty -isnot [bool] -or $actual.GitDirty -ne $Dirty) {
        throw "Expected invoice gitDirty=$Dirty; got '$($actual.GitDirty)'"
    }
}

$previousUpstreamRoot = $env:INVOICE_UPSTREAM_ROOT
$previousGitIndexFile = $env:GIT_INDEX_FILE
try {
    if ($Group -in @('All', 'PinnedRoot')) {
        # Execute the production resolver without executing its repository checks.
        # Missing/invalid production functions fail outside negative assertions.
        $tokens = $null
        $parseErrors = $null
        $ast = [Management.Automation.Language.Parser]::ParseFile($UpstreamScript, [ref]$tokens, [ref]$parseErrors)
        if ($parseErrors.Count -gt 0) { throw "Upstream script parse errors: $($parseErrors -join '; ')" }
        $resolver = $ast.Find({
            param($node)
            $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq 'Resolve-InvoiceUpstreamRoot'
        }, $true)
        if ($null -eq $resolver) { throw 'Missing production function Resolve-InvoiceUpstreamRoot; pinned-root cases cannot execute' }
        . ([scriptblock]::Create($resolver.Extent.Text))

        Invoke-MonorepoCase -Id 'pinned-env-override' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-env-override' -Pinned
            $override = Join-Path $fixture.Workspace 'explicit-pinned'
            $null = New-Item -ItemType Directory -Path $override
            $env:INVOICE_UPSTREAM_ROOT = $override
            Assert-ExactDirectory -Actual (Resolve-InvoiceUpstreamRoot -ProjectRoot $fixture.Invoice) -Expected $override
        }
        Invoke-MonorepoCase -Id 'pinned-missing-override-fails' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-missing-override-fails' -Pinned
            $env:INVOICE_UPSTREAM_ROOT = Join-Path $fixture.Workspace 'absent-override'
            Assert-ContractThrows -Action { Resolve-InvoiceUpstreamRoot -ProjectRoot $fixture.Invoice } -Pattern '(?i)(INVOICE_UPSTREAM_ROOT|pinned|upstream).*(missing|not found|exist|directory|resolve)'
        }
        Invoke-MonorepoCase -Id 'pinned-relative-override-fails' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-relative-override-fails' -Pinned
            $env:INVOICE_UPSTREAM_ROOT = '06-upstream/pinned'
            # The relative directory exists, so rejecting it cannot accidentally
            # pass because a later missing-directory guard rejects it instead.
            Push-Location -LiteralPath $fixture.Workspace
            $previousProcessDirectory = [Environment]::CurrentDirectory
            try {
                [Environment]::CurrentDirectory = $fixture.Workspace
                Assert-ContractThrows -Action { Resolve-InvoiceUpstreamRoot -ProjectRoot $fixture.Invoice } -Pattern '(?i)(absolute|fully.qualified|rooted)'
            } finally {
                [Environment]::CurrentDirectory = $previousProcessDirectory
                Pop-Location
            }
        }
        Invoke-MonorepoCase -Id 'pinned-file-override-fails' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-file-override-fails' -Pinned
            $env:INVOICE_UPSTREAM_ROOT = Join-Path $fixture.Invoice 'tracked.txt'
            Assert-ContractThrows -Action { Resolve-InvoiceUpstreamRoot -ProjectRoot $fixture.Invoice } -Pattern '(?i)(INVOICE_UPSTREAM_ROOT|pinned|upstream).*(directory|container)'
        }
        Invoke-MonorepoCase -Id 'pinned-default-main' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-default-main' -Pinned
            $env:INVOICE_UPSTREAM_ROOT = $null
            Assert-ExactDirectory -Actual (Resolve-InvoiceUpstreamRoot -ProjectRoot $fixture.Invoice) -Expected $fixture.PinnedRoot
        }
        Invoke-MonorepoCase -Id 'pinned-default-linked' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-default-linked' -Pinned -Linked
            $env:INVOICE_UPSTREAM_ROOT = $null
            Assert-ExactDirectory -Actual (Resolve-InvoiceUpstreamRoot -ProjectRoot $fixture.Invoice) -Expected $fixture.PinnedRoot
        }
        Invoke-MonorepoCase -Id 'pinned-no-latest-fallback' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-no-latest-fallback'
            $null = New-Item -ItemType Directory -Path (Join-Path $fixture.Workspace '06-upstream/new-api'), (Join-Path $fixture.Workspace '06-upstream/sub2api')
            $env:INVOICE_UPSTREAM_ROOT = $null
            Assert-ContractThrows -Action { Resolve-InvoiceUpstreamRoot -ProjectRoot $fixture.Invoice } -Pattern '(?i)(pinned|upstream).*(missing|not found|exist|directory|resolve)'
        }
        Invoke-MonorepoCase -Id 'pinned-git-error-fails' -Action {
            $fixture = New-MonorepoFixture -Name 'pinned-git-error-fails' -Pinned
            $notRepository = Join-Path $fixture.Workspace 'not-a-repository'
            $null = New-Item -ItemType Directory -Path $notRepository
            $env:INVOICE_UPSTREAM_ROOT = $null
            $previousGitCeiling = $env:GIT_CEILING_DIRECTORIES
            try {
                # The default fixture lives inside the real monorepo. Stop Git
                # discovery at this fixture instead of finding that parent repo.
                $env:GIT_CEILING_DIRECTORIES = $fixture.Workspace
                Assert-ContractThrows -Action { Resolve-InvoiceUpstreamRoot -ProjectRoot $notRepository } -Pattern '(?i)git.*(exit|failed|resolve|common)'
            } finally {
                $env:GIT_CEILING_DIRECTORIES = $previousGitCeiling
            }
        }
    }

    if ($Group -in @('All', 'GitProvenance')) {
        . $ProvenanceLibrary
        Invoke-MonorepoCase -Id 'provenance-clean-full-head' -Action {
            $fixture = New-MonorepoFixture -Name 'provenance-clean-full-head'
            Assert-FixtureProvenance -Fixture $fixture -Dirty $false
        }
        foreach ($change in @('tracked', 'untracked', 'staged')) {
            Invoke-MonorepoCase -Id "provenance-platform-$change" -Action {
                $fixture = New-MonorepoFixture -Name "provenance-platform-$change"
                $name = if ($change -eq 'untracked') { 'new.txt' } else { 'tracked.txt' }
                Set-Content -LiteralPath (Join-Path $fixture.Platform $name) -Value "platform $change modification"
                if ($change -eq 'staged') { $null = Invoke-FixtureGit -Root $fixture.Repository -Arguments @('add', '--', 'platform/tracked.txt') }
                Assert-FixtureProvenance -Fixture $fixture -Dirty $false
            }
            Invoke-MonorepoCase -Id "provenance-invoice-$change" -Action {
                $fixture = New-MonorepoFixture -Name "provenance-invoice-$change"
                $name = if ($change -eq 'untracked') { 'new.txt' } else { 'tracked.txt' }
                Set-Content -LiteralPath (Join-Path $fixture.Invoice $name) -Value "invoice $change modification"
                if ($change -eq 'staged') { $null = Invoke-FixtureGit -Root $fixture.Repository -Arguments @('add', '--', 'invoice/tracked.txt') }
                Assert-FixtureProvenance -Fixture $fixture -Dirty $true
            }
        }
        Invoke-MonorepoCase -Id 'provenance-linked-full-head' -Action {
            $fixture = New-MonorepoFixture -Name 'provenance-linked-full-head' -Linked
            $linkedPlatform = Join-Path (Split-Path -Parent $fixture.Invoice) 'platform/tracked.txt'
            Set-Content -LiteralPath $linkedPlatform -Value 'linked platform modification'
            Assert-FixtureProvenance -Fixture $fixture -Dirty $false
        }
        Invoke-MonorepoCase -Id 'provenance-git-status-error-fails' -Action {
            $fixture = New-MonorepoFixture -Name 'provenance-git-status-error-fails'
            $invalidIndex = Join-Path $fixture.Workspace 'invalid-index-directory'
            $null = New-Item -ItemType Directory -Path $invalidIndex
            try {
                $env:GIT_INDEX_FILE = $invalidIndex
                Assert-ContractThrows -Action { Get-ReleaseGitProvenance -RepositoryRoot $fixture.Invoice } -Pattern 'git status.*exit'
            } finally { $env:GIT_INDEX_FILE = $previousGitIndexFile }
        }
        Invoke-MonorepoCase -Id 'provenance-git-head-error-fails' -Action {
            $unborn = Join-Path $runRoot 'provenance-git-head-error-fails'
            $invoice = Join-Path $unborn 'invoice'
            $null = New-Item -ItemType Directory -Path $invoice
            $null = Invoke-FixtureGit -Root $unborn -Arguments @('init', '--initial-branch=main')
            Assert-ContractThrows -Action { Get-ReleaseGitProvenance -RepositoryRoot $invoice } -Pattern '(?i)git.*(HEAD|rev-parse).*(exit|failed|valid)'
        }
    }
} finally {
    $env:INVOICE_UPSTREAM_ROOT = $previousUpstreamRoot
    $env:GIT_INDEX_FILE = $previousGitIndexFile
}

if ($executedCases -eq 0) { throw "No monorepo cases matched group '$Group' and case '$CaseId'" }
if ($failures.Count -gt 0) {
    throw "Monorepo contract failures ($($failures.Count)/$executedCases):`n- $($failures -join "`n- ")"
}
Write-Host "Monorepo release contract fixtures passed ($executedCases cases)."
$global:LASTEXITCODE = 0
