# EV：platformusers v2 real GetUser（NewAPI）字段形状核对（2026-08-28/09-03）

- 审批：`docs/approvals/NEWAPI_REAL_APPROVAL.md`（NEWAPI_REAL_APPROVAL，
  APPROVED 2026-09-03），授权
  `docs/superpowers/plans/2026-08-28-platform-user-read-v2.md` Task 5。
- 证据来源：`cmd/evidence-capture capture --platform newapi`，写入
  `docs/evidence/users-real/newapi/20260902T054724Z/`（本文件只引用路径与
  哈希，不复制脱敏样本内容）。

## 核对结论

- 上游实例版本 `v1.0.0-rc.30`（`version.json`）；采集时观测的
  `quota_per_unit=500000`——**这是采集那一刻的观测值，不是永久基数**，
  real 实现（`getNewAPIUser`）每次 `GetUser` 调用都重新调用
  `newapiQuotaPerUnit` 现读一次 `/api/status`，不跨调用缓存；
  `TestRealClientNewAPIGetUserRereadsQuotaPerUnitEveryCall`
  （`connectors/platformusers/newapi_v2_test.go`）用两次基数不同的
  `/api/status` 响应验证同一个 `RealClient` 实例两次调用得到不同的换算
  结果，证明没有缓存。
- `users_page.redacted.json` 的信封形状是
  `{success, message, data:{items,total}}`，`success` 为 `true`；与
  `newapiEnvelope`/`newapiPage` 一致。
- 八个 kept 字段（`id`/`username`/`display_name`/`email`/`status`/`quota`/
  `last_login_at`/`DeletedAt`）与 `connectors/platformusers/upstream.go` 的
  `newapiUserItem` 逐字一致。dropped 字段列表中出现了
  `password`/`original_password`/`verification_code` 三个字段名（批准文本
  已记录：值从未离开采集进程，但字段名本身值得安全侧留意）——
  `newapiUserItem` 结构体压根没有声明这三个字段对应的成员，
  `json.Unmarshal` 不可能把它们的值填进本包任何结构体。
- 本次采集的 5 条样本 `DeletedAt` 全为字面 `null`，**未观测到实际软删除
  样本**（NEWAPI_REAL_APPROVAL 决定文本已注明）。`item.softDeleted()` 的
  软删除判据（非空且非字面 `"null"`）与 v1 `fetchNewAPIUsers` 复用同一个
  方法，行为已由 v1 契约测试覆盖；本次新增的
  `TestNewAPIV2GetUserIgnoresSoftDeletedUser` 用一份手写的最小合成 JSON
  单独验证 GetUser 对软删除用户返回 `ErrNotFound`（合成数据已在测试代码
  注释中说明来源，不是真实抓包）。
- 5 条样本 `status` 均为 `1`（启用）；上游只有 `1`/`2` 两个取值
  （`UserStatusEnabled`/`UserStatusDisabled`），`ParseUserStatus` 已覆盖
  两者，`0` 不会被上游真实发出。
- `last_login_at` 为 Unix 秒整数，`quota` 为整数（本次样本含 `0` 与
  `10000000`），脱敏工具的数字清零规则保留了字面量是否为纯数字这一形状，
  `matchNewAPIUserInPage` 复用 `amount.go` 的 `quotaToMinorUnits`，全程不经
  `float64`。
- GET 写端点黑名单：`/api/user/token`、`/api/user/aff`、
  `/api/user/epay/notify` 等（`connectors/newapi/upstream.go` 的
  `writeDisguisedAsGetRoutes`）继续生效——`newAPIV2AllowedPath` 用允许清单
  （只放行 `/api/user/` 与 `/api/status`）机械覆盖这些端点与黑名单未列出
  的任何新写端点；`TestNewAPIV2WriteLikeGETsStayBlocked` 钉死这条纪律。
- 管理员 token 只经 `CredentialRef` 注入（`RealClient.authorize`），错误
  正文零透传（`connector.Error` 从不把上游响应体纳入错误链）。
- 上游没有原生的按 ID 精确查询端点，`getNewAPIUser` 对 `/api/user/` 分页
  做完整扫描（预算与 v1 `fetchNewAPIUsers` 一致，`newapiMaxPages`），命中
  即返回；翻完仍未命中返回 `ErrNotFound`，预算耗尽仍有更多页返回
  `ErrLookupIncomplete`。
- 只声明 `platformusers.user.detail_read` 一项 capability
  （`RealClient.V2Capabilities`），不继承 Sub2API 的 daily/key 布局
  （`TestNewAPIV2CapabilitiesDoNotInheritDailyOrKey`）：`created_at`
  等字段在证据里被列为 dropped，`UserDetail.RegisteredAt` 保持零值；
  `topup`/`log`/`data`/token 端点是否携带同一稳定 `user_id`
  本审批明确不依赖（证据工具从未采集这一项），留给未来的
  DailyUsage/Key metadata real reader 独立审批时核实。

## 核验日期

2026-09-03（本文写作时；证据采集时间 2026-09-02T05:47:24Z / 05:47:25Z，
见 `docs/evidence/users-real/newapi/20260902T054724Z/README.md`）。
