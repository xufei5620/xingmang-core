package sms

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 定时巡检（ADR-022，XM-SMS2 #7）：每家做一次只读连接测试，有余额能力的再抓
// 一次余额快照。**一次都不花钱**——这条链上没有任何购买。
//
// 快照是给阶段 3 对账用的时间序列：「快照差」与「成本事件和」对不上，就是
// 上游多扣了或者我们漏记了。

func probeService(t *testing.T, store *memStore, hero *extrasFake, a62 *fakeAdapter) *Service {
	t.Helper()
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: true, VerifiedAt: testNow}
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	return NewService(
		[]Provider{{ID: ProviderSMS62, Adapter: a62}, {ID: ProviderHero, Adapter: hero}},
		store, nil, func() time.Time { return testNow },
	)
}

func resultFor(results []ProbeResult, provider string) ProbeResult {
	for _, r := range results {
		if r.Provider == provider {
			return r
		}
	}
	return ProbeResult{}
}

func TestProbeVerifiesEachProviderAndSnapshotsBalance(t *testing.T) {
	store := newMemStore()
	hero := &extrasFake{balanceText: "12.3456"}
	a62 := &fakeAdapter{testIP: "38.147.105.28"}
	svc := probeService(t, store, hero, a62)

	results, err := svc.ProbeOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("每家都该有一条结果, got %+v", results)
	}
	got62 := resultFor(results, ProviderSMS62)
	if !got62.Verified || got62.ClientIP != "38.147.105.28" {
		t.Errorf("62 的连接测试结果不对: %+v", got62)
	}
	// 62 没有余额能力：不读余额，也**不算失败**。
	if got62.AmountText != "" || got62.Reason != "" {
		t.Errorf("62 不该读余额也不该报错: %+v", got62)
	}
	gotHero := resultFor(results, ProviderHero)
	if !gotHero.Verified || gotHero.AmountText != "12.3456" {
		t.Errorf("Hero 应验证并读到余额: %+v", gotHero)
	}

	snaps, err := store.LatestBalanceSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Provider != ProviderHero || snaps[0].AmountText != "12.3456" {
		t.Fatalf("只该有 Hero 一条快照, got %+v", snaps)
	}
	if !snaps[0].TakenAt.Equal(testNow) {
		t.Errorf("快照时间应是本轮时钟, got %v", snaps[0].TakenAt)
	}
	// 连接测试的事实要落库：页面上的「已验证」读的是这一列。
	if st := store.status[ProviderHero]; st.VerifiedAt.IsZero() {
		t.Errorf("verified_at 应被写上")
	}
}

// 关着的供应商**一个上游请求都不发**。
//
// 运营刻意停掉一家的理由通常是「这家出问题了」或「先不用它」；巡检还继续
// 打它，等于把一个已经决定不用的上游变成每五分钟一次的噪声与流量。
func TestProbeSkipsDisabledProviderWithoutCallingUpstream(t *testing.T) {
	store := newMemStore()
	hero := &extrasFake{balanceText: "1.5"}
	a62 := &fakeAdapter{}
	svc := probeService(t, store, hero, a62)
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: false, VerifiedAt: testNow}

	results, err := svc.ProbeOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := resultFor(results, ProviderSMS62)
	if !got.Skipped || got.Reason == "" {
		t.Fatalf("关着的该被跳过并说明原因, got %+v", got)
	}
	if a62.testCalls != 0 {
		t.Fatalf("关着的不该打上游, got %d 次", a62.testCalls)
	}
	// 别家照常。
	if !resultFor(results, ProviderHero).Verified {
		t.Errorf("一家关着不该影响另一家")
	}
}

// 连接测试失败就不读余额：凭据坏了或网络不通时，读余额只会再失败一次，
// 而错误已经记在 provider_status.last_error 里了。
func TestProbeSkipsBalanceWhenConnectionFails(t *testing.T) {
	store := newMemStore()
	hero := &extrasFake{balanceText: "9.9"}
	hero.testErr = errors.New("401 unauthorized")
	a62 := &fakeAdapter{}
	svc := probeService(t, store, hero, a62)

	results, err := svc.ProbeOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := resultFor(results, ProviderHero)
	if got.Verified || got.Reason == "" || got.AmountText != "" {
		t.Fatalf("连接失败时不该读余额, got %+v", got)
	}
	if hero.balanceCalls != 0 {
		t.Fatalf("不该调余额, got %d 次", hero.balanceCalls)
	}
	snaps, _ := store.LatestBalanceSnapshots(context.Background())
	if len(snaps) != 0 {
		t.Fatalf("失败不该落快照, got %+v", snaps)
	}
	// **上一次的验证事实不被清掉**：一次网络抖动不该让配好的供应商变回
	// 「从未验证」，那会连带把购买挡死。
	if store.status[ProviderHero].VerifiedAt.IsZero() {
		t.Errorf("失败不该清掉 verified_at")
	}
}

// 余额读失败只丢这一次快照，连接测试的结论仍然作数。
func TestProbeBalanceFailureKeepsVerified(t *testing.T) {
	store := newMemStore()
	hero := &extrasFake{balanceErr: errors.New("502 bad gateway")}
	svc := probeService(t, store, hero, &fakeAdapter{})

	results, err := svc.ProbeOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := resultFor(results, ProviderHero)
	if !got.Verified {
		t.Errorf("连接测试成功了, got %+v", got)
	}
	if got.Reason == "" || got.AmountText != "" {
		t.Errorf("余额失败要写原因且没有余额, got %+v", got)
	}
	if snaps, _ := store.LatestBalanceSnapshots(context.Background()); len(snaps) != 0 {
		t.Fatalf("读失败不该落快照, got %+v", snaps)
	}
}

// 快照是**追加**的时间序列，不是「当前余额」一行：阶段 3 要用相邻两次的差
// 和成本事件对账，覆盖写就没有差可算了。
func TestProbeAppendsSnapshotEachRound(t *testing.T) {
	store := newMemStore()
	hero := &extrasFake{balanceText: "5.00"}
	svc := probeService(t, store, hero, &fakeAdapter{})

	if _, err := svc.ProbeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	hero.balanceText = "4.20"
	if _, err := svc.ProbeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.snapshotCount() != 2 {
		t.Fatalf("两轮应有两条快照, got %d", store.snapshotCount())
	}
	latest, _ := store.LatestBalanceSnapshots(context.Background())
	if len(latest) != 1 || latest[0].AmountText != "4.20" {
		t.Fatalf("最新一条该是第二轮的, got %+v", latest)
	}
}

// 落库失败是基础设施故障，要抛上去让 River 重试；上游失败不是。
func TestProbeReturnsErrorWhenSnapshotWriteFails(t *testing.T) {
	store := newMemStore()
	store.snapshotErr = errors.New("库满了")
	svc := probeService(t, store, &extrasFake{balanceText: "1.0"}, &fakeAdapter{})

	if _, err := svc.ProbeOnce(context.Background()); err == nil {
		t.Fatal("落库失败必须抛上去")
	}
}
