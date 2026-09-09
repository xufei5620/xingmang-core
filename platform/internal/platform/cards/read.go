package cards

import "time"

// FreshnessInfo 描述一份投影数据有多新。
//
// 判定放在服务端：前端再判一次迟早会与服务端分叉，而分叉的那一边
// 会把陈旧数据显示成实时的（宪法条款 12）。
type FreshnessInfo struct {
	// SyncedAt 是最后一次成功同步的时刻；从未同步时为零值。
	SyncedAt time.Time `json:"synced_at,omitempty"`
	// AgeSeconds 是距今多久；从未同步时为 0 且 NeverSynced 为真。
	AgeSeconds int64 `json:"age_seconds"`
	// Stale 为真时前端应显式提示「数据可能已过期」。
	Stale bool `json:"stale"`
	// NeverSynced 与「很久没同步」是两件事：前者说明这条投影从建立起
	// 就没被同步过，后者说明同步停了。混在一起会让排查方向错。
	NeverSynced bool `json:"never_synced"`
}

// staleFactor 是判定陈旧的周期倍数。
//
// 取 2 而不是 1：同步作业本身有抖动，按 1 个周期判会让看板在正常运行时
// 也频繁闪「过期」，闪多了就没人看了。
const staleFactor = 2

// Freshness 判定新鲜度。interval 是同步作业的周期。
func Freshness(syncedAt, now time.Time, interval time.Duration) FreshnessInfo {
	if syncedAt.IsZero() {
		return FreshnessInfo{Stale: true, NeverSynced: true}
	}
	age := now.Sub(syncedAt)
	return FreshnessInfo{
		SyncedAt:   syncedAt,
		AgeSeconds: int64(age.Seconds()),
		Stale:      age > time.Duration(staleFactor)*interval,
	}
}

// NeedsAttention 挑出需要人工处置的操作，是管理端红条的数据源。
//
// 只挑 NeedsHumanReview 为真的：处于 unknown 但还在宽限期内的操作
// 不该打扰人——同步作业还在替它查。
func NeedsAttention(ops []Operation) []Operation {
	var out []Operation
	for _, op := range ops {
		if op.NeedsHumanReview {
			out = append(out, op)
		}
	}
	return out
}
