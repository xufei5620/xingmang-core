package ratelimit

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func canonicalTestIdentity() CanonicalIdentity {
	return CanonicalIdentity{
		KeyVersion:    1,
		Environment:   "staging",
		PrincipalType: principal.TypeHuman,
		PrincipalID:   "staff:alice",
		Method:        "GET",
		RouteTemplate: "/api/v1/audit/events",
	}
}

func TestCanonicalBytesAreLengthPrefixedAndUnambiguous(t *testing.T) {
	got, err := CanonicalBytes(canonicalTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	wantHex := "02000000010000000773746167696e670000000548554d414e0000000b73746166663a616c69636500000003474554000000142f6170692f76312f61756469742f6576656e7473"
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical bytes = %x, want %x", got, want)
	}

	// Delimiters and NUL are ordinary UTF-8 bytes in a length-prefixed field;
	// they must not make two different tuples collide.
	a := canonicalTestIdentity()
	a.PrincipalID = "a:b\x00c"
	b := a
	b.PrincipalID = "a:b\x00d"
	first, err := CanonicalBytes(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("different NUL/colon-containing identities collided")
	}
}

func TestCanonicalBytesSeparateEnvironmentTypeMethodAndRoute(t *testing.T) {
	base := canonicalTestIdentity()
	base.PrincipalID = "用户/α"
	base.RouteTemplate = "/api/{platform}/requests"
	baseline, err := CanonicalBytes(base)
	if err != nil {
		t.Fatal(err)
	}
	variants := []struct {
		name string
		edit func(*CanonicalIdentity)
	}{
		{"environment", func(i *CanonicalIdentity) { i.Environment = "development" }},
		{"type", func(i *CanonicalIdentity) { i.PrincipalType = principal.TypeService }},
		{"method", func(i *CanonicalIdentity) { i.Method = "POST" }},
		{"route", func(i *CanonicalIdentity) { i.RouteTemplate = "/api/{platform}/users" }},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			variant := base
			tc.edit(&variant)
			got, err := CanonicalBytes(variant)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(got, baseline) {
				t.Fatal("field change did not change canonical bytes")
			}
		})
	}
}

func TestCanonicalUnknownRouteUsesFixedSentinel(t *testing.T) {
	i := canonicalTestIdentity()
	i.RouteTemplate = ""
	got, err := CanonicalBytes(i)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(UnknownRouteSentinel)) {
		t.Fatalf("canonical bytes do not contain fixed sentinel: %x", got)
	}
	if bytes.Contains(got, []byte("/private/actual/path")) {
		t.Fatal("unknown route must never embed the actual URL path")
	}
	if _, err := CanonicalBytes(CanonicalIdentity{
		KeyVersion: 1, Environment: "staging", PrincipalType: principal.TypeHuman,
		PrincipalID: "alice", Method: "GET", RouteTemplate: "/private/actual/path",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalRejectsMissingIdentity(t *testing.T) {
	base := canonicalTestIdentity()
	cases := []struct {
		name string
		edit func(*CanonicalIdentity)
	}{
		{"key version", func(i *CanonicalIdentity) { i.KeyVersion = 0 }},
		{"environment", func(i *CanonicalIdentity) { i.Environment = "" }},
		{"unknown environment", func(i *CanonicalIdentity) { i.Environment = "qa" }},
		{"principal type", func(i *CanonicalIdentity) { i.PrincipalType = "UNKNOWN" }},
		{"principal id", func(i *CanonicalIdentity) { i.PrincipalID = "" }},
		{"method", func(i *CanonicalIdentity) { i.Method = "" }},
		{"method non-ascii", func(i *CanonicalIdentity) { i.Method = "ＧＥＴ" }},
		{"route invalid utf8", func(i *CanonicalIdentity) { i.RouteTemplate = string([]byte{0xff}) }},
		{"environment oversized", func(i *CanonicalIdentity) { i.Environment = string(bytes.Repeat([]byte{'x'}, 65)) }},
		{"principal id oversized", func(i *CanonicalIdentity) { i.PrincipalID = string(bytes.Repeat([]byte{'x'}, 4097)) }},
		{"route oversized", func(i *CanonicalIdentity) { i.RouteTemplate = string(bytes.Repeat([]byte{'x'}, 4097)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := base
			tc.edit(&i)
			_, err := CanonicalBytes(i)
			if err == nil {
				t.Fatal("expected identity validation error")
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != ErrorKindInvalidIdentity {
				t.Fatalf("error = %T %v, want typed identity error", err, err)
			}
		})
	}
}

func TestHMACDigestIsDeterministicAndKeyed(t *testing.T) {
	canonical, err := CanonicalBytes(canonicalTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	got, err := HMACDigest(key, canonical)
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString("6057bcd531c2052132d1c105d3d145a53444843bbdbe1c975e073bb7e8933520")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:], want) {
		t.Fatalf("HMAC = %x, want %x", got, want)
	}
	again, err := HMACDigest(key, canonical)
	if err != nil || got != again {
		t.Fatalf("same key/message must be deterministic: %x vs %x (%v)", got, again, err)
	}
	otherKey := bytes.Repeat([]byte{0xa5}, 32)
	other, err := HMACDigest(otherKey, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got == other {
		t.Fatal("different HMAC keys must produce different digests")
	}
}

func TestHMACDigestDoesNotContainIdentity(t *testing.T) {
	i := canonicalTestIdentity()
	canonical, err := CanonicalBytes(i)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	digest, err := HMACDigest(key, canonical)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte(i.Environment), []byte(i.PrincipalID), []byte(i.RouteTemplate)} {
		if bytes.Contains(digest[:], raw) {
			t.Fatalf("digest unexpectedly contains identity bytes %q: %x", raw, digest)
		}
	}
	for _, size := range []int{0, 31, 33} {
		if _, err := HMACDigest(make([]byte, size), canonical); err == nil {
			t.Fatalf("key length %d should be rejected", size)
		}
	}
}
