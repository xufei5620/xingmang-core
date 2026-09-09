package newapi

import (
	"context"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

type DirectoryCompleteness struct {
	Complete      bool
	Truncated     bool
	ReportedCount *int64
	FetchedCount  int64
	Evidence      string
}

type ChannelDirectorySnapshot struct {
	Snapshot
	Completeness    DirectoryCompleteness
	CoveragePartial bool
	Items           []ChannelStatus
}

type ReadClientV2 interface {
	ReadClient
	ChannelDirectory(context.Context) (ChannelDirectorySnapshot, error)
}

func LegacyChannelStatuses(directory ChannelDirectorySnapshot) []ChannelStatus {
	return append([]ChannelStatus(nil), directory.Items...)
}

func ToChannelDirectoryObservation(
	now time.Time, instanceID, environment string, directory ChannelDirectorySnapshot,
) ops.Observation {
	observation := channelsObservation(now, instanceID, environment, directory.Items)
	if observation.Value == nil {
		observation.Value = map[string]any{}
	}
	observation.Value["inventory_completeness"] = map[string]any{
		"complete":       directory.Completeness.Complete,
		"truncated":      directory.Completeness.Truncated,
		"reported_count": directory.Completeness.ReportedCount,
		"fetched_count":  directory.Completeness.FetchedCount,
		"evidence":       directory.Completeness.Evidence,
	}
	observation.Value["coverage_partial"] = directory.CoveragePartial
	return observation
}
