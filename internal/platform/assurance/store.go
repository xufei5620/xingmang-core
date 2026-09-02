package assurance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// 本包刻意不 import internal/platform/credentials（避免让本包的读取路径
// 依赖那个包的内部实现细节），改用自己的一份窄 SELECT 直接读
// core.connector_config 需要的四列，与 internal/platform/jobs.
// NewPgConnectorConfigSource 各自独立读同一张表是同一个先例（每个消费方按
// 自己需要的列各开一条窄读取）；ModeReal/ModeFake 两个导出常量声明在
// doc.go，取值必须与那张表的 CHECK 约束一致。

// JobEnqueuer 是 Store 入队探测 Job 需要的最小 river.Client 能力。
// *river.Client[pgx.Tx] 结构性满足这个接口。
type JobEnqueuer interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// CatalogReader 是 Store 回查渠道目录做存在性校验/解析渠道展示名需要的最小
// 能力；*ops.Store 结构性满足这个接口（同一个方法签名），不需要任何适配层。
// 声明成窄接口只是方便测试注入假实现，与 channelassurance.Reader 同一个理由。
type CatalogReader interface {
	Get(ctx context.Context, metricKey, environment string) (ops.Observation, error)
}

// ConnectorProbeConfig 是 Store 从 core.connector_config 读出的、探测判定
// 需要的四列（Mode/TargetAllowlist/ProbeEnabled/ProbeCredentialRef）。
type ConnectorProbeConfig struct {
	Mode               string
	TargetAllowlist    []string
	ProbeEnabled       bool
	ProbeCredentialRef string
}

// Store 是 assurance.probe_declaration/probe_run/probe_result 三张表的读写
// 仓储，兼做探测判定闸（预算/冷却/并发/Kill Switch）的唯一实现——Action
// Handler（actions.go）与只读 Query（service.go）都调用同一份判定逻辑
// （checkGates），避免"两处各算一遍迟早分叉"。
type Store struct {
	pool    *pgxpool.Pool
	jobs    JobEnqueuer
	catalog CatalogReader
	limits  Limits
	// globalKillSwitchEnabled 是 XM_ASSURE_PROBE_ENABLED 的解析结果
	// （ADR-019 决策·四·#5 的全局 Kill Switch）。由 cmd/platform-api 在
	// 进程启动时解析一次并传入构造函数；cmd/platform-worker 独立解析同一个
	// 环境变量传给 Job（两个进程互不可见对方内存，这是既有的
	// XM_CONNECTOR_PROBE_ENABLED 先例）。
	globalKillSwitchEnabled bool
	now                     func() time.Time
}

// Option 调整 Store 的构造。
type Option func(*Store)

// WithClock 注入时钟（测试用；默认 time.Now）。
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// WithLimits 覆盖预算/冷却缺省值。
func WithLimits(l Limits) Option {
	return func(s *Store) { s.limits = l.normalized() }
}

// WithGlobalKillSwitch 设置全局 Kill Switch 的解析结果。
func WithGlobalKillSwitch(enabled bool) Option {
	return func(s *Store) { s.globalKillSwitchEnabled = enabled }
}

// NewStore 构造仓储。pool/catalog 为 nil 时构造期即拒绝——与
// channelassurance.NewService 对 reader 的处理同一条纪律：一个"没接上
// 目录"的 Store 能被构造出来，只会把这个错误推迟到第一次调用才发现。
//
// **jobs 允许为 nil**：cmd/platform-worker 装配的 Store 只给
// AssuranceProbeWorker 用（读取/标记批次状态、写结果），从不调用
// EvaluateAndCreateRun（那是 Action Handler 的职责，只在 cmd/platform-api
// 进程注册），因此永远不会走到 insertPendingRunAndEnqueue 需要 jobs 的那
// 条路径。要求 worker 进程也传一个真实的 river.Client 会造成构造顺序上的
// 循环——jobs.NewClient 需要先有 Worker（因而需要先有 Store）才能建出
// river.Client 本身。改为在 insertPendingRunAndEnqueue 调用时才检查 nil
// （与 credentials.RegisterActions 对 nil store 的处理同一条"延迟到调用点
// 才报错"纪律），cmd/platform-api 一侧仍然必须传入真实的 river 客户端。
func NewStore(pool *pgxpool.Pool, jobs JobEnqueuer, catalog CatalogReader, opts ...Option) (*Store, error) {
	if pool == nil {
		return nil, fmt.Errorf("assurance: pool 为空: %w", ErrInvalidInput)
	}
	if catalog == nil {
		return nil, fmt.Errorf("assurance: catalog reader 为空: %w", ErrInvalidInput)
	}
	s := &Store{pool: pool, jobs: jobs, catalog: catalog, limits: Limits{}.normalized(), now: time.Now}
	for _, o := range opts {
		if o != nil {
			o(s)
		}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

func storeErr(stage string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrStore, stage, err)
}

type rowScanner interface {
	Scan(dest ...any) error
}

// ---------------------------------------------------------------------------
// 声明（probe_declaration）
// ---------------------------------------------------------------------------

const declarationColumns = `
id, platform, environment, name, prompt_template_key, target_host, targets, max_tokens,
expected_shape, schedule_cron, status, version, created_at, created_by, updated_at, updated_by,
cancelled_at, cancelled_by, cancel_reason`

func scanDeclaration(row rowScanner) (Declaration, error) {
	var d Declaration
	var targetsRaw, shapeRaw []byte
	var scheduleCron, cancelledBy, cancelReason *string
	if err := row.Scan(
		&d.ID, &d.Platform, &d.Environment, &d.Name, &d.PromptTemplateKey, &d.TargetHost,
		&targetsRaw, &d.MaxTokens, &shapeRaw, &scheduleCron, &d.Status, &d.Version,
		&d.CreatedAt, &d.CreatedBy, &d.UpdatedAt, &d.UpdatedBy,
		&d.CancelledAt, &cancelledBy, &cancelReason,
	); err != nil {
		return Declaration{}, err
	}
	if err := json.Unmarshal(targetsRaw, &d.Targets); err != nil {
		return Declaration{}, fmt.Errorf("解析 targets: %w", err)
	}
	if err := json.Unmarshal(shapeRaw, &d.ExpectedShape); err != nil {
		return Declaration{}, fmt.Errorf("解析 expected_shape: %w", err)
	}
	if scheduleCron != nil {
		d.ScheduleCron = *scheduleCron
	}
	if cancelledBy != nil {
		d.CancelledBy = *cancelledBy
	}
	if cancelReason != nil {
		d.CancelReason = *cancelReason
	}
	d.CreatedAt = d.CreatedAt.UTC()
	d.UpdatedAt = d.UpdatedAt.UTC()
	if d.CancelledAt != nil {
		t := d.CancelledAt.UTC()
		d.CancelledAt = &t
	}
	return d, nil
}

// DeclareInput 是 Declare 的入参（environment 由调用方——Action Handler——
// 从 Principal 派生，不是一个 Action 参数：与 finance 等其它模块的既有纪律
// 一致，环境不接受调用方偷渡）。
type DeclareInput struct {
	DeclarationID     string
	ExpectedVersion   int
	Platform          string
	Environment       string
	Name              string
	PromptTemplateKey string
	TargetHost        string
	Targets           []Target
	MaxTokens         int
	ExpectedShape     ExpectedShape
	ScheduleCron      string
}

// MaxTokensHardCap 是 max_tokens 的硬上限（ADR-019 决策·五）。
const MaxTokensHardCap = 512

func (in DeclareInput) validate() error {
	if !slicesContains(Platforms, in.Platform) {
		return fmt.Errorf("platform %q 不在 %v 内: %w", in.Platform, Platforms, ErrInvalidInput)
	}
	if strings.TrimSpace(in.Environment) == "" {
		return fmt.Errorf("environment 为空: %w", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("name 为空: %w", ErrInvalidInput)
	}
	if !ValidPromptTemplateKey(in.PromptTemplateKey) {
		return fmt.Errorf("prompt_template_key %q 不在 %v 内: %w", in.PromptTemplateKey, PromptTemplateKeys, ErrInvalidInput)
	}
	if strings.TrimSpace(in.TargetHost) == "" {
		return fmt.Errorf("target_host 为空: %w", ErrInvalidInput)
	}
	if in.MaxTokens <= 0 || in.MaxTokens > MaxTokensHardCap {
		return fmt.Errorf("max_tokens 必须在 (0, %d] 内: %w", MaxTokensHardCap, ErrInvalidInput)
	}
	if len(in.Targets) == 0 {
		return fmt.Errorf("targets 不能为空: %w", ErrInvalidInput)
	}
	for i, t := range in.Targets {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("targets[%d]: %w", i, err)
		}
	}
	if err := in.ExpectedShape.Validate(); err != nil {
		return err
	}
	return nil
}

func slicesContains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Declare 新建或更新一条检测任务声明（assurance.probe.declare@1 的仓储层）。
//
// 更新路径要求声明当前 status='active' 且 version 与 ExpectedVersion 一致；
// 已 cancelled 的声明**不允许**被再次 declare 复活——必须走新的 declare
// 不带 declaration_id 取新 id（设计稿 §8.1 明确写着这条测试断言）。
func (s *Store) Declare(ctx context.Context, in DeclareInput, actor string) (Declaration, error) {
	if err := in.validate(); err != nil {
		return Declaration{}, err
	}
	if strings.TrimSpace(actor) == "" {
		return Declaration{}, fmt.Errorf("actor 为空: %w", ErrInvalidInput)
	}
	if err := s.checkTargetsExist(ctx, in.Platform, in.Environment, in.Targets); err != nil {
		return Declaration{}, err
	}
	targetsJSON, err := json.Marshal(in.Targets)
	if err != nil {
		return Declaration{}, storeErr("marshal_targets", err)
	}
	shapeJSON, err := json.Marshal(in.ExpectedShape)
	if err != nil {
		return Declaration{}, storeErr("marshal_expected_shape", err)
	}
	var scheduleCron *string
	if v := strings.TrimSpace(in.ScheduleCron); v != "" {
		scheduleCron = &v
	}
	now := s.now().UTC()
	actor = strings.TrimSpace(actor)

	if strings.TrimSpace(in.DeclarationID) == "" {
		row := s.pool.QueryRow(ctx, `
INSERT INTO assurance.probe_declaration
    (platform, environment, name, prompt_template_key, target_host, targets, max_tokens,
     expected_shape, schedule_cron, status, version, created_at, created_by, updated_at, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', 1, $10, $11, $10, $11)
RETURNING`+declarationColumns,
			in.Platform, in.Environment, in.Name, in.PromptTemplateKey, in.TargetHost,
			targetsJSON, in.MaxTokens, shapeJSON, scheduleCron, now, actor)
		d, err := scanDeclaration(row)
		if err != nil {
			return Declaration{}, storeErr("declare_insert", err)
		}
		return d, nil
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Declaration{}, storeErr("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanDeclaration(tx.QueryRow(ctx,
		`SELECT`+declarationColumns+` FROM assurance.probe_declaration WHERE id = $1 FOR UPDATE`,
		in.DeclarationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Declaration{}, ErrNotFound
	}
	if err != nil {
		return Declaration{}, storeErr("lock", err)
	}
	if existing.Status != DeclarationStatusActive {
		return Declaration{}, ErrDeclarationNotActive
	}
	if existing.Platform != in.Platform {
		return Declaration{}, fmt.Errorf("更新声明不能改变 platform: %w", ErrInvalidInput)
	}
	if existing.Version != in.ExpectedVersion {
		return Declaration{}, ErrVersionConflict
	}

	row := tx.QueryRow(ctx, `
UPDATE assurance.probe_declaration SET
    name = $2, prompt_template_key = $3, target_host = $4, targets = $5, max_tokens = $6,
    expected_shape = $7, schedule_cron = $8, version = version + 1, updated_at = $9, updated_by = $10
WHERE id = $1
RETURNING`+declarationColumns,
		in.DeclarationID, in.Name, in.PromptTemplateKey, in.TargetHost, targetsJSON, in.MaxTokens,
		shapeJSON, scheduleCron, now, actor)
	d, err := scanDeclaration(row)
	if err != nil {
		return Declaration{}, storeErr("declare_update", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Declaration{}, storeErr("commit", err)
	}
	return d, nil
}

// Cancel 撤销一条声明（assurance.probe.cancel@1 的仓储层）。已 cancelled 的
// 声明再次 cancel 返回 ErrDeclarationNotActive（映射到 PRECONDITION_FAILED），
// 不是静默成功——防止"我以为我刚取消了"的误判。
func (s *Store) Cancel(ctx context.Context, declarationID, reason, actor string) (Declaration, error) {
	declarationID = strings.TrimSpace(declarationID)
	reason = strings.TrimSpace(reason)
	actor = strings.TrimSpace(actor)
	if declarationID == "" || reason == "" || actor == "" {
		return Declaration{}, fmt.Errorf("declaration_id/reason/actor 为空: %w", ErrInvalidInput)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Declaration{}, storeErr("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanDeclaration(tx.QueryRow(ctx,
		`SELECT`+declarationColumns+` FROM assurance.probe_declaration WHERE id = $1 FOR UPDATE`, declarationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Declaration{}, ErrNotFound
	}
	if err != nil {
		return Declaration{}, storeErr("lock", err)
	}
	if existing.Status != DeclarationStatusActive {
		return Declaration{}, ErrDeclarationNotActive
	}

	now := s.now().UTC()
	row := tx.QueryRow(ctx, `
UPDATE assurance.probe_declaration SET
    status = 'cancelled', cancelled_at = $2, cancelled_by = $3, cancel_reason = $4,
    updated_at = $2, updated_by = $3
WHERE id = $1
RETURNING`+declarationColumns, declarationID, now, actor, reason)
	d, err := scanDeclaration(row)
	if err != nil {
		return Declaration{}, storeErr("cancel", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Declaration{}, storeErr("commit", err)
	}
	return d, nil
}

// GetDeclaration 按 id 读取一条声明。
func (s *Store) GetDeclaration(ctx context.Context, id string) (Declaration, error) {
	d, err := scanDeclaration(s.pool.QueryRow(ctx,
		`SELECT`+declarationColumns+` FROM assurance.probe_declaration WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Declaration{}, ErrNotFound
	}
	if err != nil {
		return Declaration{}, storeErr("get_declaration", err)
	}
	return d, nil
}

// ListDeclarations 按状态列出某平台+环境下的声明，按创建时间倒序。
func (s *Store) ListDeclarations(ctx context.Context, platform, environment string, statuses []string) ([]Declaration, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT`+declarationColumns+` FROM assurance.probe_declaration
WHERE platform = $1 AND environment = $2 AND status = ANY($3)
ORDER BY created_at DESC`, platform, environment, statuses)
	if err != nil {
		return nil, storeErr("list_declarations", err)
	}
	defer rows.Close()
	out := make([]Declaration, 0)
	for rows.Next() {
		d, err := scanDeclaration(rows)
		if err != nil {
			return nil, storeErr("scan_declaration", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, storeErr("rows_declarations", err)
	}
	return out, nil
}

// checkTargetsExist 回查渠道目录（<platform>.channels.status 的 ops 观测，
// 与"渠道管理"页读的是同一份数据源）做存在性校验：declare@1 不接受渠道目录
// 里查无此渠道的声明（设计稿 §1.2.2）。
//
// 该观测里的 channel_id 字段实际就是上游自己的账号 ID（见 types.go Target
// 的文档注释），因此只对 channel_id 做存在性校验；external_channel_id 是
// 可选的展示态冗余字段，不做二次校验。
func (s *Store) checkTargetsExist(ctx context.Context, platform, environment string, targets []Target) error {
	known, err := s.channelCatalog(ctx, platform, environment)
	if err != nil {
		return err
	}
	if known == nil {
		return fmt.Errorf("渠道目录尚未采集（%s 尚无 channels.status 观测，无法校验声明的渠道是否存在）: %w",
			platform, ErrInvalidInput)
	}
	for _, t := range targets {
		if _, ok := known[t.ChannelID]; !ok {
			return fmt.Errorf("渠道目录里查无此渠道 %q: %w", t.ChannelID, ErrInvalidInput)
		}
	}
	return nil
}

// channelCatalog 读取并解析某平台的渠道目录观测，返回 channel_id -> 展示名
// 的映射；没有任何观测时返回 (nil, nil)。
func (s *Store) channelCatalog(ctx context.Context, platform, environment string) (map[string]string, error) {
	metricKey := platform + ".channels.status"
	obs, err := s.catalog.Get(ctx, metricKey, environment)
	if err != nil {
		// ops.Store.Get 把"没有行"包装成 fmt.Errorf("get metric observation: %w", err)，
		// 底层是 pgx.ErrNoRows；errors.Is 能透过 %w 链条识别到它。
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, storeErr("channel_catalog", err)
	}
	rawItems, _ := obs.Value["channels"].([]any)
	if rawItems == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(rawItems))
	for _, raw := range rawItems {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := row["channel_id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		name, _ := row["channel_name"].(string)
		if name == "" {
			name, _ = row["name"].(string)
		}
		if name == "" {
			name = id
		}
		out[id] = name
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 探测判定闸（Kill Switch / 预算 / 冷却 / 并发）
// ---------------------------------------------------------------------------

// GateCheck 是一次"能不能现在跑"的判定结果。
type GateCheck struct {
	Allowed bool
	// Reason 是 Reason* 常量之一；Allowed=true 时恒为空串。
	Reason string
}

// GetConnectorProbeConfig 读取某平台+环境当前 core.connector_config 里与
// 探测判定相关的四列；不存在返回 (nil, nil)——fake 模式的合法默认状态。
func (s *Store) GetConnectorProbeConfig(ctx context.Context, platform, environment string) (*ConnectorProbeConfig, error) {
	var c ConnectorProbeConfig
	err := s.pool.QueryRow(ctx, `
SELECT mode, target_allowlist, probe_enabled, probe_credential_ref
FROM core.connector_config WHERE platform = $1 AND environment = $2`, platform, environment).
		Scan(&c.Mode, &c.TargetAllowlist, &c.ProbeEnabled, &c.ProbeCredentialRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storeErr("get_connector_probe_config", err)
	}
	return &c, nil
}

// checkGates 是设计稿 §2.4 步骤 1-5 的唯一实现：Action Handler（run@1 接受
// 时）与只读 Query（检测任务表的 can_run_now/cannot_run_reason）都调用这一
// 份逻辑，Job 执行时刻的竞态复检（jobs/assurance_probe.go）调用的是它的一个
// 子集（省略并发闸——那一刻本来就是并发闸放行的那条 run 在执行）。
//
// mode='fake'（或没有 connector_config 行）时**直接放行**，不检查 Kill
// Switch/预算/白名单/冷却/并发——fake 模式零成本，ADR-019 决策·四明确
// 允许整条链路跳过这些闸（"直接放行到第 6 步"）。
func (s *Store) checkGates(ctx context.Context, decl Declaration) (GateCheck, *ConnectorProbeConfig, error) {
	if decl.Status != DeclarationStatusActive {
		return GateCheck{Reason: ReasonDeclarationCancelled}, nil, nil
	}
	cfg, err := s.GetConnectorProbeConfig(ctx, decl.Platform, decl.Environment)
	if err != nil {
		return GateCheck{}, nil, err
	}
	if cfg == nil || cfg.Mode != ModeReal {
		return GateCheck{Allowed: true}, cfg, nil
	}
	if !s.globalKillSwitchEnabled {
		return GateCheck{Reason: ReasonGlobalKillSwitchOff}, cfg, nil
	}
	if !cfg.ProbeEnabled {
		return GateCheck{Reason: ReasonPlatformKillSwitchOff}, cfg, nil
	}
	if strings.TrimSpace(cfg.ProbeCredentialRef) == "" {
		return GateCheck{Reason: ReasonProbeCredentialMissing}, cfg, nil
	}
	if !allowlistContains(cfg.TargetAllowlist, decl.TargetHost) {
		return GateCheck{Reason: ReasonTargetHostNotAllowlisted}, cfg, nil
	}
	used, err := s.countBudgetUsedToday(ctx, decl.Platform, decl.Environment)
	if err != nil {
		return GateCheck{}, cfg, err
	}
	if used >= s.limits.DailyBudgetPerPlatform {
		return GateCheck{Reason: ReasonDailyBudgetExhausted}, cfg, nil
	}
	lastAt, err := s.lastNonRefusedRunAt(ctx, decl.ID)
	if err != nil {
		return GateCheck{}, cfg, err
	}
	if lastAt != nil && s.now().Sub(*lastAt) < s.limits.Cooldown {
		return GateCheck{Reason: ReasonCooldownNotElapsed}, cfg, nil
	}
	inProgress, err := s.hasInProgressRun(ctx, decl.Platform, decl.Environment)
	if err != nil {
		return GateCheck{}, cfg, err
	}
	if inProgress {
		return GateCheck{Reason: ReasonPlatformRunInProgress}, cfg, nil
	}
	return GateCheck{Allowed: true}, cfg, nil
}

// CheckGatesReadOnly 暴露给 Service（检测任务表的 can_run_now/
// cannot_run_reason 两列）——纯读取，不写任何行。
func (s *Store) CheckGatesReadOnly(ctx context.Context, decl Declaration) (GateCheck, error) {
	gate, _, err := s.checkGates(ctx, decl)
	return gate, err
}

// RecheckAtExecution 是设计稿 §3.2 步骤 2 的执行时刻竞态复检："Action 通过
// 到 Job 真正执行之间存在竞态窗口，任一条件此刻不再满足"。
//
// **不能直接复用 checkGates**：并发闸（hasInProgressRun）此刻查到的
// "pending/running" 集合恒包含正在被复检的这条 run 自己——它自己就是
// pending 状态——直接复用会把每一条正常执行的批次都误判成
// platform_run_in_progress。设计稿原文也只点名"Kill Switch/预算"两类需要
// 复检，因此本方法刻意只做这两类（外加信息几乎零成本的凭据/白名单复检），
// 不碰冷却/并发——那两项此刻复检在语义上就是自相矛盾的。
//
// **runID 必须是被复检的这条 run 自己的 id**：它在这一刻已经以 pending
// 状态存在于 assurance.probe_run 里（Action Handler 早已插入），而预算计数
// 统计的正是"今天非 refused/cancelled 的 probe_run 行数"——不排除自己，
// 预算恰好用满 1 的那次探测会在复检时把自己也数进去，凭空多算一次而错误
// 拒绝一条本该合法执行的批次。这与并发闸不能直接复用 checkGates 是同一类
// "复检对象自己已经是被统计对象之一"的坑。
func (s *Store) RecheckAtExecution(ctx context.Context, runID string, decl Declaration) (GateCheck, error) {
	if decl.Status != DeclarationStatusActive {
		return GateCheck{Reason: ReasonDeclarationCancelled}, nil
	}
	cfg, err := s.GetConnectorProbeConfig(ctx, decl.Platform, decl.Environment)
	if err != nil {
		return GateCheck{}, err
	}
	if cfg == nil || cfg.Mode != ModeReal {
		return GateCheck{Allowed: true}, nil
	}
	if !s.globalKillSwitchEnabled {
		return GateCheck{Reason: ReasonGlobalKillSwitchOff}, nil
	}
	if !cfg.ProbeEnabled {
		return GateCheck{Reason: ReasonPlatformKillSwitchOff}, nil
	}
	if strings.TrimSpace(cfg.ProbeCredentialRef) == "" {
		return GateCheck{Reason: ReasonProbeCredentialMissing}, nil
	}
	if !allowlistContains(cfg.TargetAllowlist, decl.TargetHost) {
		return GateCheck{Reason: ReasonTargetHostNotAllowlisted}, nil
	}
	used, err := s.countBudgetUsedTodayExcluding(ctx, decl.Platform, decl.Environment, runID)
	if err != nil {
		return GateCheck{}, err
	}
	if used >= s.limits.DailyBudgetPerPlatform {
		return GateCheck{Reason: ReasonDailyBudgetExhausted}, nil
	}
	return GateCheck{Allowed: true}, nil
}

func allowlistContains(list []string, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, h := range list {
		if strings.ToLower(strings.TrimSpace(h)) == host {
			return true
		}
	}
	return false
}

// businessDayStart 返回"今天"（业务日，Asia/Shanghai 固定偏移）在 UTC 下的
// 起点时刻，用于预算计数窗口。**不复用 finance.BusinessDayAt**：那个函数
// 返回的是"日历日在 UTC 零点的字面表示"（供 date 列存储），不是真实时区
// 边界的那个时刻——直接拿它去比较 timestamptz 会算错时区偏移。这里需要的是
// 真实的"业务日零点"瞬间，因此自己按同一个固定时区常量重新算一遍。
func businessDayStart(now time.Time) time.Time {
	loc := businessDayLocation()
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
}

// businessDayLocation 与 finance.DefaultBusinessDayLocation 使用同一个固定
// 偏移（Asia/Shanghai，UTC+8，FixedZone 而不是 time.LoadLocation：不依赖
// 容器里是否装了 tzdata，见 finance/account.go 的同款注释）。不直接调用
// finance 包的函数只是为了不给本包新增一个跨领域包依赖，偏移量本身必须
// 与 finance 保持一致（两处都是字面量 8*3600，任何一处改动都需要同步）。
func businessDayLocation() *time.Location {
	return time.FixedZone("CST", 8*3600)
}

func (s *Store) countBudgetUsedToday(ctx context.Context, platform, environment string) (int, error) {
	return s.countBudgetUsedTodayExcluding(ctx, platform, environment, "")
}

// countBudgetUsedTodayExcluding 是 countBudgetUsedToday 的通用版本：
// excludeRunID 非空时，那一条 run 自己不计入统计（供 RecheckAtExecution
// 排除掉正在被复检的这条 run 本身，见该方法的文档注释）。空字符串永远不会
// 匹配任何真实的 uuid 主键，因此 excludeRunID=="" 时这个条件对结果没有
// 任何影响，等价于原先不带排除的查询。
func (s *Store) countBudgetUsedTodayExcluding(ctx context.Context, platform, environment, excludeRunID string) (int, error) {
	cutoff := businessDayStart(s.now().UTC())
	var count int
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM assurance.probe_run
WHERE platform = $1 AND environment = $2 AND status NOT IN ('refused', 'cancelled') AND created_at >= $3
  AND id::text <> $4`,
		platform, environment, cutoff, excludeRunID).Scan(&count)
	if err != nil {
		return 0, storeErr("count_budget", err)
	}
	return count, nil
}

func (s *Store) lastNonRefusedRunAt(ctx context.Context, declarationID string) (*time.Time, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx, `
SELECT max(created_at) FROM assurance.probe_run WHERE declaration_id = $1 AND status <> 'refused'`,
		declarationID).Scan(&at)
	if err != nil {
		return nil, storeErr("last_non_refused_run", err)
	}
	if at != nil {
		t := at.UTC()
		at = &t
	}
	return at, nil
}

func (s *Store) hasInProgressRun(ctx context.Context, platform, environment string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM assurance.probe_run
    WHERE platform = $1 AND environment = $2 AND status IN ('pending', 'running'))`,
		platform, environment).Scan(&exists)
	if err != nil {
		return false, storeErr("in_progress", err)
	}
	return exists, nil
}

// ---------------------------------------------------------------------------
// 批次（probe_run）
// ---------------------------------------------------------------------------

const runColumns = `
id, declaration_id, platform, environment, trigger, requested_by, client_run_key,
status, refusal_reason, action_run_id, started_at, finished_at, created_at`

func scanRun(row rowScanner) (Run, error) {
	var r Run
	var requestedBy, clientRunKey, refusalReason, actionRunID *string
	if err := row.Scan(
		&r.ID, &r.DeclarationID, &r.Platform, &r.Environment, &r.Trigger, &requestedBy, &clientRunKey,
		&r.Status, &refusalReason, &actionRunID, &r.StartedAt, &r.FinishedAt, &r.CreatedAt,
	); err != nil {
		return Run{}, err
	}
	if requestedBy != nil {
		r.RequestedBy = *requestedBy
	}
	if clientRunKey != nil {
		r.ClientRunKey = *clientRunKey
	}
	if refusalReason != nil {
		r.RefusalReason = *refusalReason
	}
	if actionRunID != nil {
		r.ActionRunID = *actionRunID
	}
	r.CreatedAt = r.CreatedAt.UTC()
	if r.StartedAt != nil {
		t := r.StartedAt.UTC()
		r.StartedAt = &t
	}
	if r.FinishedAt != nil {
		t := r.FinishedAt.UTC()
		r.FinishedAt = &t
	}
	return r, nil
}

// EvaluateRunInput 是触发一次探测的入参（run@1 的仓储层）。
type EvaluateRunInput struct {
	DeclarationID string
	ClientRunKey  string
	RequestedBy   string
	Trigger       string
}

// EvaluateAndCreateRun 是 assurance.probe.run@1 Handler 的核心：按设计稿
// §2.4 的顺序判定，写一条 pending（并入队 Job）或 refused 的 probe_run 行，
// 永远返回一个 Run（"决定不跑"本身是一次成功执行，不是 Action 层错误）。
func (s *Store) EvaluateAndCreateRun(ctx context.Context, in EvaluateRunInput) (Run, error) {
	declarationID := strings.TrimSpace(in.DeclarationID)
	if declarationID == "" {
		return Run{}, fmt.Errorf("declaration_id 为空: %w", ErrInvalidInput)
	}
	in.DeclarationID = declarationID

	if key := strings.TrimSpace(in.ClientRunKey); key != "" {
		existing, err := s.findActiveRunByClientKey(ctx, declarationID, key)
		if err != nil {
			return Run{}, err
		}
		if existing != nil {
			return *existing, nil
		}
	}

	decl, err := s.GetDeclaration(ctx, declarationID)
	if err != nil {
		return Run{}, err
	}
	gate, _, err := s.checkGates(ctx, decl)
	if err != nil {
		return Run{}, err
	}
	if !gate.Allowed {
		return s.insertRefusedRun(ctx, decl, in, gate.Reason)
	}
	return s.insertPendingRunAndEnqueue(ctx, decl, in)
}

func (s *Store) findActiveRunByClientKey(ctx context.Context, declarationID, clientRunKey string) (*Run, error) {
	row := s.pool.QueryRow(ctx, `
SELECT`+runColumns+` FROM assurance.probe_run
WHERE declaration_id = $1 AND client_run_key = $2 AND status IN ('pending', 'running')
ORDER BY created_at DESC LIMIT 1`, declarationID, clientRunKey)
	r, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storeErr("find_by_client_run_key", err)
	}
	return &r, nil
}

func nullableString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func (s *Store) insertRefusedRun(ctx context.Context, decl Declaration, in EvaluateRunInput, reason string) (Run, error) {
	now := s.now().UTC()
	row := s.pool.QueryRow(ctx, `
INSERT INTO assurance.probe_run
    (declaration_id, platform, environment, trigger, requested_by, client_run_key,
     status, refusal_reason, started_at, finished_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, 'refused', $7, NULL, $8, $8)
RETURNING`+runColumns,
		decl.ID, decl.Platform, decl.Environment, in.Trigger, nullableString(in.RequestedBy),
		nullableString(in.ClientRunKey), reason, now)
	r, err := scanRun(row)
	if err != nil {
		return Run{}, storeErr("insert_refused_run", err)
	}
	return r, nil
}

// insertPendingRunAndEnqueue 在同一个事务里插入 pending 的 probe_run 行并
// InsertTx 入队 assurance_probe Job——要么两者都生效，要么都不生效，避免
// "库里有一条 pending 但 River 里没有对应任务"的孤儿行。
func (s *Store) insertPendingRunAndEnqueue(ctx context.Context, decl Declaration, in EvaluateRunInput) (Run, error) {
	if s.jobs == nil {
		return Run{}, fmt.Errorf("assurance: 本 Store 未装配 JobEnqueuer（大概率用的是 worker 侧只读构造）: %w", ErrInvalidInput)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Run{}, storeErr("begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := s.now().UTC()
	row := tx.QueryRow(ctx, `
INSERT INTO assurance.probe_run
    (declaration_id, platform, environment, trigger, requested_by, client_run_key, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7)
RETURNING`+runColumns,
		decl.ID, decl.Platform, decl.Environment, in.Trigger, nullableString(in.RequestedBy),
		nullableString(in.ClientRunKey), now)
	run, err := scanRun(row)
	if err != nil {
		return Run{}, storeErr("insert_pending_run", err)
	}

	opts := ProbeArgs{}.InsertOpts()
	if _, err := s.jobs.InsertTx(ctx, tx, ProbeArgs{RunID: run.ID}, &opts); err != nil {
		return Run{}, storeErr("enqueue", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, storeErr("commit", err)
	}
	return run, nil
}

// GetRun 按 id 读取一条批次。
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.pool.QueryRow(ctx, `SELECT`+runColumns+` FROM assurance.probe_run WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, storeErr("get_run", err)
	}
	return r, nil
}

// LatestRunForDeclaration 读取某声明最近一次批次（用于检测任务表的
// last_run_*）；从未跑过返回 (nil, nil)。
func (s *Store) LatestRunForDeclaration(ctx context.Context, declarationID string) (*Run, error) {
	row := s.pool.QueryRow(ctx, `
SELECT`+runColumns+` FROM assurance.probe_run WHERE declaration_id = $1
ORDER BY created_at DESC LIMIT 1`, declarationID)
	r, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storeErr("latest_run", err)
	}
	return &r, nil
}

// MarkRunRunning 把批次标为 running（Job Work() 开始执行时）。
func (s *Store) MarkRunRunning(ctx context.Context, id string, startedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE assurance.probe_run SET status = 'running', started_at = $2 WHERE id = $1`,
		id, startedAt.UTC())
	if err != nil {
		return storeErr("mark_running", err)
	}
	return nil
}

// MarkRunRefused 把批次标为 refused（Job 执行时刻竞态复检命中，见设计稿
// §3.2 步骤 2；reason 应带 race_ 前缀，由调用方——jobs/assurance_probe.go
// ——负责拼接）。
func (s *Store) MarkRunRefused(ctx context.Context, id, reason string, finishedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE assurance.probe_run SET status = 'refused', refusal_reason = $2, finished_at = $3 WHERE id = $1`,
		id, reason, finishedAt.UTC())
	if err != nil {
		return storeErr("mark_refused", err)
	}
	return nil
}

// MarkRunCancelled 把批次标为 cancelled——本片唯一会走到这里的路径是 Job
// 执行时刻发现声明已被撤销（见 doc.go RunStatusCancelled 的注释：没有任何
// Action 能直接撤销一个已入队的 run，那个"取消"作用于声明本身）。
func (s *Store) MarkRunCancelled(ctx context.Context, id, reason string, finishedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE assurance.probe_run SET status = 'cancelled', refusal_reason = $2, finished_at = $3 WHERE id = $1`,
		id, reason, finishedAt.UTC())
	if err != nil {
		return storeErr("mark_cancelled", err)
	}
	return nil
}

// MarkRunSucceeded 把批次标为 succeeded——**指"批次跑完并写全了结果"，不
// 代表所有渠道都健康**，与 connector_probe.probeOK 的措辞纪律一致。
func (s *Store) MarkRunSucceeded(ctx context.Context, id string, finishedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE assurance.probe_run SET status = 'succeeded', finished_at = $2 WHERE id = $1`,
		id, finishedAt.UTC())
	if err != nil {
		return storeErr("mark_succeeded", err)
	}
	return nil
}

// MarkRunFailed 把批次标为 failed——只在**存储写失败**（不是探测本身的失败）
// 时使用，见设计稿 §3.2 步骤 7。
func (s *Store) MarkRunFailed(ctx context.Context, id string, finishedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE assurance.probe_run SET status = 'failed', finished_at = $2 WHERE id = $1`,
		id, finishedAt.UTC())
	if err != nil {
		return storeErr("mark_failed", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 结果（probe_result）
// ---------------------------------------------------------------------------

// InsertResult 写入一条探测结果，返回带 ID/EvidenceRef/CreatedAt 的完整行。
func (s *Store) InsertResult(ctx context.Context, r Result) (Result, error) {
	id := uuid.New().String()
	r.ID = id
	r.EvidenceRef = evidenceRefFor(id)
	now := s.now().UTC()
	r.CreatedAt = now
	_, err := s.pool.Exec(ctx, `
INSERT INTO assurance.probe_result
    (id, run_id, channel_id, external_channel_id, model, status, verdict, latency_ms, first_token_ms,
     measured_first_token, tokens_used, http_status, error_kind, evidence_ref, observed_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		r.ID, r.RunID, r.ChannelID, nullableString(r.ExternalChannelID), r.Model, r.Status, r.Verdict,
		r.LatencyMS, r.FirstTokenMS, r.MeasuredFirstToken, r.TokensUsed, r.HTTPStatus,
		nullableString(r.ErrorKind), r.EvidenceRef, r.ObservedAt.UTC(), now)
	if err != nil {
		return Result{}, storeErr("insert_result", err)
	}
	return r, nil
}

// evidenceRefFor 从结果行的 UUID 派生一个短小的展示态证据编号（设计稿
// §1.4："形如 probe-8842"）。取 UUID 去掉连字符后的前 8 位十六进制——
// 确定性地从主键派生，不需要额外一次序列/计数查询，碰撞的后果只是展示态
// 编号重复（不影响任何唯一性约束，evidence_ref 不是主键）。
func evidenceRefFor(id string) string {
	compact := strings.ReplaceAll(id, "-", "")
	if len(compact) > 8 {
		compact = compact[:8]
	}
	return "probe-" + compact
}

// LatestBenchmarkScore 读取某 (channel_id, model) 上一次基准题集探测的得分
// （供 templates.Assess 的"与上一次分数比较"）；没有可比较的历史返回
// (nil, nil)。只看非 refused 批次产出的结果——refused 的批次从未真正探测过。
func (s *Store) LatestBenchmarkScore(ctx context.Context, channelID, model string) (*int, error) {
	rows, err := s.pool.Query(ctx, `
SELECT pr.verdict FROM assurance.probe_result pr
JOIN assurance.probe_run run ON run.id = pr.run_id
WHERE pr.channel_id = $1 AND pr.model = $2 AND run.status <> 'refused'
ORDER BY pr.created_at DESC LIMIT 20`, channelID, model)
	if err != nil {
		return nil, storeErr("latest_benchmark_score", err)
	}
	defer rows.Close()
	for rows.Next() {
		var verdict string
		if err := rows.Scan(&verdict); err != nil {
			return nil, storeErr("scan_benchmark_verdict", err)
		}
		if score, ok := ParseBenchmarkScore(verdict); ok {
			v := score
			return &v, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, storeErr("rows_benchmark_verdict", err)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// 历史记录（供 service.go 的 ProbeHistory）
// ---------------------------------------------------------------------------

// HistoryEntry 是一条"主动检测历史"行（探测结果 + 所属批次/声明的展示态
// 上下文）。
type HistoryEntry struct {
	Result
	DeclarationName   string
	PromptTemplateKey string
}

// DefaultProbeHistoryLimit / MaxProbeHistoryLimit 是历史记录分页的缺省/上限
// 条数。
const (
	DefaultProbeHistoryLimit = 50
	MaxProbeHistoryLimit     = 200
)

// ListProbeHistory 按时间倒序分页读取某平台+环境的主动探测历史（设计稿
// §5.2）。cursor 非 nil 时只返回 created_at < *cursor 的行（配合前端"加载
// 更多"传回上一页最后一行的 created_at）。
func (s *Store) ListProbeHistory(
	ctx context.Context, platform, environment string, cursor *time.Time, limit int,
) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = DefaultProbeHistoryLimit
	}
	if limit > MaxProbeHistoryLimit {
		limit = MaxProbeHistoryLimit
	}
	cutoff := time.Now().UTC()
	if cursor != nil {
		cutoff = cursor.UTC()
	}
	rows, err := s.pool.Query(ctx, `
SELECT pr.id, pr.run_id, pr.channel_id, pr.external_channel_id, pr.model, pr.status, pr.verdict,
       pr.latency_ms, pr.first_token_ms, pr.measured_first_token, pr.tokens_used, pr.http_status,
       pr.error_kind, pr.evidence_ref, pr.observed_at, pr.created_at,
       d.name, d.prompt_template_key
FROM assurance.probe_result pr
JOIN assurance.probe_run run ON run.id = pr.run_id
JOIN assurance.probe_declaration d ON d.id = run.declaration_id
WHERE run.platform = $1 AND run.environment = $2 AND pr.created_at < $3
ORDER BY pr.created_at DESC
LIMIT $4`, platform, environment, cutoff, limit)
	if err != nil {
		return nil, storeErr("list_probe_history", err)
	}
	defer rows.Close()
	out := make([]HistoryEntry, 0)
	for rows.Next() {
		var e HistoryEntry
		var externalChannelID, errorKind *string
		if err := rows.Scan(
			&e.ID, &e.RunID, &e.ChannelID, &externalChannelID, &e.Model, &e.Status, &e.Verdict,
			&e.LatencyMS, &e.FirstTokenMS, &e.MeasuredFirstToken, &e.TokensUsed, &e.HTTPStatus,
			&errorKind, &e.EvidenceRef, &e.ObservedAt, &e.CreatedAt,
			&e.DeclarationName, &e.PromptTemplateKey,
		); err != nil {
			return nil, storeErr("scan_probe_history", err)
		}
		if externalChannelID != nil {
			e.ExternalChannelID = *externalChannelID
		}
		if errorKind != nil {
			e.ErrorKind = *errorKind
		}
		e.ObservedAt = e.ObservedAt.UTC()
		e.CreatedAt = e.CreatedAt.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, storeErr("rows_probe_history", err)
	}
	return out, nil
}

// ResultsForRun 读取一个批次的全部逐渠道/模型结果（供检测任务表汇总
// last_run_status/last_run_verdict）。
func (s *Store) ResultsForRun(ctx context.Context, runID string) ([]Result, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, run_id, channel_id, external_channel_id, model, status, verdict, latency_ms, first_token_ms,
       measured_first_token, tokens_used, http_status, error_kind, evidence_ref, observed_at, created_at
FROM assurance.probe_result WHERE run_id = $1 ORDER BY created_at`, runID)
	if err != nil {
		return nil, storeErr("results_for_run", err)
	}
	defer rows.Close()
	out := make([]Result, 0)
	for rows.Next() {
		var r Result
		var externalChannelID, errorKind *string
		if err := rows.Scan(
			&r.ID, &r.RunID, &r.ChannelID, &externalChannelID, &r.Model, &r.Status, &r.Verdict,
			&r.LatencyMS, &r.FirstTokenMS, &r.MeasuredFirstToken, &r.TokensUsed, &r.HTTPStatus,
			&errorKind, &r.EvidenceRef, &r.ObservedAt, &r.CreatedAt,
		); err != nil {
			return nil, storeErr("scan_result", err)
		}
		if externalChannelID != nil {
			r.ExternalChannelID = *externalChannelID
		}
		if errorKind != nil {
			r.ErrorKind = *errorKind
		}
		r.ObservedAt = r.ObservedAt.UTC()
		r.CreatedAt = r.CreatedAt.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, storeErr("rows_results", err)
	}
	return out, nil
}

// ChannelDisplayNames 解析一批 channel_id 的展示名（用于检测任务表的
// channel_names 列），查不到名字时原样回退成 id。
func (s *Store) ChannelDisplayNames(ctx context.Context, platform, environment string, ids []string) ([]string, error) {
	catalog, err := s.channelCatalog(ctx, platform, environment)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := catalog[id]; ok {
			out = append(out, name)
		} else {
			out = append(out, id)
		}
	}
	return out, nil
}

// businessDayLabel 是给日志/测试用的业务日字符串（YYYY-MM-DD），供测试断言
// countBudgetUsedToday 的窗口口径是否正确（见 store_test.go）。
func businessDayLabel(now time.Time) string {
	return businessDayStart(now).In(businessDayLocation()).Format("2006-01-02")
}
