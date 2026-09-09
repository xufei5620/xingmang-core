// Package publishing 实现「内容发布」（`/ext/publishing`）的第一层。
//
// # 裁定
//
// ADMIN-IA §5.4 原本把扩展能力四页一律定为「只读蓝图……不得因此提前建后端」。
// 产品负责人 2026-09-08 推翻了这一条在**这一页**上的适用（§5.4.1），要求真建，
// 并给出两条线：主线是对外社交平台（X 及其它）发布，次线是站内公告。
//
// # 本包做什么 —— 第一层：内容的生命周期在平台内是真的
//
// 草稿与不可变版本、素材引用、排期时间、渠道与账号登记、发布审批（复用
// XM-0030a 的审批中心）、发布记录。这些都是真实数据，走真实的 Action 与 Query。
//
// # 本包**不**做什么 —— 第二层：真正的出站投递
//
// 平台今天**没有任何出站投递器**：没有 X 的 Connector、没有 OAuth 流程、
// 没有速率限制、没有失败重试。因此 `publishing.publish.submit` 这个 Action
// 走完审批、执行成功之后，落下的发布记录结果一律是「未投递」。
//
// 这句话是一条**缺席型主张**，所以它不能只写在注释里：
//
//   - 库层：`publish_record.result` 的 CHECK 只认 'NOT_DELIVERED'，且带编号或
//     投递时刻的「未投递」记录被 CHECK 拒绝（迁移 000052）；
//   - 代码层：Service 持有一张 deliverers 表，生产装配传的是空表，
//     `DelivererFor` 因此总是返回 nil（见 deliver.go）；
//   - 测试层：`TestNoOutboundDelivererIsRegistered` 与
//     `TestPublishRecordsNotDeliveredOutcome` 钉住它，且**做过变异验证**——
//     往表里塞一个假投递器，两条用例都变红（见 deliver_test.go 的注释）。
//
// 接上真投递器需要：一次显式迁移（放开 result 枚举）、一份 Connector 契约、
// 一条经 ADR-021 登记的写通道，以及对「发出去之后怎么撤」的裁定。
// 少任何一样都不该让这个包看起来能发。
//
// # 站内公告为什么不在这里
//
// 次线（站内公告）经调查**走不通**，结论见
// docs/handoffs/slices/XM-EXT-PUBLISHING.md：
//
//   - Sub2API 有完整的公告 API（`/api/admin/announcements` 五个端点 + 用户侧
//     已读状态，自 v0.1.178 起就有，生产版本具备），NewAPI 有一个全局公告选项
//     （`console_setting.announcements`，经 `PUT /api/option/` 整体覆写）；
//     两者都**要求平台向它们发写请求**。
//   - ADR-018 对这两个上游立的是四道**只读**闸；ADR-021 新增的供应商写通道
//     明文写着「适用范围仅限平台作为客户购买服务的供应商……复用本 ADR 去写
//     NewAPI/Sub2API……仍然违反 ADR-018 与宪法条款 5」。
//   - 平台自有通道只有企业微信群机器人（internal/platform/notify +
//     alerts.WeComNotifier），收件人是运维群而不是被管平台的终端用户；
//     平台**没有**邮件能力（SMTP 只存在于开票系统，另一个仓库）。
//
// 所以本包**不建**任何 announcement 渠道类型：一个发不出去的取值摆在渠道
// 下拉框里，等于在页面上放一个假入口。这条线要走通得由产品负责人另立 ADR。
package publishing
