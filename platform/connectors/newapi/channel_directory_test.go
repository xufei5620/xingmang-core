package newapi

import (
	"testing"
	"time"
)

func TestCoveragePartialDoesNotMeanDirectoryIncomplete(t *testing.T) {
	client := NewFake(FakeOptions{Partial: true, Now: func() time.Time { return time.Unix(30, 0) }})
	directory, err := client.ChannelDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !directory.Completeness.Complete || !directory.CoveragePartial {
		t.Fatalf("directory=%+v", directory)
	}
}

func TestLegacyProjectionDoesNotMutateDirectory(t *testing.T) {
	directory := ChannelDirectorySnapshot{Items: []ChannelStatus{{ChannelID: "1"}}}
	legacy := LegacyChannelStatuses(directory)
	legacy[0].ChannelID = "changed"
	if directory.Items[0].ChannelID != "1" {
		t.Fatal("legacy projection mutated directory")
	}
}
