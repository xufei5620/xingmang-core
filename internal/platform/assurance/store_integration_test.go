package assurance_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// assurancePool connects to the real test database (XM_TEST_DATABASE_URL)
// and clears the tables this package's tests own or write to. Only
// assurance.* is blanket-cleared (this package's own tables, zero
// cross-package risk); core.connector_config is cleared with the same
// narrow (platform, environment) filter internal/platform/jobs's own
// integration tests use, and the two ops observation keys this package
// reads are deleted by exact metric_key + environment rather than
// truncating the whole shared ops.metric_observation table.
func assurancePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 测试库: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "TRUNCATE assurance.probe_result, assurance.probe_run, assurance.probe_declaration"); err != nil {
		t.Fatalf("清空 assurance 表: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM core.connector_config WHERE platform IN ('sub2api','newapi') AND environment = 'staging'`); err != nil {
		t.Fatalf("清空 connector_config: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM ops.metric_observation WHERE metric_key IN ('sub2api.channels.status','newapi.channels.status') AND environment = 'staging'`); err != nil {
		t.Fatalf("清空渠道目录观测: %v", err)
	}
	return pool
}

// fakeJobEnqueuer records every InsertTx call instead of touching a real
// river_job table (this test DB never runs River's own migrator) — Store's
// enqueue path only needs *something* satisfying assurance.JobEnqueuer, and
// this proves the transactional insert+enqueue coupling without requiring
// River's schema in the test database.
type fakeJobEnqueuer struct {
	calls []assurance.ProbeArgs
	err   error
}

func (f *fakeJobEnqueuer) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	probeArgs, ok := args.(assurance.ProbeArgs)
	if !ok {
		return nil, fmt.Errorf("unexpected job args type %T", args)
	}
	f.calls = append(f.calls, probeArgs)
	return &rivertype.JobInsertResult{}, nil
}

func seedChannelCatalog(t *testing.T, pool *pgxpool.Pool, platform string, channelIDs ...string) {
	t.Helper()
	channels := make([]any, 0, len(channelIDs))
	for _, id := range channelIDs {
		channels = append(channels, map[string]any{"channel_id": id, "channel_name": "渠道 " + id})
	}
	now := time.Now().UTC()
	_, err := ops.NewStore(pool).UpsertWithSample(context.Background(), ops.Observation{
		MetricKey: platform + ".channels.status", Source: platform + "-test", Environment: "staging",
		SyncedAt: now, ObservedAt: &now, LastSuccess: &now, Status: ops.SyncOK,
		StalenessThresholdSeconds: 1800, Value: map[string]any{"channels": channels},
	})
	if err != nil {
		t.Fatalf("seed channel catalog: %v", err)
	}
}

func newTestStore(t *testing.T, pool *pgxpool.Pool, enq assurance.JobEnqueuer, opts ...assurance.Option) *assurance.Store {
	t.Helper()
	store, err := assurance.NewStore(pool, enq, ops.NewStore(pool), opts...)
	if err != nil {
		t.Fatalf("NewStore() = %v, want nil", err)
	}
	return store
}

func declareValidInput(platform string) assurance.DeclareInput {
	return assurance.DeclareInput{
		Platform: platform, Environment: "staging", Name: "模型指纹",
		PromptTemplateKey: assurance.TemplateModelFingerprint, TargetHost: "api.example.test",
		Targets:       []assurance.Target{{ChannelID: "chn-1", Model: "claude-sonnet-4"}},
		MaxTokens:     64,
		ExpectedShape: assurance.ExpectedShape{MinLength: 1, MaxLength: 2000, TimeoutMS: 30000},
	}
}

func TestStoreDeclareInsertUpdateVersionConflict(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	ctx := context.Background()

	created, err := store.Declare(ctx, declareValidInput("sub2api"), "alice")
	if err != nil {
		t.Fatalf("Declare() = %v, want nil", err)
	}
	if created.Version != 1 || created.Status != assurance.DeclarationStatusActive {
		t.Fatalf("created = %+v, unexpected", created)
	}

	update := declareValidInput("sub2api")
	update.DeclarationID = created.ID
	update.ExpectedVersion = 1
	update.Name = "模型指纹（更新）"
	updated, err := store.Declare(ctx, update, "bob")
	if err != nil {
		t.Fatalf("Declare(update) = %v, want nil", err)
	}
	if updated.Version != 2 || updated.Name != "模型指纹（更新）" {
		t.Fatalf("updated = %+v, unexpected", updated)
	}

	stale := update
	stale.ExpectedVersion = 1 // 已经是 2 了
	if _, err := store.Declare(ctx, stale, "carol"); !errors.Is(err, assurance.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
}

func TestStoreDeclareRejectsUnknownChannel(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-known")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	in := declareValidInput("sub2api")
	in.Targets = []assurance.Target{{ChannelID: "chn-does-not-exist", Model: "gpt-4o"}}
	if _, err := store.Declare(context.Background(), in, "alice"); !errors.Is(err, assurance.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput (渠道目录里查无此渠道)", err)
	}
}

func TestStoreDeclareRejectsWhenCatalogNeverCollected(t *testing.T) {
	pool := assurancePool(t)
	// 故意不 seedChannelCatalog：渠道目录尚未采集过。
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	if _, err := store.Declare(context.Background(), declareValidInput("sub2api"), "alice"); !errors.Is(err, assurance.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestStoreCancelDoubleCancelRejected(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	ctx := context.Background()
	decl, err := store.Declare(ctx, declareValidInput("sub2api"), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(ctx, decl.ID, "不再需要", "alice"); err != nil {
		t.Fatalf("Cancel() = %v, want nil", err)
	}
	if _, err := store.Cancel(ctx, decl.ID, "再次取消", "alice"); !errors.Is(err, assurance.ErrDeclarationNotActive) {
		t.Fatalf("err = %v, want ErrDeclarationNotActive", err)
	}
	// 已 cancelled 的声明不允许通过带 declaration_id 的 declare 复活。
	update := declareValidInput("sub2api")
	update.DeclarationID = decl.ID
	update.ExpectedVersion = 1
	if _, err := store.Declare(ctx, update, "alice"); !errors.Is(err, assurance.ErrDeclarationNotActive) {
		t.Fatalf("err = %v, want ErrDeclarationNotActive (cancelled 声明不可复活)", err)
	}
}

func TestStoreCancelNotFound(t *testing.T) {
	pool := assurancePool(t)
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	if _, err := store.Cancel(context.Background(), "00000000-0000-0000-0000-000000000000", "reason", "alice"); !errors.Is(err, assurance.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func declareAndFetch(t *testing.T, store *assurance.Store, in assurance.DeclareInput) assurance.Declaration {
	t.Helper()
	d, err := store.Declare(context.Background(), in, "alice")
	if err != nil {
		t.Fatalf("Declare() = %v, want nil", err)
	}
	return d
}

func TestEvaluateAndCreateRunFakeModeBypassesAllGates(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	enq := &fakeJobEnqueuer{}
	store := newTestStore(t, pool, enq)
	decl := declareAndFetch(t, store, declareValidInput("sub2api"))

	// 没有 connector_config 行 == fake 模式默认；即便这里不设预算/冷却/
	// 并发场景，也应该总是放行——这条测试的重点是"从不检查这些闸"。
	run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
		DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
	})
	if err != nil {
		t.Fatalf("EvaluateAndCreateRun() = %v, want nil", err)
	}
	if run.Status != assurance.RunStatusPending {
		t.Fatalf("status = %q, want pending", run.Status)
	}
	if len(enq.calls) != 1 || enq.calls[0].RunID != run.ID {
		t.Fatalf("enqueue calls = %+v, want exactly one matching RunID %s", enq.calls, run.ID)
	}
}

func TestEvaluateAndCreateRunClientRunKeyDedup(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	enq := &fakeJobEnqueuer{}
	store := newTestStore(t, pool, enq)
	decl := declareAndFetch(t, store, declareValidInput("sub2api"))

	in := assurance.EvaluateRunInput{DeclarationID: decl.ID, ClientRunKey: "click-1", RequestedBy: "alice", Trigger: assurance.TriggerManual}
	first, err := store.EvaluateAndCreateRun(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EvaluateAndCreateRun(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("second run id = %s, want same as first (%s) — same client_run_key must dedupe", second.ID, first.ID)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1 (dedup must not enqueue twice)", len(enq.calls))
	}
}

func TestEvaluateAndCreateRunDeclarationCancelledRefuses(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	ctx := context.Background()
	decl := declareAndFetch(t, store, declareValidInput("sub2api"))
	if _, err := store.Cancel(ctx, decl.ID, "撤销", "alice"); err != nil {
		t.Fatal(err)
	}
	run, err := store.EvaluateAndCreateRun(ctx, assurance.EvaluateRunInput{DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual})
	if err != nil {
		t.Fatalf("EvaluateAndCreateRun() = %v, want nil (a refusal is a successful decision)", err)
	}
	if run.Status != assurance.RunStatusRefused || run.RefusalReason != assurance.ReasonDeclarationCancelled {
		t.Fatalf("run = %+v, want refused/declaration_cancelled", run)
	}
}

// realModeConnectorConfig registers a mode=real connector_config row via the
// credentials package's own writer (the authoritative writer for that
// table), so this package's real-mode gate tests exercise the same rows
// GetConnectorProbeConfig will read.
func realModeConnectorConfig(t *testing.T, pool *pgxpool.Pool, platform string, allowlist []string) {
	t.Helper()
	credStore := credentials.NewStore(pool, t.TempDir())
	if _, _, err := credStore.SetConnectorConfig(context.Background(), credentials.ConnectorConfig{
		Platform: platform, Environment: "staging", Mode: "real",
		Endpoint: "https://" + allowlist[0], TargetAllowlist: allowlist,
		CredentialRef: "secret://" + platform + "-prod/read-token",
	}, "alice"); err != nil {
		t.Fatalf("SetConnectorConfig() = %v, want nil", err)
	}
}

func enableProbeSwitch(t *testing.T, pool *pgxpool.Pool, platform, probeRef string) {
	t.Helper()
	credStore := credentials.NewStore(pool, t.TempDir())
	ref := probeRef
	if _, _, err := credStore.SetProbeSwitch(context.Background(), platform, "staging", true, &ref, "alice"); err != nil {
		t.Fatalf("SetProbeSwitch() = %v, want nil", err)
	}
}

func TestEvaluateAndCreateRunRealModeRefusalReasons(t *testing.T) {
	const probeRef = "secret://sub2api-probe/token"
	const allowedHost = "api.example.test"

	setup := func(t *testing.T) (*pgxpool.Pool, *assurance.Store, assurance.Declaration) {
		t.Helper()
		pool := assurancePool(t)
		seedChannelCatalog(t, pool, "sub2api", "chn-1")
		realModeConnectorConfig(t, pool, "sub2api", []string{allowedHost})
		store := newTestStore(t, pool, &fakeJobEnqueuer{}, assurance.WithGlobalKillSwitch(true))
		in := declareValidInput("sub2api")
		in.TargetHost = allowedHost
		decl := declareAndFetch(t, store, in)
		return pool, store, decl
	}

	assertRefused := func(t *testing.T, store *assurance.Store, decl assurance.Declaration, wantReason string) {
		t.Helper()
		run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
			DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
		})
		if err != nil {
			t.Fatalf("EvaluateAndCreateRun() = %v, want nil", err)
		}
		if run.Status != assurance.RunStatusRefused || run.RefusalReason != wantReason {
			t.Fatalf("run = %+v, want refused/%s", run, wantReason)
		}
	}

	t.Run("global_kill_switch_off", func(t *testing.T) {
		pool := assurancePool(t)
		seedChannelCatalog(t, pool, "sub2api", "chn-1")
		realModeConnectorConfig(t, pool, "sub2api", []string{allowedHost})
		// WithGlobalKillSwitch(false)（默认）：全局开关未开。
		store := newTestStore(t, pool, &fakeJobEnqueuer{})
		in := declareValidInput("sub2api")
		in.TargetHost = allowedHost
		decl := declareAndFetch(t, store, in)
		assertRefused(t, store, decl, assurance.ReasonGlobalKillSwitchOff)
	})

	t.Run("platform_kill_switch_off", func(t *testing.T) {
		_, store, decl := setup(t)
		// probe_enabled 默认 false（未调用 SetProbeSwitch）。
		assertRefused(t, store, decl, assurance.ReasonPlatformKillSwitchOff)
	})

	t.Run("probe_credential_missing", func(t *testing.T) {
		pool, store, decl := setup(t)
		// 打开开关但故意不给凭据引用——SetProbeSwitch 本身会拒绝这个组合，
		// 所以这里直接绕过它，用 credentials.Store 手工把 probe_enabled 置 true
		// 但引用留空，模拟"数据被绕过 Action 直接改坏"的防御性场景。
		if _, err := pool.Exec(context.Background(),
			`UPDATE core.connector_config SET probe_enabled = true WHERE platform = 'sub2api' AND environment = 'staging'`); err != nil {
			t.Fatal(err)
		}
		assertRefused(t, store, decl, assurance.ReasonProbeCredentialMissing)
	})

	t.Run("target_host_not_allowlisted", func(t *testing.T) {
		pool, store, decl := setup(t)
		enableProbeSwitch(t, pool, "sub2api", probeRef)
		// 声明的 target_host 换成一个不在白名单里的主机。
		bad := declareValidInput("sub2api")
		bad.TargetHost = "not-allowlisted.example.test"
		badDecl := declareAndFetch(t, store, bad)
		assertRefused(t, store, badDecl, assurance.ReasonTargetHostNotAllowlisted)
		_ = decl
	})

	t.Run("daily_budget_exhausted", func(t *testing.T) {
		pool, _, decl := setup(t)
		enableProbeSwitch(t, pool, "sub2api", probeRef)
		limited := newTestStore(t, pool, &fakeJobEnqueuer{},
			assurance.WithGlobalKillSwitch(true), assurance.WithLimits(assurance.Limits{DailyBudgetPerPlatform: 1}))
		first, err := limited.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
			DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
		})
		if err != nil || first.Status != assurance.RunStatusPending {
			t.Fatalf("first run = %+v, err=%v, want pending", first, err)
		}
		assertRefused(t, limited, decl, assurance.ReasonDailyBudgetExhausted)
	})

	t.Run("cooldown_not_elapsed", func(t *testing.T) {
		pool, _, decl := setup(t)
		enableProbeSwitch(t, pool, "sub2api", probeRef)
		cooled := newTestStore(t, pool, &fakeJobEnqueuer{},
			assurance.WithGlobalKillSwitch(true), assurance.WithLimits(assurance.Limits{Cooldown: time.Hour}))
		first, err := cooled.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
			DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
		})
		if err != nil || first.Status != assurance.RunStatusPending {
			t.Fatalf("first run = %+v, err=%v, want pending", first, err)
		}
		if _, err := pool.Exec(context.Background(),
			`UPDATE assurance.probe_run SET status = 'succeeded' WHERE id = $1`, first.ID); err != nil {
			t.Fatal(err)
		}
		assertRefused(t, cooled, decl, assurance.ReasonCooldownNotElapsed)
	})

	t.Run("platform_run_in_progress", func(t *testing.T) {
		pool, _, decl := setup(t)
		enableProbeSwitch(t, pool, "sub2api", probeRef)
		// 冷却闸在预算之后、并发闸之前判定（checkGates 的判定顺序），默认
		// 60s 冷却会在两次紧邻的调用之间必然命中，掩盖住这里真正要测的并发
		// 闸——用一个近乎为零的冷却间隔隔离出并发闸单独的判定。
		store := newTestStore(t, pool, &fakeJobEnqueuer{},
			assurance.WithGlobalKillSwitch(true), assurance.WithLimits(assurance.Limits{Cooldown: time.Nanosecond}))
		first, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
			DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
		})
		if err != nil || first.Status != assurance.RunStatusPending {
			t.Fatalf("first run = %+v, err=%v, want pending (still in-flight)", first, err)
		}
		assertRefused(t, store, decl, assurance.ReasonPlatformRunInProgress)
	})

	t.Run("all_gates_pass_enqueues", func(t *testing.T) {
		pool, store, decl := setup(t)
		enableProbeSwitch(t, pool, "sub2api", probeRef)
		run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
			DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
		})
		if err != nil {
			t.Fatalf("EvaluateAndCreateRun() = %v, want nil", err)
		}
		if run.Status != assurance.RunStatusPending {
			t.Fatalf("run = %+v, want pending (every real-mode gate satisfied)", run)
		}
	})
}

func TestRecheckAtExecutionExcludesItselfFromBudget(t *testing.T) {
	const probeRef = "secret://sub2api-probe/token"
	const allowedHost = "api.example.test"
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	realModeConnectorConfig(t, pool, "sub2api", []string{allowedHost})
	enableProbeSwitch(t, pool, "sub2api", probeRef)
	store := newTestStore(t, pool, &fakeJobEnqueuer{},
		assurance.WithGlobalKillSwitch(true), assurance.WithLimits(assurance.Limits{DailyBudgetPerPlatform: 1}))
	in := declareValidInput("sub2api")
	in.TargetHost = allowedHost
	decl := declareAndFetch(t, store, in)

	run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
		DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
	})
	if err != nil || run.Status != assurance.RunStatusPending {
		t.Fatalf("run = %+v, err=%v, want pending (budget=1, this is the first run today)", run, err)
	}

	// 这条 run 此刻已经以 pending 落库，恰好把预算(1)用满了它自己那一份。
	// RecheckAtExecution 复检的是"它自己"，必须排除自身，否则会把刚刚合法
	// 通过的这条 run 在执行时刻错误地判成 daily_budget_exhausted。
	gate, err := store.RecheckAtExecution(context.Background(), run.ID, decl)
	if err != nil {
		t.Fatalf("RecheckAtExecution() = %v, want nil", err)
	}
	if !gate.Allowed {
		t.Fatalf("gate = %+v, want Allowed=true (a run must not count against its own budget at recheck time)", gate)
	}

	// 但一个不同的 run id（模拟另一条真实存在的批次）应当仍然正确地把这条
	// run 计入预算——排除逻辑只应该排除"自己"，不能变成"预算检查形同虚设"。
	gate2, err := store.RecheckAtExecution(context.Background(), "00000000-0000-0000-0000-000000000000", decl)
	if err != nil {
		t.Fatalf("RecheckAtExecution() = %v, want nil", err)
	}
	if gate2.Allowed || gate2.Reason != assurance.ReasonDailyBudgetExhausted {
		t.Fatalf("gate2 = %+v, want refused/daily_budget_exhausted (budget genuinely exhausted by the real run)", gate2)
	}
}

func TestGetConnectorProbeConfigNilWhenAbsent(t *testing.T) {
	pool := assurancePool(t)
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	got, err := store.GetConnectorProbeConfig(context.Background(), "sub2api", "staging")
	if err != nil || got != nil {
		t.Fatalf("got=%+v err=%v, want (nil, nil)", got, err)
	}
}

func TestInsertResultGeneratesEvidenceRef(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	decl := declareAndFetch(t, store, declareValidInput("sub2api"))
	run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
		DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.InsertResult(context.Background(), assurance.Result{
		RunID: run.ID, ChannelID: "chn-1", Model: "claude-sonnet-4",
		Status: assurance.ResultStatusOK, Verdict: "一致", ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertResult() = %v, want nil", err)
	}
	if result.EvidenceRef == "" || result.EvidenceRef[:6] != "probe-" {
		t.Fatalf("EvidenceRef = %q, want a probe-<hex> style label", result.EvidenceRef)
	}
}

func TestLatestBenchmarkScoreReadsBackParsedVerdict(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	decl := declareAndFetch(t, store, declareValidInput("sub2api"))
	run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
		DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	assessed, err := assurance.Assess(assurance.AssessInput{
		TemplateKey: assurance.TemplateBenchmarkSet, Model: "claude-sonnet-4",
		Text: "12\nParis\n27\n", Expected: assurance.ExpectedShape{MinLength: 1, MaxLength: 200, TimeoutMS: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertResult(context.Background(), assurance.Result{
		RunID: run.ID, ChannelID: "chn-1", Model: "claude-sonnet-4",
		Status: assessed.Status, Verdict: assessed.Verdict, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	score, err := store.LatestBenchmarkScore(context.Background(), "chn-1", "claude-sonnet-4")
	if err != nil {
		t.Fatalf("LatestBenchmarkScore() = %v, want nil", err)
	}
	if score == nil || *score != assurance.BenchmarkQuestionCount {
		t.Fatalf("score = %v, want %d", score, assurance.BenchmarkQuestionCount)
	}
}

func TestListProbeHistoryPagination(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	decl := declareAndFetch(t, store, declareValidInput("sub2api"))
	run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
		DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := store.InsertResult(context.Background(), assurance.Result{
			RunID: run.ID, ChannelID: "chn-1", Model: "claude-sonnet-4",
			Status: assurance.ResultStatusOK, Verdict: "一致", ObservedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	page1, err := store.ListProbeHistory(context.Background(), "sub2api", "staging", nil, 2)
	if err != nil {
		t.Fatalf("ListProbeHistory() = %v, want nil", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 = %d entries, want 2", len(page1))
	}
	cursor := page1[len(page1)-1].CreatedAt
	page2, err := store.ListProbeHistory(context.Background(), "sub2api", "staging", &cursor, 2)
	if err != nil {
		t.Fatalf("ListProbeHistory(page2) = %v, want nil", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2 = %d entries, want 2", len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Fatal("page2 must not repeat page1's rows")
	}
}
