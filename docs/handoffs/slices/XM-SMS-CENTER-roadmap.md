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

## XM-SMS2 · 中心化与多上游骨架 — status: in-progress（1–5 done）

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
6. 「要号」流程：Action `sms.number.request`（sms.purchase）——服务 + 国家 + 数量
   （+ 可选指定供应商）→ 按规则选供应商 → 买 → 落资源 → 自动取码由页面驱动
   （每 15 秒）。失败按规则回落下一家，**每家最多试一次**，全部失败落 failed。
7. 定时作业（platform-worker）：每家供应商连接测试 + 余额快照（写
   `sms.balance_snapshot`，阶段 3 用）。
8. 内部告警条件（不外发）：余额低于阈值（阈值走 Action 配）、unknown 待核对超过
   N 分钟、租用号 1 小时内到期——落 `sms.alert_event`，页面红条。

## XM-SMS3 · 成本核算 — status: todo

1. `sms.cost_event`：由 runLedger / Purchase 在成功那一刻写入（买号、租用、延长、
   重激活、买邮箱、重下单；Hero 取消的退款为负）。金额 numeric，对外文本；
   币种按上游（62 为 USD，Hero 为 ISO 数字码）。
2. 与余额快照对账：同一供应商同一币种，「快照差」与「事件和」的差额超阈值
   落 alert_event。
3. 统计页签：服务端整表聚合，按供应商 × 币种 × 服务 × 天；分币种不折算；
   CSV 导出。事实表形状 = 跨平台财务的输入。

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
