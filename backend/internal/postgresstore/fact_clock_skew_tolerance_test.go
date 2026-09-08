package postgresstore

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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
	// Anchored to arm 1's own alias: arm 2 selects on sie.first_batch_id and
	// deliberately carries no time predicate (an event's first delivery is the
	// observation, so there is nothing to compare it against), so a fragment
	// that drifted into arm 2 would be a different rule wearing this one's
	// name.
	const armOnePredicate = "AND b.scan_ceiling_at<=sie.observed_at+"
	if !strings.Contains(claimBindingSelect, armOnePredicate+factClockSkewToleranceSQL) {
		t.Fatalf("claimBindingSelect does not apply %q to arm 1's own batch alias (%q)",
			factClockSkewToleranceSQL, armOnePredicate)
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
// The boundary cases matter because the repair tool's fourth case reproduces
// this comparison exactly: `After(observed+tolerance)` accepts a watermark
// exactly one tolerance ahead and refuses one nanosecond further.
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
		{"exactly one tolerance ahead is still carried", observed.Add(factClockSkewTolerance), false},
		{"one nanosecond past the tolerance is not", observed.Add(factClockSkewTolerance + time.Nanosecond), true},
		{"the production skew of 2026-09-07", observed.Add(2*time.Hour + 15*time.Minute), true},
	} {
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
