package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type fakeUsersQuerier struct {
	page connusers.UserPage
	err  error
	got  platformusers.ListInput
}

func (f *fakeUsersQuerier) List(_ context.Context, in platformusers.ListInput) (connusers.UserPage, error) {
	f.got = in
	if f.err != nil {
		return connusers.UserPage{}, f.err
	}
	return f.page, nil
}

func samplePage() connusers.UserPage {
	return connusers.UserPage{
		Users: []connusers.User{
			{
				ID:             "u_1",
				Username:       "张伟",
				EmailMasked:    "zh***@example.com",
				Status:         connusers.StatusActive,
				Balance:        connusers.KnownAmount(1284500, "CNY"),
				PeriodRecharge: connusers.KnownAmount(120000, "CNY"),
				PeriodConsumed: connusers.KnownAmount(31200, "CNY"),
				LastActiveAt:   time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC),
				TokenPrefix:    "sk-a1b2",
			},
			{
				// 逐用户流水缺席（原型 warnbar：v1 契约给不出），且从未活跃
				ID:             "u_2",
				Username:       "试用账号",
				EmailMasked:    "",
				Status:         connusers.StatusLimited,
				Balance:        connusers.KnownAmount(0, "CNY"),
				PeriodRecharge: connusers.UnknownAmount(),
				PeriodConsumed: connusers.UnknownAmount(),
			},
		},
		TotalCount:   connusers.KnownCount(2),
		TotalBalance: connusers.KnownAmount(1284500, "CNY"),
		NextCursor:   "2",
		Snapshot: connusers.Snapshot{
			ObservedAt: time.Now().UTC(),
			Source:     "sub2api-fake",
			Watermark:  "wm-1",
		},
	}
}

func serveUsers(t *testing.T, q PlatformUsersQuerier, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/platforms/{platform}/users", ListPlatformUsersHandler(q))
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeUsers(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v(body=%s)", err, rec.Body.String())
	}
	return body
}

func TestListPlatformUsersReturnsPage(t *testing.T) {
	q := &fakeUsersQuerier{page: samplePage()}
	rec := serveUsers(t, q, "/platforms/sub2api/users")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeUsers(t, rec)
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应有 2 条,得到 %d", len(items))
	}
	if body["next_cursor"] != "2" {
		t.Fatalf("next_cursor 应透传,得到 %v", body["next_cursor"])
	}
	// 新鲜度与 /api/v1/metrics 同一个形状,前端复用同一个徽章组件
	fresh, _ := body["freshness"].(map[string]any)
	if fresh["state"] != "fresh" {
		t.Fatalf("新鲜度应为 fresh,得到 %v", fresh["state"])
	}
	if body["data_source"] != "sub2api-fake" {
		t.Fatalf("data_source 应透传来源,得到 %v", body["data_source"])
	}
}

func TestListPlatformUsersMoneyIsString(t *testing.T) {
	// 宪法 13 条:金额禁止 float。JSON number 在 JS 里是 float64,
	// 超过 2^53 的最小单位金额会静默丢精度
	q := &fakeUsersQuerier{page: samplePage()}
	body := decodeUsers(t, serveUsers(t, q, "/platforms/sub2api/users"))
	items, _ := body["items"].([]any)
	first, _ := items[0].(map[string]any)
	balance, _ := first["balance"].(map[string]any)
	if _, ok := balance["minor_units"].(string); !ok {
		t.Fatalf("余额应当是字符串,得到 %T(%v)", balance["minor_units"], balance["minor_units"])
	}
	if balance["currency"] != "CNY" {
		t.Fatalf("余额应带币种,得到 %v", balance["currency"])
	}
}

func TestListPlatformUsersUnknownAmountIsNull(t *testing.T) {
	// 「上游没给」与「值为 0」是相反的两件事:前者要显示成「—」
	q := &fakeUsersQuerier{page: samplePage()}
	body := decodeUsers(t, serveUsers(t, q, "/platforms/sub2api/users"))
	items, _ := body["items"].([]any)
	second, _ := items[1].(map[string]any)

	recharge, _ := second["period_recharge"].(map[string]any)
	if recharge["minor_units"] != nil {
		t.Fatalf("缺席的充值额应当是 null,得到 %v", recharge["minor_units"])
	}
	// 同一条里余额是**已知的 0**,必须是字符串 "0" 而不是 null
	balance, _ := second["balance"].(map[string]any)
	if balance["minor_units"] != "0" {
		t.Fatalf("已知的零余额应当是 \"0\",得到 %v", balance["minor_units"])
	}
	// 从未活跃 → null,与「很久以前活跃过」不是一回事
	if second["last_active_at"] != nil {
		t.Fatalf("从未活跃应当是 null,得到 %v", second["last_active_at"])
	}
}

func TestListPlatformUsersNeverLeaksPlaintextEmail(t *testing.T) {
	// 响应体是契约,不是结构体的倒影:这条断言守的是「明文一个字都不出连接器」
	q := &fakeUsersQuerier{page: samplePage()}
	rec := serveUsers(t, q, "/platforms/sub2api/users")
	raw := rec.Body.String()
	if strings.Contains(raw, "zhangwei@") || strings.Contains(raw, "@example.com\"") && !strings.Contains(raw, "***@example.com") {
		t.Fatalf("响应里出现了未打码的邮箱: %s", raw)
	}
	if !strings.Contains(raw, "zh***@example.com") {
		t.Fatalf("打过码的邮箱应当原样透传(不做二次处理): %s", raw)
	}
}

func TestListPlatformUsersPassesFilters(t *testing.T) {
	q := &fakeUsersQuerier{page: samplePage()}
	serveUsers(t, q, "/platforms/newapi/users?q=%E5%BC%A0&status=active&sort=consumed_desc&limit=20&cursor=abc")
	if q.got.Platform != "newapi" {
		t.Fatalf("平台应取自路径,得到 %q", q.got.Platform)
	}
	if q.got.Query != "张" || q.got.Status != "active" || q.got.Sort != "consumed_desc" {
		t.Fatalf("过滤条件透传有误: %+v", q.got)
	}
	if q.got.Limit != 20 || q.got.Cursor != "abc" {
		t.Fatalf("分页参数透传有误: %+v", q.got)
	}
}

func TestListPlatformUsersRejectsBadLimit(t *testing.T) {
	q := &fakeUsersQuerier{page: samplePage()}
	rec := serveUsers(t, q, "/platforms/sub2api/users?limit=-1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("负数 limit 应当 400,得到 %d", rec.Code)
	}
}

func TestListPlatformUsersPropagatesServiceError(t *testing.T) {
	// 平台不认识 → 404(交接文档 §8:未知对象显示 Not Found)
	q := &fakeUsersQuerier{err: action.NewError(action.CodeNotRegistered, "平台没有终端用户清单", nil)}
	rec := serveUsers(t, q, "/platforms/cpa/users")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知平台应当 404,得到 %d(body=%s)", rec.Code, rec.Body.String())
	}
}

func TestListPlatformUsersNotSupportedIsNotAnUpstreamFailure(t *testing.T) {
	// real 骨架未实装时前端要显示「功能待上线」,不是「上游故障」
	q := &fakeUsersQuerier{err: action.NewError(action.CodeAdvancedControlsRequired,
		"用户清单尚未接通真实数据源", nil)}
	rec := serveUsers(t, q, "/platforms/sub2api/users")
	if rec.Code == http.StatusBadGateway || rec.Code == http.StatusInternalServerError {
		t.Fatalf("not_supported 不该被当成上游故障,得到 %d", rec.Code)
	}
}
