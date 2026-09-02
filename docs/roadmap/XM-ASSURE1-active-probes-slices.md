# XM-ASSURE1 切片路线：渠道主动探测（检测任务）

本文件是 `docs/adr/ADR-019-渠道主动探测通道.md` 与
`docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md` 的执行
排序索引，不重复两者的设计内容——细节以 ADR 与规格文档本体为准。格式沿用
`docs/roadmap/CR-0006-console-auth-slices.md` 开创的先例（仓库文档治理没有
独立"路线图"类别，`docs/roadmap/` 是为跨多个切片的设计供后续参照复用的形式）。

## 切片一览

| 切片 | 依赖 | 交付内容 |
|---|---|---|
| [XM-ASSURE1-core](#xm-assure1-core) | 无（ADR-019 与配套规格已冻结即可开工） | `assurance` schema 迁移、四个 L1 Action、`assurance_probe` River 任务、fake 模式探测客户端 |
| [XM-ASSURE1-ui](#xm-assure1-ui) | XM-ASSURE1-core 已合入 | 检测任务/历史记录子页签接入真实 Query/Action，移除原型遗留 warnbar |
| [XM-ASSURE1-real](#xm-assure1-real) | XM-ASSURE1-ui 已合入生产；产品负责人已指定探测账号并完成 §"开放问题"里的登记动作 | 真实模式的一次性验证证据（非新增代码为主） |

责任人标注沿用 CR-0005/CR-0006 先例："平台线"当前由验收线执行（不预设某个
具体代理工具）。

## 发布排序

```text
XM-ASSURE1-core ──▶ XM-ASSURE1-ui ──▶ [fake 模式生产可用，检测任务真实可用] ──▶ 负责人开通探测账号 ──▶ XM-ASSURE1-real
```

- **XM-ASSURE1-core 必须先于 XM-ASSURE1-ui**：前端要接的是真实 Query/Action，
  没有后端就没有接口可接。
- **XM-ASSURE1-ui 合入 ≠ 真实探测可用**：两片合入后，检测任务在 fake 模式下
  完整可用（原型的"这是目标布局"占位彻底消失），但任何平台在真实模式下的
  `probe_enabled` 默认是 `false`——真实探测在这个时间点仍是关闭的，这是
  ADR-019 决策·四要求的默认值，不是遗漏。
- **XM-ASSURE1-real 的开工前提是产品负责人已完成账号层面的准备工作**（在
  Sub2API 或 NewAPI 自己的产品里开通一个专用探测账号并拿到 Key），这件事
  不在任何一个代码切片的范围内，因为平台代码不能替负责人决定"用哪个账号
  的钱做探测"。哪个平台先做真实模式验证（Sub2API 还是 NewAPI）、每日预算
  上限具体是多少，由负责人在开工前答复，见交接文档"开放问题"。
- 与 CR-0006 路线同一条纪律：**"负责人已完成前置准备"不是一个可交付的
  切片**，是一个门槛，达到与否由验收线在 `docs/handoffs/ACCEPTANCE-LOG.md`
  记录判断依据。

## 各切片摘要

### XM-ASSURE1-core

- **前提**：无（ADR-019 状态为"提议"，需要产品负责人先对 ADR 与规格文档的
  "待拍板问题"给出答复；若负责人对风险等级判断有异议，本切片的 Action
  定义需要相应调整，不应该在悬而未决时抢先按设计稿实现）。
- **范围**：`db/migrations` 新增 `assurance` schema 与
  `core.connector_config` 两列；`internal/platform/assurance`（新包，参照
  `internal/platform/channelassurance` 的分层习惯：Store + Service + Action
  Handler）；`internal/platform/jobs/assurance_probe.go`（River Worker，
  fake 模式探测客户端 + real 模式 HTTP 客户端代码，后者只单测不接真实厂商）；
  `contracts/actions/assurance.probe.declare.v1.json`、
  `.cancel.v1.json`、`.run.v1.json`、`.kill_switch.set.v1.json`
  （随本切片代码一起提交，见 ADR-019"边界与后果"一节对"为什么本设计片
  不提前放这些文件"的说明——它们属于实现切片，不属于设计切片）。
- **不做**：不碰任何前端代码；不对任何真实厂商发起过请求；不修改
  `docs/modules/action/PERMISSIONS.md` 之外的现有 Action。

### XM-ASSURE1-ui

- **前提**：XM-ASSURE1-core 已合入（Query/Action 端点存在）。
- **范围**：`web/apps/admin-web/src/components/PlatformAssurancePanel.tsx`
  的检测任务/历史记录两个子页签从蓝图占位换成真实数据渲染（规格文档 §6 的
  UI 状态表）；`web/apps/admin-web/src/api/assurance.ts` 追加检测任务/探测
  历史两个客户端方法；真实浏览器证据（fake 模式）：声明一个检测任务、
  发起一次检测、查看历史记录里的新纪录、触发至少一种"未启用/预算耗尽"类
  拒绝状态的 UI 呈现。
- **不做**：不涉及真实厂商凭据；不新增后端 Action（如需要在联调中调整
  Action 参数形状，走对 XM-ASSURE1-core 的补丁而非在本片新增）。

### XM-ASSURE1-real

- **前提**：XM-ASSURE1-ui 已在生产可用；产品负责人已经：(a) 在选定平台
  （建议先做一个平台，不必两个同时开）的真实产品里开通专用探测账号并生成
  API Key；(b) 明确该平台的每日探测预算上限数值；(c) 明确探测覆盖哪些渠道/
  模型（不必是全部，可以先选一小部分验证）。
- **范围**：把 (a) 的 Key 通过 `assurance.probe.kill_switch.set@1` 登记为
  `probe_credential_ref`；服务器环境变量设置
  `XM_ASSURE_PROBE_ENABLED=true`；对已登记的渠道/模型跑一次真实探测；人工
  核对探测记录里的延迟、状态、`verdict` 是否与手动用同一账号发一次请求的
  结果吻合；产出 `docs/evidence/EV-<日期>-assure1-real-verify.md`。
- **不做**：不在本片扩大到"两个平台同时开真实探测"；不在本片实现定时调度
  的生产默认开启（`schedule_cron` 是否默认排期、排多频繁，是负责人在看过
  至少一轮按需触发的真实数据后再决定的独立判断，不由本片自动升级）。

## 相关文档

- `docs/adr/ADR-019-渠道主动探测通道.md` —— 本路线所服务的架构决策。
- `docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md` ——
  数据模型、Action 契约草稿、River 任务、Query、威胁模型、测试矩阵。
- `docs/handoffs/slices/XM-ASSURE1-design.md` —— 本次设计交付的交接记录。
- `docs/handoffs/slices/XM-ASSURE0-passive-assurance.md` —— 被动指标切片，
  "渠道保障"页面的姊妹数据源。
