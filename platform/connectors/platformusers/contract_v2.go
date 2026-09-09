package platformusers

import (
	"context"
	"errors"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	CapabilityUserDetailRead       = registry.Capability("platformusers.user.detail_read")
	CapabilityUserDailyUsageRead   = registry.Capability("platformusers.user.daily_usage_read")
	CapabilityUserKeysMetadataRead = registry.Capability("platformusers.user.keys_metadata_read")
)

type EvidenceSnapshot struct {
	ObservedAt time.Time `json:"observed_at"`
	Source     string    `json:"source"`
	Watermark  string    `json:"watermark"`
	IsPartial  bool      `json:"is_partial"`
}

type GetUserQuery struct {
	Ref         UserRef
	Day         string
	Granularity Granularity
}

type UserDetail struct {
	Ref          UserRef
	User         User
	RegisteredAt time.Time
	Period       Period
	Snapshot     EvidenceSnapshot
	Capabilities []registry.Capability
}

type UserDetailReader interface {
	GetUser(context.Context, GetUserQuery) (UserDetail, error)
}

var ErrLookupIncomplete = errors.New("platformusers: exact lookup incomplete")
