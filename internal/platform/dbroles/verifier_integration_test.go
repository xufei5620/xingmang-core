package dbroles

import (
	"context"
	"testing"
	"time"
)

// The real PG18 lifecycle belongs to the already-approved DBR1 harness. This
// guard keeps the verifier's connection boundary testable without accepting a
// shared/staging DSN in ordinary unit runs.
func TestVerifyCatalogRejectsNilConnection(t *testing.T) {
	if _, err := VerifyCatalog(context.Background(), nil, DefaultPolicyV1(), time.Now().UTC()); err == nil {
		t.Fatal("nil catalog connection must fail closed")
	}
}
