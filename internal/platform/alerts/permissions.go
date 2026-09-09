package alerts

// ScopeRead 是读取告警所需的权限。
//
// 复用 ops.read 而不是自立一个 alerts.read：告警的内容就是运营指标的判读
// 结果——「渠道甲余额只剩 3000」这条告警泄漏的信息，与 ops.read 能直接读到
// 的余额数字完全相同。多一个 scope 只是多一个要维护、要授予、会漏授的
// 授权面，换不来任何实际的信息隔离。
//
// 字面量而不是 import ops.ScopeRead：本包已经 import 了 ops（评估器要读
// 观测），复用常量本可行；写成字面量是为了让「这个 scope 是一个**决定**」
// 显式可见——将来告警要独立授权时，改的是这一行，而不是去动 ops 包。
// 一致性由 permissions_test.go 里对 ops.ScopeRead 的断言守住。
const ScopeRead = "ops.read"

// ScopeAcknowledge 是确认告警所需的权限（L0 动作）。
//
// alerts.upstream_version.acknowledge（L1，核对上游版本）**复用它**，不另立
// scope：两件事都是「我看过了」，而核对上游版本的爆炸半径比静默小一个量级
// ——它只让**这一条**上游的版本提醒停下来，不会让任何别的告警闭嘴。
// 新增一个 scope 要同时改 oidcauth/rolemap.go 与前端的权限清单，
// 换不来任何实际的信息隔离。
const ScopeAcknowledge = "alerts.alert.manage"

// ScopeSilenceManage 是创建静默窗口所需的权限（L1 动作）。
//
// 与 ScopeAcknowledge **分开**授予是有意的：确认只是说「我看见了」，
// 告警仍然留在列表里；静默是让告警**不再出现也不再投递**，一个足够宽的
// 窗口等于临时关掉整套告警。两件事的爆炸半径差一个量级，
// 不该由同一个 scope 一并放行（ADR-003 风险等级的同一条思路）。
const ScopeSilenceManage = "alerts.silence.manage"
