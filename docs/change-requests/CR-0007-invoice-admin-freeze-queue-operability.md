# CR-0007：开票管理端冻结队列可操作性（账号标识 + 嵌入模式提示可见性）

> 状态：**proposed**。问题一、二为 P1（阻塞开票资格冻结队列的正常人工复核）；问题三为 P2（信息性，改善抽屉文案，不改变服务端判定逻辑）。
> 依据：验收线 2026-09-03 对生产开票资格冻结队列做只读复核时的直接发现，已记入 `docs/handoffs/ACCEPTANCE-LOG.md` 当日"开票冻结队列复核"条目（83 条待处理记录，队列不显示账号标识→待开 CR）；问题二另有 2026-09-02 19:16Z 边缘代理五次解冻请求全部收到 503 的记录佐证。

## 发起方
验收线（生产只读复核开票资格冻结队列时发现）。

## 接收方
开票线（invoice-system，三个问题的改动均在此）；平台线（xingmang-platform，仅当问题二的实现选择涉及调整 iframe 高度/postMessage 策略时才涉及，见"变更范围"）。

## 目标资源
- 资格冻结只读队列 `GET /api/v1/admin/eligibility-freezes`（`backend/internal/httpapi/operations.go` 的 `listEligibilityFreezes`/`eligibilityFreezeDTO`；`backend/internal/postgresstore/eligibility_operations.go` 的 `eligibilityFreezeSelect`/`EligibilityFreeze`/`EligibilityFreezePageQuery`）。
- 资格冻结解除接口 `POST /api/v1/admin/eligibility-freezes/{id}/resolve`（同文件 `resolveEligibilityFreeze`/`ResolveEligibilityFreeze`/`assertSourceFreshTx`）。
- 开票管理端前端队列页与抽屉（`web/src/App.tsx` 的 `EligibilityFreezesPage`/`EligibilityFreezeDrawer`；`web/src/lib/http-api.ts` 的 `BackendEligibilityFreeze`/`mapEligibilityFreeze`；`web/src/types.ts` 的 `EligibilityFreeze`；`web/src/styles.css` 的 `.toast`）与嵌入模式判定（`web/src/lib/embedded-admin-scope.ts`）。
- 星芒控制台侧开票嵌入面板与 iframe 承载壳（`web/apps/admin-web/src/components/InvoiceConsolePanel.tsx`；`web/packages/ui-admin/src/EmbeddedConsoleFrame.tsx`），仅问题二可能涉及。
- 文档 `docs/ELIGIBILITY-OPERATIONS.md`。

## 背景/问题
资格冻结队列是开票系统在检测到来源数据异常（九类原因之一，如未知负余额、迟到事件、水位回退）时自动创建的安全阻断记录；能否解除完全由服务端依据五条来源数据流（identities/payments/usage/credits/balances）新鲜度、投影任务、最新对账结论、退款暴露判定，人工只能提交加密证据与说明，不能覆盖判定。CR-0005/CR-0006 已把该队列以 iframe 嵌入星芒控制台三处入口。验收线 2026-09-03 复核生产队列（当时 83 条待处理）时发现三个问题：

**问题一（P1）：队列不显示"冻结的是谁"。** 列表与抽屉只有来源类型/名称、冻结原因、范围（固定显示"整个平台账号"）、状态与时间，没有任何可辨识具体账号的字段。83 条记录必须逐条查库才能对应到具体上游用户。

**问题二（P1）：嵌入模式下操作结果不可见。** 控制台把开票管理端 iframe 高度撑到与内容等高（最高 4000px），可能明显超过浏览器视口；开票前端的提示条用 `position: fixed` 定位在**该 iframe 自己文档**的右下角，而不是浏览器视口——iframe 被撑高后，这个"文档右下角"可能落在当前视口滚动范围之外。一次解冻被拒绝时，操作员在可见区域内看不到任何反馈，只感觉"点了没反应"。

**问题三（P2，信息性）：拒绝原因对操作员不透明，且这一层在服务端和前端都已被抹平。** `ResolveEligibilityFreeze` 内部对退款暴露（四种子情形之一）、投影任务未清、对账结论未匹配三类互不相同的失败统一返回同一个 Go 哨兵错误 `domain.ErrInvalidState`；HTTP 层它和版本冲突 `domain.ErrVersionConflict` 共享同一个状态码 409 与同一个 JSON `code` 字段值 `"CONFLICT"`。前端 `friendlyError()` 又把所有 409 一律显示成"数据已经发生变化，请刷新后重试。"——对"服务端正确挡住一次不安全解冻"的情形这句话具有误导性（暗示重试即可，实际要等对应条件真正满足）；所有 5xx（含五流新鲜度失败对应的 503 `SOURCE_SYNC_UNAVAILABLE`）也一律显示"开票服务暂时不可用，请稍后重试。"

不在本 CR 范围：83 条冻结记录数量为何持续增长（`SOURCE_GAP`/锚定账号余额检查点这类机制性根因）由另一只读调研任务处理，本 CR 只解决"看得懂、找得到、够不够透明"。

## 证据
- `eligibilityFreezeSelect`（`eligibility_operations.go:105-113`）已 `JOIN external_accounts ea`，但只 `SELECT ea.invoice_user_id`（开票系统自己的内部用户 ID），没有选 `ea.external_user_id`（上游平台数字用户 ID，`external_accounts` 表既有列，见 `identity.go:311` 等处的既有查询）。
- `eligibilityFreezeDTO`（`operations.go:62-72`）与前端 `BackendEligibilityFreeze`/`mapEligibilityFreeze`（`http-api.ts:134-148`、`624-645`）的输出字段一致确认：`id/principal_id/source_instance_id/source_type/source_name/funding_lot_id/scope/freeze_reason/status/eligibility_status/opened_at/version/resolved_at`，没有任何外部用户标识。`docs/ELIGIBILITY-OPERATIONS.md`"Administrator queue"一节明确写着"It never returns external user IDs"——这是既有的、写在文档里的设计取舍，本 CR 是有意识地推翻它，不是疏漏。
- 已有的同类脱敏先例：`identity.go` 的 `ListExternalAccounts` 查询用 SQL `CASE` 对 `external_user_id` 做首2尾2脱敏，经 `operations.go` 的 `listSourceAccounts` 暴露为 `external_user_id_masked`；星芒控制台自己的用户详情页对同一数字 ID 是**明文**展示（`web/apps/admin-web/src/pages/PlatformUserDetailPage.tsx:156`）。
- `.toast`（`web/src/styles.css:1617-1621`）：`position: fixed; right: 22px; bottom: 22px；` `EmbeddedConsoleFrame.tsx` 的 `MAX_HEIGHT_PX = 4000`、`MIN_HEIGHT_PX = 480` 与 `clampHeight()` 确认 iframe 高度按内容撑到最高 4000px；`EligibilityFreezeDrawer.resolve()`（`App.tsx` 约 3159-3178 行）唯一的失败反馈路径就是 `toast(...)`，抽屉的 `freeze-resolution-form` 区块本身没有任何内联错误展示位。
- `handleDomainError`（`server.go:1145-1166`）：`ErrConflict`/`ErrInvalidState`/`ErrVersionConflict` 均映射到 `409 CONFLICT`；`ErrSourceUnavailable` 映射到 `503 SOURCE_SYNC_UNAVAILABLE`。`friendlyError()`（`http-api.ts:396-403`）对 409 固定返回"数据已经发生变化，请刷新后重试。"，对 ≥500 固定返回"开票服务暂时不可用，请稍后重试。"，不读取服务端返回的具体 `code`/`message`。`assertSourceFreshTx`（`source_sync.go:970-1020`）确认五流任一不新鲜即返回 `domain.ErrSourceUnavailable`；`ResolveEligibilityFreeze`（`eligibility_operations.go:186-230`）确认退款暴露四种子情形与投影任务未清共用同一处 `domain.ErrInvalidState` 返回，对账结论不匹配也是同一个哨兵错误。

## 变更范围

**开票线（主要）：**
- 后端：`eligibility_operations.go`（`eligibilityFreezeSelect` 增加 `ea.external_user_id`；`EligibilityFreeze` 结构体增加字段；`EligibilityFreezePageQuery` 增加 `ExternalUserID` 过滤条件及校验）；`operations.go`（`eligibilityFreezeDTO` 增加输出字段；`listEligibilityFreezes` 解析新查询参数）。
- 前端：`types.ts`（`EligibilityFreeze`/`EligibilityFreezeFilters`）；`http-api.ts`（`BackendEligibilityFreeze` 类型与 `mapEligibilityFreeze` 的 `allowed` 允许键列表——**必须与后端改动同一次提交**，见"契约变化"）；`App.tsx`（队列表格加一列、加一个筛选输入框；抽屉展示该字段）。
- 抽屉可见性：`EligibilityFreezeDrawer.resolve()` 增加抽屉内联状态展示（不止 toast），且抽屉/toast 的定位不再假设 iframe 高度等于视口高度。
- 错误细化（P2）：`domain` 包为退款暴露/投影任务未清/对账结论未匹配/五流不新鲜各自新增可区分的错误；`handleDomainError` 分配独立 `code`；`friendlyError()` 对这些新 `code` 不再折叠进通用 409/5xx 分支；抽屉展示对应中文原因。
- 文档：`docs/ELIGIBILITY-OPERATIONS.md` 更新"从不返回外部用户 ID"的表述。

**平台线（条件性，仅问题二选择"控制台侧同步展示"方案时需要）：**
- `InvoiceConsolePanel.tsx`/`EmbeddedConsoleFrame.tsx` 接收开票前端新增的一个 `xm-embed` 信封 `kind`（沿用 CR-0006 已确立的版本化信封先例），在宿主侧也呈现一次失败提示。若开票线选择"抽屉内联展示 + toast 改锚定视口（而非 iframe 文档）"即可解决问题二，则平台线无需任何改动——两种方案是否都做、做哪种，由开票线技术方案阶段决定，本 CR 不预先裁定。

## 契约变化

`GET /api/v1/admin/eligibility-freezes` 响应项，现状（三处允许键集合——Go DTO、`BackendEligibilityFreeze`、`mapEligibilityFreeze` 的 `allowed`——必须逐字一致，否则前端严格白名单校验 `exactObjectKeys` 会判整页"格式无效，已停止显示"）：

```json
{
  "id": "f2b1...", "principal_id": "8a3c...", "source_instance_id": "0d5e...",
  "source_type": "sub2api", "source_name": "Sub2API 生产实例",
  "funding_lot_id": "", "scope": "account", "freeze_reason": "SOURCE_GAP",
  "status": "open", "eligibility_status": "frozen",
  "opened_at": "2026-09-02T02:00:00Z", "version": 3
}
```

新增（仅追加，其余不变）：`"external_user_id": "1147"`，取自 `external_accounts.external_user_id`（该表已被 JOIN，只是未被 SELECT），明文、不做脱敏——理由见"兼容性/安全"。请求侧新增可选查询参数 `external_user_id`，精确匹配，可与既有 `source_instance_id` 同时传以在多平台下消歧。

关于"masked email/username"：本 CR **不**新增。`external_accounts` 表没有任何明文或可展示的上游邮箱/用户名列；唯一相关的加密字段 `invoice_users.email_ciphertext` 是开票系统自己核实的收件邮箱，语义不同，展示它需要新的解密路径，超出本 CR 的最小改动范围。这一点是对原始需求的收窄：如确有必要展示更友好的名称，需另立变更单评估。

`POST .../resolve` 失败呈现：不改变判定逻辑或返回的业务对象，只是不再丢弃已经算出的失败原因。新增细分错误码建议：`ELIGIBILITY_SOURCE_STALE`（五流新鲜度）、`ELIGIBILITY_PROJECTION_PENDING`（投影任务未清）、`ELIGIBILITY_REFUND_EXPOSURE`（退款相关四种子情形，操作员应对动作相同故合并一码）、`ELIGIBILITY_BALANCE_NOT_SAFE`（对账结论未匹配）；HTTP 状态码维持现状（503/409）不变，版本冲突与自我解冻禁止两类维持现有通用提示（"刷新重试"对真实版本冲突本就是准确建议）。

## 兼容性/安全
- 新增字段是纯追加，但前端 `exactObjectKeys` 白名单意味着后端与前端两侧必须在同一次提交内一起改，任一方单独上线都会让队列页整页报错——这是本 CR 对实现方最重要的提醒。
- `external_user_id` 明文不构成新的 PII 类别：管理员在星芒控制台自己的用户详情页（`PlatformUserDetailPage.tsx:156`）对同一数字 ID 本就明文展示；开票后端另一个端点（`listSourceAccounts`）也已把它当作"可以给管理员看"的数据处理（只是做了脱敏）。本 CR 是把这一既有判断在冻结队列页上补齐，不是新开先例。
- 不新增任何写操作，不改变 `ResolveEligibilityFreeze` 的判定条件、顺序或权限模型；审计、CSRF、角色与 IP 白名单不变。
- 问题三新增的细分错误不产生新的信息泄露：这些原因今天已可通过读数据库或看来源健康页推断，本 CR 只是把同一批人已经能看到的信息挪到更顺手的地方。
- 若采用平台侧 postMessage 方案，新增信封 `kind` 遵守既有"仅接受精确来源，不放宽为通配符"纪律（`EmbeddedConsoleFrame.tsx` 现有校验模式）。

## 验收标准
1. 在全部三个嵌入入口与独立入口，管理员无需查库即可从列表和抽屉看到每条记录对应的来源平台数字用户 ID。
2. 按数字用户 ID（可选配合来源实例）过滤队列，结果与直接查库核对一致。
3. 在任一嵌入入口内触发一次会被服务端拒绝的解冻请求，拒绝原因在抽屉可见区域内（无需滚动整个宿主页面）可见。
4. 分别构造五流新鲜度失败、投影任务未清、退款暴露、对账结论未匹配四种情形各触发一次解冻，抽屉展示的中文原因四种互不相同，且与实际触发条件一一对应。
5. 独立入口（非嵌入）下队列与抽屉行为不因本 CR 回归；`docs/ELIGIBILITY-OPERATIONS.md` 与实现一致。

## 优先级
问题一、问题二：P1（阻塞开票资格冻结队列的正常人工复核，是当前生产 83 条待处理记录人工核实工作的直接障碍）。问题三：P2（体验改进，不阻塞复核本身，但拖慢每一次复核，建议与问题一/二同一切片一并做）。

## 状态
proposed。

## 明确不变
- `ResolveEligibilityFreeze` 的判定条件、顺序与安全边界不变；本 CR 不放宽、不新增任何一条实质判定。
- 队列与解除接口既有的角色、CSRF、IP 白名单访问控制不变。
- 证据与说明加密落库、审计只写哈希的既有纪律不变。
- 平台侧 iframe"不认识开票业务内容"的既定原则（CR-0005"明确不变"）不变：即便采用 postMessage 方案，平台侧转发的也只是一个不透明的失败提示，不解析开票业务数据或金额。

## 回滚
本 CR 全部改动为新增字段/新增可选查询参数/新增前端展示/新增错误码，没有数据迁移。回滚即把对应改动还原到上一个已发布版本；`external_user_id` 是只读派生自既有列的展示字段，从未写入过任何新数据，回滚不涉及数据修复。

## 确认
- 平台线：验收线，2026-09-03（按运营复核发现起草）。
- 开票线：验收线，2026-09-03（按运营复核发现起草）。
- 产品负责人：待确认。

## 执行记录
- 2026-09-03 立单（设计阶段，未派发实现切片）。
