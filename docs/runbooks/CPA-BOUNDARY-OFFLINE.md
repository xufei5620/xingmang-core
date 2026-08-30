# CPA boundary offline audit runbook (R213-1)

This runbook describes the local, read-only auditor. It never connects to CPA,
SSH, Docker, a browser, a firewall, or a credential provider. A reviewer must
explicitly name two regular local files: the boundary contract and a redacted
evidence bundle.

```text
go run ./cmd/cpa-boundary-audit \
  --boundary contracts/cpa/management-boundary.v1.json \
  --evidence ./evidence/cpa-boundary.json \
  --out ./evidence/cpa-boundary-report.json
```

The command exits `0` only for a complete report with no findings. Malformed or
unsafe input exits `1`; missing/invalid paths and output errors exit `2`.
`--out` is create-only (`O_EXCL`) so an existing report cannot be silently
overwritten. URL, UNC, NUL, traversal, symlink, and non-regular paths are
rejected.

## Review gates

1. The contract must load with strict unknown/duplicate-field rejection. It
   contains exact paths only; no wildcard, generic proxy, redirect, encoded
   path, write, plugin, credential, auth, config, or usage capability is
   admitted.
2. The callback exception is limited to the per-instance callback hostname and
   exact GET/POST `/v0/management/oauth-callback`; the inference hostname
   denies the complete management prefix.
3. Evidence must identify one exact target version/image/route digest and UTC
   freshness. The checked-in zero route digest is explicitly a placeholder and
   always yields `partial` until replaced by target evidence.
4. Findings contain stable codes/resource classes/remediation and a redacted
   evidence digest only. Never paste source lines or secret-bearing values into
   an issue, log, or handoff.

## Not authorized by R213-1

No adapter, callback forwarder, network lab, CPA request, management key or
certificate resolution, route scan, SSH command, firewall change, Compose
change, deployment, or production/staging attestation. Those require the
separately approved R213-2/R213-3 slices and evidence.

