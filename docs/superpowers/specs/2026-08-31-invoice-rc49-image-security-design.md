# RC49 Image Security Remediation Design

> **SUPERSEDED — DO NOT EXECUTE.** This design is retained as historical RC49
> evidence. Current execution is governed only by
> `docs/superpowers/plans/2026-09-01-invoice-rc65-release-recovery.md`.

## Approval and goal

The user approved the independent security patch slice on 2026-08-31. The goal is to remediate the image findings that blocked RC48, retain only a genuinely no-fix vendor-rejected finding under an exact evidence-bound exception, generate a new RC49 candidate without changing RC48 evidence, and deploy RC49 through the existing production runbook.

## Verified starting facts

The production-versus-RC48 comparison is retained outside Git at `release/0.1.0-rc48-production-comparison-20260831-exact1`. It used the exact RC48 Trivy 0.74.0 image, cache database timestamps, serial scan parameters, `HIGH,CRITICAL` severity set, and `ignoreUnfixed=false` policy.

For every like-for-like production runtime service, the canonical finding set is identical to RC48. API and Web have different image IDs but the same finding tuples. PDF scanner, Keycloak, PostgreSQL, ClamAV, and ingest Nginx use the same image IDs. Source agent is zero-finding in both releases. RC48 additionally contains a non-runtime tools image with the same OpenSSL finding. Therefore RC48 remains correctly blocked, but blocking it does not reduce the exposure already present in production; remediation is urgent.

## Non-negotiable policy

- `release/0.1.0-rc48-exact1` and its failed manifest, reports, SBOMs, logs, and checksums remain byte-for-byte historical evidence.
- Trivy remains pinned to `0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969`.
- Scans remain serial, `--scanners vuln --severity HIGH,CRITICAL`, with `ignoreUnfixed=false` and no ignored vulnerability list.
- No severity reduction, `--ignore-unfixed`, generic allowlist, wildcard digest, mutable production image tag, or rewritten Trivy report is permitted.
- A finding with a non-empty upstream `FixedVersion` must be fixed. It cannot use an exception merely because reachability appears low.
- An exception may approve only an exact, reviewed no-fix or vendor-wontfix tuple. It must bind the original raw report, immutable base and derived image IDs, package and installed version, CVE, severity, status, fixed-version state, rationale, evidence links, review timestamp, and review deadline. Any drift or expiry fails closed.

## Remediation architecture

### Alpine application images

Keep the reviewed Alpine and Nginx base digests, but install the official Alpine v3.24 fixed packages `libcrypto3=3.5.8-r0` and `libssl3=3.5.8-r0` in the final runtime stages. Backend API/tools and PDF scanner inherit the fixed packages from their respective base stages. Web installs them in its final Nginx stage. Existing qpdf and application binary pins remain unchanged.

### Derived runtime images

External production services become release-tagged, locally built images with `pull_policy: never`:

- `invoice-postgres:<release>` derives from the exact PostgreSQL 18.6 Alpine digest. It installs the fixed OpenSSL packages and replaces `/usr/local/bin/gosu` with gosu 1.19 built from commit `6456aaa0f3c854d199d0f037f068eb97515b7513` by the existing pinned Go 1.25.13 builder. Go module sums and the resulting version/platform are verified during build. All 22 gosu stdlib findings have upstream fixed versions, so RC49 must not use the legacy gosu exception.
- `invoice-clamav:<release>` derives from the existing exact ClamAV 1.4.5 digest and installs the fixed OpenSSL packages. It retains the current supported LTS runtime behavior and health contract.
- `invoice-ingest-proxy:<release>` derives from the exact Nginx 1.30.4 Alpine digest and installs the fixed OpenSSL packages.

Both application and Keycloak PostgreSQL services use the same RC49 `invoice-postgres` image. Production Compose continues to contain no `build:` directives.

### Keycloak

Refresh the exact Keycloak 26.7.2 base to the official post-release rebuild:

`quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067`

The existing final-image pruning removes the unused MSSQL JDBC driver, eliminating its fixable finding. The final derived image retains exactly one raw finding:

- target class/type: `os-pkgs` / `redhat`
- CVE: `CVE-2026-22020`
- package: `java-21-openjdk-headless`
- installed version: `1:21.0.12.1.1-1.2.el9`
- fixed version: empty
- severity/status: `HIGH` / `affected`

This tuple may pass only through the existing vendor-rejection mechanism. Its disposition is `vendor_rejected_not_affected`: Red Hat rejected the CVE and the supporting analysis limits the affected bundled libpng path to Oracle proprietary JDK rather than this OpenJDK/system-libpng runtime. The review is dated `2026-08-31T00:00:00Z` and expires at `2026-09-30T00:00:00Z`. After expiry, or on any tuple/digest change, the gate blocks until a new review is committed.

### Exception mechanism

The PostgreSQL mechanism remains in the codebase as a fail-closed exact mechanism, but no PostgreSQL exception is approved for RC49 because every RC48 PostgreSQL finding has a fixed version. Tests must explicitly reject the old fixed-version gosu fixture as exception-eligible.

The Keycloak exception record and the independent artifact verifier must validate every field listed above plus the current derived image ID, base digest, pruning proof, runtime smoke, and review window. The raw Trivy finding remains unchanged in its report.

## Release and deployment flow

1. Run offline/static tests, full `scripts/verify.ps1`, image builds, Trivy/SBOM generation, runtime smoke, realm provisioning, and the independent artifact verifier from a clean signed RC49 source commit.
2. The existing image gate may correctly report `image_approved_pending_canary` before production canaries; that is not a bypass. Transfer only manifest-bound images to the server and follow the runbook's backup, isolated start, migration, and canary sequence.
3. Prove real OIDC discovery, MFA, RP logout, back-channel logout, platform-password login, Sub2API 2FA, API/Web health, PDF/ClamAV, source-agent health, backup integrity, and rollback evidence.
4. Retain final deployment evidence bound to `v0.1.0-rc49-signed`. Do not retag or edit RC48.

## Failure behavior

Any build, scan, manifest-binding, signature, backup, migration, health, or canary failure stops the release. Failed artifacts remain retained. Production traffic and the existing production deployment remain unchanged until the corresponding runbook cutover gate passes.
