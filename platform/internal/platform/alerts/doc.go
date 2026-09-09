// Package alerts 实现规格 §9.3 的告警生命周期与 §9.4 的告警渠道
// （Foundation-A 档，XM-0033）。
//
// 铁律：
//   - **去重是第一位的**：同一个真实问题在多轮评估里必须收敛成一条告警 +
//     递增的 fire_count，而不是每分钟新开一条。去重键由规则声明，库层还有
//     一条部分唯一索引兜底（见 db/migrations/000007_alerts.up.sql）；
//   - **静默不是解决**：命中静默窗口的告警进 SILENCED 且不投递，但它仍然
//     活着——窗口一过、条件仍成立，它要转回 OPEN 并投递。把静默实现成
//     「删掉」或「标记已解决」等于让平台替人撒谎（宪法 12 条）；
//   - **投递状态与告警状态正交**：一条 OPEN 的告警完全可能还没投递出去。
//     这两个事实必须分别可见，否则会出现「以为告警会通知我」的最危险状态；
//   - **凭据只在发请求那一瞬存在**：Telegram Bot Token 经 SecretProvider 解析，
//     绝不进日志、错误串、notify_error 或任何响应（宪法 7 条）。
//     TestTelegramNotifierNeverLeaksToken 机械地守住这条；
//   - **规则不入库**：Foundation-A 第一批规则写死在 rules.go。改规则是一次
//     带 PR 与测试的代码变更——在没有表达式沙箱之前，这比「规则是数据」更安全。
//
// 边界（Foundation-A 不做，见 docs/modules/alerts/README.md）：
// 分组、升级、Incident 关联、Runbook 关联、按严重度路由渠道。
package alerts
