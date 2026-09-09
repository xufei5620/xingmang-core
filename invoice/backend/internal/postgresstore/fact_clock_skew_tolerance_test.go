package postgresstore

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// XM-INV-BINDING-SKEW T3a. The half of the "one definition" contract that
// needs no database: the SQL fragment really is rendered from
// factClockSkewTolerance, it is embedded in claimBindingSelect's first arm,
// and it is the *only* interval literal on the claim path -- so a later edit
// that hand-writes `interval '5 minutes'` beside it, or instead of it, is
// caught here rather than by review alone. Behaviour is identical under that
// edit, which is exactly why no behavioural test can see it.
//
// The DDL half lives in
// TestFactClockSkewToleranceMatchesEveryFactTableCheckConstraint
// (fact_clock_skew_tolerance_integration_test.go); it is separate because
// this one must stay runnable without INVOICE_TEST_DATABASE_URL.
func TestFactClockSkewToleranceIsTheOnlyIntervalLiteralOnTheClaimPath(t *testing.T) {
	rendered := regexp.MustCompile(`^interval '(\d+) seconds'$`).FindStringSubmatch(factClockSkewToleranceSQL)
	if rendered == nil {
		t.Fatalf("factClockSkewToleranceSQL=%q, want the form interval '<n> seconds'; the seconds spelling is what "+
			"keeps a hand-written interval '5 minutes' from masquerading as the rendered fragment", factClockSkewToleranceSQL)
	}
	wantSeconds := int64(factClockSkewTolerance / time.Second)
	if rendered[1] != strconv.FormatInt(wantSeconds, 10) {
		t.Fatalf("factClockSkewToleranceSQL renders %s seconds but factClockSkewTolerance is %s; the fragment is "+
			"restating the rule instead of rendering it", rendered[1], factClockSkewTolerance)
	}

	if count := strings.Count(claimBindingSelect, factClockSkewToleranceSQL); count != 1 {
		t.Fatalf("claimBindingSelect embeds %q %d times, want exactly 1", factClockSkewToleranceSQL, count)
	}
	// Anchored to the ranking arm's own batch alias, in full, including the
	// CASE that makes the clock a rank rather than a filter. Three things this
	// pins that no behavioural test in this package sees cheaply: that the
	// tolerance is added to the *observation* and compared against the
	// *ceiling* and not the other way round; that the comparison is inclusive,
	// so a ceiling exactly one tolerance ahead still ranks 1, complementing
	// factWatermarkOutsideClockSkew's strict After on the Go side; and that
	// the losing side ranks 2 rather than disappearing, which is what keeps
	// 'payments' -- a stream whose writer has no fact clock rule at all -- on
	// the binding it would otherwise have had.
	const rankPredicate = "CASE WHEN b.scan_ceiling_at<=sie.observed_at+"
	const rankTail = " THEN 1 ELSE 2 END AS preference"
	if !strings.Contains(claimBindingSelect, rankPredicate+factClockSkewToleranceSQL+rankTail) {
		t.Fatalf("claimBindingSelect does not rank on %q as %q...%q; the clock rule must stay a preference on "+
			"the mapping arm's own batch alias", factClockSkewToleranceSQL, rankPredicate, rankTail)
	}
	// ...and the rank has to order the arm, or it is a column nothing reads.
	if !strings.Contains(claimBindingSelect, "ORDER BY preference,b.sequence DESC") {
		t.Fatal("claimBindingSelect computes a preference it never orders by; the clock rule would be inert")
	}
	// The first_batch_id fallback must sort below both, or an event with a
	// merely late valid binding would drop to its first batch after all.
	if !strings.Contains(claimBindingSelect, "3 AS preference") {
		t.Fatal("claimBindingSelect's first_batch_id arm no longer ranks below both mapping preferences")
	}

	// Every way PostgreSQL spells an interval constant, so a second copy of
	// the rule cannot hide behind a different spelling of the same value.
	intervalLiteral := regexp.MustCompile(`(?i)interval\s*'[^']*'|'[^']*'\s*::\s*interval|make_interval\s*\(`)
	found := intervalLiteral.FindAllString(claimBindingSelect, -1)
	if len(found) != 1 || found[0] != factClockSkewToleranceSQL {
		t.Fatalf("claimBindingSelect contains interval constants %q, want exactly [%q]: the claim path must state "+
			"the clock-skew tolerance once, by embedding the rendered fragment", found, factClockSkewToleranceSQL)
	}
	if strings.Contains(claimBindingSelect, "'5 minutes'") {
		t.Fatal("claimBindingSelect hand-writes '5 minutes'; embed factClockSkewToleranceSQL instead")
	}
}

// TestFactClockSkewToleranceIsWhatValidateFactMetadataApplies pins the other
// end of the same wire: the constant is not merely defined, it is the bound
// the fact validator actually uses, and the message it returns is the one
// production logged eight times per event on 2026-09-07 and the one
// ingestRequeueDeadReplayBindingTx quotes back to an operator.
//
// The table drives factWatermarkOutsideClockSkew directly and then requires
// validateFactMetadata to agree with it at every point. That is deliberate:
// the value has an anchor (the DDL comparison in
// TestFactClockSkewToleranceMatchesEveryFactTableCheckConstraint) but the
// *shape* of the comparison had none, and while the repair tool restated it
// instead of calling it, widening only the tool's copy by ninety seconds -- or
// flipping its `>` into `>=` -- left this whole package green. Both callers
// now share this function, so both boundaries below are the boundary the
// runtime and the repair tool apply.
func TestFactClockSkewToleranceIsWhatValidateFactMetadataApplies(t *testing.T) {
	if factMetadataTimeInvalidMessage != "source fact event time/watermark is invalid" {
		t.Fatalf("factMetadataTimeInvalidMessage=%q; this exact text is what production logs and what the runbook "+
			"tells operators to grep for -- changing it is a contract change, not a wording change",
			factMetadataTimeInvalidMessage)
	}
	observed := time.Date(2026, 9, 7, 7, 8, 33, 0, time.UTC)
	event := observed.Add(-2 * time.Hour)
	validate := func(watermark time.Time) error {
		return validateFactMetadata("10000000-0000-4000-8000-000000000001", "u1", "e1",
			strings.Repeat("a", 64), "usage:1", strings.Repeat("b", 64), strings.Repeat("c", 64),
			"SUB2_BALANCE_1E8", event, observed, watermark, 1)
	}
	for _, tc := range []struct {
		name      string
		watermark time.Time
		wantErr   bool
	}{
		{"at the observation", observed, false},
		{"one nanosecond inside the tolerance", observed.Add(factClockSkewTolerance - time.Nanosecond), false},
		{"exactly one tolerance ahead is still carried", observed.Add(factClockSkewTolerance), false},
		{"one nanosecond past the tolerance is not", observed.Add(factClockSkewTolerance + time.Nanosecond), true},
		{"one microsecond past it, the smallest gap PostgreSQL can store", observed.Add(factClockSkewTolerance + time.Microsecond), true},
		{"the production skew of 2026-09-07", observed.Add(2*time.Hour + 15*time.Minute), true},
	} {
		if outside := factWatermarkOutsideClockSkew(observed, tc.watermark); outside != tc.wantErr {
			t.Fatalf("%s: factWatermarkOutsideClockSkew(observed, observed%+s)=%t, want %t",
				tc.name, tc.watermark.Sub(observed), outside, tc.wantErr)
		}
		err := validate(tc.watermark)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: validateFactMetadata accepted a watermark %s past the observation",
					tc.name, tc.watermark.Sub(observed))
			}
			if err.Error() != factMetadataTimeInvalidMessage {
				t.Fatalf("%s: validateFactMetadata returned %q, want %q verbatim",
					tc.name, err.Error(), factMetadataTimeInvalidMessage)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: validateFactMetadata refused a watermark %s past the observation: %v",
				tc.name, tc.watermark.Sub(observed), err)
		}
	}
}

// TestFactClockSkewStreamsMatchesTheWritersThatApplyIt discovers which streams
// actually carry the fact clock rule instead of trusting the map that says so.
//
// factClockSkewStreams has one consumer -- the repair tool's clock-skew
// verdict, which quotes validateFactMetadata by name -- and one failure mode:
// drifting from the writers. Told about a payments event, that verdict names a
// function payments never reaches, and ingest-acknowledge-unreplayable, which
// re-derives its answer from the same code, would then let an operator write
// off a payment or refund the runtime would have accepted. A hand-listed set
// with no probe is exactly the stale gate this repository keeps being bitten
// by, so the probe drives the four real writers.
//
// It needs no database: every writer that applies the rule applies it as its
// first statement, before any pool access, and the two payments probes stop at
// the last gate before ObserveFundingLot opens a transaction (an incomplete
// identity, and a lot that fails domain validation). A nil pool is therefore
// not a hazard but the assertion's teeth -- reaching the database at all would
// panic rather than quietly pass.
func TestFactClockSkewStreamsMatchesTheWritersThatApplyIt(t *testing.T) {
	ctx := context.Background()
	store := &Store{}
	hash := strings.Repeat("a", 64)
	observed := time.Date(2026, 9, 7, 7, 8, 33, 0, time.UTC)
	// The production gap, well past the tolerance in every spelling.
	watermark := observed.Add(2*time.Hour + 15*time.Minute)
	event := observed.Add(-2 * time.Hour)
	const sourceID = "10000000-0000-4000-8000-000000000001"

	// One probe per economic stream, each calling the writer the application
	// layer dispatches that stream to (application/source_processor.go's
	// claim.StreamID switch: balances -> ObserveBalanceCheckpoint,
	// usage -> ObserveUsageEvent, credits -> ObserveCreditEvent,
	// payments -> ObserveFundingLot).
	probes := map[string][]func() error{
		"usage": {func() error {
			return store.ObserveUsageEvent(ctx, UsageObservation{
				SourceInstanceID: sourceID, ExternalUserID: "u1", ExternalEventID: "e1",
				ExternalUsageID: "x1", SourceRevision: hash, SourceCursor: "usage:1",
				CutoverManifestHash: hash, ConfigurationHash: hash, UnitCode: "SUB2_BALANCE_1E8",
				BillingScope: "wallet", ServiceUnits: "1", SourceSequence: 1,
				EventTime: event, ObservedAt: observed, StreamWatermarkAt: watermark,
			}, AuditActor{Type: "system", ID: "probe"})
		}},
		"credits": {func() error {
			return store.ObserveCreditEvent(ctx, CreditObservation{
				SourceInstanceID: sourceID, ExternalUserID: "u1", ExternalEventID: "e1",
				ExternalCreditID: "x1", CreditKind: "BONUS", SourceRevision: hash,
				SourceCursor: "credits:1", CutoverManifestHash: hash, ConfigurationHash: hash,
				UnitCode: "SUB2_BALANCE_1E8", ServiceUnits: "1", SourceSequence: 1,
				EventTime: event, ObservedAt: observed, StreamWatermarkAt: watermark,
			}, AuditActor{Type: "system", ID: "probe"})
		}},
		"balances": {func() error {
			return store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
				SourceInstanceID: sourceID, ExternalUserID: "u1", ExternalEventID: "e1",
				CheckpointID: "x1", CheckpointKind: "reconciliation", SourceRevision: hash,
				SourceCursor: "balances:1", CutoverManifestHash: hash, ConfigurationHash: hash,
				UnitCode: "SUB2_BALANCE_1E8", BalanceServiceUnits: "1", SourceSequence: 1,
				AsOf: event, ObservedAt: observed, StreamWatermarkAt: watermark,
			}, AuditActor{Type: "system", ID: "probe"})
		}},
		"payments": {
			func() error {
				// Stops at the observation identity gate.
				_, err := store.ObserveFundingLot(ctx, SourceObservation{
					ExternalUserID: "u1", EventKind: "payment", ExternalEventID: "e1",
					SchemaVersion: "3.0", SourceSequence: 1, CutoverManifestHash: hash,
					ConfigurationHash: hash, SourceCursor: "payments:1",
					StreamWatermarkAt: watermark,
				}, AuditActor{Type: "system", ID: "probe"})
				return err
			},
			func() error {
				// ...and this one at domain validation, the last gate before
				// the transaction opens, so a rule inserted anywhere earlier
				// in the function is still seen.
				lot := domain.FundingLot{
					ID: "20000000-0000-4000-8000-000000000001", PrincipalID: sourceID,
					SourceInstanceID: sourceID, ExternalOrderID: "o1",
					SourceType: domain.SourceSub2API, Currency: domain.CurrencyCNY,
					SourceRevision: hash, ObservedAt: observed, UpdatedAt: observed,
					OriginalMinor: -1,
				}
				_, err := store.ObserveFundingLot(ctx, SourceObservation{
					Lot: lot, ExternalUserID: "u1", EventKind: "payment", ExternalEventID: "e1",
					SchemaVersion: "3.0", SourceSequence: 1, CutoverManifestHash: hash,
					ConfigurationHash: hash, SourceCursor: "payments:1",
					StreamWatermarkAt: watermark,
				}, AuditActor{Type: "system", ID: "probe"})
				return err
			},
		},
	}
	// The scope is the economic streams, discovered from validEconomicStream
	// rather than listed here, so a fifth economic stream fails this test until
	// somebody decides which side of the rule it is on.
	for _, stream := range []string{"payments", "usage", "credits", "balances", "identities"} {
		if !validEconomicStream(stream) {
			continue
		}
		calls, ok := probes[stream]
		if !ok {
			t.Fatalf("economic stream %q has no writer probe here; decide whether its fact writer applies "+
				"factWatermarkOutsideClockSkew and add it to factClockSkewStreams and to this test", stream)
		}
		for i, call := range calls {
			err := call()
			applies := err != nil && err.Error() == factMetadataTimeInvalidMessage
			if applies != factClockSkewStreams[stream] {
				t.Fatalf("stream %q probe %d: writer %s the fact clock rule (err=%v) but factClockSkewStreams "+
					"says %t; the repair tool's clock-skew reason names validateFactMetadata, so this set has "+
					"to be what the writers do",
					stream, i, map[bool]string{true: "applies", false: "does not apply"}[applies], err,
					factClockSkewStreams[stream])
			}
		}
	}
	// Non-vacuity: a probe list that silently stopped covering a stream would
	// make the loop above assert nothing about it.
	if len(probes) != 4 {
		t.Fatalf("probes cover %d streams, want all four economic ones", len(probes))
	}
	for stream := range factClockSkewStreams {
		if !validEconomicStream(stream) {
			t.Fatalf("factClockSkewStreams names %q, which is not an economic stream at all", stream)
		}
	}
}
