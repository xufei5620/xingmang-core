# R2-13 CPA 管理面网络隔离设计

> **状态：P0 审批设计，不是实现授权。** 本文冻结 CPA 管理面、推理面、OAuth
> callback 与 usage 消费的网络/凭据边界。它不授权部署 CPA、配置 management key、
> 暴露端口、调用管理 API、启用插件、消费 usage queue、迁移或生产变更。

## 1. 结论

CPA 单进程把推理 API 与 Management API 放在同一端口，因此“只把管理 API 改成
另一个端口”不是现成能力。推荐每个 CPA 实例在同一主机上部署三个**相互隔离的边界**：

1. 公共推理 gateway：只允许获批推理路径；
2. 公共 callback gateway：只允许该实例精确 OAuth callback GET/POST，不持 management key；
3. 私有 management adapter：只监听 private/loopback + mTLS，把 capability ID 映射成
   精确 method/path，并在本机注入 CPA management key；不提供 generic reverse proxy。

CPA 本身只绑定 loopback，公网和普通 private clients 都不能直达原始 8317。未来 usage collector 使用
第四条私有、单消费者 capability，依赖 R2-10 与 R2-15；本设计不启用它。

阶段裁决：

| 阶段 | 结论 |
|---|---|
| R213-0 本设计 | 可审批 |
| R213-1 policy/离线配置审计器 | 条件 GO；不连接 CPA |
| R213-2 management/callback adapter + disposable lab | 独立批准；仅假 CPA/隔离网络 |
| R213-3 staging 实例隔离验收 | NO-GO；实例、网络、证书、CredentialRef另批 |
| M4 CPA 真实管理能力 | NO-GO；至少 R213-3 + R2-14 + capability/Action审批 |
| production | NO-GO；staging新证据后再批 |

R2-13 只有在 R213-3 的外部扫描、mTLS、callback、推理、健康和回滚证据被接受后
才满足。通过 R2-13 也不解锁插件、自动写或高频 usage 消费。

## 2. 官方事实快照（2026-08-29）

官方 Management API 文档：`https://help.router-for.me/management/api`。

- 基础路径是 `http://localhost:8317/v0/management`，与推理服务共用端口；
- 所有 management 请求（包括 localhost）都需 management key；remote access 另受
  `remote-management.allow-remote` 控制；
- `MANAGEMENT_PASSWORD` 会注册额外明文 secret，并强制 remote management 保持开启，
  即使 `allow-remote=false`；本设计因此明确禁止该环境变量；
- Management API 可返回完整 runtime config、替换 YAML、管理 API keys/auth files、
  改路由/日志/额度状态；这些响应可能含 key、proxy URL、header 或凭据元数据；
- `/api-call` 可选择已配置 credential，并用 `$TOKEN$` 发任意绝对 URL 请求；这是
  高危通用外呼，v1 adapter 永久 deny；
- plugin-store 可以下载/启用服务端可执行插件，必须等待 R2-14；
- `/usage-queue` 是 pop：返回即从 queue 移除，与 Redis LPOP/RPOP 共用同一 queue；
- Management `GET /latest-version` 查询的是上游最新 release，不是当前运行版本，不能
  作为实例兼容性证据；
- `/oauth-callback` GET/POST 不走 management-key middleware，但 CPA 会校验 pending state
  与 provider；它是必须单独保留的公共例外，不能与整个 management prefix 一起开放。

以上是时点事实；实施前需对目标 CPA exact version 重跑 contract/route inventory。未知或
漂移版本 fail closed，不沿用本文快照猜路径。

## 3. 为什么 management key 是高危根凭据

同一 key 可触发配置写、credential 文件上传/删除、runtime 状态改变、插件安装和带
credential 的任意外呼。即使先只做 GET，浏览器持 key 仍能被 XSS、扩展或日志窃取后
直接升级为全权调用。因此：

- 浏览器、手机、前端 bundle 永不接触 management key；
- 星芒中心 registry 也不保存 CPA 原始 management key；
- key 仅存在 CPA 主机的 local CredentialRef provider，由私有 adapter 在 loopback
  请求最后一刻注入；
- 平台到 adapter 只用 mTLS workload identity，不能透传 management bearer；
- adapter 先 strip `Authorization`、`X-Management-Key`、Cookie，再按 capability 注入；
- 日志、错误、审计、trace、metrics 不能含 key/header/body/raw config/auth file。

## 4. 目标拓扑

```text
Internet client
  -> public inference TLS gateway
       -> exact inference route allowlist
       -> 127.0.0.1:8317

OAuth provider/browser
  -> per-instance callback TLS hostname
       -> callback adapter (no management secret)
       -> exact GET/POST /v0/management/oauth-callback
       -> 127.0.0.1:8317

Xingmang backend
  -> approved private route (VPN/private subnet) + mTLS
       -> management adapter (deny-by-default capability map)
       -> inject host-local management key
       -> exact /v0/management/... on 127.0.0.1:8317

Future usage collector (disabled)
  -> private mTLS capability + R2-10 single owner + R2-15 budget
       -> destructive usage-queue pop
```

CPA raw port is never publicly published. Public gateway and adapters are separate processes but
must run on the same host loopback or explicitly share the CPA network namespace；their only
upstream is loopback CPA. Separate containers on ordinary bridge networks cannot reach a CPA bound
to its own `127.0.0.1` and are rejected rather than weakening CPA to `0.0.0.0`. A public reverse proxy
must not forward wildcard `/v0/management`, encoded variants or unknown paths.
If the approved CPA version cannot bind raw 8317 to loopback while preserving required behavior,
R213 v1 is NO-GO for that instance；do not substitute a broad firewall-only exception.

## 5. 四个 plane

### 5.1 Public inference plane

- public TLS 443 only；raw 8317 blocked by host/cloud firewall；
- exact versioned inference route allowlist from CPA Connector manifest；
- always deny `/v0/management`, case/encoding/double-slash/path traversal variants；
- strip management headers/cookies；do not log inference authorization values；
- preserve streaming/HTTP2/timeouts and approved health behavior；
- unknown route/method returns local 404/405, never reaches CPA management router。

The exact inference routes are version evidence, not guessed here. R213 implementation must first
capture them from the approved CPA version and contract-test both allowed and denied sets.

### 5.2 Public callback plane

Use a per-instance callback hostname so routing never trusts an attacker-controlled state to choose
an instance. Allow exactly:

```text
GET  /v0/management/oauth-callback
POST /v0/management/oauth-callback
```

Route identity is `(plane hostname/SNI, normalized method, normalized path)`. The inference hostname
denies the entire management prefix **including callback**. Only the separate callback hostname may
match the exact exception above；every sibling on that hostname is denied. There is no “allow exact
path then fall through to wildcard management” ordering on one public host.

Rules:

- callback adapter has no management key, private-network client cert or generic proxy capability；
- exact Host/SNI mapping, TLS, method, normalized path, query/body size and rate limits；
- strip Authorization/X-Management-Key/Cookie/forwarded client credentials；
- POST only approved JSON content type；GET only approved query keys/lengths；
- GET keys are limited to provider/state/code/error/error_description；public POST v1 is limited to
  provider/state/code/error. Although upstream can accept `redirect_url`, the public adapter rejects
  it and every absolute URL until an exact future contract proves a non-confusable need；
- no redirects, arbitrary destination, websocket, CONNECT or path suffix；
- CPA remains final state/provider validator；adapter never logs code/state raw values；
- `get-auth-status`, auth URL creation/cancel and every other management route remain private mTLS。

### 5.3 Private management plane

The management adapter exposes **capability IDs**, not raw URLs:

```go
type CPACapability struct {
    ID, CPAMethod, CPAPathTemplate, RiskClass string
    ResponseProjection string
    RequiresFoundationB, Destructive, ConsumesQueue bool
    MinVersion, MaxVersion string
}
```

Request callers cannot set host, upstream URL, arbitrary path/method/header or redirect policy.
Adapter maps one approved ID to one fixed local method/path, enforces version/capability and applies
response projection before returning.

v1 capability policy starts deny-all. R213 lab may enable only harmless version/health contract
fixtures. Real CPA reads/writes are M4 slices. The following are explicitly denied until separate
approvals:

- full `/config` or `/config.yaml`；
- auth-file upload/download/delete/field mutation；
- API key/provider credential endpoints；
- `/api-call` arbitrary outbound；
- plugin install/delete/enable/config（R2-14）；
- log download/delete unless separately redacted and approved；
- quota reset or routing changes；
- usage queue consume（R2-10/R2-15）；
- any unknown method/path/version。

### 5.4 Private destructive telemetry plane

`usage-queue` is destructive pop, not a normal GET. It requires a separately named capability,
single-owner lease from R2-10, explicit count limit/cursor semantics from target version, R2-15
budget/backoff and downstream durable receipt **before** pop. None exists in R213-0/1/2.

## 6. CPA process and secret configuration invariants

For every instance:

- CPA listens on loopback only；public/private network clients cannot reach raw 8317；
- `remote-management.allow-remote=false`；
- `MANAGEMENT_PASSWORD` is absent；
- lifecycle generates a host-local plaintext secret in the adapter CredentialRef provider and writes
  only its bcrypt hash to CPA `remote-management.secret-key`；CPA runtime receives no plaintext
  env/argument/mount；
- adapter injects the local plaintext and CPA verifies the hash；the secret is per instance, not global；
- official v1 exposes one `secret-key`, so management-secret rotation is a quiesced single-active
  transaction, **not** fake A/B overlap：drain adapter, stage new ref, replace CPA hash/reload,
  switch active ref, verify, or restore old hash/ref；`MANAGEMENT_PASSWORD` must not be used as overlap；
- plaintext never appears in process args, compose interpolation output or logs；
- adapter outbound allowlist is exactly loopback CPA port；CPA redirect response is rejected；
- management adapter itself listens only private/loopback and requires mTLS。

Official warning is explicit：`MANAGEMENT_PASSWORD` forces remote management enabled, so setting it
and relying on firewall alone is a configuration failure, not a tolerated fallback.

## 7. mTLS workload identity

- per-environment client CA, per-instance server identity；no shared wildcard client cert；
- TLS 1.3, server name pin, private DNS/route and certificate expiry checks；
- client identity maps to an adapter capability allowlist；frontend user identity never maps
  directly to CPA；
- short-lived cert preferred；A/B rotation supports overlap with exact serial/fingerprint evidence；
- revoked/expired/wrong environment/wrong SNI cert fails before reading local management secret；
- private-network reachability alone is not authentication；mTLS alone does not authorize raw paths。

Platform stores only adapter endpoint and mTLS CredentialRefs in its registry. It does not store
CPA management key or local secret path.

## 8. Capability/version contract

`contracts/cpa/management-boundary.v1.json` freezes:

```text
contract version,
CPA version range / route inventory hash,
inference allowlist,
public callback exact route,
management deny-list,
approved private capabilities,
response projection keys,
redirect/body/timeout/rate limits,
required network/config/secret facts
```

Strict loader rejects unknown fields, overlapping allow/deny routes, wildcard management prefixes,
generic proxy capability, unbounded body/count, write without Foundation-B, plugin capability before
R2-14 or queue capability before R2-10/R2-15.

Target CPA version/build来自部署 image digest、构建标签或隔离主机上的受控 `--version`
证据；不得拿 `/latest-version` 冒充。若目标版本没有可信运行版本证据，或 route inventory
hash mismatch，所有能力仅保留本地隔离诊断并 fail closed。

## 9. NetworkIsolationAttestationV1

R213 verifier produces a signed, secret-free evidence artifact:

```text
version, instance_id, environment, CPA version,
boundary contract version/hash, image/config digests,
public inference hostname, callback hostname, private adapter identity,
raw port external reachability=false,
management wildcard reachability=false,
callback exact GET/POST=true and sibling routes=false,
private mTLS positive/negative matrix,
allow-remote=false, management-password-absent=true,
inference/health/OAuth-callback/usage-path preservation results,
certificate/key-rotation facts (fingerprints only),
tested_at, tester build, evidence digest, validity, signature
```

No raw IP inventory, management key, auth code/state, DSN, private key or response body containing
credential material is permitted.

Attestation states: `not_tested`, `pass`, `fail`, `stale`, `version_drift`, `partial`. UI may only
show pass while signature/validity/contract/image/version all match current instance.

## 10. Health, OAuth and usage preservation

Isolation cannot “pass” by breaking necessary runtime behavior:

- inference smoke: approved canary request or non-billable version-specific probe through public
  gateway，with streaming/timeout evidence；
- health: exact target-version liveness/readiness evidence that does not expose management data；
- OAuth: create pending flow privately, receive one synthetic callback publicly, observe status
  privately, then cancel/expire；no real credential is retained in lab；
- usage path: prove private capability remains routable but **do not pop production queue**；staging
  uses dedicated canary record only after R2-10/R2-15 approval；
- callback failure must not make wildcard management public；health failure must not trigger a
  bypass to raw 8317。

## 11. Threat model and negative paths

Tests include:

- direct public TCP/HTTP access to 8317；IPv4/IPv6；
- `/v0/management`, suffixes, percent/double encoding, `//`, dot segments, case variants；
- Host/SNI confusion, absolute-form URI, X-Forwarded-* spoofing, websocket/CONNECT/upgrade；
- callback sibling routes/methods/content types/oversize/replay/rate abuse；
- management mTLS missing/wrong/revoked/expired cert and wrong environment；
- inbound management bearer/header smuggling；adapter must strip before local injection；
- redirect from CPA to non-loopback or DNS rebinding；
- capability path/verb/header/body override；
- secret/error/trace/config/auth-file leakage；
- `MANAGEMENT_PASSWORD` present or allow-remote true；
- plugin/API-call/config/auth-file/usage capability attempted while gated。

## 12. Read-only UI placement

CPA overview may add a “管理面隔离” gate card and evidence table：raw public port、public management
prefix、callback exception、private mTLS、contract/version、cert expiry、attestation freshness.

No secret reveal, raw endpoint, management key test field, run-now, firewall edit, tunnel start or
Drawer. A full remediation wizard is R2-17/M4; R213 UI only explains failed facts and ownership.

## 13. Verification matrix

### Offline policy

- official snapshot fields/deny routes represented；
- allow/deny disjoint, no wildcard/raw proxy；
- callback only exact GET/POST；
- `MANAGEMENT_PASSWORD` and allow-remote failures；
- capability dependency gates (Foundation-B/R2-10/14/15)；
- response projection never includes credential/config/auth-file keys。

### Disposable network lab

Use harness-owned containers/namespaces and a fake CPA with inference, management, callback and
usage fixtures. No real management key/upstream account.

- external namespace sees only inference+callback TLS；raw port unreachable；
- private mTLS adapter sees capability endpoint；wrong cert/route denied；
- fake CPA receives adapter-injected local key, never caller key；
- callback adapter has no secret mount and cannot access management capability；
- encoded-path/redirect/header-smuggling corpus denied；
- inference/streaming, health and synthetic OAuth state still work；
- usage fixture is not consumed while gate disabled；
- secret scan/log capture contains no test key/body；
- mTLS cert A/B rotation plus quiesced management-secret replace/rollback positive/negative；
- rollback restores prior gateway with raw port still non-public。

### Staging

- approved exact CPA version/image/config/network/contract/certs；
- external scan from independent network and IPv6；
- private Xingmang route + mTLS；
- per-instance callback SNI and synthetic state；
- inference and health SLO；
- firewall/security-group/container bind evidence；
- adapter crash/CPA crash/cert expiry/route drift failure injection；
- signed attestation and rollback drill；
- zero wildcard management exposure and zero skipped test。

## 14. Rollout and rollback

1. approve R213 contract/topology；
2. build offline auditor and disposable lab；
3. approve adapter binaries/dependencies and local secret provider；
4. deploy staging gateways/adapters with CPA raw port still loopback；
5. prove public inference/callback and private management matrix；
6. soak/rotate/failure-inject；
7. accept attestation；only then may M4 read capability design proceed；
8. production is a new change.

Rollback removes platform access/capabilities first and keeps raw CPA port non-public. It restores
the prior public inference gateway only if the same deny corpus still passes, revokes client cert,
retires the local management-key ref/hash pair, and marks attestation stale/fail. Never rollback by
setting `MANAGEMENT_PASSWORD`, `allow-remote=true`
or exposing 8317.

## 15. 分片

| 片 | 交付 | 门禁 |
|---|---|---|
| R213-0 | 本设计/计划 | docs only |
| R213-1 | boundary contract + offline config/route auditor | 人工批准；不连 CPA |
| R213-2 | separate management/callback adapters + fake-CPA network lab | dependency/security approval；no real instance |
| R213-3 | staging per-instance deployment/attestation/rollback | network/cert/CredentialRef/staging approval |
| R213-4 | production isolation baseline | accepted staging evidence；M4 capability still separate |

## 16. 审批请求

1. 是否确认 public inference / public callback / private mTLS management / private usage 四平面；
2. CPA raw port loopback-only、allow-remote=false、MANAGEMENT_PASSWORD absent；
3. local adapter 持 management key，中心平台只持 mTLS CredentialRef；
4. capability ID 代替 raw proxy，以及 v1 deny-list；
5. per-instance callback hostname和 exact unauthenticated route exception；
6. signed attestation字段、staleness与验收矩阵；
7. R213-1/2/3/4分别审批；R213-3通过前M4真实能力不得开始；
8. R2-14插件与R2-10/R2-15 usage依赖继续独立。
