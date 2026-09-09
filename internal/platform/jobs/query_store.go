package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// 本文件是「后台任务」管理页（XM-JOBS0）唯一的只读数据源。它直接对
// river_job / river_queue 发参数化 SELECT——River 是本平台自己数据库里的表，
// 宪法 27 条禁止的是任意 SQL 与第三方系统，不是对自己库里 River 表的只读查询
// （PROJECT-CONSTITUTION.md 红线口径）。本文件不修改 River 的任何 schema。
//
// **环境归属的诚实声明**：六个已注册周期任务的 Args（见 heartbeat.go /
// sub2api_sync.go / newapi_sync.go / cost_sync.go / retention.go /
// alert_evaluate.go）今天都只有 run_id 一个字段，没有一个携带 environment。
// staging 与 production 共用同一个物理数据库（表级用 environment 列隔离，
// 见 docs 记录），但 river_job 是 River 自带的 schema，我们不能加列。于是
// 这些维护型任务的运行记录**天然是平台级、跨环境的**，不是某个 environment
// 私有的。本文件按 args->>'environment' 做「软过滤」：字段存在就按调用者的
// environment 过滤，字段不存在（今天的全部六种）就照常返回——这与团队交接
// 里「心跳/清理没有 environment 字段时照常返回」的口径一致，只是把同一条
// 规则用到了全部任务上，因为验证下来它们现在全部符合「没有该字段」这一支。
// 等未来某个任务的 Args 真的加上 environment（例如按环境抓取的同步型任务），
// 这条软过滤会自动开始生效，不需要再改这个文件。

// RunState mirrors db/migrations/river 的 river_job_state 七个取值。定义成
// 本包自己的字符串类型，而不是依赖 River 内部包，是因为这七个值本身就是
// 对外契约的一部分（前端按它渲染徽章），不该随 River 内部类型改名而漂移。
type RunState string

const (
	RunStateAvailable RunState = "available"
	RunStateCancelled RunState = "cancelled"
	RunStateCompleted RunState = "completed"
	RunStateDiscarded RunState = "discarded"
	RunStateRetryable RunState = "retryable"
	RunStateRunning   RunState = "running"
	RunStateScheduled RunState = "scheduled"
)

// ValidRunStates 列出 river_job_state 接受的全部取值。httpapi 用它在拼 SQL
// 之前校验 ?state=，把一次会在数据库层报枚举转换错误的坏参数，变成一次
// 干净的 400（而不是泄漏成 500）。
func ValidRunStates() []RunState {
	return []RunState{
		RunStateAvailable, RunStateCancelled, RunStateCompleted,
		RunStateDiscarded, RunStateRetryable, RunStateRunning, RunStateScheduled,
	}
}

func isValidRunState(s RunState) bool {
	for _, v := range ValidRunStates() {
		if v == s {
			return true
		}
	}
	return false
}

// ErrInvalidRunState 是防御性的二道闸——真正面向调用方的校验在 httpapi
// 用 ValidRunStates() 完成；这里只防将来有别的调用方跳过那一层。
var ErrInvalidRunState = errors.New("jobs: unknown run state")

// runArgsAllowlist 是 river_job.args 里允许对外暴露的顶层字段。白名单制：
// 没列出的字段——包括未来任何人往 Args 加的字段——一律不透出，宁可漏显示
// 也不多泄漏（对照 requestSummaryItem「响应体是契约，不是结构体倒影」的
// 同一条纪律）。approval_envelope_sha256 只是哈希引用，不是凭据。
var runArgsAllowlist = map[string]struct{}{
	"environment":              {},
	"source":                   {},
	"business_day":             {},
	"run_id":                   {},
	"approval_envelope_sha256": {},
}

// maxRunErrorMessageBytes 界定单条错误文案能占的字节数。Worker 返回的错误
// 是自由文本（见各 Work() 实现），长度没有上限承诺。
const maxRunErrorMessageBytes = 500

// RunError 是最近一次失败尝试的截断投影。
type RunError struct {
	At             time.Time
	Message        string
	Truncated      bool
	OriginalLength int
}

// RunRecord 是一条 river_job 记录的只读投影，不是整行的直接序列化：
// Args 已按 runArgsAllowlist 过滤，错误文案已截断。
type RunRecord struct {
	ID          int64
	Kind        string
	Queue       string
	State       RunState
	Attempt     int
	MaxAttempts int
	CreatedAt   time.Time
	ScheduledAt time.Time
	AttemptedAt *time.Time
	FinalizedAt *time.Time
	ErrorCount  int
	LastError   *RunError
	Args        map[string]any
}

// DurationMS 只在任务已经跑完一次尝试（有 AttemptedAt）且已经终结
// （有 FinalizedAt）时才给值：一个还在跑的任务不该被算出一个每秒变大的
// 假耗时。
func (r RunRecord) DurationMS() *int64 {
	if r.AttemptedAt == nil || r.FinalizedAt == nil {
		return nil
	}
	d := r.FinalizedAt.Sub(*r.AttemptedAt).Milliseconds()
	if d < 0 {
		d = 0
	}
	return &d
}

// ScheduleActivity 是「定时任务」页签里对某个周期任务能诚实说出的活跃状态。
// **没有 Enabled 布尔量**：httpapi 与 platform-worker 是两个进程，httpapi
// 读不到 worker 手上那份 jobs.Config，任何「已启用/已停用」的断言都会是
// 装出来的事实（宪法 12 条禁止裸数字冒充实时完整数据，这里同理禁止裸结论
// 冒充配置真相）。Activity 只描述「数据库里看到了什么」。
type ScheduleActivity string

const (
	// ScheduleActivityObserved：在活跃窗口内看到过这个 kind 的新记录。
	ScheduleActivityObserved ScheduleActivity = "activity_observed"
	// ScheduleActivityStale：登记过运行，但已经超过活跃窗口没有新记录——
	// 可能是被关掉了，也可能是卡住了，本层不替你下结论，只如实报告。
	ScheduleActivityStale ScheduleActivity = "no_recent_activity"
	// ScheduleActivityNever：river_job 里一条这个 kind 的记录都没有。
	ScheduleActivityNever ScheduleActivity = "never_observed"
)

// ScheduleStatus 是一个已注册周期任务的目录项，用 river_job 里观测到的数据
// 做了尽量诚实的补充。
type ScheduleStatus struct {
	ID                string
	Kind              string
	Queue             string
	ScheduleConfigEnv string
	SideEffectClass   string
	LastRun           *RunRecord
	// ObservedIntervalSeconds 来自最近两次出现的时间差，是数据推出来的观测值，
	// 不是配置值——httpapi 进程读不到 worker 的 Config（见上）。只有一次或
	// 零次观测时为 nil。
	ObservedIntervalSeconds *int64
	// NextRunEstimatedAt = LastRun.CreatedAt + ObservedIntervalSeconds，
	// 同样是估计值，不是 River 调度器的真值。
	NextRunEstimatedAt *time.Time
	Activity           ScheduleActivity

	// --- XM-OPS-TAILS0：部署配置读数，补 XM-JOBS0 留的两处诚实缺口 ---
	//
	// 两组字段都可能为 nil/空——QueryStore 在没有接上对应依赖
	// （WithDeployedSchedules / WithConnectorConfigSource）时保持旧行为，
	// 不假装有数据。

	// Configured 是从 httpapi 进程自己的环境变量解析出的"部署声明"调度
	// （DeployedSchedulesFromEnv）。nil 表示 QueryStore 没有接这份数据源
	// （测试或未升级的装配路径），不是"未配置"。
	//
	// ⚠️ 这不是"worker 进程确认"：httpapi 与 worker 是两个进程，读不到
	// 对方内存里的 Config（同 XM-JOBS0 的既有诚实声明）。它是"httpapi 按
	// 与 worker 相同的规则解析同一份部署环境变量得到的结论"——两个进程在
	// 正常部署下共享同一份 .env / compose 环境变量，通常一致，但这不是
	// 一个可以替代"worker 真的在跑"这句话的证据。
	Configured *DeployedJobSchedule

	// ConfiguredMode 只对 sub2api_sync / newapi_sync 有意义（platform 概念
	// 来自 core.connector_config 的 CHECK 约束，只认 sub2api/newapi）；
	// 其它任务恒为 nil，代表"该任务没有 real/fake 这个维度"，不是"未知"。
	ConfiguredMode *ConfiguredModeStatus
}

// ConfiguredModeStatus 是某个平台在 core.connector_config 里的当前生效模式。
type ConfiguredModeStatus struct {
	// Mode 是 "real" 或 "fake"。库里没有这一行时按 credentials.Store.
	// ListConnectorConfigs 的既定口径视为 "fake"（"没在后台配过"是正常
	// 状态，Source 会标成 "default"，不是错误）。
	Mode string
	// Source: "database" 表示库里确实有这一行；"default" 表示没有配过、
	// 按 fake 兜底；"unavailable" 表示读库失败（QueryStore 不会因此让整个
	// Overview 报错，只在这一格如实说"读不到"）。
	Source string
}

// QueueBacklogRow 是一个队列的积压快照。Completed24h 只统计最近 24 小时——
// River 自带的 job cleaner 会定期清掉更早的已完成记录，统计更早的窗口只会
// 读到一个被清理策略随机截断的数字。
type QueueBacklogRow struct {
	Queue        string
	Available    int64
	Running      int64
	Retryable    int64
	Scheduled    int64
	Completed24h int64
	Discarded    int64
}

// WorkerHeartbeatStatus 是心跳周期任务（platform_heartbeat）最近一次出现的
// 投影。Environment 恒为 "platform"：心跳的 Args 里没有 environment 字段，
// 而且心跳回答的是「worker 进程活着没」，不是某个业务环境的问题。
type WorkerHeartbeatStatus struct {
	LastSeenAt  *time.Time
	SecondsAgo  *int64
	State       RunState
	Environment string
	// Known=false 表示 river_job 里从来没有出现过心跳记录（全新库、
	// 或心跳链路整条没起来），前端必须显示成「未知」而不是 0 秒前。
	Known bool
}

// Overview 是「后台任务」页概览需要的一次性快照：周期任务目录、队列积压、
// worker 心跳。
type Overview struct {
	Environment     string
	GeneratedAt     time.Time
	Schedules       []ScheduleStatus
	QueueBacklog    []QueueBacklogRow
	WorkerHeartbeat WorkerHeartbeatStatus
}

// ListRunsInput 是 /jobs/runs 的查询参数。State 为空表示不筛状态；Kinds 为空
// 表示不筛类型，非空则按「其中任意一个」匹配（前端「同步批次」页签要同时看
// sub2api_sync / newapi_sync / finance_cost_sync 三种，服务端一次查询做完，
// 好过前端拼三个游标各翻各的页）。
// Before=0 表示从最新的一条开始。
type ListRunsInput struct {
	Environment string
	Kinds       []string
	State       RunState
	Before      int64
	Limit       int32
}

// RunPage 是一页运行记录。NextBefore=0 表示已经翻到底（与 audit 事件分页
// 同一条约定：ListAuditEventsHandler 的 next_before）。
type RunPage struct {
	Items      []RunRecord
	NextBefore int64
}

const (
	// DefaultRunsLimit 是 limit 缺省值。
	DefaultRunsLimit int32 = 50
	// MaxRunsLimit 是 limit 的硬上限（团队交接明确要求「上限 200」）。
	MaxRunsLimit int32 = 200
)

// QueryStore 是「后台任务」页的只读仓储，只依赖连接池——不依赖 River 客户端，
// 因为它从不插入或修改任何任务，只读 river_job / river_queue。
type QueryStore struct {
	pool *pgxpool.Pool

	// deployedSchedules / connectorConfig 都是 XM-OPS-TAILS0 新增的可选
	// 依赖，零值（nil）时 Overview() 完全不填 Configured* 字段——旧的装配
	// 路径（比如现有测试直接 NewQueryStore(pool)）行为不变。
	deployedSchedules map[string]DeployedJobSchedule
	connectorConfig   ConnectorConfigSource
}

// QueryStoreOption 配置 QueryStore 的可选依赖。
type QueryStoreOption func(*QueryStore)

// WithDeployedSchedules 接上 DeployedSchedulesFromEnv 的结果，让 Overview()
// 在每个 schedule 目录项上补 Configured 字段。
func WithDeployedSchedules(schedules map[string]DeployedJobSchedule) QueryStoreOption {
	return func(s *QueryStore) { s.deployedSchedules = schedules }
}

// WithConnectorConfigSource 接上 core.connector_config 的读源（通常是
// NewPgConnectorConfigSource(pool)，带 30s 缓存），让 Overview() 在
// sub2api_sync / newapi_sync 两个 schedule 目录项上补 ConfiguredMode。
func WithConnectorConfigSource(source ConnectorConfigSource) QueryStoreOption {
	return func(s *QueryStore) { s.connectorConfig = source }
}

// NewQueryStore 创建仓储。opts 均可省略；不传时 Overview() 的
// Configured/ConfiguredMode 字段保持 nil（见两个字段各自的注释）。
func NewQueryStore(pool *pgxpool.Pool, opts ...QueryStoreOption) *QueryStore {
	s := &QueryStore{pool: pool}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// scheduleConfiguredMode 把 spec.Kind 映射到 core.connector_config 的
// platform 列。只有 sub2api_sync / newapi_sync 有对应平台——这与该表的
// CHECK 约束（platform IN ('sub2api','newapi')）逐字一致，不是本层新发明
// 的裁量。
func scheduleConfiguredModePlatform(kind string) (platform string, applicable bool) {
	switch kind {
	case Sub2APISyncJobKind:
		return ConnectorPlatformSub2API, true
	case NewAPISyncJobKind:
		return ConnectorPlatformNewAPI, true
	default:
		return "", false
	}
}

// scheduleActivityWindow 决定「多久没露面算失联」。取观测周期的 3 倍、
// 但不少于 1 小时：一次性抖动不该被读成「停了」，而生产最短周期（心跳，
// 默认 1 分钟）的 3 倍也才 3 分钟，对人工查看窗口来说仍然太敏感，所以设了
// 1 小时下限。
func scheduleActivityWindow(observedIntervalSeconds *int64) time.Duration {
	const floor = time.Hour
	if observedIntervalSeconds == nil || *observedIntervalSeconds <= 0 {
		return floor
	}
	w := time.Duration(*observedIntervalSeconds) * time.Second * 3
	if w < floor {
		return floor
	}
	return w
}

// Overview 汇总周期任务目录、队列积压与 worker 心跳。
func (s *QueryStore) Overview(ctx context.Context, environment string) (Overview, error) {
	if s == nil || s.pool == nil {
		return Overview{}, fmt.Errorf("jobs: query store has no pool")
	}
	specs := RegisteredPeriodicJobSpecs()
	kinds := make([]string, 0, len(specs))
	for _, spec := range specs {
		kinds = append(kinds, spec.Kind)
	}

	now := time.Now().UTC()
	recent, err := s.recentRunsByKind(ctx, kinds, environment, 2)
	if err != nil {
		return Overview{}, fmt.Errorf("jobs: overview recent runs: %w", err)
	}

	schedules := make([]ScheduleStatus, 0, len(specs))
	heartbeat := WorkerHeartbeatStatus{Environment: "platform"}
	for _, spec := range specs {
		rows := recent[spec.Kind]
		status := ScheduleStatus{
			ID: spec.ID, Kind: spec.Kind, Queue: spec.Queue,
			ScheduleConfigEnv: spec.ScheduleConfig, SideEffectClass: spec.SideEffectClass,
			Activity: ScheduleActivityNever,
		}
		if len(rows) > 0 {
			last := rows[0]
			status.LastRun = &last
			if len(rows) > 1 {
				interval := int64(last.CreatedAt.Sub(rows[1].CreatedAt).Seconds())
				if interval > 0 {
					status.ObservedIntervalSeconds = &interval
					next := last.CreatedAt.Add(time.Duration(interval) * time.Second)
					status.NextRunEstimatedAt = &next
				}
			}
			if now.Sub(last.CreatedAt) <= scheduleActivityWindow(status.ObservedIntervalSeconds) {
				status.Activity = ScheduleActivityObserved
			} else {
				status.Activity = ScheduleActivityStale
			}
		}

		if s.deployedSchedules != nil {
			if configured, ok := s.deployedSchedules[spec.ID]; ok {
				c := configured
				status.Configured = &c
			}
		}
		if s.connectorConfig != nil {
			if platform, applicable := scheduleConfiguredModePlatform(spec.Kind); applicable {
				status.ConfiguredMode = s.configuredModeFor(ctx, platform, environment)
			}
		}

		schedules = append(schedules, status)

		if spec.Kind == HeartbeatJobKind && status.LastRun != nil {
			at := status.LastRun.CreatedAt
			ago := int64(now.Sub(at).Seconds())
			if ago < 0 {
				ago = 0
			}
			heartbeat.LastSeenAt = &at
			heartbeat.SecondsAgo = &ago
			heartbeat.State = status.LastRun.State
			heartbeat.Known = true
		}
	}

	backlog, err := s.queueBacklog(ctx, now)
	if err != nil {
		return Overview{}, fmt.Errorf("jobs: overview queue backlog: %w", err)
	}

	return Overview{
		Environment: environment, GeneratedAt: now,
		Schedules: schedules, QueueBacklog: backlog, WorkerHeartbeat: heartbeat,
	}, nil
}

// configuredModeFor 读一次 core.connector_config（经 s.connectorConfig，
// 通常带 30s 缓存，见 NewPgConnectorConfigSource）。读库失败**不让整个
// Overview() 报错**——这是一个附加在已有目录项上的补充事实，不是
// 主查询；失败时如实标 "unavailable"，其余格子照常返回。
func (s *QueryStore) configuredModeFor(ctx context.Context, platform, environment string) *ConfiguredModeStatus {
	row, err := s.connectorConfig.Get(ctx, platform, environment)
	if err != nil {
		return &ConfiguredModeStatus{Source: "unavailable"}
	}
	if row == nil {
		// 没有这一行 = 没在后台配过，按既定口径视为 fake
		// （credentials.Store.ListConnectorConfigs 的同一条注释；
		// core.connector_config.mode 的 CHECK 约束只接受 'fake'/'real'
		// 两个裸字符串，这里直接用字面量而不是借用某个平台专属的
		// Sub2APIMode/NewAPIMode 类型常量——那两个是给客户端工厂用的
		// 类型化枚举，不是这张表的字面量）。
		return &ConfiguredModeStatus{Mode: "fake", Source: "default"}
	}
	return &ConfiguredModeStatus{Mode: row.Mode, Source: "database"}
}

var recentRunsByKindSQL = fmt.Sprintf(`
SELECT id, kind, queue, state, attempt, max_attempts,
       created_at, scheduled_at, attempted_at, finalized_at,
       to_jsonb(coalesce(errors, ARRAY[]::jsonb[])) AS errors_json,
       coalesce(args, '{}'::jsonb) AS args_json
FROM (
    SELECT id, kind, queue, state, attempt, max_attempts,
           created_at, scheduled_at, attempted_at, finalized_at, errors, args,
           row_number() OVER (
               PARTITION BY kind ORDER BY created_at DESC, id DESC
           ) AS rn
    FROM river_job
    WHERE kind = ANY($1::text[])
      AND %s
) ranked
WHERE rn <= $3::int
ORDER BY kind, rn;
`, fmt.Sprintf(riverJobEnvironmentPredicate, "$2"))

// recentRunsByKind 返回每个 kind 最近 perKind 条记录，按 (kind, 最新在前) 排序。
func (s *QueryStore) recentRunsByKind(
	ctx context.Context, kinds []string, environment string, perKind int32,
) (map[string][]RunRecord, error) {
	if len(kinds) == 0 {
		return map[string][]RunRecord{}, nil
	}
	rows, err := s.pool.Query(ctx, recentRunsByKindSQL, kinds, environment, perKind)
	if err != nil {
		return nil, fmt.Errorf("jobs: recent runs by kind: %w", err)
	}
	records, err := scanRunRecords(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]RunRecord, len(kinds))
	for _, r := range records {
		out[r.Kind] = append(out[r.Kind], r)
	}
	return out, nil
}

const queueBacklogSQL = `
SELECT queue, state::text, count(*)
FROM river_job
WHERE state::text <> 'completed' OR finalized_at >= $1
GROUP BY queue, state;
`

// queueBacklog 汇总各队列在各状态下的行数。已完成只统计最近 24 小时
// （见 QueueBacklogRow 的注释）。default / maintenance 两个已知队列
// 总是出现在结果里，哪怕当前一行都没有——没有数据不代表队列不存在。
func (s *QueryStore) queueBacklog(ctx context.Context, now time.Time) ([]QueueBacklogRow, error) {
	cutoff := now.Add(-24 * time.Hour)
	rows, err := s.pool.Query(ctx, queueBacklogSQL, cutoff)
	if err != nil {
		return nil, fmt.Errorf("jobs: queue backlog: %w", err)
	}
	defer rows.Close()

	byQueue := map[string]*QueueBacklogRow{
		river.QueueDefault: {Queue: river.QueueDefault},
		QueueMaintenance:   {Queue: QueueMaintenance},
	}
	order := []string{river.QueueDefault, QueueMaintenance}

	for rows.Next() {
		var queue, state string
		var n int64
		if err := rows.Scan(&queue, &state, &n); err != nil {
			return nil, fmt.Errorf("jobs: scan queue backlog row: %w", err)
		}
		row, ok := byQueue[queue]
		if !ok {
			row = &QueueBacklogRow{Queue: queue}
			byQueue[queue] = row
			order = append(order, queue)
		}
		switch RunState(state) {
		case RunStateAvailable:
			row.Available = n
		case RunStateRunning:
			row.Running = n
		case RunStateRetryable:
			row.Retryable = n
		case RunStateScheduled:
			row.Scheduled = n
		case RunStateCompleted:
			row.Completed24h = n
		case RunStateDiscarded:
			row.Discarded = n
			// cancelled 不在团队交接要求的六个计数里，故意不收集。
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: iterate queue backlog rows: %w", err)
	}

	out := make([]QueueBacklogRow, 0, len(order))
	for _, q := range order {
		out = append(out, *byQueue[q])
	}
	return out, nil
}

// FailedRunSummaryWindow 是失败作业摘要的回看窗口。
//
// 24 小时与 queueBacklog 的「已完成只算最近 24 小时」同一个口径：待处理清单
// 问的是「今天要处理什么」，三天前那一批要么已经处理了、要么还在今天的窗口
// 里继续产生新的失败行。
const FailedRunSummaryWindow = 24 * time.Hour

// FailedRunSummary 是某一类作业在窗口内的失败摘要。
//
// 存在的理由直接来自 2026-09-08 的现场：card_sync 在 24 小时里产生了 288 条
// discarded，而管理端「我的待处理」只取最新 20 条失败作业、不分类型也不合并
// 同类——于是这 288 条把那一格全占满，真正需要人处理的东西被挤出首屏。
// 按 kind 合并之后，那 288 条是**一行**：「card_sync 失败 288 次，最早
// 09-05 10:27，最近 …，上游说 …」。
type FailedRunSummary struct {
	Kind  string
	Count int64
	// FirstAt / LastAt 是窗口内最早与最晚一次失败的时刻（UTC）。
	// 两个都留：只留其一就答不出「这是刚坏的还是坏了三天」。
	FirstAt time.Time
	LastAt  time.Time
	// LastRunID 是最近那一条失败作业的 id，供前端跳到 /jobs/runs 定位。
	LastRunID int64
	// LastError 是**最近那一次尝试**写下的错误（已按 maxRunErrorMessageBytes
	// 截断）。取最近而不是最早：取错方向时人看到的是三天前那句话。
	LastError *RunError
	// ErrorCount 是最近那条作业的尝试次数。
	ErrorCount int
}

// riverJobEnvironmentPredicate 是「这条作业属于本环境吗」的唯一写法。
//
// 抽成一处而不是在每条 SQL 里各写一遍：它是一条**规则**（args 里没有
// environment 的旧行视为本环境，有就必须相等），三条查询各写一遍的话，
// 改一处不会带动另外两处，而那种漂移的后果是「某一个视图悄悄多出/少掉
// 别的环境的作业」。占位符位置由各查询自己编号，所以这里留一个 %s。
//
// 三个使用点：listRunsSQL（$4）、recentRunsByKindSQL（$2）、
// failedRunSummaryByKindSQL（$2）。
const riverJobEnvironmentPredicate = `(args->>'environment' IS NULL OR args->>'environment' = %s::text)`

// failedRunSummaryByKindSQL 按 kind 汇总窗口内的 discarded 作业。
//
// 三处照抄既有纪律：
//  1. `state::text = 'discarded'` 而不是枚举字面量——策略里 xm_api_runtime 对
//     public.river_job_state 没有 USAGE，listRunsSQL 与 queueBacklogSQL 都是
//     这么写的。
//  2. 只收 discarded：retryable 会自己消失、cancelled 是人主动取消，
//     与前端「我的待处理」写下的理由保持同一口径，不在后端另立一套。
//  3. 时刻取 coalesce(finalized_at, attempted_at, created_at)——discarded 行
//     必有 finalized_at（River 的约束），后两个只是防御。
var failedRunSummaryByKindSQL = fmt.Sprintf(`
WITH failed AS (
    SELECT id, kind,
           coalesce(finalized_at, attempted_at, created_at) AS at,
           to_jsonb(coalesce(errors, ARRAY[]::jsonb[])) AS errors_json
    FROM river_job
    WHERE state::text = 'discarded'
      AND coalesce(finalized_at, attempted_at, created_at) >= $1
      AND %s
), agg AS (
    SELECT kind, count(*) AS failure_count, min(at) AS first_at, max(at) AS last_at
    FROM failed GROUP BY kind
)
SELECT a.kind, a.failure_count, a.first_at, a.last_at, l.id, l.errors_json
FROM agg a
JOIN LATERAL (
    SELECT f.id, f.errors_json FROM failed f
    WHERE f.kind = a.kind
    ORDER BY f.at DESC, f.id DESC
    LIMIT 1
) l ON true
ORDER BY a.failure_count DESC, a.kind;
`, fmt.Sprintf(riverJobEnvironmentPredicate, "$2"))

// FailedRunSummaryByKind 返回窗口内每一类作业的失败摘要，失败最多的在前。
//
// 按 count 降序：待处理清单的首屏要先看到最多的那一类。同 count 时按 kind
// 升序，让结果稳定可比对（日志、测试、截图）。
func (s *QueryStore) FailedRunSummaryByKind(
	ctx context.Context, environment string, since time.Time,
) ([]FailedRunSummary, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("jobs: query store has no pool")
	}
	rows, err := s.pool.Query(ctx, failedRunSummaryByKindSQL, since.UTC(), environment)
	if err != nil {
		return nil, fmt.Errorf("jobs: failed run summary by kind: %w", err)
	}
	defer rows.Close()

	out := []FailedRunSummary{}
	for rows.Next() {
		var (
			kind            string
			count           int64
			firstAt, lastAt time.Time
			lastID          int64
			errorsJSON      []byte
		)
		if err := rows.Scan(&kind, &count, &firstAt, &lastAt, &lastID, &errorsJSON); err != nil {
			return nil, fmt.Errorf("jobs: scan failed run summary row: %w", err)
		}
		lastErr, attempts, decodeErr := decodeLastError(errorsJSON)
		if decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, FailedRunSummary{
			Kind:       kind,
			Count:      count,
			FirstAt:    firstAt.UTC(),
			LastAt:     lastAt.UTC(),
			LastRunID:  lastID,
			LastError:  lastErr,
			ErrorCount: attempts,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: iterate failed run summary rows: %w", err)
	}
	return out, nil
}

var listRunsSQL = fmt.Sprintf(`
SELECT id, kind, queue, state, attempt, max_attempts,
       created_at, scheduled_at, attempted_at, finalized_at,
       to_jsonb(coalesce(errors, ARRAY[]::jsonb[])) AS errors_json,
       coalesce(args, '{}'::jsonb) AS args_json
FROM river_job
WHERE (cardinality($1::text[]) = 0 OR kind = ANY($1::text[]))
  AND ($2::text = '' OR state::text = $2::text)
  AND ($3::bigint = 0 OR id < $3::bigint)
  AND %s
ORDER BY id DESC
LIMIT $5::int;
`, fmt.Sprintf(riverJobEnvironmentPredicate, "$4"))

// ListRuns 按可选的 kinds / state 游标分页列出最近的运行记录，按 id 降序
// （最新在前）。游标是上一页最后一条的 id；state 必须是空或
// ValidRunStates() 之一，否则报错而不是让数据库枚举转换失败泄漏成 500。
func (s *QueryStore) ListRuns(ctx context.Context, in ListRunsInput) (RunPage, error) {
	if s == nil || s.pool == nil {
		return RunPage{}, fmt.Errorf("jobs: query store has no pool")
	}
	if in.State != "" && !isValidRunState(in.State) {
		return RunPage{}, fmt.Errorf("%w: %q", ErrInvalidRunState, in.State)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultRunsLimit
	}
	if limit > MaxRunsLimit {
		limit = MaxRunsLimit
	}
	if in.Before < 0 {
		return RunPage{}, fmt.Errorf("jobs: before must be non-negative, got %d", in.Before)
	}

	kinds := make([]string, 0, len(in.Kinds))
	for _, k := range in.Kinds {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, k)
		}
	}

	// 多取一行，专门用来判断是否还有下一页（与 ops.Store.ListSamples 同一条
	// 纪律：见该方法注释——静默截断比多一次查询更危险）。
	rows, err := s.pool.Query(ctx, listRunsSQL,
		kinds, string(in.State), in.Before, in.Environment, limit+1)
	if err != nil {
		return RunPage{}, fmt.Errorf("jobs: list runs: %w", err)
	}
	records, err := scanRunRecords(rows)
	if err != nil {
		return RunPage{}, err
	}

	var next int64
	if int32(len(records)) > limit {
		records = records[:limit]
		next = records[len(records)-1].ID
	}
	return RunPage{Items: records, NextBefore: next}, nil
}

// scanRunRecords 消费并关闭 rows，是 recentRunsByKind 与 ListRuns 共用的
// 唯一解码路径——两条查询的列顺序逐字相同。
func scanRunRecords(rows pgx.Rows) ([]RunRecord, error) {
	defer rows.Close()
	var out []RunRecord
	for rows.Next() {
		var (
			id                       int64
			kind, queue, state       string
			attempt, maxAttempts     int
			createdAt, scheduledAt   time.Time
			attemptedAt, finalizedAt *time.Time
			errorsJSON, argsJSON     []byte
		)
		if err := rows.Scan(&id, &kind, &queue, &state, &attempt, &maxAttempts,
			&createdAt, &scheduledAt, &attemptedAt, &finalizedAt, &errorsJSON, &argsJSON); err != nil {
			return nil, fmt.Errorf("jobs: scan river_job row: %w", err)
		}
		record := RunRecord{
			ID: id, Kind: kind, Queue: queue, State: RunState(state),
			Attempt: attempt, MaxAttempts: maxAttempts,
			CreatedAt: createdAt.UTC(), ScheduledAt: scheduledAt.UTC(),
		}
		if attemptedAt != nil {
			t := attemptedAt.UTC()
			record.AttemptedAt = &t
		}
		if finalizedAt != nil {
			t := finalizedAt.UTC()
			record.FinalizedAt = &t
		}
		lastErr, count, err := decodeLastError(errorsJSON)
		if err != nil {
			return nil, err
		}
		record.LastError = lastErr
		record.ErrorCount = count
		args, err := decodeAllowedArgs(argsJSON)
		if err != nil {
			return nil, err
		}
		record.Args = args
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: iterate river_job rows: %w", err)
	}
	return out, nil
}

// decodeLastError 解出最近一次尝试的错误，并把文案截到
// maxRunErrorMessageBytes。用 rivertype.AttemptError 而不是自己重新声明一份
// 字段，是为了让 JSON 形状永远与 River 驱动实际写入的一致（json tag
// at/attempt/error/trace，见 rivertype.AttemptError）。
func decodeLastError(raw []byte) (*RunError, int, error) {
	var attempts []rivertype.AttemptError
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &attempts); err != nil {
			return nil, 0, fmt.Errorf("jobs: decode errors: %w", err)
		}
	}
	if len(attempts) == 0 {
		return nil, 0, nil
	}
	last := attempts[len(attempts)-1]
	msg, truncated := safeTruncateUTF8(last.Error, maxRunErrorMessageBytes)
	return &RunError{
		At: last.At.UTC(), Message: msg, Truncated: truncated,
		OriginalLength: len(last.Error),
	}, len(attempts), nil
}

// decodeAllowedArgs 只保留 runArgsAllowlist 里的顶层字段——绝不把整段
// args 原样吐给调用方（团队交接的明确红线）。
func decodeAllowedArgs(raw []byte) (map[string]any, error) {
	full := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &full); err != nil {
			return nil, fmt.Errorf("jobs: decode args: %w", err)
		}
	}
	out := map[string]any{}
	for k, v := range full {
		if _, ok := runArgsAllowlist[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// safeTruncateUTF8 截断到至多 maxBytes 字节，且绝不切碎一个多字节 UTF-8
// 字符——错误文案里可能有中文（本仓不少 fmt.Errorf 是中文文案）。
func safeTruncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	b := s[:maxBytes]
	for len(b) > 0 {
		r, size := utf8.DecodeLastRuneInString(b)
		if r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b, true
}
