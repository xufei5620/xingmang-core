# 跨副本一致限流设计（XM-C-RL0）

> **状态：仅供审批，docs-only。** 本文不授权实现、迁移、数据库连接、凭据创建或
> 轮换、compose 修改、staging/production 操作、合并或部署。
>
> **当前裁决：RL0 文档可批准；RL1 契约/纯模型片可另行申请批准；RL2～RL4
> 不得凭本文直接开工。live enforcement 仍为 NO-GO。**
>
> 基线：`release/v0.1-launch@543087f`。文内“现状”只指该基线；实施前必须重新
> 取证。日期：2026-08-28。

## 1. 意图、成功标准与边界

当前 XM-R011 是 API 进程内的令牌桶。单副本时有效，但 N 个 API 副本会各自拥有
一份额度，集群实际可放行约 N 倍。目标是在不把平台引入新的基础设施依赖、不泄漏
身份、不让共享计数器故障静默放行的前提下，使同一个
`(environment, principal type, principal id, HTTP method, route template)` 在所有
API 副本上共享一条严格、可解释的配额状态。

成功标准：

1. 两个或更多 API 副本并发访问同一键时，总放行量仍等于一份 policy，而非副本数倍；
2. 时间只取 PostgreSQL 时钟，应用实例时钟偏移不能制造额外额度；
3. 每个键只竞争自己的行锁；不同键可以并行，不存在进程级或数据库级全局锁；
4. 数据库只保存 HMAC-SHA256 摘要，不保存 principal、原始路径或可逆桶键；
5. 配额耗尽返回 `429 RATE_LIMITED` 和计算所得的 `Retry-After`；共享计数器不可判定时
   返回 `503 RATE_LIMIT_BACKEND_UNAVAILABLE`，不调用后续 handler；
6. `/healthz` 不查依赖，`/readyz` 能证明限流 policy、函数、权限与 key version 可用；
7. 清理不会重置一个仍在被拒绝的活跃桶；审计 append-only 表不在清理范围；
8. 受限数据库角色只能执行被批准的限流函数，不能直接改 policy/bucket；
9. migration、DBR、CredentialRef、staging enforcement、production enforcement 分别 STOP；
10. 任一自动降级为本地内存或 fail-open 都被禁止。

不在本设计内：

- 不把平台放进用户实时中转请求路径；本限流只保护管理平台 `/api/v1`；
- 不建立通用分布式锁、通用配额产品、租户计费系统或 WAF；
- 不实现 policy 管理 UI/Action；初始 policy 只走版本化 lifecycle bootstrap；
- 不把 bucket 写入 `audit` schema，不把短期 bucket 伪装成审计事件；
- 不引入 Redis/Valkey/Nginx Plus，也不修改上游三方系统；
- 不承诺跨地域强一致或 PostgreSQL 故障时继续提供受保护的管理 API。

## 2. 当前证据（基线 `543087f`）

### 2.1 现有算法与键

| 证据 | 当前事实 | 设计影响 |
|---|---|---|
| `internal/platform/httpapi/ratelimit.go:32-36` | 明确记录“多副本时每个副本各算各的” | 本片修的是已知后续项，不是现网回归 |
| `internal/platform/httpapi/ratelimit.go:43-64` | 默认 120/min、burst 20、10m sweep、15m idle TTL | 首次切换保持默认值与用户可见语义 |
| `internal/platform/httpapi/ratelimit.go:67-87` | `RateLimitConfig` 只有配额和可注入的应用时钟 | 共享实现需把时钟权威移到 DB |
| `internal/platform/httpapi/ratelimit.go:103-180` | 单个 mutex 保护进程 map；允许、补充、清理都在内存 | 不能跨进程；热键还会与所有键共享同一锁 |
| `internal/platform/httpapi/ratelimit.go:182-252` | middleware 返回 429；键为 principal ID + chi pattern；取不到 pattern 时回落原始路径 | 保留 429；键升级为 HMAC v2；禁止原始路径回落 |
| `internal/platform/httpapi/ratelimit_test.go:33-179` | 已覆盖 burst、补充、键独立、route template、分隔符、429 | RL1 必须保留这些行为并补齐跨副本/故障测试 |

### 2.2 装配、探针与配置

| 证据 | 当前事实 | 设计影响 |
|---|---|---|
| `internal/platform/httpapi/router.go:86-99` | probes 在根路由；限流在 `/api/v1` 且位于 `RequirePrincipal` 后 | 继续靠装配位置豁免探针，不维护豁免列表 |
| `cmd/platform-api/config.go:100-124` | `XM_RATE_LIMIT_PER_MINUTE/BURST` 必须为正，无法关闭 | 变量在切换前仍有效；切 PG 后只允许 bootstrap 使用 |
| `cmd/platform-api/main.go:41-55,160-197` | API 建一个通用 pgx pool，并把内存配置注入 router | 共享限流使用独立小 pool，避免占满业务查询池 |
| `internal/platform/httpapi/health.go:17-48` | health 只看进程；ready 只 `DB.Ping` | ready 需组合限流专用验证，health 语义不变 |
| `deploy/compose/launch.yaml:102-138` | staging 只有一个 `platform-api` service；注释承认内存计数 | 当前 staging 尚无副本倍增，但扩容前必须解决 |
| `deploy/compose/.env.example:63-74` | 配额仍是环境变量 | policy bootstrap 要有显式过渡，不能静默出现双权威 |

### 2.3 已有技术栈与数据库治理依赖

- `VERSIONS.lock:10-17,35-40` 已锁 PostgreSQL 18、pgx v5.10.0、River 0.45.0
  和 PG18 image digest；没有 Redis/Valkey 客户端或镜像。
- `deploy/docker/migrate-entrypoint.sh:1-32` 把业务迁移与 River migration 定义成
  Platform Lifecycle Operation；限流表/函数只能沿这条审批线进入数据库。
- `internal/platform/pgdsn/pgdsn.go:26-44` 已允许 `application_name`、pool 参数；
  `cmd/platform-api/database.go:24-80` 已要求非开发数据库密码经 CredentialRef。
- `PROJECT-CONSTITUTION.md:8-35` 要求准确、可回滚、环境显式、CredentialRef、
  append-only 审计、生产可追溯、人类合并和禁止自动生产部署。
- 数据库角色拆分设计位于独立未合入切片。当前复核证据是
  `ai/codex/XM-C-DBR0-role-separation-spec@7019230`：它提出 `xm_migrator` owner、
  `xm_api_runtime`/`xm_worker_runtime` capability roles、PUBLIC/default ACL verifier，
  并把 harness 加固为**自行创建且独占完整 PG18 cluster、不接受外部 admin DSN、固定生产
  role 名、验证 RepoDigest/labels/fresh fingerprint、按随机 Compose project 整体销毁**。
  该 branch SHA 在合入前不是权威，也不能被写死成 RL2 依赖。RL2 必须从获批 CR/Task 读取
  `DBR_APPROVED_MERGE_SHA`，证明它是当时目标 base 的 ancestor，且其 harness 契约至少覆盖
  `7019230` 的上述不变量；然后直接复用已合入 DBR1 harness，禁止复制第二套角色/容器模型。

## 3. 三案裁决

### 3.1 对比

| 方案 | 一致性与语义 | 新运行面 | 延迟/热点 | 故障与回滚 | 裁决 |
|---|---|---|---|---|---|
| **PostgreSQL 18 原子 GCRA** | 一条键一行，`INSERT ... ON CONFLICT`/行锁原子判定；DB 时钟统一 | 无新服务；复用已锁版 PG/pgx/迁移/备份/DBR | 每个受保护请求 1 次 DB round trip + logged write；只序列化同一热键 | 与平台 DB 同故障域；可保留 schema 并回退单副本 memory | **推荐** |
| Redis/Valkey + Lua | `EVAL` + server `TIME` 可做原子 GCRA，原生 TTL | 新镜像、digest、Go client、secret、HA、监控、备份、升级与值班面 | 延迟通常低；热键仍在单分片串行 | 多一个独立故障域，也多一个必须治理的故障源 | 暂不引入，达到 §16 阈值重评 |
| 新反代层限流 | 单 Nginx 实例可用 shared-memory zone 做粗粒度 IP/path 限流 | 若要求跨节点同步需新商业/集群能力 | 位于入口，开销低 | 无法保持当前 post-auth principal + chi route 语义；错误体/Retry-After 也漂移 | 否决为主限流；可另作粗粒度 DDoS 辅助层 |

### 3.2 为什么选 PostgreSQL

平台本身是低到中等流量的管理控制面，不在用户实时中转路径。共享 PostgreSQL 已是
所有受保护 Query 的依赖；当前 `VERSIONS.lock`、pgx、迁移、River、探针和数据库角色
路线均围绕它建立。用 PG 可在不新增一个必须 HA、备份、轮换和观察的服务前提下解决
当前问题，并用真实 ACL 证明最小权限。

这不是“PostgreSQL 永远优于 Redis”的结论。它是当前流量、故障域和运维成熟度下的
YAGNI 裁决。RL3 shadow 必须收集 §16 的阈值；超过阈值后重新出 ADR，不可偷偷把
Redis 加进 compose。

### 3.3 为什么不是令牌桶表

数据库令牌桶要保存可变小数 tokens、补充时刻和浮点舍入规则。GCRA 只需整数微秒
`theoretical arrival time`（TAT），能够用一个原子 upsert 表达 burst、稳定速率与
准确 Retry-After，避免不同语言/副本浮点漂移。因此共享后端统一使用 `gcra-v1`；
内存参考模型也按同一整数算法实现。

## 4. 桶键、隐私与 HMAC

### 4.1 逻辑键 v2 与版本名

`format_version` 与 HMAC `key_version` 是两个独立版本域：

- canonical wire format 固定为 `format_version = 0x02`，简称 key format v2；
- HMAC material 初始 `key_version = 1`，后续 version 只标识 secret material；
- 二者禁止共用一个字段、日志名或配置名。

逻辑键字段固定为：

1. `key_version`（初始 `1`，canonical 中按 `u32be` 编码）；
2. resolved `environment`；
3. `principal.Type`；
4. `principal.ID`；
5. 大写 HTTP method；
6. chi route template。

不包含 scope、query string、body、IP、User-Agent 或实际 path parameter。scope 会变化且
不应重置额度；query/path 实值会产生无界桶；IP 会把代理后的多人错误合桶。

身份必须来自 `RequirePrincipal` 后的 context，且逐请求机械证明：

```text
principal.Environment == process ENVIRONMENT == ReadyState.Environment == DB policy.environment
```

HMAC CredentialRef 的 scope/name 中 environment 也必须等于 process ENVIRONMENT；禁止
staging 进程解析 production ref。缺身份、类型非法或任一 environment/ref scope 不一致均
返回 503，且不调用 HMAC、consume 或业务 handler，不能使用 `anonymous` 新桶继续处理。

chi route template 取不到时，route 字段使用固定 ASCII 哨兵：

```text
<unmatched-api-route>
```

禁止回落到 `r.URL.Path`。固定哨兵可能把未知端点收紧到同一桶，但不会让攻击者通过
枚举 path 制造无限桶；同时发出 bounded 告警供修正路由装配。

### 4.2 无歧义 canonical bytes

canonical wire format 逐字冻结为：

```text
canonical = 0x02
          || u32be(hmac_key_version)
          || segment(environment)
          || segment(principal_type)
          || segment(principal_id)
          || segment(upper_http_method)
          || segment(route_template_or_sentinel)

segment(s) = u32be(len(utf8(s))) || utf8(s)
```

`u32be` 是恰好 4 bytes 的无符号大端整数；字符串长度按 UTF-8 **bytes**，不是 rune/字符数。
不做 Unicode normalization、case folding 或 trim；只有 HTTP method 在校验为 ASCII token 后转
大写。environment 必须是 registry 精确值，principal type 必须是当前四个精确大写枚举。
字节上限固定为 environment 64、type 32、principal ID 4096、method 32、route 4096，且
canonical 总长不得超过 16 KiB；空值、越界、非法 UTF-8/HTTP token 一律 fail closed。
不得再用 `:`、NUL 或字符串拼接充当协议。

RL1 必须把下列 fixture 固定为跨实现 golden；测试 key 为 bytes `00..1f`，只可用于测试：

```text
format_version = 0x02
key_version    = 1
environment    = staging
principal_type = HUMAN
principal_id   = staff:alice
method         = GET
route          = /api/v1/audit/events

canonical_hex = 02000000010000000773746167696e670000000548554d414e0000000b73746166663a616c69636500000003474554000000142f6170692f76312f61756469742f6576656e7473
hmac_sha256    = 6057bcd531c2052132d1c105d3d145a53444843bbdbe1c975e073bb7e8933520
```

另有分隔符、NUL、Unicode、超长值与 unknown-route goldens，任何 canonical byte 改动都要新
`format_version`，不得在 v2 下静默漂移。

### 4.3 数据库只见摘要

API 进程使用 HMAC-SHA256：

```text
digest = HMAC-SHA256(secret key for key_version, canonical bytes)
```

数据库只存 32-byte `bytea` digest。普通 SHA-256 不够：principal ID 往往低熵，拿到
数据库备份即可离线枚举；HMAC key 不随库备份泄漏。

建议 primary/staged CredentialRef 形状：

```text
secret://rate-limit-<environment>/bucket-hmac-v1
secret://rate-limit-<environment>/bucket-hmac-v2
```

仓库只存 ref，不存真实 key。ref 通过显式配置提供，不能由字符串拼接偷偷推导，也不能
在缺失时使用默认 key。进程内 `Keyring` 最多容纳两个**版本互异**的 slot：必填 primary
与可选 staged；slot 名不决定 active，DB policy 的 `key_version` 才是 active selector。
policy 指向的 version 不在 keyring、存在重复 version/ref 或 slot 超过两个时，ready 失败且
请求 503 fail-closed。

每个 key material 的编码固定：CredentialRef 解析结果必须是**无 padding 的严格
base64url**，解码后精确 32 bytes；其它长度、普通 base64、宽松忽略非法字符或空值均
拒绝 ready。错误和日志只能说 `key_material_invalid`，不得回显值或可推断其内容的片段。

### 4.4 key 轮换边界

RL0～RL4 初次上线只要求 v1。Keyring/ready 必须具备 staged slot 契约，但**任何实际
HMAC key/version 轮换仍是独立 CredentialRef + policy Action/history + environment STOP**；
RL2 的 insert-only bootstrap 不能执行轮换。未来获批轮换顺序固定为：

1. 经真实 `secrets.NewAudited(inner, recorder, environment)` provider，并用
   `secrets.WithCaller(ctx,"api:platform")` 标注 Resolve context，在所有目标副本预装
   `{v1,v2}`；每个 slot 调用 `Resolve(ctx, ref,
   "platform-api rate-limit bucket HMAC")`，DB policy 仍选 v1；
2. 每个副本 ready 证明 active v1 可选、staged v2 可严格解析，且日志/审计不含值；
3. 阻断该 environment 管理 API 入口，**fleet-wide quiesce**，排空全部副本；
4. 已另批的 policy Action 在同一事务 append history 后提升 revision + key version；
5. 全部副本以 DB policy 选择 v2 并 ready；任一旧副本/缺 v2 副本保持摘除和 503；
6. inventory 证明全 fleet revision/version 一致后一次恢复流量，不做逐副本混合滚动；
7. 观察窗后将 v2 变为唯一 primary 并撤 v1；旧 bucket 只由 TTL job 清理。

本文不宣称 Action/history 或现有部署已支持这条轮换。它们完成并在 staging 演练前，
production rotation 维持 NO-GO。

## 5. Policy 权威与配置过渡

### 5.1 单一权威

`httpapi.rate_limit_policy` 是 PostgreSQL backend 的唯一运行时配额权威。建议字段：

| 字段 | 约束/语义 |
|---|---|
| `environment text` | PK + FK `core.environment(id) ON DELETE RESTRICT` |
| `policy_revision bigint` | 正数、单调递增；任何算法/配额/key 变化都递增 |
| `algorithm_version text` | 初始精确值 `gcra-v1` |
| `key_version integer` | 正数；与 API 解析的 HMAC material 一致 |
| `per_minute integer` | `> 0` |
| `burst integer` | `> 0` |
| `idle_ttl_seconds integer` | `>= max(60, 2 * full-refill-seconds)` |
| `cleanup_interval_seconds integer` | `> 0` 且 `< idle_ttl_seconds` |
| `updated_at timestamptz` | DB 时钟 |
| `updated_by text` / `change_ref text` | 各 1..256 bytes；lifecycle 审批/变更单引用；不含秘密 |
| `applied_by_db_role text` | 函数从 `session_user` 取得，不接受调用方伪造 |

不提供 `enabled=false`。需要放宽时修改正数 policy；需要紧急停用共享 backend 时走
§14 的受控回滚，而不是让数据库里存在一个悄悄关闭保护的布尔值。

### 5.2 环境变量只作 bootstrap

现有 `XM_RATE_LIMIT_PER_MINUTE/BURST` 在 memory backend 继续保持当前权威。在经过
RL2 migration 与单独 lifecycle 审批后，由版本化 policy lifecycle 命令通过
`bootstrap_rate_limit_policy` 窄函数写入当前 environment 的**唯一首条** policy：

- 只接受 `expected_revision=0, new_revision=1`，policy 已存在时拒绝覆盖；
- 非法值拒绝；
- 记录 revision、change ref、algorithm/key version 与不可伪造的 `session_user`；
- API 的 `postgres`/`shadow` 模式绝不从环境变量覆盖已有 DB policy；
- `postgres` 模式缺 policy 时拒绝 ready 并对请求 503。

bootstrap 冻结为**受信 signed fleet manifest 输入**，不允许 CLI 自称“已经看过所有副本”。
manifest v1 的 canonical payload 至少包含：

```text
kind=xingmang-rate-limit-fleet-manifest, version=1,
environment, fleet_generation, complete=true,
generated_at, valid_until, change_ref,
expected_replica_count,
replicas[] sorted by replica_id:
  replica_id, build_digest, backend=memory, process_environment,
  effective_per_minute, effective_burst
```

外层 envelope 保存 payload SHA-256、Ed25519 `signature_key_id/signature`；验签公钥只来自
独立批准的 fleet-manifest trusted keyring，manifest 不自带受信公钥。CLI 要求 signature/
kind/version/environment/change_ref/时效/complete/数量/唯一 replica ID 全部有效，并要求所有
replica build、process environment、backend 与 quota 等值；missing/duplicate/unknown/stale/
mismatch 任一情况均在调用 DB routine 前退出非零。该签名是授权 operator 对 inventory 完整性的
证明，不扩大 AI 审批权。

payload 使用固定字段顺序、UTF-8、无多余空白的 canonical JSON；签名 domain 固定为
`xm-rate-limit-fleet-manifest-v1\nsha256=<payload_sha256>\n`。trusted key purpose 必须是
`rate_limit_fleet_manifest_signing`，并验证 fingerprint、validity/revocation；跨 purpose/domain
重放与 embedded public key 自认证都失败。RL2 pin 一组 literal bytes/hash/signature golden。

写入后逐字段证明 DB revision 1 与 manifest/批准值等价，shadow/enforcement 前不得漂移。
切入 postgres enforcement 后，从 API runtime compose/config 删除旧 quota 变量；它们不得以
“仍可编辑但已忽略”的假权威残留。受控单副本 memory 回滚需从最后获批 policy 明确回填值。

RL0/RL1 不创建该命令。revision 2+（配额、算法、TTL 或 key 的任一变化）必须另出
Action + append-only policy history + 审批设计；RL2 bootstrap routine/CLI 不能更新现有行，
API 启动也不能写配置，compose 启动不得重置数据库真值。

### 5.3 policy 变更的额度语义

bucket 主键包含 `policy_revision`。revision 变化创建新 namespace，旧桶自然过期：

- 避免把旧速率的 TAT 套到新速率；
- 变更瞬间每个键获得新 policy 的一个 burst，这是明确、获批的边界效应；
- policy 不能高频编辑；每次变更必须记录 change ref；
- 不能复用 revision，也不能降低 revision。

## 6. PostgreSQL 18 原子 GCRA

### 6.1 整数公式

对某 policy：

```text
interval_us  = ceil(60_000_000 / per_minute)
tolerance_us = (burst - 1) * interval_us
```

Go 与 SQL 使用同一数值域：`key_version/per_minute/burst/idle_ttl_seconds/
cleanup_interval_seconds` 均为 `1..2147483647`（TTL/interval 另受下述关系约束），不得让
Go `uint32` 接受 PostgreSQL `integer` 无法保存的值。revision 为正 `int64`。ceil division、
`burst * 60`、tolerance、TAT 推进、epoch-us 转换和 Retry-After 全部先做 checked integer
arithmetic；任何 overflow/underflow、非整秒 TTL 或数据库域外值返回 typed invalid-policy，
绝不产生 allow。

每次 consume 在一条数据库事务内只取一次 `clock_timestamp()`，转换为整数微秒
`db_now_us`。读取旧桶后：

```text
effective_now_us = max(db_now_us, last_seen_us)
eligible_at_us   = tat_us - tolerance_us
allowed          = effective_now_us >= eligible_at_us

if allowed:
    new_tat_us = max(tat_us, effective_now_us) + interval_us
else:
    new_tat_us = tat_us

new_last_seen_us = effective_now_us
retry_after_s = allowed ? 0 : max(1, ceil((eligible_at_us - effective_now_us) / 1_000_000))
```

不存在 bucket 时视作 `tat_us = effective_now_us`。按此定义 burst=N 会在同一时刻恰好
允许 N 次。使用整数微秒，禁止 float tokens、应用 `time.Now()` 或固定窗口。

`max(db_now,last_seen)` 是 DB 时钟向后跳的防线：时钟回退不能产生额外额度；同时记录
clock rollback counter。它不解决数据库跨地域时钟/复制一致性；本设计只支持一个
PostgreSQL primary。

### 6.2 原子路径

`httpapi.consume_rate_limit(environment, key_version, digest)` 必须：

1. 在同一 statement/transaction 读取并锁定 active policy row；
2. 只计算一次 DB now；
3. 用 `INSERT ... ON CONFLICT ... DO UPDATE` 或等价行锁路径原子更新一条 bucket；
4. allow 时推进 TAT；deny 时 TAT 不动，但更新 last_seen；
5. 返回 bounded status、allowed、retry seconds、policy revision、DB observed time；
6. policy 缺失、algorithm/key version 不匹配返回 machine-readable mismatch，API 映射 503；
7. SQL/连接/超时错误不伪装成 limited。

同一 digest 的并发会在其 bucket row 上串行；不同 digest 不共享 advisory/global lock。
禁止在整个表上 `LOCK TABLE`，禁止在 Go 端先 SELECT 后 UPDATE，禁止用事务外两条语句
拼出“看似原子”的判定。

### 6.3 独立小 pool

API 为共享限流建立专用 pgx pool，使用与 API 相同的受限 login/CredentialRef，但设置：

```text
application_name=platform-api-rate-limit
MaxConns=4
MinConns=0
decision timeout=100ms
```

目的是把热键/数据库抖动限制在每副本最多四条连接，不能耗尽正常 Query pool。100ms 是
从 pool acquire 开始，覆盖 acquire + function call + row decode 的端到端 decision deadline；
实际 context deadline 取它与父请求剩余时间的较早者，不是自动放行门槛，超时返回 503。

`4` 是单副本上限，不是 fleet 预算。RL3 前必须读取 DB `max_connections` 与每个 normal API
pool、rate-limit pool、worker、migration/lifecycle、ops/backup 的实际上限，证明：

```text
declared_total = api_replicas * (normal_api_max + rate_limit_max)
               + worker_max + lifecycle_peak + ops_backup_peak
reserve        = max(10, ceil(max_connections * 20%))
declared_total + reserve <= max_connections
```

未知/默认 pool 上限不能按零计算。副本数或任一 pool 上限变化都重跑该门；不满足时停止扩容，
不得靠连接争抢和 100ms timeout 充当容量控制。RL3 shadow 数据若证明 deadline 造成误伤，须走
配置审查调整，不能在代码里无限等待。

### 6.4 热点与容量

- 一条请求至少产生一次 WAL-logged row write，包括 deny（为防 TTL 重置）；
- 单一恶意 principal+route 会序列化自己，不拖住其它键，但可能制造该行锁等待；
- key 空间上限由已认证 principal × bounded route templates × active revisions 决定；
- route template 必须作为低基数 allowlist label；未知 route 共用固定哨兵；
- RL3 必须测 DB CPU、WAL、row-lock wait、pool acquire 和 p95/p99 decision latency。

## 7. Schema、函数与 DBR 最小权限

### 7.1 对象

新增自有 schema `httpapi`：

```text
httpapi.rate_limit_policy
httpapi.rate_limit_bucket
httpapi.consume_rate_limit(text, integer, bytea)
httpapi.rate_limit_ready(text)
httpapi.prune_rate_limit_buckets(text, integer)
httpapi.bootstrap_rate_limit_policy(
  text, bigint, bigint, text, integer, integer, integer, integer, integer, text, text)
```

bucket 建议主键：

```text
(environment, policy_revision, key_version, key_digest)
```

并检查 `octet_length(key_digest)=32`、TAT/last_seen 非负。为按 environment 清理建立
`(environment, last_seen_us)` 索引。表不保存 raw key、principal、route、IP 或请求内容。

### 7.2 SECURITY DEFINER 是受控例外

API/worker/lifecycle 若无表级 DML，只能通过窄函数完成获批能力，因此上述**四个 routine**
均由 `xm_migrator` 持有并使用 `SECURITY DEFINER`，同时满足：

- `SET search_path = pg_catalog, httpapi, pg_temp`，把默认优先搜索的临时 schema 显式放在
  最后；SQL 内仍完整限定对象；
- 不接收 identifier、SQL fragment 或动态 SQL；
- 严格校验 environment、key version、digest length、batch 上限；
- `REVOKE ALL ... FROM PUBLIC`；
- owner 不是 API/worker login 或 capability role；
- verifier 精确检查 owner、`prosecdef=true`、`proconfig` 恰含上述 search_path、httpapi schema
  仅 migrator 可 CREATE、EXECUTE ACL 与 PUBLIC/default routine ACL；
- migration 在创建函数的同一事务内完成 revoke/grant，不能留默认 PUBLIC 窗口。

这是 DBR0 一般“避免第二套业务写契约”的窄例外：bucket 是安全控制状态，不是业务
Action；函数只表达原子 GCRA/清理，不复制业务写实现。

### 7.3 精确 grant

在 DBR 目标角色合入后：

| 角色 | 允许 | 明确拒绝 |
|---|---|---|
| `xm_api_runtime` | schema USAGE；EXECUTE consume/ready | policy/bucket 直接 SELECT/INSERT/UPDATE/DELETE；prune；DDL |
| `xm_worker_runtime` | schema USAGE；EXECUTE prune（必要时只读 ready） | consume；policy/bucket 直接 DML；DDL |
| `xm_lifecycle_runtime` | EXECUTE bootstrap-policy；只准 expected=0/new=1，带 updated_by/change_ref，DB 记录 session_user | 表直接 DML；更新已有 policy；consume/prune；DDL |
| `xm_migrator` | owner、migration、函数定义与 ACL | runtime/lifecycle login 使用 |
| `xm_ops_read` | 经另批只读诊断 view/函数；默认无桶明细 | digest 导出、DML |
| `PUBLIC` | 无 | schema USAGE、table privileges、routine EXECUTE |

新增对象必须进入 DBR policy/verifier；`ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator`
继续撤销 PUBLIC routine EXECUTE/type USAGE。custom schema 不自动授 runtime；本 migration
对四个 routine 逐签名做显式 grant。

### 7.4 DBR 硬依赖

由于当前 staging 共享 superuser，单写 `REVOKE` 不能形成权限边界。因此：

- RL1 可独立开发纯契约/参考模型；
- RL2 的 migration/函数可以在独占 disposable PG18 开发和测试，但必须等待 DBR1
  verifier 合入，并把新 schema/table/routine 加入 policy；
- 任何 staging 数据库执行必须等待 DBR2 owner/lifecycle 和 DBR3 API/worker 受限身份
  证据；
- DBR 未合入时不得以“SQL 中写了 REVOKE”宣称权限已完成；
- RL migration 若先排号，DBR owner/ACL manifest 必须发现这些对象；DBR 若先合入，
  RL migration 必须在同一 PR 更新 DBR policy。

RL2 不得硬编码或原地改 `role-policy.v1.json`。它从批准 CR/Task 读取
`DBR_APPROVED_MERGE_SHA` 与 `DBR_CURRENT_POLICY_VERSION`，证明 merge SHA 是 target base
ancestor、文件版本与声明一致，然后发布 `current+1` 的新 policy 文件与 exact diff。current
未知、文件缺失、version mismatch、next 已存在或 diff 未获独立 DBR CR 批准都 STOP；旧 policy
保持不可变，verifier 对 unknown object/version fail closed。

## 8. Go 边界与 Store 契约

### 8.1 domain package

把算法/键/决定移到 `internal/platform/ratelimit`，HTTP 只负责取 principal/route、映射
状态码和日志。建议稳定接口：

```go
type BucketKey struct {
    Environment string
    KeyVersion  uint32
    Digest      [32]byte
}

type Decision struct {
    Allowed        bool
    RetryAfter     time.Duration
    PolicyRevision int64
    ObservedAt     time.Time
}

type ReadyState struct {
    Environment     string
    PolicyRevision  int64
    Algorithm       string
    ActiveKeyVersion uint32
}

type Store interface {
    Consume(ctx context.Context, key BucketKey) (Decision, error)
    Ready(ctx context.Context, environment string) (ReadyState, error)
}
```

错误使用 sentinel/typed kind，至少区分 `policy_missing`、`policy_mismatch`、
`timeout`、`unreachable`、`permission_denied`、`invalid_result`。HTTP 不解析数据库
错误字符串；PostgresStore 负责映射 SQLSTATE/函数 status。

### 8.2 实现分层

- `Canonicalizer`：resolved request identity → canonical bytes；
- `Keyer`：CredentialRef 解析出的 secret + canonical bytes → HMAC digest；
- `MemoryStore`：整数 GCRA 参考实现，只用于 RL1 parity、测试和受控单副本回滚；
- `PostgresStore`：独立小 pool + consume/ready 函数；ready 读取 DB active version，装配层再从
  最多两-slot keyring 选择对应 key，并原子发布本地 ready snapshot；
- HTTP middleware：不理解 TAT/SQL，只处理 context、Store decision 和统一错误响应；
- River cleanup worker：只调用 prune function，不读写 bucket table。

Store 永远只接收 digest，不能接收 raw principal/path。`MemoryStore` 与
`PostgresStore` 通过同一 golden cases，避免 shadow disagreement 是算法不同而不是共享性不同。

### 8.3 backend modes

未来运行模式只有：

| mode | 响应权威 | PG side effect | 允许场景 |
|---|---|---|---|
| `memory` | MemoryStore | 无 | 当前基线；或获批且已缩为 1 个 API 的紧急回滚 |
| `shadow` | MemoryStore | consume 写桶、比较结果 | RL3 staging soak；不得直接生产启用 |
| `postgres` | PostgresStore | consume 写桶 | RL4 staging/production 分别批准后 |

没有 `off`、`auto`、`degraded-local`、`fail-open`。shadow 的 PG 错误不改变本次响应，
但必须进入指标/ready；postgres 的任何不可判定均 503。

## 9. HTTP、探针与故障语义

### 9.1 响应矩阵

| 情况 | HTTP | error.code | Retry-After | handler |
|---|---:|---|---|---|
| 允许 | 继续 | 无 | 无 | 调用一次 |
| 配额耗尽 | 429 | `RATE_LIMITED` | GCRA 计算的正整数秒 | 不调用 |
| DB/timeout/ACL/invalid result | 503 | `RATE_LIMIT_BACKEND_UNAVAILABLE` | `1` | 不调用 |
| policy/key mismatch/missing | 503 | `RATE_LIMIT_BACKEND_UNAVAILABLE` | `1` | 不调用 |
| principal/context invariant 失败 | 503 | `RATE_LIMIT_BACKEND_UNAVAILABLE` | `1` | 不调用 |

503 外部文案不泄漏 DB host、role、digest、policy 内容或 secret ref。内部日志只记录 bounded
kind、request ID、route template、backend、policy revision（若可信）；根因错误按现有日志
脱敏纪律处理。

### 9.2 probes

- `/healthz` 保持进程存活语义，不查限流或数据库；
- `/readyz` 组合现有 DB Ping 与 `RateLimitStore.Ready`；
- postgres/shadow mode 的 Ready 至少证明：专用 pool 可达、policy 存在、algorithm 匹配、
  DB active key version 可从最多两-slot keyring 精确选择、可选 staged material 也已严格解析、
  ready 函数可 EXECUTE、返回结构可解析；
- probes 仍在 `/api/v1` 外，因此不消费 bucket；
- readiness 失败只让负载均衡摘副本，不触发进程因依赖抖动反复重启；
- shadow 模式 ready 不得伪装成“共享限流已可 enforce”：响应/日志要暴露 mode。

### 9.3 共享计数器故障策略

**正式 enforcement 一律 fail-closed。** 管理平台故障不得影响上游用户实时请求，因此
选择 503 比放大数据库读取或让限流静默失效更安全。禁止：

- DB 超时后在当前副本临时用内存桶放行；
- 读取上次 decision 缓存继续放行；
- 某副本 postgres、某副本 memory 的混合 enforcement；
- 用 ready 绿代替请求路径的 fail-closed 测试。

唯一回退是人类批准、部署层先确认 API replica=1，再把全体实例切回 memory 的受控操作。

## 10. TTL 与清理

默认保持现有语义：idle TTL 15m、cleanup interval 10m。
`prune_rate_limit_buckets(environment, batch_size)` 按该 environment **当前 active policy**
的 TTL 清理；旧 revision/key version 也明确使用当前 TTL，不再假装能读取已被替换的旧 TTL：

- 每批最多 2000 行，参数有硬上限；
- 以 `(environment,last_seen_us)` 索引选取，`FOR UPDATE SKIP LOCKED`，短事务；
- deny 也更新 last_seen，持续攻击中的桶不会因 TTL 被删后重获 burst；
- 旧 policy revision/key version 同样按 last_seen 清理；
- River args 必含 environment，并以 kind+args+queue+10m period 唯一；至少一次语义下重复执行幂等；
- 失败让 River 重试并报警，不影响 consume 正确性；
- runtime prune 只返回 `deleted_count, has_more, backlog_capped, oldest_seconds`；候选最多读取
  `batch_size+1`，`backlog_capped <= batch_size+1`，不能在 10m job 内做全量 COUNT；
- `has_more`/oldest age 超阈值报警；不能用无界 `DELETE`；精确 backlog count 只允许获批的
  低频只读诊断，以 migrator/另批 ops 身份在维护证据窗口执行，不进入 prune routine/request path；
- `audit.audit_event`、审计锚、Action 历史与其它 append-only 表完全不在函数可见范围。

worker 只获 prune EXECUTE，不获 bucket DELETE。RL2 在至少 1,000,000 行跨 environment/revision fixture
上运行 `EXPLAIN (ANALYZE, BUFFERS)`，证明目标索引与 batch-bounded scan，并并发证明 deny-touch
活跃桶不被删除。

## 11. 配置与 CredentialRef

预期新增配置（仅在对应后续片获批后）：

| 配置 | 默认/约束 | 权威 |
|---|---|---|
| `XM_RATE_LIMIT_BACKEND` | development 缺省 memory；staging/production 必须显式；enforcement 批准后 manifest 必须 postgres | 部署 |
| `XM_RATE_LIMIT_HMAC_PRIMARY_KEY_REF/VERSION` | postgres/shadow 必填；version 为正 int32 | 部署 keyring slot |
| `XM_RATE_LIMIT_HMAC_STAGED_KEY_REF/VERSION` | 成对可选；与 primary 版本/ref 不同 | 部署 keyring slot；最多第二把 |
| `XM_RATE_LIMIT_DECISION_TIMEOUT` | 默认 100ms，必须正且小于 request timeout | 部署 |
| `XM_RATE_LIMIT_POOL_MAX_CONNS` | 默认/上限均 4；调高需容量审批 | 部署 |
| 现有 per-minute/burst | memory 权威；PG 仅 lifecycle bootstrap 输入 | DB policy 切换后不再由 API 读 |

fail-closed 是代码与响应映射的不变量，不提供 failure-mode 变量。真实 secret、DSN 和 role
password 均由人类配置。装配层用真实 API：

```go
audited := secrets.NewAudited(inner, recorder, cfg.Environment)
resolveCtx := secrets.WithCaller(ctx, "api:platform")
value, err := audited.Resolve(resolveCtx, ref, "platform-api rate-limit bucket HMAC")
```

primary/staged 每个 slot 的成功和失败都必须产生 AccessRecord，精确断言 caller、purpose、
environment、CredentialRef、provider、success/error_code；审计/日志/error 不含 secret value、
decoded bytes 或 digest。新增 ref 要登记 Provider；ref scope environment mismatch、缺失/未知均拒绝 ready。

## 12. 可观测性与隐私

### 12.1 bounded signals

基线没有 Prometheus/OpenTelemetry exporter，RL3 不为限流单独引入新依赖。实现先定义
bounded `Observer`，以固定延迟 buckets/原子计数聚合，并每 60 秒输出一条结构化
`rate_limit_summary`；cleanup 每轮输出一条 bounded 结果。下列名称是稳定逻辑信号名，
未来接入统一 exporter 时原样复用：

- `rate_limit_decisions_total{backend,decision,route}`；
- `rate_limit_decision_duration_seconds{backend,route}`；
- `rate_limit_backend_errors_total{backend,kind}`；
- `rate_limit_retry_after_seconds{route}`；
- `rate_limit_pool_acquire_duration_seconds`；
- `rate_limit_row_lock_wait_seconds`（能可靠采集时）；
- `rate_limit_shadow_difference_total{memory_decision,postgres_decision,route}`；
- `rate_limit_shadow_classified_total{class,route}`，class 仅
  `algorithm_mismatch|expected_consolidation|unclassified`；
- `rate_limit_clock_rollback_total`；
- `rate_limit_cleanup_deleted_total`、`rate_limit_cleanup_has_more`、
  `rate_limit_cleanup_backlog_capped`、`rate_limit_cleanup_oldest_seconds`。

label 只用枚举 backend/decision/kind 和已注册 route template；禁止 principal、digest、
raw path、query、request ID 或 error string 进入 label。

### 12.2 日志与数据库隐私

- 限流日志不记录 principal ID、canonical bytes、digest、HMAC key/ref 或 raw URL；
- request ID 可以进日志但不进 metric label；
- DB bucket 只含 environment/revision/version/digest/整数时刻；
- 测试查询证明 principal ID/route 文本不在表字节中；
- ops 默认看聚合视图，不给 bucket 明细导出；
- Handoff 不附 DSN、secret 值或可复用 credential。

残余威胁必须进入 Handoff：同一 key version 的 digest 仍泄漏等值/活跃度关系；同时取得 DB
备份与 HMAC key 可枚举低熵 ID；被攻陷的 API 可持 key 制造/消耗桶；route 聚合暴露流量
形状。备份/key 分域、两-slot 限界和禁止 bucket 导出降低风险，但 HMAC 不是匿名化，也不防已控 API。

## 13. 验证矩阵

### 13.1 纯模型与契约（RL1）

1. GCRA burst=N 同时恰放 N 次，第 N+1 次拒绝；
2. 稳定补充、长时间 idle 不超过 burst；
3. Retry-After 向上取整且至少 1 秒；
4. synthetic clock backward 不增加额度；
5. integer overflow/bounds、非法 policy fail closed；
6. canonical 长度前缀、Unicode、NUL/冒号、method/route/environment/type 独立；
7. unknown route 使用固定 sentinel，不含原始 path；
8. HMAC deterministic、不同 key/version 不同 digest、日志不泄漏；
9. MemoryStore 与 golden GCRA 完全一致；
10. 现有 HTTP 429/Retry-After/probe 回归保持全绿；新 503 接线测试留在 RL3。

### 13.2 disposable PostgreSQL 18（RL2）

使用随机 project/volume/database、受限测试角色和锁定的 PG18 digest：

- migration up/down 只在 disposable DB；production 只验证 forward migration；
- 两个独立 pgx pools 模拟两 API，100 和 1000 并发请求打同一 key；在
  `per_minute=1, burst=20` 下总允许精确 20，而不是每 pool 20；
- 两个不同 key 各自允许 20，证明无全局锁；
- 未来 `last_seen` fixture 模拟 DB clock rollback，不能重获 burst；
- policy revision/key mismatch/missing；函数返回与 Retry-After 边界；
- enforcement 与 cleanup 并发，活跃 denied bucket 不被重置；
- API role 只能 consume/ready，不能 SELECT/DML/prune/DDL；
- worker role 只能 prune，不能 consume/直接 DELETE；PUBLIC 无 EXECUTE；
- SECURITY DEFINER owner/search_path/default ACL drift verifier；
- DB 表内找不到原 principal/route，只存在 32-byte digest；
- 终止连接、停止 PG container、撤函数 EXECUTE、制造 acquire timeout 时返回 503，
  handler 从未执行；ready 同步变红；
- 容器/roles/volume 最终清理，并证明未触及外部 DSN。

### 13.3 staging 两副本 shadow/enforcement（RL3/RL4）

在单独批准的 staging 变更窗：

- 明确启动两个 API 副本并证明二者 `application_name`、build、backend、policy revision；
- shadow 比较 memory/PG decision，不以 PG 结果影响响应。live difference 先记
  `unclassified`，不得从单请求猜原因；单副本有序 parity lane 才可判
  `algorithm_mismatch`，两副本聚合额度的受控 trace 才可判 `expected_consolidation`；
- 并发、时钟偏移（两个 app clock 相反偏移）、热键/多键、DB restart、网络延迟、pool
  饱和、cleanup backlog；
- 先跑同负载 memory baseline 与 shadow 各 30m warm-up + 60m measurement；每段至少
  100k decision、top route 各至少 10k，不足就延长但不降低样本门；再跑至少 24h ambient
  soak。跨 fleet 汇总 60s Observer snapshot，连续 5 个 1m window 才称“持续”；CPU/IO/WAL
  增幅使用同一 replay、同副本数的配对 baseline；
- 进入 postgres enforcement 前证明没有 memory/postgres 混合副本；
- 429、503、ready 摘除、恢复、单副本 memory 回滚均实测；
- production 仍需独立批准，不能因 staging 通过自动部署。

## 14. 部署、回滚与恢复

### 14.1 顺序

1. RL1 合入纯契约/参考模型，不改变 runtime；
2. DBR1 verifier 合入并扩展限流对象 policy；
3. RL2 迁移/函数/PG Store 在 disposable PG18 通过；
4. 分别取得 migration 与 CredentialRef 审批；
5. DBR2/DBR3 建立 owner、ACL、API/worker restricted roles；
6. 人类在 staging 配置真实 ref，应用 migration/bootstrap；API 仍 memory；
7. staging 切 shadow，两副本 soak；
8. 单独批准 staging postgres enforcement；
9. 通过故障/回滚/恢复演练与阈值审查；
10. 单独 production change 审批后，先发布可理解 schema 的 binary；enforcement 激活仍走
    fleet-wide quiesce，不以 rolling/canary 形成混合 backend。

不得把 migration、secret、shadow、enforcement 合成一个不可回滚的大爆炸发布。staging 与
production 激活都必须：入口阻断 → 全副本 drain → 全 fleet 切换 → 逐副本离线 ready/inventory
验证 → 确认零 memory/shadow serving 实例 → 一次恢复流量。不存在“首个 postgres 副本先回流”的 canary。

### 14.2 应用回滚

优先回滚到仍理解现有 schema/policy 的前一版本，数据库对象保留。若必须回到 memory：

1. 人类阻断入口并排空全 fleet；
2. 把 API 缩为精确 1 个副本，证明无 serving/mixed backend；
3. 从最后获批 DB policy 显式回填 memory quota，将该副本切 memory 并验证 429/probes；
4. 保留 PG 表/函数和证据，不执行 live down migration；
5. 记录事故、原因和恢复到 postgres 的新审批。

绝不自动 fail-open；多副本 memory 不是回滚完成态。

### 14.3 数据库回滚/恢复

- migration 遵守 forward-only；live 不跑 `down`；
- 函数缺陷用新 migration 修复，或应用回滚停止调用；
- bucket 是可重建短期安全状态，不进入长期业务恢复承诺；丢失 bucket 会重置 burst，
  因此恢复/重建时必须在维护窗或阻断入口下完成；
- policy 是配置真值，必须进入数据库备份/恢复验证；恢复后先 ready 验 policy revision/key
  version，再接流量；
- HMAC key 不与 DB 备份同存，遵守 ADR-014；缺 key 时 503，不生成替代 key。

## 15. RL0～RL4 分片与审批闸

| 片 | 交付 | 可以申请的批准 | 明确 STOP/live 状态 |
|---|---|---|---|
| **RL0** | 本 design + implementation plan | docs 审批 | docs-only；GO |
| **RL1** | domain types、Store interface、canonical/HMAC、纯 GCRA golden、未接线 MemoryStore | RL0 批准后可申请“代码但无 runtime 行为”批准 | 无 migration/secret/compose/DB；可另批，live NO-GO |
| **RL2** | PG migration/functions/PostgresStore/cleanup + DBR policy + disposable PG18 权限/并发/故障测试 | **本文不能批准**；需更新基线、DBR1、精确 migration review | migration、DBR、CredentialRef 全 STOP；不接 staging |
| **RL3** | runtime config/small pool/ready/shadow、staging 两副本 soak | **本文不能批准**；migration、DBR、CredentialRef、staging 四批齐全后另审 | memory 仍响应权威；production STOP |
| **RL4** | staging postgres enforcement、回滚/恢复证据；随后 production change | staging enforcement 与 production 各自独立审批 | 当前全部 NO-GO；不自动部署 |

审批不继承：

1. 批准 RL0 不等于批准 RL1；
2. 批准 RL1 不等于批准 migration/DBR/secret/runtime wiring；
3. migration SQL、DBR owner/ACL、CredentialRef/真实值、staging DB、staging enforcement、
   production 各是单独 STOP；
4. 只有 RL0/RL1 目前可申请批准；RL2～RL4 必须带最新证据重新申请；
5. Codex 不执行 live database、真实凭据、合并或部署；
6. 当前 live enforcement 明确为 **NO-GO**。

## 16. Redis/Valkey 强制重评阈值

阈值只用 §13.3 的配对 60m measurement 与 60s fleet snapshot 计算；“持续”固定为连续 5 个
完整 1m window，比例分母为该 window 全 fleet decisions，DB 增幅相对同 workload baseline。
样本/窗口缺失、`unclassified` 未闭合、任一 `algorithm_mismatch` 或连接预算不通过均直接
BLOCKED，不得解释成未触发阈值。RL3 shadow/soak 出现任一项，停止 PG enforcement 扩大并
新建 ADR 比较 Redis 与 Valkey：

1. 限流 decision p95 持续 `>20ms` 或 p99 `>50ms`；
2. timeout、pool acquire failure 或 row-lock wait 比例持续 `>0.1%`；
3. 限流造成数据库 CPU、IO 或 WAL 持续增加 `>5%`；
4. 管理 API 受保护流量持续 `>500 requests/second`；
5. 单热键锁等待影响其它键或正常 Query SLO；
6. 需要跨地域 active-active/独立一致计数器故障域；
7. PG backup/replication/maintenance 因短期 bucket WAL 明显恶化；
8. 产品需要原生 TTL、大量动态 policy 或远高于控制面规模的 key cardinality。

重评必须比较 Redis 与 Valkey 的锁定版本、license、HA、持久化、备份/恢复、secret、
监控、Lua script version、cluster time、故障策略与完整 TCO；不能只以“更快”批准新服务。

## 17. 风险、开放事实与非承诺

- 当前 staging compose 只有一个 API；两副本编排是 RL3 待证，不得写成已完成；
- DBR0 分支尚未合入，角色名/ACL 仍待人审；本设计不能提前使用 live role；
- PG 每请求写一行会增加 WAL；是否可接受必须由 shadow 数据而非推理决定；
- policy revision 或 key version 切换会给每键新 burst，这是明确的切换效应；
- DB primary 时钟是单一权威但不是绝对真时；只防向后跳的额外放行；
- shared counter 故障会让管理 API 503，这是选定的安全/可用性权衡；
- 现有 AccessLog 的 path 记录不由本片重构；新增限流日志/metrics 不得扩大该暴露面；
- 本设计没有真实 staging/production 测试、凭据、备份恢复或容量证据。

## 18. RL0 审批清单

- [ ] 接受 PostgreSQL 18 GCRA 为首选、Redis/Valkey 按 §16 重评、反代不作主限流；
- [ ] 接受 key v2 + HMAC-SHA256，数据库不存 raw identity/path；
- [ ] 接受 DB policy 为 PG backend 唯一权威、env 仅一次性 bootstrap；
- [ ] 接受专用小 pool、DB clock、per-key row lock、deny 更新 last_seen；
- [ ] 接受 429/503 fail-closed，禁止自动 local fallback/fail-open；
- [ ] 接受 health 不查依赖、ready 验 policy/function/grant/key version；
- [ ] 接受 SECURITY DEFINER 窄函数及 DBR verifier/ACL 前置；
- [ ] 接受 RL0/RL1 可申请批准，RL2～RL4 与 live enforcement 当前 NO-GO；
- [ ] 接受 migration、DBR、CredentialRef、staging、production 审批互不继承。

## 19. 官方技术依据

- PostgreSQL 18 `INSERT ... ON CONFLICT`：
  <https://www.postgresql.org/docs/18/sql-insert.html>
- PostgreSQL 18 时间函数（`clock_timestamp()`）：
  <https://www.postgresql.org/docs/18/functions-datetime.html>
- PostgreSQL 18 显式/行级锁：
  <https://www.postgresql.org/docs/18/explicit-locking.html>
- PostgreSQL 18 函数安全：
  <https://www.postgresql.org/docs/18/perm-functions.html>
- Redis rate limiter pattern：
  <https://redis.io/docs/latest/develop/use-cases/rate-limiter/>
- Valkey `EVAL` / `TIME` / `PEXPIRE`：
  <https://valkey.io/commands/eval/>、<https://valkey.io/commands/time/>、
  <https://valkey.io/commands/pexpire/>
- Nginx `limit_req`：
  <https://nginx.org/en/docs/http/ngx_http_limit_req_module.html>

**批准本文只批准 RL0 设计进入评审；不授权 RL1 或任何 live 变更。**
