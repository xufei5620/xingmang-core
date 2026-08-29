package platformusers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type stubClient struct {
	page         connusers.UserPage
	err          error
	got          connusers.ListFilter
	detail       connusers.UserDetail
	detailErr    error
	detailCalled bool
}

func (s *stubClient) ListUsers(_ context.Context, f connusers.ListFilter) (connusers.UserPage, error) {
	s.got = f
	return s.page, s.err
}

func (s *stubClient) GetUser(_ context.Context, q connusers.GetUserQuery) (connusers.UserDetail, error) {
	s.detailCalled = true
	if s.detailErr != nil {
		return connusers.UserDetail{}, s.detailErr
	}
	s.detail.Ref = q.Ref
	return s.detail, nil
}

type listOnlyClient struct{}

func (listOnlyClient) ListUsers(context.Context, connusers.ListFilter) (connusers.UserPage, error) {
	return connusers.UserPage{}, nil
}

func newService(t *testing.T, c platformusers.Client) *platformusers.Service {
	t.Helper()
	svc, err := platformusers.NewService(map[string]platformusers.Client{
		connusers.SourceSub2API: c,
		connusers.SourceNewAPI:  c,
	})
	if err != nil {
		t.Fatalf("构造 Service 失败: %v", err)
	}
	return svc
}

func codeOf(t *testing.T, err error) action.Code {
	t.Helper()
	var actErr *action.Error
	if !errors.As(err, &actErr) {
		t.Fatalf("期望 action.Error，得到 %T(%v)", err, err)
	}
	return actErr.Code
}

func TestNewServiceRejectsBadConfig(t *testing.T) {
	// 一个「一个平台都没接」的进程能正常启动、却在第一次点用户页签时才报错,
	// 是最坏的失败时机
	if _, err := platformusers.NewService(nil); err == nil {
		t.Fatal("空客户端表应当在构造期被拒绝")
	}
	if _, err := platformusers.NewService(map[string]platformusers.Client{
		"cpa": &stubClient{},
	}); err == nil {
		t.Fatal("未知平台应当在构造期被拒绝")
	}
	if _, err := platformusers.NewService(map[string]platformusers.Client{
		connusers.SourceSub2API: nil,
	}); err == nil {
		t.Fatal("nil 客户端应当在构造期被拒绝")
	}
}

func TestSupportsPlatform(t *testing.T) {
	for _, ok := range []string{"sub2api", "newapi"} {
		if !platformusers.SupportsPlatform(ok) {
			t.Fatalf("%q 应当有用户清单", ok)
		}
	}
	// 给 CPA 挂一个永远空的用户页签，等于告诉运营「这个平台没有用户」,
	// 而事实是 CPA 的「用户」是代理商，语义不同
	for _, no := range []string{"cpa", "server", "invoice", ""} {
		if platformusers.SupportsPlatform(no) {
			t.Fatalf("%q 不该有用户清单", no)
		}
	}
}

func TestListRejectsUnknownPlatformAs404(t *testing.T) {
	// 交接文档 §8：未知对象显示 Not Found
	svc := newService(t, &stubClient{})
	_, err := svc.List(context.Background(), platformusers.ListInput{Platform: "cpa"})
	if got := codeOf(t, err); got != action.CodeNotRegistered {
		t.Fatalf("未知平台应归 NOT_REGISTERED，得到 %q", got)
	}
}

func TestListRejectsUnconfiguredPlatform(t *testing.T) {
	// 平台认得，但这个进程没配它的客户端——是**配置**问题，不是地址问题
	svc, err := platformusers.NewService(map[string]platformusers.Client{
		connusers.SourceSub2API: &stubClient{},
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	_, err = svc.List(context.Background(), platformusers.ListInput{Platform: "newapi"})
	if got := codeOf(t, err); got != action.CodeNotRegistered {
		t.Fatalf("未接入的平台应归 NOT_REGISTERED，得到 %q", got)
	}
}

func TestListRejectsBadSortAndStatus(t *testing.T) {
	svc := newService(t, &stubClient{})
	// 拼错的排序键回落到默认，人会以为自己排过了
	_, err := svc.List(context.Background(), platformusers.ListInput{
		Platform: "sub2api", Sort: "balance",
	})
	if got := codeOf(t, err); got != action.CodeInvalidParams {
		t.Fatalf("坏排序键应归 INVALID_PARAMS，得到 %q", got)
	}
	// 一个 status=Active（大小写错）被悄悄当成「不筛」，人会照着一份没筛过的清单做判断
	_, err = svc.List(context.Background(), platformusers.ListInput{
		Platform: "sub2api", Status: "Active",
	})
	if got := codeOf(t, err); got != action.CodeInvalidParams {
		t.Fatalf("坏状态应归 INVALID_PARAMS，得到 %q", got)
	}
}

func TestListPassesFilterThrough(t *testing.T) {
	c := &stubClient{}
	svc := newService(t, c)
	if _, err := svc.List(context.Background(), platformusers.ListInput{
		Platform: "newapi", Query: "张伟", Status: "active",
		Sort: "consumed_desc", Limit: 20, Cursor: "abc",
	}); err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if c.got.Source != connusers.SourceNewAPI {
		t.Fatalf("来源透传有误: %q", c.got.Source)
	}
	if c.got.Query != "张伟" || c.got.Status != connusers.StatusActive ||
		c.got.Sort != connusers.SortConsumedDesc || c.got.Limit != 20 || c.got.Cursor != "abc" {
		t.Fatalf("过滤条件透传有误: %+v", c.got)
	}
}

func TestTranslateErrorKinds(t *testing.T) {
	cases := map[connector.ErrorKind]action.Code{
		connector.KindBadResponse: action.CodeInvalidParams,
		connector.KindAuth:        action.CodeExecutionFailed,
		connector.KindRateLimited: action.CodeExecutionFailed,
		// 501 而不是 502：不是上游坏了，是平台这条链路还没接通。
		// 前端据此显示「功能待上线」而不是「上游故障」
		connector.KindNotSupported: action.CodeAdvancedControlsRequired,
		// 护栏拒绝 = 平台自己的配置问题，不该说成上游故障
		connector.KindForbiddenTarget: action.CodeInternal,
		connector.KindWriteAttempt:    action.CodeInternal,
	}
	for kind, want := range cases {
		svc := newService(t, &stubClient{
			err: connector.NewError(kind, "platformusers.list_users", errors.New("boom")),
		})
		_, err := svc.List(context.Background(), platformusers.ListInput{Platform: "sub2api"})
		if got := codeOf(t, err); got != want {
			t.Errorf("%s 应归 %q，得到 %q", kind, want, got)
		}
	}
}

func TestGetUsesExactDetailReaderAndPreservesErrorBoundary(t *testing.T) {
	c := &stubClient{detail: connusers.UserDetail{User: connusers.User{ID: "u_1"}, Snapshot: connusers.EvidenceSnapshot{Source: "fake"}}}
	svc := newService(t, c).WithClock(func() time.Time { return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC) })
	detail, err := svc.Get(context.Background(), platformusers.DetailInput{Platform: "sub2api", UserID: "u_1"})
	if err != nil || !c.detailCalled || detail.Ref.ID != "u_1" {
		t.Fatalf("detail=%+v called=%v err=%v", detail, c.detailCalled, err)
	}
	c.detailErr = connusers.ErrLookupIncomplete
	_, err = svc.Get(context.Background(), platformusers.DetailInput{Platform: "sub2api", UserID: "u_1"})
	if got := codeOf(t, err); got != action.CodeExecutionFailed {
		t.Fatalf("incomplete code=%q", got)
	}
}

func TestGetWithoutDetailReaderIsUnavailableNotEmptyUser(t *testing.T) {
	svc, err := platformusers.NewService(map[string]platformusers.Client{connusers.SourceSub2API: listOnlyClient{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Get(context.Background(), platformusers.DetailInput{Platform: "sub2api", UserID: "u_1"})
	if got := codeOf(t, err); got != action.CodeAdvancedControlsRequired {
		t.Fatalf("code=%q err=%v", got, err)
	}
}

func TestTranslateErrorNeverLeaksUpstreamText(t *testing.T) {
	// Message 是要回给前端的（规格 §18.4），而上游的错误正文里可能带着
	// 用户邮箱与令牌
	secret := "zhangwei@example.com sk-livesecret"
	svc := newService(t, &stubClient{
		err: connector.NewError(connector.KindAuth, "op", errors.New(secret)),
	})
	_, err := svc.List(context.Background(), platformusers.ListInput{Platform: "sub2api"})
	var actErr *action.Error
	if !errors.As(err, &actErr) {
		t.Fatalf("期望 action.Error，得到 %T", err)
	}
	if actErr.Message == "" {
		t.Fatal("应当有一句面向人的说明")
	}
	if contains(actErr.Message, "zhangwei@") || contains(actErr.Message, "sk-live") {
		t.Fatalf("上游错误正文漏进了 Message: %q", actErr.Message)
	}
}

func TestNotFoundMapsTo404(t *testing.T) {
	svc := newService(t, &stubClient{err: connusers.ErrNotFound})
	_, err := svc.List(context.Background(), platformusers.ListInput{Platform: "sub2api"})
	if got := codeOf(t, err); got != action.CodeNotRegistered {
		t.Fatalf("记录不存在应归 NOT_REGISTERED，得到 %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// TestListResolvesPeriodServerSide:区间在**服务端**解析,前端只传原始意图。
//
// 让前端算「今天」的后果很具体:一个在 UTC-5 的运营,浏览器本地日期比业务日
// (CST +08:00)早一天,于是他看到的「今天」是账面上的昨天——而两份数都长得
// 像正常读数,没人会怀疑(宪法 14 条)。
func TestListResolvesPeriodServerSide(t *testing.T) {
	c := &stubClient{}
	svc := newService(t, c).WithClock(func() time.Time {
		// UTC 16:30 时 CST 已经是次日 00:30
		return time.Date(2026, 8, 27, 16, 30, 0, 0, time.UTC)
	})

	if _, err := svc.List(context.Background(), platformusers.ListInput{
		Platform: "sub2api",
	}); err != nil {
		t.Fatal(err)
	}
	if c.got.Period.Day != "2026-08-28" {
		t.Fatalf("默认业务日 = %q, want 2026-08-28(CST 已跨日)", c.got.Period.Day)
	}
	if c.got.Period.Granularity != connusers.GranularityDay {
		t.Fatalf("默认粒度 = %q, want day", c.got.Period.Granularity)
	}
}

// TestListPassesPeriodThrough:显式指定的日期与粒度原样解析后传下去。
func TestListPassesPeriodThrough(t *testing.T) {
	c := &stubClient{}
	svc := newService(t, c)

	if _, err := svc.List(context.Background(), platformusers.ListInput{
		Platform:    "newapi",
		Day:         "2026-08-27",
		Granularity: "week",
	}); err != nil {
		t.Fatal(err)
	}
	if c.got.Period.From != "2026-08-24" || c.got.Period.To != "2026-08-30" {
		t.Fatalf("区间 = %s..%s, want 2026-08-24..2026-08-30",
			c.got.Period.From, c.got.Period.To)
	}
}

// TestListRejectsBadPeriodAs400:坏日期/坏粒度是**调用方**的参数错误。
//
// 归 400 而不是 500,而且消息里要能看出错在哪一项——否则运营只会看到
// 「服务内部错误」,而实际上他只是把日期写成了 2026-8-1。
func TestListRejectsBadPeriodAs400(t *testing.T) {
	for _, in := range []platformusers.ListInput{
		{Platform: "sub2api", Day: "2026-8-1"},
		{Platform: "sub2api", Day: "昨天"},
		{Platform: "sub2api", Granularity: "weekly"},
	} {
		_, err := newService(t, &stubClient{}).List(context.Background(), in)
		if err == nil {
			t.Fatalf("%+v 应当被拒绝", in)
		}
		if got := codeOf(t, err); got != action.CodeInvalidParams {
			t.Fatalf("%+v 的错误码 = %v, want INVALID_PARAMS", in, got)
		}
	}
}
