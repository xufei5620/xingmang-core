package ratelimit

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an unwired, process-local reference implementation. It is
// intentionally kept behind the Store interface so a future PostgreSQL store
// can be parity-tested without importing HTTP or database packages here.
type MemoryStore struct {
	mu sync.Mutex

	policy       Policy
	policyErr    error
	environment  string
	now          func() time.Time
	buckets      map[BucketKey]GCRAState
	lastSweepUs  int64
	hasLastSweep bool
}

// MemoryStoreOptions makes the checked constructor explicit for callers that
// want to bind a store to one environment.
type MemoryStoreOptions struct {
	Policy      Policy
	Environment string
	Now         func() time.Time
}

// NewMemoryStore creates a reference store. Invalid policy values are retained
// as a fail-closed error returned by Consume/Ready; callers that prefer startup
// validation can use NewMemoryStoreWithError.
func NewMemoryStore(policy Policy, now func() time.Time) *MemoryStore {
	return newMemoryStore("", policy, now)
}

// NewMemoryStoreForEnvironment creates a store bound to an exact environment.
// Binding is useful for readiness checks and prevents a key from being reused
// across environment namespaces.
func NewMemoryStoreForEnvironment(environment string, policy Policy, now func() time.Time) *MemoryStore {
	return newMemoryStore(environment, policy, now)
}

// NewMemoryStoreWithError performs constructor-time validation while retaining
// the same concrete store for callers that need an error-returning factory.
func NewMemoryStoreWithError(policy Policy, now func() time.Time) (*MemoryStore, error) {
	store := newMemoryStore("", policy, now)
	if store.policyErr != nil {
		return nil, store.policyErr
	}
	return store, nil
}

// NewMemoryStoreWithOptions is the fully checked constructor variant.
func NewMemoryStoreWithOptions(options MemoryStoreOptions) (*MemoryStore, error) {
	store := newMemoryStore(options.Environment, options.Policy, options.Now)
	if store.policyErr != nil {
		return nil, store.policyErr
	}
	return store, nil
}

func newMemoryStore(environment string, policy Policy, now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	store := &MemoryStore{
		policy:      policy,
		environment: environment,
		now:         now,
		buckets:     make(map[BucketKey]GCRAState),
	}
	if err := policy.Validate(); err != nil {
		store.policyErr = err
	}
	if environment != "" && !isKnownEnvironment(environment) {
		store.policyErr = domainError(ErrorKindInvalidKey, "environment", ErrInvalidKey)
	}
	return store
}

// Consume evaluates and commits one bucket atomically under the store mutex.
// A canceled context has no side effect; all domain errors fail closed with a
// zero decision.
func (s *MemoryStore) Consume(ctx context.Context, key BucketKey) (Decision, error) {
	if ctx == nil {
		return Decision{}, domainError(ErrorKindInvalidKey, "context", ErrInvalidKey)
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if s == nil {
		return Decision{}, domainError(ErrorKindInvalidKey, "store", ErrInvalidKey)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyErr != nil {
		return Decision{}, s.policyErr
	}
	if err := validateBucketKey(key, s.policy, s.environment); err != nil {
		return Decision{}, err
	}
	now := s.now()
	// Run cleanup before evaluating this request. Cleanup uses the same
	// monotonic effective timestamp as Evaluate, so a backward clock jump
	// cannot make active buckets disappear early.
	nowUs, err := timeToMicros(now)
	if err != nil {
		return Decision{}, err
	}
	sweepErr := s.sweepLocked(nowUs)
	if sweepErr != nil {
		return Decision{}, sweepErr
	}
	state, exists := s.buckets[key]
	if !exists {
		state = GCRAState{}
	}
	next, decision, err := Evaluate(s.policy, state, now)
	if err != nil {
		return Decision{}, err
	}
	s.buckets[key] = next
	return decision, nil
}

// Ready validates the active policy and environment without touching bucket
// state. The requested environment must be a known registry value and, when a
// store is bound, must match that binding exactly.
func (s *MemoryStore) Ready(ctx context.Context, environment string) (ReadyState, error) {
	if ctx == nil {
		return ReadyState{}, domainError(ErrorKindInvalidKey, "context", ErrInvalidKey)
	}
	if err := ctx.Err(); err != nil {
		return ReadyState{}, err
	}
	if s == nil {
		return ReadyState{}, domainError(ErrorKindInvalidKey, "store", ErrInvalidKey)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyErr != nil {
		return ReadyState{}, s.policyErr
	}
	if !isKnownEnvironment(environment) {
		return ReadyState{}, domainError(ErrorKindInvalidKey, "environment", ErrInvalidKey)
	}
	if s.environment != "" && s.environment != environment {
		return ReadyState{}, domainError(ErrorKindPolicyMismatch, "environment", ErrPolicyMismatch)
	}
	return ReadyState{
		Environment:      environment,
		PolicyRevision:   s.policy.Revision,
		Algorithm:        s.policy.Algorithm,
		ActiveKeyVersion: s.policy.KeyVersion,
	}, nil
}

// BucketCount is a bounded diagnostic useful to tests and local rollback
// tooling; it exposes no bucket keys or identity material.
func (s *MemoryStore) BucketCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buckets)
}

func validateBucketKey(key BucketKey, policy Policy, boundEnvironment string) error {
	if !isKnownEnvironment(key.Environment) {
		return domainError(ErrorKindInvalidKey, "environment", ErrInvalidKey)
	}
	if boundEnvironment != "" && key.Environment != boundEnvironment {
		return domainError(ErrorKindPolicyMismatch, "environment", ErrPolicyMismatch)
	}
	if key.KeyVersion == 0 || key.KeyVersion > MaxSQLInt {
		return domainError(ErrorKindInvalidKey, "key_version", ErrInvalidKey)
	}
	if key.KeyVersion != policy.KeyVersion {
		return domainError(ErrorKindPolicyMismatch, "key_version", ErrPolicyMismatch)
	}
	return nil
}

func (s *MemoryStore) sweepLocked(nowUs int64) error {
	interval, err := checkedMul(int64(s.policy.CleanupInterval/time.Second), microsPerSecond)
	if err != nil {
		return domainError(ErrorKindOverflow, "cleanup_interval", ErrOverflow)
	}
	if s.hasLastSweep {
		// A backward clock cannot trigger a premature sweep.
		if nowUs < s.lastSweepUs || nowUs-s.lastSweepUs < interval {
			return nil
		}
	}
	s.lastSweepUs = nowUs
	s.hasLastSweep = true
	idle, err := checkedMul(int64(s.policy.IdleTTL/time.Second), microsPerSecond)
	if err != nil {
		return domainError(ErrorKindOverflow, "idle_ttl", ErrOverflow)
	}
	for key, state := range s.buckets {
		if nowUs >= state.LastSeenMicros && nowUs-state.LastSeenMicros >= idle {
			delete(s.buckets, key)
		}
	}
	return nil
}

var _ Store = (*MemoryStore)(nil)
