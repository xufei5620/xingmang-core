package sourceagent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidatorAcceptsIdenticalBatchRetry(t *testing.T) {
	raw := mustMockBatch(t)
	validator := NewValidator("sub2api-mock")
	if _, err := validator.ValidateAndCommit(raw, SHA256Hex(raw)); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	retry, err := validator.ValidateAndCommit(raw, SHA256Hex(raw))
	if err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	if !retry.Duplicate {
		t.Fatal("identical retry must be marked duplicate")
	}
}

func TestValidatorRejectsBatchIDReuseWithDifferentBody(t *testing.T) {
	raw := mustMockBatch(t)
	validator := NewValidator("sub2api-mock")
	if _, err := validator.ValidateAndCommit(raw, SHA256Hex(raw)); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	changed := mustDecodedMockBatch(t)
	changed.AgentVersion = "changed"
	changedRaw := mustMarshal(t, changed)
	if _, err := validator.ValidateAndCommit(changedRaw, SHA256Hex(changedRaw)); !errors.Is(err, ErrReplay) {
		t.Fatalf("expected conflicting replay rejection, got %v", err)
	}
}

func TestValidatorRejectsOutOfOrderSequence(t *testing.T) {
	batch := mustDecodedMockBatch(t)
	batch.Sequence = 2
	previous := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	batch.PreviousBatchHash = &previous
	raw := mustMarshal(t, batch)

	_, err := NewValidator("sub2api-mock").ValidateAndCommit(raw, SHA256Hex(raw))
	if !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("expected out-of-order rejection, got %v", err)
	}
}

func TestValidatorRejectsCrossSourceBatch(t *testing.T) {
	raw := mustMockBatch(t)
	_, err := NewValidator("10000000-0000-4000-8000-000000000002").ValidateAndCommit(raw, SHA256Hex(raw))
	if !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("expected source mismatch, got %v", err)
	}
}

func TestValidatorRejectsWrongClaimedBodyHash(t *testing.T) {
	raw := mustMockBatch(t)
	_, err := NewValidator("sub2api-mock").ValidateAndCommit(raw, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if !errors.Is(err, ErrBodyHashMismatch) {
		t.Fatalf("expected body hash mismatch, got %v", err)
	}
}

func TestValidatorChecksPreviousBodyHash(t *testing.T) {
	firstRaw := mustMockBatch(t)
	validator := NewValidator("sub2api-mock")
	first, err := validator.ValidateAndCommit(firstRaw, SHA256Hex(firstRaw))
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}

	second := mustDecodedMockBatch(t)
	second.BatchID = "018f4ec7-08d0-7b72-a2d4-1df742eec6c9"
	second.Sequence = 2
	second.PreviousBatchHash = &first.BodyHash
	second.Records[0].EventID = "018f4ec7-27a4-7fd2-9f0d-f564046c71d2"
	second.Records[1].EventID = "018f4ec7-2f0d-7b47-a1d4-d7edaaad01bd"
	second.Records[2].EventID = "018f4ec7-3e17-73f6-8f82-edbd9233cc8e"
	secondRaw := mustMarshal(t, second)
	if _, err := validator.ValidateAndCommit(secondRaw, SHA256Hex(secondRaw)); err != nil {
		t.Fatalf("valid second batch: %v", err)
	}
}

func TestValidatorRejectsWrongPreviousBodyHash(t *testing.T) {
	firstRaw := mustMockBatch(t)
	validator := NewValidator("sub2api-mock")
	if _, err := validator.ValidateAndCommit(firstRaw, SHA256Hex(firstRaw)); err != nil {
		t.Fatalf("first batch: %v", err)
	}

	second := mustDecodedMockBatch(t)
	second.BatchID = "018f4ec7-08d0-7b72-a2d4-1df742eec6ca"
	second.Sequence = 2
	wrong := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second.PreviousBatchHash = &wrong
	secondRaw := mustMarshal(t, second)
	if _, err := validator.ValidateAndCommit(secondRaw, SHA256Hex(secondRaw)); !errors.Is(err, ErrPreviousHashMismatch) {
		t.Fatalf("expected previous hash rejection, got %v", err)
	}
}

func TestValidatorRejectsTamperedPayload(t *testing.T) {
	batch := mustDecodedMockBatch(t)
	var payload map[string]any
	if err := json.Unmarshal(batch.Records[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["pay_amount"] = "999.00"
	batch.Records[0].Payload = mustMarshal(t, payload)
	raw := mustMarshal(t, batch)

	_, err := NewValidator("sub2api-mock").ValidateAndCommit(raw, SHA256Hex(raw))
	if !errors.Is(err, ErrPayloadHashMismatch) {
		t.Fatalf("expected payload hash rejection, got %v", err)
	}
}

func mustMockBatch(t *testing.T) []byte {
	t.Helper()
	raw, err := ReadMockBatch("testdata/source-agent-batch.mock.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustDecodedMockBatch(t *testing.T) Batch {
	t.Helper()
	batch, err := decodeBatch(mustMockBatch(t))
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
