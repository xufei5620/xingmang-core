// Package channelassurance 是「渠道保障」页签保障概览 / 历史记录两个子页签
// 的平台侧只读查询入口（XM-ASSURE0 第一片，只做被动指标，不含主动探测）。
//
// 数据源与 internal/platform/requestlog 相同——都是记录代理落盘的请求审计
// index.jsonl，只是聚合维度不同：requestlog 服务的是「逐条列表 + 正文」,
// 本包服务的是「选定窗口/近 7 天的状态分类、延迟百分位、按模型分组」。
// 两者刻意分成两个包而不是塞进同一个 Service：requestlog.Service 的核心纪律
// 是「正文读取必须留审计」，而本包读的自始至终只是元数据聚合（同
// request.read 一个泄漏面），硬塞进同一个类型会让「这个方法需不需要审计」
// 变成一个要靠记忆维护的隐性规则。
//
// **不做渠道级聚合**：磁盘记录格式本身不采集 channel/upstream 维度（见
// connectors/reqlog 的 ChannelBreakdownUnsupportedReason），本包忠实转述这个
// 限制，不在这一层悄悄拿 model 或别的字段冒充渠道。
package channelassurance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
)

// HistoryWindowDays 是「历史记录」子页签固定回看的天数。
//
// 钉死 7 天而不是做成参数：与 XM-PAY1 月累计端点的 `maxHistoryHours=168`
// 同一条纪律（见 web 侧 lib/metrics.ts 对该端点的注释）——不让这个 Query
// 被当成任意跨度的数据导出口。真要更长的历史，需要先有 rollup（本片没有,
// 见 XM-REQLOG-METRICS 交接文档 risks #1：这几条指标本就被排除在降采样
// 策略之外），而不是让这个端点扫更多天的原始索引。
const HistoryWindowDays = 7

// Reader 是本包需要的读取能力（*reqlog.MetricsReader 满足）。
//
// 声明成窄接口是为了让 Service 能在没有真实磁盘数据的机器上被测到——与
// requestlog.Client 抽出接口的理由相同。
type Reader interface {
	WindowAssurance(ctx context.Context, source string, preset reqlog.AssuranceWindowPreset, now time.Time) (reqlog.AssuranceResult, error)
	HistoryDays(ctx context.Context, source string, at time.Time, n int) ([]reqlog.AssuranceResult, error)
}

// Service 是渠道保障被动指标的只读查询入口。
type Service struct {
	reader Reader
	now    func() time.Time
}

// Option 调整 Service 的构造。
type Option func(*Service)

// WithClock 注入时钟（测试用；默认 time.Now）。
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService 构造查询入口。reader 为 nil 时构造期即拒绝——与
// requestlog.NewService 对 audit 的处理同一条纪律：一个「读取器没接上」的
// Service 能被构造出来，只会把这个错误推迟到第一次调用才发现。
func NewService(reader Reader, opts ...Option) (*Service, error) {
	if reader == nil {
		return nil, errors.New("channelassurance: reader 为空")
	}
	s := &Service{reader: reader, now: time.Now}
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

// Overview 读取某平台在指定窗口内的被动保障聚合（保障概览子页签）。
//
// 权限（requestlog.ScopeRead，即 request.read）由路由上的 RequireScope
// 判定，不在此处重复——与 requestlog.Service.List 同一条纪律：这批数据
// 是聚合而不是逐条，泄漏面等同于「这个平台今天调用多不多」这一类指标,
// 复用 request.read 而不是新开一个 scope（团队交接的明确要求：能复用就不
// 新增，新增了要额外走人员与权限的角色目录登记）。
func (s *Service) Overview(
	ctx context.Context, platform string, preset reqlog.AssuranceWindowPreset,
) (reqlog.AssuranceResult, error) {
	source, err := requestlog.ResolvePlatform(platform)
	if err != nil {
		return reqlog.AssuranceResult{}, err
	}
	result, err := s.reader.WindowAssurance(ctx, source, preset, s.now().UTC())
	if err != nil {
		return reqlog.AssuranceResult{}, translateError("assurance.overview", err)
	}
	return result, nil
}

// History 读取某平台最近 HistoryWindowDays 天的逐日被动保障聚合（历史记录
// 子页签）。没有可用的 rollup 时这是唯一的历史来源，因此显式钉死 7 天上限。
func (s *Service) History(ctx context.Context, platform string) ([]reqlog.AssuranceResult, error) {
	source, err := requestlog.ResolvePlatform(platform)
	if err != nil {
		return nil, err
	}
	days, err := s.reader.HistoryDays(ctx, source, s.now().UTC(), HistoryWindowDays)
	if err != nil {
		return nil, translateError("assurance.history", err)
	}
	return days, nil
}

// translateError 把读取器的错误翻译成 Action 错误码。
//
// WindowAssurance/HistoryDays 只在两种情况下返回非 nil：调用方的 context
// 被取消（客户端断开、请求超时），或本包自己传入了非法参数（预设/天数,
// 两者在到达这里之前已经过校验，正常路径不会触发）——不像
// requestlog.translateError 要处理一整张上游连接器错误分类表，这里不存在
// 「上游服务拒绝」「限流」这类场景（本读取器不发任何网络请求，只读本地
// 挂载的文件），因此不需要那张完整的映射表，但同样遵守「上游/内部错误的
// 原始文本不进 Message」——这里虽然没有 PII 泄漏风险（没有第三方响应体经过
// 这条路径），保持同一条纪律是为了不需要记住「这个 Query 是例外」。
func translateError(op string, err error) error {
	if err == nil {
		return nil
	}
	return action.NewError(action.CodeExecutionFailed,
		"请求审计聚合读取失败，请重试", fmt.Errorf("%s: %w", op, err))
}
