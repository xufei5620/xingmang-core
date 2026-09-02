package application

import (
	"bytes"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

func TestCNYMinorUsesExactIntegerArithmetic(t *testing.T) {
	tests := map[string]int64{
		"0": 0, "0.01": 1, "1.2": 120, "500.00": 50_000,
		"92233720368547758.07": math.MaxInt64,
	}
	for input, expected := range tests {
		actual, err := cnyMinor(input)
		if err != nil || actual != expected {
			t.Fatalf("cnyMinor(%q)=%d err=%v want=%d", input, actual, err, expected)
		}
	}
	for _, input := range []string{"0.001", "1e2", "+1", "-1", "92233720368547758.08", ""} {
		if _, err := cnyMinor(input); err == nil {
			t.Fatalf("cnyMinor(%q) unexpectedly succeeded", input)
		}
	}
}

func TestStrictSourcePayloadRejectsUnknownAndTrailingData(t *testing.T) {
	var payload paymentCandidatePayload
	if err := strictJSON([]byte(`{"external_order_id":"1","unknown":true}`), &payload); err == nil {
		t.Fatal("unknown source payload field was accepted")
	}
	if err := strictJSON([]byte(`{} {}`), &payload); err == nil {
		t.Fatal("trailing source payload JSON was accepted")
	}
}

// TestLogProjectionFailureLogsSourceStreamEventAndErrorNotPayload covers the
// standalone logging helper RunOnce calls right before marking an event
// PROJECTION_FAILED (the same call site MarkSourceEventFailed uses whether
// the event ends up 'failed' or, after enough attempts, 'dead' -- see
// MarkSourceEventFailed's own attempt_count>=8 branch in source_sync.go).
// Exercised directly against a hand-built claim rather than through RunOnce:
// Service.store is a concrete *postgresstore.Store, not an interface, so a
// true fake-store unit test of the full RunOnce loop is not practical
// without a real database (see TestSourceProjectionWorkerLogsProjectionFailure
// in service_integration_test.go for the real end-to-end wiring check).
func TestLogProjectionFailureLogsSourceStreamEventAndErrorNotPayload(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	claim := postgresstore.SourceEventClaim{
		SourceInstanceID:  "10000000-0000-4000-8000-000000000001",
		StreamID:          "usage",
		EventID:           "82000000-0000-4000-8000-000000000001",
		PayloadCiphertext: []byte("must-never-appear-in-the-log-line"),
	}
	cause := errors.New("boom: deterministic unit code mismatch")
	logProjectionFailure(logger, claim, cause)
	out := buf.String()
	for _, want := range []string{claim.SourceInstanceID, claim.StreamID, claim.EventID, cause.Error()} {
		if !strings.Contains(out, want) {
			t.Fatalf("log line missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "must-never-appear-in-the-log-line") {
		t.Fatalf("log line leaked payload contents: %s", out)
	}
}

func TestLogProjectionFailureDefaultsToSlogDefaultWithoutPanicking(t *testing.T) {
	// A nil logger must not panic -- matches sourceingest.Receiver's
	// Logger-falls-back-to-slog.Default() convention.
	logProjectionFailure(nil, postgresstore.SourceEventClaim{SourceInstanceID: "x", StreamID: "usage", EventID: "y"}, errors.New("boom"))
}

func TestBalanceCheckpointDependencyClassificationIsExact(t *testing.T) {
	service := &Service{keys: testKeys()}
	claim := postgresstore.SourceEventClaim{SourceInstanceID: "10000000-0000-4000-8000-000000000001"}
	member := true
	nonMember := false
	waitErr := service.balanceCheckpointDependencyError(domain.ErrSourceUnavailable, claim,
		balanceCheckpointPayload{ExternalUserID: "77", CheckpointKind: "reconciliation", BaselineMember: &member})
	var dependency *sourceDependencyWait
	if !errors.As(waitErr, &dependency) || dependency.Kind != "source_eligibility_cutover" ||
		!strings.HasPrefix(dependency.KeyHMAC, "h1:") {
		t.Fatalf("baseline member dependency=%+v err=%v", dependency, waitErr)
	}
	for name, payload := range map[string]balanceCheckpointPayload{
		"cutover waits for manifest retry": {ExternalUserID: "77", CheckpointKind: "cutover", BaselineMember: &member},
		"new account bootstraps":           {ExternalUserID: "77", CheckpointKind: "reconciliation", BaselineMember: &nonMember},
		"missing membership is invalid":    {ExternalUserID: "77", CheckpointKind: "reconciliation"},
	} {
		t.Run(name, func(t *testing.T) {
			got := service.balanceCheckpointDependencyError(domain.ErrSourceUnavailable, claim, payload)
			var unexpected *sourceDependencyWait
			if !errors.Is(got, domain.ErrSourceUnavailable) || errors.As(got, &unexpected) {
				t.Fatalf("error=%v dependency=%+v", got, unexpected)
			}
		})
	}
}
