package requestlog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// recordingSink 记下写进来的审计事件；err 非空时模拟审计写入失败。
type recordingSink struct {
	events []audit.Event
	err    error
}

func (s *recordingSink) Append(_ context.Context, e audit.Event) (audit.Event, error) {
	if s.err != nil {
		return audit.Event{}, s.err
	}
	s.events = append(s.events, e)
	return e, nil
}

// failingClient 让每个方法返回指定错误，用来验证错误翻译表。
type failingClient struct{ err error }

func (c failingClient) ListRequests(context.Context, reqlog.ListFilter) (reqlog.RequestLogPage, error) {
	return reqlog.RequestLogPage{}, c.err
}

func (c failingClient) RequestContent(context.Context, string, string) (reqlog.RequestLogContent, error) {
	return reqlog.RequestLogContent{}, c.err
}

var testPrincipal = principal.Principal{
	ID: "u-42", Type: principal.TypeHuman, Issuer: "test",
	Environment: "development", Scopes: []string{ScopeRead, ScopeContentRead},
}

var fixedNow = time.Date(2026, 8, 28, 9, 30, 0, 0, time.UTC)

func newTestService(t *testing.T, client Client, sink AuditAppender) *Service {
	t.Helper()
	s, err := NewService(client, sink, WithClock(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s
}

// firstRecordID 从 fake 里挑一条真实存在的记录，避免把 id 写死在测试里。
func firstRecordID(t *testing.T, s *Service, platform string) string {
	t.Helper()
	page, err := s.List(context.Background(), ListInput{Platform: platform})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("平台 %s 没有样本", platform)
	}
	return page.Items[0].ID
}

func TestNewServiceRequiresAuditSink(t *testing.T) {
	// 「审计没接上」必须在启动期就炸，而不是等到有人点开详情页那一刻
	if _, err := NewService(reqlog.NewFake(reqlog.FakeOptions{}), nil); err == nil {
		t.Fatal("审计 Sink 为空应被拒绝：正文读取必须留审计（交接文档 §9.4）")
	}
	if _, err := NewService(nil, &recordingSink{}); err == nil {
		t.Fatal("client 为空应被拒绝")
	}
}

func TestContentWritesAuditEvent(t *testing.T) {
	sink := &recordingSink{}
	s := newTestService(t, reqlog.NewFake(reqlog.FakeOptions{}), sink)
	id := firstRecordID(t, s, reqlog.SourceSub2API)

	content, err := s.Content(context.Background(), ContentInput{
		Principal: testPrincipal, Platform: reqlog.SourceSub2API, ID: id,
		Reason: "客诉核查 #8812", RequestID: "req-abc",
	})
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if content.Summary.ID != id {
		t.Fatalf("返回的内容对不上：%q vs %q", content.Summary.ID, id)
	}

	if len(sink.events) != 1 {
		t.Fatalf("应写一条审计事件, got %d", len(sink.events))
	}
	e := sink.events[0]
	if e.ActionID != EventContentViewed || e.ActionVersion != EventVersion {
		t.Fatalf("事件类型不对: %s@%s", e.ActionID, e.ActionVersion)
	}
	if e.PrincipalID != testPrincipal.ID || e.PrincipalType != testPrincipal.Type {
		t.Fatalf("身份没记对: %s/%s", e.PrincipalID, e.PrincipalType)
	}
	if e.Environment != testPrincipal.Environment {
		t.Fatalf("环境没记对: %s", e.Environment)
	}
	if e.Reason != "客诉核查 #8812" || e.RequestID != "req-abc" {
		t.Fatalf("原因/请求 ID 没记对: %q %q", e.Reason, e.RequestID)
	}
	if e.Result != audit.ResultSucceeded {
		t.Fatalf("结果应为 succeeded, got %s", e.Result)
	}
	if !e.OccurredAt.Equal(fixedNow) {
		t.Fatalf("时刻应取注入的时钟, got %v", e.OccurredAt)
	}
	// resource_id 必须带上 source：两个来源的 id 可能重号，
	// 只记 id 的话「看的是哪一条」在审计里就答不上来
	if want := reqlog.SourceSub2API + "/" + id; e.ResourceID != want {
		t.Fatalf("ResourceID = %q, want %q", e.ResourceID, want)
	}
	if e.ResourceType != ResourceTypeRequest {
		t.Fatalf("ResourceType = %q", e.ResourceType)
	}
	// 读取没有 Action 运行记录可指——不该现编一个随机 UUID
	if e.ActionRunID != uuid.Nil {
		t.Fatalf("ActionRunID 应为零值, got %v", e.ActionRunID)
	}
}

func TestAuditSummaryCarriesNoRequestBody(t *testing.T) {
	// 审计链是 append-only 的：正文一旦进去就**永远**在那里，改读 API 只是
	// 不再显示它。所以这条断言拦的是「往摘要里顺手多加一个字段」。
	sink := &recordingSink{}
	s := newTestService(t, reqlog.NewFake(reqlog.FakeOptions{}), sink)
	id := firstRecordID(t, s, reqlog.SourceSub2API)

	content, err := s.Content(context.Background(), ContentInput{
		Principal: testPrincipal, Platform: reqlog.SourceSub2API, ID: id,
	})
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	encoded, err := json.Marshal(sink.events[0].AfterSummary)
	if err != nil {
		t.Fatalf("序列化摘要: %v", err)
	}
	blob := string(encoded)

	for _, m := range content.Messages {
		needle := []rune(m.Content)
		if len(needle) < 24 {
			continue
		}
		if strings.Contains(blob, string(needle[:24])) {
			t.Fatalf("审计摘要里出现了正文片段（角色 %s）", m.Role)
		}
	}
	if len(content.FinalReply) > 24 && strings.Contains(blob, content.FinalReply[:24]) {
		t.Fatal("审计摘要里出现了最终回复片段")
	}
	// token_prefix 也不该在里面：audit.read 与 request.content.read 是两个
	// 独立授予的权限，摘要会经 /api/v1/audit/events 回显给前者
	if content.Summary.TokenPrefix != "" && strings.Contains(blob, content.Summary.TokenPrefix) {
		t.Fatal("审计摘要里出现了 token_prefix")
	}
	// 但披露面的大小必须记下来，否则「谁在批量翻看内容」查不出来
	for _, key := range []string{"message_count", "final_reply_bytes", "record_id", "source"} {
		if _, ok := sink.events[0].AfterSummary[key]; !ok {
			t.Fatalf("审计摘要缺少 %s", key)
		}
	}
}

func TestContentFailsClosedWhenAuditFails(t *testing.T) {
	// 审计写不进去就不返回内容：一次无记录的高敏披露没有任何人会发现，
	// 而打不开详情页是一次会被报障的可见故障
	sink := &recordingSink{err: errors.New("audit chain locked: pq: connection reset")}
	s := newTestService(t, reqlog.NewFake(reqlog.FakeOptions{}), sink)
	id := firstRecordID(t, s, reqlog.SourceSub2API)

	content, err := s.Content(context.Background(), ContentInput{
		Principal: testPrincipal, Platform: reqlog.SourceSub2API, ID: id,
	})
	if err == nil {
		t.Fatal("审计失败时不应返回内容")
	}
	if len(content.Messages) != 0 || content.FinalReply != "" || content.RawRequest.Body != "" {
		t.Fatal("审计失败时返回值必须是空的——内容一个字都不该出去")
	}

	var ae *action.Error
	if !errors.As(err, &ae) || ae.Code != action.CodeInternal {
		t.Fatalf("应为 INTERNAL, got %v", err)
	}
	// 审计失败的根因不进对外文案（它可能带库表名与连接串片段）
	if strings.Contains(ae.Message, "pq:") || strings.Contains(ae.Message, "connection reset") {
		t.Fatalf("对外文案泄漏了根因: %q", ae.Message)
	}
}

func TestFailedReadWritesNoAuditEvent(t *testing.T) {
	// 一次没有发生的披露不该出现在「谁看过什么」的清单里
	sink := &recordingSink{}
	s := newTestService(t, reqlog.NewFake(reqlog.FakeOptions{}), sink)

	if _, err := s.Content(context.Background(), ContentInput{
		Principal: testPrincipal, Platform: reqlog.SourceSub2API, ID: "no-such-record",
	}); err == nil {
		t.Fatal("不存在的记录应报错")
	}
	if len(sink.events) != 0 {
		t.Fatalf("读取失败不该写审计事件, got %d 条", len(sink.events))
	}
}

func TestUnknownPlatformIsNotFound(t *testing.T) {
	s := newTestService(t, reqlog.NewFake(reqlog.FakeOptions{}), &recordingSink{})
	ctx := context.Background()

	// 「不存在的平台」与「存在但不在抄录范围内的平台」都是 404：
	// 从这个端点看，那个平台的请求资源确实不存在（交接文档 §8）
	for _, platform := range []string{"cpa", "invoice", "payment", "nope", ""} {
		if _, err := s.List(ctx, ListInput{Platform: platform}); !isCode(err, action.CodeNotRegistered) {
			t.Errorf("List(%q) 应为 NOT_REGISTERED, got %v", platform, err)
		}
		if _, err := s.Content(ctx, ContentInput{
			Principal: testPrincipal, Platform: platform, ID: "x",
		}); !isCode(err, action.CodeNotRegistered) {
			t.Errorf("Content(%q) 应为 NOT_REGISTERED, got %v", platform, err)
		}
	}
}

func TestMissingRecordIsNotFound(t *testing.T) {
	s := newTestService(t, reqlog.NewFake(reqlog.FakeOptions{}), &recordingSink{})
	id := firstRecordID(t, s, reqlog.SourceSub2API)

	// 跨来源读取必须落空，不能因为 id 恰好唯一就静默命中另一个平台的用户数据
	if _, err := s.Content(context.Background(), ContentInput{
		Principal: testPrincipal, Platform: reqlog.SourceNewAPI, ID: id,
	}); !isCode(err, action.CodeNotRegistered) {
		t.Fatalf("跨来源读取应为 NOT_REGISTERED, got %v", err)
	}
}

func TestErrorTranslation(t *testing.T) {
	cases := []struct {
		kind connector.ErrorKind
		want action.Code
	}{
		{connector.KindUnavailable, action.CodeExecutionFailed},
		{connector.KindAuth, action.CodeExecutionFailed},
		{connector.KindRateLimited, action.CodeExecutionFailed},
		{connector.KindBadResponse, action.CodeInvalidParams},
		// 「链路还没接通」不是「上游坏了」：前端据此显示「功能待上线」
		{connector.KindNotSupported, action.CodeAdvancedControlsRequired},
		{connector.KindForbiddenTarget, action.CodeInternal},
		{connector.KindWriteAttempt, action.CodeInternal},
	}
	for _, c := range cases {
		s := newTestService(t,
			failingClient{err: connector.NewError(c.kind, "reqlog.test", nil)}, &recordingSink{})
		_, err := s.List(context.Background(), ListInput{Platform: reqlog.SourceSub2API})
		if !isCode(err, c.want) {
			t.Errorf("%s 应翻译成 %s, got %v", c.kind, c.want, err)
		}
	}
}

func TestUpstreamErrorTextNeverReachesMessage(t *testing.T) {
	// 这条通道上游的错误正文里装的正是用户对话明文
	const leaky = "用户问：我的银行卡号是 6222"
	s := newTestService(t,
		failingClient{err: connector.NewError(connector.KindUnavailable, "reqlog.test",
			errors.New(leaky))}, &recordingSink{})

	_, err := s.List(context.Background(), ListInput{Platform: reqlog.SourceSub2API})
	var ae *action.Error
	if !errors.As(err, &ae) {
		t.Fatalf("应为 action.Error, got %v", err)
	}
	if strings.Contains(ae.Message, leaky) {
		t.Fatalf("上游错误文本泄漏进对外文案: %q", ae.Message)
	}
	// 根因仍在 Unwrap 链里，服务端日志查得到
	if !strings.Contains(err.Error()+errorChainText(err), "reqlog.test") {
		t.Fatal("根因应仍可从 Unwrap 链取到")
	}
}

func TestListPassesFiltersThrough(t *testing.T) {
	s := newTestService(t, reqlog.NewFake(reqlog.FakeOptions{}), &recordingSink{})
	ctx := context.Background()

	all, err := s.List(ctx, ListInput{Platform: reqlog.SourceSub2API, Limit: reqlog.MaxListLimit})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all.Items) == 0 {
		t.Fatal("样本为空")
	}
	target := all.Items[0].Username

	byUser, err := s.List(ctx, ListInput{
		Platform: reqlog.SourceSub2API, Username: target, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("按用户名过滤: %v", err)
	}
	if len(byUser.Items) == 0 || len(byUser.Items) >= len(all.Items) {
		t.Fatalf("过滤没有收窄结果: %d vs %d", len(byUser.Items), len(all.Items))
	}
	for _, item := range byUser.Items {
		if item.Username != target {
			t.Fatalf("过滤后混进了 %q", item.Username)
		}
	}
	if all.RetentionDays <= 0 {
		t.Fatal("保留期没有透传上来")
	}
}

func TestSupportsPlatform(t *testing.T) {
	for _, p := range []string{"sub2api", "newapi"} {
		if !SupportsPlatform(p) {
			t.Errorf("%s 应有请求数据", p)
		}
	}
	// reqlog 只在 nginx 与 NewAPI/Sub2API 之间抄录；给别的平台挂一个永远空的
	// 页签，等于说「这个平台没有请求」，而事实是我们压根没抄它
	for _, p := range []string{"cpa", "invoice", "payment", "server", "model-assurance", ""} {
		if SupportsPlatform(p) {
			t.Errorf("%s 不该被认为有请求数据", p)
		}
	}
}

func isCode(err error, want action.Code) bool {
	var ae *action.Error
	return errors.As(err, &ae) && ae.Code == want
}

func errorChainText(err error) string {
	var b strings.Builder
	for e := err; e != nil; e = errors.Unwrap(e) {
		b.WriteString(e.Error())
	}
	return b.String()
}
