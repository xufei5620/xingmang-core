# XM-INV-NOTICE-VIEW：管理端能查「那条企业微信通知发出去了没有」

- **status:** implemented，未发布。**只读、只加一条路由**，不改数据库、不改
  投递行为、对现有环境零影响。
- **branch:** `ai/claude/XM-INV-NOTICE-VIEW`（自发布线 `ai/claude/XM-INV-AUTOLOGIN`）。
- **来源：** `XM-INV-SUBMIT-NOTICE` 自己留的 follow_up——「今天要查一条通知发没发
  出去只能看库」。平台仓库刚写的消息目录（XM-NOTIFY-CATALOG）里，
  「收不到消息时按这个顺序查」的第 3 步在开票这条线上正好走不通。

## 为什么是新路由，而不是并进 `/delivery`

`GET /api/v1/admin/invoice-requests/{id}/delivery` 看起来是个现成的位置，
但它**在申请还没有开票文档时直接返回 404**（`getAttachedDocumentTx` 找不到就
`ErrNotFound`）——它答的是"发票邮件寄了没有"。

而通知恰恰在**提交那一刻**就产生了，那时申请还是 `pending_review`、离开票还早。
并进去等于让这条信息在最需要它的时候查不到。所以：

```
GET /api/v1/admin/invoice-requests/{id}/notices  →  {"items": [...]}
```

## 只有 admin，没有 user 变体

「那条企业微信通知发出去了没有」是**运维事实**，不是申请人的业务数据。
用户看到自己的申请触发了什么内部推送，既没有用，也是一次不必要的内部暴露。
库层那个方法因此**刻意不做 principal 归属校验**并在注释里写明原因——它只挂在
admin 路由上。

## 返回什么

每条：`id / kind / status / attempt_count / next_attempt_at / delivered_at /
last_error_code / created_at / updated_at`。

**`delivered_at` 未送达时是 `null`，不是零时刻**：「1970 年送达」比「还没送达」
更容易被读成一次真实投递。

**`last_error_code` 可以直接给人看**，这一点是查过才敢写的：投递侧
（`internal/notify` 的 `WeComSender.Send`）对四种失败路径都只回我方分类过的短码
——传输失败那条**刻意不带 `err`**，因为 net/http 会把含 key 的 URL 放进自己的
错误里；errcode 那条**刻意不带 errmsg**，因为那是上游自由文本。所以这一列里
不会有 Webhook 地址，也不会有上游原文。

**返回数组而不是单对象**：今天一份申请只可能有一条通知（`request.submitted`），
将来加「审核通过」「开具完成」时响应形状不用变。**没有记录时是 `[]` 不是 `null`**
——前端对两者处理不同，而「这份申请没有通知记录」是正常状态（通知功能默认关）。

## 文件

- 修改：`backend/internal/postgresstore/notice_outbox.go`（新增
  `InvoiceNoticeState` 与 `ListInvoiceNoticesForRequest`）、
  `backend/internal/application/service.go`、
  `backend/internal/httpapi/{service_contract.go,server.go,operations.go}`
- 新增：`backend/internal/httpapi/notice_view_test.go`
- 修改（测试）：`backend/internal/httpapi/operations_source_filter_test.go`
  ——给 `OperationsService` 加方法会让既有替身不再合规，按该文件自己的约定补一个
  panic 桩（"意外的调用路径要响，不能静默返回零值"）。
- 修改（文档）：`docs/handoffs/XM-INV-SUBMIT-NOTICE.md` 的对应 follow_up 划掉。

## 测试

- `notice_view_test.go` 四条：字段转发与响应形状；排队中 `delivered_at` 为
  `null` 且失败原因看得见；无记录时是空数组；**用户侧没有这条路由**。
- `notice_outbox_integration_test.go` 两条：排队→失败→重新取件→送达的全过程
  （每一步的 `status` / `attempt_count` / `delivered_at` / `last_error_code`），
  按申请隔离（另一份申请的通知不能混进来）；无记录时返回空切片而不是 nil。

**「用户侧没有这条路由」那条做过变异验证。** 它一开始写的是「状态不等于 200」
——那是**恒真的**：一条存在但要鉴权的用户路由在无凭据时回 401，同样不等于 200。
改成「先证明参照的已注册用户路由不回 404，再要求这条路径回 404」之后，
临时加一条 user 路由跑一次，它如期变红（回 401，参照也回 401）。

写测试时还撞到一次：先 `MarkInvoiceNoticeFailed` 再直接 `MarkInvoiceNoticeSent`
不生效。**查了代码才发现是测试错了不是代码错**——`MarkInvoiceNoticeSent` 只对
`status='sending'` 生效，而失败会把行放回 `queued`；投递循环的真实次序是
取件→投递→标记，跳过取件在生产里不会发生。测试补上了重新取件（并顺带钉住
「退避到期后能重新取件」）。

门禁：`go build ./...`、`go vet ./...`、
`go test -p 1 -count=1 ./...` 全绿（带 `INVOICE_TEST_DATABASE_URL`）、
`gofmt`（LF 归一后）干净、`gitleaks protect --staged` 无泄漏。

## 上线

**随下一个 RC 一起发，没有单独的上线动作。** 新路由是只读的，通知功能没启用时
它返回空数组。

## follow_ups

- 只有按申请查，没有「最近失败的通知」全局列表。真要排查"最近有没有发失败的"，
  今天还是要查库。等有人真的需要再说——加一个列表就要加分页、筛选与保留期。
- 管理端前端还没有用这条路由（这一片只做后端）。申请详情页加一格「通知」
  是很自然的下一步。
