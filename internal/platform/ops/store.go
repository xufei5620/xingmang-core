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

// Store 是指标观测仓储。
//
// 每个 (metric_key, environment) 只保留最新一条：看板要的是「现在是什么」，
// 历史序列另有归档任务（本模块不承担时序存储）。
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
