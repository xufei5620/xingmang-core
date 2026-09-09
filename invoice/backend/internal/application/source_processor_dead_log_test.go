package application

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"invoice-system/backend/internal/postgresstore"
)

// TestDeadAndRetryableProjectionFailuresAreDistinguishableByLevel is the whole
// point of XM-INV-READYZ-DETAIL's second half, stated as a test.
//
// Before the slice both outcomes wrote the identical logProjectionFailure
// line at Warn. The eighth failure -- the one that makes the event terminal,
// takes SourceIngestHealth.Dead above zero and holds /readyz at 503 until
// someone repairs it -- was textually indistinguishable from the first seven,
// which recover on their own. An operator watching for error-level lines saw
// a clean log through a six-hour outage.
//
// The assertion is therefore on the level, not merely on the presence of some
// output: `level=ERROR` for the terminal transition and `level=WARN` for the
// retry, from the same claim.
func TestDeadAndRetryableProjectionFailuresAreDistinguishableByLevel(t *testing.T) {
	claim := postgresstore.SourceEventClaim{
		SourceInstanceID:  "10000000-0000-4000-8000-000000000001",
		StreamID:          "usage",
		EventID:           "82000000-0000-4000-8000-000000000001",
		EntityType:        "usage_event",
		Attempt:           8,
		PayloadCiphertext: []byte("must-never-appear-in-the-log-line"),
	}
	cause := errors.New("boom: deterministic unit code mismatch")

	var retryBuf bytes.Buffer
	logProjectionFailure(slog.New(slog.NewTextHandler(&retryBuf, nil)), claim, cause)
	retry := retryBuf.String()

	var deadBuf bytes.Buffer
	logSourceEventDead(slog.New(slog.NewTextHandler(&deadBuf, nil)), claim, cause)
	dead := deadBuf.String()

	if !strings.Contains(retry, "level=WARN") {
		t.Fatalf("a retryable projection failure must stay at Warn: %s", retry)
	}
	if strings.Contains(retry, "level=ERROR") {
		t.Fatalf("a retryable projection failure must not be an error; that is what would drown the terminal one: %s", retry)
	}
	if !strings.Contains(dead, "level=ERROR") {
		t.Fatalf("the terminal dead transition must be reported at Error, since level is what a watch reads: %s", dead)
	}
	if !strings.Contains(dead, `msg="source event marked dead"`) {
		t.Fatalf("the dead line must be findable by message too: %s", dead)
	}
	if strings.Contains(dead, `msg="source event projection failed"`) {
		t.Fatalf("the dead line must be distinguishable from the retry line by message as well: %s", dead)
	}
	for _, want := range []string{
		"source_instance_id=" + claim.SourceInstanceID,
		"stream_id=" + claim.StreamID,
		"event_id=" + claim.EventID,
		"entity_type=" + claim.EntityType,
		"attempt=8",
		cause.Error(),
	} {
		if !strings.Contains(dead, want) {
			t.Fatalf("dead line missing %q, which is what makes it actionable without a database query: %s", want, dead)
		}
	}
	// Same rule as logProjectionFailure: ids and the error, never payload.
	if strings.Contains(dead, "must-never-appear-in-the-log-line") {
		t.Fatalf("dead line leaked payload contents: %s", dead)
	}
}

func TestLogSourceEventDeadDefaultsToSlogDefaultWithoutPanicking(t *testing.T) {
	// Mirrors TestLogProjectionFailureDefaultsToSlogDefaultWithoutPanicking:
	// a nil logger falls back rather than panicking, matching this package's
	// and sourceingest.Receiver's convention.
	logSourceEventDead(nil, postgresstore.SourceEventClaim{SourceInstanceID: "x", StreamID: "usage", EventID: "y"}, errors.New("boom"))
}

// TestSourceEventGradeConstantsMatchTheDatabaseVocabulary pins the two string
// values RunOnce now branches on against the CASE expression in
// MarkSourceEventFailed. A typo here would silently stop the dead line from
// ever being written, and no other test in this package would notice.
func TestSourceEventGradeConstantsMatchTheDatabaseVocabulary(t *testing.T) {
	if postgresstore.SourceEventDead != "dead" {
		t.Fatalf("SourceEventDead=%q, want the processing_status value 'dead'", postgresstore.SourceEventDead)
	}
	if postgresstore.SourceEventFailed != "failed" {
		t.Fatalf("SourceEventFailed=%q, want the processing_status value 'failed'", postgresstore.SourceEventFailed)
	}
}
