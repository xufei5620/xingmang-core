package ops

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func now() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) }

func obs() Observation {
	at := now().Add(-60 * time.Second)
	return Observation{
		ID:                        uuid.New(),
		MetricKey:                 "sub2api.revenue.daily",
		Source:                    "sub2api-prod",
		Environment:               "production",
		ObservedAt:                &at,
		SyncedAt:                  at,
		Watermark:                 "wm-1",
		Status:                    SyncOK,
		StalenessThresholdSeconds: 1800,
		LastSuccess:               &at,
		Value:                     map[string]any{"amount_minor": 123456, "currency": "CNY"},
	}
}

func TestFreshnessUninitialized(t *testing.T) {
	o := obs()
	o.ObservedAt = nil
	f := o.Freshness(now())
	if f.State != StateUninitialized {
		t.Fatalf("state = %q, want uninitialized", f.State)
	}
	if f.StalenessSeconds != nil {
		t.Fatalf("从未采集时不应有 staleness: %v", *f.StalenessSeconds)
	}
	// 即便如此，来源与阈值仍要返回，前端要能显示「数据来源」
	if f.Source == "" || f.ThresholdSeconds == 0 {
		t.Fatalf("原始字段不应丢失: %+v", f)
	}
}

// TestFreshnessFirstSyncFailureIsNotUninitialized 回归 Codex 冷审 PR #43
// （head `ba8e275` 第 3 条）：#44 的首次真实同步失败被 #46 伪装成 neutral
// 「未初始化」。
//
// 「从未采集过」与「第一次采集就失败了」是两个事实。第一次失败时任务写
// SyncFailed + last_error_code，但没有历史值可留，observed_at / last_success
// 都是 nil；旧优先级在检查 failed 之前先返回 uninitialized，于是 real 模式
// 未实现、凭据错误这类**正在发生的故障**在前端显示成无害的「尚未接入」。
func TestFreshnessFirstSyncFailureIsNotUninitialized(t *testing.T) {
	// 事实一：一次都没采过——中性的 uninitialized
	never := obs()
	never.ObservedAt = nil
	never.LastSuccess = nil
	never.Status = SyncOK
	never.LastErrorCode = ""
	if f := never.Freshness(now()); f.State != StateUninitialized {
		t.Fatalf("从未采集应为 uninitialized, got %q", f.State)
	}

	// 事实二：第一次采集就失败了——必须是 failed，不能是 uninitialized
	firstFail := obs()
	firstFail.ObservedAt = nil
	firstFail.LastSuccess = nil
	firstFail.Status = SyncFailed
	firstFail.LastErrorCode = "not_supported"
	f := firstFail.Freshness(now())
	if f.State != StateFailed {
		t.Fatalf("首次采集失败应为 failed（而非 uninitialized）, got %q", f.State)
	}
	// 故障原因必须随主状态一起返回，前端才不用把它塞进 hover title
	if f.LastErrorCode != "not_supported" {
		t.Fatalf("错误码丢失: %+v", f)
	}
	// 没有 observed_at 就没有可算的滞后量——不能编一个 0 让失败看起来像刚采过
	if f.StalenessSeconds != nil {
		t.Fatalf("无 observed_at 时不应有 staleness: %v", *f.StalenessSeconds)
	}
	// 「从未成功过」这条事实仍要能被读出来
	if f.LastSuccess != nil {
		t.Fatalf("首次失败时 LastSuccess 应为 nil: %v", *f.LastSuccess)
	}

	// 领域层不变量：这条记录本身是合法的（失败必须带错误码）
	if err := firstFail.Validate(); err != nil {
		t.Fatalf("首次失败观测应是合法记录: %v", err)
	}
}

func TestFreshnessFreshAndPartial(t *testing.T) {
	o := obs()
	if f := o.Freshness(now()); f.State != StateFresh {
		t.Fatalf("阈值内且完整应为 fresh, got %q", f.State)
	}
	o.IsPartial = true
	f := o.Freshness(now())
	if f.State != StatePartial {
		t.Fatalf("阈值内但不完整应为 partial, got %q", f.State)
	}
	if !f.IsPartial {
		t.Fatal("IsPartial 应照样返回")
	}
}

func TestFreshnessStaleBoundary(t *testing.T) {
	o := obs()
	// 恰好等于阈值 → 延迟（边界取 >=）
	at := now().Add(-time.Duration(o.StalenessThresholdSeconds) * time.Second)
	o.ObservedAt = &at
	if f := o.Freshness(now()); f.State != StateStale {
		t.Fatalf("恰好等于阈值应为 stale, got %q", f.State)
	}
	// 差一秒 → 仍新鲜
	at2 := now().Add(-time.Duration(o.StalenessThresholdSeconds-1) * time.Second)
	o.ObservedAt = &at2
	if f := o.Freshness(now()); f.State != StateFresh {
		t.Fatalf("阈值内一秒应为 fresh, got %q", f.State)
	}
}

func TestFreshnessPriority(t *testing.T) {
	// 失败优先于延迟：同步失败比数据旧更需要立即动作
	o := obs()
	at := now().Add(-2 * time.Hour)
	o.ObservedAt = &at
	o.Status = SyncFailed
	o.LastErrorCode = "upstream_timeout"
	o.IsPartial = true
	f := o.Freshness(now())
	if f.State != StateFailed {
		t.Fatalf("失败应优先，got %q", f.State)
	}
	// 被主状态盖住的信息不能丢
	if !f.IsPartial || f.LastErrorCode != "upstream_timeout" || f.StalenessSeconds == nil {
		t.Fatalf("原始字段丢失: %+v", f)
	}

	// 延迟优先于部分
	o.Status = SyncOK
	o.LastErrorCode = ""
	if f := o.Freshness(now()); f.State != StateStale {
		t.Fatalf("延迟应优先于部分, got %q", f.State)
	}

	// 失败优先于未初始化：没有 observed_at 但带着错误码，说明正在出故障，
	// 不是「还没接上」（XM-0031，见 TestFreshnessFirstSyncFailureIsNotUninitialized）
	o.ObservedAt = nil
	o.LastSuccess = nil
	o.Status = SyncFailed
	o.LastErrorCode = "upstream_timeout"
	if f := o.Freshness(now()); f.State != StateFailed {
		t.Fatalf("失败应优先于未初始化, got %q", f.State)
	}
}

func TestStalenessIsComputedNotStored(t *testing.T) {
	// 同一条记录在不同「当前时刻」下状态不同——证明是动态计算，
	// 不需要后台任务去翻转标记
	o := obs()
	at := now().Add(-100 * time.Second)
	o.ObservedAt = &at
	o.StalenessThresholdSeconds = 300

	if f := o.Freshness(now()); f.State != StateFresh || *f.StalenessSeconds != 100 {
		t.Fatalf("此刻应为 fresh/100s, got %q/%v", f.State, f.StalenessSeconds)
	}
	later := now().Add(10 * time.Minute)
	if f := o.Freshness(later); f.State != StateStale || *f.StalenessSeconds != 700 {
		t.Fatalf("10 分钟后应为 stale/700s, got %q/%v", f.State, f.StalenessSeconds)
	}
}

func TestFreshnessClampsFutureObservedAt(t *testing.T) {
	// 时钟漂移导致观测时间在未来时，不能显示成负数或「非常新鲜」
	o := obs()
	at := now().Add(30 * time.Second)
	o.ObservedAt = &at
	f := o.Freshness(now())
	if f.StalenessSeconds == nil || *f.StalenessSeconds != 0 {
		t.Fatalf("未来时间应钳到 0, got %v", f.StalenessSeconds)
	}
}

func TestValidate(t *testing.T) {
	if err := obs().Validate(); err != nil {
		t.Fatalf("合法观测被拒绝: %v", err)
	}
	for name, mutate := range map[string]func(*Observation){
		"空 metric_key":  func(o *Observation) { o.MetricKey = "" },
		"非法 metric_key": func(o *Observation) { o.MetricKey = "Sub2API.Revenue" },
		"空 source":      func(o *Observation) { o.Source = "" },
		"空 environment": func(o *Observation) { o.Environment = "" },
		"非法 status":     func(o *Observation) { o.Status = SyncStatus("pending") },
		"阈值为零":          func(o *Observation) { o.StalenessThresholdSeconds = 0 },
		"阈值为负":          func(o *Observation) { o.StalenessThresholdSeconds = -1 },
		"失败但无错误码":       func(o *Observation) { o.Status = SyncFailed },
		"成功却带错误码":       func(o *Observation) { o.LastErrorCode = "boom" },
		"空 synced_at":   func(o *Observation) { o.SyncedAt = time.Time{} },
	} {
		o := obs()
		mutate(&o)
		if err := o.Validate(); err == nil {
			t.Fatalf("%s：应被拒绝但通过了", name)
		}
	}
}

func TestValidateErrorTypes(t *testing.T) {
	o := obs()
	o.Status = SyncFailed
	if err := o.Validate(); !errors.Is(err, ErrInconsistent) {
		t.Fatalf("状态与错误码不一致应为 ErrInconsistent, got %v", err)
	}
}
