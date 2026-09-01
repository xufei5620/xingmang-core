// Package contracttest 提供 NewAPI 只读契约的合规测试套件。
//
// 任何 ReadClient 实现——Fake 或 XM-0038 的真实实现——都必须通过 RunSuite
// 才算合规。契约测试是一等交付物：它把「只读」「带新鲜度」「金额与比率
// 都不用浮点」「余额未配置不等于零」这些口头约定变成可执行的断言。
//
// 覆盖规格 §19.4 Connector 测试矩阵中不依赖真实网络的部分。
package contracttest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// testDay 是套件里用到的业务日。固定值：契约测试不该随运行日期变结果。
const testDay = "2026-08-27"

// Factory 构造一个待测客户端。opts 允许套件注入故障场景；
// 真实实现可以忽略与网络无关的选项，但**必须**支持 FailWith 与 Latency，
// 否则故障路径无法被验证。
type Factory func(opts newapi.FakeOptions) newapi.ReadClient

// RunSuite 跑完整契约套件。
//
// 前 10 项与 sub2api 的套件逐条对应（同一套测试矩阵，评审时不必重新理解）；
// 后 2 项是 NewAPI 独有的两条纪律：渠道余额的可空语义、错误率的 ppm 整数表达。
func RunSuite(t *testing.T, newClient Factory) {
	t.Helper()
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
	t.Run("渠道余额未配置不等于零", func(t *testing.T) { testChannelBalanceNilIsNotZero(t, newClient) })
	t.Run("错误率是ppm整数不是浮点", func(t *testing.T) { testErrorRateIsIntegerPPM(t, newClient) })
}

func testCapabilitiesAreReadOnly(t *testing.T, newClient Factory) {
	caps, err := newClient(newapi.FakeOptions{}).Capabilities(context.Background())
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
		if !strings.HasPrefix(string(c), newapi.ConnectorKey+".") {
			t.Fatalf("能力 %q 应以 %q 为前缀", c, newapi.ConnectorKey)
		}
	}
}

func testEveryResultCarriesSnapshot(t *testing.T, newClient Factory) {
	// 规格 §9.1：禁止裸数字冒充实时完整数据
	c := newClient(newapi.FakeOptions{})
	ctx := context.Background()

	stats, err := c.UserStats(ctx)
	if err != nil {
		t.Fatalf("UserStats: %v", err)
	}
	assertSnapshot(t, "UserStats", stats.Snapshot)

	orders, err := c.DailyOrders(ctx, testDay)
	if err != nil {
		t.Fatalf("DailyOrders: %v", err)
	}
	assertSnapshot(t, "DailyOrders", orders.Snapshot)

	channels, err := c.Channels(ctx)
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(channels) == 0 {
		t.Fatal("渠道清单不应为空")
	}
	for i, ch := range channels {
		assertSnapshot(t, "Channels["+ch.ChannelID+"]", ch.Snapshot)
		if ch.ChannelID == "" {
			t.Fatalf("渠道 #%d 缺少 ChannelID", i)
		}
	}

	usages, err := c.ModelUsages(ctx, testDay)
	if err != nil {
		t.Fatalf("ModelUsages: %v", err)
	}
	if len(usages) == 0 {
		t.Fatal("模型用量不应为空")
	}
	for i, u := range usages {
		assertSnapshot(t, "ModelUsages["+u.ModelName+"]", u.Snapshot)
		if u.ModelName == "" {
			t.Fatalf("模型用量 #%d 缺少 ModelName", i)
		}
	}
}

func assertSnapshot(t *testing.T, what string, s newapi.Snapshot) {
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
	// 类型系统已保证是 int64（另见 testErrorRateIsIntegerPPM 的反射断言）；
	// 这里验证 Currency 必填——没有币种的金额是无意义的数字。
	c := newClient(newapi.FakeOptions{})
	ctx := context.Background()

	stats, err := c.UserStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Currency == "" {
		t.Fatal("UserStats 的金额缺少 Currency")
	}
	orders, err := c.DailyOrders(ctx, testDay)
	if err != nil {
		t.Fatal(err)
	}
	if orders.Currency == "" {
		t.Fatal("OrderSummary 的金额缺少 Currency")
	}
	channels, err := c.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range channels {
		// 余额为 nil 的渠道不要求币种：没有金额，币种就没有依附的对象。
		// 有金额却没币种才是错的。
		if ch.BalanceMinorUnits != nil && ch.Currency == "" {
			t.Fatalf("渠道 %s 有余额却缺少 Currency", ch.ChannelID)
		}
	}
	usages, err := c.ModelUsages(ctx, testDay)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range usages {
		if u.Currency == "" {
			t.Fatalf("模型 %s 的消耗额缺少 Currency", u.ModelName)
		}
	}
}

func testUnsupportedVersionIsFlagged(t *testing.T, newClient Factory) {
	// 不支持的版本应返回 Supported=false 而非报错：
	// 是否 Fail Closed 由调用方按场景决定（读取可降级，写入必须停）
	v, err := newClient(newapi.FakeOptions{UnsupportedVersion: true}).
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

	ok, err := newClient(newapi.FakeOptions{}).Version(context.Background())
	if err != nil || !ok.Supported {
		t.Fatalf("受支持的版本应 Supported=true: %+v %v", ok, err)
	}
	if ok.DetectedAt.IsZero() {
		t.Fatal("缺少 DetectedAt")
	}
}

func testHealthFailureIsStructured(t *testing.T, newClient Factory) {
	h, err := newClient(newapi.FakeOptions{Unhealthy: true}).Health(context.Background())
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
		c := newClient(newapi.FakeOptions{FailWith: kind})
		// 四个数据读取方法都要归类，而不是只测一个：分组失败记录
		// （jobs 的 newapi_sync）依赖的正是「每个读取各自给出分类错误」。
		if _, err := c.UserStats(context.Background()); connector.KindOf(err) != kind {
			t.Fatalf("UserStats 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
		if _, err := c.DailyOrders(context.Background(), testDay); connector.KindOf(err) != kind {
			t.Fatalf("DailyOrders 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
		if _, err := c.Channels(context.Background()); connector.KindOf(err) != kind {
			t.Fatalf("Channels 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
		if _, err := c.ModelUsages(context.Background(), testDay); connector.KindOf(err) != kind {
			t.Fatalf("ModelUsages 错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
	}
}

func testContextCancellation(t *testing.T, newClient Factory) {
	c := newClient(newapi.FakeOptions{Latency: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.UserStats(ctx)
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
	c := newClient(newapi.FakeOptions{})
	for _, day := range []string{"", "2026-13-01", "20260827", "2026/08/27", "yesterday"} {
		if _, err := c.DailyOrders(context.Background(), day); err == nil {
			t.Fatalf("DailyOrders 非法业务日 %q 应被拒绝", day)
		}
		// ModelUsages 也吃业务日，同一条判据必须同样生效——
		// 只在一个入口上校验，另一个入口就成了绕过它的后门。
		if _, err := c.ModelUsages(context.Background(), day); err == nil {
			t.Fatalf("ModelUsages 非法业务日 %q 应被拒绝", day)
		}
	}
}

func testPartialIsFlagged(t *testing.T, newClient Factory) {
	// 部分数据必须被标记，否则看板会把不完整数据当完整数据展示（规格 §9.1）
	stats, err := newClient(newapi.FakeOptions{Partial: true}).UserStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.IsPartial {
		t.Fatal("部分数据未被标记")
	}
	full, err := newClient(newapi.FakeOptions{}).UserStats(context.Background())
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
	missing := newapi.ReadCapabilities[0]
	caps, err := newClient(newapi.FakeOptions{
		MissingCapabilities: []registry.Capability{missing},
	}).Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[registry.Capability]struct{}, len(newapi.ReadCapabilities))
	for _, c := range newapi.ReadCapabilities {
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

// testChannelBalanceNilIsNotZero 锁住渠道余额的可空语义。
//
// 断言分两层，因为只有第一层对任何实现都成立：
//
//  1. **类型必须是 *int64**（反射）。这是把 XM-0017 sub2api-real 的教训固化下来：
//     那边曾把「上游没给这个字段」和「上游给了 0」在解码时合流，前端只好靠猜。
//     有人把这个字段改回 int64「省掉一层指针」时，这条会当场变红。
//  2. **nil 的渠道不得在聚合指标里出现余额键**。真实实例上可能一条 nil 都没有，
//     所以这一层是条件断言：有 nil 才验，没有 nil 不算失败——
//     断言数据长什么样是测试上游，不是测试契约。
func testChannelBalanceNilIsNotZero(t *testing.T, newClient Factory) {
	field, ok := reflect.TypeOf(newapi.ChannelStatus{}).FieldByName("BalanceMinorUnits")
	if !ok {
		t.Fatal("ChannelStatus 缺少 BalanceMinorUnits 字段")
	}
	if field.Type.Kind() != reflect.Pointer || field.Type.Elem().Kind() != reflect.Int64 {
		t.Fatalf("ChannelStatus.BalanceMinorUnits 必须是 *int64（nil = 未配置，与 0 = 已耗尽区分开），"+
			"got %s", field.Type)
	}

	channels, err := newClient(newapi.FakeOptions{}).Channels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	obs := newapi.ToObservations(time.Now().UTC(), "contracttest", "test",
		newapi.UserStats{}, newapi.OrderSummary{}, channels, nil)

	var rows []any
	for _, o := range obs {
		if o.MetricKey != newapi.MetricChannelsStatus {
			continue
		}
		list, ok := o.Value["channels"].([]any)
		if !ok {
			t.Fatalf("%s 的 value.channels 不是数组: %T", o.MetricKey, o.Value["channels"])
		}
		rows = list
	}
	if len(rows) != len(channels) {
		t.Fatalf("聚合指标里的渠道条数 = %d，实际读到 %d", len(rows), len(channels))
	}

	for i, ch := range channels {
		row, ok := rows[i].(map[string]any)
		if !ok {
			t.Fatalf("渠道 #%d 的聚合行不是对象: %T", i, rows[i])
		}
		balance, present := row["balance_minor_units"]
		if ch.BalanceMinorUnits == nil {
			if present {
				t.Fatalf("渠道 %s 余额未配置，聚合指标里不该出现 balance_minor_units（got %v）——"+
					"写成 0 会被看板当成「余额已耗尽」", ch.ChannelID, balance)
			}
			continue
		}
		if !present {
			t.Fatalf("渠道 %s 有余额，聚合指标里却缺 balance_minor_units", ch.ChannelID)
		}
		if got, want := balance, *ch.BalanceMinorUnits; got != any(want) {
			t.Fatalf("渠道 %s 的余额 = %v, want %d", ch.ChannelID, got, want)
		}
	}
}

// testErrorRateIsIntegerPPM 锁住「比率与金额一样禁止浮点」这条纪律。
//
// 反射遍历全部契约结构体，命中任何 float32/float64 就失败——
// 这让纪律成为一条会在有人加字段时立刻变红的测试，而不是一段迟早没人看的注释
// （手法与 connectors/invoice 用反射挡 PII 字段同源）。
//
// 光靠「现在的字段都是 int64」不够：真正的失败模式是**将来**有人图省事加一个
// `ErrorRate float64` 或 `CostRate float64`，而那个改动本身编译得过、测试全绿。
func testErrorRateIsIntegerPPM(t *testing.T, newClient Factory) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(newapi.UserStats{}),
		reflect.TypeOf(newapi.OrderSummary{}),
		reflect.TypeOf(newapi.ChannelStatus{}),
		reflect.TypeOf(newapi.ModelUsage{}),
		reflect.TypeOf(newapi.Snapshot{}),
	} {
		assertNoFloatFields(t, typ)
	}

	channels, err := newClient(newapi.FakeOptions{}).Channels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range channels {
		// 0 ~ 1_000_000 是 ppm 的定义域（0% ~ 100%）。越界说明实现把单位搞混了：
		// 比如把 0.05 这个**分数**截断成 0，或者把百分比数直接当 ppm 塞进来。
		// 这条抓不住 5（本意 5%，正确值 50000）那种同号错位，但抓得住负数、
		// 抓得住 >100%，也抓得住把小数当整数写的那一类。
		if ch.ErrorRatePPM < 0 || ch.ErrorRatePPM > 1_000_000 {
			t.Fatalf("渠道 %s 的 ErrorRatePPM = %d，超出 ppm 定义域 [0, 1000000]——"+
				"单位大概率搞混了（1%% = 10000 ppm）", ch.ChannelID, ch.ErrorRatePPM)
		}
		if ch.LatencyMS < 0 {
			t.Fatalf("渠道 %s 的 LatencyMS = %d，延迟不能为负", ch.ChannelID, ch.LatencyMS)
		}
	}
}

func assertNoFloatFields(t *testing.T, typ reflect.Type) {
	t.Helper()
	for i := range typ.NumField() {
		field := typ.Field(i)
		kind := field.Type.Kind()
		// XM-CHAN-FIELDS0：可空数值字段一律是 *int64/*bool/*string/*time.Time
		// 之类的指针类型（区分"上游没给"与"上游给了 0"），检查必须透过指针看
		// 指向的类型，否则 `*float64` 会因为 Kind()==Ptr 而不是 Float64
		// 悄悄漏网——这正是本函数存在的理由要防的那类"改动本身编译得过、
		// 测试全绿"的疏漏。
		if kind == reflect.Ptr {
			kind = field.Type.Elem().Kind()
		}
		switch kind {
		case reflect.Float32, reflect.Float64:
			t.Fatalf("%s.%s 是浮点类型——金额与比率一律用整数表达"+
				"（金额：最小货币单位；比率：ppm）。规格 §5.9",
				typ.Name(), field.Name)
		case reflect.Struct:
			// 内嵌的 Snapshot 也要走一遍：漏掉内嵌就等于给它开了个后门
			if field.Anonymous {
				assertNoFloatFields(t, field.Type)
			}
		default:
		}
	}
}

// AssertChannelStatusCatalogInvariants 检查一条 v3 目录行（XM-CHAN-FIELDS0）
// 的跨字段一致性，与 RunSuite 的其余检查同一等级——**任何**实现（Fake 或
// 真实客户端）产出的 ChannelStatus 都必须满足。不接进 RunSuite 本身，理由
// 与 sub2api 的同名函数一致：RunSuite 的 Factory 只返回 newapi.ReadClient
// （v1 接口），不认识 ChannelDirectory；调用方（Fake 侧与真实客户端侧的
// client_contract_test.go）各自对着 Channels()/ChannelDirectory() 的结果
// 逐条调用本函数。
func AssertChannelStatusCatalogInvariants(t *testing.T, item newapi.ChannelStatus) {
	t.Helper()
	if item.TodayCostMinorUnits != nil && (item.TodayCurrency == nil || item.TodayScale == nil) {
		t.Fatalf("渠道 %s 给了 TodayCostMinorUnits 却缺 Currency/Scale：金额没有单位就是无意义的数字（规格 §5.9）",
			item.ChannelID)
	}
	if item.TodaySuccessRatePPM != nil && (*item.TodaySuccessRatePPM < 0 || *item.TodaySuccessRatePPM > 1_000_000) {
		t.Fatalf("渠道 %s 的 TodaySuccessRatePPM = %d，超出 ppm 定义域 [0, 1000000]",
			item.ChannelID, *item.TodaySuccessRatePPM)
	}
	if item.CapacityUsed != nil && item.CapacityLimit != nil && *item.CapacityUsed > *item.CapacityLimit {
		t.Logf("提醒：渠道 %s 的 CapacityUsed(%d) > CapacityLimit(%d)，确认不是字段对调",
			item.ChannelID, *item.CapacityUsed, *item.CapacityLimit)
	}
}
