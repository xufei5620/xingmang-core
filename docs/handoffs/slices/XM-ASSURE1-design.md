# XM-ASSURE1：渠道主动探测（检测任务）· 设计交接

## status

READY（设计稿，待产品负责人拍板；不含任何实现代码，见下方 not_run）。

## branch / commit

- branch: `ai/claude/XM-ASSURE1-design`
- worktree: `K:/星芒统一控制平台/wt-xmASSURE1D`
- base: `release/v0.1-launch` @ `531e071`
- 提交（`git log --oneline release/v0.1-launch..HEAD`，按时间顺序）：
  1. `docs(adr): add ADR-019 active probe channel decision`
  2. `docs(specs): add XM-ASSURE1 active probes design spec`
  3. `docs(roadmap): add XM-ASSURE1 slice sequence`
  4. 本文件作为第四个提交单独加入（团队约定 Handoff 最后提交，先例见
     XM-ASSURE0）。

## summary

本片是纯设计交付，回答 XM-ASSURE0 交接文档 follow_ups 里明确留给下一片的
问题："探测该怎么定性、Kill Switch 粒度多细、结果怎么存、频率与预算怎么定"。
产出三份文档：

1. `docs/adr/ADR-019-渠道主动探测通道.md`——决策与理由的权威来源。
2. `docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md`——
   数据模型、Action 参数、River 任务、Kill Switch 语义、Query、UI 状态、
   威胁模型、测试矩阵。
3. `docs/roadmap/XM-ASSURE1-active-probes-slices.md`——三个实现切片
   （core/ui/real）的排序与依赖。

### 核心决策（供负责人快速核对）

- **探测的定性**：平台用专用探测 API Key 调用 Sub2API/NewAPI 自己面向
  付费用户的 OpenAI 兼容推理端点，从不直连上游厂商、从不碰 admin API——是
  平台自己的健康自检，不是"代表用户发业务请求"，因此不违反宪法第 5 条。
  这是一条 ADR-004/ADR-018 都没预见过的第三种出站通道，ADR-019 是它的权威
  来源。
- **风险等级偏离了原始派工的假设**：派工要求"划为 L2 Action"，我核实代码
  后发现 `internal/platform/action/risk.go` 的 `RequiresAdvancedControls()`
  让 Foundation-A 内核对 L2+ 一律硬拒绝（`kernel.go:156-160`），且
  `contracts/actions/*.json` 里当前**唯一能真正执行**的 Action 全部是
  L0/L1——仓库里的三个 L2/L3 声明（`registry.connector.create` 等）都在
  `docs/modules/action/PERMISSIONS.md` 里标注"❌ 需 Foundation-B"，是冻结
  等待接线的空壳，不是能跑但要审批的东西。若按原计划定 L2，XM-ASSURE1 连
  fake 模式验证都做不了，必须先等 XM-0030（Foundation-B，目前"设计稿，待
  拍板"）落地，与"先 fake 模式起步"的路线矛盾。因此我把
  `assurance.probe.run@1` 改定为 **L1**，用 Kill Switch + 预算硬顶 + 并发闸
  + 冷却这些 L1 允许的"按需幂等"手段兜底真实风险，并在 ADR-019 决策·三里
  完整写明了推理链条。这是本片相对原始派工最大的一处偏离，现在就说清楚：
  **这个判断需要产品负责人明确认可**（ADR 文末"待拍板问题"#1/#2）；如果
  负责人坚持 L2，XM-ASSURE1-core 的可交付范围会退化成"只有 declare/cancel
  可用，run 要等 Foundation-B"，需要另外评估路线。
- **Kill Switch 是两层**：平台级（`core.connector_config.probe_enabled`，
  新 Action `assurance.probe.kill_switch.set@1` 单独授权，不与
  `connector.config.set@1` 共用权限点）+ 全局环境变量
  `XM_ASSURE_PROBE_ENABLED`，与仓库既有的 `XM_CONNECTOR_PROBE_ENABLED` 等
  先例同构。
- **预算**：固定小 Prompt 模板（不接受自由文本）+ 每次 `max_tokens` 硬上限
  （≤512）+ 每平台每日探测次数硬顶（业务日边界重置）+ 同平台并发 1 + 触发
  冷却 + 失败不自动重试（`MaxAttempts=1`，避免对已产生费用的失败探测重复
  扣费）。刻意不依赖 R2-15 的 `SourceBudgetAuthority`（那套机制本身还是
  NO-GO，只有设计文档获批），本片预算是自建的简单计数器。
- **结果存储**：新 schema `assurance`（`probe_declaration`/`probe_run`/
  `probe_result`），与 `ops.metric_observation` 完全分离的命名空间——直接
  回应 ASSURE0 follow_ups 的明确警告。表结构上就不留"原始模型响应文本"字段，
  只落结构化 verdict，见规格文档 §7.1 的威胁模型推理。
- **与 ASSURE0 历史记录的关系**：同一个"历史记录"子页签渲染两张独立卡片
  （被动聚合近 7 天，ASSURE0 原样不动；主动检测历史，本片新增），不合并
  成一张表，避免数据来源被误读。

## files_changed

新增（全部为文档，无代码）：

- `docs/adr/ADR-019-渠道主动探测通道.md`
- `docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md`
- `docs/roadmap/XM-ASSURE1-active-probes-slices.md`
- `docs/handoffs/slices/XM-ASSURE1-design.md`（本文件）

未修改任何既有文件（本片不触碰 `docs/modules/action/PERMISSIONS.md`——那张
表记录的是**已注册**的 Action，`assurance.probe.*` 尚未注册任何代码，提前
往那张表里加行会造成"这个 Action 已经存在"的错误印象，留给 XM-ASSURE1-core
在真正注册代码时更新）。

## tests_run

无（纯文档设计片，无代码可测）。已跑：

```
bash scripts/check-governance.sh
```

见下方结果。

## tests_not_run

- 无代码，因此没有 `go test`/`pnpm test`/浏览器验证可跑。
- ADR-019 与规格文档里的 SQL/Go 结构均为**设计稿**，未经编译器或
  `sqlc`/`golangci-lint` 验证，实现时可能需要调整字段名/类型（文档已明确
  声明"不是冻结契约"）。

## not_run

- **未与产品负责人核对任何一条"待拍板问题"**：ADR-019 与规格文档标注的
  "提议"/"设计稿"状态是真实状态，不是形式用语。尤其是风险等级判断
  （L1 vs L2）与探测账号/预算上限两项，需要负责人明确回应后才能进入
  XM-ASSURE1-core。
- **未创建 `contracts/actions/assurance.probe.*.v1.json`**：按文档治理"文档
  修改与代码同 PR"的要求，这些契约文件计划随 XM-ASSURE1-core 的实现代码
  一起提交，不在本设计片提前落地一个没有对应代码的"契约"。ADR-019"边界与
  后果"一节记录了这个判断，供验收线核对是否认可这个处理方式。
- **未评估探测对 Sub2API/NewAPI 自身速率限制/风控规则的影响**：规格文档
  §7.3 把"上游侧影响"标记为需要负责人在开通探测账号时一并考虑的开放问题，
  本片没有能力代为评估（不掌握两个被管平台自己的风控实现）。
- **未量化"每平台每日探测预算"的具体数字**：规格文档给出的是机制（硬顶 +
  超出即拒绝并可见），不是数值——数值由负责人在 XM-ASSURE1-real 开工前
  给出（ADR-019 待拍板问题 #3）。

## risks

1. **风险等级判断（L1）是本片对原始派工的实质性偏离，尚未获得确认**——
   见 summary 一节的完整推理。如果负责人不认可，ADR-019 与规格文档里所有
   依赖"L1 可执行"的设计（尤其是 XM-ASSURE1-core 的 fake 模式立即可用）
   都需要重新评估，可能变成"declare/cancel 先行，run 等 Foundation-B"的
   缩小范围方案。
2. **`assurance.probe_run` 的并发闸不能用 River `UniqueOpts` 实现**（规格
   文档 §3.1 已详述原因：`RunID` 每次不同，参数级去重锁不住"同平台同时只
   跑一个"这个约束），必须在 Action Handler 与 Job 执行两处都做应用层查询
   校验。这是实现时最容易被简化掉的一处，标记为高风险点供
   XM-ASSURE1-core 的代码审查重点关注。
3. **固定 Prompt 模板集合（`prompt_template_key` 的四个枚举值）目前只有
   原型的任务名和一句话断言方式描述，没有具体的 Prompt 文本、断言阈值
   （如"基准题集"的具体三道题和评分标准）**——这些内容涉及"探测在检验
   什么"的产品判断，本片作为纯架构/流程设计，把具体内容留给
   XM-ASSURE1-core 实现时与负责人商定，规格文档 §1.2.1 只锁定了机制（固定
   模板、不接受自由文本），没有锁定内容本身。
4. **本片没有评估探测流量是否会被 Sub2API/NewAPI 自己的异常检测/限流
   误判为攻击流量**——理论上一个专用账号的低频小请求不太可能触发风控，
   但没有真实环境数据支撑这个判断，列为风险供 XM-ASSURE1-real 阶段留意。

## follow_ups

- **XM-ASSURE1-core 开工前**：产品负责人需要回应 ADR-019 文末"待拍板问题"
  全部五条，尤其是 #1（L1 判断）；若认可，可直接按规格文档实现；若不认可，
  需要先修订 ADR-019 再排期。
- **探测 Prompt 模板内容**：`model_fingerprint`/`benchmark_set`/
  `context_length`/`min_viable_request` 四个模板各自的具体 Prompt 文本、
  预期片段、评分基准，需要在 XM-ASSURE1-core 实现前由负责人或该切片的
  执行者与负责人商定，不应该由代理自行编造"标准答案"。
- **探测账号来源**：Sub2API/NewAPI 各自用哪个真实账号做探测、账号本身在
  两个平台里怎么开通，是负责人需要在 XM-ASSURE1-real 之前完成的账号侧
  动作，规格文档与本片都无法替代这一步。
- **每平台每日探测预算的数值**：机制已设计好，数值等负责人在看过声明的
  渠道/模型数量级后给出，建议先给一个保守的小数字（如个位数）作为
  XM-ASSURE1-real 首次验证的起点，之后再按实际需要调整。
- **定时调度（`schedule_cron`）默认是否开启**：规格文档 §9 的
  XM-ASSURE1-real 明确排除了"默认开启定时调度"，把这个决定留给负责人在
  看过至少一轮按需触发的真实数据之后再做——避免探测切片一上线就自动产生
  持续的真实费用。

## 开放问题（供负责人逐条答复）

1. `assurance.probe.run@1` 定为 L1（而非派工原文假设的 L2）是否被接受？
   不接受的话，XM-ASSURE1-core 的范围需要重新讨论。
2. Foundation-B/XM-0030 落地后，`probe.run` 是否应该改判 L2、要求每次人工
   审批？本设计建议永久留在 L1，但这是产品判断，需要负责人明确表态（可以
   现在答，也可以留到 XM-0030 真正落地时再决定）。
3. 每平台每日探测预算的具体次数上限是多少？
4. Sub2API、NewAPI 两个平台，先做哪一个的真实模式验证（XM-ASSURE1-real）？
   建议只选一个先跑通。
5. 探测是否允许定时调度，还是先只做按需触发？（机制两者都支持，是否默认
   开启定时调度是运营决定）
6. 探测专用账号具体怎么在 Sub2API/NewAPI 里开通（谁去开、开成什么形态、
   有没有专门的"内部账号"标签能力）？这一步完全在平台代码控制范围之外，
   需要负责人牵头。
