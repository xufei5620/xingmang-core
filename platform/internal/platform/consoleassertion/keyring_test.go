package consoleassertion

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func validRecord(t *testing.T, keyID string) PublicKeyRecord {
	t.Helper()
	pub, _ := mustKeyPair(t)
	record, err := NewPublicKeyRecord(keyID, pub,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewPublicKeyRecord: %v", err)
	}
	return record
}

func TestNewPublicKeyRecordSelfValidates(t *testing.T) {
	record := validRecord(t, "2026-09")
	if record.Purpose != KeyringPurpose || record.Protocol != KeyringProtocol {
		t.Fatalf("unexpected purpose/protocol: %+v", record)
	}
	if record.Algorithm != KeyringAlgorithm {
		t.Fatalf("unexpected algorithm: %s", record.Algorithm)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRecordsEmptyIsValid(t *testing.T) {
	if err := ValidateRecords(nil); err != nil {
		t.Fatalf("empty manifest should be valid (unpopulated placeholder): %v", err)
	}
}

func TestValidateRecordsRejectsUnsortedOrDuplicate(t *testing.T) {
	a := validRecord(t, "2026-09")
	b := validRecord(t, "2026-01")
	if err := ValidateRecords([]PublicKeyRecord{a, b}); err == nil {
		t.Fatal("expected error for out-of-order key ids")
	}
	if err := ValidateRecords([]PublicKeyRecord{a, a}); err == nil {
		t.Fatal("expected error for duplicate key id")
	}
}

func TestValidateRecordsRejectsReusedPublicMaterial(t *testing.T) {
	pub, _ := mustKeyPair(t)
	first, err := NewPublicKeyRecord("2026-01", pub,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewPublicKeyRecord: %v", err)
	}
	second, err := NewPublicKeyRecord("2026-09", pub,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewPublicKeyRecord: %v", err)
	}
	if err := ValidateRecords([]PublicKeyRecord{first, second}); err == nil {
		t.Fatal("expected error for reused public key material across two ids")
	}
}

func TestPublicKeyRecordValidateRejectsForeignDomain(t *testing.T) {
	record := validRecord(t, "2026-09")
	record.Purpose = "job_fleet_manifest_signing"
	record.Protocol = "xm-job-fleet-manifest-v1"
	if err := record.Validate(); err == nil {
		t.Fatal("expected rejection of a foreign signing domain (job-fleet's own purpose/protocol)")
	}
}

func TestPublicKeyRecordValidateRejectsNonEd25519Algorithm(t *testing.T) {
	record := validRecord(t, "2026-09")
	record.Algorithm = "RS256"
	if err := record.Validate(); err == nil {
		t.Fatal("expected rejection of a non-Ed25519 algorithm")
	}
}

func TestPublicKeyRecordValidateRejectsFingerprintMismatch(t *testing.T) {
	record := validRecord(t, "2026-09")
	other, _ := mustKeyPair(t)
	record.PublicKey = base64.StdEncoding.EncodeToString(other)
	if err := record.Validate(); err == nil {
		t.Fatal("expected rejection of a fingerprint that does not match public_key")
	}
}

func TestPublicKeyRecordValidateRejectsInvalidValidityWindow(t *testing.T) {
	record := validRecord(t, "2026-09")
	record.ValidFrom, record.ValidUntil = record.ValidUntil, record.ValidFrom
	if err := record.Validate(); err == nil {
		t.Fatal("expected rejection of valid_from >= valid_until")
	}
}

func TestPublicKeyRecordValidateRejectsRevocationAtOrAfterExpiry(t *testing.T) {
	record := validRecord(t, "2026-09")
	late := record.ValidUntil
	record.RevokedAt = &late
	record.RevokeReason = "compromised"
	if err := record.Validate(); err == nil {
		t.Fatal("expected rejection of revocation at/after expiry")
	}
}

func TestPublicKeyRecordValidateRequiresRevokeReason(t *testing.T) {
	record := validRecord(t, "2026-09")
	mid := record.ValidFrom.Add(24 * time.Hour)
	record.RevokedAt = &mid
	record.RevokeReason = ""
	if err := record.Validate(); err == nil {
		t.Fatal("expected rejection of a blank revoke reason")
	}
}

func TestMarshalLoadKeyringJSONRoundTrip(t *testing.T) {
	record := validRecord(t, "2026-09")
	raw, err := MarshalManifestJSON([]PublicKeyRecord{record})
	if err != nil {
		t.Fatalf("MarshalManifestJSON: %v", err)
	}
	loaded, err := LoadKeyringJSON(raw)
	if err != nil {
		t.Fatalf("LoadKeyringJSON: %v", err)
	}
	if len(loaded) != 1 || loaded[0].KeyID != record.KeyID || loaded[0].PublicKey != record.PublicKey {
		t.Fatalf("round-trip mismatch: %+v", loaded)
	}
}

func TestLoadKeyringJSONAcceptsEmptyArrayPlaceholder(t *testing.T) {
	records, err := LoadKeyringJSON([]byte("[]\n"))
	if err != nil {
		t.Fatalf("LoadKeyringJSON([]): %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected zero records, got %d", len(records))
	}
}

func TestLoadKeyringJSONRejectsUnknownFields(t *testing.T) {
	raw := `[{"key_id":"2026-09","algorithm":"Ed25519","public_key":"x","fingerprint":"y",` +
		`"purpose":"console_admin_assertion_signing","protocol":"xm-console-assertion-v1",` +
		`"valid_from":"2026-01-01T00:00:00Z","valid_until":"2027-01-01T00:00:00Z",` +
		`"revoked_at":null,"revoke_reason":"","extra":"nope"}]`
	if _, err := LoadKeyringJSON([]byte(raw)); err == nil {
		t.Fatal("expected rejection of an unknown field")
	}
}

func TestLoadKeyringJSONRejectsDuplicateObjectKeys(t *testing.T) {
	raw := `[{"key_id":"2026-09","key_id":"2026-10","algorithm":"Ed25519","public_key":"x",` +
		`"fingerprint":"y","purpose":"console_admin_assertion_signing",` +
		`"protocol":"xm-console-assertion-v1","valid_from":"2026-01-01T00:00:00Z",` +
		`"valid_until":"2027-01-01T00:00:00Z","revoked_at":null,"revoke_reason":""}]`
	if _, err := LoadKeyringJSON([]byte(raw)); err == nil {
		t.Fatal("expected rejection of a duplicate object key")
	}
}

func TestLoadKeyringJSONRejectsTrailingData(t *testing.T) {
	if _, err := LoadKeyringJSON([]byte("[]\n{}")); err == nil {
		t.Fatal("expected rejection of trailing JSON after the array")
	}
}

func TestLoadKeyringJSONRejectsNonUTCTime(t *testing.T) {
	raw := strings.ReplaceAll(string(mustMarshal(t, validRecord(t, "2026-09"))), "Z\"", "+00:00\"")
	if _, err := LoadKeyringJSON([]byte(raw)); err == nil {
		t.Fatal("expected rejection of a non-Z UTC offset")
	}
}

// TestAlgorithmConstantsAreDistinctAndCorrect guards against the
// XM-INVCON-KEYRING-ALG incident recurring: Algorithm (the JWS header alg,
// signCompact's sole production caller) and KeyringAlgorithm (the manifest
// "algorithm" field, PublicKeyRecord's sole valid value) must never collapse
// back into one shared constant.
func TestAlgorithmConstantsAreDistinctAndCorrect(t *testing.T) {
	if Algorithm != "EdDSA" {
		t.Fatalf("Algorithm (JWS header alg) = %q, want EdDSA", Algorithm)
	}
	if KeyringAlgorithm != "Ed25519" {
		t.Fatalf("KeyringAlgorithm (manifest algorithm field) = %q, want Ed25519", KeyringAlgorithm)
	}
	if Algorithm == KeyringAlgorithm {
		t.Fatal("Algorithm and KeyringAlgorithm must remain distinct constants")
	}
}

// TestRealManifestRecordsUseKeyAlgorithmName loads the actual, committed
// contracts/auth/console-assertion-keyring.v1.json through this package's own
// loader and asserts every record's algorithm field equals KeyringAlgorithm
// ("Ed25519") -- the invoice-system consumer (backend/internal/auth/
// console_assertion.go, a different repository) rejects any other literal
// value for this field at its own startup. This is the regression test for
// the XM-INVCON-KEYRING-ALG incident: the committed manifest previously
// carried "algorithm":"EdDSA" (the JWS header's alg, wrongly reused here),
// which made the invoice-system api refuse to start once console-assertion
// redemption was enabled.
func TestRealManifestRecordsUseKeyAlgorithmName(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "auth", "console-assertion-keyring.v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	records, err := LoadKeyringJSON(raw)
	if err != nil {
		t.Fatalf("LoadKeyringJSON(%s): %v", path, err)
	}
	if len(records) == 0 {
		t.Skip("manifest is still the empty-array placeholder; nothing to check yet")
	}
	for _, r := range records {
		if r.Algorithm != KeyringAlgorithm {
			t.Fatalf("committed manifest record %q has algorithm %q, want %q (the key-algorithm name, not the JWS header alg)",
				r.KeyID, r.Algorithm, KeyringAlgorithm)
		}
	}
}

func mustMarshal(t *testing.T, record PublicKeyRecord) []byte {
	t.Helper()
	raw, err := MarshalManifestJSON([]PublicKeyRecord{record})
	if err != nil {
		t.Fatalf("MarshalManifestJSON: %v", err)
	}
	return raw
}
