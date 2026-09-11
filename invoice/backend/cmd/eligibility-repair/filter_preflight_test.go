package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepairWhitespaceFiltersFailBeforeCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, kind, flag string
		filters          repairFilters
	}{
		{"projection account", kindProjectionRequeueDead, "--account", repairFilters{accountID: "   "}},
		{"ingest account", kindIngestRequeueDead, "--account", repairFilters{accountID: "\t\n"}},
		{"ingest event", kindIngestRequeueDead, "--event", repairFilters{eventID: "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			absent := filepath.Join(t.TempDir(), "absent")
			var out bytes.Buffer
			err := run(context.Background(), absent, absent, absent, true, "fixture-operator", tc.kind, tc.filters, &out)
			if err == nil || !strings.Contains(err.Error(), tc.flag+" must not be whitespace") {
				t.Fatalf("expected whitespace filter rejection before credentials, got %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("invalid filter printed a repair summary")
			}
		})
	}
}

func TestRepairLegalFiltersReachCredentialBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		filters    repairFilters
	}{
		{"projection omitted", kindProjectionRequeueDead, repairFilters{}},
		{"ingest omitted", kindIngestRequeueDead, repairFilters{}},
		{"projection account", kindProjectionRequeueDead, repairFilters{accountID: "40000000-0000-4000-8000-000000000001"}},
		{"ingest event", kindIngestRequeueDead, repairFilters{eventID: "40000000-0000-4000-8000-000000000001"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			absent := filepath.Join(t.TempDir(), "absent")
			var out bytes.Buffer
			err := run(context.Background(), absent, absent, absent, false, "", tc.kind, tc.filters, &out)
			// The nonexistent, owned fixture is the first I/O. It guarantees no
			// credential load, connection or repair can occur in this control.
			if err == nil || !strings.Contains(err.Error(), "read database credential") {
				t.Fatalf("legal filter did not reach the inert credential boundary: %v", err)
			}
		})
	}
}
