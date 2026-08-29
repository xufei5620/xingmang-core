package platformusers

import (
	"context"
	"errors"
	"strings"
	"time"
)

type KeyMetadataQuery struct {
	Ref    UserRef
	Limit  int
	Cursor string
}

type KeyMetadata struct {
	ID           string
	Prefix       string
	Status       string
	CreatedAt    time.Time
	LastUsedAt   time.Time
	TodayPeakRPM CountValue
}

type KeyMetadataPage struct {
	Items      []KeyMetadata
	NextCursor string
	Snapshot   EvidenceSnapshot
}

type KeyMetadataReader interface {
	ListKeyMetadata(context.Context, KeyMetadataQuery) (KeyMetadataPage, error)
}

var ErrInvalidKeyMetadataQuery = errors.New("platformusers: invalid key metadata query")

func ValidateKeyPrefix(prefix string) error {
	if !utf8String(prefix) || len([]rune(prefix)) > 8 {
		return ErrInvalidKeyMetadataQuery
	}
	return nil
}

func utf8String(value string) bool {
	return strings.ToValidUTF8(value, "") == value
}

func (c *FakeClient) ListKeyMetadata(ctx context.Context, query KeyMetadataQuery) (KeyMetadataPage, error) {
	if err := ctx.Err(); err != nil {
		return KeyMetadataPage{}, err
	}
	if err := query.Ref.Validate(); err != nil || query.Ref.Platform != c.source {
		return KeyMetadataPage{}, connectorBadSource(query.Ref.Platform)
	}
	limit := query.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return KeyMetadataPage{}, connectorBadCursor(query.Cursor)
	}
	items := []KeyMetadata{{ID: "key-meta-1", Prefix: "sk-a1b2", Status: "active", TodayPeakRPM: KnownCount(42)}, {ID: "key-meta-2", Prefix: "sk-f6g7", Status: "revoked", TodayPeakRPM: KnownCount(0)}}
	if query.Cursor != "" {
		items = items[1:]
	}
	if len(items) > limit {
		items = items[:limit]
	}
	next := ""
	if len(items) == 1 && query.Cursor == "" {
		next = "key-meta-1"
	}
	now := c.now().UTC()
	return KeyMetadataPage{Items: items, NextCursor: next, Snapshot: EvidenceSnapshot{ObservedAt: now, Source: c.source + "-fake", Watermark: "wm-fake-keys-" + now.Format("20060102T150405Z")}}, nil
}
