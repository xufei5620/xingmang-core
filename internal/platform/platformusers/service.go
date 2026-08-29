// Package platformusers 是「被管平台终端用户清单」的平台侧只读查询入口
// (XM-0046,交接文档 §9.3)。
//
// 它站在 HTTP 层与 platformusers 连接器之间,只做三件连接器不该做、Handler
// 又不该各写一遍的事:
//
//  1. **平台 → 来源的映射**——路由里的 {platform} 不是所有值都有终端用户;
//  2. **错误分类到 Action 错误码的翻译**——翻译表只该有一份;
//  3. **逐平台的客户端选择**——sub2api 与 newapi 各有自己的连接器实例。
//
// 本包**不缓存、不落库**:用户明细是第三方系统的业务真相,平台只做带权限的
// 只读网关(宪法 5 条)。唯一会落库的将来会是审计,但列表读取暂不进审计——
// 与 request.read 一致(逐条**正文**才进审计,元数据列表不进),否则每打开一次
// 用户页签就往审计链里塞一条,那份「谁看过什么」的清单会被噪音淹掉。
package platformusers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// Client 是本包需要的连接器能力(platformusers.ReadClient 满足)。
type Client interface {
	ListUsers(ctx context.Context, filter platformusers.ListFilter) (platformusers.UserPage, error)
}

// Service 是终端用户清单的只读查询入口。
type Service struct {
	// clients 按 source 索引。缺一个平台就等于那个平台没接——
	// 而不是回落到另一个平台的客户端去(那会把 A 平台的用户显示成 B 的)
	clients       map[string]Client
	detailReaders map[string]platformusers.UserDetailReader
	dailyReaders  map[string]platformusers.DailyUsageReader
	keyReaders    map[string]platformusers.KeyMetadataReader
	// now 供测试注入固定时钟;nil 时用 time.Now。
	//
	// 需要时钟是因为「今天」要在**服务端**解释:让前端算「今天」的话,
	// 一个在 UTC-5 的运营看到的今天会比账面业务日早一天(宪法 14 条)。
	nowFn func() time.Time
}

func (s *Service) now() time.Time {
	if s.nowFn == nil {
		return time.Now()
	}
	return s.nowFn()
}

// WithClock 注入时钟,只供测试用。
func (s *Service) WithClock(now func() time.Time) *Service {
	s.nowFn = now
	return s
}

// NewService 构造查询入口。
//
// clients 为空会在构造期被拒绝,而不是等到有人打开页面才发现:一个
// 「一个平台都没接」的进程能正常启动、却在第一次点用户页签时才报错,
// 是最坏的失败时机。
func NewService(clients map[string]Client) (*Service, error) {
	if len(clients) == 0 {
		return nil, errors.New("platformusers: 没有配置任何平台的用户客户端")
	}
	cp := make(map[string]Client, len(clients))
	details := make(map[string]platformusers.UserDetailReader)
	daily := make(map[string]platformusers.DailyUsageReader)
	keys := make(map[string]platformusers.KeyMetadataReader)
	for source, c := range clients {
		known, err := platformusers.ParseSource(source)
		if err != nil {
			return nil, fmt.Errorf("platformusers: %w", err)
		}
		if c == nil {
			return nil, fmt.Errorf("platformusers: 平台 %q 的客户端为空", source)
		}
		cp[known] = c
		if reader, ok := c.(platformusers.UserDetailReader); ok {
			details[known] = reader
		}
		if reader, ok := c.(platformusers.DailyUsageReader); ok {
			daily[known] = reader
		}
		if reader, ok := c.(platformusers.KeyMetadataReader); ok {
			keys[known] = reader
		}
	}
	return &Service{clients: cp, detailReaders: details, dailyReaders: daily, keyReaders: keys}, nil
}

// SupportsPlatform 判断某个平台有没有终端用户清单。
//
// 前端据此决定「用户管理」页签对哪些平台显示——给 CPA 挂一个永远空的用户
// 页签,等于告诉运营「这个平台没有用户」,而事实是 CPA 的「用户」是代理商,
// 语义不同,我们压根没接(原型自己也留了这个提问)。
func SupportsPlatform(platform string) bool {
	_, err := platformusers.ParseSource(platform)
	return err == nil
}

// ListInput 是列表查询的入参。
type ListInput struct {
	Platform string
	Query    string
	Status   string
	Sort     string
	// Day 是统计区间的锚点业务日(YYYY-MM-DD)。空串 = 今天。
	Day string
	// Granularity 是统计粒度(day / week / month)。空串 = day。
	Granularity string
	Limit       int
	Cursor      string
}

// List 读取一页终端用户。
//
// 权限(ScopeRead)由路由上的 RequireScope 判定,不在此处重复——
// 权限声明集中在路由表上,散在 Service 里的第二套判定谁也审计不了。
func (s *Service) List(ctx context.Context, in ListInput) (platformusers.UserPage, error) {
	source, client, err := s.resolve(in.Platform)
	if err != nil {
		return platformusers.UserPage{}, err
	}

	sort, err := platformusers.ParseSortKey(in.Sort)
	if err != nil {
		// 拼错的排序键当场 400,不静默回落默认值:回落之后人会以为自己排过了
		return platformusers.UserPage{}, action.NewError(action.CodeInvalidParams,
			"排序键不合法", err)
	}
	status, err := parseStatusFilter(in.Status)
	if err != nil {
		return platformusers.UserPage{}, err
	}
	// 区间在这里就校验一次,而不是等连接器报 bad_response 再翻译:
	// 拼错的粒度是**调用方**的参数错误,消息里要能说出错在哪一项
	// (「未知统计粒度 "weekly"」),而经过 translateError 之后只剩
	// 一句笼统的「查询条件不合法」。
	period, err := platformusers.Period{
		Day:         in.Day,
		Granularity: platformusers.Granularity(in.Granularity),
	}.Normalize(s.now(), nil)
	if err != nil {
		return platformusers.UserPage{}, action.NewError(action.CodeInvalidParams,
			"统计区间不合法", err)
	}

	page, err := client.ListUsers(ctx, platformusers.ListFilter{
		Source: source,
		Query:  in.Query,
		Status: status,
		Sort:   sort,
		Period: period,
		Limit:  in.Limit,
		Cursor: in.Cursor,
	})
	if err != nil {
		return platformusers.UserPage{}, translateError("users.list", err)
	}
	return page, nil
}

type DetailInput struct {
	Platform    string
	UserID      string
	Day         string
	Granularity string
}

// Get 读取一个平台用户的精确详情。它不扫描列表，也不跨用户名/邮箱/令牌前缀关联。
func (s *Service) Get(ctx context.Context, in DetailInput) (platformusers.UserDetail, error) {
	source, _, err := s.resolve(in.Platform)
	if err != nil {
		return platformusers.UserDetail{}, err
	}
	reader, ok := s.detailReaders[source]
	if !ok {
		// 客户端存在但没有 detail capability：这是未接入，不是“没有这个用户”。
		return platformusers.UserDetail{}, action.NewError(
			action.CodeAdvancedControlsRequired, "用户详情真实读取尚未接入", nil)
	}
	ref := platformusers.UserRef{Platform: source, ID: in.UserID}
	if err := ref.Validate(); err != nil {
		return platformusers.UserDetail{}, action.NewError(action.CodeInvalidParams, "用户引用不合法", err)
	}
	period, err := platformusers.Period{Day: in.Day, Granularity: platformusers.Granularity(in.Granularity)}.Normalize(s.now(), nil)
	if err != nil {
		return platformusers.UserDetail{}, action.NewError(action.CodeInvalidParams, "统计区间不合法", err)
	}
	detail, err := reader.GetUser(ctx, platformusers.GetUserQuery{Ref: ref, Day: period.Day, Granularity: period.Granularity})
	if err != nil {
		return platformusers.UserDetail{}, translateError("users.detail", err)
	}
	return detail, nil
}

type DailyUsageInput struct {
	Platform string
	UserID   string
	Day      string
	Days     int
}

func (s *Service) DailyUsage(ctx context.Context, in DailyUsageInput) (platformusers.DailyUsageSeries, error) {
	source, _, err := s.resolve(in.Platform)
	if err != nil {
		return platformusers.DailyUsageSeries{}, err
	}
	reader, ok := s.dailyReaders[source]
	if !ok {
		return platformusers.DailyUsageSeries{}, action.NewError(action.CodeAdvancedControlsRequired, "每日消费趋势真实读取尚未接入", nil)
	}
	ref := platformusers.UserRef{Platform: source, ID: in.UserID}
	if err := ref.Validate(); err != nil {
		return platformusers.DailyUsageSeries{}, action.NewError(action.CodeInvalidParams, "用户引用不合法", err)
	}
	series, err := reader.DailyUsage(ctx, platformusers.DailyUsageQuery{Ref: ref, Day: in.Day, Days: in.Days})
	if err != nil {
		return platformusers.DailyUsageSeries{}, translateError("users.daily_usage", err)
	}
	return series, nil
}

type KeyMetadataInput struct {
	Platform string
	UserID   string
	Limit    int
	Cursor   string
}

func (s *Service) KeyMetadata(ctx context.Context, in KeyMetadataInput) (platformusers.KeyMetadataPage, error) {
	source, _, err := s.resolve(in.Platform)
	if err != nil {
		return platformusers.KeyMetadataPage{}, err
	}
	reader, ok := s.keyReaders[source]
	if !ok {
		return platformusers.KeyMetadataPage{}, action.NewError(action.CodeAdvancedControlsRequired, "API Key 元数据真实读取尚未接入", nil)
	}
	ref := platformusers.UserRef{Platform: source, ID: in.UserID}
	if err := ref.Validate(); err != nil {
		return platformusers.KeyMetadataPage{}, action.NewError(action.CodeInvalidParams, "用户引用不合法", err)
	}
	page, err := reader.ListKeyMetadata(ctx, platformusers.KeyMetadataQuery{Ref: ref, Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return platformusers.KeyMetadataPage{}, translateError("users.keys", err)
	}
	return page, nil
}

// parseStatusFilter 把查询参数翻译成契约里的状态。
//
// 空串是「不筛」。**拼错的状态当场 400**,不静默忽略:一个 `status=Active`
// (大小写错)如果被悄悄当成「不筛」,人会以为自己筛过了,然后照着一份
// 没筛过的清单做判断。
func parseStatusFilter(raw string) (platformusers.UserStatus, error) {
	if raw == "" {
		return "", nil
	}
	switch platformusers.UserStatus(raw) {
	case platformusers.StatusActive, platformusers.StatusLimited,
		platformusers.StatusDisabled, platformusers.StatusUnknown:
		return platformusers.UserStatus(raw), nil
	}
	return "", action.NewError(action.CodeInvalidParams,
		fmt.Sprintf("状态 %q 不合法(可选 active / limited / disabled / unknown)", raw), nil)
}

// resolve 把路由里的 {platform} 翻译成来源与客户端。
//
// 不认识的平台归 NOT_REGISTERED(404)而不是 INVALID_PARAMS(400):
// 交接文档 §8 要求「未知对象显示 Not Found」。对一个**存在但没有终端用户**的
// 平台(CPA、服务器)同样是 404——从这个端点的角度,那个平台的用户资源
// 确实不存在。
func (s *Service) resolve(platform string) (string, Client, error) {
	source, err := platformusers.ParseSource(platform)
	if err != nil {
		return "", nil, action.NewError(action.CodeNotRegistered,
			fmt.Sprintf("平台 %q 没有终端用户清单:只有 Sub2API 与 NewAPI 有", platform), err)
	}
	client, ok := s.clients[source]
	if !ok {
		// 平台认得,但这个进程没配它的客户端。这是**配置**问题,不是地址问题,
		// 所以说的是「没接」而不是「没有这个平台」
		return "", nil, action.NewError(action.CodeNotRegistered,
			fmt.Sprintf("平台 %q 的用户清单尚未接入本环境", platform), nil)
	}
	return source, client, nil
}

// translateError 把连接器错误翻译成 Action 错误码。
//
// 翻译表只该有一份:散在两个 Handler 里的 switch 迟早会漂开,
// 于是同一种上游故障在两个页面上显示成两回事。
//
// **上游错误的原始文本一个字都不进 Message**:Message 是要回给前端的
// (规格 §18.4),而这条通道上游的错误正文里可能带着用户邮箱与令牌。
func translateError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, platformusers.ErrLookupIncomplete) {
		return action.NewError(action.CodeExecutionFailed, "用户精确查找未完成，请重试", err)
	}
	if errors.Is(err, platformusers.ErrNotFound) {
		return action.NewError(action.CodeNotRegistered, "没有这条用户记录", err)
	}
	switch connector.KindOf(err) {
	case connector.KindBadResponse:
		// 走到这里的 bad_response 多半是**参数**问题(坏游标、未知来源),
		// 归 400 让调用方去改请求,而不是让他以为平台坏了
		return action.NewError(action.CodeInvalidParams, "查询条件不合法", err)
	case connector.KindAuth:
		return action.NewError(action.CodeExecutionFailed,
			"上游拒绝了平台的只读凭据", err)
	case connector.KindRateLimited:
		return action.NewError(action.CodeExecutionFailed,
			"上游限流,请稍后重试", err)
	case connector.KindNotSupported:
		// 501 而不是 502:这不是上游坏了,是平台这条链路还没接通——
		// 前端据此显示「功能待上线」而不是「上游故障」
		return action.NewError(action.CodeAdvancedControlsRequired,
			"用户清单尚未接通真实数据源(XM-0046:上游响应形状待核对)", err)
	case connector.KindForbiddenTarget, connector.KindWriteAttempt:
		// 护栏拒绝 = 配置错了或代码里出现了写路径,都是平台自己的问题
		return action.NewError(action.CodeInternal, "服务内部错误", err)
	default:
		return action.NewError(action.CodeExecutionFailed,
			"上游暂时不可用", fmt.Errorf("%s: %w", op, err))
	}
}
