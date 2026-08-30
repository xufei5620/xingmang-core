package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func newMemoryTestClock() *memoryTestClock {
	return &memoryTestClock{t: time.Unix(10_000, 0).UTC()}
}

func (c *memoryTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *memoryTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func memoryKey(n byte) BucketKey {
	var digest [32]byte
	digest[0] = n
	return BucketKey{Environment: "staging", KeyVersion: 1, Digest: digest}
}

func TestMemoryStoreMatchesGCRAGolden(t *testing.T) {
	clock := newMemoryTestClock()
	p := testPolicy()
	p.PerMinute = 1
	p.Burst = 20
	p.IdleTTL = time.Hour
	p.CleanupInterval = 10 * time.Minute
	store := NewMemoryStore(p, clock.Now)
	key := memoryKey(1)
	var state GCRAState
	for i := 0; i < 21; i++ {
		got, err := store.Consume(context.Background(), key)
		if err != nil {
			t.Fatalf("consume %d: %v", i+1, err)
		}
		var want Decision
		state, want, err = Evaluate(p, state, clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got.Allowed != want.Allowed || got.RetryAfter != want.RetryAfter || got.PolicyRevision != want.PolicyRevision {
			t.Fatalf("consume %d = %+v, golden = %+v", i+1, got, want)
		}
	}
}

func TestMemoryStoreKeysAreIndependent(t *testing.T) {
	clock := newMemoryTestClock()
	p := testPolicy()
	p.PerMinute = 60
	p.Burst = 1
	store := NewMemoryStore(p, clock.Now)
	if got, err := store.Consume(context.Background(), memoryKey(1)); err != nil || !got.Allowed {
		t.Fatalf("first key: %+v %v", got, err)
	}
	if got, err := store.Consume(context.Background(), memoryKey(2)); err != nil || !got.Allowed {
		t.Fatalf("different digest should have its own bucket: %+v %v", got, err)
	}
	otherEnvironment := memoryKey(1)
	otherEnvironment.Environment = "development"
	if got, err := store.Consume(context.Background(), otherEnvironment); err != nil || !got.Allowed {
		t.Fatalf("different environment should have its own bucket: %+v %v", got, err)
	}
}

func TestMemoryStoreDeniedTouchPreventsIdleReset(t *testing.T) {
	clock := newMemoryTestClock()
	p := testPolicy()
	p.PerMinute = 1
	p.Burst = 1
	p.IdleTTL = 3 * time.Minute
	p.CleanupInterval = time.Minute
	store := NewMemoryStore(p, clock.Now)
	key := memoryKey(3)
	if got, err := store.Consume(context.Background(), key); err != nil || !got.Allowed {
		t.Fatalf("first request: %+v %v", got, err)
	}
	clock.Advance(30 * time.Second)
	if got, err := store.Consume(context.Background(), key); err != nil || got.Allowed {
		t.Fatalf("first denial: %+v %v", got, err)
	}
	store.mu.Lock()
	state := store.buckets[key]
	store.mu.Unlock()
	if state.LastSeenMicros != clock.Now().UnixMicro() {
		t.Fatalf("denied request did not touch last_seen: %+v", state)
	}
	// The denied request above touched last_seen. Cleanup shortly before the
	// idle TTL must retain the bucket even though the original request is older.
	clock.Advance(130 * time.Second)
	store.mu.Lock()
	if err := store.sweepLocked(clock.Now().UnixMicro()); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	_, exists := store.buckets[key]
	store.mu.Unlock()
	if !exists {
		t.Fatal("active denied bucket was reset by idle cleanup")
	}
}

func TestMemoryStoreReadyRejectsPolicyOrKeyMismatch(t *testing.T) {
	clock := newMemoryTestClock()
	bad := testPolicy()
	bad.Algorithm = "token-bucket"
	if _, err := NewMemoryStoreWithError(bad, clock.Now); err == nil {
		t.Fatal("invalid policy should be rejected by the checked constructor")
	}
	store := NewMemoryStoreForEnvironment("staging", testPolicy(), clock.Now)
	if _, err := store.Ready(context.Background(), "production"); err == nil {
		t.Fatal("ready must reject an environment mismatch")
	}
	if _, err := store.Consume(context.Background(), BucketKey{Environment: "staging", KeyVersion: 2}); err == nil {
		t.Fatal("consume must reject a key-version mismatch")
	} else if !errors.Is(err, ErrPolicyMismatch) {
		t.Fatalf("mismatch error = %v, want ErrPolicyMismatch", err)
	}
	ready, err := store.Ready(context.Background(), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if ready.Environment != "staging" || ready.PolicyRevision != 1 || ready.Algorithm != AlgorithmGCRAv1 || ready.ActiveKeyVersion != 1 {
		t.Fatalf("unexpected ready state: %+v", ready)
	}
}

func TestMemoryStoreConcurrentSameKeyIsExact(t *testing.T) {
	clock := newMemoryTestClock()
	p := testPolicy()
	p.PerMinute = 1
	p.Burst = 20
	p.IdleTTL = time.Hour
	p.CleanupInterval = 10 * time.Minute
	store := NewMemoryStore(p, clock.Now)
	key := memoryKey(4)
	const calls = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	errs := make([]error, 0)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			decision, err := store.Consume(context.Background(), key)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if decision.Allowed {
				allowed++
			}
		}()
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("unexpected consume errors: %v", errs)
	}
	if allowed != int(p.Burst) {
		t.Fatalf("allowed = %d, want exact burst %d", allowed, p.Burst)
	}
}
