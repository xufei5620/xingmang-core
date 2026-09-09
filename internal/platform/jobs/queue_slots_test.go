package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// realisticTimeouts 是 XM-OPS-TRUTH 之后三个慢任务的执行期限。
// 数值与 newapiSyncJobTimeout / sub2apiSyncJobTimeout / financeCollectJobTimeout
// 一致，但这几条测试**不依赖**它们恰好是这些数：判据是「超过队列最快节拍」，
// 具体数字换了照样成立。
func realisticTimeouts() jobTimeouts {
	return jobTimeouts{
		NewAPISyncJobKind:     newapiSyncJobTimeout(DefaultNewAPIRequestTimeout),
		Sub2APISyncJobKind:    sub2apiSyncJobTimeout(DefaultSub2APIRequestTimeout),
		FinanceCollectJobKind: financeCollectJobTimeout,
	}
}

func slotConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Environment = "staging"
	return cfg
}

// TestMaintenanceQueueSlotsLeavesRoomForTheFastestCadence 是审稿第 1 条的正面
// 断言：maintenance 是单槽队列，而 XM-OPS-TRUTH 把两条同步采集的执行期限抬到
// 100s/120s——不开槽的话，上游一慢，60 秒节拍的心跳与告警评估就被挤到下一轮。
func TestMaintenanceQueueSlotsLeavesRoomForTheFastestCadence(t *testing.T) {
	cfg := slotConfig(t)
	plan, err := maintenanceQueueSlots(cfg, realisticTimeouts())
	if err != nil {
		t.Fatalf("maintenanceQueueSlots: %v", err)
	}

	// 地板取自队列里最快的那个周期，不是写死的 60s：心跳与告警评估都是 1 分钟。
	if plan.Cadence != time.Minute {
		t.Fatalf("fastest cadence = %s, want %s", plan.Cadence, time.Minute)
	}
	want := []string{FinanceCollectJobKind, NewAPISyncJobKind, Sub2APISyncJobKind}
	if !slices.Equal(plan.SlowKinds, want) {
		t.Fatalf("slow kinds = %v, want %v", plan.SlowKinds, want)
	}
	// 慢任务全在跑时仍要剩一个槽：三个慢任务 → 四个槽。
	if plan.Slots != len(want)+1 {
		t.Fatalf("maintenance slots = %d, want %d（慢任务 %d 个 + 留给快节拍的 1 个）",
			plan.Slots, len(want)+1, len(want))
	}
	// 最要紧的那一条单独写出来：默认部署的槽位必须**大于** cfg.MaxWorkers，
	// 否则这个函数等于没做事。
	if plan.Slots <= cfg.MaxWorkers {
		t.Fatalf("maintenance slots = %d，没有超过通用的 MaxWorkers=%d："+
			"慢任务仍会占满整个队列", plan.Slots, cfg.MaxWorkers)
	}
}

// TestMaintenanceQueueSlotsThresholdFollowsCadence 钉住那条线是**算**出来的：
// 节拍比 River 默认期限更快时，连只用默认期限的任务也会漏拍，判据要跟着收紧。
// 把 slowJobThreshold 写死成 riverDefaultJobTimeout 的话，这一条会红。
func TestMaintenanceQueueSlotsThresholdFollowsCadence(t *testing.T) {
	cfg := slotConfig(t)
	base, err := maintenanceQueueSlots(cfg, realisticTimeouts())
	if err != nil {
		t.Fatalf("maintenanceQueueSlots: %v", err)
	}
	if base.Threshold != riverDefaultJobTimeout {
		t.Fatalf("默认部署的判据 = %s, want %s", base.Threshold, riverDefaultJobTimeout)
	}

	// 心跳调到 30 秒：队列的地板比 River 默认期限更低，判据必须跟着降到 30s，
	// 于是连心跳自己（默认期限 60s）都算慢任务。
	fast := slotConfig(t)
	fast.HeartbeatInterval = 30 * time.Second
	plan, err := maintenanceQueueSlots(fast, realisticTimeouts())
	if err != nil {
		t.Fatalf("maintenanceQueueSlots: %v", err)
	}
	if plan.Cadence != 30*time.Second {
		t.Fatalf("fastest cadence = %s, want 30s", plan.Cadence)
	}
	if plan.Threshold != 30*time.Second {
		t.Fatalf("判据 = %s, want 30s（节拍比 River 默认期限更快时应取节拍）", plan.Threshold)
	}
	if !slices.Contains(plan.SlowKinds, HeartbeatJobKind) {
		t.Fatalf("slow kinds = %v，30 秒节拍下连 %s 都可能漏拍，它必须算慢任务",
			plan.SlowKinds, HeartbeatJobKind)
	}
	if plan.Slots <= base.Slots {
		t.Fatalf("30 秒节拍的槽位 = %d，没有多于 60 秒节拍的 %d", plan.Slots, base.Slots)
	}
}

// TestMaintenanceQueueSlotsFollowsDeployment 钉住「按这个部署实际启用了什么算」，
// 而不是按一份写死的名单算。
func TestMaintenanceQueueSlotsFollowsDeployment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Config)
		slots int
	}{
		{"全开：三个慢任务 + 一个空槽", func(*Config) {}, 4},
		{"关掉 newapi 同步：慢任务少一个", func(c *Config) { c.NewAPISyncEnabled = false }, 3},
		{"关掉三条慢链路：回到通用 MaxWorkers", func(c *Config) {
			c.NewAPISyncEnabled = false
			c.Sub2APISyncEnabled = false
			c.FinanceCollectEnabled = false
		}, DefaultMaxWorkers},
		{"部署自己调大了 MaxWorkers：推导值只是下限", func(c *Config) { c.MaxWorkers = 8 }, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := slotConfig(t)
			tc.apply(&cfg)
			plan, err := maintenanceQueueSlots(cfg, realisticTimeouts())
			if err != nil {
				t.Fatalf("maintenanceQueueSlots: %v", err)
			}
			if plan.Slots != tc.slots {
				t.Fatalf("maintenance slots = %d, want %d（慢任务 %v）",
					plan.Slots, tc.slots, plan.SlowKinds)
			}
		})
	}
}

// TestMaintenanceQueueSlotsDiscoverNewSlowJobs 是这条闸的范围检查：慢任务是
// **数**出来的，不是手列的。给一个今天不慢的任务（留存清理）声明一个超过节拍
// 的执行期限，槽位必须自己跟着加一——加第四个慢任务的人不需要记得回来改代码。
func TestMaintenanceQueueSlotsDiscoverNewSlowJobs(t *testing.T) {
	cfg := slotConfig(t)
	base, err := maintenanceQueueSlots(cfg, realisticTimeouts())
	if err != nil {
		t.Fatalf("maintenanceQueueSlots: %v", err)
	}

	grown := realisticTimeouts()
	grown[RetentionJobKind] = 90 * time.Second
	after, err := maintenanceQueueSlots(cfg, grown)
	if err != nil {
		t.Fatalf("maintenanceQueueSlots: %v", err)
	}
	if after.Slots != base.Slots+1 {
		t.Fatalf("多一个慢任务后 slots = %d, want %d（原 %d）", after.Slots, base.Slots+1, base.Slots)
	}
	if !slices.Contains(after.SlowKinds, RetentionJobKind) {
		t.Fatalf("slow kinds = %v，没数上新声明的 %s", after.SlowKinds, RetentionJobKind)
	}

	// 反向：期限没超过节拍的任务不算慢，不许白开槽。
	notSlow := realisticTimeouts()
	notSlow[RetentionJobKind] = 30 * time.Second
	same, err := maintenanceQueueSlots(cfg, notSlow)
	if err != nil {
		t.Fatalf("maintenanceQueueSlots: %v", err)
	}
	if same.Slots != base.Slots {
		t.Fatalf("30s 的任务被当成慢任务了：slots = %d, want %d", same.Slots, base.Slots)
	}
}

// TestNewClientWiresDerivedMaintenanceSlots 是「规则存在 ≠ 规则被调用到」那一条：
// 上面几条都在验纯函数，这一条从最外层（NewClient）打进来，读它交给 River 的
// 那张队列配置——日志里的 max_workers 取自 map 本身，不是 plan 的副本。
//
// 顺带证明执行期限是从**真实 Worker** 收来的：三个慢任务的 kind 必须都出现在
// slow_job_kinds 里，而这几条测试从没把它们写进过 NewClient 的入参。
func TestNewClientWiresDerivedMaintenanceSlots(t *testing.T) {
	var logs bytes.Buffer
	cfg := DefaultConfig()
	cfg.Environment = "staging"
	cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	// 与 TestConfigDefaultsAndValidation 同一条路子：pgxpool.New 不连库，
	// NewClient 本身也不连（见它的文档注释）。
	pool, err := pgxpool.New(context.Background(), "postgres://localhost/xingmang")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := NewClient(pool, cfg); err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	line := findLogEvent(t, logs.Bytes(), "queue_slots")
	if got := line["queue"]; got != QueueMaintenance {
		t.Fatalf("queue = %v, want %v", got, QueueMaintenance)
	}
	maxWorkers, ok := line["max_workers"].(float64)
	if !ok {
		t.Fatalf("queue_slots 缺 max_workers: %v", line)
	}
	kinds := logStrings(t, line["slow_job_kinds"])
	for _, kind := range []string{NewAPISyncJobKind, Sub2APISyncJobKind, FinanceCollectJobKind} {
		if !slices.Contains(kinds, kind) {
			t.Fatalf("slow_job_kinds = %v，少了 %s：执行期限没有从真实 Worker 收上来",
				kinds, kind)
		}
	}
	if int(maxWorkers) != len(kinds)+1 {
		t.Fatalf("交给 River 的 maintenance 槽位 = %d, want %d（慢任务 %v + 1）",
			int(maxWorkers), len(kinds)+1, kinds)
	}
	if int(maxWorkers) <= cfg.MaxWorkers {
		t.Fatalf("交给 River 的 maintenance 槽位 = %d，没有超过 MaxWorkers=%d："+
			"慢任务仍会占满整个队列", int(maxWorkers), cfg.MaxWorkers)
	}
}

// TestSlowMaintenanceJobsCannotOverlapThemselves 守住多开槽位带来的那个副作用。
//
// 单槽时 River 不可能让同一种任务的两轮同时跑；多槽之后就可能了——只要一轮
// 的执行期限够长，长到下一个周期的那一份被取走时它还没结束。这条要求每个
// 慢任务的执行期限**严格小于**它自己的周期，于是那个窗口在结构上不存在。
//
// 慢任务的范围不是这条测试自己列的：TestNewClientWiresDerivedMaintenanceSlots
// 已经从真实注册点证明了慢任务恰好是这三个（三个都在 + 槽位 = 个数 + 1，
// 多一个少一个都会红），这里只是对那三个各查一次周期。
func TestSlowMaintenanceJobsCannotOverlapThemselves(t *testing.T) {
	cfg := slotConfig(t)
	timeouts := realisticTimeouts()
	plan, err := maintenanceQueueSlots(cfg, timeouts)
	if err != nil {
		t.Fatalf("maintenanceQueueSlots: %v", err)
	}
	if len(plan.SlowKinds) == 0 {
		t.Fatal("没有慢任务可查：这条测试失去了意义")
	}
	for _, kind := range plan.SlowKinds {
		enabled, _, interval, _, err := effectiveJobConfig(cfg, kind)
		if err != nil {
			t.Fatalf("effectiveJobConfig(%s): %v", kind, err)
		}
		if !enabled {
			t.Fatalf("%s 被算成慢任务却没启用", kind)
		}
		if got := timeouts.declared(kind); got >= interval {
			t.Fatalf("%s 的执行期限 %v 不小于它的周期 %v："+
				"多槽队列里这会让同一种任务的两轮重叠，而单槽时不会", kind, got, interval)
		}
	}
}

// TestWorkersRegisterThroughAddWorker 让「在注册点收集」这件事不至于被下一个人
// 绕过：直接调 river.AddWorker 不会报错，只会让那个 kind 的执行期限收不到，
// 于是槽位按少一个慢任务算——一个不会报错、只给旧答案的闸，正是本片要消灭的
// 那种东西。
func TestWorkersRegisterThroughAddWorker(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("读 client.go: %v", err)
	}
	if bytes.Contains(src, []byte("river.AddWorker(")) {
		t.Fatal("client.go 里出现了 river.AddWorker(：Worker 必须经 addWorker 注册，" +
			"否则它声明的 Timeout 不会进 jobTimeouts，maintenance 槽位会少算")
	}
}

// findLogEvent 从 JSON 日志里取出指定 event 的最后一行。
func findLogEvent(t *testing.T, raw []byte, event string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("日志行不是 JSON: %s", line)
		}
		if row["event"] == event {
			found = row
		}
	}
	if found == nil {
		t.Fatalf("日志里没有 event=%s: %s", event, raw)
	}
	return found
}

func logStrings(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("期望字符串数组，得到 %T (%v)", value, value)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("数组里有非字符串项: %T", item)
		}
		out = append(out, text)
	}
	return out
}

// TestJobCadenceRejectsSelfOverlappingInterval 钉住「一个任务不可能与自己
// 重叠」这条不变量在**配置域上**成立，而不是只在默认值上成立。
//
// 这是复审那条 major 的直接落点：原先这条不变量只被 slotConfig(t)（也就是
// DefaultConfig，300s 周期 vs 120s 期限）测过。把 XM_NEWAPI_SYNC_INTERVAL
// 配成 90s 就能让它不成立，而在这条闸之前没有任何一道门禁会红——唯一的下限
// 是 River 的 1 秒。所以这里的每一行都用**非默认**配置。
//
// 边界取严：期限恰好等于周期也要红。周期唯一性按周期分桶，期限跑满一整个
// 周期时下一个桶已经开了。
func TestJobCadenceRejectsSelfOverlappingInterval(t *testing.T) {
	newapiTimeout := newapiSyncJobTimeout(DefaultNewAPIRequestTimeout)
	sub2apiTimeout := sub2apiSyncJobTimeout(DefaultSub2APIRequestTimeout)

	for _, tc := range []struct {
		name    string
		apply   func(*Config)
		wantErr bool
	}{
		{"复审那条探针：newapi 周期 90s，期限 2m0s", func(c *Config) {
			c.NewAPISyncInterval = 90 * time.Second
		}, true},
		{"newapi 周期恰好等于期限", func(c *Config) {
			c.NewAPISyncInterval = newapiTimeout
		}, true},
		{"newapi 周期比期限多一秒", func(c *Config) {
			c.NewAPISyncInterval = newapiTimeout + time.Second
		}, false},
		{"sub2api 周期恰好等于期限", func(c *Config) {
			c.Sub2APISyncInterval = sub2apiTimeout
		}, true},
		{"sub2api 周期比期限多一秒", func(c *Config) {
			c.Sub2APISyncInterval = sub2apiTimeout + time.Second
		}, false},
		{"finance 周期恰好等于期限", func(c *Config) {
			c.FinanceCollectInterval = financeCollectJobTimeout
		}, true},
		{"finance 周期比期限多一秒", func(c *Config) {
			c.FinanceCollectInterval = financeCollectJobTimeout + time.Second
		}, false},
		{"关掉的任务不参与：周期再短也不该拦启动", func(c *Config) {
			c.NewAPISyncEnabled = false
			c.NewAPISyncInterval = 90 * time.Second
		}, false},
		{"没覆写 Timeout() 的任务不参与：留存清理配 90s 照样放行", func(c *Config) {
			c.RetentionInterval = 90 * time.Second
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := slotConfig(t)
			tc.apply(&cfg)
			err := cfg.normalized().validate()
			if tc.wantErr && err == nil {
				t.Fatal("这份配置让某个任务会与自己重叠，validate 必须拒绝")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("这份配置是合法的，validate 不该拒绝: %v", err)
			}
		})
	}
}

// TestJobCadenceErrorNamesTheKnob：拒绝启动的那句话必须说清动哪个旋钮。
//
// 一条只说「配置非法」的启动错误会让运维去翻代码。这里钉住三样：是哪个任务、
// 那两个数各是多少、以及该改哪个环境变量。
func TestJobCadenceErrorNamesTheKnob(t *testing.T) {
	cfg := slotConfig(t)
	cfg.NewAPISyncInterval = 90 * time.Second
	err := cfg.normalized().validate()
	if err == nil {
		t.Fatal("期限 2m0s、周期 1m30s 的配置必须被拒绝")
	}
	for _, want := range []string{
		NewAPISyncJobKind,
		"XM_NEWAPI_SYNC_INTERVAL",
		newapiSyncJobTimeout(DefaultNewAPIRequestTimeout).String(),
		(90 * time.Second).String(),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("启动错误里缺 %q，运维只能去翻代码: %v", want, err)
		}
	}
}

// TestConfiguredJobTimeoutCoversEverySlowJob 是这条闸的**范围检查**。
//
// validateJobCadence 的任务清单来自注册表（发现），启用与周期来自
// effectiveJobConfig（发现），只有「期限是多少」这一项在 configuredJobTimeout
// 里按 kind 分支——那是一份手写名单，而手写名单与被它描述的对象漂开时不会
// 报错，只会给出旧答案。
//
// 所以这里从**真实注册点**反查：跑一次 NewClient（它经 addWorker 收集每个
// Worker 自己声明的 Timeout()），读它算出来的 slow_job_kinds，逐个要求
// configuredJobTimeout 认得。加第四个慢任务却忘了在这里登记的人会看到这条红。
func TestConfiguredJobTimeoutCoversEverySlowJob(t *testing.T) {
	var logs bytes.Buffer
	cfg := slotConfig(t)
	cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	// 与 TestNewClientWiresDerivedMaintenanceSlots 同一条路子：不连库。
	pool, err := pgxpool.New(context.Background(), "postgres://localhost/xingmang")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := NewClient(pool, cfg); err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	kinds := logStrings(t, findLogEvent(t, logs.Bytes(), "queue_slots")["slow_job_kinds"])
	if len(kinds) == 0 {
		t.Fatal("一个慢任务都没数出来，下面的循环会空转（恒真）")
	}
	for _, kind := range kinds {
		declared, ok := configuredJobTimeout(cfg.normalized(), kind)
		if !ok {
			t.Fatalf("%s 被真实注册点判成慢任务，但 configuredJobTimeout 不认得它："+
				"validateJobCadence 会静静跳过它，那条不变量对它不成立", kind)
		}
		if declared <= riverDefaultJobTimeout {
			t.Fatalf("%s 登记的期限 %s 不超过 River 默认值，与它被判成慢任务矛盾",
				kind, declared)
		}
	}
}
