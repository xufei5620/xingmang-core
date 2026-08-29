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
			return UserDetail{
				Ref:    query.Ref,
				User:   c.toUser(raw, now, period),
				Period: period,
				Snapshot: EvidenceSnapshot{
					ObservedAt: now, Source: c.source + "-fake",
					Watermark: "wm-fake-detail-" + now.Format("20060102T150405Z"),
				},
				Capabilities: []registry.Capability{CapabilityUserDetailRead, CapabilityUserDailyUsageRead},
			}, nil
		}
	}
	return UserDetail{}, ErrNotFound
}

func (c *FakeClient) V2Capabilities() []registry.Capability {
	return []registry.Capability{CapabilityUserDetailRead, CapabilityUserDailyUsageRead}
}

func (c *FakeClient) V2KeyCapabilities() []registry.Capability {
	return []registry.Capability{CapabilityUserKeysMetadataRead}
}
