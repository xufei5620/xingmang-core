package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

func discardJobLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeApprovals struct {
	expired    int64
	expireErr  error
	stats      approval.QueueStats
	statsErr   error
	calls      []string
	statsCalls int
}

func (f *fakeApprovals) ExpirePending(context.Context) (int64, error) {
	f.calls = append(f.calls, "expire")
	return f.expired, f.expireErr
}

func (f *fakeApprovals) PendingStats(context.Context) (approval.QueueStats, error) {
	f.calls = append(f.calls, "stats")
	f.statsCalls++
	return f.stats, f.statsErr
}

type recordingObservations struct {
	written []ops.Observation
	err     error
}

func (r *recordingObservations) Get(context.Context, string, string) (ops.Observation, error) {
	// 本任务不读旧观测（每轮的数字都是当场算的），所以这一支永远不会被调用。
	// 实现它只是为了满足 ObservationStore 接口。
	return ops.Observation{}, nil
}

func (r *recordingObservations) UpsertWithSample(_ context.Context, o ops.Observation) (ops.Observation, error) {
	r.written = append(r.written, o)
	return o, r.err
}

// approvalJob 传 nil：Work 只用 job 打日志（logJob 里判了 nil），
// 造一个真 JobRow 只是噪声。既有的 retention_test 也是这么做的。
func approvalJob() *river.Job[ApprovalExpireArgs] { return nil }

func newApprovalWorker(t *testing.T, a ApprovalQueueMaintainer, obs ObservationStore) *ApprovalExpireWorker {
	t.Helper()
	return NewApprovalExpireWorker(ApprovalExpireOptions{
		Logger:           discardJobLogger(),
		Environment:      "staging",
		Approvals:        a,
		Observations:     obs,
		ExpectedInterval: DefaultApprovalExpireInterval,
		Now:              func() time.Time { return time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC) },
	})
}

// TestApprovalExpireCleansThenCountsInThatOrder 钉住这个任务里唯一一处
// **顺序有语义**的地方：先清理、再统计。
//
// 反过来的话，一张昨晚就该过期的单会被算进「在等人」，于是告警报的是清理
// 没跑，说出来的却是没人审批——一条指向错误方向的告警。
func TestApprovalExpireCleansThenCountsInThatOrder(t *testing.T) {
	a := &fakeApprovals{expired: 3, stats: approval.QueueStats{
		PendingCount: 2, OldestPendingAge: 90 * time.Minute,
	}}
	obs := &recordingObservations{}
	w := newApprovalWorker(t, a, obs)

	if err := w.Work(context.Background(), approvalJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(a.calls) != 2 || a.calls[0] != "expire" || a.calls[1] != "stats" {
		t.Fatalf("调用顺序不对：%v", a.calls)
	}
	if len(obs.written) != 1 {
		t.Fatalf("应当写一条观测：%+v", obs.written)
	}
	got := obs.written[0]
	if got.MetricKey != MetricApprovalQueue || got.Status != ops.SyncOK {
		t.Fatalf("观测形状不对：%+v", got)
	}
	if got.Value["pending_count"] != int64(2) ||
		got.Value["oldest_pending_age_seconds"] != int64(5400) ||
		got.Value["expired_this_round"] != int64(3) {
		t.Fatalf("观测内容不对：%+v", got.Value)
	}
	if got.ObservedAt == nil || got.LastSuccess == nil {
		t.Fatalf("成功的观测要带 observed_at 与 last_success：%+v", got)
	}
}

// TestApprovalExpireSkipsStatsWhenCleanupFails：清理失败时**不统计**，
// 写一条 failed 观测让「同步失败」那条既有规则去响。
//
// 断言的是「没调用 stats」，容易恒真，所以配了对照组：让清理成功，
// 同一个 worker 就应当调用 stats。
func TestApprovalExpireSkipsStatsWhenCleanupFails(t *testing.T) {
	a := &fakeApprovals{expireErr: errors.New("数据库挂了")}
	obs := &recordingObservations{}
	w := newApprovalWorker(t, a, obs)

	if err := w.Work(context.Background(), approvalJob()); err == nil {
		t.Fatal("清理失败必须让整轮失败——River 才会重试")
	}
	if a.statsCalls != 0 {
		t.Fatalf("清理失败还去统计了 %d 次：那一轮的数字会把过期的单算成在等人", a.statsCalls)
	}
	if len(obs.written) != 1 || obs.written[0].Status != ops.SyncFailed {
		t.Fatalf("应当写一条 failed 观测：%+v", obs.written)
	}
	if obs.written[0].LastErrorCode != "approval_expire_failed" {
		t.Fatalf("错误码不对：%q", obs.written[0].LastErrorCode)
	}
	// failed 的观测不该带 observed_at——那会让「新鲜度」看起来是好的。
	if obs.written[0].ObservedAt != nil {
		t.Fatalf("失败的观测不该带 observed_at：%+v", obs.written[0])
	}

	// 对照：清理成功时 stats 会被调用，确认上面拦住的是失败本身。
	ok := &fakeApprovals{}
	if err := newApprovalWorker(t, ok, &recordingObservations{}).Work(context.Background(), approvalJob()); err != nil {
		t.Fatalf("清理成功时不该失败：%v", err)
	}
	if ok.statsCalls != 1 {
		t.Fatalf("清理成功时应当统计一次，got %d", ok.statsCalls)
	}
}

func TestApprovalExpireStatsFailureIsRecorded(t *testing.T) {
	a := &fakeApprovals{expired: 1, statsErr: errors.New("超时")}
	obs := &recordingObservations{}
	if err := newApprovalWorker(t, a, obs).Work(context.Background(), approvalJob()); err == nil {
		t.Fatal("统计失败也该让整轮失败")
	}
	if len(obs.written) != 1 || obs.written[0].LastErrorCode != "approval_queue_stats_failed" {
		t.Fatalf("错误码要能区分是哪一步坏了：%+v", obs.written)
	}
}

// TestApprovalExpireNoServiceIsQuietNoop：没接审批服务时什么也不做且不报错，
// 但**也不写观测**——一条「队列长度 0」的观测会让「审批中心已经在跑」
// 看起来成立。
func TestApprovalExpireNoServiceIsQuietNoop(t *testing.T) {
	obs := &recordingObservations{}
	w := newApprovalWorker(t, nil, obs)
	if err := w.Work(context.Background(), approvalJob()); err != nil {
		t.Fatalf("没接服务不该报错：%v", err)
	}
	if len(obs.written) != 0 {
		t.Fatalf("没接服务不该写观测：%+v", obs.written)
	}
	// 对照：接上服务就会写——确认上面不写的原因是没服务。
	obs2 := &recordingObservations{}
	if err := newApprovalWorker(t, &fakeApprovals{}, obs2).Work(context.Background(), approvalJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(obs2.written) != 1 {
		t.Fatalf("接上服务应当写一条观测，got %d", len(obs2.written))
	}
}

// TestApprovalExpireObservationWriteFailureSurfaces：观测写失败要冒出来。
// 它是告警的唯一输入，静默吞掉等于让规则永远不响而且没人知道。
func TestApprovalExpireObservationWriteFailureSurfaces(t *testing.T) {
	obs := &recordingObservations{err: errors.New("写不进去")}
	err := newApprovalWorker(t, &fakeApprovals{}, obs).Work(context.Background(), approvalJob())
	if err == nil {
		t.Fatal("观测写失败必须报错")
	}
}

// TestApprovalExpireEmptyQueueStillObserves：队列空的时候也要写观测。
// 不写的话观测会变陈旧，「指标数据陈旧」那条规则会替「没人审批」响，
// 而运维照着它去查采集链路——查错方向。
func TestApprovalExpireEmptyQueueStillObserves(t *testing.T) {
	obs := &recordingObservations{}
	if err := newApprovalWorker(t, &fakeApprovals{}, obs).Work(context.Background(), approvalJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(obs.written) != 1 {
		t.Fatalf("空队列也要写观测：%+v", obs.written)
	}
	if obs.written[0].Value["pending_count"] != int64(0) {
		t.Fatalf("空队列的 pending_count 应当是 0：%+v", obs.written[0].Value)
	}
}
