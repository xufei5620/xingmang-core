# Release-candidate image security review

> The RC1/RC17/RC24/RC32/RC34/RC38/RC48 entries retained later in this document
> are historical evidence.
> They are not renamed or rewritten as RC59 evidence.  The current RC59 section
> records source/static remediation design and review policy only; it is **not**
> an image build, Trivy result, SBOM, signature, artifact-verifier, runtime,
> provisioning, transfer, deployment, canary, or rollback record.

Historical RC1 review time: 2026-08-21 (Asia/Shanghai). Scanner: Trivy 0.74.0 with the
vulnerability and Java databases downloaded immediately before the scan.
The policy fails on every HIGH or CRITICAL finding unless this file contains a
specific reachability review.

## Current RC59 remediation and review status (source/static only)

RC59 is an urgent remediation slice, not an approved release.  The RC48
failure evidence at `release/0.1.0-rc48-exact1` remains byte-for-byte
historical evidence and RC48 remains blocked.  No current image result is
claimed here.

RC49 also remains failed historical evidence: signed tag
`v0.1.0-rc49-signed` is fixed at
`eb7b4365d3af30241debe7b1a054b7eed8b94dcd`, and the partial
`release/0.1.0-rc49-exact1`, `release/0.1.0-rc49-exact2`, and
`release/0.1.0-rc49-exact3` directories contain no release manifest. They are
retained and must be treated as read-only; their file set and hashes are
anchored by `docs/RC49-FAILURE-EVIDENCE-SHA256SUMS.txt`. They cannot be renamed,
amended, or used as RC59 evidence. RC59 requires a new exact tag, nine newly
tagged images, and a new artifact directory.

RC50 also remains failed historical evidence: signed tag
`v0.1.0-rc50-signed` is fixed at
`d08b3a2e40e55f7f600759c250f45b16b82bd0e1`. The interrupted
`release/0.1.0-rc50-exact1` contains no release manifest; its file set and
SHA-256 are anchored by `docs/RC50-FAILURE-EVIDENCE-SHA256SUMS.txt`. Treat it
as read-only and never resume, reuse, amend, rename, or represent it as RC59
evidence.

RC51 also remains failed historical evidence: signed tag
`v0.1.0-rc51-signed` is fixed at
`229ca5bea04e9fa8384fa308e342fcf5f6b6332f`. exact1 completed all nine builds,
scans, SBOMs and manifest generation, but final verification failed when
PowerShell converted `reviewedAt` and `reviewDueAt` from ISO strings to
`DateTime`. Its complete 65-file set is anchored by
`docs/RC51-FAILURE-EVIDENCE-SHA256SUMS.txt`; treat it as read-only and never
resume, amend, rename, reuse, or represent it as RC59 evidence.

RC52 also remains failed historical evidence: signed tag
`v0.1.0-rc52-signed` is fixed at
`adf152771e779e6a1bd7a95b3e628d5fa85c3b3f`. exact1 stopped during source
verification because the isolated PostgreSQL 15 host port was unreachable
through local host NAT. Its one-file set is anchored by
`docs/RC52-FAILURE-EVIDENCE-SHA256SUMS.txt`; treat it as read-only and never
resume, amend, rename, reuse, or represent it as RC59 evidence.

### Production versus RC48 fact

The comparison retained outside Git at
`release/0.1.0-rc48-production-comparison-20260831-exact1` used the exact
RC48 Trivy image `0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969`,
the recorded vulnerability and Java-cache database timestamps, serial scans,
`--scanners vuln --severity HIGH,CRITICAL`, and `ignoreUnfixed=false`.  It did
not reduce severity or use an ignore list.

Every like-for-like production runtime finding tuple equalled its RC48 tuple.
API and Web image IDs differed but their finding tuples were equal.  PDF
scanner, Keycloak, PostgreSQL, ClamAV, and ingest Nginx had the same image IDs;
the source agent had zero findings in both sets.  RC48 also included a
non-runtime tools image with the same OpenSSL finding.  Thus blocking RC48 was
correct, but it did not reduce the exposure already present in production;
remediation is urgent.

### Actual RC59 source changes awaiting evidence

- Alpine runtime stages install `libcrypto3=3.5.8-r0` and
  `libssl3=3.5.8-r0`.
- `gosu` 1.19 is rebuilt from commit
  `6456aaa0f3c854d199d0f037f068eb97515b7513` with Go 1.25.13.
- The locally built runtime derivatives are `invoice-postgres`,
  `invoice-clamav`, and `invoice-ingest-proxy`; the two PostgreSQL services
  use the same `invoice-postgres` image.
- Keycloak refreshes its base to
  `quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067`
  and thereby remediates the RC48 `sqlite-libs` family
  `CVE-2026-11822` and `CVE-2026-11824`.  Final-image removal of the unused
  MSSQL JDBC driver and `/opt/keycloak/bin/client` is retained pruning proof
  from the existing hardening; it is required evidence, not an RC59 source
  remediation.

The intended RC59 inventory is nine manifest-bound images under one exact,
immutable release tag: `invoice-system-api`, `invoice-system-pdf-scanner`,
`invoice-system-tools`, `invoice-system-web`, `invoice-source-agent`,
`invoice-postgres`, `invoice-clamav`, `invoice-ingest-proxy`, and
`invoice-keycloak`.  Production Compose has no `build:` directives and every
locally built service uses `pull_policy: never`; a missing transferred image
must fail closed.  This inventory is a source contract until the RC59 release
gate produces and independently verifies the manifest and its IDs.

### RC59 exception policy

PostgreSQL has **no RC59 exception**.  Its policy is zero findings: every old
`gosu` finding has an upstream fixed version and the legacy fixed-version
fixture is deliberately rejected as exception-eligible.

The sole permitted RC59 exception is the retained raw Keycloak finding with
this exact tuple:

The scanner policy remains `HIGH,CRITICAL` with `ignoreUnfixed=false`; there is
no severity reduction, hidden finding, or generic allowlist.

| Field | Required value |
|---|---|
| Scope | `exact-keycloak-26.7.2-base-derived-image-and-single-finding-only` |
| Base reference | `quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067` |
| Derived image ID | exactly the generated `invoice-keycloak:<tag>` manifest image ID |
| CVE | `CVE-2026-22020` |
| Target | `invoice-keycloak:<tag> (redhat 9.8)` |
| Trivy class/type | `os-pkgs` / `redhat` |
| Package / installed version | `java-21-openjdk-headless` / `1:21.0.12.1.1-1.2.el9` |
| Fixed version | empty |
| Severity / status | `HIGH` / `affected` |
| Disposition | `vendor_rejected_not_affected` |
| Review window | reviewed `2026-08-31T00:00:00Z`; due `2026-09-30T00:00:00Z` |
| Pruning proof | `proof/keycloak-runtime-pruning.txt` bound to that derived image ID |

The exact generator/verifier rationale is: `Red Hat officially rejected the
CVE; AWS records that it affects Oracle proprietary bundled libpng while
OpenJDK distributions using system libpng are not affected.`  Its retained
evidence URLs are <https://bugzilla.redhat.com/show_bug.cgi?id=2460045#c11>
and <https://explore.alas.aws.amazon.com/CVE-2026-22020.html>.  The raw Trivy
finding remains in the report; there is no Trivy ignore.  The release gate and
independent verifier must bind the exact base/derived image IDs, target, tuple,
pruning proof, runtime/provisioning proofs, rationale and review window.  Any
tuple or digest drift, missing proof, or review expiry fails closed and requires
a new committed review.

## Historical RC1 approved-candidate snapshot (retired)

This table is retained only as the 2026-08-21 RC1 snapshot. Its generic
`release-candidate` tags are not RC59 references and do not authorize current
build, transfer, canary, or deployment work.

| Image | Local immutable image ID | HIGH | CRITICAL |
|---|---|---:|---:|
| `invoice-system-api:release-candidate` | `sha256:a008ad2576edc0f0a66edd443def23ccec6949fc8d9443d486903a9c9e89d6c9` | 0 | 0 |
| `invoice-system-pdf-scanner:release-candidate` | `sha256:c67598436158279329de54e4cf9cc65de227adc0515e9e2f59774672495e3fd2` | 0 | 0 |
| `invoice-system-tools:release-candidate` | `sha256:d3eacfed49d635f41b349b13857ce25c3fb9bcb0f077074bdf115cc7aed3d5e0` | 0 | 0 |
| `invoice-system-web:release-candidate` | `sha256:5bd95083cdff9b260264d527af38977820d3fce368228f32b4bdc12b33396a91` | 0 | 0 |
| `invoice-source-agent:release-candidate` | `sha256:bf832057ea8db378279f001fba4efbe2cab50e8eb731b8f4d9230287fadf6469` | 0 | 0 |
| `clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8` | same digest | 0 | 0 |
| `nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46` | same digest | 0 | 0 |

The previous Nginx 1.29 candidate had 11 fixed HIGH findings. It was not
accepted: every reference was upgraded to the reviewed Nginx 1.30 digest and
the web image was rebuilt before the zero-finding result above.

CycloneDX 1.7 SBOMs for every locally built image are under
`release/0.1.0-rc1/`. They must be regenerated whenever an image ID changes.
That directory predates the machine-readable manifest gate and is retained as
historical evidence, not as a production-verifiable release bundle.

## Superseded automated 26.7.2 validation evidence

The reproducible gate completed on 2026-08-21 under
`release/0.1.0-rc1-gate-validation6/`. At generation time its independent
verifier passed every manifest hash, SHA256SUMS entry, Trivy/CycloneDX ImageID
and local image binding. Subsequent backend/deployment changes intentionally
make that directory stale; the current verifier rejects it. The table remains
useful validation evidence for the exact exception logic, not a deployable
current release. A fresh gate must follow a green full PostgreSQL verifier.

| Image | Immutable image ID | HIGH | CRITICAL | Decision |
|---|---|---:|---:|---|
| API | `sha256:905b9ca340140fb78d89ee341d9d028bfd2937eacaa56ae82a8d0320ad81c506` | 0 | 0 | approved |
| PDF scanner | `sha256:af8f8701d52e3272dfc19eb98bcff918a68fc6b4b00baba270221734027ff725` | 0 | 0 | approved |
| tools | `sha256:5e9de867da4e16be74badf965e6fa506cfff411583520ebe6d8ae25be8be9c6a` | 0 | 0 | approved |
| web | `sha256:0a14a2294044a45f229cc8e649141f301449309de39db02e2ab71b7c8466738c` | 0 | 0 | approved |
| source agent | `sha256:72ec88c4578b71ab259529644c584fc0d3a09dd445fb6f2c0b33a7cfb7cf36b6` | 0 | 0 | approved |
| PostgreSQL runtime | `sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2` | 21 | 1 | exact `gosu` binary exception |
| ClamAV runtime | `sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8` | 0 | 0 | approved |
| ingest Nginx | `sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46` | 0 | 0 | approved |
| Keycloak 26.7.2 | `sha256:203c8267b2497df2d93d25fe5a26c1a1d915aec4b8b413031251bf6c2e85ff08` | 1 | 0 | exact vendor-rejected-CVE exception; canary pending |

No Trivy finding is hidden or placed in an ignore file. The Keycloak report
retains exactly one finding: `CVE-2026-22020`, target
`invoice-keycloak:release-candidate (redhat 9.8)`, package
`java-21-openjdk-headless` version `1:21.0.12.0.8-1.2.el9`, status `affected`,
with no fixed version. The exception is deliberately non-generalizable:

- the base must remain Keycloak 26.7.2 digest
  `sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec`;
- the derived image ID and exact finding tuple above must match the generated
  manifest;
- `/opt/keycloak/bin/client` and every MSSQL JDBC driver must be absent from
  the final image;
- the isolated PostgreSQL 18 + Keycloak runtime smoke must prove health,
  discovery, authorization/token/userinfo/JWKS/end-session metadata,
  back-channel/session logout support, RS256 and no linkage errors;
- Red Hat's official bug comment 11 states that the CVE was officially
  rejected, while AWS records that the issue concerns Oracle's proprietary
  bundled libpng and that OpenJDK distributions using system libpng are not
  affected: <https://bugzilla.redhat.com/show_bug.cgi?id=2460045#c11> and
  <https://explore.alas.aws.amazon.com/CVE-2026-22020.html>.

Any additional finding, target/package/version/status change, base digest
change, missing pruning proof or failed runtime smoke makes Keycloak fail.

## Historical RC1 PostgreSQL reachability exception (retired)

The reviewed PostgreSQL image is
`postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2`.
Its Alpine packages have 0 HIGH/CRITICAL findings. Trivy reports 21 HIGH and
one CRITICAL against the Go 1.24.6 standard library embedded in `gosu 1.19`.

At RC1 review time this was accepted as a narrow binary-reachability exception:

- `gosu` is invoked only by the fixed upstream entrypoint to change from root
  to the local `postgres` UID and `exec` the database process;
- it has no listener and receives no network, URL, certificate, MIME, XML or
  template input in this deployment;
- `govulncheck v1.7.0 -mode=binary` against the exact extracted binary reports
  zero called vulnerabilities. It found vulnerable standard-library packages
  in the binary but no call path to their vulnerable symbols.

That retired exception required reevaluation whenever the PostgreSQL digest
changed. It is not permitted for RC59, whose PostgreSQL policy is zero findings
with a null exception record.

## Historical rejected 26.7.1 reference image

`invoice-keycloak:release-candidate`
(`sha256:bb39464f7d6fdfa0278c1bdb14561ffe5874361f2670eab77a89a27921733a8e`)
is **not approved for public production**. When Keycloak 26.7.1 was reviewed,
its full Java scan found 17 HIGH findings (14 unique),
including runtime Jackson 2.21.2, Netty 4.1.135, Micrometer 1.16.3 and pgJDBC
42.7.11 components.

Several Netty/Micrometer findings are excluded by the loopback-only HTTP/1.1
reverse-proxy boundary and unused protocols. The Jackson asynchronous parser
and polymorphic validation findings cannot be proven unreachable from all
Keycloak request paths, so they are not waived. Public launch therefore needs
one of:

1. a newer Keycloak release whose exact image passes the same scan; or
2. a separately approved standards-compliant managed OIDC provider that
   supports RP-initiated and back-channel logout.

Do not patch vendor JARs ad hoc or deploy the rejected image by exception.

## Rejected self-hosted alternative

ZITADEL was evaluated before asking the owner to accept a managed provider.
The current stable v4.17.1 images also fail the release policy:

- `ghcr.io/zitadel/zitadel:v4.17.1@sha256:3ac6910685d48f32481f01f45e3e6215efe5a9df2c069591b481e9a101712db5`:
  eight HIGH findings in the Go 1.25.11 server binary;
- `ghcr.io/zitadel/zitadel-login:v4.17.1@sha256:8035df2409afb35a3999482ee98e453261715f98d47e4b62e948e4a1ddf4345f`:
  one CRITICAL and twelve HIGH findings, including fixed Next.js
  authentication-bypass/SSRF issues.

It also does not satisfy the current administrator assurance contract: both
Login V1 and V2 emit an empty standard `acr` claim, while the invoice service
requires the configured LoA2 `acr` plus a signed MFA `amr` value for every
administrator step-up. ZITADEL can emit compatible `otp`/`mfa` AMR values, but
a custom role claim cannot safely replace the missing standard ACR evidence.
Its default 15-minute back-channel logout-token lifetime also exceeds the
invoice verifier's reviewed 10-minute maximum (plus bounded clock skew); a
future approved ZITADEL deployment would have to set that lifetime to 10
minutes and pass a real token canary.

The alternative was therefore not added to the deployment tree. In that
historical review, the self-hosted path was the then-reviewed Keycloak 26.7.2
derived image above and still required the immutable-image gate plus real
production OIDC/MFA/logout canary. The current RC59 path is defined only by the
exact tuple and refreshed base digest in the bounded RC59 section at the top of
this document.
