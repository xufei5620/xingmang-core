package publishing

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// memStore 是 Store 的内存实现，只服务不需要真库的用例（控制流、错误映射、
// 「有没有投递器」那条缺席主张）。真正的 SQL 语义由 store_integration_test.go
// 对着真库验证——两者分工，不互相冒充。
type memStore struct {
	channels  map[uuid.UUID]Channel
	drafts    map[uuid.UUID]Draft
	revisions map[uuid.UUID][]Revision
	assets    map[uuid.UUID]Asset
	records   []PublishRecord
	// failCreateRecord 让用例能模拟落库失败。
	failCreateRecord error
}

func newMemStore() *memStore {
	return &memStore{
		channels:  map[uuid.UUID]Channel{},
		drafts:    map[uuid.UUID]Draft{},
		revisions: map[uuid.UUID][]Revision{},
		assets:    map[uuid.UUID]Asset{},
	}
}

var _ Store = (*memStore)(nil)

func (m *memStore) UpsertChannel(_ context.Context, c Channel) (Channel, error) {
	for id, existing := range m.channels {
		if existing.Platform == c.Platform && existing.Handle == c.Handle {
			existing.DisplayName, existing.Purpose = c.DisplayName, c.Purpose
			existing.CredentialRef, existing.Note = c.CredentialRef, c.Note
			existing.UpdatedAt = c.CreatedAt
			m.channels[id] = existing
			return existing, nil
		}
	}
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	c.UpdatedAt = c.CreatedAt
	m.channels[c.ID] = c
	return c, nil
}

func (m *memStore) SetChannelStatus(_ context.Context, id uuid.UUID, status ChannelStatus, at time.Time) (Channel, error) {
	c, ok := m.channels[id]
	if !ok {
		return Channel{}, ErrNotFound
	}
	c.Status, c.UpdatedAt = status, at
	m.channels[id] = c
	return c, nil
}

func (m *memStore) GetChannel(_ context.Context, id uuid.UUID) (Channel, error) {
	c, ok := m.channels[id]
	if !ok {
		return Channel{}, ErrNotFound
	}
	return c, nil
}

func (m *memStore) ListChannels(_ context.Context, _ int32) ([]Channel, error) {
	out := make([]Channel, 0, len(m.channels))
	for _, c := range m.channels {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

func (m *memStore) SaveDraft(_ context.Context, in DraftInput) (Draft, error) {
	if err := ValidateAssetCount(len(in.AssetIDs)); err != nil {
		return Draft{}, err
	}
	var d Draft
	if in.ID == nil {
		d = Draft{ID: uuid.New(), Title: in.Title, Body: in.Body,
			ScheduledAt: in.ScheduledAt, CurrentVersion: 1,
			CreatedBy: in.Actor, CreatedAt: in.Now}
	} else {
		existing, ok := m.drafts[*in.ID]
		if !ok || existing.Status == DraftArchived {
			return Draft{}, ErrNotFound
		}
		d = existing
		d.Title, d.Body, d.ScheduledAt = in.Title, in.Body, in.ScheduledAt
		d.CurrentVersion++
	}
	d.Status = statusFor(in.ScheduledAt)
	d.UpdatedAt = in.Now
	d.AssetIDs = nil
	for _, id := range in.AssetIDs {
		if _, ok := m.assets[id]; !ok {
			return Draft{}, fmt.Errorf("%w: 素材 %s 不存在", ErrInvalidInput, id)
		}
		d.AssetIDs = append(d.AssetIDs, id)
	}
	m.drafts[d.ID] = d
	m.revisions[d.ID] = append(m.revisions[d.ID], Revision{
		DraftID: d.ID, Version: d.CurrentVersion, Title: d.Title, Body: d.Body,
		ScheduledAt: d.ScheduledAt, Note: in.Note, CreatedBy: in.Actor, CreatedAt: in.Now,
	})
	return d, nil
}

func (m *memStore) ArchiveDraft(_ context.Context, id uuid.UUID, at time.Time) (Draft, error) {
	d, ok := m.drafts[id]
	if !ok {
		return Draft{}, ErrNotFound
	}
	d.Status, d.ScheduledAt, d.UpdatedAt = DraftArchived, nil, at
	m.drafts[id] = d
	return d, nil
}

func (m *memStore) GetDraft(_ context.Context, id uuid.UUID) (Draft, error) {
	d, ok := m.drafts[id]
	if !ok {
		return Draft{}, ErrNotFound
	}
	return d, nil
}

func (m *memStore) ListDrafts(_ context.Context, f DraftFilter) ([]Draft, error) {
	out := make([]Draft, 0, len(m.drafts))
	for _, d := range m.drafts {
		if f.Status != "" && d.Status != f.Status {
			continue
		}
		if !f.ScheduledFrom.IsZero() && (d.ScheduledAt == nil || d.ScheduledAt.Before(f.ScheduledFrom)) {
			continue
		}
		if !f.ScheduledTo.IsZero() && (d.ScheduledAt == nil || !d.ScheduledAt.Before(f.ScheduledTo)) {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

func (m *memStore) ListRevisions(_ context.Context, draftID uuid.UUID) ([]Revision, error) {
	src := m.revisions[draftID]
	out := make([]Revision, len(src))
	copy(out, src)
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (m *memStore) UpsertAsset(_ context.Context, a Asset) (Asset, error) {
	for id, existing := range m.assets {
		if existing.Name == a.Name {
			existing.Kind, existing.URI, existing.Note = a.Kind, a.URI, a.Note
			existing.UpdatedAt = a.CreatedAt
			m.assets[id] = existing
			return existing, nil
		}
	}
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	a.UpdatedAt = a.CreatedAt
	m.assets[a.ID] = a
	return a, nil
}

func (m *memStore) RemoveAsset(_ context.Context, id uuid.UUID) error {
	if _, ok := m.assets[id]; !ok {
		return ErrNotFound
	}
	for _, d := range m.drafts {
		for _, assetID := range d.AssetIDs {
			if assetID == id {
				return fmt.Errorf("%w: 素材仍被草稿引用，先从草稿里移除", ErrConflict)
			}
		}
	}
	delete(m.assets, id)
	return nil
}

func (m *memStore) GetAsset(_ context.Context, id uuid.UUID) (Asset, error) {
	a, ok := m.assets[id]
	if !ok {
		return Asset{}, ErrNotFound
	}
	return a, nil
}

func (m *memStore) ListAssets(_ context.Context, _ int32) ([]Asset, error) {
	out := make([]Asset, 0, len(m.assets))
	for _, a := range m.assets {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memStore) AssetsForDraft(_ context.Context, draftID uuid.UUID) ([]Asset, error) {
	d, ok := m.drafts[draftID]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]Asset, 0, len(d.AssetIDs))
	for _, id := range d.AssetIDs {
		if a, ok := m.assets[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// CreatePublishRecord 同时**模拟迁移 000052 的两道 CHECK**。
//
// 不模拟的话，「未投递的记录不许带编号/投递时刻」这条约束在内存用例里就是
// 空的，而恰恰是这条约束在挡住「悄悄接上一半投递」。真库那一侧另有用例
// （TestPublishRecordRejectsDeliveredShape）验证 CHECK 本身确实存在。
func (m *memStore) CreatePublishRecord(_ context.Context, r PublishRecord) (PublishRecord, error) {
	if m.failCreateRecord != nil {
		return PublishRecord{}, m.failCreateRecord
	}
	if r.Result != ResultNotDelivered {
		return PublishRecord{}, fmt.Errorf("%w: 发布记录不合库层约束", ErrInvalidInput)
	}
	if r.ExternalRef != "" || r.DeliveredAt != nil {
		return PublishRecord{}, fmt.Errorf("%w: 发布记录不合库层约束", ErrInvalidInput)
	}
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	m.records = append(m.records, r)
	return r, nil
}

func (m *memStore) ListPublishRecords(_ context.Context, _ int32) ([]PublishRecord, error) {
	out := make([]PublishRecord, len(m.records))
	copy(out, m.records)
	return out, nil
}
