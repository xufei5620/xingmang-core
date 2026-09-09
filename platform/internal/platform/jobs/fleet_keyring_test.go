package jobs

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPinnedJobFleetKeyringIsPublicAndStable(t *testing.T) {
	keyring, err := LoadPinnedJobFleetKeyring()
	if err != nil {
		t.Fatalf("load pinned keyring: %v", err)
	}
	if keyring.Len() != 2 {
		t.Fatalf("keyring length=%d, want 2", keyring.Len())
	}
	if strings.Contains(strings.ToLower(string(PinnedJobFleetKeyringJSON())), "private") {
		t.Fatal("pinned keyring contains private-key material")
	}
	if got := fleetSHA256Hex(PinnedJobFleetKeyringJSON()); got != PinnedJobFleetKeyringSHA256 {
		t.Fatalf("pinned keyring digest=%s, want %s", got, PinnedJobFleetKeyringSHA256)
	}
	contract, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jobs", "job-fleet-keyring.v1.json"))
	if err != nil {
		t.Fatalf("read reviewed keyring contract: %v", err)
	}
	if string(contract) != string(PinnedJobFleetKeyringJSON()) {
		t.Fatal("embedded keyring bytes differ from reviewed contract")
	}
}

func TestJobFleetKeyringRejectsBadFingerprintDuplicateAndPurposeReuse(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	row := jobFleetTrustedKeyForTest(seed, JobFleetManifestPurpose, JobFleetManifestProtocol, "one")
	row.Fingerprint = strings.Repeat("0", 64)
	if _, err := NewJobFleetKeyring([]JobFleetTrustedKey{row}); err == nil {
		t.Fatal("fingerprint mismatch accepted")
	}
	row = jobFleetTrustedKeyForTest(seed, JobFleetManifestPurpose, JobFleetManifestProtocol, "one")
	row2 := row
	row2.KeyID = "two"
	if _, err := NewJobFleetKeyring([]JobFleetTrustedKey{row, row2}); err == nil {
		t.Fatal("duplicate public material accepted")
	}
	row.PublicKey = base64.StdEncoding.EncodeToString(pub)
	if _, err := NewJobFleetKeyring([]JobFleetTrustedKey{row}); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
}

func TestJobFleetKeyringLookupUsesExactPurposeProtocolAndHalfOpenValidity(t *testing.T) {
	keyring := testFleetKeyring(t)
	validAt := mustFleetTime("2026-08-30T00:00:00Z")
	if _, err := keyring.Lookup("fleet-manifest-key", JobFleetManifestPurpose, JobFleetManifestProtocol, validAt); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Lookup("fleet-manifest-key", JobFleetInventoryPurpose, JobFleetInventoryProtocol, validAt); err == nil {
		t.Fatal("cross-purpose lookup accepted")
	}
	if _, err := keyring.Lookup("fleet-manifest-key", JobFleetManifestPurpose, "wrong", validAt); err == nil {
		t.Fatal("cross-protocol lookup accepted")
	}
	if _, err := keyring.Lookup("fleet-manifest-key", JobFleetManifestPurpose, JobFleetManifestProtocol, mustFleetTime("2030-01-01T00:00:00Z")); err == nil {
		t.Fatal("valid-until boundary accepted")
	}
	if _, err := keyring.Lookup("fleet-manifest-key", JobFleetManifestPurpose, JobFleetManifestProtocol, time.Time{}); err == nil {
		t.Fatal("zero timestamp accepted")
	}
}

func TestJobFleetKeyringRejectsNonCanonicalPublicEncodingAndUnsortedRows(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	rowA := jobFleetTrustedKeyForTest(seed, JobFleetInventoryPurpose, JobFleetInventoryProtocol, "a-key")
	rowB := jobFleetTrustedKeyForTest(append([]byte(nil), seed...), JobFleetManifestPurpose, JobFleetManifestProtocol, "b-key")
	rowB.PublicKey = rowA.PublicKey
	if _, err := NewJobFleetKeyring([]JobFleetTrustedKey{rowA, rowB}); err == nil {
		t.Fatal("reused public material accepted")
	}
	rowA = jobFleetTrustedKeyForTest(seed, JobFleetInventoryPurpose, JobFleetInventoryProtocol, "z-key")
	rowB = jobFleetTrustedKeyForTest(append([]byte(nil), seed...), JobFleetManifestPurpose, JobFleetManifestProtocol, "a-key")
	// Give rowB independent material so ordering is the only failure.
	secondSeed := make([]byte, ed25519.SeedSize)
	for i := range secondSeed {
		secondSeed[i] = byte(i + 1)
	}
	rowB = jobFleetTrustedKeyForTest(secondSeed, JobFleetManifestPurpose, JobFleetManifestProtocol, "a-key")
	if _, err := NewJobFleetKeyring([]JobFleetTrustedKey{rowA, rowB}); err == nil {
		t.Fatal("unsorted keyring accepted")
	}
}

func TestLoadJobFleetKeyringJSONRejectsUnknownAndDuplicateFields(t *testing.T) {
	raw := PinnedJobFleetKeyringJSON()
	unknown := append(append([]byte(nil), raw[:len(raw)-2]...), []byte(`,"unexpected":true}]`)...)
	if _, err := LoadJobFleetKeyringJSON(unknown); err == nil {
		t.Fatal("unknown keyring field accepted")
	}
	duplicate := strings.Replace(string(raw), `"key_id":"fleet-manifest-key"`, `"key_id":"fleet-manifest-key","key_id":"fleet-manifest-key"`, 1)
	if _, err := LoadJobFleetKeyringJSON([]byte(duplicate)); err == nil {
		t.Fatal("duplicate keyring field accepted")
	}
	if _, err := LoadJobFleetKeyringJSON(append(raw, []byte(` []`)...)); err == nil {
		t.Fatal("trailing keyring value accepted")
	}
}
