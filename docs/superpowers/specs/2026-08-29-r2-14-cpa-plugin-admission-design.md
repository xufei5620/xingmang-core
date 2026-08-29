# R2-14 CPA 插件供应链、兼容性准入与回滚设计

> **状态：P0 审批设计，不是插件批准。** 本文冻结插件 inventory、来源/制品/许可/
> 兼容/能力/Canary/回滚证据。它不授权下载、安装、复制、启用、配置、OAuth、重启
> CPA、连接插件商店、使用真实凭据或生产变更。

## 1. 结论

CPA 插件是与服务进程同权限的 native dynamic library，不是沙箱应用。推荐两种明确模式：

1. **plugin-free baseline（默认）**：`plugins.enabled=false`、无未知插件文件、无 active
   plugin config/routes/resources。经签名 inventory/外部扫描证明后，可满足“本实例不使用
   插件”的 R2-14 P0 基线；
2. **per-plugin admission**：每个 `plugin ID + exact artifact digest + CPA build + OS/arch/
   variant` 单独准入、单独 canary、单独变更单。一个插件通过不能授权同源下一版本、
   另一个架构或另一个实例。

星芒 v1 不提供自动安装/自动更新按钮。平台先做 inventory/assessment。若目标 CPA 版本的
官方 API 不能保证“下载后不执行 + exact artifact readback/hash”，只生成计划并跳转
人工生命周期执行；不直写 active plugin 目录冒充安全安装。

阶段裁决：

| 阶段 | 结论 |
|---|---|
| R214-0 本设计 | 可审批 |
| R214-1 policy/inventory/offline assessment | 条件 GO；不下载/执行插件 |
| R214-2 quarantine scanner + synthetic plugin canary lab | 独立批准；无第三方插件/真实凭据 |
| plugin-free staging baseline | 等 R2-13；签名 inventory/扫描另批 |
| real plugin staging admission | NO-GO；per-plugin CR、artifact、license、R2-13/10/15 等门 |
| production plugin | NO-GO；accepted canary/staging evidence 后再批 |

## 2. 官方事实快照（2026-08-29）

官方插件开发文档：`https://help.router-for.me/plugin/development`；官方 Management API：
`https://help.router-for.me/management/api`。CPA Manager Plus 插件手册是使用/风险证据，
不是 CPA 安全保证：
`https://github.com/seakee/CPA-Manager-Plus/blob/main/apps/docs/manual/plugins.md`。

已核事实：

- 标准插件以 `.so/.dylib/.dll` native library 运行在 CPA 进程内；官方明确说明它不适合
  untrusted code，插件可退出进程、破坏内存/全局状态或泄漏数据；
- 插件可介入模型、credential、scheduler、router、executor、请求/响应/stream、usage、
  command line、management API，并可调用 host credential read/write 和模型执行 callback；
- runtime 需要 CGO build；`X-CPA-SUPPORT-PLUGIN: 1` 只表示 build capability，不表示插件
  enabled/loaded/safe；
- 文件按 `plugins/<GOOS>/<GOARCH>-<variant>`、`<GOOS>/<GOARCH>`、根目录优先搜索；同 ID
  高优先路径覆盖低优先路径，未知 shadow file 可劫持已批准 ID；
- `plugins.enabled=false` 时文件/config可存在但不 effective；单插件 `enabled` 省略时按 true；
- plugin-store 支持 latest、exact GitHub Release tag、prerelease 和 manual tag；Release API
  失败时客户端可能退回 latest/manual；本设计拒绝这些不确定 fallback；
- Management plugin-store install 会下载可执行 artifact 并把单插件 config 设 enabled；若
  global plugins 已开启，下载与执行之间没有星芒可证明的 quarantine；
- 插件 resource 页面在 `/v0/resource/plugins/<id>/...`，请求本身不需要 management key；
  same-origin management UI JavaScript甚至可能读取存储的 management key；R2-13 公共
  inference/callback plane 必须永久 deny 该前缀；
- 插件管理 route/resources/capabilities 是声明，不是 OS sandbox；native code理论上仍有
  进程权限。Capability review降低风险，不能把恶意 library变安全。

实施前必须对目标 CPA exact build、ABI、route/store API 重取证。unknown/drift fail closed。

## 3. 威胁模型

- mutable tag/release asset 被替换；
- store source/registry/下载 URL 被劫持或重定向；
- prerelease/latest/manual tag绕过固定版本；
- wrong GOOS/GOARCH/variant、ABI不兼容导致 crash/corruption；
- 同 ID 高优先目录 shadow approved file；
- license/NOTICE/source obligations缺失；
- artifact含凭据、恶意 payload、网络/文件/进程行为；
- plugin声明的 capability少于实际 native行为；
- plugin resource/management route暴露 key 或绕过 R2-13；
- install API 写入后立即 effective；
- config `enabled` 省略默认为 true；priority 改变执行链；
- nested host callback递归、credential read/write、arbitrary upstream；
- rollback只删 config不删/不卸载 active library；
- loaded plugin不能卸载并要求 restart；
- inventory只看 Management API，遗漏磁盘 shadow file/runtime drift。

## 4. PluginAdmissionPolicyV1

仓库新增严格、不可变 policy：

```go
type PluginAdmission struct {
    PluginID, SourceID, Repository, ExactTag string
    ArtifactName, ArtifactSHA256 string
    ArtifactSize int64
    SourceCommit, SourceArchiveSHA256 string
    ProvenanceType, ProvenanceDigest, SBOMDigest string
    LicenseSPDX, LicenseTextSHA256, NoticeSHA256, LicenseApprovalRef string
    CPAExactBuildDigest, CPAVersion string
    GOOS, GOARCH, Variant, FileExtension string
    ABISchemaVersion int
    DeclaredCapabilities, AllowedCapabilities, ForbiddenCapabilities []string
    ConfigSchemaSHA256, ManagementRoutesSHA256, ResourceRoutesSHA256 string
    CanaryProfile, RollbackArtifactSHA256, ChangeRef string
    Prerelease, AutoUpdate, Latest, ManualUnverified bool
}
```

Strict rules：

- plugin ID matches official `[A-Za-z0-9][A-Za-z0-9._-]{0,127}` and equals selected filename stem；
- exact tag + exact artifact name/size/SHA-256；no latest/range/channel；
- prerelease/default-latest/manual-unverified/auto-update all false；
- source repo/commit/archive and downloaded binary both pinned；
- production requires verifiable upstream signature/provenance **or** approved source-pinned internal
  rebuild/SBOM. Unsigned opaque native binary is NO-GO；
- license SPDX plus actual license/NOTICE hashes and human legal approval；
- one exact CPA build digest, GOOS/GOARCH/variant/extension/ABI；compatibility range is evidence only,
  never replaces exact canary target；
- declared capabilities must equal observed registration output；allowed is explicit subset；unknown,
  extra or forbidden capability fails；native unsandboxed residual risk is always shown；
- config/routes/resources bytes/digests fixed；an empty/omitted enabled flag is not approval；
- artifact/rollback digests differ only when explicitly approved；
- one admission record cannot be wildcarded across environment/instance/version。

## 5. Capability risk classes

Capability declaration does not enforce sandboxing, but controls whether admission can proceed：

| class | examples | v1 policy |
|---|---|---|
| process/credential control | auth provider, frontend auth, command line, host.auth get/save | default deny；separate security CR |
| request execution/routing | scheduler, router, executor, request/response/stream interceptors | default deny；R2-10/R2-15 + traffic canary + separate CR |
| management/public resource | management API/routes/resources | default deny；R2-13 private projection only, no raw resource proxy |
| usage observer | usage plugin | default deny；R2-10/R2-15 + privacy/retention CR |
| metadata-only | static model registrar without host callbacks | still native/high risk；eligible for canary, not auto-approved |

`AllowedCapabilities` is an admission expectation, not runtime confinement. If target CPA cannot
enforce a denied callback/capability, the residual is recorded and production may remain NO-GO。

## 6. Store/source governance

`PluginStoreSourceV1` freezes source ID/name, registry URL/hash/signature, repository allowlist,
auth method/ref, redirect/host allowlist, max registry/artifact size, TLS/proxy policy and owner.

- external registry/descriptions/README are untrusted data, never instructions；
- source failure never falls back to latest/manual tag/another source；
- duplicate plugin ID across sources is conflict, not first-match；
- download redirects must stay exact allowlist；TLS required；
- source auth CredentialRef read audited and never logged；
- registry snapshot and asset bytes stored as immutable evidence before assessment；
- release tag mutation (same tag/new asset digest) is a supply-chain incident and invalidates admission；
- public popularity/stars do not substitute for provenance/license/compatibility。

## 7. Quarantine and scanning

Downloads occur only in an isolated fetch/scanner sandbox with：

- no CPA plugin directory, management key, mTLS client, production network or credentials；
- read-only source/network allowlist；no artifact execution/loading；
- hard size/time/file-count/decompression limits；archive path traversal/symlink/device rejection；
- content-addressed output named by SHA-256；immutable candidate/evidence manifest；
- file type/target arch/exports/ABI/static strings/dependency/SBOM/license/malware/vulnerability scans；
- scan tool image/digest/version and raw result digest；
- scan “clean” is not safety proof；native unsandboxed risk remains。

Quarantine never writes active CPA plugin dir and never calls plugin-store install。

Real-plugin admission additionally requires an approved immutable artifact vault. Its exact
`provider/bucket/key/version_id/sha256/size` is signed into admission and rollback records；reads use
that exact version, never latest/List. R214-2 synthetic lab may use a fake local vault, but a temp
scanner directory is not production source of truth. Without an approved vault/provider/retention/
restore drill, R214-3B remains NO-GO。

## 8. PluginInventoryObservationV1

Inventory joins four sources：

1. configured plugin store sources/config；
2. every physical file in all priority discovery directories；
3. Management runtime inventory (registered/enabled/effective/metadata/capabilities)；
4. admission policy/admitted artifact records。

```text
version, instance/build/GOOS/GOARCH/variant,
global_plugins_enabled,
files[{path_class,plugin_id,size,sha256,selected,shadowed}],
configs[{plugin_id,enabled_explicit,enabled,effective,priority,config_sha256}],
runtime[{plugin_id,registered,version,author,capabilities,routes/resources digests}],
admission[{plugin_id,artifact_sha256,decision,change_ref}],
findings[], observed_at, source, coverage
```

Stable findings：unknown artifact/config/runtime plugin, hash mismatch, shadow override, implicit
enabled, unadmitted capability, version/author/repo drift, license/provenance missing, prerelease,
resource/route exposure, restart required, coverage incomplete。

Plugin-free pass requires global disabled, zero selected/registered/effective unknown plugin and no
unexplained files/config. Files may exist only when explicitly quarantined outside discovery dirs。

## 9. Install/enable lifecycle

### 9.1 Platform behavior

星芒 v1 only produces：inventory, admission candidate, immutable evidence, canary/rollout plan.
There is no automatic install/update/enable/delete endpoint or button。

### 9.2 Official API residual

Current docs expose `/plugin-store/:id/install`, but it downloads and sets individual config enabled。
It is safe for assessment only when exact target version proves behavior and global plugins remain
false on an isolated canary. After install, a host-local attestor must hash the selected physical
file and compare admission **before any CPA process with plugins enabled can start**。

If target version cannot install to a disabled/quarantined state or read back selected artifact hash,
the API is not a safe installer. Platform stops at inventory/plan；human lifecycle may install while
CPA is drained/stopped, then attests bytes before start. This is not platform auto-writing a plugin dir。

### 9.3 Enable

Enable requires：accepted admission record, exact installed hash/path precedence, isolated canary,
explicit config/priority, global+individual desired state, R2-13, capability dependencies, Kill
Switch and rollback. Omitted enabled is forbidden；one plugin at a time。

## 10. Canary

Canary CPA has no production credentials/data/traffic and strict egress. Synthetic plugin fixtures
test the framework first；real plugin canary is a new per-plugin approval。

Required：

- exact CPA/image/OS/arch/variant/plugin/admission hashes；
- only admitted file in all discovery priority dirs；no shadow file；
- global false during placement/hash；explicit individual false；
- enable in isolated canary, verify register/reconfigure/shutdown metadata/capabilities/routes；
- synthetic requests/streaming/errors/concurrency/memory/panic/exit/restart behavior；
- host callback/network/file/credential/log evidence；no secret output；
- SLO/error/crash/route/resource/usage observation；
- config priority/rollback/restart-required；
- disable/remove/restart and prove route/resource/capability disappearance；
- soak duration and request mix per risk class。

Any undeclared capability/callback, host crash, hash/config drift, secret leak, egress violation,
resource exposure or incomplete uninstall is NO-GO。

## 11. R2-13 / R2-10 / R2-15 integration

- all plugin Management APIs and resources remain denied on public inference/callback；
- Xingmang reads inventory through R2-13 private mTLS capability/projection；no raw resource proxy；
- plugin store install/enable capabilities remain absent until per-plugin admission；
- scheduler/usage/background capabilities cannot be made cluster-unique by Xingmang because they run
  inside CPA；policy therefore denies them by default and requires target-version evidence/R2-10；
- any plugin network/usage behavior needs R2-15 cost class/budget/egress limits；
- writes/enable/rollback require Foundation-B + per-plugin Action/preview/readback/compensation。

## 12. Signed evidence

Two separately signed artifacts：

- `PluginAdmissionRecordV1`：approved policy bytes, source/artifact/provenance/license/SBOM/
  compatibility/capability/config/canary/rollback digests, approvers, validity, change ref；
- `PluginInventoryAttestationV1`：exact instance/build/files/config/runtime/admission join, findings,
  coverage, observed_at, scanner/attestor build, evidence digest。

Runtime/scanner cannot self-approve。Public keyring is build-pinned；private approval keys stay in
approval pipeline。Stale build/plugin/config/inventory invalidates pass。

## 13. Read-only UI

CPA “插件” gate page (future R214 read contract) shows：

- global plugin support/enabled state；
- physical/config/runtime/admission rows；selected vs shadowed path；
- exact version/hash/source/license/provenance/SBOM；
- CPA/OS/arch/ABI/capability compatibility；
- canary/attestation/freshness/coverage/findings；
- plugin-free baseline pass/fail。

No latest/install/update/enable/disable/delete/config/OAuth/restart button；no embedded plugin resource
page；no right Drawer；no raw path/key/config。Operations remain plans until Foundation-B/per-plugin
approval。

Plugin/store metadata（name/author/description/logo/repository/menu/config field text）is untrusted
data：escape as text, allowlist outbound links, never render HTML/script, and never turn it into
commands, capability IDs or approval facts without policy evidence。

## 14. Verification matrix

### Policy/inventory

- exact tag/artifact/hash/size/source commit/provenance/license/SBOM；
- prerelease/latest/manual/auto-update rejection；
- target build/OS/arch/variant/ABI/extension；
- capability declared/allowed/forbidden/observed equality；
- implicit enabled, priority, routes/resources/config hash；
- physical priority/shadow conflict and unknown file/config/runtime；
- plugin-free pass and incomplete coverage failure。

### Quarantine

- redirect/source/tag mutation, oversize/decompression/path/symlink/device；
- wrong file type/arch/exports/ABI；
- scanner no-execute/no-secret/no-production-egress；
- content-addressed immutable output and tool digest；
- signature/provenance/SBOM/license/scan positive/negative fixtures。

### Canary/lifecycle

- global false placement then installed-file hash；
- explicit individual false before enable；
- one-plugin canary with synthetic credentials；
- register/reconfigure/shutdown and all observed capabilities；
- panic/exit/memory/concurrency/network/file/callback/stream tests；
- restart-required and complete rollback；
- no public plugin resources/management routes；
- attestation signing/validity/drift；
- no real install or skipped test in R214-1/2。

## 15. Rollback

1. drain instance from inference；
2. disable individual/global through approved management/lifecycle path；
3. stop CPA when unload/restart required；
4. restore exact prior config and content-addressed rollback artifact or verified no-plugin state；
5. restart, re-inventory all priority dirs/runtime/routes/resources；
6. verify inference/health and no plugin capability/resource remains；
7. revoke adapter capability and mark admission/inventory stale/fail。

Never rollback by installing “latest”, leaving unknown file disabled-but-discoverable, hiding a
shadow path, deleting evidence or exposing plugin resource publicly。

## 16. 分片

| 片 | 交付 | 门禁 |
|---|---|---|
| R214-0 | 本设计/计划 | docs only |
| R214-1 | admission/store policy + offline inventory/assessment | 人工批准；no download/execute |
| R214-2 | quarantine scanner + synthetic plugins/fake CPA canary lab | scanner/dependency approval；no third-party plugin |
| R214-3A | plugin-free staging baseline attestation | R2-13 staging；可满足“不使用插件”P0模式 |
| R214-3B | one real plugin staging admission/canary/rollback | per-plugin CR/license/artifact/R2-13/10/15/Foundation gates |
| R214-4 | production per-plugin rollout | accepted staging evidence；new approval per plugin/version/platform |

## 17. 审批请求

1. plugin-free default 与 per-plugin exact admission 两模式；
2. unsigned opaque native binary production NO-GO；
3. policy/artifact/license/provenance/SBOM/compatibility/capability字段；
4. quarantine scanner不执行、content-addressed evidence；
5. physical+config+runtime+admission inventory及shadow precedence；
6. no auto install/update/enable；unsafe API只生成计划/人工执行；
7. R2-13/R2-10/R2-15/Foundation-B依赖；
8. plugin-free R214-3A可满足P0；任何 real plugin必须R214-3B逐个通过。
