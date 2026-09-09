package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

const (
	ManifestKind      = "xingmang-audit-archive-manifest"
	CheckpointKind    = "xingmang-audit-archive-checkpoint"
	RecoveryIndexKind = "xingmang-audit-recovery-index"

	ManifestGenesisSHA256   = "db88be35e323689da04f28e9e651d7b8ca19c6b46f43b6d3da9f485c15e07386"
	CheckpointGenesisSHA256 = "8a3bb7ae096a84a371c3c3ad3ac7c7b65d11d49c0da8df617b288c5475a3a171"
	RecoveryGenesisSHA256   = "710bcb439199068a84d3a0d33d9286dd1a1903112308d127a1170f85aa434bf3"
)

var manifestObjectKeyPattern = regexp.MustCompile(
	`^audit/v1/manifest/seq-[0-9]{19}-[0-9]{19}-([0-9a-f]{64})\.json$`,
)
var checkpointObjectKeyPattern = regexp.MustCompile(
	`^audit/v1/checkpoint/seq-[0-9]{19}-([0-9a-f]{64})\.json$`,
)

type ObjectVersionV1 struct {
	BucketID         string   `json:"bucket_id"`
	Key              string   `json:"key"`
	VersionID        string   `json:"version_id"`
	SHA256           string   `json:"sha256"`
	SizeBytes        int64    `json:"size_bytes"`
	ContentType      string   `json:"content_type"`
	ProviderChecksum string   `json:"provider_checksum"`
	ETag             string   `json:"etag"`
	EncryptionMode   string   `json:"encryption_mode"`
	KMSKeyID         string   `json:"kms_key_id"`
	ObjectLockMode   string   `json:"object_lock_mode"`
	RetainUntil      WireTime `json:"retain_until"`
	RowCount         int64    `json:"row_count"`
}

type CanonicalCountV1 struct {
	Version  int16 `json:"version"`
	RowCount int64 `json:"row_count"`
}

type PreviousManifestRefV1 struct {
	Kind      string `json:"kind"`
	BucketID  string `json:"bucket_id"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
}

type ArtifactRefV1 struct {
	BucketID  string `json:"bucket_id"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
}

type OptionalArtifactRefV1 struct {
	Kind      string `json:"kind"`
	BucketID  string `json:"bucket_id"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
}

type PreviousCheckpointRefV1 struct {
	Kind      string `json:"kind"`
	BucketID  string `json:"bucket_id"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
}

type ChainRootRefV1 struct {
	ID           string   `json:"id"`
	ComputedAt   WireTime `json:"computed_at"`
	FromSequence int64    `json:"from_sequence"`
	ToSequence   int64    `json:"to_sequence"`
	RootHash     string   `json:"root_hash"`
	Signature    string   `json:"signature"`
	KeyID        string   `json:"key_id"`
}

type ProjectionRefV1 struct {
	Environment string          `json:"environment"`
	Object      ObjectVersionV1 `json:"object"`
}

type ManifestV1 struct {
	Kind                     string                `json:"kind"`
	FormatVersion            int                   `json:"format_version"`
	ExporterVersion          string                `json:"exporter_version"`
	ExporterCommit           string                `json:"exporter_commit"`
	FromSequence             int64                 `json:"from_sequence"`
	ToSequence               int64                 `json:"to_sequence"`
	RowCount                 int64                 `json:"row_count"`
	FirstPrevHash            string                `json:"first_prev_hash"`
	FirstEventHash           string                `json:"first_event_hash"`
	LastEventHash            string                `json:"last_event_hash"`
	PreviousManifest         PreviousManifestRefV1 `json:"previous_manifest"`
	CanonicalVersionCounts   [2]CanonicalCountV1   `json:"canonical_version_counts"`
	OversizedRecordCount     int64                 `json:"oversized_record_count"`
	Payload                  ObjectVersionV1       `json:"payload"`
	Projections              []ProjectionRefV1     `json:"projections"`
	ChainRoot                ChainRootRefV1        `json:"chain_root"`
	CreatedAt                WireTime              `json:"created_at"`
	SourceTipObservedAt      WireTime              `json:"source_tip_observed_at"`
	ActionRunSnapshotAt      WireTime              `json:"action_run_snapshot_at"`
	EligibleActionRunCount   int64                 `json:"eligible_action_run_count"`
	MatchedActionRunCount    int64                 `json:"matched_action_run_count"`
	MissingAuditEventCount   int64                 `json:"missing_audit_event_count"`
	DuplicateAuditEventCount int64                 `json:"duplicate_audit_event_count"`
}

type SignedManifestV1 struct {
	Unsigned           ManifestV1 `json:"unsigned"`
	UnsignedSHA256     string     `json:"unsigned_sha256"`
	SignatureAlgorithm string     `json:"signature_algorithm"`
	SignatureKeyID     string     `json:"signature_key_id"`
	Signature          string     `json:"signature"`
}

type CheckpointV1 struct {
	Kind                     string                  `json:"kind"`
	FormatVersion            int                     `json:"format_version"`
	Generation               int64                   `json:"generation"`
	PreviousCheckpoint       PreviousCheckpointRefV1 `json:"previous_checkpoint"`
	FirstManifest            ArtifactRefV1           `json:"first_manifest"`
	TerminalManifest         ArtifactRefV1           `json:"terminal_manifest"`
	FromSequence             int64                   `json:"from_sequence"`
	ToSequence               int64                   `json:"to_sequence"`
	ManifestCount            int64                   `json:"manifest_count"`
	CatalogRowsDigest        string                  `json:"catalog_rows_digest"`
	ChainRoot                ChainRootRefV1          `json:"chain_root"`
	CanonicalVersionCounts   [2]CanonicalCountV1     `json:"canonical_version_counts"`
	ChainIntegrity           string                  `json:"chain_integrity"`
	CaptureCompleteness      string                  `json:"capture_completeness"`
	EligibleActionRunCount   int64                   `json:"eligible_action_run_count"`
	MatchedActionRunCount    int64                   `json:"matched_action_run_count"`
	MissingAuditEventCount   int64                   `json:"missing_audit_event_count"`
	DuplicateAuditEventCount int64                   `json:"duplicate_audit_event_count"`
	SourceTipSequence        int64                   `json:"source_tip_sequence"`
	SourceTipObservedAt      WireTime                `json:"source_tip_observed_at"`
	ApprovalEnvelopeSHA256   string                  `json:"approval_envelope_sha256"`
	OperationIntentDigest    string                  `json:"operation_intent_digest"`
	CreatedAt                WireTime                `json:"created_at"`
}

type SignedCheckpointV1 struct {
	Unsigned           CheckpointV1 `json:"unsigned"`
	UnsignedSHA256     string       `json:"unsigned_sha256"`
	SignatureAlgorithm string       `json:"signature_algorithm"`
	SignatureKeyID     string       `json:"signature_key_id"`
	Signature          string       `json:"signature"`
}

type RecoveryIndexV1 struct {
	Kind                string        `json:"kind"`
	FormatVersion       int           `json:"format_version"`
	Generation          int64         `json:"generation"`
	PreviousGeneration  int64         `json:"previous_generation"`
	PreviousIndexSHA256 string        `json:"previous_index_sha256"`
	Checkpoint          ArtifactRefV1 `json:"checkpoint"`
	TerminalManifest    ArtifactRefV1 `json:"terminal_manifest"`
	TerminalSequence    int64         `json:"terminal_sequence"`
	TerminalRootHash    string        `json:"terminal_root_hash"`
	UpdatedAt           WireTime      `json:"updated_at"`
}

type SignedRecoveryIndexV1 struct {
	Unsigned           RecoveryIndexV1 `json:"unsigned"`
	UnsignedSHA256     string          `json:"unsigned_sha256"`
	SignatureAlgorithm string          `json:"signature_algorithm"`
	SignatureKeyID     string          `json:"signature_key_id"`
	Signature          string          `json:"signature"`
}

func encodeWire(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func strictDecodeWire[T any](raw []byte, target *T, validate func(T) error) error {
	if len(raw) < 2 || raw[len(raw)-1] != '\n' || raw[len(raw)-2] == '\n' {
		return &FormatError{Code: "archive_format_invalid"}
	}
	content := raw[:len(raw)-1]
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	if err := validate(*target); err != nil {
		return err
	}
	canonical, err := encodeWire(*target)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, raw) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	return nil
}

func EncodeChainRootRefV1(value ChainRootRefV1) ([]byte, error) {
	if err := validateChainRootRef(value); err != nil {
		return nil, err
	}
	return encodeWire(value)
}

func DecodeChainRootRefV1(raw []byte) (ChainRootRefV1, error) {
	var value ChainRootRefV1
	err := strictDecodeWire(raw, &value, validateChainRootRef)
	return value, err
}

func EncodeSignedManifestV1(value SignedManifestV1) ([]byte, error) {
	if err := validateSignedManifest(value); err != nil {
		return nil, err
	}
	return encodeWire(value)
}

func DecodeSignedManifestV1(raw []byte) (SignedManifestV1, error) {
	var value SignedManifestV1
	err := strictDecodeWire(raw, &value, validateSignedManifest)
	return value, err
}

func EncodeSignedCheckpointV1(value SignedCheckpointV1) ([]byte, error) {
	if err := validateSignedCheckpoint(value); err != nil {
		return nil, err
	}
	return encodeWire(value)
}

func DecodeSignedCheckpointV1(raw []byte) (SignedCheckpointV1, error) {
	var value SignedCheckpointV1
	err := strictDecodeWire(raw, &value, validateSignedCheckpoint)
	return value, err
}

func EncodeSignedRecoveryIndexV1(value SignedRecoveryIndexV1) ([]byte, error) {
	if err := validateSignedRecoveryIndex(value); err != nil {
		return nil, err
	}
	return encodeWire(value)
}

func DecodeSignedRecoveryIndexV1(raw []byte) (SignedRecoveryIndexV1, error) {
	var value SignedRecoveryIndexV1
	err := strictDecodeWire(raw, &value, validateSignedRecoveryIndex)
	return value, err
}

func unsignedHash(value any) (string, error) {
	encoded, err := encodeWire(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validateSignedEnvelope(unsigned any, digest, algorithm, keyID, signature string) error {
	if algorithm != "Ed25519" || strings.TrimSpace(keyID) == "" || !lowerHex64.MatchString(digest) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	decoded, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(decoded) != 64 {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	want, err := unsignedHash(unsigned)
	if err != nil || want != digest {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	return nil
}

func validateSignedManifest(value SignedManifestV1) error {
	if err := ValidateManifestStructure(value.Unsigned); err != nil {
		return err
	}
	return validateSignedEnvelope(value.Unsigned, value.UnsignedSHA256, value.SignatureAlgorithm,
		value.SignatureKeyID, value.Signature)
}

func validateSignedCheckpoint(value SignedCheckpointV1) error {
	if err := validateCheckpoint(value.Unsigned); err != nil {
		return err
	}
	return validateSignedEnvelope(value.Unsigned, value.UnsignedSHA256, value.SignatureAlgorithm,
		value.SignatureKeyID, value.Signature)
}

func validateSignedRecoveryIndex(value SignedRecoveryIndexV1) error {
	if err := validateRecoveryIndex(value.Unsigned); err != nil {
		return err
	}
	return validateSignedEnvelope(value.Unsigned, value.UnsignedSHA256, value.SignatureAlgorithm,
		value.SignatureKeyID, value.Signature)
}

func validateChainRootRef(value ChainRootRefV1) error {
	id, err := uuid.Parse(value.ID)
	if err != nil || id.String() != value.ID || value.FromSequence < 1 ||
		value.ToSequence < value.FromSequence || !lowerHex64.MatchString(value.RootHash) ||
		strings.TrimSpace(value.KeyID) == "" {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	if _, err := ParseWireTime(value.ComputedAt); err != nil {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	signature, err := base64.StdEncoding.DecodeString(value.Signature)
	if err != nil || len(signature) != 64 {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	return nil
}

func validateObjectVersion(value ObjectVersionV1) error {
	if strings.TrimSpace(value.BucketID) == "" || strings.TrimSpace(value.Key) == "" ||
		strings.TrimSpace(value.VersionID) == "" || !lowerHex64.MatchString(value.SHA256) ||
		value.SizeBytes < 0 || strings.TrimSpace(value.ContentType) == "" ||
		strings.TrimSpace(value.ProviderChecksum) == "" || strings.TrimSpace(value.ETag) == "" || value.RowCount < 0 {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if !validObjectKey(value.Key) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.BucketID == "local-fixture" {
		if value.EncryptionMode != "LOCAL-ONLY" || value.KMSKeyID != "" || value.ObjectLockMode != "NONE" {
			return &FormatError{Code: "archive_format_invalid"}
		}
	} else if value.EncryptionMode != "SSE-KMS" || strings.TrimSpace(value.KMSKeyID) == "" ||
		strings.TrimSpace(value.ObjectLockMode) == "" {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if _, err := ParseWireTime(value.RetainUntil); err != nil {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	return nil
}

func validateArtifactRef(value ArtifactRefV1) error {
	if strings.TrimSpace(value.BucketID) == "" || strings.TrimSpace(value.Key) == "" ||
		strings.TrimSpace(value.VersionID) == "" || !lowerHex64.MatchString(value.SHA256) ||
		!validObjectKey(value.Key) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	return nil
}

func validObjectKey(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) ||
		strings.ContainsAny(value, `:*?"<>|`) || path.Clean(value) != value ||
		!strings.HasPrefix(value, "audit/v1/") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validManifestObjectRef(key, digest string) bool {
	matches := manifestObjectKeyPattern.FindStringSubmatch(key)
	return len(matches) == 2 && matches[1] == digest
}

func validCheckpointObjectRef(key, digest string) bool {
	matches := checkpointObjectKeyPattern.FindStringSubmatch(key)
	return len(matches) == 2 && matches[1] == digest
}

func validateCounts(counts [2]CanonicalCountV1, total int64) error {
	if counts[0].Version != 1 || counts[1].Version != 2 || counts[0].RowCount < 0 ||
		counts[1].RowCount < 0 || counts[0].RowCount+counts[1].RowCount != total {
		return &FormatError{Code: "archive_format_invalid"}
	}
	return nil
}

func ValidateManifestStructure(value ManifestV1) error {
	if value.Kind != ManifestKind {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.FormatVersion != FormatVersion {
		return &CompatibilityError{Code: CompatibilityVerifierOutdated, FormatVersion: value.FormatVersion}
	}
	if strings.TrimSpace(value.ExporterVersion) == "" || strings.TrimSpace(value.ExporterCommit) == "" ||
		value.FromSequence < 1 || value.ToSequence < value.FromSequence || value.RowCount < 1 ||
		value.RowCount != value.ToSequence-value.FromSequence+1 || !lowerHex64.MatchString(value.FirstPrevHash) ||
		!lowerHex64.MatchString(value.FirstEventHash) || !lowerHex64.MatchString(value.LastEventHash) ||
		value.OversizedRecordCount < 0 || value.OversizedRecordCount > value.RowCount {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateCounts(value.CanonicalVersionCounts, value.RowCount); err != nil {
		return err
	}
	if err := validateObjectVersion(value.Payload); err != nil || value.Payload.RowCount != value.RowCount {
		return &FormatError{Code: "archive_format_invalid", Cause: err}
	}
	wantPayloadKey := fmt.Sprintf("audit/v1/payload/seq-%019d-%019d-%s.ndjson",
		value.FromSequence, value.ToSequence, value.Payload.SHA256)
	if value.Payload.Key != wantPayloadKey {
		return &FormatError{Code: "archive_format_invalid"}
	}
	var projectionRows int64
	for index, projection := range value.Projections {
		if !validEnvironment(projection.Environment) ||
			(index > 0 && value.Projections[index-1].Environment >= projection.Environment) {
			return &FormatError{Code: "archive_format_invalid"}
		}
		if err := validateObjectVersion(projection.Object); err != nil {
			return err
		}
		wantProjectionKey := fmt.Sprintf("audit/v1/projection/%s/seq-%019d-%019d-%s.ndjson",
			projection.Environment, value.FromSequence, value.ToSequence, projection.Object.SHA256)
		if projection.Object.Key != wantProjectionKey {
			return &FormatError{Code: "archive_format_invalid"}
		}
		projectionRows += projection.Object.RowCount
	}
	if projectionRows != value.RowCount {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateChainRootRef(value.ChainRoot); err != nil {
		return err
	}
	for _, timestamp := range []WireTime{value.CreatedAt, value.SourceTipObservedAt, value.ActionRunSnapshotAt} {
		if _, err := ParseWireTime(timestamp); err != nil {
			return &FormatError{Code: "archive_format_invalid", Cause: err}
		}
	}
	if value.EligibleActionRunCount < 0 || value.MatchedActionRunCount < 0 ||
		value.MissingAuditEventCount < 0 || value.DuplicateAuditEventCount < 0 ||
		value.EligibleActionRunCount != value.MatchedActionRunCount+value.MissingAuditEventCount+
			value.DuplicateAuditEventCount {
		return &FormatError{Code: "archive_format_invalid"}
	}
	previous := value.PreviousManifest
	if value.FromSequence == 1 {
		if previous.Kind != "genesis" || previous.BucketID != "" || previous.Key != "" ||
			previous.VersionID != "" || previous.SHA256 != ManifestGenesisSHA256 ||
			value.FirstPrevHash != audit.GenesisHash {
			return &FormatError{Code: "archive_format_invalid"}
		}
	} else if previous.Kind != "object" || strings.TrimSpace(previous.BucketID) == "" ||
		strings.TrimSpace(previous.Key) == "" || strings.TrimSpace(previous.VersionID) == "" ||
		!lowerHex64.MatchString(previous.SHA256) || !validObjectKey(previous.Key) ||
		!validManifestObjectRef(previous.Key, previous.SHA256) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	return nil
}

func validateCheckpoint(value CheckpointV1) error {
	if value.Kind != CheckpointKind {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.FormatVersion != FormatVersion {
		return &CompatibilityError{Code: CompatibilityVerifierOutdated, FormatVersion: value.FormatVersion}
	}
	if value.Generation < 1 || value.FromSequence < 1 || value.ToSequence < value.FromSequence ||
		value.ManifestCount < 1 || !lowerHex64.MatchString(value.CatalogRowsDigest) ||
		value.SourceTipSequence < value.ToSequence || !lowerHex64.MatchString(value.ApprovalEnvelopeSHA256) ||
		!lowerHex64.MatchString(value.OperationIntentDigest) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateArtifactRef(value.FirstManifest); err != nil {
		return err
	}
	if !validManifestObjectRef(value.FirstManifest.Key, value.FirstManifest.SHA256) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateArtifactRef(value.TerminalManifest); err != nil {
		return err
	}
	if !validManifestObjectRef(value.TerminalManifest.Key, value.TerminalManifest.SHA256) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.FirstManifest.BucketID == "local-fixture" || value.TerminalManifest.BucketID == "local-fixture" {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateChainRootRef(value.ChainRoot); err != nil {
		return err
	}
	if value.ChainRoot.ToSequence != value.ToSequence {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateCounts(value.CanonicalVersionCounts, value.ToSequence-value.FromSequence+1); err != nil {
		return err
	}
	if value.ChainIntegrity != "verified" && value.ChainIntegrity != "failed" &&
		value.ChainIntegrity != "not_verified" {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.CaptureCompleteness != "verified" && value.CaptureCompleteness != "gaps_found" &&
		value.CaptureCompleteness != "not_checked" {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.EligibleActionRunCount != value.MatchedActionRunCount+value.MissingAuditEventCount+
		value.DuplicateAuditEventCount {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if _, err := ParseWireTime(value.SourceTipObservedAt); err != nil {
		return err
	}
	if _, err := ParseWireTime(value.CreatedAt); err != nil {
		return err
	}
	previous := value.PreviousCheckpoint
	if value.Generation == 1 {
		if previous.Kind != "genesis" || previous.BucketID != "" || previous.Key != "" ||
			previous.VersionID != "" || previous.SHA256 != CheckpointGenesisSHA256 {
			return &FormatError{Code: "archive_format_invalid"}
		}
	} else if previous.Kind != "object" || strings.TrimSpace(previous.BucketID) == "" ||
		strings.TrimSpace(previous.Key) == "" || strings.TrimSpace(previous.VersionID) == "" ||
		!lowerHex64.MatchString(previous.SHA256) || !validObjectKey(previous.Key) ||
		!validCheckpointObjectRef(previous.Key, previous.SHA256) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	return nil
}

func validateRecoveryIndex(value RecoveryIndexV1) error {
	if value.Kind != RecoveryIndexKind {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.FormatVersion != FormatVersion {
		return &CompatibilityError{Code: CompatibilityVerifierOutdated, FormatVersion: value.FormatVersion}
	}
	if value.Generation < 1 || value.TerminalSequence < 1 || !lowerHex64.MatchString(value.TerminalRootHash) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateArtifactRef(value.Checkpoint); err != nil {
		return err
	}
	if !validCheckpointObjectRef(value.Checkpoint.Key, value.Checkpoint.SHA256) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if err := validateArtifactRef(value.TerminalManifest); err != nil {
		return err
	}
	if !validManifestObjectRef(value.TerminalManifest.Key, value.TerminalManifest.SHA256) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if value.Checkpoint.BucketID == "local-fixture" || value.TerminalManifest.BucketID == "local-fixture" {
		return &FormatError{Code: "archive_format_invalid"}
	}
	if _, err := ParseWireTime(value.UpdatedAt); err != nil {
		return err
	}
	if value.Generation == 1 {
		if value.PreviousGeneration != 0 || value.PreviousIndexSHA256 != RecoveryGenesisSHA256 {
			return &FormatError{Code: "archive_format_invalid"}
		}
	} else if value.PreviousGeneration != value.Generation-1 || !lowerHex64.MatchString(value.PreviousIndexSHA256) {
		return &FormatError{Code: "archive_format_invalid"}
	}
	return nil
}
