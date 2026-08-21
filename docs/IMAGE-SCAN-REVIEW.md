# Release-candidate image security review

> Historical review snapshot. The image IDs below describe the 2026-08-21
> manual candidate only. A production release must be regenerated with
> `scripts/release-image-gate.ps1`; `scripts/verify-release-image-artifacts.ps1`
> must then prove the manifest, Trivy JSON, CycloneDX ImageID, SHA256SUMS and
> current local image IDs still agree. The gate defaults to Keycloak mode and
> therefore fails closed while the rejection in this document remains active.

Review time: 2026-08-21 (Asia/Shanghai). Scanner: Trivy 0.74.0 with the
vulnerability and Java databases downloaded immediately before the scan.
The policy fails on every HIGH or CRITICAL finding unless this file contains a
specific reachability review.

## Approved candidate images

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

## PostgreSQL reachability exception

The reviewed PostgreSQL image is
`postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2`.
Its Alpine packages have 0 HIGH/CRITICAL findings. Trivy reports 21 HIGH and
one CRITICAL against the Go 1.24.6 standard library embedded in `gosu 1.19`.

This is an accepted, narrow binary-reachability exception:

- `gosu` is invoked only by the fixed upstream entrypoint to change from root
  to the local `postgres` UID and `exec` the database process;
- it has no listener and receives no network, URL, certificate, MIME, XML or
  template input in this deployment;
- `govulncheck v1.7.0 -mode=binary` against the exact extracted binary reports
  zero called vulnerabilities. It found vulnerable standard-library packages
  in the binary but no call path to their vulnerable symbols.

The exception must be reevaluated whenever the PostgreSQL digest changes.

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

The alternative was therefore not added to the deployment tree. The current
self-hosted path is the exact Keycloak 26.7.2 derived image above; it must keep
passing the immutable-image gate and must still pass the real production
OIDC/MFA/logout canary before traffic is enabled.
