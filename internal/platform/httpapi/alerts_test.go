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
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
)

type fakeAlertLister struct {
	gotEnv      string
	gotStatuses []alerts.Status
	gotLimit    int32
	recentCalls int
	items       []alerts.Alert
	err         error
}

func (f *fakeAlertLister) ListByStatus(
	_ context.Context, env string, statuses []alerts.Status, limit int32,
) ([]alerts.Alert, error) {
	f.gotEnv, f.gotStatuses, f.gotLimit = env, statuses, limit
	return f.items, f.err
}

func (f *fakeAlertLister) ListRecent(_ context.Context, env string, limit int32) ([]alerts.Alert, error) {
	f.gotEnv, f.gotLimit = env, limit
	f.recentCalls++
	return f.items, f.err
}

func alertRouter(t *testing.T, lister AlertLister) http.Handler {
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
		Alerts:         lister,
	})
}

func sampleAlert() alerts.Alert {
	now := time.Date(2026, 8, 27, 5, 0, 0, 0, time.UTC)
	acked := now.Add(2 * time.Minute)
	return alerts.Alert{
		ID:              uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		RuleKey:         alerts.RuleMetricSyncFailed,
		DedupKey:        alerts.RuleMetricSyncFailed + ":development:sub2api.revenue.daily",
		Severity:        alerts.SeverityCritical,
		Status:          alerts.StatusAcknowledged,
		Title:           "指标 sub2api.revenue.daily 同步失败",
		Detail:          "错误码 timeout",
		Environment:     "development",
		SourceMetricKey: "sub2api.revenue.daily",
		OpenedAt:        now,
		LastSeenAt:      now.Add(3 * time.Minute),
		AcknowledgedAt:  &acked,
		FireCount:       4,
		NotifyStatus:    alerts.NotifyFailed,
		NotifyError:     "telegram: HTTP 502",
	}
}

func getAlerts(t *testing.T, h http.Handler, query, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts"+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type alertsBody struct {
	Items []struct {
		ID              string  `json:"id"`
		RuleKey         string  `json:"rule_key"`
		Severity        string  `json:"severity"`
		Status          string  `json:"status"`
		Title           string  `json:"title"`
		FireCount       int32   `json:"fire_count"`
		OpenedAt        string  `json:"opened_at"`
		LastSeenAt      string  `json:"last_seen_at"`
		AcknowledgedAt  *string `json:"acknowledged_at"`
		ResolvedAt      *string `json:"resolved_at"`
		NotifyStatus    string  `json:"notify_status"`
		NotifyError     string  `json:"notify_error"`
		SourceMetricKey string  `json:"source_metric_key"`
	} `json:"items"`
}

// TestListAlertsReturnsNotifyStatus：投递状态必须随告警一起返回。
//
// 一条 OPEN 却没投递出去的告警是本模块最危险的状态——前端要显示它，
// 就得先拿得到它。这条断言防的是将来有人为了「精简响应」把它删掉。
func TestListAlertsReturnsNotifyStatus(t *testing.T) {
	lister := &fakeAlertLister{items: []alerts.Alert{sampleAlert()}}
	rec := getAlerts(t, alertRouter(t, lister), "", "ops.read")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got alertsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d", len(got.Items))
	}
	item := got.Items[0]
	if item.NotifyStatus != "failed" || item.NotifyError != "telegram: HTTP 502" {
		t.Fatalf("投递状态未如实返回: %+v", item)
	}
	if item.Severity != "critical" || item.Status != "ACKNOWLEDGED" {
		t.Fatalf("严重度/状态不对: %+v", item)
	}
	if item.FireCount != 4 {
		t.Fatalf("fire_count = %d", item.FireCount)
	}
	if item.OpenedAt != "2026-08-27T05:00:00Z" || item.LastSeenAt != "2026-08-27T05:03:00Z" {
		t.Fatalf("时间戳应是 UTC RFC3339: %+v", item)
	}
	if item.AcknowledgedAt == nil || *item.AcknowledgedAt != "2026-08-27T05:02:00Z" {
		t.Fatalf("acknowledged_at 不对: %v", item.AcknowledgedAt)
	}
	// 未解决的告警 resolved_at 是 null，不是零值时间——零值会被前端
	// 显示成 1970 年，看起来像「早就解决了」。
	if item.ResolvedAt != nil {
		t.Fatalf("未解决的告警 resolved_at 应为 null，实际 %v", *item.ResolvedAt)
	}
}

// TestListAlertsDefaultsToActiveStatuses：不传 status 时返回活跃告警
// （「现在要处理什么」），仓储收到空状态集合。
func TestListAlertsDefaultsToActiveStatuses(t *testing.T) {
	lister := &fakeAlertLister{}
	if rec := getAlerts(t, alertRouter(t, lister), "", "ops.read"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(lister.gotStatuses) != 0 {
		t.Fatalf("默认应传空状态集合（= 全部活跃），实际 %v", lister.gotStatuses)
	}
	if lister.recentCalls != 0 {
		t.Fatal("默认不该走「含已解决」的路径")
	}
	if lister.gotLimit != defaultAlertLimit {
		t.Fatalf("默认 limit = %d, want %d", lister.gotLimit, defaultAlertLimit)
	}
}

// TestListAlertsStatusFilter：逗号分隔的状态过滤。
func TestListAlertsStatusFilter(t *testing.T) {
	lister := &fakeAlertLister{}
	rec := getAlerts(t, alertRouter(t, lister), "?status=OPEN,REOPENED", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(lister.gotStatuses) != 2 ||
		lister.gotStatuses[0] != alerts.StatusOpen || lister.gotStatuses[1] != alerts.StatusReopened {
		t.Fatalf("状态过滤未传到仓储: %v", lister.gotStatuses)
	}
}

// TestListAlertsStatusAllIncludesResolved：status=all 走「含已解决」的路径。
func TestListAlertsStatusAllIncludesResolved(t *testing.T) {
	lister := &fakeAlertLister{}
	if rec := getAlerts(t, alertRouter(t, lister), "?status=all", "ops.read"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if lister.recentCalls != 1 {
		t.Fatalf("status=all 应走 ListRecent，实际调用 %d 次", lister.recentCalls)
	}
}

// TestListAlertsRejectsUnknownStatus：拼错的状态当场 400。
//
// 静默忽略的话，调用方会拿到一份看起来对、其实没按他要求过滤的列表——
// 一个 status=Open（大小写错）被忽略成「全部活跃」，人不会发现。
func TestListAlertsRejectsUnknownStatus(t *testing.T) {
	for _, q := range []string{"?status=Open", "?status=OPEN,BOGUS", "?status=resolved"} {
		rec := getAlerts(t, alertRouter(t, &fakeAlertLister{}), q, "ops.read")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400（body=%s）", q, rec.Code, rec.Body.String())
		}
	}
}

// TestListAlertsRejectsBadLimit：limit 必须是正整数。
func TestListAlertsRejectsBadLimit(t *testing.T) {
	for _, q := range []string{"?limit=0", "?limit=-1", "?limit=abc"} {
		rec := getAlerts(t, alertRouter(t, &fakeAlertLister{}), q, "ops.read")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", q, rec.Code)
		}
	}
	lister := &fakeAlertLister{}
	if rec := getAlerts(t, alertRouter(t, lister), "?limit=25", "ops.read"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if lister.gotLimit != 25 {
		t.Fatalf("limit = %d, want 25", lister.gotLimit)
	}
}

// TestListAlertsRequiresScope：读也要权限（规格 §2.4）。
func TestListAlertsRequiresScope(t *testing.T) {
	rec := getAlerts(t, alertRouter(t, &fakeAlertLister{}), "", "registry.read")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（body=%s）", rec.Code, rec.Body.String())
	}
}

// TestListAlertsRejectsCrossEnvironmentRead：生产权限不继承（规格 §20.5）。
// 一个 development 身份不该能读生产告警——告警正文里有余额与收入。
func TestListAlertsRejectsCrossEnvironmentRead(t *testing.T) {
	rec := getAlerts(t, alertRouter(t, &fakeAlertLister{}), "?environment=production", "ops.read")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（body=%s）", rec.Code, rec.Body.String())
	}
}

// TestListAlertsUsesCallerEnvironmentByDefault：不传环境时用调用者自己的。
func TestListAlertsUsesCallerEnvironmentByDefault(t *testing.T) {
	lister := &fakeAlertLister{}
	if rec := getAlerts(t, alertRouter(t, lister), "", "ops.read"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if lister.gotEnv != "development" {
		t.Fatalf("environment = %q, want development", lister.gotEnv)
	}
}

// TestListAlertsEmptyReturnsEmptyArray：没有告警时返回空数组而不是 null。
// null 会让前端的 .map 炸掉，也让「没有告警」与「字段缺失」不可区分。
func TestListAlertsEmptyReturnsEmptyArray(t *testing.T) {
	rec := getAlerts(t, alertRouter(t, &fakeAlertLister{}), "", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); !json.Valid([]byte(body)) {
		t.Fatalf("响应不是合法 JSON: %s", body)
	}
	var got alertsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if got.Items == nil {
		t.Fatal("items 应是空数组而不是 null")
	}
}

// TestListAlertsHidesStoreErrorDetails：底层错误不透传（规格 §18.4）。
func TestListAlertsHidesStoreErrorDetails(t *testing.T) {
	lister := &fakeAlertLister{err: errors.New("pq: relation \"alerts.alert\" does not exist")}
	rec := getAlerts(t, alertRouter(t, lister), "", "ops.read")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"relation", "alerts.alert", "pq:"} {
		if strings.Contains(body, leak) {
			t.Fatalf("响应泄漏了底层错误（片段 %q）: %s", leak, body)
		}
	}
}

// TestAlertsEndpointIsNoStore：/api/v1 下每一条响应都不可缓存。
// 告警正文按 Principal 与环境裁剪过，落盘或进共享缓存都是泄漏。
func TestAlertsEndpointIsNoStore(t *testing.T) {
	rec := getAlerts(t, alertRouter(t, &fakeAlertLister{}), "", "ops.read")
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Fatal("缺少 Cache-Control（应由 /api/v1 组上的 NoStore 中间件设置）")
	}
}
