# XM-ASSURE1-glue：渠道主动探测（检测任务）· 前后端整合校验

## status

READY（待验收线审读、复跑并人工合入）。全部本地门禁绿；开工期间发现一处
不在本片范围内、影响全平台的 Kernel 缺口，已拆成独立切片
XM-KERNEL-ERRCODE0 并已修复合入 `release/v0.1-launch`——本片已把
`release/v0.1-launch` 合并进来、更新了相关断言，见下方"重要发现"。

## branch / commit

- branch: `ai/claude/XM-ASSURE1-glue`
- worktree: `K:/星芒统一控制平台/wt-xmASSURE1G`
- base: `release/v0.1-launch` @ `5b69b91`（含 XM-ASSURE1-core 与
  XM-ASSURE1-ui 两片合入）
- 提交（`git log --oneline release/v0.1-launch..HEAD`，按时间顺序，
  base 一节的 `5b69b91` 是本片开工时的 base；分支中途又合并了一次更新后的
  `release/v0.1-launch`，见下面第 6 条）：
  1. `257fa4e` fix(httpapi): project probe_enabled/probe_credential_registered on connectors config
  2. `e1069eb` feat(admin-web): fall back to connector config for kill-switch state on zero probes
  3. `f129779` test(jobs): prove XM-ASSURE1-ui/-core compatibility with a real declare->run->query pass
  4. `db101f0` docs(handoffs): add XM-ASSURE1-glue slice handoff
  5. `d151b7d` docs(handoffs): reference XM-KERNEL-ERRCODE0 for the Kernel error-code finding
  6. `git merge release/v0.1-launch`（无 rebase，把 XM-KERNEL-ERRCODE0
     的修复——含 `internal/platform/action/kernel.go` 的 Execute() 改动、
     该切片自己的 handoff——合并进本分支；干净合并，无冲突）
  7. 本次更新（团队约定 Handoff 随最后一次改动一起提交，见下方"重要发现"）
  本文件作为分支上的一部分随第 7 个提交更新（先例见 XM-ASSURE1-core/-ui
  把 Handoff 作为独立提交；本片因为在验收反馈后有过两轮回填，
  Handoff 文件本身经历了三次提交，符合"Handoff 是本分支最后落地状态"的
  精神，不是字面"只提交一次"）。

## 派工任务的三处具体要求，逐条回报

### 1. `GET /api/v1/connectors/config` 补投影（已修，Go + 前端各一处）

`internal/platform/httpapi/credentials.go` 的 `connectorConfigItem` 新增
`probe_enabled`（bool，原样透传）与 `probe_credential_registered`
（bool，= `probe_credential_ref` 非空）两个字段，`toConnectorConfigItem`
对应赋值。

**redacted-to-boolean 与既有先例的取舍**：派工消息要求"redacted to a
boolean...never the ref string"，但复核后发现既有 `credential_ref`
字段本身就是原样透传（它只是引用名，不是凭据值，`connectorConfigItem`
顶部注释早已写明）——严格"跟随既有先例"应该是也原样透传
`probe_credential_ref`。两条指导原则在这里冲突，我按派工消息的字面
要求（布尔值）实现，因为这是消息里唯一明确、无歧义的具体指令；已在
`connectorConfigItem` 的文档注释里写清楚这处冲突与判断依据，留给验收线
确认是否需要改回原样透传。

前端 `web/apps/admin-web/src/api/connectors.ts` 的 `ConnectorConfig`/
`projectConfig` 同步跟上两个新字段（防御性布尔强转，见测试）。
`AssuranceProbeKillSwitch.tsx` 新增 `fallbackConfig` 可选 prop，
`PlatformAssurancePanel.tsx` 只在该平台检测任务表为空时才多打一次
`GET /api/v1/connectors/config`（`useQuery` 的 `enabled` 门控），查询
失败（比如调用者没有 `connector.manage`，这与能打开 Kill Switch 的
`assurance-probe-admin` 角色是两个完全独立的权限点，见
`internal/platform/oidcauth/rolemap.go`）时静默保留"未知"这个既有展示，
不让整块面板报错。

**这不是 `kill_switch_state` 的完整替身**：后端
`assurance.Service.killSwitchState()` 还叠加了一个进程级全局开关
（`XM_ASSURE_PROBE_ENABLED`），这个全局位没有任何端点会暴露给前端，
因此零声明兜底态推出的"已启用/未启用"只反映"这个平台自己的 Kill
Switch 开关"，不含全局开关那个因子——已在 `AssuranceProbeKillSwitch.tsx`
的文档注释里写明，这是刻意的近似值，不是错误。

### 2/3. `targets.channel_id` 语义与自由文本模型名（验证通过，无需改动，补了集成测试）

逐句核对 `internal/platform/assurance/store.go`（`checkTargetsExist`/
`channelCatalog`）与 `internal/platform/httpapi/platform_channels.go`
（`catalogRowsForService`/`inventoryForService`）：两者读的是**同一个**
`<platform>.channels.status` ops 观测、按**同一个** `row["channel_id"]`
字段键控。这证明 XM-ASSURE1-ui 交接文档偏离 #6 的判断（渠道目录的
`externalChannelId` 同时塞进 `channel_id`/`external_channel_id` 两个
字段）与后端 `checkTargetsExist` 的存在性校验用的是同一个 ID 空间——
不是巧合，是两片各自独立读同一份数据源的必然结果。

自由文本模型名：`internal/platform/assurance/templates.go` 的
`expectedFingerprintFragment` 对不认识的厂商前缀会退化成模型字符串本身
（小写），`FakeClient`/`Assess` 用的是同一个函数，因此**任何**非空模型名
都能在 `model_fingerprint` 模板下产出 `ok` 结果；`Target.Validate()`
只检查非空白，Action Schema 的 `targets` 是 JSON 字符串参数，模型名不受
任何枚举限制。`internal/platform/jobs/assurance_probe.go` 的
`probeOneTarget` 把 `target.Model` 原样写进 `Result.Model`，不做任何
规整/截断。

以上两点原本只是读代码得出的结论，为了"没有中间 mock"地钉住这两个结论，
新增 `internal/platform/jobs/assurance_probe_ui_compat_integration_test.go`
（详见 files_changed/tests_run）：请求体按前端 `assuranceProbes.ts` 的
`declareProbe()`/`encodeTargets()` 逐字段构造（含一次真实的 JSON
marshal/unmarshal 往返，确保数字真的是 `float64`，贴近真实 HTTP 请求体的
类型），跑真实 `action.Kernel.Execute`（Schema 校验/权限/环境检查全部
真实发生，不是直接调 Handler）→ 真实 `assurance.Store`（真库）→ 真实
`AssuranceProbeWorker`（fake 探测客户端）→ 真实 `assurance.Service`
的两个 Query 方法（httpapi 层直接调用的同一批方法）。

### 4. UI 请求形状是否需要改（未发现需要改的地方）

逐项核对 declare/cancel/run/kill_switch.set 四个 Action 的参数、两个
Query 的响应字段名、Action ID 字面量、权限点/角色名常量——`assuranceProbes.ts`
与后端契约完全一致，包括：

- `expected_shape` 的 JSON 编码结构与默认值
- `client_run_key`/`declaration_id`/`expected_version` 的省略语义
- `probe_credential_ref` 省略保留/显式清空两种语义
- 四段式 Action ID `assurance.probe.kill_switch.set`（UI 已经用的是冻结
  设计稿的字面量，不是派工消息的简写，与 XM-ASSURE1-core 的选择一致）

没有发现 UI 请求形状与后端不匹配、需要改小的地方。

## 重要发现（已由 XM-KERNEL-ERRCODE0 修复，本片已同步）

开工期间发现 `internal/platform/action/kernel.go` 的 `Execute()` 会把
Handler 自己算出的错误码一律丢弃、重新包成通用的 `CodeExecutionFailed`
（`errors.As` 只找链条上第一个 `*action.Error`，那是 Kernel 新包的外层，
不是 Handler 原本返回的那个），影响全平台所有 Action，不只是 ASSURE1。
已通过 SendMessage 同步给 team-lead，确认后拆成独立切片
**XM-KERNEL-ERRCODE0**（agent `kernelerrcode`，worktree
`wt-kernel-errcode0`）修复，修复提交 `e6ddbfe`（`fix(action): preserve
handler error codes through Kernel.Execute`）已合入
`release/v0.1-launch`。本片随后 `git merge release/v0.1-launch` 把这个
修复带了进来，并把 `TestUICompatDeclareRejectsUnknownChannelID` 的断言
从"当前真实行为是 EXECUTION_FAILED"改成了"修复后应该是
INVALID_PARAMS"（见 tests_run），本片自己不再需要跟踪这件事。

## summary

本片是纯粹的整合校验+补缺口，不改设计、不新增业务能力：

1. 补一处后端投影缺口（`connectorConfigItem` 两个新字段）并接上前端
   消费者（`AssuranceProbeKillSwitch` 的零声明兜底）。
2. 用一对真实端到端集成测试钉住两处此前只有"读代码 + 分别的单元测试"
   支撑、从未被真正验证过的兼容性假设（`channel_id` 的 ID 空间、自由
   文本模型名），证明两片确实彼此兼容。
3. 逐项核对全部 Action/Query 请求形状，未发现需要改小的地方。
4. 意外发现一处不在本片范围内、影响全平台的 Kernel 缺口，上报后由
   XM-KERNEL-ERRCODE0 独立修复并已合入，本片合并了这次修复并同步更新
   了相关断言（见"重要发现"）。

## files_changed

（`git merge release/v0.1-launch` 带进来的 XM-KERNEL-ERRCODE0 修复
——`internal/platform/action/kernel.go`/`kernel_test.go` 与其它随
`release/v0.1-launch` 一并合入的文件——不计入本片 files_changed，那些
不是本片改的；完整清单见 `docs/handoffs/slices/XM-KERNEL-ERRCODE0.md`。
下面只列本片自己写的改动。）

修改：

- `internal/platform/httpapi/credentials.go`（`connectorConfigItem`
  加两个字段、`toConnectorConfigItem` 加两行赋值）
- `internal/platform/httpapi/credentials_test.go`（`TestListConnectorConfigsShape`
  覆盖两个新字段的取值与零值分支、字段数从 8 改成 10、断言凭据引用
  字面值不出现在响应体里）
- `docs/handoffs/slices/XM-CRED0-backend.md`（在已交付的"只读 Query"
  一节原地追加一段 `[XM-ASSURE1-glue 补充]` 注记，说明新增字段来源与
  语义——不是重写历史，是在唯一记录这个端点响应形状的文件上补一笔）
- `web/apps/admin-web/src/api/connectors.ts`（`ConnectorConfig` 加两个
  字段、`projectConfig` 加两行防御性布尔强转）
- `web/apps/admin-web/src/api/connectors.test.ts`（覆盖新字段投影与
  非布尔值防御性强转为 false）
- `web/apps/admin-web/src/components/AssuranceProbeKillSwitch.tsx`
  （新增 `fallbackConfig` prop 与 `killSwitchStateFromConfig` 派生函数，
  "未知"文案去掉"尚无检测任务"这个不再是唯一原因的具体措辞）
- `web/apps/admin-web/src/components/AssuranceProbeKillSwitch.test.tsx`
  （更新一条既有测试的文案断言、新增四条覆盖 fallbackConfig 各分支与
  "检测任务表有数据时忽略 fallbackConfig"的测试）
- `web/apps/admin-web/src/components/PlatformAssurancePanel.tsx`（新增
  一个门控 `useQuery` 读连接器配置、把结果整理后传给
  `AssuranceProbeKillSwitch`）

新增：

- `internal/platform/jobs/assurance_probe_ui_compat_integration_test.go`
  （两个端到端集成测试，见下方 tests_run）
- `docs/handoffs/slices/XM-ASSURE1-glue.md`（本文件）

## tests_run

Go（在仓库根目录，`XM_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/xm_test?sslmode=disable"`
指向的测试库，迁移已到 000025，未新增迁移）：

```
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go build ./...
  —— PASS（全仓库）
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go vet ./...
  —— PASS（无输出）
"$(go env GOROOT)/bin/gofmt" -l <本片新增/改动的每个具体 .go 文件>
  —— 干净，无需改动
XM_TEST_DATABASE_URL=... env -u HTTP_PROXY ... go test -p 1 -count=1 ./...
  —— PASS，全仓库所有有测试文件的包 `ok`，0 个 FAIL（跑了两轮：第一轮
    `internal/platform/jobs` 包单独 `-run UICompat` 时绿，但混进全仓库
    `./...` 一起跑时暴露一个真实 bug——见下方"实现期发现"；修复后第二轮
    全绿，另外把 `internal/platform/jobs`/`assurance`/`credentials`/
    `httpapi` 四个包单独 `-count=2` 跑了两遍，确认不是偶发）
```

合并 `release/v0.1-launch`（带着 XM-KERNEL-ERRCODE0 的修复）之后又跑了
一轮，针对性验证团队交接消息要求的范围：

```
git merge release/v0.1-launch
  —— 干净合并，无冲突（26 files changed）
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
  go vet ./internal/platform/jobs/... ./internal/platform/action/...
  —— PASS（无输出）
"$(go env GOROOT)/bin/gofmt" -l internal/platform/jobs/assurance_probe_ui_compat_integration_test.go
  —— 干净，无需改动
XM_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/xm_test?sslmode=disable" \
  env -u HTTP_PROXY ... go test -p 1 -count=1 -v ./internal/platform/jobs/...
  —— PASS（含 TestUICompatDeclareRunQueryEndToEnd/
    TestUICompatDeclareRejectsUnknownChannelID 两条，后者现在断言
    CodeInvalidParams 且通过）
XM_TEST_DATABASE_URL=... env -u HTTP_PROXY ... go test -p 1 -count=1 ./internal/platform/action/...
  —— PASS
```

`internal/platform/jobs/assurance_probe_ui_compat_integration_test.go`
新增测试覆盖：

- `TestUICompatDeclareRunQueryEndToEnd`：declare@1（含真实 JSON marshal/
  unmarshal 往返构造的请求体）→ run@1 → `AssuranceProbeWorker.Work`
  真实执行一次批次（fake 探测客户端）→ `Service.ProbeList`/
  `Service.ProbeHistory` 核对渠道 ID、自由文本模型名、`ok` 结果全部
  正确回显。
- `TestUICompatDeclareRejectsUnknownChannelID`：对渠道目录里确实不存在
  的 `channel_id` 声明，断言 `action.ErrorCode(err) ==
  action.CodeInvalidParams`（XM-KERNEL-ERRCODE0 修复后的正确行为，合并
  该切片前这里断言的是 `CodeExecutionFailed`，见"重要发现"一节）、
  `errors.Is(err, assurance.ErrInvalidInput)` 为真，以及 `err.Error()`
  直接提到具体的未知渠道 ID（修复后 Handler 的 Message 也会原样带到最
  外层，不再需要 Unwrap 才能看到）。

`internal/platform/httpapi/credentials_test.go` 更新的
`TestListConnectorConfigsShape`：两个新字段的非零值/零值分支、字段数
断言、探测凭据引用字面值不泄漏进响应体。

前端（`web/` 目录，worktree 用镜像脚本补齐 `node_modules`）：

```
pnpm --config.verify-deps-before-run=false -r run typecheck
  —— PASS（5 个前端 workspace 包全部 tsc --noEmit 无输出）
pnpm --config.verify-deps-before-run=false -r run test
  —— PASS：design-tokens 10、ui-primitives 16、ui-admin 261、admin-web
    1464，共 1751 个用例全绿（admin-web 从 XM-ASSURE1-ui 交付时的 1458
    增长到 1464，净增 6 个）
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
  —— PASS（"Storybook build completed successfully"；本片未新增
    ui-admin/ui-primitives 组件，没有新故事要写）
bash scripts/check-governance.sh
  —— PASS（exit 0，无输出）
```

`web/apps/admin-web/src/api/connectors.test.ts` 新增/修改：现有两条
`toEqual` 快照断言补上两个新字段（否则会因为多出字段而失败）；新增一条
覆盖非布尔值防御性强转为 `false`。

`web/apps/admin-web/src/components/AssuranceProbeKillSwitch.test.tsx`
新增/修改：既有"零声明"测试的文案断言更新；新增"零声明但能读到连接器
配置时用它派生当前状态"（含初始目标状态跟着派生值走的断言）、"mode=real
但 probe_enabled=false"、"mode=fake"、"检测任务表本身有数据时忽略
fallbackConfig"四条测试。

gitleaks：见下方 risks 之前的说明（本片提交前跑过，见 not_run 之后的
"gitleaks 复核"）。

### gitleaks 复核

`git clone --no-local --branch ai/claude/XM-ASSURE1-glue --single-branch
<worktree> /tmp/scan` → `gitleaks detect --source=.
--log-opts="5b69b91..HEAD" --verbose --redact=0` → 3 commits scanned
（本文件作为第四个提交尚未加入时的扫描；本文件本身不含任何凭据值，
只含字段名与文档引用）。

## not_run

- **未新建/验证 `assurance-probe-admin` 与 `connector.manage` 两个权限
  同时持有的真实账号场景**：`AssuranceProbeKillSwitch` 的零声明兜底
  依赖调用者也能读 `GET /api/v1/connectors/config`（`connector.manage`
  scope），但 `internal/platform/oidcauth/rolemap.go` 里
  `assurance-probe-admin` 角色**只**含 `assurance.probe.kill_switch`
  一个 scope（ADR-019 决策·四·#4 的刻意设计，见该文件"专门角色
  assurance-probe-admin"一段的注释）——如果一个真实账号只被授予了
  `assurance-probe-admin`、没有额外授予 `connector.manage`/`admin`/
  `credential-admin`，零声明兜底查询会 403，前端会静默退回"未知"
  （这是设计好的降级行为，测试也覆盖了这个查询失败的分支，但没有搭一套
  真实 OIDC/本地登录会话去验证"只有 assurance-probe-admin 单一角色"这个
  最常见的实际部署场景下、兜底确实优雅降级而不是报错）。
- **未验证 real 模式端到端**：本片全部集成测试用 fake 探测客户端（无
  connector_config 行）——real 模式的四层闸（Kill Switch/预算/白名单/
  冷却+并发）验证属于 XM-ASSURE1-core 交接文档划给 XM-ASSURE1-real 的
  范围，本片不重复覆盖。
- **未对着真实浏览器重新截图**：本片未改动任何视觉呈现（`AssuranceProbeKillSwitch`
  的文案微调是"未知"分支的措辞，不是新增视觉状态），XM-ASSURE1-ui
  已经交付过的六张截图仍然如实反映当前 UI。

## risks

1. **`probe_credential_registered` 布尔化与 `credential_ref` 既有先例
   冲突**（见"派工任务的三处具体要求"第 1 条）：如果验收线认为应该
   跟随既有先例原样透传引用字面值，需要一次改动（Go 字段类型、前端
   `ConnectorConfig` 类型、两处测试），影响面小但需要明确决定。
2. ~~Kernel 缺口影响全平台~~ **已解决**：已由 XM-KERNEL-ERRCODE0 修复
   并合入 `release/v0.1-launch`，本片已合并该修复，见"重要发现"。
3. **零声明兜底的全局开关盲区**（见"派工任务"第 1 条）：一个平台的
   Kill Switch 本身已打开、但进程级全局开关
   `XM_ASSURE_PROBE_ENABLED` 被关闭这种少见运维场景下，零声明兜底会
   显示"已启用"而服务端实际会拒绝执行——这是前端今天没有任何端点能
   读到全局开关状态导致的已知局限，不是本片的疏忽，已在代码注释里
   写明。

## follow_ups

- ~~Kernel 错误码透传缺口~~（risks #2）：**已完成**——XM-KERNEL-ERRCODE0
  已修复并合入，本片已合并、更新断言、重跑门禁，不再需要后续动作。
- **`probe_credential_registered` 是否改回原样透传**（risks #1）：需要
  产品/安全负责人一次性拍板，两种做法都只是几行改动。
- **全局开关状态的前端可见性**（risks #3）：如果运营侧认为这个盲区
  值得补，需要一个新的只读端点（或在 `GET /api/v1/connectors/config`
  再加一个进程级字段——但这个字段不属于任何单一 `(platform,
  environment)` 行，形状上需要另外设计）。
- **零声明兜底在只持有 `assurance-probe-admin` 单一角色时的真实降级
  体验**（not_run 第一条）：建议验收线用一个只授予这一个角色的真实
  账号走一遍零声明平台的 Kill Switch 对话框，确认"未知"这个降级态
  在真实 403 响应下确实如期出现。
