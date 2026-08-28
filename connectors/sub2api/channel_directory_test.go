package sub2api

import (
	"testing"
	"time"
)

func TestManagedChannelBalanceIsNullableAndLegacyOmitsOnlyNil(t *testing.T) {
	value := int64(0)
	reported := int64(2)
	directory := ManagedChannelDirectory{
		Snapshot:        Snapshot{ObservedAt: time.Unix(10, 0), Watermark: "wm"},
		Completeness:    DirectoryCompleteness{Complete: true, ReportedCount: &reported, FetchedCount: 2},
		CoveragePartial: true,
		Items: []ManagedChannel{
			{ChannelID: "zero", Status: "active", BalanceMinorUnits: &value, Currency: "CNY"},
			{ChannelID: "subscription", Status: "active", BalanceMinorUnits: nil, Currency: "CNY"},
		},
	}
	legacy := LegacyChannelBalances(directory)
	if len(legacy) != 1 || legacy[0].BalanceMinorUnits != 0 || !legacy[0].IsPartial {
		t.Fatalf("legacy=%+v", legacy)
	}
}

func TestDirectoryObservationKeepsReportedUnknownDistinctFromZero(t *testing.T) {
	unknown := ToChannelDirectoryObservation(time.Unix(20, 0), "s", "staging",
		ManagedChannelDirectory{Completeness: DirectoryCompleteness{Complete: true, ReportedCount: nil}})
	zero := int64(0)
	explicit := ToChannelDirectoryObservation(time.Unix(20, 0), "s", "staging",
		ManagedChannelDirectory{Completeness: DirectoryCompleteness{Complete: true, ReportedCount: &zero}})
	u := unknown.Value["inventory_completeness"].(map[string]any)["reported_count"]
	z := explicit.Value["inventory_completeness"].(map[string]any)["reported_count"]
	if u != (*int64)(nil) || z == nil || *(z.(*int64)) != 0 {
		t.Fatalf("unknown=%#v zero=%#v", u, z)
	}
}
