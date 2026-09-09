package jobs

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
)

// 装配漏了 syncer 时任务必须**失败**而不是崩溃或静默成功。
// 静默成功最危险：不确定态永远收敛不了，而看板上一切正常。
func TestCardSyncWorkerWithoutSyncerFails(t *testing.T) {
	w := NewCardSyncWorker(slog.Default(), nil)

	err := w.Work(context.Background(), &river.Job[CardSyncArgs]{})
	if err == nil {
		t.Fatal("未绑定 syncer 时必须失败")
	}
}

func TestCardSyncArgsKindIsStable(t *testing.T) {
	if got := (CardSyncArgs{}).Kind(); got != "card_sync" {
		t.Fatalf("job kind = %q，改动它会让已入队的任务变成孤儿", got)
	}
}

// 重试次数必须有限：一个永远重试的花钱相关任务会把上游打爆。
func TestCardSyncArgsHasBoundedRetries(t *testing.T) {
	opts := (CardSyncArgs{}).InsertOpts()

	if opts.MaxAttempts <= 0 {
		t.Fatal("必须设置最大重试次数")
	}
	if opts.Queue == "" {
		t.Fatal("必须指定队列，否则会挤占默认队列")
	}
}

// card_sync 必须登记进 JobManifest，否则 worker 起不来。
//
// 2026-09-04 生产事故：作业类型、Worker、Args 都写好了，唯独没进注册表。
// newManifestPeriodicJob 找不到 spec 就返回错误，NewClient 失败，worker
// 崩溃重启——而崩溃日志只有 error_code，没有错误正文，排查花了很久。
//
// 原来的测试只验了 Args.Kind() 这类孤立行为，没有一条走真实注册路径，
// 于是「定义了但没登记」这类错误在本地全绿。
func TestCardSyncJobIsRegisteredInManifest(t *testing.T) {
	spec, ok := registeredPeriodicJobSpec(CardSyncJobKind)
	if !ok {
		t.Fatal("card_sync 不在 JobManifest 注册表里：worker 启动时会直接失败")
	}
	if spec.Kind != CardSyncJobKind {
		t.Fatalf("spec.Kind = %q, want %q", spec.Kind, CardSyncJobKind)
	}
	if spec.Queue == "" {
		t.Fatal("spec.Queue 为空：周期任务入队时没有队列可去")
	}
}

// 启用卡片同步的配置必须能真的造出 River 客户端。
//
// 这是上面那条的端到端版：只断言「在注册表里」还不够，注册表条目本身
// 也可能与周期任务的要求不符（间隔必须是整秒等）。
func TestNewClientAcceptsCardSyncEnabled(t *testing.T) {
	spec, ok := registeredPeriodicJobSpec(CardSyncJobKind)
	if !ok {
		t.Fatal("card_sync 未登记")
	}
	if _, err := newManifestPeriodicJob(
		CardSyncJobKind, DefaultCardSyncInterval, true,
		func() (river.JobArgs, *river.InsertOpts) {
			args := CardSyncArgs{}
			opts := args.InsertOpts()
			return args, &opts
		},
	); err != nil {
		t.Fatalf("按 worker 的实际参数构造周期任务应成功: %v（spec=%+v）", err, spec)
	}
}
