package sub2api

import (
	"context"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

type ManagedChannel struct {
	Snapshot
	ChannelID         string
	Name              string
	Status            string
	BalanceMinorUnits *int64
	Currency          string
}

type DirectoryCompleteness struct {
	Complete      bool
	Truncated     bool
	ReportedCount *int64
	FetchedCount  int64
	Evidence      string
}

type ManagedChannelDirectory struct {
	Snapshot
	Completeness    DirectoryCompleteness
	CoveragePartial bool
	Items           []ManagedChannel
}

type ReadClientV2 interface {
	ReadClient
	ChannelDirectory(context.Context) (ManagedChannelDirectory, error)
}

func LegacyChannelBalances(directory ManagedChannelDirectory) []ChannelBalance {
	out := make([]ChannelBalance, 0, len(directory.Items))
	skipped := 0
	for _, item := range directory.Items {
		if item.BalanceMinorUnits == nil {
			skipped++
			continue
		}
		snapshot := item.Snapshot
		snapshot.IsPartial = snapshot.IsPartial || skipped > 0 || !directory.Completeness.Complete
		out = append(out, ChannelBalance{
			Snapshot: snapshot, ChannelID: item.ChannelID, ChannelName: item.Name,
			BalanceMinorUnits: *item.BalanceMinorUnits, Currency: item.Currency,
			TokenValid: item.Status == "active",
		})
	}
	if skipped > 0 {
		for i := range out {
			out[i].IsPartial = true
			out[i].Watermark = fmt.Sprintf("%s/skipped_no_balance:%d", out[i].Watermark, skipped)
		}
	}
	return out
}

func ToChannelDirectoryObservation(
	now time.Time, instanceID, environment string, directory ManagedChannelDirectory,
) ops.Observation {
	items := make([]any, 0, len(directory.Items))
	for _, item := range directory.Items {
		row := map[string]any{
			"channel_id": item.ChannelID, "name": item.Name, "status": item.Status,
			"currency": item.Currency,
		}
		if item.BalanceMinorUnits != nil {
			row["balance_minor_units"] = *item.BalanceMinorUnits
		}
		items = append(items, row)
	}
	completeness := map[string]any{
		"complete":       directory.Completeness.Complete,
		"truncated":      directory.Completeness.Truncated,
		"reported_count": directory.Completeness.ReportedCount,
		"fetched_count":  directory.Completeness.FetchedCount,
		"evidence":       directory.Completeness.Evidence,
	}
	return snapshotToObservation(
		MetricChannelsStatus, instanceID, environment, directory.Snapshot, now,
		map[string]any{
			"channels":               items,
			"channel_count":          len(items),
			"inventory_completeness": completeness,
			"coverage_partial":       directory.CoveragePartial,
		},
	)
}
