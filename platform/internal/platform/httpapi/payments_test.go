package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeOrdersQuerier struct {
	result PlatformOrdersResult
	err    error
	got    PlatformOrdersInput
}

func (f *fakeOrdersQuerier) ListOrders(_ context.Context, in PlatformOrdersInput) (PlatformOrdersResult, error) {
	f.got = in
	if f.err != nil {
		return PlatformOrdersResult{}, f.err
	}
	return f.result, nil
}

func sampleOrdersResult() PlatformOrdersResult {
	return PlatformOrdersResult{
		// 已按 CreatedAt 降序——Querier 契约的产出顺序，处理器不重新排序
		// （见 ListPlatformOrdersHandler 的注释）。
		Items: []PlatformOrderItem{
			{OrderID: "9001", CreatedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
				Status: "PAID", AmountMinorUnits: 10000, Currency: "USD",
				Method: "alipay", UserRef: "a***@example.test", UpstreamOrderRef: "OUT-9001"},
			{OrderID: "9002", CreatedAt: time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
				Status: "PENDING", AmountMinorUnits: 5000, Currency: "USD",
				Method: "wxpay", UserRef: "b***@example.test", UpstreamOrderRef: "OUT-9002"},
		},
		StatsByStatus: map[string]PlatformOrderStat{
			"PAID":    {Count: 1, AmountMinorUnits: 10000},
			"PENDING": {Count: 1, AmountMinorUnits: 5000},
		},
		Currency:   "USD",
		Source:     "sub2api-staging",
		ObservedAt: time.Now().UTC(),
		Watermark:  "wm-1",
		IsPartial:  false,
	}
}

func servePayments(t *testing.T, q PlatformOrdersQuerier, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/platforms/{platform}/orders", ListPlatformOrdersHandler(q))
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{
		ID: "staff", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "issuer", Subject: "subject", Environment: "staging",
	}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeOrders(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v (body=%s)", err, rec.Body.String())
	}
	return body
}

func TestListPlatformOrdersReturnsPage(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeOrders(t, rec)
	items, ok := body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %v", body["items"])
	}
	if q.got.Platform != "sub2api" {
		t.Fatalf("Platform 传给 Querier = %q, want sub2api", q.got.Platform)
	}
	if q.got.Environment != "staging" {
		t.Fatalf("Environment 应取自 Principal, got %q", q.got.Environment)
	}
}

// TestListPlatformOrdersAmountIsString：金额必须是十进制字符串，
// 不能是裸 JSON number（宪法 13 条：JS 的 number 是 float64，会丢精度）。
func TestListPlatformOrdersAmountIsString(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27")
	body := decodeOrders(t, rec)
	items := body["items"].([]any)
	first := items[0].(map[string]any)
	amount, ok := first["amount"].(map[string]any)
	if !ok {
		t.Fatalf("amount 字段缺失: %v", first)
	}
	if _, isString := amount["minor_units"].(string); !isString {
		t.Fatalf("amount.minor_units 应为字符串, got %T (%v)", amount["minor_units"], amount["minor_units"])
	}
	stats := body["stats_by_status"].(map[string]any)
	for status, raw := range stats {
		s := raw.(map[string]any)
		a := s["amount"].(map[string]any)
		if _, isString := a["minor_units"].(string); !isString {
			t.Fatalf("stats_by_status[%s].amount.minor_units 应为字符串, got %T", status, a["minor_units"])
		}
	}
}

func TestListPlatformOrdersRejectsUnknownPlatform(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/bogus/orders?day=2026-08-27")
	if rec.Code == http.StatusOK {
		t.Fatalf("未知平台应被拒绝, got 200: %s", rec.Body.String())
	}
}

func TestListPlatformOrdersRejectsDayWithFromTo(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27&from=2026-08-01&to=2026-08-27")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("day 与 from/to 同时提供应 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListPlatformOrdersDefaultsToTodayWhenNoWindowGiven(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if q.got.From.IsZero() || q.got.To.IsZero() {
		t.Fatal("都不传 day/from/to 时应默认当前 UTC 业务日，不该给 Querier 传零值窗口")
	}
}

func TestListPlatformOrdersFromToWindow(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?from=2026-08-01&to=2026-08-05")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	wantFrom := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 8, 5, 23, 59, 59, 999999999, time.UTC)
	if !q.got.From.Equal(wantFrom) {
		t.Fatalf("From = %v, want %v", q.got.From, wantFrom)
	}
	if !q.got.To.Equal(wantTo) {
		t.Fatalf("To = %v, want %v", q.got.To, wantTo)
	}
}

func TestListPlatformOrdersPassesStatusFilter(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27&status=PAID")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if q.got.Status != "PAID" {
		t.Fatalf("Status 传给 Querier = %q, want PAID", q.got.Status)
	}
}

// TestListPlatformOrdersPagination：Querier 给全量候选，处理器自己按
// order_id 做游标切页——与渠道目录端点同一套路（platform_channels.go）。
func TestListPlatformOrdersPagination(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27&limit=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeOrders(t, rec)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("limit=1 应只返回 1 条, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["order_id"] != "9001" {
		// 按 CreatedAt 降序：9001(10:00) 排在 9002(09:00) 前面
		t.Fatalf("首条 order_id = %v, want 9001（按时间降序）", first["order_id"])
	}
	nextCursor, _ := body["next_cursor"].(string)
	if nextCursor == "" {
		t.Fatal("还有第二页，next_cursor 不该为空")
	}

	rec2 := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27&limit=1&cursor="+nextCursor)
	body2 := decodeOrders(t, rec2)
	items2 := body2["items"].([]any)
	if len(items2) != 1 {
		t.Fatalf("第二页应有 1 条, got %d", len(items2))
	}
	second := items2[0].(map[string]any)
	if second["order_id"] != "9002" {
		t.Fatalf("第二页 order_id = %v, want 9002", second["order_id"])
	}
	if nc, _ := body2["next_cursor"].(string); nc != "" {
		t.Fatalf("翻到底后 next_cursor 应为空串, got %q", nc)
	}
}

func TestListPlatformOrdersPropagatesQuerierError(t *testing.T) {
	q := &fakeOrdersQuerier{err: connector.NewError(connector.KindNotSupported, "test", nil)}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27")
	if rec.Code == http.StatusOK {
		t.Fatalf("Querier 报错应透传, got 200: %s", rec.Body.String())
	}
}

func TestListPlatformOrdersFreshnessReflectsPartial(t *testing.T) {
	result := sampleOrdersResult()
	result.IsPartial = true
	q := &fakeOrdersQuerier{result: result}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27")
	body := decodeOrders(t, rec)
	freshness, ok := body["freshness"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 freshness 字段: %v", body)
	}
	if freshness["is_partial"] != true {
		t.Fatalf("freshness.is_partial 应为 true, got %v", freshness["is_partial"])
	}
	if body["data_source"] != "sub2api-staging" {
		t.Fatalf("data_source = %v, want sub2api-staging", body["data_source"])
	}
}

func TestParseOrdersWindowRejectsInvalidLimit(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27&limit=0")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 应被拒绝, got %d", rec.Code)
	}
}

// 确保未登录（缺 Principal）时不会 panic，而是返回明确的权限错误。
func TestListPlatformOrdersRequiresPrincipal(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	r := chi.NewRouter()
	r.Get("/platforms/{platform}/orders", ListPlatformOrdersHandler(q))
	req := httptest.NewRequest(http.MethodGet, "/platforms/sub2api/orders?day=2026-08-27", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 Principal 应拒绝而不是 200/500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestListPlatformOrdersFeeAndRefundAmountAreNilByDefault：XM-PAY1 新增的
// fee/refund_amount 字段，未设置时应渲染成 amountBody 的零值（minor_units 为
// JSON null），不是裸 0——与既有 amount 字段的"未知不冒充 0"同一条纪律。
func TestListPlatformOrdersFeeAndRefundAmountAreNilByDefault(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27")
	body := decodeOrders(t, rec)
	items := body["items"].([]any)
	first := items[0].(map[string]any)
	fee, ok := first["fee"].(map[string]any)
	if !ok {
		t.Fatalf("fee 字段缺失: %v", first)
	}
	if fee["minor_units"] != nil {
		t.Fatalf("未设置 FeeMinorUnits 时 fee.minor_units 应为 null, got %v", fee["minor_units"])
	}
	refund, ok := first["refund_amount"].(map[string]any)
	if !ok {
		t.Fatalf("refund_amount 字段缺失: %v", first)
	}
	if refund["minor_units"] != nil {
		t.Fatalf("未设置 RefundAmountMinorUnits 时 refund_amount.minor_units 应为 null, got %v", refund["minor_units"])
	}
}

// TestListPlatformOrdersFeeAndRefundAmountArePresentWhenSet：设置了的话必须
// 是十进制字符串（宪法 13 条），且币种复用订单自己的 Currency。
func TestListPlatformOrdersFeeAndRefundAmountArePresentWhenSet(t *testing.T) {
	result := sampleOrdersResult()
	fee := int64(240)
	refund := int64(0)
	result.Items[0].FeeMinorUnits = &fee
	result.Items[0].RefundAmountMinorUnits = &refund
	q := &fakeOrdersQuerier{result: result}
	rec := servePayments(t, q, "/platforms/sub2api/orders?day=2026-08-27")
	body := decodeOrders(t, rec)
	items := body["items"].([]any)
	first := items[0].(map[string]any)
	feeBody := first["fee"].(map[string]any)
	if feeBody["minor_units"] != "240" {
		t.Fatalf("fee.minor_units = %v, want \"240\"", feeBody["minor_units"])
	}
	if feeBody["currency"] != "USD" {
		t.Fatalf("fee.currency = %v, want USD（应复用订单币种）", feeBody["currency"])
	}
	refundBody := first["refund_amount"].(map[string]any)
	// 真实的 0（比如未退款订单）必须显示成 "0"，不是 null——两者是相反的
	// 两件事（规格 §12）。
	if refundBody["minor_units"] != "0" {
		t.Fatalf("refund_amount.minor_units = %v, want \"0\"（已知的零，不是未知）", refundBody["minor_units"])
	}
}

func serveOrderDetail(t *testing.T, q PlatformOrdersQuerier, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/platforms/{platform}/orders/{id}", GetPlatformOrderHandler(q))
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{
		ID: "staff", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "issuer", Subject: "subject", Environment: "staging",
	}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestGetPlatformOrderFoundInWindow(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := serveOrderDetail(t, q, "/platforms/sub2api/orders/9002?day=2026-08-27")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	order, ok := body["order"].(map[string]any)
	if !ok {
		t.Fatalf("order 字段缺失: %v", body)
	}
	if order["order_id"] != "9002" {
		t.Fatalf("order_id = %v, want 9002", order["order_id"])
	}
	if order["status"] != "PENDING" {
		t.Fatalf("status = %v, want PENDING", order["status"])
	}
	if body["from"] != "2026-08-27" || body["to"] != "2026-08-27" {
		t.Fatalf("from/to = %v/%v, 应回显已搜索的窗口", body["from"], body["to"])
	}
	if q.got.Status != "" {
		t.Fatalf("订单详情不应按 status 过滤 Querier, got %q", q.got.Status)
	}
}

// TestGetPlatformOrderNotFoundIsHonest：窗口内翻完全部候选找不到这个 ID 时
// 必须诚实 404，不能悄悄回退到另一条订单（ADMIN-IA 交接文档 §8）。
func TestGetPlatformOrderNotFoundIsHonest(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := serveOrderDetail(t, q, "/platforms/sub2api/orders/does-not-exist?day=2026-08-27")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	errBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 error 字段: %v", body)
	}
	// 必须是结构化的业务错误码，不是 chi 的裸 404 文本——否则前端的
	// looksLikeUnmountedRoute 会误把"这一条没找到"当成"整组端点没挂载"。
	if errBody["code"] != "ACTION_NOT_REGISTERED" {
		t.Fatalf("error.code = %v, want ACTION_NOT_REGISTERED", errBody["code"])
	}
}

func TestGetPlatformOrderDefaultsToTodayWindow(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := serveOrderDetail(t, q, "/platforms/sub2api/orders/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	if q.got.From.IsZero() || q.got.To.IsZero() {
		t.Fatal("不传 day/from/to 时应默认当前 UTC 业务日，不该给 Querier 传零值窗口")
	}
}

func TestGetPlatformOrderRejectsUnknownPlatform(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	rec := serveOrderDetail(t, q, "/platforms/bogus/orders/9001?day=2026-08-27")
	if rec.Code == http.StatusOK {
		t.Fatalf("未知平台应被拒绝, got 200: %s", rec.Body.String())
	}
}

func TestGetPlatformOrderFeeAndRefundAmountRoundTrip(t *testing.T) {
	result := sampleOrdersResult()
	refund := int64(20000)
	result.Items[0].RefundAmountMinorUnits = &refund
	q := &fakeOrdersQuerier{result: result}
	rec := serveOrderDetail(t, q, "/platforms/sub2api/orders/9001?day=2026-08-27")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	order := body["order"].(map[string]any)
	refundBody := order["refund_amount"].(map[string]any)
	if refundBody["minor_units"] != "20000" {
		t.Fatalf("refund_amount.minor_units = %v, want \"20000\"", refundBody["minor_units"])
	}
	feeBody := order["fee"].(map[string]any)
	if feeBody["minor_units"] != nil {
		t.Fatalf("未设置 FeeMinorUnits 时应为 null, got %v", feeBody["minor_units"])
	}
}

func TestGetPlatformOrderRequiresPrincipal(t *testing.T) {
	q := &fakeOrdersQuerier{result: sampleOrdersResult()}
	r := chi.NewRouter()
	r.Get("/platforms/{platform}/orders/{id}", GetPlatformOrderHandler(q))
	req := httptest.NewRequest(http.MethodGet, "/platforms/sub2api/orders/9001?day=2026-08-27", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 Principal 应拒绝而不是 200/500, got %d: %s", rec.Code, rec.Body.String())
	}
}
