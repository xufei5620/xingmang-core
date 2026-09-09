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

// silenceNow 是这些用例里「此刻」的固定值。
//
// 固定时钟而不是 time.Now：三态分类的全部内容就是「拿现在去比起止时间」，
// 跟着真实时钟走的话，一条本该「已过期」的样本会在某个边界上变成「生效中」，
// 而失败会是间歇性的——那种红比不红更难查。
var silenceNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

type fakeSilenceLister struct {
	gotEnv       string
	gotLimit     int32
	gotAt        time.Time
	activeCalls  int
	historyCalls int

	active  []alerts.Silence
	history []alerts.Silence
	err     error
}

func (f *fakeSilenceLister) ListActiveSilences(
	_ context.Context, env string, at time.Time,
) ([]alerts.Silence, error) {
	f.gotEnv, f.gotAt = env, at
	f.activeCalls++
	return f.active, f.err
}

func (f *fakeSilenceLister) ListSilences(
	_ context.Context, env string, limit int32,
) ([]alerts.Silence, error) {
	f.gotEnv, f.gotLimit = env, limit
	f.historyCalls++
	return f.history, f.err
}

func silenceRouter(t *testing.T, lister SilenceLister) http.Handler {
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
		Silences:       lister,
		SilenceNow:     func() time.Time { return silenceNow },
	})
}

func getSilences(t *testing.T, h http.Handler, query, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/silences"+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type silencesBody struct {
	Items []struct {
		ID          string `json:"id"`
		RuleKey     string `json:"rule_key"`
		Environment string `json:"environment"`
		Reason      string `json:"reason"`
		StartsAt    string `json:"starts_at"`
		EndsAt      string `json:"ends_at"`
		CreatedBy   string `json:"created_by"`
		CreatedAt   string `json:"created_at"`
		State       string `json:"state"`
	} `json:"items"`
	Limit     int32  `json:"limit"`
	Truncated bool   `json:"truncated"`
	AsOf      string `json:"as_of"`
}

func decodeSilences(t *testing.T, rec *httptest.ResponseRecorder) silencesBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got silencesBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v（body=%s）", err, rec.Body.String())
	}
	return got
}

// silence 造一个窗口；start/end 相对 silenceNow 偏移。
func silence(reason string, startOffset, endOffset time.Duration) alerts.Silence {
	return alerts.Silence{
		ID:          uuid.New(),
		RuleKey:     alerts.RuleMetricSyncFailed,
		Environment: "development",
		Reason:      reason,
		StartsAt:    silenceNow.Add(startOffset),
		EndsAt:      silenceNow.Add(endOffset),
		CreatedBy:   "staff_alice",
		CreatedAt:   silenceNow.Add(-24 * time.Hour),
	}
}

// TestListSilencesClassifiesThreeStates 是这一片的核心断言：三个窗口喂进去，
// 每一个都要被归到对的那一态。
//
// 分类由服务端算（silenceStateAt → alerts.Silence.Active），前端不再自己比
// 时间。这条用例同时钉住「算得对」与「算完了随响应回去」。
func TestListSilencesClassifiesThreeStates(t *testing.T) {
	lister := &fakeSilenceLister{history: []alerts.Silence{
		silence("正在压着", -30*time.Minute, 30*time.Minute),
		silence("还没开始", time.Hour, 2*time.Hour),
		silence("早就过了", -3*time.Hour, -time.Hour),
	}}
	got := decodeSilences(t, getSilences(t, silenceRouter(t, lister), "?state=all", "ops.read"))

	if len(got.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(got.Items))
	}
	want := map[string]string{
		"正在压着": "active",
		"还没开始": "scheduled",
		"早就过了": "expired",
	}
	for _, item := range got.Items {
		if w := want[item.Reason]; item.State != w {
			t.Fatalf("reason=%q state = %q, want %q", item.Reason, item.State, w)
		}
	}
	// as_of 必须回去：分类只在那一刻成立，页面挂久了要能说清算的是哪一刻。
	if got.AsOf != "2026-09-08T12:00:00Z" {
		t.Fatalf("as_of = %q, want 2026-09-08T12:00:00Z", got.AsOf)
	}
}

// TestListSilencesBoundaryIsLeftClosedRightOpen：[starts_at, ends_at)。
//
// 两个端点各一条样本。右开是投递侧压制告警用的同一条规则
// （alerts.Silence.Active），列表说的必须与它一致——列表说「还生效」而投递
// 侧已经放行，运营会以为告警仍被压着。
func TestListSilencesBoundaryIsLeftClosedRightOpen(t *testing.T) {
	lister := &fakeSilenceLister{history: []alerts.Silence{
		// 起点那一刻：已经生效（左闭）
		silence("刚好开始", 0, time.Hour),
		// 终点那一刻：已经失效（右开）
		silence("刚好到期", -time.Hour, 0),
	}}
	got := decodeSilences(t, getSilences(t, silenceRouter(t, lister), "?state=all", "ops.read"))

	states := map[string]string{}
	for _, item := range got.Items {
		states[item.Reason] = item.State
	}
	if states["刚好开始"] != "active" {
		t.Fatalf("starts_at 那一刻应已生效（左闭），实际 %q", states["刚好开始"])
	}
	if states["刚好到期"] != "expired" {
		t.Fatalf("ends_at 那一刻应已失效（右开），实际 %q", states["刚好到期"])
	}
}

// TestListSilencesDefaultsToActiveQuery：不传 state 时走「此刻生效」那条 SQL。
//
// 关键是**不能**退化成「全取回来再在 Go 里筛」：那样会先被 limit 截断，
// 一屏历史窗口就能把生效中的挤掉，页面于是显示「当前没有静默」。
func TestListSilencesDefaultsToActiveQuery(t *testing.T) {
	lister := &fakeSilenceLister{}
	if rec := getSilences(t, silenceRouter(t, lister), "", "ops.read"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if lister.activeCalls != 1 {
		t.Fatalf("默认应调用 ListActiveSilences 一次，实际 %d 次", lister.activeCalls)
	}
	if lister.historyCalls != 0 {
		t.Fatalf("默认不该走含历史的那条查询，实际调了 %d 次", lister.historyCalls)
	}
	// 传给 SQL 的时刻必须是 handler 那一刻，不是零值——零值会让
	// starts_at <= $2 恒假，「生效中」永远是空列表。
	if !lister.gotAt.Equal(silenceNow) {
		t.Fatalf("传给 ListActiveSilences 的时刻 = %s, want %s", lister.gotAt, silenceNow)
	}
	if lister.gotEnv != "development" {
		t.Fatalf("environment = %q, want development", lister.gotEnv)
	}
}

// TestListSilencesStateAllUsesHistoryQuery：state=all 走含已过期的那条。
func TestListSilencesStateAllUsesHistoryQuery(t *testing.T) {
	lister := &fakeSilenceLister{}
	if rec := getSilences(t, silenceRouter(t, lister), "?state=all", "ops.read"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if lister.historyCalls != 1 || lister.activeCalls != 0 {
		t.Fatalf("state=all 应只调 ListSilences：history=%d active=%d",
			lister.historyCalls, lister.activeCalls)
	}
	if lister.gotLimit != defaultSilenceLimit {
		t.Fatalf("默认 limit = %d, want %d", lister.gotLimit, defaultSilenceLimit)
	}
}

// TestListSilencesStateIsCaseInsensitive：与 /alerts 的 status=all 同样宽容。
func TestListSilencesStateIsCaseInsensitive(t *testing.T) {
	for _, q := range []string{"?state=ALL", "?state=All"} {
		lister := &fakeSilenceLister{}
		if rec := getSilences(t, silenceRouter(t, lister), q, "ops.read"); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", q, rec.Code)
		}
		if lister.historyCalls != 1 {
			t.Fatalf("%s: 应走含历史的查询", q)
		}
	}
}

// TestListSilencesRejectsUnknownState：拼错的 state 当场 400，且文案逐字。
//
// 不回落到默认：一个 state=expired（本端点不支持）被当成 active 的话，
// 调用方会拿到一份「生效中」的列表却以为是已过期的——两者的条目长得
// 一模一样，没有任何线索能让人发现。
func TestListSilencesRejectsUnknownState(t *testing.T) {
	const wantMsg = "state 必须是 active（默认，只看此刻生效的）或 all（含未开始与已过期）"
	for _, q := range []string{"?state=expired", "?state=scheduled", "?state=bogus", "?state=activ"} {
		rec := getSilences(t, silenceRouter(t, &fakeSilenceLister{}), q, "ops.read")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400（body=%s）", q, rec.Code, rec.Body.String())
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: 解析错误响应: %v（body=%s）", q, err, rec.Body.String())
		}
		if body.Error.Code != string(action.CodeInvalidParams) {
			t.Fatalf("%s: code = %q, want %q", q, body.Error.Code, action.CodeInvalidParams)
		}
		if body.Error.Message != wantMsg {
			t.Fatalf("%s: message = %q, want %q", q, body.Error.Message, wantMsg)
		}
	}
}

// TestListSilencesRejectsBadLimit：limit 必须是正整数，文案与 /alerts 逐字一致。
func TestListSilencesRejectsBadLimit(t *testing.T) {
	const wantMsg = "limit 必须是正整数"
	for _, q := range []string{"?limit=0", "?limit=-1", "?limit=abc"} {
		rec := getSilences(t, silenceRouter(t, &fakeSilenceLister{}), q, "ops.read")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", q, rec.Code)
		}
		var body struct {
			Error struct{ Message string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: 解析错误响应: %v", q, err)
		}
		if body.Error.Message != wantMsg {
			t.Fatalf("%s: message = %q, want %q", q, body.Error.Message, wantMsg)
		}
	}
	lister := &fakeSilenceLister{}
	if rec := getSilences(t, silenceRouter(t, lister), "?state=all&limit=25", "ops.read"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if lister.gotLimit != 25 {
		t.Fatalf("limit = %d, want 25", lister.gotLimit)
	}
}

// TestListSilencesActivePathHonoursLimit：ListActiveSilences 的 SQL 没有 LIMIT，
// limit 由 handler 兑现，且截断要如实说。
func TestListSilencesActivePathHonoursLimit(t *testing.T) {
	lister := &fakeSilenceLister{active: []alerts.Silence{
		silence("一", -time.Hour, time.Hour),
		silence("二", -time.Hour, time.Hour),
		silence("三", -time.Hour, time.Hour),
	}}
	got := decodeSilences(t, getSilences(t, silenceRouter(t, lister), "?limit=2", "ops.read"))
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2（limit 未兑现）", len(got.Items))
	}
	if got.Limit != 2 {
		t.Fatalf("limit = %d, want 2", got.Limit)
	}
	if !got.Truncated {
		t.Fatal("正好取满 limit 时 truncated 必须为 true")
	}
}

// TestListSilencesNotTruncatedWhenUnderLimit：没取满就不能说可能截断。
func TestListSilencesNotTruncatedWhenUnderLimit(t *testing.T) {
	lister := &fakeSilenceLister{active: []alerts.Silence{silence("独苗", -time.Hour, time.Hour)}}
	got := decodeSilences(t, getSilences(t, silenceRouter(t, lister), "?limit=5", "ops.read"))
	if got.Truncated {
		t.Fatal("只有 1 条而 limit=5，truncated 应为 false")
	}
	if got.Limit != 5 {
		t.Fatalf("limit = %d, want 5", got.Limit)
	}
}

// TestListSilencesReturnsAllFields：字段逐个到达前端。
//
// 「是谁按的、为什么、什么时候到期」是这一页存在的全部理由，
// 少任何一个这条端点就白加了。
func TestListSilencesReturnsAllFields(t *testing.T) {
	s := silence("上游在维护，先压两小时", -30*time.Minute, 90*time.Minute)
	lister := &fakeSilenceLister{active: []alerts.Silence{s}}
	got := decodeSilences(t, getSilences(t, silenceRouter(t, lister), "", "ops.read"))

	if len(got.Items) != 1 {
		t.Fatalf("items = %d", len(got.Items))
	}
	item := got.Items[0]
	if item.ID != s.ID.String() {
		t.Fatalf("id = %q, want %q", item.ID, s.ID)
	}
	if item.RuleKey != alerts.RuleMetricSyncFailed {
		t.Fatalf("rule_key = %q", item.RuleKey)
	}
	if item.Reason != "上游在维护，先压两小时" {
		t.Fatalf("reason = %q", item.Reason)
	}
	if item.CreatedBy != "staff_alice" {
		t.Fatalf("created_by = %q", item.CreatedBy)
	}
	if item.Environment != "development" {
		t.Fatalf("environment = %q", item.Environment)
	}
	if item.StartsAt != "2026-09-08T11:30:00Z" || item.EndsAt != "2026-09-08T13:30:00Z" {
		t.Fatalf("起止时间应是 UTC RFC3339: %+v", item)
	}
	if item.CreatedAt != "2026-09-07T12:00:00Z" {
		t.Fatalf("created_at = %q", item.CreatedAt)
	}
}

// TestListSilencesKeepsGlobalRuleKeyEmpty：全局窗口的 rule_key 保持空串。
//
// 换成 "*" 或 "all" 之类的哨兵值会与一条真叫这个名字的规则撞车；
// 翻译成中文留给前端。
func TestListSilencesKeepsGlobalRuleKeyEmpty(t *testing.T) {
	global := silence("全线维护", -time.Hour, time.Hour)
	global.RuleKey = ""
	lister := &fakeSilenceLister{active: []alerts.Silence{global}}
	got := decodeSilences(t, getSilences(t, silenceRouter(t, lister), "", "ops.read"))

	if len(got.Items) != 1 {
		t.Fatalf("items = %d", len(got.Items))
	}
	if got.Items[0].RuleKey != "" {
		t.Fatalf("全局窗口的 rule_key 应保持空串，实际 %q", got.Items[0].RuleKey)
	}
	// 全局窗口同样要被判成生效中——它是压得最狠的那一种，
	// 恰恰最不能从「现在有什么生效」里漏掉。
	if got.Items[0].State != "active" {
		t.Fatalf("全局窗口 state = %q, want active", got.Items[0].State)
	}
}

// TestListSilencesRequiresScope：读也要权限（规格 §2.4）。
func TestListSilencesRequiresScope(t *testing.T) {
	rec := getSilences(t, silenceRouter(t, &fakeSilenceLister{}), "", "registry.read")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（body=%s）", rec.Code, rec.Body.String())
	}
}

// TestListSilencesAcceptsAlertsReadScope：与 /alerts 同一个 scope，不另开一个。
func TestListSilencesAcceptsAlertsReadScope(t *testing.T) {
	if alerts.ScopeRead != "ops.read" {
		t.Fatalf("alerts.ScopeRead = %q，本用例与路由都按 ops.read 写", alerts.ScopeRead)
	}
	rec := getSilences(t, silenceRouter(t, &fakeSilenceLister{}), "", alerts.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
}

// TestListSilencesRejectsCrossEnvironmentRead：生产权限不继承（规格 §20.5）。
func TestListSilencesRejectsCrossEnvironmentRead(t *testing.T) {
	rec := getSilences(t, silenceRouter(t, &fakeSilenceLister{}), "?environment=production", "ops.read")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（body=%s）", rec.Code, rec.Body.String())
	}
}

// TestListSilencesEmptyReturnsEmptyArray：没有窗口时返回空数组而不是 null。
func TestListSilencesEmptyReturnsEmptyArray(t *testing.T) {
	rec := getSilences(t, silenceRouter(t, &fakeSilenceLister{}), "", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("响应不是合法 JSON: %s", rec.Body.String())
	}
	var got silencesBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if got.Items == nil {
		t.Fatal("items 应是空数组而不是 null")
	}
}

// TestListSilencesHidesStoreErrorDetails：底层错误不透传（规格 §18.4）。
func TestListSilencesHidesStoreErrorDetails(t *testing.T) {
	lister := &fakeSilenceLister{err: errors.New("pq: relation \"alerts.alert_silence\" does not exist")}
	rec := getSilences(t, silenceRouter(t, lister), "", "ops.read")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"relation", "alert_silence", "pq:"} {
		if strings.Contains(body, leak) {
			t.Fatalf("响应泄漏了底层错误（片段 %q）: %s", leak, body)
		}
	}
}

// TestSilencesEndpointIsNoStore：/api/v1 下每一条响应都不可缓存。
func TestSilencesEndpointIsNoStore(t *testing.T) {
	rec := getSilences(t, silenceRouter(t, &fakeSilenceLister{}), "", "ops.read")
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Fatal("缺少 Cache-Control（应由 /api/v1 组上的 NoStore 中间件设置）")
	}
}
