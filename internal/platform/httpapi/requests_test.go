package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
)

const (
	scopeRequestRead    = requestlog.ScopeRead
	scopeRequestContent = requestlog.ScopeContentRead
	bothRequestScopes   = scopeRequestRead + "," + scopeRequestContent
)

// capturingSink 记下审计事件，供「查看必留痕」那条断言检查。
type capturingSink struct {
	events []audit.Event
	err    error
}

func (s *capturingSink) Append(_ context.Context, e audit.Event) (audit.Event, error) {
	if s.err != nil {
		return audit.Event{}, s.err
	}
	s.events = append(s.events, e)
	return e, nil
}

func requestsRouter(t *testing.T, sink *capturingSink) http.Handler {
	t.Helper()
	svc, err := requestlog.NewService(reqlog.NewFake(reqlog.FakeOptions{}), sink)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: &fakeExecutor{},
		ActionRegistry: action.NewRegistry(), RequestLogs: svc,
	})
}

func doGet(t *testing.T, h http.Handler, path, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeRequestPage(t *testing.T, rec *httptest.ResponseRecorder) requestPage {
	t.Helper()
	var page requestPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	return page
}

func firstRequestID(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := doGet(t, h, "/api/v1/platforms/sub2api/requests", scopeRequestRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	page := decodeRequestPage(t, rec)
	if len(page.Items) == 0 {
		t.Fatal("样本为空")
	}
	return page.Items[0].ID
}

func TestListRequestsRequiresRequestReadScope(t *testing.T) {
	h := requestsRouter(t, &capturingSink{})

	// 拿着别的读权限来也不行：request.read 是单独授予的一档
	for _, scopes := range []string{"", "ops.read", "audit.read", "registry.read"} {
		rec := doGet(t, h, "/api/v1/platforms/sub2api/requests", scopes)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("scopes=%q 应 403, got %d", scopes, rec.Code)
		}
		// 403 文案里要带上缺哪个权限，前端据此显示「需要 X」而不是干瞪眼
		if !strings.Contains(rec.Body.String(), scopeRequestRead) {
			t.Fatalf("403 文案应指明缺 %s: %s", scopeRequestRead, rec.Body.String())
		}
	}
	if rec := doGet(t, h, "/api/v1/platforms/sub2api/requests", scopeRequestRead); rec.Code != http.StatusOK {
		t.Fatalf("有 %s 应 200, got %d (%s)", scopeRequestRead, rec.Code, rec.Body.String())
	}
}

func TestRequestContentNeedsItsOwnScope(t *testing.T) {
	// 本切片最重要的一条授权断言：列表权限**不能**顺带看到正文
	h := requestsRouter(t, &capturingSink{})
	id := firstRequestID(t, h)
	path := "/api/v1/platforms/sub2api/requests/" + id

	rec := doGet(t, h, path, scopeRequestRead)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("只有 %s 时读正文应 403, got %d (%s)",
			scopeRequestRead, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), scopeRequestContent) {
		t.Fatalf("403 文案应指明缺 %s: %s", scopeRequestContent, rec.Body.String())
	}
	// 正文一个字都不该出现在 403 响应里
	if strings.Contains(rec.Body.String(), "messages") {
		t.Fatalf("403 响应里出现了内容字段: %s", rec.Body.String())
	}

	if rec := doGet(t, h, path, bothRequestScopes); rec.Code != http.StatusOK {
		t.Fatalf("两个权限齐备应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestListResponseCarriesNoBody(t *testing.T) {
	// 契约层已经保证摘要不含正文，这里守的是 HTTP 层：
	// 有人把 handler 改成直接序列化连接器结构体，这条会变红
	h := requestsRouter(t, &capturingSink{})
	rec := doGet(t, h, "/api/v1/platforms/sub2api/requests?limit=200", scopeRequestRead)
	body := rec.Body.String()

	for _, forbidden := range []string{
		"\"messages\"", "\"final_reply\"", "\"raw_request\"", "\"raw_response\"",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("列表响应里出现了正文字段 %s", forbidden)
		}
	}

	page := decodeRequestPage(t, rec)
	if len(page.Items) == 0 {
		t.Fatal("样本为空")
	}
	// 新鲜度不是可选字段（规格 §9.1）
	if page.Freshness.State == "" || page.Freshness.ObservedAt == nil {
		t.Fatalf("列表必须带新鲜度: %+v", page.Freshness)
	}
	// 保留期要能就地说清「只覆盖最近 N 天」
	if page.RetentionDays <= 0 {
		t.Fatalf("列表必须带保留期天数, got %d", page.RetentionDays)
	}
	// 来源必须回给前端：请求详情不走指标表，没有 observation 承载来源，
	// 缺了它前端只能假定这是真实用户对话——最坏的默认
	if page.DataSource != reqlog.FakeInstance {
		t.Fatalf("data_source = %q, want %q", page.DataSource, reqlog.FakeInstance)
	}
	// IP 必须是脱敏形态
	for _, item := range page.Items {
		if item.ClientIP != "" && !strings.HasSuffix(item.ClientIP, reqlog.MaskedIPSuffix) {
			t.Fatalf("记录 %s 的 client_ip %q 未脱敏", item.ID, item.ClientIP)
		}
	}
}

func TestTTFBNullIsDistinctFromZero(t *testing.T) {
	// 0 毫秒是合法观测值（缓存命中），null 是「没测」。序列化成同一个值的话，
	// 趋势图上会多出一批不存在的零延迟请求
	h := requestsRouter(t, &capturingSink{})
	rec := doGet(t, h, "/api/v1/platforms/sub2api/requests?limit=200", scopeRequestRead)

	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	sawNull, sawZero := false, false
	for _, item := range raw.Items {
		switch string(item["ttfb_ms"]) {
		case "null":
			sawNull = true
		case "0":
			sawZero = true
		}
	}
	if !sawNull {
		t.Fatal("样本里应有 ttfb_ms 为 null 的记录（非流式请求通常没测）")
	}
	if !sawZero {
		t.Fatal("样本里应有 ttfb_ms 为 0 的记录（缓存命中）——它与 null 必须分得开")
	}
}

func TestReadingContentWritesAuditEvent(t *testing.T) {
	sink := &capturingSink{}
	h := requestsRouter(t, sink)
	id := firstRequestID(t, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/platforms/sub2api/requests/"+id+"?reason="+url.QueryEscape("客诉核查"), nil)
	devHeaders(req, bothRequestScopes)
	req.Header.Set("X-Request-ID", "req-from-header")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 1 {
		t.Fatalf("每次查看正文应写一条审计事件, got %d", len(sink.events))
	}
	e := sink.events[0]
	if e.ActionID != requestlog.EventContentViewed {
		t.Fatalf("事件类型 = %q", e.ActionID)
	}
	if e.PrincipalID != "staff_alice" {
		t.Fatalf("查看人没记对: %q", e.PrincipalID)
	}
	if e.Reason != "客诉核查" {
		t.Fatalf("原因没记对: %q", e.Reason)
	}
	// request_id 必须来自中间件，这样审计事件与访问日志、错误响应对得上
	if e.RequestID != "req-from-header" {
		t.Fatalf("request_id 没记对: %q", e.RequestID)
	}
	if !strings.HasSuffix(e.ResourceID, id) || !strings.HasPrefix(e.ResourceID, "sub2api/") {
		t.Fatalf("resource_id = %q，应形如 sub2api/<id>", e.ResourceID)
	}

	// 列表读取**不**写审计事件：那一档权限低、量大，逐条记会把
	// 「谁看过正文」这份真正要查的清单淹掉
	doGet(t, h, "/api/v1/platforms/sub2api/requests", scopeRequestRead)
	if len(sink.events) != 1 {
		t.Fatalf("列表读取不该写审计事件, got %d 条", len(sink.events))
	}
}

func TestContentFailsClosedWhenAuditWriteFails(t *testing.T) {
	// 写不进审计就不返回内容——一次无记录的高敏披露没有任何人会发现
	h := requestsRouter(t, &capturingSink{})
	id := firstRequestID(t, h)

	failing := &capturingSink{err: errDBDown{}}
	hf := requestsRouter(t, failing)
	rec := doGet(t, hf, "/api/v1/platforms/sub2api/requests/"+id, bothRequestScopes)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("审计失败应 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{"messages", "final_reply", "raw_request"} {
		if strings.Contains(body, leak) {
			t.Fatalf("审计失败的响应里出现了内容字段 %q: %s", leak, body)
		}
	}
	_ = h
}

type errDBDown struct{}

func (errDBDown) Error() string { return "pq: connection reset by peer" }

func TestUnknownPlatformIs404(t *testing.T) {
	// 交接文档 §8：未知对象显示 Not Found，不允许默默回退到第一条样例记录
	h := requestsRouter(t, &capturingSink{})
	for _, platform := range []string{"cpa", "invoice", "payment", "nope"} {
		rec := doGet(t, h, "/api/v1/platforms/"+platform+"/requests", scopeRequestRead)
		if rec.Code != http.StatusNotFound {
			t.Errorf("平台 %q 应 404, got %d (%s)", platform, rec.Code, rec.Body.String())
		}
	}
	rec := doGet(t, h, "/api/v1/platforms/sub2api/requests/no-such-record", bothRequestScopes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的记录应 404, got %d", rec.Code)
	}
}

func TestInvalidFilterParamsAre400(t *testing.T) {
	h := requestsRouter(t, &capturingSink{})
	cases := map[string]string{
		"status 认不出": "?status=failed",
		"since 不是时间": "?since=yesterday",
		"until 不是时间": "?until=2026-08-28",
		"limit 不是数":  "?limit=abc",
		"limit 为负":   "?limit=-1",
		"游标被人改过":     "?cursor=%E4%B8%8D%E6%98%AF%E6%B8%B8%E6%A0%87",
		"时间区间空":      "?since=2026-08-28T10:00:00Z&until=2026-08-28T09:00:00Z",
	}
	for name, query := range cases {
		rec := doGet(t, h, "/api/v1/platforms/sub2api/requests"+query, scopeRequestRead)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s：应 400, got %d (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestFiltersAndPaginationOverHTTP(t *testing.T) {
	h := requestsRouter(t, &capturingSink{})

	all := decodeRequestPage(t, doGet(t,
		h, "/api/v1/platforms/sub2api/requests?limit=200", scopeRequestRead))
	if len(all.Items) < 4 {
		t.Fatalf("样本太少: %d", len(all.Items))
	}

	byUser := decodeRequestPage(t, doGet(t, h,
		"/api/v1/platforms/sub2api/requests?limit=200&username="+url.QueryEscape(all.Items[0].Username),
		scopeRequestRead))
	if len(byUser.Items) == 0 || len(byUser.Items) >= len(all.Items) {
		t.Fatalf("用户名过滤没有收窄: %d vs %d", len(byUser.Items), len(all.Items))
	}

	// 翻页：小页翻完必须与一次取满等价
	seen := map[string]bool{}
	cursor := ""
	for range 50 {
		path := "/api/v1/platforms/sub2api/requests?limit=3"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page := decodeRequestPage(t, doGet(t, h, path, scopeRequestRead))
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("翻页重复返回了 %s", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != len(all.Items) {
		t.Fatalf("翻页得到 %d 条，一次取满 %d 条", len(seen), len(all.Items))
	}
}

func TestContentResponseIsNotStored(t *testing.T) {
	// /api/v1 整组已有 NoStore，但正文这条尤其不能落浏览器磁盘缓存或中间代理
	// ——否则 §9.4「默认禁止导出」就成了一句只在按钮层面成立的话
	h := requestsRouter(t, &capturingSink{})
	id := firstRequestID(t, h)
	rec := doGet(t, h, "/api/v1/platforms/sub2api/requests/"+id, bothRequestScopes)

	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q，正文响应必须 no-store", cc)
	}
}

func TestContentResponseShape(t *testing.T) {
	h := requestsRouter(t, &capturingSink{})
	id := firstRequestID(t, h)
	rec := doGet(t, h, "/api/v1/platforms/sub2api/requests/"+id, bothRequestScopes)

	var body requestContentBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if body.Summary.ID != id {
		t.Fatalf("摘要对不上: %q vs %q", body.Summary.ID, id)
	}
	if len(body.Messages) == 0 || !body.MessagesParsed {
		t.Fatalf("这条样本应有分角色消息: %+v", body.Messages)
	}
	if body.Freshness.State == "" {
		t.Fatal("内容也必须带新鲜度")
	}
	// 详情页尤其要挂演示横幅：它最容易被当成真实用户问答截图转发出去
	if body.DataSource != reqlog.FakeInstance {
		t.Fatalf("data_source = %q, want %q", body.DataSource, reqlog.FakeInstance)
	}
	// 截断标记与原始字节数要成对出现，界面才说得出「只显示了 X / 共 Y」
	sawTruncated := false
	for _, m := range body.Messages {
		if m.Truncated {
			sawTruncated = true
			if m.OriginalBytes <= int64(len(m.Content)) {
				t.Fatalf("角色 %s 标了截断但 original_bytes 不大于当前长度", m.Role)
			}
		}
	}
	if !sawTruncated {
		t.Log("这条样本没有触发截断（不是错误，截断路径由连接器契约测试覆盖）")
	}
	if body.RawResponse.ContentType == "" {
		t.Fatal("原始载荷应带 content_type：SSE 与 JSON 的渲染方式不同")
	}
}

func TestRequestEndpointsAbsentWhenNotConfigured(t *testing.T) {
	// 没配 reqlog 的部署整组不挂载：端点不存在（404）比端点存在却一调就 500
	// 诚实——前端据此分得清「没接」和「坏了」
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: &fakeExecutor{},
		ActionRegistry: action.NewRegistry(),
	})
	for _, path := range []string{
		"/api/v1/platforms/sub2api/requests",
		"/api/v1/platforms/sub2api/requests/abc",
	} {
		rec := doGet(t, h, path, bothRequestScopes)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 未配置时应 404, got %d", path, rec.Code)
		}
	}
}
