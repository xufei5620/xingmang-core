# XM-CPA0 · CPA 平台真实数据第一片

## status

READY（详情见下方 not_run / risks——真实 usage.sqlite 与生产部署未接触，
留给验收线在服务器上核对，§8 契约里列了逐项待核对清单）

## branch / commit / base

- branch: `ai/claude/XM-CPA0`
- base: `ef0a85f`（`release/v0.1-launch` 当前 tip）
- implementation commit: `0b5a356`
- worktree: `K:/星芒统一控制平台/acceptance/wt-cpa0`

## scope

把侧栏"平台 → CPA"从占位平台做成有真实数据的平台页（第一片）。CPA = 服务器
上的 CLI Proxy API（容器 `cli-proxy-api`）+ 它的管理侧车 `cpa-manager-plus`
（数据落在宿主机 `/root/cpa-stack/cpam-data/usage.sqlite`）。

六块交付：

1. **`connectors/cpa/`（新增连接器）**：只读文件后端 `NewFileClient`，纯 Go
   SQLite 驱动（`github.com/ncruces/go-sqlite3`，已在 go.mod 里但之前只是
   indirect 依赖，本片提升为直接依赖，无 CGO），只读打开 usage.sqlite
   （`mode=ro` + `busy_timeout`），三个读方法：
   - `UsageSummary(day)`：按 provider/model 聚合请求数/token/成本，成本经
     `model_prices` 折算，**定点**（micro-USD，money.MicroScale=6），全程
     不经 float64 计算（只在 SQL→Go 边界读一次 REAL 后立刻转十进制文本再
     解析成整数）；未配价的模型/请求诚实标记为"未知"，不计 0；
   - `KeyUsage(day)`：按 `api_key_hash`（join `api_key_aliases`）聚合，
     `GET /api/v1/platforms/cpa/keys` 与 `cpa.keys.usage` 观测的数据源；
   - `AccountHealth()`：读 `codex_inspection_runs/results` 最近一轮结果。
   - `usage_events` 的 token 列名未经真实库核对，用 `PRAGMA table_info` +
     候选名单在运行时解析；解析不到的角色按 0 计、用量结果显式标 partial，
     且 `Health()` 返回 unhealthy 并报告缺口（不崩，也不冒充完整结果；
     需要真实部署核对，见契约 §8）。
2. **`internal/platform/jobs/cpa_sync.go`（新增周期任务）**：`cpa_sync`，
   默认 5 分钟，`XM_CPA_MODE=off|file`（没有 fake——SQLite 文件没有值得
   伪造的响应）。三个读方法相互独立，一个失败不连累另外两个（与
   sub2api_sync/cost_sync"一次上游调用、全部指标一起失败"的形状不同，是
   本片对既有四件套模式的必要改造）；失败观测保留上一次成功值
   （同既有纪律）。装配进 `internal/platform/jobs/client.go`
   （Config 字段 + `NewClient` 注册块）、`job_manifest.go`（第 7 个周期任务，
   `contracts/jobs/cluster-jobs.v1.json` 同步更新）、
   `cmd/platform-worker/config.go`（env 解析）。
3. **`internal/platform/ops`（指标白名单 + rollup 策略）**：四条新
   `metric_key`（`cpa.requests.daily`/`cpa.cost.daily`/`cpa.keys.usage`/
   `cpa.accounts.health`）加进 `freshness.go` 的 `registeredMetrics` 白名单
   与 `contracts/ops/metric-rollup-policy.v1.json` 的策略表（14→18 条）。
4. **`GET /api/v1/platforms/cpa/keys?day=`（新增 Query）**：`internal/platform/
   httpapi/cpa_keys.go`，scope `platform.users.read`（复用字符串，不经
   `platformusers.Service`——CPA 的"用户"是 API key，那个域明确不认
   `"cpa"`，理由见代码注释与契约 §4）。金额走 `moneyItem`（`amount_minor`+
   `currency`+`scale`，profit_daily.go 既有类型），不是 `amountBody`——
   CPA 成本是 scale-6 微单位，不是币种自身最小单位，复用后者会让前端
   显示的金额错 10000 倍且不报错（本片实现过程中发现并修正的一处真实
   bug，见下方 tests_run 的说明）。装配进 `router.go`（`Deps.CPAKeys`，
   nil 时不挂载）与 `cmd/platform-api`（新增 `cpa.go`，env 解析 + 构造）。
5. **前端（`web/apps/admin-web`）**：
   - `api/cpa.ts`：`listCPAKeys` + cpa.* 观测 value 形状的只读类型；
   - `components/CPAOverviewPanel.tsx`：概览三卡（请求量/成本折算/账号
     健康）+ 未配价模型提示条（只在真有未配价用量时出现）；
   - `components/CPAKeysPanel.tsx`：用户管理页签，DataTableV2 逐 key
     用量；
   - `components/CPAAssurancePanel.tsx`：渠道保障页签的 `overview` 子页签
     接账号巡检真实数据（三个 StatTile + 异常账号表，≤50 条 + "还有 N 条"）；
     `probes`/`history` 两个子页签 CPA 没有对应数据形状，落回既有共享蓝图
     （不新增假数据）；
   - `pages/PlatformDetailPage.tsx`：在 overview/users/model(overview 子页签)
     三处判 `serviceType==="cpa"` 接上真实组件；upstream/finance 两页保持
     "未接入"但写明具体缺什么（原型的 CPA 蓝图态改动前是通用占位文案，
     现在按契约 §8/§9 说清缺口）；navigation.ts **未改**（CPA 已在目录里）。
6. **compose**：`launch.yaml`/`server-prod.yaml` 透传 `XM_CPA_MODE`/
   `XM_CPA_DATA_DIR`/`XM_CPA_FILE_NAME`/`XM_CPA_SYNC_ENABLED`/
   `XM_CPA_SYNC_INTERVAL`/`XM_CPA_INSTANCE_ID`；`server-prod.yaml` 给
   `platform-worker`/`platform-api` 各加一条独立的
   `${XM_CPA_HOST_DATA_DIR:-/root/cpa-stack/cpam-data}:/var/lib/xm/cpa:ro`
   绑定挂载（挂**整个目录**不是单文件——WAL 读者需要 `-wal`/`-shm` 兄弟
   文件，理由见契约 §5；绝不挂载 `/root/cpa-stack/cpa/auths` 或
   `config.yaml`）。`XM_CPA_FILE_NAME` 只接受该目录内的安全 basename，拒绝
   绝对路径、目录穿越与 SQLite URI query 注入。两份 compose 文件均过 `docker compose config`（用占位
   `.env` 校验，见 tests_run）。

`contracts/connectors/cpa.read.v1.md`（新增，DRAFT）：完整契约文档，§8 列出
5 项未经真实库核对的假设（token 列名、业务日时区、`codex_inspection_runs`
时间戳列、货币假设、`disabled` 列类型），§9 说明为什么账号巡检数据落在
"渠道保障"页签位置而不是新开页签。

## files_changed

新增：

- `connectors/cpa/`：`contract.go`、`contract_test.go`、`cost.go`、
  `cost_test.go`、`schema.go`、`file_client.go`、`file_client_test.go`、
  `observations.go`、`observations_test.go`
- `internal/platform/jobs/cpa_sync.go`、`cpa_sync_test.go`
- `internal/platform/httpapi/cpa_keys.go`、`cpa_keys_test.go`
- `cmd/platform-api/cpa.go`、`cpa_test.go`
- `contracts/connectors/cpa.read.v1.md`
- `web/apps/admin-web/src/api/cpa.ts`
- `web/apps/admin-web/src/components/CPAOverviewPanel.tsx`
- `web/apps/admin-web/src/components/CPAKeysPanel.tsx`
- `web/apps/admin-web/src/components/CPAAssurancePanel.tsx`
- `docs/handoffs/slices/XM-CPA0.md`（本文件）

修改：

- `internal/platform/ops/freshness.go`（registeredMetrics 加 4 条）
- `internal/platform/ops/metrickeys_test.go`（契约一致性测试加 4 条）
- `internal/platform/ops/rollup_policy_test.go`（14→18 条计数 + 4 条形状断言）
- `contracts/ops/metric-rollup-policy.v1.json`（加 4 条 policy）
- `internal/platform/jobs/client.go`（Config 字段 + NewClient 注册块 + validate）
- `internal/platform/jobs/job_manifest.go`（第 7 个周期任务）
- `internal/platform/jobs/job_manifest_test.go`（6→7 处更新）
- `internal/platform/jobs/effective_manifest.go`（effectiveJobConfig + productionRunID）
- `contracts/jobs/cluster-jobs.v1.json`（cpa_sync 条目）
- `cmd/platform-worker/config.go`（XM_CPA_* env 解析）
- `cmd/platform-worker/config_test.go`（新增用例）
- `internal/platform/httpapi/router.go`（Deps.CPAKeys + 路由挂载）
- `cmd/platform-api/config.go`（cpaConfig 字段）
- `cmd/platform-api/main.go`（构造 + 装配进 Deps）
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`（三处 cpa 特判 + upstream/finance 说明文案）
- `web/apps/admin-web/src/router.test.tsx`（两个既有用例断言的是旧占位文案，随本片行为变化改为断言真实内容）
- `deploy/compose/launch.yaml`（platform-api/platform-worker 环境变量）
- `deploy/compose/server-prod.yaml`（环境变量 + 两个服务各一条只读挂载）
- `go.mod`（`github.com/ncruces/go-sqlite3` 从 indirect 提升为 direct，`go mod tidy`
  产出，版本未变 v0.32.0；go.sum 无变化）
- `VERSIONS.lock`（补一行 `go-sqlite3 = v0.32.0`）

## tests_run

```
go build ./...                                          PASS
go vet ./...                                             PASS
gofmt -l <全部改动/新增 .go 文件>                          干净（无输出）
go test ./...                                             全部 PASS（无 FAIL）
  connectors/cpa                                          24 项测试全过，含：
    - 定点成本折算（含 half-up 舍入、大数溢出保护）
    - 合成 sqlite：UsageSummary/KeyUsage/AccountHealth 全路径
      （多 provider/model、跨日过滤、未配价模型、多 run 只读最新一轮）
    - token 列回退（候选名单第二选项匹配 + 全不匹配时 partial/unhealthy 降级）
    - FileName 绝对路径、目录穿越与 SQLite URI query 注入拒绝
    - Windows file: URI 路径（含中文路径分隔符）验证通过
  internal/platform/ops                                   含新增 4 条指标的白名单/policy 一致性测试
  internal/platform/jobs                                  7 个 CPASync 专项测试
      （成功写 4 条/单项失败不连累其它/失败保留上次成功值/
        工厂失败四条一起失败/写失败触发重试/取消不算失败/
        Config.validate 五个不变量）+ 既有 job manifest/effective
        manifest 测试更新后全过
  internal/platform/httpapi                                9 个 cpa_keys 专项测试
      （成功/day 默认今天/非法 day 400/scope 403/dep=nil 404/
        三种 connector 错误分类映射状态码/未初始化快照拒绝/
        响应体不含裸 api_key 字段）
  cmd/platform-worker                                       7 个 CPA 相关新增用例
  cmd/platform-api                                          7 个 CPA 相关新增用例

docker compose -f deploy/compose/launch.yaml config                              PASS（占位 .env）
docker compose -f deploy/compose/launch.yaml -f deploy/compose/server-prod.yaml config  PASS（占位 .env，含全部既有 :? 必填变量）
  确认 XM_CPA_* 变量渲染正确、两个服务各自的
  /root/cpa-stack/cpam-data:/var/lib/xm/cpa:ro 挂载均出现在渲染结果里

pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck    PASS
pnpm --config.verify-deps-before-run=false --filter admin-web run test         PASS（79 files / 1134 tests）
pnpm --config.verify-deps-before-run=false --filter admin-web run build        PASS（tsc + vite build，291 modules）
pnpm --config.verify-deps-before-run=false -r run typecheck                     PASS（全部 5 个工作区包，安全网）
bash scripts/check-governance.sh                                                 PASS（exit 0）
```

⚠️ **Windows 已知坑（本次实测复现）**：裸 `pnpm --filter admin-web run
typecheck`（不带 `--config.verify-deps-before-run=false`）在本 worktree
（`node_modules` 是指回主检出的符号链接）会触发 pnpm 的依赖校验 → 自动
install → Windows 目录 rename 到非空目录 EPERM → 无限重试，表现为"卡死"
而不是报错（见 memory `windows-toolchain-quirks.md`）。首次尝试确实卡了
~10 分钟无输出，加上该 flag 后秒级完成。所有前端门禁命令都必须带这个 flag，
验收线在其它机器上复核时也一样。

实现过程中发现并当场修正的两处问题：

1. **金额标度 bug**：`cpa_keys.go` 最初把 CPA 成本折算（scale-6 微单位）
   套进了 `amountBody`（`users.go` 既有类型，隐含"已经是币种自身最小
   单位"），会让前端把成本显示放大 10000 倍且不报错。改用 `profit_daily.go`
   已有的 `moneyItem`（显式带 `scale` 字段）修正，测试同步更新为断言
   `scale=6`。
2. **两个既有前端测试断言的是旧占位文案**：`router.test.tsx` 里
   "CPA 没有用户清单" 与 "未实现的页签给诚实占位" 两个用例断言
   `/platforms/cpa?tab=users` 与 `?tab=finance` 显示通用占位文本——这两处
   正是本片改变的行为（users 接了真实 key 用量、finance 换成写明缺口的
   说明），已按新行为改写断言，不是绕过失败。

## not_run

- **未连服务器 / 未读真实 usage.sqlite**：按任务边界，部署与真实数据验证
  留给验收线。所有 SQLite 测试用合成库（代码里现建表现插样本行），不碰
  任何真实数据或凭据。
- **未跑 `security-review` / `/code-review` skill**：时间盒内未执行，
  建议验收线跑一遍（尤其复核只读挂载路径拼接、`api_key_hash` 是否有任何
  路径可能被拼进日志或响应之外的地方）。
- **未验证 cpa-manager-plus 是否真的按 WAL 模式写库**：契约假设它是（据此
  设计了整目录挂载），但没有拿到真实文件核对 journal_mode。
- **`RunAt`（AccountHealthSummary）恒为 nil**：`codex_inspection_runs` 的
  时间戳列名未核对，见契约 §8 第 3 项。

## risks

1. **usage_events 的 token 列名是猜的**（契约 §8 第 1 项）。解析失败时
   优雅降级（角色按 0 计、结果显式 partial、Health unhealthy），不会让端点
   500，也不会把偏低值冒充完整结果；但**成本与用量数字在真实部署前仍可能
   系统性偏低**——验收线第一步应该核对
   `PRAGMA table_info(usage_events)` 的真实列名，对不上就去改
   `connectors/cpa/schema.go` 的候选名单。
2. **业务日时区假设 UTC**（契约 §8 第 2 项）。如果 cli-proxy-api 实际按
   本地时区（如 UTC+8）打 `timestamp_ms`，当前"日"边界会切错——影响
   `UsageSummary`/`KeyUsage` 的当日归属，不影响 `AccountHealth`（无日期
   概念）。
3. **`Currency="USD"` 硬编码假设**（契约 §8 第 4 项），`model_prices` 没有
   货币列。若未来接入非 USD 计价的 provider，成本会被错误地当成 USD 折算。
4. **`RunAt` 恒为 nil**，前端"最近一轮巡检"目前无法显示具体时刻，只能显示
   `run_id`——用户体验上是一个已知缺口，不是 bug。
5. **`cpa.keys.usage` 观测只有 20 条样本**，`GET /platforms/cpa/keys` 才是
   完整数据源；如果前端将来有人想从观测里拼出"全部 key 列表"会拿到不完整
   结果——已在契约 §3.2 和代码注释里写清楚，但值得在这里再提一次。
6. **`contracts/ops/metric-rollup-policy.v1.json` 新增的 4 条 `"metric_key":
   "cpa.xxx.yyy"` 字面量，形状与曾经触发 gitleaks generic-api-key 误报的
   `sub2api.revenue.daily`/`sub2api.cost.daily` 相同**（长度/熵都在同一
   量级）。本次未跑 gitleaks 本地复核（过渡期该检查是 GitHub Actions 项，
   不在 `scripts/check-governance.sh` 里，已跑并 PASS）。Go 侧全部改用
   常量引用（`MetricKey: MetricRequestsDaily`），不是字面量赋值，按既有
   经验这一半不会触发；JSON 数据文件里的 14 条既有同形状条目已经合入
   主线，本次只是照抄同一模式再加 4 条。如果验收线跑 gitleaks 真的报了，
   照旧解法：不加 allowlist，确认是这类误报后 squash 提交。
7. **worktree 未跑 `pnpm install`**（按团队指示，`node_modules` 是符号
   链接到主仓库），如果 `ncruces/go-sqlite3` 的子包（`driver`/`embed`）在
   `go.sum` 里的校验和与实际下载不一致，`go build` 会失败——本次本地构建
   全程未见此问题，但生产 CI/服务器构建环境的模块缓存可能不同，建议验收
   线第一次构建时留意。

## follow_ups

1. 按契约 §8 逐项核对真实 usage.sqlite 后，把结论写回契约文档并按需调整
   `connectors/cpa/schema.go` 的候选列名 / `dayBoundsUTC` 的时区假设 /
   `Currency` 常量。
2. 若 `codex_inspection_runs` 确有时间戳列，接上并去掉 `RunAt` 恒 nil 的
   限制。
3. 渠道管理（`upstream` 页签）与支付财务（`finance` 页签）仍是"未接入"——
   需要 cpa-manager-plus 提供渠道↔模型映射或支付/充值数据源才能进一步做，
   当前不在本片范围内。
4. 目前 CPA 没有"day 选择器"（概览/用户管理默认今天，UTC）；如果运营需要
   回看历史某一天的用量，需要加一个日期选择控件，接口已经支持
   （`day` query param），只是前端未暴露。
5. 建议后续一片跑一次 `/security-review`，重点看只读路径拼接与
   `api_key_hash` 的展示边界。
