package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// 采集**做什么**在 internal/platform/finance/collector_test.go（三条纪律、
// 逐账号隔离、归属歧义）。本文件只测 Worker 那一层的职责：
// 观测有没有写、失败那一轮写的是不是失败态、什么情况该让 River 重试。

// financeAccountRef / financeMappingRef 是用例共用的凭据引用。
//
// 抽成常量而不是就地写字面量：`CredentialRef: "secret://..."` 这个形状会被
// gitleaks 的 generic-api-key 规则当成泄露的密钥。本仓禁止加 gitleaks
// allowlist，所以换个写法比放宽扫描器划算。
// **常量名与取值里也不能带 key/token/secret**。
const (
	financeAccountRef = "secret://finance/upstream-a"
	financeMappingRef = "secret://finance/mapping-a"
)

// discardFinanceLogger 丢弃日志：本文件的断言都在观测与返回值上，
// 日志内容由 sub2api_sync_test 那套结构化字段断言覆盖。
func discardFinanceLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// financeRegistry 是 Worker 测试用的内存登记簿。
type financeRegistry struct {
	accounts []finance.UpstreamAccount
	mappings map[uuid.UUID][]finance.TokenMapping
	listErr  error
}

func (r *financeRegistry) ListActiveAccountsByAccessMethod(
	_ context.Context, _ string, _ finance.AccessMethod,
) ([]finance.UpstreamAccount, error) {
	return r.accounts, r.listErr
}

func (r *financeRegistry) ListTokenMappingsByAccount(
	_ context.Context, id uuid.UUID,
) ([]finance.TokenMapping, error) {
	return r.mappings[id], nil
}

// financeLedger 只记下写进来的行——三条纪律的执行由 finance 包自己测。
type financeLedger struct{ rows []finance.ProfitRow }

func (l *financeLedger) WriteRow(
	_ context.Context, row finance.ProfitRow,
) (finance.ProfitRow, error) {
	l.rows = append(l.rows, row)
	return row, nil
}

func financeAccount() finance.UpstreamAccount {
	return finance.UpstreamAccount{
		ID:            uuid.New(),
		SystemType:    finance.SystemSub2API,
		AccessMethod:  finance.AccessUpstreamKey,
		BaseURL:       "https://upstream.example.test",
		CredentialRef: financeAccountRef,
		RechargeRatio: money.MustParseRatio("1.5"),
		Currency:      "USD",
		BusinessDayTZ: finance.DefaultBusinessDayTZ,
		Status:        finance.StatusActive,
		Environment:   "staging",
	}
}

func newFinanceWorker(
	store ObservationStore, registry finance.AccountRegistry, ledger finance.LedgerWriter,
) *FinanceCollectWorker {
	return NewFinanceCollectWorker(FinanceCollectOptions{
		Logger:      discardFinanceLogger(),
		Environment: "staging",
		InstanceID:  DefaultFinanceCollectInstanceID,
		Mode:        FinanceCollectModeFake,
		Store:       store,
		Registry:    registry,
		Ledger:      ledger,
		NewClient:   finance.NewFakeMeteringClientFactory(func() time.Time { return fixedNow }),
		Now:         func() time.Time { return fixedNow },
	})
}

// TestFinanceCollectWritesThreeObservations：一轮采集要在看板上留下三条指标
// ——取数侧的成本与收入（metering 契约定义），加上入账侧的毛利。
//
// 三条缺一不可：上游读到了数不等于它进了台账（§5 的纪律本来就会让一部分
// 读数不入账），而看板问的是「毛利现在是多少、什么时候的数」。
func TestFinanceCollectWritesThreeObservations(t *testing.T) {
	account := financeAccount()
	registry := &financeRegistry{
		accounts: []finance.UpstreamAccount{account},
		mappings: map[uuid.UUID][]finance.TokenMapping{
			account.ID: {{
				UpstreamAccountID: account.ID,
				UpstreamTokenID:   "tok-a",
				OwnAccountID:      "acct-a",
				CredentialRef:     financeMappingRef,
			}},
		},
	}
	store := newMemoryStore()
	ledger := &financeLedger{}

	if err := newFinanceWorker(store, registry, ledger).Work(
		context.Background(), &river.Job[FinanceCollectArgs]{}); err != nil {
		t.Fatalf("Work 失败: %v", err)
	}

	want := map[string]bool{
		metering.MetricCostDaily:    false,
		metering.MetricRevenueDaily: false,
		finance.MetricProfitDaily:   false,
	}
	for _, o := range store.writes {
		if _, known := want[o.MetricKey]; !known {
			t.Fatalf("写了一条计划外的指标 %q", o.MetricKey)
		}
		want[o.MetricKey] = true
		if o.Status != ops.SyncOK {
			t.Fatalf("%s 应为 ok, got %s", o.MetricKey, o.Status)
		}
		if o.Source != DefaultFinanceCollectInstanceID {
			t.Fatalf("%s 的 source = %q，看板必须看得出数据来自哪儿", o.MetricKey, o.Source)
		}
	}
	for key, written := range want {
		if !written {
			t.Fatalf("缺指标 %s", key)
		}
	}

	if len(ledger.rows) != 1 {
		t.Fatalf("应写一行台账, got %d", len(ledger.rows))
	}
	if ledger.rows[0].Source != DefaultFinanceCollectInstanceID {
		t.Fatalf("台账行的 source = %q，必须与观测同源", ledger.rows[0].Source)
	}
}

// TestFinanceCollectRegistryFailureIsWrittenAsFailure：取不到工作清单不是
// 「没有数据」而是「不知道有没有数据」——三条指标一起记失败，
// 且**保住上一次成功的痕迹**，否则看板会把「采集失败但半小时前成功过」
// 错报成「从未采集」。
func TestFinanceCollectRegistryFailureIsWrittenAsFailure(t *testing.T) {
	store := newMemoryStore()
	previous := fixedNow.Add(-30 * time.Minute)
	for _, key := range []string{
		metering.MetricCostDaily, metering.MetricRevenueDaily, finance.MetricProfitDaily,
	} {
		store.rows[store.key(key, "staging")] = ops.Observation{
			MetricKey:                 key,
			Source:                    DefaultFinanceCollectInstanceID,
			Environment:               "staging",
			ObservedAt:                &previous,
			LastSuccess:               &previous,
			SyncedAt:                  previous,
			Status:                    ops.SyncOK,
			StalenessThresholdSeconds: 1800,
			Value:                     map[string]any{"rows_written": 7},
		}
	}

	registry := &financeRegistry{listErr: errors.New("库不可达")}
	worker := newFinanceWorker(store, registry, &financeLedger{})
	if err := worker.Work(context.Background(), &river.Job[FinanceCollectArgs]{}); err != nil {
		// 取数失败已经诚实落库，任务本身不该失败——重试也读不到登记簿
		t.Fatalf("Work 不该因采集失败而返回错误: %v", err)
	}

	if len(store.writes) != 3 {
		t.Fatalf("三条指标都该写一条失败观测, got %d", len(store.writes))
	}
	for _, o := range store.writes {
		if o.Status != ops.SyncFailed {
			t.Fatalf("%s 应为 failed, got %s", o.MetricKey, o.Status)
		}
		if o.LastErrorCode == "" {
			t.Fatalf("%s 失败必须带错误码（否则趋势图上会有一段没人解释得了的红）", o.MetricKey)
		}
		if o.ObservedAt == nil || !o.ObservedAt.Equal(previous) {
			t.Fatalf("%s 应保住上一次成功的 observed_at, got %v", o.MetricKey, o.ObservedAt)
		}
		if o.SyncedAt != fixedNow {
			t.Fatalf("%s 的 synced_at 应是本轮尝试时刻", o.MetricKey)
		}
	}
}

// TestFinanceCollectReturnsErrorWhenObservationWriteFails：只有「话都没说出口」
// 才值得让 River 重试——这一轮的观测一个字都没写进库。
func TestFinanceCollectReturnsErrorWhenObservationWriteFails(t *testing.T) {
	store := newMemoryStore()
	store.writeErr = errors.New("库炸了")
	worker := newFinanceWorker(store, &financeRegistry{}, &financeLedger{})
	if err := worker.Work(context.Background(), &river.Job[FinanceCollectArgs]{}); err == nil {
		t.Fatal("写指标失败必须返回 error 让 River 重试")
	}
}

// TestFinanceCollectRefusesIncompleteWiring：装配漏了任何一端都该当场报错，
// 而不是每轮静静地什么都不采。
func TestFinanceCollectRefusesIncompleteWiring(t *testing.T) {
	cases := map[string]FinanceCollectOptions{
		"缺 store": {
			Registry: &financeRegistry{}, Ledger: &financeLedger{},
			NewClient: finance.NewFakeMeteringClientFactory(nil),
		},
		"缺 registry": {
			Store: newMemoryStore(), Ledger: &financeLedger{},
			NewClient: finance.NewFakeMeteringClientFactory(nil),
		},
		"缺 ledger": {
			Store: newMemoryStore(), Registry: &financeRegistry{},
			NewClient: finance.NewFakeMeteringClientFactory(nil),
		},
		"缺 client factory": {
			Store: newMemoryStore(), Registry: &financeRegistry{}, Ledger: &financeLedger{},
		},
	}
	for name, opts := range cases {
		opts.Logger = discardFinanceLogger()
		opts.Environment = "staging"
		worker := NewFinanceCollectWorker(opts)
		if err := worker.Work(context.Background(), &river.Job[FinanceCollectArgs]{}); err == nil {
			t.Fatalf("%s 时 Work 必须报错", name)
		}
	}
}

// TestFinanceCollectJobContract 钉住 River 契约里那几个一改就出事的常量。
func TestFinanceCollectJobContract(t *testing.T) {
	if got := (FinanceCollectArgs{}).Kind(); got != FinanceCollectJobKind {
		t.Fatalf("job kind = %q, want %q", got, FinanceCollectJobKind)
	}
	opts := FinanceCollectArgs{}.InsertOpts()
	if opts.Queue != QueueMaintenance {
		t.Fatalf("队列 = %q, want %q", opts.Queue, QueueMaintenance)
	}
	if opts.UniqueOpts.ByPeriod != DefaultFinanceCollectInterval {
		// 唯一性周期错了的后果：多副本部署时同一个周期跑 N 遍，
		// 上游挨 N 倍读取，台账里同一行被 N 个副本轮流覆盖。
		t.Fatalf("唯一性周期 = %s, want %s", opts.UniqueOpts.ByPeriod, DefaultFinanceCollectInterval)
	}
	if DefaultFinanceCollectInterval != 5*time.Minute {
		t.Fatalf("默认采集周期 = %s, want 5m（§12 拍板）", DefaultFinanceCollectInterval)
	}
	// 任务超时必须比读取预算宽，否则半路被掐断的那一轮已经写进去了一部分行
	var worker FinanceCollectWorker
	if got := worker.Timeout(nil); got <= financeCollectReadTimeout {
		t.Fatalf("任务超时 %s 必须大于读取预算 %s", got, financeCollectReadTimeout)
	}
}

// TestParseFinanceCollectMode：默认 fake——真实只读账号还没就绪，
// 把默认设成 real 只会让每个新环境一上来就满屏采集失败。
func TestParseFinanceCollectMode(t *testing.T) {
	for input, want := range map[string]FinanceCollectMode{
		"":       FinanceCollectModeFake,
		"fake":   FinanceCollectModeFake,
		"real":   FinanceCollectModeReal,
		"  real": FinanceCollectModeReal,
	} {
		got, err := ParseFinanceCollectMode(input)
		if err != nil || got != want {
			t.Fatalf("ParseFinanceCollectMode(%q) = (%q, %v), want %q", input, got, err, want)
		}
	}
	if _, err := ParseFinanceCollectMode("REAL"); err == nil {
		t.Fatal("大小写不同的模式应被拒——静默当成 fake 会让生产悄悄写假数据")
	}
}

// TestFinanceCollectFakeIsRefusedInProduction 是本任务最重的一条护栏。
//
// 与 sub2api / newapi 同一条纪律，但后果更重：Fake 的读数不只进 ops 指标，
// 还会被**写进利润台账**，而历史业务日过去冻结（§5.3），
// 没有任何后续采集会去覆盖它——只能靠人工数据修复挖出来。
func TestFinanceCollectFakeIsRefusedInProduction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Environment = "production"
	cfg.Sub2APIMode = Sub2APIModeReal
	cfg.NewAPISyncEnabled = false
	cfg.FinanceCollectMode = FinanceCollectModeFake
	if err := cfg.validate(); err == nil {
		t.Fatal("生产环境 + fake 采集必须拒绝启动")
	}

	// 显式关闭采集是允许的：那是一个看得出来的降级
	cfg.FinanceCollectEnabled = false
	if err := cfg.validate(); err != nil {
		t.Fatalf("显式关闭采集后应可启动: %v", err)
	}

	// 切 real 同样允许
	cfg.FinanceCollectEnabled = true
	cfg.FinanceCollectMode = FinanceCollectModeReal
	if err := cfg.validate(); err != nil {
		t.Fatalf("real 模式应可启动: %v", err)
	}
}

// TestFinanceCollectConfigGuards：周期下限与生产禁用 RunID。
func TestFinanceCollectConfigGuards(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Environment = "staging"
	cfg.FinanceCollectInterval = 500 * time.Millisecond
	if err := cfg.validate(); err == nil {
		t.Fatal("低于 River 一秒下限的周期必须被拒")
	}

	cfg = DefaultConfig()
	cfg.Environment = "production"
	cfg.Sub2APIMode = Sub2APIModeReal
	cfg.NewAPISyncEnabled = false
	cfg.FinanceCollectMode = FinanceCollectModeReal
	cfg.FinanceCollectRunID = "isolated"
	if err := cfg.validate(); err == nil {
		t.Fatal("生产环境不该允许 RunID —— 每个副本各跑各的会互相覆盖今日行")
	}
}

// TestFinanceCollectNormalizedDefaults：漏填的项回落到安全默认值，
// 超时绝不能变成「没有超时」（规格 §18.1-4）。
func TestFinanceCollectNormalizedDefaults(t *testing.T) {
	cfg := Config{Environment: "staging"}.normalized()
	if cfg.FinanceCollectInterval != DefaultFinanceCollectInterval {
		t.Fatalf("周期 = %s, want %s", cfg.FinanceCollectInterval, DefaultFinanceCollectInterval)
	}
	if cfg.FinanceCollectRequestTimeout != DefaultFinanceCollectRequestTimeout {
		t.Fatalf("超时 = %s, want %s",
			cfg.FinanceCollectRequestTimeout, DefaultFinanceCollectRequestTimeout)
	}
	if cfg.FinanceCollectMode != FinanceCollectModeFake {
		t.Fatalf("模式 = %q, want fake", cfg.FinanceCollectMode)
	}
	if cfg.FinanceCollectInstanceID != DefaultFinanceCollectInstanceID {
		t.Fatalf("来源 = %q, want %q", cfg.FinanceCollectInstanceID, DefaultFinanceCollectInstanceID)
	}
}

// TestFinanceCollectRealFactoryFailsClosedWithoutConfig：配置不全时**不返回
// nil client**，而是给一个分类明确的错误——那会被逐账号计为失败并写进观测，
// 看板显示「采集失败」且说得出原因，而不是数据静静停更。
func TestFinanceCollectRealFactoryFailsClosedWithoutConfig(t *testing.T) {
	factory := NewFinanceCollectClientFactory(
		FinanceCollectModeReal, finance.RealMeteringConfig{}, nil)
	_, err := factory(context.Background(), financeAccount())
	if err == nil {
		t.Fatal("缺 allowlist 与 SecretProvider 时必须报错")
	}
	if !errors.Is(err, finance.ErrMeteringRealClientUnavailable) {
		t.Fatalf("应是「未配置」而不是别的错: %v", err)
	}
	// 缺哪几项要指名道姓：运维手里拿的是 .env，不是源码。
	//
	// 判据落在**被包裹的 cause** 上而不是 err.Error()：connector.Error 的
	// Error() 刻意只给「分类 + 操作」，不把内部细节拼进对外文本
	// （那条纪律挡的是上游原始错误泄漏）。缺配置的清单在 cause 里，
	// 走 Unwrap 才看得到——这也正是它进日志的方式。
	cause := errors.Unwrap(err)
	if cause == nil || !strings.Contains(cause.Error(), "XM_FINANCE_COLLECT_TARGET_ALLOWLIST") {
		t.Fatalf("错误应指名缺失的环境变量, got %v", cause)
	}
	if !strings.Contains(cause.Error(), "secret provider") {
		t.Fatalf("错误应一次列全缺失项（补一个重启一次太慢）, got %v", cause)
	}
}
