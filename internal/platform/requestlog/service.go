// Package requestlog 是「请求详情」的平台侧只读查询入口。
//
// 它站在 HTTP 层与 reqlog 连接器之间，只做三件连接器不该做、Handler 又不该
// 各写一遍的事：
//
//  1. **正文读取落审计**——每次成功读取写一条 request.content.viewed，
//     写不进去就不返回内容（fail closed）；
//  2. **平台 → 来源的映射**——路由里的 {platform} 不是所有值都有请求数据，
//     reqlog 只抄 NewAPI 与 Sub2API 两家；
//  3. **错误分类到 Action 错误码的翻译**——连接器给的是 connector.ErrorKind，
//     HTTP 层要的是 action.Code，翻译表只该有一份。
//
// 本包**不缓存任何内容，也不往平台库写任何正文**（宪法 5 条：平台不拥有第三方
// 业务真相；交接文档 §9.4：完整内容属于高敏数据）。唯一落库的是审计事件，
// 而那条事件里只有「谁看了哪一条」，没有一个字的对话内容。
package requestlog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// AuditAppender 是审计追加能力（*audit.Store 满足）。
//
// 声明成窄接口而不是直接吃 *audit.Store：本包的核心纪律（写不进审计就不返回
// 内容）必须能在**没有数据库**的机器上被测到，否则它会在 CI 之外被 t.Skip 掉
// ——而这正是最该一直跑着的那条断言。
type AuditAppender interface {
	Append(ctx context.Context, e audit.Event) (audit.Event, error)
}

// Client 是本包需要的连接器能力（reqlog.ReadClient 满足）。
type Client interface {
	ListRequests(ctx context.Context, filter reqlog.ListFilter) (reqlog.RequestLogPage, error)
	RequestContent(ctx context.Context, source, id string) (reqlog.RequestLogContent, error)
}

// Service 是请求详情的只读查询入口。
type Service struct {
	client Client
	audit  AuditAppender
	now    func() time.Time
}

// Option 调整 Service 的构造。
type Option func(*Service)

// WithClock 注入时钟（测试用；默认 time.Now）。
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService 构造查询入口。
//
// audit 为 nil 会在构造期就被拒绝，而不是等到第一次读正文时才发现：
// 一个「审计没接上」的进程能正常列出请求、却在有人点开详情时才崩，
// 是最坏的失败时机——那时候已经有人在等着看内容了。
func NewService(client Client, sink AuditAppender, opts ...Option) (*Service, error) {
	if client == nil {
		return nil, errors.New("requestlog: client 为空")
	}
	if sink == nil {
		return nil, errors.New("requestlog: 审计 Sink 为空——正文读取必须留审计（交接文档 §9.4）")
	}
	s := &Service{client: client, audit: sink, now: time.Now}
	for _, o := range opts {
		if o != nil {
			o(s)
		}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// SupportsPlatform 判断某个平台有没有请求数据。
//
// reqlog 只在 nginx 与 NewAPI/Sub2API 之间抄录，别的被管平台（CPA、开票、
// 支付、服务器）根本不在它的代理路径上。前端据此决定「请求」页签对哪些平台
// 显示——给一个不在抄录范围内的平台挂一个永远空的页签，等于告诉运营
// 「这个平台没有请求」，而事实是我们压根没抄它。
func SupportsPlatform(platform string) bool {
	_, err := reqlog.ParseSource(platform)
	return err == nil
}

// ListInput 是列表查询的入参。
type ListInput struct {
	Platform string
	Username string
	Model    string
	Status   reqlog.StatusFilter
	Since    time.Time
	Until    time.Time
	Limit    int
	Cursor   string
}

// List 读取请求元数据分页。
//
// 权限（ScopeRead）由路由上的 RequireScope 判定，不在此处重复——
// 权限声明集中在路由表上，散在 Service 里的第二套判定谁也审计不了。
func (s *Service) List(ctx context.Context, in ListInput) (reqlog.RequestLogPage, error) {
	source, err := s.resolvePlatform(in.Platform)
	if err != nil {
		return reqlog.RequestLogPage{}, err
	}
	page, err := s.client.ListRequests(ctx, reqlog.ListFilter{
		Source:   source,
		Username: in.Username,
		Model:    in.Model,
		Status:   in.Status,
		Since:    in.Since,
		Until:    in.Until,
		Limit:    in.Limit,
		Cursor:   in.Cursor,
	})
	if err != nil {
		return reqlog.RequestLogPage{}, translateError("requests.list", err)
	}
	return page, nil
}

// ContentInput 是正文读取的入参。
type ContentInput struct {
	Principal principal.Principal
	Platform  string
	// ID 是 reqlog 侧的记录标识，平台一律当不透明字符串。
	ID string
	// Reason 是调用方声明的查看原因，写进审计事件。
	//
	// 目前**可选**：交接文档 §9.4 要求「记录查看人、时间、原因和 request_id」，
	// 四样里只有原因需要人来填，而「必填原因」是一条会改变日常操作手感的
	// 产品决定（每看一条都要打字），不该由实现顺手定下来。留空时审计里
	// 就是空——那本身也是一条可查的事实：谁在没写原因的情况下看了多少条。
	Reason string
	// RequestID 来自 X-Request-ID 中间件，用于把审计事件与访问日志对上。
	RequestID string
}

// Content 读取单条请求的完整内容，并把这次查看写进审计链。
//
// 顺序是**先取内容、再落审计、最后才返回**，三点都不能换：
//
//   - 先审计后取内容 → 取失败时链上留下一条「看过了」，而其实没看到；
//   - 审计失败仍返回 → 一次**无记录的高敏数据披露**。交接文档 §9.4 把
//     「查看动作进入审计」列为这条能力成立的前提，前提不成立就不该有能力。
//     所以这里 fail closed：内容已经在进程内存里了，但一个字都不出去。
//
// fail closed 的代价是数据库抖动期间详情页打不开。这是刻意选的：
// 打不开是一次可见的、会被报障的故障；而静默的无审计披露没有任何人会发现。
//
// 失败的读取（记录不存在、上游不可用）**不写审计事件**：那是一次没有发生的
// 披露，访问日志已经记下了这次尝试。把尝试也写进审计链会让「谁看过什么」
// 这份清单里混进大量没看到的条目，反而更难读（这条口径写进了契约文档，
// 将来若要改成连尝试也记，改的是这里 + 契约表）。
func (s *Service) Content(ctx context.Context, in ContentInput) (reqlog.RequestLogContent, error) {
	source, err := s.resolvePlatform(in.Platform)
	if err != nil {
		return reqlog.RequestLogContent{}, err
	}
	content, err := s.client.RequestContent(ctx, source, in.ID)
	if err != nil {
		return reqlog.RequestLogContent{}, translateError("requests.content", err)
	}

	if _, err := s.audit.Append(ctx, contentViewedEvent(s.now().UTC(), in, source, content)); err != nil {
		// 审计失败的**根因不进响应**：它可能带库表名与连接串片段。
		// 响应里只说「这次读取没能留下审计记录，因此不返回内容」——
		// 那已经足够让人知道去查什么了。
		return reqlog.RequestLogContent{}, action.NewError(action.CodeInternal,
			"本次查看未能写入审计，按规定不返回内容", err)
	}
	return content, nil
}

// resolvePlatform 把路由里的 {platform} 翻译成 reqlog 的 source。
//
// 不认识的平台归 NOT_REGISTERED（404）而不是 INVALID_PARAMS（400）：
// 交接文档 §8 要求「未知对象显示 Not Found，不允许默默回退到第一条样例记录」。
// 而对一个**存在但没有请求数据**的平台（CPA、开票）同样是 404——
// 从这个端点的角度，那个平台的请求资源确实不存在。
func (s *Service) resolvePlatform(platform string) (string, error) {
	source, err := reqlog.ParseSource(platform)
	if err != nil {
		return "", action.NewError(action.CodeNotRegistered,
			fmt.Sprintf("平台 %q 没有请求数据：请求审计只覆盖 NewAPI 与 Sub2API", platform), err)
	}
	return source, nil
}

// translateError 把连接器错误翻译成 Action 错误码。
//
// 翻译表只该有一份：散在两个 Handler 里的 switch 迟早会漂开，
// 于是同一种上游故障在列表页和详情页显示成两回事。
//
// **上游错误的原始文本一个字都不进 Message**：连接器已经保证了 Error() 只有
// 「分类 + 操作名」，但这里再收一道——Message 是要回给前端的（规格 §18.4），
// 而这条通道上游的错误正文里装的正是用户对话明文。
func translateError(op string, err error) error {
	if err == nil {
		return nil
	}
	// 记录不存在的判据是 errors.Is 而不是 Kind：connector.ErrorKind 里没有
	// 「记录不存在」这一档（见 reqlog.ErrNotFound 的说明）
	if errors.Is(err, reqlog.ErrNotFound) {
		return action.NewError(action.CodeNotRegistered,
			"没有这条请求记录：可能已过保留期，或平台选错了", err)
	}
	switch connector.KindOf(err) {
	case connector.KindBadResponse:
		// 本包传给连接器的过滤条件已经被 Handler 校验过一轮，走到这里的
		// bad_response 多半是**参数**问题（未知 source、坏游标），
		// 归 400 让调用方去改请求，而不是让他以为平台坏了
		return action.NewError(action.CodeInvalidParams,
			"查询条件不合法", err)
	case connector.KindAuth:
		return action.NewError(action.CodeExecutionFailed,
			"请求审计系统拒绝了平台的只读凭据", err)
	case connector.KindRateLimited:
		return action.NewError(action.CodeExecutionFailed,
			"请求审计系统限流，请稍后重试", err)
	case connector.KindNotSupported:
		// 真实控制台 API 形状未核实时走这里（fake 模式不会）。
		// 501 而不是 502：这不是上游坏了，是平台这条链路还没接通——
		// 前端据此显示「功能待上线」而不是「上游故障」（见 StatusForCode）
		return action.NewError(action.CodeAdvancedControlsRequired,
			"请求详情尚未接通真实数据源（XM-0039：控制台 API 形状待核对）", err)
	case connector.KindForbiddenTarget, connector.KindWriteAttempt:
		// 护栏拒绝 = 配置错了或代码里出现了写路径，都是平台自己的问题
		return action.NewError(action.CodeInternal, "服务内部错误", err)
	default:
		return action.NewError(action.CodeExecutionFailed,
			"请求审计系统暂时不可用", fmt.Errorf("%s: %w", op, err))
	}
}
