package sourceagent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPublisherAdvancesSequenceOnlyAfterMatchingAck(t *testing.T) {
	store := &MemorySequenceStore{}
	builder := BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
		Now: func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) },
	}
	failed := &Publisher{Builder: builder, Store: store, Client: ingestFunc(func(context.Context, ValidatedBatch) (IngestAck, error) {
		return IngestAck{}, errors.New("unavailable")
	})}
	if _, err := failed.Publish(context.Background(), []Projection{candidateProjectionForTest()}); err == nil {
		t.Fatal("expected failed publish")
	}
	state, _ := store.Load(context.Background())
	if state.Sequence != 0 || state.LastBatchHash != "" {
		t.Fatalf("failed publish advanced state: %#v", state)
	}

	succeeded := &Publisher{Builder: builder, Store: store, Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
		return IngestAck{
			Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
			StreamID: batch.Batch.StreamID,
			BatchID:  batch.Batch.BatchID, Sequence: batch.Batch.Sequence,
			AcceptedRecords: len(batch.Batch.Records),
		}, nil
	})}
	if _, err := succeeded.Publish(context.Background(), []Projection{candidateProjectionForTest()}); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load(context.Background())
	if state.Revision != 1 || state.Sequence != 1 || !hexHashPattern.MatchString(state.LastBatchHash) {
		t.Fatalf("successful publish did not advance state: %#v", state)
	}
}

func TestValidatorAcceptsSameDeterministicEventInLaterBatch(t *testing.T) {
	buildTime := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	builder := BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
		Now: func() time.Time { return buildTime },
	}
	first, err := builder.Build(1, "", []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	validator := NewValidator("10000000-0000-4000-8000-000000000002", "payments")
	if _, err := validator.ValidateAndCommit(first.RawBody, first.BodyHash); err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build(2, first.BodyHash, []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	if first.Batch.Records[0].EventID != second.Batch.Records[0].EventID {
		t.Fatal("same source projection must retain deterministic event id")
	}
	if _, err := validator.ValidateAndCommit(second.RawBody, second.BodyHash); err != nil {
		t.Fatalf("idempotent event in next sequence was rejected: %v", err)
	}
}

type ingestFunc func(context.Context, ValidatedBatch) (IngestAck, error)

func (f ingestFunc) Send(ctx context.Context, batch ValidatedBatch) (IngestAck, error) {
	return f(ctx, batch)
}
