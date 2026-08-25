$ErrorActionPreference = 'Stop'

function Invoke-InviteStateFixture {
    param([Parameter(Mandatory)][string]$Scenario)

    $operationNonce = 'operation-a'
    $state = [ordered]@{
        RealmRevision = 1
        SMTP = 'empty'
        User = $null
        InvitationSent = $false
        FailedClosed = $false
        ExternalChangePreserved = $true
    }
    $baselineRevision = $state.RealmRevision
    $userMutationAttempted = $false
    $smtpMutationAttempted = $false

    function Rollback {
        if ($userMutationAttempted -and $null -ne $state.User) {
            if ($state.User.Nonce -eq $operationNonce) {
                $state.User = $null
            } else {
                $state.FailedClosed = $true
            }
        }
        if ($smtpMutationAttempted) {
            if ($state.SMTP -eq 'source') {
                # Fresh-current restoration changes only SMTP. Any concurrent
                # non-SMTP revision is retained.
                $state.SMTP = 'empty'
            } elseif ($state.SMTP -ne 'empty') {
                $state.FailedClosed = $true
            }
        }
    }

    try {
        $userMutationAttempted = $true
        $state.User = [ordered]@{ Nonce = $operationNonce; Role = 'admin' }
        if ($Scenario -eq 'post-response-lost') { throw 'lost response after create' }
        if ($Scenario -eq 'nonce-mismatch') {
            $state.User.Nonce = 'parallel-winner'
            throw 'parallel create won'
        }
        if ($Scenario -eq 'concurrent-before-put') {
            $state.RealmRevision++
        }
        if ($state.RealmRevision -ne $baselineRevision -or $state.SMTP -ne 'empty') {
            throw 'pre-PUT canonical drift'
        }

        $smtpMutationAttempted = $true
        $state.SMTP = 'source'
        if ($Scenario -eq 'put-response-lost') { throw 'lost response after SMTP PUT' }
        if ($state.RealmRevision -ne $baselineRevision -or $state.SMTP -ne 'source') {
            throw 'post-PUT canonical drift'
        }

        $state.InvitationSent = $true
        if ($Scenario -eq 'execute-response-lost') { throw 'lost response after execute-actions' }
        if ($Scenario -eq 'concurrent-before-restore') {
            $state.RealmRevision++
        }
        if ($state.RealmRevision -ne $baselineRevision) {
            throw 'pre-restore canonical drift'
        }
        $state.SMTP = 'empty'
        $smtpMutationAttempted = $false
    } catch {
        Rollback
        $state.FailedClosed = $true
    }
    return [pscustomobject]$state
}

$fixtures = @(
    @{ Name = 'post-response-lost'; UserPresent = $false; SMTP = 'empty'; Failed = $true },
    @{ Name = 'put-response-lost'; UserPresent = $false; SMTP = 'empty'; Failed = $true },
    @{ Name = 'execute-response-lost'; UserPresent = $false; SMTP = 'empty'; Failed = $true },
    @{ Name = 'concurrent-before-put'; UserPresent = $false; SMTP = 'empty'; Failed = $true; Revision = 2 },
    @{ Name = 'concurrent-before-restore'; UserPresent = $false; SMTP = 'empty'; Failed = $true; Revision = 2 },
    @{ Name = 'nonce-mismatch'; UserPresent = $true; SMTP = 'empty'; Failed = $true }
)

foreach ($fixture in $fixtures) {
    $result = Invoke-InviteStateFixture -Scenario $fixture.Name
    if (($null -ne $result.User) -ne $fixture.UserPresent -or
        $result.SMTP -ne $fixture.SMTP -or
        $result.FailedClosed -ne $fixture.Failed) {
        throw "negative state fixture failed: $($fixture.Name)"
    }
    if ($fixture.ContainsKey('Revision') -and $result.RealmRevision -ne $fixture.Revision) {
        throw "concurrent state was overwritten: $($fixture.Name)"
    }
    if ($fixture.Name -eq 'nonce-mismatch' -and $result.User.Nonce -ne 'parallel-winner') {
        throw 'nonce mismatch deleted the parallel winner'
    }
}

Write-Host 'Permanent master administrator response-loss, nonce and concurrent-drift state fixtures passed.'
$global:LASTEXITCODE = 0
