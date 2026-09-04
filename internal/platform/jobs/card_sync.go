package jobs

// 卡片周期同步任务（XM-CARD2）。照四件套：job kind 常量、Args 实现
// river.JobArgs、Worker 嵌 river.WorkerDefaults、间隔常量；遵 ADR-013，
// 同 cpa_sync / cost_sync。
//
// 与其他同步任务的关键不同：这个任务不只是「把上游的数抄进投影」，
// 它还承担**不确定态的对账收敛**——上游没有幂等键，开卡请求超时后既不能
// 当成功也不能当失败，只能靠这个任务按 alias 去上游查证。因此本文件刻意
// 只做薄封装：判定逻辑全在 internal/platform/cards.Syncer 里，
// 那里能用替身把「查到一张」「查到多张」「超宽限期查不到」这些分支
// 完整测到，而在 River Worker 里测这些既慢又难。

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
)

const (
	// CardSyncJobKind is the stable River kind for the Infini card sync.
	CardSyncJobKind = "card_sync"

	// DefaultCardSyncInterval matches the other sync jobs' 5-minute default.
	//
	// 卡状态不像用量那样每分钟都在变，但开卡是异步的：申请单要轮询到
	// active，5 分钟是「不至于让运营干等」与「不至于打爆一个限流阈值未知的
	// 上游」之间的折中。上游的限流阈值文档未提及（见契约验证清单），
	// 实测出来之后应重新审这个值。
	DefaultCardSyncInterval = 300 * time.Second

	// DefaultCardUnknownGrace 是不确定态的宽限期。
	//
	// 期内查不到就继续等，超期仍无结论才亮红条要人工。30 分钟的依据是
	// 开卡异步流程的正常时长远小于它，而更短的宽限会把「上游慢了一点」
	// 变成一次不必要的人工介入。
	DefaultCardUnknownGrace = 30 * time.Minute

	cardSyncMaxAttempts = 3

	// cardSyncTimeout 兜住一整轮同步。一轮要遍历所有卡，
	// 所以比单次上游调用的超时宽得多。
	cardSyncTimeout = 5 * time.Minute
)

// CardSyncArgs 是卡片同步任务的参数。
type CardSyncArgs struct {
	// RunID 仅供集成测试隔离，生产必须留空。
	RunID string `json:"run_id,omitempty"`
}

func (CardSyncArgs) Kind() string { return CardSyncJobKind }

func (CardSyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: cardSyncMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true, ByPeriod: DefaultCardSyncInterval, ByQueue: true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// CardSyncWorker 跑一轮 cards.Syncer。
type CardSyncWorker struct {
	river.WorkerDefaults[CardSyncArgs]

	logger *slog.Logger
	syncer *cards.Syncer
}

// NewCardSyncWorker 组装 Worker。syncer 为 nil 时任务直接失败而不是崩溃——
// 装配漏了应该是一个刺眼的失败，不是一个空指针。
func NewCardSyncWorker(logger *slog.Logger, syncer *cards.Syncer) *CardSyncWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &CardSyncWorker{logger: logger, syncer: syncer}
}

func (w *CardSyncWorker) Work(ctx context.Context, job *river.Job[CardSyncArgs]) error {
	if w.syncer == nil {
		return fmt.Errorf("card sync worker 未绑定 syncer")
	}

	ctx, cancel := context.WithTimeout(ctx, cardSyncTimeout)
	defer cancel()

	if err := w.syncer.RunOnce(ctx); err != nil {
		// 上游读不到时 Syncer 不会动台账，只把错误抛上来让 River 重试。
		// 这里记 warn 而不是 error：一轮同步失败是可预期的（上游抖动、
		// 限流），真正需要人看的是「不确定态超期未收敛」，那条由管理端
		// 的红条负责，不靠日志级别。
		w.logger.WarnContext(ctx, "卡片同步未完全成功",
			slog.String("job_kind", CardSyncJobKind),
			slog.String("error", err.Error()))
		return err
	}
	return nil
}
