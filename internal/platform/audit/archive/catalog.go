package archive

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrCatalogEmpty = fmt.Errorf("%w: catalog is empty", ErrObjectNotFound)

// ValidateCommittedSegment applies the provider-independent checks that must hold
// before a catalog row is materialized. Database-backed writers repeat these checks
// inside their transaction and additionally bind the row to RecoveryIndex generation.
func ValidateCommittedSegment(value CommittedSegment) error {
	if value.ID == uuid.Nil || value.RecoveryGeneration < 1 || !isLowerHex64(value.CheckpointSHA256) ||
		value.CommittedAt.IsZero() || value.VerifiedAt.IsZero() {
		return fmt.Errorf("%w: committed segment envelope", ErrArchiveValidation)
	}
	if err := ValidateManifestStructure(value.Manifest.Unsigned); err != nil {
		return err
	}
	if value.Manifest.Unsigned.FromSequence < 1 || value.Manifest.Unsigned.ToSequence < value.Manifest.Unsigned.FromSequence ||
		value.Manifest.Unsigned.RowCount != value.Manifest.Unsigned.ToSequence-value.Manifest.Unsigned.FromSequence+1 {
		return fmt.Errorf("%w: manifest range", ErrArchiveValidation)
	}
	if value.ManifestObject.BucketID == "" {
		return fmt.Errorf("%w: manifest object missing", ErrArchiveValidation)
	}
	if err := ValidateObjectVersion(value.ManifestObject); err != nil {
		return err
	}
	encoded, err := EncodeSignedManifestV1(value.Manifest)
	if err != nil {
		return err
	}
	if value.ManifestObject.SHA256 != sha256Hex(encoded) || value.ManifestObject.SizeBytes != int64(len(encoded)) {
		return fmt.Errorf("%w: manifest object digest/readback", ErrArchiveValidation)
	}
	wantKey := fmt.Sprintf("audit/v1/manifest/seq-%019d-%019d-%s.json",
		value.Manifest.Unsigned.FromSequence, value.Manifest.Unsigned.ToSequence, value.ManifestObject.SHA256)
	if value.ManifestObject.Key != wantKey {
		return fmt.Errorf("%w: manifest object locator", ErrArchiveValidation)
	}
	return nil
}

// ValidateCatalogCoverage proves that a candidate catalog row is covered by the
// exact RecoveryIndex generation. It is intentionally separate from the catalog
// interface because the PostgreSQL writer obtains the index snapshot in the same
// transaction; callers must not infer coverage from catalog rows alone.
func ValidateCatalogCoverage(segment CommittedSegment, index SignedRecoveryIndexV1) error {
	if err := ValidateCommittedSegment(segment); err != nil {
		return err
	}
	if err := ValidateRecoveryIndexBinding(index); err != nil {
		return err
	}
	if segment.RecoveryGeneration != index.Unsigned.Generation {
		return fmt.Errorf("%w: recovery generation mismatch", ErrCatalogConflict)
	}
	if segment.Manifest.Unsigned.ToSequence > index.Unsigned.TerminalSequence {
		return fmt.Errorf("%w: segment exceeds recovery terminal", ErrCatalogConflict)
	}
	if !isLowerHex64(segment.CheckpointSHA256) || segment.CheckpointSHA256 != index.Unsigned.Checkpoint.SHA256 {
		return fmt.Errorf("%w: checkpoint locator mismatch", ErrCatalogConflict)
	}
	if segment.Manifest.Unsigned.ToSequence == index.Unsigned.TerminalSequence {
		terminal := index.Unsigned.TerminalManifest
		if segment.ManifestObject.BucketID != terminal.BucketID || segment.ManifestObject.Key != terminal.Key ||
			segment.ManifestObject.VersionID != terminal.VersionID || segment.ManifestObject.SHA256 != terminal.SHA256 ||
			segment.Manifest.Unsigned.ChainRoot.RootHash != index.Unsigned.TerminalRootHash {
			return fmt.Errorf("%w: terminal manifest/root mismatch", ErrCatalogConflict)
		}
	}
	return nil
}

func segmentEquivalent(left, right CommittedSegment) bool {
	leftManifest, leftErr := EncodeSignedManifestV1(left.Manifest)
	rightManifest, rightErr := EncodeSignedManifestV1(right.Manifest)
	if leftErr != nil || rightErr != nil || sha256Hex(leftManifest) != sha256Hex(rightManifest) {
		return false
	}
	return left.Manifest.Unsigned.FromSequence == right.Manifest.Unsigned.FromSequence &&
		left.Manifest.Unsigned.ToSequence == right.Manifest.Unsigned.ToSequence &&
		left.ManifestObject.BucketID == right.ManifestObject.BucketID &&
		left.ManifestObject.Key == right.ManifestObject.Key &&
		left.ManifestObject.VersionID == right.ManifestObject.VersionID &&
		left.ManifestObject.SHA256 == right.ManifestObject.SHA256 &&
		left.CheckpointSHA256 == right.CheckpointSHA256 &&
		left.RecoveryGeneration == right.RecoveryGeneration
}

func cloneCommittedSegment(value CommittedSegment) CommittedSegment {
	value.Manifest.Unsigned.Projections = append([]ProjectionRefV1(nil), value.Manifest.Unsigned.Projections...)
	return value
}

// MemoryCatalog is a deterministic, append-only fixture used by protocol tests.
// It models the contiguous transaction rule without pretending to be the PostgreSQL
// catalog implementation.
type MemoryCatalog struct {
	mu       sync.RWMutex
	segments []CommittedSegment
}

func NewMemoryCatalog() *MemoryCatalog { return &MemoryCatalog{} }

func (c *MemoryCatalog) CommitCoveredSegment(ctx context.Context, value CommittedSegment) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("%w: nil catalog", ErrCatalogConflict)
	}
	if err := ValidateCommittedSegment(value); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.segments) > 0 {
		latest := c.segments[len(c.segments)-1]
		if latest.Manifest.Unsigned.ToSequence == math.MaxInt64 {
			return fmt.Errorf("%w: catalog sequence exhausted", ErrCatalogConflict)
		}
		wantFrom := latest.Manifest.Unsigned.ToSequence + 1
		if value.Manifest.Unsigned.FromSequence != wantFrom {
			for _, existing := range c.segments {
				if existing.Manifest.Unsigned.FromSequence == value.Manifest.Unsigned.FromSequence && segmentEquivalent(existing, value) {
					return nil
				}
			}
			return fmt.Errorf("%w: expected from_sequence=%d got=%d", ErrCatalogConflict, wantFrom, value.Manifest.Unsigned.FromSequence)
		}
	} else if value.Manifest.Unsigned.FromSequence != 1 {
		return fmt.Errorf("%w: first segment must start at sequence 1", ErrCatalogConflict)
	}
	c.segments = append(c.segments, cloneCommittedSegment(value))
	return nil
}

func (c *MemoryCatalog) CommitCoveredSegmentWithIndex(ctx context.Context, value CommittedSegment, index SignedRecoveryIndexV1) error {
	if err := ValidateCatalogCoverage(value, index); err != nil {
		return err
	}
	return c.CommitCoveredSegment(ctx, value)
}

func (c *MemoryCatalog) Latest(ctx context.Context) (CommittedSegment, error) {
	if err := contextErr(ctx); err != nil {
		return CommittedSegment{}, err
	}
	if c == nil {
		return CommittedSegment{}, ErrCatalogEmpty
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.segments) == 0 {
		return CommittedSegment{}, ErrCatalogEmpty
	}
	return cloneCommittedSegment(c.segments[len(c.segments)-1]), nil
}

func (c *MemoryCatalog) ListBefore(ctx context.Context, before int64, limit int32) ([]CommittedSegment, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if c == nil || limit <= 0 {
		return nil, fmt.Errorf("%w: positive limit required", ErrArchiveValidation)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]CommittedSegment, 0, minInt(int(limit), len(c.segments)))
	for index := len(c.segments) - 1; index >= 0 && len(result) < int(limit); index-- {
		segment := c.segments[index]
		if before > 0 && segment.Manifest.Unsigned.ToSequence >= before {
			continue
		}
		result = append(result, cloneCommittedSegment(segment))
	}
	return result, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// ValidateCatalogOrdering is useful to database reconciliation code and remains
// free of I/O. It rejects gaps, overlap, duplicate ranges and catalog rows that
// are not sorted by terminal sequence.
func ValidateCatalogOrdering(rows []CommittedSegment) error {
	if len(rows) == 0 {
		return nil
	}
	want := int64(1)
	var previousTo int64
	for index, row := range rows {
		if err := ValidateCommittedSegment(row); err != nil {
			return err
		}
		if index > 0 && row.Manifest.Unsigned.FromSequence <= previousTo {
			return fmt.Errorf("%w: catalog rows are not strictly ordered", ErrCatalogConflict)
		}
		if row.Manifest.Unsigned.FromSequence != want {
			return fmt.Errorf("%w: catalog gap/overlap at %d", ErrCatalogConflict, want)
		}
		previousTo = row.Manifest.Unsigned.ToSequence
		if previousTo == math.MaxInt64 {
			if index != len(rows)-1 {
				return fmt.Errorf("%w: sequence overflow after terminal row", ErrCatalogConflict)
			}
			return nil
		}
		want = previousTo + 1
	}
	return nil
}

func isArchiveTimestamp(value time.Time) bool { return !value.IsZero() }
