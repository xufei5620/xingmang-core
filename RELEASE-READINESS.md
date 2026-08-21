# Release readiness: 0.1.0-rc1

Status as of 2026-08-21:

- application code and local release candidate: **GO**;
- isolated staging deployment after real configuration is supplied: **GO with
  external prerequisites**;
- direct public production launch: **NO-GO** until every item in the final
  checklist below is completed.

This is an independent service. No Sub2API or New API source file, container,
database schema or reverse-proxy configuration was modified while producing
this release candidate.

## Verified locally

- full race/vet/frontend/Nginx/Compose/PostgreSQL/migration/concurrency/backup
  and upstream-integrity gate: `scripts/verify.ps1`;
- Go `govulncheck v1.7.0`: no called vulnerabilities in backend or source
  agent modules;
- real qpdf 12.3.2 image build gate: safe static PDF accepted; structurally
  valid JavaScript, Launch, URI, attachment and encrypted PDFs rejected;
- API, scanner, tools, web and source-agent Dockerfiles built successfully
  using digest-pinned build bases;
- Trivy 0.74.0 and CycloneDX results recorded in
  `docs/IMAGE-SCAN-REVIEW.md` and `release/0.1.0-rc1/`; this is now explicitly
  marked as a historical manual snapshot. Fresh releases must use the
  fail-closed `scripts/release-image-gate.ps1` machine manifest and independent
  artifact verifier, so rebuilt image IDs cannot reuse stale SBOMs;
- a full automated Keycloak-mode validation completed at
  `release/0.1.0-rc1-gate-validation6/`: all five application images plus
  ClamAV/Nginx are 0 HIGH/CRITICAL, the exact PostgreSQL `gosu` proof passed,
  the exact Keycloak 26.7.2 vendor-rejected-CVE exception and isolated runtime
  OIDC smoke passed, and the independent artifact verifier passed at generation
  time. Later backend/deployment changes now make this bundle deliberately
  stale; a fresh full gate is required after PostgreSQL verification is green;
- read-only `fiberstate` preflight: live Sub2API 0.1.178 and New API rc.25
  match the frozen contracts; planned ports and five Docker networks are free,
  NTP/disk/Cloudflare real-IP policy are acceptable.

## Production prerequisites not yet performed

- obtain explicit authorization for DNS, Nginx, database grants/views,
  containers and upstream OIDC/menu configuration;
- deploy the approved exact Keycloak 26.7.2 derived image and pass the real
  production OIDC discovery, ID-token, administrator MFA `acr`/`amr`,
  RP-initiated logout and back-channel logout canary before enabling traffic;
- create DNS/TLS for `invoice.solov.cc` and the selected IdP host(s);
- supply exact administrator and independent break-glass `/32` or `/128`
  routes, actual hidden issuer settings and at least three finance operators;
- choose QQ/enterprise QQ or Gmail SMTP and provide its app password through a
  host secret (never Git or browser storage);
- generate all field/session/OIDC/database/source-agent/mTLS/signing/spool,
  cutover and balance-snapshot,
  age backup and offline backup-signing keys according to the runbook;
- install and prove the ten column/row-restricted upstream projection roles;
- capture each source cutover once in a database-clocked `REPEATABLE READ READ
  ONLY` transaction, authenticate both encrypted manifests/baselines, and
  retain them in every source-state backup;
- bind existing Sub2API/New API accounts to the same OIDC identity. Never
  merge by email alone;
- run the real encrypted backup/restore drill and the full canary: login,
  account binding, ten-process/five-stream-per-source sync, real-cash consumed
  allocation with bonus-first behavior, CNY 200 boundary, allocation concurrency,
  New API two-person verification plus independent issuer, PDF rejection and
  download, SMTP delivery, refund attention and more than ten simultaneous
  back-channel logouts;
- create the initial Git commit and signed release tag. The repository has no
  configured author identity, so no identity was invented and no commit was
  made.

## Conservative V1 decisions requiring owner acknowledgement

- any non-zero Sub2API refund freezes the entire funding lot; even an
  unrefunded remainder cannot be newly invoiced until a later product rule is
  explicitly approved. This prevents over-invoicing but may under-serve a
  partially refunded customer;
- New API observed top-ups never become invoiceable automatically. Two
  distinct finance reviewers must verify external payment evidence, and a
  third operator issues the invoice;
- ordinary invoices only, minimum CNY 200.00, item `技术服务`, hidden issuing
  entity, and email notification with authenticated download rather than a PDF
  attachment.

## Release decision

Do not publish traffic merely because the local gate is green. Follow
`docs/PRODUCTION-RUNBOOK.md` in order and record each approval, immutable image
digest, backup proof and canary result. Delete the upstream custom menus and
revoke the integration credentials to roll back; no upstream source rollback
is involved.
