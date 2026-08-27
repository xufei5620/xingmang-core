package ops

import (
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
//     只有 INSERT 与 SELECT，没有任何 UPDATE/DELETE 路径。
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

func observationFromRow(r gen.OpsMetricObservation) (Observation, error) {
	value := map[string]any{}
	if len(r.ValueJson) > 0 {
		if err := json.Unmarshal(r.ValueJson, &value); err != nil {
			return Observation{}, fmt.Errorf("value_json: %w", err)
		}
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

// Upsert 写入或覆盖某个 (metric_key, environment) 的最新观测。
func (s *Store) Upsert(ctx context.Context, o Observation) (Observation, error) {
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	value := o.Value
	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return Observation{}, fmt.Errorf("value_json: %w", err)
	}

	row, err := s.q.UpsertMetricObservation(ctx, gen.UpsertMetricObservationParams{
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
	})
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
// 与 Upsert 并列调用，不是替代：Upsert 维护「现在是什么」，本方法维护
// 「一路是怎么过来的」。**失败观测也要追加**——趋势图上那段红正是从
// status=failed 的样本里画出来的，不写就等于让图假装那段时间什么都没发生。
//
// 不返回写入行：样本没有需要回读的服务端生成字段（自增 id 只服务于唯一性），
// 返回一个没人用的结构体只会诱使调用方去依赖它。
func (s *Store) InsertSample(ctx context.Context, o Observation) error {
	if err := o.ValidateSample(); err != nil {
		return err
	}
	value := o.Value
	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("value_json: %w", err)
	}

	if err := s.q.InsertMetricObservationSample(ctx, gen.InsertMetricObservationSampleParams{
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
	}); err != nil {
		return fmt.Errorf("insert metric observation sample: %w", err)
	}
	return nil
}

// ListSamples 按 synced_at 升序返回某环境某指标在 since 之后的样本。
//
// limit <= 0 或超过 MaxSampleLimit 时钳到 MaxSampleLimit：这是仓储层的硬闸门，
// 不指望每个调用方都记得传合理值——一个手滑的 0 不该变成全表扫描。
// 窗口内样本超量时被丢掉的是**最旧**的那些（理由见 db/queries/ops.sql）。
//
// 返回值复用 Observation，但样本表不存 ID / LastSuccess /
// StalenessThresholdSeconds，这三个字段在返回值里恒为零值。**不要对样本调用
// Freshness()**：新鲜度是相对「现在」算的派生量，对一个历史时刻算它没有意义，
// 何况阈值为零会让每个点都被判成 stale。历史点位需要的解释信息
// （status / is_partial / observed_at / last_error_code）都已逐点带回。
func (s *Store) ListSamples(
	ctx context.Context, environment, metricKey string, since time.Time, limit int32,
) ([]Observation, error) {
	if limit <= 0 || limit > MaxSampleLimit {
		limit = MaxSampleLimit
	}
	rows, err := s.q.ListMetricObservationSamples(ctx, gen.ListMetricObservationSamplesParams{
		Environment: environment,
		MetricKey:   metricKey,
		SyncedAt:    ts(since),
		Limit:       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list metric observation samples: %w", err)
	}
	out := make([]Observation, 0, len(rows))
	for _, r := range rows {
		value := map[string]any{}
		if len(r.ValueJson) > 0 {
			if err := json.Unmarshal(r.ValueJson, &value); err != nil {
				return nil, fmt.Errorf("value_json: %w", err)
			}
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
	return out, nil
}
