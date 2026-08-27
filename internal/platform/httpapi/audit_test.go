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

type fakeAuditLister struct {
	gotEnv    string
	gotBefore int64
	gotLimit  int32
	items     []audit.Event
	err       error
}

func (f *fakeAuditLister) ListRecent(
	_ context.Context, env string, beforeSeq int64, limit int32,
) ([]audit.Event, error) {
	f.gotEnv, f.gotBefore, f.gotLimit = env, beforeSeq, limit
	if f.err != nil {
		return nil, f.err
	}
	// 模拟存储层的截断，让 handler 的 next_before 判定拿到真实形状
	if int(limit) < len(f.items) {
		return f.items[:limit], nil
	}
	return f.items, nil
}

// auditRouter 装配一个只带审计依赖的路由。
// 不动 testhelpers_test.go 里的共享构造器——那是别的端点在用的。
func auditRouter(t *testing.T, lister AuditEventLister) http.Handler {
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
		AuditEvents:    lister,
	})
}

func auditEvt(seq int64, env string) audit.Event {
	return audit.Event{
		ID: uuid.New(), Sequence: seq,
		OccurredAt:  time.Date(2026, 8, 26, 10, 0, 0, 123456000, time.UTC),
		PrincipalID: "staff_alice", PrincipalType: principal.TypeHuman,
		ActionID: "registry.service.create", ActionVersion: "1", ActionRunID: uuid.New(),
		ResourceType: "core.service", ResourceID: "sub2api-prod",
		Environment: env, RequestID: "req-1",
		BeforeSummary: map[string]any{"exists": false},
		AfterSummary:  map[string]any{"exists": true},
		Result:        audit.ResultSucceeded,
		PrevHash:      strings.Repeat("a", 64),
		EventHash:     strings.Repeat("b", 64),
	}
}

type auditPageBody struct {
	Items []struct {
		Sequence      int64          `json:"sequence"`
		OccurredAt    string         `json:"occurred_at"`
		PrincipalID   string         `json:"principal_id"`
		PrincipalType string         `json:"principal_type"`
		ActionID      string         `json:"action_id"`
		ActionVersion string         `json:"action_version"`
		ActionRunID   string         `json:"action_run_id"`
		ResourceType  string         `json:"resource_type"`
		ResourceID    string         `json:"resource_id"`
		Environment   string         `json:"environment"`
		RequestID     string         `json:"request_id"`
		Result        string         `json:"result"`
		ErrorCode     string         `json:"error_code"`
		BeforeSummary map[string]any `json:"before_summary"`
		AfterSummary  map[string]any `json:"after_summary"`
		EventHash     string         `json:"event_hash"`
		PrevHash      string         `json:"prev_hash"`
	} `json:"items"`
	NextBefore int64 `json:"next_before"`
}

func getAudit(t *testing.T, h http.Handler, query, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events"+query, nil)
	if scopes != "" {
		devHeaders(req, scopes)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListAuditEventsRequiresAuditReadScope(t *testing.T) {
	h := auditRouter(t, &fakeAuditLister{items: []audit.Event{auditEvt(1, "development")}})

	// ops.read / registry.read 都不该放行——审计带前后摘要，单独授予
	for _, scopes := range []string{"ops.read", "registry.read", "ops.read,registry.read"} {
		rec := getAudit(t, h, "", scopes)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("scopes=%q 应 403, got %d (%s)", scopes, rec.Code, rec.Body.String())
		}
	}
	// 无身份同样 403
	if rec := getAudit(t, h, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("无身份应 403, got %d", rec.Code)
	}
	// 有 audit.read 才通
	if rec := getAudit(t, h, "", audit.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("持 audit.read 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestListAuditEventsUsesPrincipalEnvironmentOnly(t *testing.T) {
	lister := &fakeAuditLister{}
	h := auditRouter(t, lister)

	// 不传 environment：用 Principal 的环境
	if rec := getAudit(t, h, "", audit.ScopeRead); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if lister.gotEnv != "development" {
		t.Fatalf("应查 Principal 的环境，got %q", lister.gotEnv)
	}

	// 传了 environment 也**不生效**：审计只看自己环境，查询参数不是越权通道
	rec := getAudit(t, h, "?environment=production", audit.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if lister.gotEnv != "development" {
		t.Fatalf("environment 查询参数不得改变查询范围，got %q", lister.gotEnv)
	}
}

func TestListAuditEventsClampsLimit(t *testing.T) {
	lister := &fakeAuditLister{}
	h := auditRouter(t, lister)

	cases := []struct {
		query string
		want  int32
	}{
		{"", audit.DefaultListLimit},
		{"?limit=10", 10},
		{"?limit=100", audit.MaxListLimit},
		{"?limit=101", audit.MaxListLimit},
		{"?limit=100000", audit.MaxListLimit},
		{"?limit=0", audit.DefaultListLimit},
	}
	for _, c := range cases {
		if rec := getAudit(t, h, c.query, audit.ScopeRead); rec.Code != http.StatusOK {
			t.Fatalf("%q status = %d", c.query, rec.Code)
		}
		if lister.gotLimit != c.want {
			t.Fatalf("%q → limit = %d, want %d", c.query, lister.gotLimit, c.want)
		}
	}

	// 拼错的参数是 400 而不是静默取默认值——静默会让调用方永远发现不了
	for _, q := range []string{"?limit=abc", "?limit=-1", "?before_seq=xyz", "?before_seq=-1"} {
		if rec := getAudit(t, h, q, audit.ScopeRead); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q 应 400, got %d", q, rec.Code)
		}
	}
}

func TestListAuditEventsPassesCursorAndReportsNextBefore(t *testing.T) {
	lister := &fakeAuditLister{items: []audit.Event{
		auditEvt(9, "development"), auditEvt(8, "development"), auditEvt(7, "development"),
	}}
	h := auditRouter(t, lister)

	// 满页 → next_before 是本页最后一条，可直接当下一页游标
	rec := getAudit(t, h, "?limit=3&before_seq=12", audit.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if lister.gotBefore != 12 {
		t.Fatalf("before_seq 未透传，got %d", lister.gotBefore)
	}
	var page auditPageBody
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if len(page.Items) != 3 || page.Items[0].Sequence != 9 || page.Items[2].Sequence != 7 {
		t.Fatalf("items 应为 9/8/7，got %+v", page.Items)
	}
	if page.NextBefore != 7 {
		t.Fatalf("满页时 next_before 应为本页最后一条 7，got %d", page.NextBefore)
	}

	// 不满页 → 已到底，next_before = 0
	rec = getAudit(t, h, "?limit=10", audit.ScopeRead)
	page = auditPageBody{}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.NextBefore != 0 {
		t.Fatalf("不满页应 next_before=0（到底），got %d", page.NextBefore)
	}

	// 空页也必须是 items:[] 而不是 null，否则前端要多写一层判空
	lister.items = nil
	rec = getAudit(t, h, "", audit.ScopeRead)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("空结果应为 []，got %s", rec.Body.String())
	}
}

func TestListAuditEventsFieldContract(t *testing.T) {
	e := auditEvt(42, "development")
	e.CompensationResult = "COMPENSATION_FAILED"
	e.Result = audit.ResultFailed
	h := auditRouter(t, &fakeAuditLister{items: []audit.Event{e}})

	rec := getAudit(t, h, "", audit.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var page auditPageBody
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	it := page.Items[0]
	if it.Sequence != 42 || it.PrincipalID != "staff_alice" || it.PrincipalType != "HUMAN" {
		t.Fatalf("身份字段不符: %+v", it)
	}
	if it.ActionID != "registry.service.create" || it.ActionVersion != "1" ||
		it.ActionRunID != e.ActionRunID.String() {
		t.Fatalf("动作字段不符: %+v", it)
	}
	if it.ResourceType != "core.service" || it.ResourceID != "sub2api-prod" ||
		it.Environment != "development" || it.RequestID != "req-1" {
		t.Fatalf("资源/环境字段不符: %+v", it)
	}
	if it.Result != "failed" {
		t.Fatalf("result = %q", it.Result)
	}
	// error_code 映射自 CompensationResult
	if it.ErrorCode != "COMPENSATION_FAILED" {
		t.Fatalf("error_code 应映射自 compensation_result，got %q", it.ErrorCode)
	}
	// 哈希原样返回，不截断不改写
	if it.EventHash != e.EventHash || it.PrevHash != e.PrevHash {
		t.Fatalf("哈希被改写: %+v", it)
	}
	// occurred_at 保留微秒：截到秒会让同一秒内的多条事件看起来同时发生
	if it.OccurredAt != "2026-08-26T10:00:00.123456Z" {
		t.Fatalf("occurred_at = %q", it.OccurredAt)
	}

	// 内部字段不得出现在响应里（reason / approval_id / trace_id / source_ip /
	// connector 摘要）——响应体是契约，不是结构体的倒影
	body := rec.Body.String()
	for _, leaked := range []string{
		"reason", "approval_id", "trace_id", "source_ip",
		"connector_request_summary", "connector_response_summary", "recorded_at",
	} {
		if strings.Contains(body, `"`+leaked+`"`) {
			t.Fatalf("响应泄漏内部字段 %s: %s", leaked, body)
		}
	}
}

func TestListAuditEventsEmptySummariesSerializeAsNull(t *testing.T) {
	// 库里 jsonb 列 NOT NULL DEFAULT '{}'，读回来是非 nil 空 map；
	// 前端要区分「没有前后镜像」和「镜像为空」，所以必须是 null 不是 {}
	e := auditEvt(1, "development")
	e.BeforeSummary = map[string]any{}
	e.AfterSummary = nil
	h := auditRouter(t, &fakeAuditLister{items: []audit.Event{e}})

	body := getAudit(t, h, "", audit.ScopeRead).Body.String()
	if !strings.Contains(body, `"before_summary":null`) {
		t.Fatalf("空 before_summary 应为 null: %s", body)
	}
	if !strings.Contains(body, `"after_summary":null`) {
		t.Fatalf("nil after_summary 应为 null: %s", body)
	}
	if strings.Contains(body, `_summary":{}`) {
		t.Fatalf("不应出现 {}: %s", body)
	}
}

func TestListAuditEventsHidesStoreErrorDetail(t *testing.T) {
	h := auditRouter(t, &fakeAuditLister{
		err: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"),
	})
	rec := getAudit(t, h, "", audit.ScopeRead)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("响应泄漏内网地址: %s", rec.Body.String())
	}
}
