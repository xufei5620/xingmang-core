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
				ID:              "u_1",
				Username:        "张伟",
				EmailMasked:     "zh***@example.com",
				Status:          connusers.StatusActive,
				Balance:         connusers.KnownAmount(1284500, "CNY"),
				PeriodRecharge:  connusers.KnownAmount(120000, "CNY"),
				PeriodConsumed:  connusers.KnownAmount(31200, "CNY"),
				Last30dConsumed: connusers.KnownAmount(812000, "CNY"),
				LastActiveAt:    time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC),
				TokenPrefix:     "sk-a1b2",
			},
			{
				// 逐用户流水缺席（原型 warnbar：v1 契约给不出），且从未活跃
				ID:              "u_2",
				Username:        "试用账号",
				EmailMasked:     "",
				Status:          connusers.StatusLimited,
				Balance:         connusers.KnownAmount(0, "CNY"),
				PeriodRecharge:  connusers.UnknownAmount(),
				PeriodConsumed:  connusers.UnknownAmount(),
				Last30dConsumed: connusers.UnknownAmount(),
			},
		},
		TotalCount:   connusers.KnownCount(2),
		TotalBalance: connusers.KnownAmount(1284500, "CNY"),
		ActiveToday:  connusers.KnownCount(1),
		// 两个用户里只有一个给得出流水 → 合计是**下界**
		PeriodTotals: connusers.Totals{
			Recharge:     connusers.KnownAmount(120000, "CNY"),
			Consumed:     connusers.KnownAmount(31200, "CNY"),
			CoveredUsers: 1,
			TotalUsers:   2,
		},
		Period: connusers.Period{
			Day: "2026-08-27", Granularity: connusers.GranularityDay,
			From: "2026-08-27", To: "2026-08-27",
		},
		NextCursor: "2",
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

// TestListPlatformUsersPassesPeriodParams：day / granularity 原样传给 Service。
func TestListPlatformUsersPassesPeriodParams(t *testing.T) {
	q := &fakeUsersQuerier{page: samplePage()}
	serveUsers(t, q, "/platforms/sub2api/users?day=2026-08-27&granularity=week")
	if q.got.Day != "2026-08-27" || q.got.Granularity != "week" {
		t.Fatalf("区间入参 = %q / %q", q.got.Day, q.got.Granularity)
	}

	// 都不传 = 交给服务端解释成「今天 · 按日」，Handler 自己不填默认值：
	// 填了的话「今天」就是**进程**的今天，而业务日该由契约层按 CST 切
	bare := &fakeUsersQuerier{page: samplePage()}
	serveUsers(t, bare, "/platforms/sub2api/users")
	if bare.got.Day != "" || bare.got.Granularity != "" {
		t.Fatalf("不传时不该由 Handler 编默认值: %q / %q",
			bare.got.Day, bare.got.Granularity)
	}
}

// TestListPlatformUsersEchoesPeriod：响应回显服务端实际用的区间。
//
// 回显 from/to 而不只是 granularity：粒度是「周」时，人要能看见到底是哪七天，
// 跨月那几天尤其容易理解错。
func TestListPlatformUsersEchoesPeriod(t *testing.T) {
	rec := serveUsers(t, &fakeUsersQuerier{page: samplePage()}, "/platforms/sub2api/users")
	body := decodeUsers(t, rec)
	period, ok := body["period"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 period: %s", rec.Body.String())
	}
	for key, want := range map[string]string{
		"day": "2026-08-27", "granularity": "day",
		"from": "2026-08-27", "to": "2026-08-27",
	} {
		if got, _ := period[key].(string); got != want {
			t.Fatalf("period.%s = %q, want %q", key, got, want)
		}
	}
}

// TestListPlatformUsersTotalsCarryCoverage 是这一层最要紧的诚实性断言。
//
// 区间合计只加得动「上游给得出流水」的那些用户。响应里必须带上覆盖率，
// 否则前端只能把一个**下界**当成全量显示——而它看起来和真的合计一模一样
// （宪法 12 条：禁止裸数字冒充完整数据）。
func TestListPlatformUsersTotalsCarryCoverage(t *testing.T) {
	rec := serveUsers(t, &fakeUsersQuerier{page: samplePage()}, "/platforms/sub2api/users")
	body := decodeUsers(t, rec)
	totals, ok := body["period_totals"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 period_totals: %s", rec.Body.String())
	}
	if totals["covered_users"] != float64(1) || totals["total_users"] != float64(2) {
		t.Fatalf("覆盖率 = %v / %v, want 1 / 2", totals["covered_users"], totals["total_users"])
	}
	if totals["complete"] != false {
		t.Fatal("覆盖不全时 complete 必须为 false")
	}
	// 合计金额同样走字符串（宪法 13 条）
	recharge, _ := totals["recharge"].(map[string]any)
	if recharge["minor_units"] != "120000" {
		t.Fatalf("合计充值 = %v, want 字符串 \"120000\"", recharge["minor_units"])
	}
}

// TestListPlatformUsersActiveToday：今日活跃是可缺席的计数。
func TestListPlatformUsersActiveToday(t *testing.T) {
	rec := serveUsers(t, &fakeUsersQuerier{page: samplePage()}, "/platforms/sub2api/users")
	active, _ := decodeUsers(t, rec)["active_today"].(map[string]any)
	if active["value"] != float64(1) {
		t.Fatalf("active_today = %v, want 1", active["value"])
	}

	// 上游给不出时是 null，不是 0——「没人活跃」和「不知道」是两回事
	page := samplePage()
	page.ActiveToday = connusers.UnknownCount()
	rec = serveUsers(t, &fakeUsersQuerier{page: page}, "/platforms/sub2api/users")
	active, _ = decodeUsers(t, rec)["active_today"].(map[string]any)
	if active["value"] != nil {
		t.Fatalf("缺席时 active_today.value 应为 null, got %v", active["value"])
	}
}

// TestListPlatformUsersLast30dIsSeparateFromPeriod：近 30 天单独一列。
func TestListPlatformUsersLast30dIsSeparateFromPeriod(t *testing.T) {
	rec := serveUsers(t, &fakeUsersQuerier{page: samplePage()}, "/platforms/sub2api/users")
	items, _ := decodeUsers(t, rec)["items"].([]any)
	first, _ := items[0].(map[string]any)
	last30, ok := first["last_30d_consumed"].(map[string]any)
	if !ok {
		t.Fatalf("items[0] 缺少 last_30d_consumed: %s", rec.Body.String())
	}
	if last30["minor_units"] != "812000" {
		t.Fatalf("近30天消费 = %v", last30["minor_units"])
	}
	// 缺席的那一条仍然是 null
	second, _ := items[1].(map[string]any)
	last30b, _ := second["last_30d_consumed"].(map[string]any)
	if last30b["minor_units"] != nil {
		t.Fatalf("缺席时应为 null, got %v", last30b["minor_units"])
	}
}

// TestListPlatformUsersEndToEndWithFakeConnector 把整条链路串起来跑一遍。
//
// 上面那些用例每一条都桩掉了一层（Handler 桩 Service、Service 桩连接器）。
// 桩得越干净，「两层之间的字段没接上」这种错就越藏得住——比如 Handler 读
// `granularity` 而 Service 读的是 `period`，各自的单测都会绿。
// 这一条用**真的** Service + 真的 fake 连接器，只桩 HTTP 请求本身。
func TestListPlatformUsersEndToEndWithFakeConnector(t *testing.T) {
	svc, err := platformusers.NewService(map[string]platformusers.Client{
		connusers.SourceSub2API: connusers.NewFakeClient(connusers.SourceSub2API, nil),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 按周查看：区间必须被解析成周一至周日，且金额比按日大
	rec := serveUsers(t, svc, "/platforms/sub2api/users?day=2026-08-27&granularity=week")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeUsers(t, rec)

	period, _ := body["period"].(map[string]any)
	if period["from"] != "2026-08-24" || period["to"] != "2026-08-30" {
		t.Fatalf("周区间 = %v..%v, want 2026-08-24..2026-08-30", period["from"], period["to"])
	}

	weekTotals, _ := body["period_totals"].(map[string]any)
	// 样本里有几个用户上游给不出流水 → 合计是下界，必须如实报出来
	if weekTotals["complete"] != false {
		t.Fatal("样本覆盖不全时 complete 必须为 false")
	}
	covered, _ := weekTotals["covered_users"].(float64)
	total, _ := weekTotals["total_users"].(float64)
	if covered <= 0 || covered >= total {
		t.Fatalf("覆盖率 = %v/%v，样本应当既有已知也有缺席的", covered, total)
	}

	// 同一天按日查看：金额必须更小——粒度按钮真的生效了
	recDay := serveUsers(t, svc, "/platforms/sub2api/users?day=2026-08-27&granularity=day")
	dayTotals, _ := decodeUsers(t, recDay)["period_totals"].(map[string]any)
	dayConsumed, _ := dayTotals["consumed"].(map[string]any)
	weekConsumed, _ := weekTotals["consumed"].(map[string]any)
	dayText, _ := dayConsumed["minor_units"].(string)
	weekText, _ := weekConsumed["minor_units"].(string)
	if dayText == "" || weekText == "" || dayText == weekText {
		t.Fatalf("日与周的区间消费应当不同: %q vs %q", dayText, weekText)
	}

	// 逐用户三列都在，且缺席仍是 null（不是 0）
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("样本不该是空的")
	}
	var sawKnown, sawUnknown bool
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		last30, ok := item["last_30d_consumed"].(map[string]any)
		if !ok {
			t.Fatalf("缺少 last_30d_consumed: %v", item)
		}
		if last30["minor_units"] == nil {
			sawUnknown = true
		} else {
			sawKnown = true
		}
	}
	if !sawKnown || !sawUnknown {
		t.Fatal("样本必须同时含「已知」与「上游没给」的近30天消费——那是本契约的教学点")
	}

	// 坏粒度一路走到底仍是 400，不是 500
	bad := serveUsers(t, svc, "/platforms/sub2api/users?granularity=weekly")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("坏粒度 status = %d, want 400 (body=%s)", bad.Code, bad.Body.String())
	}
}
