package action

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action/gen"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// PgRunStore 把 ActionRun 写入 PostgreSQL（append-only，规格 §4.4）。
type PgRunStore struct {
	q      *gen.Queries
	logger *slog.Logger
}

// NewPgRunStore 创建持久化实现。logger 用于记录审计写入失败——
// 审计写不进去是严重事件，必须可见，不能静默。
func NewPgRunStore(pool *pgxpool.Pool, logger *slog.Logger) *PgRunStore {
	return &PgRunStore{q: gen.New(pool), logger: logger}
}

// InsertRun 写入一条执行记录。
func (s *PgRunStore) InsertRun(ctx context.Context, r Run) error {
	err := s.q.InsertActionRun(ctx, gen.InsertActionRunParams{
		ID:            r.ID,
		ActionID:      r.ActionID,
		ActionVersion: r.ActionVersion,
		PrincipalID:   r.PrincipalID,
		PrincipalType: string(r.PrincipalType),
		Environment:   r.Environment,
		RequestID:     r.RequestID,
		RiskLevel:     string(r.RiskLevel),
		Status:        string(r.Status),
		ErrorCode:     string(r.ErrorCode),
		DurationMs:    r.DurationMS,
		StartedAt:     pgtype.Timestamptz{Time: r.StartedAt.UTC(), Valid: true},
		FinishedAt:    pgtype.Timestamptz{Time: r.FinishedAt.UTC(), Valid: true},
	})
	if err != nil {
		s.logger.Error("ActionRun 写入失败（审计缺口）",
			slog.String("module", "action"),
			slog.String("action_id", r.ActionID),
			slog.String("action_run_id", r.ID.String()),
			slog.String("request_id", r.RequestID),
			slog.String("error_code", "audit_write_failed"),
			slog.Any("err", err),
		)
		return fmt.Errorf("insert action run: %w", err)
	}
	return nil
}

// ListRunsByAction 读取某 Action 的最近执行记录（供审计查询与测试断言）。
func (s *PgRunStore) ListRunsByAction(ctx context.Context, actionID string, limit int32) ([]Run, error) {
	rows, err := s.q.ListActionRunsByAction(ctx, gen.ListActionRunsByActionParams{
		ActionID: actionID,
		Limit:    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list action runs: %w", err)
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, runFromRow(r))
	}
	return out, nil
}

// 跨 Action 执行记录分页（XM-ACTIONS0：操作与审批页「执行记录」子页签）的
// 页大小边界。与 audit.DefaultListLimit / MaxListLimit 同一思路：分页参数
// 不是安全边界，超出一律夹到上限而不是报错。
const (
	DefaultRunListLimit int32 = 50
	MaxRunListLimit     int32 = 100
)

// RunFilter 是跨 Action 执行记录查询的过滤条件。
type RunFilter struct {
	// Environment 必填：调用方（httpapi 层）必须填 Principal 的环境，不接受
	// 调用方传任意环境——与审计事件、请求详情同一条纪律（规格 §20.5：生产
	// 权限不继承，环境不是一个调用方可选的查询参数）。
	Environment string
	ActionID    string // 空串 = 不过滤
	Status      string // 空串 = 不过滤；非空须是 succeeded / failed
	PrincipalID string // 空串 = 不过滤
	Limit       int32  // <=0 用 DefaultRunListLimit；超过 MaxRunListLimit 夹到上限
	// Cursor 空串 = 首页；否则是上一页 RunPage.NextCursor 原样传回。
	// 不透明字符串，调用方不解析、不构造（同 reqlog 游标的纪律）。
	Cursor string
}

// RunPage 是一页跨 Action 执行记录。
type RunPage struct {
	Items []Run
	// NextCursor 为空串表示已经翻到底。
	NextCursor string
}

// runCursorSeparator 把 (started_at, id) 拼进一个游标字符串。用竖线而不是
// 依赖固定宽度：RFC3339Nano 的时间戳不含竖线，UUID 的字符集也不含，两段
// 天然不会互相吞掉边界。
const runCursorSeparator = "|"

// encodeRunCursor / decodeRunCursor 实现 (started_at, id) 复合 keyset 游标。
//
// 为什么不能只用 started_at：时间戳可能相同（同一进程内连续两次 Execute
// 落在同一微秒），单独当游标会像 audit.ListRecentAuditEvents 修复前那样
// 翻页重复或漏读（见 db/queries/audit.sql 的说明）。action_run 没有
// audit_event 那种全局唯一递增的 sequence 列，但 id 是 UUID 主键，
// (started_at DESC, id DESC) 仍是一个严格全序，足以做 keyset 分页。
func encodeRunCursor(startedAt time.Time, id uuid.UUID) string {
	raw := startedAt.UTC().Format(time.RFC3339Nano) + runCursorSeparator + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeRunCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor 不是合法的 base64: %w", err)
	}
	parts := strings.SplitN(string(raw), runCursorSeparator, 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor 缺少分隔符")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor 时间戳不合法: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor id 不合法: %w", err)
	}
	return t.UTC(), id, nil
}

// ListRuns 跨 Action 分页读取执行记录（XM-ACTIONS0）。
//
// 走 (environment, started_at DESC, id DESC) 复合索引（迁移 000022），理由
// 与 audit_event_environment_sequence_idx（迁移 000006）相同：没有它时，
// 稀疏环境的一页要沿全局索引倒扫直到凑够 limit 行。
func (s *PgRunStore) ListRuns(ctx context.Context, filter RunFilter) (RunPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultRunListLimit
	}
	if limit > MaxRunListLimit {
		limit = MaxRunListLimit
	}

	var hasCursor bool
	var beforeStartedAt pgtype.Timestamptz
	var beforeID uuid.UUID
	if filter.Cursor != "" {
		t, id, err := decodeRunCursor(filter.Cursor)
		if err != nil {
			return RunPage{}, NewError(CodeInvalidParams, "cursor 无效", err)
		}
		hasCursor = true
		beforeStartedAt = pgtype.Timestamptz{Time: t, Valid: true}
		beforeID = id
	}

	rows, err := s.q.ListActionRuns(ctx, gen.ListActionRunsParams{
		Environment:     filter.Environment,
		ActionID:        filter.ActionID,
		Status:          filter.Status,
		PrincipalID:     filter.PrincipalID,
		HasCursor:       hasCursor,
		BeforeStartedAt: beforeStartedAt,
		BeforeID:        beforeID,
		RowLimit:        limit,
	})
	if err != nil {
		return RunPage{}, fmt.Errorf("list action runs: %w", err)
	}

	items := make([]Run, 0, len(rows))
	for _, r := range rows {
		items = append(items, runFromRow(r))
	}
	var next string
	if int32(len(items)) == limit && len(items) > 0 {
		last := items[len(items)-1]
		next = encodeRunCursor(last.StartedAt, last.ID)
	}
	return RunPage{Items: items, NextCursor: next}, nil
}

// GetRun 按 ID 读取单条执行记录（供执行记录详情页，XM-ACTIONS0）。
// 不存在返回 (zero, false, nil)——调用方据此回 404 而不是 500。
func (s *PgRunStore) GetRun(ctx context.Context, id uuid.UUID) (Run, bool, error) {
	row, err := s.q.GetActionRunByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("get action run: %w", err)
	}
	return runFromRow(row), true, nil
}

func runFromRow(r gen.ActionActionRun) Run {
	return Run{
		ID:            r.ID,
		ActionID:      r.ActionID,
		ActionVersion: r.ActionVersion,
		PrincipalID:   r.PrincipalID,
		PrincipalType: principal.Type(r.PrincipalType),
		Environment:   r.Environment,
		RequestID:     r.RequestID,
		RiskLevel:     RiskLevel(r.RiskLevel),
		Status:        RunStatus(r.Status),
		ErrorCode:     Code(r.ErrorCode),
		DurationMS:    r.DurationMs,
		StartedAt:     r.StartedAt.Time.UTC(),
		FinishedAt:    r.FinishedAt.Time.UTC(),
	}
}
