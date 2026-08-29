package platformusers

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type KeyMetadataQuery struct {
	Ref    UserRef
	Limit  int
	Cursor string
}

// KeyMetadata deliberately contains metadata only. A record ID is an opaque
// inventory identifier and must not be usable as a credential.
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

// ValidateKeyMetadataID keeps record IDs opaque and bounded. IDs that look like
// a source token, secret URI, or an unbounded user-provided blob are rejected at
// the connector boundary instead of being reflected back by HTTP.
func ValidateKeyMetadataID(id string) error {
	trimmed := strings.TrimSpace(id)
	if id != trimmed || trimmed == "" || !utf8String(id) || len([]rune(id)) > 128 {
		return ErrInvalidKeyMetadataQuery
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrInvalidKeyMetadataQuery
		}
	}
	lower := strings.ToLower(id)
	for _, marker := range []string{"sk-", "sk_", "secret://", "secret", "plaintext", "password", "token", "token-hash", "credential", "api_key", "api-key", "apikey", "email", "phone", "tax", "bank", "address", "complete-key-sentinel"} {
		if strings.Contains(lower, marker) {
			return ErrInvalidKeyMetadataQuery
		}
	}
	if strings.Contains(id, "@") || looksLikePhone(id) {
		return ErrInvalidKeyMetadataQuery
	}
	return nil
}

// ValidateKeyMetadataCursor only enforces the transport safety boundary. The
// cursor remains opaque so each real reader may choose its own encoding; it may
// not contain whitespace or credential-like material and is bounded in size.
func ValidateKeyMetadataCursor(cursor string) error {
	if cursor == "" {
		return nil
	}
	if !utf8String(cursor) || len([]rune(cursor)) > 512 {
		return ErrInvalidKeyMetadataQuery
	}
	for _, r := range cursor {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrInvalidKeyMetadataQuery
		}
	}
	lower := strings.ToLower(cursor)
	for _, marker := range []string{"secret://", "secret", "plaintext", "password", "token", "token-hash", "credential", "api_key", "api-key", "apikey", "email", "phone", "tax", "bank", "address", "complete-key-sentinel"} {
		if strings.Contains(lower, marker) {
			return ErrInvalidKeyMetadataQuery
		}
	}
	if strings.Contains(cursor, "@") || looksLikePhone(cursor) {
		return ErrInvalidKeyMetadataQuery
	}
	return nil
}

func looksLikePhone(value string) bool {
	digits := 0
	separated := false
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits++
			continue
		}
		if r == '+' || r == '-' || r == ' ' || r == '(' || r == ')' {
			separated = true
			continue
		}
		return false
	}
	// Keep opaque numeric record IDs legal; reject common phone-shaped values
	// when they carry an international/formatting separator or the canonical
	// mainland 11-digit 1xxxxxxxxxx form.
	return separated && digits >= 7 || (digits == 11 && strings.HasPrefix(value, "1"))
}

// ValidateKeyPrefix protects the metadata boundary: even a prefix is capped at
// eight Unicode code points so it cannot become a usable credential fragment.
func ValidateKeyPrefix(prefix string) error {
	if prefix != strings.TrimSpace(prefix) || !utf8String(prefix) || len([]rune(prefix)) > 8 {
		return ErrInvalidKeyMetadataQuery
	}
	for _, r := range prefix {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ErrInvalidKeyMetadataQuery
		}
	}
	if strings.Contains(prefix, "@") || looksLikePhone(prefix) {
		return ErrInvalidKeyMetadataQuery
	}
	return nil
}

func utf8String(value string) bool {
	return strings.ToValidUTF8(value, "") == value
}

// fakeKeyMetadata derives a small, deterministic inventory from the selected
// user. It intentionally never stores or returns the source token; only the
// already-redacted prefix is exposed.
func fakeKeyMetadata(ref UserRef, now time.Time) ([]KeyMetadata, error) {
	for _, raw := range fakeUsers {
		if raw.id != ref.ID {
			continue
		}
		prefix := MaskTokenPrefix(raw.token)
		if prefix == "" {
			// An account with no API token has no key metadata row. Do not
			// synthesize an empty-prefix record that could be mistaken for one.
			return []KeyMetadata{}, nil
		}
		if err := ValidateKeyPrefix(prefix); err != nil {
			return nil, connectorBadCursor(prefix)
		}
		created := now.Add(-time.Duration(14+len(raw.id)) * 24 * time.Hour)
		lastUsed := time.Time{}
		if raw.lastHours >= 0 {
			lastUsed = now.Add(-time.Duration(raw.lastHours) * time.Hour)
		}
		items := []KeyMetadata{{
			ID:           "km-" + hex.EncodeToString([]byte(ref.ID)) + "-primary",
			Prefix:       prefix,
			Status:       "active",
			CreatedAt:    created,
			LastUsedAt:   lastUsed,
			TodayPeakRPM: KnownCount(42),
		}}
		if err := ValidateKeyMetadataID(items[0].ID); err != nil {
			return nil, err
		}
		// Give one user a revoked metadata row so the UI demonstrates both
		// states without exposing any additional credential material.
		if ref.ID == "u_10241" {
			items = append(items, KeyMetadata{
				ID:           "km-" + hex.EncodeToString([]byte(ref.ID)) + "-old",
				Prefix:       "sk-old1",
				Status:       "revoked",
				CreatedAt:    created.Add(-90 * 24 * time.Hour),
				LastUsedAt:   now.Add(-45 * 24 * time.Hour),
				TodayPeakRPM: KnownCount(0),
			})
			if err := ValidateKeyMetadataID(items[1].ID); err != nil {
				return nil, err
			}
		}
		return items, nil
	}
	return nil, ErrNotFound
}

// keyCursor is opaque to callers while binding the offset to the exact
// UserRef. The user ID is hex-encoded only as cursor state, never a credential.
func keyCursor(ref UserRef, offset int) string {
	return fmt.Sprintf("km-%x-%d", []byte(ref.Platform+"\x00"+ref.ID), offset)
}

func parseKeyCursor(ref UserRef, cursor string) (int, error) {
	if err := ValidateKeyMetadataCursor(cursor); err != nil {
		return 0, connectorBadCursor(cursor)
	}
	if cursor == "" {
		return 0, nil
	}
	parts := strings.Split(cursor, "-")
	if len(parts) != 3 || parts[0] != "km" {
		return 0, connectorBadCursor(cursor)
	}
	bound, err := hex.DecodeString(parts[1])
	if err != nil || string(bound) != ref.Platform+"\x00"+ref.ID {
		return 0, connectorBadCursor(cursor)
	}
	offset, err := strconv.Atoi(parts[2])
	if err != nil || offset < 0 {
		return 0, connectorBadCursor(cursor)
	}
	return offset, nil
}

func (c *FakeClient) ListKeyMetadata(ctx context.Context, query KeyMetadataQuery) (KeyMetadataPage, error) {
	if err := ctx.Err(); err != nil {
		return KeyMetadataPage{}, err
	}
	if c.source != SourceSub2API {
		return KeyMetadataPage{}, notSupported(c.source, "Fake 客户端未声明 Key metadata capability")
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
	now := c.now().UTC()
	items, err := fakeKeyMetadata(query.Ref, now)
	if err != nil {
		return KeyMetadataPage{}, err
	}
	offset, err := parseKeyCursor(query.Ref, query.Cursor)
	if err != nil {
		return KeyMetadataPage{}, err
	}
	if offset > len(items) {
		return KeyMetadataPage{}, connectorBadCursor(query.Cursor)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = keyCursor(query.Ref, end)
	}
	return KeyMetadataPage{
		Items:      items[offset:end],
		NextCursor: next,
		Snapshot:   EvidenceSnapshot{ObservedAt: now, Source: c.source + "-fake", Watermark: "wm-fake-keys-" + now.Format("20060102T150405Z")},
	}, nil
}
