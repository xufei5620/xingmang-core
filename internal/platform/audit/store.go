package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit/gen"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Store 是审计事件仓储。
//
// 追加走 advisory lock 串行化：哈希链要求「读链尖 → 算哈希 → 插入」是原子的，
// 否则并发追加会产生分叉（两条事件指向同一个 prev_hash）。
type Store struct {
	pool *pgxpool.Pool
}

// NewStore 创建仓储。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func mapToJSON(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func jsonToMap(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func fromTS(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func eventFromRow(r gen.AuditAuditEvent) (Event, error) {
	before, err := jsonToMap(r.BeforeSummary)
	if err != nil {
		return Event{}, fmt.Errorf("before_summary: %w", err)
	}
	after, err := jsonToMap(r.AfterSummary)
	if err != nil {
		return Event{}, fmt.Errorf("after_summary: %w", err)
	}
	creq, err := jsonToMap(r.ConnectorRequestSummary)
	if err != nil {
		return Event{}, fmt.Errorf("connector_request_summary: %w", err)
	}
	cresp, err := jsonToMap(r.ConnectorResponseSummary)
	if err != nil {
		return Event{}, fmt.Errorf("connector_response_summary: %w", err)
	}
	return Event{
		ID:                       r.ID,
		Sequence:                 r.Sequence,
		OccurredAt:               fromTS(r.OccurredAt),
		RecordedAt:               fromTS(r.RecordedAt),
		PrincipalID:              r.PrincipalID,
		PrincipalType:            principal.Type(r.PrincipalType),
		ActionID:                 r.ActionID,
		ActionVersion:            r.ActionVersion,
		ActionRunID:              r.ActionRunID,
		ResourceType:             r.ResourceType,
		ResourceID:               r.ResourceID,
		Environment:              r.Environment,
		Reason:                   r.Reason,
		ApprovalID:               r.ApprovalID,
		RequestID:                r.RequestID,
		TraceID:                  r.TraceID,
		SourceIP:                 r.SourceIp,
		BeforeSummary:            before,
		AfterSummary:             after,
		ConnectorRequestSummary:  creq,
		ConnectorResponseSummary: cresp,
		Result:                   Result(r.Result),
		CompensationResult:       r.CompensationResult,
		PrevHash:                 r.PrevHash,
		EventHash:                r.EventHash,
	}, nil
}

// Tip 返回链尖的 sequence 与 event_hash。空链返回 (0, GenesisHash, nil)。
func (s *Store) Tip(ctx context.Context) (int64, string, error) {
	row, err := gen.New(s.pool).GetAuditTip(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, GenesisHash, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("get audit tip: %w", err)
	}
	return row.Sequence, row.EventHash, nil
}

// Append 追加一条审计事件并接入哈希链。
//
// 调用方只需填业务字段；Sequence、PrevHash、RecordedAt、EventHash 由本方法
// 计算——让调用方自己算哈希等于把链的完整性交给调用方，那就不成其为保障。
func (s *Store) Append(ctx context.Context, e Event) (Event, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Event{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := gen.New(tx)
	// 串行化整条链的追加：事务结束自动释放
	if err := q.LockAuditChain(ctx); err != nil {
		return Event{}, fmt.Errorf("lock audit chain: %w", err)
	}

	prevSeq := int64(0)
	prevHash := GenesisHash
	tip, err := q.GetAuditTip(ctx)
	switch {
	case err == nil:
		prevSeq, prevHash = tip.Sequence, tip.EventHash
	case errors.Is(err, pgx.ErrNoRows):
		// 空链，用创世值
	default:
		return Event{}, fmt.Errorf("get audit tip: %w", err)
	}

	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	e.Sequence = prevSeq + 1
	e.PrevHash = prevHash
	e.RecordedAt = time.Now().UTC()

	// 归一化后再算哈希：否则写入前的表示（Go 原生类型、纳秒时间）与读回后的
	// 表示（jsonb→float64、微秒时间）不同，链一校验就断。
	e, err = e.Normalize()
	if err != nil {
		return Event{}, fmt.Errorf("normalize: %w", err)
	}

	hash, err := e.ComputeHash()
	if err != nil {
		return Event{}, fmt.Errorf("compute hash: %w", err)
	}
	e.EventHash = hash

	before, err := mapToJSON(e.BeforeSummary)
	if err != nil {
		return Event{}, fmt.Errorf("before_summary: %w", err)
	}
	after, err := mapToJSON(e.AfterSummary)
	if err != nil {
		return Event{}, fmt.Errorf("after_summary: %w", err)
	}
	creq, err := mapToJSON(e.ConnectorRequestSummary)
	if err != nil {
		return Event{}, fmt.Errorf("connector_request_summary: %w", err)
	}
	cresp, err := mapToJSON(e.ConnectorResponseSummary)
	if err != nil {
		return Event{}, fmt.Errorf("connector_response_summary: %w", err)
	}

	row, err := q.InsertAuditEvent(ctx, gen.InsertAuditEventParams{
		ID:                       e.ID,
		Sequence:                 e.Sequence,
		OccurredAt:               ts(e.OccurredAt),
		RecordedAt:               ts(e.RecordedAt),
		PrincipalID:              e.PrincipalID,
		PrincipalType:            string(e.PrincipalType),
		ActionID:                 e.ActionID,
		ActionVersion:            e.ActionVersion,
		ActionRunID:              e.ActionRunID,
		ResourceType:             e.ResourceType,
		ResourceID:               e.ResourceID,
		Environment:              e.Environment,
		Reason:                   e.Reason,
		ApprovalID:               e.ApprovalID,
		RequestID:                e.RequestID,
		TraceID:                  e.TraceID,
		SourceIp:                 e.SourceIP,
		BeforeSummary:            before,
		AfterSummary:             after,
		ConnectorRequestSummary:  creq,
		ConnectorResponseSummary: cresp,
		Result:                   string(e.Result),
		CompensationResult:       e.CompensationResult,
		PrevHash:                 e.PrevHash,
		EventHash:                e.EventHash,
	})
	if err != nil {
		return Event{}, fmt.Errorf("insert audit event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Event{}, fmt.Errorf("commit: %w", err)
	}
	return eventFromRow(row)
}

// List 读取 [from, to] 区间的事件（按 sequence 升序）。
func (s *Store) List(ctx context.Context, from, to int64) ([]Event, error) {
	rows, err := gen.New(s.pool).ListAuditEvents(ctx, gen.ListAuditEventsParams{
		Sequence:   from,
		Sequence_2: to,
	})
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		e, err := eventFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// ChainProblem 描述链校验发现的第一个问题。
type ChainProblem struct {
	Sequence int64
	Kind     string // hash_mismatch / broken_link / sequence_gap
	Detail   string
}

func (p ChainProblem) String() string {
	return fmt.Sprintf("sequence=%d kind=%s detail=%s", p.Sequence, p.Kind, p.Detail)
}

// VerifyChain 校验 [from, to] 区间的哈希链。链完好时返回 (nil, nil)。
//
// 三类问题：
//   - sequence_gap：序号不连续（有记录被删除）
//   - broken_link：prev_hash 与上一条的 event_hash 对不上
//   - hash_mismatch：重算哈希与存储值不符（内容被改动）
func (s *Store) VerifyChain(ctx context.Context, from, to int64) (*ChainProblem, error) {
	if from < 1 {
		from = 1
	}
	events, err := s.List(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}

	expectedSeq := events[0].Sequence
	prevHash := events[0].PrevHash
	// 从链首校验时，prev_hash 必须是创世值
	if expectedSeq == 1 && prevHash != GenesisHash {
		return &ChainProblem{Sequence: 1, Kind: "broken_link",
			Detail: "链首事件的 prev_hash 不是创世值"}, nil
	}

	for _, e := range events {
		if e.Sequence != expectedSeq {
			return &ChainProblem{Sequence: expectedSeq, Kind: "sequence_gap",
				Detail: fmt.Sprintf("期望 sequence=%d，实际读到 %d", expectedSeq, e.Sequence)}, nil
		}
		if e.PrevHash != prevHash {
			return &ChainProblem{Sequence: e.Sequence, Kind: "broken_link",
				Detail: "prev_hash 与上一条的 event_hash 不一致"}, nil
		}
		computed, err := e.ComputeHash()
		if err != nil {
			return nil, fmt.Errorf("sequence=%d 计算哈希失败: %w", e.Sequence, err)
		}
		if computed != e.EventHash {
			return &ChainProblem{Sequence: e.Sequence, Kind: "hash_mismatch",
				Detail: "重算哈希与存储值不符，内容已被改动"}, nil
		}
		prevHash = e.EventHash
		expectedSeq++
	}

	// 区间右端若有缺口（比如末尾若干条被删），也要报出来
	if to >= expectedSeq {
		lastSeq, _, err := s.Tip(ctx)
		if err != nil {
			return nil, err
		}
		if lastSeq >= expectedSeq {
			return &ChainProblem{Sequence: expectedSeq, Kind: "sequence_gap",
				Detail: fmt.Sprintf("区间内缺少 sequence=%d，但链尖为 %d", expectedSeq, lastSeq)}, nil
		}
	}
	return nil, nil
}
