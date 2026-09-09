package publishing

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DraftInput 是一次「保存草稿」的入参。
//
// ID 为 nil 表示新建；非 nil 表示在既有草稿上落一版新修订。两件事共用一个
// 入口是因为它们对调用方是同一件事（「把我写的这一版存下来」），而版本号的
// 推进必须与写入原子——分成 Create/Update 两个方法会让「先读版本号再写」的
// 竞态从仓储里漏到调用方。
type DraftInput struct {
	ID          *uuid.UUID
	Title       string
	Body        string
	ScheduledAt *time.Time
	// Note 是这一版改了什么，进 draft_revision，不进 draft。
	Note     string
	AssetIDs []uuid.UUID
	Actor    string
	Now      time.Time
}

// DraftFilter 是草稿列表的过滤条件。
type DraftFilter struct {
	// Status 为空表示不过滤。
	Status DraftStatus
	// ScheduledFrom / ScheduledTo 划定排期区间（左闭右开），供内容日历使用。
	// 两者都为零值表示不按排期过滤。
	ScheduledFrom time.Time
	ScheduledTo   time.Time
	Limit         int32
}

// Store 是内容发布的持久化面。
//
// 拆成接口而不是直接用 PgStore：Action handler 的用例不必起容器就能跑，而
// 「没有出站投递器」那条缺席用例本来就不需要真数据库——它要证明的是控制流，
// 不是 SQL。
type Store interface {
	UpsertChannel(ctx context.Context, c Channel) (Channel, error)
	SetChannelStatus(ctx context.Context, id uuid.UUID, status ChannelStatus, at time.Time) (Channel, error)
	GetChannel(ctx context.Context, id uuid.UUID) (Channel, error)
	ListChannels(ctx context.Context, limit int32) ([]Channel, error)

	// SaveDraft 在一个事务里落草稿、推进版本号、写修订、重建素材引用。
	SaveDraft(ctx context.Context, in DraftInput) (Draft, error)
	ArchiveDraft(ctx context.Context, id uuid.UUID, at time.Time) (Draft, error)
	GetDraft(ctx context.Context, id uuid.UUID) (Draft, error)
	ListDrafts(ctx context.Context, f DraftFilter) ([]Draft, error)
	ListRevisions(ctx context.Context, draftID uuid.UUID) ([]Revision, error)

	UpsertAsset(ctx context.Context, a Asset) (Asset, error)
	// RemoveAsset 删除素材。仍被草稿引用时返回 ErrConflict——库层是 RESTRICT
	// 外键，这里把它翻成一个说得清的领域错误。
	RemoveAsset(ctx context.Context, id uuid.UUID) error
	GetAsset(ctx context.Context, id uuid.UUID) (Asset, error)
	ListAssets(ctx context.Context, limit int32) ([]Asset, error)
	AssetsForDraft(ctx context.Context, draftID uuid.UUID) ([]Asset, error)

	CreatePublishRecord(ctx context.Context, r PublishRecord) (PublishRecord, error)
	ListPublishRecords(ctx context.Context, limit int32) ([]PublishRecord, error)
}
