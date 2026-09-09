# R2-10 多副本任务唯一性与集群作业所有权设计

> **状态：审批设计，不是实现授权。** 本文只冻结星芒周期任务在多副本环境里的
> 所有权、租约、二次去重、版本能力与重复探测语义。它不授权迁移、增加副本、
> staging/production 切换、外部写操作或合并。

## 1. 结论

推荐复用已钉版本的 **River OSS PostgreSQL leadership** 作为 `platform-worker`
周期任务的唯一调度租约，同时保留 River job unique 作为第二道入队防线，再以
仓库内 job manifest、签名 fleet manifest 和上一完整窗口的重复探测形成可审计证据。

不新增第二套自研租约表，也不把 Kubernetes/Compose 副本选举当业务真相。双重
leader 会出现两套 TTL、两套故障切换和互相不知道的所有权，反而更难证明。

阶段裁决：

| 阶段 | 结论 |
|---|---|
| R210-0 本设计 | 可审批 |
| R210-1 manifest/纯契约/当前六任务盘点 | 条件 GO；无迁移、无运行时切换 |
| R210-2 cluster/replica identity、两客户端真 PG 验证 | 独立批准；仅 disposable PG；DBR binding-read契约另批 |
| R210-3 probe/指标/告警/只读 UI | NO-GO；契约、DBR、告警规则另批 |
| R210-4 staging 双副本验收 | NO-GO；人类批准的 staging 窗口 |
| production 多副本 | NO-GO；必须基于 R210-4 新证据再批 |

**R2-10 闸门只有在 R210-4 通过后才算满足。** 文档、单元测试或 River 的一条
unique 配置都不能单独证明集群级唯一性。
即使满足 R2-10，也不自动解锁自动写、插件或高频采集；R2-13/14/15 及各业务审批
仍必须分别通过。

## 2. 需求来源与边界

第二轮需求 R2-10 的核心风险是：多副本会重复备份、探测、同步和定时测试，制造
N 倍上游负载、重复对象或覆盖。报告要求区分逻辑集群与副本，使用集群级租约、
幂等键、重复证据与版本能力矩阵。

边界保持：

- 星芒只能协调自己的任务，不修改 Sub2API/NewAPI/CPA 内部 cron；
- 不直改第三方数据库抢锁；
- 不把“一个 JobRow”误说成“Work 只调用一次”；River 工作语义仍是 at-least-once；
- 外部 GET 重试可能重复读取，必须进入 R2-15 采集预算，不能宣称 exactly-once；
- 任何自动写仍等待 Foundation-B/P0 全部门禁与业务幂等契约。

## 3. 当前事实

### 3.1 当前周期任务

`internal/platform/jobs/client.go` 在一个 River client 中登记六个稳定
`PeriodicJobOpts.ID`：

| periodic ID / kind | 默认周期 | RunOnStart | 队列 | 主要副作用 |
|---|---:|---:|---|---|
| `platform_heartbeat` | 60s | 是 | default | 平台审计心跳 |
| `sub2api_sync` | 300s | 是 | default | 上游只读 GET + 最新态/样本事务 |
| `newapi_sync` | 300s | 是 | default | 上游只读 GET + 最新态/样本事务 |
| `finance_collect` | 300s | 是 | default | 上游只读取数 + 成本/利润台账事务 |
| `retention_prune` | 24h | 否 | maintenance | 有界删除已批准数据 |
| `alert_evaluate` | 60s | 是 | maintenance | 告警 reconcile/投递 |

每类 Args 当前均声明 `ByArgs + ByQueue + ByPeriod`；生产 RunID 强制为空，所有副本
因此共享相同 unique key。周期构造器又把 `ByPeriod` 调整为实际配置周期。

### 3.2 River 已经提供调度 lease

仓库钉 `river v0.45.0`。该版本在同一 PostgreSQL database/schema 的
`river_leader(name='default')` 上选出唯一 leader；leader term 使用数据库签发时间与
TTL，续约失败超出本地信任窗口会保守退位。`PeriodicJobEnqueuer` 只随 leader 运行。

因此当前不是“每个副本都无条件插一遍周期任务”。真实缺口是：

1. 仓库没有把上述 River 领导权作为正式受测契约；
2. 没有冻结“一个 database/schema 只属于一个 environment/逻辑 worker cluster”；
3. 没有证明两个真实 client 的 leader failover 与每槽唯一 JobRow；
4. 没有 job manifest/effective digest，滚动版本可能登记不同任务或周期；
5. 没有区分 logical duplicate 与同一 JobRow retry 的探测证据；
6. 没有 future job 准入门，新的 goroutine/ticker 仍可能绕过 River。

### 3.3 当前部署

当前 `launch.yaml` 只有一个 `platform-worker` 服务实例，没有已发生的 N 倍执行事故。
R2-10 是扩容前置闸门，不是当前事故修复。

## 4. 三种方案

| 方案 | 优点 | 风险 | 裁决 |
|---|---|---|---|
| River 内建 leader + job unique + manifest/probe | 已钉版本、DB 时间、同一事务/队列、无新服务 | 必须补黑盒验证和 rollout 纪律 | **采用** |
| 自建 `job_lease` 表包住 River | 表面可控 | 双 lease/双 TTL、故障状态组合爆炸 | 否决 |
| 外部 Kubernetes CronJob/单独 scheduler | 基础设施可见 | 本地/Compose 不一致、仍需业务幂等与 DB fence | 暂不采用 |

未来非 River scheduler 必须另立设计；不得用本设计批准任意 `time.Ticker`。

## 5. 四层所有权语义

### 5.1 调度 lease

同一 `database + river schema` 只有一行 `river_leader/default`。它只证明“谁可以
安排周期 JobRow”，不证明 Job Work exactly-once。

强约束：一个 River schema 只能服务一个 `environment + worker_cluster_id`。不同环境
或逻辑集群不得共享 schema；需要共库时必须使用独立 River schema 并另批迁移/DBR。

### 5.2 入队二次防线

每个 cluster-singleton 任务必须同时有：

- 非空、稳定、全局唯一的 `PeriodicJobOpts.ID`；
- Args `ByArgs=true`、`ByQueue=true`；
- `ByPeriod` 精确等于 effective schedule interval；
- `ByState` 不依赖未来库默认漂移，代码显式使用 v0.45
  `rivertype.UniqueOptsByStateDefault()`，manifest 冻结同一状态集合；
- production Args 不含 per-replica 隔离字段；
- metadata 保留 River `river:periodic_job_id`。

Unique 只负责同槽第二次 INSERT 返回 duplicate，不替代 leader lease。

### 5.3 执行 claim

River 对一个 JobRow 做独占 claim，但 crash/timeout 可重试同一行：

- 一行 `attempt > 1` 是 retry，不是 logical duplicate；
- 同 `cluster/job/slot` 出现多行才是 duplicate logical job；
- 同槽两行执行时间重叠是 duplicate overlap；
- 外部请求在 crash 前已发出但 DB 未提交，重试仍可能再次读取。

### 5.4 业务幂等/补偿

每个 manifest 条目必须声明 side-effect class 和证据：

- DB upsert/transaction：唯一键或同事务 read-modify-write；
- bounded delete：重复执行不扩大 cutoff/范围；
- upstream read：允许 retry，但记入采集预算/attempt；
- external write：本期一律禁止，未来必须有 Foundation-B、幂等键、读回和补偿。

## 6. JobManifestV1

仓库新增不可变 `contracts/jobs/cluster-jobs.v1.json`。严格 loader 拒绝 unknown field、
重复 ID/kind、非正周期、缺证据、未注册 job 或任意非 River scheduler。

```json
{
  "version": 1,
  "scheduler": "river-oss-postgres-leader-v0.45",
  "cluster_model": "one-environment-per-database-schema",
  "jobs": [
    {
      "id": "sub2api_sync",
      "kind": "sub2api_sync",
      "queue": "default",
      "owner_process": "platform-worker",
      "ownership": "cluster_singleton",
      "schedule_config": "XM_SUB2API_SYNC_INTERVAL",
      "run_on_start_source": "jobs.DefaultConfig.Sub2APISyncRunOnStart",
      "catch_up": "at_most_one_immediate",
      "enqueue_fences": ["river_leader/default", "args+queue+period"],
      "unique_states": ["available", "completed", "pending", "running", "retryable", "scheduled"],
      "execution": "at_least_once",
      "side_effect_class": "upstream_read_then_db_transaction",
      "idempotency_evidence": "latest+sample atomic transaction; retry remains upstream-read attempt"
    }
  ]
}
```

v1 必须精确覆盖当前六任务。新增/删除/改 ID、queue、schedule source、catch-up、side
effect 或 fence 都发布新 manifest version，不能原地改 v1。

## 7. EffectiveJobManifest

静态 contract 不含部署值。每个 replica 在启动前根据同一 contract 与非秘密配置解析：

```go
type EffectiveJobSpec struct {
    ID, Kind, Queue string
    Enabled, RunOnStart bool
    IntervalSeconds int64
    Ownership, CatchUp, SideEffectClass string
}
type EffectiveJobManifest struct {
    ContractVersion int
    Environment, WorkerClusterID, RiverSchema string
    Jobs []EffectiveJobSpec // ID byte-order sorted
}
```

canonical JSON 固定 UTF-8、字段顺序、整数十进制、数组按 ID 排序，SHA-256 作为
`effective_manifest_hash`。以下任一不一致拒绝 ready：

- process environment 与 fleet manifest environment 不等；
- cluster ID/river schema 不等；
- effective job set/interval/RunOnStart/hash 不等；
- River migration version或 library version不在 capability matrix；
- production RunID 非空；
- worker binary登记了 contract 不认识的 Periodic ID。

## 8. Cluster 与 replica identity

```go
type WorkerClusterIdentity struct {
    Environment, WorkerClusterID, RiverSchema string
    ManifestVersion int
    EffectiveManifestHash, DatabaseBindingHash string
}
type WorkerReplicaIdentity struct {
    LogicalReplicaID, BootID, BuildDigest string
}
```

- `worker_cluster_id` 是部署批准的非秘密稳定 ID；同一 environment/schema 唯一；
- logical replica ID 由 orchestrator slot 提供；同一时刻不可重复；
- BootID 由进程用 CSPRNG 生成，重启即变；
- River client ID 固定为 `<cluster>/<logical-replica>/<boot-id>` 并满足 127 字节上限；
- 不把 hostname、PID、raw DSN、凭据写入业务 API/指标。

## 9. Signed JobFleetManifestV1 与能力矩阵

部署前由批准流水线签名：

```text
environment, worker_cluster_id, river_schema, database_binding_hash, epoch,
job_contract_version, effective_manifest_hash,
active_binding_inventory_epoch, active_binding_inventory_sha256,
replicas[{logical_replica_id,build_digest}],
build_capabilities[{build_digest,river_version,river_migration_version,
                    job_contract_version,effective_manifest_hash}],
issued_at, valid_from, valid_until, change_ref, nonce
```

同一签名域还包含 `JobFleetInventoryV1`：按 binding hash 排序的 active/closed
binding→environment/cluster 映射、单调 epoch、issued/validity/nonce；fleet manifest
钉 inventory epoch/digest。runtime 同时验签两件 artifact，禁止只信 manifest 自己声称
“没有冲突”。

runtime 只持有 repo-pinned public keyring。`database_binding_hash` 是建连后由
PostgreSQL 18 `pg_control_system().system_identifier + current_database() + river_schema`
的固定 bytes 计算 SHA-256：每段为 `u32be(length) || UTF-8 bytes`，依次是
`system_identifier::text`、database、schema，不做 Unicode normalization。system
identifier 是 cluster-wide 事实，
可跨物理流复制成员保持同一绑定，原值不进入日志/API。DBR 必须显式批准受限 worker
读取这两个非秘密事实（直接精确 EXECUTE 或安全固定函数二选一），不能因此授 catalog/
owner 权限。签发流水线维护 append-only active binding history，同一 binding hash 不能
同时处于两个 active environment/cluster。

PostgreSQL 18 控制数据函数依据：
`https://www.postgresql.org/docs/18/functions-info.html#FUNCTIONS-INFO-CONTROL`。

验证分两段：签名/slot/build/effective manifest 在读取 secret/建连前通过；解析数据库
CredentialRef 并建立安全连接后，只运行 binding/version/readiness 查询，再要求 binding
hash 相等，最后才创建/启动 River client。每个 replica 必须恰好出现一次。unknown
build/replica、过期、wrong purpose/protocol/signature、重复 slot、binding 或 manifest hash
不同均 fail closed。实现不得用 host/port/DSN 字符串冒充数据库身份。

同一 effective manifest hash 下允许 current/next build 滚动；job manifest 改变时禁止
滚动混跑，必须 fleet-wide quiesce：阻断入口/排空 worker、停止全部旧 client、更新签名
manifest、启动并验证全部新 replica、再恢复任务。错过窗口按每项 catch-up 规则最多补一轮，
不逐槽追跑。

所有 ID 使用 ASCII `[a-z0-9][a-z0-9._-]{0,62}`；build digest 为
`sha256:<64 lowercase hex>`，manifest/binding/inventory digest 为 64 lowercase hex；epoch
为正整数且严格递增；validity 为 UTC half-open `[valid_from,valid_until)`，最长有效期由
部署策略批准。超长、Unicode、路径分隔符、重复 nonce 与 unknown field 均拒绝。

## 10. 重复与缺口探测

R210-3 新增 `job_ownership_probe`（它自己也必须写入 manifest 新版本）。probe 只检查
**上一完整 slot**，不读 Job Args/payload，不向 API 暴露 River 原表。

输入：cluster identity、effective manifest hash、DB clock evaluation time。查询仅用：
`river_leader` 的存在/expiry，以及 `river_job` 的 id/kind/queue/state/attempt/
attempted_at/finalized_at/scheduled_at/metadata periodic ID。
slot 与 leader remaining 都以一次 PostgreSQL `clock_timestamp()` 快照计算，不使用各副本
本地时钟；Query/报告携带同一个 `evaluated_at`。

稳定分类：

| code | 含义 |
|---|---|
| `ownership_ok` | 每个 enabled job 在应有槽恰好一条 logical JobRow |
| `logical_duplicate` | 同 job/slot 多于一行 |
| `execution_overlap` | duplicate rows 的执行区间相交 |
| `logical_missing` | 完整槽没有应有 JobRow |
| `failover_gap_expected` | 已批准故障注入窗口内、leader term 切换可证明且不超过一槽的缺口；单列，不算正常 |
| `unexpected_job_id` | DB 出现 manifest 未登记 periodic ID |
| `leader_missing` / `leader_expired` | 当前无可信 scheduler lease |
| `manifest_mismatch` | probe 参数/运行 manifest 不一致 |
| `retry_observed` | 同一 JobRow attempt >1；单列，不计 duplicate |
| `evidence_truncated` | JobRow retention 不足以覆盖请求窗口 |

probe 写一个低基数 ops observation `platform.jobs.ownership`，包含 job 数、duplicate/
missing/retry 数、manifest version/hash、evaluated_at、coverage；不写 raw leader/client ID。
probe 失败或不再运行会让 observation 变 stale，由 R210 告警规则捕获。

value contract v1 固定：

```text
version=1, environment, worker_cluster_id,
job_manifest_version, effective_manifest_sha256,
evaluated_at, leader_present, leader_term_remaining_seconds,
coverage_complete, coverage_from, coverage_to,
jobs[{job_id,slot_from,slot_to,logical_rows,attempts,code}],
logical_duplicate_count, logical_missing_count, unexpected_job_count, retry_row_count
```

时间为 UTC RFC3339 微秒；count 为非负整数；jobs 按 job ID/slot 排序；hash 是完整
64hex（UI 只显示前缀）。unknown/malformed/重复 row 一律 fail closed，不把它映射成 ok。

## 11. 故障与 failover

- leader DB 续约失败超过 trust window：必须停止 enqueue；不得本地延长 lease；
- follower 只在 DB lease 过期/显式 resign 后接任；
- leader crash 后只在有 term/fault-window 证据时允许最多一槽
  `failover_gap_expected`，不允许 duplicate logical row；其余缺口仍是 `logical_missing`；
- PostgreSQL restart 会清空 UNLOGGED leader row；恢复后必须重新选举，并由已持久化
  JobRow unique 防止 RunOnStart/slot 重复；不能把旧 leader 记忆带过 restart；
- DB 不可用时任务不 enqueue、不执行下游；health 可活，ready 失败；
- 同一 JobRow retry 保持 River 语义；probe 单列 attempts；
- job manifest mismatch 时不启动 River，避免错误 leader接管；
- 正常 soak 中 duplicate/missing/unexpected 任何非零均阻止验收；故障注入阶段只允许
  已绑定 term/fault-window 的 bounded `failover_gap_expected`，超界即失败。

## 12. 可观测与只读 UI

结构化字段：cluster ID、environment、manifest version/hash、job ID、slot start/end、
logical row count、attempt count、stable code、River version/migration version、request ID。
禁止 raw args、leader/client ID、DSN、credential ref/value。

低基数指标：

- `job_ownership_slots_total{job_id,result}`；
- `job_ownership_duplicates_total{job_id}`；
- `job_ownership_missing_total{job_id}`；
- `job_ownership_retries_total{job_id}`；
- `job_leader_present`、`job_leader_term_remaining_seconds`；
- `job_manifest_info{version,hash_prefix}`；
- `job_probe_age_seconds`。

R210-3 可在全局“作业”页展示只读集群/manifest/leader/probe/六任务表；来源仅为安全
projection/ops observation。禁止右 Drawer、手工抢主、强制解锁或从 UI 重跑任务；任何
控制面动作另走 Foundation-B。

## 13. 安全与 DBR

- worker 使用现有 River runtime 身份；只增加 probe 所需最小 SELECT/projection；
- API 不直接 SELECT `river_job`/`river_leader`；只读 ops observation；
- DBR policy 以新版本/独立 CR 精确列 River leader/job安全列与禁止列；
- probe 查询固定 SQL，无调用方表名/schema/where；
- fleet manifest keyring 只验签，不包含私钥；
- River migration/bundle mirror继续由 pinned verifier保证；
- 不使用 superuser/owner 身份证明权限正确。

## 14. 验证矩阵

### 14.1 纯契约

- manifest 精确覆盖六任务及代码常量；
- 每个 periodic ID 非空且 unique；
- Args/queue/period与 contract一致；
- RunID production 为空；
- canonical/effective hash跨进程 golden一致；
- unknown job/config/field/version fail closed。

### 14.2 真 PostgreSQL 两副本

在 harness 自持的 digest-pinned PG18 中启动两个独立 River client：

- client ID/BootID不同，cluster/environment/schema/manifest相同；
- `river_leader/default` 始终最多一行；
- 至少 3 个短测试槽，每 job/slot恰一 logical row；
- 同一行 retry不被误报 duplicate；
- 杀 leader 后 bounded failover，新 leader接任且无 overlap row；
- 重启 PostgreSQL 后重新选举且持久 JobRow不重复；
- 隔离旧 leader 的 DB 网络直到 trust expiry，旧进程不再 enqueue；
- duplicate client ID、cross-environment same schema、manifest mismatch均在 start/ready 前失败；
- 注入 duplicate/missing/unexpected/retry，probe 分类逐字正确；
- harness 外部 DSN/0.0.0.0/fixed port/wrong label/digest/PG version必须 Fatal。

### 14.3 rollout/恢复

- unchanged manifest 下 current/next build滚动允许；
- changed manifest 下 rolling被拒，只允许fleet quiesce；
- missed slot按 catch-up policy至多一轮；
- 回滚到旧 build前确认其 capability支持当前 manifest；
- River migration ahead/behind、job contract downgrade、unknown future job均STOP。

## 15. 分片

| 片 | 交付 | 门禁 |
|---|---|---|
| R210-0 | 本设计/实施计划 | docs only |
| R210-1 | JobManifestV1、代码registry、effective hash、六任务契约测试 | 人工批准；无迁移 |
| R210-2 | cluster/replica/fleet identity、River client ID、两副本/leader failover harness | fleet keyring/config另批；仅disposable PG |
| R210-3 | manifest v2 + ownership probe、ops metric/alert、只读 jobs UI | metric/API/DBR/告警契约另批 |
| R210-4 | staging 双副本部署、故障注入、soak/rollback | staging单独批准；production仍NO-GO |

## 16. 回滚

- R210-1 纯契约可恢复旧 image；不删 River job/leader记录；
- R210-2 wiring异常时恢复旧 image/config并缩回一个 worker副本；明确标记 R2-10 未满足；
- R210-3 probe异常可关闭 probe/告警展示，不能关闭 River leader/unique；
- manifest 变更只能回滚到 capability matrix声明兼容的旧 build；
- 不通过清空 `river_leader`、删 JobRow 或改 DB 时钟“修复”所有权；
- production任何回滚另走变更单。

## 17. 审批请求

1. 是否确认 River v0.45 DB leadership 为 platform-worker 唯一 scheduler lease；
2. 是否确认一个 database/schema 只属于一个 environment/worker cluster；
3. JobManifestV1 六任务口径、at-least-once 与 side-effect evidence；
4. signed JobFleetManifest、唯一 database binding inventory、build capability与manifest变化时fleet quiesce；
5. probe只读安全列、stable codes与新 ops metric/alert；
6. R210-1/2/3/4 分别审批，R2-10仅在R210-4通过后满足；
7. 任何外部写、非River ticker或自动抢主都不在本设计授权内。
