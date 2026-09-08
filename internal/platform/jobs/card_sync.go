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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
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

	// MetricCardSyncStatus 是卡片同步的每轮状态观测（XM-CARD-VISIBILITY）。
	//
	// alerts 的 cards.sync.failed 规则以它为**唯一输入**——不在 ops 的白名单里
	// 的话，/metrics/history 会把它判成未注册，而那条规则的 ListSamples 会
	// 一条不返回且**不报错**：一条结构上不可能响的告警。
	//
	// 常量定义在 jobs 而不是某个 connectors/* 包，与 MetricApprovalQueue、
	// MetricPlatformHeartbeat 同一条先例：它不来自任何 Connector 契约，
	// 而是控制平面自己的运维信号。ops 的白名单是字面量重复（ops 不能反向
	// import jobs），一致性由 ops_test 外部测试包里的
	// TestRegisteredMetricsMatchConnectorContracts 兜住。
	MetricCardSyncStatus = "cards.sync.status"

	// CardSyncStalenessThresholdSeconds 取默认周期的 2 倍。
	//
	// 比 approval 队列那条（3 倍）紧一档：那条的价值在于「有没有人审批」，
	// 抖一下不要紧；这条的价值在于「同步这一轮到底怎么了」，一轮没写上来
	// 本身就是需要被看见的事。
	CardSyncStalenessThresholdSeconds int32 = 10 * 60
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

	// observations / environment / expectedInterval 一起由 WithObservations
	// 装上；为 nil 时只记日志不写观测（同 HeartbeatWorker 的做法）。
	observations     ObservationStore
	environment      string
	expectedInterval time.Duration
	now              func() time.Time
}

// NewCardSyncWorker 组装 Worker。syncer 为 nil 时任务直接失败而不是崩溃——
// 装配漏了应该是一个刺眼的失败，不是一个空指针。
func NewCardSyncWorker(logger *slog.Logger, syncer *cards.Syncer) *CardSyncWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &CardSyncWorker{logger: logger, syncer: syncer, now: time.Now}
}

// WithObservations 让每一轮把按账号/步骤的结果写成一条 ops 观测。
//
// 单独一个方法而不是加构造参数：既有调用点不必全改，而没装上它时行为与
// 此前一致（只记日志）。这与 HeartbeatWorker.WithObservations 是同一形状。
//
// **它不是可选的锦上添花**：RunOnce 现在对部分成功返回 nil，作业不再变红，
// 于是「某个账号持续失败」这件事只剩这条观测能承载。少了它，这次改动就是
// 把一个吵闹但真实的信号换成一个安静的盲区。
func (w *CardSyncWorker) WithObservations(store ObservationStore, environment string, expectedInterval time.Duration) *CardSyncWorker {
	w.observations = store
	w.environment = environment
	w.expectedInterval = expectedInterval
	return w
}

// WithClock 注入时钟（测试用）。
func (w *CardSyncWorker) WithClock(now func() time.Time) *CardSyncWorker {
	if now != nil {
		w.now = now
	}
	return w
}

func (w *CardSyncWorker) Work(ctx context.Context, job *river.Job[CardSyncArgs]) error {
	if w.syncer == nil {
		return fmt.Errorf("card sync worker 未绑定 syncer")
	}

	ctx, cancel := context.WithTimeout(ctx, cardSyncTimeout)
	defer cancel()

	res, err := w.syncer.RunOnce(ctx)
	obsErr := w.writeObservation(ctx, res, err)

	if err == nil {
		if len(res.Failures()) > 0 {
			// 部分成功：有失败，但也有事情做成了。**不返回错误**——
			// 六步里砸一步就把整轮判失败、烧满 3 次重试、落一条 discarded，
			// 正是 2026-09-08 那 24 小时里 288 条废作业的成因，而卡状态
			// 其实一直在被兜底路径刷新。持续失败由 cards.sync.failed 告警
			// 按「同一账号同一步骤连续 N 轮」挑出来。
			w.logger.WarnContext(ctx, "卡片同步部分成功",
				w.logAttrs(res, nil)...)
		}
		return obsErr
	}

	// 整轮全砸了。记 warn 而不是 error：一轮同步失败是可预期的（上游抖动、
	// 限流），真正需要人看的是「连续多轮同一账号同一步骤都失败」，
	// 那条由 cards.sync.failed 负责，不靠日志级别。
	w.logger.WarnContext(ctx, "卡片同步未完全成功", w.logAttrs(res, err)...)

	if !res.Retryable() {
		// 全部失败且**没有一条值得再试**（rejected / auth / ip_not_allowed
		// 这一档在全仓的定义就是「重试没有意义」）。直接终结这一轮，
		// 别烧掉剩下的 2 次尝试——错误照样返回，所以这不是「静默成功」，
		// 只是不再对一个明确说了「不」的上游重复问同一句话。
		// 下一个 5 分钟周期本身就是重试。
		return river.JobCancel(errors.Join(err, obsErr))
	}
	return errors.Join(err, obsErr)
}

// logAttrs 拼出一条同步日志的结构化字段。
//
// 三件事必须同时在一条记录里：分类后的错误串（现在带上游码与脱敏 message）、
// 按账号/步骤的结果、以及 Unwrap 链里最外层的那条脱敏简述。
// 此前这里只有 slog.String("error", err.Error())，而那时的 err.Error()
// 恰好是一句不含任何上游信息的话——「日志与后台都看不到」的落点就在这一行。
func (w *CardSyncWorker) logAttrs(res cards.RoundResult, err error) []any {
	attrs := []any{
		slog.String("job_kind", CardSyncJobKind),
		slog.Any("round", res.Summary()),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	if failures := res.Failures(); len(failures) > 0 {
		attrs = append(attrs,
			slog.String("first_failure_account", failures[0].Account),
			slog.String("first_failure_step", failures[0].Step),
			slog.String("first_failure_kind", string(failures[0].Kind)),
			// Detail 已经在连接器那一层脱敏过（connectors/infini/redact.go）。
			slog.String("first_failure_detail", failures[0].Detail))
	}
	if paused := res.PausedAccountIDs(); len(paused) > 0 {
		attrs = append(attrs, slog.Any("paused_accounts", paused))
	}
	return attrs
}

// writeObservation 把这一轮的结果写成 cards.sync.status 观测。
//
// status 只在**整轮全砸**时记 failed：部分成功记 ok 但 value_json 里带着
// 失败明细。这与告警的判据是配套的——规则数的是 (账号, 步骤) 的连续失败，
// 不是观测的总状态；把部分成功记成 failed 会让 R1（指标同步失败）每轮都响，
// 而那条告警对处置毫无帮助。
func (w *CardSyncWorker) writeObservation(ctx context.Context, res cards.RoundResult, runErr error) error {
	if w.observations == nil {
		return nil
	}
	now := w.now().UTC()
	obs := ops.Observation{
		MetricKey:                 MetricCardSyncStatus,
		Source:                    CardSyncJobKind,
		Environment:               w.environment,
		SyncedAt:                  now,
		StalenessThresholdSeconds: CardSyncStalenessThresholdSeconds,
		Value:                     res.Summary(),
	}
	if runErr == nil {
		obs.Status = ops.SyncOK
		obs.ObservedAt = &now
		obs.LastSuccess = &now
		// 有失败但整轮不算失败时标成 partial：宪法 12 条要求
		// 「不完整」这件事本身可见，而不是被一个绿点盖过去。
		obs.IsPartial = len(res.Failures()) > 0
	} else {
		obs.Status = ops.SyncFailed
		obs.LastErrorCode = cardSyncErrorCode(res)
	}
	annotateRollupMetadata(&obs, w.expectedInterval)
	if _, err := w.observations.UpsertWithSample(ctx, obs); err != nil {
		return fmt.Errorf("写卡片同步观测: %w", err)
	}
	return nil
}

// cardSyncErrorCode 取第一条失败的连接器分类当错误码。
//
// 用分类而不是错误文本：last_error_code 会进新鲜度模型并被前端按值分组，
// 塞一段自由文本进去等于让分组失效。完整的原因在 value_json 的 detail 里。
func cardSyncErrorCode(res cards.RoundResult) string {
	failures := res.Failures()
	if len(failures) == 0 {
		return "unknown"
	}
	if kind := string(failures[0].Kind); kind != "" {
		return kind
	}
	return "unknown"
}
