# R2-14 CPA Plugin Admission Implementation Plan

> **Approval artifact only.** Do not execute a slice until a human approves it and its named
> dependencies. Each slice uses an independent worktree/PR. Codex does not merge, install plugins,
> configure CPA or handle real credentials.

**Goal:** deliver a deny-by-default plugin-free baseline and a per-plugin admission/canary/rollback
evidence chain without treating native plugins as sandboxed or adding automatic install/update.

**Spec:** `docs/superpowers/specs/2026-08-29-r2-14-cpa-plugin-admission-design.md`

## Global Gates

- R214-0 is docs-only and approves no plugin.
- R214-1 is offline policy/inventory assessment；no source network/download/CPA connection.
- R214-2 may use internally built synthetic plugins and a disposable fake/approved lab only；no
  third-party plugin, production credential/data/network.
- Real plugin requires one exact per-plugin CR, license/provenance/artifact and R214-3B staging.
- Native capabilities are declarations, not sandbox enforcement. Residual full-process risk remains.
- No latest/range/prerelease/manual-unverified/auto-update/fallback source.
- No platform automatic install/enable/delete/config/OAuth/restart button or Action in R214-1/2.
- R2-13 gates all management/resource access；R2-10/15 gate scheduler/usage；Foundation-B gates writes.
- Public inference/callback permanently deny `/v0/resource/plugins/*` and plugin management routes.
- Any skipped artifact/ABI/network/canary/rollback test is failure.
- R2-14 plugin-free mode is complete only after R214-3A；real plugin only after accepted R214-3B.

## Files by Slice

### R214-1 — policy and offline inventory

- Create: `contracts/cpa/plugin-admission-policy.v1.json`
- Create: `contracts/cpa/plugin-store-sources.v1.json`
- Create: `internal/platform/cpaplugin/policy.go`
- Create: `internal/platform/cpaplugin/policy_test.go`
- Create: `internal/platform/cpaplugin/inventory.go`
- Create: `internal/platform/cpaplugin/inventory_test.go`
- Create: `internal/platform/cpaplugin/evidence.go`
- Create: `internal/platform/cpaplugin/evidence_test.go`
- Create: `cmd/cpa-plugin-assess/main.go`
- Create: `cmd/cpa-plugin-assess/main_test.go`
- Create: `docs/evidence/TEMPLATE-cpa-plugin-inventory.md`
- Create: `docs/evidence/TEMPLATE-cpa-plugin-admission.md`
- Create: `docs/runbooks/CPA-PLUGIN-GOVERNANCE.md`

### R214-2 — quarantine scanner and synthetic canary lab

- Create: `internal/platform/cpaplugin/quarantine.go`
- Create: `internal/platform/cpaplugin/quarantine_test.go`
- Create: `internal/platform/cpaplugin/scanner.go`
- Create: `internal/platform/cpaplugin/scanner_test.go`
- Create: `cmd/cpa-plugin-quarantine/main.go`
- Create: `cmd/cpa-plugin-quarantine/main_test.go`
- Create: `internal/platform/cpaplugin/fixtures/` (source-built synthetic fixtures only)
- Create: `tests/security/cpa-plugin-canary.test.ps1`
- Create: `deploy/cpa-plugin-lab/compose.yaml`
- Create: `deploy/cpa-plugin-lab/README.md`
- Modify after dependency approval: `deploy/docker/go.Dockerfile`

### R214-3A — plugin-free staging baseline

- Create: `contracts/cpa/plugin-inventory-attestation.v1.schema.json`
- Create: `internal/platform/cpaplugin/attestation.go`
- Create: `internal/platform/cpaplugin/attestation_test.go`
- Create: `cmd/cpa-plugin-attest/main.go`
- Create: `cmd/cpa-plugin-attest/main_test.go`
- Create: `docs/evidence/TEMPLATE-cpa-plugin-free-acceptance.md`

### R214-3B / R214-4 — human per-plugin staging/production

- One new immutable admission record/evidence packet per plugin/version/platform/instance rollout.
- Optional read-only UI only after inventory ingestion/query contract approval.

---

## 0. Preflight

- [ ] Verify worktree/base/branch/clean status and exact approval refs.
- [ ] Fetch release and require exact approved tip.
- [ ] Capture official CPA plugin/Management docs URL + time, exact target CPA build/image/CGO/
      GOOS/GOARCH/variant/ABI/route inventory. `/latest-version` is not runtime evidence.
- [ ] Confirm no real CPA/plugin source/key/path is in scope；record live tests `not_run`.
- [ ] Search repository/workspace for accidental plugin binary/config/key without printing content.
- [ ] STOP on requested “latest”, prerelease, unsigned opaque binary, unapproved license or auto-install.

## R214-1 — Strict Admission and Inventory

### Task 1: Freeze PluginAdmissionPolicyV1

```go
type PluginAdmission struct {
    PluginID, SourceID, Repository, ExactTag string
    ArtifactName, ArtifactSHA256 string
    ArtifactSize int64
    SourceCommit, SourceArchiveSHA256, ProvenanceType, ProvenanceDigest, SBOMDigest string
    LicenseSPDX, LicenseTextSHA256, NoticeSHA256, LicenseApprovalRef string
    CPAExactBuildDigest, CPAVersion, GOOS, GOARCH, Variant, FileExtension string
    ABISchemaVersion int
    DeclaredCapabilities, AllowedCapabilities, ForbiddenCapabilities []string
    ConfigSchemaSHA256, ManagementRoutesSHA256, ResourceRoutesSHA256 string
    CanaryProfile, RollbackArtifactSHA256, ChangeRef string
    Prerelease, AutoUpdate, Latest, ManualUnverified bool
}
func LoadAdmissionPolicy([]byte) (map[string]PluginAdmission, string, error)
```

- [ ] RED tests:

```go
func TestPolicyRequiresExactSourceTagArtifactSizeAndSHA(t *testing.T)
func TestPolicyRequiresOfficialPluginIDAndFilenameStem(t *testing.T)
func TestPolicyRejectsLatestRangePrereleaseManualFallbackAndAutoUpdate(t *testing.T)
func TestPolicyRequiresSignatureOrApprovedSourcePinnedRebuildAndSBOM(t *testing.T)
func TestPolicyRequiresLicenseTextNoticeAndHumanApprovalRef(t *testing.T)
func TestPolicyPinsExactCPABuildOSArchVariantExtensionAndABI(t *testing.T)
func TestPolicyRejectsUnknownExtraAndForbiddenCapabilities(t *testing.T)
func TestPolicyTreatsNativePluginAsUnsandboxedResidualRisk(t *testing.T)
func TestOneAdmissionCannotWildcardEnvironmentInstanceVersionOrPlatform(t *testing.T)
```

- [ ] Run full package and preserve RED.
- [ ] Implement strict canonical JSON loader；unknown/duplicate/missing/invalid lengths/case/URLs fail.
- [ ] v1 repository may contain no admitted real plugin until a per-plugin CR is approved. An empty
      policy plus deny rules is valid plugin-free baseline；sample/fake approval is forbidden.
- [ ] Commit policy/loader and stop. No network or artifact.

### Task 2: Freeze PluginStoreSourceV1

- [ ] RED: duplicate source/plugin IDs, mutable/unsigned registry, non-HTTPS, wildcard/redirect host,
      missing auth ref, unbounded size, source fallback and untrusted text-as-instruction.
- [ ] Exact registry bytes/signature/hash and repo/asset host allowlists；source failure is terminal.
- [ ] CredentialRef is audited；records/logs contain ref metadata only, no value/header.
- [ ] External description/readme fields remain opaque display data and never feed commands/paths.
- [ ] Commit source policy separately. It still does not authorize fetch.

### Task 3: Build four-source inventory join

```go
type PhysicalPlugin struct { PathClass, PluginID, SHA256 string; Size int64; Selected, Shadowed bool }
type ConfigPlugin struct { PluginID, ConfigSHA256 string; EnabledExplicit, Enabled, Effective bool; Priority int }
type RuntimePlugin struct { PluginID, Version, Author string; Registered, Enabled, Effective bool; Capabilities []string; RouteDigest, ResourceDigest string }
type PluginInventory struct { GlobalEnabled bool; Files []PhysicalPlugin; Configs []ConfigPlugin; Runtime []RuntimePlugin; Findings []Finding; Coverage Coverage }
func JoinInventory(Policy, PhysicalSnapshot, ConfigProjection, RuntimeProjection) (PluginInventory, error)
```

- [ ] RED: priority path selected/shadowed, same ID conflict, unknown file/config/runtime, missing
      source, hash/version/author/repo/capability/route/config drift, omitted enabled default true,
      global disabled vs effective, restart required and incomplete coverage.
- [ ] Plugin-free PASS only with global false and zero unexplained selected/registered/effective rows.
- [ ] Inventory cannot rely on Management API alone；physical snapshot enumerates every priority dir.
- [ ] No raw config/path/credential enters output；path is class+digest, not absolute path.

### Task 4: Offline assessment CLI

CLI accepts explicit local evidence bundle + committed policies only. No network/CPA/SSH/browser/
download/install capability.

- [ ] RED: incomplete evidence, stale target build, malformed bundle, secret-looking field, policy/
      inventory mismatch, plugin-free pass/fail and per-plugin candidate decisions.
- [ ] Output stable findings and an **unsigned candidate**, never approved admission/attestation.
- [ ] Run governance/secret/diff and commit. R214-1 does not prove plugin safety or live state.

## R214-2 — Quarantine and Synthetic Canary Lab

### Task 5: Quarantine fetch/scanner contract

```go
type FetchIntent struct { SourceID, Repository, ExactTag, ArtifactName, ExpectedSHA256 string; MaxBytes int64 }
type ArtifactRefV1 struct { Provider, Bucket, Key, VersionID, SHA256 string; Size int64 }
type QuarantineReceipt struct { IntentDigest, ArtifactSHA256, ToolDigest string; Size int64; Artifact *ArtifactRefV1; Findings []Finding }
```

- [ ] RED: source/redirect/tag/asset mutation, wrong hash/size, timeout, decompression bomb, traversal,
      symlink/device, wrong file type/arch/ABI/export, hidden executable and secret fixture.
- [ ] Scanner sandbox has no CPA dir/key/cert/prod network and never loads/executes artifact.
- [ ] Output is content-addressed immutable bytes + receipt；no copy to active plugin dir.
- [ ] Signature/provenance/SBOM/license/malware/vulnerability tools are pinned by image/digest；a
      clean scan remains “candidate”, never “safe”.
- [ ] Network fetch is disabled by default and requires an approved disposable source fixture in
      R214-2. Real third-party network remains per-plugin approval.
- [ ] Synthetic lab uses a fake exact-version vault. Real admission additionally requires an
      approved immutable provider/retention/restore drill；temp files or latest-key reads are NO-GO.

### Task 6: Source-build synthetic plugin fixtures

Fixtures are repository-owned, deterministic test code for current approved CPA plugin ABI：

- metadata-only normal plugin；
- undeclared capability/route plugin；
- panic/exit/slow/memory-limit fixture；
- credential/host-callback attempt fixture using synthetic host data；
- management resource/route fixture；
- wrong ABI/arch/export fixture。

Build outputs are temp/untracked, hashed and deleted. No prebuilt binary committed.

### Task 7: Disposable fake/approved CPA canary matrix

Harness owns random Compose project/network/volume/temp certs and synthetic credentials. No real
account/traffic. Target CPA image/plugin support needs separate dependency approval；otherwise use
fake plugin host to test policy/lifecycle only and mark CPA ABI tests not_run.

Required RED→GREEN：

- plugins globally false during placement/hash；individual enabled explicitly false；
- inventory selected path/hash exactly equals admission candidate；shadow paths fail；
- enable one synthetic plugin in isolated canary；registration metadata/capabilities/routes equal；
- reconfigure/shutdown/disable/remove/restart-required behavior；
- panic/exit/memory/concurrency/stream/network/file/host-callback evidence；
- no production egress/credential/log leak；
- plugin resource/management paths remain non-public under R2-13 fixture；
- rollback restores exact no-plugin or prior artifact/config and route/resource disappearance；
- omitted enabled, global already true during install, latest/prerelease/manual fallback all fail；
- Management install API is qualified only if it leaves artifact non-effective until attested.

Run twice, no Skip. Commit lab framework only；do not fetch/admit a real plugin.

## R214-3A — Plugin-Free Staging Baseline

### Task 8: Freeze PluginInventoryAttestationV1

- [ ] Strict schema/wire/signature tests for exact instance/build/OS/arch/variant, global switch,
      physical/config/runtime/admission join, findings/coverage/freshness, scanner/attestor build,
      validity/change ref and forbidden secret/path fields.
- [ ] Runtime/attestor produces unsigned candidate；external approval signer produces acceptance.
- [ ] Public keyring build-pinned；private key outside platform runtime.
- [ ] stale build/config/file/runtime/contract invalidates pass.

### Task 9: Prepare staging packet and STOP

Require R2-13 current attestation, exact instance/CPA build, all discovery dirs, global/config/runtime
projections, host-local read-only hash attestor, rollback and external signer. No plugin store source,
artifact download or enable capability is needed for plugin-free baseline.

### Task 10: HUMAN staging plugin-free verification

1. prove R2-13 and backup/rollback；
2. global plugins false；
3. enumerate every priority directory/config/runtime registration；
4. prove no unknown selected/shadow/config/runtime plugin；
5. scan public management/resource paths remain denied；
6. restart CPA and repeat inventory；
7. sign/accept plugin-free attestation；
8. rollback on any unknown/incomplete evidence。

Accepted R214-3A satisfies R2-14 only for an explicit **no-plugin** CPA operating mode.

## R214-3B — One Real Plugin (HUMAN, Per-Plugin NO-GO Until Approved)

Required packet：exact immutable admission, legal approval, source/artifact/provenance/SBOM/scans,
CPA/OS/arch/ABI compatibility, R2-13, capability-specific R2-10/15/Foundation gates, canary plan,
exact-version artifact-vault + restore evidence, rollback artifact/config and no production credential dependency.

Execution outline：

1. quarantine/scan exact artifact；
2. create isolated no-production-credential canary；
3. keep global false, place/install, host-attest exact selected bytes；
4. explicit individual false/config/priority；
5. enable global+individual only inside canary；
6. run full capability/SLO/host-callback/route/resource/rollback tests；
7. disable/remove/restart and prove disappearance；
8. sign admission/canary evidence；
9. production remains a new approval。

If safe disabled install/readback is impossible, platform stops at the plan and a human lifecycle
operator may place bytes only while CPA is drained/stopped. The platform does not automate a direct
active-directory write.

## Optional Read-Only UI (separate query/data approval)

- plugin-free gate status and global support/enabled facts；
- file/config/runtime/admission rows with selected/shadowed state；
- exact hash/source/license/provenance/SBOM/compatibility/capability/canary/freshness；
- loading/live/empty/error/stale/permission/incomplete states；
- no raw path/config/key/resource embed；
- untrusted plugin/store names/descriptions/logos/routes render as escaped text/allowlisted links；
- no latest/install/update/enable/disable/delete/OAuth/restart/Drawer；
- browser/keyboard/responsive tests。

## Rollback

- Drain inference before changing native plugin state；
- disable individual/global through approved private management/lifecycle path；
- stop CPA whenever unload is unavailable/restart-required；
- restore exact prior config + content-addressed artifact or verified no-plugin state；
- re-enumerate all priority dirs/runtime/routes/resources and verify inference/health；
- revoke management capability and mark admission/inventory stale/fail；
- never use latest, leave shadow/unknown files, delete evidence or expose plugin resources publicly。

## Final Requirement-to-Evidence Matrix

| Requirement | Evidence |
|---|---|
| default deny/plugin-free | global false + four-source zero-unknown inventory + restart repeat |
| exact artifact | source/tag/name/size/SHA + immutable quarantine receipt |
| provenance/license | signature/internal rebuild + SBOM + license/NOTICE/human approval |
| compatibility | exact CPA build/OS/arch/variant/ABI + canary |
| path precedence | selected/shadowed physical inventory tests |
| capability risk | declared/allowed/forbidden/observed equality + residual-risk statement |
| no auto install | CLI/API absence + unsafe installer qualification tests |
| canary | synthetic then per-plugin isolated behavior/SLO/rollback evidence |
| R2-13/10/15 | private path/public deny + scheduler/usage/network dependency gates |
| signed evidence | admission + inventory attestation wire/signature/drift tests |
| P0 completion | accepted R214-3A no-plugin or R214-3B exact-plugin staging packet |

Any missing, stale, indirect or skipped evidence leaves R2-14 incomplete.
