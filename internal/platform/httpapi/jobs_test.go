package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

// fakeJobsQuerier 记录最近一次调用的参数，供断言 handler 是否正确把
// HTTP 查询参数翻译成了 jobs.ListRunsInput / 环境字符串。
type fakeJobsQuerier struct {
	overview       jobs.Overview
	overviewErr    error
	gotOverviewEnv string

	page      jobs.RunPage
	listErr   error
	gotListIn jobs.ListRunsInput

	summaries     []jobs.FailedRunSummary
	summaryErr    error
	gotSummaryEnv string
	// gotSummarySince 让用例能断言窗口是服务端算的、不是调用方传的。
	gotSummarySince time.Time
}

func (f *fakeJobsQuerier) FailedRunSummaryByKind(
	_ context.Context, environment string, since time.Time,
) ([]jobs.FailedRunSummary, error) {
	f.gotSummaryEnv = environment
	f.gotSummarySince = since
	return f.summaries, f.summaryErr
}

func (f *fakeJobsQuerier) Overview(_ context.Context, environment string) (jobs.Overview, error) {
	f.gotOverviewEnv = environment
	return f.overview, f.overviewErr
}

func (f *fakeJobsQuerier) ListRuns(_ context.Context, in jobs.ListRunsInput) (jobs.RunPage, error) {
	f.gotListIn = in
	return f.page, f.listErr
}

func TestJobsOverviewHandlerReturnsSnapshot(t *testing.T) {
	generatedAt := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	lastRunAt := generatedAt.Add(-30 * time.Second)
	attemptedAt := lastRunAt
	finalizedAt := lastRunAt.Add(200 * time.Millisecond)
	interval := int64(60)
	nextRun := lastRunAt.Add(60 * time.Second)
	secondsAgo := int64(30)

	q := &fakeJobsQuerier{overview: jobs.Overview{
		Environment: "development", GeneratedAt: generatedAt,
		Schedules: []jobs.ScheduleStatus{{
			ID: jobs.HeartbeatJobKind, Kind: jobs.HeartbeatJobKind, Queue: jobs.QueueMaintenance,
			ScheduleConfigEnv: "HEARTBEAT_INTERVAL", SideEffectClass: "audit_append",
			LastRun: &jobs.RunRecord{
				ID: 42, Kind: jobs.HeartbeatJobKind, Queue: jobs.QueueMaintenance,
				State: jobs.RunStateCompleted, Attempt: 1, MaxAttempts: 3,
				CreatedAt: lastRunAt, ScheduledAt: lastRunAt,
				AttemptedAt: &attemptedAt, FinalizedAt: &finalizedAt,
				Args: map[string]any{"run_id": "abc"},
			},
			ObservedIntervalSeconds: &interval, NextRunEstimatedAt: &nextRun,
			Activity: jobs.ScheduleActivityObserved,
		}},
		QueueBacklog: []jobs.QueueBacklogRow{
			{Queue: "default", Available: 1, Running: 2, Retryable: 3, Scheduled: 4, Completed24h: 5, Discarded: 6},
		},
		WorkerHeartbeat: jobs.WorkerHeartbeatStatus{
			LastSeenAt: &lastRunAt, SecondsAgo: &secondsAgo,
			State: jobs.RunStateCompleted, Environment: "platform", Known: true,
		},
	}}
	h := testRouterWithJobs(t, q)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/overview", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if q.gotOverviewEnv != "development" {
		t.Fatalf("environment passed to store = %q, want %q", q.gotOverviewEnv, "development")
	}

	var got struct {
		Environment string `json:"environment"`
		GeneratedAt string `json:"generated_at"`
		Schedules   []struct {
			ID                      string  `json:"id"`
			Kind                    string  `json:"kind"`
			ScheduleConfigEnv       string  `json:"schedule_config_env"`
			ObservedIntervalSeconds *int64  `json:"observed_interval_seconds"`
			NextRunEstimatedAt      *string `json:"next_run_estimated_at"`
			Activity                string  `json:"activity"`
			LastRun                 *struct {
				ID         int64          `json:"id"`
				DurationMS *int64         `json:"duration_ms"`
				Args       map[string]any `json:"args"`
			} `json:"last_run"`
		} `json:"schedules"`
		QueueBacklog []struct {
			Queue        string `json:"queue"`
			Available    int64  `json:"available"`
			Completed24h int64  `json:"completed_24h"`
		} `json:"queue_backlog"`
		WorkerHeartbeat struct {
			SecondsAgo  *int64 `json:"seconds_ago"`
			Environment string `json:"environment"`
			Known       bool   `json:"known"`
		} `json:"worker_heartbeat"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}

	if got.Environment != "development" {
		t.Fatalf("environment = %q", got.Environment)
	}
	if len(got.Schedules) != 1 || got.Schedules[0].Kind != jobs.HeartbeatJobKind {
		t.Fatalf("schedules = %+v", got.Schedules)
	}
	sch := got.Schedules[0]
	if sch.ObservedIntervalSeconds == nil || *sch.ObservedIntervalSeconds != 60 {
		t.Fatalf("observed_interval_seconds = %v, want 60", sch.ObservedIntervalSeconds)
	}
	if sch.Activity != string(jobs.ScheduleActivityObserved) {
		t.Fatalf("activity = %q", sch.Activity)
	}
	if sch.LastRun == nil || sch.LastRun.ID != 42 {
		t.Fatalf("last_run = %+v", sch.LastRun)
	}
	if sch.LastRun.DurationMS == nil || *sch.LastRun.DurationMS != 200 {
		t.Fatalf("last_run.duration_ms = %v, want 200", sch.LastRun.DurationMS)
	}
	if sch.LastRun.Args["run_id"] != "abc" {
		t.Fatalf("last_run.args = %+v, want run_id=abc", sch.LastRun.Args)
	}
	if len(got.QueueBacklog) != 1 || got.QueueBacklog[0].Queue != "default" || got.QueueBacklog[0].Completed24h != 5 {
		t.Fatalf("queue_backlog = %+v", got.QueueBacklog)
	}
	if !got.WorkerHeartbeat.Known || got.WorkerHeartbeat.Environment != "platform" {
		t.Fatalf("worker_heartbeat = %+v", got.WorkerHeartbeat)
	}
	if got.WorkerHeartbeat.SecondsAgo == nil || *got.WorkerHeartbeat.SecondsAgo != 30 {
		t.Fatalf("worker_heartbeat.seconds_ago = %v, want 30", got.WorkerHeartbeat.SecondsAgo)
	}
}

func TestJobsOverviewHandlerConfiguredScheduleAndMode(t *testing.T) {
	enabled := true
	interval := int64(300)
	q := &fakeJobsQuerier{overview: jobs.Overview{
		Schedules: []jobs.ScheduleStatus{
			{
				Kind:     jobs.Sub2APISyncJobKind,
				Activity: jobs.ScheduleActivityNever,
				Configured: &jobs.DeployedJobSchedule{
					Enabled: enabled, IntervalSeconds: interval, EnabledSource: "XM_SUB2API_SYNC_ENABLED",
				},
				ConfiguredMode: &jobs.ConfiguredModeStatus{Mode: "real", Source: "database"},
			},
			{
				Kind:           jobs.NewAPISyncJobKind,
				Activity:       jobs.ScheduleActivityNever,
				ConfiguredMode: &jobs.ConfiguredModeStatus{Source: "unavailable"},
			},
			{
				// heartbeat：两个新字段都不接（QueryStore 没配对应依赖时的
				// 缺省形状），必须原样输出 null，不能编一个假值。
				Kind:     jobs.HeartbeatJobKind,
				Activity: jobs.ScheduleActivityNever,
			},
		},
	}}
	h := testRouterWithJobs(t, q)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/overview", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Schedules []struct {
			Kind                      string  `json:"kind"`
			ConfiguredEnabled         *bool   `json:"configured_enabled"`
			ConfiguredIntervalSeconds *int64  `json:"configured_interval_seconds"`
			ConfiguredSource          *string `json:"configured_source"`
			ConfiguredMode            *string `json:"configured_mode"`
			ConfiguredModeSource      *string `json:"configured_mode_source"`
		} `json:"schedules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if len(got.Schedules) != 3 {
		t.Fatalf("schedules = %+v", got.Schedules)
	}

	sub2api := got.Schedules[0]
	if sub2api.ConfiguredEnabled == nil || !*sub2api.ConfiguredEnabled {
		t.Fatalf("sub2api configured_enabled = %v, want true", sub2api.ConfiguredEnabled)
	}
	if sub2api.ConfiguredIntervalSeconds == nil || *sub2api.ConfiguredIntervalSeconds != 300 {
		t.Fatalf("sub2api configured_interval_seconds = %v, want 300", sub2api.ConfiguredIntervalSeconds)
	}
	if sub2api.ConfiguredSource == nil || *sub2api.ConfiguredSource != "XM_SUB2API_SYNC_ENABLED" {
		t.Fatalf("sub2api configured_source = %v", sub2api.ConfiguredSource)
	}
	if sub2api.ConfiguredMode == nil || *sub2api.ConfiguredMode != "real" {
		t.Fatalf("sub2api configured_mode = %v, want real", sub2api.ConfiguredMode)
	}
	if sub2api.ConfiguredModeSource == nil || *sub2api.ConfiguredModeSource != "database" {
		t.Fatalf("sub2api configured_mode_source = %v, want database", sub2api.ConfiguredModeSource)
	}

	newapi := got.Schedules[1]
	// unavailable：configured_mode 必须是 null（没有可信的模式值），
	// configured_mode_source 仍然如实报告 "unavailable"——两者不能都是 null,
	// 否则前端分不清"这个任务没有模式维度"与"读库失败"。
	if newapi.ConfiguredMode != nil {
		t.Fatalf("newapi configured_mode = %v, want null when source is unavailable", newapi.ConfiguredMode)
	}
	if newapi.ConfiguredModeSource == nil || *newapi.ConfiguredModeSource != "unavailable" {
		t.Fatalf("newapi configured_mode_source = %v, want unavailable", newapi.ConfiguredModeSource)
	}

	heartbeat := got.Schedules[2]
	if heartbeat.ConfiguredEnabled != nil || heartbeat.ConfiguredIntervalSeconds != nil || heartbeat.ConfiguredSource != nil {
		t.Fatalf("heartbeat Configured* fields = %+v, want all null (QueryStore had no deployed-schedule dependency)", heartbeat)
	}
	if heartbeat.ConfiguredMode != nil || heartbeat.ConfiguredModeSource != nil {
		t.Fatalf("heartbeat ConfiguredMode* fields = %+v, want all null (no platform mode dimension)", heartbeat)
	}
}

func TestJobsOverviewHandlerNeverEnabledFlag(t *testing.T) {
	// 回归防线：Activity 字段存在就够了，响应体绝不能出现一个叫 enabled 的
	// 布尔字段——httpapi 进程读不到 worker 的 jobs.Config，任何 enabled 断言
	// 都会是装出来的事实（见 jobs.ScheduleStatus 的注释）。
	q := &fakeJobsQuerier{overview: jobs.Overview{
		Schedules: []jobs.ScheduleStatus{{Kind: jobs.HeartbeatJobKind, Activity: jobs.ScheduleActivityNever}},
	}}
	h := testRouterWithJobs(t, q)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/overview", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), `"enabled"`) {
		t.Fatalf("response must never claim to know worker enablement: %s", rec.Body.String())
	}
}

func TestJobsOverviewHandlerRequiresPrincipalAndScope(t *testing.T) {
	h := testRouterWithJobs(t, &fakeJobsQuerier{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/overview", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无身份应 403, got %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/overview", nil)
	devHeaders(req, "audit.read") // 有身份，但没有 ops.read
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("缺少 ops.read 应 403, got %d", rec2.Code)
	}
}

func TestJobsOverviewHandlerRejectsCrossEnvironment(t *testing.T) {
	h := testRouterWithJobs(t, &fakeJobsQuerier{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/overview?environment=production", nil)
	devHeaders(req, "ops.read") // devHeaders 的身份环境是 development
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨环境读取应 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestJobsOverviewHandlerHidesStoreErrorDetail(t *testing.T) {
	q := &fakeJobsQuerier{overviewErr: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}
	h := testRouterWithJobs(t, q)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/overview", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("响应泄漏内网地址: %s", rec.Body.String())
	}
}

func TestListJobRunsHandlerParsesQueryParams(t *testing.T) {
	q := &fakeJobsQuerier{page: jobs.RunPage{Items: nil, NextBefore: 0}}
	h := testRouterWithJobs(t, q)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/jobs/runs?kind=sub2api_sync&state=retryable&before=100&limit=25", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if q.gotListIn.Environment != "development" {
		t.Fatalf("environment = %q", q.gotListIn.Environment)
	}
	if len(q.gotListIn.Kinds) != 1 || q.gotListIn.Kinds[0] != "sub2api_sync" {
		t.Fatalf("kinds = %v", q.gotListIn.Kinds)
	}
	if q.gotListIn.State != jobs.RunStateRetryable {
		t.Fatalf("state = %q", q.gotListIn.State)
	}
	if q.gotListIn.Before != 100 {
		t.Fatalf("before = %d", q.gotListIn.Before)
	}
	if q.gotListIn.Limit != 25 {
		t.Fatalf("limit = %d", q.gotListIn.Limit)
	}
}

func TestListJobRunsHandlerParsesCommaSeparatedKinds(t *testing.T) {
	// 「同步批次」页签要同时看 sub2api_sync / newapi_sync / finance_cost_sync
	// 三种，前端一次请求带逗号分隔的 kind，服务端拆成切片。
	q := &fakeJobsQuerier{}
	h := testRouterWithJobs(t, q)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/jobs/runs?kind=sub2api_sync,%20newapi_sync,finance_cost_sync", nil)
	devHeaders(req, "ops.read")
	h.ServeHTTP(httptest.NewRecorder(), req)

	want := []string{"sub2api_sync", "newapi_sync", "finance_cost_sync"}
	if len(q.gotListIn.Kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", q.gotListIn.Kinds, want)
	}
	for i, k := range want {
		if q.gotListIn.Kinds[i] != k {
			t.Fatalf("kinds[%d] = %q, want %q (raw parsed: %v)", i, q.gotListIn.Kinds[i], k, q.gotListIn.Kinds)
		}
	}
}

func TestListJobRunsHandlerDefaultsAndClampsLimit(t *testing.T) {
	q := &fakeJobsQuerier{}
	h := testRouterWithJobs(t, q)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs", nil)
	devHeaders(req, "ops.read")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if q.gotListIn.Limit != jobs.DefaultRunsLimit {
		t.Fatalf("default limit = %d, want %d", q.gotListIn.Limit, jobs.DefaultRunsLimit)
	}
	if q.gotListIn.Before != 0 {
		t.Fatalf("default before = %d, want 0", q.gotListIn.Before)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs?limit=999999", nil)
	devHeaders(req2, "ops.read")
	h.ServeHTTP(httptest.NewRecorder(), req2)
	if q.gotListIn.Limit != jobs.MaxRunsLimit {
		t.Fatalf("limit should clamp to %d, got %d", jobs.MaxRunsLimit, q.gotListIn.Limit)
	}
}

func TestListJobRunsHandlerRejectsUnknownState(t *testing.T) {
	h := testRouterWithJobs(t, &fakeJobsQuerier{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs?state=not_a_real_state", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 state 应 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListJobRunsHandlerRejectsNegativeBefore(t *testing.T) {
	h := testRouterWithJobs(t, &fakeJobsQuerier{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs?before=-1", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("负数游标应 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListJobRunsHandlerReturnsItemsAndNextBefore(t *testing.T) {
	createdAt := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	q := &fakeJobsQuerier{page: jobs.RunPage{
		Items: []jobs.RunRecord{{
			ID: 7, Kind: "audit_archive_manual", Queue: jobs.QueueMaintenance,
			State: jobs.RunStateDiscarded, Attempt: 3, MaxAttempts: 3,
			CreatedAt: createdAt, ScheduledAt: createdAt,
			ErrorCount: 2,
			LastError: &jobs.RunError{
				At: createdAt, Message: "boom", Truncated: false, OriginalLength: 4,
			},
			Args: map[string]any{"approval_envelope_sha256": "deadbeef"},
		}},
		NextBefore: 7,
	}}
	h := testRouterWithJobs(t, q)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs?state=discarded", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			ID         int64  `json:"id"`
			Kind       string `json:"kind"`
			State      string `json:"state"`
			ErrorCount int    `json:"error_count"`
			LastError  *struct {
				Message   string `json:"message"`
				Truncated bool   `json:"truncated"`
			} `json:"last_error"`
			Args map[string]any `json:"args"`
		} `json:"items"`
		NextBefore int64 `json:"next_before"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if len(got.Items) != 1 || got.Items[0].ID != 7 || got.Items[0].State != "discarded" {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].LastError == nil || got.Items[0].LastError.Message != "boom" {
		t.Fatalf("last_error = %+v", got.Items[0].LastError)
	}
	if got.Items[0].Args["approval_envelope_sha256"] != "deadbeef" {
		t.Fatalf("args = %+v", got.Items[0].Args)
	}
	if got.NextBefore != 7 {
		t.Fatalf("next_before = %d, want 7", got.NextBefore)
	}
}

func TestListJobRunsHandlerHidesStoreErrorDetail(t *testing.T) {
	q := &fakeJobsQuerier{listErr: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}
	h := testRouterWithJobs(t, q)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("响应泄漏内网地址: %s", rec.Body.String())
	}
}
