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

## XM-SMS2 · 中心化与多上游骨架 — status: todo

1. 改名「接码中心」：导航、页面标题、文档、导航防漂移测试。
2. 供应商注册表：`internal/platform/sms/registry.go` 定义 `ProviderSpec`（ID、标签、
   能力集、凭据引用、构造）；`SupportsAction` 改由能力集推导；`AllProviders` 由
   注册表生成；`/sms/providers` 回 `capabilities` 数组（保留 `supports_lifecycle`
   一段时间给旧页面）；页面按能力渲染按钮。
3. 迁移 000041：去掉 `provider_status / sms_order / sms_resource / sms_operation /
   sms_code / sms_email` 六处对 provider 名字的 CHECK（写路径由注册表校验兜底）。
4. 统一号码状态：`sms_resource.state`（待收码 / 已收码 / 已完成 / 已取消 / 已过期），
   `status` 列保留上游原话；各适配器给出映射；页面显示统一状态、悬停看原话。
5. 路由规则：`sms.routing_rule`（服务、国家、供应商优先级列表、单价上限、启用），
   Action `sms.routing.set` / `sms.routing.remove`（sms.manage），后台页面。
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

（每个切片完成后填）
