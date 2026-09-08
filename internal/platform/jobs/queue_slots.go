package jobs

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/riverqueue/river"
)

// 队列槽位（XM-OPS-TRUTH 审稿第 1 条）。
//
// 起因：maintenance 队列在生产是**单槽**（DefaultMaxWorkers = 1，全仓没有任何
// env 或 compose 覆盖点），而心跳、告警评估、留存清理、两条同步采集、成本采集
// 全挤在这一个槽上。XM-OPS-TRUTH 把 newapi_sync 的执行期限从 River 默认的
// 1 分钟抬到 120s、sub2api_sync 抬到 100s，正是为了让上游变慢时五组读取都有
// 机会跑完——代价是这两个任务在故障期会**稳定占满**那个唯一的槽。
//
// 算一下就知道这不是小事：一轮 300s 的周期里，最坏情况下 newapi(120) +
// sub2api(100) + finance(120) = 340s，已经超过一个周期。告警评估的节拍是
// 60s（规格 §9.5 把「发现到投递延迟」列为 SLI，那条注释的原话是「这个周期是
// 它的地板」），单槽意味着**上游一变慢，告警引擎的节拍反而在故障期间变松**：
// fire_count 的「分钟数」语义退化成「轮数」，心跳新鲜度还可能自己转出新告警。
// 一个为「让观测说真话」而做的改动，不该让故障期的观测变差。
//
// 处置：给 maintenance 队列**按发现出来的慢任务个数**开槽，而不是继续用
// cfg.MaxWorkers 那一个通用数字。槽位数 = 慢任务个数 + 1——慢任务全在跑时
// 仍留一个槽给快节拍的任务。
//
// 「慢任务」不是手写名单：谁慢由两件事**数**出来——(1) 这个部署实际启用了
// 哪些任务、各自的周期是多少（EffectiveJobSchedules，与 /ops 那张部署态
// 时刻表同一段代码）；(2) 每个 Worker 自己声明的执行期限（在真正的注册点
// addWorker 收集）。加第四个慢任务时槽位自动跟着加，不需要有人记得回来改这里。
const (
	// riverDefaultJobTimeout 是 River 在 Worker 不覆写 Timeout() 时用的
	// 执行期限。它不是我们能配的值；写在这里只为把「覆写了没有」这个判断
	// 收在一处（Worker 返回 <= 0 就是没覆写）。
	riverDefaultJobTimeout = time.Minute
)

// jobTimeouts 记录每个 job kind 声明的执行期限。
//
// 它在**真正的注册点**（addWorker）填充，不是另开一份手写名单：名单与注册点
// 漂开时不会报错，只会给出旧答案，而那正是本片存在的理由。
type jobTimeouts map[string]time.Duration

// declared 返回 River 实际会对这个 kind 用的执行期限。
//
// 没登记过的 kind 也按 River 默认算：那要么是队列里根本没有的任务，要么是
// 这个部署没启用它——两种情况下把它当「不慢」都不会少开槽。
func (m jobTimeouts) declared(kind string) time.Duration {
	if d, ok := m[kind]; ok && d > 0 {
		return d
	}
	return riverDefaultJobTimeout
}

// addWorker 是本包注册 River Worker 的唯一入口：注册的同时把这个 kind 声明的
// 执行期限记下来。直接调 river.AddWorker 会绕过收集，槽位就按旧的慢任务个数算。
func addWorker[T river.JobArgs](timeouts jobTimeouts, workers *river.Workers, worker river.Worker[T]) {
	var zero T
	timeouts[zero.Kind()] = worker.Timeout(nil)
	river.AddWorker(workers, worker)
}

// queueSlotPlan 是「maintenance 队列该开几个槽、为什么」的完整答案。
// 它整个被打进 worker 的启动日志，运维不必读代码就能核对这个数字。
type queueSlotPlan struct {
	// Slots 是最终槽位数。
	Slots int
	// Cadence 是这个队列里最快的那个节拍——要守住的地板。
	Cadence time.Duration
	// Threshold 是判「慢」的那条线，见 slowJobThreshold。
	Threshold time.Duration
	// SlowKinds 是执行期限超过 Threshold 的那些任务，按 kind 排序。
	SlowKinds []string
}

// slowJobThreshold 是「多长算慢」那条线：节拍与 River 默认期限里**更小的
// 那一个**。
//
// 两个上界各挡一头：
//   - 节拍：能把一个槽占过最快节拍的任务，就会让那个节拍漏拍。
//   - River 默认期限：只用默认期限的任务是这个队列一直以来的常住人口，单槽
//     本来就是按它们配的；把它们也算成慢任务，等于每个任务都要一个槽。
//     真正的变化是有人**声明自己要更久**——XM-OPS-TRUTH 让两条同步采集从
//     默认的 1 分钟变成 100s/120s，这条线抓的就是这种声明。
//
// 取更小的那个，意味着这个函数永远不会少开槽。
func slowJobThreshold(cadence time.Duration) time.Duration {
	if cadence > 0 && cadence < riverDefaultJobTimeout {
		return cadence
	}
	return riverDefaultJobTimeout
}

// maintenanceQueueSlots 算 maintenance 队列的槽位数。
//
// 判据只有一条：**慢任务不许占满这个队列**。
//   - 地板 = 队列里已启用任务中最快的那个周期（默认部署里是心跳与告警评估的 60s）。
//   - 慢任务 = 声明的执行期限严格超过这个地板的任务。
//   - 槽位 = 慢任务个数 + 1。
//
// cfg.MaxWorkers 仍是下限：它是「这个进程同时跑几个任务」的通用旋钮，调大它
// 的部署不该被这里调小。
func maintenanceQueueSlots(cfg Config, timeouts jobTimeouts) (queueSlotPlan, error) {
	// 走 effectiveJobConfig（BuildEffectiveManifest 与 /ops 那张部署态时刻表
	// 用的同一段每任务解析），而不是 EffectiveJobSchedules：后者会对**每一个**
	// 已注册任务校验周期为正，包括这个部署没启用、因此手写 Config 里根本没填
	// 周期的那些。NewClient 从来没有这么严，本片不该顺手把它变严——一个
	// 「算槽位」的函数不许改变谁能启动。
	type queueJob struct {
		kind     string
		interval time.Duration
	}
	var running []queueJob
	for _, spec := range RegisteredPeriodicJobSpecs() {
		if spec.Queue != QueueMaintenance {
			continue
		}
		enabled, _, interval, _, err := effectiveJobConfig(cfg, spec.ID)
		if err != nil {
			return queueSlotPlan{}, fmt.Errorf("maintenance queue slots: %w", err)
		}
		// 没启用的任务占不到槽；周期非正的任务连注册都过不去
		// （newManifestPeriodicJob 会拒），两种都不参与算账。
		if !enabled || interval <= 0 {
			continue
		}
		running = append(running, queueJob{kind: spec.Kind, interval: interval})
	}

	plan := queueSlotPlan{Slots: cfg.MaxWorkers}
	for _, job := range running {
		if plan.Cadence == 0 || job.interval < plan.Cadence {
			plan.Cadence = job.interval
		}
	}
	if plan.Cadence == 0 {
		// 这个队列一个任务都没启用：没有要守的地板，也就没有要多开的槽。
		return plan, nil
	}
	plan.Threshold = slowJobThreshold(plan.Cadence)
	for _, job := range running {
		if timeouts.declared(job.kind) > plan.Threshold {
			plan.SlowKinds = append(plan.SlowKinds, job.kind)
		}
	}
	sort.Strings(plan.SlowKinds)
	if want := len(plan.SlowKinds) + 1; want > plan.Slots {
		plan.Slots = want
	}
	return plan, nil
}

// riverQueueConfigs 把槽位方案摊成 River 的队列配置。
//
// 只有 maintenance 走推导：river.QueueDefault 与 assurance.QueueProbe 里没有
// 「慢任务把快节拍挤掉」这个问题（前者今天没有周期任务，后者只有按需触发的
// 探测），继续用 cfg.MaxWorkers。
func riverQueueConfigs(cfg Config, probeQueue string, timeouts jobTimeouts) (map[string]river.QueueConfig, queueSlotPlan, error) {
	plan, err := maintenanceQueueSlots(cfg, timeouts)
	if err != nil {
		return nil, queueSlotPlan{}, err
	}
	return map[string]river.QueueConfig{
		river.QueueDefault: {MaxWorkers: cfg.MaxWorkers},
		QueueMaintenance:   {MaxWorkers: plan.Slots},
		probeQueue:         {MaxWorkers: cfg.MaxWorkers},
	}, plan, nil
}

// logQueueSlots 把槽位方案打进启动日志。
//
// 为什么值得占一条日志：这个数字是**算出来**的，而运维在生产上唯一能核对它的
// 办法就是看日志。故障复盘时「告警晚了几分钟」与「同步占着槽」是两件要对上的
// 事，slow_job_kinds 与 fastest_cadence_seconds 让它们能对上。
//
// max_workers 刻意**从交给 River 的那张 map 里取**，不从 plan 里取：这样它就是
// 队列真正拿到的数字，测试断言这一行等于断言了那张 map，中间没有可以偷偷改掉
// 的一段。
func logQueueSlots(logger *slog.Logger, environment string, plan queueSlotPlan, queues map[string]river.QueueConfig) {
	if logger == nil {
		return
	}
	logger.Info("queue_slots",
		slog.String("event", "queue_slots"),
		slog.String("module", "platform.jobs"),
		slog.String("environment", environment),
		slog.String("principal_id", "worker:platform"),
		slog.String("queue", QueueMaintenance),
		slog.Int("max_workers", queues[QueueMaintenance].MaxWorkers),
		slog.Int64("fastest_cadence_seconds", int64(plan.Cadence/time.Second)),
		slog.Int64("slow_job_threshold_seconds", int64(plan.Threshold/time.Second)),
		slog.Int("slow_job_count", len(plan.SlowKinds)),
		slog.Any("slow_job_kinds", plan.SlowKinds))
}
