# XM-ASSURE0：渠道保障 · 被动指标（第一片）

## status

READY（待验收线审读、复跑并人工合入）。所有本地门禁绿；未连接真实生产
reqlog 数据验证过实际量级下的表现（见 not_run）。

## branch / commit

- branch: `ai/claude/XM-ASSURE0-passive-assurance`
- worktree: `K:/星芒统一控制平台/wt-xmASSURE0`
- base: `release/v0.1-launch` @ `01d5eee`（含 XM-USERS-V2-real-detail）
- 六个提交（`git log --oneline release/v0.1-launch..HEAD`，均以时间顺序）:
  1. `7ec394c` feat(reqlog): passive assurance aggregation over the request log index
  2. `c3c5587` feat(channelassurance): platform-side query for passive assurance
  3. `b8505e0` feat(httpapi): wire GET .../assurance/overview and .../assurance/history
  4. `65ca086` feat(platform-api): construct channel assurance only in reqlog file mode
  5. `f73a946` feat(admin-web): assurance API client
  6. `40aba46` feat(admin-web): wire real passive data into the 渠道保障 tab
- 本文件作为第七个提交单独加入（团队约定 Handoff 最后提交）。

`git diff release/v0.1-launch..HEAD` 会额外显示 `docs/handoffs/ACCEPTANCE-LOG.md`
少一行——**本片没有碰这个文件**（六个提交里没有一个涉及它）；那一行是
`release/v0.1-launch` 在我分支之后被其他并行切片继续推进时新增的日志条目，
纯粹是与一个持续前移的共享分支比较时的正常分叉，不是本片的改动，核对方式
见下方命令：

```
git merge-base HEAD release/v0.1-launch   # 01d5eee，与我的分支点一致，没有漂移
grep -c "XM-USERS-V2-real-detail" docs/handoffs/ACCEPTANCE-LOG.md  # 本片工作树里是 0
```

## 我对任务范围做的一处偏离（先说清楚，再看细节）

派工原文要求"渠道详情页的『渠道保障记录』卡片读同一个 Query，针对该渠道"。
开工核对数据源后发现这个前提**不成立**：请求审计（reqlog）落盘的
`Record` 结构体（`internal/platform/reqlogformat/record.go`）**没有 channel/
upstream 字段**——不是当前实现没读出来，是这条数据源从写入那一刻起就不
采集这个维度（逐字段核对 `reqlogger.go` 确认，`contracts/connectors/
reqlog.read.v1.md` §10.1 第 21 项也记录了同一个结论）。据此，任何"这一条
渠道的 24h 成功率"都是编造，没有真实数据支撑。

**处置**：渠道详情页的"渠道保障"卡片保持"未接入"，但把原因从笼统占位
改成具体、准确的解释，并加一个链接指向该平台"渠道保障 · 保障概览"的
真实数据（按平台/模型，不按渠道）。这比字面执行派工指令更诚实——按字面
做只能是编数据或者悄悄把"渠道"偷换成"模型"却不说明，两者都违反宪法
12 条。已在浏览器截图 `06-channel-detail-full.png` 里验证这个链接确实能跳
到真实数据页。

## summary

被动保障指标（保障概览 + 历史记录两个子页签）从请求审计索引
（`connectors/reqlog` 的 `index.jsonl`，与"请求详情"页同一份数据源、同一个
`request.read` 权限）现算聚合，不依赖任何新的采集任务或 rollup。

### 数据来源与诚实边界（逐条引用）

- **可以做**：按平台（source）、按窗口（15 分钟/1 小时/24 小时，或历史的
  固定近 7 天）、按模型（`Record.Model`，index 行自带）聚合出请求量、状态
  分类（2xx/4xx/5xx/连接中断/其他）、总耗时与首字节延迟的 p50/p95/p99——
  这些字段全部在 `reqlogformat.Record` 里已经存在（`connectors/reqlog/
  metrics.go`、`internal/platform/reqlogformat/record.go`）。
- **做不到、也没有假装做到**：按渠道/上游拆分。磁盘格式没有这两个字段
  （见上一节的引用），每次响应都显式带 `channel_breakdown_supported:
  false` + 具体原因（`connectors/reqlog` 的
  `ChannelBreakdownUnsupportedReason` 常量），前端原样转述这句话，不重新
  编一份措辞、也不用 model 或别的字段冒充渠道。
- **首字节延迟的"已测量"判据**：沿用 `Record.MeasuredTTFB()`
  （`RespSize>0` 反推 TTFB 是否被真实写过）——这是 XM-REQLOG-MERGE 已经
  定好的口径，本片直接复用，没有重新发明。
- **检测任务（主动探测）明确不在本片范围**：声明 → 探针 → 结论需要一个
  真正发起请求的 Action，且必须带 Kill Switch（宪法 26 条），设计与实现
  划给 XM-ASSURE1（见文末"follow_ups"里的设计笔记）。子页签保留原型的
  列结构，一行检测结果都不显示。

### 实现分层

1. **`connectors/reqlog/assurance.go`**（新增）：`MetricsReader` 新增
   `WindowAssurance`（任选窗口）与 `HistoryDays`（固定近 7 天，与
   `TrendDays` 同一个排列口径）两个方法，复用已有的 `scanDay`；
   `spanningDayDirs` 从 `WindowStats` 里抽出来的共享辅助（纯重构，行为
   不变，既有测试全部原样通过）。延迟百分位用整数最近秩算法
   （`computePercentiles`，不经过浮点）；按 model 分组的累积器
   （`assuranceAccumulator`）供两个方法共用同一份"拿到一条记录怎么记账"
   的逻辑。
2. **`internal/platform/channelassurance`**（新包）：`Service.Overview`/
   `Service.History` 包一层平台解析（复用
   `requestlog.ResolvePlatform`，为此把这个函数从 `requestlog` 包私有
   方法导出，两个 Query 共享同一份"未知/不覆盖的平台一律 404"判定）与
   错误翻译，不重复审计（这批数据是聚合，泄漏面等同"这个平台今天调用多
   不多"，不含正文，不需要审计事件）。
3. **`internal/platform/httpapi/assurance.go`**：两个只读端点的 DTO 转换
   与新鲜度合成（`freshnessFromAssurance`：`IsPartial` 取自
   `MissingDays>0`，这是一次实时读取而不是周期观测，因此没有"上游多久
   没更新"这个维度，只有"这次聚合覆盖全不全"）。路由挂载与
   `RequestLogs` 同一条纪律：`d.ChannelAssurance == nil` 时两个端点整组
   不挂载（404），不会带着 nil 依赖硬跑成 500。
4. **`cmd/platform-api`**：`newChannelAssuranceService` **只在
   `XM_REQLOG_MODE=file` 时构造**——与 `reqlog_metrics` 周期任务
   （`internal/platform/jobs/reqlog_metrics.go`）遇到 fake/real 时的降级
   逻辑同一个先例：fake 是纯内存 ReadClient，没有 `index.jsonl` 可扫；
   real 的控制台 API 形状仍未核实（§5 未决冲突）。file 模式与"请求详情"
   页读的是同一份 `XM_REQLOG_DATA_DIR`。
5. **`web/apps/admin-web/src/api/assurance.ts`**：客户端，沿用
   `api/requests.ts` 的既有约定（`FeatureNotMountedError`、防御性数值
   解析、`sampleCount==0 ⇔ 三个百分位皆 null` 的一致性自检）。
6. **`web/apps/admin-web/src/components/PlatformAssurancePanel.tsx`**：
   保障概览（窗口选择器 + 四格真实 KPI + 按模型 DataTableV2）、历史记录
   （近 7 天逐日表格，含目录缺失徽章）、检测任务（蓝图态文案改指向
   XM-ASSURE1 + Kill Switch，不再是"M1.5"这种过期占位语）。
   `assuranceSubTab` 签名加了 `platform` 参数——Sub2API/NewAPI 共用同一套
   组件，已用真实浏览器验证两个平台数据独立正确渲染（见截图 01/05）。
7. **`web/apps/admin-web/src/pages/ChannelDetailPage.tsx`**：如上一节
   "偏离"所述。
8. **`web/apps/admin-web/src/pages/PlatformDetailPage.tsx`**：
   `refreshAll` 追加失效 `["assurance"]` 查询前缀，与页面其余指标一起
   刷新。

## files_changed

新增：

- `connectors/reqlog/assurance.go`、`connectors/reqlog/assurance_test.go`
- `internal/platform/channelassurance/service.go`、
  `internal/platform/channelassurance/service_test.go`
- `internal/platform/httpapi/assurance.go`、
  `internal/platform/httpapi/assurance_test.go`
- `web/apps/admin-web/src/api/assurance.ts`
- `docs/handoffs/slices/XM-ASSURE0-passive-assurance.md`（本文件）

修改：

- `connectors/reqlog/metrics.go`（`spanningDayDirs` 重构抽出，`WindowStats`
  行为不变）
- `internal/platform/requestlog/service.go`（导出 `ResolvePlatform`）
- `internal/platform/httpapi/router.go`（`Deps.ChannelAssurance` + 两条
  路由）
- `cmd/platform-api/reqlog.go`（`newChannelAssuranceService`、
  `channelAssuranceOrNil`）
- `cmd/platform-api/main.go`（装配 + 拒绝启动分支）
- `web/apps/admin-web/src/components/PlatformAssurancePanel.tsx`（全面
  重写：保障概览/历史记录接真实数据，检测任务文案更新）
- `web/apps/admin-web/src/pages/PlatformDetailPage.tsx`（`assuranceSubTab`
  调用点补 platform 参数，`refreshAll` 追加失效）
- `web/apps/admin-web/src/pages/ChannelDetailPage.tsx`（"渠道保障"卡片
  改成诚实说明 + 指路链接）
- `web/apps/admin-web/src/router.test.tsx`（重写"渠道保障页签"describe
  块：删掉过期的纯蓝图断言，新增真实数据/窗口切换/历史覆盖率断言）

## tests_run

Go（在仓库根目录）：

```
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go build ./...
  — PASS（全仓库）
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go vet ./...
  — PASS（无输出）
"$(go env GOROOT)/bin/gofmt" -l <本片新增/改动的全部 .go 文件>
  — 第一轮命中 2 个文件（对齐空白），已用 `go fmt` 修正，复核 GOFMT_CLEAN
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy go test -p 1 -count=1 ./...
  — 连续跑了 4 轮：第 1 轮全绿；第 2、3 轮各命中一个**与本片无关包**的
    httptest 用例（`cmd/reqlog-recorder` 的
    `TestMakeProxyRecordsNonStreamingChatRequest`/
    `TestMakeProxyPassesThroughUninterestingPaths`，随后
    `internal/platform/alerts` 的 `TestTelegramNotifierSendsMessage`），
    单独 `-run` 重跑均秒级通过；第 4 轮再次全绿（`EXIT=0`）。这两个包
    本片一行代码都没碰（`connectors/reqlog`、`internal/platform/
    channelassurance`、`internal/platform/httpapi`、
    `internal/platform/requestlog`、`cmd/platform-api` 之外未改任何
    `.go` 文件），符合派工里明确预警的"loopback 今天偶发抖动，隔离重跑
    过的 httptest 超时是环境问题"——已按环境问题记录，不是本片改动引入
    的回归，证据是上面这串独立重跑记录。
```

前端（`web/` 目录，worktree 用镜像脚本补齐 `node_modules`，命令带
`--config.verify-deps-before-run=false` 跳过 pnpm 依赖校验；三个 pnpm 门禁
未并发跑）：

```
pnpm --config.verify-deps-before-run=false -r run typecheck
  — PASS（5 个前端 workspace 包全部 tsc --noEmit 无输出）
pnpm --config.verify-deps-before-run=false -r run test
  — PASS：design-tokens 10、ui-primitives 16、ui-admin 253、admin-web
    1389，共 1668 个用例全绿
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
  — PASS（"Storybook build completed successfully"；本片未新增 ui-admin/
    ui-primitives 包组件，没有新故事要写）
bash scripts/check-governance.sh
  — PASS（exit 0，无输出）
gitleaks git --log-opts="release/v0.1-launch..HEAD"
  — PASS（6 commits scanned，"no leaks found"）
```

新增测试覆盖清单：

- `connectors/reqlog/assurance_test.go`：窗口预设解析（合法三档 + 拒绝
  非法值）、百分位算法（空样本→nil、单样本退化、100 个已知值核对
  nearest-rank 下标公式）、状态分类归类、TTFB "已测量 vs 未测量"区分
  （`RespSize>0`）、按模型分组（含空模型名不丢、分组之和等于窗口总数）、
  超上限截断、窗口跨越缺失目录时 `MissingDays`/`IsPartial()`、
  `HistoryDays` 固定长度/升序/以今天结尾/缺目录标记、渠道拆分维度恒
  `false` 且原因非空、context 取消报错。
- `internal/platform/channelassurance/service_test.go`：nil reader 拒绝
  构造、时钟可注入并透传、未知平台短路（不调用 reader）+ 返回
  `NotRegistered`、reader 错误翻译成 `EXECUTION_FAILED`、History 固定
  7 天且不接受参数化。
- `internal/platform/httpapi/assurance_test.go`：两个端点各自的
  scope 校验（无 `request.read` 时 403，且文案指名缺的 scope）、window
  参数校验（三档 200，非法值 400）、响应形状（`channel_breakdown_
  supported` 恒 false、`window` 原样回显、`freshness.state` 非空）、
  Service 层错误正确映射到 HTTP 状态码、依赖为 nil 时两个端点整组
  404（未挂载）。
- `web/apps/admin-web/src/router.test.tsx`（"渠道保障页签"
  describe 块）：三个子页签逐字 + 默认落在保障概览带窗口选择器；真实
  KPI/按模型明细渲染且渠道拆分原因原样转述；点击窗口按钮触发带新
  `window` 参数的真实重新请求；检测任务子页签仍是纯蓝图但文案指名
  XM-ASSURE1/Kill Switch，且原型样例的假探测结果不出现；历史记录显示
  真实近 7 天聚合，缺目录的天数显式标"目录缺失"徽章与覆盖率合计。

真实浏览器实测（`node node_modules/vite/bin/vite.js --port 5183
--strictPort --config vite.dev.local.config.ts`——临时配置文件只放宽
`server.fs.strict`，验证完已删除，未提交；手写 Node mock API 服务器,
`127.0.0.1:8080`，未提交；1440×1000 桌面视口，Playwright MCP 驱动）：

- `docs/evidence/screens/XM-ASSURE0/01-sub2api-model-overview.png`——
  Sub2API 保障概览默认态（1 小时窗口）：四格真实 KPI（请求量 842、成功率
  96.2% 附失败构成、总耗时/首字节延迟的 P50/P95/P99）、渠道拆分限制
  说明条、按模型明细表（三行，含一行空模型名正确显示"(未知模型)"）、
  底部新鲜度徽章 + 覆盖率说明
- `docs/evidence/screens/XM-ASSURE0/02-sub2api-model-overview-24h.png`——
  点击"24 小时"按钮后：按钮状态切到 `pressed`、KPI 卡片"窗口"文案与表格
  caption 同步更新、`数据时间` 时间戳变化（证明发生了真实的带新参数重
  请求，不是前端本地假切换）
- `docs/evidence/screens/XM-ASSURE0/03-sub2api-model-history.png`——
  历史记录近 7 天表格：5 天"完整"+ 2 天"目录缺失"徽章，页脚"近 7 天中有
  2 天索引目录缺失"汇总正确
- `docs/evidence/screens/XM-ASSURE0/04-sub2api-model-probes.png`——检测
  任务：顶部说明条明确点名 XM-ASSURE1 与 Kill Switch 要求，列结构蓝图
  保留，空态文案不冒充已上线
- `docs/evidence/screens/XM-ASSURE0/05-newapi-model-overview.png`——NewAPI
  同一套组件独立正确渲染（gpt-4o/gpt-4o-mini 真实数据），验证两平台
  共用组件参数化正确、无串数据
- `docs/evidence/screens/XM-ASSURE0/06-channel-detail-full.png`——渠道
  详情页"渠道保障"卡片：诚实未接入 + 指路链接，点击链接实测跳转到
  `?tab=model&sub=overview` 且渲染真实数据（本文件"偏离"一节的证据）

过程中真实浏览器测试本身抓到一处 mock 数据的疏漏（不是应用代码问题）：
历史记录的 mock 响应第一版里 `models[]` 只给了 `model`/`request_count`
两个字段，缺 `status_classes`/`duration_ms`/`ttfb_ms`，被前端防御性解析
按设计拒绝（"渠道保障 models[].status_classes.success 格式异常"）——这
正好反向验证了 `api/assurance.ts` 的解析器按预期 fail closed，不会把
半真半假的数据渲染出来；补全 mock 数据后复验通过。

## not_run

- **未连接真实生产环境**：没有 SSH 到服务器，没有对着真实
  `/root/reqlog/data`（容器内 `/var/lib/xm/reqlog`）验证聚合结果与真实
  请求量级下的表现。`XM_REQLOG_MODE` 在生产当前是 `file`（据
  ACCEPTANCE-LOG 最近记录），理论上接入后端点会自动挂载，但未实测。
- **未做真实量级下的性能测算**：`WindowAssurance`/`HistoryDays` 每次调用
  都会重新扫描对应的 `index.jsonl`（与 `WindowStats`/`TrendDays` 同一条
  已知取舍，理由见 `connectors/reqlog/metrics.go` 的既有注释：按目录名
  剪枝在 CST/UTC 换算边界容易漏数据）。24 小时窗口最多扫 2 个日目录、
  历史记录扫 7 个，按项目记忆记录的量级（约 13k 请求/日）预期可接受，
  未拿真实文件量测过单次请求耗时。
- **未对真实数据的 model 取值分布验证 `MaxAssuranceModelRows`（50）是否
  够用**：真实环境的 model 字段是自由文本（上游透传），如果实际种类明显
  超过 50，多出的会被截断且标 `models_truncated`，但没有真实样本验证这
  个上限是否常态化触顶。
- **`docker build`（真正构建镜像）未做**：只做了 `go build`/`go vet`/
  `go test`，未跑 `deploy/docker/go.Dockerfile` 的多阶段构建。
- **未新增/修改任何 `ops.metric_observation` 观测或周期任务**：本片是
  纯粹的实时聚合 Query，不落库、不参与降采样策略，因此
  `internal/platform/ops` 的 rollup/freshness 白名单没有改动，也不需要
  改。

## risks

1. **`MaxAssuranceModelRows`（50）与展示上限的取舍未经真实分布验证**——
   见上方 not_run。如果真实环境 model 种类经常超过 50，`models_
   truncated` 会频繁触发，届时前端"只显示请求数最多的前 N 个"这句说明
   仍然诚实，但可能需要重新评估上限或加分页。
2. **`WindowAssurance`/`HistoryDays` 每次调用全量扫描对应日目录，无缓存**
   ——与既有的 `WindowStats`/`TrendDays` 同一条已知取舍，量级增长后可能
   需要一起解决（见 `docs/handoffs/slices/XM-REQLOG-METRICS.md` 的 risks
   #2，本片延续同一条记录，未新增风险，只是新增了两个同样受影响的调用
   点）。
3. **`channelassurance.Service` 与 `requestlog.Service` 现在共享
   `ResolvePlatform`，但各自独立构造**——两者读的是同一份磁盘数据（同一个
   `XM_REQLOG_DATA_DIR`），本片没有把它们合并成一个服务/一次装配，是
   刻意保持关注点分离（见 `channelassurance` 包文档的说明），但意味着
   两者的启动配置校验各自独立执行，理论上存在"其中一个配对了、另一个
   配错"的组合（虽然当前两者读同一个 `cfg.Reqlog`，正常配置路径下不会
   发生）。
4. **`success_rate`/百分位这类"展示态"计算里出现了一次浮点除法**
   （`successRateText`，`(success/total*100).toFixed(1)`）——刻意与宪法
   13 条"金额禁止 float"分开评估：这条铁律管的是货币，这里算的是延迟/
   计数的**只读展示**百分比，不进入任何持久化、传输或再计算（与
   `ChannelDetailPage.tsx` 里已有的 `(row.today.successRate *
   100).toFixed(1)` 是同一类用法），已在代码注释里写清楚这条区分，供
   验收线复核这个判断是否成立。

## follow_ups

### XM-ASSURE1（主动探测）设计笔记，供下一片直接起步

本片"检测任务"子页签只保留了原型的列结构和一句诚实的未接入说明，没有
设计任何后端。以下是开工时顺手记下的几个真正的设计难点，不是完整方案:

- **执行体必须是一个真正发起出站请求的 Action，且要过 Connector 的四道
  只读闸吗？** 不完全适用——探测本身就是要"发一个真实请求去看模型是否
  正常响应"，这与 ADR-018 的"只读闸"精神（平台不该代表用户发起业务请求）
  存在张力，需要单独判断这属于"平台自身的健康探测"（更像 `ops` 域的
  探针）还是"代表用户的业务调用"（那就该完全避免）。这条判断没有做,
  留给 XM-ASSURE1 开工时先定性。
- **Kill Switch 的粒度**：全局一个开关，还是按平台/按渠道/按探测策略
  分别可关？宪法 26 条只要求"可以停用"，没规定粒度。建议至少做到
  按平台可关（探测 Sub2API 出问题时不必连 NewAPI 一起停），因为两个
  平台的探测频率/目标模型集合本就该独立配置。
- **探测结果的存储**：需要一张新表（或复用 `ops.metric_observation` 的
  变体），且必须能回答"这次探测是主动发起的，不是被动统计"——不能与
  本片的被动指标共用同一套 `metric_key` 命名空间，否则概览页会分不清
  一个数字是"用户真实调用统计出来的"还是"我们主动打的探测"，这是两种
  完全不同性质的可信度。
- **频率与预算**：原型文案"支持不定时抽检，也保证最低检测频率"暗示
  两种模式并存（计划任务 + 按需触发），后者更接近一个 L1/L2 Action;
  前者是新的周期任务，需要预算评估（每轮探测多少个渠道 × 多少个模型,
  参考 `today-stats` 预算 40 账号/次踩过的坑，见
  `docs/handoffs/slices/XM-CHAN-WIRE0-catalog-wire.md` 的 follow_ups）。
- **落点位置**：`s2/model` 的"检测任务"子页签，以及渠道详情页"渠道保障"
  卡片里"最近模型检测结论"等字段（本片已经在 `ChannelDetailPage.tsx` 里
  留好了这几个字段位置，全部标未接入并注明"等 XM-ASSURE1"）。

### 其余

- **渠道级别的被动指标依然是空白**：如果产品侧后续判定"按渠道拆分"是
  必须的能力，唯一的路径是让请求审计的写侧（`cmd/reqlog-recorder`）
  开始采集路由信息并写进 `Record`——这是磁盘格式变更，影响老数据兼容性,
  需要独立评估，不是消费侧能补的。
- **保障概览/历史记录目前完全独立于 `ops.metric_observation` 体系**（不
  经过 rollup、不受降采样策略约束、没有长跨度趋势）。如果未来需要更长
  的历史（超过 7 天），需要专门设计一版 rollup policy——与
  `XM-REQLOG-METRICS` 的 risks #1（`success_rate_24h`/`trend_7d` 已经
  因为形状不兼容被排除在 `RollupPolicy` 之外）是同一类未解决问题，建议
  合并成一次设计任务。
- **验收线在服务器上对着真实 `/root/reqlog/data` 跑一轮，核对保障概览/
  历史记录的实际数值与页面渲染**，并把结果补进一份
  `docs/evidence/EV-<日期>-assure0-verify.md`（本片未创建，因为没有真实
  服务器可验证）。
