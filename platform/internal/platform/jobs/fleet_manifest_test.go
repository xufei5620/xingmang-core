package jobs

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const fleetTestTime = "2026-08-30T00:00:00Z"

func testFleetManifest() JobFleetManifestV1 {
	return JobFleetManifestV1{
		Kind:                  JobFleetManifestKind,
		Version:               1,
		Environment:           "staging",
		WorkerClusterID:       "cluster-a",
		RiverSchema:           "river",
		DatabaseBindingHash:   strings.Repeat("a", 64),
		Epoch:                 7,
		JobContractVersion:    1,
		EffectiveManifestHash: strings.Repeat("b", 64),
		Replicas: []JobFleetReplicaV1{
			{LogicalReplicaID: "worker-0", BuildDigest: "sha256:" + strings.Repeat("1", 64)},
			{LogicalReplicaID: "worker-1", BuildDigest: "sha256:" + strings.Repeat("2", 64)},
		},
		BuildCapabilities: []JobFleetBuildCapabilityV1{
			{BuildDigest: "sha256:" + strings.Repeat("1", 64), RiverVersion: "0.45.0", RiverMigrationVersion: "20260801", JobContractVersion: 1, EffectiveManifestHash: strings.Repeat("b", 64)},
			{BuildDigest: "sha256:" + strings.Repeat("2", 64), RiverVersion: "0.45.0", RiverMigrationVersion: "20260801", JobContractVersion: 1, EffectiveManifestHash: strings.Repeat("b", 64)},
		},
		ActiveBindingInventoryEpoch:  7,
		ActiveBindingInventorySHA256: strings.Repeat("c", 64),
		IssuedAt:                     fleetTestTime,
		ValidFrom:                    fleetTestTime,
		ValidUntil:                   "2026-09-06T00:00:00Z",
		ChangeRef:                    "change-7",
		Nonce:                        "nonce-7",
	}
}

func testFleetInventory() JobFleetInventoryV1 {
	return JobFleetInventoryV1{
		Kind:    JobFleetInventoryKind,
		Version: 1,
		Epoch:   7,
		Bindings: []JobFleetBindingV1{
			{DatabaseBindingHash: strings.Repeat("a", 64), Environment: "staging", WorkerClusterID: "cluster-a", Status: JobFleetBindingActive},
		},
		IssuedAt:   fleetTestTime,
		ValidFrom:  fleetTestTime,
		ValidUntil: "2026-09-06T00:00:00Z",
		ChangeRef:  "change-7",
		Nonce:      "inventory-nonce-7",
	}
}

func testFleetKeyring(t *testing.T) *JobFleetKeyring {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	secondSeed := make([]byte, ed25519.SeedSize)
	for i := range secondSeed {
		secondSeed[i] = byte(i + 33)
	}
	rows := []JobFleetTrustedKey{
		jobFleetTrustedKeyForTest(secondSeed, JobFleetInventoryPurpose, JobFleetInventoryProtocol, "fleet-inventory-key"),
		jobFleetTrustedKeyForTest(seed, JobFleetManifestPurpose, JobFleetManifestProtocol, "fleet-manifest-key"),
	}
	keyring, err := NewJobFleetKeyring(rows)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}
	return keyring
}

func jobFleetTrustedKeyForTest(seed []byte, purpose, protocol, id string) JobFleetTrustedKey {
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return JobFleetTrustedKey{
		KeyID: id, Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(pub),
		Fingerprint: fleetSHA256Hex(pub), Purpose: purpose, Protocol: protocol,
		ValidFrom: mustFleetTime("2026-01-01T00:00:00Z"), ValidUntil: mustFleetTime("2030-01-01T00:00:00Z"),
	}
}

func mustFleetTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func signFleetManifestForTest(t *testing.T, value JobFleetManifestV1) SignedJobFleetManifestV1 {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	canonical, err := CanonicalJobFleetManifestBytes(value)
	if err != nil {
		t.Fatalf("canonical manifest: %v", err)
	}
	digest := fleetSHA256Hex(canonical)
	key := ed25519.NewKeyFromSeed(seed)
	return SignedJobFleetManifestV1{
		JobFleetManifestV1: value,
		UnsignedSHA256:     digest,
		SignatureAlgorithm: FleetSignatureAlgorithm,
		SignatureKeyID:     "fleet-manifest-key",
		Signature:          base64.StdEncoding.EncodeToString(ed25519.Sign(key, fleetSignaturePayload(JobFleetManifestProtocol, digest))),
	}
}

func signFleetInventoryForTest(t *testing.T, value JobFleetInventoryV1) SignedJobFleetInventoryV1 {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 33)
	}
	canonical, err := CanonicalJobFleetInventoryBytes(value)
	if err != nil {
		t.Fatalf("canonical inventory: %v", err)
	}
	digest := fleetSHA256Hex(canonical)
	key := ed25519.NewKeyFromSeed(seed)
	return SignedJobFleetInventoryV1{
		JobFleetInventoryV1: value,
		UnsignedSHA256:      digest,
		SignatureAlgorithm:  FleetSignatureAlgorithm,
		SignatureKeyID:      "fleet-inventory-key",
		Signature:           base64.StdEncoding.EncodeToString(ed25519.Sign(key, fleetSignaturePayload(JobFleetInventoryProtocol, digest))),
	}
}

func TestSignedJobFleetManifestCanonicalAndSignature(t *testing.T) {
	manifest := testFleetManifest()
	value := signFleetManifestForTest(t, manifest)
	if err := VerifySignedJobFleetManifest(value, testFleetKeyring(t), mustFleetTime(fleetTestTime)); err != nil {
		t.Fatalf("verify signed manifest: %v", err)
	}
	const goldenManifestSignature = "flghORquChrJhAkjbbHLrbdQyOs0wSH8UjKZwLqnYJ1p7JwQZWFypGwZy46ZqKkAqvvnmdObzg5z9fEOET/eDw=="
	if value.Signature != goldenManifestSignature {
		t.Fatal("manifest signature changed")
	}
	raw, err := EncodeSignedJobFleetManifest(value)
	if err != nil {
		t.Fatalf("encode signed manifest: %v", err)
	}
	decoded, err := DecodeSignedJobFleetManifest(raw)
	if err != nil {
		t.Fatalf("decode signed manifest: %v", err)
	}
	if !bytes.Equal(raw, mustEncodeSignedManifest(t, decoded)) {
		t.Fatal("signed manifest encoding is not stable")
	}
	const goldenManifest = `{"kind":"xingmang-job-fleet-manifest","version":1,"environment":"staging","worker_cluster_id":"cluster-a","river_schema":"river","database_binding_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","epoch":7,"job_contract_version":1,"effective_manifest_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","replicas":[{"logical_replica_id":"worker-0","build_digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"},{"logical_replica_id":"worker-1","build_digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222"}],"build_capabilities":[{"build_digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","river_version":"0.45.0","river_migration_version":"20260801","job_contract_version":1,"effective_manifest_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},{"build_digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","river_version":"0.45.0","river_migration_version":"20260801","job_contract_version":1,"effective_manifest_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}],"active_binding_inventory_epoch":7,"active_binding_inventory_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","issued_at":"2026-08-30T00:00:00Z","valid_from":"2026-08-30T00:00:00Z","valid_until":"2026-09-06T00:00:00Z","change_ref":"change-7","nonce":"nonce-7"}`
	canonical := mustCanonicalManifest(t, manifest)
	if string(canonical) != goldenManifest {
		t.Fatalf("canonical manifest bytes changed:\n%s", canonical)
	}
	const goldenDigest = "a989243f5ceacca04f05060d5d34adb5125a370c237dd5b5552b98bc2009d597"
	if got := fleetSHA256Hex(canonical); got != goldenDigest {
		t.Fatalf("canonical manifest digest=%s, want %s", got, goldenDigest)
	}
}

func TestSignedJobFleetManifestRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	value := signFleetManifestForTest(t, testFleetManifest())
	raw, err := EncodeSignedJobFleetManifest(value)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unexpected":true}`)...)
	if _, err := DecodeSignedJobFleetManifest(unknown); err == nil {
		t.Fatal("unknown field accepted")
	}
	duplicate := bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
	if _, err := DecodeSignedJobFleetManifest(duplicate); err == nil {
		t.Fatal("duplicate field accepted")
	}
	if _, err := DecodeSignedJobFleetManifest(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := DecodeSignedJobFleetManifest(append([]byte(" "), raw...)); err == nil {
		t.Fatal("leading whitespace accepted")
	}
	reordered := strings.Replace(string(raw), `{"kind":"xingmang-job-fleet-manifest","version":1`, `{"version":1,"kind":"xingmang-job-fleet-manifest"`, 1)
	if _, err := DecodeSignedJobFleetManifest([]byte(reordered)); err == nil {
		t.Fatal("reordered JSON accepted")
	}
	escaped := strings.Replace(string(raw), `"staging"`, `"\u0073taging"`, 1)
	if _, err := DecodeSignedJobFleetManifest([]byte(escaped)); err == nil {
		t.Fatal("escaped non-canonical JSON accepted")
	}
}

func TestJobFleetManifestValidationRejectsSecurityBoundaryViolations(t *testing.T) {
	base := testFleetManifest()
	cases := map[string]func(*JobFleetManifestV1){
		"unicode cluster":  func(v *JobFleetManifestV1) { v.WorkerClusterID = "集群" },
		"path separator":   func(v *JobFleetManifestV1) { v.Environment = "staging/prod" },
		"bad binding hash": func(v *JobFleetManifestV1) { v.DatabaseBindingHash = strings.Repeat("A", 64) },
		"zero epoch":       func(v *JobFleetManifestV1) { v.Epoch = 0 },
		"duplicate replica": func(v *JobFleetManifestV1) {
			v.Replicas[1].LogicalReplicaID = v.Replicas[0].LogicalReplicaID
		},
		"unknown build":          func(v *JobFleetManifestV1) { v.Replicas[0].BuildDigest = "sha256:" + strings.Repeat("3", 64) },
		"effective mismatch":     func(v *JobFleetManifestV1) { v.BuildCapabilities[0].EffectiveManifestHash = strings.Repeat("d", 64) },
		"overlong validity":      func(v *JobFleetManifestV1) { v.ValidUntil = "2031-01-01T00:00:00Z" },
		"uppercase build digest": func(v *JobFleetManifestV1) { v.Replicas[0].BuildDigest = "sha256:" + strings.Repeat("A", 64) },
		"missing replicas":       func(v *JobFleetManifestV1) { v.Replicas = nil },
		"missing build capability": func(v *JobFleetManifestV1) {
			v.BuildCapabilities = v.BuildCapabilities[:1]
		},
		"orphan build capability": func(v *JobFleetManifestV1) {
			v.BuildCapabilities = append(v.BuildCapabilities, JobFleetBuildCapabilityV1{
				BuildDigest: "sha256:" + strings.Repeat("3", 64), RiverVersion: "0.45.0", RiverMigrationVersion: "20260801",
				JobContractVersion: v.JobContractVersion, EffectiveManifestHash: v.EffectiveManifestHash,
			})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Replicas = append([]JobFleetReplicaV1(nil), base.Replicas...)
			value.BuildCapabilities = append([]JobFleetBuildCapabilityV1(nil), base.BuildCapabilities...)
			mutate(&value)
			if err := ValidateJobFleetManifest(value); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestSignedJobFleetManifestRejectsBadPurposeKeyAndValidity(t *testing.T) {
	value := signFleetManifestForTest(t, testFleetManifest())
	keyring := testFleetKeyring(t)
	if err := VerifySignedJobFleetManifest(value, keyring, mustFleetTime("2026-08-30T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	value.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := VerifySignedJobFleetManifest(value, keyring, mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("bad signature accepted")
	}
	value = signFleetManifestForTest(t, testFleetManifest())
	value.SignatureAlgorithm = "RSA"
	if err := VerifySignedJobFleetManifest(value, keyring, mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("wrong signature algorithm accepted")
	}
	value = signFleetManifestForTest(t, testFleetManifest())
	if err := VerifySignedJobFleetManifest(value, keyring, mustFleetTime("2026-09-06T00:00:00Z")); err == nil {
		t.Fatal("valid-until boundary accepted")
	}
	future := testFleetManifest()
	future.IssuedAt = "2026-08-30T00:00:00Z"
	future.ValidFrom = "2026-09-01T00:00:00Z"
	future.ValidUntil = "2026-09-08T00:00:00Z"
	value = signFleetManifestForTest(t, future)
	if err := VerifySignedJobFleetManifest(value, keyring, mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("not-yet-valid manifest accepted")
	}
}

func mustCanonicalManifest(t *testing.T, value JobFleetManifestV1) []byte {
	t.Helper()
	raw, err := CanonicalJobFleetManifestBytes(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustEncodeSignedManifest(t *testing.T, value SignedJobFleetManifestV1) []byte {
	t.Helper()
	raw, err := EncodeSignedJobFleetManifest(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Keep encoding/json imported in the RED test so the strict-decoder fixture
// can be expanded without weakening the duplicate-key assertion.
var _ = json.Valid
