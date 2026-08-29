package finance

// Runway 阈值的运行时快照与生命周期 bootstrap（XM-C-RUNWAY0）。
//
// 这里刻意没有通用 Config Store：阈值同时被看板和告警消费，必须在 finance
// 域内按 environment 读取同一 revision。环境变量只在一次性 bootstrap 时使用，
// Current 永远不会在数据库缺行时偷偷回落默认值。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
)

var (
	// ErrRunwayConfigUnavailable 表示当前环境没有可信的阈值快照。
	// HTTP 层会把它映射为稳定的 503；数据库根因只留在服务端错误链。
	ErrRunwayConfigUnavailable = errors.New("finance: runway threshold config unavailable")
	// ErrRunwayBootstrapConflict 表示 bootstrap 发现已有不同的快照，拒绝覆盖。
	ErrRunwayBootstrapConflict = errors.New("finance: runway threshold bootstrap conflict")
)

// RunwayThresholdSnapshot 是 API/worker 在一个工作单元内复用的不可变快照。
type RunwayThresholdSnapshot struct {
	Environment string
	Thresholds  RunwayThresholds
	Revision    int64
	Source      string
	UpdatedAt   time.Time
	UpdatedBy   string
	Reason      string
	RequestID   string
}

// RunwayThresholdProvider 是 API 请求或 worker 评估轮次的唯一读入口。
type RunwayThresholdProvider interface {
	Current(context.Context, string) (RunwayThresholdSnapshot, error)
}

// BootstrapRunwayThresholdInput 是生命周期命令的输入；不接受调用方提供的
// source/environment 之外的任何运行时覆盖。
type BootstrapRunwayThresholdInput struct {
	Environment string
	Thresholds  RunwayThresholds
	Actor       string
	Reason      string
	RequestID   string
}

// RunwayThresholdHistoryEntry 是只读历史行的领域投影。
type RunwayThresholdHistoryEntry struct {
	Environment  string
	Revision     int64
	Thresholds   RunwayThresholds
	ChangedAt    time.Time
	ChangedBy    string
	Reason       string
	RequestID    string
	ChangeSource string
}

// RunwayThresholdHistoryQuery 是历史 Query 的分页条件。
type RunwayThresholdHistoryQuery struct {
	Environment    string
	BeforeRevision int64
	Limit          int
}

// RunwayThresholdHistoryLister 供 HTTP 层读取历史，不暴露 SQL 生成类型。
type RunwayThresholdHistoryLister interface {
	ListHistory(context.Context, RunwayThresholdHistoryQuery) ([]RunwayThresholdHistoryEntry, error)
}

// RunwayThresholdStore 是 PostgreSQL 运行时快照与 bootstrap 实现。
type RunwayThresholdStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	now  func() time.Time
}

func NewRunwayThresholdStore(pool *pgxpool.Pool, now func() time.Time) *RunwayThresholdStore {
	return newRunwayThresholdStore(pool, now)
}

// NewRunwayThresholdCurrentStore builds the worker-safe provider. Worker DB
// roles are intentionally granted SELECT only on the verified current view;
// the view performs the current/history match inside PostgreSQL, so a worker
// evaluation never needs direct history SELECT while orphan rows still fail
// closed.
func NewRunwayThresholdCurrentStore(pool *pgxpool.Pool, now func() time.Time) *RunwayThresholdStore {
	return newRunwayThresholdStore(pool, now)
}

func newRunwayThresholdStore(pool *pgxpool.Pool, now func() time.Time) *RunwayThresholdStore {
	if now == nil {
		now = time.Now
	}
	return &RunwayThresholdStore{pool: pool, q: gen.New(pool), now: now}
}

func snapshotFromValues(environment string, criticalDays, warningDays, seriousDays int32, revision int64, updatedAt pgtype.Timestamptz, updatedBy, reason, requestID string) RunwayThresholdSnapshot {
	return RunwayThresholdSnapshot{
		Environment: environment,
		Thresholds: RunwayThresholds{
			CriticalDays: int(criticalDays),
			WarningDays:  int(warningDays),
			SeriousDays:  int(seriousDays),
		},
		Revision:  revision,
		Source:    "database",
		UpdatedAt: fromTS(updatedAt),
		UpdatedBy: updatedBy,
		Reason:    reason,
		RequestID: requestID,
	}
}

func snapshotFromConfig(row gen.FinanceRunwayThresholdConfig) RunwayThresholdSnapshot {
	return snapshotFromValues(row.Environment, row.CriticalDays, row.WarningDays, row.SeriousDays, row.Revision, row.UpdatedAt, row.UpdatedBy, row.Reason, row.RequestID)
}

func snapshotFromVerified(row gen.FinanceRunwayThresholdCurrentVerified) RunwayThresholdSnapshot {
	return snapshotFromValues(row.Environment, row.CriticalDays, row.WarningDays, row.SeriousDays, row.Revision, row.UpdatedAt, row.UpdatedBy, row.Reason, row.RequestID)
}

func historyFromRow(row gen.FinanceRunwayThresholdHistory) RunwayThresholdHistoryEntry {
	return RunwayThresholdHistoryEntry{
		Environment: row.Environment,
		Revision:    row.Revision,
		Thresholds: RunwayThresholds{
			CriticalDays: int(row.CriticalDays),
			WarningDays:  int(row.WarningDays),
			SeriousDays:  int(row.SeriousDays),
		},
		ChangedAt:    fromTS(row.ChangedAt),
		ChangedBy:    row.ChangedBy,
		Reason:       row.Reason,
		RequestID:    row.RequestID,
		ChangeSource: row.ChangeSource,
	}
}

// currentHistoryMatches verifies the cross-table invariant that the runtime
// snapshot and its immutable evidence row describe the same revision and
// thresholds. The schema constrains each table independently; it cannot add a
// foreign key from current to history without making the atomic insert/update
// order impossible. Keep the invariant at the read boundary as a second line
// of defence so an orphan or manually-corrupted current row never becomes a
// trusted classifier input.
func currentHistoryMatches(current gen.FinanceRunwayThresholdConfig, history gen.FinanceRunwayThresholdHistory) bool {
	return current.Environment == history.Environment &&
		current.Revision == history.Revision &&
		current.CriticalDays == history.CriticalDays &&
		current.WarningDays == history.WarningDays &&
		current.SeriousDays == history.SeriousDays
}

func validateRunwayEnvironment(environment string) error {
	if strings.TrimSpace(environment) == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	return nil
}

// Current 只读当前快照。缺行与任意数据库错误都收敛到同一个领域错误，
// 调用方不能把缺行误读成默认 5/10/20。
func (s *RunwayThresholdStore) Current(ctx context.Context, environment string) (RunwayThresholdSnapshot, error) {
	if err := validateRunwayEnvironment(environment); err != nil {
		return RunwayThresholdSnapshot{}, err
	}
	row, err := s.q.GetRunwayThresholdConfigVerified(ctx, strings.TrimSpace(environment))
	if errors.Is(err, pgx.ErrNoRows) {
		return RunwayThresholdSnapshot{}, ErrRunwayConfigUnavailable
	}
	if err != nil {
		return RunwayThresholdSnapshot{}, fmt.Errorf("%w: read runway threshold snapshot: %w", ErrRunwayConfigUnavailable, err)
	}
	snapshot := snapshotFromVerified(row)
	if err := snapshot.Thresholds.Validate(); err != nil {
		return RunwayThresholdSnapshot{}, fmt.Errorf("%w: invalid stored threshold snapshot", ErrRunwayConfigUnavailable)
	}
	return snapshot, nil
}

// Bootstrap 在一个事务里写 current + history。重复调用同一组三档是幂等的；
// 已存在但不同则拒绝覆盖，避免部署脚本悄悄改掉人工调整过的配置。
func (s *RunwayThresholdStore) Bootstrap(
	ctx context.Context, in BootstrapRunwayThresholdInput,
) (RunwayThresholdSnapshot, error) {
	in.Environment = strings.TrimSpace(in.Environment)
	in.Actor = strings.TrimSpace(in.Actor)
	in.Reason = strings.TrimSpace(in.Reason)
	in.RequestID = strings.TrimSpace(in.RequestID)
	if err := validateRunwayEnvironment(in.Environment); err != nil {
		return RunwayThresholdSnapshot{}, err
	}
	if err := in.Thresholds.Validate(); err != nil {
		return RunwayThresholdSnapshot{}, err
	}
	if in.Actor == "" || in.Reason == "" || in.RequestID == "" {
		return RunwayThresholdSnapshot{}, fmt.Errorf("bootstrap actor/reason/request_id: %w", ErrMissingField)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RunwayThresholdSnapshot{}, fmt.Errorf("begin runway bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	now := s.now().UTC()
	row, err := q.InsertRunwayThresholdBootstrap(ctx, gen.InsertRunwayThresholdBootstrapParams{
		Environment:  in.Environment,
		CriticalDays: int32(in.Thresholds.CriticalDays),
		WarningDays:  int32(in.Thresholds.WarningDays),
		SeriousDays:  int32(in.Thresholds.SeriousDays),
		UpdatedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedBy:    in.Actor,
		Reason:       in.Reason,
		RequestID:    in.RequestID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := q.GetRunwayThresholdConfig(ctx, in.Environment)
		if getErr != nil {
			return RunwayThresholdSnapshot{}, fmt.Errorf("%w: inspect existing bootstrap: %w", ErrRunwayConfigUnavailable, getErr)
		}
		history, historyErr := q.GetRunwayThresholdHistory(ctx, gen.GetRunwayThresholdHistoryParams{
			Environment: in.Environment, Revision: existing.Revision,
		})
		if errors.Is(historyErr, pgx.ErrNoRows) {
			// current/history 不成对时不能把「已有 current」误报成幂等成功；
			// 这通常意味着上次 lifecycle 事务被外部破坏，应停下来人工核对。
			return RunwayThresholdSnapshot{}, fmt.Errorf("%w: existing snapshot has no history row: %w", ErrRunwayConfigUnavailable, historyErr)
		}
		if historyErr != nil {
			return RunwayThresholdSnapshot{}, fmt.Errorf("%w: inspect existing bootstrap history: %w", ErrRunwayConfigUnavailable, historyErr)
		}
		got := snapshotFromConfig(existing)
		if got.Thresholds != in.Thresholds || history.CriticalDays != existing.CriticalDays ||
			history.WarningDays != existing.WarningDays || history.SeriousDays != existing.SeriousDays {
			return RunwayThresholdSnapshot{}, ErrRunwayBootstrapConflict
		}
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			return RunwayThresholdSnapshot{}, fmt.Errorf("rollback idempotent bootstrap: %w", err)
		}
		return got, nil
	}
	if err != nil {
		return RunwayThresholdSnapshot{}, fmt.Errorf("insert runway threshold config: %w", err)
	}
	if _, err := q.InsertRunwayThresholdHistory(ctx, gen.InsertRunwayThresholdHistoryParams{
		Environment:  in.Environment,
		Revision:     row.Revision,
		CriticalDays: row.CriticalDays,
		WarningDays:  row.WarningDays,
		SeriousDays:  row.SeriousDays,
		ChangedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		ChangedBy:    in.Actor,
		Reason:       in.Reason,
		RequestID:    in.RequestID,
		ChangeSource: "bootstrap",
	}); err != nil {
		return RunwayThresholdSnapshot{}, fmt.Errorf("insert runway threshold history: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RunwayThresholdSnapshot{}, fmt.Errorf("commit runway bootstrap: %w", err)
	}
	return snapshotFromConfig(row), nil
}

// ListHistory 返回稳定降序历史。调用方负责把 limit 限制在 HTTP 合同上限内。
func (s *RunwayThresholdStore) ListHistory(
	ctx context.Context, query RunwayThresholdHistoryQuery,
) ([]RunwayThresholdHistoryEntry, error) {
	if err := validateRunwayEnvironment(query.Environment); err != nil {
		return nil, err
	}
	if query.Limit <= 0 {
		return nil, fmt.Errorf("history limit: %w", ErrInvalidFormat)
	}
	rows, err := s.q.ListRunwayThresholdHistory(ctx, gen.ListRunwayThresholdHistoryParams{
		Environment: query.Environment, BeforeRevision: query.BeforeRevision, ResultLimit: int32(query.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: list runway threshold history: %w", ErrRunwayConfigUnavailable, err)
	}
	out := make([]RunwayThresholdHistoryEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, historyFromRow(row))
	}
	return out, nil
}
