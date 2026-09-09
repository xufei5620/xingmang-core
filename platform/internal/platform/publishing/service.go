package publishing

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Service 是内容发布的领域服务。
//
// deliverers 表在生产装配里是 **nil**（见 cmd/platform-api）。它不是配置项，
// 也没有任何开关能把它填上——本仓库里根本不存在 Deliverer 的实现。见 doc.go。
type Service struct {
	store      Store
	deliverers delivererTable
	now        func() time.Time
}

// NewService 创建服务。deliverers 传 nil 表示没有任何出站投递器。
func NewService(store Store, deliverers map[Platform]Deliverer, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	var table delivererTable
	if len(deliverers) > 0 {
		table = make(delivererTable, len(deliverers))
		for p, d := range deliverers {
			table[p] = d
		}
	}
	return &Service{store: store, deliverers: table, now: now}
}

// Store 暴露仓储给只读 Query 层。
//
// Query 走的是「读取一律 Query」那条路（宪法 2 条），不经 Action；把仓储交给
// HTTP 层直接读，比在这里镜像一遍全部列表方法要少一层会漂的转译。
func (s *Service) Store() Store { return s.store }

// PublishRequest 是一次「提起对外发布」的入参。
type PublishRequest struct {
	DraftID     uuid.UUID
	ChannelID   uuid.UUID
	RequestedBy string
}

// PublishOutcome 是一次发布请求的结果。
type PublishOutcome struct {
	Record PublishRecord
	// Delivered 报告内容是否真的发出去了。**今天恒为 false**。
	Delivered bool
	// Reason 是给人看的一句话；未投递时逐字等于 NotDeliveredReason。
	Reason string
}

// Publish 提起一次对外发布。
//
// 顺序：取草稿 → 取渠道 → 渠道是否接受内容 → **查这个平台有没有出站投递器** →
// 落发布记录。
//
// 第四步今天必然落空（`DelivererFor` 恒为 nil），于是记录的结果是「未投递」，
// detail 逐字是 NotDeliveredReason。这不是失败——审批与排期确实发生了，
// 记录也确实落了；没发生的只有「发出去」这一件事，而这条记录如实说了。
//
// **不把它做成错误返回**是刻意的：返回错误会让页面显示成红色的「发布失败」，
// 运营会去重试、去查网络、去问是不是凭据配错了——而真相是这个能力还不存在。
func (s *Service) Publish(ctx context.Context, in PublishRequest) (PublishOutcome, error) {
	draft, err := s.store.GetDraft(ctx, in.DraftID)
	if err != nil {
		return PublishOutcome{}, err
	}
	if draft.Status == DraftArchived {
		return PublishOutcome{}, fmt.Errorf("%w: 草稿已归档，不能发布", ErrInvalidInput)
	}
	channel, err := s.store.GetChannel(ctx, in.ChannelID)
	if err != nil {
		return PublishOutcome{}, err
	}
	if !channel.AcceptsContent() {
		return PublishOutcome{}, fmt.Errorf("%w: 渠道 %s 当前状态是 %s",
			ErrChannelNotPublishable, channel.Handle, channel.Status)
	}

	record := PublishRecord{
		ID:           uuid.New(),
		DraftID:      draft.ID,
		DraftVersion: draft.CurrentVersion,
		ChannelID:    channel.ID,
		ScheduledAt:  draft.ScheduledAt,
		RequestedBy:  in.RequestedBy,
		Result:       ResultNotDelivered,
		Detail:       NotDeliveredReason,
		CreatedAt:    s.now().UTC(),
	}

	if deliverer := s.DelivererFor(channel.Platform); deliverer != nil {
		// 这一段今天**跑不到**：本仓库没有任何 Deliverer 实现，生产装配传的是
		// nil 表。留着它是第二层的接缝，也是那条缺席用例的变异靶点——往表里
		// 塞一个假投递器，用例就会走进这里并变红。
		//
		// 真接投递器时这里还差三样东西，不补齐不许上线：幂等键（避免审批单
		// 被重复触发时发两遍）、失败重试策略、以及把 result 枚举放开的迁移。
		// 所以这里**不**在成功时写 external_ref / delivered_at——库层的 CHECK
		// 会拒绝那样一条记录，让「悄悄接上一半」当场失败。
		return PublishOutcome{}, fmt.Errorf(
			"publishing: 平台 %s 登记了出站投递器，但投递链路（幂等、重试、库层结果枚举）尚未接通", channel.Platform)
	}

	saved, err := s.store.CreatePublishRecord(ctx, record)
	if err != nil {
		return PublishOutcome{}, err
	}
	return PublishOutcome{Record: saved, Delivered: false, Reason: NotDeliveredReason}, nil
}
