package archive

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

func repeatedSeed(value byte) []byte {
	return bytes.Repeat([]byte{value}, ed25519.SeedSize)
}

func publicRecord(keyID string, seed []byte, purpose KeyPurpose, protocol string, validFrom, validUntil time.Time) TrustedKey {
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	fingerprint := sha256.Sum256(public)
	return TrustedKey{
		KeyID: keyID, Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(public),
		Fingerprint: hex.EncodeToString(fingerprint[:]), Purpose: purpose, Protocol: protocol,
		ValidFrom: validFrom, ValidUntil: validUntil,
	}
}

func TestVerifyRejectsWrongPurposeProtocolFingerprintAndExpiredKey(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	signedAt := time.Date(2026, 8, 28, 10, 6, 0, 0, time.UTC)
	base := publicRecord("manifest-key-1", repeatedSeed(0x22), PurposeManifest,
		ProtocolManifestV1, from, until)
	for name, mutate := range map[string]func(*TrustedKey){
		"purpose":     func(key *TrustedKey) { key.Purpose = PurposeChainRoot },
		"protocol":    func(key *TrustedKey) { key.Protocol = ProtocolCheckpointV1 },
		"fingerprint": func(key *TrustedKey) { key.Fingerprint = string(bytes.Repeat([]byte{'0'}, 64)) },
		"expired":     func(key *TrustedKey) { key.ValidUntil = signedAt },
	} {
		t.Run(name, func(t *testing.T) {
			key := base
			mutate(&key)
			keyring, err := NewStaticKeyring([]TrustedKey{key})
			if name == "fingerprint" {
				if err == nil {
					t.Fatal("mismatched public-key fingerprint was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := keyring.Lookup(base.KeyID, PurposeManifest, ProtocolManifestV1, signedAt); err == nil {
				t.Fatal("wrong trust metadata was accepted")
			}
		})
	}
}

func TestKeyringRejectsSameRawKeyAcrossPurposes(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	first := publicRecord("root-key", repeatedSeed(0x11), PurposeChainRoot, ProtocolChainRootV1, from, until)
	second := publicRecord("manifest-key", repeatedSeed(0x11), PurposeManifest, ProtocolManifestV1, from, until)
	if _, err := NewStaticKeyring([]TrustedKey{first, second}); err == nil {
		t.Fatal("the same raw public material was registered to two purposes")
	}

	rootSigner, err := NewEd25519PurposeSigner("root-key", repeatedSeed(0x11), PurposeChainRoot, ProtocolChainRootV1)
	if err != nil {
		t.Fatal(err)
	}
	manifestSigner, err := NewEd25519PurposeSigner("manifest-key", repeatedSeed(0x11), PurposeManifest, ProtocolManifestV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateUniqueSignerMaterial(rootSigner, manifestSigner); err == nil {
		t.Fatal("the same raw private/public material was accepted for two signing purposes")
	}
}

func TestTrustMaterialSeparationCrossChecksIndependentKeyringFiles(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	rootKeys, err := NewStaticKeyring([]TrustedKey{publicRecord(
		"root-key", repeatedSeed(0x11), PurposeChainRoot, ProtocolChainRootV1, from, until)})
	if err != nil {
		t.Fatal(err)
	}
	manifestKeys, err := NewStaticKeyring([]TrustedKey{publicRecord(
		"manifest-key", repeatedSeed(0x11), PurposeManifest, ProtocolManifestV1, from, until)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustMaterialSeparation(nil, rootKeys, manifestKeys); err == nil {
		t.Fatal("independent keyring files reused the same raw key across purposes")
	}

	manifestSigner, err := NewEd25519PurposeSigner("manifest-key", repeatedSeed(0x11),
		PurposeManifest, ProtocolManifestV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustMaterialSeparation([]PurposeSigner{manifestSigner}, rootKeys); err == nil {
		t.Fatal("manifest signer reused Chain Root trust material")
	}
}

func TestKeyValidityUsesSignedAtHalfOpenBoundary(t *testing.T) {
	from := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	until := from.Add(time.Hour)
	record := publicRecord("manifest-key", repeatedSeed(0x22), PurposeManifest, ProtocolManifestV1, from, until)
	keyring, err := NewStaticKeyring([]TrustedKey{record})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		at   time.Time
		want bool
	}{
		"before":     {from.Add(-time.Microsecond), false},
		"at-start":   {from, true},
		"before-end": {until.Add(-time.Microsecond), true},
		"at-end":     {until, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := keyring.Lookup(record.KeyID, record.Purpose, record.Protocol, test.at)
			if (err == nil) != test.want {
				t.Fatalf("Lookup at %s error=%v wantSuccess=%v", test.at, err, test.want)
			}
		})
	}
}

func TestManifestCheckpointIndexAndPLODomainReplayFails(t *testing.T) {
	private := ed25519.NewKeyFromSeed(repeatedSeed(0x22))
	public := private.Public().(ed25519.PublicKey)
	digest := string(bytes.Repeat([]byte{'a'}, 64))
	signature := ed25519.Sign(private, SignaturePayload(DomainManifestV1, digest))
	if !VerifyDetachedDomainSignature(DomainManifestV1, digest, signature, public) {
		t.Fatal("signature does not verify in its own domain")
	}
	for _, domain := range []string{
		DomainCheckpointV1, DomainRecoveryIndexV1, DomainPLOApprovalV1, DomainPLOResultV1,
		DomainKillSwitchV1, DomainScrubReceiptV1, DomainScrubHeadV1,
	} {
		if VerifyDetachedDomainSignature(domain, digest, signature, public) {
			t.Fatalf("manifest signature replayed in %q", domain)
		}
	}
}

func TestVerifyRejectsRevokedKeyForNewSignature(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	revoked := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	record := publicRecord("manifest-key", repeatedSeed(0x22), PurposeManifest, ProtocolManifestV1, from, until)
	record.RevokedAt = &revoked
	record.RevokeReason = "rotation"
	keyring, err := NewStaticKeyring([]TrustedKey{record})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Lookup(record.KeyID, record.Purpose, record.Protocol, revoked.Add(-time.Microsecond)); err != nil {
		t.Fatalf("pre-revocation historical signature was rejected: %v", err)
	}
	if _, err := keyring.Lookup(record.KeyID, record.Purpose, record.Protocol, revoked); err == nil {
		t.Fatal("signature at revocation instant was accepted")
	}
}

func TestKeyringGoldenFilesContainOnlyPublicTrustRecords(t *testing.T) {
	for _, name := range []string{"root-keyring.json", "manifest-keyring.json"} {
		raw, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		keyring, err := LoadStaticKeyringJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		if keyring.Len() != 1 {
			t.Fatalf("%s records = %d", name, keyring.Len())
		}
		if bytes.Contains(bytes.ToLower(raw), []byte("private")) || bytes.Contains(bytes.ToLower(raw), []byte("seed")) {
			t.Fatalf("%s contains private-key-shaped fields", name)
		}
	}
}

func TestKeyringRejectsDuplicateFieldsInsteadOfTakingLastValue(t *testing.T) {
	raw, err := os.ReadFile("testdata/manifest-keyring.json")
	if err != nil {
		t.Fatal(err)
	}
	duplicated := []byte(strings.Replace(string(raw),
		`"key_id":"manifest-key-1"`,
		`"key_id":"manifest-key-1","key_id":"manifest-key-1"`, 1))
	if _, err := LoadStaticKeyringJSON(duplicated); err == nil {
		t.Fatal("duplicate trusted-key field was silently resolved by last-value-wins")
	}
}
