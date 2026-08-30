// Package ratelimit contains the transport-independent contract and reference
// implementation for the platform's shared rate limiter.
//
// The package deliberately has no database, HTTP, or configuration dependency.
// It is the small domain boundary that the future PostgreSQL implementation and
// the HTTP adapter will both consume.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	// AlgorithmGCRAv1 is the only algorithm version understood by this domain.
	AlgorithmGCRAv1 = "gcra-v1"

	// MaxSQLInt is the largest value accepted for fields represented by a
	// PostgreSQL integer. Keeping this cap at the domain boundary prevents a
	// uint32 value from silently wrapping when a later SQL implementation is
	// introduced.
	MaxSQLInt uint32 = math.MaxInt32
)

// ErrorKind is a bounded machine-readable category for domain failures.
type ErrorKind string

const (
	ErrorKindInvalidPolicy   ErrorKind = "invalid_policy"
	ErrorKindInvalidState    ErrorKind = "invalid_state"
	ErrorKindInvalidKey      ErrorKind = "invalid_key"
	ErrorKindInvalidIdentity ErrorKind = "invalid_identity"
	ErrorKindInvalidHMACKey  ErrorKind = "invalid_hmac_key"
	ErrorKindOverflow        ErrorKind = "overflow"
	ErrorKindPolicyMismatch  ErrorKind = "policy_mismatch"
)

var (
	// These sentinels are intentionally stable so adapters can classify errors
	// without parsing error text.
	ErrInvalidPolicy   = errors.New("invalid rate-limit policy")
	ErrInvalidState    = errors.New("invalid rate-limit state")
	ErrInvalidKey      = errors.New("invalid rate-limit bucket key")
	ErrInvalidIdentity = errors.New("invalid rate-limit identity")
	ErrInvalidHMACKey  = errors.New("invalid rate-limit HMAC key")
	ErrOverflow        = errors.New("rate-limit integer overflow")
	ErrPolicyMismatch  = errors.New("rate-limit policy mismatch")
)

// Error is the typed, bounded error returned by domain validation and
// arithmetic. Field contains a schema field name, never a secret or identity
// value.
type Error struct {
	Kind  ErrorKind
	Field string
	Cause error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Field == "" {
		return fmt.Sprintf("ratelimit: %s", e.Kind)
	}
	return fmt.Sprintf("ratelimit: %s (%s)", e.Kind, e.Field)
}

// Unwrap lets callers use errors.Is without exposing implementation details.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func domainError(kind ErrorKind, field string, cause error) error {
	return &Error{Kind: kind, Field: field, Cause: cause}
}

// Policy is the immutable runtime policy consumed by the GCRA reference
// model. Duration fields must be whole seconds and are bounded to PostgreSQL
// integer seconds by Validate.
type Policy struct {
	Revision        int64
	Algorithm       string
	KeyVersion      uint32
	PerMinute       uint32
	Burst           uint32
	IdleTTL         time.Duration
	CleanupInterval time.Duration
}

// BucketKey is the only key shape accepted by a Store. The digest is already
// HMAC-SHA256'd; raw principal/path material never crosses this boundary.
type BucketKey struct {
	Environment string
	KeyVersion  uint32
	Digest      [32]byte
}

// Decision is the result of one atomic consume operation.
type Decision struct {
	Allowed        bool
	RetryAfter     time.Duration
	PolicyRevision int64
	ObservedAt     time.Time
}

// ReadyState is the bounded readiness snapshot returned by a Store.
type ReadyState struct {
	Environment      string
	PolicyRevision   int64
	Algorithm        string
	ActiveKeyVersion uint32
}

// GCRAState stores integer microsecond state. Initialized is optional for
// callers that persist the two integer fields: a non-zero field also denotes
// an initialized state. The explicit bit lets a state at Unix epoch be
// represented without ambiguity.
type GCRAState struct {
	TATMicros      int64
	LastSeenMicros int64
	Initialized    bool
}

// Store is the transport-neutral shared counter contract. Implementations
// must receive only a BucketKey digest and return a bounded Decision.
type Store interface {
	Consume(context.Context, BucketKey) (Decision, error)
	Ready(context.Context, string) (ReadyState, error)
}
