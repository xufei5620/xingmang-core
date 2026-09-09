package alerts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts/gen"
)

// MaxListLimit 是一次列表查询能返回的最多条数。
//
// 与 ops.MaxSampleLimit 同样是给**界面**定的上限，不是给库定的：
// 告警页一屏装不下 500 条，再多只是把 JSON 撑大。真要审全量走导出。
const MaxListLimit int32 = 500

// reopenLookback 是「复发」的判定窗口（规格 §9.3「重新打开」）。
//
// 24 小时内解决过又回来 = REOPENED，更久之前 = 一次新的 OPEN。
// 窗口是必要的：没有窗口的话，三个月前发生过一次的问题今天再来仍会被标成
// 「复发」，那个标签就不再携带任何信息。24h 对齐「一个值班周期」——
// 在同一个班次里二次出现，正是值得多看一眼的那种复发。
const reopenLookback = 24 * time.Hour

// uniqueViolation 是 PostgreSQL 唯一约束冲突的 SQLSTATE。
const uniqueViolation = "23505"

// Store 是告警与静默窗口的仓储。
type Store struct {
	q *gen.Queries
}

// NewStore 创建仓储。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{q: gen.New(pool)}
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

func fromTSPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func alertFromRow(r gen.AlertsAlert) Alert {
	return Alert{
		ID:              r.ID,
		RuleKey:         r.RuleKey,
		DedupKey:        r.DedupKey,
		Severity:        Severity(r.Severity),
		Status:          Status(r.Status),
		Title:           r.Title,
		Detail:          r.Detail,
		Environment:     r.Environment,
		OpenedAt:        fromTS(r.OpenedAt),
		AcknowledgedAt:  fromTSPtr(r.AcknowledgedAt),
		ResolvedAt:      fromTSPtr(r.ResolvedAt),
		LastSeenAt:      fromTS(r.LastSeenAt),
		FireCount:       r.FireCount,
		TriggerCount:    r.TriggerCount,
		FirstOpenedAt:   fromTSPtr(r.FirstOpenedAt),
		SourceMetricKey: r.SourceMetricKey,
		NotifyStatus:    NotifyStatus(r.NotifyStatus),
		NotifyError:     r.NotifyError,
		NotifiedAt:      fromTSPtr(r.NotifiedAt),
		CreatedAt:       fromTS(r.CreatedAt),
		UpdatedAt:       fromTS(r.UpdatedAt),
	}
}

func silenceFromRow(r gen.AlertsAlertSilence) Silence {
	return Silence{
		ID:          r.ID,
		RuleKey:     r.RuleKey,
		Environment: r.Environment,
		Reason:      r.Reason,
		StartsAt:    fromTS(r.StartsAt),
		EndsAt:      fromTS(r.EndsAt),
		CreatedBy:   r.CreatedBy,
		CreatedAt:   fromTS(r.CreatedAt),
	}
}

func alertsFromRows(rows []gen.AlertsAlert) []Alert {
	out := make([]Alert, 0, len(rows))
	for _, r := range rows {
		out = append(out, alertFromRow(r))
	}
	return out
}

func clampLimit(limit int32) int32 {
	return ClampListLimit(limit)
}

// ClampListLimit 是列表查询实际会用的条数。
//
// 导出它是为了让 HTTP 层能回答「这一页是不是被截断了」：调用方传的 limit 和
// 真正生效的 limit 可能不是一个数（0 或超过上界都会被钳到 MaxListLimit），
// 而「返回条数是否等于生效上限」正是判断截断的唯一依据。让 HTTP 层自己
// 复刻这段钳制，就会出现两处各写一个数、改一处忘另一处的老问题
// （parseAlertLimit 的注释已经点过这件事）。
func ClampListLimit(limit int32) int32 {
	if limit <= 0 || limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}

// UpsertInput 是一次「规则命中」要写进库的全部事实。
type UpsertInput struct {
	RuleKey         string
	DedupKey        string
	Severity        Severity
	Title           string
	Detail          string
	Environment     string
	SourceMetricKey string
	// Silenced 表示此刻命中了一个生效中的静默窗口。
	// 它由调用方（评估器）判定并传进来，仓储不自己去查窗口——
	// 一轮评估里几十条命中共用同一份窗口清单，让仓储逐条重查是浪费，
	// 更重要的是那样会让同一轮里的判定基准在毫秒间漂移。
	Silenced bool
	Now      time.Time
}

// Upsert 按去重键写入或合并一条告警（规格 §9.3「去重」）。
//
// 返回值 created 报告这次是新开了一条还是合并进了已有的那条：调用方据此
// 决定日志级别，也让「第一次出现」在集成测试里可断言。
//
// 状态转换只有三条，全都在这里，不散落到别处：
//   - 命中静默 → SILENCED（无论之前是什么状态：新建静默窗口的目的就是
//     让正在响的告警闭嘴）；
//   - 之前是 SILENCED、现在不静默了 → 转回 OPEN 并把投递推回 pending
//     （任务书：「过期后仍满足→转 OPEN 投递」）；
//   - 其余情况保持原状态。特别地 ACKNOWLEDGED 持续命中仍是 ACKNOWLEDGED：
//     「有人接手了」这个事实不该被下一轮评估抹掉。
func (s *Store) Upsert(ctx context.Context, in UpsertInput) (Alert, bool, error) {
	if err := in.validate(); err != nil {
		return Alert{}, false, err
	}
	now := in.Now.UTC()

	existing, err := s.GetActiveByDedupKey(ctx, in.DedupKey)
	switch {
	case err == nil:
		merged, touchErr := s.touch(ctx, existing, in, now)
		return merged, false, touchErr
	case errors.Is(err, ErrNotFound):
		// 继续走新建路径
	default:
		return Alert{}, false, err
	}

	created, err := s.insert(ctx, in, now)
	if err == nil {
		return created, true, nil
	}
	// 唯一索引冲突 = 另一个评估器在这两步之间抢先插入了同一个 dedup_key。
	// 这在正常部署里不该发生（River 的周期唯一性保证同一周期只跑一个），
	// 但唯一索引存在的意义正是兜住「不该发生」。重查一次并合并进去，
	// 而不是把一次成功的去重报成错误。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		raced, lookupErr := s.GetActiveByDedupKey(ctx, in.DedupKey)
		if lookupErr != nil {
			return Alert{}, false, err
		}
		merged, touchErr := s.touch(ctx, raced, in, now)
		return merged, false, touchErr
	}
	return Alert{}, false, err
}

func (in UpsertInput) validate() error {
	if in.RuleKey == "" {
		return fmt.Errorf("rule_key: %w", ErrMissingField)
	}
	if !ruleKeyPattern.MatchString(in.RuleKey) {
		return fmt.Errorf("rule_key=%q 须匹配 ^[a-z0-9][a-z0-9_.-]{0,127}$: %w",
			in.RuleKey, ErrInvalidFormat)
	}
	if in.DedupKey == "" {
		return fmt.Errorf("dedup_key: %w", ErrMissingField)
	}
	if in.Title == "" {
		return fmt.Errorf("title: %w", ErrMissingField)
	}
	if in.Environment == "" {
		return fmt.Errorf("environment: %w", ErrMissingField)
	}
	if _, err := ParseSeverity(string(in.Severity)); err != nil {
		return err
	}
	if in.Now.IsZero() {
		return fmt.Errorf("now: %w", ErrMissingField)
	}
	return nil
}

// touch 把一次重复命中合并进已有的告警。
func (s *Store) touch(ctx context.Context, existing Alert, in UpsertInput, now time.Time) (Alert, error) {
	target := existing.Status
	resetNotify := false
	switch {
	case in.Silenced:
		target = StatusSilenced
	case existing.Status == StatusSilenced:
		// 窗口过期、条件仍成立：转回 OPEN 并重新排队投递。
		// 不用 REOPENED——那个状态留给「解决之后又回来」，
		// 而这条告警从头到尾就没被解决过，只是被捂住了嘴。
		target = StatusOpen
		resetNotify = true
	}

	row, err := s.q.TouchAlert(ctx, gen.TouchAlertParams{
		ID:          existing.ID,
		LastSeenAt:  ts(now),
		Status:      string(target),
		Detail:      in.Detail,
		ResetNotify: resetNotify,
	})
	if err != nil {
		return Alert{}, fmt.Errorf("touch alert: %w", err)
	}
	return alertFromRow(row), nil
}

// insert 新开一条告警，并判定它该是 OPEN、REOPENED 还是 SILENCED。
//
// first_opened_at 与 trigger_count 在复发路径上**一起继承**上一次那条：
// 前者取它的有效首开时刻（「已持续从首次开算，不因抖动归零」，子片 B 任务书
// 第 1 条），后者取它的值 +1。继承范围天然就是 reopenLookback（24 小时）——
// 超过那个窗口本来就不算同一件事，不必再发明第二个「多久算同一件事」的常量。
//
// **两个字段必须同进同退。** 只继承 first_opened_at 的话，一条开→关→开
// 四轮的告警在界面上会是「已持续 4 小时，触发 1 次」——两个数各自都对，
// 并排放在一行上给出的合成答案却是假的，而合成正是前端被告知要做的事
// （见 docs/handoffs/slices/XM-OPS-TRUTH-B.md 的字段表）。它与本片要治的
// 「触发 669 次」是同一类误读，只是方向相反：那个多报，这个少报。
func (s *Store) insert(ctx context.Context, in UpsertInput, now time.Time) (Alert, error) {
	status := StatusOpen
	firstOpenedAt := now
	// 新开一条告警**就是**一次真正的触发，所以非复发路径恒为 1。
	triggerCount := int32(1)
	triggerCountArg := &triggerCount
	if in.Silenced {
		status = StatusSilenced
	} else {
		previous, reopened, err := s.recentlyResolved(ctx, in.DedupKey, now)
		if err != nil {
			return Alert{}, err
		}
		if reopened {
			status = StatusReopened
			// 上一条自己也可能是复发链上的一环，所以取它的**有效**首开时刻
			// 而不是它的 opened_at：三次复发之后，「已持续」仍然从第一次算起。
			if inherited, _ := previous.EffectiveFirstOpenedAt(); !inherited.IsZero() {
				firstOpenedAt = inherited
			}
			// 上一条是本列上线前的旧行（TriggerCount 为 nil）时，链上真正
			// 触发过几次没有人记下来过。这里传 NULL 而不是从 1 重新起算：
			// 「不知道」在界面上显示成「—」是诚实的，显示成「触发 1 次」
			// 则是一个看起来像真答案的假答案（宪法 12 条）。
			if previous.TriggerCount == nil {
				triggerCountArg = nil
			} else {
				inheritedCount := *previous.TriggerCount + 1
				triggerCountArg = &inheritedCount
			}
		}
	}

	row, err := s.q.InsertAlert(ctx, gen.InsertAlertParams{
		ID:              uuid.New(),
		RuleKey:         in.RuleKey,
		DedupKey:        in.DedupKey,
		Severity:        string(in.Severity),
		Status:          string(status),
		Title:           in.Title,
		Detail:          in.Detail,
		Environment:     in.Environment,
		OpenedAt:        ts(now),
		TriggerCount:    triggerCountArg,
		FirstOpenedAt:   ts(firstOpenedAt),
		SourceMetricKey: in.SourceMetricKey,
	})
	if err != nil {
		return Alert{}, fmt.Errorf("insert alert: %w", err)
	}
	return alertFromRow(row), nil
}

// recentlyResolved 找复发窗口内最近解决的那一条。
//
// 返回整行而不是一个布尔：调用方要的不只是「是不是复发」，还要它的首开
// 时刻。原先只返回布尔时，那一行的其它事实在这里被丢掉了，于是复发的新行
// 只能从 now 重新起算——界面上「已持续」每复发一次就归零。
func (s *Store) recentlyResolved(ctx context.Context, dedupKey string, now time.Time) (Alert, bool, error) {
	row, err := s.q.GetLatestResolvedAlertByDedupKey(ctx, gen.GetLatestResolvedAlertByDedupKeyParams{
		DedupKey:   dedupKey,
		ResolvedAt: ts(now.Add(-reopenLookback)),
	})
	switch {
	case err == nil:
		return alertFromRow(row), true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return Alert{}, false, nil
	default:
		return Alert{}, false, fmt.Errorf("lookup resolved alert: %w", err)
	}
}

// GetActiveByDedupKey 取活跃告警；不存在返回 ErrNotFound。
func (s *Store) GetActiveByDedupKey(ctx context.Context, dedupKey string) (Alert, error) {
	row, err := s.q.GetActiveAlertByDedupKey(ctx, dedupKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, fmt.Errorf("dedup_key=%s: %w", dedupKey, ErrNotFound)
	}
	if err != nil {
		return Alert{}, fmt.Errorf("get active alert: %w", err)
	}
	return alertFromRow(row), nil
}

// Get 按 ID 取一条告警；不存在返回 ErrNotFound。
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Alert, error) {
	row, err := s.q.GetAlert(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, fmt.Errorf("alert %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Alert{}, fmt.Errorf("get alert: %w", err)
	}
	return alertFromRow(row), nil
}

// Resolve 自动恢复一条告警（规格 §9.3「恢复条件」）。
//
// 已经是终态时返回 ErrNotFound 而不是静默成功：调用方多半是在重复解决，
// 而「重复解决」与「解决了一条不存在的告警」都值得让调用方知道。
func (s *Store) Resolve(ctx context.Context, id uuid.UUID, at time.Time) (Alert, error) {
	row, err := s.q.ResolveAlert(ctx, gen.ResolveAlertParams{ID: id, ResolvedAt: ts(at)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, fmt.Errorf("alert %s 不在可解决的状态: %w", id, ErrNotFound)
	}
	if err != nil {
		return Alert{}, fmt.Errorf("resolve alert: %w", err)
	}
	return alertFromRow(row), nil
}

// Acknowledge 确认一条告警（规格 §9.3「确认」）。
//
// 只有 OPEN / REOPENED 能被确认。已解决的告警确认没有意义；被静默的告警
// 确认更没有——静默的意思正是「现在不想看见它」。状态不对时返回
// ErrNotAcknowledgeable，让 HTTP 层能把它映射成 400 而不是 404。
func (s *Store) Acknowledge(ctx context.Context, id uuid.UUID, at time.Time) (Alert, error) {
	row, err := s.q.AcknowledgeAlert(ctx, gen.AcknowledgeAlertParams{
		ID: id, AcknowledgedAt: ts(at),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 区分「压根没这条」与「有但状态不对」：两者要给运维的提示完全不同。
		if _, getErr := s.Get(ctx, id); getErr != nil {
			return Alert{}, getErr
		}
		return Alert{}, fmt.Errorf("alert %s 只有 OPEN / REOPENED 可确认: %w",
			id, ErrNotAcknowledgeable)
	}
	if err != nil {
		return Alert{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	return alertFromRow(row), nil
}

// ListActive 列出某环境下全部活跃告警（含 SILENCED）。
func (s *Store) ListActive(ctx context.Context, environment string) ([]Alert, error) {
	rows, err := s.q.ListActiveAlertsByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list active alerts: %w", err)
	}
	return alertsFromRows(rows), nil
}

// ListByStatus 按状态集合过滤列出告警。空集合等价于「全部活跃状态」。
func (s *Store) ListByStatus(
	ctx context.Context, environment string, statuses []Status, limit int32,
) ([]Alert, error) {
	values := make([]string, 0, len(statuses))
	for _, st := range statuses {
		values = append(values, string(st))
	}
	if len(values) == 0 {
		values = ActiveStatusStrings()
	}
	rows, err := s.q.ListAlertsByEnvironmentAndStatus(ctx, gen.ListAlertsByEnvironmentAndStatusParams{
		Environment: environment,
		Statuses:    values,
		Limit:       clampLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list alerts by status: %w", err)
	}
	return alertsFromRows(rows), nil
}

// ListByStatusWithTruncation is the bounded variant used by read-only impact
// previews. It fetches one extra row so callers can distinguish a complete
// active-alert set from a page cap; without that bit a truncated set could be
// misclassified as a missing R5 alert.
func (s *Store) ListByStatusWithTruncation(
	ctx context.Context, environment string, statuses []Status, limit int32,
) ([]Alert, bool, error) {
	values := make([]string, 0, len(statuses))
	for _, st := range statuses {
		values = append(values, string(st))
	}
	if len(values) == 0 {
		values = ActiveStatusStrings()
	}
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	rows, err := s.q.ListAlertsByEnvironmentAndStatus(ctx, gen.ListAlertsByEnvironmentAndStatusParams{
		Environment: environment, Statuses: values, Limit: limit + 1,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list alerts by status with truncation: %w", err)
	}
	truncated := len(rows) > int(limit)
	if truncated {
		rows = rows[:limit]
	}
	return alertsFromRows(rows), truncated, nil
}

// ListRecent 列出某环境下最近的告警（含已解决）。
func (s *Store) ListRecent(ctx context.Context, environment string, limit int32) ([]Alert, error) {
	rows, err := s.q.ListRecentAlertsByEnvironment(ctx, gen.ListRecentAlertsByEnvironmentParams{
		Environment: environment,
		Limit:       clampLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list recent alerts: %w", err)
	}
	return alertsFromRows(rows), nil
}

// ListPendingNotify 列出待投递与投递失败的告警（规格 §9.3「失败重试」）。
func (s *Store) ListPendingNotify(ctx context.Context, environment string, limit int32) ([]Alert, error) {
	rows, err := s.q.ListAlertsPendingNotify(ctx, gen.ListAlertsPendingNotifyParams{
		Environment: environment,
		Limit:       clampLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list pending notify alerts: %w", err)
	}
	return alertsFromRows(rows), nil
}

// MarkDelivered 记录投递成功。
func (s *Store) MarkDelivered(ctx context.Context, id uuid.UUID, at time.Time) error {
	if err := s.q.MarkAlertDelivered(ctx, gen.MarkAlertDeliveredParams{
		ID: id, NotifiedAt: ts(at),
	}); err != nil {
		return fmt.Errorf("mark alert delivered: %w", err)
	}
	return nil
}

// MarkNotifyFailed 记录投递失败与原因。
//
// **调用方必须先脱敏**：reason 会原样落库并原样回给前端。Telegram Bot Token
// 出现在这里等于把凭据写进了一张会被读的表（宪法 7 条）。notifier 侧已经做了
// 一层 redact，这里只兜住空串——库层 CHECK 要求 failed 必须带原因，
// 传空串会让整条投递记录写不进去，于是失败反而变成了静默失败。
func (s *Store) MarkNotifyFailed(ctx context.Context, id uuid.UUID, reason string) error {
	if reason == "" {
		reason = "投递失败（未提供原因）"
	}
	if err := s.q.MarkAlertNotifyFailed(ctx, gen.MarkAlertNotifyFailedParams{
		ID: id, NotifyError: reason,
	}); err != nil {
		return fmt.Errorf("mark alert notify failed: %w", err)
	}
	return nil
}

// CreateSilence 建一个静默窗口（规格 §9.3「静默」）。
func (s *Store) CreateSilence(ctx context.Context, in Silence) (Silence, error) {
	if err := in.Validate(); err != nil {
		return Silence{}, err
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	row, err := s.q.InsertAlertSilence(ctx, gen.InsertAlertSilenceParams{
		ID:          in.ID,
		RuleKey:     in.RuleKey,
		Environment: in.Environment,
		Reason:      in.Reason,
		StartsAt:    ts(in.StartsAt),
		EndsAt:      ts(in.EndsAt),
		CreatedBy:   in.CreatedBy,
	})
	if err != nil {
		return Silence{}, fmt.Errorf("insert alert silence: %w", err)
	}
	return silenceFromRow(row), nil
}

// ListActiveSilences 列出某环境下此刻生效的静默窗口。
func (s *Store) ListActiveSilences(
	ctx context.Context, environment string, at time.Time,
) ([]Silence, error) {
	rows, err := s.q.ListActiveSilences(ctx, gen.ListActiveSilencesParams{
		Environment: environment,
		StartsAt:    ts(at),
	})
	if err != nil {
		return nil, fmt.Errorf("list active silences: %w", err)
	}
	out := make([]Silence, 0, len(rows))
	for _, r := range rows {
		out = append(out, silenceFromRow(r))
	}
	return out, nil
}

// ListSilences 列出某环境下的静默窗口（含已过期），按开始时间倒序。
func (s *Store) ListSilences(ctx context.Context, environment string, limit int32) ([]Silence, error) {
	rows, err := s.q.ListSilencesByEnvironment(ctx, gen.ListSilencesByEnvironmentParams{
		Environment: environment,
		Limit:       clampLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list silences: %w", err)
	}
	out := make([]Silence, 0, len(rows))
	for _, r := range rows {
		out = append(out, silenceFromRow(r))
	}
	return out, nil
}

// PruneResolved 删除 resolved_at 早于 cutoff 的**已解决**告警，一次最多 batchSize 行。
//
// XM-R012（Issue #75）。只删已解决的：活跃告警（OPEN / ACKNOWLEDGED /
// SILENCED / REOPENED）永远不删，不管它多老——一条挂了半年没人管的告警恰恰是
// 最该被看见的那条，清掉它等于用清理任务掩盖运维欠账。判据在 SQL 里
// （db/queries/alerts.sql），不在这里，于是绕过本方法的调用也删不掉活跃告警。
//
// 返回本批实际删除的行数；调用方循环到返回值 < batchSize 为止。
func (s *Store) PruneResolved(ctx context.Context, cutoff time.Time, batchSize int32) (int64, error) {
	return s.q.PruneResolvedAlerts(ctx, gen.PruneResolvedAlertsParams{
		Cutoff:    ts(cutoff.UTC()),
		BatchSize: batchSize,
	})
}

func ackFromRow(r gen.AlertsUpstreamVersionAck) UpstreamVersionAck {
	return UpstreamVersionAck{
		Environment:    r.Environment,
		MetricKey:      r.MetricKey,
		Version:        r.Version,
		Source:         r.Source,
		AcknowledgedBy: r.AcknowledgedBy,
		AcknowledgedAt: fromTS(r.AcknowledgedAt),
		Note:           r.Note,
	}
}

// SetUpstreamVersionAck 记下「这条上游的这个版本我核对过了」，覆盖同一条
// 上游此前记着的版本。
//
// 校验（版本形态、指标键是否已注册、与当轮观测是否逐字相同）全部在 Action
// Handler 里，不在这里：那些判据要读 ops 观测，而仓储层不该反向依赖 ops。
// 这里只保证写进去的东西满足库层 CHECK。
func (s *Store) SetUpstreamVersionAck(ctx context.Context, in UpstreamVersionAck) (UpstreamVersionAck, error) {
	if in.Environment == "" {
		return UpstreamVersionAck{}, fmt.Errorf("environment: %w", ErrMissingField)
	}
	if !ruleKeyPattern.MatchString(in.MetricKey) {
		return UpstreamVersionAck{}, fmt.Errorf("metric_key=%q 须匹配 ^[a-z0-9][a-z0-9_.-]{0,127}$: %w",
			in.MetricKey, ErrInvalidFormat)
	}
	if strings.TrimSpace(in.Version) == "" {
		return UpstreamVersionAck{}, fmt.Errorf("version: %w", ErrMissingField)
	}
	if strings.TrimSpace(in.AcknowledgedBy) == "" {
		return UpstreamVersionAck{}, fmt.Errorf("acknowledged_by: %w", ErrMissingField)
	}
	at := in.AcknowledgedAt
	if at.IsZero() {
		return UpstreamVersionAck{}, fmt.Errorf("acknowledged_at: %w", ErrMissingField)
	}
	row, err := s.q.SetUpstreamVersionAck(ctx, gen.SetUpstreamVersionAckParams{
		Environment:    in.Environment,
		MetricKey:      in.MetricKey,
		Version:        in.Version,
		Source:         in.Source,
		AcknowledgedBy: in.AcknowledgedBy,
		AcknowledgedAt: ts(at),
		Note:           in.Note,
	})
	if err != nil {
		return UpstreamVersionAck{}, fmt.Errorf("set upstream version ack: %w", err)
	}
	return ackFromRow(row), nil
}

// GetUpstreamVersionAck 取一条已核对记录；不存在返回 ErrNotFound。
func (s *Store) GetUpstreamVersionAck(ctx context.Context, environment, metricKey string) (UpstreamVersionAck, error) {
	row, err := s.q.GetUpstreamVersionAck(ctx, gen.GetUpstreamVersionAckParams{
		Environment: environment,
		MetricKey:   metricKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return UpstreamVersionAck{}, fmt.Errorf("upstream_version_ack %s/%s: %w", environment, metricKey, ErrNotFound)
	}
	if err != nil {
		return UpstreamVersionAck{}, fmt.Errorf("get upstream version ack: %w", err)
	}
	return ackFromRow(row), nil
}

// DeleteUpstreamVersionAck 撤销一条已核对记录；不存在返回 ErrNotFound。
//
// 返回被删掉的那一行而不是一个布尔：调用方（Action Handler）要把它写进审计的
// before 摘要——「撤销了什么」比「撤销成功了」有用得多。
func (s *Store) DeleteUpstreamVersionAck(ctx context.Context, environment, metricKey string) (UpstreamVersionAck, error) {
	row, err := s.q.DeleteUpstreamVersionAck(ctx, gen.DeleteUpstreamVersionAckParams{
		Environment: environment,
		MetricKey:   metricKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return UpstreamVersionAck{}, fmt.Errorf("upstream_version_ack %s/%s: %w", environment, metricKey, ErrNotFound)
	}
	if err != nil {
		return UpstreamVersionAck{}, fmt.Errorf("delete upstream version ack: %w", err)
	}
	return ackFromRow(row), nil
}

// ListUpstreamVersionAcksOrdered 返回某环境下的全部已核对记录（按 metric_key 升序）。
//
// 与 ListUpstreamVersionAcks 返回 map 的那个版本是两个用途：评估器要按
// metric_key 随机取，只读端点要一个稳定顺序的列表。两者共用同一条查询。
func (s *Store) ListUpstreamVersionAcksOrdered(ctx context.Context, environment string) ([]UpstreamVersionAck, error) {
	rows, err := s.q.ListUpstreamVersionAcks(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list upstream version acks: %w", err)
	}
	out := make([]UpstreamVersionAck, 0, len(rows))
	for _, r := range rows {
		out = append(out, ackFromRow(r))
	}
	return out, nil
}

// ListUpstreamVersionAcks 返回某环境下的全部已核对记录，键是 metric_key。
//
// 返回 map 而不是切片：调用方（评估器）逐条观测查的就是「这条指标核对过
// 什么版本」，切片会让每条观测再线性扫一遍。
func (s *Store) ListUpstreamVersionAcks(ctx context.Context, environment string) (map[string]UpstreamVersionAck, error) {
	rows, err := s.q.ListUpstreamVersionAcks(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list upstream version acks: %w", err)
	}
	out := make(map[string]UpstreamVersionAck, len(rows))
	for _, r := range rows {
		ack := ackFromRow(r)
		out[ack.MetricKey] = ack
	}
	return out, nil
}
