# R2-13 CPA Management-Plane Network Isolation Implementation Plan

> **Approval artifact only.** Do not execute a slice until a human approves the named slice and
> dependencies. Each slice uses its own worktree/branch/PR. Codex does not merge, deploy or handle
> real CPA credentials/certificates.

**Goal:** prove that a CPA instance can preserve public inference and exact OAuth callback behavior
while raw port/management routes remain non-public and Xingmang uses a private mTLS capability
adapter that alone injects a host-local management secret.

**Spec:** `docs/superpowers/specs/2026-08-29-r2-13-cpa-network-isolation-design.md`

## Global Gates

- R213-0 is docs-only and authorizes nothing else.
- No real CPA, management key, cert, VPN, firewall, DNS, cloud rule or public port is touched in
  R213-1/2 without separate approval.
- CPA raw 8317 never becomes public；`allow-remote=false`；`MANAGEMENT_PASSWORD` absent.
- Browser/frontends never receive management key or call adapter directly.
- Callback adapter has no management secret and only exact GET/POST callback route.
- Management adapter exposes capability IDs, never arbitrary upstream URL/path/method/headers.
- v1 deny-list includes full config/YAML, credentials/auth files, API-call, plugin, logs delete,
  quota/routing writes and usage pop.
- R2-14 gates plugin；R2-10 + R2-15 gate usage consume；Foundation-B gates writes.
- Target CPA version/route inventory is exact evidence；unknown/drift fails closed.
- Every slice runs relevant tests, secret scan/governance and exact diff checks.
- R2-13 remains incomplete until R213-3 staging evidence is accepted.

## Slice Files

### R213-1 — boundary contract and offline auditor

- Create: `contracts/cpa/management-boundary.v1.json`
- Create: `internal/platform/cpaboundary/policy.go`
- Create: `internal/platform/cpaboundary/policy_test.go`
- Create: `internal/platform/cpaboundary/evidence.go`
- Create: `internal/platform/cpaboundary/evidence_test.go`
- Create: `cmd/cpa-boundary-audit/main.go`
- Create: `cmd/cpa-boundary-audit/main_test.go`
- Create: `docs/evidence/TEMPLATE-cpa-route-inventory.md`
- Create: `docs/runbooks/CPA-NETWORK-ISOLATION.md`

### R213-2 — separate adapters and fake-CPA network lab

- Create: `cmd/cpa-management-adapter/main.go`
- Create: `cmd/cpa-management-adapter/config.go`
- Create: `cmd/cpa-management-adapter/config_test.go`
- Create: `cmd/cpa-callback-adapter/main.go`
- Create: `cmd/cpa-callback-adapter/config.go`
- Create: `cmd/cpa-callback-adapter/config_test.go`
- Create: `internal/platform/cpaboundary/management_adapter.go`
- Create: `internal/platform/cpaboundary/management_adapter_test.go`
- Create: `internal/platform/cpaboundary/callback_adapter.go`
- Create: `internal/platform/cpaboundary/callback_adapter_test.go`
- Create: `internal/platform/cpaboundary/testserver/fake_cpa.go`
- Create: `tests/security/cpa-network-boundary.test.ps1`
- Create: `deploy/cpa-lab/compose.yaml`
- Create: `deploy/cpa-lab/inference-gateway.conf`
- Create: `deploy/cpa-lab/README.md`
- Modify: `deploy/docker/go.Dockerfile`（adapter targets only after dependency review）

### R213-3 — staging attestation and optional read-only projection

- Create: `contracts/cpa/network-isolation-attestation.v1.schema.json`
- Create: `internal/platform/cpaboundary/attestation.go`
- Create: `internal/platform/cpaboundary/attestation_test.go`
- Create: `cmd/cpa-boundary-attest/main.go`
- Create: `cmd/cpa-boundary-attest/main_test.go`
- Create: `docs/evidence/TEMPLATE-cpa-network-isolation-acceptance.md`
- Optional after separate API/data approval: CPA overview isolation card/tests

### R213-4 — production baseline

- Human-approved deployment artifacts only after accepted staging evidence.

---

## 0. Preflight

- [ ] Verify exact worktree/base/branch/clean status and approval refs.
- [ ] Fetch release and require exact approved tip, not only merge-base.
- [ ] Record official CPA Management API URL, captured-at time, target exact version/image digest
      and route inventory evidence. Web docs alone do not prove target instance version.
- [ ] Explicitly reject Management `/latest-version` as current-runtime evidence；it reports the
      latest upstream release. Version comes from approved image/build or controlled host command.
- [ ] Confirm current CPA remains `todo`/not connected；record every live/network test `not_run`.
- [ ] Search for any existing CPA credential/endpoint/plaintext；do not print values.
- [ ] STOP on unapproved target host, real key/cert or inferred network topology.

## R213-1 — Contract and Offline Auditor

### Task 1: Freeze ManagementBoundaryV1

```go
type RouteRule struct {
    Plane, Method, Path, CapabilityID, RiskClass string
    MaxBodyBytes, TimeoutMillis, RatePerMinute int
    InjectsManagementKey, RequiresMTLS, Public bool
}
type ManagementBoundary struct {
    Version int
    CPAMinVersion, CPAMaxVersion, RouteInventorySHA256 string
    Inference, Callback, PrivateCapabilities, Denied []RouteRule
    RequiredFacts map[string]string
}
func LoadBoundary([]byte) (ManagementBoundary, string, error)
```

- [ ] RED tests:

```go
func TestBoundaryFreezesOfficialManagementRiskFacts(t *testing.T)
func TestBoundaryAllowsOnlyExactCallbackGETPOSTPublicly(t *testing.T)
func TestInferenceHostnameDeniesCallbackAndCallbackHostnameDeniesAllSiblings(t *testing.T)
func TestBoundaryDeniesWildcardConfigAuthAPICallPluginUsageAndWrites(t *testing.T)
func TestBoundaryRejectsOverlappingEncodedWildcardAndGenericProxyRoutes(t *testing.T)
func TestBoundaryRequiresAllowRemoteFalseAndManagementPasswordAbsent(t *testing.T)
func TestCapabilityDependenciesRequireFoundationBR210R214R215(t *testing.T)
func TestResponseProjectionRejectsCredentialConfigAuthAndHeaderKeys(t *testing.T)
```

- [ ] Run full package and preserve RED.
- [ ] Copy exact approved official/target-version facts, not a guessed `/v1/*` wildcard. Freeze strict
      loader, path normalization and full deny-list.
- [ ] Hash canonical contract bytes；unknown field/version/path/method/risk fails closed.
- [ ] Commit contract/loader only. No adapter/network/CPA request.

### Task 2: Build offline deployment/config auditor

Input is an explicitly named local evidence bundle, never a live host:

```text
CPA config projection (redacted), process/container bind facts,
public gateway route AST, firewall/security-group export,
adapter config projection, image/route inventory digests
```

```go
type IsolationFinding struct { Code, Severity, Resource, EvidenceDigest, Remediation string }
type IsolationReport struct { ContractHash, CPAImageDigest string; Findings []IsolationFinding; Complete bool }
func AuditOffline(Boundary, EvidenceBundle) (IsolationReport, error)
```

- [ ] RED: public 8317, allow-remote true, MANAGEMENT_PASSWORD present, wildcard management,
      callback sibling, missing IPv6 rule, adapter public/no mTLS, secret in config/process args,
      redirect allowed, route/version digest mismatch and incomplete evidence.
- [ ] Parser must understand structured config/route export；regex/substring “no finding” is not pass.
- [ ] Output only stable codes/digests；never echo source lines that may contain secrets.
- [ ] CLI defaults to failure on incomplete evidence and has no connect/SSH/curl/browser capability.
- [ ] Run tests/governance/secret scan and commit. R213-1 does not prove network isolation.

## R213-2 — Adapters and Disposable Network Lab

### Task 3: Management adapter deny-by-default core

```go
type CapabilityRequest struct { CapabilityID string; Params map[string]string; Body []byte }
type CPARequest struct { Method, Path string; Body []byte }
type CapabilityMapper interface { Map(CapabilityRequest, string /* CPA version */) (CPARequest, error) }
```

- [ ] RED tests: unknown capability, path/method/header/host override, wrong version, redirect,
      response forbidden keys, oversized body, timeout, caller bearer/header/cookie stripping and
      management-key non-leakage.
- [ ] Config requires private listen address, mTLS server cert/key refs, client CA/keyring, exact
      loopback CPA endpoint and local management CredentialRef. `0.0.0.0`, public address,
      non-loopback upstream, HTTP redirect and plaintext env value fail startup.
- [ ] Provision one per-instance plaintext adapter CredentialRef and only its bcrypt hash in CPA
      config；CPA process gets no plaintext env/argument/mount. Resolve adapter key through
      `secrets.NewAudited` with exact caller/purpose；strip
      inbound auth first, inject local key only on mapped local request, zero references after use.
- [ ] mTLS identity maps to capability allowlist；private route without valid cert is denied.
- [ ] v1 lab capability must be fake-version/health only. No real GET `/config` convenience.

### Task 4: Callback adapter with no secret dependency

- [ ] RED tests for exact Host/SNI, GET/POST only, normalized exact path, approved query keys,
      JSON content type/body cap, replay/rate limits, sibling/encoded/double-slash/dot path,
      Authorization/Cookie stripping, rejected `redirect_url`/absolute URL, no redirect and no
      arbitrary instance selector.
- [ ] Config has per-instance callback hostname and loopback CPA endpoint only. It has no
      SecretProvider field/import/mount. Static source/compose test rejects any management secret.
- [ ] Forward state/code/error opaquely within bounds；log only request ID/provider enum/result code,
      never raw state/code/body.
- [ ] Fake CPA validates pending state/provider；adapter itself does not weaken state checks.

### Task 5: Harness-owned network lab

Harness creates random isolated Compose project/networks/volumes, ephemeral CA/server/client certs
and fake CPA. Gateway/adapters explicitly share fake CPA's network namespace (or a host-loopback
fixture) so the fake CPA stays bound to `127.0.0.1`; an ordinary bridged sidecar configuration must
fail instead of changing CPA to `0.0.0.0`. It publishes public inference/callback ports only to random loopback host ports；a
separate attacker container represents external network. All artifacts live in temp and are deleted
in `finally`; no real DNS/key/account.

Required RED→GREEN matrix:

- attacker cannot TCP/HTTP raw CPA or private adapter；IPv4/IPv6；
- public inference allowlist/streaming/health succeeds；every management/encoded variant fails locally；
- exact callback GET/POST succeeds only for pending fake state；siblings/methods/oversize fail；
- private client with right cert invokes one fake safe capability；wrong/no/expired/revoked cert fails；
- fake CPA sees only adapter-injected local key；caller key/header/cookie never arrives；
- management adapter cannot reach non-loopback/redirect target；callback has no secret mount；
- `MANAGEMENT_PASSWORD`/allow-remote/public bind fixtures hard fail；
- plugin/api-call/config/auth/usage capabilities unavailable；
- mTLS cert A/B rotation works；management-secret rotation drains adapter, swaps CPA bcrypt hash
  and active ref as one maintenance transaction, verifies, and restores old hash/ref on failure；
  `MANAGEMENT_PASSWORD` is never used for overlap；
- captured logs/config/process args contain no fixture secret；
- rollback leaves raw port non-public and inference/callback behavior known.

Run the matrix twice with pinned images/tool versions；a skipped namespace/IPv6/mTLS test is failure.
Commit adapter/lab only after dependency/security review. No real instance.

## R213-3 — Attestation and Staging

### Task 6: Freeze signed NetworkIsolationAttestationV1

- [ ] Strict schema/wire/signature tests for every spec §9 field, exact contract/image/version,
      pass/fail/partial/stale, validity, wrong purpose/key, duplicate finding and forbidden secret/IP.
- [ ] Separate unsigned evidence candidate from externally signed acceptance artifact；runtime cannot
      self-certify pass. Keyring is build-pinned public data.
- [ ] Attestor accepts only an approved target inventory and explicit test endpoints；no wildcard scan.
- [ ] Incomplete/skipped test produces partial/fail, never pass.

### Task 7: Prepare staging packet and STOP

Before any command, require:

- exact staging instance owner/host/image/version/config and rollback；
- approved private network/firewall/DNS/callback hostname；
- mTLS CA/server/client refs and local management key provider/rotation plan；
- boundary/adapter image/keyring hashes；
- external IPv4/IPv6 test runner；
- synthetic OAuth canary and non-production inference/health fixtures；
- R2-10/R2-15 approvals before any usage pop；R2-14 before plugin path；
- no platform DB/API/UI schema assumption.

No staging command is executed by R213-0/1/2.

### Task 8: HUMAN staging execution

1. verify backup/rollback and raw port currently non-public；
2. deploy gateways/adapters without enabling platform management capability；
3. run outside scan/path corpus and private mTLS matrix；
4. run inference/streaming/health and synthetic OAuth callback/status/cancel；
5. verify MANAGEMENT_PASSWORD absent, allow-remote false and raw bind loopback；
6. rotate mTLS cert；run quiesced management-secret replace/rollback；failure-inject
   adapter/CPA/cert/route drift；
7. generate/sign attestation；soak and rerun from independent network；
8. rollback on any wildcard exposure, bypass, secret leak or functionality regression.

Only an accepted, current signed attestation satisfies R2-13. M4 capabilities remain separate.

## Optional Read-Only UI (separate contract approval)

If an attestation ingestion/query contract is approved later:

- add CPA overview “管理面隔离” card/table；
- show public raw port, management wildcard, callback exception, mTLS, contract/version/cert expiry,
  attestation state/freshness/source；
- loading/live/empty/error/stale/permission/invalid-signature states；
- no endpoint/key field, test button, firewall/tunnel control, write action or Drawer；
- browser test at 1440/1024/800/390 and keyboard order.

## Rollback

- Disable platform mTLS access/capabilities first；
- keep CPA raw port loopback-only and public management denied throughout；
- restore prior inference gateway only if the same deny corpus passes；
- remove callback adapter only with OAuth flows drained/cancelled；
- revoke client cert, retire the local management-key ref/hash pair and mark attestation stale/fail；
- never rollback through MANAGEMENT_PASSWORD, allow-remote true, raw 8317 publication or browser key；
- production rollback requires a new change record.

## Final Requirement-to-Evidence Matrix

| Requirement | Evidence |
|---|---|
| raw management non-public | external IPv4/IPv6/TCP/HTTP scan + bind/firewall facts |
| public inference preserved | exact route/streaming/health tests |
| OAuth callback exception | per-instance SNI + exact GET/POST + state tests |
| private management | mTLS positive/negative + private route tests |
| key confinement | host-local audited resolve/injection + no-leak scan |
| no generic proxy | capability/override/redirect/encoded-path tests |
| destructive gates | plugin/api-call/auth/config/usage deny tests |
| version compatibility | route inventory hash + unknown/drift fail closed |
| attestation | strict signed wire + external candidate/acceptance evidence |
| rollback | raw port remains closed + old identity revoked + rerun corpus |
| P0 completion | accepted current R213-3 staging packet, zero skipped tests |

Any missing/stale/indirect/skipped evidence leaves R2-13 incomplete.
