package archive

import (
	"context"
	"fmt"
	"sync"
)

// ValidateRecoveryIndexBinding validates the frozen wire and its exact signed
// bytes. Signature trust (purpose/key validity) remains the caller's Keyring job.
func ValidateRecoveryIndexBinding(value SignedRecoveryIndexV1) error {
	if err := validateRecoveryIndex(value.Unsigned); err != nil {
		return err
	}
	if value.UnsignedSHA256 == "" || value.SignatureAlgorithm == "" || value.SignatureKeyID == "" || value.Signature == "" {
		return fmt.Errorf("%w: recovery index signature envelope", ErrArchiveValidation)
	}
	if _, err := EncodeSignedRecoveryIndexV1(value); err != nil {
		return err
	}
	return nil
}

func SignedRecoveryIndexDigest(value SignedRecoveryIndexV1) (string, error) {
	encoded, err := EncodeSignedRecoveryIndexV1(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// MemoryRecoveryIndex models the fixed-locator CAS protocol for tests. It does
// not persist data and must never be used as a production RecoveryIndex store.
type MemoryRecoveryIndex struct {
	mu       sync.RWMutex
	locator  FixedLocator
	current  SignedRecoveryIndexV1
	version  IndexVersion
	provided bool
}

func NewMemoryRecoveryIndex(locator FixedLocator) *MemoryRecoveryIndex {
	return &MemoryRecoveryIndex{locator: locator}
}

func sameLocator(left, right FixedLocator) bool {
	return left.ApprovedConfigRef == right.ApprovedConfigRef
}

func (m *MemoryRecoveryIndex) LoadCurrent(ctx context.Context, locator FixedLocator) (SignedRecoveryIndexV1, IndexVersion, error) {
	if err := contextErr(ctx); err != nil {
		return SignedRecoveryIndexV1{}, IndexVersion{}, err
	}
	if m == nil {
		return SignedRecoveryIndexV1{}, IndexVersion{}, fmt.Errorf("%w: nil recovery index", ErrRecoveryConflict)
	}
	if err := ValidateFixedLocator(locator); err != nil {
		return SignedRecoveryIndexV1{}, IndexVersion{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !sameLocator(locator, m.locator) {
		return SignedRecoveryIndexV1{}, IndexVersion{}, fmt.Errorf("%w: fixed locator mismatch", ErrRecoveryConflict)
	}
	if !m.provided {
		return SignedRecoveryIndexV1{}, IndexVersion{}, ErrObjectNotFound
	}
	return cloneSignedRecoveryIndex(m.current), m.version, nil
}

func (m *MemoryRecoveryIndex) CompareAndSwap(ctx context.Context, locator FixedLocator, expected ExpectedIndex, next SignedRecoveryIndexV1) (IndexVersion, error) {
	if err := contextErr(ctx); err != nil {
		return IndexVersion{}, err
	}
	if m == nil {
		return IndexVersion{}, fmt.Errorf("%w: nil recovery index", ErrRecoveryConflict)
	}
	if err := ValidateFixedLocator(locator); err != nil {
		return IndexVersion{}, err
	}
	if err := ValidateRecoveryIndexBinding(next); err != nil {
		return IndexVersion{}, err
	}
	if next.Unsigned.Generation < 1 {
		return IndexVersion{}, fmt.Errorf("%w: generation", ErrRecoveryConflict)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !sameLocator(locator, m.locator) {
		return IndexVersion{}, fmt.Errorf("%w: fixed locator mismatch", ErrRecoveryConflict)
	}
	if !m.provided {
		if expected.Generation != 0 || expected.SHA256 != "" || expected.ProviderVersion != "" {
			return IndexVersion{}, fmt.Errorf("%w: expected initial index", ErrRecoveryConflict)
		}
	} else if expected.Generation != m.version.Generation || expected.SHA256 != m.version.SHA256 ||
		(expected.ProviderVersion != "" && expected.ProviderVersion != m.version.ProviderVersion) {
		return IndexVersion{}, fmt.Errorf("%w: compare-and-swap expected value mismatch", ErrRecoveryConflict)
	}
	wantGeneration := expected.Generation + 1
	if next.Unsigned.Generation != wantGeneration || next.Unsigned.PreviousGeneration != expected.Generation {
		return IndexVersion{}, fmt.Errorf("%w: non-contiguous generation", ErrRecoveryConflict)
	}
	if expected.Generation == 0 {
		if next.Unsigned.PreviousIndexSHA256 != RecoveryGenesisSHA256 {
			return IndexVersion{}, fmt.Errorf("%w: initial recovery genesis", ErrRecoveryConflict)
		}
	} else if next.Unsigned.PreviousIndexSHA256 != expected.SHA256 {
		return IndexVersion{}, fmt.Errorf("%w: previous index digest mismatch", ErrRecoveryConflict)
	}
	digest, err := SignedRecoveryIndexDigest(next)
	if err != nil {
		return IndexVersion{}, err
	}
	version := IndexVersion{Generation: next.Unsigned.Generation, SHA256: digest,
		ProviderVersion: "memory-index-" + digest}
	m.current, m.version, m.provided = cloneSignedRecoveryIndex(next), version, true
	return version, nil
}

func cloneSignedRecoveryIndex(value SignedRecoveryIndexV1) SignedRecoveryIndexV1 {
	return value
}
