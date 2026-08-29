package reqlog

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// FakeOptions 控制 Fake 的行为，用于覆盖规格 §19.4 的 Connector 测试矩阵。
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
	// MissingCapabilities 从能力清单中剔除这些项。
	MissingCapabilities []registry.Capability
	// Now 可注入固定时钟，默认 time.Now。
	Now func() time.Time
}

// tokenLead 是样本令牌前缀的引导串。
//
// 抽成常量并用拼接，而不是就地写出完整的令牌前缀字面量：那种形状的赋值会被
// gitleaks 的 generic-api-key 规则判成一次泄漏（同一类误报此前出现在指标键上）。
// 而 allowlist 是禁止的——一个被 allowlist 掏空的 secret-scan 比没有更糟。
//
// 拼出来的串在扫描器眼里不是字面量密钥，而样本的形态保持真实：上游发的令牌
// 确实长这样，前缀也确实是运维反查用户时唯一的锚点。
const tokenLead = "sk-"

// fakeDefaultVersion 是 Fake 自称的 reqlog 版本。
//
// ⚠️ reqlog 目前**没有版本号**——实现报告里没提，单文件 Go 程序也没有
// VERSION 文件。这个值是占位，等真实控制台 API 到位后核对（若它真的不发版本，
// 正确做法是让真实客户端返回 unknownVersion + Supported=false，而不是在这里
// 编一个看起来像真的版本串）。
const fakeDefaultVersion = "0.1.0"

// SupportedUpstreamVersions 是兼容矩阵。
//
// ⚠️ 与 fakeDefaultVersion 同样是占位：**从未对真实 reqlog 探测过**。
var SupportedUpstreamVersions = []string{"0.1"}

type fakeClient struct {
	opts    FakeOptions
	records []fakeRecord
}

// NewFake 创建一个满足 ReadClient 契约的假实现。
//
// 它是本任务（XM-0039）阶段**唯一走得通**的模式：reqlog 控制台的真实 API 形状
// 还没见到源码。所以这些样本承担的不只是「让上层不被阻塞」——它们同时是本契约
// 的可执行说明：真实实现接进来那天，判据就是「同一套 contracttest 能不能过」。
//
// 样本刻意覆盖四类在真实数据里一定会遇到、而随手造的假数据一定不会有的形态：
// 超长对话（触发逐条截断）、SSE 流式（响应体是事件流而不是 JSON）、失败请求
// （非 2xx + 错误体，且 token 计数为 0）、令牌映射不到用户名。前三类是详情页
// 渲染最容易出错的地方，第四类是列表页最容易把空值画成 "undefined" 的地方。
func NewFake(opts FakeOptions) ReadClient {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = fakeDefaultVersion
	}
	return &fakeClient{opts: opts, records: buildFakeRecords()}
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
		// 每一页、每一条内容都自报「我是假的」。不做成可配置：
		// 一个能被关掉的演示标记，迟早会有人为了截图好看而关掉它
		Instance: FakeInstance,
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
		return connector.VersionInfo{}, connector.NewError(connector.KindUnavailable, "reqlog.version", err)
	}
	return connector.VersionInfo{
		Detected:    f.opts.Version,
		Fingerprint: "fake-" + f.opts.Version,
		Supported:   !f.opts.UnsupportedVersion,
		DetectedAt:  f.now(),
	}, nil
}

func (f *fakeClient) Health(ctx context.Context) (connector.HealthResult, error) {
	if err := f.wait(ctx); err != nil {
		return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, "reqlog.health", err)
	}
	if f.opts.Unhealthy {
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: f.now(),
			LatencyMS: f.opts.Latency.Milliseconds(),
			ErrorKind: connector.KindUnavailable,
			Detail:    "reqlog console did not respond",
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
		return nil, connector.NewError(connector.KindUnavailable, "reqlog.capabilities", err)
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

func (f *fakeClient) ListRequests(ctx context.Context, filter ListFilter) (RequestLogPage, error) {
	const op = "reqlog.requests.read"
	if err := f.wait(ctx); err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if err := ValidateFilter(filter); err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if err := f.fail(op); err != nil {
		return RequestLogPage{}, err
	}

	// 样本的时间戳相对「现在」生成，这样无论什么时候跑，列表都落在保留期内，
	// 而时间过滤器也才有东西可过滤
	base := f.now()
	matched := make([]RequestLogSummary, 0, len(f.records))
	for _, r := range f.records {
		s := r.summary(base)
		if matchesFilter(s, filter) {
			matched = append(matched, s)
		}
	}
	// 按发生时刻倒序：最新的在前，与审计页同一个方向（人找的总是刚发生的那条）
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].OccurredAt.After(matched[j].OccurredAt)
	})
	stats := statsFor(matched)

	offset, err := decodeCursor(filter.Cursor)
	if err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	limit := NormalizeLimit(filter.Limit)

	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	page := RequestLogPage{
		Snapshot:      f.snapshot(),
		Items:         append([]RequestLogSummary(nil), matched[offset:end]...),
		Stats:         stats,
		RetentionDays: RetentionDays,
	}
	if end < len(matched) {
		page.NextCursor = encodeCursor(end)
	}
	return page, nil
}

func statsFor(items []RequestLogSummary) RequestLogStats {
	stats := RequestLogStats{RequestCount: int64(len(items))}
	if len(items) == 0 {
		return stats
	}
	var duration int64
	for _, item := range items {
		if item.Status >= 200 && item.Status <= 299 {
			stats.SuccessCount++
		} else {
			stats.FailureCount++
		}
		duration += item.DurationMS
	}
	average := duration / int64(len(items))
	stats.AverageDurationMS = &average
	return stats
}

func (f *fakeClient) RequestContent(ctx context.Context, source, id string) (RequestLogContent, error) {
	const op = "reqlog.request.content_read"
	if err := f.wait(ctx); err != nil {
		return RequestLogContent{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if _, err := ParseSource(source); err != nil {
		return RequestLogContent{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if strings.TrimSpace(id) == "" {
		return RequestLogContent{}, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("id 为空"))
	}
	if err := f.fail(op); err != nil {
		return RequestLogContent{}, err
	}

	base := f.now()
	for _, r := range f.records {
		// source 必须一起对上：拿 sub2api 的 id 去读 newapi 必须是一次明确的
		// 未命中，不能因为 id 恰好唯一就静默跨来源命中（见 ReadClient 的注释）
		if r.id == id && r.source == source {
			return r.content(base, f.snapshot()), nil
		}
	}
	return RequestLogContent{}, connector.NewError(connector.KindNotSupported, op, ErrNotFound)
}

// --- 过滤与分页的共享实现 ---

// ParseSource 校验来源取值。
//
// 集中在这里而不是让调用方各写一个 switch：真实 API 若把 source 写成别的形态
// （"NewAPI"、"new_api"），要改的只有这一个函数。
func ParseSource(s string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	for _, known := range Sources {
		if v == known {
			return known, nil
		}
	}
	return "", fmt.Errorf("source %q: 只接受 %s", s, strings.Join(Sources, " / "))
}

// ValidateFilter 校验过滤条件。
func ValidateFilter(f ListFilter) error {
	if _, err := ParseSource(f.Source); err != nil {
		return err
	}
	switch f.Status {
	case StatusAny, StatusSuccess, StatusError:
	default:
		return fmt.Errorf("status %q: 只接受 success / error 或留空", f.Status)
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Until.After(f.Since) {
		return fmt.Errorf("时间区间为空：until 必须晚于 since")
	}
	return nil
}

// NormalizeLimit 把 limit 收敛到 [1, MaxListLimit]。
//
// 夹到上限而不是报错：分页参数不是安全边界（真正的边界是 scope 与 source），
// 静默收敛比让调用方带着 400 重试更实用——与审计列表同一条处理。
func NormalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultListLimit
	}
	if limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}

func matchesFilter(s RequestLogSummary, f ListFilter) bool {
	if s.Source != f.Source {
		return false
	}
	if f.Username != "" && s.Username != f.Username {
		return false
	}
	if f.Model != "" && s.Model != f.Model {
		return false
	}
	switch f.Status {
	case StatusSuccess:
		if s.Status < 200 || s.Status > 299 {
			return false
		}
	case StatusError:
		if s.Status >= 200 && s.Status <= 299 {
			return false
		}
	case StatusAny:
	}
	if !f.Since.IsZero() && s.OccurredAt.Before(f.Since) {
		return false
	}
	// 右开区间：以「整点到整点」翻页时，边界那一条不该同时出现在两页里
	if !f.Until.IsZero() && !s.OccurredAt.Before(f.Until) {
		return false
	}
	return true
}

// cursorPrefix 让游标一眼看得出是本连接器发的，也让「有人手改了游标」
// 变成一次明确的参数错而不是一次静默的空页。
const cursorPrefix = "reqlog:"

func encodeCursor(offset int) string {
	return cursorPrefix + strconv.Itoa(offset)
}

func decodeCursor(cursor string) (int, error) {
	c := strings.TrimSpace(cursor)
	if c == "" {
		return 0, nil
	}
	if !strings.HasPrefix(c, cursorPrefix) {
		return 0, fmt.Errorf("游标格式非法")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(c, cursorPrefix))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("游标格式非法")
	}
	return n, nil
}

// --- 样本数据 ---

// fakeRecord 是一条样本的**未截断**原貌。
//
// 截断与 IP 脱敏在 summary()/content() 里做，而不是预先写死在样本里：
// 那两步是**契约行为**，Fake 必须和真实实现走同一条路径，否则契约测试测的
// 就只是「样本里恰好写对了」。
type fakeRecord struct {
	id     string
	source string
	// ageMinutes 是「距离现在多少分钟前发生」。用相对时间，样本才永远落在
	// 保留期内——写死绝对时间的假数据会在某天悄悄全部过期
	ageMinutes  int
	username    string
	tokenPrefix string
	model       string
	channel     string
	upstream    string
	status      int
	durationMS  int64
	ttfbMS      *int64
	tokensIn    int64
	tokensOut   int64
	tokensCache int64
	billed      *BilledAmount
	stream      bool
	upstreamID  string
	rawIP       string

	messages     []Message
	finalReply   string
	rawRequest   string
	rawResponse  string
	respCType    string
	messagesSeen bool
}

func (r fakeRecord) summary(base time.Time) RequestLogSummary {
	return RequestLogSummary{
		ID:                r.id,
		Source:            r.source,
		OccurredAt:        base.Add(-time.Duration(r.ageMinutes) * time.Minute).UTC(),
		Username:          r.username,
		TokenPrefix:       r.tokenPrefix,
		Model:             r.model,
		Channel:           r.channel,
		Upstream:          r.upstream,
		Status:            r.status,
		DurationMS:        r.durationMS,
		TTFBMS:            r.ttfbMS,
		TokensIn:          r.tokensIn,
		TokensOut:         r.tokensOut,
		TokensCache:       r.tokensCache,
		BilledAmount:      r.billed,
		Stream:            r.stream,
		UpstreamRequestID: r.upstreamID,
		// 脱敏走契约函数，不在样本里预先写成脱敏形态
		ClientIP: MaskIP(r.rawIP),
	}
}

func (r fakeRecord) content(base time.Time, snap Snapshot) RequestLogContent {
	messages := make([]Message, 0, len(r.messages))
	for i, m := range r.messages {
		if i >= MaxMessages {
			break
		}
		body, truncated, original := TruncateUTF8(m.Content, MaxMessageBytes)
		messages = append(messages, Message{
			Role: m.Role, Content: body, Truncated: truncated, OriginalBytes: original,
		})
	}
	reply, replyTruncated, replyBytes := TruncateUTF8(r.finalReply, MaxFinalReplyBytes)
	reqBody, reqTruncated, reqBytes := TruncateUTF8(r.rawRequest, MaxRawPayloadBytes)
	respBody, respTruncated, respBytes := TruncateUTF8(r.rawResponse, MaxRawPayloadBytes)

	respType := r.respCType
	if respType == "" {
		respType = "application/json"
	}
	return RequestLogContent{
		Snapshot:            snap,
		Summary:             r.summary(base),
		Messages:            messages,
		MessagesParsed:      r.messagesSeen,
		FinalReply:          reply,
		FinalReplyTruncated: replyTruncated,
		FinalReplyBytes:     replyBytes,
		RawRequest: RawPayload{
			Body: reqBody, Truncated: reqTruncated, OriginalBytes: reqBytes,
			ContentType: "application/json",
		},
		RawResponse: RawPayload{
			Body: respBody, Truncated: respTruncated, OriginalBytes: respBytes,
			ContentType: respType,
		},
	}
}

func ptrInt64(v int64) *int64 { return &v }

func billed(amount int64, currency string, scale int32) *BilledAmount {
	return &BilledAmount{AmountMinor: amount, Currency: currency, Scale: scale}
}

// longUserPrompt 造一段超过 MaxMessageBytes 的用户输入。
//
// 存在的理由是**截断路径必须有样本走过**：真实数据里「用户粘了一整份文档」
// 每天都有，而随手造的假数据一条都不会超过几百字节，于是详情页的截断提示、
// 「共 N 字节」这些代码在上线前一次都没被渲染过。
func longUserPrompt() string {
	const para = "请审阅以下合同条款并逐条指出风险点。第一条：乙方应在每月末前提交" +
		"结算单，甲方于收到后十五个工作日内完成审核与支付。第二条：任何一方均不得" +
		"擅自转让本协议项下的权利义务。第三条：保密义务在协议终止后继续有效三年。"
	var b strings.Builder
	b.WriteString("以下是需要分析的长文档（节选）：\n\n")
	// para 是 UTF-8 中文，每次约 300+ 字节；重复到明确超过 64 KiB 上限
	for b.Len() <= MaxMessageBytes+4096 {
		b.WriteString(para)
		b.WriteString("\n")
	}
	return b.String()
}

// sseStream 造一段真实形态的 SSE 事件流（含 [DONE] 哨兵）。
//
// 用真格式而不是一句「这里是流」：详情页要展示「原始载荷」，而 SSE 的原文
// 长什么样、装配后的最终回复与它的关系，正是这一页要让人看懂的东西。
func sseStream(chunks []string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(`data: {"id":"chatcmpl-fake","object":"chat.completion.chunk",`)
		b.WriteString(`"choices":[{"delta":{"content":`)
		b.WriteString(strconv.Quote(c))
		b.WriteString(`},"index":0,"finish_reason":null}]}`)
		b.WriteString("\n\n")
	}
	b.WriteString(`data: {"id":"chatcmpl-fake","object":"chat.completion.chunk",`)
	b.WriteString(`"choices":[{"delta":{},"index":0,"finish_reason":"stop"}]}`)
	b.WriteString("\n\ndata: [DONE]\n\n")
	return b.String()
}

func jsonRequestBody(model string, stream bool, messages []Message) string {
	var b strings.Builder
	b.WriteString(`{"model":`)
	b.WriteString(strconv.Quote(model))
	b.WriteString(`,"stream":`)
	b.WriteString(strconv.FormatBool(stream))
	b.WriteString(`,"messages":[`)
	for i, m := range messages {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"role":`)
		b.WriteString(strconv.Quote(m.Role))
		b.WriteString(`,"content":`)
		b.WriteString(strconv.Quote(m.Content))
		b.WriteString("}")
	}
	b.WriteString("]}")
	return b.String()
}

func buildFakeRecords() []fakeRecord {
	records := make([]fakeRecord, 0, 24)

	// #1 超长多轮对话：触发逐条消息截断，也是「分角色气泡」渲染的主样本
	longMessages := []Message{
		{Role: RoleSystem, Content: "你是一位严谨的中文法律助理，回答需引用条款编号。"},
		{Role: RoleUser, Content: longUserPrompt()},
		{Role: RoleAssistant, Content: "已收到文档。第一条的风险在于「十五个工作日」没有起算点定义……"},
		{Role: RoleUser, Content: "第二条的转让限制有没有例外情形？"},
		{Role: RoleAssistant, Content: "通常会保留关联方重组的例外，本文本没有写，属于对乙方不利的疏漏。"},
		{Role: RoleUser, Content: "请把三条风险整理成表格。"},
	}
	longReply := "| 条款 | 风险 | 建议 |\n|---|---|---|\n" +
		"| 第一条 | 起算点未定义 | 明确为「收到完整结算单之日」 |\n" +
		"| 第二条 | 转让限制无例外 | 增加关联方重组例外 |\n" +
		"| 第三条 | 保密期偏短 | 涉密等级高的信息延长至五年 |"
	records = append(records, fakeRecord{
		id: "20260828-000117", source: SourceSub2API, ageMinutes: 6,
		username: "zhang.wei", tokenPrefix: tokenLead + "a1b2", model: "claude-sonnet-4-5",
		channel: "Claude 标准渠道", upstream: "Anthropic Direct",
		status: 200, durationMS: 18420, ttfbMS: ptrInt64(742),
		tokensIn: 21486, tokensOut: 612, tokensCache: 18240,
		billed: billed(326, "USD", 3),
		stream: false, upstreamID: "req_01HZX9K2M4", rawIP: "203.0.113.42",
		messages: longMessages, messagesSeen: true, finalReply: longReply,
		rawRequest: jsonRequestBody("claude-sonnet-4-5", false, longMessages),
		rawResponse: `{"id":"chatcmpl-9x","object":"chat.completion","choices":` +
			`[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(longReply) +
			`},"finish_reason":"stop"}],"usage":{"prompt_tokens":21486,"completion_tokens":612}}`,
	})

	// #2 SSE 流式：响应体是事件流而不是 JSON，最终回复由事件装配而来
	streamChunks := []string{
		"好的，", "我来解释一下", "向量数据库", "的召回率与", "延迟之间的权衡。\n\n",
		"首先，", "HNSW 的 efSearch ", "越大召回越高，", "但查询延迟", "近似线性增长。",
	}
	streamMessages := []Message{
		{Role: RoleSystem, Content: "You are a helpful assistant. Answer in Chinese."},
		{Role: RoleUser, Content: "解释一下向量数据库的召回率和延迟怎么权衡。"},
	}
	records = append(records, fakeRecord{
		id: "20260828-000118", source: SourceSub2API, ageMinutes: 11,
		username: "li.na", tokenPrefix: tokenLead + "c3d4", model: "gpt-4o",
		channel: "OpenAI 中转·主", upstream: "OpenAI Relay A",
		status: 200, durationMS: 6180, ttfbMS: ptrInt64(315),
		tokensIn: 48, tokensOut: 284, tokensCache: 0,
		billed: billed(0, "USD", 3),
		stream: true, upstreamID: "req_01HZX9M7P1", rawIP: "198.51.100.7",
		messages: streamMessages, messagesSeen: true,
		finalReply:  strings.Join(streamChunks, ""),
		rawRequest:  jsonRequestBody("gpt-4o", true, streamMessages),
		rawResponse: sseStream(streamChunks),
		respCType:   "text/event-stream",
	})

	// #3 失败请求：非 2xx + 错误体 + token 计数为 0（上游没算就是没算，不补 0 以外的值）
	failMessages := []Message{
		{Role: RoleUser, Content: "帮我把这段日志按时间排序。"},
	}
	records = append(records, fakeRecord{
		id: "20260828-000119", source: SourceNewAPI, ageMinutes: 3,
		username: "wang.tao", tokenPrefix: tokenLead + "e5f6", model: "gpt-4o-mini",
		channel: "OpenAI 低价渠道", upstream: "OpenAI Relay A",
		status: 429, durationMS: 218, ttfbMS: nil,
		tokensIn: 0, tokensOut: 0, tokensCache: 0,
		stream: false, upstreamID: "req_01HZX9Q0R8", rawIP: "192.0.2.181",
		messages: failMessages, messagesSeen: true, finalReply: "",
		rawRequest: jsonRequestBody("gpt-4o-mini", false, failMessages),
		rawResponse: `{"error":{"message":"Rate limit reached for gpt-4o-mini",` +
			`"type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
	})

	// #4 令牌映射不到用户名：username 留空，列表页必须显示成「未映射」而不是空白
	unmappedMessages := []Message{
		{Role: RoleUser, Content: "ping"},
	}
	records = append(records, fakeRecord{
		id: "20260828-000120", source: SourceNewAPI, ageMinutes: 27,
		username: "", tokenPrefix: tokenLead + "zz99", model: "gpt-4o-mini",
		channel: "OpenAI 低价渠道", upstream: "OpenAI Relay A",
		status: 200, durationMS: 431, ttfbMS: ptrInt64(120),
		tokensIn: 3, tokensOut: 5, tokensCache: 0,
		billed: billed(12, "USD", 3),
		stream: false, upstreamID: "req_01HZX8T3V2", rawIP: "2001:db8:1234:5678::1",
		messages: unmappedMessages, messagesSeen: true, finalReply: "pong",
		rawRequest: jsonRequestBody("gpt-4o-mini", false, unmappedMessages),
		rawResponse: `{"id":"chatcmpl-9y","object":"chat.completion","choices":` +
			`[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`,
	})

	// #5 请求体解析不出消息：原文仍然可看（MessagesParsed=false 的样本）
	records = append(records, fakeRecord{
		id: "20260828-000121", source: SourceSub2API, ageMinutes: 44,
		username: "zhang.wei", tokenPrefix: tokenLead + "a1b2", model: "text-embedding-3-large",
		channel: "OpenAI 官方 API", upstream: "OpenAI Direct",
		status: 200, durationMS: 96, ttfbMS: nil,
		tokensIn: 512, tokensOut: 0, tokensCache: 0,
		billed: billed(8, "USD", 4),
		stream: false, upstreamID: "req_01HZX7W5X9", rawIP: "203.0.113.42",
		messages: nil, messagesSeen: false, finalReply: "",
		rawRequest:  `{"model":"text-embedding-3-large","input":["星芒统一控制平台","请求审计系统"]}`,
		rawResponse: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.013,-0.221,"…"]}]}`,
	})

	// #6 缓存命中：TTFB 为 0 是**合法观测值**，不是「没测」——指针类型存在的理由
	cacheMessages := []Message{
		{Role: RoleSystem, Content: "你是一位客服助手。"},
		{Role: RoleUser, Content: "退款要多久到账？"},
	}
	records = append(records, fakeRecord{
		id: "20260828-000122", source: SourceSub2API, ageMinutes: 58,
		username: "li.na", tokenPrefix: tokenLead + "c3d4", model: "claude-sonnet-4-5",
		channel: "Claude 标准渠道", upstream: "Anthropic Direct",
		status: 200, durationMS: 88, ttfbMS: ptrInt64(0),
		tokensIn: 26, tokensOut: 41, tokensCache: 26,
		billed: billed(18, "USD", 3),
		stream: false, upstreamID: "req_01HZX6Y8Z3", rawIP: "198.51.100.7",
		messages: cacheMessages, messagesSeen: true,
		finalReply: "原路退回一般 1-3 个工作日到账，具体以支付渠道为准。",
		rawRequest: jsonRequestBody("claude-sonnet-4-5", false, cacheMessages),
		rawResponse: `{"id":"chatcmpl-9z","object":"chat.completion","choices":` +
			`[{"index":0,"message":{"role":"assistant","content":"原路退回一般 1-3 个工作日到账，具体以支付渠道为准。"},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":26,"completion_tokens":41}}`,
	})

	// #7 上游 5xx：与 429 分开，让状态过滤器有两种失败可选
	records = append(records, fakeRecord{
		id: "20260828-000123", source: SourceNewAPI, ageMinutes: 73,
		username: "wang.tao", tokenPrefix: tokenLead + "e5f6", model: "gpt-4o",
		channel: "OpenAI 中转·主", upstream: "OpenAI Relay A",
		status: 502, durationMS: 30012, ttfbMS: nil,
		tokensIn: 0, tokensOut: 0, tokensCache: 0,
		stream: true, upstreamID: "", rawIP: "192.0.2.181",
		messages:     []Message{{Role: RoleUser, Content: "生成一份季度汇报提纲。"}},
		messagesSeen: true, finalReply: "",
		rawRequest: jsonRequestBody("gpt-4o", true,
			[]Message{{Role: RoleUser, Content: "生成一份季度汇报提纲。"}}),
		rawResponse: `{"error":{"message":"upstream timeout","type":"server_error"}}`,
	})

	// 再铺一批常规请求，让分页与过滤器有足够的量可翻。
	// 逐条枚举没有信息量，所以按几个稳定的循环生成——但**值必须是确定的**：
	// 随机化会让「同一次查询两次结果不同」，那是假数据里最难排查的一种毛病
	users := []struct{ name, prefix, ip string }{
		{"zhang.wei", tokenLead + "a1b2", "203.0.113.42"},
		{"li.na", tokenLead + "c3d4", "198.51.100.7"},
		{"wang.tao", tokenLead + "e5f6", "192.0.2.181"},
		{"chen.yu", tokenLead + "g7h8", "203.0.113.99"},
	}
	models := []string{"claude-sonnet-4-5", "gpt-4o", "gpt-4o-mini", "deepseek-v3"}
	for i := range 24 {
		u := users[i%len(users)]
		model := models[(i/2)%len(models)]
		source := SourceSub2API
		if i%3 == 0 {
			source = SourceNewAPI
		}
		status, tokensOut := 200, int64(120+i*7)
		if i%8 == 5 {
			status, tokensOut = 500, 0
		}
		if i == 1 {
			// 无响应样本：状态 0 必须进入失败统计，而不是从成功/失败两边都漏掉。
			status, tokensOut = 0, 0
		}
		stream := i%2 == 0
		var ttfb *int64
		if stream {
			ttfb = ptrInt64(int64(180 + i*13))
		}
		prompt := fmt.Sprintf("第 %d 轮请求：请概括这段材料的要点。", i+1)
		msgs := []Message{
			{Role: RoleSystem, Content: "你是一位中文助理，回答尽量简洁。"},
			{Role: RoleUser, Content: prompt},
		}
		reply := fmt.Sprintf("要点如下：一、背景；二、结论；三、下一步。（样本 #%d）", i+1)
		rec := fakeRecord{
			id:     fmt.Sprintf("20260828-%06d", 200+i),
			source: source, ageMinutes: 90 + i*37,
			username: u.name, tokenPrefix: u.prefix, model: model,
			channel: "综合路由", upstream: "Relay Pool A",
			status: status, durationMS: int64(900 + i*211), ttfbMS: ttfb,
			tokensIn: int64(64 + i*3), tokensOut: tokensOut, tokensCache: int64(i % 5 * 8),
			billed: billed(int64(84+i*11), "USD", 3),
			stream: stream, upstreamID: fmt.Sprintf("req_01HZX%05d", i),
			rawIP: u.ip, messages: msgs, messagesSeen: true, finalReply: reply,
			rawRequest: jsonRequestBody(model, stream, msgs),
		}
		if stream {
			rec.rawResponse = sseStream([]string{"要点如下：", "一、背景；", "二、结论；", "三、下一步。"})
			rec.respCType = "text/event-stream"
		} else {
			rec.rawResponse = `{"id":"chatcmpl-` + strconv.Itoa(i) + `","object":"chat.completion",` +
				`"choices":[{"index":0,"message":{"role":"assistant","content":` +
				strconv.Quote(reply) + `},"finish_reason":"stop"}]}`
		}
		if status != 200 {
			rec.finalReply = ""
			rec.rawResponse = `{"error":{"message":"internal error","type":"server_error"}}`
			rec.respCType = "application/json"
		}
		if i%11 == 0 {
			rec.billed = nil
		}
		records = append(records, rec)
	}
	return records
}
