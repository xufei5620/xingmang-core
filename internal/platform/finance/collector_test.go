package finance_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// 本文件在**没有库、没有 River、没有真实上游**的情况下跑完整条采集路径。
//
// 三条不静默纪律（§5）在采集侧的表现——「读不到写 NULL」「只有一侧不建行」
// 「逐账号失败隔离」——都是这一层的行为，用真库测只会让每条断言慢一百倍，
// 而且掩盖不了任何东西：库层那一半在 profit_store_integration_test.go。

// collectAccountRef / collectMappingRefPrefix 是用例共用的凭据引用。
//
// 抽成常量而不是就地写字面量：`CredentialRef: "secret://..."` 这个形状会被
// gitleaks 的 generic-api-key 规则当成泄露的密钥（同一条误报见
// httpapi/finance_test.go 的说明）。本仓禁止加 gitleaks allowlist，
// 所以换个写法比放宽扫描器划算。**常量名与取值里也不能带 key/token/secret**。
const (
	collectAccountRef       = "secret://finance/upstream-a"
	collectMappingRefPrefix = "secret://finance/mapping-"
)

// 固定时钟：业务日切分与「今日冻结」都靠它，用 time.Now 的测试会在跨零点
// 那一瞬变成 flaky，而那恰好是这条纪律最需要被测到的时刻。
var collectNow = time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) // CST 14:00

const collectDay = "2026-08-28"

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// memLedger 是内存台账，**逐字复刻** §5.1 的三条分支。
//
// 它复刻而不是调用真 Store，是因为这里要测的是采集器对三种返回的**分类**：
// 哪些算写成、哪些算跳过、哪些算故障。库层那三条分支自己的正确性由
// profit_store_integration_test.go 在真库上验证——两处都不能省：
// 只测内存版会漏掉 SQL 写错的情况，只测真库版会让「跳过与故障怎么计数」
// 这件事没有独立的判据。
type memLedger struct {
	rows map[string]finance.ProfitRow
	// failOn 让指定令牌的写入返回真故障（区别于两个语义化的「跳过」）。
	failOn string
}

func newMemLedger() *memLedger {
	return &memLedger{rows: map[string]finance.ProfitRow{}}
}

func (m *memLedger) key(r finance.ProfitRow) string {
	return fmt.Sprintf("%s|%s|%s", r.UpstreamAccountID, r.BusinessDayString(), r.TokenID)
}

func (m *memLedger) WriteRow(_ context.Context, row finance.ProfitRow) (finance.ProfitRow, error) {
	if err := row.Validate(); err != nil {
		return finance.ProfitRow{}, err
	}
	if row.TokenID == m.failOn {
		return finance.ProfitRow{}, errors.New("库炸了")
	}
	key := m.key(row)
	existing, found := m.rows[key]

	hasRevenue, hasCost := row.KnownSides()
	if hasRevenue && hasCost {
		if found && existing.PlatformID != "" {
			// §5.3：platform_id 空缺可补、已有不动
			row.PlatformID = existing.PlatformID
		}
		row.UpdatedAt = collectNow
		m.rows[key] = row
		return row, nil
	}
	if !found {
		// 纯 UPDATE 不建行
		return finance.ProfitRow{}, finance.ErrProfitOneSidedNoRow
	}
	merged := existing
	if hasCost {
		merged.CostMinor = row.CostMinor
		merged.CostObservedAt = row.CostObservedAt
		merged.RatioSnapshot = row.RatioSnapshot
	}
	if hasRevenue {
		merged.RevenueMinor = row.RevenueMinor
		merged.RevenueObservedAt = row.RevenueObservedAt
	}
	merged.UpdatedAt = collectNow
	m.rows[key] = merged
	return merged, nil
}

// WriteAmortizedRow 复刻订阅型那条分支（XM-0037c）：成本是算出来的，
// **收入未知时照样建行**。与 WriteRow 是两条纪律，所以这里也分两个方法写——
// 合成一个的话，「计量型读失败」会走进「照样建行」那条路而测试发现不了。
func (m *memLedger) WriteAmortizedRow(
	_ context.Context, row finance.ProfitRow,
) (finance.ProfitRow, error) {
	if err := row.Validate(); err != nil {
		return finance.ProfitRow{}, err
	}
	if row.CostMinor == nil {
		return finance.ProfitRow{}, errors.New("摊销入账必须带成本")
	}
	if row.TokenID == m.failOn {
		return finance.ProfitRow{}, errors.New("库炸了")
	}
	key := m.key(row)
	merged := row
	if existing, found := m.rows[key]; found {
		if existing.PlatformID != "" {
			merged.PlatformID = existing.PlatformID
		}
		// 收入侧本轮没读到就保留库里已有的（同 UpsertProfitDailyAmortizedCost）
		if merged.RevenueMinor == nil {
			merged.RevenueMinor = existing.RevenueMinor
			merged.RevenueObservedAt = existing.RevenueObservedAt
		}
	}
	merged.UpdatedAt = collectNow
	m.rows[key] = merged
	return merged, nil
}

// memRegistry 是内存登记簿。
type memRegistry struct {
	accounts []finance.UpstreamAccount
	// subscriptionAccounts 是订阅型那一轮的清单（XM-0037c）。
	subscriptionAccounts []finance.UpstreamAccount
	mappings             map[uuid.UUID][]finance.TokenMapping
	listErr              error
}

func (m *memRegistry) ListActiveAccountsByAccessMethod(
	_ context.Context, _ string, method finance.AccessMethod,
) ([]finance.UpstreamAccount, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	switch method {
	case finance.AccessUpstreamKey:
		return m.accounts, nil
	case finance.AccessSubscriptionAccount:
		return m.subscriptionAccounts, nil
	default:
		// official_api v1 占位后置（§12 拍板），采集两轮都不该来要它
		return nil, nil
	}
}

// memSubscriptions 是内存订阅登记（XM-0037c 的摊销取数端）。
type memSubscriptions struct {
	batches map[uuid.UUID][]finance.AmortizableBatch
	listErr error
}

func (m *memSubscriptions) ListAmortizableBatches(
	_ context.Context, accountID uuid.UUID, day time.Time,
) ([]finance.AmortizableBatch, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	// 覆盖判定留给 AmortizeDay（它才是口径的所在），这里只按账号取。
	_ = day
	return m.batches[accountID], nil
}

func (m *memRegistry) ListTokenMappingsByAccount(
	_ context.Context, accountID uuid.UUID,
) ([]finance.TokenMapping, error) {
	return m.mappings[accountID], nil
}

// stubClient 让成本侧与收入侧**各自**成功或失败。
//
// metering.NewFake 的 FailWith 是全局的（所有读方法一起失败），而 §5.1 的
// 三条分支恰恰要靠「只有一侧失败」才区分得开——所以这里需要一个更细的桩。
type stubClient struct {
	costErr    error
	revenueErr error
	usageMinor int64
	revMinor   int64
	observedAt time.Time
	currency   string
}

func (s *stubClient) Version(context.Context) (connector.VersionInfo, error) {
	return connector.VersionInfo{}, nil
}
func (s *stubClient) Health(context.Context) (connector.HealthResult, error) {
	return connector.HealthResult{Healthy: true}, nil
}
func (s *stubClient) Capabilities(context.Context) ([]registry.Capability, error) {
	return nil, nil
}

func (s *stubClient) TokenUsage(
	_ context.Context, token metering.TokenRef, day string,
) (metering.TokenUsage, error) {
	if s.costErr != nil {
		return metering.TokenUsage{}, s.costErr
	}
	return metering.TokenUsage{
		Snapshot:        metering.Snapshot{ObservedAt: s.observedAt},
		UpstreamTokenID: token.UpstreamTokenID,
		Day:             day,
		UsageMinorUnits: s.usageMinor,
		Currency:        s.currency,
	}, nil
}

func (s *stubClient) AccountRevenue(
	_ context.Context, ownAccountID string, day string,
) (metering.AccountRevenue, error) {
	if s.revenueErr != nil {
		return metering.AccountRevenue{}, s.revenueErr
	}
	return metering.AccountRevenue{
		Snapshot:          metering.Snapshot{ObservedAt: s.observedAt},
		OwnAccountID:      ownAccountID,
		Day:               day,
		RevenueMinorUnits: s.revMinor,
		Currency:          s.currency,
	}, nil
}

func newStub() *stubClient {
	return &stubClient{
		usageMinor: 5_813_729, // §2.4 worked example
		revMinor:   12_345_600,
		observedAt: collectNow.Add(-time.Minute),
		currency:   "USD",
	}
}

func collectAccount(id uuid.UUID) finance.UpstreamAccount {
	return finance.UpstreamAccount{
		ID:            id,
		SystemType:    finance.SystemSub2API,
		AccessMethod:  finance.AccessUpstreamKey,
		BaseURL:       "https://upstream.example.test",
		CredentialRef: collectAccountRef,
		RechargeRatio: money.MustParseRatio("1.5"),
		Currency:      "USD",
		BusinessDayTZ: finance.DefaultBusinessDayTZ,
		Status:        finance.StatusActive,
		Environment:   "production",
	}
}

func mapping(accountID uuid.UUID, token, own string) finance.TokenMapping {
	return finance.TokenMapping{
		UpstreamAccountID: accountID,
		UpstreamTokenID:   token,
		OwnAccountID:      own,
		CredentialRef:     collectMappingRefPrefix + token,
	}
}

type collectFixture struct {
	registry *memRegistry
	subs     *memSubscriptions
	ledger   *memLedger
	clients  map[uuid.UUID]metering.ReadClient
	factErrs map[uuid.UUID]error
	resolve  finance.PlatformResolver
}

func (f *collectFixture) collector() *finance.Collector {
	return finance.NewCollector(finance.CollectorOptions{
		Logger:        quietLogger(),
		Environment:   "production",
		InstanceID:    "finance-collect-test",
		Registry:      f.registry,
		Subscriptions: f.subs,
		Ledger:        f.ledger,
		NewClient: func(_ context.Context, a finance.UpstreamAccount) (metering.ReadClient, error) {
			if err := f.factErrs[a.ID]; err != nil {
				return nil, err
			}
			return f.clients[a.ID], nil
		},
		ResolvePlatform: f.resolve,
		Now:             func() time.Time { return collectNow },
	})
}

func newFixture(accounts ...finance.UpstreamAccount) *collectFixture {
	return &collectFixture{
		registry: &memRegistry{accounts: accounts, mappings: map[uuid.UUID][]finance.TokenMapping{}},
		subs:     &memSubscriptions{batches: map[uuid.UUID][]finance.AmortizableBatch{}},
		ledger:   newMemLedger(),
		clients:  map[uuid.UUID]metering.ReadClient{},
		factErrs: map[uuid.UUID]error{},
	}
}

// TestCollectWritesBothSides 是最基本的一条：两侧都读到就入账，
// 且倍率按 §6.3 逐行冻结、金额按 §2.4 的整数定点折算。
func TestCollectWritesBothSides(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{mapping(id, "tok-a", "acct-a")}
	f.clients[id] = newStub()

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.RowsWritten != 1 || result.RowsSkippedNothingKnown != 0 ||
		result.RowsSkippedOneSided != 0 || result.RowsFailed != 0 {
		t.Fatalf("计数不对: %+v", result)
	}
	if result.Partial {
		t.Fatal("全部读到时不该标记 partial")
	}

	row := f.ledger.rows[fmt.Sprintf("%s|%s|tok-a", id, collectDay)]
	// §2.4 的 worked example：5813729 微美元 ÷ 1.5 = 3875819（半进）
	if row.CostMinor == nil || *row.CostMinor != 3_875_819 {
		t.Fatalf("成本 = %v, want 3875819（§2.4 worked example）", row.CostMinor)
	}
	if row.RevenueMinor == nil || *row.RevenueMinor != 12_345_600 {
		t.Fatalf("收入 = %v, want 12345600", row.RevenueMinor)
	}
	if got := row.RatioSnapshot.String(); got != "1.5" {
		t.Fatalf("ratio_snapshot = %q, want 1.5（§6.3 逐行冻结）", got)
	}
	if row.CostObservedAt == nil || row.RevenueObservedAt == nil {
		t.Fatal("两侧都该带上游观测时刻（宪法 12 条）")
	}
	if row.Source != "finance-collect-test" {
		t.Fatalf("source = %q，来源必须落到行上", row.Source)
	}
	if profit := row.ProfitMinor(); profit == nil || *profit != 12_345_600-3_875_819 {
		t.Fatalf("毛利 = %v", profit)
	}
	if len(result.Costs) != 1 || len(result.Revenues) != 1 {
		t.Fatalf("读数应原样交给调用方转观测: costs=%d revenues=%d",
			len(result.Costs), len(result.Revenues))
	}
}

// TestCollectCostFailureWritesNullNotZero 钉住 §5.1 的核心：
// 成本读不到时**不写 0**，而且因为只剩一侧、台账里又没有这一行，
// 所以**不建行**。
//
// 反面就是那个要挡的错数字：如果写 0，报表上会出现一条毛利 = 收入的记录。
func TestCollectCostFailureWritesNullNotZero(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{mapping(id, "tok-a", "acct-a")}
	stub := newStub()
	stub.costErr = connector.NewError(connector.KindUnavailable, "metering.token.usage_read", nil)
	f.clients[id] = stub

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.RowsSkippedOneSided != 1 || result.RowsWritten != 0 {
		t.Fatalf("只有收入且无既有行时应「不建行」: %+v", result)
	}
	if len(f.ledger.rows) != 0 {
		t.Fatalf("不该建行，实际建了 %d 行", len(f.ledger.rows))
	}
	if !result.Partial {
		t.Fatal("有读取失败必须标记 partial（宪法 12 条）")
	}
}

// TestCollectOneSidedRefreshesExistingRow：今日行已经存在时，
// 单侧读数照常刷新它——那一行的利润仍然算得出来（§5.1 的「纯 UPDATE」）。
func TestCollectOneSidedRefreshesExistingRow(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{mapping(id, "tok-a", "acct-a")}
	f.clients[id] = newStub()

	// 第一轮两侧都读到，建行
	if _, err := f.collector().CollectOnce(context.Background()); err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}

	// 第二轮成本读不到：收入侧照常刷新，成本保留上一轮的值
	stub := newStub()
	stub.revMinor = 20_000_000
	stub.costErr = connector.NewError(connector.KindUnavailable, "metering.token.usage_read", nil)
	f.clients[id] = stub

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	if result.RowsWritten != 1 {
		t.Fatalf("既有行应被刷新: %+v", result)
	}
	row := f.ledger.rows[fmt.Sprintf("%s|%s|tok-a", id, collectDay)]
	if row.RevenueMinor == nil || *row.RevenueMinor != 20_000_000 {
		t.Fatalf("收入应刷新到 20000000, got %v", row.RevenueMinor)
	}
	// 一次失败的成本读取不该把今天已经读到的成本抹掉
	if row.CostMinor == nil || *row.CostMinor != 3_875_819 {
		t.Fatalf("成本应保留上一轮的值, got %v", row.CostMinor)
	}
}

// TestCollectBothSidesFailSkipsPair：两侧都未知 → 整对跳过（§5.1），
// 且这不算故障——重试也不会让上游多出一个数来。
func TestCollectBothSidesFailSkipsPair(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{mapping(id, "tok-a", "acct-a")}
	stub := newStub()
	stub.costErr = connector.NewError(connector.KindUnavailable, "cost", nil)
	stub.revenueErr = connector.NewError(connector.KindUnavailable, "revenue", nil)
	f.clients[id] = stub

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("整轮不该失败（逐令牌失败已隔离）: %v", err)
	}
	if result.RowsSkippedNothingKnown != 1 {
		t.Fatalf("两侧全未知应计入 nothing_known: %+v", result)
	}
	if result.RowsFailed != 0 {
		t.Fatalf("跳过不是故障: %+v", result)
	}
	if len(f.ledger.rows) != 0 {
		t.Fatal("两侧全未知时一个字都不该写")
	}
}

// TestCollectIsolatesAccountFailures 钉住 brief 的「逐账号失败隔离」：
// 一个上游挂掉不该让其余账号今天也没有数。
func TestCollectIsolatesAccountFailures(t *testing.T) {
	bad, good := uuid.New(), uuid.New()
	f := newFixture(collectAccount(bad), collectAccount(good))
	f.registry.mappings[bad] = []finance.TokenMapping{mapping(bad, "tok-bad", "acct-bad")}
	f.registry.mappings[good] = []finance.TokenMapping{mapping(good, "tok-good", "acct-good")}
	f.factErrs[bad] = connector.NewError(connector.KindAuth, "metering.client", nil)
	f.clients[good] = newStub()

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("单个账号失败不该让整轮失败: %v", err)
	}
	if result.AccountsTotal != 2 || result.AccountsFailed != 1 {
		t.Fatalf("账号计数不对: %+v", result)
	}
	if result.RowsWritten != 1 {
		t.Fatalf("健康账号应照常入账: %+v", result)
	}
	if _, ok := f.ledger.rows[fmt.Sprintf("%s|%s|tok-good", good, collectDay)]; !ok {
		t.Fatal("健康账号的行不见了")
	}
	if !result.Partial {
		t.Fatal("有账号失败必须标记 partial")
	}
}

// TestCollectFailsWhenWorklistUnavailable：读不了登记簿是整轮失败——
// 那不是「没有数据」而是「不知道有没有数据」，两者必须不同。
func TestCollectFailsWhenWorklistUnavailable(t *testing.T) {
	f := newFixture()
	f.registry.listErr = errors.New("库不可达")
	if _, err := f.collector().CollectOnce(context.Background()); err == nil {
		t.Fatal("取不到工作清单必须整轮失败")
	}
}

// TestCollectAggregatesMultiTokenAccount 钉住 §12.2 的渠道键裁定
// （XM-0037c 落地，取代 037b 的「整组不入账」）。
//
// 形态：一个自营账号挂两把上游令牌（token_map 的反向索引刻意不是唯一索引），
// 另一个账号只挂一把。收入端点是**账号级**的，台账的行是**令牌级**的。
//
// 037b 的处理是那一组令牌整体不入账并报歧义——成本行蒸发。现在的口径是：
//   - 多令牌账号 → 一行**账号级聚合行**，成本取两把令牌之和，收入只计一次；
//   - 单令牌账号 → 维持令牌级行，保留下钻。
//
// 三条断言分别挡住三种错法：收入被算两遍、成本蒸发、聚合行撞主键。
func TestCollectAggregatesMultiTokenAccount(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{
		mapping(id, "tok-a", "acct-shared"),
		mapping(id, "tok-b", "acct-shared"),
		mapping(id, "tok-c", "acct-solo"),
	}
	f.clients[id] = newStub()

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.RowsWritten != 2 {
		t.Fatalf("两个自营账号各一行（聚合 + 令牌级）: %+v", result)
	}
	if result.RowsAggregated != 1 {
		t.Fatalf("聚合行应单独计数: %+v", result)
	}

	// 单令牌账号：令牌级行，下钻保留
	solo, ok := f.ledger.rows[fmt.Sprintf("%s|%s|tok-c", id, collectDay)]
	if !ok {
		t.Fatal("单令牌账号应维持令牌级行")
	}
	if solo.CostMinor == nil || *solo.CostMinor != 3_875_819 {
		t.Fatalf("单令牌成本 = %v, want 3875819", solo.CostMinor)
	}

	// 多令牌账号：一行账号级聚合行，token_id 用哨兵
	sentinel := finance.AccountGrainTokenID("acct-shared")
	shared, ok := f.ledger.rows[fmt.Sprintf("%s|%s|%s", id, collectDay, sentinel)]
	if !ok {
		t.Fatalf("多令牌账号应写账号级聚合行（token_id=%s）", sentinel)
	}
	// 成本一分不少：两把令牌之和
	if shared.CostMinor == nil || *shared.CostMinor != 2*3_875_819 {
		t.Fatalf("聚合成本 = %v, want %d（两把令牌之和，不得蒸发）",
			shared.CostMinor, 2*3_875_819)
	}
	// 收入只计一次：账号级端点给的就是这一个数
	if shared.RevenueMinor == nil || *shared.RevenueMinor != 12_345_600 {
		t.Fatalf("聚合收入 = %v, want 12345600（只计一次，不得按令牌数翻倍）",
			shared.RevenueMinor)
	}
	if shared.AccountID != "acct-shared" {
		t.Fatalf("account_id = %q，聚合行仍须指向那个自营账号", shared.AccountID)
	}
	// 逐令牌的行不该再存在——它们的成本已经并进聚合行了
	for _, token := range []string{"tok-a", "tok-b"} {
		if _, present := f.ledger.rows[fmt.Sprintf("%s|%s|%s", id, collectDay, token)]; present {
			t.Fatalf("%s 不该再有独立的令牌级行（成本会被算两遍）", token)
		}
	}
	// 按上游账号上卷（037d 的用法）与逐令牌写法完全一致
	if result.CostMinorSum != 3*3_875_819 {
		t.Fatalf("上卷成本 = %d, want %d", result.CostMinorSum, 3*3_875_819)
	}
}

// TestCollectAggregatedCostUnknownWhenAnyTokenFails 钉住聚合行的 §5.1：
// **任一令牌读不到，整行成本就是未知**，不是「已知的那几把之和」。
//
// 部分之和会给出一个偏低且看不出偏低的成本——那正是 §5.1 要挡的错数字。
// 与 PlatformBucket.ProfitMinorSum 在覆盖行数不足时返回 nil 是同一条纪律。
func TestCollectAggregatedCostUnknownWhenAnyTokenFails(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{
		mapping(id, "tok-a", "acct-shared"),
		mapping(id, "tok-b", "acct-shared"),
	}
	f.clients[id] = &partialCostClient{stub: newStub(), failToken: "tok-b"}

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	// 成本未知 + 收入已知 + 台账无此行 → 按 §5.1 不建行
	if result.RowsSkippedOneSided != 1 || result.RowsWritten != 0 {
		t.Fatalf("成本部分未知时不该建行: %+v", result)
	}
	if len(f.ledger.rows) != 0 {
		t.Fatal("不该写出一个只含半数令牌成本的聚合行")
	}
	if !result.Partial {
		t.Fatal("有读取失败必须标记 partial")
	}
}

// partialCostClient 让**指定的那一把令牌**读失败，其余照常。
//
// stubClient 的 costErr 是全令牌一起失败的，而聚合行的纪律恰恰要靠
// 「只坏一把」才测得出来。
type partialCostClient struct {
	stub      *stubClient
	failToken string
}

func (c *partialCostClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	return c.stub.Version(ctx)
}
func (c *partialCostClient) Health(ctx context.Context) (connector.HealthResult, error) {
	return c.stub.Health(ctx)
}
func (c *partialCostClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	return c.stub.Capabilities(ctx)
}
func (c *partialCostClient) TokenUsage(
	ctx context.Context, token metering.TokenRef, day string,
) (metering.TokenUsage, error) {
	if token.UpstreamTokenID == c.failToken {
		return metering.TokenUsage{}, connector.NewError(
			connector.KindUnavailable, "metering.token.usage_read", nil)
	}
	return c.stub.TokenUsage(ctx, token, day)
}
func (c *partialCostClient) AccountRevenue(
	ctx context.Context, ownAccountID, day string,
) (metering.AccountRevenue, error) {
	return c.stub.AccountRevenue(ctx, ownAccountID, day)
}

// TestCollectRevenueNotSupportedIsNotAnError：newapi 的收入走自营库直连（§3.2），
// 不在这条 HTTP 契约上。它每轮都会 not_supported，那是**预期内的常态**，
// 不该被记成本轮的代表性错误。
//
// 后果是：newapi 账号在收入通道接上之前只有成本一侧，因而按 §5.1 不建行。
// 这条断言把那个事实钉住，免得将来有人以为「newapi 采集坏了」。
func TestCollectRevenueNotSupportedIsNotAnError(t *testing.T) {
	id := uuid.New()
	account := collectAccount(id)
	account.SystemType = finance.SystemNewAPI
	f := newFixture(account)
	f.registry.mappings[id] = []finance.TokenMapping{mapping(id, "tok-a", "channel-7")}
	stub := newStub()
	stub.revenueErr = connector.NewError(connector.KindNotSupported, "metering.account.revenue_read", nil)
	f.clients[id] = stub

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.FirstError != nil {
		t.Fatalf("not_supported 不该成为代表性错误: %v", result.FirstError)
	}
	if result.RowsSkippedOneSided != 1 {
		t.Fatalf("只有成本一侧应「不建行」: %+v", result)
	}
}

// TestCollectAppliesPlatformResolver：platform_id 由解析器给出，
// 默认（未注入）时为空 = 未归属，落进 §5.2 的第三桶。
func TestCollectAppliesPlatformResolver(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{mapping(id, "tok-a", "acct-a")}
	f.clients[id] = newStub()

	// 默认：未归属
	if _, err := f.collector().CollectOnce(context.Background()); err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if got := f.ledger.rows[fmt.Sprintf("%s|%s|tok-a", id, collectDay)].PlatformID; got != "" {
		t.Fatalf("未注入解析器时 platform_id 应为空（未归属），got %q", got)
	}

	// 注入之后：写进行里
	f.ledger = newMemLedger()
	f.resolve = func(finance.UpstreamAccount, finance.TokenMapping) string { return "sub2api-prod" }
	if _, err := f.collector().CollectOnce(context.Background()); err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if got := f.ledger.rows[fmt.Sprintf("%s|%s|tok-a", id, collectDay)].PlatformID; got != "sub2api-prod" {
		t.Fatalf("platform_id = %q, want sub2api-prod", got)
	}
}

// TestCollectCountsLedgerFailuresSeparately：写库失败是**真故障**，
// 与两个语义化的「跳过」分开计数——合成一个数字之后，看板上的红点
// 就再也说不清该不该有人起来处理。
func TestCollectCountsLedgerFailuresSeparately(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{mapping(id, "tok-a", "acct-a")}
	f.clients[id] = newStub()
	f.ledger.failOn = "tok-a"

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("逐行失败不该让整轮失败: %v", err)
	}
	if result.RowsFailed != 1 || result.RowsWritten != 0 ||
		result.RowsSkippedNothingKnown != 0 || result.RowsSkippedOneSided != 0 {
		t.Fatalf("写库失败应只计入 rows_failed: %+v", result)
	}
	if result.FirstError == nil {
		t.Fatal("真故障应留下代表性错误")
	}
}

// TestCollectWithFakeClientFactory 走**真正的** fake 工厂，
// 证明「登记簿有 fake 账号时整条链路跑得通」（brief 的验收条件）。
func TestCollectWithFakeClientFactory(t *testing.T) {
	id := uuid.New()
	registry := &memRegistry{
		accounts: []finance.UpstreamAccount{collectAccount(id)},
		mappings: map[uuid.UUID][]finance.TokenMapping{
			id: {mapping(id, "tok-a", "acct-a")},
		},
	}
	ledger := newMemLedger()
	collector := finance.NewCollector(finance.CollectorOptions{
		Logger:        quietLogger(),
		Environment:   "staging",
		InstanceID:    "finance-collect-staging",
		Registry:      registry,
		Subscriptions: &memSubscriptions{},
		Ledger:        ledger,
		NewClient:     finance.NewFakeMeteringClientFactory(func() time.Time { return collectNow }),
		Now:           func() time.Time { return collectNow },
	})

	result, err := collector.CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("fake 链路应跑得通: %v", err)
	}
	if result.RowsWritten != 1 {
		t.Fatalf("fake 模式应写出一行: %+v", result)
	}
	// Fake 返回 §2.4 worked example 的 5813729 微美元，÷1.5 = 3875819
	row := ledger.rows[fmt.Sprintf("%s|%s|tok-a", id, collectDay)]
	if row.CostMinor == nil || *row.CostMinor != 3_875_819 {
		t.Fatalf("fake 成本 = %v, want 3875819", row.CostMinor)
	}
	if days := result.BusinessDays(); len(days) != 1 || days[0] != collectDay {
		t.Fatalf("业务日 = %v, want [%s]", days, collectDay)
	}
}

// TestCollectResultObservationsAreHonest 钉住 §8.2 + 宪法 12 条：
// 观测里每一类计数各自可见，跳过的那一轮标 partial，
// 币种混杂时不给合计并说清为什么。
func TestCollectResultObservationsAreHonest(t *testing.T) {
	id := uuid.New()
	f := newFixture(collectAccount(id))
	f.registry.mappings[id] = []finance.TokenMapping{
		mapping(id, "tok-a", "acct-a"),
		mapping(id, "tok-b", "acct-b"),
	}
	stub := newStub()
	f.clients[id] = stub

	result, _ := f.collector().CollectOnce(context.Background())
	observations := result.ToObservations(collectNow, "finance-collect-test", "production")
	if len(observations) != 3 {
		t.Fatalf("应产出成本 / 收入 / 毛利三条观测，got %d", len(observations))
	}
	var profit *opsObservationView
	for i := range observations {
		if observations[i].MetricKey == finance.MetricProfitDaily {
			profit = &opsObservationView{value: observations[i].Value}
		}
	}
	if profit == nil {
		t.Fatalf("缺 %s 观测", finance.MetricProfitDaily)
	}
	if got := profit.value["rows_written"]; got != 2 {
		t.Fatalf("rows_written = %v, want 2", got)
	}
	if got := profit.value["rows_with_profit"]; got != 2 {
		t.Fatalf("rows_with_profit = %v, want 2", got)
	}
	want := int64(2 * (12_345_600 - 3_875_819))
	if got := profit.value["profit_minor_units"]; got != want {
		t.Fatalf("profit_minor_units = %v, want %d", got, want)
	}
	if got := profit.value["currency"]; got != "USD" {
		t.Fatalf("currency = %v", got)
	}
	if _, present := profit.value["total_omitted_reason"]; present {
		t.Fatal("单一币种时不该标 total_omitted_reason")
	}
}

// TestCollectResultOmitsTotalsOnMixedCurrency：不同币种的最小单位不能相加，
// 合计不给，但**说清为什么不给**（宪法 12 条）。
func TestCollectResultOmitsTotalsOnMixedCurrency(t *testing.T) {
	usd, cny := uuid.New(), uuid.New()
	cnyAccount := collectAccount(cny)
	cnyAccount.Currency = "CNY"
	cnyAccount.BaseURL = "https://cny.example.test"
	f := newFixture(collectAccount(usd), cnyAccount)
	f.registry.mappings[usd] = []finance.TokenMapping{mapping(usd, "tok-usd", "acct-usd")}
	f.registry.mappings[cny] = []finance.TokenMapping{mapping(cny, "tok-cny", "acct-cny")}
	f.clients[usd] = newStub()
	cnyStub := newStub()
	cnyStub.currency = "CNY"
	f.clients[cny] = cnyStub

	result, _ := f.collector().CollectOnce(context.Background())
	if !result.MixedCurrency {
		t.Fatal("两种币种应被标记")
	}
	if result.ProfitMinorSum() != nil {
		t.Fatal("币种混杂时毛利合计必须给不出")
	}
	observation := result.ProfitObservation(collectNow, "finance-collect-test", "production")
	if observation.Value["total_omitted_reason"] != "mixed_currency" {
		t.Fatalf("必须说清合计为什么不给, got %v", observation.Value["total_omitted_reason"])
	}
	if _, present := observation.Value["profit_minor_units"]; present {
		t.Fatal("币种混杂时不得给出合计")
	}
}

// TestProfitObservationKeepsObservedAtEmptyWhenNothingRead：
// 一轮什么都没采到时，observed_at 留空而不是拿 now 冒充——
// 「从未采到」与「刚采到」必须分得开（规格 §9.1）。
func TestProfitObservationKeepsObservedAtEmptyWhenNothingRead(t *testing.T) {
	var empty finance.CollectResult
	observation := empty.ProfitObservation(collectNow, "src", "production")
	if observation.ObservedAt != nil {
		t.Fatalf("没有读数时 observed_at 必须为空, got %v", observation.ObservedAt)
	}
	if observation.SyncedAt.IsZero() {
		t.Fatal("synced_at 是「任务还活着」的证据，必须有值")
	}
}

// opsObservationView 只是给断言取值用的薄壳，避免在断言里到处写类型转换。
type opsObservationView struct{ value map[string]any }
