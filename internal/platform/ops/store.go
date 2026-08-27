package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops/gen"
)

// MaxSampleLimit 是一次历史查询能返回的最多样本数。
//
// 1000 是给**图**定的上限，不是给库定的：折线图上超过一千个点，屏幕像素
// 就已经不够画了，再多只是把 JSON 撑大。真正需要更长跨度的分析走导出，
// 不该由看板端点承担。
const MaxSampleLimit int32 = 1000

// Store 是指标观测仓储。
//
// 它管两张表，语义相反、故意分开：
//   - ops.metric_observation：每个 (metric_key, environment) 只保留最新一条，
//     回答「现在是什么」，整行覆盖；
//   - ops.metric_observation_sample：追加型样本，回答「这段时间是怎么变的」，
//     只有 INSERT 与 SELECT，没有 UPDATE 路径；唯一的 DELETE 是保留期清理
//     （PruneSamples，XM-R012），按时间批量删除过期样本，不改任何一行的内容。
type Store struct {
	// pool 只服务于需要**多条语句原子生效**的写入（UpsertWithSample）。
	// 单语句路径继续走 q，不必每次都开一个事务。
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewStore 创建仓储。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func tsPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func fromTSPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func fromTS(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// decodeValueJSON 把 jsonb 列解成 map，数字保持 json.Number 而不是 float64。
//
// 为什么必须是 UseNumber（XM-0031，回归 Codex 冷审 PR #48 第 4 条 /
// PR #43 head `419ecf8`）：value_json 里装的是金额的 **minor units**
// （`balance_minor_units`、`amount_minor` 等 int64）。默认 json.Unmarshal 把
// 所有数字解成 float64，尾数只有 53 位，超过 2^53（约 9.007e15）的整数一读回
// 就静默丢精度——9007199254740995 会变成 9007199254740996。这张表长期保存财务
// 趋势，失真是**永久**的：库里那条 jsonb 还是对的，但每一次读取都返回错的值，
// 而且错得毫无痕迹。宪法「金额禁止 float」在这条读回路径上必须机械成立。
//
// json.Number 是原始字面量的字符串包装，encoding/json 编码它时原样写出数字
// 字面量（不加引号），所以 HTTP 响应里仍是 JSON number，前端契约不变。
//
// **与 audit 包的差异是刻意的**：`internal/platform/audit` 的
// `Event.Normalize()` 反过来**故意**做 float64 归一化。那里的目标不是保精度，
// 而是让「写入前算的哈希」与「从 jsonb 读回后重算的哈希」逐字节相同——
// 归一化到与 jsonb 读回一致的表示，链才校验得过。审计摘要不承载金额口径，
// 运营指标承载；两者的正确答案因此相反。**不要**把这里的 UseNumber 搬进
// audit，那会让全部历史链一次性失效。
func decodeValueJSON(raw []byte) (map[string]any, error) {
	value := map[string]any{}
	if len(raw) == 0 {
		return value, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("value_json: %w", err)
	}
	if value == nil {
		// jsonb 里存的是字面量 null：当作空对象，调用方不必再判一次 nil
		value = map[string]any{}
	}
	return value, nil
}

func observationFromRow(r gen.OpsMetricObservation) (Observation, error) {
	value, err := decodeValueJSON(r.ValueJson)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		ID:                        r.ID,
		MetricKey:                 r.MetricKey,
		Source:                    r.Source,
		Environment:               r.Environment,
		ObservedAt:                fromTSPtr(r.ObservedAt),
		SyncedAt:                  fromTS(r.SyncedAt),
		Watermark:                 r.Watermark,
		Status:                    SyncStatus(r.Status),
		IsPartial:                 r.IsPartial,
		LastSuccess:               fromTSPtr(r.LastSuccess),
		LastErrorCode:             r.LastErrorCode,
		StalenessThresholdSeconds: r.StalenessThresholdSeconds,
		Value:                     value,
		UpdatedAt:                 fromTS(r.UpdatedAt),
	}, nil
}

// marshalValue 把观测值编码成 jsonb 列的字节。
//
// nil 与空 map 都编码成 `{}`：库里那一列是 NOT NULL，而「这一刻没有值」
// 由 status/observed_at 表达，不该再靠一个 NULL 说第二遍。
func marshalValue(o Observation) ([]byte, error) {
	value := o.Value
	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("value_json: %w", err)
	}
	return valueJSON, nil
}

// upsertParams / sampleParams 把领域对象翻成两条语句各自的参数。
//
// 抽出来是因为 UpsertWithSample 要在事务里重跑同样的翻译（XM-R010）。
// 让「字段怎么映射到列」只有一份实现：将来给观测加字段时，忘了改事务路径
// 会在编译期就显形，而不是让事务写出一份少几列的样本。
func upsertParams(o Observation) (gen.UpsertMetricObservationParams, error) {
	valueJSON, err := marshalValue(o)
	if err != nil {
		return gen.UpsertMetricObservationParams{}, err
	}
	return gen.UpsertMetricObservationParams{
		ID:                        o.ID,
		MetricKey:                 o.MetricKey,
		Source:                    o.Source,
		Environment:               o.Environment,
		ObservedAt:                tsPtr(o.ObservedAt),
		SyncedAt:                  ts(o.SyncedAt),
		Watermark:                 o.Watermark,
		Status:                    string(o.Status),
		IsPartial:                 o.IsPartial,
		LastSuccess:               tsPtr(o.LastSuccess),
		LastErrorCode:             o.LastErrorCode,
		StalenessThresholdSeconds: o.StalenessThresholdSeconds,
		ValueJson:                 valueJSON,
	}, nil
}

func sampleParams(o Observation) (gen.InsertMetricObservationSampleParams, error) {
	valueJSON, err := marshalValue(o)
	if err != nil {
		return gen.InsertMetricObservationSampleParams{}, err
	}
	return gen.InsertMetricObservationSampleParams{
		MetricKey:     o.MetricKey,
		Source:        o.Source,
		Environment:   o.Environment,
		ObservedAt:    tsPtr(o.ObservedAt),
		SyncedAt:      ts(o.SyncedAt),
		Status:        string(o.Status),
		IsPartial:     o.IsPartial,
		Watermark:     o.Watermark,
		LastErrorCode: o.LastErrorCode,
		ValueJson:     valueJSON,
	}, nil
}

// Upsert 写入或覆盖某个 (metric_key, environment) 的最新观测。
//
// **周期同步任务不该用它**，用 UpsertWithSample：单独调用 Upsert 只更新
// 「现在是什么」，历史序列会缺掉这一刻（理由见 UpsertWithSample 的注释）。
// 本方法留给「只关心最新态、本来就不该留历史点」的场景——手工订正一行、
// 一次性回填、以及只验证最新态语义的测试。
func (s *Store) Upsert(ctx context.Context, o Observation) (Observation, error) {
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	params, err := upsertParams(o)
	if err != nil {
		return Observation{}, err
	}

	row, err := s.q.UpsertMetricObservation(ctx, params)
	if err != nil {
		return Observation{}, fmt.Errorf("upsert metric observation: %w", err)
	}
	return observationFromRow(row)
}

// ListByEnvironment 列出某环境下的全部指标观测（按 metric_key 排序）。
func (s *Store) ListByEnvironment(ctx context.Context, environment string) ([]Observation, error) {
	rows, err := s.q.ListMetricObservationsByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list metric observations: %w", err)
	}
	out := make([]Observation, 0, len(rows))
	for _, r := range rows {
		o, err := observationFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// Get 读取单个指标观测。
func (s *Store) Get(ctx context.Context, metricKey, environment string) (Observation, error) {
	row, err := s.q.GetMetricObservation(ctx, gen.GetMetricObservationParams{
		MetricKey:   metricKey,
		Environment: environment,
	})
	if err != nil {
		return Observation{}, fmt.Errorf("get metric observation: %w", err)
	}
	return observationFromRow(row)
}

// InsertSample 追加一条历史样本。
//
// **周期同步任务不该用它**，用 UpsertWithSample。本方法留给「只补一个历史点、
// 不动最新态」的场景：历史回填、以及只验证样本表语义的测试。
//
// **失败观测也要追加**——趋势图上那段红正是从 status=failed 的样本里画出来的，
// 不写就等于让图假装那段时间什么都没发生。
//
// 不返回写入行：样本没有需要回读的服务端生成字段（自增 id 只服务于唯一性），
// 返回一个没人用的结构体只会诱使调用方去依赖它。
func (s *Store) InsertSample(ctx context.Context, o Observation) error {
	if err := o.ValidateSample(); err != nil {
		return err
	}
	params, err := sampleParams(o)
	if err != nil {
		return err
	}
	if err := s.q.InsertMetricObservationSample(ctx, params); err != nil {
		return fmt.Errorf("insert metric observation sample: %w", err)
	}
	return nil
}

// UpsertWithSample 在**同一个事务**里更新最新态并追加历史样本，是采集路径
// 唯一正确的写入方式（XM-R010，回归 Codex 冷审 PR #48 第 1 条）。
//
// 为什么必须同事务，而不是「先 Upsert 再 InsertSample，样本失败就重试」：
// 采集任务每次执行（首轮和每一次重试）都重新取当前时间、重新读一遍上游。
// 两写分离时，样本那一条挂掉会留下一个**自相矛盾**的库：最新态已经宣称
// 「T1 采到了 V」，历史里却永远没有 T1 这个点——T1 那一刻的上游数据已经过去，
// 没有任何补数途径。重试写的是 T2 的新快照，补不回 T1；对本轮已经写成功的
// 那几条指标，重试还会再添一个 T2 的点，值可能与 T1 不同。所以旧注释与
// README 说的「同一个 synced_at 上两个点、图上同一位置、无害」是**错的**：
// 重复点根本不在同一个 synced_at 上，而缺失的那个点不可恢复。
//
// 同事务之后语义变得干净：要么最新态与样本一起生效，要么两张表都没动。
// 失败时重试用新的 now 重读上游是**完整重放**而不是打补丁——上一轮什么都
// 没写进去，没有缺口需要补，也不存在只写了一半的自相矛盾状态。
//
// 一次事务只包**一条指标**的两写，不包整轮五条：整轮同事务会让一条指标的
// 库错误连带回滚另外四条已经算好的观测（包括那几条如实记录上游失败的
// failed 观测），把「四条真话 + 一条写不进去」变成「一条都不写」。粒度落在
// 单条指标上，不变式是「最新态里出现过的每一个 (metric_key, synced_at)，
// 历史里都有对应的点」——这正是被打破的那一条。整轮里其余指标是否写成功，
// 各自独立，不构成任何序列的缺口（见 docs/modules/ops/README.md）。
//
// 只跑 Validate 不再跑 ValidateSample：Validate 是它的超集（多校验一条
// staleness_threshold_seconds > 0，那是最新态才存的列），重复调用只是噪音。
func (s *Store) UpsertWithSample(ctx context.Context, o Observation) (Observation, error) {
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	upsert, err := upsertParams(o)
	if err != nil {
		return Observation{}, err
	}
	sample, err := sampleParams(o)
	if err != nil {
		return Observation{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("begin: %w", err)
	}
	// 任何提前 return 都回滚；Commit 成功后这次 Rollback 是空操作。
	defer func() { _ = tx.Rollback(ctx) }()

	q := gen.New(tx)
	row, err := q.UpsertMetricObservation(ctx, upsert)
	if err != nil {
		return Observation{}, fmt.Errorf("upsert metric observation: %w", err)
	}
	if err := q.InsertMetricObservationSample(ctx, sample); err != nil {
		// 上面那条 upsert 会随事务一起消失——这正是本方法存在的理由。
		return Observation{}, fmt.Errorf("insert metric observation sample: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Observation{}, fmt.Errorf("commit: %w", err)
	}
	// 提交后才解码返回值：解码失败不该让一次已经写成功的采集被报成失败。
	return observationFromRow(row)
}

// ListSamples 按 (synced_at, id) 升序返回某环境某指标在 since 之后的样本，
// 并如实报告窗口内是否有更旧的样本被丢掉。
//
// limit <= 0 或超过 MaxSampleLimit 时钳到 MaxSampleLimit：这是仓储层的硬闸门，
// 不指望每个调用方都记得传合理值——一个手滑的 0 不该变成全表扫描。
// 窗口内样本超量时被丢掉的是**最旧**的那些（理由见 db/queries/ops.sql）。
//
// 第二个返回值 truncated 报告「窗口里还有更旧的样本，但没返回」（XM-0031，
// 回归 Codex 冷审 PR #48 第 2 条：「允许 168 小时，却静默截成最新 1000 点，
// 响应没有任何『被截断』的事实」）。判据是多取一行：向库要 limit+1 条，真拿
// 到 limit+1 条就说明窗口内至少还剩一条。多一行的代价是常数，换来的是让「前
// 3.5 天真的没有数据」与「服务端把它裁掉了」变成两个可区分的事实（宪法 12 条）。
//
// 它是**返回值**而不是一个可选字段：签名强迫每个调用方处理截断，忘记传给前端
// 会在编译期就显形。悄悄丢一半窗口正是这条修复要根除的东西。
//
// 返回值复用 Observation，但样本表不存 ID / LastSuccess /
// StalenessThresholdSeconds，这三个字段在返回值里恒为零值。**不要对样本调用
// Freshness()**：新鲜度是相对「现在」算的派生量，对一个历史时刻算它没有意义，
// 何况阈值为零会让每个点都被判成 stale。历史点位需要的解释信息
// （status / is_partial / observed_at / last_error_code / source）都已逐点带回。
func (s *Store) ListSamples(
	ctx context.Context, environment, metricKey string, since time.Time, limit int32,
) ([]Observation, bool, error) {
	if limit <= 0 || limit > MaxSampleLimit {
		limit = MaxSampleLimit
	}
	// 多取一行专门用来判断截断，它不会进返回值。
	rows, err := s.q.ListMetricObservationSamples(ctx, gen.ListMetricObservationSamplesParams{
		Environment: environment,
		MetricKey:   metricKey,
		SyncedAt:    ts(since),
		Limit:       limit + 1,
	})
	if err != nil {
		return nil, false, fmt.Errorf("list metric observation samples: %w", err)
	}
	truncated := int32(len(rows)) > limit
	if truncated {
		// 行是升序的，多出来的那条在最前面（最旧）——正是被丢弃的方向。
		rows = rows[len(rows)-int(limit):]
	}
	out := make([]Observation, 0, len(rows))
	for _, r := range rows {
		// 与最新态同一条解码规则：金额 minor units 保持 json.Number，
		// 不经 float64（见 decodeValueJSON）。历史序列尤其关键——趋势图上
		// 一个被截断的大额会被当成真实的业务波动。
		value, err := decodeValueJSON(r.ValueJson)
		if err != nil {
			return nil, false, err
		}
		out = append(out, Observation{
			MetricKey:     r.MetricKey,
			Source:        r.Source,
			Environment:   r.Environment,
			ObservedAt:    fromTSPtr(r.ObservedAt),
			SyncedAt:      fromTS(r.SyncedAt),
			Watermark:     r.Watermark,
			Status:        SyncStatus(r.Status),
			IsPartial:     r.IsPartial,
			LastErrorCode: r.LastErrorCode,
			Value:         value,
		})
	}
	return out, truncated, nil
}

// PruneSamples 删除 synced_at 早于 cutoff 的样本，**一次最多 batchSize 行**。
//
// XM-R012（Codex 冷审 #48 第 8 条）：这张表 5 分钟粒度 ≈ 288 条/日/指标，
// 十几条指标一年就是百万量级，而在此之前没有任何清理路径。
//
// 返回本批实际删除的行数。调用方循环调用直到返回值 < batchSize——
// 分批的理由（长事务、行锁、WAL、与采集任务抢同一张表）见 db/queries/ops.sql。
//
// **不在这里循环**：一个「删到干净为止」的方法在积压很大时会跑很久，而它的
// 调用方（River 任务）需要在批之间检查 ctx 是否已取消、并把进度记进日志。
// 把循环留给调用方，这一层只负责「删一批」这件能被清楚描述的事。
func (s *Store) PruneSamples(ctx context.Context, cutoff time.Time, batchSize int32) (int64, error) {
	return s.q.PruneMetricSamples(ctx, gen.PruneMetricSamplesParams{
		Cutoff:    pgtype.Timestamptz{Time: cutoff.UTC(), Valid: true},
		BatchSize: batchSize,
	})
}
