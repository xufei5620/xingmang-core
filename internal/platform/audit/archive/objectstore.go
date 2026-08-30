package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Capability-specific interfaces are intentionally small.  In particular, there is
// no Delete, List, latest-object read, or general Overwrite operation in AUD2.
type ObjectWriteIntentV1 struct {
	OperationID              uuid.UUID `json:"operation_id"`
	Ordinal                  int32     `json:"ordinal"`
	BucketID                 string    `json:"bucket_id"`
	Key                      string    `json:"key"`
	SHA256                   string    `json:"sha256"`
	SizeBytes                int64     `json:"size_bytes"`
	ContentType              string    `json:"content_type"`
	EncryptionMode           string    `json:"encryption_mode"`
	KMSKeyID                 string    `json:"kms_key_id"`
	ObjectLockMode           string    `json:"object_lock_mode"`
	RetainUntil              WireTime  `json:"retain_until"`
	ProviderIdempotencyToken string    `json:"provider_idempotency_token"`
}

type FixedLocator struct {
	ApprovedConfigRef string `json:"approved_config_ref"`
}

type IndexVersion struct {
	Generation      int64  `json:"generation"`
	SHA256          string `json:"sha256"`
	ProviderVersion string `json:"provider_version"`
}

type ExpectedIndex struct {
	Generation      int64  `json:"generation"`
	SHA256          string `json:"sha256"`
	ProviderVersion string `json:"provider_version"`
}

type OperationIntentV1 struct {
	OperationID              uuid.UUID             `json:"operation_id"`
	ApprovalEnvelopeSHA256   string                `json:"approval_envelope_sha256"`
	DeterministicBytesDigest string                `json:"deterministic_bytes_digest"`
	Objects                  []ObjectWriteIntentV1 `json:"objects"`
}

type OperationReceiptV1 struct {
	Intent               OperationIntentV1
	PutResults           []ObjectVersionV1
	TerminalResultBytes  []byte
	TerminalResultRef    *ArtifactRefV1
	TerminalResultDigest string
}

type ObjectWriter interface {
	PutIfAbsent(context.Context, ObjectWriteIntentV1, io.Reader) (ObjectVersionV1, error)
	RecoverPutResult(context.Context, ObjectWriteIntentV1) (ObjectVersionV1, error)
}

type ExactObjectReader interface {
	HeadVersion(context.Context, ObjectVersionV1) (ObjectVersionV1, error)
	GetVersion(context.Context, ObjectVersionV1) (io.ReadCloser, ObjectVersionV1, error)
}

type RecoveryIndexReader interface {
	LoadCurrent(context.Context, FixedLocator) (SignedRecoveryIndexV1, IndexVersion, error)
}

type RecoveryIndexCASWriter interface {
	CompareAndSwap(context.Context, FixedLocator, ExpectedIndex, SignedRecoveryIndexV1) (IndexVersion, error)
}

type ReceiptJournalWriter interface {
	BeginIntent(context.Context, OperationIntentV1) error
	AppendPutResult(context.Context, uuid.UUID, int32, ObjectVersionV1) error
	AppendTerminalResult(context.Context, uuid.UUID, []byte, *ArtifactRefV1, string) error
}

type ReceiptJournalReader interface {
	LoadOperation(context.Context, uuid.UUID) (OperationReceiptV1, error)
}

type CommittedSegment struct {
	ID                 uuid.UUID
	Manifest           SignedManifestV1
	ManifestObject     ObjectVersionV1
	CheckpointSHA256   string
	RecoveryGeneration int64
	CommittedAt        time.Time
	VerifiedAt         time.Time
}

type CatalogReader interface {
	Latest(context.Context) (CommittedSegment, error)
	ListBefore(context.Context, int64, int32) ([]CommittedSegment, error)
}

type CatalogWriter interface {
	CommitCoveredSegment(context.Context, CommittedSegment) error
}

// MaxObjectWriteBytes bounds buffered fixture writes and avoids int64 overflow
// when adding the one-byte over-read guard. Production adapters enforce the
// approved stream limit at their own boundary.
const MaxObjectWriteBytes int64 = 1 << 30

var (
	ErrArchiveValidation = errors.New("archive_validation_failed")
	ErrObjectNotFound    = errors.New("archive_object_not_found")
	ErrObjectConflict    = errors.New("archive_object_conflict")
	ErrJournalConflict   = errors.New("archive_journal_conflict")
	ErrCatalogConflict   = errors.New("archive_catalog_conflict")
	ErrRecoveryConflict  = errors.New("archive_recovery_index_conflict")
)

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func validArchiveText(value string, max int) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || len(value) > max {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validBucketID(value string) bool {
	if !validArchiveText(value, 128) || strings.ContainsAny(value, `/\\`) || strings.Contains(value, "..") {
		return false
	}
	return true
}

func objectKeyDigestMatches(key, digest string) bool {
	if strings.HasSuffix(key, ".ndjson") {
		return len(key) >= 71 && key[len(key)-71:] == digest+".ndjson"
	}
	if strings.HasSuffix(key, ".json") {
		return len(key) >= 69 && key[len(key)-69:] == digest+".json"
	}
	return false
}

// ValidateObjectWriteIntent is the shared fail-closed guard used by filesystem and
// provider adapters before any external write is attempted.
func ValidateObjectWriteIntent(value ObjectWriteIntentV1) error {
	if value.OperationID == uuid.Nil || value.Ordinal < 0 || !validBucketID(value.BucketID) ||
		!validObjectKey(value.Key) || !isLowerHex64(value.SHA256) || value.SizeBytes < 0 ||
		value.SizeBytes > MaxObjectWriteBytes ||
		!validArchiveText(value.ContentType, 256) || !validArchiveText(value.ProviderIdempotencyToken, 512) ||
		strings.EqualFold(value.ProviderIdempotencyToken, "latest") ||
		!objectKeyDigestMatches(value.Key, value.SHA256) {
		return fmt.Errorf("%w: object write intent fields", ErrArchiveValidation)
	}
	if value.BucketID == "local-fixture" {
		if value.EncryptionMode != "LOCAL-ONLY" || value.KMSKeyID != "" || value.ObjectLockMode != "NONE" {
			return fmt.Errorf("%w: local fixture protection", ErrArchiveValidation)
		}
	} else {
		// The approved MinIO deployment uses SSE-S3; retain SSE-KMS for previously
		// frozen non-fixture wire fixtures. Both modes must carry an opaque key id.
		if (value.EncryptionMode != "SSE-S3" && value.EncryptionMode != "SSE-KMS") ||
			!validArchiveText(value.KMSKeyID, 512) || !validArchiveText(value.ObjectLockMode, 64) ||
			value.ObjectLockMode == "NONE" {
			return fmt.Errorf("%w: provider protection", ErrArchiveValidation)
		}
	}
	if _, err := ParseWireTime(value.RetainUntil); err != nil {
		return fmt.Errorf("%w: retain_until: %v", ErrArchiveValidation, err)
	}
	return nil
}

// ValidateObjectVersion exposes the frozen wire validator to adapters without
// widening the object-store capability surface.
func ValidateObjectVersion(value ObjectVersionV1) error {
	if value.SizeBytes < 0 || value.SizeBytes > MaxObjectWriteBytes {
		return fmt.Errorf("%w: object size", ErrArchiveValidation)
	}
	if !objectKeyDigestMatches(value.Key, value.SHA256) {
		return fmt.Errorf("%w: object locator digest", ErrArchiveValidation)
	}
	if value.BucketID != "local-fixture" && value.EncryptionMode == "SSE-S3" {
		// Approved MinIO uses SSE-S3 backed by its configured KMS secret. The
		// frozen AUD1 wire validator predates that exception, so validate this
		// adapter path locally without changing the published wire contract.
		copy := value
		copy.EncryptionMode = "SSE-KMS"
		if err := validateObjectVersion(copy); err != nil {
			return err
		}
		return nil
	}
	if err := validateObjectVersion(value); err != nil {
		return err
	}
	return nil
}

func CanonicalObjectVersionBytes(value ObjectVersionV1) ([]byte, error) {
	if err := ValidateObjectVersion(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func ObjectVersionDigest(value ObjectVersionV1) (string, error) {
	bytes, err := CanonicalObjectVersionBytes(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(bytes), nil
}

func ValidateOperationIntent(value OperationIntentV1) error {
	if value.OperationID == uuid.Nil || !isLowerHex64(value.ApprovalEnvelopeSHA256) ||
		!isLowerHex64(value.DeterministicBytesDigest) || len(value.Objects) == 0 {
		return fmt.Errorf("%w: operation intent envelope", ErrArchiveValidation)
	}
	seenKeys := make(map[string]struct{}, len(value.Objects))
	for index, object := range value.Objects {
		if object.OperationID != value.OperationID || int(object.Ordinal) != index {
			return fmt.Errorf("%w: object ordinal/operation", ErrArchiveValidation)
		}
		if err := ValidateObjectWriteIntent(object); err != nil {
			return err
		}
		if _, exists := seenKeys[object.BucketID+"\x00"+object.Key]; exists {
			return fmt.Errorf("%w: duplicate object key", ErrArchiveValidation)
		}
		seenKeys[object.BucketID+"\x00"+object.Key] = struct{}{}
	}
	return nil
}

func CanonicalOperationIntentBytes(value OperationIntentV1) ([]byte, error) {
	if err := ValidateOperationIntent(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func OperationIntentDigest(value OperationIntentV1) (string, error) {
	bytes, err := CanonicalOperationIntentBytes(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(bytes), nil
}

func ValidateFixedLocator(value FixedLocator) error {
	if !validArchiveText(value.ApprovedConfigRef, 512) || strings.Contains(value.ApprovedConfigRef, "..") {
		return fmt.Errorf("%w: fixed locator", ErrArchiveValidation)
	}
	return nil
}

func cloneObjectVersion(value ObjectVersionV1) ObjectVersionV1 { return value }

func cloneOperationIntent(value OperationIntentV1) OperationIntentV1 {
	value.Objects = append([]ObjectWriteIntentV1(nil), value.Objects...)
	return value
}

func cloneReceipt(value OperationReceiptV1) OperationReceiptV1 {
	value.Intent = cloneOperationIntent(value.Intent)
	value.PutResults = append([]ObjectVersionV1(nil), value.PutResults...)
	value.TerminalResultBytes = append([]byte(nil), value.TerminalResultBytes...)
	if value.TerminalResultRef != nil {
		ref := *value.TerminalResultRef
		value.TerminalResultRef = &ref
	}
	return value
}
