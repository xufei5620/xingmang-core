// Package contracttest 提供开票系统只读契约的合规测试套件。
//
// # 状态：草案（DRAFT），随契约一起未冻结
//
// 断言的是**平台侧的通用纪律**（只读、带新鲜度、金额整数、错误分类、
// PII 不进平台），这些不依赖 CR-0002 的确认结果；而字段清单与状态集合
// 一旦被开票线推翻，本套件里与之相关的断言也要跟着改（见 invoice 包的
// 包注释）。在 CR-0002 双方确认前，通过本套件**不等于**跨线批准。
//
// 任何 ReadClient 实现——Fake 或 XM-0029 的真实实现——都必须通过 RunSuite
// 才算合规。契约测试是一等交付物：它把「只读」「带新鲜度」「金额不用浮点」
// 「PII 不进平台」这些口头约定变成可执行的断言。
//
// 覆盖规格 §19.4 Connector 测试矩阵中不依赖真实网络的部分。
//
// 与 sub2api/contracttest 的关系：前 10 项是同一套矩阵（两个 Connector 的
// 通用纪律一致），后 5 项是开票独有的——分页、时间范围、状态枚举、以及
// CR-0002 勘察后最要紧的那条：契约结构体里不许出现 PII 字段。
package contracttest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/invoice"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// Factory 构造一个待测客户端。opts 允许套件注入故障场景；
// 真实实现可以忽略与网络无关的选项，但**必须**支持 FailWith 与 Latency，
// 否则故障路径无法被验证。
type Factory func(opts invoice.FakeOptions) invoice.ReadClient

// 套件里到处要用的一个合法业务日。用固定值而不是 time.Now()：
// 契约测试不该因为「今天几号」而有不同的行为。
const sampleDay = "2026-08-27"

// RunSuite 跑完整契约套件。
func RunSuite(t *testing.T, newClient Factory) {
	t.Helper()
	// 与 sub2api 同一套通用矩阵
	t.Run("Capabilities只读且可解析", func(t *testing.T) { testCapabilitiesAreReadOnly(t, newClient) })
	t.Run("每个结果都带新鲜度", func(t *testing.T) { testEveryResultCarriesSnapshot(t, newClient) })
	t.Run("金额是整数最小单位且带币种", func(t *testing.T) { testAmountsAreIntegerMinorUnits(t, newClient) })
	t.Run("不支持的版本不报错而是标记", func(t *testing.T) { testUnsupportedVersionIsFlagged(t, newClient) })
	t.Run("健康失败给结构化分类", func(t *testing.T) { testHealthFailureIsStructured(t, newClient) })
	t.Run("错误不透传上游原始文本", func(t *testing.T) { testErrorsAreClassified(t, newClient) })
	t.Run("上下文取消立即返回", func(t *testing.T) { testContextCancellation(t, newClient) })
	t.Run("非法业务日被拒绝", func(t *testing.T) { testInvalidDayRejected(t, newClient) })
	t.Run("部分数据被标记", func(t *testing.T) { testPartialIsFlagged(t, newClient) })
	t.Run("能力可少于清单", func(t *testing.T) { testCapabilitiesMayBeSubset(t, newClient) })

	// 开票独有
	t.Run("分页游标不重不漏", func(t *testing.T) { testPaginationIsCompleteAndUnique(t, newClient) })
	t.Run("非法时间范围被拒绝", func(t *testing.T) { testInvalidRangeRejected(t, newClient) })
	t.Run("非法状态过滤被拒绝", func(t *testing.T) { testInvalidStatusFilterRejected(t, newClient) })
	t.Run("状态只在9项枚举内", func(t *testing.T) { testStatusesAreWithinEnum(t, newClient) })
	t.Run("契约结构体不含PII字段", func(t *testing.T) { testNoPIIFieldsInContract(t) })
}

func testCapabilitiesAreReadOnly(t *testing.T, newClient Factory) {
	caps, err := newClient(invoice.FakeOptions{}).Capabilities(context.Background())
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
		// 「只读」在这里成为可验证属性而非口头承诺
		if parsed.IsWrite() {
			t.Fatalf("只读契约里出现了写能力 %q（ADR-018 闸 4）", c)
		}
		if !strings.HasPrefix(string(c), invoice.ConnectorKey+".") {
			t.Fatalf("能力 %q 应以 %q 为前缀", c, invoice.ConnectorKey)
		}
	}
}

func testEveryResultCarriesSnapshot(t *testing.T, newClient Factory) {
	// 规格 §9.1：禁止裸数字冒充实时完整数据
	c := newClient(invoice.FakeOptions{})
	ctx := context.Background()

	page, err := c.ListRequests(ctx, invoice.ListQuery{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	// 页级快照：空页也必须能回答「这个 0 是什么时候的 0」
	assertSnapshot(t, "RequestPage", page.Snapshot)
	if len(page.Items) == 0 {
		t.Fatal("固定数据集不应为空——空数据集下分页与汇总断言都失去意义")
	}
	for i, it := range page.Items {
		assertSnapshot(t, "InvoiceRequest["+it.ID+"]", it.Snapshot)
		if it.ID == "" {
			t.Fatalf("第 %d 条缺少 ID", i)
		}
		if it.RequestNo == "" {
			t.Fatalf("第 %d 条缺少 RequestNo", i)
		}
		if it.SubmittedAt.IsZero() {
			t.Fatalf("第 %d 条缺少 SubmittedAt", i)
		}
		if it.SubmittedAt.Location() != time.UTC {
			t.Fatalf("第 %d 条的 SubmittedAt 应为 UTC（规格 §5.9），got %v", i, it.SubmittedAt.Location())
		}
		if it.UpdatedAt.IsZero() {
			t.Fatalf("第 %d 条缺少 UpdatedAt", i)
		}
		if it.UpdatedAt.Location() != time.UTC {
			t.Fatalf("第 %d 条的 UpdatedAt 应为 UTC（规格 §5.9），got %v", i, it.UpdatedAt.Location())
		}
	}

	sum, err := c.DailySummary(ctx, sampleDay)
	if err != nil {
		t.Fatalf("DailySummary: %v", err)
	}
	assertSnapshot(t, "DailySummary", sum.Snapshot)
	if sum.Day != sampleDay {
		t.Fatalf("DailySummary.Day = %q, want %q——业务日必须原样回填，否则看板不知道这是哪天的数",
			sum.Day, sampleDay)
	}
}

func assertSnapshot(t *testing.T, what string, s invoice.Snapshot) {
	t.Helper()
	if s.ObservedAt.IsZero() {
		t.Fatalf("%s 缺少 ObservedAt——没有观测时刻就无法判断新鲜度（规格 §9.1）", what)
	}
	if s.ObservedAt.Location() != time.UTC {
		t.Fatalf("%s 的 ObservedAt 应为 UTC（规格 §5.9），got %v", what, s.ObservedAt.Location())
	}
	if s.Watermark == "" {
		t.Fatalf("%s 缺少 Watermark——无法判断是否读到完整区间", what)
	}
}

func testAmountsAreIntegerMinorUnits(t *testing.T, newClient Factory) {
	// 规格 §5.9：货币金额用整数最小货币单位，禁止 float。
	// 类型系统已保证是 int64；这里验证 Currency 必填——
	// 没有币种的金额是无意义的数字。
	c := newClient(invoice.FakeOptions{})
	ctx := context.Background()

	page, err := c.ListRequests(ctx, invoice.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range page.Items {
		if it.Currency == "" {
			t.Fatalf("开票申请 %s 的金额缺少 Currency", it.ID)
		}
		if it.AmountMinor < 0 {
			t.Fatalf("开票申请 %s 的金额为负: %d", it.ID, it.AmountMinor)
		}
	}

	sum, err := c.DailySummary(ctx, sampleDay)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Currency == "" {
		t.Fatal("DailySummary 的金额缺少 Currency")
	}
}

func testUnsupportedVersionIsFlagged(t *testing.T, newClient Factory) {
	// 不支持的版本应返回 Supported=false 而非报错：
	// 是否 Fail Closed 由调用方按场景决定（读取可降级）
	v, err := newClient(invoice.FakeOptions{UnsupportedVersion: true}).
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

	ok, err := newClient(invoice.FakeOptions{}).Version(context.Background())
	if err != nil || !ok.Supported {
		t.Fatalf("受支持的版本应 Supported=true: %+v %v", ok, err)
	}
	if ok.DetectedAt.IsZero() {
		t.Fatal("缺少 DetectedAt")
	}
}

func testHealthFailureIsStructured(t *testing.T, newClient Factory) {
	h, err := newClient(invoice.FakeOptions{Unhealthy: true}).Health(context.Background())
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
		c := newClient(invoice.FakeOptions{FailWith: kind})
		if _, err := c.ListRequests(context.Background(), invoice.ListQuery{}); err == nil {
			t.Fatalf("ListRequests 注入 %s 后应报错", kind)
		} else if connector.KindOf(err) != kind {
			t.Fatalf("ListRequests 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
		if _, err := c.DailySummary(context.Background(), sampleDay); err == nil {
			t.Fatalf("DailySummary 注入 %s 后应报错", kind)
		} else if connector.KindOf(err) != kind {
			t.Fatalf("DailySummary 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
	}
}

func testContextCancellation(t *testing.T, newClient Factory) {
	c := newClient(invoice.FakeOptions{Latency: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.ListRequests(ctx, invoice.ListQuery{})
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

func testInvalidDayRejected(t *testing.T, newClient Factory) {
	c := newClient(invoice.FakeOptions{})
	for _, day := range []string{"", "2026-13-01", "20260827", "2026/08/27", "yesterday"} {
		if _, err := c.DailySummary(context.Background(), day); err == nil {
			t.Fatalf("非法业务日 %q 应被拒绝", day)
		}
	}
}

func testPartialIsFlagged(t *testing.T, newClient Factory) {
	// 部分数据必须被标记，否则看板会把不完整数据当完整数据展示（规格 §9.1）
	ctx := context.Background()

	sum, err := newClient(invoice.FakeOptions{Partial: true}).DailySummary(ctx, sampleDay)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.IsPartial {
		t.Fatal("部分数据未被标记（DailySummary）")
	}
	page, err := newClient(invoice.FakeOptions{Partial: true}).ListRequests(ctx, invoice.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !page.IsPartial {
		t.Fatal("部分数据未被标记（RequestPage）")
	}

	full, err := newClient(invoice.FakeOptions{}).DailySummary(ctx, sampleDay)
	if err != nil {
		t.Fatal(err)
	}
	if full.IsPartial {
		t.Fatal("完整数据不应被标记为部分")
	}
}

func testCapabilitiesMayBeSubset(t *testing.T, newClient Factory) {
	// 旧版本上游可能少支持几项能力；契约允许返回子集，
	// 但不允许返回清单之外的能力
	missing := invoice.ReadCapabilities[0]
	caps, err := newClient(invoice.FakeOptions{
		MissingCapabilities: []registry.Capability{missing},
	}).Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[registry.Capability]struct{}, len(invoice.ReadCapabilities))
	for _, c := range invoice.ReadCapabilities {
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

// collectAllPages 按给定页大小翻完全部数据，返回 ID 顺序。
//
// 不依赖任何实现的数据条数：真实实现的数据量与 Fake 不同，套件只能断言
// 「翻页行为自洽」，不能断言「一共 25 条」。
func collectAllPages(t *testing.T, c invoice.ReadClient, limit int32) []string {
	t.Helper()
	ctx := context.Background()

	var ids []string
	cursor := ""
	// 上限防呆：游标实现有 bug 时（比如 NextCursor 永远不变）这里会无限循环，
	// 与其让 CI 挂到超时，不如当场报出「翻页不收敛」。
	const maxPages = 1000
	for page := 0; ; page++ {
		if page >= maxPages {
			t.Fatalf("翻页超过 %d 页仍未结束——游标没有推进", maxPages)
		}
		got, err := c.ListRequests(ctx, invoice.ListQuery{Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListRequests(limit=%d, cursor=%q): %v", limit, cursor, err)
		}
		if int32(len(got.Items)) > limit {
			t.Fatalf("一页返回了 %d 条，超过 Limit=%d", len(got.Items), limit)
		}
		for _, it := range got.Items {
			ids = append(ids, it.ID)
		}
		if got.NextCursor == "" {
			return ids
		}
		if len(got.Items) == 0 {
			t.Fatal("返回空页却给了 NextCursor——调用方会永远翻不到头")
		}
		cursor = got.NextCursor
	}
}

func testPaginationIsCompleteAndUnique(t *testing.T, newClient Factory) {
	c := newClient(invoice.FakeOptions{})

	// 两种页大小翻完，结果必须逐条相同：这同时否掉了「漏记录」（少了）、
	// 「重复记录」（多了）和「顺序不稳定」（错位）三种翻页 bug。
	small := collectAllPages(t, c, 3)
	large := collectAllPages(t, c, 7)

	if len(small) == 0 {
		t.Fatal("翻页结果为空——固定数据集不应为空")
	}
	if len(small) != len(large) {
		t.Fatalf("不同页大小翻出的条数不一致: limit=3 得 %d 条, limit=7 得 %d 条",
			len(small), len(large))
	}
	for i := range small {
		if small[i] != large[i] {
			t.Fatalf("第 %d 条不一致: limit=3 得 %q, limit=7 得 %q（翻页顺序不稳定）",
				i, small[i], large[i])
		}
	}

	seen := make(map[string]struct{}, len(small))
	for i, id := range small {
		if _, dup := seen[id]; dup {
			t.Fatalf("第 %d 条 ID %q 重复出现——游标重叠", i, id)
		}
		seen[id] = struct{}{}
	}

	// 一次拿完（用上限）应当与逐页翻出的是同一批，不能多也不能少
	oneShot, err := c.ListRequests(context.Background(), invoice.ListQuery{Limit: invoice.MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if oneShot.NextCursor == "" && len(oneShot.Items) != len(small) {
		t.Fatalf("单页取完得 %d 条，逐页翻出 %d 条", len(oneShot.Items), len(small))
	}

	// 坏游标必须被拒绝：静默从头开始会让调用方把第一页当成第二页处理
	if _, err := c.ListRequests(context.Background(),
		invoice.ListQuery{Cursor: "definitely-not-a-cursor"}); err == nil {
		t.Fatal("非法游标应被拒绝")
	}
}

func testInvalidRangeRejected(t *testing.T, newClient Factory) {
	c := newClient(invoice.FakeOptions{})
	from := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	to := from.Add(-time.Hour)

	// From 晚于 To 是调用方写错了，不是「查不到数据」。
	// 静默返回空集会让调用方以为那段时间真的没有开票。
	if _, err := c.ListRequests(context.Background(),
		invoice.ListQuery{From: from, To: to}); err == nil {
		t.Fatal("From 晚于 To 应被拒绝")
	}

	// 合法区间（含单端开放）必须放行
	for name, q := range map[string]invoice.ListQuery{
		"闭区间":  {From: to, To: from},
		"只给下界": {From: to},
		"只给上界": {To: from},
		"全开":   {},
	} {
		if _, err := c.ListRequests(context.Background(), q); err != nil {
			t.Fatalf("合法区间「%s」不应报错: %v", name, err)
		}
	}
}

func testInvalidStatusFilterRejected(t *testing.T, newClient Factory) {
	c := newClient(invoice.FakeOptions{})
	for _, s := range []string{"PENDING_REVIEW", "unknown", "issued ", "paid"} {
		if _, err := c.ListRequests(context.Background(), invoice.ListQuery{Status: s}); err == nil {
			t.Fatalf("非法状态过滤 %q 应被拒绝——静默忽略过滤条件会让调用方拿到自以为过滤过的全量数据", s)
		}
	}
	// 9 项合法状态都必须能当过滤条件用
	for _, s := range invoice.AllStatuses() {
		page, err := c.ListRequests(context.Background(), invoice.ListQuery{Status: string(s)})
		if err != nil {
			t.Fatalf("合法状态 %q 不应被拒绝: %v", s, err)
		}
		for _, it := range page.Items {
			if it.Status != s {
				t.Fatalf("按 %q 过滤却返回了 %q 的记录 %s", s, it.Status, it.ID)
			}
		}
	}
}

func testStatusesAreWithinEnum(t *testing.T, newClient Factory) {
	// 上游冒出契约外的状态时要当场炸，而不是让日汇总的分子分母悄悄对不上
	page, err := newClient(invoice.FakeOptions{}).
		ListRequests(context.Background(), invoice.ListQuery{Limit: invoice.MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range page.Items {
		if _, err := invoice.ParseStatus(string(it.Status)); err != nil {
			t.Fatalf("记录 %s 的状态 %q 不在 9 项枚举内: %v", it.ID, it.Status, err)
		}
	}
	if n := len(invoice.AllStatuses()); n != 9 {
		t.Fatalf("状态枚举应为 9 项（CR-0002 勘察结论），got %d", n)
	}
}

// piiFieldMarkers 是禁止出现在契约结构体字段名里的词根。
//
// 来自 CR-0002 勘察点名的那几类（TaxID / BankAccount / Address / Phone /
// Email），外加 profile / name / identity——开票抬头本身就是 PII。
// 匹配的是**字段名**而不是值：值可以脱敏，字段存在就意味着这条数据路径
// 在设计上是通的，早晚会有人往里填东西。
var piiFieldMarkers = []string{
	"profile", "tax", "email", "phone", "mobile", "address",
	"bank", "account", "identity", "idcard", "recipient", "payer",
	"title", "contact",
}

// contractTypes 是本契约暴露给上层的全部数据结构。
//
// 硬编码这份清单是刻意的：Go 没法在运行时枚举一个包的所有类型，而
// 「新加了个结构体忘了加进来」这件事，会在评审这份清单时被看见——
// 比让断言默默少覆盖一个类型强。
func contractTypes() map[string]reflect.Type {
	return map[string]reflect.Type{
		"InvoiceRequest": reflect.TypeOf(invoice.InvoiceRequest{}),
		"RequestPage":    reflect.TypeOf(invoice.RequestPage{}),
		"DailySummary":   reflect.TypeOf(invoice.DailySummary{}),
		"ListQuery":      reflect.TypeOf(invoice.ListQuery{}),
		"Snapshot":       reflect.TypeOf(invoice.Snapshot{}),
	}
}

func testNoPIIFieldsInContract(t *testing.T) {
	// 把「PII 不进平台」从评审时的口头提醒变成可执行断言。
	//
	// CR-0002 勘察发现开票系统现有 admin_dto.go 直接内嵌领域结构、吐出
	// TaxID/BankAccount/Address/Phone/Email 全量 PII。平台这边的防线不是
	// 「记得别读」，而是让这些字段在类型里根本不存在——于是需要一条会在
	// 有人加字段时立刻变红的测试，而不是一段迟早没人看的注释。
	for name, typ := range contractTypes() {
		walkStructFields(t, name, typ, 0)
	}
}

func walkStructFields(t *testing.T, path string, typ reflect.Type, depth int) {
	t.Helper()
	if depth > 8 {
		t.Fatalf("%s 嵌套过深，反射断言无法覆盖——契约结构体不该这么复杂", path)
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		lower := strings.ToLower(f.Name)
		for _, marker := range piiFieldMarkers {
			if strings.Contains(lower, marker) {
				t.Fatalf("%s.%s 命中 PII 词根 %q——开票契约里不得出现抬头/税号/"+
					"银行账号/地址/电话/邮箱等字段（CR-0002 硬要求）。"+
					"若确属误报，请在 piiFieldMarkers 旁记录理由后再放行。",
					path, f.Name, marker)
			}
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Time{}) {
			walkStructFields(t, path+"."+f.Name, ft, depth+1)
		}
	}
}
