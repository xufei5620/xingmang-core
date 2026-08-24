# Release readiness: 0.1.0-rc9 candidate

Status as of 2026-08-24:

- application code and local release candidate: **GO**;
- isolated staging deployment: **GO after the fresh RC9 image gate**;
- direct public production launch: **NO-GO** until every item in the final
  checklist below is completed.

This is an independent service. No Sub2API or New API source file, container,
database schema or reverse-proxy configuration was modified while producing
this release candidate.

## Verified locally

- full race/vet/frontend/Nginx/Compose/PostgreSQL/migration/concurrency/backup,
  Bridge V4 adversarial maintenance, PG15/PG18 matrix and upstream-integrity
  gate: `scripts/verify.ps1`;
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
- read-only `fiberstate` preflight: live Sub2API 0.1.179 and New API rc.25
  match the frozen contracts; both applications are healthy, DNS/TLS/Nginx and
  Keycloak discovery are healthy, and the invoice database contains no user,
  funding-lot, ingest-batch or invoice-request rows. The old New API partial
  role state and Sub2API legacy views remain production cleanup inputs, not an
  approved runtime boundary.

## Production prerequisites not yet performed

- generate and independently verify a fresh RC9 image/SBOM/vulnerability
  manifest bound to the committed source and one exact image tag;
- verify new source/invoice/IdP backups with isolated restores, preserve the
  existing reader SCRAM envelope, reconcile the legacy view boundary, install
  Bridge V4, prove `pg_depend=0`, and recapture both cutovers while each
  upstream application is stopped and the database quiescence gate passes;
- deploy all Invoice images under the same RC9 tag and pass real production
  OIDC ID-token, administrator MFA `acr`/`amr`,
  RP-initiated logout and back-channel logout canary before enabling traffic;
- supply exact administrator and independent break-glass `/32` or `/128`
  routes and provision at least three distinct finance operators;
- enter the QQ SMTP authorization code through the protected administrator UI
  and pass a real delivery/retry canary; never place it in Git or chat;
- bind existing Sub2API/New API accounts to the same OIDC identity. Never
  merge by email alone;
- run the real encrypted backup/restore drill and the full canary: login,
  account binding, ten-process/five-stream-per-source sync, real-cash consumed
  allocation with bonus-first behavior, CNY 200 boundary, allocation concurrency,
  New API two-person verification plus independent issuer, PDF rejection and
  download, SMTP delivery, refund attention and more than ten simultaneous
  back-channel logouts;
- create and locally verify the RC9 commit and signed release tag; no GitHub
  push or remote publication is authorized.

## Conservative V1 decisions requiring owner acknowledgement

- any non-zero Sub2API refund freezes the entire funding lot; even an
  unrefunded remainder cannot be newly invoiced until a later product rule is
  explicitly approved. This prevents over-invoicing but may under-serve a
  partially refunded customer;
- New API observed top-ups never become invoiceable automatically. Two
  distinct finance reviewers must verify external payment evidence, and a
  third operator issues the invoice;
- a successful post-cutover subscription purchase is treated as delivered
  service and becomes invoiceable for its verified real payment amount; wallet
  top-ups still require actual cash-backed consumption;
- ordinary invoices only, minimum CNY 200.00, item `技术服务`, hidden issuing
  entity, and email notification with authenticated download rather than a PDF
  attachment; administrators enter the actual tax-platform issue time.

## Release decision

Do not publish traffic merely because the local gate is green. Follow
`docs/PRODUCTION-RUNBOOK.md` in order and record each approval, immutable image
digest, backup proof and canary result. Delete the upstream custom menus and
revoke the integration credentials to roll back; no upstream source rollback
is involved.
