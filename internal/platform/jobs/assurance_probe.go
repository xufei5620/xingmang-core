package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/xufei5620/xingmang-platform/internal/platform/assurance"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// XM-ASSURE1-core：渠道主动探测（检测任务）的 River Worker。
//
// 本文件只是一层薄的 River 适配——与 connector_probe.go 依赖
// internal/platform/connector 的关系形状相同：全部业务逻辑（判定闸、模板、
// 断言、探测客户端）都在 internal/platform/assurance 包里，本 Worker 只负责
// "按 River 的生命周期调用它们，把结果写回去"。
//
// 参照设计稿 §3 的 Work() 执行顺序：
//  1. Context 取消检查；
//  2. 重新加载 probe_run + declaration + 当前 connector_config，执行时刻
//     再检查一次 Kill Switch/预算（Action 接受到 Job 真正执行之间存在竞态
//     窗口）；
//  3. 构造探测客户端（fake/real）；
//  4. probe_run.status='running'；
//  5. 对 targets 逐条**顺序**执行（不并发，避免同一批次对同一渠道的多个
//     模型同时开多条连接放大瞬时成本），单次请求超时 30s；
//  6. 全部处理完，probe_run.status='succeeded'（指"批次跑完并写全了结果"，
//     不代表所有渠道都健康）；
//  7. 只有**存储写失败**才让 probe_run 落 status='failed'。

const (
	assuranceProbePrincipalID = "worker:platform"
	assuranceProbeModule      = "platform.jobs"
	// assuranceProbePerTargetTimeout 是单次探测请求的硬顶（设计稿 §3.2 步骤
	// 5）：模型推理可能比健康检查慢得多，但仍需要一个硬顶，防止任务无限
	// 挂起吃满 Job 槽位。
	assuranceProbePerTargetTimeout = 30 * time.Second
)

// AssuranceProbeStore 是 Worker 需要的最小 assurance.Store 能力。声明成
// 接口方便测试注入假实现，不需要真库也能验证 Work() 的分支逻辑。
type AssuranceProbeStore interface {
	GetRun(ctx context.Context, id string) (assurance.Run, error)
	GetDeclaration(ctx context.Context, id string) (assurance.Declaration, error)
	RecheckAtExecution(ctx context.Context, runID string, decl assurance.Declaration) (assurance.GateCheck, error)
	MarkRunRunning(ctx context.Context, id string, startedAt time.Time) error
	MarkRunRefused(ctx context.Context, id, reason string, finishedAt time.Time) error
	MarkRunCancelled(ctx context.Context, id, reason string, finishedAt time.Time) error
	MarkRunSucceeded(ctx context.Context, id string, finishedAt time.Time) error
	MarkRunFailed(ctx context.Context, id string, finishedAt time.Time) error
	InsertResult(ctx context.Context, r assurance.Result) (assurance.Result, error)
	LatestBenchmarkScore(ctx context.Context, channelID, model string) (*int, error)
	GetConnectorProbeConfig(ctx context.Context, platform, environment string) (*assurance.ConnectorProbeConfig, error)
}

// ProbeSecretResolver 解析 probe_credential_ref → 明文 token（real 模式）。
// *secrets.Router/任何 secrets.SecretProvider 都满足这个接口。
type ProbeSecretResolver interface {
	Resolve(ctx context.Context, ref secrets.CredentialRef, purpose string) (secrets.SecretValue, error)
}

// AssuranceProbeOptions 是 Worker 的构造参数。
type AssuranceProbeOptions struct {
	Logger      *slog.Logger
	Environment string
	Store       AssuranceProbeStore
	// GlobalKillSwitchEnabled 是本进程（platform-worker）独立解析
	// XM_ASSURE_PROBE_ENABLED 的结果——与 cmd/platform-api 解析同一个变量
	// 传给 assurance.Store 是两个独立的解析（两个进程互不可见对方内存，
	// XM_CONNECTOR_PROBE_ENABLED 的既有先例同理）。
	GlobalKillSwitchEnabled bool
	// ProbeSecrets 只在 real 模式用到：解析 probe_credential_ref。
	ProbeSecrets ProbeSecretResolver
	// Now 可注入固定时钟（测试用；默认 time.Now）。
	Now func() time.Time
}

// AssuranceProbeWorker 执行一次检测任务批次。
type AssuranceProbeWorker struct {
	river.WorkerDefaults[assurance.ProbeArgs]

	logger                  *slog.Logger
	environment             string
	store                   AssuranceProbeStore
	globalKillSwitchEnabled bool
	probeSecrets            ProbeSecretResolver
	now                     func() time.Time
}

// NewAssuranceProbeWorker 构造 Worker，补齐安全缺省值。
func NewAssuranceProbeWorker(opts AssuranceProbeOptions) *AssuranceProbeWorker {
	logger := opts.Logger
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	environment := opts.Environment
	if strings.TrimSpace(environment) == "" {
		environment = "unknown"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &AssuranceProbeWorker{
		logger:                  logger,
		environment:             environment,
		store:                   opts.Store,
		globalKillSwitchEnabled: opts.GlobalKillSwitchEnabled,
		probeSecrets:            opts.ProbeSecrets,
		now:                     now,
	}
}

// Work 执行一次探测批次。返回的 error 只反映**存储写失败**，从不反映"某个
// target 探测失败"（那是一个成功的批次里一行诚实的失败结果）或"这次决定不
// 跑"（refused/cancelled 同样是 Job 的成功完成）——与 connector_probe.go
// Work() 的返回值纪律一致。
func (w *AssuranceProbeWorker) Work(ctx context.Context, job *river.Job[assurance.ProbeArgs]) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	run, err := w.store.GetRun(ctx, job.Args.RunID)
	if err != nil {
		return fmt.Errorf("加载 probe_run %s: %w", job.Args.RunID, err)
	}
	if run.Status != assurance.RunStatusPending {
		// 不是 pending：已经被处理过（River 罕见的重复投递，或测试构造了
		// 异常状态）。不重复执行，也不是这次 Job 调用的失败。
		w.logSkipped(ctx, job, run.Status)
		return nil
	}
	decl, err := w.store.GetDeclaration(ctx, run.DeclarationID)
	if err != nil {
		return fmt.Errorf("加载 declaration %s: %w", run.DeclarationID, err)
	}

	// 执行时刻竞态复检（设计稿 §3.2 步骤 2）。
	if decl.Status != assurance.DeclarationStatusActive {
		finishedAt := w.now().UTC()
		if err := w.store.MarkRunCancelled(ctx, run.ID, "race_declaration_cancelled", finishedAt); err != nil {
			return fmt.Errorf("标记 run 为 cancelled: %w", err)
		}
		w.logDecision(ctx, job, "cancelled", "race_declaration_cancelled", 0, 0)
		return nil
	}
	gate, err := w.store.RecheckAtExecution(ctx, run.ID, decl)
	if err != nil {
		return fmt.Errorf("执行时刻复检: %w", err)
	}
	if !gate.Allowed {
		finishedAt := w.now().UTC()
		reason := "race_" + gate.Reason
		if err := w.store.MarkRunRefused(ctx, run.ID, reason, finishedAt); err != nil {
			return fmt.Errorf("标记 run 为 refused: %w", err)
		}
		w.logDecision(ctx, job, "refused", reason, 0, 0)
		return nil
	}

	client, err := w.buildClient(ctx, decl)
	if err != nil {
		// 构造客户端失败（real 模式凭据解析失败等）：这不是"存储写失败"，
		// 但也不是一个能诚实归到某个 target 头上的结果——按 refused 记录，
		// 原因前缀同样标 race_，供历史记录如实呈现"这批探测没有真的发生"。
		finishedAt := w.now().UTC()
		if markErr := w.store.MarkRunRefused(ctx, run.ID, "race_"+assurance.ReasonProbeCredentialMissing, finishedAt); markErr != nil {
			return fmt.Errorf("构造探测客户端失败(%v)后标记 refused 也失败: %w", err, markErr)
		}
		w.logDecision(ctx, job, "refused", "race_"+assurance.ReasonProbeCredentialMissing, 0, 0)
		return nil
	}

	startedAt := w.now().UTC()
	if err := w.store.MarkRunRunning(ctx, run.ID, startedAt); err != nil {
		return fmt.Errorf("标记 run 为 running: %w", err)
	}

	okCount, badCount := 0, 0
	for _, target := range decl.Targets {
		result, err := w.probeOneTarget(ctx, run.ID, decl, target, client)
		if err != nil {
			// 存储写失败（不是探测本身失败）：设计稿 §3.2 步骤 7 要求整个
			// 批次落 failed。MaxAttempts=1 不会自动重试去重新发一次已经
			// 可能发生过的付费调用，因此这里走一次显式的 error 级日志。
			finishedAt := w.now().UTC()
			if markErr := w.store.MarkRunFailed(ctx, run.ID, finishedAt); markErr != nil {
				return fmt.Errorf("写结果失败(%v)后标记 run 为 failed 也失败: %w", err, markErr)
			}
			w.logJob(ctx, job, false, okCount, badCount+1, "assurance_probe_result_write_failed")
			return fmt.Errorf("写 target %s/%s 的探测结果: %w", target.ChannelID, target.Model, err)
		}
		if result.Status == assurance.ResultStatusOK {
			okCount++
		} else {
			badCount++
		}
	}

	finishedAt := w.now().UTC()
	if err := w.store.MarkRunSucceeded(ctx, run.ID, finishedAt); err != nil {
		return fmt.Errorf("标记 run 为 succeeded: %w", err)
	}
	w.logJob(ctx, job, true, okCount, badCount, "")
	return nil
}

// buildClient 按当前 connector_config 构造探测客户端：mode='fake'（或没有
// 配置行）用 assurance.FakeClient；mode='real' 解析 probe_credential_ref
// 得到 token 后构造 assurance.RealClient。
func (w *AssuranceProbeWorker) buildClient(ctx context.Context, decl assurance.Declaration) (assurance.ProbeClient, error) {
	cfg, err := w.store.GetConnectorProbeConfig(ctx, decl.Platform, decl.Environment)
	if err != nil {
		return nil, fmt.Errorf("读取 connector_config: %w", err)
	}
	if cfg == nil || cfg.Mode != assurance.ModeReal {
		return assurance.NewFakeClient(), nil
	}
	if w.probeSecrets == nil {
		return nil, fmt.Errorf("real 模式需要 ProbeSecrets，但未装配")
	}
	ref, err := secrets.ParseCredentialRef(cfg.ProbeCredentialRef)
	if err != nil {
		return nil, fmt.Errorf("probe_credential_ref: %w", err)
	}
	value, err := w.probeSecrets.Resolve(ctx, ref, "assurance.probe.run")
	if err != nil {
		return nil, fmt.Errorf("解析探测凭据失败: %w", err)
	}
	return assurance.NewRealClient(assurance.RealClientConfig{
		TargetHost: decl.TargetHost,
		// Reveal()（不是 String()——那个方法故意恒返回打码占位符防误用，
		// 见 secrets/value.go）拿到的明文只在这一瞬用来拼 Authorization
		// 头，不落日志、不落库（ADR-014）。
		Token:   value.Reveal(),
		Timeout: assuranceProbePerTargetTimeout,
	})
}

// probeOneTarget 对单个 target 发起一次探测，写一条 probe_result。返回的
// error 只反映**存储写失败**——探测本身的失败/超时被翻译成
// status=failed/timeout 的一行结果，单个 target 失败不中断批次（设计稿
// §3.2 步骤 5）。
func (w *AssuranceProbeWorker) probeOneTarget(
	ctx context.Context, runID string, decl assurance.Declaration, target assurance.Target, client assurance.ProbeClient,
) (assurance.Result, error) {
	messages, err := assurance.BuildMessages(decl.PromptTemplateKey)
	if err != nil {
		// 模板 key 校验在 declare@1 已经做过，这里出现只可能是数据被绕过
		// Action 直接改坏——按存储层错误对待，让整个批次落 failed。
		return assurance.Result{}, fmt.Errorf("构造探测消息: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, assuranceProbePerTargetTimeout)
	defer cancel()
	observedAt := w.now().UTC()
	resp, probeErr := client.Complete(reqCtx, assurance.ProbeRequest{
		TemplateKey: decl.PromptTemplateKey,
		Model:       target.Model,
		Messages:    messages,
		MaxTokens:   decl.MaxTokens,
	})

	result := assurance.Result{
		RunID:             runID,
		ChannelID:         target.ChannelID,
		ExternalChannelID: target.ExternalChannelID,
		Model:             target.Model,
		ObservedAt:        observedAt,
	}
	if resp.HTTPStatus != 0 {
		result.HTTPStatus = intPtr(resp.HTTPStatus)
	}

	switch {
	case probeErr != nil && errors.Is(reqCtx.Err(), context.DeadlineExceeded):
		result.Status = assurance.ResultStatusTimeout
		result.Verdict = "单次探测请求超时"
		result.ErrorKind = "timeout"
	case probeErr != nil:
		result.Status = assurance.ResultStatusFailed
		result.Verdict = "探测请求失败"
		result.ErrorKind = string(connector.KindOf(probeErr))
	default:
		var previousScore *int
		if decl.PromptTemplateKey == assurance.TemplateBenchmarkSet {
			previousScore, err = w.store.LatestBenchmarkScore(ctx, target.ChannelID, target.Model)
			if err != nil {
				return assurance.Result{}, fmt.Errorf("读取上一次基准题得分: %w", err)
			}
		}
		assessed, assessErr := assurance.Assess(assurance.AssessInput{
			TemplateKey:   decl.PromptTemplateKey,
			Model:         target.Model,
			Text:          resp.Text,
			Expected:      decl.ExpectedShape,
			PreviousScore: previousScore,
		})
		if assessErr != nil {
			return assurance.Result{}, fmt.Errorf("断言探测响应: %w", assessErr)
		}
		result.Status = assessed.Status
		result.Verdict = assessed.Verdict
		result.LatencyMS = intPtr(int(resp.LatencyMS))
		if resp.Measured && resp.FirstTokenMS != nil {
			result.MeasuredFirstToken = true
			result.FirstTokenMS = intPtr(int(*resp.FirstTokenMS))
		}
		result.TokensUsed = intPtr(resp.TokensUsed)
	}

	if _, err := w.store.InsertResult(ctx, result); err != nil {
		return assurance.Result{}, err
	}
	return result, nil
}

func intPtr(v int) *int { return &v }

func (w *AssuranceProbeWorker) logSkipped(ctx context.Context, job *river.Job[assurance.ProbeArgs], status string) {
	w.logger.LogAttrs(ctx, slog.LevelWarn, "assurance_probe_skipped",
		slog.String("event", "assurance_probe_skipped"),
		slog.String("module", assuranceProbeModule),
		slog.String("environment", w.environment),
		slog.String("principal_id", assuranceProbePrincipalID),
		slog.String("job_kind", assurance.ProbeJobKind),
		slog.String("run_id", job.Args.RunID),
		slog.String("run_status", status))
}

func (w *AssuranceProbeWorker) logDecision(
	ctx context.Context, job *river.Job[assurance.ProbeArgs], outcome, reason string, okCount, badCount int,
) {
	w.logger.LogAttrs(ctx, slog.LevelInfo, "assurance_probe_decision",
		slog.String("event", "assurance_probe_decision"),
		slog.String("module", assuranceProbeModule),
		slog.String("environment", w.environment),
		slog.String("principal_id", assuranceProbePrincipalID),
		slog.String("job_kind", assurance.ProbeJobKind),
		slog.String("run_id", job.Args.RunID),
		slog.String("outcome", outcome),
		slog.String("reason", reason))
}

func (w *AssuranceProbeWorker) logJob(
	ctx context.Context, job *river.Job[assurance.ProbeArgs], success bool, okCount, badCount int, errorCode string,
) {
	level, event := slog.LevelInfo, "job_completed"
	if !success {
		level, event = slog.LevelError, "job_failed"
	}
	jobID, attempt, maxAttempts := int64(0), 1, 1
	if job != nil && job.JobRow != nil {
		jobID = job.ID
		if job.Attempt > 0 {
			attempt = job.Attempt
		}
		if job.MaxAttempts > 0 {
			maxAttempts = job.MaxAttempts
		}
	}
	w.logger.LogAttrs(ctx, level, event,
		slog.String("event", event),
		slog.String("module", assuranceProbeModule),
		slog.String("environment", w.environment),
		slog.String("principal_id", assuranceProbePrincipalID),
		slog.Int64("job_id", jobID),
		slog.String("job_kind", assurance.ProbeJobKind),
		slog.String("queue", assurance.QueueProbe),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.Bool("success", success),
		slog.String("error_code", errorCode),
		slog.Int("targets_ok", okCount),
		slog.Int("targets_bad", badCount))
}
