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

// JobFleetBindingV1 is one immutable database-binding history row. A binding
// hash may have closed history rows, but at most one active row is allowed in
// an inventory snapshot.
type JobFleetBindingV1 struct {
	DatabaseBindingHash string `json:"database_binding_hash"`
	Environment         string `json:"environment"`
	WorkerClusterID     string `json:"worker_cluster_id"`
	Status              string `json:"status"`
}

// JobFleetInventoryV1 is the unsigned active/closed binding inventory pinned
// by a fleet manifest.
type JobFleetInventoryV1 struct {
	Kind       string              `json:"kind"`
	Version    int                 `json:"version"`
	Epoch      int64               `json:"epoch"`
	Bindings   []JobFleetBindingV1 `json:"bindings"`
	IssuedAt   string              `json:"issued_at"`
	ValidFrom  string              `json:"valid_from"`
	ValidUntil string              `json:"valid_until"`
	ChangeRef  string              `json:"change_ref"`
	Nonce      string              `json:"nonce"`
}

// SignedJobFleetInventoryV1 is a flat detached-signature envelope.
type SignedJobFleetInventoryV1 struct {
	JobFleetInventoryV1
	UnsignedSHA256     string `json:"unsigned_sha256"`
	SignatureAlgorithm string `json:"signature_algorithm"`
	SignatureKeyID     string `json:"signature_key_id"`
	Signature          string `json:"signature"`
}

// ValidateJobFleetInventory validates rows, ordering, lifecycle status and
// validity without consulting a keyring.
func ValidateJobFleetInventory(value JobFleetInventoryV1) error {
	if value.Kind != JobFleetInventoryKind {
		return fmt.Errorf("job fleet inventory: kind must be %q", JobFleetInventoryKind)
	}
	if value.Version != 1 {
		return fmt.Errorf("job fleet inventory: unsupported version %d", value.Version)
	}
	if value.Epoch <= 0 {
		return errors.New("job fleet inventory: epoch must be positive")
	}
	if value.Bindings == nil {
		return errors.New("job fleet inventory: bindings must be an array")
	}
	if _, _, _, err := validateFleetValidity("job fleet inventory", value.IssuedAt, value.ValidFrom, value.ValidUntil); err != nil {
		return err
	}
	if err := validateFleetChangeRef(value.ChangeRef); err != nil {
		return fmt.Errorf("job fleet inventory: %w", err)
	}
	if err := validateFleetID("nonce", value.Nonce, 127); err != nil {
		return fmt.Errorf("job fleet inventory: %w", err)
	}
	seenExact := make(map[string]struct{}, len(value.Bindings))
	activeByHash := make(map[string]JobFleetBindingV1, len(value.Bindings))
	for index, row := range value.Bindings {
		if err := validateFleetDigest("database_binding_hash", row.DatabaseBindingHash); err != nil {
			return fmt.Errorf("job fleet inventory: bindings[%d]: %w", index, err)
		}
		if err := validateFleetID("binding environment", row.Environment, 63); err != nil {
			return fmt.Errorf("job fleet inventory: bindings[%d]: %w", index, err)
		}
		if err := validateFleetID("binding worker cluster id", row.WorkerClusterID, 63); err != nil {
			return fmt.Errorf("job fleet inventory: bindings[%d]: %w", index, err)
		}
		if row.Status != JobFleetBindingActive && row.Status != JobFleetBindingClosed {
			return fmt.Errorf("job fleet inventory: bindings[%d] status %q is invalid", index, row.Status)
		}
		if index > 0 && fleetBindingSortKey(value.Bindings[index-1]) >= fleetBindingSortKey(row) {
			return errors.New("job fleet inventory: bindings must be sorted by hash/environment/cluster/status")
		}
		exact := fleetBindingSortKey(row)
		if _, duplicate := seenExact[exact]; duplicate {
			return fmt.Errorf("job fleet inventory: duplicate binding row %q", row.DatabaseBindingHash)
		}
		seenExact[exact] = struct{}{}
		if row.Status == JobFleetBindingActive {
			if previous, duplicate := activeByHash[row.DatabaseBindingHash]; duplicate {
				return fmt.Errorf("job fleet inventory: binding %q active for both %s/%s and %s/%s", row.DatabaseBindingHash, previous.Environment, previous.WorkerClusterID, row.Environment, row.WorkerClusterID)
			}
			activeByHash[row.DatabaseBindingHash] = row
		}
	}
	return nil
}

func fleetBindingSortKey(row JobFleetBindingV1) string {
	return row.DatabaseBindingHash + "\x00" + row.Environment + "\x00" + row.WorkerClusterID + "\x00" + row.Status
}

// CanonicalJobFleetInventoryBytes returns the exact JSON bytes covered by an
// inventory signature.
func CanonicalJobFleetInventoryBytes(value JobFleetInventoryV1) ([]byte, error) {
	if err := ValidateJobFleetInventory(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// JobFleetInventorySHA256 computes the lowercase digest of canonical inventory
// payload bytes.
func JobFleetInventorySHA256(value JobFleetInventoryV1) (string, error) {
	canonical, err := CanonicalJobFleetInventoryBytes(value)
	if err != nil {
		return "", err
	}
	return fleetSHA256Hex(canonical), nil
}

// EncodeSignedJobFleetInventory emits deterministic flat JSON without signing.
func EncodeSignedJobFleetInventory(value SignedJobFleetInventoryV1) ([]byte, error) {
	if err := validateSignedJobFleetInventoryWire(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// DecodeSignedJobFleetInventory strictly decodes one envelope.
func DecodeSignedJobFleetInventory(raw []byte) (SignedJobFleetInventoryV1, error) {
	if err := rejectDuplicateFleetJSONKeys(raw); err != nil {
		return SignedJobFleetInventoryV1{}, fmt.Errorf("job fleet inventory: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value SignedJobFleetInventoryV1
	if err := decoder.Decode(&value); err != nil {
		return SignedJobFleetInventoryV1{}, fmt.Errorf("job fleet inventory: decode: %w", err)
	}
	if err := rejectFleetTrailingJSON(decoder); err != nil {
		return SignedJobFleetInventoryV1{}, fmt.Errorf("job fleet inventory: %w", err)
	}
	if err := validateSignedJobFleetInventoryWire(value); err != nil {
		return SignedJobFleetInventoryV1{}, err
	}
	canonical, err := EncodeSignedJobFleetInventory(value)
	if err != nil {
		return SignedJobFleetInventoryV1{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return SignedJobFleetInventoryV1{}, errors.New("job fleet inventory: non-canonical JSON bytes")
	}
	return value, nil
}

func validateSignedJobFleetInventoryWire(value SignedJobFleetInventoryV1) error {
	if err := ValidateJobFleetInventory(value.JobFleetInventoryV1); err != nil {
		return err
	}
	if err := validateFleetDigest("unsigned_sha256", value.UnsignedSHA256); err != nil {
		return fmt.Errorf("job fleet inventory: %w", err)
	}
	digest, err := JobFleetInventorySHA256(value.JobFleetInventoryV1)
	if err != nil {
		return err
	}
	if value.UnsignedSHA256 != digest {
		return errors.New("job fleet inventory: unsigned digest mismatch")
	}
	if value.SignatureAlgorithm != FleetSignatureAlgorithm {
		return fmt.Errorf("job fleet inventory: signature_algorithm must be %s", FleetSignatureAlgorithm)
	}
	if err := validateFleetID("signature key id", value.SignatureKeyID, 63); err != nil {
		return fmt.Errorf("job fleet inventory: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(value.Signature)
	if err != nil || len(decoded) != ed25519.SignatureSize || strings.TrimSpace(value.Signature) != value.Signature || base64.StdEncoding.EncodeToString(decoded) != value.Signature {
		return errors.New("job fleet inventory: invalid Ed25519 signature encoding")
	}
	return nil
}

// VerifySignedJobFleetInventory verifies the detached signature and current
// validity using only the independently supplied public keyring.
func VerifySignedJobFleetInventory(value SignedJobFleetInventoryV1, keyring JobFleetKeyringLookup, now time.Time) error {
	if err := validateSignedJobFleetInventoryWire(value); err != nil {
		return err
	}
	digest, err := JobFleetInventorySHA256(value.JobFleetInventoryV1)
	if err != nil {
		return err
	}
	if value.UnsignedSHA256 != digest {
		return errors.New("job fleet inventory: unsigned digest mismatch")
	}
	issuedAt, from, until, _ := validateFleetValidity("job fleet inventory", value.IssuedAt, value.ValidFrom, value.ValidUntil)
	if now.IsZero() {
		return errors.New("job fleet inventory: verification time is zero")
	}
	now = now.UTC()
	if now.Before(from) || !now.Before(until) {
		return errors.New("job fleet inventory: artifact outside validity window")
	}
	if keyring == nil {
		return errors.New("job fleet inventory: keyring unavailable")
	}
	record, err := keyring.Lookup(value.SignatureKeyID, JobFleetInventoryPurpose, JobFleetInventoryProtocol, issuedAt)
	if err != nil {
		return fmt.Errorf("job fleet inventory: %w", err)
	}
	if record.Algorithm != FleetSignatureAlgorithm || record.Purpose != JobFleetInventoryPurpose || record.Protocol != JobFleetInventoryProtocol {
		return errors.New("job fleet inventory: key metadata mismatch")
	}
	public, err := decodeFleetPublicKey(record.PublicKey)
	if err != nil {
		return err
	}
	if record.Fingerprint != fleetSHA256Hex(public) {
		return errors.New("job fleet inventory: key fingerprint mismatch")
	}
	signature, _ := base64.StdEncoding.DecodeString(value.Signature)
	if !ed25519.Verify(public, fleetSignaturePayload(JobFleetInventoryProtocol, digest), signature) {
		return errors.New("job fleet inventory: signature invalid")
	}
	return nil
}

// VerifyJobFleetManifestInventoryPair verifies both artifacts and enforces the
// cross-artifact epoch/digest/active-binding/nonce invariants before a worker
// could use either artifact.
func VerifyJobFleetManifestInventoryPair(manifest SignedJobFleetManifestV1, inventory SignedJobFleetInventoryV1, keyring JobFleetKeyringLookup, now time.Time) error {
	if err := VerifySignedJobFleetManifest(manifest, keyring, now); err != nil {
		return err
	}
	if err := VerifySignedJobFleetInventory(inventory, keyring, now); err != nil {
		return err
	}
	manifestIssued, manifestFrom, manifestUntil, err := validateFleetValidity("job fleet manifest", manifest.IssuedAt, manifest.ValidFrom, manifest.ValidUntil)
	if err != nil {
		return err
	}
	_, inventoryFrom, inventoryUntil, err := validateFleetValidity("job fleet inventory", inventory.IssuedAt, inventory.ValidFrom, inventory.ValidUntil)
	if err != nil {
		return err
	}
	if !manifestFrom.Before(inventoryUntil) || !inventoryFrom.Before(manifestUntil) {
		return errors.New("job fleet manifest/inventory: validity windows do not overlap")
	}
	if manifestIssued.Before(inventoryFrom) || !manifestIssued.Before(inventoryUntil) {
		return errors.New("job fleet manifest: issued_at is outside inventory validity")
	}
	inventoryDigest, err := JobFleetInventorySHA256(inventory.JobFleetInventoryV1)
	if err != nil {
		return err
	}
	if manifest.ActiveBindingInventoryEpoch != inventory.Epoch || manifest.ActiveBindingInventorySHA256 != inventoryDigest {
		return errors.New("job fleet manifest: inventory epoch/digest mismatch")
	}
	if manifest.ChangeRef != inventory.ChangeRef {
		return errors.New("job fleet manifest/inventory: change_ref mismatch")
	}
	if manifest.Nonce == inventory.Nonce {
		return errors.New("job fleet manifest/inventory: nonce replay")
	}
	activeFound := false
	for _, row := range inventory.Bindings {
		if row.Status == JobFleetBindingActive && row.DatabaseBindingHash == manifest.DatabaseBindingHash &&
			row.Environment == manifest.Environment && row.WorkerClusterID == manifest.WorkerClusterID {
			activeFound = true
			break
		}
	}
	if !activeFound {
		return errors.New("job fleet manifest: database binding is not active for environment/cluster")
	}
	return nil
}

// VerifyJobFleetManifestInventoryPairWithHistory adds caller-supplied replay
// state to the stateless pair verifier. It is intentionally not persisted by
// this package and performs no database or worker I/O.
func VerifyJobFleetManifestInventoryPairWithHistory(
	manifest SignedJobFleetManifestV1,
	inventory SignedJobFleetInventoryV1,
	keyring JobFleetKeyringLookup,
	now time.Time,
	previousManifestEpoch, previousInventoryEpoch int64,
	previousNonces []string,
) error {
	if err := VerifyJobFleetManifestInventoryPair(manifest, inventory, keyring, now); err != nil {
		return err
	}
	if err := ValidateFleetEpochNonce(manifest.Epoch, manifest.Nonce, previousManifestEpoch, previousNonces); err != nil {
		return fmt.Errorf("job fleet manifest replay: %w", err)
	}
	if err := ValidateFleetEpochNonce(inventory.Epoch, inventory.Nonce, previousInventoryEpoch, previousNonces); err != nil {
		return fmt.Errorf("job fleet inventory replay: %w", err)
	}
	return nil
}

// ValidateFleetEpoch rejects a non-positive or replayed epoch. Callers pass
// the last accepted epoch from their append-only local state.
func ValidateFleetEpoch(current, previous int64) error {
	if current <= 0 || previous < 0 || current <= previous {
		return fmt.Errorf("job fleet epoch %d is not greater than previous %d", current, previous)
	}
	return nil
}

// ValidateFleetNonce rejects an empty/non-canonical nonce and replay against a
// previously accepted nonce.
func ValidateFleetNonce(current, previous string) error {
	return ValidateFleetNonceHistory(current, []string{previous})
}

// ValidateFleetNonceHistory checks a candidate nonce against all previously
// accepted nonces. The caller owns persistence of this history; the contract
// package deliberately keeps no mutable replay state.
func ValidateFleetNonceHistory(current string, previous []string) error {
	if err := validateFleetID("nonce", current, 127); err != nil {
		return err
	}
	for _, prior := range previous {
		if current == prior {
			return errors.New("fleet nonce replay")
		}
	}
	return nil
}

// ValidateFleetEpochNonce combines monotonic epoch and complete nonce-history
// checks for callers processing an append-only stream.
func ValidateFleetEpochNonce(currentEpoch int64, currentNonce string, previousEpoch int64, previousNonces []string) error {
	if err := ValidateFleetEpoch(currentEpoch, previousEpoch); err != nil {
		return err
	}
	return ValidateFleetNonceHistory(currentNonce, previousNonces)
}

// ValidateJobFleetInventoryTransition checks an append-only inventory update
// against the last accepted snapshot. Existing rows are immutable: a signer
// may append a new closed/reopened row in a later epoch, but cannot rewrite a
// historical environment, cluster, or status in place.
func ValidateJobFleetInventoryTransition(previous, current JobFleetInventoryV1) error {
	if err := ValidateJobFleetInventory(previous); err != nil {
		return fmt.Errorf("previous inventory: %w", err)
	}
	if err := ValidateJobFleetInventory(current); err != nil {
		return fmt.Errorf("current inventory: %w", err)
	}
	if err := ValidateFleetEpoch(current.Epoch, previous.Epoch); err != nil {
		return err
	}
	if err := ValidateFleetNonceHistory(current.Nonce, []string{previous.Nonce}); err != nil {
		return err
	}
	previousRows := make(map[string]JobFleetBindingV1, len(previous.Bindings))
	for _, row := range previous.Bindings {
		key := fleetBindingSortKey(row)
		previousRows[key] = row
	}
	currentRows := make(map[string]struct{}, len(current.Bindings))
	for _, row := range current.Bindings {
		currentRows[fleetBindingSortKey(row)] = struct{}{}
	}
	for key := range previousRows {
		if _, found := currentRows[key]; !found {
			return fmt.Errorf("inventory history mutation/removal: %s", key)
		}
	}
	return nil
}
