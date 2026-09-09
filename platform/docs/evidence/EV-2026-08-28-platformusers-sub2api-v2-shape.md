# EV：platformusers v2 real GetUser（Sub2API）字段形状核对（2026-08-28/09-03）

- 审批：`docs/approvals/SUB2_REAL_APPROVAL.md`（SUB2_REAL_APPROVAL，APPROVED
  2026-09-03），授权 `docs/superpowers/plans/2026-08-28-platform-user-read-v2.md`
  Task 4。
- 证据来源：`cmd/evidence-capture capture --platform sub2api`，写入
  `docs/evidence/users-real/sub2api/20260902T054723Z/`（本文件只引用路径与
  哈希，不复制脱敏样本内容——README 已说明理由）。

## 核对结论

- 上游实例版本 `0.1.184`（`version.json`），比
  `EV-2026-08-27-sub2api-read-survey.md` 记录的兼容矩阵上限 0.1.183 高一个
  patch 版；`users_page.redacted.json` 的六个 kept 字段（`id`/`email`/
  `username`/`status`/`balance`/`last_active_at`）与
  `connectors/platformusers/upstream.go` 的 `sub2apiUserItem` 逐字一致，
  dropped 字段列表（`allowed_groups`、`balance_notify_*`、`concurrency`、
  `created_at`、`current_concurrency`、`frozen_balance`、`last_used_at`、
  `notes`、`restrict_public_groups`、`role`、`rpm_limit`、`total_recharged`、
  `updated_at`）里没有出现与六个 kept 字段同名或替代它们的陌生字段——
  产品负责人已在 SUB2_REAL_APPROVAL 决定里接受这一个 patch 版的兼容性结论。
- `users_page.redacted.json` 的信封形状是
  `{code, message, data:{items,total}}`，与 `sub2apiEnvelope`/`sub2apiPage`
  一致；`code` 为 `0` 表示成功。
- `balance` 字段脱敏后是十进制字面量（本次样本恒为 `1.0`/`1.00000000`，
  脱敏工具把真实数字位清零、只保留小数位数与是否为字面量数字这两项形状
  信息）；`connectors/platformusers/sub2api_v2.go` 的
  `matchSub2UserInPage`/`parseSub2UsersPage` 复用 `amount.go` 的
  `decimalToMinorUnits`/`rescaleMinorUnits` 做定点换算（8 位小数
  `sub2apiBalanceScale` → 2 位小数 `platformCurrencyScale`），全程不经
  `float64`；单元测试
  `TestSub2APIV2GetUserUsesExactIDAndFixedPoint`
  （`connectors/platformusers/sub2api_v2_test.go`）断言 `1.0` 换算后恰好是
  100（1.00 美元）。
- 本次采集的 5 条样本 `status` 均为 `"active"`；上游只有 `active`/`disabled`
  两个取值（`EV-2026-08-27-sub2api-read-survey.md`
  依据的源码普查未变），`ParseUserStatus` 已覆盖两者。
- `last_active_at` 为 RFC3339（带时区偏移）字符串，`parseUpstreamTime` 已
  支持该格式。
- AdminComplianceGuard：本次采集两次请求均 200，未收到 423，说明采集所用
  的 admin 账号已在 Sub2API 后台完成过合规确认；SUB2_REAL_APPROVAL 前置
  条件已核对这一点。real GetUser 实现（`sub2api_v2.go` 的
  `getSub2APIUser`）把 423 翻译成有类型标记的
  `ErrSub2APIComplianceConfirmationRequired`，结构上不可能发出
  `POST /admin/compliance/accept`（该方法只调用 `RealClient.get`，请求方法
  硬编码为 `GET`）；见
  `TestRealClientSub2APIGetUserComplianceGuardIsTypedAndNeverPosts`。
- Mock 端点黑名单：`/api/v1/admin/users/:id/usage` 等 5 个端点已被
  `EV-2026-08-27-sub2api-read-survey.md` 证实是 mock，`sub2V2AllowedPath`
  只放行裸列表端点 `GET /api/v1/admin/users`，其余路径（含这 5 个）一律
  拒绝；`TestSub2APIV2PermanentlyRejectsMockUsageRoute` 钉死这条纪律。
- 上游没有原生的按 ID 精确查询端点（证据只覆盖列表端点），`getSub2APIUser`
  对列表分页做完整扫描（预算与 v1 `fetchSub2APIUsers` 一致，
  `sub2apiMaxPages`），命中即返回；翻完仍未命中返回 `ErrNotFound`，预算
  耗尽仍有更多页返回 `ErrLookupIncomplete`（分别见
  `TestRealClientSub2APIGetUserNotFoundAfterExhaustingAllPages`、
  `TestRealClientSub2APIGetUserLookupIncompleteWhenBudgetExceeded`）。
- 只声明 `platformusers.user.detail_read` 一项 capability
  （`RealClient.V2Capabilities`）：`created_at` 等字段在证据里被列为
  dropped，`UserDetail.RegisteredAt` 保持零值；`DailyUsage`/
  `ListKeyMetadata` 未实现，real 端不声明对应 capability。

## 核验日期

2026-09-03（本文写作时；证据采集时间 2026-09-02T05:47:23Z / 05:47:24Z，
见 `docs/evidence/users-real/sub2api/20260902T054723Z/README.md`）。
