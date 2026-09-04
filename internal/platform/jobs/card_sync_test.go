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
