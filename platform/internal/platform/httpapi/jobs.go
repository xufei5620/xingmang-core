package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// JobsQuerier 是「后台任务」页的只读能力（*jobs.QueryStore 满足）。
//
// FailedRunSummaryByKind 不被本文件的两个 handler 使用，它服务于
// /ops/overview 的 failed_jobs_by_kind（XM-OPS-TRUTH 子片 B）。放在这个接口里
// 而不是另立一个：Deps.Jobs 只有一个实现（*jobs.QueryStore），多一个接口只会
// 让装配处多一次类型断言，而断言失败是静默的——那正是「点了没生效但不报错」
// 那一类失效。
type JobsQuerier interface {
	Overview(ctx context.Context, environment string) (jobs.Overview, error)
	ListRuns(ctx context.Context, in jobs.ListRunsInput) (jobs.RunPage, error)
	FailedRunSummaryByKind(ctx context.Context, environment string, since time.Time) ([]jobs.FailedRunSummary, error)
}

// jobRunErrorBody 是最近一次失败尝试的对外表示，已经过截断
// （jobs.QueryStore.decodeLastError）。
type jobRunErrorBody struct {
	At             string `json:"at"`
	Message        string `json:"message"`
	Truncated      bool   `json:"truncated"`
	OriginalLength int    `json:"original_length"`
}

// jobRunItem 是一条 river_job 记录的对外表示。**不是 jobs.RunRecord 的直接
// 序列化**：Args 已经在仓储层按白名单过滤过，这里只是原样转发那个已经安全的
// map，不再二次过滤——过滤只有一处，双写会漂开（对照 requestSummaryItem 的
// 同一条纪律）。
type jobRunItem struct {
	ID          int64   `json:"id"`
	Kind        string  `json:"kind"`
	Queue       string  `json:"queue"`
	State       string  `json:"state"`
	Attempt     int     `json:"attempt"`
	MaxAttempts int     `json:"max_attempts"`
	CreatedAt   string  `json:"created_at"`
	ScheduledAt string  `json:"scheduled_at"`
	AttemptedAt *string `json:"attempted_at"`
	FinalizedAt *string `json:"finalized_at"`
	// DurationMS 为 null 表示还没有一次完整的尝试可以算耗时——不是 0 秒。
	DurationMS *int64           `json:"duration_ms"`
	ErrorCount int              `json:"error_count"`
	LastError  *jobRunErrorBody `json:"last_error"`
	// Args 只含白名单字段（environment/source/business_day/run_id/
	// approval_envelope_sha256 之类），绝不是整段 River args。
	Args map[string]any `json:"args"`
}

type jobRunPage struct {
	Items []jobRunItem `json:"items"`
	// NextBefore=0 表示已经翻到底；否则是下一页的 before 游标
	// （与 /api/v1/audit/events 的 next_before 同一条约定）。
	NextBefore int64 `json:"next_before"`
}

type jobScheduleBody struct {
	ID                string      `json:"id"`
	Kind              string      `json:"kind"`
	Queue             string      `json:"queue"`
	ScheduleConfigEnv string      `json:"schedule_config_env"`
	SideEffectClass   string      `json:"side_effect_class"`
	LastRun           *jobRunItem `json:"last_run"`
	// ObservedIntervalSeconds / NextRunEstimatedAt 是从最近两次出现的时间差
	// 推出来的观测值，**不是配置值**——本进程读不到 worker 的 jobs.Config
	// （见 jobs.QueryStore 文件头注释）。两者皆 null 表示观测不足两次。
	ObservedIntervalSeconds *int64  `json:"observed_interval_seconds"`
	NextRunEstimatedAt      *string `json:"next_run_estimated_at"`
	// Activity: activity_observed / no_recent_activity / never_observed。
	// 没有 enabled 字段——那会是一个装出来的事实，见上。
	Activity string `json:"activity"`

	// --- XM-OPS-TAILS0 ---

	// ConfiguredEnabled / ConfiguredIntervalSeconds / ConfiguredSource 是
	// httpapi 自己按与 worker 相同的规则解析部署环境变量得到的"应然"调度
	// （jobs.DeployedSchedulesFromEnv）。null 表示本次装配没有接这份数据源
	// （见 jobs.ScheduleStatus.Configured 的注释）——**不是** worker 进程的
	// 实时确认，两者的区别必须原样透给前端，不能被字段名"configured"暗示成
	// "已核实在跑"。
	ConfiguredEnabled         *bool   `json:"configured_enabled"`
	ConfiguredIntervalSeconds *int64  `json:"configured_interval_seconds"`
	ConfiguredSource          *string `json:"configured_source"`

	// ConfiguredMode / ConfiguredModeSource 只在 sub2api_sync / newapi_sync
	// 两个 kind 上非 null——real/fake 是 core.connector_config 的 platform
	// 维度，其余任务没有这个概念（见 jobs.ScheduleStatus.ConfiguredMode）。
	// ModeSource: "database"（库里确有这一行）/ "default"（没配过，按 fake
	// 兜底）/ "unavailable"（读库失败，此时 ConfiguredMode 也是 null）。
	ConfiguredMode       *string `json:"configured_mode"`
	ConfiguredModeSource *string `json:"configured_mode_source"`
}

type jobQueueBacklogBody struct {
	Queue     string `json:"queue"`
	Available int64  `json:"available"`
	Running   int64  `json:"running"`
	Retryable int64  `json:"retryable"`
	Scheduled int64  `json:"scheduled"`
	// Completed24h 只统计最近 24 小时（River 自带的 job cleaner 会清掉更早的
	// 已完成记录，统计更早窗口只会读到一个被清理策略随手截断的数字）。
	Completed24h int64 `json:"completed_24h"`
	Discarded    int64 `json:"discarded"`
}

type jobWorkerHeartbeatBody struct {
	LastSeenAt *string `json:"last_seen_at"`
	SecondsAgo *int64  `json:"seconds_ago"`
	State      string  `json:"state"`
	// Environment 恒为 "platform"：心跳没有按 environment 拆分，见
	// jobs.WorkerHeartbeatStatus 的注释。
	Environment string `json:"environment"`
	// Known=false 表示 river_job 里从未出现过心跳记录，前端必须显示成
	// 「未知」而不是「0 秒前」。
	Known bool `json:"known"`
}

type jobsOverviewBody struct {
	Environment     string                 `json:"environment"`
	GeneratedAt     string                 `json:"generated_at"`
	Schedules       []jobScheduleBody      `json:"schedules"`
	QueueBacklog    []jobQueueBacklogBody  `json:"queue_backlog"`
	WorkerHeartbeat jobWorkerHeartbeatBody `json:"worker_heartbeat"`
}

func toJobRunItem(r jobs.RunRecord) jobRunItem {
	args := r.Args
	if args == nil {
		args = map[string]any{}
	}
	item := jobRunItem{
		ID: r.ID, Kind: r.Kind, Queue: r.Queue, State: string(r.State),
		Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
		// 时间一律 UTC（宪法 14 条），本地化交给前端。
		CreatedAt:   r.CreatedAt.UTC().Format(time.RFC3339),
		ScheduledAt: r.ScheduledAt.UTC().Format(time.RFC3339),
		AttemptedAt: rfc3339Ptr(r.AttemptedAt),
		FinalizedAt: rfc3339Ptr(r.FinalizedAt),
		DurationMS:  r.DurationMS(),
		ErrorCount:  r.ErrorCount,
		Args:        args,
	}
	if r.LastError != nil {
		item.LastError = &jobRunErrorBody{
			At:             r.LastError.At.UTC().Format(time.RFC3339),
			Message:        r.LastError.Message,
			Truncated:      r.LastError.Truncated,
			OriginalLength: r.LastError.OriginalLength,
		}
	}
	return item
}

func toOverviewBody(o jobs.Overview) jobsOverviewBody {
	schedules := make([]jobScheduleBody, 0, len(o.Schedules))
	for _, sch := range o.Schedules {
		body := jobScheduleBody{
			ID: sch.ID, Kind: sch.Kind, Queue: sch.Queue,
			ScheduleConfigEnv:       sch.ScheduleConfigEnv,
			SideEffectClass:         sch.SideEffectClass,
			ObservedIntervalSeconds: sch.ObservedIntervalSeconds,
			NextRunEstimatedAt:      rfc3339Ptr(sch.NextRunEstimatedAt),
			Activity:                string(sch.Activity),
		}
		if sch.LastRun != nil {
			item := toJobRunItem(*sch.LastRun)
			body.LastRun = &item
		}
		if sch.Configured != nil {
			enabled := sch.Configured.Enabled
			seconds := sch.Configured.IntervalSeconds
			source := sch.Configured.EnabledSource
			body.ConfiguredEnabled = &enabled
			body.ConfiguredIntervalSeconds = &seconds
			body.ConfiguredSource = &source
		}
		if sch.ConfiguredMode != nil {
			source := sch.ConfiguredMode.Source
			body.ConfiguredModeSource = &source
			// unavailable 时 Mode 本身是空字符串（读库失败，没有可信的模式
			// 值）——留 null 而不是回一个看着合法实际是空串的 "mode":""。
			if sch.ConfiguredMode.Source != "unavailable" {
				mode := sch.ConfiguredMode.Mode
				body.ConfiguredMode = &mode
			}
		}
		schedules = append(schedules, body)
	}

	backlog := make([]jobQueueBacklogBody, 0, len(o.QueueBacklog))
	for _, b := range o.QueueBacklog {
		backlog = append(backlog, jobQueueBacklogBody{
			Queue: b.Queue, Available: b.Available, Running: b.Running,
			Retryable: b.Retryable, Scheduled: b.Scheduled,
			Completed24h: b.Completed24h, Discarded: b.Discarded,
		})
	}

	return jobsOverviewBody{
		Environment:  o.Environment,
		GeneratedAt:  o.GeneratedAt.UTC().Format(time.RFC3339),
		Schedules:    schedules,
		QueueBacklog: backlog,
		WorkerHeartbeat: jobWorkerHeartbeatBody{
			LastSeenAt:  rfc3339Ptr(o.WorkerHeartbeat.LastSeenAt),
			SecondsAgo:  o.WorkerHeartbeat.SecondsAgo,
			State:       string(o.WorkerHeartbeat.State),
			Environment: o.WorkerHeartbeat.Environment,
			Known:       o.WorkerHeartbeat.Known,
		},
	}
}

// parseKindsParam 解析 ?kind=a,b,c——「同步批次」页签要同时看
// sub2api_sync/newapi_sync/finance_cost_sync 三种，逗号分隔让前端一次请求
// 拿到服务端正确分页的合并结果，而不必自己拼三条游标各翻各的页。
// kind 在 river_job 里是自由文本，这里不校验白名单：拼错的 kind 只会让
// 那一段过滤条件查不到行，不是一个需要 400 的错误。
func parseKindsParam(r *http.Request) []string {
	raw := strings.TrimSpace(r.URL.Query().Get("kind"))
	if raw == "" {
		return nil
	}
	var kinds []string
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

// parseJobStateParam 校验 ?state=，非法值报 400 而不是让它一路传到数据库
// 枚举转换失败（那会泄漏成 500，见 jobs.QueryStore.ListRuns 的同一条纪律）。
func parseJobStateParam(r *http.Request) (jobs.RunState, error) {
	raw := jobs.RunState(strings.TrimSpace(r.URL.Query().Get("state")))
	if raw == "" {
		return "", nil
	}
	for _, v := range jobs.ValidRunStates() {
		if v == raw {
			return raw, nil
		}
	}
	return "", action.NewError(action.CodeInvalidParams,
		"state 必须是 available/cancelled/completed/discarded/retryable/running/scheduled 之一，或留空", nil)
}

// JobsOverviewHandler 返回周期任务目录、队列积压与 worker 心跳的一次性快照。
//
// 环境范围由 resolveEnvironment 决定（不传用调用者自己的，传了必须一致），
// 与 /api/v1/metrics 同一条规则。**这不是说这批数据本身按环境隔离**——
// 六个已注册周期任务的 Args 今天都没有 environment 字段，river_job 是
// 平台级共享表，见 jobs.QueryStore 文件头注释；environment 参数在这里的
// 唯一作用是保持与其他端点一致的调用约定，并在未来某个任务真的按环境
// 写入时自动开始生效。
//
// 权限（ops.ScopeRead）由路由上的 RequireScope 判定。
func JobsOverviewHandler(q JobsQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		overview, err := q.Overview(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, toOverviewBody(overview))
	}
}

// ListJobRunsHandler 按 kind / state 游标分页列出最近的运行记录。
//
// 分页：`before` 是游标（上一页最后一条的 river_job.id），`limit` 默认
// jobs.DefaultRunsLimit、上限 jobs.MaxRunsLimit。响应的 `next_before` = 本页
// 最后一条的 id；本页条数少于请求的 limit 说明已经翻到底，返回 0
// （与 /api/v1/audit/events 完全同一条约定）。
//
// 权限（ops.ScopeRead）由路由上的 RequireScope 判定。
func ListJobRunsHandler(q JobsQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		state, err := parseJobStateParam(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		limit, err := parseInt64Param(r, "limit", int64(jobs.DefaultRunsLimit))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if limit <= 0 {
			limit = int64(jobs.DefaultRunsLimit)
		}
		if limit > int64(jobs.MaxRunsLimit) {
			limit = int64(jobs.MaxRunsLimit)
		}
		before, err := parseInt64Param(r, "before", 0)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		page, err := q.ListRuns(r.Context(), jobs.ListRunsInput{
			Environment: string(env),
			Kinds:       parseKindsParam(r),
			State:       state,
			Before:      before,
			Limit:       int32(limit),
		})
		if err != nil {
			WriteError(w, r, err)
			return
		}

		items := make([]jobRunItem, 0, len(page.Items))
		for _, rec := range page.Items {
			items = append(items, toJobRunItem(rec))
		}
		WriteJSON(w, http.StatusOK, jobRunPage{Items: items, NextBefore: page.NextBefore})
	}
}
