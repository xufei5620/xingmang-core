package action

import (
	"context"
	"fmt"
	"log/slog"

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
		out = append(out, Run{
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
		})
	}
	return out, nil
}
