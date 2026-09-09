package platformusers

import (
	"context"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// GetUser 是 CORE_APPROVAL 范围内的 Fake/core 精确查询；它不代表任何 real 上游能力。
func (c *FakeClient) GetUser(ctx context.Context, query GetUserQuery) (UserDetail, error) {
	if err := ctx.Err(); err != nil {
		return UserDetail{}, err
	}
	if err := query.Ref.Validate(); err != nil || query.Ref.Platform != c.source {
		return UserDetail{}, connectorBadSource(query.Ref.Platform)
	}
	period, err := (Period{Day: query.Day, Granularity: query.Granularity}).Normalize(c.now().UTC(), nil)
	if err != nil {
		return UserDetail{}, connectorBadPeriod(err)
	}
	now := c.now().UTC()
	for _, raw := range fakeUsers {
		if raw.id == query.Ref.ID {
			caps := []registry.Capability{CapabilityUserDetailRead}
			if c.source == SourceSub2API {
				caps = append(caps, CapabilityUserDailyUsageRead, CapabilityUserKeysMetadataRead)
			}
			return UserDetail{
				Ref:    query.Ref,
				User:   c.toUser(raw, now, period),
				Period: period,
				Snapshot: EvidenceSnapshot{
					ObservedAt: now, Source: c.source + "-fake",
					Watermark: "wm-fake-detail-" + now.Format("20060102T150405Z"),
				},
				Capabilities: caps,
			}, nil
		}
	}
	return UserDetail{}, ErrNotFound
}

func (c *FakeClient) V2Capabilities() []registry.Capability {
	if c.source == SourceSub2API {
		return []registry.Capability{CapabilityUserDetailRead, CapabilityUserDailyUsageRead, CapabilityUserKeysMetadataRead}
	}
	return []registry.Capability{CapabilityUserDetailRead}
}

// V2KeyCapabilities 返回本 Fake 客户端可提供的 Key 元数据能力。
// 这里只声明元数据能力；完整 Key、复制和导出永远不属于本契约。
func (c *FakeClient) V2KeyCapabilities() []registry.Capability {
	if c.source != SourceSub2API {
		return nil
	}
	return []registry.Capability{CapabilityUserKeysMetadataRead}
}
