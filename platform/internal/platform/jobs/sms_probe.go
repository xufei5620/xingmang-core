package jobs

// 接码中心的定时巡检（XM-SMS2 #7，ADR-022 决策 5）。照四件套：job kind 常量、
// Args 实现 river.JobArgs、Worker 嵌 river.WorkerDefaults、间隔常量；遵 ADR-013，
// 同 card_sync / cpa_sync。
//
// 与其他同步任务不同的是：这个任务**只读**。它做两件事——每家一次连接测试
// （把「凭据还能不能用」变成一个有时间戳的事实），以及有余额能力的抓一次余额
// 快照（阶段 3 用相邻两次的差与成本事件对账）。这条链上没有任何购买调用，
// 判定逻辑全在 internal/platform/sms.Service.ProbeOnce 里，本文件只是薄封装。

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

const (
	// SMSProbeJobKind is the stable River kind for the SMS provider probe.
	SMSProbeJobKind = "sms_probe"

	// DefaultSMSProbeInterval 是巡检周期。
	//
	// 比其他同步任务的 5 分钟长一倍：余额和凭据状态都不是每分钟在变的量，
	// 而这一轮会给每家各打两个上游请求。10 分钟足以在「密钥过期」变成一串
	// 买号失败之前发现它，同时把常态流量压到一半。
	DefaultSMSProbeInterval = 10 * time.Minute

	smsProbeMaxAttempts = 3

	// smsProbeTimeout 兜住一整轮（每家两个只读请求）。
	smsProbeTimeout = 2 * time.Minute
)

// SMSProber 是巡检的领域入口（*sms.Service 实现它）。
//
// 定成接口而不是直接吃 *sms.Service：这一层要测的只有「跑一轮、失败怎么处理」，
// 而那些判定在领域层已经用替身测完了。
type SMSProber interface {
	ProbeOnce(ctx context.Context) ([]sms.ProbeResult, error)
	// EvaluateAlerts 紧跟在巡检之后跑：余额刚刚才刷新，这时判「低于阈值」
	// 用的是本轮的事实而不是上一轮的。**不外发**，只落事件与页面红条。
	EvaluateAlerts(ctx context.Context) ([]sms.AlertEvent, error)
}

// SMSProbeArgs 是巡检任务的参数。
type SMSProbeArgs struct {
	// RunID 仅供集成测试隔离，生产必须留空。
	RunID string `json:"run_id,omitempty"`
}

func (SMSProbeArgs) Kind() string { return SMSProbeJobKind }

func (SMSProbeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: smsProbeMaxAttempts,
		Queue:       QueueMaintenance,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true, ByPeriod: DefaultSMSProbeInterval, ByQueue: true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}

// SMSProbeWorker 跑一轮 sms.Service.ProbeOnce。
type SMSProbeWorker struct {
	river.WorkerDefaults[SMSProbeArgs]

	logger *slog.Logger
	prober SMSProber
}

// NewSMSProbeWorker 组装 Worker。prober 为 nil 时任务直接失败而不是崩溃——
// 装配漏了应该是一个刺眼的失败，不是一个空指针。
func NewSMSProbeWorker(logger *slog.Logger, prober SMSProber) *SMSProbeWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &SMSProbeWorker{logger: logger, prober: prober}
}

func (w *SMSProbeWorker) Work(ctx context.Context, job *river.Job[SMSProbeArgs]) error {
	if w.prober == nil {
		return fmt.Errorf("sms probe worker 未绑定 prober")
	}

	ctx, cancel := context.WithTimeout(ctx, smsProbeTimeout)
	defer cancel()

	results, err := w.prober.ProbeOnce(ctx)
	if err != nil {
		// 领域层只在**落库失败**时抛错，那种失败重试有意义。
		w.logger.WarnContext(ctx, "接码巡检未完成",
			slog.String("job_kind", SMSProbeJobKind),
			slog.String("error", err.Error()))
		return err
	}
	// 单家的上游失败不是任务失败：结论已经写进 provider_status.last_error，
	// 页面上看得见。这里只留一行日志，让「什么时候开始失败的」查得到。
	for _, r := range results {
		if r.Skipped || r.Reason == "" {
			continue
		}
		w.logger.WarnContext(ctx, "接码供应商巡检有问题",
			slog.String("job_kind", SMSProbeJobKind),
			slog.String("provider", r.Provider),
			slog.Bool("verified", r.Verified),
			slog.String("reason", r.Reason))
	}

	// 告警评估紧跟其后：余额刚刷新，这一轮判的是本轮的事实。
	// 它写的是自己的表，与上面的巡检互不影响，所以失败要抛——那说明库写不进去。
	alerts, err := w.prober.EvaluateAlerts(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "接码告警评估失败",
			slog.String("job_kind", SMSProbeJobKind),
			slog.String("error", err.Error()))
		return err
	}
	for _, a := range alerts {
		// **不外发**（等通知规范）：这行日志是给排查用的，不是投递。
		w.logger.WarnContext(ctx, "接码告警",
			slog.String("job_kind", SMSProbeJobKind),
			slog.String("alert_kind", a.Kind),
			slog.String("provider", a.Provider),
			slog.String("severity", a.Severity),
			slog.String("summary", a.Summary))
	}
	return nil
}
