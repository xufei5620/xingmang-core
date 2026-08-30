package jobs

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSignedJobFleetInventoryVerifiesAndBindsManifest(t *testing.T) {
	manifest := testFleetManifest()
	inventory := testFleetInventory()
	inventoryDigest, err := JobFleetInventorySHA256(inventory)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ActiveBindingInventorySHA256 = inventoryDigest
	manifestSigned := signFleetManifestForTest(t, manifest)
	inventorySigned := signFleetInventoryForTest(t, inventory)
	const goldenInventorySignature = "7dD3Sp2HTzUKpDxzpPsgsnFLD8+YFg7G/HGbd9cFBKZOSOI2GZu/8o0Mh8D889QrRaW+ydR5irC4+a/ybDrxDQ=="
	if inventorySigned.Signature != goldenInventorySignature {
		t.Fatal("inventory signature changed")
	}
	if err := VerifySignedJobFleetInventory(inventorySigned, testFleetKeyring(t), mustFleetTime(fleetTestTime)); err != nil {
		t.Fatalf("verify inventory: %v", err)
	}
	if err := VerifyJobFleetManifestInventoryPair(manifestSigned, inventorySigned, testFleetKeyring(t), mustFleetTime(fleetTestTime)); err != nil {
		t.Fatalf("verify pair: %v", err)
	}
	canonical, err := CanonicalJobFleetInventoryBytes(inventory)
	if err != nil {
		t.Fatal(err)
	}
	const goldenInventory = `{"kind":"xingmang-job-fleet-inventory","version":1,"epoch":7,"bindings":[{"database_binding_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environment":"staging","worker_cluster_id":"cluster-a","status":"active"}],"issued_at":"2026-08-30T00:00:00Z","valid_from":"2026-08-30T00:00:00Z","valid_until":"2026-09-06T00:00:00Z","change_ref":"change-7","nonce":"inventory-nonce-7"}`
	if string(canonical) != goldenInventory {
		t.Fatalf("canonical inventory bytes changed:\n%s", canonical)
	}
	const goldenDigest = "680b8f8c9a91aa67cc32ec19a2d7ace62a6f284ea412b606087b73a346fa68cb"
	if got := fleetSHA256Hex(canonical); got != goldenDigest {
		t.Fatalf("canonical inventory digest=%s, want %s", got, goldenDigest)
	}
}

func TestJobFleetInventoryRejectsDuplicateActiveAndCrossClusterBinding(t *testing.T) {
	base := testFleetInventory()
	base.Bindings = append(base.Bindings, JobFleetBindingV1{
		DatabaseBindingHash: strings.Repeat("a", 64), Environment: "production", WorkerClusterID: "cluster-b", Status: JobFleetBindingActive,
	})
	if err := ValidateJobFleetInventory(base); err == nil {
		t.Fatal("duplicate active binding accepted")
	}
	base = testFleetInventory()
	base.Bindings[0].Status = JobFleetBindingClosed
	if err := ValidateJobFleetInventory(base); err != nil {
		t.Fatalf("closed binding should be valid: %v", err)
	}
}

func TestJobFleetInventoryRequiresCanonicalBindingOrderAndRejectsDigestMismatch(t *testing.T) {
	base := testFleetInventory()
	base.Bindings = append(base.Bindings,
		JobFleetBindingV1{DatabaseBindingHash: strings.Repeat("d", 64), Environment: "staging", WorkerClusterID: "cluster-d", Status: JobFleetBindingClosed},
	)
	if err := ValidateJobFleetInventory(base); err != nil {
		t.Fatal(err)
	}
	base.Bindings[0], base.Bindings[1] = base.Bindings[1], base.Bindings[0]
	if err := ValidateJobFleetInventory(base); err == nil {
		t.Fatal("non-canonical binding order accepted")
	}
	manifest := testFleetManifest()
	manifest.ActiveBindingInventorySHA256 = strings.Repeat("e", 64)
	manifestSigned := signFleetManifestForTest(t, manifest)
	inventorySigned := signFleetInventoryForTest(t, testFleetInventory())
	if err := VerifyJobFleetManifestInventoryPair(manifestSigned, inventorySigned, testFleetKeyring(t), mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("inventory digest mismatch accepted")
	}
}

func TestJobFleetEpochAndNonceReplayChecks(t *testing.T) {
	if err := ValidateFleetEpoch(3, 2); err != nil {
		t.Fatal(err)
	}
	for _, current := range []int64{0, 2, -1} {
		if err := ValidateFleetEpoch(current, 2); err == nil {
			t.Fatalf("epoch %d accepted", current)
		}
	}
	if err := ValidateFleetNonce("new", "old"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFleetNonce("same", "same"); err == nil {
		t.Fatal("replayed nonce accepted")
	}
	if err := ValidateFleetNonceHistory("new", []string{"old", "older"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFleetNonceHistory("old", []string{"old", "older"}); err == nil {
		t.Fatal("historical nonce replay accepted")
	}
	if err := ValidateFleetEpochNonce(3, "new", 2, []string{"old"}); err != nil {
		t.Fatal(err)
	}
}

func TestSignedFleetPairWithHistoryRejectsLowerEpochAndReplayedNonce(t *testing.T) {
	manifest := testFleetManifest()
	inventory := testFleetInventory()
	digest, err := JobFleetInventorySHA256(inventory)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ActiveBindingInventorySHA256 = digest
	ms := signFleetManifestForTest(t, manifest)
	is := signFleetInventoryForTest(t, inventory)
	keyring := testFleetKeyring(t)
	if err := VerifyJobFleetManifestInventoryPairWithHistory(ms, is, keyring, mustFleetTime(fleetTestTime), 6, 6, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyJobFleetManifestInventoryPairWithHistory(ms, is, keyring, mustFleetTime(fleetTestTime), 7, 6, nil); err == nil {
		t.Fatal("lower manifest epoch accepted")
	}
	if err := VerifyJobFleetManifestInventoryPairWithHistory(ms, is, keyring, mustFleetTime(fleetTestTime), 6, 6, []string{manifest.Nonce}); err == nil {
		t.Fatal("historical manifest nonce accepted")
	}
}

func TestJobFleetInventoryTransitionIsAppendOnly(t *testing.T) {
	previous := testFleetInventory()
	current := previous
	current.Epoch = previous.Epoch + 1
	current.Nonce = "inventory-nonce-8"
	current.Bindings = append([]JobFleetBindingV1(nil), previous.Bindings...)
	current.Bindings = append(current.Bindings, JobFleetBindingV1{
		DatabaseBindingHash: strings.Repeat("d", 64), Environment: "staging", WorkerClusterID: "cluster-a", Status: JobFleetBindingClosed,
	})
	if err := ValidateJobFleetInventoryTransition(previous, current); err != nil {
		t.Fatalf("append-only transition rejected: %v", err)
	}
	mutated := current
	mutated.Bindings = append([]JobFleetBindingV1(nil), current.Bindings...)
	mutated.Bindings[0].WorkerClusterID = "cluster-b"
	if err := ValidateJobFleetInventoryTransition(previous, mutated); err == nil {
		t.Fatal("history mutation accepted")
	}
	removed := previous
	removed.Epoch = previous.Epoch + 1
	removed.Nonce = "inventory-nonce-8"
	removed.Bindings = nil
	if err := ValidateJobFleetInventoryTransition(previous, removed); err == nil {
		t.Fatal("history removal accepted")
	}
}

func TestJobFleetManifestInventoryPairRejectsNonceAndBindingScopeMismatch(t *testing.T) {
	manifest := testFleetManifest()
	inventory := testFleetInventory()
	digest, err := JobFleetInventorySHA256(inventory)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ActiveBindingInventorySHA256 = digest
	manifest.Nonce = inventory.Nonce
	if err := VerifyJobFleetManifestInventoryPair(signFleetManifestForTest(t, manifest), signFleetInventoryForTest(t, inventory), testFleetKeyring(t), mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("same manifest/inventory nonce accepted")
	}
	manifest = testFleetManifest()
	inventory = testFleetInventory()
	inventory.Bindings[0].WorkerClusterID = "cluster-other"
	digest, err = JobFleetInventorySHA256(inventory)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ActiveBindingInventorySHA256 = digest
	if err := VerifyJobFleetManifestInventoryPair(signFleetManifestForTest(t, manifest), signFleetInventoryForTest(t, inventory), testFleetKeyring(t), mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("binding from another cluster accepted")
	}
}

func TestJobFleetManifestInventoryPairRejectsValidityOutsideInventory(t *testing.T) {
	manifest := testFleetManifest()
	inventory := testFleetInventory()
	inventory.ValidUntil = "2026-09-14T00:00:00Z"
	digest, err := JobFleetInventorySHA256(inventory)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ActiveBindingInventorySHA256 = digest
	manifest.ValidFrom = "2026-09-15T00:00:00Z"
	manifest.ValidUntil = "2026-09-22T00:00:00Z"
	manifest.IssuedAt = "2026-09-15T00:00:00Z"
	if err := VerifyJobFleetManifestInventoryPair(signFleetManifestForTest(t, manifest), signFleetInventoryForTest(t, inventory), testFleetKeyring(t), mustFleetTime("2026-09-15T00:00:00Z")); err == nil {
		t.Fatal("non-overlapping validity accepted")
	}
}

func TestSignedJobFleetInventoryRejectsWrongKeyAndTampering(t *testing.T) {
	value := signFleetInventoryForTest(t, testFleetInventory())
	value.SignatureKeyID = "fleet-manifest-key"
	if err := VerifySignedJobFleetInventory(value, testFleetKeyring(t), mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("wrong purpose key accepted")
	}
	value = signFleetInventoryForTest(t, testFleetInventory())
	value.UnsignedSHA256 = strings.Repeat("f", 64)
	if err := VerifySignedJobFleetInventory(value, testFleetKeyring(t), mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("digest substitution accepted")
	}
	value = signFleetInventoryForTest(t, testFleetInventory())
	value.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
	if err := VerifySignedJobFleetInventory(value, testFleetKeyring(t), mustFleetTime(fleetTestTime)); err == nil {
		t.Fatal("zero signature accepted")
	}
}

func TestSignedJobFleetInventoryRejectsNonCanonicalEnvelopeBytes(t *testing.T) {
	value := signFleetInventoryForTest(t, testFleetInventory())
	raw, err := EncodeSignedJobFleetInventory(value)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string][]byte{
		"leading whitespace": append([]byte("\n"), raw...),
		"reordered fields":   []byte(strings.Replace(string(raw), `{"kind":"xingmang-job-fleet-inventory","version":1`, `{"version":1,"kind":"xingmang-job-fleet-inventory"`, 1)),
		"escaped value":      []byte(strings.Replace(string(raw), `"staging"`, `"\u0073taging"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSignedJobFleetInventory(candidate); err == nil {
				t.Fatal("non-canonical envelope accepted")
			}
		})
	}
}
