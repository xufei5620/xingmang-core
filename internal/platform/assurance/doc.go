// Package assurance 实现 XM-ASSURE1-core：渠道主动探测（检测任务）。
//
// 依据 docs/adr/ADR-019-渠道主动探测通道.md 与配套设计稿
// docs/superpowers/specs/2026-09-03-xm-assure1-active-probes-design.md。
// 与 XM-ASSURE0 的 internal/platform/channelassurance（被动、只读、来自
// reqlog 统计）完全独立：本包主动发出真实请求，有真实成本，数据来自平台
// 自己新建的 assurance schema，两者共享"渠道保障"页面壳，互不依赖、互不
// 冒充（ADR-019 决策·六）。
//
// 分层参照 internal/platform/channelassurance 的既有习惯：
//   - Store（store.go）：assurance.probe_declaration/probe_run/probe_result
//     三张表的读写仓储，含预算/冷却/并发闸的判定查询；
//   - Service（service.go）：检测任务表 / 历史记录（主动部分）两个只读
//     Query 的入口；
//   - Action Handler（actions.go）：declare / cancel / run / kill_switch.set
//     四个 L1 Action；
//   - 探测客户端（fakeclient.go / realclient.go）：fake 模式的确定性假连接器
//     与 real 模式对 OpenAI 兼容 chat/completions 端点的 HTTP 客户端；
//   - 固定模板（templates.go）：四个代码固化的探测任务，不接受自由文本
//     Prompt（ADR-019 决策·五）。
//
// River Worker（internal/platform/jobs/assurance_probe.go）依赖本包做全部
// 业务逻辑，自己只是一层薄的 River 适配——与 connector_probe.go 依赖
// internal/platform/connector 同一个关系形状。AssuranceProbeArgs 之所以定义
// 在本包而不是 jobs 包（与 jobs 包里其它任务的 *Args 都定义在 jobs 包自己
// 内部这一惯例不同）：本包的 Action Handler 需要在同一个数据库事务里插入
// probe_run 行并入队这个 Job（InsertTx），如果 Args 类型定义在 jobs 包，
// jobs 包又需要 import 本包才能实现 Worker，就会形成 assurance → jobs →
// assurance 的循环依赖。把 Args 类型放在本包，jobs 包单向依赖本包，
// 与 jobs 包依赖 ops/finance/sub2api/newapi 等其它领域包的方向完全一致。
package assurance

import (
	"errors"
	"time"
)

// 领域错误（供 Action Handler 翻译成稳定的 action.Code）。
var (
	// ErrNotFound：声明/批次不存在。
	ErrNotFound = errors.New("assurance: not found")
	// ErrInvalidInput：入参不合法（校验在 Handler/Store 两层都做，纵深防御）。
	ErrInvalidInput = errors.New("assurance: invalid input")
	// ErrDeclarationNotActive：对一个非 active 状态的声明做只允许 active 时
	// 才能做的操作（如再次 cancel）。
	ErrDeclarationNotActive = errors.New("assurance: declaration not active")
	// ErrVersionConflict：expected_version 与当前 version 不一致（并发覆盖）。
	ErrVersionConflict = errors.New("assurance: version conflict")
	// ErrStore：数据库失败；根因经 Unwrap 供服务端日志，不进对外文本。
	ErrStore = errors.New("assurance: store failed")
)

// Action 权限点（ADR-019 决策·三、决策·四·#4）。
//
// ScopeKillSwitch 刻意与 credentials.ScopeConnectorManage（"connector.manage"）
// 分开：能批准"这个平台允许探测花钱"的人，未必需要同时拥有"改连接器怎么
// 连上游、换只读 admin 凭据"的权限，两者要能分开授予、分开审计。
const (
	// ScopeManage 是 declare@1 / cancel@1 的权限点：纯配置写操作。
	ScopeManage = "assurance.probe.manage"
	// ScopeRun 是 run@1 的权限点：触发一次真实探测（受 Kill Switch/预算/
	// 冷却/并发闸多重约束，见 store.go EvaluateRun 的判定顺序）。
	ScopeRun = "assurance.probe.run"
	// ScopeKillSwitch 是 kill_switch.set@1 的权限点，独立于 ScopeManage/
	// ScopeRun 与 credentials.ScopeConnectorManage。
	ScopeKillSwitch = "assurance.probe.kill_switch"
)

// Platforms 是可声明检测任务的平台（与迁移里的 CHECK 约束一致）。
var Platforms = []string{"sub2api", "newapi"}

// ModeReal / ModeFake 与 credentials.ModeReal/ModeFake 是同一个取值——本包
// 刻意不 import internal/platform/credentials（见 store.go
// GetConnectorProbeConfig 的文档注释），改用自己的一份窄读取，但取值必须
// 与那张表的 CHECK 约束一致，因此在这里重新声明成本包的导出常量，供
// internal/platform/jobs 的 Worker 比较 ConnectorProbeConfig.Mode 时使用，
// 不必散落字符串字面量。
const (
	ModeReal = "real"
	ModeFake = "fake"
)

// 声明 / 批次 / 结果的状态取值（与迁移的 CHECK 约束一致）。
const (
	DeclarationStatusActive    = "active"
	DeclarationStatusCancelled = "cancelled"

	RunStatusPending   = "pending"
	RunStatusRunning   = "running"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusRefused   = "refused"
	// RunStatusCancelled 保留在 CHECK 约束里供未来"撤销一个正在排队的批次"
	// 使用，但本片只有一条路径会写这个状态：assurance.probe.cancel@1 撤销的
	// 是声明本身；如果一个 run 已经以 pending 状态入队，declaration 又在
	// Job 真正执行前被撤销，Work() 的执行时刻竞态复检（jobs/assurance_probe.go
	// 的 checkAtExecutionTime）会把这条 run 标成 cancelled 而不是 refused——
	// "声明被主动撤销"比"因为预算/开关这类策略闸被拒�内"语义上更接近
	// "取消"。除此之外没有任何路径能把一个 run 转成 cancelled（没有
	// "撤销单次批次"的 Action），交接文档会写清楚这一点。
	RunStatusCancelled = "cancelled"

	ResultStatusOK       = "ok"
	ResultStatusDegraded = "degraded"
	ResultStatusFailed   = "failed"
	ResultStatusTimeout  = "timeout"

	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
)

// 拒绝原因枚举（设计稿 §2.4/§4；供 Query 层翻成人话文案，Handler/Job 内部
// 只用这些稳定字符串）。
const (
	ReasonDeclarationCancelled     = "declaration_cancelled"
	ReasonGlobalKillSwitchOff      = "global_kill_switch_off"
	ReasonPlatformKillSwitchOff    = "platform_kill_switch_off"
	ReasonProbeCredentialMissing   = "probe_credential_missing"
	ReasonTargetHostNotAllowlisted = "target_host_not_allowlisted"
	ReasonDailyBudgetExhausted     = "daily_budget_exhausted"
	ReasonCooldownNotElapsed       = "cooldown_not_elapsed"
	ReasonPlatformRunInProgress    = "platform_run_in_progress"

	// racePrefix 标出 Work() 执行时刻复检才命中的拒绝（§3.2 步骤 2）：Action
	// 接受时到 Job 真正执行时之间的窗口里，有人关闭了开关或用光了预算。
	racePrefix = "race_"
)

// Limits 是探测的运行时闸值（ADR-019 决策·五的四层闸中，可配置的那两层）。
//
// 固定小 Prompt 与 max_tokens 硬上限（512）不在这里——它们不是"负责人可调
// 的运行参数"，是代码固化的安全边界（templates.go / MaxTokensHardCap）。
type Limits struct {
	// DailyBudgetPerPlatform 是每平台每日探测预算（按次数计，业务日边界
	// 重置）。默认 DefaultDailyBudgetPerPlatform，可用 XM_ASSURE_PROBE_
	// DAILY_BUDGET 覆盖（cmd/platform-api 与 cmd/platform-worker 各自读取
	// 同一个环境变量，两个进程独立解析）。
	DailyBudgetPerPlatform int
	// Cooldown 是同一声明连续两次"发起检测"之间的最短间隔。
	Cooldown time.Duration
}

// DefaultDailyBudgetPerPlatform / DefaultCooldown 是 Limits 的缺省值。
const (
	DefaultDailyBudgetPerPlatform = 50
	DefaultCooldown               = 60 * time.Second
)

// normalized 补齐零值。
func (l Limits) normalized() Limits {
	if l.DailyBudgetPerPlatform <= 0 {
		l.DailyBudgetPerPlatform = DefaultDailyBudgetPerPlatform
	}
	if l.Cooldown <= 0 {
		l.Cooldown = DefaultCooldown
	}
	return l
}
