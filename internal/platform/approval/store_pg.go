package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// ErrNotFound：审批单不存在（或不在本环境）。
var ErrNotFound = errors.New("approval request not found")

// Store 是审批单的持久化面。
//
// 拆成接口而不是直接用 PgStore：内核只需要「落单/取单/记票/标执行」这四件事，
// 拿接口能让内核的接线测试不必起容器。
type Store interface {
	Create(ctx context.Context, req Request) error
	Get(ctx context.Context, id uuid.UUID) (Request, error)
	// Vote 在**同一个事务里**追加一票并按 Policy 重算状态。
	// 票数判定必须与写票原子，否则两个人同时投最后一票会各自读到「还差一票」，
	// 谁也不把单推进 APPROVED（或者更糟，两次都推进）。
	Vote(ctx context.Context, id uuid.UUID, d Decision, policy Policy) (Request, error)
	// MarkExecuted 绑定 ActionRun 并落终态；execution_run_id 的 UNIQUE 让重复
	// 触发在库层撞上。
	MarkExecuted(ctx context.Context, id uuid.UUID, runID uuid.UUID, at time.Time) error
	Cancel(ctx context.Context, id uuid.UUID, requesterID string, at time.Time) error
	// ExpirePending 把过期的 PENDING 单批量落 EXPIRED，返回条数。由定时任务调用。
	ExpirePending(ctx context.Context, now time.Time) (int64, error)
	List(ctx context.Context, status Status, limit int) ([]Request, error)
}

// PgStore 是 Store 的 PostgreSQL 实现。
//
// environment 显式传入、不从参数读（宪法 15 条）：跨环境读写本就不允许，
// 让调用方指定环境等于把那道边界交给调用方守。
type PgStore struct {
	pool        *pgxpool.Pool
	environment string
	now         func() time.Time
}

func NewPgStore(pool *pgxpool.Pool, environment string, now func() time.Time) *PgStore {
	if now == nil {
		now = time.Now
	}
	return &PgStore{pool: pool, environment: environment, now: now}
}

var _ Store = (*PgStore)(nil)

const requestColumns = `id, action_id, action_version, params_json, params_hash, risk_level,
	environment, requester_id, requester_type, reason, status, expires_at,
	created_at, decided_at, executed_at, execution_run_id`

func (s *PgStore) Create(ctx context.Context, req Request) error {
	params, err := json.Marshal(req.Params)
	if err != nil {
		return fmt.Errorf("encode approval params: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO core.approval_request(`+requestColumns+`)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULL,NULL,NULL)`,
		req.ID, req.ActionID, req.ActionVersion, params, req.ParamsHash, req.RiskLevel,
		s.environment, req.RequesterID, string(req.RequesterType), req.Reason,
		string(StatusPending), req.ExpiresAt, req.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert approval request: %w", err)
	}
	return nil
}

func (s *PgStore) Get(ctx context.Context, id uuid.UUID) (Request, error) {
	return s.get(ctx, s.pool, id, false)
}

// rowQuerier 让 get 既能在池上跑，也能在事务里跑（Vote 需要后者）。
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *PgStore) get(ctx context.Context, q rowQuerier, id uuid.UUID, forUpdate bool) (Request, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	var req Request
	var params []byte
	var requesterType, status string
	err := q.QueryRow(ctx, `SELECT `+requestColumns+`
		FROM core.approval_request WHERE id=$1 AND environment=$2`+lock, id, s.environment).Scan(
		&req.ID, &req.ActionID, &req.ActionVersion, &params, &req.ParamsHash, &req.RiskLevel,
		&req.Environment, &req.RequesterID, &requesterType, &req.Reason, &status,
		&req.ExpiresAt, &req.CreatedAt, &req.DecidedAt, &req.ExecutedAt, &req.ExecutionRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("load approval request: %w", err)
	}
	req.RequesterType = principal.Type(requesterType)
	req.Status = Status(status)
	if err = json.Unmarshal(params, &req.Params); err != nil {
		return Request{}, fmt.Errorf("decode approval params: %w", err)
	}
	req.Decisions, err = s.decisions(ctx, q, id)
	if err != nil {
		return Request{}, err
	}
	return req, nil
}

func (s *PgStore) decisions(ctx context.Context, q rowQuerier, id uuid.UUID) ([]Decision, error) {
	rows, err := q.Query(ctx, `
		SELECT id, request_id, approver_id, approver_type, verdict, comment, privileged, created_at
		FROM core.approval_decision WHERE request_id=$1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("load approval decisions: %w", err)
	}
	defer rows.Close()
	out := make([]Decision, 0)
	for rows.Next() {
		var d Decision
		var approverType, verdict string
		if err = rows.Scan(&d.ID, &d.RequestID, &d.ApproverID, &approverType, &verdict,
			&d.Comment, &d.Privileged, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan approval decision: %w", err)
		}
		d.ApproverType = principal.Type(approverType)
		d.Verdict = Verdict(verdict)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Vote 追加一票并重算状态，全程一个事务、单行 FOR UPDATE。
//
// 并发是这里的要害：两个审批人同时投 L3 的第二票，如果各自先读后写，两边都会
// 读到「已有一票、还差一票」，于是谁也不把单推进 APPROVED。锁住单行让第二个
// 事务看到的是第一票已落库之后的状态。
func (s *PgStore) Vote(ctx context.Context, id uuid.UUID, d Decision, policy Policy) (Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, fmt.Errorf("begin approval vote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	req, err := s.get(ctx, tx, id, true)
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	// 领域规则在这里再走一遍：调用方可能没查，或查完到这里之间单已改变。
	approverPrincipal := principal.Principal{
		ID: d.ApproverID, Type: d.ApproverType,
		Scopes: []string{ScopeDecide},
	}
	if d.Privileged {
		approverPrincipal.Scopes = append(approverPrincipal.Scopes, ScopeL4)
	}
	if err = policy.CanVote(req, approverPrincipal, now); err != nil {
		return Request{}, err
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO core.approval_decision(id, request_id, approver_id, approver_type,
			verdict, comment, privileged, created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		d.ID, id, d.ApproverID, string(d.ApproverType), string(d.Verdict),
		d.Comment, d.Privileged, now); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Request{}, ErrDuplicateVote
		}
		return Request{}, fmt.Errorf("insert approval decision: %w", err)
	}

	d.CreatedAt = now
	settled := policy.Settle(req, append(req.Decisions, d))
	if settled != StatusPending {
		if _, err = tx.Exec(ctx, `
			UPDATE core.approval_request SET status=$2, decided_at=$3 WHERE id=$1`,
			id, string(settled), now); err != nil {
			return Request{}, fmt.Errorf("settle approval request: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Request{}, fmt.Errorf("commit approval vote: %w", err)
	}
	return s.Get(ctx, id)
}

// MarkExecuted 绑定 ActionRun。WHERE 里带 status='APPROVED' 与
// execution_run_id IS NULL：重复触发不会改到任何行，调用方据此得知「已经跑过」。
func (s *PgStore) MarkExecuted(ctx context.Context, id, runID uuid.UUID, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE core.approval_request
		SET status='EXECUTED', executed_at=$3, execution_run_id=$4
		WHERE id=$1 AND environment=$2 AND status='APPROVED' AND execution_run_id IS NULL`,
		id, s.environment, at, runID)
	if err != nil {
		return fmt.Errorf("mark approval executed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadyExecuted
	}
	return nil
}

func (s *PgStore) Cancel(ctx context.Context, id uuid.UUID, requesterID string, at time.Time) error {
	// requester_id 进 WHERE 而不是先读后判：撤回权属于提交人这件事由库来保证，
	// 少一次「读完到写之间被人改掉」的窗口。
	tag, err := s.pool.Exec(ctx, `
		UPDATE core.approval_request SET status='CANCELLED'
		WHERE id=$1 AND environment=$2 AND status='PENDING' AND requester_id=$3`,
		id, s.environment, requesterID)
	if err != nil {
		return fmt.Errorf("cancel approval request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 分不清是「不是你的单」还是「已经不 PENDING 了」——两种都不该撤回，
		// 且区分开会泄漏别人单子的存在与状态。
		return ErrNotCancellableByOther
	}
	return nil
}

// ExpirePending 把过期的 PENDING 单落 EXPIRED。
//
// 不写 decided_at：过期不是一次决定，没有人做过这个决定。库层的
// approval_request_decided_when_terminal 也正是这么要求的。
func (s *PgStore) ExpirePending(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE core.approval_request SET status='EXPIRED'
		WHERE environment=$1 AND status='PENDING' AND expires_at<=$2`, s.environment, now)
	if err != nil {
		return 0, fmt.Errorf("expire approval requests: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *PgStore) List(ctx context.Context, status Status, limit int) ([]Request, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM core.approval_request
		WHERE environment=$1 AND ($2='' OR status=$2)
		ORDER BY created_at DESC LIMIT $3`, s.environment, string(status), limit)
	if err != nil {
		return nil, fmt.Errorf("list approval requests: %w", err)
	}
	ids := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan approval id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Request, 0, len(ids))
	for _, id := range ids {
		req, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}
