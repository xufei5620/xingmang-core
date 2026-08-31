# Docker Compose v5 optional network Boolean fix

## Scope

Docker Compose v5.3.1 may omit `networks.<name>.internal` from rendered JSON when the reviewed value is `false`. Under `Set-StrictMode -Version Latest`, direct property reads in `scripts/verify.ps1` then fail before the release gate can compare the intended network boundary.

## Change

- Added `Get-ComposeOptionalBoolean` to `scripts/release-image-gate-lib.ps1`.
- A missing Boolean property returns the reviewed Compose default, `false`.
- An explicitly present value must be a real PowerShell Boolean; null, strings, numbers, and objects are rejected.
- Routed all five `verify.ps1` network `internal` checks through the helper, including the ClamAV, OIDC preflight, and Keycloak boundaries. Explicit `true` boundaries remain enforced.

## TDD evidence

1. Added pure fixtures for absent, true, false, and invalid `internal` values, plus a static assertion that `verify.ps1` uses the helper for all five network checks.
2. RED: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1` failed because `Get-ComposeOptionalBoolean` did not exist.
3. GREEN: the same command passed after the minimal helper and call-site changes.

## Focused verification

- `pwsh -NoProfile -File scripts/test-release-image-gate.ps1` — passed.
- PowerShell AST parse for the changed scripts — passed with zero parse errors.
- `git diff --check` — passed.
- `gitleaks git --staged --redact .` — passed with no leaks.
- Docker and full `scripts/verify.ps1` execution were intentionally not run for this focused source-fixture repair.
