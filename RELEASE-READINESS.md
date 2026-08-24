# Release readiness: 0.1.0-rc25 semantic-projection candidate

Status as of 2026-08-25:

- application code and local non-image gates: **GO** (`scripts/verify.ps1` passed);
- isolated staging deployment: **GO after the fresh RC25 image/SBOM gate**;
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
- recorded 2026-08-24 `fiberstate` evidence: live Sub2API 0.1.179 and New API
  rc.25 matched the frozen contracts; both applications, DNS/TLS/Nginx and
  Keycloak discovery were healthy; Bridge V4 and exact grants were installed,
  legacy views/roles were removed, signed backups restored successfully, and
  the invoice financial ledger was empty. This evidence must be rechecked in
  the maintenance window; it is not presented as live 2026-08-25 state.

The RC24 maintenance attempt applied migration 0011 and restored the public
API, Web, ClamAV and OIDC redirect chain, which removed the Nginx 502. Its
balances-first canary then failed closed before accepting any batch: both live
sources had semantically harmless configuration rewrites after the original
cutover, but the v3 fingerprint included a Sub2API settings timestamp and raw
New API group-ratio JSON. Both balances agents were stopped with sequence zero,
and the signed post-0011 recovery point was independently restored. RC25
replaces that representation-sensitive fingerprint with the reviewed v4
financial-semantics contract; it does not waive configuration validation.

## Production prerequisites not yet performed

- generate and independently verify a fresh RC25 image/SBOM/vulnerability
  manifest bound to the committed source and one exact image tag;
- retain the verified pre-0011 rollback package and the independently restored
  post-0011 RC24 recovery point. Archive the unused RC24 sequence-zero state
  generation and cutover pairs without deleting or overwriting them;
- install the reviewed v4 semantic-fingerprint functions only inside the
  dedicated `invoice_bridge` schema, re-prove exact roles/ACLs and
  `pg_depend=0`, pass all ten pre-cutover `check-db-static` commands, then
  create a new empty versioned state generation. Stop only
  one upstream application at a time, pass the database quiescence gate and
  capture each new create-only cutover before the eligibility boundary; after
  each pair exists, its five full `check-db` commands must bind the live hash
  to that pair before the application restarts;
- after both new pairs and all ten fresh sequence-zero states validate, apply
  migration 0012. It must refuse any accepted manifest, batch, advanced source
  sequence, watermark, eligibility state or financial-ledger row;
- deploy all Invoice images under the same RC25 tag and pass real production
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
- create and locally verify the RC25 commit and signed release tag; no GitHub
  push or remote publication is authorized.

## Conservative V1 decisions requiring owner acknowledgement

- any non-zero Sub2API refund freezes the entire funding lot; even an
  unrefunded remainder cannot be newly invoiced until a later product rule is
  explicitly approved. This prevents over-invoicing but may under-serve a
  partially refunded customer;
- New API observed top-ups never become invoiceable automatically. Two
  distinct finance reviewers must verify external payment evidence, and a
  third operator issues the invoice;
- both payment completion and authoritative wallet usage occurrence must be at
  or after `2026-09-01 00:00:00 Asia/Shanghai`. Historical cash is retained as
  a noninvoiceable pool and consumed first;
- subscription payments remain auditable but noninvoiceable in V1 because the
  current source contracts cannot prove an unambiguous
  purchase-to-subscription-instance-to-actual-usage relationship;
- ordinary invoices only, minimum CNY 200.00, item `技术服务`, hidden issuing
  entity, and email notification with authenticated download rather than a PDF
  attachment; administrators enter the actual tax-platform issue time.

## Release decision

Do not publish traffic merely because the local gate is green. Follow
`docs/PRODUCTION-RUNBOOK.md` in order and record each approval, immutable image
digest, backup proof and canary result. Delete the upstream custom menus and
revoke the integration credentials to take the feature offline; no upstream
source rollback is involved. After one-way migrations 0011 and 0012, however,
an application rollback must preserve one matching set of invoice DB, ten
source states, both cutover pairs, keys and compatible images. Switching only
back to RC17 or restoring one component independently is not a valid rollback.
