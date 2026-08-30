package ratelimit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"unicode/utf8"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

const (
	// CanonicalFormatVersion is the wire-format version. It is deliberately
	// independent from the HMAC key version carried in CanonicalIdentity.
	CanonicalFormatVersion byte = 0x02
	// UnknownRouteSentinel is used whenever chi did not provide a route
	// template. Raw request paths must never become bucket identity material.
	UnknownRouteSentinel = "<unmatched-api-route>"

	maxEnvironmentBytes   = 64
	maxPrincipalTypeBytes = 32
	maxPrincipalIDBytes   = 4096
	maxMethodBytes        = 32
	maxRouteBytes         = 4096
	maxCanonicalBytes     = 16 * 1024
)

// CanonicalIdentity is the resolved, request-level identity used to derive a
// private bucket digest. Principal data must come from an authenticated
// context; this type intentionally has no raw URL/path field.
type CanonicalIdentity struct {
	KeyVersion    uint32
	Environment   string
	PrincipalType principal.Type
	PrincipalID   string
	Method        string
	RouteTemplate string
}

// CanonicalBytes encodes an identity according to the frozen v2 protocol:
// version, u32be key version, then five u32be length-prefixed UTF-8 segments.
func CanonicalBytes(identity CanonicalIdentity) ([]byte, error) {
	if identity.KeyVersion == 0 || identity.KeyVersion > MaxSQLInt {
		return nil, domainError(ErrorKindInvalidIdentity, "key_version", ErrInvalidIdentity)
	}
	if !isKnownEnvironment(identity.Environment) {
		return nil, domainError(ErrorKindInvalidIdentity, "environment", ErrInvalidIdentity)
	}
	if !utf8.ValidString(identity.Environment) || len(identity.Environment) > maxEnvironmentBytes {
		return nil, domainError(ErrorKindInvalidIdentity, "environment", ErrInvalidIdentity)
	}
	parsedType, err := principal.ParseType(string(identity.PrincipalType))
	if err != nil || parsedType != identity.PrincipalType {
		return nil, domainError(ErrorKindInvalidIdentity, "principal_type", ErrInvalidIdentity)
	}
	principalType := string(identity.PrincipalType)
	if !validSegment(principalType, maxPrincipalTypeBytes, false) {
		return nil, domainError(ErrorKindInvalidIdentity, "principal_type", ErrInvalidIdentity)
	}
	if !validSegment(identity.PrincipalID, maxPrincipalIDBytes, false) {
		return nil, domainError(ErrorKindInvalidIdentity, "principal_id", ErrInvalidIdentity)
	}

	method, ok := canonicalMethod(identity.Method)
	if !ok || len(method) > maxMethodBytes {
		return nil, domainError(ErrorKindInvalidIdentity, "method", ErrInvalidIdentity)
	}
	route := identity.RouteTemplate
	if route == "" {
		route = UnknownRouteSentinel
	}
	if !validSegment(route, maxRouteBytes, false) {
		return nil, domainError(ErrorKindInvalidIdentity, "route_template", ErrInvalidIdentity)
	}

	segments := [...]string{
		identity.Environment,
		principalType,
		identity.PrincipalID,
		method,
		route,
	}
	total := 1 + 4
	for _, segment := range segments {
		if len(segment) > int(^uint32(0)) {
			return nil, domainError(ErrorKindInvalidIdentity, "segment", ErrInvalidIdentity)
		}
		if total > maxCanonicalBytes-4-len(segment) {
			return nil, domainError(ErrorKindInvalidIdentity, "canonical", ErrInvalidIdentity)
		}
		total += 4 + len(segment)
	}

	encoded := make([]byte, total)
	encoded[0] = CanonicalFormatVersion
	binary.BigEndian.PutUint32(encoded[1:5], identity.KeyVersion)
	offset := 5
	for _, segment := range segments {
		binary.BigEndian.PutUint32(encoded[offset:offset+4], uint32(len(segment)))
		offset += 4
		offset += copy(encoded[offset:], segment)
	}
	return encoded, nil
}

// CanonicalBytes is also available as a method for callers holding a resolved
// identity. The package function remains the canonical implementation.
func (i CanonicalIdentity) Bytes() ([]byte, error) { return CanonicalBytes(i) }

// HMACDigest derives the fixed-size bucket digest. Exactly one 32-byte key is
// accepted; errors intentionally contain only a bounded category and never
// key, digest, or identity material.
func HMACDigest(key, canonical []byte) ([32]byte, error) {
	if len(key) != sha256.Size {
		return [32]byte{}, domainError(ErrorKindInvalidHMACKey, "key_material", ErrInvalidHMACKey)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical) // hash.Hash.Write cannot fail
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest, nil
}

// Digest is a method-shaped convenience wrapper around HMACDigest.
func (i CanonicalIdentity) Digest(key []byte) ([32]byte, error) {
	canonical, err := CanonicalBytes(i)
	if err != nil {
		return [32]byte{}, err
	}
	return HMACDigest(key, canonical)
}

func validSegment(value string, limit int, allowEmpty bool) bool {
	if !allowEmpty && value == "" {
		return false
	}
	return utf8.ValidString(value) && len(value) <= limit
}

func canonicalMethod(method string) (string, bool) {
	if method == "" || len(method) > maxMethodBytes {
		return "", false
	}
	result := make([]byte, len(method))
	for i := 0; i < len(method); i++ {
		b := method[i]
		if !isHTTPTokenByte(b) {
			return "", false
		}
		if b >= 'a' && b <= 'z' {
			b -= 'a' - 'A'
		}
		result[i] = b
	}
	return string(result), true
}

// isHTTPTokenByte follows RFC 9110's tchar set. Restricting methods to ASCII
// avoids Unicode case folding and makes the canonical form cross-language.
func isHTTPTokenByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '!', b == '#', b == '$', b == '%', b == '&', b == '\'', b == '*':
		return true
	case b == '+', b == '-', b == '.', b == '^', b == '_', b == '`', b == '|', b == '~':
		return true
	default:
		return false
	}
}

func isKnownEnvironment(value string) bool {
	switch value {
	case "development", "staging", "production":
		return true
	default:
		return false
	}
}
