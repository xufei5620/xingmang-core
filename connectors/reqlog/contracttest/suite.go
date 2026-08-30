// Package contracttest 提供 reqlog 只读契约的合规测试套件。
//
// 任何 ReadClient 实现——今天的 Fake、将来对着真实控制台 API 写的实现——
// 都必须通过 RunSuite 才算合规。在本契约还是 DRAFT 的阶段，这套断言承担的
// 责任比平时更重：它是「真实 API 接进来那天，怎么算接对了」的唯一可执行判据。
//
// 本套件里有几条是 reqlog 独有的，别的连接器没有：
//
//   - **摘要不含正文**——列表与内容的权限分级就落在这个类型边界上；
//   - **IP 已脱敏**——脱敏在契约层做，不是留给前端；
//   - **截断被标记且不切坏字符**——长对话与 SSE 流是这条通道的常态。
//
// 覆盖规格 §19.4 Connector 测试矩阵中不依赖真实网络的部分。
package contracttest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// Factory 构造一个待测客户端。opts 允许套件注入故障场景；
// 真实实现可以忽略与网络无关的选项，但**必须**支持 FailWith 与 Latency，
// 否则故障路径无法被验证。
type Factory func(opts reqlog.FakeOptions) reqlog.ReadClient

// RunSuite 跑完整契约套件。
func RunSuite(t *testing.T, newClient Factory) {
	t.Helper()
	t.Run("Capabilities只读且可解析", func(t *testing.T) { testCapabilitiesAreReadOnly(t, newClient) })
	t.Run("列表与内容都带新鲜度", func(t *testing.T) { testResultsCarrySnapshot(t, newClient) })
	t.Run("摘要不含正文", func(t *testing.T) { testSummaryCarriesNoBody(t, newClient) })
	t.Run("IP已按契约脱敏", func(t *testing.T) { testClientIPIsMasked(t, newClient) })
	t.Run("超长内容被截断且标记", func(t *testing.T) { testLongContentIsTruncated(t, newClient) })
	t.Run("流式请求给出装配后的回复", func(t *testing.T) { testStreamingContentIsAssembled(t, newClient) })
	t.Run("失败请求可读且不伪造用量", func(t *testing.T) { testFailedRequestIsReadable(t, newClient) })
	t.Run("过滤器逐项生效", func(t *testing.T) { testFiltersNarrowResults(t, newClient) })
	t.Run("统计覆盖分页前完整过滤集", func(t *testing.T) { testStatsCoverFullFilteredSet(t, newClient) })
	t.Run("空结果平均耗时为未知", func(t *testing.T) { testEmptyStatsHaveNoAverage(t, newClient) })
	t.Run("渠道上游与计费保留未知和已知零", func(t *testing.T) { testRoutingAndBillingMetadata(t, newClient) })
	t.Run("分页不重不漏", func(t *testing.T) { testPaginationIsExhaustive(t, newClient) })
	t.Run("跨来源读内容必须落空", func(t *testing.T) { testSourceIsolation(t, newClient) })
	t.Run("非法过滤条件被拒绝", func(t *testing.T) { testInvalidFilterRejected(t, newClient) })
	t.Run("保留期随每页返回", func(t *testing.T) { testRetentionIsReported(t, newClient) })
	t.Run("不支持的版本不报错而是标记", func(t *testing.T) { testUnsupportedVersionIsFlagged(t, newClient) })
	t.Run("健康失败给结构化分类", func(t *testing.T) { testHealthFailureIsStructured(t, newClient) })
	t.Run("错误不透传上游原始文本", func(t *testing.T) { testErrorsAreClassified(t, newClient) })
	t.Run("上下文取消立即返回", func(t *testing.T) { testContextCancellation(t, newClient) })
	t.Run("部分数据被标记", func(t *testing.T) { testPartialIsFlagged(t, newClient) })
	t.Run("能力可少于清单", func(t *testing.T) { testCapabilitiesMayBeSubset(t, newClient) })
}

func testStatsCoverFullFilteredSet(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()
	full := listAll(t, c, reqlog.SourceSub2API)
	if len(full) < 4 {
		t.Fatalf("样本太少（%d 条），无法证明统计不是当前页", len(full))
	}

	page, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Limit: 3,
	})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("当前页 = %d 条, want 3", len(page.Items))
	}
	if page.Stats.RequestCount != int64(len(full)) {
		t.Fatalf("request_count = %d, want 完整过滤集 %d（不是当前页 %d）",
			page.Stats.RequestCount, len(full), len(page.Items))
	}

	var success, failure, duration int64
	sawStatusZero := false
	for _, item := range full {
		if item.Status >= 200 && item.Status <= 299 {
			success++
		} else {
			failure++
		}
		if item.Status == 0 {
			sawStatusZero = true
		}
		duration += item.DurationMS
	}
	if !sawStatusZero {
		t.Fatal("样本必须有 status=0，才能证明无响应被统计为失败")
	}
	if page.Stats.SuccessCount != success || page.Stats.FailureCount != failure {
		t.Fatalf("成功/失败 = %d/%d, want %d/%d",
			page.Stats.SuccessCount, page.Stats.FailureCount, success, failure)
	}
	if page.Stats.AverageDurationMS == nil || *page.Stats.AverageDurationMS != duration/int64(len(full)) {
		t.Fatalf("average_duration_ms = %v, want %d",
			page.Stats.AverageDurationMS, duration/int64(len(full)))
	}
}

func testEmptyStatsHaveNoAverage(t *testing.T, newClient Factory) {
	page, err := newClient(reqlog.FakeOptions{}).ListRequests(context.Background(), reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Username: "no-such-user", Limit: 3,
	})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("空过滤结果却返回 %d 行", len(page.Items))
	}
	if page.Stats.RequestCount != 0 || page.Stats.SuccessCount != 0 || page.Stats.FailureCount != 0 {
		t.Fatalf("空结果统计必须全为 0: %+v", page.Stats)
	}
	if page.Stats.AverageDurationMS != nil {
		t.Fatalf("空结果平均耗时必须为 nil，不能伪造成 0 ms: %d", *page.Stats.AverageDurationMS)
	}
}

// testRoutingAndBillingMetadata 校验渠道/上游元数据与计费金额，但只在
// 客户端**声明了**对应能力（reqlog.CapabilityRoutingRead /
// reqlog.CapabilityBillingRead）时才要求样本覆盖——这两项是可选能力，
// 磁盘格式本身不采集这个维度的后端（文件后端，见
// contracts/connectors/reqlog.read.v1.md §10）可以不声明，此时改为断言
// 该维度**恒为未知**（Channel/Upstream 恒空、BilledAmount 恒 nil），
// 而不是要求它凭空生产磁盘上从未有过的数据。声明了却给不出、或没声明却
// 偷偷给出，两者都判失败——"能力清单"因此仍然是可验证的事实。
func testRoutingAndBillingMetadata(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	caps, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	hasRouting := hasCapability(caps, reqlog.CapabilityRoutingRead)
	hasBilling := hasCapability(caps, reqlog.CapabilityBillingRead)

	items := append(listAll(t, c, reqlog.SourceSub2API), listAll(t, c, reqlog.SourceNewAPI)...)
	sawKnownZero, sawUnknown, sawRouting := false, false, false
	for _, item := range items {
		if item.Channel != "" || item.Upstream != "" {
			sawRouting = true
		}
		if item.BilledAmount == nil {
			sawUnknown = true
			continue
		}
		if !hasBilling {
			t.Fatalf("记录 %s 未声明 %s 能力，却返回了非 nil 的 BilledAmount："+
				"不采集这个维度的后端必须让金额恒为未知，不能声明不支持又偷偷给出数据",
				item.ID, reqlog.CapabilityBillingRead)
		}
		if err := item.BilledAmount.Validate(); err != nil {
			t.Fatalf("记录 %s 的计费金额非法: %v", item.ID, err)
		}
		if item.BilledAmount.AmountMinor == 0 {
			sawKnownZero = true
		}
	}

	if hasRouting {
		if !sawRouting {
			t.Fatal("声明了 routing_read 能力，样本里却没有渠道/上游元数据")
		}
	} else {
		t.Logf("未声明 %s：跳过渠道/上游元数据断言（磁盘格式本身不采集这个维度）",
			reqlog.CapabilityRoutingRead)
	}

	if hasBilling {
		if !sawKnownZero || !sawUnknown {
			t.Fatalf("声明了 billing_read 能力，计费样本必须同时覆盖已知 0 与未知: knownZero=%v unknown=%v",
				sawKnownZero, sawUnknown)
		}
	} else {
		t.Logf("未声明 %s：已确认全部样本 BilledAmount 为 nil（未知，不冒充已知 0）",
			reqlog.CapabilityBillingRead)
	}
}

func hasCapability(caps []registry.Capability, want registry.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// listAll 把某个来源的全部记录翻完，供多条断言复用。
func listAll(t *testing.T, c reqlog.ReadClient, source string) []reqlog.RequestLogSummary {
	t.Helper()
	var out []reqlog.RequestLogSummary
	cursor := ""
	for range 100 { // 上限只是防死循环，正常几页就翻完了
		page, err := c.ListRequests(context.Background(), reqlog.ListFilter{
			Source: source, Cursor: cursor, Limit: 25,
		})
		if err != nil {
			t.Fatalf("ListRequests(%s): %v", source, err)
		}
		out = append(out, page.Items...)
		if page.NextCursor == "" {
			return out
		}
		cursor = page.NextCursor
	}
	t.Fatalf("翻页超过 100 次仍未结束，游标可能不前进")
	return nil
}

func testCapabilitiesAreReadOnly(t *testing.T, newClient Factory) {
	caps, err := newClient(reqlog.FakeOptions{}).Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps) == 0 {
		t.Fatal("能力清单不应为空")
	}
	for _, c := range caps {
		parsed, err := registry.ParseCapability(string(c))
		if err != nil {
			t.Fatalf("能力 %q 不符合命名规范: %v", c, err)
		}
		// 「只读」在这里成为可验证属性而非口头承诺（ADR-018 闸 4）
		if parsed.IsWrite() {
			t.Fatalf("只读契约里出现了写能力 %q", c)
		}
		if !strings.HasPrefix(string(c), reqlog.ConnectorKey+".") {
			t.Fatalf("能力 %q 应以 %q 为前缀", c, reqlog.ConnectorKey)
		}
	}
}

func testResultsCarrySnapshot(t *testing.T, newClient Factory) {
	// 规格 §9.1：禁止裸数字冒充实时完整数据
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()

	page, err := c.ListRequests(ctx, reqlog.ListFilter{Source: reqlog.SourceSub2API})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	assertSnapshot(t, "ListRequests", page.Snapshot)
	if len(page.Items) == 0 {
		t.Fatal("样本不应为空——没有数据的契约测试什么也证明不了")
	}
	for _, item := range page.Items {
		if item.OccurredAt.IsZero() {
			t.Fatalf("记录 %s 缺少 OccurredAt", item.ID)
		}
		if item.OccurredAt.Location() != time.UTC {
			t.Fatalf("记录 %s 的 OccurredAt 应为 UTC（宪法 14 条），got %v",
				item.ID, item.OccurredAt.Location())
		}
		if item.ID == "" || item.Source == "" {
			t.Fatalf("记录缺少 ID 或 Source: %+v", item)
		}
		// id 要能原样放进 `/requests/{id}` 的一个路径段。带斜杠的 id
		// 会被路由切成两段，症状是「记录在列表里、点进去 404」——
		// 而 reqlog 按天分目录存放，上游标识很可能天生带斜杠
		if !reqlog.IsPathSafeID(item.ID) {
			t.Fatalf("记录 ID %q 不是 URL 路径安全的：连接器负责把上游标识映射成"+
				"安全形态（见 RequestLogSummary.ID 的说明）", item.ID)
		}
	}

	content, err := c.RequestContent(ctx, page.Items[0].Source, page.Items[0].ID)
	if err != nil {
		t.Fatalf("RequestContent: %v", err)
	}
	assertSnapshot(t, "RequestContent", content.Snapshot)
	if content.Summary.ID != page.Items[0].ID {
		t.Fatalf("内容里的摘要对不上：%q vs %q", content.Summary.ID, page.Items[0].ID)
	}
}

func assertSnapshot(t *testing.T, what string, s reqlog.Snapshot) {
	t.Helper()
	if s.ObservedAt.IsZero() {
		t.Fatalf("%s 缺少 ObservedAt——没有观测时刻就无法判断新鲜度（规格 §9.1）", what)
	}
	if s.ObservedAt.Location() != time.UTC {
		t.Fatalf("%s 的 ObservedAt 应为 UTC（宪法 14 条），got %v", what, s.ObservedAt.Location())
	}
	if s.Watermark == "" {
		t.Fatalf("%s 缺少 Watermark", what)
	}
	// 来源标识是「这段对话是真的吗」在数据层的唯一答案：请求详情不走指标表，
	// 没有 observation 可以承载来源，所以读取结果必须自己带上。缺了它，
	// 前端只能假定是真实数据——而那是最坏的默认
	if s.Instance == "" {
		t.Fatalf("%s 缺少 Instance：界面靠它区分演示数据与真实用户对话", what)
	}
}

func testSummaryCarriesNoBody(t *testing.T, newClient Factory) {
	// 列表与内容的权限分级（request.read vs request.content.read）就落在
	// 「摘要里有没有正文」这一条上。用序列化后的整串去找正文片段，
	// 这样将来给 Summary 新加一个字段、又不小心把正文塞进去，这条会当场变红。
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()

	for _, source := range []string{reqlog.SourceSub2API, reqlog.SourceNewAPI} {
		items := listAll(t, c, source)
		if len(items) == 0 {
			t.Fatalf("来源 %s 没有样本", source)
		}
		for _, item := range items {
			content, err := c.RequestContent(ctx, item.Source, item.ID)
			if err != nil {
				t.Fatalf("RequestContent(%s): %v", item.ID, err)
			}
			encoded, err := json.Marshal(item)
			if err != nil {
				t.Fatalf("序列化摘要: %v", err)
			}
			blob := string(encoded)
			for _, m := range content.Messages {
				// 取一段足够长的片段比对：短片段（"ok"）会误伤
				needle := firstRunes(m.Content, 24)
				if needle == "" {
					continue
				}
				if strings.Contains(blob, needle) {
					t.Fatalf("摘要里出现了正文片段（记录 %s，角色 %s）：摘要必须不含正文",
						item.ID, m.Role)
				}
			}
			if needle := firstRunes(content.FinalReply, 24); needle != "" &&
				strings.Contains(blob, needle) {
				t.Fatalf("摘要里出现了最终回复片段（记录 %s）", item.ID)
			}
		}
	}
}

// firstRunes 取前 n 个字符（按 rune，不按字节），不足则返回空串。
func firstRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) < n {
		return ""
	}
	return string(runes[:n])
}

func testClientIPIsMasked(t *testing.T, newClient Factory) {
	// 脱敏在契约层做（见 RequestLogSummary.ClientIP 的注释）：一个原样返回
	// 明文 IP 的列表端点，只要有人抓一遍就等于导出了全量用户 IP。
	c := newClient(reqlog.FakeOptions{})
	for _, source := range []string{reqlog.SourceSub2API, reqlog.SourceNewAPI} {
		for _, item := range listAll(t, c, source) {
			if item.ClientIP == "" {
				continue // 「没记 IP」是允许的
			}
			if item.ClientIP == reqlog.UnparsedIPPlaceholder {
				t.Fatalf("记录 %s 的 IP 解析失败：样本里不该有解析不了的地址（真实数据里出现它是信号，样本里出现它是 bug）", item.ID)
			}
			if !strings.HasSuffix(item.ClientIP, reqlog.MaskedIPSuffix) {
				t.Fatalf("记录 %s 的 ClientIP %q 未脱敏：必须以 %q 结尾",
					item.ID, item.ClientIP, reqlog.MaskedIPSuffix)
			}
		}
	}
}

func testLongContentIsTruncated(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()

	sawTruncated := false
	for _, source := range []string{reqlog.SourceSub2API, reqlog.SourceNewAPI} {
		for _, item := range listAll(t, c, source) {
			content, err := c.RequestContent(ctx, item.Source, item.ID)
			if err != nil {
				t.Fatalf("RequestContent(%s): %v", item.ID, err)
			}
			if len(content.Messages) > reqlog.MaxMessages {
				t.Fatalf("记录 %s 返回 %d 条消息，超过上限 %d",
					item.ID, len(content.Messages), reqlog.MaxMessages)
			}
			for _, m := range content.Messages {
				if len(m.Content) > reqlog.MaxMessageBytes {
					t.Fatalf("记录 %s 的消息超过 %d 字节上限：%d",
						item.ID, reqlog.MaxMessageBytes, len(m.Content))
				}
				// 截断不许切坏字符：切开一个多字节字符会产出 U+FFFD，
				// 而那个替换符看起来像「上游返回了乱码」
				if !utf8.ValidString(m.Content) {
					t.Fatalf("记录 %s 的消息被截成了非法 UTF-8", item.ID)
				}
				if m.Truncated {
					sawTruncated = true
					if m.OriginalBytes <= int64(len(m.Content)) {
						t.Fatalf("记录 %s 标了截断，但 OriginalBytes(%d) 未大于当前长度(%d)——"+
							"界面靠这个数字说「共多少字节」",
							item.ID, m.OriginalBytes, len(m.Content))
					}
				} else if m.OriginalBytes != int64(len(m.Content)) {
					t.Fatalf("记录 %s 未截断，但 OriginalBytes(%d) != 长度(%d)",
						item.ID, m.OriginalBytes, len(m.Content))
				}
			}
			if len(content.FinalReply) > reqlog.MaxFinalReplyBytes {
				t.Fatalf("记录 %s 的最终回复超过上限", item.ID)
			}
			for name, p := range map[string]reqlog.RawPayload{
				"request": content.RawRequest, "response": content.RawResponse,
			} {
				if len(p.Body) > reqlog.MaxRawPayloadBytes {
					t.Fatalf("记录 %s 的 %s 原始载荷超过上限", item.ID, name)
				}
				if !utf8.ValidString(p.Body) {
					t.Fatalf("记录 %s 的 %s 原始载荷被截成了非法 UTF-8", item.ID, name)
				}
			}
		}
	}
	if !sawTruncated {
		t.Fatal("没有任何样本触发截断：截断路径必须有样本走过，" +
			"否则界面上的截断提示在上线前一次都没被渲染过")
	}
}

func testStreamingContentIsAssembled(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()

	found := false
	for _, source := range []string{reqlog.SourceSub2API, reqlog.SourceNewAPI} {
		for _, item := range listAll(t, c, source) {
			if !item.Stream || item.Status != 200 {
				continue
			}
			content, err := c.RequestContent(ctx, item.Source, item.ID)
			if err != nil {
				t.Fatalf("RequestContent(%s): %v", item.ID, err)
			}
			// 装配后的回复必须有内容：详情页把它当成「模型到底回了什么」
			if strings.TrimSpace(content.FinalReply) == "" {
				t.Fatalf("流式记录 %s 的 FinalReply 为空：SSE 事件流必须被装配成最终回复", item.ID)
			}
			// 原始载荷必须仍是事件流，而不是被装配结果顶替——两者都要，
			// 因为装配可能出错，而排查那种问题只能看原文
			if !strings.Contains(content.RawResponse.Body, "data:") {
				t.Fatalf("流式记录 %s 的原始响应不像 SSE 事件流：装配结果不能顶替原文", item.ID)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("样本里没有成功的流式请求：SSE 是这条通道的常态形态，必须有样本")
	}
}

func testFailedRequestIsReadable(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()

	found := false
	for _, source := range []string{reqlog.SourceSub2API, reqlog.SourceNewAPI} {
		for _, item := range listAll(t, c, source) {
			if item.Status >= 200 && item.Status <= 299 {
				continue
			}
			found = true
			// 失败请求的用量不该被编出来：上游没算就是 0，
			// 补一个「估算值」会让计费口径凭空多出一批不存在的消耗
			if item.TokensOut != 0 {
				t.Fatalf("失败记录 %s 的 TokensOut=%d：上游没产出就不该有输出用量",
					item.ID, item.TokensOut)
			}
			content, err := c.RequestContent(ctx, item.Source, item.ID)
			if err != nil {
				t.Fatalf("失败请求的内容也必须可读（排查就靠它）：%v", err)
			}
			if strings.TrimSpace(content.RawResponse.Body) == "" {
				t.Fatalf("失败记录 %s 没有响应原文：那正是要看的东西", item.ID)
			}
		}
	}
	if !found {
		t.Fatal("样本里没有失败请求：状态过滤器与错误渲染都无从验证")
	}
}

func testFiltersNarrowResults(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()
	all := listAll(t, c, reqlog.SourceSub2API)
	if len(all) < 3 {
		t.Fatalf("样本太少（%d 条），过滤器无从验证", len(all))
	}

	// 用户名
	target := ""
	for _, item := range all {
		if item.Username != "" {
			target = item.Username
			break
		}
	}
	if target == "" {
		t.Fatal("样本里没有带用户名的记录")
	}
	byUser, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Username: target, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("按用户名过滤: %v", err)
	}
	if len(byUser.Items) == 0 {
		t.Fatalf("按用户名 %q 过滤后为空", target)
	}
	for _, item := range byUser.Items {
		if item.Username != target {
			t.Fatalf("按用户名 %q 过滤后混进了 %q", target, item.Username)
		}
	}

	// 模型
	model := all[0].Model
	byModel, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Model: model, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("按模型过滤: %v", err)
	}
	if len(byModel.Items) == 0 {
		t.Fatalf("按模型 %q 过滤后为空", model)
	}
	for _, item := range byModel.Items {
		if item.Model != model {
			t.Fatalf("按模型 %q 过滤后混进了 %q", model, item.Model)
		}
	}

	// 状态：success / error 必须互补且并起来等于全集
	ok, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Status: reqlog.StatusSuccess, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("按成功过滤: %v", err)
	}
	bad, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Status: reqlog.StatusError, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("按失败过滤: %v", err)
	}
	for _, item := range ok.Items {
		if item.Status < 200 || item.Status > 299 {
			t.Fatalf("success 里混进了状态 %d", item.Status)
		}
	}
	for _, item := range bad.Items {
		if item.Status >= 200 && item.Status <= 299 {
			t.Fatalf("error 里混进了状态 %d", item.Status)
		}
	}
	if len(ok.Items)+len(bad.Items) != len(all) {
		t.Fatalf("success(%d) + error(%d) != 全集(%d)：状态过滤必须是一次划分，"+
			"漏掉的那些记录在任何一个筛选下都看不见",
			len(ok.Items), len(bad.Items), len(all))
	}

	// 时间区间：取最新一条的时刻作为下界，结果必须都不早于它
	newest := all[0].OccurredAt
	for _, item := range all {
		if item.OccurredAt.After(newest) {
			newest = item.OccurredAt
		}
	}
	since := newest.Add(-time.Minute)
	recent, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Since: since, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("按时间过滤: %v", err)
	}
	if len(recent.Items) == 0 {
		t.Fatal("按最近一分钟过滤后为空，但样本里刚有一条落在这个区间")
	}
	for _, item := range recent.Items {
		if item.OccurredAt.Before(since) {
			t.Fatalf("时间过滤后混进了 %v（下界 %v）", item.OccurredAt, since)
		}
	}
}

func testPaginationIsExhaustive(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()

	// 一次取满
	full, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(full.Items) < 4 {
		t.Fatalf("样本太少（%d 条），分页无从验证", len(full.Items))
	}

	// 小页翻完，逐条比对：既不能漏，也不能重
	seen := make(map[string]int, len(full.Items))
	var order []string
	cursor := ""
	for range 100 {
		page, err := c.ListRequests(ctx, reqlog.ListFilter{
			Source: reqlog.SourceSub2API, Limit: 3, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("翻页: %v", err)
		}
		for _, item := range page.Items {
			seen[item.ID]++
			order = append(order, item.ID)
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("游标没有前进，会无限翻页")
		}
		cursor = page.NextCursor
	}
	if len(order) != len(full.Items) {
		t.Fatalf("分页翻出 %d 条，一次取满是 %d 条", len(order), len(full.Items))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("记录 %s 在翻页中出现了 %d 次", id, n)
		}
	}
	for i, item := range full.Items {
		if order[i] != item.ID {
			t.Fatalf("第 %d 条顺序不一致：翻页得到 %s，一次取满是 %s", i, order[i], item.ID)
		}
	}

	// 非法游标必须是一次明确的参数错，而不是静默返回第一页——
	// 静默回退会让「游标坏了」表现为「翻页翻不动」，最难查的那一种
	if _, err := c.ListRequests(ctx, reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Cursor: "不是游标",
	}); err == nil {
		t.Fatal("非法游标应被拒绝")
	}
}

func testSourceIsolation(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()

	items := listAll(t, c, reqlog.SourceSub2API)
	if len(items) == 0 {
		t.Fatal("sub2api 没有样本")
	}
	// 拿 sub2api 的 id 去读 newapi：必须落空。id 恰好唯一不构成放行的理由——
	// 那会让一次「平台选错了」的点击静默命中另一个平台的用户数据
	_, err := c.RequestContent(ctx, reqlog.SourceNewAPI, items[0].ID)
	if err == nil {
		t.Fatalf("跨来源读取 %s 应落空", items[0].ID)
	}
	if !errors.Is(err, reqlog.ErrNotFound) {
		t.Fatalf("跨来源读取应可追溯到 ErrNotFound，got %v", err)
	}

	// 不存在的 id 同样落空
	if _, err := c.RequestContent(ctx, reqlog.SourceSub2API, "no-such-record"); err == nil {
		t.Fatal("不存在的 id 应落空")
	}
}

func testInvalidFilterRejected(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{})
	ctx := context.Background()
	now := time.Now().UTC()

	cases := map[string]reqlog.ListFilter{
		"source 为空":  {},
		"source 不认识": {Source: "cpa"},
		"status 不认识": {Source: reqlog.SourceSub2API, Status: "failed"},
		"时间区间为空":     {Source: reqlog.SourceSub2API, Since: now, Until: now.Add(-time.Hour)},
	}
	for name, filter := range cases {
		if _, err := c.ListRequests(ctx, filter); err == nil {
			t.Fatalf("%s：应被拒绝", name)
		}
	}
	// source 为空必须报错而不是「查全部」：混合两个平台的结果是最容易被
	// 误读成「这个平台请求量翻倍了」的那种错
	if _, err := c.RequestContent(ctx, "", "whatever"); err == nil {
		t.Fatal("RequestContent 的空 source 应被拒绝")
	}
}

func testRetentionIsReported(t *testing.T, newClient Factory) {
	// 「查不到」必须能被区分成「那天没有请求」与「过了保留期」，
	// 而界面要就地说得出保留窗口有多长
	page, err := newClient(reqlog.FakeOptions{}).ListRequests(context.Background(),
		reqlog.ListFilter{Source: reqlog.SourceSub2API})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if page.RetentionDays <= 0 {
		t.Fatalf("每页必须带保留期天数，got %d", page.RetentionDays)
	}
}

func testUnsupportedVersionIsFlagged(t *testing.T, newClient Factory) {
	v, err := newClient(reqlog.FakeOptions{UnsupportedVersion: true}).
		Version(context.Background())
	if err != nil {
		t.Fatalf("不支持的版本不应报错，而应标记 Supported=false: %v", err)
	}
	if v.Supported {
		t.Fatal("应标记为不支持")
	}
	if v.Detected == "" || v.Fingerprint == "" {
		t.Fatalf("即便不支持也要给出探测值与指纹: %+v", v)
	}

	ok, err := newClient(reqlog.FakeOptions{}).Version(context.Background())
	if err != nil || !ok.Supported {
		t.Fatalf("受支持的版本应 Supported=true: %+v %v", ok, err)
	}
	if ok.DetectedAt.IsZero() {
		t.Fatal("缺少 DetectedAt")
	}
}

func testHealthFailureIsStructured(t *testing.T, newClient Factory) {
	h, err := newClient(reqlog.FakeOptions{Unhealthy: true}).Health(context.Background())
	if err != nil {
		t.Fatalf("不健康不等于调用失败，应返回结构化结果: %v", err)
	}
	if h.Healthy {
		t.Fatal("应标记为不健康")
	}
	if h.ErrorKind == "" {
		t.Fatal("不健康时必须给出 ErrorKind，调用方据此决策")
	}
	if h.CheckedAt.IsZero() {
		t.Fatal("缺少 CheckedAt")
	}
}

func testErrorsAreClassified(t *testing.T, newClient Factory) {
	// ADR-004 铁律：第三方错误不得原样透传
	for _, kind := range []connector.ErrorKind{
		connector.KindUnavailable, connector.KindAuth,
		connector.KindRateLimited, connector.KindBadResponse,
	} {
		c := newClient(reqlog.FakeOptions{FailWith: kind})
		if _, err := c.ListRequests(context.Background(),
			reqlog.ListFilter{Source: reqlog.SourceSub2API}); err == nil {
			t.Fatalf("注入 %s 后 ListRequests 应报错", kind)
		} else if connector.KindOf(err) != kind {
			t.Fatalf("ListRequests 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
		if _, err := c.RequestContent(context.Background(),
			reqlog.SourceSub2API, "20260828-000117"); err == nil {
			t.Fatalf("注入 %s 后 RequestContent 应报错", kind)
		} else if connector.KindOf(err) != kind {
			t.Fatalf("RequestContent 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
	}
}

func testContextCancellation(t *testing.T, newClient Factory) {
	c := newClient(reqlog.FakeOptions{Latency: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.ListRequests(ctx, reqlog.ListFilter{Source: reqlog.SourceSub2API})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("上下文取消后应返回错误")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("上下文取消后应立即返回，实际耗时 %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) && connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("取消错误应可追溯到 context 或归类为 unavailable: %v", err)
	}
}

func testPartialIsFlagged(t *testing.T, newClient Factory) {
	// 部分数据必须被标记，否则看板会把不完整数据当完整数据展示（规格 §9.1）
	page, err := newClient(reqlog.FakeOptions{Partial: true}).ListRequests(
		context.Background(), reqlog.ListFilter{Source: reqlog.SourceSub2API})
	if err != nil {
		t.Fatal(err)
	}
	if !page.IsPartial {
		t.Fatal("部分数据未被标记")
	}
	full, err := newClient(reqlog.FakeOptions{}).ListRequests(
		context.Background(), reqlog.ListFilter{Source: reqlog.SourceSub2API})
	if err != nil {
		t.Fatal(err)
	}
	if full.IsPartial {
		t.Fatal("完整数据不应被标记为部分")
	}
}

func testCapabilitiesMayBeSubset(t *testing.T, newClient Factory) {
	missing := reqlog.ReadCapabilities[0]
	caps, err := newClient(reqlog.FakeOptions{
		MissingCapabilities: []registry.Capability{missing},
	}).Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[registry.Capability]struct{}, len(reqlog.ReadCapabilities))
	for _, c := range reqlog.ReadCapabilities {
		known[c] = struct{}{}
	}
	for _, c := range caps {
		if _, ok := known[c]; !ok {
			t.Fatalf("返回了清单之外的能力 %q", c)
		}
		if c == missing {
			t.Fatalf("被移除的能力 %q 仍然出现", missing)
		}
	}
}
