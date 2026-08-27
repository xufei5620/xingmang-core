package invoice

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// FakeOptions 控制 Fake 的行为，用于覆盖规格 §19.4 的 Connector 测试矩阵。
//
// 选项集合刻意与 sub2api.FakeOptions 一一对应：两个 Connector 的测试矩阵
// 是同一套（版本/健康/错误分类/取消/部分数据/能力子集），套件写法能互相
// 参照，评审时也不用重新理解一遍「这个 Fake 又是怎么造故障的」。
type FakeOptions struct {
	// Version 是上游版本；空则用默认值。
	Version string
	// UnsupportedVersion 让 Version() 返回 Supported=false。
	UnsupportedVersion bool
	// Unhealthy 让 Health() 返回不健康。
	Unhealthy bool
	// FailWith 非空时，所有数据读取方法返回该分类的错误。
	FailWith connector.ErrorKind
	// Partial 让返回的数据标记为部分数据。
	Partial bool
	// ObservedAge 是观测时刻距今的时长（用于制造陈旧数据）。
	ObservedAge time.Duration
	// Latency 是每次调用的模拟延迟（用于验证上下文取消）。
	Latency time.Duration
	// MissingCapabilities 从能力清单中剔除这些项（模拟旧版本上游）。
	MissingCapabilities []registry.Capability
	// Now 可注入固定时钟，默认 time.Now。
	Now func() time.Time
}

// 操作名（进 connector.Error 的 Op，与能力清单对齐，便于按能力排查）。
const (
	opVersion      = "invoice.service.version_read"
	opHealth       = "invoice.health.read"
	opCapabilities = "invoice.capabilities"
	opListRequests = "invoice.requests.read"
	opDailySummary = "invoice.summary.daily_read"
)

// FakeRecordCount 是 Fake 固定数据集的条数。
//
// 25 条是刻意选的：比任何一个测试用的 Limit 都大好几倍，能翻出至少三页，
// 又小到可以把每条的状态与金额在脑子里算清楚——分页测试断言的是「不重不漏」，
// 造一份自己都算不明白的数据只会让断言失去意义。
const FakeRecordCount = 25

// fakeSourceTypes 是来源类型的取值样例。
//
// 平台不解释 SourceType 的语义（见 InvoiceRequest.SourceType），这里只是给
// 上层开发一组长得像真数据的值，用来验证分组渲染；不构成对上游取值集合的断言。
var fakeSourceTypes = []string{"consumption", "recharge", "manual"}

// fakeCursorPrefix 是 Fake 自己的游标编码前缀。
//
// 编码是**实现细节**：契约只保证游标可原样回传（见 RequestPage.NextCursor）。
// 这里用「最后一条记录的 ID」而不是偏移量，是为了让 Fake 与真实实现遵循同一种
// keyset 语义——如果 Fake 用 offset 而真实实现用 keyset，上层在 Fake 上验证过的
// 翻页逻辑到真环境会出现漏记录，而契约测试还是绿的。
const fakeCursorPrefix = "after:"

type fakeClient struct {
	opts FakeOptions
}

// NewFake 创建一个满足 ReadClient 契约的假实现。
//
// 它存在的意义有两层：让 XM-0029（真实客户端 + 看板卡片）之前的上层开发
// 不被开票系统的端点与凭据阻塞——CR-0002 里 /readonly/v1/ 还没建、bearer
// 还没接线，但看板卡片现在就能照着契约写；以及作为 contracttest 套件的
// 第一个被测实现——套件本身要先被验证有效，否则 XM-0029 拿它当合规判据
// 就没有意义。
func NewFake(opts FakeOptions) ReadClient {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "0.1.0"
	}
	return &fakeClient{opts: opts}
}

func (f *fakeClient) now() time.Time { return f.opts.Now().UTC() }

func (f *fakeClient) observedAt() time.Time {
	return f.now().Add(-f.opts.ObservedAge)
}

func (f *fakeClient) snapshot() Snapshot {
	return Snapshot{
		ObservedAt: f.observedAt(),
		Watermark:  fmt.Sprintf("wm-%d", f.observedAt().Unix()),
		IsPartial:  f.opts.Partial,
	}
}

// wait 模拟延迟，同时尊重上下文取消——契约要求取消时立即返回。
func (f *fakeClient) wait(ctx context.Context) error {
	if f.opts.Latency <= 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(f.opts.Latency):
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeClient) fail(op string) error {
	if f.opts.FailWith == "" {
		return nil
	}
	return connector.NewError(f.opts.FailWith, op, nil)
}

func (f *fakeClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	if err := f.wait(ctx); err != nil {
		return connector.VersionInfo{}, connector.NewError(connector.KindUnavailable, opVersion, err)
	}
	return connector.VersionInfo{
		Detected:    f.opts.Version,
		Fingerprint: "fake-" + f.opts.Version,
		// 不支持的版本返回 Supported=false 而非报错——由调用方决定是否 Fail Closed
		Supported:  !f.opts.UnsupportedVersion,
		DetectedAt: f.now(),
	}, nil
}

func (f *fakeClient) Health(ctx context.Context) (connector.HealthResult, error) {
	if err := f.wait(ctx); err != nil {
		return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, opHealth, err)
	}
	if f.opts.Unhealthy {
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: f.now(),
			LatencyMS: f.opts.Latency.Milliseconds(),
			ErrorKind: connector.KindUnavailable,
			// 安全的简短说明，不含上游原始错误文本
			Detail: "upstream health endpoint returned non-2xx",
		}, nil
	}
	return connector.HealthResult{
		Healthy:   true,
		CheckedAt: f.now(),
		LatencyMS: f.opts.Latency.Milliseconds(),
	}, nil
}

func (f *fakeClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	if err := f.wait(ctx); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, opCapabilities, err)
	}
	missing := make(map[registry.Capability]struct{}, len(f.opts.MissingCapabilities))
	for _, c := range f.opts.MissingCapabilities {
		missing[c] = struct{}{}
	}
	out := make([]registry.Capability, 0, len(ReadCapabilities))
	for _, c := range ReadCapabilities {
		if _, gone := missing[c]; gone {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// dataset 生成固定的 25 条开票申请，按提交时间倒序（最新在前）。
//
// 全部由下标推导，不写字面量表格：数据集要能在断言里被独立算出来
// （金额是等差、状态按 9 项枚举轮转），否则「Fake 返回了什么」和
// 「测试期望什么」就变成两份需要手工同步的清单。
func (f *fakeClient) dataset() []InvoiceRequest {
	snap := f.snapshot()
	statuses := AllStatuses()
	base := f.observedAt()

	out := make([]InvoiceRequest, 0, FakeRecordCount)
	for i := range FakeRecordCount {
		// 每条比前一条早一小时：既让排序稳定可断言，也让 From/To 过滤
		// 在固定时钟下有确定的命中条数。
		submitted := base.Add(-time.Duration(i+1) * time.Hour)
		out = append(out, InvoiceRequest{
			Snapshot:    snap,
			ID:          fmt.Sprintf("inv-req-%02d", i+1),
			RequestNo:   fmt.Sprintf("INV-2026-%04d", i+1),
			Status:      statuses[i%len(statuses)],
			AmountMinor: int64(i+1) * 10_000, // 100.00、200.00 …… 2500.00 元
			Currency:    CurrencyCNY,
			SourceType:  fakeSourceTypes[i%len(fakeSourceTypes)],
			SubmittedAt: submitted,
			UpdatedAt:   submitted.Add(30 * time.Minute),
		})
	}
	return out
}

func (f *fakeClient) ListRequests(ctx context.Context, q ListQuery) (RequestPage, error) {
	if err := f.wait(ctx); err != nil {
		return RequestPage{}, connector.NewError(connector.KindUnavailable, opListRequests, err)
	}
	// 校验先于故障注入：非法参数应当被拒绝，跟上游健不健康无关。
	// 分类沿用 sub2api 对非法业务日的处置（bad_response）——请求参数与响应
	// 都算「这次交互的数据形状不对」，两个 Connector 用同一个分类，调用方
	// 的错误处理才不用按 Connector 分叉。
	if err := q.Validate(); err != nil {
		return RequestPage{}, connector.NewError(connector.KindBadResponse, opListRequests, err)
	}
	if err := f.fail(opListRequests); err != nil {
		return RequestPage{}, err
	}

	items := f.dataset()

	// 过滤：时间范围是闭区间，状态是精确匹配。
	filtered := make([]InvoiceRequest, 0, len(items))
	for _, it := range items {
		if !q.From.IsZero() && it.SubmittedAt.Before(q.From.UTC()) {
			continue
		}
		if !q.To.IsZero() && it.SubmittedAt.After(q.To.UTC()) {
			continue
		}
		if q.Status != "" && string(it.Status) != q.Status {
			continue
		}
		filtered = append(filtered, it)
	}

	// 游标定位：找到上一页最后一条，从它的下一条开始。
	start := 0
	if q.Cursor != "" {
		after, ok := strings.CutPrefix(q.Cursor, fakeCursorPrefix)
		if !ok || after == "" {
			return RequestPage{}, connector.NewError(connector.KindBadResponse, opListRequests,
				fmt.Errorf("cursor %q 无法解析: %w", q.Cursor, ErrInvalidCursor))
		}
		idx := -1
		for i, it := range filtered {
			if it.ID == after {
				idx = i
				break
			}
		}
		if idx < 0 {
			// 游标指向的记录在当前过滤结果里不存在：多半是换了过滤条件却
			// 沿用了旧游标。报错而不是从头开始——从头开始会让调用方拿到
			// 一份看似「下一页」实则重复第一页的数据。
			return RequestPage{}, connector.NewError(connector.KindBadResponse, opListRequests,
				fmt.Errorf("cursor %q 已失效: %w", q.Cursor, ErrInvalidCursor))
		}
		start = idx + 1
	}

	end := min(start+int(q.EffectiveLimit()), len(filtered))
	page := filtered[start:end]

	next := ""
	if end < len(filtered) {
		next = fakeCursorPrefix + page[len(page)-1].ID
	}
	return RequestPage{
		Snapshot:   f.snapshot(),
		Items:      page,
		NextCursor: next,
	}, nil
}

func (f *fakeClient) DailySummary(ctx context.Context, day string) (DailySummary, error) {
	if err := f.wait(ctx); err != nil {
		return DailySummary{}, connector.NewError(connector.KindUnavailable, opDailySummary, err)
	}
	if err := ValidateBusinessDay(day); err != nil {
		return DailySummary{}, connector.NewError(connector.KindBadResponse, opDailySummary, err)
	}
	if err := f.fail(opDailySummary); err != nil {
		return DailySummary{}, err
	}

	// Fake 的数据集横跨 25 小时、不按日切分，所以汇总覆盖**全部**固定数据，
	// 与传入的 day 无关（day 只做格式校验并原样回填）。真实实现必须按
	// Asia/Shanghai 业务日切分——那是上游的 SQL 做的事，见 CR-0002 §3。
	//
	// 汇总由数据集**现算**而不是写死常量：失败/待处理的口径由开票系统冻结，
	// 现在这两个集合只是演示值，改起来只该动下面两个 map，
	// 不该再去同步一堆手算出来的期望值。
	sum := DailySummary{
		Snapshot: f.snapshot(),
		Day:      day,
		Currency: CurrencyCNY,
	}
	for _, it := range f.dataset() {
		sum.Count++
		sum.TotalAmountMinor += it.AmountMinor
		if _, isFailed := fakeFailedStatuses[it.Status]; isFailed {
			sum.FailedCount++
		}
		if _, isPending := fakePendingStatuses[it.Status]; isPending {
			sum.PendingCount++
		}
	}
	return sum, nil
}

// fakeFailedStatuses 是 Fake 为了让演示数据非零而挑的一组状态。
//
// ⚠️ **不是口径**：口径由开票系统冻结，平台不自行解释状态集合
// （见 DailySummary.FailedCount）。真实实现绝不该照抄这两个集合——
// 它该原样透传上游算好的数字。
var fakeFailedStatuses = map[Status]struct{}{
	StatusRejected:        {},
	StatusRefundAttention: {},
}

// fakePendingStatuses 同上，是演示用的一组状态，不是口径。
//
// ⚠️ 见 DailySummary.PendingCount。
var fakePendingStatuses = map[Status]struct{}{
	StatusPendingReview:          {},
	StatusNeedsChanges:           {},
	StatusManualIssuing:          {},
	StatusIssuedAwaitingDocument: {},
}
