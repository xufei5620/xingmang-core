# RC49 Image Security Remediation Implementation Plan

> **SUPERSEDED — DO NOT EXECUTE.** RC49 failed and is retained as historical
> evidence. Continue only with
> `docs/superpowers/plans/2026-09-03-invoice-rc78-release-recovery.md`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce and deploy a new RC49 whose fixable HIGH/CRITICAL image findings are removed and whose only residual finding is the exact, time-bounded Keycloak vendor-rejected tuple.

**Architecture:** Patch final Alpine packages in application images, build release-tagged derived PostgreSQL/ClamAV/Nginx images, rebuild gosu 1.19 with Go 1.25.13, refresh the exact Keycloak rebuild digest, and strengthen the existing exception/verifier contract. Keep the two-phase production canary gate and all historical RC48 evidence unchanged.

**Tech Stack:** Docker/BuildKit, PowerShell 7, Trivy 0.74.0, Go 1.25.13, Docker Compose, Keycloak 26.7.2, PostgreSQL 18.6, Alpine 3.24.

**Spec:** `docs/superpowers/specs/2026-08-31-invoice-rc49-image-security-design.md`

## Global Constraints

- Do not modify any file below `release/0.1.0-rc48-exact1`.
- Trivy stays at the exact reviewed digest; severity stays `HIGH,CRITICAL`; `ignoreUnfixed` stays `false`; ignored vulnerabilities stay empty.
- Every finding with non-empty `FixedVersion` must be fixed, not excepted.
- The only approved RC49 exception tuple is Keycloak `CVE-2026-22020` on `java-21-openjdk-headless@1:21.0.12.1.1-1.2.el9`, empty fixed version, `HIGH`, `affected`, exact base digest `sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067`.
- Exception review window is exactly `2026-08-31T00:00:00Z` through `2026-09-30T00:00:00Z`; expiry and drift fail closed.
- PostgreSQL RC49 must be zero-finding; the legacy gosu exception must reject the old fixed-version fixture.
- Production Compose has no `build:` entries and uses one exact release tag with `pull_policy: never` for every locally built image.

---

### Task 1: Enforce exact exception tuples and expiry with TDD

**Files:**
- Modify: `scripts/test-release-image-gate.ps1`
- Modify: `scripts/fixtures/release-image-gate/keycloak-vendor-rejected-cve-report.json`
- Modify: `scripts/release-image-gate-lib.ps1`
- Modify: `scripts/release-image-gate.ps1`
- Modify: `scripts/verify-release-image-artifacts.ps1`
- Test: `scripts/test-release-image-gate.ps1`

**Interfaces:**
- Consumes: existing `Assert-PostgresGosuFindingScope`, `Assert-KeycloakVendorRejectedCveScope`, and `Get-IdpGateDecision` functions.
- Produces: `Assert-ExceptionReviewContract`, the refreshed exact Keycloak exception, and fail-closed PostgreSQL fixed-version rejection used by later tasks.

- [ ] **Step 1: Add failing tests for fixed-version rejection**

Mutate the PostgreSQL fixture so its current non-empty `FixedVersion` is passed to `Assert-PostgresGosuFindingScope`; assert that the unmodified current fixture is rejected as exception-ineligible. Add explicit negative cases for CVE, class/type, installed version, status, and fixed-version drift.

- [ ] **Step 2: Add failing tests for the refreshed Keycloak tuple**

Update the fixture to `1:21.0.12.1.1-1.2.el9`, then assert exact acceptance and rejection for base digest, derived image ID, CVE, package, installed version, fixed version, severity, status, class, and type.

- [ ] **Step 3: Add failing tests for review metadata**

Exercise a pure review-contract function with the fixed clock `2026-08-31T12:00:00Z`. Missing rationale, missing timestamps, invalid timestamps, `reviewDueAt <= reviewedAt`, and an expired due date must fail. The exact approved window must pass.

- [ ] **Step 4: Run RED verification**

Run: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1`

Expected: FAIL because fixed-version rejection, refreshed tuple, or review-window enforcement is not implemented.

- [ ] **Step 5: Implement `Assert-ExceptionReviewContract`**

Accept rationale, reviewed-at, review-due-at, and current time. Parse invariant ISO-8601 timestamps, require non-empty rationale, require due-after-review, and reject when current time is after the deadline.

- [ ] **Step 6: Tighten PostgreSQL scope**

Reject every finding with a non-empty `FixedVersion`, any status other than an explicitly reviewed no-fix status, or any tuple not present in an exact reviewed tuple list. With no approved PostgreSQL no-fix tuple in RC49, the RC48 fixture must fail.

- [ ] **Step 7: Refresh and bind the Keycloak exception**

Use base digest `sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067`, installed OpenJDK version `1:21.0.12.1.1-1.2.el9`, and exact review timestamps. Retain the raw report and existing Red Hat/AWS rationale links. Include review metadata and rationale in the manifest exception.

- [ ] **Step 8: Make the independent verifier revalidate every exception field**

Verify base and derived IDs, target/class/type, CVE/package/installed/fixed/severity/status, disposition, evidence URLs, rationale, review timestamps, pruning/runtime proof hashes, and expiry.

- [ ] **Step 9: Run GREEN verification**

Run: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1`

Expected: `Release image gate offline/static fixtures passed.` and exit 0.

- [ ] **Step 10: Commit**

Commit message: `fix(security): 收紧镜像漏洞精确例外`

### Task 2: Add release-tagged fixed runtime images

**Files:**
- Create: `deploy/postgres/Dockerfile`
- Create: `deploy/postgres/gosu-build/go.mod`
- Create: `deploy/postgres/gosu-build/go.sum`
- Create: `deploy/clamav/Dockerfile`
- Create: `deploy/ingest-proxy/Dockerfile`
- Modify: `backend/Dockerfile`
- Modify: `web/Dockerfile`
- Modify: `deploy/keycloak/Dockerfile`
- Modify: `deploy/docker-compose.prod.yml`
- Modify: `deploy/docker-compose.idp.yml`
- Modify: `scripts/test-release-image-gate.ps1`

**Interfaces:**
- Consumes: exact base digests and fixed package versions from the spec, plus Task 1's exception contract.
- Produces: `invoice-postgres`, `invoice-clamav`, `invoice-ingest-proxy`, patched application images, and refreshed Keycloak image.

- [ ] **Step 1: Add static failing assertions**

Assert exact package pins, gosu commit pseudo-version and module sums, Go builder digest, Keycloak digest, local Compose image references, `pull_policy: never`, and absence of the three old external production image references.

- [ ] **Step 2: Run RED verification**

Run: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1`

Expected: FAIL because the derived Dockerfiles and Compose references do not exist.

- [ ] **Step 3: Implement application package fixes**

Install `libcrypto3=3.5.8-r0` and `libssl3=3.5.8-r0` in backend `api-base`, backend `scanner-base`, and the final Web Nginx stage. Preserve qpdf 12.3.2 and all existing users, permissions, entrypoints, and base digests.

- [ ] **Step 4: Implement the PostgreSQL image**

Build gosu 1.19 from pseudo-version `v0.0.0-20250923190938-6456aaa0f3c8` with pinned Go 1.25.13, `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and read-only module mode. Copy it over the exact PostgreSQL 18.6 base binary and assert `1.19 (go1.25.13 on linux/amd64; gc)`. Install the exact fixed OpenSSL packages.

Use this exact module file:

```go.mod
module invoice.local/gosu-build

go 1.25.0

require github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8

require (
	github.com/moby/sys/user v0.1.0 // indirect
	golang.org/x/sys v0.1.0 // indirect
)
```

Use these exact module sums:

```text
github.com/moby/sys/user v0.1.0 h1:WmZ93f5Ux6het5iituh9x2zAG7NFY9Aqi49jjE1PaQg=
github.com/moby/sys/user v0.1.0/go.mod h1:fKJhFOnsCN6xZ5gSfbM6zaHGgDJMrqt9/reuj4T7MmU=
github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8 h1:HIpXk5mGBQGfOqcaBbRT4Vnss8NPICnMGlD5xTlPBdQ=
github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8/go.mod h1:SwhRwWsO6iqXZN9CpIaU9CnOrUqpWDINW16KaaSqnrU=
golang.org/x/sys v0.1.0 h1:kunALQeHf1/185U1i0GOB/fy1IPRDDpuoOOqRReG57U=
golang.org/x/sys v0.1.0/go.mod h1:oPkhp1MJrh7nUepCBck5+mAzfO9JrbApNNgaTdGDITg=
```

- [ ] **Step 5: Implement ClamAV and ingest images**

Derive from the current exact digests, install the two fixed OpenSSL packages, and assert ClamAV 1.4.5 and Nginx 1.30.4 at build time.

- [ ] **Step 6: Refresh Keycloak and Compose**

Update both Keycloak stages to the exact new digest. Change the services to `invoice-postgres:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}`, `invoice-clamav:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}`, and `invoice-ingest-proxy:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}`, each with `pull_policy: never` in production Compose.

- [ ] **Step 7: Run GREEN static verification**

Run: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1`

Expected: exit 0.

- [ ] **Step 8: Commit**

Commit message: `fix(images): 修复 RC49 运行时基础镜像漏洞`

### Task 3: Integrate derived images into the release gate and verifier

**Files:**
- Modify: `scripts/release-image-gate.ps1`
- Modify: `scripts/release-image-gate-lib.ps1`
- Modify: `scripts/verify-release-image-artifacts.ps1`
- Modify: `scripts/verify.ps1`
- Modify: `scripts/verify-keycloak-runtime.ps1`
- Modify: `scripts/verify-keycloak-provisioning.ps1`
- Modify: `scripts/verify-nginx-configs.ps1`
- Modify: `scripts/verify-web-security-headers.ps1`
- Modify: `deploy/backup/backup.sh`
- Test: `scripts/test-release-image-gate.ps1`

**Interfaces:**
- Consumes: Task 2 Dockerfiles and image repository names.
- Produces: a manifest-bound build/scan/SBOM/runtime pipeline for all nine RC49 images.

- [ ] **Step 1: Add failing gate-source assertions**

Require the three derived image definitions, build contexts, common release tag inclusion, zero-finding PostgreSQL policy, and updated runtime verifier image references. Assert that direct external runtime references no longer appear in active scripts.

- [ ] **Step 2: Run RED verification**

Run: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1`

Expected: FAIL on missing gate integration.

- [ ] **Step 3: Build all derived images in the gate**

Add build contexts for `deploy/postgres`, `deploy/clamav`, and `deploy/ingest-proxy`; tag them with the same `ImageTag`; scan and generate SBOMs serially. Treat PostgreSQL as zero-findings for RC49.

- [ ] **Step 4: Update binding and runtime checks**

Include all locally built runtime images in the common-tag verifier and source fingerprints. Update Compose/static/runtime/backup checks to require the derived images without weakening any no-build/no-pull or backup contract.

- [ ] **Step 5: Run GREEN offline verification**

Run: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1`

Expected: exit 0.

- [ ] **Step 6: Commit**

Commit message: `feat(release): 纳入 RC49 派生镜像全门禁`

### Task 4: Update security review and production runbook

**Files:**
- Modify: `docs/IMAGE-SCAN-REVIEW.md`
- Modify: `docs/PRODUCTION-RUNBOOK.md`
- Modify: `RELEASE-READINESS.md`

**Interfaces:**
- Consumes: Tasks 1-3 exact policy and image names.
- Produces: operator-facing rationale, review deadline, image inventory, and RC49 two-phase canary instructions.

- [ ] **Step 1: Document the production-versus-RC48 fact**

Record that all like-for-like runtime finding sets were equal under the exact RC48 Trivy database and parameters, without changing the gate conclusion.

- [ ] **Step 2: Document remediation and exception classification**

List every fixable family and its real remediation. State that PostgreSQL uses no RC49 exception. Document the exact Keycloak tuple, risk rationale, raw-finding retention, reviewed-at and due-at values, and drift/expiry failure behavior.

- [ ] **Step 3: Update deployment inventory**

Describe the three derived runtime images, same-tag constraint, no-build production Compose rule, and backup/canary sequence. Preserve the pending-canary hard gate.

- [ ] **Step 4: Run documentation/static gates**

Run: `pwsh -NoProfile -File scripts/test-release-image-gate.ps1`

Expected: exit 0.

- [ ] **Step 5: Commit**

Commit message: `docs(security): 记录 RC49 镜像修复与复审期限`

### Task 5: Full verification, signed RC49, artifact gate, and production deployment

**Files:**
- Generate only: `release/0.1.0-rc49-exact1/**`
- Generate only: new RC49 deployment evidence directory
- Do not modify: `release/0.1.0-rc48-exact1/**`

**Interfaces:**
- Consumes: all prior tasks on a clean branch.
- Produces: signed source/tag, RC49 manifest/SBOM/reports/checksums, production canary and deployment evidence.

- [ ] **Step 1: Run source verification**

Run: `pwsh -NoProfile -File scripts/verify.ps1`

Expected: all backend, frontend, PostgreSQL, agent, Compose, Nginx, backup, migration, and upstream-integrity gates pass.

- [ ] **Step 2: Review and merge the patch branch**

Run a whole-branch review. Merge only after a clean review and clean worktree. Create a signed commit and signed annotated tag `v0.1.0-rc49-signed`; verify both signatures.

- [ ] **Step 3: Run gitleaks on the exact RC48-to-RC49 range**

Run: `gitleaks git --redact --no-banner --log-opts="08aff147766c046b12e19221a6aabb675485d452..HEAD"`

Expected: exit 0 before tag/export/deployment.

- [ ] **Step 4: Generate the fresh image bundle**

Run `scripts/release-image-gate.ps1` with release name/tag `0.1.0-rc49`, source-agent version `0.3.0`, Keycloak mode, and new directory `release/0.1.0-rc49-exact1`. Confirm all fixable images report zero HIGH/CRITICAL; Keycloak reports exactly the one approved tuple and passes pruning/runtime/provisioning.

- [ ] **Step 5: Independently verify artifacts**

Run `scripts/verify-release-image-artifacts.ps1 -ReleaseDirectory release/0.1.0-rc49-exact1`. Confirm all hashes, image IDs, source fingerprints, common tags, review dates, and raw reports bind.

- [ ] **Step 6: Follow the production runbook**

Take and verify the required backup/off-host ACK, transfer only manifest-bound images and source, preflight the exact host/network/secrets, perform the isolated database and Keycloak/app rollout, and preserve the prior deployment for rollback.

- [ ] **Step 7: Run real production canaries**

Verify OIDC discovery, MFA, RP/back-channel logout, platform password login, Sub2API 2FA, API/Web health, PDF/ClamAV rejection, source agents, backup integrity, and no secret leakage. Record exact timestamps and image IDs.

- [ ] **Step 8: Finalize evidence**

Retain the final RC49 deployment evidence and report the exact running images, health results, and rollback point. RC48 evidence remains untouched.
