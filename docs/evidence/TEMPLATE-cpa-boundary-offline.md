# CPA boundary offline evidence (R213-1)

> This is a redacted, local evidence template. It is not a live probe and
> must never contain management keys, certificates, tokens, passwords, raw
> response bodies, DSNs, private IP inventories, or OAuth state/code values.

## Required capture

- `version: 1`, UTC `observed_at`, exact CPA target version and image digest.
- Exact route-inventory digest from the approved target build (the checked-in
  zero digest is a placeholder and must produce `partial`, never `pass`).
- Redacted CPA config projection proving `allow_remote=false`,
  `MANAGEMENT_PASSWORD` absent, and raw port 8317 bound to loopback.
- Process/container bind projection and a no-secret process argument/environment
  projection.
- Public route projection with separate, exact inference and per-instance
  callback hostnames; callback is only GET/POST
  `/v0/management/oauth-callback`.
- Firewall/security-group projection covering raw-port denial on IPv4 and IPv6.
- Private adapter projection proving non-public listen, mTLS, no redirect, and
  no secret in config/arguments.

## Honest status

`cmd/cpa-boundary-audit` reports only stable finding codes and SHA-256 digests.
Missing target/version/route facts, placeholder inventory digests, or omitted
IPv6/config/process projections are `partial`/fail-closed. A clean offline
report is not network isolation evidence; R213-2 disposable lab and R213-3
staging attestation remain `not_run`.
