package sourceagent

import "testing"

// TestReconcileBaselineCursorAfterCompletion covers the
// XM-INV-AGENT-CREDITS-RECONCILE-FIX regression: the rolling reconcile
// baseline recorded when a scan page completes a cycle must be usage-only.
// Stamping it for credits made every completed credits reconcile page
// produce a cursor the stored-cursor validator (and, downstream, the pending
// spool) rejected, looping the credits stream's periodic reconcile forever
// in production.
func TestReconcileBaselineCursorAfterCompletion(t *testing.T) {
	for name, fixture := range map[string]struct {
		mode ScanMode
		want string
	}{
		"usage reconcile completion records the baseline":                   {mode: ScanReconcile, want: "usage_logs:900"},
		"usage full scan completion does not record a reconcile baseline":   {mode: ScanFull, want: ""},
		"usage incremental completion does not record a reconcile baseline": {mode: ScanIncremental, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := reconcileBaselineCursorAfterCompletion(fixture.mode, StreamUsage, "usage_logs:900"); got != fixture.want {
				t.Fatalf("reconcileBaselineCursorAfterCompletion(usage)=%q want=%q", got, fixture.want)
			}
		})
	}
	for name, fixture := range map[string]struct{ mode ScanMode }{
		"credits reconcile completion never records a baseline: credits restarts at zero every cycle": {mode: ScanReconcile},
		"credits full scan completion does not record a reconcile baseline":                           {mode: ScanFull},
		"credits incremental completion does not record a reconcile baseline":                         {mode: ScanIncremental},
	} {
		t.Run(name, func(t *testing.T) {
			if got := reconcileBaselineCursorAfterCompletion(fixture.mode, StreamCredits, "promo_code_usages:900;user_affiliate_ledger:1;redeem_codes:1"); got != "" {
				t.Fatalf("reconcileBaselineCursorAfterCompletion(credits)=%q want empty", got)
			}
		})
	}
}

// TestCreditsReconcileCompletionCursorPassesStoredCursorValidation ties
// reconcileBaselineCursorAfterCompletion to the actual regression signature:
// validateStoredFileCursorForStream (state_store_file.go) rejects a
// non-empty ReconcileBaselineCursor on any stream but usage. Before the fix,
// a completed credits ScanReconcile page produced a cursor that failed this
// exact check.
func TestCreditsReconcileCompletionCursorPassesStoredCursorValidation(t *testing.T) {
	cursor := testV3Cursor(StreamCredits, "credits-reconcile-completion")
	cursor.ReconcileBaselineCursor = reconcileBaselineCursorAfterCompletion(ScanReconcile, StreamCredits, cursor.WatermarkCursor)
	if cursor.ReconcileBaselineCursor != "" {
		t.Fatalf("credits reconcile completion recorded a baseline: %q", cursor.ReconcileBaselineCursor)
	}
	if err := validateStoredFileCursorForStream(cursor, StreamCredits); err != nil {
		t.Fatalf("credits cursor after a completed reconcile failed stored-cursor validation: %v", err)
	}
}

// TestUsageReconcileCompletionCursorPassesStoredCursorValidation is the
// counterpart proving the usage stream's existing behavior (recording the
// baseline) still passes stored-cursor validation -- it must, since usage is
// the one stream the validator allows this field on.
func TestUsageReconcileCompletionCursorPassesStoredCursorValidation(t *testing.T) {
	cursor := testV3Cursor(StreamUsage, "usage-reconcile-completion")
	cursor.ReconcileBaselineCursor = reconcileBaselineCursorAfterCompletion(ScanReconcile, StreamUsage, cursor.WatermarkCursor)
	if cursor.ReconcileBaselineCursor != cursor.WatermarkCursor {
		t.Fatalf("usage reconcile completion did not record the baseline: %q", cursor.ReconcileBaselineCursor)
	}
	if err := validateStoredFileCursorForStream(cursor, StreamUsage); err != nil {
		t.Fatalf("usage cursor after a completed reconcile failed stored-cursor validation: %v", err)
	}
}

// TestShouldAbandonLegacyReconcileCycle covers every branch of the
// XM-INV-AGENT-RESTART-GRACE part B upgrade-detection helper: only an
// in-flight (not-yet-completed) ScanReconcile cycle on the usage stream that
// was never marked ReconcileWindowBounded counts as a legacy, pre-upgrade
// cycle to abandon.
func TestShouldAbandonLegacyReconcileCycle(t *testing.T) {
	for name, fixture := range map[string]struct {
		cursor   ScanCursor
		newCycle bool
		mode     ScanMode
		stream   string
		want     bool
	}{
		"legacy in-flight usage reconcile is abandoned": {
			cursor: ScanCursor{ReconcileWindowBounded: false}, newCycle: false,
			mode: ScanReconcile, stream: StreamUsage, want: true,
		},
		"bounded in-flight usage reconcile resumes normally": {
			cursor: ScanCursor{ReconcileWindowBounded: true}, newCycle: false,
			mode: ScanReconcile, stream: StreamUsage, want: false,
		},
		"a fresh cycle is never an abandonment, it is the normal start path": {
			cursor: ScanCursor{ReconcileWindowBounded: false}, newCycle: true,
			mode: ScanReconcile, stream: StreamUsage, want: false,
		},
		"in-flight incremental cycles are untouched": {
			cursor: ScanCursor{ReconcileWindowBounded: false}, newCycle: false,
			mode: ScanIncremental, stream: StreamUsage, want: false,
		},
		"in-flight full scans (New API) are untouched": {
			cursor: ScanCursor{ReconcileWindowBounded: false}, newCycle: false,
			mode: ScanFull, stream: StreamUsage, want: false,
		},
		"credits never carries this legacy state: it restarts at zero every cycle": {
			cursor: ScanCursor{ReconcileWindowBounded: false}, newCycle: false,
			mode: ScanReconcile, stream: StreamCredits, want: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := shouldAbandonLegacyReconcileCycle(fixture.cursor, fixture.newCycle, fixture.mode, fixture.stream); got != fixture.want {
				t.Fatalf("shouldAbandonLegacyReconcileCycle=%t want=%t", got, fixture.want)
			}
		})
	}
}

// TestReconcileWindowStart covers the rolling-window position selection
// (XM-INV-AGENT-RESTART-GRACE part B): prefer the previous completed
// reconcile's baseline, fall back to the live watermark when absent or
// unparseable, and never rewind before the cutover manifest.
func TestReconcileWindowStart(t *testing.T) {
	domains := []string{"usage_logs"}
	cutoverPositions := map[string]int64{"usage_logs": 100}

	for name, fixture := range map[string]struct {
		baseline, watermark string
		want                map[string]int64
	}{
		"prefers a valid recorded baseline over the watermark": {
			baseline: "usage_logs:500", watermark: "usage_logs:900",
			want: map[string]int64{"usage_logs": 500},
		},
		"falls back to the watermark when no baseline was ever recorded": {
			baseline: "", watermark: "usage_logs:900",
			want: map[string]int64{"usage_logs": 900},
		},
		"falls back to the watermark when the baseline is corrupt": {
			baseline: "not-a-cursor", watermark: "usage_logs:900",
			want: map[string]int64{"usage_logs": 900},
		},
		"falls back to the cutover manifest when both are corrupt": {
			baseline: "not-a-cursor", watermark: "also-not-a-cursor",
			want: map[string]int64{"usage_logs": 100},
		},
		"falls back to the cutover manifest when both are empty": {
			baseline: "", watermark: "",
			want: map[string]int64{"usage_logs": 100},
		},
		"never rewinds before the cutover manifest even if the baseline somehow regressed": {
			baseline: "usage_logs:50", watermark: "usage_logs:900",
			want: map[string]int64{"usage_logs": 100},
		},
		"a baseline for the wrong domain set falls back to the watermark": {
			baseline: "other_domain:5", watermark: "usage_logs:900",
			want: map[string]int64{"usage_logs": 900},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := reconcileWindowStart(domains, fixture.baseline, fixture.watermark, cutoverPositions)
			if got["usage_logs"] != fixture.want["usage_logs"] {
				t.Fatalf("reconcileWindowStart=%#v want=%#v", got, fixture.want)
			}
		})
	}
}
