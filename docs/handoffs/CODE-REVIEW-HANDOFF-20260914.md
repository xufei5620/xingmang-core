# 代码收敛复审交接（2026-09-14）

负责人决定由另一位 AI 重新梳理代码及收敛修复。本次交付只补齐版本、已核实问题与证据摘要，没有修改业务代码、部署、重新处理死信或解冻账户。

**接手时从包含本文件的最新 `main` 建立工作分支。生产与 GitHub 的业务代码相同，但生产运行版本和后续文档提交要分开识别。此前发布门禁与30分钟观察通过，不代表后续用户故障已解决。**

## 1. 提交号与实际生产

| 项目 | 已核实值 |
|---|---|
| 生产代码 / CODE_HEAD | `a11d0b768119c7ae1c12e5dfc0c0e6953af005d7` |
| 上一文档提交 / 此次整理前 GitHub main | `35753ae247b4bf5f0f8171f3e5d3e137389241a4` |
| 两者差异 | 仅5个 Markdown：`UNIFIED-CUTOVER-RESULT.md`、`UNIFIED-CUTOVER-READINESS.md`、平台 `AGENTS.md`、`PROJECT-CONSTITUTION.md`、`GIT-WORKFLOW.md`；没有业务代码变更 |
| 本交接文件所在提交 | `git log -1 --format=%H -- docs/handoffs/CODE-REVIEW-HANDOFF-20260914.md`；避免把文件自身提交号递归写回文件 |
| 发布 manifest SHA-256 | `def6efe3b8adc2fb102fca4daf36fede490d31116f02546b58bb7b02cad0c171` |
| 生产 source / incoming 叶名 | `final-a11d0b768119-20260913-r1` |
| 生产发布来源 | 签名制品与 `/srv/xingmang-releases/final-a11d0b768119-20260913-r1`，不能用旧 checkout 的 HEAD 替代运行版本 |
| 统一栈 / 来源采集项目 | `xingmang-unified-a11d0b76` / `xingmang-unified-sources-a11d0b76` |
| 当前生产入口 | 管理端 `console.solov.cc`；客户端 `invoice.solov.cc` |
| 最近版本复核 | 2026-09-14 **10:00:08.100612–10:00:08.334876 UTC**，原生0；18个运行容器全部匹配原发布镜像，发布记录为 COMMITTED |
| SUB2API 实际二进制 | **0.2.4**，`5de5e2bed035d43591a2e10e51f420ef6a84eb98`；容器旧镜像标签不等于现运行二进制版本 |
| NewAPI 实际版本 | **v1.0.0-rc.30**，`27ff6a8767e728f879d52770c273d4f73214a430` |

原发布与门禁收据入口：[UNIFIED-CUTOVER-RESULT.md](UNIFIED-CUTOVER-RESULT.md)。后续只读结果的可携带摘要：[20260914-code-review-baseline.json](evidence/20260914-code-review-baseline.json)。摘要包含观察时间及本地原件哈希，不包含客户邮箱、凭据、个人金额、事件或账户标识。

## 2. 已确认但尚未修复的问题

| 项 | 已核实事实 | 代码定位（相对仓库根，行号基于上述基线） |
|---|---|---|
| 首屏失败显示零金额 | 金额摘要初始为 null；申请记录失败时摘要不更新，卡片用 `?? 0` 显示零。未读取成功不是业务返回零 | `invoice/web/src/App.tsx:228,666–680`；`invoice/web/src/lib/user-data-load.ts:119,218` |
| “等待首次同步”含义不准确 | 取 `max(funding_lots.observed_at)`，不是账号或余额实际同步时间；缺少资金批次即可显示等待首次同步 | `invoice/backend/internal/postgresstore/identity.go:955–995`；`invoice/web/src/lib/http-api.ts:1808` |
| 用户读取重复调用全局同步统计 | 首屏资金、资格摘要及申请列表补充资金请求会触发3次 SourceHealth；即使申请列表为空也再次读取资金列表 | `invoice/web/src/lib/http-api.ts:1650–1697`；`invoice/backend/internal/application/service.go:587,599,1294` |
| 同步统计扫描历史事件 | SourceHealth 的 JOIN 没有事件状态或时间预过滤。生产 EXPLAIN 为 `source_ingest_events` 顺序扫描，估算1,117,873行；单独实测2.1695秒成功 | `invoice/backend/internal/postgresstore/source_sync.go:1665–1693` |
| 死信造成具体账户冻结 | 当前16个死信事件均已尝试8次；13个 usage、3个 balances。4个SUB账户合计19条未关闭 EVENT_DEAD 冻结记录 | `invoice/backend/internal/application/source_processor.go:203–275`；`invoice/backend/internal/postgresstore/source_sync.go:889–990` |
| 错误原因在操作界面过于笼统 | 死信持久化错误统一为 PROJECTION_FAILED；日志可见13个事件 `conflict`、3个事件 `source fact event time/watermark is invalid`。需要暴露可理解的具体原因和处理路径 | `invoice/backend/internal/application/source_processor.go:52–58,108–115,258–262` |
| 管理员余额来源没有独立采集分支 | 已安装SUB credits函数不含 admin_balance 分支；New credits函数不含管理员 quota_add 日志分支。余额观察仍存在，不能据此说余额完全未采集 | `invoice/contracts/sub2api-economic-projection-grants.postgresql.sql:267–281`；`invoice/contracts/newapi-economic-projection-grants.postgresql.sql:231–241` |
| 有支付背景的兑换记录可能错归非现金 | SUB未关联本库支付单的余额兑换记录被归为bonus；New兑换记录也归bonus。缺本库支付关联不等于赠送，需要核对真正来源 | 同上 credits SQL；`invoice/backend/internal/postgresstore/consumption.go:2334–2355` |

“对账正常但不开票”和“余额真的不匹配”是两类问题：当前 `ADMIN/BONUS/REBATE/UNKNOWN_POSITIVE` credit 全部进入非现金池；只有已核定的现金资金批次产生可开金额。**只补一个 ADMIN 标签并不能落实管理员正常充值1:1可开。** 来源丢失的正差额可能被归为 UNKNOWN_POSITIVE，余额对平但仍不可开；负差额可能进入待核对状态。代码位置：`consumption.go:2288–2308,2334–2344,4620–4634,4843–4948`。

## 3. 不能当作已确定根因的部分

- 客户端“开票申请记录超时”可能来自申请列表请求，也可能来自其后顺序执行的资金列表请求。客户端超时是15秒；本次只读SQL计时为2.17秒，**没有复现当时15秒超时，也没有证明历史扫描是唯一根因**。不要用增加超时或重试代替定位。
- 负责人指出的再次冻结案例，已确认是一条消费事件反复报 `conflict`，8次后在2026-09-14 09:11:39 UTC形成EVENT_DEAD冻结。**具体冲突字段尚未确认**；`conflict` 是业务通用错误，不等于数据库锁冲突。锁忙、PG 40001/40P01已有独立重排分支。
- 三个时间错误在写账事务之前的 `validateFactMetadata` 触发。条件是零时间、事实时间晚于传入水位，或批次 `scan_ceiling_at` 超过首收 `observed_at` 300秒。旧事件重送保留首收观察时间；批次选择虽优先匹配时间，但仍允许回退到不匹配绑定。**还没有逐事件完成绑定和时间字段比对，不能宣称最终根因已证实。** 位置：`consumption.go:422–431`；`source_sync.go:450–480,703–768`。
- 截图客户案例当前唯一匹配、绑定verified，有4个funding_lot、0个申请、6条open freeze。但这是之后的数据库快照，**不是截图当时HTTP响应**；按真实external_account_id已查到资金观察时间，故不能把这张截图单独解释为“没有充值批次”。
- 4个冻结账户、19条freeze记录、16个dead事件是不同口径。不要相加，也不要把上游1663笔管理员正向调整等同于1663个故障账户。
- “来源同步可用”的绿色状态表示已圈定的死信没有阻断其他账户，不表示对应冻结账户已经恢复。

## 4. 真实余额来源与用户要求

以下业务途径按来源归并；数量来自2026-09-14 08:10–08:12 UTC的保留记录/配置快照，不能当作实时金额或每笔现金付款证明。

| 来源 | SUB2API | NewAPI |
|---|---|---|
| 在线支付充值 | 2741笔COMPLETED余额订单；另2笔退款申请 | 37笔成功钱包充值：Epay/支付宝36、Stripe1 |
| 余额兑换码/管理员代兑 | 4419笔正额已用记录：2743笔关联本库支付，1676笔未关联；在线支付内部生成的兑换记录必须去重 | 当前兑换码记录0笔 |
| 管理员余额调整 | 1663笔正差额；`admin_balance`也包含负差额 | 87笔add、10笔override；override不一定是增加；收款人位于审计参数，日志user_id是操作者 |
| 返利/邀请额度 | 59笔返利转入；计提与转入不能重复算充值 | 未查到奖励历史或剩余额度 |
| 注册优惠码奖励 | 已启用，23笔正额使用记录 | 不据此虚构同类来源 |
| 注册/首次绑定初始额度 | 邮箱注册0.5已启用，其余已查来源发放关闭 | 配置字符串0.1，但本版本用整数Atoi解析；未确认按0.1发放，奖励日志0 |
| 签到 | 本次未列为已核实来源 | 源码支持，记录0，未查到启用覆盖 |

套餐购买应另列为订阅权益，不算增加钱包；退款回补、预扣差额退回或冻结释放也不是新的充值。之前界面示例中的泛化“活动赠送”不作为业务依据。

负责人已要求：**管理员正常充值按USD 1＝CNY 1纳入；返利不纳入充值；套餐按购买成功的实际支付金额。** 当前实现与这项要求尚未完全对齐。必须区分“用户目标”“源码能力”“生产配置/记录”“已实现可开规则”，不能把UI原型当作实现或验收证据。

## 5. 接手范围与收敛方向

先按负责人新任务梳理，优先解决事件重复失败与恢复、读取超时和错误状态显示、真实来源与现金核算对应。恢复流程应明确哪笔记录、什么原因、如何复核；不能用直接删死信、清冻结、改钱包余额或无条件放开开票替代修复。

复用与代码/环境仍匹配的有效验证；按改动风险选择相关验证。不要为文档或低风险界面改动机械重建全部镜像、重跑迁移演练；涉及资金、身份、生产迁移的实际要求仍适用。不要再扩展到基础PG镜像CVE或无关功能。

本交接不授予新的生产写入或部署授权。保留用户改动、旧分支/工作树、失败证据与原数据；03-ai-manager和06-upstream源码保持只读，不访问旧盘路径，不读取密钥正文。需要用运行时上游版本时，区分历史契约快照和真实二进制版本。

本机原始只读证据位于 `G:/xingmang/logs/balance-source-audit-20260914/`，包括 `REPORT.md`、`invoice-account-diagnostics-01/RESULT.json`、`dead-and-timeout-diagnostics-01/RESULT.json`、`current-release-check-01/RESULT.json`。未将客户原始记录、错误日志正文、凭据路径或界面预览提交到仓库。
