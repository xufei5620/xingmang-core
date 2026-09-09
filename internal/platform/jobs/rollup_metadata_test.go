package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

func TestAnnotateRollupMetadataUsesEffectiveCadence(t *testing.T) {
	o := ops.Observation{MetricKey: "test.metric"}
	annotateRollupMetadata(&o, 7*time.Minute)
	if o.RollupPolicyVersion != ops.RollupPolicyVersion {
		t.Fatalf("policy version = %d, want %d", o.RollupPolicyVersion, ops.RollupPolicyVersion)
	}
	if o.ExpectedIntervalSeconds == nil || *o.ExpectedIntervalSeconds != 420 {
		t.Fatalf("effective cadence = %v, want 420 seconds", o.ExpectedIntervalSeconds)
	}
}

func TestAnnotateRollupMetadataKeepsLegacyCadenceUnknown(t *testing.T) {
	o := ops.Observation{MetricKey: "test.metric"}
	annotateRollupMetadata(&o, 0)
	if o.RollupPolicyVersion != ops.RollupPolicyVersion {
		t.Fatalf("policy version = %d, want %d", o.RollupPolicyVersion, ops.RollupPolicyVersion)
	}
	if o.ExpectedIntervalSeconds != nil {
		t.Fatalf("zero cadence must remain unknown, got %v", *o.ExpectedIntervalSeconds)
	}
}

func TestAnnotateRollupMetadataRejectsSubsecondOrOverflowCadence(t *testing.T) {
	for _, interval := range []time.Duration{500 * time.Millisecond, (time.Duration(1<<31) * time.Second)} {
		o := ops.Observation{MetricKey: "test.metric"}
		annotateRollupMetadata(&o, interval)
		if o.ExpectedIntervalSeconds != nil {
			t.Fatalf("invalid interval %s must remain unknown, got %v", interval, *o.ExpectedIntervalSeconds)
		}
	}
}

func TestSub2APIWriterPersistsEffectiveCadence(t *testing.T) {
	store := newMemoryStore()
	worker := NewSub2APISyncWorker(Sub2APISyncOptions{
		Logger: structuredDefaultLogger(), Environment: "staging", InstanceID: "sub2api-test",
		Store:     store,
		NewClient: fakeFactory(sub2api.FakeOptions{Now: func() time.Time { return fixedNow }}),
		Now:       func() time.Time { return fixedNow }, ExpectedInterval: 7 * time.Minute,
	})
	if err := worker.Work(context.Background(), syncJob()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) != len(contractMetricKeys) {
		t.Fatalf("writes = %d, want %d", len(store.writes), len(contractMetricKeys))
	}
	for _, row := range store.writes {
		if row.RollupPolicyVersion != ops.RollupPolicyVersion || row.ExpectedIntervalSeconds == nil || *row.ExpectedIntervalSeconds != 420 {
			t.Fatalf("%s metadata = version %d cadence %v", row.MetricKey, row.RollupPolicyVersion, row.ExpectedIntervalSeconds)
		}
	}
}

func TestNewAPIWriterPersistsEffectiveCadence(t *testing.T) {
	store := newMemoryStore()
	worker := NewNewAPISyncWorker(NewAPISyncOptions{
		Logger: structuredDefaultLogger(), Environment: "staging", InstanceID: "newapi-test",
		Store:     store,
		NewClient: newapiFakeFactory(newapi.FakeOptions{Now: func() time.Time { return fixedNow }}),
		Now:       func() time.Time { return fixedNow }, ExpectedInterval: 11 * time.Minute,
	})
	if err := worker.Work(context.Background(), newapiSyncJob()); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) != len(newapiMetricKeys) {
		t.Fatalf("writes = %d, want %d", len(store.writes), len(newapiMetricKeys))
	}
	for _, row := range store.writes {
		if row.RollupPolicyVersion != ops.RollupPolicyVersion || row.ExpectedIntervalSeconds == nil || *row.ExpectedIntervalSeconds != 660 {
			t.Fatalf("%s metadata = version %d cadence %v", row.MetricKey, row.RollupPolicyVersion, row.ExpectedIntervalSeconds)
		}
	}
}

func TestFinanceWriterPersistsEffectiveCadence(t *testing.T) {
	account := financeAccount()
	registry := &financeRegistry{accounts: []finance.UpstreamAccount{account}, mappings: map[uuid.UUID][]finance.TokenMapping{
		account.ID: {{UpstreamAccountID: account.ID, UpstreamTokenID: "tok-rollup", OwnAccountID: "acct-rollup", CredentialRef: financeMappingRef}},
	}}
	store := newMemoryStore()
	worker := NewFinanceCollectWorker(FinanceCollectOptions{
		Logger: discardFinanceLogger(), Environment: "staging", InstanceID: "finance-test", Mode: FinanceCollectModeFake,
		Store: store, Registry: registry, Subscriptions: financeSubscriptions{}, Balances: financeBalances{}, Ledger: &financeLedger{},
		NewClient: finance.NewFakeMeteringClientFactory(func() time.Time { return fixedNow }),
		Now:       func() time.Time { return fixedNow }, ExpectedInterval: 13 * time.Minute,
	})
	if err := worker.Work(context.Background(), &river.Job[FinanceCollectArgs]{}); err != nil {
		t.Fatal(err)
	}
	if len(store.writes) != 3 {
		t.Fatalf("writes = %d, want 3", len(store.writes))
	}
	for _, row := range store.writes {
		if row.RollupPolicyVersion != ops.RollupPolicyVersion || row.ExpectedIntervalSeconds == nil || *row.ExpectedIntervalSeconds != 780 {
			t.Fatalf("%s metadata = version %d cadence %v", row.MetricKey, row.RollupPolicyVersion, row.ExpectedIntervalSeconds)
		}
	}
}
