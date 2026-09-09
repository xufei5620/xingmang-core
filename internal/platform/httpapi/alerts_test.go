package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
)

// revenueMetric 是这些用例共用的指标键。
//
// 抽成常量而不是就地写字面量：`SomethingKey: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见
// web/apps/admin-web/src/pages/OverviewPage.test.tsx 与 jobs/client.go）。
// 本仓禁止加 gitleaks allowlist（会顺手掩盖真报，见 scripts/check-governance.sh），
// 所以换个写法比放宽扫描器划算。**常量名里也不能带 key**——
// 那条规则看的是「标识符含 key/token/secret + 赋值 + 一串高熵值」，
// 叫 revenueMetricKey 照样会被判成泄漏。
const revenueMetric = "sub2api.revenue.daily"

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
		SourceMetricKey: revenueMetric,
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

// TestAlertItemSeparatesFireCountFromTriggerCount 是 2026-09-08 那条
// 「触发 669 次」的对外修正。
//
// 真相是「触发 1 次、已持续 668 分钟」：fire_count 是评估轮数（每 60 秒一轮，
// 条件仍成立就 +1），trigger_count 才是发生次数。两个数必须分别出现在响应里，
// 前端才不会再把前者当成后者。
//
// 断言解成 map 而不是一个只含新字段的结构体：后者会让「旧字段被删掉」这种
// 回归静默通过，而前端片（XM-WORKBENCH-TRUTH）正在并行消费那些旧字段。
func TestAlertItemSeparatesFireCountFromTriggerCount(t *testing.T) {
	a := sampleAlert()
	// 照 09-08 生产上那条 sub2api 版本告警的真实形态：持续 669 轮、触发 1 次。
	opened := time.Date(2026, 9, 8, 4, 12, 0, 0, time.UTC)
	a.OpenedAt = opened
	a.FirstOpenedAt = &opened
	a.FireCount = 669
	one := int32(1)
	a.TriggerCount = &one

	rec := getAlerts(t, alertRouter(t, &fakeAlertLister{items: []alerts.Alert{a}}), "", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if len(raw.Items) != 1 {
		t.Fatalf("items = %d", len(raw.Items))
	}
	item := raw.Items[0]

	if got := item["fire_count"]; got != float64(669) {
		t.Fatalf("fire_count = %v, want 669", got)
	}
	if got := item["trigger_count"]; got != float64(1) {
		t.Fatalf("trigger_count = %v, want 1（与 fire_count 分离）", got)
	}
	// 时间恒为 UTC RFC3339，与既有的 opened_at / last_seen_at 逐字同形。
	if got := item["first_opened_at"]; got != "2026-09-08T04:12:00Z" {
		t.Fatalf("first_opened_at = %v", got)
	}
	if got := item["first_opened_at_estimated"]; got != false {
		t.Fatalf("有真实首开时刻时不该标成估计值：%v", got)
	}

	// 既有键仍在且值不变——前端片正在并行消费它们，改名等于让它当场炸。
	for _, key := range []string{
		"id", "rule_key", "dedup_key", "severity", "status", "title", "detail",
		"environment", "source_metric_key", "opened_at", "last_seen_at",
		"acknowledged_at", "resolved_at", "fire_count",
		"notify_status", "notify_error", "notified_at",
	} {
		if _, ok := item[key]; !ok {
			t.Fatalf("既有字段 %q 不见了", key)
		}
	}
}

// TestAlertItemTellsYouWhenFirstOpenedAtIsAGuess：迁移 000054 明确不回填，
// 所以库里既有的行没有真实的首开时刻。
//
// 两个字段的空值口径**故意不同**：
//   - trigger_count 回 null（前端显示「—」）——没有可兜的底，
//     显示成 0 是一个看起来像真答案的假答案；
//   - first_opened_at 恒非空（用 opened_at 兜底）——「已持续」是待处理清单的
//     主行文案，必须渲染得出东西，但要用 first_opened_at_estimated 说出实情。
func TestAlertItemTellsYouWhenFirstOpenedAtIsAGuess(t *testing.T) {
	a := sampleAlert()
	a.TriggerCount = nil  // 本列上线前的旧行
	a.FirstOpenedAt = nil // 同上

	rec := getAlerts(t, alertRouter(t, &fakeAlertLister{items: []alerts.Alert{a}}), "", "ops.read")
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	item := raw.Items[0]

	if got, ok := item["trigger_count"]; !ok || got != nil {
		t.Fatalf("旧行的 trigger_count 应是 null（不是 0），实际 %v", got)
	}
	if got := item["first_opened_at"]; got != a.OpenedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("first_opened_at 应兜底成 opened_at，实际 %v", got)
	}
	if got := item["first_opened_at_estimated"]; got != true {
		t.Fatalf("兜底值必须被标成估计值，实际 %v", got)
	}
	// 零值时刻绝不能被格式化成 0001-01-01——界面上「不知道」必须显示成不知道。
	if strings.Contains(rec.Body.String(), "0001-01-01") {
		t.Fatalf("响应里出现了零值时刻: %s", rec.Body.String())
	}
}

// TestAlertItemJSONKeysMatchTheStructTags 用反射清点，不手列。
//
// 手列一份键清单会让「新增字段漏进响应」这种回归静默通过——而闸的范围要
// 从被校验对象身上发现，不能与它同源手抄。
func TestAlertItemJSONKeysMatchTheStructTags(t *testing.T) {
	rec := getAlerts(t, alertRouter(t, &fakeAlertLister{items: []alerts.Alert{sampleAlert()}}),
		"", "ops.read")
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	item := raw.Items[0]

	typ := reflect.TypeOf(alertItem{})
	if typ.NumField() != len(item) {
		t.Fatalf("响应键数 %d 与 alertItem 字段数 %d 不符：%v", len(item), typ.NumField(), item)
	}
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" {
			t.Fatalf("字段 %s 没有 json tag", typ.Field(i).Name)
		}
		if _, ok := item[tag]; !ok {
			t.Fatalf("结构体声明了 %q，响应里却没有", tag)
		}
	}
}
