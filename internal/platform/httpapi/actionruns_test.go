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

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeActionRunQuerier struct {
	gotFilter action.RunFilter
	page      action.RunPage
	listErr   error

	runsByID map[uuid.UUID]action.Run
	getErr   error
}

func (f *fakeActionRunQuerier) ListRuns(_ context.Context, filter action.RunFilter) (action.RunPage, error) {
	f.gotFilter = filter
	if f.listErr != nil {
		return action.RunPage{}, f.listErr
	}
	return f.page, nil
}

func (f *fakeActionRunQuerier) GetRun(_ context.Context, id uuid.UUID) (action.Run, bool, error) {
	if f.getErr != nil {
		return action.Run{}, false, f.getErr
	}
	r, ok := f.runsByID[id]
	return r, ok, nil
}

type fakeActionRunAuditLookup struct {
	gotRunID uuid.UUID
	event    audit.Event
	found    bool
	err      error
}

func (f *fakeActionRunAuditLookup) GetByActionRunID(_ context.Context, runID uuid.UUID) (audit.Event, bool, error) {
	f.gotRunID = runID
	if f.err != nil {
		return audit.Event{}, false, f.err
	}
	return f.event, f.found, nil
}

// actionRunsRouter 装配一个只带执行记录依赖的路由。
// 不动 testhelpers_test.go 里的共享构造器——那是别的端点在用的（同
// audit_test.go 的 auditRouter）。
func actionRunsRouter(t *testing.T, runs ActionRunQuerier, auditLookup ActionRunAuditLookup) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:         discardLogger(),
		Service:        "platform-api",
		Environment:    "development",
		DB:             fakePinger{},
		Resolver:       res,
		Kernel:         &fakeExecutor{},
		ActionRegistry: action.NewRegistry(),
		ActionRuns:     runs,
		ActionRunAudit: auditLookup,
	})
}

func sampleRunItem(id uuid.UUID, actionID, env string, status action.RunStatus) action.Run {
	now := time.Date(2026, 8, 29, 10, 0, 0, 123456000, time.UTC)
	r := action.Run{
		ID: id, ActionID: actionID, ActionVersion: "1",
		PrincipalID: "staff_alice", PrincipalType: principal.TypeHuman,
		Environment: env, RequestID: "req-1",
		RiskLevel: action.L1, Status: status,
		DurationMS: 12, StartedAt: now, FinishedAt: now.Add(12 * time.Millisecond),
	}
	if status == action.RunFailed {
		r.ErrorCode = action.CodePermissionDenied
	}
	return r
}

func getRuns(t *testing.T, h http.Handler, query, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/actions/runs"+query, nil)
	if scopes != "" {
		devHeaders(req, scopes)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getRun(t *testing.T, h http.Handler, runID, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/actions/runs/"+runID, nil)
	if scopes != "" {
		devHeaders(req, scopes)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type actionRunItemBody struct {
	ID            string `json:"id"`
	ActionID      string `json:"action_id"`
	ActionVersion string `json:"action_version"`
	PrincipalID   string `json:"principal_id"`
	PrincipalType string `json:"principal_type"`
	Environment   string `json:"environment"`
	RequestID     string `json:"request_id"`
	RiskLevel     string `json:"risk_level"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code"`
	DurationMS    int64  `json:"duration_ms"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
}

type actionRunPageBody struct {
	Items      []actionRunItemBody `json:"items"`
	NextCursor string              `json:"next_cursor"`
}

func TestListActionRunsRequiresActionReadScope(t *testing.T) {
	h := actionRunsRouter(t, &fakeActionRunQuerier{}, &fakeActionRunAuditLookup{})

	// ops.read / audit.read 都不该单独放行——执行记录是它自己的一档权限
	for _, scopes := range []string{"ops.read", "audit.read", "ops.read,audit.read"} {
		if rec := getRuns(t, h, "", scopes); rec.Code != http.StatusForbidden {
			t.Fatalf("scopes=%q 应 403, got %d (%s)", scopes, rec.Code, rec.Body.String())
		}
	}
	if rec := getRuns(t, h, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("无身份应 403, got %d", rec.Code)
	}
	if rec := getRuns(t, h, "", action.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("持 action.read 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestListActionRunsUsesPrincipalEnvironmentOnly(t *testing.T) {
	store := &fakeActionRunQuerier{}
	h := actionRunsRouter(t, store, &fakeActionRunAuditLookup{})

	if rec := getRuns(t, h, "", action.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if store.gotFilter.Environment != "development" {
		t.Fatalf("应查 Principal 的环境，got %q", store.gotFilter.Environment)
	}

	// 本端点根本不读 environment 查询参数：不是「传了但被覆盖」，
	// 是从设计上就没有这个入口
	rec := getRuns(t, h, "?environment=production", action.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if store.gotFilter.Environment != "development" {
		t.Fatalf("environment 查询参数不得改变查询范围，got %q", store.gotFilter.Environment)
	}
}

func TestListActionRunsPassesFiltersAndClampsLimit(t *testing.T) {
	store := &fakeActionRunQuerier{}
	h := actionRunsRouter(t, store, &fakeActionRunAuditLookup{})

	rec := getRuns(t, h, "?action_id=registry.service.create&status=failed&principal=staff_bob&cursor=abc123", action.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if store.gotFilter.ActionID != "registry.service.create" || store.gotFilter.Status != "failed" ||
		store.gotFilter.PrincipalID != "staff_bob" || store.gotFilter.Cursor != "abc123" {
		t.Fatalf("过滤条件未透传: %+v", store.gotFilter)
	}

	cases := []struct {
		query string
		want  int32
	}{
		{"", 0}, // 0 交给 Store 用 DefaultRunListLimit
		{"?limit=10", 10},
		{"?limit=200", 200}, // 是否夹到上限由 Store 负责，handler 只负责解析
	}
	for _, c := range cases {
		if rec := getRuns(t, h, c.query, action.ScopeRead); rec.Code != http.StatusOK {
			t.Fatalf("%q status = %d", c.query, rec.Code)
		}
		if store.gotFilter.Limit != c.want {
			t.Fatalf("%q → limit = %d, want %d", c.query, store.gotFilter.Limit, c.want)
		}
	}

	for _, q := range []string{"?limit=abc", "?limit=-1"} {
		if rec := getRuns(t, h, q, action.ScopeRead); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q 应 400, got %d", q, rec.Code)
		}
	}
}

func TestListActionRunsRejectsBadStatus(t *testing.T) {
	h := actionRunsRouter(t, &fakeActionRunQuerier{}, &fakeActionRunAuditLookup{})
	rec := getRuns(t, h, "?status=bogus", action.ScopeRead)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 status 应 400, got %d", rec.Code)
	}
	for _, s := range []string{"succeeded", "failed", ""} {
		if rec := getRuns(t, h, "?status="+s, action.ScopeRead); rec.Code != http.StatusOK {
			t.Fatalf("status=%q 应 200, got %d", s, rec.Code)
		}
	}
}

func TestListActionRunsFieldContractAndCursor(t *testing.T) {
	id := uuid.New()
	store := &fakeActionRunQuerier{page: action.RunPage{
		Items:      []action.Run{sampleRunItem(id, "registry.service.create", "development", action.RunFailed)},
		NextCursor: "opaque-next-page-token",
	}}
	h := actionRunsRouter(t, store, &fakeActionRunAuditLookup{})

	rec := getRuns(t, h, "", action.ScopeRead)
	var page actionRunPageBody
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if page.NextCursor != "opaque-next-page-token" {
		t.Fatalf("next_cursor 未原样返回: %q", page.NextCursor)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items 数量 = %d", len(page.Items))
	}
	it := page.Items[0]
	if it.ID != id.String() || it.ActionID != "registry.service.create" || it.ActionVersion != "1" {
		t.Fatalf("动作字段不符: %+v", it)
	}
	if it.PrincipalID != "staff_alice" || it.PrincipalType != "HUMAN" || it.Environment != "development" {
		t.Fatalf("身份/环境字段不符: %+v", it)
	}
	if it.RiskLevel != "L1" || it.Status != "failed" || it.ErrorCode != "PERMISSION_DENIED" {
		t.Fatalf("风险/状态字段不符: %+v", it)
	}
	if it.DurationMS != 12 {
		t.Fatalf("duration_ms = %d", it.DurationMS)
	}
	// 微秒精度保留，不截到秒
	if it.StartedAt != "2026-08-29T10:00:00.123456Z" {
		t.Fatalf("started_at = %q", it.StartedAt)
	}
	// before/after 绝不出现在列表响应里——那是详情端点、且要另一个 scope
	body := rec.Body.String()
	if strings.Contains(body, "before_summary") || strings.Contains(body, "after_summary") {
		t.Fatalf("列表响应不应包含审计摘要字段: %s", body)
	}
}

func TestListActionRunsHidesStoreErrorDetail(t *testing.T) {
	h := actionRunsRouter(t, &fakeActionRunQuerier{
		listErr: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"),
	}, &fakeActionRunAuditLookup{})
	rec := getRuns(t, h, "", action.ScopeRead)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("响应泄漏内网地址: %s", rec.Body.String())
	}
}

func TestGetActionRunRequiresBothActionAndAuditReadScope(t *testing.T) {
	id := uuid.New()
	store := &fakeActionRunQuerier{runsByID: map[uuid.UUID]action.Run{
		id: sampleRunItem(id, "registry.service.create", "development", action.RunSucceeded),
	}}
	h := actionRunsRouter(t, store, &fakeActionRunAuditLookup{})

	for _, scopes := range []string{action.ScopeRead, audit.ScopeRead, ""} {
		if rec := getRun(t, h, id.String(), scopes); rec.Code != http.StatusForbidden {
			t.Fatalf("scopes=%q 应 403（详情需要两个 scope 都持有）, got %d", scopes, rec.Code)
		}
	}
	rec := getRun(t, h, id.String(), action.ScopeRead+","+audit.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("持两个 scope 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetActionRunRejectsBadUUID(t *testing.T) {
	h := actionRunsRouter(t, &fakeActionRunQuerier{}, &fakeActionRunAuditLookup{})
	rec := getRun(t, h, "not-a-uuid", action.ScopeRead+","+audit.ScopeRead)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 run_id 应 400, got %d", rec.Code)
	}
}

func TestGetActionRunNotFoundWhenMissingOrWrongEnvironment(t *testing.T) {
	inOtherEnv := uuid.New()
	store := &fakeActionRunQuerier{runsByID: map[uuid.UUID]action.Run{
		inOtherEnv: sampleRunItem(inOtherEnv, "registry.service.create", "production", action.RunSucceeded),
	}}
	h := actionRunsRouter(t, store, &fakeActionRunAuditLookup{})
	scopes := action.ScopeRead + "," + audit.ScopeRead

	// 压根没有这条记录
	if rec := getRun(t, h, uuid.New().String(), scopes); rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404, got %d", rec.Code)
	}
	// 记录存在，但属于别的环境（development 身份不该看见 production 的记录）：
	// 同样 404，不是 403——403 会向调用者确认「这个 id 确实存在」
	if rec := getRun(t, h, inOtherEnv.String(), scopes); rec.Code != http.StatusNotFound {
		t.Fatalf("跨环境记录应 404 而非其它状态, got %d", rec.Code)
	}
}

func TestGetActionRunIncludesAuditSummaryWhenPresent(t *testing.T) {
	id := uuid.New()
	store := &fakeActionRunQuerier{runsByID: map[uuid.UUID]action.Run{
		id: sampleRunItem(id, "registry.service.create", "development", action.RunSucceeded),
	}}
	lookup := &fakeActionRunAuditLookup{
		found: true,
		event: audit.Event{
			OccurredAt:    time.Date(2026, 8, 29, 10, 0, 0, 123456000, time.UTC),
			ResourceType:  "core.service",
			ResourceID:    "sub2api-prod",
			Reason:        "巡检",
			BeforeSummary: map[string]any{"exists": false},
			AfterSummary:  map[string]any{"exists": true},
			Result:        audit.ResultSucceeded,
		},
	}
	h := actionRunsRouter(t, store, lookup)

	rec := getRun(t, h, id.String(), action.ScopeRead+","+audit.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if lookup.gotRunID != id {
		t.Fatalf("查询审计事件时传的 run_id 不对: %v", lookup.gotRunID)
	}
	var detail struct {
		Run   actionRunItemBody `json:"run"`
		Audit *struct {
			ResourceType  string         `json:"resource_type"`
			ResourceID    string         `json:"resource_id"`
			Reason        string         `json:"reason"`
			BeforeSummary map[string]any `json:"before_summary"`
			AfterSummary  map[string]any `json:"after_summary"`
			Result        string         `json:"result"`
			OccurredAt    string         `json:"occurred_at"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if detail.Run.ID != id.String() {
		t.Fatalf("run.id = %q", detail.Run.ID)
	}
	if detail.Audit == nil {
		t.Fatal("应带出关联的审计摘要")
	}
	if detail.Audit.ResourceType != "core.service" || detail.Audit.ResourceID != "sub2api-prod" {
		t.Fatalf("资源字段不符: %+v", detail.Audit)
	}
	if detail.Audit.BeforeSummary["exists"] != false || detail.Audit.AfterSummary["exists"] != true {
		t.Fatalf("前后摘要不符: %+v", detail.Audit)
	}
	if detail.Audit.Result != "succeeded" {
		t.Fatalf("result = %q", detail.Audit.Result)
	}
}

func TestGetActionRunAuditNullWhenNoAssociatedEvent(t *testing.T) {
	id := uuid.New()
	store := &fakeActionRunQuerier{runsByID: map[uuid.UUID]action.Run{
		id: sampleRunItem(id, "registry.service.create", "development", action.RunSucceeded),
	}}
	h := actionRunsRouter(t, store, &fakeActionRunAuditLookup{found: false})

	rec := getRun(t, h, id.String(), action.ScopeRead+","+audit.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"audit":null`) {
		t.Fatalf("无关联审计事件时 audit 应为 null: %s", rec.Body.String())
	}
}

func TestGetActionRunHidesAuditLookupErrorDetail(t *testing.T) {
	id := uuid.New()
	store := &fakeActionRunQuerier{runsByID: map[uuid.UUID]action.Run{
		id: sampleRunItem(id, "registry.service.create", "development", action.RunSucceeded),
	}}
	lookup := &fakeActionRunAuditLookup{err: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}
	h := actionRunsRouter(t, store, lookup)

	rec := getRun(t, h, id.String(), action.ScopeRead+","+audit.ScopeRead)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("响应泄漏内网地址: %s", rec.Body.String())
	}
}
