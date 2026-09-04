package cards

import (
	"testing"
	"time"
)

// 数据新鲜度必须可见（宪法条款 12：禁止裸数字冒充实时完整数据）。
// 判定放在服务端而不是前端：两处各判一次迟早会分叉，而分叉的那一边
// 会把陈旧数据显示成实时的。
func TestFreshnessClassification(t *testing.T) {
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	interval := 5 * time.Minute

	cases := []struct {
		name       string
		syncedAgo  time.Duration
		wantStale  bool
		wantNeverS bool
	}{
		{"刚同步过", 30 * time.Second, false, false},
		{"一个周期内", 4 * time.Minute, false, false},
		{"超过两个周期就算陈旧", 11 * time.Minute, true, false},
		{"从未同步", 0, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var syncedAt time.Time
			if !tc.wantNeverS {
				syncedAt = now.Add(-tc.syncedAgo)
			}

			got := Freshness(syncedAt, now, interval)
			if got.Stale != tc.wantStale {
				t.Fatalf("Stale = %v, want %v", got.Stale, tc.wantStale)
			}
			if got.NeverSynced != tc.wantNeverS {
				t.Fatalf("NeverSynced = %v, want %v", got.NeverSynced, tc.wantNeverS)
			}
		})
	}
}

// 从未同步过的数据不能显示成「1970 年同步的」——那是个看得见
// 但没人会去查的错误。
func TestFreshnessNeverSyncedHasNoAge(t *testing.T) {
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)

	f := Freshness(time.Time{}, now, 5*time.Minute)
	if f.AgeSeconds != 0 {
		t.Fatalf("从未同步时不该有年龄, got %d", f.AgeSeconds)
	}
	if !f.NeverSynced {
		t.Fatal("应标记为从未同步")
	}
}

// 需要人工处置的操作要能被单独挑出来——它是管理端红条的数据源。
func TestNeedsAttentionFiltersOperations(t *testing.T) {
	ops := []Operation{
		{IdempotencyKey: "a", State: StateSucceeded},
		{IdempotencyKey: "b", State: StateUnknown},
		{IdempotencyKey: "c", State: StateUnknown, NeedsHumanReview: true},
		{IdempotencyKey: "d", State: StateFailed},
	}

	got := NeedsAttention(ops)

	if len(got) != 1 || got[0].IdempotencyKey != "c" {
		t.Fatalf("只有 needs_human_review 的操作该被挑出来, got %+v", got)
	}
}
