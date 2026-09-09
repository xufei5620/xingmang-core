package postgresstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestEconomicHeartbeatAndWatermarkHaveIndependentFreshnessBudgets(t *testing.T) {
	now := time.Date(2026, time.August, 25, 2, 0, 0, 0, time.UTC)
	newItem := func() SourceStreamHealth {
		return SourceStreamHealth{
			SourceEnabled:          true,
			StreamID:               "payments",
			ApprovedRuntimeVersion: "0.1.179",
			ObservedRuntimeVersion: "0.1.179",
			ProjectionStatus:       "healthy",
			LastAcceptedAt:         now.Add(-30 * time.Second),
			EconomicWatermarkAt:    now.Add(-6 * time.Minute),
		}
	}
	policy := SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute,
		IdentitiesMaxAge: 15 * time.Minute, Now: now,
	}
	item := newItem()
	evaluateSourceStreamHealth(&item, SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: policy.EconomicHeartbeatMaxAge,
		EconomicWatermarkMaxAge: policy.EconomicWatermarkMaxAge,
		IdentitiesMaxAge:        policy.IdentitiesMaxAge, Now: policy.Now,
	})
	if !item.Ready || len(item.Reasons) != 0 || item.MaximumAgeSeconds != 300 || item.EconomicWatermarkMaximumAgeSeconds != 900 {
		t.Fatalf("quiet stream should be ready with reviewed headroom: %#v", item)
	}

	item = newItem()
	item.LastAcceptedAt = now.Add(-6 * time.Minute)
	evaluateSourceStreamHealth(&item, policy)
	if item.Ready || !containsReason(item.Reasons, "STREAM_STALE") || containsReason(item.Reasons, "ECONOMIC_WATERMARK_STALE") {
		t.Fatalf("six-minute heartbeat did not fail independently: %#v", item)
	}

	item = newItem()
	item.EconomicWatermarkAt = now.Add(-15*time.Minute - time.Nanosecond)
	evaluateSourceStreamHealth(&item, policy)
	if item.Ready || containsReason(item.Reasons, "STREAM_STALE") || !containsReason(item.Reasons, "ECONOMIC_WATERMARK_STALE") {
		t.Fatalf("watermark beyond 15 minutes did not fail independently: %#v", item)
	}

	identity := newItem()
	identity.StreamID = "identities"
	identity.EconomicWatermarkAt = time.Time{}
	evaluateSourceStreamHealth(&identity, policy)
	if !identity.Ready || identity.MaximumAgeSeconds != 900 || identity.EconomicWatermarkMaximumAgeSeconds != 0 {
		t.Fatalf("identity stream inherited an economic watermark budget: %#v", identity)
	}
}

func containsReason(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestEconomicWatermarkStaleDowngradesToActiveRescanOnlyWithinTheBoundedWindow
// is the pure (no database) proof for XM-INV-AGENT-RESTART-GRACE part A's
// honesty guard: an active_rescan_updated_at within
// EconomicRescanActivityMaxAge of policy.Now downgrades the otherwise-fatal
// ECONOMIC_WATERMARK_STALE reason to the non-fatal ECONOMIC_RESCAN_ACTIVE and
// flips Ready back to true; anything outside that window, or a policy with
// the grace disabled (zero EconomicRescanActivityMaxAge), reports the plain,
// fatal reason exactly as before this feature existed.
func TestEconomicWatermarkStaleDowngradesToActiveRescanOnlyWithinTheBoundedWindow(t *testing.T) {
	now := time.Date(2026, time.August, 25, 2, 0, 0, 0, time.UTC)
	basePolicy := SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute,
		IdentitiesMaxAge: 15 * time.Minute, EconomicRescanActivityMaxAge: 10 * time.Minute, Now: now,
	}
	newStaleItem := func() SourceStreamHealth {
		return SourceStreamHealth{
			SourceEnabled: true, StreamID: "usage", ApprovedRuntimeVersion: "0.1.179",
			ObservedRuntimeVersion: "0.1.179", ProjectionStatus: "healthy",
			LastAcceptedAt: now.Add(-30 * time.Second), EconomicWatermarkAt: now.Add(-16 * time.Minute),
		}
	}

	for name, fixture := range map[string]struct {
		policy                SourceFreshnessPolicy
		activeRescanUpdatedAt time.Time
		wantReady             bool
		wantReason            string
	}{
		"fresh active cycle downgrades to the non-fatal reason": {
			policy: basePolicy, activeRescanUpdatedAt: now.Add(-2 * time.Minute),
			wantReady: true, wantReason: "ECONOMIC_RESCAN_ACTIVE",
		},
		"exactly at the window boundary still counts as active": {
			policy: basePolicy, activeRescanUpdatedAt: now.Add(-10 * time.Minute),
			wantReady: true, wantReason: "ECONOMIC_RESCAN_ACTIVE",
		},
		"one nanosecond beyond the window is stalled, not active": {
			policy: basePolicy, activeRescanUpdatedAt: now.Add(-10*time.Minute - time.Nanosecond),
			wantReady: false, wantReason: "ECONOMIC_WATERMARK_STALE",
		},
		"no active cycle at all reports the plain reason": {
			policy: basePolicy, activeRescanUpdatedAt: time.Time{},
			wantReady: false, wantReason: "ECONOMIC_WATERMARK_STALE",
		},
		"the grace disabled (zero max age) never downgrades even with a fresh cycle": {
			policy: SourceFreshnessPolicy{
				EconomicHeartbeatMaxAge: basePolicy.EconomicHeartbeatMaxAge, EconomicWatermarkMaxAge: basePolicy.EconomicWatermarkMaxAge,
				IdentitiesMaxAge: basePolicy.IdentitiesMaxAge, Now: now,
			},
			activeRescanUpdatedAt: now.Add(-time.Second), wantReady: false, wantReason: "ECONOMIC_WATERMARK_STALE",
		},
		"a cycle updated in the future beyond clock skew tolerance is not trusted": {
			policy: basePolicy, activeRescanUpdatedAt: now.Add(6 * time.Minute),
			wantReady: false, wantReason: "ECONOMIC_WATERMARK_STALE",
		},
	} {
		t.Run(name, func(t *testing.T) {
			item := newStaleItem()
			item.ActiveRescanUpdatedAt = fixture.activeRescanUpdatedAt
			evaluateSourceStreamHealth(&item, fixture.policy)
			if item.Ready != fixture.wantReady || len(item.Reasons) != 1 || item.Reasons[0] != fixture.wantReason {
				t.Fatalf("ready=%t reasons=%v want ready=%t reason=%q", item.Ready, item.Reasons, fixture.wantReady, fixture.wantReason)
			}
		})
	}
}

// TestSourceHealthQueryJoinsScanCyclesThroughTheActivePartialIndex mirrors
// TestSourceReadinessQueryIsBoundedToActivePartialIndex for the
// XM-INV-AGENT-RESTART-GRACE part A join: it must filter to the same
// cycle_status set the partial unique index (source_economic_one_active_scan_cycle,
// backend/migrations/0009_consumption_eligibility_ledger.sql) covers, so
// Postgres can satisfy it as an index lookup regardless of how large
// source_economic_scan_cycles grows.
func TestSourceHealthQueryJoinsScanCyclesThroughTheActivePartialIndex(t *testing.T) {
	activeCyclePredicate := "sesc.cycle_status IN ('receiving','processing')"
	readinessQuery := strings.Join(strings.Fields(sourceReadinessHealthQuery), " ")
	if !strings.Contains(readinessQuery, "LEFT JOIN source_economic_scan_cycles sesc ON sesc.source_instance_id=si.id AND sesc.stream_id=required.stream_id AND "+activeCyclePredicate) {
		t.Fatalf("readiness query's scan-cycle join is not bound to the active partial index: %s", readinessQuery)
	}
	migration, err := os.ReadFile("../../migrations/0009_consumption_eligibility_ledger.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.Join(strings.Fields(string(migration)), " ")
	if !strings.Contains(ddl, "CREATE UNIQUE INDEX source_economic_one_active_scan_cycle ON source_economic_scan_cycles(source_instance_id,stream_id) WHERE cycle_status IN ('receiving','processing')") {
		t.Fatalf("the active-scan-cycle partial unique index no longer covers this join's predicate: %s", ddl)
	}
}

func TestSourceReadinessQueryIsBoundedToActivePartialIndex(t *testing.T) {
	query := strings.Join(strings.Fields(sourceReadinessHealthQuery), " ")
	// The alias appeared with XM-INV-DEAD-CONTAINMENT, which qualified the
	// classified subquery's columns so the containment predicate could
	// correlate against sie.payload_hash. What this guard is about is
	// unchanged: the subquery's FROM must stay bounded to the statuses the
	// partial index covers.
	activePredicate := "sie.processing_status IN ('queued','failed','processing','dead')"
	if !strings.Contains(query, "WHERE "+activePredicate) {
		t.Fatalf("readiness query lost its active-only predicate: %s", query)
	}
	if strings.Contains(query, "waiting_dependency") || strings.Contains(query, "parked_identity") ||
		strings.Contains(query, "processing_status <> 'processed'") {
		t.Fatalf("readiness query can read dependency backlog rows: %s", query)
	}
	if !strings.Contains(query, "WITH active_event_health AS MATERIALIZED") ||
		!strings.Contains(query, "FROM active_event_health") {
		t.Fatalf("readiness query no longer pre-aggregates its bounded event input: %s", query)
	}

	migration, err := os.ReadFile("../../migrations/0013_source_readiness_active_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.Join(strings.Fields(string(migration)), " ")
	if !strings.Contains(ddl, "Production must prebuild this exact index") ||
		!strings.Contains(ddl, "Even CREATE INDEX IF NOT EXISTS requests a table lock") ||
		strings.Contains(ddl, "CREATE INDEX IF NOT EXISTS source_ingest_events_readiness_active_idx") {
		t.Fatalf("readiness partial index no longer covers the query contract: %s", ddl)
	}
	returnAt := strings.Index(ddl, "IF exact_index THEN RETURN; END IF;")
	lockAt := strings.Index(ddl, "LOCK TABLE public.source_ingest_events IN SHARE MODE")
	createAt := strings.Index(ddl, "EXECUTE 'CREATE INDEX source_ingest_events_readiness_active_idx '")
	if returnAt < 0 || lockAt < 0 || createAt < 0 || returnAt > lockAt || returnAt > createAt {
		t.Fatalf("exact prebuilt index path can reach a table lock or CREATE: %s", ddl)
	}
	logicalPhysicalGate := "IF EXISTS (SELECT 1 FROM public.source_ingest_events LIMIT 1) OR pg_catalog.pg_relation_size('public.source_ingest_events'::pg_catalog.regclass)<>0 THEN"
	if strings.Count(ddl, logicalPhysicalGate) != 2 ||
		!strings.Contains(ddl, "pg_catalog.to_regclass('public.source_ingest_events_readiness_active_idx') IS NOT NULL") ||
		!strings.Contains(ddl, "must be prebuilt externally with CREATE INDEX CONCURRENTLY before migration") ||
		!strings.Contains(ddl, "ON public.source_ingest_events(source_instance_id,stream_id,processing_status,created_at)") ||
		!strings.Contains(ddl, "WHERE processing_status IN (''queued'',''failed'',''processing'',''dead'')") {
		t.Fatalf("missing-index empty-install gate is incomplete: %s", ddl)
	}
	if strings.Count(ddl, "FROM pg_catalog.pg_index AS i") != 2 || strings.Count(ddl, "INTO exact_index") != 2 {
		t.Fatalf("readiness migration must assert exact catalogs before and after empty-install creation: %s", ddl)
	}
	for _, catalogContract := range []string{
		"DO $$ DECLARE exact_index BOOLEAN; BEGIN SELECT EXISTS ( SELECT 1 FROM pg_catalog.pg_index",
		"idx_ns.nspname='public'",
		"am.amname='btree'",
		"tbl_ns.nspname='public'",
		"i.indnatts=4",
		"i.indnkeyatts=4",
		"i.indexprs IS NULL",
		"pg_catalog.pg_get_indexdef(idx.oid,1,true)='source_instance_id'",
		"pg_catalog.pg_get_indexdef(idx.oid,2,true)='stream_id'",
		"pg_catalog.pg_get_indexdef(idx.oid,3,true)='processing_status'",
		"pg_catalog.pg_get_indexdef(idx.oid,4,true)='created_at'",
		"i.indpred IS NOT NULL",
		")='(processing_status=ANY(ARRAY[''queued''::text,''failed''::text,''processing''::text,''dead''::text]))'",
		"NOT i.indisunique",
		"NOT i.indisprimary",
		"NOT i.indisexclusion",
		"i.indisvalid",
		"i.indisready",
		"i.indislive",
		"source readiness active index catalog contract mismatch",
	} {
		if !strings.Contains(ddl, catalogContract) {
			t.Fatalf("readiness migration lost catalog assertion %q: %s", catalogContract, ddl)
		}
	}
}

// TestContainedDeadIsNonFatalWhileUncontainedDeadStaysFatal is the pure
// (no database) rule for XM-INV-DEAD-CONTAINMENT. Before this slice every
// case below with DeadEvents>0 produced Ready=false and Reasons==
// ["EVENTS_DEAD"], so none of these is a positive assertion that the old
// implementation would have satisfied anyway.
//
// The mixed case matters more than the two pure ones: "some dead events are
// contained" must not be allowed to read as "the dead events are handled".
func TestContainedDeadIsNonFatalWhileUncontainedDeadStaysFatal(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	policy := SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute,
		IdentitiesMaxAge: 15 * time.Minute, Now: now,
	}
	newHealthyItem := func() SourceStreamHealth {
		return SourceStreamHealth{
			SourceEnabled: true, StreamID: "usage", ApprovedRuntimeVersion: "0.1.179",
			ObservedRuntimeVersion: "0.1.179", ProjectionStatus: "healthy",
			LastAcceptedAt: now.Add(-30 * time.Second), EconomicWatermarkAt: now.Add(-time.Minute),
		}
	}

	for name, fixture := range map[string]struct {
		pending, dead, contained int64
		wantReady                bool
		wantReasons              []string
	}{
		"one dead event, contained, leaves the stream ready": {
			dead: 1, contained: 1, wantReady: true, wantReasons: []string{"EVENTS_DEAD_CONTAINED"},
		},
		"one dead event nobody owns is still fatal": {
			dead: 1, wantReady: false, wantReasons: []string{"EVENTS_DEAD"},
		},
		"a partly contained stream reports both and stays fatal": {
			dead: 2, contained: 1, wantReady: false,
			wantReasons: []string{"EVENTS_DEAD", "EVENTS_DEAD_CONTAINED"},
		},
		"no dead events at all reports nothing": {
			wantReady: true, wantReasons: []string{},
		},
		"pending events remain fatal alongside containment": {
			pending: 1, dead: 1, contained: 1, wantReady: false,
			wantReasons: []string{"EVENTS_PENDING", "EVENTS_DEAD_CONTAINED"},
		},
		// A report claiming more contained than dead contradicts itself. The
		// bare subtraction would give a negative remainder, report neither
		// reason, and hand back Ready=true -- a real dead event passing the
		// gate because two surfaces disagreed about a column.
		"a self-contradicting report never subtracts its way to ready": {
			dead: 1, contained: 2, wantReady: false, wantReasons: []string{"EVENTS_DEAD", "EVENTS_DEAD_CONTAINED"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			item := newHealthyItem()
			item.PendingEvents, item.DeadEvents, item.ContainedDeadEvents = fixture.pending, fixture.dead, fixture.contained
			evaluateSourceStreamHealth(&item, policy)
			if item.Ready != fixture.wantReady {
				t.Fatalf("ready=%t want %t (reasons=%v)", item.Ready, fixture.wantReady, item.Reasons)
			}
			if len(item.Reasons) != len(fixture.wantReasons) {
				t.Fatalf("reasons=%v want %v", item.Reasons, fixture.wantReasons)
			}
			for i, want := range fixture.wantReasons {
				if item.Reasons[i] != want {
					t.Fatalf("reasons=%v want %v", item.Reasons, fixture.wantReasons)
				}
			}
		})
	}
}

// TestNonFatalStreamHealthReasonsIsTheOneListAndHandsOutCopies pins the set
// itself. cmd/api derives its two allowed-reason maps from this function
// instead of restating the strings, which only helps if the function really
// is the single list and really does hand out something a caller cannot use
// to edit it.
func TestNonFatalStreamHealthReasonsIsTheOneListAndHandsOutCopies(t *testing.T) {
	want := map[string]bool{"ECONOMIC_RESCAN_ACTIVE": true, "EVENTS_DEAD_CONTAINED": true}
	got := NonFatalStreamHealthReasons()
	if len(got) != len(want) {
		t.Fatalf("non-fatal reasons=%v want %v", got, want)
	}
	for reason := range want {
		if !got[reason] {
			t.Fatalf("non-fatal reasons=%v want %v", got, want)
		}
	}
	got["EVENTS_DEAD"] = true
	delete(got, "EVENTS_DEAD_CONTAINED")
	again := NonFatalStreamHealthReasons()
	if again["EVENTS_DEAD"] || !again["EVENTS_DEAD_CONTAINED"] {
		t.Fatalf("a caller edited the package's own non-fatal set through the returned map: %v", again)
	}
	if !nonFatalStreamHealthReasons[eventsDeadContainedReason] || nonFatalStreamHealthReasons["EVENTS_DEAD"] {
		t.Fatalf("the internal set was mutated through the returned copy: %v", nonFatalStreamHealthReasons)
	}
}

// The dead-event rendering guard that used to live here now lives in
// containment_discovery_test.go, alongside the two other discovery rules this
// slice needed. It was a per-line strings.Contains pair, and a code review
// broke it three ways without turning it red (a FILTER split over two lines,
// spaces around the `=`, and `IN ('dead')`); the replacement matches on
// whitespace-normalised declaration text and self-tests its own matcher.
