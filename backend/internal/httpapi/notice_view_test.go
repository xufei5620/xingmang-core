package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/postgresstore"
)

// XM-INV-NOTICE-VIEW：管理端能查"那条企业微信通知发出去了没有"。
//
// 在这一片之前只能查库——而这正是收不到通知时排查的第三步。

// 复用 operations_source_filter_test.go 里那个替身：OperationsService 很宽，
// 它已经把本测试用不到的方法都实现成 panic 了（意外的调用路径要响，不能
// 静默返回零值）。这里只覆盖要测的那一个。
type fakeNoticeOperations struct {
	*fakeSourceFilterOperations

	gotRequestID string
	notices      []postgresstore.InvoiceNoticeState
	err          error
}

func (f *fakeNoticeOperations) ListInvoiceNoticesForRequest(_ context.Context, requestID string) ([]postgresstore.InvoiceNoticeState, error) {
	f.gotRequestID = requestID
	return f.notices, f.err
}

func noticeTestServer(t *testing.T) (*Server, *fakeNoticeOperations) {
	t.Helper()
	fake := &fakeNoticeOperations{
		fakeSourceFilterOperations: &fakeSourceFilterOperations{Service: ledger.NewService()},
	}
	server, err := NewWithConfig(fake, Config{
		AuthMode: "mock", AdminIPAllowlist: []string{"127.0.0.1/32", "::1/128"},
		BreakGlassCIDRs: []string{"127.0.0.1/32", "::1/128"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, fake
}

const noticeRequestID = "60000000-0000-4000-8000-000000000099"

func TestAdminNoticeViewReturnsDeliveryFacts(t *testing.T) {
	server, fake := noticeTestServer(t)
	delivered := time.Date(2026, 9, 6, 8, 13, 0, 0, time.UTC)
	fake.notices = []postgresstore.InvoiceNoticeState{{
		ID: "90000000-0000-4000-8000-000000000001", Kind: postgresstore.NoticeKindRequestSubmitted,
		Status: "sent", AttemptCount: 1,
		NextAttemptAt: time.Date(2026, 9, 6, 8, 12, 0, 0, time.UTC),
		DeliveredAt:   &delivered,
		CreatedAt:     time.Date(2026, 9, 6, 8, 12, 0, 0, time.UTC),
		UpdatedAt:     delivered,
	}}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodGet,
		"/api/v1/admin/invoice-requests/"+noticeRequestID+"/notices", "127.0.0.1", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.gotRequestID != noticeRequestID {
		t.Fatalf("handler did not forward the request id: %q", fake.gotRequestID)
	}
	var body struct {
		Items []struct {
			Kind          string  `json:"kind"`
			Status        string  `json:"status"`
			AttemptCount  int     `json:"attempt_count"`
			DeliveredAt   *string `json:"delivered_at"`
			LastErrorCode string  `json:"last_error_code"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("want 1 notice, got %d: %s", len(body.Items), recorder.Body.String())
	}
	item := body.Items[0]
	if item.Kind != "request.submitted" || item.Status != "sent" || item.AttemptCount != 1 {
		t.Fatalf("unexpected notice: %+v", item)
	}
	if item.DeliveredAt == nil {
		t.Fatal("a delivered notice must carry its delivered_at")
	}
}

// 还没送达时 delivered_at 必须是 null，不能是零时刻——"1970 年送达"比
// "还没送达"更容易被读成一次真实投递。
func TestAdminNoticeViewLeavesDeliveredAtNullWhenQueued(t *testing.T) {
	server, fake := noticeTestServer(t)
	fake.notices = []postgresstore.InvoiceNoticeState{{
		ID: "90000000-0000-4000-8000-000000000002", Kind: postgresstore.NoticeKindRequestSubmitted,
		Status: "queued", AttemptCount: 3, LastErrorCode: "wecom: errcode 93000",
		NextAttemptAt: time.Date(2026, 9, 6, 8, 20, 0, 0, time.UTC),
	}}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodGet,
		"/api/v1/admin/invoice-requests/"+noticeRequestID+"/notices", "127.0.0.1", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	items, _ := raw["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want 1 item: %s", recorder.Body.String())
	}
	item, _ := items[0].(map[string]any)
	if got, ok := item["delivered_at"]; !ok || got != nil {
		t.Fatalf("delivered_at must be null while queued, got %#v", got)
	}
	// 失败原因要看得见：它是"为什么没发出去"的唯一线索。
	if item["last_error_code"] != "wecom: errcode 93000" {
		t.Fatalf("last_error_code missing: %#v", item["last_error_code"])
	}
}

// 一条通知都没有时返回空数组而不是 null：前端对 null 与 [] 的处理不同，
// 而"这份申请没有通知记录"是一个正常状态（通知功能没启用时就是这样）。
func TestAdminNoticeViewReturnsEmptyArrayNotNull(t *testing.T) {
	server, fake := noticeTestServer(t)
	fake.notices = nil

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest(http.MethodGet,
		"/api/v1/admin/invoice-requests/"+noticeRequestID+"/notices", "127.0.0.1", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	items, ok := raw["items"].([]any)
	if !ok {
		t.Fatalf("items must be an array, got %#v", raw["items"])
	}
	if len(items) != 0 {
		t.Fatalf("want an empty array, got %d items", len(items))
	}
}

// **没有 user 变体**：这是运维事实，不是申请人的业务数据。
//
// 断言 404 而不是"不等于 200"：一条**存在但要鉴权**的用户路由在无凭据时
// 回的是 401，同样不等于 200——那样写的话，将来真有人加了 user 变体，
// 这条测试还会继续绿。所以先证明"已注册的用户路由不回 404"，
// 再要求这条路径回 404。
func TestNoticeViewHasNoUserRoute(t *testing.T) {
	server, _ := noticeTestServer(t)
	get := func(path string) int {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder.Code
	}

	// 参照路径：这条用户路由确实注册了（/delivery），无凭据时不会是 404。
	registered := get("/api/v1/user/invoice-requests/" + noticeRequestID + "/delivery")
	if registered == http.StatusNotFound {
		t.Fatalf("参照路由本身回了 404，这条测试就分不出「没注册」和「被拒」了")
	}

	if code := get("/api/v1/user/invoice-requests/" + noticeRequestID + "/notices"); code != http.StatusNotFound {
		t.Fatalf("用户侧不该注册通知投递视图，却回了 %d（参照路由回 %d）", code, registered)
	}
}
