package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit/gen"
)

const archiveCatalogAdvisoryLock int64 = 4771002

// CatalogCoverageChecker is supplied by the archive service after it has loaded
// and verified the fixed RecoveryIndex. A nil checker is fail-closed: a catalog
// row must never be materialized without an index-generation proof.
type CatalogCoverageChecker func(context.Context, CommittedSegment) error

// CatalogCoverageProof is an immutable snapshot of the externally signed
// RecoveryIndex facts needed to bind one catalog row. Callers obtain it from
// the fixed locator before opening the PostgreSQL transaction; the writer then
// rechecks these exact bytes under the catalog advisory lock, eliminating a
// mutable-index TOCTOU window.
type CatalogCoverageProof struct {
	Generation                int64
	CheckpointSHA256          string
	TerminalManifestSHA256    string
	TerminalManifestKey       string
	TerminalManifestVersionID string
	TerminalSequence          int64
	TerminalRootHash          string
}

type CatalogCoverageProofProvider func(context.Context, CommittedSegment) (CatalogCoverageProof, error)

// ManifestResolver hydrates the exact signed manifest and its complete object
// metadata referenced by a catalog row. The database stores only fixed locator
// metadata; object bytes/protection facts remain in the approved object store.
type ManifestResolver func(context.Context, ObjectVersionV1) (SignedManifestV1, ObjectVersionV1, error)

type PostgresCatalog struct {
	pool            *pgxpool.Pool
	coverage        CatalogCoverageChecker
	proof           CatalogCoverageProofProvider
	resolve         ManifestResolver
	bucketID        string
	productionProof bool
}

func NewPostgresCatalog(pool *pgxpool.Pool, coverage CatalogCoverageChecker, resolve ManifestResolver) (*PostgresCatalog, error) {
	// Legacy/test constructor. Production callers must use NewPostgresCatalogWithProof
	// so the immutable RecoveryIndex facts are captured and rechecked around the
	// catalog transaction; a callback alone cannot establish that snapshot boundary.
	return NewPostgresCatalogWithBucket(pool, coverage, resolve, "archive")
}

func NewPostgresCatalogWithBucket(pool *pgxpool.Pool, coverage CatalogCoverageChecker, resolve ManifestResolver, bucketID string) (*PostgresCatalog, error) {
	if pool == nil {
		return nil, fmt.Errorf("%w: nil PostgreSQL pool", ErrCatalogConflict)
	}
	if !validBucketID(bucketID) {
		return nil, fmt.Errorf("%w: invalid archive bucket id", ErrArchiveValidation)
	}
	return &PostgresCatalog{pool: pool, coverage: coverage, resolve: resolve, bucketID: bucketID}, nil
}

func NewPostgresCatalogWithProof(pool *pgxpool.Pool, proof CatalogCoverageProofProvider, resolve ManifestResolver, bucketID string) (*PostgresCatalog, error) {
	// Fixture-only convenience. Production callers must use
	// NewPostgresCatalogWithProofAndChecker so the signed checkpoint/range
	// membership checker is also required.
	return newPostgresCatalogWithProof(pool, proof, nil, resolve, bucketID, false)
}

func NewPostgresCatalogWithProofAndChecker(pool *pgxpool.Pool, proof CatalogCoverageProofProvider, coverage CatalogCoverageChecker, resolve ManifestResolver, bucketID string) (*PostgresCatalog, error) {
	if coverage == nil {
		return nil, fmt.Errorf("%w: production coverage checker required", ErrCatalogConflict)
	}
	return newPostgresCatalogWithProof(pool, proof, coverage, resolve, bucketID, true)
}

func newPostgresCatalogWithProof(pool *pgxpool.Pool, proof CatalogCoverageProofProvider, coverage CatalogCoverageChecker, resolve ManifestResolver, bucketID string, production bool) (*PostgresCatalog, error) {
	if pool == nil {
		return nil, fmt.Errorf("%w: nil PostgreSQL pool", ErrCatalogConflict)
	}
	if proof == nil || !validBucketID(bucketID) {
		return nil, fmt.Errorf("%w: proof provider/bucket required", ErrCatalogConflict)
	}
	return &PostgresCatalog{pool: pool, proof: proof, coverage: coverage, resolve: resolve, bucketID: bucketID, productionProof: production}, nil
}

func archiveTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: !value.IsZero()}
}

func archiveTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func canonicalCountsJSON(value [2]CanonicalCountV1) ([]byte, error) {
	return json.Marshal(value)
}

func environmentCountsJSON(value []ProjectionRefV1) ([]byte, error) {
	counts := make(map[string]int64, len(value))
	for _, projection := range value {
		if projection.Environment == "" {
			return nil, fmt.Errorf("%w: empty projection environment", ErrArchiveValidation)
		}
		counts[projection.Environment] += projection.Object.RowCount
	}
	return json.Marshal(counts)
}

func committedSegmentParams(value CommittedSegment) (gen.InsertArchiveSegmentParams, error) {
	if err := ValidateCommittedSegment(value); err != nil {
		return gen.InsertArchiveSegmentParams{}, err
	}
	counts, err := canonicalCountsJSON(value.Manifest.Unsigned.CanonicalVersionCounts)
	if err != nil {
		return gen.InsertArchiveSegmentParams{}, err
	}
	environments, err := environmentCountsJSON(value.Manifest.Unsigned.Projections)
	if err != nil {
		return gen.InsertArchiveSegmentParams{}, err
	}
	projections, err := json.Marshal(value.Manifest.Unsigned.Projections)
	if err != nil {
		return gen.InsertArchiveSegmentParams{}, err
	}
	rootID, err := uuid.Parse(value.Manifest.Unsigned.ChainRoot.ID)
	if err != nil {
		return gen.InsertArchiveSegmentParams{}, fmt.Errorf("%w: chain root id", ErrArchiveValidation)
	}
	return gen.InsertArchiveSegmentParams{
		ID: value.ID, FormatVersion: int16(value.Manifest.Unsigned.FormatVersion),
		FromSequence: value.Manifest.Unsigned.FromSequence, ToSequence: value.Manifest.Unsigned.ToSequence,
		RowCount: value.Manifest.Unsigned.RowCount, FirstPrevHash: value.Manifest.Unsigned.FirstPrevHash,
		LastEventHash: value.Manifest.Unsigned.LastEventHash, CanonicalVersionCounts: counts,
		EnvironmentCounts: environments, PayloadObjectKey: value.Manifest.Unsigned.Payload.Key,
		PayloadVersionID: value.Manifest.Unsigned.Payload.VersionID, PayloadSha256: value.Manifest.Unsigned.Payload.SHA256,
		PayloadSizeBytes: value.Manifest.Unsigned.Payload.SizeBytes, Projections: projections,
		ManifestObjectKey: value.ManifestObject.Key, ManifestVersionID: value.ManifestObject.VersionID,
		ManifestSha256: value.ManifestObject.SHA256, ManifestSignature: value.Manifest.Signature,
		ManifestKeyID: value.Manifest.SignatureKeyID, ChainRootID: rootID,
		ChainRootHash: value.Manifest.Unsigned.ChainRoot.RootHash, CheckpointSha256: value.CheckpointSHA256,
		RecoveryGeneration: value.RecoveryGeneration, CommittedAt: archiveTimestamp(value.CommittedAt),
		VerifiedAt: archiveTimestamp(value.VerifiedAt),
	}, nil
}

func ValidateCatalogCoverageFacts(segment CommittedSegment, proof CatalogCoverageProof) error {
	if proof.Generation < 1 || !isLowerHex64(proof.CheckpointSHA256) ||
		segment.RecoveryGeneration != proof.Generation || segment.CheckpointSHA256 != proof.CheckpointSHA256 {
		return fmt.Errorf("%w: recovery generation/checkpoint proof mismatch", ErrCatalogConflict)
	}
	if proof.TerminalSequence < segment.Manifest.Unsigned.ToSequence ||
		!isLowerHex64(proof.TerminalRootHash) || proof.TerminalManifestSHA256 == "" ||
		proof.TerminalManifestKey == "" || proof.TerminalManifestVersionID == "" {
		return fmt.Errorf("%w: invalid recovery terminal proof", ErrCatalogConflict)
	}
	if segment.Manifest.Unsigned.ToSequence == proof.TerminalSequence {
		if segment.ManifestObject.SHA256 != proof.TerminalManifestSHA256 ||
			segment.ManifestObject.Key != proof.TerminalManifestKey ||
			segment.ManifestObject.VersionID != proof.TerminalManifestVersionID ||
			segment.Manifest.Unsigned.ChainRoot.RootHash != proof.TerminalRootHash {
			return fmt.Errorf("%w: terminal manifest/root proof mismatch", ErrCatalogConflict)
		}
	}
	return nil
}

func archiveRowMatchesParams(row gen.AuditArchiveSegment, want gen.InsertArchiveSegmentParams) bool {
	return row.ID == want.ID && row.FormatVersion == want.FormatVersion && row.FromSequence == want.FromSequence &&
		row.ToSequence == want.ToSequence && row.RowCount == want.RowCount && row.FirstPrevHash == want.FirstPrevHash &&
		row.LastEventHash == want.LastEventHash && jsonEquivalent(row.CanonicalVersionCounts, want.CanonicalVersionCounts) &&
		jsonEquivalent(row.EnvironmentCounts, want.EnvironmentCounts) && row.PayloadObjectKey == want.PayloadObjectKey &&
		row.PayloadVersionID == want.PayloadVersionID && row.PayloadSha256 == want.PayloadSha256 &&
		row.PayloadSizeBytes == want.PayloadSizeBytes && jsonEquivalent(row.Projections, want.Projections) &&
		row.ManifestObjectKey == want.ManifestObjectKey && row.ManifestVersionID == want.ManifestVersionID &&
		row.ManifestSha256 == want.ManifestSha256 && row.ManifestSignature == want.ManifestSignature &&
		row.ManifestKeyID == want.ManifestKeyID && row.ChainRootID == want.ChainRootID &&
		row.ChainRootHash == want.ChainRootHash && row.CheckpointSha256 == want.CheckpointSha256 &&
		row.RecoveryGeneration == want.RecoveryGeneration && pgTimestampEqual(row.CommittedAt, want.CommittedAt) &&
		pgTimestampEqual(row.VerifiedAt, want.VerifiedAt)
}

func pgTimestampEqual(left, right pgtype.Timestamptz) bool {
	if left.Valid != right.Valid || left.InfinityModifier != right.InfinityModifier {
		return false
	}
	if !left.Valid {
		return true
	}
	return left.Time.Equal(right.Time)
}

func jsonEquivalent(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return bytes.Equal(left, right)
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func objectVersionFromRow(row gen.AuditArchiveSegment, bucketID string) ObjectVersionV1 {
	return ObjectVersionV1{
		// This is a locator only. The resolver must return the complete exact
		// ObjectVersionV1 (size/checksum/ETag/protection/retain metadata) before
		// committedSegmentFromRow validates or exposes the segment.
		BucketID: bucketID, Key: row.ManifestObjectKey, VersionID: row.ManifestVersionID,
		SHA256: row.ManifestSha256,
	}
}

func (c *PostgresCatalog) CommitCoveredSegment(ctx context.Context, value CommittedSegment) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("%w: nil PostgreSQL catalog", ErrCatalogConflict)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if c.coverage == nil && c.proof == nil {
		return fmt.Errorf("%w: RecoveryIndex coverage checker is required", ErrCatalogConflict)
	}
	if c.productionProof && c.coverage == nil {
		return fmt.Errorf("%w: production catalog requires signed range coverage checker", ErrCatalogConflict)
	}
	if value.ManifestObject.BucketID != c.bucketID {
		return fmt.Errorf("%w: manifest bucket does not match configured archive bucket", ErrCatalogConflict)
	}
	var coverageProof *CatalogCoverageProof
	if c.proof != nil {
		proof, err := c.proof(ctx, value)
		if err != nil {
			return err
		}
		coverageProof = &proof
		if err := ValidateCatalogCoverageFacts(value, proof); err != nil {
			return err
		}
	}
	params, err := committedSegmentParams(value)
	if err != nil {
		return err
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin catalog commit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", archiveCatalogAdvisoryLock); err != nil {
		return fmt.Errorf("lock archive catalog: %w", err)
	}
	latest, err := gen.New(tx).GetLatestArchiveSegment(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		if value.Manifest.Unsigned.FromSequence != 1 {
			return fmt.Errorf("%w: first segment must start at sequence 1", ErrCatalogConflict)
		}
	} else if err != nil {
		return fmt.Errorf("read archive catalog tip: %w", err)
	} else {
		// Validate the persisted tip before using it to derive the next range. The
		// DB constraints are a second line; this application check also binds all
		// fixed columns to the hydrated manifest rather than trusting `to_sequence`.
		if err := validatePersistedCatalogRow(latest); err != nil {
			return fmt.Errorf("%w: persisted catalog tip failed validation", ErrCatalogConflict)
		}
		wantFrom := latest.ToSequence + 1
		if latest.ToSequence == int64(^uint64(0)>>1) {
			return fmt.Errorf("%w: catalog sequence exhausted", ErrCatalogConflict)
		}
		if value.Manifest.Unsigned.FromSequence != wantFrom {
			// Replaying the current terminal row is idempotent. A replay of an
			// older row is handled by the reconciliation pass, never by an
			// overwrite/update here.
			if latest.FromSequence == value.Manifest.Unsigned.FromSequence &&
				latest.ToSequence == value.Manifest.Unsigned.ToSequence && archiveRowMatchesParams(latest, params) {
				return tx.Commit(ctx)
			}
			return fmt.Errorf("%w: expected from_sequence=%d got=%d", ErrCatalogConflict, wantFrom, value.Manifest.Unsigned.FromSequence)
		}
	}
	// Evaluate the legacy checker only after taking the catalog lock. The proof
	// path above is preferred because it carries immutable signed facts captured
	// before the transaction and revalidated here.
	if c.coverage != nil {
		if err := c.coverage(ctx, value); err != nil {
			return err
		}
	}
	if coverageProof != nil {
		if err := ValidateCatalogCoverageFacts(value, *coverageProof); err != nil {
			return err
		}
	}
	if _, err := gen.New(tx).InsertArchiveSegment(ctx, params); err != nil {
		return fmt.Errorf("insert archive catalog segment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit archive catalog segment: %w", err)
	}
	return nil
}

func validatePersistedCatalogRow(row gen.AuditArchiveSegment) error {
	if row.ID == uuid.Nil || row.FormatVersion != 1 || row.FromSequence < 1 || row.ToSequence < row.FromSequence ||
		row.RowCount != row.ToSequence-row.FromSequence+1 || row.RowCount <= 0 || !isLowerHex64(row.FirstPrevHash) ||
		!isLowerHex64(row.LastEventHash) || !isLowerHex64(row.PayloadSha256) || !isLowerHex64(row.ManifestSha256) ||
		!isLowerHex64(row.CheckpointSha256) || row.PayloadSizeBytes < 0 || row.RecoveryGeneration < 1 ||
		row.ManifestObjectKey == "" || row.ManifestVersionID == "" || row.PayloadObjectKey == "" || row.PayloadVersionID == "" ||
		row.ManifestSignature == "" || row.ManifestKeyID == "" || row.ChainRootID == uuid.Nil || !row.CommittedAt.Valid || !row.VerifiedAt.Valid {
		return fmt.Errorf("%w: fixed catalog fields", ErrCatalogValidation)
	}
	if !objectKeyDigestMatches(row.PayloadObjectKey, row.PayloadSha256) || !objectKeyDigestMatches(row.ManifestObjectKey, row.ManifestSha256) {
		return fmt.Errorf("%w: catalog object locator digest", ErrCatalogValidation)
	}
	var counts []json.RawMessage
	var environments map[string]json.RawMessage
	var projections []json.RawMessage
	if json.Unmarshal(row.CanonicalVersionCounts, &counts) != nil || len(counts) != 2 ||
		json.Unmarshal(row.EnvironmentCounts, &environments) != nil || len(environments) == 0 ||
		json.Unmarshal(row.Projections, &projections) != nil || len(projections) == 0 {
		return fmt.Errorf("%w: catalog projections", ErrCatalogValidation)
	}
	return nil
}

func (c *PostgresCatalog) Latest(ctx context.Context) (CommittedSegment, error) {
	if c == nil || c.pool == nil || c.resolve == nil {
		return CommittedSegment{}, fmt.Errorf("%w: PostgreSQL catalog requires manifest resolver", ErrCatalogConflict)
	}
	row, err := gen.New(c.pool).GetLatestArchiveSegment(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommittedSegment{}, ErrCatalogEmpty
	}
	if err != nil {
		return CommittedSegment{}, fmt.Errorf("read archive catalog tip: %w", err)
	}
	manifestLocator := objectVersionFromRow(row, c.bucketID)
	manifest, manifestObject, err := c.resolve(ctx, manifestLocator)
	if err != nil {
		return CommittedSegment{}, err
	}
	return committedSegmentFromRow(row, manifest, manifestObject, c.bucketID)
}

func (c *PostgresCatalog) ListBefore(ctx context.Context, before int64, limit int32) ([]CommittedSegment, error) {
	if c == nil || c.pool == nil || c.resolve == nil {
		return nil, fmt.Errorf("%w: PostgreSQL catalog requires manifest resolver", ErrCatalogConflict)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("%w: positive limit required", ErrArchiveValidation)
	}
	if before <= 0 {
		before = int64(^uint64(0) >> 1)
	}
	rows, err := gen.New(c.pool).ListArchiveSegmentsBefore(ctx, gen.ListArchiveSegmentsBeforeParams{
		ToSequence: before, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list archive catalog: %w", err)
	}
	result := make([]CommittedSegment, 0, len(rows))
	for _, row := range rows {
		locator := objectVersionFromRow(row, c.bucketID)
		manifest, object, err := c.resolve(ctx, locator)
		if err != nil {
			return nil, err
		}
		segment, err := committedSegmentFromRow(row, manifest, object, c.bucketID)
		if err != nil {
			return nil, err
		}
		result = append(result, segment)
	}
	return result, nil
}

func committedSegmentFromRow(row gen.AuditArchiveSegment, manifest SignedManifestV1, object ObjectVersionV1, expectedBucket string) (CommittedSegment, error) {
	if err := validatePersistedCatalogRow(row); err != nil {
		return CommittedSegment{}, err
	}
	if object.BucketID == "" || object.BucketID != expectedBucket || object.Key != row.ManifestObjectKey || object.VersionID != row.ManifestVersionID ||
		object.SHA256 != row.ManifestSha256 {
		return CommittedSegment{}, fmt.Errorf("%w: resolver locator differs from catalog row", ErrCatalogValidation)
	}
	segment := CommittedSegment{ID: row.ID, Manifest: manifest, ManifestObject: object,
		CheckpointSHA256: row.CheckpointSha256, RecoveryGeneration: row.RecoveryGeneration,
		CommittedAt: archiveTime(row.CommittedAt), VerifiedAt: archiveTime(row.VerifiedAt)}
	if err := ValidateCommittedSegment(segment); err != nil {
		return CommittedSegment{}, err
	}
	params, err := committedSegmentParams(segment)
	if err != nil || !archiveRowMatchesParams(row, params) {
		return CommittedSegment{}, fmt.Errorf("%w: catalog row differs from hydrated manifest", ErrCatalogValidation)
	}
	return segment, nil
}

var ErrCatalogValidation = errors.New("archive_catalog_validation_failed")
