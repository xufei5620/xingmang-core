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
		// 校验时按这一列选编码：不读回来的话，历史行（v1）会被当成 v2 重算，
		// 整条链报 hash_mismatch（XM-R009）。
		CanonicalVersion: r.CanonicalVersion,
	}, nil
}

// eventFromRecentRow 把 ListRecent 的**投影**行转成 Event。
//
// 与 eventFromRow 的差别只有一处：投影不取两个 connector 摘要，所以返回的
// Event 里它们恒为空 map（XM-0031，见 db/queries/audit.sql 的说明——那两列是
// 无大小约束的 jsonb，读 API 根本不返回它们，取回来只是白占内存）。
//
// **因此 ListRecent 返回的 Event 不能用来校验链**：ComputeHash 把四个摘要都
// 算进 canonical，缺两个就必然对不上。链校验走 List / VerifyChain，那条路径
// 取全列。这也是为什么不把两个函数合并成一个「摘要可空」的版本——让「能算
// 哈希的事件」与「给人看的事件」在类型来源上就分开，比留一个注释可靠。
func eventFromRecentRow(r gen.ListRecentAuditEventsRow) (Event, error) {
	before, err := jsonToMap(r.BeforeSummary)
	if err != nil {
		return Event{}, fmt.Errorf("before_summary: %w", err)
	}
	after, err := jsonToMap(r.AfterSummary)
	if err != nil {
		return Event{}, fmt.Errorf("after_summary: %w", err)
	}
	return Event{
		ID:            r.ID,
		Sequence:      r.Sequence,
		OccurredAt:    fromTS(r.OccurredAt),
		RecordedAt:    fromTS(r.RecordedAt),
		PrincipalID:   r.PrincipalID,
		PrincipalType: principal.Type(r.PrincipalType),
		ActionID:      r.ActionID,
		ActionVersion: r.ActionVersion,
		ActionRunID:   r.ActionRunID,
		ResourceType:  r.ResourceType,
		ResourceID:    r.ResourceID,
		Environment:   r.Environment,
		Reason:        r.Reason,
		ApprovalID:    r.ApprovalID,
		RequestID:     r.RequestID,
		TraceID:       r.TraceID,
		SourceIP:      r.SourceIp,
		BeforeSummary: before,
		AfterSummary:  after,
		// ConnectorRequestSummary / ConnectorResponseSummary 刻意留空，见上。
		ConnectorRequestSummary:  map[string]any{},
		ConnectorResponseSummary: map[string]any{},
		Result:                   Result(r.Result),
		CompensationResult:       r.CompensationResult,
		PrevHash:                 r.PrevHash,
		EventHash:                r.EventHash,
		// 这一列**必须取**，哪怕这个投影刻意省掉了两个 connector 摘要：
		// 它决定用哪版编码重算哈希，漏了就会把 v2 写入的行按 v1 重算、
		// 全部报「哈希不符」。它是 smallint，与那两个无界 jsonb 不是一回事，
		// 不在「不取的列就别取」那条纪律的射程内。
		CanonicalVersion: r.CanonicalVersion,
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

	// 新写入一律用当前版本（XM-R009）。**必须在算哈希之前钉住**：
	// Canonical 按这个字段选编码，写完哈希再改它就等于给这一行配了个
	// 对不上的版本号。
	e.CanonicalVersion = CurrentCanonicalVersion

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
		// 必须显式写：Go 结构体字面量省略字段会填零值，而 0 不在库层
		// CHECK 允许的 (1, 2) 里——插入会被拒。漏写不会静默通过，
		// 但报错信息是约束名，看的人未必立刻想到是这里少了一行。
		CanonicalVersion: e.CanonicalVersion,
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

// 倒序分页读取的边界（看板/审计视图用）。
const (
	// MaxListLimit 是单页上限。审计事件带前后摘要，一条能有几 KB；
	// 不封顶的话一个 limit=100000 就能把整条链拉进内存，既是内存风险
	// 也是「一次性拖走全部操作明细」的取数便利。超出一律夹到上限而不是
	// 报错——分页参数不是安全边界，静默收敛比让调用方重试更实用。
	MaxListLimit = 100
	// DefaultListLimit 是未指定 limit 时的默认页大小。
	DefaultListLimit = 50
)

// ListRecent 按 sequence **降序**读取某环境最近的审计事件（看板首屏与翻页）。
//
// 与 List 的区别不只是方向：List 服务于链校验（必须连续、必须全环境），
// ListRecent 服务于人看——按环境过滤且分页。两者不能合并，因为
// 「过滤后的区间」对链校验毫无意义（过滤本身就会制造序号缺口）。
//
// beforeSeq = 0 表示从最新一条开始；否则只返回 sequence 严格小于它的事件，
// 于是「上一页最后一条的 sequence」可以直接当下一页的游标，不重不漏。
// limit <= 0 用默认值，超过 MaxListLimit 夹到上限。
//
// 本方法只读，不参与哈希链的构建，也不校验链——调用方拿到的 event_hash /
// prev_hash 是库里的原样值，是否可信由 VerifyChain 回答。
//
// 返回的 Event 是**读投影**，两个 connector 摘要恒为空（XM-0031，理由见
// eventFromRecentRow 与 db/queries/audit.sql）。**不要对它调用 ComputeHash**。
func (s *Store) ListRecent(ctx context.Context, environment string, beforeSeq int64, limit int32) ([]Event, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	if beforeSeq < 0 {
		// 负游标没有意义；按「从最新开始」处理，避免把 -1 当成有效上界
		beforeSeq = 0
	}
	rows, err := gen.New(s.pool).ListRecentAuditEvents(ctx, gen.ListRecentAuditEventsParams{
		Environment: environment,
		BeforeSeq:   beforeSeq,
		RowLimit:    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list recent audit events: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		e, err := eventFromRecentRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// GetByActionRunID 读取某次 Action 执行关联的审计事件（供操作与审批页
// 「执行记录」详情的 before/after 摘要关联展示，XM-ACTIONS0）。不存在返回
// (zero, false, nil)——调用方据此回「无关联审计事件」而不是当成错误。
//
// 一次 Execute 只产生一条审计事件，成功失败都写（kernel.go Execute）；库层
// 没有唯一约束强制这一点，取 sequence 最小的一条给出确定结果（见
// GetAuditEventByActionRunID 的查询注释）。
//
// 与 ListRecent 同一条纪律：不取两个 connector 摘要（无界 jsonb，这一屏用不到）。
func (s *Store) GetByActionRunID(ctx context.Context, runID uuid.UUID) (Event, bool, error) {
	row, err := gen.New(s.pool).GetAuditEventByActionRunID(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, fmt.Errorf("get audit event by action_run_id: %w", err)
	}
	before, err := jsonToMap(row.BeforeSummary)
	if err != nil {
		return Event{}, false, fmt.Errorf("before_summary: %w", err)
	}
	after, err := jsonToMap(row.AfterSummary)
	if err != nil {
		return Event{}, false, fmt.Errorf("after_summary: %w", err)
	}
	return Event{
		ID:                       row.ID,
		Sequence:                 row.Sequence,
		OccurredAt:               fromTS(row.OccurredAt),
		RecordedAt:               fromTS(row.RecordedAt),
		PrincipalID:              row.PrincipalID,
		PrincipalType:            principal.Type(row.PrincipalType),
		ActionID:                 row.ActionID,
		ActionVersion:            row.ActionVersion,
		ActionRunID:              row.ActionRunID,
		ResourceType:             row.ResourceType,
		ResourceID:               row.ResourceID,
		Environment:              row.Environment,
		Reason:                   row.Reason,
		ApprovalID:               row.ApprovalID,
		RequestID:                row.RequestID,
		TraceID:                  row.TraceID,
		SourceIP:                 row.SourceIp,
		BeforeSummary:            before,
		AfterSummary:             after,
		ConnectorRequestSummary:  map[string]any{},
		ConnectorResponseSummary: map[string]any{},
		Result:                   Result(row.Result),
		CompensationResult:       row.CompensationResult,
		PrevHash:                 row.PrevHash,
		EventHash:                row.EventHash,
		CanonicalVersion:         row.CanonicalVersion,
	}, true, nil
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
