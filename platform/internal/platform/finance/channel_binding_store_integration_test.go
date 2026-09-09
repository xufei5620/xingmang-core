package finance_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

func bindingPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过绑定集成测试")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), "TRUNCATE finance.platform_channel_binding"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestChannelBindingStoreCreateIdempotentRebindAndHistory(t *testing.T) {
	pool := bindingPool(t)
	ctx := context.Background()
	var serviceID, accountA, accountB uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM core.service WHERE environment='production' AND status <> 'retired' LIMIT 1").Scan(&serviceID); err != nil {
		t.Skip("测试库没有 production service fixture")
	}
	if err := pool.QueryRow(ctx, "SELECT id FROM finance.upstream_account WHERE environment='production' LIMIT 1").Scan(&accountA); err != nil {
		t.Skip("测试库没有 production upstream account fixture")
	}
	if err := pool.QueryRow(ctx, "SELECT id FROM finance.upstream_account WHERE environment='production' AND id <> $1 LIMIT 1", accountA).Scan(&accountB); err != nil {
		t.Skip("测试库没有第二条 production upstream account fixture")
	}
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	store := finance.NewChannelBindingStore(pool, func() time.Time { return now })
	ref, _ := finance.NewChannelRef(serviceID, "  channel-1 ")
	created, err := store.Set(ctx, finance.SetBindingInput{
		Environment: "production", Channel: ref, UpstreamAccountID: accountA,
		Provenance: "manual", Reason: "initial", CreatedBy: "staff:test",
	})
	if err != nil || !created.Changed || created.Previous != nil {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	expected := created.Binding.ID
	noop, err := store.Set(ctx, finance.SetBindingInput{
		Environment: "production", Channel: ref, UpstreamAccountID: accountA,
		ExpectedBindingID: &expected, Provenance: "manual", Reason: "confirm", CreatedBy: "staff:test",
	})
	if err != nil || noop.Changed || noop.Binding.ID != expected {
		t.Fatalf("noop=%+v err=%v", noop, err)
	}
	now = now.Add(time.Hour)
	rebound, err := store.Set(ctx, finance.SetBindingInput{
		Environment: "production", Channel: ref, UpstreamAccountID: accountB,
		ExpectedBindingID: &expected, Provenance: "manual", Reason: "move", CreatedBy: "staff:test",
	})
	if err != nil || !rebound.Changed || rebound.Binding.ID == expected || rebound.Previous == nil {
		t.Fatalf("rebind=%+v err=%v", rebound, err)
	}
	history, err := store.History(ctx, ref)
	if err != nil || len(history) != 2 || history[1].ValidTo == nil {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestChannelBindingStoreRejectsStaleExpected(t *testing.T) {
	store := finance.NewChannelBindingStore(bindingPool(t), time.Now)
	_, err := store.Set(context.Background(), finance.SetBindingInput{
		Environment:       "production",
		Channel:           finance.ChannelRef{ServiceID: uuid.New(), ExternalChannelID: "x"},
		UpstreamAccountID: uuid.New(), ExpectedBindingID: func() *uuid.UUID { id := uuid.New(); return &id }(),
		Provenance: "manual", Reason: "stale", CreatedBy: "staff:test",
	})
	if err != nil && !errors.Is(err, finance.ErrBindingConflict) && !errors.Is(err, finance.ErrNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
}
