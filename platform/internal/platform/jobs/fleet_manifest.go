package jobs

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// JobFleetReplicaV1 identifies one logical worker slot and the immutable build
// that is allowed to occupy it. LogicalReplicaID is not a hostname or PID.
type JobFleetReplicaV1 struct {
	LogicalReplicaID string `json:"logical_replica_id"`
	BuildDigest      string `json:"build_digest"`
}

// JobFleetBuildCapabilityV1 records the runtime versions a signed build
// advertises. Every build referenced by Replicas must have exactly one row.
type JobFleetBuildCapabilityV1 struct {
	BuildDigest           string `json:"build_digest"`
	RiverVersion          string `json:"river_version"`
	RiverMigrationVersion string `json:"river_migration_version"`
	JobContractVersion    int    `json:"job_contract_version"`
	EffectiveManifestHash string `json:"effective_manifest_hash"`
}

// JobFleetManifestV1 is the unsigned payload of a signed fleet decision. Its
// field order is frozen by the struct declaration and encoding/json's stable
// struct-field encoding.
type JobFleetManifestV1 struct {
	Kind                         string                      `json:"kind"`
	Version                      int                         `json:"version"`
	Environment                  string                      `json:"environment"`
	WorkerClusterID              string                      `json:"worker_cluster_id"`
	RiverSchema                  string                      `json:"river_schema"`
	DatabaseBindingHash          string                      `json:"database_binding_hash"`
	Epoch                        int64                       `json:"epoch"`
	JobContractVersion           int                         `json:"job_contract_version"`
	EffectiveManifestHash        string                      `json:"effective_manifest_hash"`
	Replicas                     []JobFleetReplicaV1         `json:"replicas"`
	BuildCapabilities            []JobFleetBuildCapabilityV1 `json:"build_capabilities"`
	ActiveBindingInventoryEpoch  int64                       `json:"active_binding_inventory_epoch"`
	ActiveBindingInventorySHA256 string                      `json:"active_binding_inventory_sha256"`
	IssuedAt                     string                      `json:"issued_at"`
	ValidFrom                    string                      `json:"valid_from"`
	ValidUntil                   string                      `json:"valid_until"`
	ChangeRef                    string                      `json:"change_ref"`
	Nonce                        string                      `json:"nonce"`
}

// SignedJobFleetManifestV1 is a flat wire envelope. It embeds the unsigned
// payload so JSON remains easy to inspect while the digest/signature fields are
// clearly separated at the end of the canonical object.
type SignedJobFleetManifestV1 struct {
	JobFleetManifestV1
	UnsignedSHA256     string `json:"unsigned_sha256"`
	SignatureAlgorithm string `json:"signature_algorithm"`
	SignatureKeyID     string `json:"signature_key_id"`
	Signature          string `json:"signature"`
}

// ValidateJobFleetManifest checks all structural and cross-row invariants but
// does not verify a signature or consult a keyring.
func ValidateJobFleetManifest(value JobFleetManifestV1) error {
	if value.Kind != JobFleetManifestKind {
		return fmt.Errorf("job fleet manifest: kind must be %q", JobFleetManifestKind)
	}
	if value.Version != 1 {
		return fmt.Errorf("job fleet manifest: unsupported version %d", value.Version)
	}
	identityFields := []struct {
		name  string
		value string
	}{
		{name: "environment", value: value.Environment},
		{name: "worker_cluster_id", value: value.WorkerClusterID},
		{name: "river_schema", value: value.RiverSchema},
	}
	for _, field := range identityFields {
		name, candidate := field.name, field.value
		if err := validateFleetID(name, candidate, 63); err != nil {
			return fmt.Errorf("job fleet manifest: %w", err)
		}
	}
	if err := validateFleetDigest("database_binding_hash", value.DatabaseBindingHash); err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	if value.Epoch <= 0 || value.ActiveBindingInventoryEpoch <= 0 {
		return errors.New("job fleet manifest: epochs must be positive")
	}
	if value.JobContractVersion <= 0 {
		return errors.New("job fleet manifest: job_contract_version must be positive")
	}
	if err := validateFleetDigest("effective_manifest_hash", value.EffectiveManifestHash); err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	if err := validateFleetDigest("active_binding_inventory_sha256", value.ActiveBindingInventorySHA256); err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	if len(value.Replicas) == 0 {
		return errors.New("job fleet manifest: replicas must not be empty")
	}
	seenReplica := make(map[string]struct{}, len(value.Replicas))
	usedBuilds := make(map[string]struct{}, len(value.Replicas))
	for index, replica := range value.Replicas {
		if err := validateFleetID("replica logical id", replica.LogicalReplicaID, 63); err != nil {
			return fmt.Errorf("job fleet manifest: replicas[%d]: %w", index, err)
		}
		if err := validateFleetBuildDigest("replica build digest", replica.BuildDigest); err != nil {
			return fmt.Errorf("job fleet manifest: replicas[%d]: %w", index, err)
		}
		if _, duplicate := seenReplica[replica.LogicalReplicaID]; duplicate {
			return fmt.Errorf("job fleet manifest: duplicate replica %q", replica.LogicalReplicaID)
		}
		seenReplica[replica.LogicalReplicaID] = struct{}{}
		usedBuilds[replica.BuildDigest] = struct{}{}
		if index > 0 && value.Replicas[index-1].LogicalReplicaID >= replica.LogicalReplicaID {
			return errors.New("job fleet manifest: replicas must be sorted by logical_replica_id")
		}
	}
	if len(value.BuildCapabilities) == 0 {
		return errors.New("job fleet manifest: build_capabilities must not be empty")
	}
	seenBuild := make(map[string]struct{}, len(value.BuildCapabilities))
	capabilityByBuild := make(map[string]JobFleetBuildCapabilityV1, len(value.BuildCapabilities))
	for index, capability := range value.BuildCapabilities {
		if err := validateFleetBuildDigest("capability build digest", capability.BuildDigest); err != nil {
			return fmt.Errorf("job fleet manifest: build_capabilities[%d]: %w", index, err)
		}
		if err := validateFleetPrintable("river_version", capability.RiverVersion, 127); err != nil {
			return fmt.Errorf("job fleet manifest: build_capabilities[%d]: %w", index, err)
		}
		if err := validateFleetPrintable("river_migration_version", capability.RiverMigrationVersion, 127); err != nil {
			return fmt.Errorf("job fleet manifest: build_capabilities[%d]: %w", index, err)
		}
		if capability.JobContractVersion != value.JobContractVersion {
			return fmt.Errorf("job fleet manifest: build %q job contract version mismatch", capability.BuildDigest)
		}
		if capability.EffectiveManifestHash != value.EffectiveManifestHash {
			return fmt.Errorf("job fleet manifest: build %q effective hash mismatch", capability.BuildDigest)
		}
		if _, duplicate := seenBuild[capability.BuildDigest]; duplicate {
			return fmt.Errorf("job fleet manifest: duplicate build capability %q", capability.BuildDigest)
		}
		seenBuild[capability.BuildDigest] = struct{}{}
		capabilityByBuild[capability.BuildDigest] = capability
		if index > 0 && value.BuildCapabilities[index-1].BuildDigest >= capability.BuildDigest {
			return errors.New("job fleet manifest: build_capabilities must be sorted by build_digest")
		}
	}
	for _, replica := range value.Replicas {
		if _, ok := capabilityByBuild[replica.BuildDigest]; !ok {
			return fmt.Errorf("job fleet manifest: unknown build %q", replica.BuildDigest)
		}
	}
	for build := range capabilityByBuild {
		if _, used := usedBuilds[build]; !used {
			return fmt.Errorf("job fleet manifest: orphan build capability %q", build)
		}
	}
	if _, _, _, err := validateFleetValidity("job fleet manifest", value.IssuedAt, value.ValidFrom, value.ValidUntil); err != nil {
		return err
	}
	if err := validateFleetChangeRef(value.ChangeRef); err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	if err := validateFleetID("nonce", value.Nonce, 127); err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	return nil
}

func validateFleetChangeRef(value string) error {
	return validateFleetPrintable("change_ref", value, 256)
}

func validateFleetPrintable(name, value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be 1..%d trimmed bytes", name, maxBytes)
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return fmt.Errorf("%s must contain printable ASCII only", name)
		}
	}
	return nil
}

// CanonicalJobFleetManifestBytes returns the exact UTF-8 JSON bytes covered by
// the detached signature. The input must already use the frozen row order.
func CanonicalJobFleetManifestBytes(value JobFleetManifestV1) ([]byte, error) {
	if err := ValidateJobFleetManifest(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// JobFleetManifestSHA256 computes the lowercase SHA-256 of canonical payload
// bytes, never of caller formatting or the signed envelope.
func JobFleetManifestSHA256(value JobFleetManifestV1) (string, error) {
	canonical, err := CanonicalJobFleetManifestBytes(value)
	if err != nil {
		return "", err
	}
	return fleetSHA256Hex(canonical), nil
}

// EncodeSignedJobFleetManifest emits deterministic flat JSON after validating
// the payload and envelope shape. It does not sign; signing is separate
// authority code and is intentionally absent from this runtime package.
func EncodeSignedJobFleetManifest(value SignedJobFleetManifestV1) ([]byte, error) {
	if err := validateSignedJobFleetManifestWire(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// DecodeSignedJobFleetManifest strictly decodes one JSON value and rejects
// unknown/duplicate fields and trailing values. Signature verification is a
// separate explicit step.
func DecodeSignedJobFleetManifest(raw []byte) (SignedJobFleetManifestV1, error) {
	if err := rejectDuplicateFleetJSONKeys(raw); err != nil {
		return SignedJobFleetManifestV1{}, fmt.Errorf("job fleet manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value SignedJobFleetManifestV1
	if err := decoder.Decode(&value); err != nil {
		return SignedJobFleetManifestV1{}, fmt.Errorf("job fleet manifest: decode: %w", err)
	}
	if err := rejectFleetTrailingJSON(decoder); err != nil {
		return SignedJobFleetManifestV1{}, fmt.Errorf("job fleet manifest: %w", err)
	}
	if err := validateSignedJobFleetManifestWire(value); err != nil {
		return SignedJobFleetManifestV1{}, err
	}
	canonical, err := EncodeSignedJobFleetManifest(value)
	if err != nil {
		return SignedJobFleetManifestV1{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return SignedJobFleetManifestV1{}, errors.New("job fleet manifest: non-canonical JSON bytes")
	}
	return value, nil
}

func validateSignedJobFleetManifestWire(value SignedJobFleetManifestV1) error {
	if err := ValidateJobFleetManifest(value.JobFleetManifestV1); err != nil {
		return err
	}
	if err := validateFleetDigest("unsigned_sha256", value.UnsignedSHA256); err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	digest, err := JobFleetManifestSHA256(value.JobFleetManifestV1)
	if err != nil {
		return err
	}
	if value.UnsignedSHA256 != digest {
		return errors.New("job fleet manifest: unsigned digest mismatch")
	}
	if value.SignatureAlgorithm != FleetSignatureAlgorithm {
		return fmt.Errorf("job fleet manifest: signature_algorithm must be %s", FleetSignatureAlgorithm)
	}
	if err := validateFleetID("signature key id", value.SignatureKeyID, 63); err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(value.Signature)
	if err != nil || len(decoded) != ed25519.SignatureSize || strings.TrimSpace(value.Signature) != value.Signature || base64.StdEncoding.EncodeToString(decoded) != value.Signature {
		return errors.New("job fleet manifest: invalid Ed25519 signature encoding")
	}
	return nil
}

// VerifySignedJobFleetManifest verifies structure, canonical digest, exact
// domain/purpose/protocol key selection, signature, and current validity.
func VerifySignedJobFleetManifest(value SignedJobFleetManifestV1, keyring JobFleetKeyringLookup, now time.Time) error {
	if err := validateSignedJobFleetManifestWire(value); err != nil {
		return err
	}
	digest, err := JobFleetManifestSHA256(value.JobFleetManifestV1)
	if err != nil {
		return err
	}
	if value.UnsignedSHA256 != digest {
		return errors.New("job fleet manifest: unsigned digest mismatch")
	}
	issuedAt, from, until, _ := validateFleetValidity("job fleet manifest", value.IssuedAt, value.ValidFrom, value.ValidUntil)
	if now.IsZero() {
		return errors.New("job fleet manifest: verification time is zero")
	}
	now = now.UTC()
	if now.Before(from) || !now.Before(until) {
		return errors.New("job fleet manifest: artifact outside validity window")
	}
	if keyring == nil {
		return errors.New("job fleet manifest: keyring unavailable")
	}
	record, err := keyring.Lookup(value.SignatureKeyID, JobFleetManifestPurpose, JobFleetManifestProtocol, issuedAt)
	if err != nil {
		return fmt.Errorf("job fleet manifest: %w", err)
	}
	if record.Algorithm != FleetSignatureAlgorithm || record.Purpose != JobFleetManifestPurpose || record.Protocol != JobFleetManifestProtocol {
		return errors.New("job fleet manifest: key metadata mismatch")
	}
	public, err := decodeFleetPublicKey(record.PublicKey)
	if err != nil {
		return err
	}
	if record.Fingerprint != fleetSHA256Hex(public) {
		return errors.New("job fleet manifest: key fingerprint mismatch")
	}
	signature, _ := base64.StdEncoding.DecodeString(value.Signature)
	if !ed25519.Verify(public, fleetSignaturePayload(JobFleetManifestProtocol, digest), signature) {
		return errors.New("job fleet manifest: signature invalid")
	}
	return nil
}
