# XM-REQLOG-MERGE · 请求审计系统收编（记录代理 + 文件后端连接器）

## status

READY（详情见下方 not_run / risks——部分校对项本次未接触真实生产环境，
留给验收线在服务器上核对）

## branch / commit / base

- branch: `ai/claude/XM-REQLOG-MERGE`
- base: `c00c853`（`release/v0.1-launch` 当前 tip）
- implementation commit: `b766697`
- worktree: `K:/星芒统一控制平台/acceptance/wt-reqlog-merge`

## scope

把用户在桌面端做的「请求审计系统」原型（透明记录代理 `reqlogger.go` +
查看端 `viewer.go`，已在生产以 systemd 服务运行）合并进星芒仓库，并让平台
「请求详情」直接读记录代理落盘的数据，不再规划接入原来 `:9300` 的 HTTP
查看端——用户原话「能不能把他合并到我们的项目中来，而不是接入之前的」。

四块交付：

1. **`cmd/reqlog-recorder/`**：从 `reqlogger.go` 收编的代理+存储+清理+令牌
   映射，硬编码值全部改成可配置项（flag，默认取环境变量，默认值再退到原
   硬编码值——见 `config.go`）；文件权限从原来的 `0700`/`0600`（仅 root 可读）
   收紧改造为可配置、**新默认** `0750`/`0640` + 可配置属组 GID（默认
   `10001`，即星芒平台容器的运行用户），让平台容器能以只读方式读到数据，
   不必再靠"平台连 root 的东西"。磁盘格式（`Record`/`FullRecord` 的字段与
   JSON 标签）逐字段与原型保持兼容，老数据可以被新代码读出来。
2. **`internal/platform/reqlogformat`**（新增共享包）：把 `viewer.go` 里
   `parseTurns`/`sseEvents`/`extractFinalText`/`extractUsage` 等解析逻辑
   连同磁盘记录的类型定义一起抽出来，供写侧（recorder）与读侧（连接器）
   共用同一份实现——避免两处各写一份、迟早漂开（宪法 4 条：同一个业务
   动作只实现一次）。
3. **`connectors/reqlog` 的 `NewFileClient`**：只读文件后端，实现完整
   `ReadClient` 契约（`ListRequests`/`RequestContent`/`Version`/`Health`/
   `Capabilities`），直读记录代理落盘的数据与 `tokenmap.json`，不发 HTTP
   请求——从根上绕开了 DRAFT 契约 §5 记录的"控制台是回环明文、只读闸要求
   https"未决冲突。跑通了 `connectors/reqlog/contracttest` 套件（20/21 项
   通过，1 项结构性预期失败，见下方 tests_run 与 risks）。
4. **装配 + 部署物**：`cmd/platform-api/reqlog.go` 新增 `XM_REQLOG_MODE=file`
   （与 `off`/`fake`/`real` 并列，**允许在生产使用**——它读的是记录代理落盘
   的真实数据，不是 fake 那样编造的样本）；`deploy/compose/launch.yaml` /
   `server-prod.yaml` 透传新变量并为 `platform-api` 加只读绑定挂载；
   `deploy/reqlog/reqlog-recorder.service` 新 systemd 单元；
   `docs/runbooks/REQLOG-RECORDER.md` 记录从旧 `reqlogger` 平滑切换到新
   `reqlog-recorder` 的完整步骤。

`contracts/connectors/reqlog.read.v1.md` 同步更新：新增 §10「File 后端」，
把从真实源码逐条核对出的结论（磁盘格式的 id 形状、ttfb 缺失表达、
channel/upstream/计费字段确认不存在等）写回 §8 的核对清单——这是本契约
DRAFT 状态首次拿到真实源码后的实质性核对，不是猜测。

## files_changed

新增：

- `internal/platform/reqlogformat/`：`doc.go`、`record.go`、`turns.go`、
  `sse.go`、`usage.go`、`truncate.go` + 对应 `*_test.go`（5 个测试文件）
- `cmd/reqlog-recorder/`：`main.go`、`recorder.go`、`config.go`、`storage.go`、
  `proxy.go`、`serve.go`、`tokenmap.go`、`perm.go` + 对应 `*_test.go`
  （`config_test.go`、`storage_test.go`、`proxy_test.go`、`tokenmap_test.go`）
- `connectors/reqlog/file_client.go`（NewFileClient 实现）
- `connectors/reqlog/file_client_test.go`、`file_fixture_test.go`
  （合成固件 + contracttest 套件接线 + 文件后端专属测试）
- `cmd/platform-api/reqlog_file_test.go`
- `deploy/reqlog/reqlog-recorder.service`
- `docs/runbooks/REQLOG-RECORDER.md`

修改：

- `cmd/platform-api/reqlog.go`（新增 `reqlogModeFile`、`reqlogConfig.DataDir`/
  `TokenMapPath`、`newReqlogFileClient`）
- `contracts/connectors/reqlog.read.v1.md`（新增 §10，更新 §8 引言与头部
  「实现」「合规判据」两行）
- `deploy/compose/launch.yaml`（透传 `XM_REQLOG_DATA_DIR`/`XM_REQLOG_TOKENMAP`，
  默认空——staging 一键栈不挂载对应目录）
- `deploy/compose/server-prod.yaml`（`platform-api` 加两个只读绑定挂载 +
  容器内路径默认值，宿主机路径可由 `XM_REQLOG_HOST_DATA_DIR`/
  `XM_REQLOG_HOST_TOKENMAP` 覆盖）
- `deploy/compose/.env.example`（新增「请求详情 · reqlog file 模式」小节）

删除：

- `_import/`（导入的只读参考源码，按指示不入库，已在提交前删除）

## tests_run

```
go build ./...                          — PASS（全仓库，61 个包）
go vet ./...                            — PASS（无输出）
gofmt -l <本片改动的全部 .go 文件>       — PASS（改动前发现 4 个文件未格式化，已 gofmt -w 修好并复核）
go test ./...                           — 60/61 包 PASS，唯一失败见下
bash scripts/check-governance.sh        — PASS（exit 0）
gitleaks detect --no-git -s . -v        — 4 处历史误报（sub2api.revenue.daily 等指标键字面量，
                                            均在本片未触碰的既有文件里），本片新增文件 0 处命中
docker compose -f launch.yaml -f server-prod.yaml
  --env-file <scratch> config           — PASS，人工核对生成的 platform-api 配置：
                                            volumes 含 xm-secrets + 两个新只读绑定挂载（三项都在，
                                            证实 compose 对 list 字段是合并而不是覆盖）；
                                            XM_REQLOG_MODE/DATA_DIR/TOKENMAP 默认值与 .env 覆盖
                                            均按预期解析（含 XM_REQLOG_HOST_* 覆盖宿主机路径的验证）
```

**唯一的测试失败**（预期内、已在代码与契约文档里说明，不是本次要修的
bug）：

```
--- FAIL: TestFileClientSatisfiesContract
    --- FAIL: TestFileClientSatisfiesContract/渠道上游与计费保留未知和已知零
        suite.go:147: 样本里没有渠道/上游元数据
```

`connectors/reqlog/contracttest` 的这一项断言任何合规实现的样本集里都能
凑出非空 `Channel`/`Upstream` 与已知零/未知两种 `BilledAmount`。逐字段核对
桌面端原型 `reqlogger.go` 的 `Record`/`FullRecord` 结构体后确认：**磁盘记录
格式本身完全没有渠道/上游/计费这三个字段**——不是文件后端漏接，是这条
数据源从不采集这个维度。文件后端如实对这三个字段返回空串/`nil`，没有
为了让这条子测试变绿而编造数据。详见
`contracts/connectors/reqlog.read.v1.md` §10.2 与
`connectors/reqlog/file_client_test.go` 里 `TestFileClientSatisfiesContract`
的文档注释。其余 20 项子测试全部通过。

新增测试覆盖清单（对照任务要求逐项核对）：

- SSE 抄录与用量解析：`internal/platform/reqlogformat/sse_test.go`（10 个用例，
  覆盖 Anthropic/OpenAI Chat/OpenAI Responses 三种协议形状 + 工具调用装配）、
  `usage_test.go`（7 个用例，覆盖三种用量字段命名与多事件累积）
- index 追加：`cmd/reqlog-recorder/storage_test.go` 的
  `TestWriteOneAppendsIndexLinesInOrder`（连续两条写入必须是追加而不是覆盖）
- 清理边界：`TestCleanOnceRetentionBoundary`（cut 当天保留、早于 cut 删除、
  非日期形态目录名忽略——三态都覆盖）
- 权限位：`TestWriteOnePermissionBits`（Linux/macOS 断言 0750/0640 精确落地；
  Windows 因权限模型不映射 Unix rwx 位而 `t.Skip`，生产部署目标是 Linux）
- 文件后端合规：`TestFileClientSatisfiesContract`（见上）+ 7 个文件后端
  专属测试（跨天分页、令牌邮箱打码、坏索引行容错、tokenmap 缺失降级、
  非法 ID 拒绝等）
- 平台装配：`cmd/platform-api/reqlog_file_test.go`（6 个用例，含"file 模式
  在生产环境应该被允许"这条与 fake 相反的钉子测试）

## not_run

- **未连接真实生产环境**：本片全程在本地 worktree 完成，没有 SSH 到
  fiberstate 服务器，没有跑过真实的 `docker exec ... psql` 令牌映射刷新，
  没有对着真实的 `/root/reqlog/data` 验证权限收紧后的实际读取效果。
  `docker compose config` 的验证用的是本地临时 `.env`（占位值），不是
  服务器上的真实 `.env`。
- **未做二进制的交叉编译产物验证**：`docs/runbooks/REQLOG-RECORDER.md`
  第 1 步给出的构建命令未在本地实际执行交叉编译并放到 Linux 机器上跑——
  本地只跑了 `go build ./cmd/reqlog-recorder/...`（当前平台）确认编译通过。
- **未跑前端相关门禁**：本片不改前端代码（任务说明里已确认"本任务不改
  前端"），未运行 `pnpm` 系列命令。
- **未做 web 镜像/go.Dockerfile 的 `docker build`**：`reqlog-recorder` 不
  参与 platform-api/platform-worker 的容器构建（它是宿主机 systemd 服务，
  不进容器栈），因此未改动、也未验证 `deploy/docker/go.Dockerfile`。

## risks

1. **令牌映射刷新绕开 Connector/CredentialRef 体系**（ADR-014、ADR-018 的
   四道只读闸）：`cmd/reqlog-recorder/tokenmap.go` 的 `refreshTokenMap`
   原样保留了原型的 `docker exec postgres psql ...` 与
   `docker exec sub2api-mig-postgres sh -c 'psql ...'` 两条命令，直接假定
   本机能 docker exec 进两个特定容器名执行只读 SQL，导出结果含用户名/邮箱
   等 PII，写入 `tokenmap.json`。这是桌面端原型已经在生产用的既有做法，
   本次收编的目标是让它可配置、可测试、磁盘格式兼容，**没有**重新设计
   这条凭据/数据获取链路——那需要独立的 ADR/Change Request，不在本次
   2 小时时间盒内。**这是记录代理运行在生产数据面上的既有事实，任何行为
   改动都要克制**（团队交接原话），本次除了权限位与可配置性之外没有改动
   它的行为。
2. **PII 处理点**：`tokenmap.json` 由记录代理从两个上游库只读导出，
   含用户名/邮箱，落盘明文（本身如此，非本次引入）。读侧
   `connectors/reqlog/file_client.go` 的 `resolveUsername` 在数据离开
   连接器之前，对形如邮箱的标识调用既有的 `platformusers.MaskEmail` 打码
   （宪法 7 条）；`TokenPrefix` 字段按契约既有口径原样透出（不是凭据，
   只是排查锚点，见 `contract.go` 的说明，不属于本次新增的 PII 面）。
   请求头里的 `authorization`/`x-api-key`/`cookie` 在落盘时已由记录代理
   截断打码；契约的 `RawPayload` 类型本身不包含头部字段，因此读侧没有
   "再脱敏一次"的对象——不透出头部本身就是比再脱敏更强的一层，细节见
   `file_client.go` 的 `RequestContent` 注释。
3. **`connectors/reqlog/contracttest` 有一项对文件后端结构性不适用**：
   见上方 tests_run 的详细说明与 `contracts/connectors/reqlog.read.v1.md`
   §10.2。不是本次的 bug，但验收线审读契约测试结果时需要知道这一点，
   不要误判成"文件后端没做完"。
4. **ADR-011（控制平面与数据平面边界）的适用性边界**：`reqlog-recorder`
   是一个站在用户实时请求路径上做透明转发的反向代理——这与 ADR-011「控制
   平台不参与用户实时请求路径」字面上冲突。但 `reqlog-recorder` 不是"星芒
   平台控制面"的一部分：它是收编前就已经独立运行在生产的宿主机数据面
   组件，本次任务只是把它的源码纳入版本管理、让配置可审阅，platform-api
   （真正的控制面）对它只有**只读挂载消费**这一种关系，不调用它、不管理
   它的生命周期、不共享故障域。`internal/platform/reqlogformat` 的包文档
   与 `cmd/reqlog-recorder` 的包文档都写了这条边界说明，供后续审阅时
   核对；如果这个边界判断不成立，需要另开 ADR 讨论，不是本片能自行拍板的
   事。
5. **文件后端的目录扫描未按时间过滤剪枝**：`ListRequests` 在给定
   `Since`/`Until` 时仍会扫描全部保留期内的按天目录（出于时区正确性考虑
   ——磁盘目录名按 CST 分天，过滤条件是 UTC，剪枝算错一格会静默漏数据，
   见 `file_client.go` 的注释），当前量级（约 13k 请求/日 × 30 天）下
   测得可接受，量级明显增长时可能需要补一版安全的按目录名剪枝。
6. **两个独立进程的保留期配置没有自动同步**：`cmd/reqlog-recorder` 的
   `--retention-days` 与 `connectors/reqlog` 文件后端的
   `FileConfig.RetentionDays`（当前平台侧未暴露对应环境变量，默认落到
   契约常量 30）是两处独立配置，运维如果只改了记录代理一侧，界面上显示
   的"只覆盖最近 N 天"会与实际保留期不一致。
7. **发现一个磁盘格式本身的缺口（非本次引入，收编时读源码发现）**：
   如果上游连接在响应头返回**之前**就失败，`ErrorHandler` 直接给客户端
   回 502，不经过 `ModifyResponse`/`teeBody`，这次请求**完全不会被记录**
   ——不是记一条 `status=0` 的记录。已写进
   `docs/runbooks/REQLOG-RECORDER.md`「已知限制」一节，供运维知悉这是
   一个盲区而不是意外发现的 bug。

## follow_ups

- 验收线在服务器上按 `docs/runbooks/REQLOG-RECORDER.md` 实际走一遍切换
  流程，把第 1 步的交叉编译产物、第 3 步的 `chgrp -R` 结果、第 8 步的
  真实数据验证结果补进一份 `docs/evidence/EV-<日期>-reqlog-file-cutover.md`
  （本片未创建这份证据文档，因为没有真实服务器可验证）。
- 评估是否需要为 `refreshTokenMap` 的 `docker exec` 依赖单开一个 ADR，
  把它纳入 ADR-014/ADR-018 的正式豁免范围或规划一条走 Connector 的替代
  路径（见 risks #1）。
- 若请求量级增长到需要按时间剪枝目录扫描，回来实现 risks #5 提到的安全
  剪枝（口径：目录名按 CST，过滤条件按 UTC，剪枝范围需要在两端各放宽
  一天才安全）。
- 考虑给 `cmd/platform-api` 加一个 `XM_REQLOG_RETENTION_DAYS`（或类似）
  环境变量，让文件后端上报的保留期天数可以显式对齐记录代理实际配置的
  `--retention-days`（见 risks #6），本次为保持改动面精简、严格对齐任务
  列出的环境变量清单而未添加。
- 前端未改动；若产品侧希望在请求详情页显式说明"渠道/上游/计费信息在这条
  数据源里不可用"（而不是显示空白），需要一次独立的前端小改动
  （contracts §10.2 已经记录了这条限制，前端目前的空值渲染方式未知，
  需要产品/前端确认是否需要专门的空态文案）。
