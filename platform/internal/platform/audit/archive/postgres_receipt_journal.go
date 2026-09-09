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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit/gen"
)

type PostgresReceiptJournal struct {
	pool *pgxpool.Pool
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func NewPostgresReceiptJournal(pool *pgxpool.Pool) (*PostgresReceiptJournal, error) {
	if pool == nil {
		return nil, fmt.Errorf("%w: nil PostgreSQL pool", ErrJournalConflict)
	}
	return &PostgresReceiptJournal{pool: pool}, nil
}

func receiptTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: !value.IsZero()}
}

func (j *PostgresReceiptJournal) loadValidatedPutRows(ctx context.Context, operationID uuid.UUID, intent OperationIntentV1) ([]ObjectVersionV1, error) {
	rows, err := gen.New(j.pool).ListArchivePutReceipts(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if len(rows) > len(intent.Objects) {
		return nil, fmt.Errorf("%w: too many put receipts", ErrJournalConflict)
	}
	result := make([]ObjectVersionV1, len(intent.Objects))
	seen := make([]bool, len(intent.Objects))
	for _, row := range rows {
		if row.Ordinal < 0 || int(row.Ordinal) >= len(intent.Objects) || seen[row.Ordinal] {
			return nil, fmt.Errorf("%w: put receipt ordinal set is not contiguous", ErrJournalConflict)
		}
		var object ObjectVersionV1
		if err := decodeCanonicalJSON(row.ObjectVersionBytes, &object); err != nil {
			return nil, fmt.Errorf("%w: object receipt decode", ErrJournalConflict)
		}
		if err := ValidateObjectVersion(object); err != nil || row.ObjectVersionSha256 != sha256Hex(row.ObjectVersionBytes) {
			return nil, fmt.Errorf("%w: object receipt digest", ErrJournalConflict)
		}
		if intent.Objects[row.Ordinal].OperationID != operationID || intent.Objects[row.Ordinal].Ordinal != row.Ordinal ||
			!objectMatchesIntent(object, intent.Objects[row.Ordinal]) {
			return nil, fmt.Errorf("%w: object receipt does not match intent", ErrJournalConflict)
		}
		result[row.Ordinal] = object
		seen[row.Ordinal] = true
	}
	for ordinal, present := range seen {
		if !present {
			return nil, fmt.Errorf("%w: missing put receipt ordinal=%d", ErrJournalIncomplete, ordinal)
		}
	}
	return result, nil
}

func receiptTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func (j *PostgresReceiptJournal) BeginIntent(ctx context.Context, value OperationIntentV1) error {
	if j == nil || j.pool == nil {
		return fmt.Errorf("%w: nil PostgreSQL journal", ErrJournalConflict)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	canonical, err := CanonicalOperationIntentBytes(value)
	if err != nil {
		return err
	}
	_, err = j.pool.Exec(ctx, `
		INSERT INTO audit.archive_operation_intent
		  (operation_id, approval_envelope_sha256, deterministic_bytes_digest, canonical_intent_bytes, created_at)
		VALUES ($1,$2,$3,$4,$5)`, value.OperationID, value.ApprovalEnvelopeSHA256,
		value.DeterministicBytesDigest, canonical, receiptTimestamp(time.Now().UTC()))
	if err != nil && !isUniqueViolation(err) {
		return fmt.Errorf("insert archive operation intent: %w", err)
	}
	row, err := gen.New(j.pool).GetArchiveOperationIntent(ctx, value.OperationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: intent disappeared", ErrJournalConflict)
	}
	if err != nil {
		return fmt.Errorf("read archive operation intent: %w", err)
	}
	if string(row.CanonicalIntentBytes) != string(canonical) || row.ApprovalEnvelopeSha256 != value.ApprovalEnvelopeSHA256 ||
		row.DeterministicBytesDigest != value.DeterministicBytesDigest {
		return fmt.Errorf("%w: operation intent bytes changed", ErrJournalConflict)
	}
	return nil
}

func (j *PostgresReceiptJournal) AppendPutResult(ctx context.Context, operationID uuid.UUID, ordinal int32, value ObjectVersionV1) error {
	if j == nil || j.pool == nil {
		return fmt.Errorf("%w: nil PostgreSQL journal", ErrJournalConflict)
	}
	if operationID == uuid.Nil || ordinal < 0 {
		return fmt.Errorf("%w: operation/ordinal", ErrArchiveValidation)
	}
	if err := ValidateObjectVersion(value); err != nil {
		return err
	}
	intentRow, err := gen.New(j.pool).GetArchiveOperationIntent(ctx, operationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: intent is not durable", ErrJournalConflict)
		}
		return err
	}
	var intent OperationIntentV1
	if err := decodeCanonicalJSON(intentRow.CanonicalIntentBytes, &intent); err != nil {
		return fmt.Errorf("%w: intent decode", ErrJournalConflict)
	}
	if intent.OperationID != operationID {
		return fmt.Errorf("%w: intent operation id mismatch", ErrJournalConflict)
	}
	if err := ValidateOperationIntent(intent); err != nil || int(ordinal) >= len(intent.Objects) ||
		intent.Objects[ordinal].OperationID != operationID || !objectMatchesIntent(value, intent.Objects[ordinal]) {
		return fmt.Errorf("%w: object receipt does not match intent", ErrJournalConflict)
	}
	objectBytes, err := CanonicalObjectVersionBytes(value)
	if err != nil {
		return err
	}
	_, err = j.pool.Exec(ctx, `
		INSERT INTO audit.archive_put_receipt
		  (operation_id, ordinal, object_version_bytes, object_version_sha256, recorded_at)
		VALUES ($1,$2,$3,$4,$5)`, operationID, ordinal, objectBytes, sha256Hex(objectBytes), receiptTimestamp(time.Now().UTC()))
	if err != nil && !isUniqueViolation(err) {
		return fmt.Errorf("insert archive put receipt: %w", err)
	}
	rows, err := gen.New(j.pool).ListArchivePutReceipts(ctx, operationID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Ordinal != ordinal {
			continue
		}
		if string(row.ObjectVersionBytes) != string(objectBytes) || row.ObjectVersionSha256 != sha256Hex(objectBytes) {
			return fmt.Errorf("%w: object receipt bytes changed", ErrJournalConflict)
		}
		return nil
	}
	return fmt.Errorf("%w: put receipt disappeared", ErrJournalConflict)
}

func (j *PostgresReceiptJournal) AppendTerminalResult(ctx context.Context, operationID uuid.UUID, resultBytes []byte, ref *ArtifactRefV1, digest string) error {
	if j == nil || j.pool == nil {
		return fmt.Errorf("%w: nil PostgreSQL journal", ErrJournalConflict)
	}
	if operationID == uuid.Nil || len(resultBytes) == 0 || !isLowerHex64(digest) || sha256Hex(resultBytes) != digest {
		return fmt.Errorf("%w: terminal receipt fields", ErrArchiveValidation)
	}
	if ref != nil {
		if err := ValidateArtifactRef(*ref); err != nil {
			return err
		}
	}
	if _, err := gen.New(j.pool).GetArchiveOperationIntent(ctx, operationID); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: intent is not durable", ErrJournalConflict)
	} else if err != nil {
		return err
	}
	// Decode the intent to determine the exact expected ordinal count.
	intentRow, err := gen.New(j.pool).GetArchiveOperationIntent(ctx, operationID)
	if err != nil {
		return err
	}
	var intent OperationIntentV1
	if err := decodeCanonicalJSON(intentRow.CanonicalIntentBytes, &intent); err != nil {
		return fmt.Errorf("%w: intent decode", ErrJournalConflict)
	}
	if intent.OperationID != operationID {
		return fmt.Errorf("%w: intent operation id mismatch", ErrJournalConflict)
	}
	if _, err := j.loadValidatedPutRows(ctx, operationID, intent); err != nil {
		return fmt.Errorf("%w: terminal receipt before all object receipts: %v", ErrJournalConflict, err)
	}
	var refBytes []byte
	if ref != nil {
		refBytes, err = json.Marshal(ref)
		if err != nil {
			return err
		}
	}
	_, err = j.pool.Exec(ctx, `
		INSERT INTO audit.archive_terminal_receipt
		  (operation_id, signed_result_bytes, terminal_result_digest, optional_artifact_ref_bytes, recorded_at)
		VALUES ($1,$2,$3,$4,$5)`, operationID, resultBytes, digest, refBytes, receiptTimestamp(time.Now().UTC()))
	if err != nil && !isUniqueViolation(err) {
		return fmt.Errorf("insert archive terminal receipt: %w", err)
	}
	row, err := gen.New(j.pool).GetArchiveTerminalReceipt(ctx, operationID)
	if err != nil {
		return err
	}
	if string(row.SignedResultBytes) != string(resultBytes) || row.TerminalResultDigest != digest {
		return fmt.Errorf("%w: terminal receipt bytes changed", ErrJournalConflict)
	}
	if !nullableBytesEqual(row.OptionalArtifactRefBytes, refBytes) {
		return fmt.Errorf("%w: terminal artifact ref changed", ErrJournalConflict)
	}
	return nil
}

func (j *PostgresReceiptJournal) LoadOperation(ctx context.Context, operationID uuid.UUID) (OperationReceiptV1, error) {
	if j == nil || j.pool == nil || operationID == uuid.Nil {
		return OperationReceiptV1{}, fmt.Errorf("%w: operation", ErrJournalConflict)
	}
	intentRow, err := gen.New(j.pool).GetArchiveOperationIntent(ctx, operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationReceiptV1{}, ErrObjectNotFound
	}
	if err != nil {
		return OperationReceiptV1{}, err
	}
	var intent OperationIntentV1
	if err := decodeCanonicalJSON(intentRow.CanonicalIntentBytes, &intent); err != nil {
		return OperationReceiptV1{}, fmt.Errorf("%w: intent decode", ErrJournalConflict)
	}
	if intent.OperationID != operationID {
		return OperationReceiptV1{}, fmt.Errorf("%w: intent operation id mismatch", ErrJournalConflict)
	}
	if err := ValidateOperationIntent(intent); err != nil {
		return OperationReceiptV1{}, err
	}
	result := OperationReceiptV1{Intent: intent}
	putResults, err := j.loadValidatedPutRows(ctx, operationID, intent)
	if err != nil {
		return OperationReceiptV1{}, err
	}
	result.PutResults = append(result.PutResults, putResults...)
	terminal, err := gen.New(j.pool).GetArchiveTerminalReceipt(ctx, operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return OperationReceiptV1{}, err
	}
	if terminal.TerminalResultDigest != sha256Hex(terminal.SignedResultBytes) {
		return OperationReceiptV1{}, fmt.Errorf("%w: terminal digest", ErrJournalConflict)
	}
	result.TerminalResultBytes = append([]byte(nil), terminal.SignedResultBytes...)
	result.TerminalResultDigest = terminal.TerminalResultDigest
	if terminal.OptionalArtifactRefBytes != nil && len(terminal.OptionalArtifactRefBytes) == 0 {
		return OperationReceiptV1{}, fmt.Errorf("%w: empty optional artifact ref bytes", ErrJournalConflict)
	}
	if terminal.OptionalArtifactRefBytes != nil {
		var ref ArtifactRefV1
		if err := decodeCanonicalJSON(terminal.OptionalArtifactRefBytes, &ref); err != nil {
			return OperationReceiptV1{}, fmt.Errorf("%w: terminal artifact ref", ErrJournalConflict)
		}
		if err := ValidateArtifactRef(ref); err != nil {
			return OperationReceiptV1{}, err
		}
		result.TerminalResultRef = &ref
	}
	return cloneReceipt(result), nil
}

func nullableBytesEqual(left, right []byte) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	return bytes.Equal(left, right)
}
