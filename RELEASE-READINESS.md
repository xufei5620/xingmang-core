# Release readiness: RC32 historical issuer lifecycle and RC53 image-security candidate

Status as of 2026-08-31:

- prior application/full non-image baseline at `75fac62522a823ce8854f11f6bd4fa8ff82b526e`:
  **GO** (`scripts/verify.ps1` passed); the current RC53 script/document delta
  has only targeted verification and must rerun the full gate before signing;
- isolated staging deployment: **GO only after the fresh RC53 image/SBOM gate
  and strict artifact verification**;
- direct public production launch: **NO-GO** until every item in the final
  checklist below is completed.

## RC53 source/static security stage (current)

**Production: NO-GO.**  RC53 has source/static changes only.  The complete
release evidence is still absent: no RC53 Docker build, zero-finding scan, SBOM,
signature/tag, artifact verifier, runtime/provisioning proof, transfer,
deployment, canary, or rollback point has occurred.  The passing
`scripts/test-release-image-gate.ps1` fixture suite validates source contracts;
it does not create release evidence.

RC49 is failed historical evidence. `v0.1.0-rc49-signed` remains fixed at
`eb7b4365d3af30241debe7b1a054b7eed8b94dcd`, while
`release/0.1.0-rc49-exact1`, `release/0.1.0-rc49-exact2`, and
`release/0.1.0-rc49-exact3` are retained partial failure directories with no
release manifest and must be treated as read-only. Their file set and hashes
are anchored by `docs/RC49-FAILURE-EVIDENCE-SHA256SUMS.txt`. They are not
transfer authority and must not be reused, renamed, amended, or represented as
RC53 evidence.

RC50 is also failed historical evidence. `v0.1.0-rc50-signed` remains fixed at
`d08b3a2e40e55f7f600759c250f45b16b82bd0e1`; the interrupted
`release/0.1.0-rc50-exact1` contains no manifest and must be treated as
read-only. Its exact file set and SHA-256 are anchored by
`docs/RC50-FAILURE-EVIDENCE-SHA256SUMS.txt`. Never move the tag or resume,
reuse, rename, amend, or transfer exact1 as RC53 evidence.

RC51 is also failed historical evidence. `v0.1.0-rc51-signed` remains fixed at
`229ca5bea04e9fa8384fa308e342fcf5f6b6332f`; exact1 completed build, scan,
SBOM and manifest generation but final verification failed because PowerShell
converted the two reviewed ISO date strings to `DateTime`. Its complete 65-file
set is anchored by `docs/RC51-FAILURE-EVIDENCE-SHA256SUMS.txt`; never move the
tag or resume, rewrite, rename, reuse, or transfer exact1 as RC53 evidence.

RC52 is also failed historical evidence. `v0.1.0-rc52-signed` remains fixed at
`adf152771e779e6a1bd7a95b3e628d5fa85c3b3f`; exact1 stopped in source
verification because the isolated PostgreSQL 15 host port could not become
reachable through local host NAT. Its one-file set is anchored by
`docs/RC52-FAILURE-EVIDENCE-SHA256SUMS.txt`; never move the tag or resume,
rewrite, rename, reuse, or transfer exact1 as RC53 evidence.

The pending release must build and bind one common tag for nine images: API,
PDF scanner, tools, web, source agent, derived PostgreSQL, derived ClamAV,
derived ingest proxy, and Keycloak.  PostgreSQL is zero-finding with no RC53
exception.  The only permitted residual tuple is the exact time-bounded
Keycloak vendor-rejected finding recorded in `docs/IMAGE-SCAN-REVIEW.md`;
tuple/digest drift or expiry on `2026-09-30T00:00:00Z` blocks the gate.

RC53 keeps the two-stage hard gate: first create and verify the signed source
commit/tag, then produce, independently verify, and separately sign the
manifest-bound image evidence; transfer only those images and then complete
backup, isolated startup, migration and real production canaries.
`image_approved_pending_canary` remains a blocking result. RC48 through RC52 remain
blocked historical evidence. The recorded RC48 block did not reduce the
like-for-like production exposure, so the fixes are urgent rather than an
approval to ship.

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

The first RC25 balances canary then accepted all signed batches but failed
closed before projection: the runtime attempted `FOR SHARE` on both the mutable
scan-cycle row and the intentionally immutable batch row. PostgreSQL correctly
requires UPDATE privilege for a locked row, while `invoice_app` correctly has
no UPDATE on batches. RC26 locks only the mutable scan-cycle row; a production-
equivalent least-privilege integration test proves that the immutable batch
remains readable but not mutable. The accepted batches, local sequence hashes
and dependency-wait records were retained for forward recovery.

The RC26 retry reached the next historical instance of the same design error:
existing-manifest and idempotence reads still requested row locks on append-only
projection tables. RC27 audits the runtime lock set, removes row locks from
immutable manifests, batches, derived credits and balance checkpoints, and
retains serialization on advisory/source, mutable cycle, watermark, account
and funding-lot rows. A static regression gate rejects reintroduction of the
exact forbidden lock forms.

RC27 then processed both retained manifests and published both baseline scan
cycles without weakening permissions. During the next Sub2API balance cycle,
restart recovery exposed an ordering bug: `Connector.Scan` captured and replaced
the current encrypted balance snapshot before `Publisher` replayed an existing
pending batch, leaving the acknowledged target cursor bound to the older
snapshot. RC28 resumes/finalizes any pending batch before the first connector
read. Restart tests now make the connector fail if it is called before replay.

The RC28 ten-agent startup then proved the configured ingestion dynamic pool
was undersized: the old `/29` pool could not hold the API plus ten source
agents. RC29 uses a `/27` internal subnet, a `/28` dynamic pool with at least 11
container addresses, and reserves `.30` outside that pool for the exact trusted
ingest proxy `/32`. Production preflight now rejects smaller pools.

After RC29 admitted the API plus all ten senders, a Sub2API payment stream with
no new rows reused the preceding published `scan_cycle_id`, because the ID was
derived only from an unchanged ceiling. RC30 also binds the committed cursor
revision: an ACK retry at the same revision remains byte-stable, while the next
empty cycle receives a distinct ID and cannot append to a finalized cycle.
The public Nginx template now also proxies the exact `/readyz` path to the API;
the SPA fallback can no longer turn a failed dependency check into an HTML 200.

RC30 then exposed a readiness timing contradiction rather than a source outage:
the five-minute payment staleness limit equalled the five-minute economic safety
delay, and an idle Sub2API payment cycle published its last order timestamp
instead of the source transaction horizon. RC31 retains the exact row-ID ceiling
but publishes `transaction_timestamp()-5m` as the completed scan watermark.
RC31 keeps the independently accepted-batch heartbeat at five minutes, raises
only the proven economic-watermark budget to 15 minutes, and refuses a runtime
watermark budget smaller than the safety delay, receiver clock-skew allowance
and two poll intervals. A real PostgreSQL idle-cycle test proves the horizon
advances while the row cursor does not.

RC32 closes the production issuer-configuration lifecycle. The API may start
with the one reserved `待配置开票主体` bootstrap value so an authenticated,
IP-restricted administrator can replace it. The settings API reports every
trimmed empty value or reserved `待配置...`/`请替换...` prefix as unconfigured;
invoice-settings updates reject those values, and manual issue confirmation fails closed before
capturing an immutable issuer snapshot. The create-only bootstrap command has
an explicit path that accepts either the exact reserved value or a real issuer
while retaining all amount, date, SMTP and administrator-CIDR validation.
`/healthz` and the protected administrator routes remain available during this
setup state, but `/readyz` deliberately returns 503 until a real issuer is
saved; orchestration must not route user invoice traffic before readiness.

## Production prerequisites not yet performed

- generate and independently verify a fresh RC53 image/SBOM/vulnerability
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
- deploy all Invoice images under the same RC53 tag and pass real production
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
- create and locally verify the RC53 commit and signed release tag; no GitHub
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
