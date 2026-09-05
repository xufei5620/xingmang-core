# 接码中心路线图（XM-SMS2 → XM-SMS5）

status: in-progress
branch: ai/claude/XM-CARD0-infini-connector
决策依据：`docs/adr/ADR-022-接码中心.md`（产品负责人 2026-09-06 拍板：自动路由、
先内部后外部但先立接入规范、转售预充值扣费、通知外发等另一条线的规范）

## 工作方式（产品负责人休息期间的自主推进，2026-09-06 起）

- **只提交到分支**，每个切片一个或多个提交，门禁全绿才提交。**不合验收线、不推
  远端、不碰生产**——这三件事等产品负责人回来看过再做。
- 每个切片独立可上线，做完更新本文件的状态与「验证」小节。
- 通知外发一律不做：告警条件落成内部事件与页面提示，外发等通知规范。
- 遇到需要产品负责人拍板的事（新的花钱路径、权限边界、数据删除），停下写在
  本文件「待决」一节，不猜。

## XM-SMS2 · 中心化与多上游骨架 — status: done（1–8 全部完成）

1. ~~改名「接码中心」~~ done（2d4b559）。
2. ~~供应商注册表~~ done：`internal/platform/sms/registry.go` 定义 `ProviderSpec`（ID、标签、
   能力集、凭据引用、构造）；`SupportsAction` 改由能力集推导；`AllProviders` 由
   注册表生成；`/sms/providers` 回 `capabilities` 数组（保留 `supports_lifecycle`
   一段时间给旧页面）；页面按能力渲染按钮。
3. ~~迁移 000041~~ done：去掉 `provider_status / sms_order / sms_resource / sms_operation /
   sms_code / sms_email` 六处对 provider 名字的 CHECK（写路径由注册表校验兜底）。
4. ~~统一号码状态~~ done：迁移 000042 加 `sms_resource.state`（waiting_code /
   code_received / finished / cancelled / expired）并按 Hero 状态码 + 本地已有码回填；
   `status` 列保留上游原话。`MapHeroStatus` 映射官方状态码，62 导入一律待收码；
   取到码 → 已收码、取消 / 完成成功 → 已取消 / 已完成（**压过适配器回读**，Hero
   setStatus 后 getStatus 可能还回旧值）；`EffectiveState(now)` 把「待收码但过了
   到期时间」算成已过期，服务端算好以 `effective_state` 回给页面。页面号码栏与
   详情头显示统一状态徽标，悬停看「上游状态：<原话>」；state 为空的旧数据退回
   显示原话。
5. ~~路由规则~~ done：迁移 000043 建 `sms.routing_rule`（环境 × 服务 × 国家唯一；
   providers text[] 保序；max_unit_price numeric、对外文本；CHECK 供应商非空、上限 > 0）。
   `ResolveRoute` 命中顺序：精确 > 服务通配国家 > 国家通配服务 > 全通配 > 装配顺序；
   人指定供应商就只用那家但上限仍按命中规则。`ValidateRoutingRule` 按注册表拒未装配 /
   不能买号 / 重复的供应商，上限只收正的十进制文本。Action `sms.routing.set`（enabled
   缺省 true，同键覆盖）/ `sms.routing.remove`，sms.manage、L1、只给人，领域错误翻成
   INVALID_PARAMS / PRECONDITION_FAILED（否则内核归一成「执行失败」看不出是哪个字段）。
   契约 `contracts/actions/sms.routing.*.v1.json`。读端点 `GET /sms/routing` 带
   `default_order`。页面新页签「路由规则」：加入 / 上移 / 移除定优先级、编辑回填、
   删除点两次、只列有购买能力的供应商、说明默认顺序。
6. ~~「要号」流程~~ done：`Service.RequestNumber` + Action `sms.number.request`
   （sms.purchase、L1、只给人；契约 `contracts/actions/sms.number.request.v1.json`）。
   服务 + 国家 + 数量（+ 可选指定供应商）→ `Route` → 逐家 `purchase`：明确失败
   （上游拒绝 / 关着 / 没验证 / 翻译不了 / 超上限）回落下一家，**每家最多一次**；
   **unknown 就停**（钱可能花了，needs_review）。幂等：每家的台账 ID =
   UUIDv5(request_id, provider)，重试同一个 request_id 是回放（先查台账，试过的
   不再打上游，成功的回同一批号）。翻译：Hero 服务原样、国家转数字、上限作
   maxPrice；62 按商品名（纯数字当平台 ID）+ 国家匹配，有货、上限内、最便宜，
   没价格不买。迁移 000044 `sms_resource.operation_id`（FK）记下买它的操作——
   回放与成本核算都靠它；新买的号从待收码起。页面：「要号」对话框（两步确认、
   自动 / 指定供应商、失败列每家原因、未知提示人工核对），要到号选中第一个，
   详情面板既有的 15 秒自动取码接手。
7. ~~定时作业~~ done：River 周期任务 `sms_probe`（第 11 个，10 分钟一轮，
   队列 maintenance）。`Service.ProbeOnce` 逐家：关着的**一个上游请求都不发**；
   开着的做一次只读连接测试（写 verified_at / last_error，失败不清旧的验证事实）；
   连接成功且有 CapBalance 的再抓一次余额，追加进迁移 000045 的
   `sms.balance_snapshot`（numeric、对外文本、币种空 = 上游没说）。只有落库失败
   才抛错让 River 重试，单家上游失败只记 warn——结论已经在 provider_status 里。
   开关跟 `XM_SMS_MODE` 走（无独立 ENABLED），节奏 `XM_SMS_PROBE_INTERVAL`；
   两者都补进 launch.yaml 的 worker 段与 .env.example。装配搬进领域包
   （`sms.ParseMode` / `sms.BuildProviders`），API 与 worker 共用一份——两份解析
   迟早分叉成「API 能买号、worker 不认识这家」。读端点 `GET /sms/balances`，
   供应商卡片显示「余额 x · 抓取于 …」（**必须带抓取时间**，它是快照不是实时值）。
8. ~~内部告警条件（不外发）~~ done：迁移 000046 建 `sms.alert_event`（按
   (环境, 指纹) 唯一）与 `sms.balance_threshold`（按家配、numeric、CHECK > 0）。
   `Service.EvaluateAlerts` 全量评估三条：余额低于阈值（十进制比较，没配阈值
   或还没巡到过就不报——「没有数据」不是「没有钱」）、unknown 待核对超过 30 分钟
   （critical）、租用号 1 小时内到期（只看 subtype=2 且还在等码的：普通激活号
   20 分钟过期是常态）。按指纹去重，条件消失自动收敛（不用人手动关）。
   Action `sms.alert.set_balance_threshold`（sms.manage、L1、只给人；留空 = 清掉，
   不是阈值为 0），契约同名。评估挂在巡检任务之后（余额刚刷新），
   **一次外发都没有**——通知器在这条链上一次都不会被调用，投递等通知规范。
   读端点 `GET /sms/alerts`（告警 + 阈值一次回），页面顶部红条按严重度排序、
   写明「不会外发」，供应商卡片给有余额能力的那家一个阈值输入框。

## XM-SMS3 · 成本核算 — status: done（1–3 全部完成）

1. ~~`sms.cost_event`~~ done：迁移 000047，由 `purchase` / `runLedger` /
   `ExecuteAction` 在**成功那一刻**写（买号、租用、延长、重激活、买邮箱、重下单；
   取消记负数退款）。按 (环境, 操作, 主体) 幂等，ON CONFLICT DO NOTHING——重放
   不改写已记的账。服务与国家落在事件上（统计要按服务聚合，事后 join 资源表会
   被取消 / 过期的号带偏）。**金额可为 NULL = 上游没说，不是 0**：62 买号只回
   订单 ID（留订单号等对账补，来源 pending_order_amount）、Hero 延长写体不回价格
   （upstream_silent）；补 0 会让这个月少算一笔，比缺一行更难发现。币种：62 记
   USD（ADR 口径，上游不回币种字段），Hero 活动不回币种留空、邮箱用官方 ISO
   数字码。失败与 unknown **一条都不记**——「可能花了」不能变成账面上的一笔。
   成本写失败只吞掉、不改操作状态：钱已经花了，翻成失败会诱使人再买一次，
   少掉的行由 #2 的余额对账兜底。
2. ~~与余额快照对账~~ done：`reconcileBalance` 取某家最近两张快照，比「余额差」
   与这段窗口（**左开右闭**，落在上一张快照那一刻的事件属于上一个窗口）内同币种
   的成本之和。差额超容差（`DefaultReconcileTolerance` = 0.05，各家自己的币种）
   且方向是「掉得比账本多」才报 `balance_drift`——正方向是充值，不是异常。
   三种不下结论的情形直接跳过：只有一张快照（没有窗口）、窗口里有金额未知的
   事件（我们自己的和就不完整，报差额等于报自己的无知）、窗口里混着别的币种
   （跨币种相减得到的数字什么都不是）。评估挂在既有的 EvaluateAlerts 里，
   共用同一套「算出该报的、把不该报的收敛掉」，页面红条直接显示。
   容差如果实际用起来太吵，下一步是做成 Action 配的（与余额阈值同形状）。
3. ~~统计页签~~ done：`AggregateCostsByDay` **在库里聚合**（几十万行明细不拉到
   浏览器里算），四个维度在同一行——嵌套结构导不出 CSV，也喂不了财务那边。
   读端点 `GET /sms/costs?from&to`（YYYY-MM-DD、UTC、两端含，一次最多 366 天，
   非法日期回 INVALID_PARAMS）。新页签「成本统计」：按币种分开的合计（**不折算**）、
   明细表、CSV 导出（RFC 4180 转义，服务代号是上游给的，保证不了不含逗号）。
   **金额未知的笔数单独显示并写明「合计是下限而不是实际花费」**——一份背后有
   二十笔金额不明的报表，那个数字会被当成实际花费。

## XM-SMS4 · 接入规范与内部接口 — status: todo

1. 规范文档 `docs/modules/sms/INTEGRATION.md`：机器身份与 API Key、幂等申请
   （request_id 由调用方生成）、状态机、取码、释放、配额与花费上限、错误码、
   **回调形状**（字段定稿，投递等通知规范）。
2. Action 放开 MACHINE 身份：`sms.number.request` / `sms.code.fetch` /
   `sms.resource.action` 允许机器调用，按 principal 计配额。
3. `sms.consumer_quota`（每个消费者的日配额与花费上限）+ Action + 后台页面。
4. 首个消费者未定：只做规范与骨架，不做任何具体对接。

## XM-SMS5 · 转售（预充值扣费）— status: todo（依赖平台用户 / 支付模块）

1. `sms.pricing_rule`（服务 × 国家 × 加价系数或固定加价）+ Action。
2. `sms.sale`（客户、资源、售价、币种、时间）；下单先扣预充值余额，失败回滚。
3. 门户页面复用平台用户模块的登录与余额。
4. 利润 = sale − cost_event，按币种分，进跨平台财务。

## 待决（需要产品负责人）

（暂无）

## 验证记录

- 2026-09-06 XM-SMS2 #1–#3：注册表测试钉住 ID 唯一 / 标签 / 凭据引用可解析 /
  构造函数存在，SupportsAction 逐动作对照能力集；真库测试
  `TestPgStoreProviderStatusAcceptsThirdProviderAfter000041` 直接写一个注册表里
  没有的供应商名进 provider_status——迁移前会撞 CHECK，迁移后成功，这才证明
  「接第三家不改迁移」成立；token 形状与未知供应商由代码拒绝（错误是
  ErrProviderUnknown 而不是 constraint）。cmd 层 buildSMSAdapter 改走
  spec.Build，凭据引用走 spec.CredentialRef。/sms/providers 多回 label 与
  capabilities，前端标签优先取服务端的。
- 2026-09-06 XM-SMS2 #4：`state_test.go` 钉住官方状态码逐个映射、EffectiveState
  只对待收码生效、取到码写 code_received、没取到不动、取消 / 完成写统一状态且
  不改原话、失败不动。其中 `TestCancelAndFinishWriteUnifiedState` 先红了一次——
  适配器回读的资源带 waiting_code，落库时把刚写的 cancelled 冲掉了；修法是同号
  回读时强制用我们的状态，换号时新号从 waiting_code 起，再在落库之后补写原号
  状态。真库 `TestPgStoreResourceStateRoundTrip`：不带 state 的同步保留原值、
  SetResourceState 生效、ListResources 带出、带 state 的同步覆盖（迁移 000042 在
  测试库上真跑过）。页面测试三条：统一状态徽标 + 悬停原话、effective_state 为
  expired 时显示已过期、无 state 的旧数据退回原话。门禁：go vet / go test -p 1
  ./...（含真库）/ check-governance / pnpm -r typecheck / pnpm -r test
  （admin-web 1540 用例）全绿。
- 2026-09-06 XM-SMS2 #5：`routing_test.go` 先红（符号未定义）后绿：命中顺序七个
  用例（含「服务规则压过国家规则」「停用不算」「人指定只用那家但上限仍按规则」）、
  无规则回装配顺序、校验九种非法形状 + 未装配供应商、同键覆盖同一条且服务小写
  去空白、删两次第二次 NotFound、Route 走库里规则、两个 Action 注册（L1 / manage）
  与参数通过 Schema、enabled 缺省 true。真库 `TestPgStoreRoutingRuleRoundTrip`：
  同键覆盖、numeric 文本进出、text[] 保序、非 uuid 的 ID 是 NotFound、空供应商
  被 CHECK 挡（迁移 000043 在测试库上真跑过）。页面测试七条（先红：模块不存在）：
  标签显示与默认顺序、按加入顺序提交、上移 / 移除 / 不可重复加入、删除点两次、
  编辑回填、停用徽标、只列可买号的供应商——其中一条因表头「单价上限」经
  aria-labelledby 也算标签而撞名，改表单 aria-label 为「路由单价上限」。门禁：
  go vet / go test -p 1 ./...（含真库）/ check-governance 0 / pnpm -r typecheck /
  pnpm -r test（admin-web 1547 用例）全绿。
- 2026-09-06 XM-SMS2 #6：`request_test.go` 先红（符号未定义）后绿，十条：62 明确
  拒绝回落 Hero 且每家只打一次、unknown 停下不碰下一家、全拒绝为 failed、指定
  供应商只试那家、62 选上限内最便宜且数量原样 / 超上限不买并说明、Hero 入参
  （服务小写、国家数字、上限作 maxPrice）/ 非数字国家跳过、关着的跳过并写「未
  启用」、七种非法输入、同 request_id 回放不打上游且回同一批号、Action 注册为
  sms.purchase + 非法输入翻 INVALID_PARAMS。脚本改 Purchase 时漏了一处返回值、
  把 OperationID 加到了 Operation 而不是 Resource（正则命中了前一个结构体）——
  编译器抓住，修正后一次全绿。真库 `TestPgStoreResourceOperationLink`：FK 指向
  sms_operation、同步不冲掉、按操作列出、指向不存在的操作被 FK 挡（迁移 000044
  在测试库上真跑过）。页面 `SMSRequestDialog.test.tsx` 五条（先红：模块不存在，
  再红：缺 QueryClientProvider）：两步确认默认不带 provider、指定供应商带
  provider、全部失败列每家原因且不选号、未知提示人工核对并显示操作 ID、无可用
  供应商禁用。门禁：go vet / go test -p 1 ./...（含真库）/ check-governance 0 /
  pnpm -r typecheck / pnpm -r test（admin-web 1552 用例）全绿。
- 2026-09-06 XM-SMS2 #7：`probe_test.go` 六条先红后绿：每家验证 + 只有 Hero 落
  快照、关着的 testCalls==0、连接失败不读余额且不清 verified_at、余额失败保留
  验证结论且不落快照、两轮追加两条（最新一条是第二轮）、落库失败抛错。
  `jobs/sms_probe_test.go` 五条：跑一轮、单家上游失败不算任务失败、基础设施
  错误算失败、未绑定 prober 失败、InsertOpts 用 maintenance 队列 + args/queue/
  period 唯一，外加部署态时刻表认得这个任务（XM_SMS_MODE=fake|real + 15m）。
  `build_test.go` 四条钉住 ParseMode 只收 off/fake/real 且错误信息逐字列出合法值
  （XM_CARDS_MODE 写成 live 让生产下线四分钟的教训）。真库
  `TestPgStoreBalanceSnapshotAppendsAndReadsLatest`：三条追加、DISTINCT ON 回
  「那一行」（币种与时间同源）、每家只回最新（迁移 000045 在测试库上真跑过）。
  钉死的作业契约按规矩两边一起改：`cluster-jobs.v1.json` 加第 11 条，
  `job_manifest_test.go` 的「Exactly Ten」改成 Eleven 并把 sms_probe 加进 want。
  compose 门禁如期抓到 XM_SMS_PROBE_INTERVAL 没进 launch.yaml（正是它存在的理由），
  补上后归零。**顺带发现的既有小分叉**（未修，不在本切片范围）：
  `XM_CARDS_SYNC_INTERVAL` 被部署态时刻表解析，但 cmd/platform-worker/config.go
  从没读它——那张表会显示一个 worker 实际不用的周期。门禁：go vet / go test -p 1
  ./...（含真库）/ check-governance 0 / pnpm -r typecheck / pnpm -r test
  （admin-web 1554 用例）全绿。
- 2026-09-06 XM-SMS2 #8：`alerts_test.go` 八条先红后绿：没配阈值不报余额、
  十进制边界（4.999999 报 / 5.00 与 5.000001 不报 / 0 报）、空值清阈值且非法值
  与未装配供应商被拒、超期 unknown 报 critical 而新鲜的与已结的不报、只提醒
  还在等码的租用号（普通激活号与已取消的租用号都不报）、反复评估只有一条事件
  且条件消失自动收敛、**通知器零调用**（不外发这条纪律用断言钉住，不是靠注释）。
  真库三条：指纹去重时开着的保留 first_seen_at、收敛后同指纹再发生会重开并重置
  起始时间、阈值 upsert / CHECK 0 / 幂等清除、两条评估查询各自只回该回的行
  （迁移 000046 在测试库上真跑过）。**真库测试抓到一个真 bug**：
  `ResolveAlertEventsNotIn` 传 nil 切片到 Postgres 是 NULL，而
  `NOT (x = ANY(NULL))` 是 NULL，于是「这一轮一条都没在报」时旧红条一条都收敛
  不掉——恰恰是最该全部收敛的那一轮；改成传空数组。jobs 侧：SMSProber 接口加
  EvaluateAlerts，巡检之后紧接着评估（余额刚刷新），评估失败让任务失败。
  页面测试三条：红条按严重度排（critical 在前）、无告警不显示、只有有余额能力
  的那家才有阈值输入框。门禁：go vet / go test -p 1 ./...（含真库）/
  check-governance 0 / pnpm -r typecheck / pnpm -r test（admin-web 1557 用例）全绿。
- 2026-09-06 XM-SMS3 #1：`cost_test.go` 十条先红后绿：Hero 逐个报价一号一条
  （带服务 / 国家 / 操作 ID / 主体 / 来源）、62 只回订单号时金额留空且来源是
  pending_order_amount 并留下订单号、失败与 unknown 一条不记、(操作, 主体) 幂等、
  租用记 rent、取消记 -0.35 且来源写明按原价退、延长记一条金额未知、取码不记、
  金额十进制原样保留且发生时间用本次操作的时钟。真库
  `TestPgStoreCostEventsAreIdempotentAndAllowUnknownAmount`：三条插入后重放插 0 条、
  NULL 金额读回是空串而不是 0、负数原样、按供应商筛（迁移 000047 在测试库上真
  跑过）。runLedger 里顺带补了一处：落库后把带本地 ID 的资源 / 邮箱副本留下——
  成本事件的主体必须是本地 ID，不是上游 ID。门禁：go vet / go test -p 1 ./...
  （含真库）/ check-governance 0 / pnpm -r typecheck / pnpm -r test 全绿。
- 2026-09-06 XM-SMS3 #2：`reconcile_test.go` 八条先红后绿：对得上不报、多扣 2.00
  报且摘要给出差额、充值（余额变多）不报、容差内的 0.02 不报、窗口含未知金额
  不下结论、只有一张快照不报、混币种不下结论、退款按负数抵消后对得上。真库
  `TestPgStoreReconcileQueries`：最近两条快照新的在前且只回本家、窗口左开右闭
  （边界那条 5.00 不算进 (t0,t1]）、按币种分组、未知金额计入笔数但不进和、
  别家的不算。金额一路走 big.Rat 不过 float。门禁：go vet / go test -p 1 ./...
  （含真库）/ check-governance 0 / pnpm -r typecheck / pnpm -r test 全绿。
- 2026-09-06 XM-SMS3 #3：真库 `TestPgStoreAggregateCostsByDay`：跨日切分正确
  （23:30 与次日 00:30 分属两天）、退款负数抵消后为 0、未知金额计入笔数但不进和、
  币种分组（空 / 840 / USD 各自成行）、窗口外不算、按日期倒序。页面
  `SMSCostPanel.test.tsx` 七条：合计按币种分开、有未知金额时写明「合计是下限」、
  没有就不写、无数据时导出按钮禁用、CSV 表头 / 行格式 / 结尾换行、逗号与引号
  按 RFC 4180 转义、空币种单独成组。其中一条先红：合计条上也有「N 笔金额未知」，
  与提醒句撞名——改成按「合计是下限」这句独有的话定位。门禁：go vet /
  go test -p 1 ./...（含真库）/ check-governance 0 / pnpm -r typecheck /
  pnpm -r test（admin-web 1564 用例）全绿。
