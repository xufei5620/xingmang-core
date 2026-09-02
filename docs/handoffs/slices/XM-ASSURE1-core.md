# XM-ASSURE1-core：渠道主动探测（检测任务）后端

## status

READY（待验收线审读、复跑并人工合入）。全部本地门禁绿；未连接真实生产
凭据/上游验证过真实模式的实际探测（这条留给 XM-ASSURE1-real，见交接
文档的"上线路径"）。

## branch / commit

- branch: `ai/claude/XM-ASSURE1-core`
- worktree: `K:/星芒统一控制平台/wt-xmASSURE1C`
- base: `release/v0.1-launch` @ `04a1ea9`（含 XM-ASSURE1 设计片合入与
  ADR-019 拍板记录）
- 七个提交（`git log --oneline release/v0.1-launch..HEAD`，按时间顺序）：
  1. `1a6608f` feat(db): add assurance schema for active channel probes
  2. `14cfdfe` feat(credentials): probe kill switch write/read path on connector_config
  3. `8299222` feat(assurance): probe declarations/runs/results core
  4. `f0c0b0f` feat(jobs): assurance_probe River worker
  5. `0919e39` feat(httpapi): mount detection-task and probe-history endpoints
  6. `d1638a3` feat(cmd): wire assurance probes into platform-api and platform-worker
  7. `30b104d` docs(assurance): data model reference and switch-flipping runbook
  本文件作为第八个提交单独加入（团队约定 Handoff 最后提交）。
- gitleaks 复核：`git clone --no-local --branch ai/claude/XM-ASSURE1-core
  --single-branch <worktree> /tmp/scan` → `gitleaks detect --source=.
  --log-opts="04a1ea9..HEAD" --verbose --redact=0` → 7 commits scanned,
  no leaks found。

## 与派工消息的偏离（先说清楚，再看细节）

我对照团队交接消息逐句核对了一遍实现，以下是全部发现的偏离，不是事后
挑几条显眼的：

1. **kill_switch Action 的字面 ID**：派工消息用简写
   `assurance.probe.switch@1`；我实现的是冻结设计稿
   （`docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md`
   §2.3）的四段式 `assurance.probe.kill_switch.set@1`。判断依据：设计稿是
   本任务被明确要求实现的"冻结设计"，派工消息的简写更像口语转述而非字面
   契约。已在 `internal/platform/assurance/actions.go` 顶部注释、契约文件
   `assurance.probe.kill_switch.set.v1.json`、以及一条专门的测试
   （`TestKillSwitchSetActionIDMatchesFrozenDesign`）里钉住这个选择，供
   验收线复核是否接受。
2. **targets / expected_shape 的编码方式**：设计稿把它们写成 JSON
   `array`/`object`，但 `internal/platform/action.Schema` 今天只有
   string/int/bool/string_slice 四种字段类型，没有对象数组/嵌套对象。
   两个参数因此编码成 JSON 字符串（`ParseTargets`/`ParseExpectedShape`
   解析），与 `connector.config.set@1` 把 `target_allowlist` 编码成
   逗号分隔字符串是同一类折衷。ADR-019"边界与后果"一节明确写着这份 JSON
   结构是设计稿、字段名/结构在实现前仍可能被验收线要求调整，因此判断这个
   折衷在这次实现的授权范围内，但仍然是一处对字面设计稿的偏离，值得
   验收线知道。
3. **迁移表结构相对设计稿 §1 的四处调整**（详见迁移文件头部注释与
   `docs/modules/assurance/DATA-MODEL.md`）：
   - 新增 `probe_run.client_run_key` 列——设计稿 §2.4 明确要求"同一
     declaration_id + client_run_key 已存在一条 pending/running 的 run
     直接返回该 run"，但 §1.3 的建表语句遗漏了这一列，没有它这条去重
     语义无法真正实现，因此补上；
   - `probe_credential_ref` 定为 `NOT NULL DEFAULT ''` 而非设计稿的可空
     `text`，与既有 `credential_ref` 列的"空串表示未登记"惯例一致；
   - `probe_declaration`/`probe_run`/`probe_result` 的 `environment`
     一律加了 `REFERENCES core.environment(id)`，设计稿原文没写这条外键，
     但与 `core.connector_config`/`core.credential_ref` 的既有纪律一致；
   - `created_at`/`updated_at` 不带 `DEFAULT now()`，由应用层注入的时钟
     显式写入——这是本仓库既有的可测试性约定（`credentials.Store` 等所有
     写入路径都是这样），不是漏写。
4. **"first-token if streaming" 没有实现流式请求**：`RealClient`
   （real 模式的 HTTP 客户端）用非流式 `chat/completions` 请求
   （`"stream": false`），因此 `measured_first_token` 在 real 模式恒为
   `false`、`first_token_ms` 恒为 `nil`——这是与设计稿 §1.4"首字节延迟
   只在真实测到时才填，测不到就是 null"完全一致的行为，但意味着 real
   模式今天**没有**首字节延迟这个维度的数据。fake 模式为了让整条链路
   （包括这个字段的展示态）可测，会给出一个明确标注为模拟值的固定数字。
   如果 XM-ASSURE1-real 或后续负责人认为首字节延迟对真实探测是必需的
   信号，需要在 real 模式补一版流式实现——这不在本片范围内，本片选择了
   更简单的非流式实现来控制范围，留 follow_up。
5. **"through the existing outbound client"**：`RealClient` 是本片新写的
   独立 HTTP 客户端（`internal/platform/assurance/realclient.go`），没有
   复用 `connectors/sub2api`/`connectors/newapi` 的既有连接器代码——那两个
   包的 `ReadClient` 接口是围绕它们自己的只读 admin 端点（`Version`/
   `Health`/`UserStats`/...）设计的固定方法集，不是一个"对任意路径 POST
   任意 JSON"的通用能力，硬套上去反而会破坏 ADR-018 闸 4"Connector 包内
   无写路径"的字面意义（探测本质上是一次新的、不同性质的出站请求，见
   ADR-019 决策·二）。复用的是 `internal/platform/connector` 包的
   `ErrorKind` 分类习惯与类型，没有复用具体的连接器实现代码。
6. **daily_budget 之外的运行参数没有做成环境变量**：团队交接消息只明确
   说"default daily budget 50 probes per platform (env-overridable)"，
   冷却间隔（默认 60s）因此保持 Go 常量+构造参数可覆盖
   （`assurance.WithLimits`），没有在 `cmd/platform-api`/
   `cmd/platform-worker` 里接一个环境变量——这是按团队交接消息的字面
   范围做的判断，如果负责人需要冷却间隔也能通过部署配置调整，是一处很
   小的后续工作。
7. **max_tokens 没有 Schema 级默认值 64**：交接消息说"default max_tokens
   64 (hard cap 512)"，但设计稿 §2.1 明确把 `max_tokens` 列为
   declare@1 的**必填**参数（1..512）。本片保留"必填、无默认"以贴合冻结
   设计稿，64 只作为文档/测试里推荐使用的典型值（`docs/modules/assurance/
   DATA-MODEL.md` 与测试固件里都用了 64），没有在 Handler 或 Schema 层
   插入一个"省略则回落 64"的分支。如果负责人希望 Action 参数本身可省略
   并回落 64，需要单独确认这个改动。
8. **迁移编号**：设计稿写"当前最新是 000024_staff_totp，本设计建议下一个
   空号"——开工时确认过 000024 之后确实空缺，本片用的是 `000025`，与建议
   一致，未偏离，写在这里只是确认过这一点。

## 与设计稿一致、值得强调的两个实现期发现（不是偏离，是写测试时抓到的
真实 bug，已修）

- **`RecheckAtExecution`（Job 执行时刻复检）如果直接复用 Action 接受时的
  预算计数查询，会把正在被复检的这条 run 自己也算进"今天已用的预算"**——
  预算恰好用满 1 的那次探测会在执行时刻把自己算作超额而错误拒绝。修法是
  给计数查询加一个"排除这条 run 自己"的参数（`countBudgetUsedTodayExcluding`），
  测试 `TestRecheckAtExecutionExcludesItselfFromBudget` 钉住这个行为
  （先验证排除自己后放行，再验证换一个不同的 run id 时预算确实被正确
  判定为耗尽，防止排除逻辑变成"预算检查形同虚设"）。
- **并发闸同理**：`RecheckAtExecution` 不能直接复用 `checkGates` 的完整
  逻辑，因为 `hasInProgressRun` 此刻恒会查到正在被复检的这条 run 自己
  （它就是 `pending` 状态）——这一点设计稿 §3.2 已经点出来了（"不能只靠
  River UniqueOpts"那段推理的姊妹问题），本片按设计稿的建议只在
  `RecheckAtExecution` 里做 Kill Switch/凭据/白名单/预算四类复检，不碰
  冷却/并发。

## summary

`internal/platform/assurance`（新包，Store + Service + Action Handler
三层，参照 `internal/platform/channelassurance` 的分层习惯）实现检测任务
声明、触发一次探测批次、Kill Switch、以及两个只读 Query。四个 L1 Action：
`assurance.probe.declare@1`、`.cancel@1`、`.run@1`、
`.kill_switch.set@1`。River Worker `assurance_probe`
（`internal/platform/jobs/assurance_probe.go`）按需（非周期）执行一次
批次：fake 模式零成本、确定性、全断言逻辑可测；real 模式对被管平台自己的
OpenAI 兼容 `chat/completions` 端点发一次非流式请求，本片未对任何真实
厂商执行过。

### 判定顺序 / 拒绝原因 / Kill Switch 语义

见 `docs/modules/assurance/DATA-MODEL.md`"判定顺序"一节——本文件不重复,
只强调一点：**fake 模式（或该平台尚无 connector_config 行）直接放行,
跳过 Kill Switch/预算/白名单/冷却/并发全部检查**,这是 ADR-019 明确要求
的行为,不是本片偷懒。

### 四个固定探测模板

`model_fingerprint` / `benchmark_set` / `context_length` /
`min_viable_request`，固化在 `internal/platform/assurance/templates.go`,
不接受任何自由文本 Prompt。`benchmark_set` 的"与上一次分数比较"需要读回
历史得分，而 `probe_result` 表没有单独的分数列——分数编码进 `verdict` 的
固定前缀,由 `ParseBenchmarkScore` 解析回来(细节见 DATA-MODEL.md)。

## files_changed

新增：

- `db/migrations/000025_assurance_probes.{up,down}.sql`
- `contracts/actions/assurance.probe.{declare,cancel,run,kill_switch.set}.v1.json`
- `internal/platform/assurance/`（`doc.go`、`types.go`、`templates.go`、
  `client.go`、`fakeclient.go`、`realclient.go`、`job_args.go`、
  `store.go`、`service.go`、`actions.go` + 对应 `*_test.go`）
- `internal/platform/credentials/connector_config_probe_test.go`
- `internal/platform/jobs/assurance_probe.go` + `assurance_probe_test.go`
- `internal/platform/httpapi/assurance_probes.go` + `assurance_probes_test.go`
- `cmd/platform-api/assurance.go`、`cmd/platform-worker/assurance.go`
- `docs/modules/assurance/DATA-MODEL.md`、`RUNBOOK.md`
- `docs/handoffs/slices/XM-ASSURE1-core.md`（本文件）

修改：

- `internal/platform/credentials/connector_config.go`（`ConnectorConfig`
  加两个字段、`SetConnectorConfig` 的列表更新、新增
  `GetConnectorConfig`/`SetProbeSwitch`/`ErrConnectorConfigNotFound`）
- `internal/platform/jobs/client.go`（`Config` 加三个字段，`NewClient`
  始终注册 `AssuranceProbeWorker`、新增队列 `assurance.QueueProbe`）
- `internal/platform/httpapi/router.go`（`Deps.AssuranceProbes` + 两条
  路由）
- `internal/platform/oidcauth/rolemap.go`（`admin` 加两个 scope，新增
  角色 `assurance-probe-admin`）
- `docs/modules/action/PERMISSIONS.md`（四行 + 一段 ADR-019 引用）
- `cmd/platform-api/main.go`（Action 注册、insert-only River 客户端、
  Query Deps 装配）
- `cmd/platform-worker/config.go`、`main.go`（两个新环境变量解析、
  探测 Worker 装配、探测凭据 Provider 装配）

## tests_run

Go（在仓库根目录，`invoice-test-pg` 容器的 `xm_test` 库，已跑
`go run ./cmd/migrate -database "postgres://postgres:test@127.0.0.1:55432/xm_test?sslmode=disable" -path db/migrations up`，
并验证过 down→up 往返）：

```
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go build ./...
  — PASS（全仓库）
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go vet ./...
  — PASS（无输出）
"$(go env GOROOT)/bin/gofmt" -l <本片新增/改动的每个具体 .go 文件>
  — 第一轮命中 10 个文件（多是本片新写代码里 struct 字面量的字段对齐），
    "$(go env GOROOT)/bin/gofmt" -w 逐个文件（不带 /...）修正，复核
    再跑 -l 干净
XM_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/xm_test?sslmode=disable" \
  env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
  go test -p 1 -count=1 ./...
  — PASS，跑了两轮（第一轮抓到并修了两个真实 bug，见上文"实现期发现",
    第二轮全绿）：全仓库 50 个有测试文件的包全部 `ok`，0 个 FAIL
bash scripts/check-governance.sh
  — PASS（exit 0，无输出）
gitleaks（本地二进制 /c/Users/58439/.local/bin/gitleaks 8.30.1，clone 到
  临时目录后扫描）
  — PASS：7 commits scanned, no leaks found
```

`internal/platform/assurance` 新增测试覆盖清单：

- `types_test.go`：`ParseTargets`/`ParseExpectedShape` 的合法/非法输入
  （空数组、缺字段、非法 JSON、区间颠倒、`timeout_ms` 超硬顶/为零）。
- `templates_test.go`：四个模板的 `BuildMessages`；`Assess` 对每个模板的
  ok/degraded 分支（含 `must_contain`/长度区间通用断言）；
  `benchmark_set` 的"与上一次分数比较"三种走向（下降/持平/无历史）；
  `ParseBenchmarkScore` 往返与拒绝外来文本；`expectedFingerprintFragment`
  的厂商前缀表与回退。
- `fakeclient_test.go`：四个模板的健康响应都能让 `Assess` 判 ok；
  `-fake-fail` 后缀确定性失败；`max_tokens` 上限遵守；ctx 取消。
- `realclient_test.go`（httptest.NewTLSServer）：成功解析
  choices/usage；401/429/5xx/4xx/畸形 JSON/空 choices 的错误分类；
  ctx 超时；构造期拒绝空 host/token。
- `actions_test.go`：`RegisterActions` 对 nil store/switcher 的拒绝；
  四个 Definition 的 ID/风险等级/权限/PrincipalTypes 与契约文件一致；
  `kill_switch.set` 字面 ID 钉住冻结设计稿的选择；`killSwitchSetHandler`
  的成功/省略 ref/显式清空 ref/三种错误映射/缺 Principal；
  `domainError` 的完整映射表。
- `store_integration_test.go`（真库）：`Declare` 新建/更新/版本冲突/
  拒绝未知渠道/拒绝目录未采集；`Cancel` 双重取消/不存在/取消后不可复活；
  `EvaluateAndCreateRun` 的 fake 模式全绕过、`client_run_key` 去重、
  声明已取消、**real 模式全部八种拒绝原因逐一构造前置状态断言精确原因**
  （子测试形式）、全部满足时成功入队；`RecheckAtExecution` 的自排除
  预算测试；`GetConnectorProbeConfig` 缺省 nil；`InsertResult` 的
  evidence_ref 派生；`LatestBenchmarkScore` 读回；`ListProbeHistory`
  游标分页。
- `service_integration_test.go`（真库）：从未运行/fake 模式
  kill_switch_state；多结果取"最坏"汇总（且只在批次 succeeded 之后才
  汇总，仍在 pending/running 时如实显示"进行中"）；real 模式
  kill_switch_state 从 disabled → enabled 随配置变化；`ProbeHistory`
  带上声明上下文。
- `internal/platform/jobs/assurance_probe_test.go`：非 pending 批次跳过；
  声明被撤销的执行时刻竞态 → cancelled；预算耗尽的执行时刻竞态复检 →
  refused（`race_` 前缀）；fake 模式全绿；`-fake-fail`
  混入健康 target 的部分失败不中断批次；存储写失败 → 整批 failed；
  real 模式缺 SecretProvider/凭据引用非法两种"构造客户端失败"路径 →
  refused（不是 Job 错误）；`benchmark_set` 读回上一次分数影响 trend
  文案；超时与"失败"分类可区分（用父 context 短 deadline 隔离，不用等
  真实 30s）。
- `internal/platform/httpapi/assurance_probes_test.go`：两个端点各自的
  scope 校验；响应形状（含 `channel_breakdown_supported` 恒 true、
  `assertion_disclaimer` 常驻、`cannot_run_reason`/人话文案并存）；
  历史端点的游标分页与 `next_cursor`；非法 cursor 400；Deps 为 nil 时
  两个端点整组 404。
- `internal/platform/credentials/connector_config_probe_test.go`（真库）：
  `SetProbeSwitch` 要求已有 connector_config 行、要求 real 模式+凭据引用
  同时满足、成功路径不触碰 mode/endpoint/credential_ref、省略 ref 保留
  原值/显式空串清空、非法引用拒绝；`GetConnectorConfig` 缺省 nil。

## not_run

- **未针对任何真实厂商执行过 real 模式探测**：`RealClient` 只在
  `httptest` 假服务器上单测过请求构造/超时/错误映射，符合团队交接消息
  "no live call in tests"的要求，也符合设计稿 §9 把"真实模式的一次性
  验证证据"划给 XM-ASSURE1-real 的路线。
- **未跑 XM-ASSURE1-ui 那一片的任何前端代码**：本片 0 处改动
  `web/` 目录下的文件，`pnpm --config.verify-deps-before-run=false
  -r run typecheck`/`-r run test` 因此**未运行**——没有前端 diff 需要
  验证，跑这两个门禁不会告诉验收线任何本片改动相关的信息。若验收线仍
  希望留一条记录：本片交付前 `git status --short` 确认过 `web/` 目录
  零改动。
- **未新建"migration test"这个测试形态**：仓库里翻遍现有测试文件,
  没有找到任何一个专门测"一次迁移本身"的独立测试文件先例（既有的都是
  "在真库上跑集成测试，隐式验证了迁移已正确应用"）。本片按同一惯例处理：
  没有新增 `db/migrations` 相关的独立测试文件，"迁移是否正确"由
  `go run ./cmd/migrate ... up`（已跑，见上）+ 全部 `assurance`/
  `credentials` 集成测试能够正确建表/插入/查询这件事本身来验证。如果
  验收线期望一个不同形态的"迁移测试"，这是需要另外补的东西，不是被
  这份交接文档悄悄跳过的。
- **未接入 `internal/platform/jobs/retention.go` 的保留期清理**：设计稿
  建议 `probe_run`/`probe_result` 默认保留 90 天，迁移里的时间戳列已经
  具备被保留期任务扫描的条件，但本片没有在 `retention.go` 里新增第三个
  清理目标——团队交接消息的范围条目只写了"migration...with retention
  columns"，没有明确要求本片接线清理任务本身，按字面范围判断为
  follow_up 而非本片遗漏，见下方 follow_ups。
- **未做真实 River 队列的端到端插入验证**：`assurance.Store` 的集成
  测试用一个记录调用的 `fakeJobEnqueuer`（不需要真库有 `river_job`
  表），没有验证过一个真实 `river.Client[pgx.Tx].InsertTx` 在这个测试库
  上确实能成功插入——这条路径的正确性由类型系统保证（`*river.Client
  [pgx.Tx]` 结构性满足 `assurance.JobEnqueuer`，`cmd/platform-api/
  assurance.go` 的 `newAssuranceProbeInsertClient` 直接返回这个类型）,
  但没有一次真正跑通"Action 插入 → Worker 真的从队列里取到并执行"的
  完整集成测试（`internal/platform/jobs/assurance_probe_test.go` 测的是
  Worker 单独调用，不经过真实 River 队列）。

## risks

1. **kill_switch Action 的字面 ID 选择**（偏离 1）如果验收线判断应该
   跟随派工消息的简写而不是冻结设计稿，需要一次改名（契约文件名、
   `ActionKillSwitchSet` 常量、`PERMISSIONS.md` 那一行、一条测试）——
   影响面小但需要明确决定。
2. **real 模式没有首字节延迟数据**（偏离 4）：如果产品负责人认为这个
   信号对判断"渠道是否退化"很重要，需要一次追加的流式实现，不是本片
   遗漏而是刻意的范围裁剪，但值得在合入前再确认一次是否可接受。
3. **`assurance-probe-admin` 角色目前没有任何账号持有**：本地登录模式下
   `probe.kill_switch` 这个权限点已经在默认角色映射表里可用,但需要
   负责人显式跑一次 `staff.account.set_roles`（或 OIDC 模式下改
   `XM_OIDC_ROLE_SCOPES`）才能真正授予给某个人——在那之前,任何人（含
   admin）都无法调用 `kill_switch.set@1`,real 模式的第三层闸因此对
   所有人都是关着的（这是设计意图,不是缺陷,但合入后如果立刻想验证
   real 模式,记得先做这一步）。
4. **四层闸都建在假设"两个进程的环境变量配的是同一个值"上**：
   `XM_ASSURE_PROBE_ENABLED`/`XM_ASSURE_PROBE_DAILY_BUDGET`
   在 `cmd/platform-api`/`cmd/platform-worker` 各自独立解析,配歪了（比如
   只改了一边）会出现"Action 说能跑,Job 执行时刻却拒绝"的诡异体验——
   已在 RUNBOOK.md 的排障表里写明,但这是这套"两进程各自读环境变量"架构
   （`XM_CONNECTOR_PROBE_ENABLED` 的既有先例）本身自带的运维风险,不是
   本片独有。
5. **`checkTargetsExist` 依赖 `<platform>.channels.status` 的 ops
   观测已经存在**：一个从未跑过 `sub2api_sync`/`newapi_sync` 周期任务的
   全新环境,declare@1 会对任何 targets 一律拒绝（"渠道目录尚未采集"）,
   这是刻意的 fail-closed 行为,但意味着**声明检测任务的前提是该平台的
   常规同步任务至少成功跑过一轮**——如果验收线在一个刚起的全新环境测试
   本片,先确认这一点,不要误判成 declare@1 本身坏了。

## follow_ups

### 供 XM-ASSURE1-ui 直接消费的 Query 形状

`GET /api/v1/platforms/{platform}/assurance/probes`（权限 `request.read`）：

```json
{
  "platform": "sub2api",
  "probes": [{
    "declaration_id": "uuid",
    "name": "模型指纹",
    "channel_ids": ["chn-1"],
    "channel_names": ["渠道 chn-1"],
    "target_models": ["claude-sonnet-4"],
    "policy_text": "按需 · 无定时",
    "schedule_cron": "",
    "last_run_at": "2026-09-03T02:30:00Z",
    "last_run_status": "ok",
    "last_run_verdict": "模型自称一致",
    "kill_switch_state": "not_applicable_fake",
    "can_run_now": true,
    "cannot_run_reason": "",
    "cannot_run_reason_text": ""
  }],
  "freshness": {"state": "fresh", "staleness_seconds": 0, "threshold_seconds": 1800, "observed_at": "...", "last_success": "...", "last_error_code": ""},
  "assertion_disclaimer": "检测结果为形状与延迟检测，非语义正确性保证。",
  "channel_breakdown_supported": true
}
```

`last_run_status` 取值：`never_run` / `pending` / `running` / `refused` /
`cancelled` / `ok` / `degraded` / `failed` / `timeout`（后四个是逐渠道
结果里"最坏"汇总，只在批次已经 `succeeded`/`failed` 之后才出现）。
`cannot_run_reason` 是稳定枚举串（`assurance.Reason*`），
`cannot_run_reason_text` 是人话版本，前端 tooltip 用后者、不要显示前者
原始字符串（设计稿 §6.1）。

`GET /api/v1/platforms/{platform}/assurance/probe-history?cursor=<RFC3339>&limit=<1..200>`
（权限同上；`cursor`/`limit` 均可省略，默认 50 条，游标是上一页最后一条
的 `created_at`）：

```json
{
  "platform": "sub2api",
  "entries": [{
    "observed_at": "2026-09-03T02:30:00Z",
    "channel_id": "chn-1", "external_channel_id": "",
    "model": "claude-sonnet-4",
    "declaration_name": "模型指纹", "prompt_template_key": "model_fingerprint",
    "status": "ok", "verdict": "模型自称一致", "evidence_ref": "probe-8f3a1c2d",
    "latency_ms": 12, "first_token_ms": 5, "measured_first_token": true,
    "tokens_used": 8, "http_status": 200, "error_kind": ""
  }],
  "next_cursor": "2026-09-03T02:29:00Z",
  "freshness": {...},
  "channel_breakdown_supported": true
}
```

`next_cursor` 非 null 时表示"可能还有更多"（返回条数等于请求的
`limit`），前端据此决定是否显示"加载更多"。

### 其余

- **首字节延迟（流式实现）**：见 risks #2，是否需要给 `RealClient` 补一版
  流式请求，取决于产品负责人对这个信号的重要性判断。
- **保留期清理**：`assurance.probe_run`/`probe_result` 尚未接入
  `internal/platform/jobs/retention.go`，建议默认 90 天（与团队既有
  "历史类明细表"保留期同量级）。`assurance.probe_declaration` 不参与
  保留期清理（声明是配置，不是明细日志）。
- **冷却间隔的环境变量化**：目前只有每日预算做了
  `XM_ASSURE_PROBE_DAILY_BUDGET`，冷却间隔（默认 60s）只能通过改代码里
  的 `assurance.DefaultCooldown` 或构造参数调整，没有接部署环境变量。
- **定时调度（`schedule_cron`）**：声明时可以填，落库但不生效——本片
  刻意不接线任何周期调度器，见团队交接消息"on-demand only (no schedule)
  in this slice"。真要做，需要一个新的 River 周期任务，在到期时以
  `principal_id='worker:platform'`、`SERVICE` 身份调用
  `assurance.probe.run@1`（不绕过 Handler 的任何一步检查，设计稿 §3.3）。
- **渠道展示名解析的降级路径**：`ChannelDisplayNames` 读不到渠道目录时
  退回显示原始 `channel_id`（不是让整个 Query 500），这是刻意的降级,
  但意味着如果渠道目录采集长期失败，检测任务表会一直显示 id 而不是人类
  可读的名字——这本身就是"目录没采集"这件事的一个可见信号，不需要额外
  处理，写在这里是为了让 XM-ASSURE1-ui 的实现者知道这是预期行为。
- **XM-ASSURE1-real 的前提条件**：见 `docs/modules/assurance/RUNBOOK.md`
  的完整步骤（登记探测专用凭据、打开三层开关）。负责人需要预先决定：
  哪个平台先做真实验证、每日预算具体数值、探测账号用哪一个——这些都不是
  代码切片能替负责人决定的（ADR-019 待拍板问题 #3/#4）。
