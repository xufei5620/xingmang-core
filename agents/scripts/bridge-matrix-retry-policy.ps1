function Test-BridgeMatrixTransientHostPortFailure {
    param([AllowEmptyString()][string]$Text)

    $options = [Text.RegularExpressions.RegexOptions]::IgnoreCase -bor
        [Text.RegularExpressions.RegexOptions]::CultureInvariant
    $connectionPattern = 'dial tcp 127\.0\.0\.1:[0-9]+[^\r\n]*(?:connectex|connection refused|no connection could be made)'
    if (-not [regex]::IsMatch($Text, $connectionPattern, $options)) { return $false }

    # Retry only when every concrete Go test diagnostic is the same explicit
    # loopback host-port failure. A mixed assertion plus a later NAT failure is
    # deterministic test evidence and must remain fail-closed.
    $diagnostics = [regex]::Matches(
        $Text,
        '^\s*[^\r\n]*_test\.go:[0-9]+:\s*(?<message>[^\r\n]+)$',
        $options -bor [Text.RegularExpressions.RegexOptions]::Multiline
    )
    if ($diagnostics.Count -eq 0) { return $false }
    foreach ($diagnostic in $diagnostics) {
        if (-not [regex]::IsMatch($diagnostic.Groups['message'].Value, $connectionPattern, $options)) {
            return $false
        }
    }
    return $true
}

function Complete-BridgeMatrixAttempt {
    param(
        [Parameter(Mandatory)][string]$ContainerName,
        [Parameter(Mandatory)][int]$CleanupExitCode,
        [AllowNull()]$AttemptError
    )

    if ($CleanupExitCode -ne 0) {
        if ($null -ne $AttemptError) {
            $attemptMessage = if ($AttemptError -is [Management.Automation.ErrorRecord]) {
                $AttemptError.Exception.Message
            } elseif ($AttemptError -is [Exception]) {
                $AttemptError.Message
            } else {
                [string]$AttemptError
            }
            throw "Bridge matrix attempt failed ($attemptMessage); cleanup of container $ContainerName also failed with exit code $CleanupExitCode."
        }
        throw "Bridge matrix cleanup of container $ContainerName failed with exit code $CleanupExitCode."
    }
    if ($null -ne $AttemptError) { throw $AttemptError }
}
