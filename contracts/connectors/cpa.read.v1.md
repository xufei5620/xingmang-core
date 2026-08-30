# CPA（CLI Proxy API + cpa-manager-plus）只读接入契约 v1 — **DRAFT**

| 项 | 值 |
|---|---|
| 状态 | **DRAFT（XM-CPA0）**。表结构由任务简报核对确认，但 usage_events 的部分 token 列名、codex_inspection_runs 的时间戳列、业务日时区三项**未对真实生产库核对**，见 §8 |
| Connector Key | `cpa` |
| Contract Version | `1` |
| 实现 | `connectors/cpa`：只读文件后端 `NewFileClient`，直读 cpa-manager-plus 落盘的 `usage.sqlite`（`modernc`/`ncruces` 系纯 Go SQLite 驱动，无 CGO），不经 HTTP。**没有 real（HTTP 管理 API）后端**——见 §1 |
| 合规判据 | `go test ./connectors/cpa/...`（合成 sqlite 库，覆盖用量/成本折算/账号巡检/token 列回退/schema 缺失场景） |
| 平台侧入口 | 周期同步：`internal/platform/jobs.CPASyncWorker`（写 `internal/platform/ops` 四条观测）；HTTP 层：`internal/platform/httpapi/cpa_keys.go`（仅 `GET /platforms/cpa/keys`，其余页面读 `/api/v1/metrics`） |
| 依据 | 团队交接口述简报（XM-CPA0，2026-08-31）；`docs/architecture/ADMIN-IA.md` CPA 5 页签定义 |

⚠️ **DRAFT 的含义**：usage.sqlite 的核心表（`usage_events`/`model_prices`/
`api_key_aliases`/`codex_inspection_runs`/`codex_inspection_results`）与本契约
实际读取的列已由任务简报核对；但部分列名与两个业务假设（时区、货币）**未对
生产库或代码核对**，逐项列在 §8。冻结前（真实部署验收）不得把这些假设当作
已核实的事实写进跨线接口。

---

## 1. 数据源事实（不是平台建的）

CPA 在本仓库语境里指两个容器：

- **`cli-proxy-api`**（镜像 `eceasy/cli-proxy-api`）：实际处理请求的代理进程；
- **`cpa-manager-plus`**：管理与用量统计侧车，监听 `127.0.0.1:18317`，把
  用量、定价与账号巡检结果落进 SQLite（`/root/cpa-stack/cpam-data/usage.sqlite`）。

平台**不参与 cli-proxy-api 的实时请求路径**（宪法 5 条），只在 cpa-manager-plus
落盘的库上做**周期只读快照**。cpa-manager-plus 是否暴露一个值得读的 HTTP
管理 API 未经验证——本契约的唯一数据路径是直读它自己的 SQLite 文件。

**绝不读取、绝不挂载**：`/root/cpa-stack/cpa/auths`（上游 OAuth 令牌）与任何
`config.yaml`（含 API key 等凭据）。平台容器只读绑定挂载
`/root/cpa-stack/cpam-data`（整个目录，理由见 §5），不触碰凭据目录——这是
本契约与部署配置共同承担的红线，代码层面 `connectors/cpa` 也没有任何路径
能读到这两个位置之外的文件。

usage.sqlite 已核对的表（任务简报确认，列名逐字）：

- `usage_events(id, request_id, event_hash, timestamp_ms, timestamp, provider, executor_type, model, endpoint, method, path, auth_type, auth_index, source, source_hash, api_key_hash, …)` ——
  token 计数列名**未核对**，见 §8；
- `model_prices(model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m, …)` —— 均为 `REAL`（USD/1M tokens）；
- `api_key_aliases(api_key_hash, alias)`；
- `codex_inspection_runs(run_id, …)` / `codex_inspection_results(run_id, account_key, file_name, display_account, auth_index, account_id, provider, disabled, status, state, action, action_reason, …)`；
- `settings`, `usage_rollup_checkpoints`（本契约不读）。

## 2. 能力清单

```
cpa.service.version_read      文件后端自身格式版本（不是 CPA 版本，见 §6）
cpa.health.read                usage.sqlite 可读性 + schema 探测
cpa.usage.read                 UsageSummary(day)：按 provider/model 聚合
cpa.key_usage.read             KeyUsage(day)：按 api_key_hash 聚合
cpa.account_health.read        AccountHealth()：最近一轮账号巡检
```

五项全部由 `FileClient` 兑现（与 reqlog 文件后端不同，这里没有"磁盘格式
不采集这个维度"需要摘除的能力）。

## 3. 数据形状

### 3.1 `UsageSummary` — 当日按 provider/model 的用量与折算成本

```go
type ProviderModelUsage struct {
    Provider, Model                     string
    RequestCount                        int64
    TokensIn, TokensOut                 int64
    TokensCacheRead, TokensCacheCreation int64
    CostMicros *int64 // 微 USD（money.MicroScale=6）；nil = 未配价，不是 0
}
type UsageSummary struct {
    Snapshot
    BusinessDay          string // UTC 日历日 YYYY-MM-DD，见 §8 时区假设
    Rows                 []ProviderModelUsage
    TotalRequestCount    int64
    TotalCostMicros      *int64   // 已配价行之和；全部未配价则为 nil
    Currency              string  // TotalCostMicros != nil 时为 "USD"（见 §8）
    UnpricedRequestCount int64
    UnpricedModels       []string // "provider/model"，排序去重
}
```

成本折算规则（`connectors/cpa/cost.go`）：**逐行全有或全无**。一行只要用到的
某个 token 档位（prompt/completion/cache_read/cache_creation）在 `model_prices`
里没有可用价格，整行判未配价，绝不只算"部分层级"冒充完整成本
（宪法 12 条）。`cache_per_1m` 是 `cache_read_per_1m`/`cache_creation_per_1m`
缺席时的通用兜底，两者都在时优先用分档价格。价格转换路径：SQLite `REAL` →
`strconv.FormatFloat('f',-1,64)`（最短可回读十进制文本）→
`money.ParseMinorUnits(text, 6)`——全程不做浮点乘除（宪法 13 条）。

### 3.2 `KeyUsagePage` — 当日按 api_key_hash 的用量与折算成本

```go
type KeyUsageRow struct {
    APIKeyHash string // 单向哈希，从不还原（宪法 7 条）
    Alias      string // "" = api_key_aliases 里没有这个哈希，正常状态
    RequestCount, TokensIn, TokensOut               int64
    TokensCacheRead, TokensCacheCreation             int64
    CostMicros           *int64 // 语义同 UsageSummary，但按 (key, model) 折算后按 key 汇总
    UnpricedRequestCount int64
    LastUsedAt *time.Time // 该 key 在**这一天**里的最晚请求时刻，不是全量最后使用
}
type KeyUsagePage struct {
    Snapshot
    BusinessDay   string
    Rows          []KeyUsageRow // 按 RequestCount 降序
    TotalKeyCount int64
    Truncated     bool
}
```

定价在 `(api_key_hash, model)` 粒度算好后再按 key 汇总——不能先把一把 key
名下不同模型的 token 混在一起再折算，那样会用错单价（与 §3.1 同一条纪律，
只是分组键多了一维）。

`GET /api/v1/platforms/cpa/keys?day=` 直接暴露这个形状（`day` 缺省 = 服务端
UTC 今天）；`cpa.keys.usage` 观测只携带 `key_count` + 前 20 名的小样本，
完整明细走这个端点现读，不经观测（见 §7）。

### 3.3 `AccountHealthSummary` — 最近一轮账号巡检

```go
type AccountAnomaly struct {
    AccountKey, DisplayAccount, Provider string
    Disabled                             bool
    Status, State, Action, ActionReason  string
}
type AccountHealthSummary struct {
    Snapshot
    RunID         string     // "" = 从未跑完一轮
    RunAt         *time.Time // 恒为 nil，见 §8（无已核实时间戳列）
    AccountCount, DisabledCount int64
    Anomalies     []AccountAnomaly // 已按 MaxAnomalies=50 截断
    AnomalyCount  int64            // 截断前的真实条数
    Truncated     bool
}
```

"最近一轮"取 `codex_inspection_runs` 里 SQLite `rowid` 最大的一行——插入序的
代理指标，见 §8。"异常"定义为 `disabled=true` 或 `action` 非空且不等于
`"none"`（大小写不敏感）；列表截断上限 50 条，前端必须显示"还有 N 条"
（XM-0051 的教训：不设上限的列表能把整页撑到几千像素）。

### 3.4 `Snapshot` — 每个结果都带的新鲜度信封（规格 §9.1）

```go
type Snapshot struct {
    ObservedAt time.Time // 本次读取发生的时刻（UTC）——usage.sqlite 没有自己的水位
    Watermark  string    // ObservedAt 的 RFC3339 文本
    IsPartial  bool      // 有未配价行 / 巡检列表被截断时为 true
    Instance   string    // 恒为 FileInstance = "cpa-file"
}
```

## 4. 权限与审计

- `GET /api/v1/platforms/cpa/keys` 要求 `platform.users.read`
  ——**复用**该字符串（不是导入 `platformusers.ScopeRead` 常量）：CPA 的
  "用户管理"页签给的是 API key 用量，不是终端用户身份，`platformusers.
  ParseSource` 明确不认 `"cpa"`（该域的"用户"专指 Sub2API/NewAPI 的终端
  客户），这条端点因此完全不经过 `platformusers.Service`。复用同一个权限
  等级是有意的授权选择：已经被授权看别的平台终端用户资金明细的角色，看
  CPA 的 key 级用量泄漏面不会更大（同 `alerts.ScopeRead` 复用 `ops.ScopeRead`
  的理由）。
- 概览 / 渠道保障两页读 `GET /api/v1/metrics`（`ops.read`），与其它平台的
  聚合指标同一条权限、同一个端点。
- 审计：本连接器只读，不产生写路径，因此不接入 Action 审计链；`ops.read`/
  `platform.users.read` 的调用仍走 httpapi 通用访问日志（`RequestID`/
  `AccessLog` 中间件），与其它只读端点一致。

## 5. 只读边界（部署纪律，非代码可强制）

- **挂载整个 `cpam-data` 目录，不是单个 `.sqlite` 文件**：SQLite WAL 模式下
  只读连接需要能读到同目录的 `-wal`/`-shm` 兄弟文件，只挂主文件会在
  checkpoint 前后读到不一致的快照（`connectors/cpa/file_client.go` 的
  `FileConfig.DataDir` 文档）。
- **只读打开**：DSN 固定 `file:<path>?mode=ro&_pragma=busy_timeout(5000)`
  （`connectors/cpa` 的 `busyTimeoutMillis`）——`mode=ro` 由 SQLite 自身强制
  只读，不依赖容器文件系统权限这一层防线；`busy_timeout` 让读者在极少数
  与 cpa-manager-plus 的 checkpoint 撞车时重试而不是直接报错。
- **绝不挂载凭据目录**：见 §1。容器编排层（`deploy/compose/server-prod.yaml`）
  与代码（`connectors/cpa` 从不拼接超出 `DataDir` 的路径）两层都不触碰
  `/root/cpa-stack/cpa/auths` 或任何 `config.yaml`。
- **`api_key_hash` 单向**：本契约任何路径都不尝试还原成真实 key，也不把它
  与能还原的文件（auths/config.yaml）联表。

## 6. 错误映射

`connector.ErrorKind` 与其它连接器共用同一枚举（ADR-004）：

| 场景 | Kind |
|---|---|
| usage.sqlite 打不开 / busy_timeout 用尽仍占用 | `unavailable` |
| `day` 参数格式非法（非 `YYYY-MM-DD`） | `bad_response` |
| 表结构读不出（`PRAGMA table_info` 失败） | `bad_response` |
| 价格文本解析失败（理论上不可达，money 包内部错误） | `bad_response` |

`Version()` 报告的是**文件后端自己的格式版本**（`"file/1"`），不是 CPA /
cli-proxy-api 的版本——usage.sqlite 没有已知的 schema 版本标记可探测
（与 `connectors/reqlog` 的 `fileBackendVersion` 同一条理由）。

## 7. 配置

### `cmd/platform-worker`（周期同步 `cpa_sync`）

| 变量 | 默认 | 说明 |
|---|---|---|
| `XM_CPA_MODE` | `off` | `off` \| `file`。**没有 fake**：SQLite 文件没有值得伪造的响应形状 |
| `XM_CPA_DATA_DIR` | 空 | 容器内只读挂载目录，`file` 模式必填 |
| `XM_CPA_FILE_NAME` | 空（回落 `usage.sqlite`） | 覆盖库文件名，一般不需要 |
| `XM_CPA_SYNC_ENABLED` | 跟随 `XM_CPA_MODE` | 留空 = file 打开/off 关闭；显式 `false` 可在 file 模式下暂停（宪法 26 条），**不能**在 off 模式下打开（矛盾配置启动即拒） |
| `XM_CPA_SYNC_INTERVAL` | `300s` | 同步周期 |
| `XM_CPA_INSTANCE_ID` | `cpa-cli-proxy-api` | 落进每条观测的 `Source` |

### `cmd/platform-api`（`GET /platforms/cpa/keys`）

| 变量 | 默认 | 说明 |
|---|---|---|
| `XM_CPA_MODE` | `off` | `off` \| `file`，同上；`off` 时端点不挂载（404，非 500） |
| `XM_CPA_DATA_DIR` / `XM_CPA_FILE_NAME` | 同上 | 独立的只读文件句柄，与 worker 各开各的 |

### 观测（`internal/platform/ops`，`XM_CPA_SYNC_ENABLED=true` 时每轮写入）

| metric_key | 形状要点 |
|---|---|
| `cpa.requests.daily` | `total_request_count`、`by_provider`、`by_model` |
| `cpa.cost.daily` | `total_cost_minor_units`（scale 6，money.MicroScale）+ `currency`，或 `total_omitted_reason`；`unpriced_request_count`、`unpriced_models` |
| `cpa.keys.usage` | `key_count` + 前 20 名 `top_keys` 小样本；**不是**完整明细（那是 §3.2 的端点） |
| `cpa.accounts.health` | `run_id`、`account_count`、`disabled_count`、`anomalies`（≤50）、`truncated` |

四条键的新鲜度阈值均为 1800 秒（对齐 5 分钟采集周期，两轮失败才判 stale）。

## 8. 等真实部署核对的字段清单

与 reqlog DRAFT 契约同一套纪律：这是本契约与真实运行环境之间**唯一的**
差异台账，接入真实生产库后逐项核对、写回结论。

1. **`usage_events` 的 token 计数列名**——任务简报只给了 join/定价相关列，
   四个 token 角色（input/output/cache_read/cache_creation）的具体列名
   没有核对。`connectors/cpa/schema.go` 用 `PRAGMA table_info` 在候选名单
   （`input_tokens`/`prompt_tokens`… 等）里挑第一个匹配的，全不匹配时该
   角色按 0 计且 `Health()` 报告缺哪个角色——不会崩，但成本/用量可能系统性
   偏低。**核对动作**：对生产库跑一次 `PRAGMA table_info(usage_events)`，
   把真实列名写回 `candidateInputTokenColumns` 等常量的候选名单首位。
2. **业务日时区**——`dayBoundsUTC` 假设 `timestamp_ms` 是 UTC 纪元毫秒，
   业务日按 UTC 日历日切分。未核对 cli-proxy-api 是否按本地时区打时间戳。
   若实际是本地时间（如 UTC+8），当前实现会把"日"切错 8 小时。
3. **`codex_inspection_runs` 的时间戳列**——"最近一轮"目前靠 SQLite `rowid`
   排序代替真实时间戳（该表除 `run_id` 外的列名未核对）。若表其实有
   `started_at`/`created_at` 之类的列，应该改用它，并把 `AccountHealthSummary.
   RunAt` 从恒为 `nil` 改成真实值。
4. **`Currency = "USD"` 假设**——`model_prices` 没有货币列；假设所有价格是
   USD，未核对是否存在非 USD 计价的 provider。
5. **`disabled` 列的真实类型**——按 SQLite `INTEGER`（0/1）读入
   `sql.NullBool`；未核对是否可能是其它编码（如字符串 `"true"`）。

## 9. 为什么账号巡检落在"渠道保障"页签位置

ADMIN-IA 把 CPA 第 4 格定为"渠道保障"（M1.5 徽标），与 Sub2API/NewAPI 未来
要做的"模型路由验证"（渠道↔模型映射的探测，见 `PlatformAssurancePanel.tsx`
的 UI 蓝图）是同一个页签*位置*、完全不同的*语义*——CPA 目前唯一可用的账号
级健康信号是 `codex_inspection_runs/results`（这些账号是否被禁用、巡检给出
的处置建议），不是模型路由是否可用。团队口述简报明确要求把这份数据接进
这一格（而不是新开一个页签），前端因此在 `PlatformAssurancePanel` 之外新增
`CPAAssurancePanel.tsx`，只接管 `model` 页签的 `overview` 子页签；`probes`/
`history` 两个子页签 CPA 没有对应的数据形状（不存在"检测任务"或按模型分的
"保障历史"概念），继续落回共享的蓝图占位，不硬凑假数据冒充。

## 10. 破坏性变更

DRAFT 状态下 v1 的内容仍可调整（§8 任何一项核对结论若与假设不符，直接改
本文件与 `connectors/cpa` 的实现，不发 v2）；形状一旦经真实部署验收冻结，
后续破坏性变更必须发新契约版本。
