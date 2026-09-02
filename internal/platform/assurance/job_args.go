package assurance

import (
	"github.com/riverqueue/river"
)

// ProbeJobKind 是 River 任务的稳定 kind（设计稿 §3："assurance_probe"）。
const ProbeJobKind = "assurance_probe"

// QueueProbe 是探测任务的专属队列，与 jobs.QueueMaintenance 隔离：探测是
// 外部可见、有真实成本的动作，不该被内部维护任务的排队延迟影响，反之亦然
// （设计稿 §3 的注释）。
const QueueProbe = "assurance_probe"

// ProbeArgs 是 assurance_probe River 任务的参数。
//
// 定义在本包而不是 internal/platform/jobs（与 jobs 包里其它任务的 *Args
// 都定义在 jobs 包自己内部这一惯例不同）：本包的 Action Handler 需要在
// 声明 Action 执行的同一个数据库事务里插入 probe_run 行并 InsertTx 这个
// Job，如果 Args 类型定义在 jobs 包，就会形成 assurance → jobs → assurance
// 的循环 import（见 doc.go 顶部说明）。
type ProbeArgs struct {
	RunID string `json:"run_id"`
}

// Kind 返回稳定的 River kind。
func (ProbeArgs) Kind() string { return ProbeJobKind }

// InsertOpts 给每个探测批次同样的策略：MaxAttempts=1（不自动重试——重试
// 可能对已经花过一次钱的探测再收一次费，ADR-019 决策·五），独立队列。
//
// **不用 River UniqueOpts 做并发闸**（设计稿 §3.1 的关键推理）：
// ProbeArgs.RunID 对每次探测批次都不同（它就是主键），UniqueOpts 按参数
// 去重在这里保证不了"同平台同时只有一个在跑"，因为两个不同批次的 RunID
// 天生不相等。并发闸因此必须在 Action Handler 层（EvaluateRun 的
// platform_run_in_progress 检查）与 Job 执行开始时（jobs/assurance_probe.go
// 的执行时刻竞态复检）各查一次 probe_run 表状态，是应用层锁而非 River
// 层锁——写在这里，避免后来者以为"River UniqueOpts 已经处理了这个问题"。
func (ProbeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 1,
		Queue:       QueueProbe,
	}
}
