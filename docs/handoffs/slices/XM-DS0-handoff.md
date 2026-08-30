sprint-section: 7

# XM-DS0 · 指标原始样本与日粒度降采样设计交接

## status

READY

本片是 **docs-only** 交接：把已合入 release 的 DS0 设计稿和实施计划的范围、决策
依据、审批边界和后续验收门固化为可审阅的 Handoff。READY 仅表示文档交接完成，**不
表示任何 DS1–DS4 实施授权**，也不要求或触发 Docker 栈部署。

## branch / commit / base

- branch: `ai/codex/XM-DS0-handoff`
- base: `release/v0.1-launch@c657a02f336fe6ea0c971bcd9b9b8ab3daa83bd3`
- worktree: `K:/星芒统一控制平台/wt-xmDS0-handoff`
- source design: `docs/superpowers/specs/2026-08-28-metric-downsampling-design.md`
  (`a87fcfd8104237ab29f55ef80e5a258526168157`)
- source plan: `docs/superpowers/plans/2026-08-28-metric-downsampling.md`
  (`a87fcfd8104237ab29f55ef80e5a258526168157`)
- handoff commit: see READY line after this file is committed

## scope / summary

DS0 固化的目标是：原始指标样本至少保留 90 天，同时为后续可验证的 UTC 日粒度历史
提供设计边界。设计采用版本化逐指标 policy、immutable raw、可合并 daily accumulator、
逐样本 append-only receipt 和 per-stream state。关键语义如下：

- registry 当前 15 个指标逐项冻结 value kind、primary 指针、单位、scale、币种和
  quality 语义；snapshot 不做同日求和，金额/计数全程整数或 fixed-point。
- v1 日桶固定 UTC `[00:00Z, next 00:00Z)`；history 继续按 `(synced_at,id)` 排序，
  `id` 只用于稳定排序，不能当提交水位。
- full、partial、failed 三类质量桶互斥统计；failed 携带的旧值不进入 numeric，
  coverage 未知时返回 null 和 reason，不补零或伪造完整度。
- exactly-once 由 per-stream state 行锁加 receipt 唯一键保证；低 ID 晚提交、重试和
  commit-result 不确定都必须在下一轮可发现且不重复计数。
- legacy `hours=1..168` 保持 raw-only；新的 `raw/day/auto` range contract 需另批
  API 契约，`auto` 只在当前 UTC 日读 raw、此前完整日读 daily。
- raw 删除必须逐行具备 matching receipt、daily/state parity、backup/kill-switch 等
  证明；DS0 不创建迁移、worker、API、配置，也不执行回填或删除。

## authority / approval basis

1. 设计稿 §1、§13 明确 DS0 `docs/design = GO`，并将 DS1–DS4 拆成独立审批门。
2. 该设计提交 `a87fcfd` 已是当前 `release/v0.1-launch@c657a02` 的祖先；按
   `CODEX-PROJECT-HANDOFF.md` 的“规格文档合入 release 即批准”规则，DS0 文档制品可
   交接。原设计文件顶部的“待共同审批”文字属于设计形成时的历史状态；本 Handoff
   不把它扩大解释为实施批准。
3. 开工前已 `git fetch --prune origin` 并读取 `docs/handoffs/ACCEPTANCE-LOG.md`
   最新记录。当前队列记录为 `RL1-impl(RL0) → DS0 → R210 → R215 → DBR0 → AUD2`；
   没有 DS0 的 `MERGED` 或 `REJECT` 信号需要处理。
4. `docs/change-requests/CR-0002-invoice-readonly-interface.md` 当前仍为“待开票线
   确认”，因此 invoice 两个候选指标只保留为 **CR-gated** 形状；本片不生成 policy
   bytes、不修改开票仓库，也不把 CR-0002 状态推断为批准。

## authorization boundary / gates

| 后续片 | 本 Handoff 允许的范围 | 必须先满足的门 | 当前状态 |
| --- | --- | --- | --- |
| DS1 | policy/schema/纯 accumulator 的 disposable PG 设计实现 | CR-0002 冻结；15-key exact policy；fresh release 上的 exact migration diff；pinned PG18 且缺配置即 Fatal | **条件 GO**，未授权生产或 staging |
| DS2 | receipt exactly-once、late/backfill、手工 CLI、默认关闭的独立 worker | DS1 合入并重新审读；仅 disposable PG；不接 API、staging 或 delete | **条件 GO** |
| DS3 | raw/day/auto repository、HTTP/cursor/coverage 与 legacy UI 诚实性 | 独立 API/响应契约审批；range UI 另立 DS3-UI 审批 | **NO-GO** |
| DS4 | aggregate-and-prune、DBR、River 多副本、backup/restore、staging soak | DBR/R2-10、备份恢复、staging、raw-delete 各自证据和人工批准；`XM_METRIC_RAW_DELETE_ENABLED` 仍 false | **硬 NO-GO** |

以下事项属于 §7.1 五类阻塞，若未来触及必须写 `BLOCKED:`，不能由 DS0 READY 继承：
真实凭据/生产或第三方系统、宪法变更、采购或外部基础设施、删除既有能力/改变上线口径、
以及开票线 CR 级契约变更。其余技术决策按设计稿和最小权限原则写入对应片的
`decisions`，不在本片等待口头确认。

## decisions

- 以 15-key registry 的精确集合为 policy 边界；不得通过删掉 invoice 行来绕过
  CR-0002。未知 key、version、kind、pointer、scale、币种或 cadence 一律 fail closed。
- daily 只保存明确 policy/hash/cadence 的可审计结果；`sum_mode=forbidden` 的
  snapshot 不相加，mixed currency 的 numeric 聚合为 null。
- receipt 是逐 raw sample 的证据，不与 raw 建阻止删除的 FK；删除资格必须等 DS4
  在同一事务完成 aggregate + receipt + proof 后再单独获批。
- 不引入 Timescale 或其它数据库扩展；不把现有 platform-worker/collector 身份扩大
  为 rollup/delete 身份，DBR 权限和 CredentialRef 由独立片决定。
- DS3 的 range API 与现有 hours API 并存；未显式 opt-in 的调用不返回 daily，前端
  历史页不因 DS0 自动增加范围控件。

## grid → source / status mapping

| 设计格 | 权威来源 | 诚实状态 |
| --- | --- | --- |
| raw 样本、状态、watermark、source | `ops.metric_observation_sample`（现有表；DS1 才补 policy/cadence 字段） | 当前 raw 可读；降采样尚未实现 |
| 日桶 aggregate / quality / coverage | 设计中的 `ops.metric_observation_daily` | DS0 仅设计；无运行时数据 |
| exactly-once 与 late sample | 设计中的 `metric_rollup_receipt` + `metric_rollup_state` | DS2 才能在 disposable PG 验证 |
| hot/cold history | 现有 `GET /api/v1/metrics/history` + 未来 opt-in range contract | legacy 保持；DS3 API 仍 NO-GO |
| retention / raw DELETE | 现有 retention job（尚无 rollup proof） | DS4 硬 NO-GO；两个开关默认 false |
| invoice 两项指标 | `CR-0002` 及其 connector 冻结字段 | CR 待确认；不生成 v1 policy bytes |

## files_changed

- `docs/handoffs/slices/XM-DS0-handoff.md`（本片唯一新增文件）

未修改 DS0 spec/plan、registry、迁移、查询、Go/TypeScript、Compose、worker 或共享栈。

## tests_run

- `git diff --check` — PASS。
- Markdown structural check（UTF-8 可读、无行尾空白、标题层级与 fenced code 成对）— PASS。
- `bash scripts/check-governance.sh` — PASS。
- `gitleaks git --redact --no-banner --log-opts=<base>..HEAD` — 待本片提交后运行并记录实际范围；
  不添加 allowlist、不扫描或输出任何凭据。
- 本片无源码变更；Go、前端、Docker 与运行时门禁不适用，未触碰共享栈。

## tests_not_run / risks

- 未运行 Go/前端全量测试、Storybook、Compose 部署或 PostgreSQL 集成测试：本片没有
  运行时代码，且 DS0 明确禁止实现这些内容。
- 未接触真实 Sub2API/NewAPI、开票系统、凭据、生产数据库或外部对象存储。
- 设计稿中的 15-key policy、CR-0002 invoice 字段、迁移字节和 DBR grant 仍需在各自
  后续片重新从最新 release 计算；任何基线移动都使旧 digest/迁移号失效。
- 容量增长、daily row bytes 和 cadence 不能沿用旧 README 估算；实施前需获批只读
  snapshot 并记录 p50/p95/p99 与 relation/index size。

## follow_ups

1. 验收线审读本 Handoff 并在合入后追加 `MERGED <sha>`；本片没有服务需要部署。
2. 按队列先完成 RL1-impl，再从最新 release 新建 DS1；DS1 开工前重新读取 Sprint §7
   与 ACCEPTANCE-LOG，重新确认 CR-0002 和 15-key exact policy 门。
3. DS1/DS2/DS3/DS4 每片独立写 Handoff 和证据；任何批准不跨片继承，尤其不能把
   DS0 READY 解释为 API、DBR、staging 或 raw DELETE 授权。
