package archive

import (
	"context"
	"os"
	"strings"
	"testing"
)

func goldenRecoveryIndex(t *testing.T) SignedRecoveryIndexV1 {
	t.Helper()
	raw, err := os.ReadFile("testdata/recovery-index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeSignedRecoveryIndexV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testRecoveryIndex(t *testing.T, generation int64) SignedRecoveryIndexV1 {
	t.Helper()
	value := goldenRecoveryIndex(t)
	value.Unsigned.Generation = generation
	if generation == 1 {
		value.Unsigned.PreviousGeneration = 0
		value.Unsigned.PreviousIndexSHA256 = RecoveryGenesisSHA256
	}
	return value
}

func TestAUD2RecoveryIndexCASUsesFixedLocatorAndExpectedGeneration(t *testing.T) {
	index := NewMemoryRecoveryIndex(FixedLocator{ApprovedConfigRef: "secret://archive/index"})
	locator := FixedLocator{ApprovedConfigRef: "secret://archive/index"}
	first := testRecoveryIndex(t, 1)
	first.UnsignedSHA256 = ""
	first.Signature = ""
	// The fixture is signed; changing generation invalidates its signature, so
	// this test exercises the fixed-locator guard before signature verification.
	if _, err := index.CompareAndSwap(context.Background(), locator, ExpectedIndex{}, first); err == nil {
		t.Fatal("unsigned/tampered recovery index unexpectedly accepted")
	}
	if _, _, err := index.LoadCurrent(context.Background(), FixedLocator{ApprovedConfigRef: "secret://archive/other"}); err == nil {
		t.Fatal("wrong fixed locator unexpectedly accepted")
	}
}

func TestAUD2RecoveryIndexAcceptsSignedInitialCASAndRoundTrips(t *testing.T) {
	locator := FixedLocator{ApprovedConfigRef: "secret://archive/index"}
	index := NewMemoryRecoveryIndex(locator)
	first := goldenRecoveryIndex(t)
	version, err := index.CompareAndSwap(context.Background(), locator, ExpectedIndex{}, first)
	if err != nil {
		t.Fatalf("initial signed index CAS rejected: %v", err)
	}
	if version.Generation != 1 || version.SHA256 == "" || version.ProviderVersion == "" {
		t.Fatalf("unexpected index version: %+v", version)
	}
	got, gotVersion, err := index.LoadCurrent(context.Background(), locator)
	if err != nil {
		t.Fatal(err)
	}
	if got != first || gotVersion != version {
		t.Fatalf("round-trip mismatch: got=%+v/%+v want=%+v/%+v", got, gotVersion, first, version)
	}
}

func TestAUD2RecoveryIndexRejectsStaleCAS(t *testing.T) {
	index := NewMemoryRecoveryIndex(FixedLocator{ApprovedConfigRef: "secret://archive/index"})
	if _, err := index.CompareAndSwap(context.Background(), FixedLocator{ApprovedConfigRef: "secret://archive/index"}, ExpectedIndex{Generation: 99}, testRecoveryIndex(t, 1)); err == nil {
		t.Fatal("stale expected generation unexpectedly accepted")
	}
}

func TestAUD2FixedLocatorRejectsPathAndCredentialMaterial(t *testing.T) {
	for _, ref := range []string{"", "../index", "secret://archive/index\nsecret"} {
		if err := ValidateFixedLocator(FixedLocator{ApprovedConfigRef: ref}); err == nil {
			t.Fatalf("unsafe fixed locator %q unexpectedly accepted", strings.ReplaceAll(ref, "\n", "\\n"))
		}
	}
}
