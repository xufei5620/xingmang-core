package assurance_test

import (
	"context"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
)

func newTestService(t *testing.T, store *assurance.Store) *assurance.Service {
	t.Helper()
	svc, err := assurance.NewService(store)
	if err != nil {
		t.Fatalf("NewService() = %v, want nil", err)
	}
	return svc
}

func TestProbeListNeverRunAndFakeKillSwitchState(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	svc := newTestService(t, store)
	declareAndFetch(t, store, declareValidInput("sub2api"))

	result, err := svc.ProbeList(context.Background(), "sub2api", "staging")
	if err != nil {
		t.Fatalf("ProbeList() = %v, want nil", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.LastRunStatus != assurance.StatusNeverRun {
		t.Fatalf("LastRunStatus = %q, want never_run", item.LastRunStatus)
	}
	if item.KillSwitchState != "not_applicable_fake" {
		t.Fatalf("KillSwitchState = %q, want not_applicable_fake (no connector_config row == fake mode default)", item.KillSwitchState)
	}
	if !item.CanRunNow || item.CannotRunReason != "" {
		t.Fatalf("CanRunNow=%v CannotRunReason=%q, want true/empty (fake mode always allowed)", item.CanRunNow, item.CannotRunReason)
	}
	if len(item.ChannelNames) != 1 || item.ChannelNames[0] != "渠道 chn-1" {
		t.Fatalf("ChannelNames = %v, want resolved display name from the catalog", item.ChannelNames)
	}
}

func TestProbeListRollsUpWorstResultAcrossTargets(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1", "chn-2")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	svc := newTestService(t, store)
	in := declareValidInput("sub2api")
	in.Targets = []assurance.Target{
		{ChannelID: "chn-1", Model: "claude-sonnet-4"},
		{ChannelID: "chn-2", Model: "gpt-4o"},
	}
	decl := declareAndFetch(t, store, in)
	run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
		DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertResult(context.Background(), assurance.Result{
		RunID: run.ID, ChannelID: "chn-1", Model: "claude-sonnet-4",
		Status: assurance.ResultStatusOK, Verdict: "一致", ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertResult(context.Background(), assurance.Result{
		RunID: run.ID, ChannelID: "chn-2", Model: "gpt-4o",
		Status: assurance.ResultStatusFailed, Verdict: "探测请求失败", ErrorKind: "unavailable", ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// 结果汇总只在批次跑完（Job 已经把 run 标 succeeded）之后才有意义——
	// 一个仍是 pending/running 的批次，检测任务表就该老实显示"进行中"，
	// 不该提前把还没写全的结果汇总成一个"结论"。
	if err := store.MarkRunSucceeded(context.Background(), run.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	result, err := svc.ProbeList(context.Background(), "sub2api", "staging")
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.LastRunStatus != assurance.ResultStatusFailed {
		t.Fatalf("LastRunStatus = %q, want failed (worst of ok+failed)", item.LastRunStatus)
	}
	if item.LastRunVerdict == nil || *item.LastRunVerdict != "探测请求失败" {
		t.Fatalf("LastRunVerdict = %v, want the failed target's verdict", item.LastRunVerdict)
	}
	if item.LastRunAt == nil {
		t.Fatal("LastRunAt should be populated once a run exists")
	}
}

func TestProbeListRealModeKillSwitchStates(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	realModeConnectorConfig(t, pool, "sub2api", []string{"api.example.test"})
	store := newTestStore(t, pool, &fakeJobEnqueuer{}, assurance.WithGlobalKillSwitch(true))
	svc := newTestService(t, store)
	in := declareValidInput("sub2api")
	in.TargetHost = "api.example.test"
	declareAndFetch(t, store, in)

	result, err := svc.ProbeList(context.Background(), "sub2api", "staging")
	if err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.KillSwitchState != "disabled" {
		t.Fatalf("KillSwitchState = %q, want disabled (mode=real but probe_enabled=false)", item.KillSwitchState)
	}
	if item.CanRunNow || item.CannotRunReason != assurance.ReasonPlatformKillSwitchOff {
		t.Fatalf("CanRunNow=%v CannotRunReason=%q, want false/platform_kill_switch_off", item.CanRunNow, item.CannotRunReason)
	}

	enableProbeSwitch(t, pool, "sub2api", "secret://sub2api-probe/token")
	result2, err := svc.ProbeList(context.Background(), "sub2api", "staging")
	if err != nil {
		t.Fatal(err)
	}
	item2 := result2.Items[0]
	if item2.KillSwitchState != "enabled" {
		t.Fatalf("KillSwitchState = %q, want enabled", item2.KillSwitchState)
	}
	if !item2.CanRunNow {
		t.Fatalf("CanRunNow = false, want true once every real-mode gate is satisfied")
	}
}

func TestProbeHistoryReturnsEntriesWithDeclarationContext(t *testing.T) {
	pool := assurancePool(t)
	seedChannelCatalog(t, pool, "sub2api", "chn-1")
	store := newTestStore(t, pool, &fakeJobEnqueuer{})
	svc := newTestService(t, store)
	decl := declareAndFetch(t, store, declareValidInput("sub2api"))
	run, err := store.EvaluateAndCreateRun(context.Background(), assurance.EvaluateRunInput{
		DeclarationID: decl.ID, RequestedBy: "alice", Trigger: assurance.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertResult(context.Background(), assurance.Result{
		RunID: run.ID, ChannelID: "chn-1", Model: "claude-sonnet-4",
		Status: assurance.ResultStatusOK, Verdict: "一致", ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := svc.ProbeHistory(context.Background(), "sub2api", "staging", nil, 10)
	if err != nil {
		t.Fatalf("ProbeHistory() = %v, want nil", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].DeclarationName != decl.Name || entries[0].PromptTemplateKey != decl.PromptTemplateKey {
		t.Fatalf("entries[0] = %+v, missing declaration context", entries[0])
	}
	if entries[0].EvidenceRef == "" {
		t.Fatal("evidence_ref must never be empty")
	}
}
