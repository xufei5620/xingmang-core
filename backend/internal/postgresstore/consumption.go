package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

const defaultEligibilityFinalizationDelay = 15 * time.Minute

// XM-INV-PROOF-CONTENTION 1: BALANCE_PROOF_PENDING retry schedule.
//
// ProcessEligibilityProjectionJobs's BALANCE_PROOF_PENDING requeue backs off
// exponentially per attempt (the job's own attempt_count, already
// incremented by the claim step above it): 30s, 60s, 120s, 240s, 480s, then
// capped at balanceProofPendingBackoffCapSeconds (10 minutes) from the 6th
// attempt on. Before this, every attempt retried at a flat 30s regardless of
// how many times it had already found the proof unprovable -- on the
// production account that motivated this slice, that meant 500+ retries
// each re-running the (now also fixed, see ensureBalanceCarryForwardProofTx)
// per-visibility proof loop under the per-account advisory lock every 30s
// for hours.
//
// A newly observed fact for the account (the two ON CONFLICT(external_account_id)
// DO UPDATE requeue upserts, in finalizeSourceAccountsTx and
// ObserveBalanceCheckpoint) still may mean the proof has become provable
// sooner than the backoff schedule would otherwise retry it -- a new
// balances cycle publishing is exactly the kind of fact that can unblock a
// pending proof. But unconditionally resetting next_attempt_at=now() on
// every such upsert (the pre-existing behavior) is what let the retry
// cadence collapse back to sub-second in practice: every fact for a
// contended account -- including ones unrelated to the proof itself, since
// facts keep streaming in while the proof stays pending -- re-armed the job
// immediately, defeating the backoff entirely and re-creating the same lock
// contention the backoff exists to bound. The requeue upserts below instead
// pull a deep backoff forward to "now" only when the job is currently
// BALANCE_PROOF_PENDING *and* still more than balanceProofPendingRequeueResetWindow
// away from its next attempt; once pulled forward, the job's own next
// failure re-derives next_attempt_at from its (unchanged, honestly
// incrementing) attempt_count, so a burst of facts within that window cannot
// keep re-arming it faster than once per window. A job that is not
// currently in a BALANCE_PROOF_PENDING backoff (queued for an unrelated
// reason, mid-lease, or freshly failed) is unaffected -- it keeps the
// pre-existing immediate-requeue behavior.
const (
	balanceProofPendingBackoffCapSeconds  = 600
	balanceProofPendingRequeueResetWindow = 5 * time.Minute
)

// eligibilityProjectionReclaimGraceSeconds (XM-INV-READY-LEASE) bounds the
// brief window between a queued BALANCE_PROOF_PENDING job's next_attempt_at
// elapsing and the 2-second eligibility-projection worker (see its Interval
// in buildProductionRuntime) actually ticking around to reclaim and retry it.
// Without this grace, EligibilityProjectionHealth's ProofPending bucket
// (below) would drop such a row the instant next_attempt_at passes, and it
// would count toward OldestPending instead carrying its *previous* attempt's
// updated_at -- which can be many minutes old for a job on a long backoff --
// even though the worker simply has not had its next tick yet. 30s is
// comfortably larger than the 2s tick interval (so a normal reclaim never
// trips it) yet far short of eligibilityProjectionStuckAfter (15m, in
// cmd/api/runtime.go) or even the shortest realistic backoff, so a row that
// is genuinely stuck -- the worker skipped it for much longer than one tick
// -- still ages into OldestPending as before.
const eligibilityProjectionReclaimGraceSeconds = 30

// XM-INV-PROJECTION-FAILURE-GRADING: a per-account error from
// processEligibilityProjectionJob other than errBalanceCarryForwardProofPending
// (that case keeps its own, unrelated BALANCE_PROOF_PENDING backoff below --
// it is never graded here and never spends an attempt) is graded exactly like
// MarkSourceEventFailed grades a source_ingest_events row (source_sync.go):
// the job's own consecutive-failure counter, attempts -- a new column,
// deliberately distinct from the pre-existing attempt_count, which the claim
// UPDATE above increments on every claim regardless of outcome, so it cannot
// tell a genuine failure streak from a job that has simply been retried many
// times while succeeding or waiting on a balance proof -- increments by one;
// below projectionFailureDeadThreshold consecutive failures the job stays
// status='queued' with an exponential backoff (30s doubling, capped at
// projectionFailureBackoffCapSeconds); at the threshold it becomes a
// terminal status='dead' and an audit event is written. A successful run
// resets the counter to zero the same way source_ingest_events' dead-letter
// contract does: the job row is deleted outright (unchanged by this slice --
// see processEligibilityProjectionJob's final DELETE), so any future error
// for this account starts a brand new row at attempts=0.
const (
	projectionFailureDeadThreshold      = 8
	projectionFailureBackoffBaseSeconds = 30
	projectionFailureBackoffCapSeconds  = 1800
)

var (
	serviceUnitsPattern                = regexp.MustCompile(`^(0|[1-9][0-9]{0,77})$`)
	unitCodePattern                    = regexp.MustCompile(`^[A-Z0-9_:-]{1,32}$`)
	errBalanceCarryForwardProofPending = errors.New("signed balance carry-forward proof is not published yet")
	errBalanceCarryForwardProofInvalid = errors.New("signed balance carry-forward proof contract is invalid")
)

type eligibilityAccount struct {
	ExternalAccountID string
	SourceInstanceID  string
	PrincipalID       string
	CutoverAt         time.Time
	GlobalCutoverAt   time.Time
	PolicyStartAt     time.Time
	PolicyVersion     int64
	UnitCode          string
	ManifestHash      string
	ConfigurationHash string
	BootstrapKind     string
	FinalizedThrough  time.Time
	Delay             time.Duration
	Status            string
	Version           int64
}

type eligibilityFact struct {
	Kind            string
	ID              string
	At              time.Time
	Units           *big.Int
	LotID           string
	CreditID        string
	UsageID         string
	PaidMinor       int64
	InvoiceEligible bool
	CausalDomain    string
	CausalOrder     *big.Int
	Revision        string
}

type projectedLot struct {
	ID            string
	PaidMinor     int64
	TotalUnits    *big.Int
	ConsumedUnits *big.Int
	RoundedMinor  int64
	Numerator     *big.Int
	Remainder     *big.Int
	OldMinor      int64
	OldUnits      *big.Int
	IssuedMinor   int64
	ReservedMinor int64
}

type projectedAllocation struct {
	UsageID, LotID, CreditID string
	Order                    int
	Units                    *big.Int
	CashMinorDelta           int64
}

type eligibilityProjection struct {
	Lots            map[string]*projectedLot
	Allocations     []projectedAllocation
	ExpectedBalance *big.Int
	AmbiguousAt     time.Time
	// ShortfallUsage/ShortfallUnits (XM-INV-ELIG-AUTO-RECONCILE, design
	// section 3(B)) identify usage this projection could not allocate against
	// any non-cash or cash pool -- reprojectEligibilityTx records these on the
	// account row (recordUsageOverageTx) instead of freezing.
	//
	// Since XM-INV-OVERAGE-CARRY-FORWARD these describe what is *still*
	// unallocated at the end of the window, not the first shortfall ever seen:
	// an overdraw that a later cash top-up settled is no longer an overage at
	// all, because the units were charged to that top-up's lot when it
	// arrived. Only cash settles one; see the carried queue's own comment.
	ShortfallUsage string
	ShortfallUnits *big.Int
	// UnallocatedUnits (XM-INV-NEGATIVE-DEFICIT) is every unit of usage this
	// projection could not charge to any pool by the end of the window --
	// the carried cash debts plus non-invoice-eligible shortfalls, all of
	// them, not only the oldest one the overage columns name. The source
	// deducted every one of those units, so a negative balance it reports
	// should be exactly this magnitude.
	UnallocatedUnits *big.Int
}

// EligibilityProjectionHealth's OldestPending/ProofPending split
// (XM-INV-READY-PENDING) exists because a job can be legitimately, not
// unhealthily, unresolved for a long time: XM-INV-PROOF-CONTENTION's
// BALANCE_PROOF_PENDING exponential backoff (up to the 10-minute cap) means
// every attempt so far has successfully decided "the balance proof is not
// published yet" and correctly rescheduled itself -- that is forward
// progress, not a stall, even though the row itself (and its created_at) can
// be very old if the source stream stays behind for a while. A production
// account tripped exactly this: /readyz stayed 503 solely because a
// proof-pending job's created_at exceeded the 15-minute readiness window,
// even though attempt_count kept climbing on schedule and nothing was
// actually stuck.
//
// A row counts as "waiting on a balance proof" (ProofPending/
// OldestProofPending) only while ALL of: status='queued' (which the table's
// own CHECK constraint already guarantees means lease_token/lease_expires_at
// are both clear -- processing jobs are never proof-pending),
// last_error_code='BALANCE_PROOF_PENDING', and next_attempt_at is still in
// the future or elapsed less than eligibilityProjectionReclaimGraceSeconds
// ago (an actively scheduled backoff the worker has not had a tick to
// reclaim yet, not one it missed). Every other row -- failed, plain queued,
// a BALANCE_PROOF_PENDING row whose own backoff window elapsed longer ago
// than that grace, or a processing row whose lease has lapsed -- falls into
// OldestPending instead, keyed off updated_at (the last real attempt: set by
// the claim UPDATE, the failure/backoff marks, and the ON CONFLICT requeue
// upserts) rather than created_at, so a job actively cycling through real
// attempts never looks stale merely because its row has existed for a while.
// Only a genuine gap between attempts -- the worker not reaching a due row --
// ages OldestPending past the readiness threshold.
//
// XM-INV-READY-LEASE: a processing row whose lease is still live
// (lease_expires_at in the future) is likewise excluded from OldestPending.
// ProcessEligibilityProjectionJobs' batch claim stamps every row it claims
// with one shared "now" as updated_at, then works through the batch
// serially, each account taking up to its own 300s budget -- so a row still
// waiting its turn legitimately carries an updated_at that ages well past
// eligibilityProjectionStuckAfter (cmd/api/runtime.go) even though the
// worker has not abandoned it. A confirmed production incident
// (2026-09-03 03:17:24Z) tripped /readyz on exactly this: two rows sitting
// status='processing', both still within their lease, both actively being
// attempted. Only a lapsed lease -- the worker crashed, or the process died
// mid-batch -- means a processing row is actually stuck.
//
// XM-INV-PROJECTION-FAILURE-GRADING: Dead (renamed from Failed -- readiness
// and the release-rehearsal shadow tool were its only two consumers, both
// updated by this slice) now counts only status='dead', the new terminal
// grade reached after projectionFailureDeadThreshold consecutive per-account
// failures; a job merely retrying with backoff (status='queued', attempts>0)
// is not terminal and must never make /readyz unhealthy by itself -- see
// Retrying below and eligibilityProjectionReady in cmd/api/runtime.go. For
// the same reason OldestPending's exclusion is widened, alongside the
// pre-existing proof-pending and live-lease cases, to a queued job whose own
// failure-grading backoff has not elapsed yet (attempts>0 and
// next_attempt_at still in the future, with the same worker-reclaim grace as
// the proof-pending case): a job sitting in backoff is legitimately
// scheduled forward, not stuck, exactly like a BALANCE_PROOF_PENDING job.
// Retrying counts every job currently in the failure-grading retry ladder,
// due or not -- a coarser, purely informational operational signal, not a
// readiness input. It can overlap with ProofPending in one edge case (a job
// that failed at least once, so attempts>0, and later separately hit a
// balance-proof-pending outcome, which never resets attempts): both counts
// then include that one row, which is intentional -- it really is both
// "has failed before" and "currently proof-pending" at once.
type EligibilityProjectionHealth struct {
	Queued, Dead, Processing int64
	OldestPending            time.Time
	ProofPending             int64
	OldestProofPending       time.Time
	Retrying                 int64
}

func (s *Store) EligibilityProjectionHealth(ctx context.Context) (EligibilityProjectionHealth, error) {
	now := time.Now().UTC()
	var health EligibilityProjectionHealth
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*) FILTER (WHERE status='queued'),count(*) FILTER (WHERE status='dead'),
			count(*) FILTER (WHERE status='processing'),
			COALESCE(min(updated_at) FILTER (WHERE NOT (
				(status='queued' AND last_error_code='BALANCE_PROOF_PENDING' AND next_attempt_at>$1::timestamptz-interval '%[1]d seconds')
				OR (status='queued' AND attempts>0 AND next_attempt_at>$1::timestamptz-interval '%[1]d seconds')
				OR (status='processing' AND lease_expires_at>$1)
			)),'epoch'::timestamptz),
			count(*) FILTER (WHERE status='queued' AND last_error_code='BALANCE_PROOF_PENDING' AND next_attempt_at>$1::timestamptz-interval '%[1]d seconds'),
			COALESCE(min(updated_at) FILTER (WHERE status='queued' AND last_error_code='BALANCE_PROOF_PENDING' AND next_attempt_at>$1::timestamptz-interval '%[1]d seconds'),'epoch'::timestamptz),
			count(*) FILTER (WHERE status='queued' AND attempts>0)
		FROM eligibility_projection_jobs`, eligibilityProjectionReclaimGraceSeconds), now).Scan(&health.Queued, &health.Dead, &health.Processing,
		&health.OldestPending, &health.ProofPending, &health.OldestProofPending, &health.Retrying)
	if health.OldestPending.Equal(time.Unix(0, 0).UTC()) {
		health.OldestPending = time.Time{}
	}
	if health.OldestProofPending.Equal(time.Unix(0, 0).UTC()) {
		health.OldestProofPending = time.Time{}
	}
	return health, err
}

func parseUnsignedUnits(value, field string, allowZero bool) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if !serviceUnitsPattern.MatchString(value) {
		return nil, fmt.Errorf("%s must be a canonical non-negative integer", field)
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok || (!allowZero && result.Sign() == 0) {
		return nil, fmt.Errorf("%s must be positive", field)
	}
	return result, nil
}

func parseOptionalCausal(domainValue, orderValue string) (string, *big.Int, error) {
	domainValue = strings.TrimSpace(domainValue)
	orderValue = strings.TrimSpace(orderValue)
	if (domainValue == "") != (orderValue == "") {
		return "", nil, errors.New("causal domain and causal order must be supplied together")
	}
	if domainValue == "" {
		return "", nil, nil
	}
	if len(domainValue) > 128 || strings.ContainsAny(domainValue, "\x00\r\n") {
		return "", nil, errors.New("causal domain is invalid")
	}
	order, err := parseUnsignedUnits(orderValue, "causal order", true)
	if err != nil {
		return "", nil, err
	}
	return domainValue, order, nil
}

func validHash(value string) bool { return hexHashPattern.MatchString(strings.TrimSpace(value)) }

// XM-INV-BINDING-SKEW: factClockSkewTolerance is the one definition of how far
// a fact's stream_watermark_at may run ahead of the observation that carries
// it. It is a clock-skew allowance between the agent host and this database,
// nothing more: a watermark further ahead than this means the batch the fact
// is being written against is not the scan that observed the event.
//
// Three places have to agree, and each used to state the five minutes
// separately: validateFactMetadata below; the three fact tables' DDL CHECKs
// (migrations/0009_consumption_eligibility_ledger.sql -- source_usage_events,
// source_credit_events, balance_reconciliation_checkpoints); and, from this
// slice on, claimBindingSelect (source_sync.go), which ranks a binding this
// rule would refuse below one it would carry. The Go rule and the SQL fragment are
// both rendered from this constant. The DDL copies cannot be -- a migration is
// a frozen file -- so TestFactClockSkewToleranceMatchesEveryFactTableCheckConstraint
// reads them back with pg_get_constraintdef and fails if they drift. Same
// discipline as transientRequeueMarkers/transientRequeueMarkerSQL in
// source_sync.go: one definition, rendered, plus a test that proves nothing
// else states it.
//
// This unifies exactly one judgement, not every five minutes in the tree.
// Deliberate near-misses, none of them this rule and none of them safe to fold
// in: RegisterCutoverManifest's CutoverAt-against-DatabaseClock bound below;
// the batch-level scan_ceiling_at<=source_captured_at CHECK in the same
// migration (agent capture time, not any record's observation); the payload
// freshness bound in application/source_processor.go (the source's own
// updated_at against claim.ObservedAt); and sourceingest/receiver.go's
// transport-level DefaultMaximumSkew.
const factClockSkewTolerance = 5 * time.Minute

// factClockSkewToleranceSQL renders factClockSkewTolerance for the queries
// that must apply the same rule server-side. Rendered in seconds rather than
// as "5 minutes" on purpose: the text has to be something nobody would type by
// hand, so a later edit that spells the interval out inside a query instead of
// embedding this variable is caught by a test rather than by review alone.
// PostgreSQL parses interval '300 seconds' and interval '5 minutes' to the
// identical value, so the spelling is free; being un-typeable is not.
var factClockSkewToleranceSQL = renderFactClockSkewToleranceSQL(factClockSkewTolerance)

func renderFactClockSkewToleranceSQL(tolerance time.Duration) string {
	// The input is a compile-time constant, so a value that could change the
	// shape of the query is a programming error: stop the process rather than
	// emit a query nobody checked. Mirrors renderTransientRequeueMarkerSQL.
	if tolerance <= 0 || tolerance%time.Second != 0 || tolerance > time.Hour {
		panic("fact clock skew tolerance must be a positive whole number of seconds no larger than an hour: " + tolerance.String())
	}
	return fmt.Sprintf("interval '%d seconds'", int64(tolerance/time.Second))
}

// factMetadataTimeInvalidMessage is validateFactMetadata's verdict on a fact
// whose three timestamps cannot be true together -- any of them missing, the
// event later than the watermark, or the watermark further than
// factClockSkewTolerance past the observation. It is a named constant because
// it is quoted outside this file: it is the exact text production logged eight
// times per event on 2026-09-07, and ingestRequeueDeadReplayBindingTx's
// clock-skew verdict quotes it so an operator reading a dry run can match the
// reason to the log line character for character. Deliberately covers all
// three branches, so it is named after the check and not after any one of
// them.
const factMetadataTimeInvalidMessage = "source fact event time/watermark is invalid"

// factWatermarkOutsideClockSkew is the comparison itself, and the only place
// it is written in Go. factClockSkewTolerance pins the *value*; this pins the
// *shape* -- which side the tolerance is added to, and that a watermark
// exactly one tolerance ahead is still carried while one microsecond further
// is not.
//
// Two callers, and they must agree or the 2026-09-07 deadlock returns:
// validateFactMetadata below, which is the runtime's verdict, and
// ingestRequeueDeadReplayBindingTx (ingest_requeue_dead_repair.go), whose
// entire contract is to predict that verdict before an operator spends a
// retry ladder finding it out. When those two were separate expressions,
// widening only the tool's by ninety seconds -- or flipping its `>` to `>=`
// -- left every test in this package green: the tool would call an event
// replayable that the runtime refuses, or unreplayable one it would accept,
// and in the second direction an operator writes off a recoverable fact.
// Sharing one function is what makes "the tool's rule is the runtime's rule"
// structural instead of a claim in a comment. The third statement of this
// rule is the SQL in claimBindingSelect, which cannot call a Go function; it
// embeds factClockSkewToleranceSQL and its direction is pinned by
// TestFactClockSkewToleranceIsTheOnlyIntervalLiteralOnTheClaimPath.
func factWatermarkOutsideClockSkew(observedAt, watermarkAt time.Time) bool {
	return watermarkAt.After(observedAt.Add(factClockSkewTolerance))
}

// factClockSkewStreams names the economic streams whose fact writer actually
// applies factWatermarkOutsideClockSkew, and it is deliberately not all four.
// ObserveUsageEvent, ObserveCreditEvent and ObserveBalanceCheckpoint each open
// with validateFactMetadata; ObserveFundingLot ('payments') does not and never
// has -- it verifies the batch context and the trust anchors and nothing about
// the fact's clock.
//
// This set exists for one caller: the repair tool's clock-skew verdict, which
// quotes validateFactMetadata by name. Told about a payments event, that
// verdict would be a falsifiable lie -- the named function is not on that
// stream's path -- and an operator acting on it would write off a refund the
// runtime would have accepted, which over-states what the customer paid and
// over-invoices. The claim path needs no such set: XM-INV-BINDING-SKEW makes
// the clock a *preference* between bindings rather than a filter, so a stream
// with no clock rule keeps whatever binding it would have had.
//
// Hand-listed sets rot. TestFactClockSkewStreamsMatchesTheWritersThatApplyIt
// discovers the truth instead of restating it: for every economic stream it
// calls that stream's real writer with a watermark past the tolerance and
// reads back whether the rule fired, so adding the rule to payments -- or
// dropping it from credits -- fails here until this map is corrected.
var factClockSkewStreams = map[string]bool{"usage": true, "credits": true, "balances": true}

func validateFactMetadata(sourceID, externalUserID, eventID, revision, cursor, manifestHash, configurationHash, unitCode string, eventAt, observedAt, watermarkAt time.Time, sequence int64) error {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(externalUserID) == "" ||
		strings.TrimSpace(eventID) == "" || len(eventID) > 256 || sequence <= 0 ||
		strings.TrimSpace(cursor) == "" || len(cursor) > 256 || !validHash(revision) ||
		!validHash(manifestHash) || !validHash(configurationHash) || !unitCodePattern.MatchString(unitCode) {
		return errors.New("complete v3 source fact metadata is required")
	}
	if eventAt.IsZero() || observedAt.IsZero() || watermarkAt.IsZero() || eventAt.After(watermarkAt) ||
		factWatermarkOutsideClockSkew(observedAt, watermarkAt) {
		return errors.New(factMetadataTimeInvalidMessage)
	}
	return nil
}

func (s *Store) RegisterCutoverManifest(ctx context.Context, manifest CutoverManifest, actor AuditActor) error {
	if strings.TrimSpace(manifest.SourceInstanceID) == "" || !validHash(manifest.ManifestHash) ||
		!validHash(manifest.ConfigurationHash) || !unitCodePattern.MatchString(manifest.UnitCode) ||
		manifest.CutoverAt.IsZero() || manifest.DatabaseClock.IsZero() ||
		manifest.CutoverAt.After(manifest.DatabaseClock.Add(5*time.Minute)) ||
		strings.TrimSpace(manifest.SourceRuntimeVersion) == "" || len(manifest.SourceRuntimeVersion) > 128 ||
		strings.TrimSpace(manifest.ProjectionContract) == "" || len(manifest.ProjectionContract) > 128 ||
		strings.TrimSpace(manifest.SigningKeyID) == "" || len(manifest.SigningKeyID) > 128 ||
		!validHash(manifest.BaselineSnapshotID) || !validHash(manifest.BaselineSnapshotHash) ||
		manifest.BaselineSnapshotID != manifest.BaselineSnapshotHash ||
		manifest.BaselineRowCount < 0 || manifest.BaselineRowCount > 2_000_000 {
		return errors.New("complete signed cutover manifest is required")
	}
	actor = actor.normalized()
	if actor.Type != "source_connector" || actor.ID != manifest.SourceInstanceID {
		return domain.ErrForbidden
	}
	ceilings := []string{manifest.PaymentsCeiling, manifest.UsageCeiling, manifest.CreditsCeiling, manifest.BalancesCeiling}
	for _, ceiling := range ceilings {
		if strings.TrimSpace(ceiling) == "" || len(ceiling) > 256 {
			return errors.New("all cutover stream ceilings are required")
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,41))`, manifest.SourceInstanceID); err != nil {
		return err
	}
	// The source-scoped advisory lock serializes create/idempotence checks.
	// source_cutover_manifests is append-only, so a row lock would add no
	// protection and would incorrectly require UPDATE on the hardened runtime.
	var policyStart time.Time
	if err = tx.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		return err
	}
	if !manifest.CutoverAt.UTC().Before(policyStart.UTC()) ||
		!manifest.DatabaseClock.UTC().Before(policyStart.UTC()) {
		return domain.ErrConflict
	}
	trustedSigningKey, err := verifyFactBatchContextTx(ctx, tx, manifest.SourceInstanceID, "balances",
		manifest.ExternalEventID, manifest.BatchID, manifest.ScanCycleID, manifest.SourceRevision,
		manifest.StreamWatermarkAt)
	if err != nil {
		return err
	}
	// The payload field is informational only. The persisted key comes from the
	// already authenticated mTLS/Ed25519 batch row.
	manifest.SigningKeyID = trustedSigningKey
	var existing CutoverManifest
	err = tx.QueryRow(ctx, `
		SELECT source_instance_id,manifest_hash,source_runtime_version,projection_contract,configuration_hash,unit_code,
			payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,
			baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,
			signing_key_id,cutover_at,database_clock
		FROM source_cutover_manifests WHERE source_instance_id=$1`, manifest.SourceInstanceID).Scan(
		&existing.SourceInstanceID, &existing.ManifestHash, &existing.SourceRuntimeVersion, &existing.ProjectionContract, &existing.ConfigurationHash,
		&existing.UnitCode, &existing.PaymentsCeiling, &existing.UsageCeiling, &existing.CreditsCeiling,
		&existing.BalancesCeiling, &existing.BaselineSnapshotID, &existing.BaselineSnapshotHash,
		&existing.BaselineRowCount, &existing.SigningKeyID, &existing.CutoverAt, &existing.DatabaseClock)
	if err == nil {
		if !sameCutoverManifest(existing, manifest) {
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var configuredRuntime string
	var sourceType domain.SourceType
	if err = tx.QueryRow(ctx, `SELECT runtime_version,source_type FROM source_instances WHERE id=$1 AND enabled FOR SHARE`, manifest.SourceInstanceID).Scan(&configuredRuntime, &sourceType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrForbidden
		}
		return err
	}
	expectedContract := "sub2api-economic-v4"
	if sourceType == domain.SourceNewAPI {
		expectedContract = "newapi-economic-rc25-v4"
	}
	if configuredRuntime != manifest.SourceRuntimeVersion || manifest.UnitCode != expectedUnitForSource(sourceType) ||
		manifest.ProjectionContract != expectedContract {
		return domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO source_cutover_manifests(
			source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,projection_contract,
			configuration_hash,unit_code,payments_ceiling,usage_ceiling,credits_ceiling,
			balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, manifest.SourceInstanceID,
		manifest.ManifestHash, manifest.CutoverAt.UTC(), manifest.DatabaseClock.UTC(), manifest.SourceRuntimeVersion,
		manifest.ProjectionContract, manifest.ConfigurationHash, manifest.UnitCode, manifest.PaymentsCeiling, manifest.UsageCeiling,
		manifest.CreditsCeiling, manifest.BalancesCeiling, manifest.BaselineSnapshotID,
		manifest.BaselineSnapshotHash, manifest.BaselineRowCount, manifest.SigningKeyID)
	if err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.cutover_manifest.accepted", "source_instance", manifest.SourceInstanceID,
		nil, map[string]any{"manifest_hash": manifest.ManifestHash, "cutover_at": manifest.CutoverAt,
			"configuration_hash": manifest.ConfigurationHash}); err != nil {
		return err
	}
	for _, stream := range []string{"payments", "usage", "credits", "balances"} {
		if err = tryPublishEconomicScanCyclesTx(ctx, tx, manifest.SourceInstanceID, stream, actor); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) CutoverManifestExists(ctx context.Context, sourceID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_cutover_manifests WHERE source_instance_id=$1)`, sourceID).Scan(&exists)
	return exists, err
}

func sameCutoverManifest(a, b CutoverManifest) bool {
	return a.SourceInstanceID == b.SourceInstanceID && a.ManifestHash == b.ManifestHash &&
		a.SourceRuntimeVersion == b.SourceRuntimeVersion && a.ProjectionContract == b.ProjectionContract &&
		a.ConfigurationHash == b.ConfigurationHash && a.UnitCode == b.UnitCode &&
		a.PaymentsCeiling == b.PaymentsCeiling && a.UsageCeiling == b.UsageCeiling &&
		a.CreditsCeiling == b.CreditsCeiling && a.BalancesCeiling == b.BalancesCeiling &&
		a.BaselineSnapshotID == b.BaselineSnapshotID && a.BaselineSnapshotHash == b.BaselineSnapshotHash &&
		a.BaselineRowCount == b.BaselineRowCount && a.SigningKeyID == b.SigningKeyID &&
		a.CutoverAt.Equal(b.CutoverAt) && a.DatabaseClock.Equal(b.DatabaseClock)
}

func validEconomicStream(value string) bool {
	return value == "payments" || value == "usage" || value == "credits" || value == "balances"
}

// AdvanceSourceEconomicWatermark consumes signed batch completeness metadata.
// It is intentionally source-scoped: an empty batch advances inactive users as
// well as users represented by rows in that batch.
func (s *Store) AdvanceSourceEconomicWatermark(ctx context.Context, sourceID, manifestHash, configurationHash string, watermark EligibilityWatermark, actor AuditActor) error {
	if strings.TrimSpace(sourceID) == "" || !validHash(manifestHash) || !validHash(configurationHash) ||
		!validEconomicStream(watermark.StreamKind) || watermark.WatermarkAt.IsZero() ||
		watermark.SourceSequence < 0 || strings.TrimSpace(watermark.SourceCursor) == "" || len(watermark.SourceCursor) > 256 {
		return errors.New("invalid source economic watermark")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, sourceID); err != nil {
		return err
	}
	var trustedManifest, trustedConfig string
	if err = tx.QueryRow(ctx, `SELECT manifest_hash,configuration_hash FROM source_cutover_manifests WHERE source_instance_id=$1`, sourceID).Scan(&trustedManifest, &trustedConfig); err != nil {
		return err
	}
	if trustedManifest != manifestHash || trustedConfig != configurationHash {
		return domain.ErrConflict
	}
	var oldAt time.Time
	var oldSequence int64
	var oldCursor string
	err = tx.QueryRow(ctx, `
		SELECT watermark_at,source_sequence,source_cursor
		FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind=$2 FOR UPDATE`, sourceID, watermark.StreamKind).Scan(&oldAt, &oldSequence, &oldCursor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && (watermark.WatermarkAt.Before(oldAt) || watermark.SourceSequence < oldSequence) {
		if freezeErr := freezeSourceAccountsTx(ctx, tx, sourceID, "STREAM_WATERMARK_REGRESSION", "source_watermark", watermark.StreamKind,
			"", actor); freezeErr != nil {
			return freezeErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return domain.ErrConflict
	}
	if err == nil && watermark.WatermarkAt.Equal(oldAt) && watermark.SourceSequence == oldSequence {
		if oldCursor != watermark.SourceCursor {
			if freezeErr := freezeSourceAccountsTx(ctx, tx, sourceID, "EVENT_PAYLOAD_DRIFT", "source_watermark", watermark.StreamKind, "", actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO source_economic_stream_watermarks(
			source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET
			watermark_at=EXCLUDED.watermark_at,source_sequence=EXCLUDED.source_sequence,
			source_cursor=EXCLUDED.source_cursor,configuration_hash=EXCLUDED.configuration_hash,updated_at=now()`,
		sourceID, watermark.StreamKind, watermark.WatermarkAt.UTC(), watermark.SourceSequence,
		watermark.SourceCursor, configurationHash)
	if err != nil {
		return err
	}
	if err = finalizeSourceAccountsTx(ctx, tx, sourceID, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func finalizeSourceAccountsTx(ctx context.Context, tx pgx.Tx, sourceID string, _ AuditActor) error {
	var minWatermark time.Time
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT min(watermark_at),count(*) FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1`, sourceID).Scan(&minWatermark, &count); err != nil {
		return err
	}
	if count != 4 {
		return nil
	}
	// Enqueue only accounts with facts in the newly finalized interval. This is
	// a single set-based operation; ingestion never loops/replays thousands of
	// accounts while holding the signed batch transaction open.
	// XM-INV-CATCHUP-BURST-BACKPRESSURE: the enqueue below must never wait on
	// an account whose projection job is mid-transaction. processEligibilityProjectionJob
	// takes FOR UPDATE on its own eligibility_projection_jobs row as its first
	// statement and holds it until commit -- through the carry-forward proof
	// phase, which on a contended account runs for minutes -- while this
	// function is called from inside tryPublishEconomicScanCyclesTx with the
	// stream's scan-cycle and watermark rows already locked. Under the runtime
	// role's lock_timeout=5s, an ON CONFLICT DO UPDATE that waited on the job
	// row died with 55P03 and took the whole cycle publication (and the event
	// mark that wrapped it) down with it; the balances stream could not
	// publish for 29 minutes on 2026-09-04 and readiness answered 503 the
	// whole time, for one account's catch-up. See the handoff's 2026-09-05
	// revision for the row-level evidence.
	//
	// `held` locks, with SKIP LOCKED, only the job rows this pass can take
	// without waiting; an account whose row is held by a running job is left
	// out of this pass entirely. Nothing is lost by skipping it: requested_through
	// is recomputed from the stream watermarks on every pass, the running job
	// publishes finalized_through when it commits, and the next pass -- at most
	// one poll interval later -- enqueues whatever window is still open. Late
	// facts at or below an already-finalized boundary are not this function's
	// concern; the observe paths enqueue those themselves (and deliberately
	// still wait, because skipping there would orphan the fact).
	//
	// Accounts with no job row at all are inserted as before: a conflict there
	// can only be with another concurrent enqueue, which is short-lived.
	_, err := tx.Exec(ctx, `
		WITH balances_frontier AS (
			SELECT max(c.scan_ceiling_at) AS newest_published
			FROM source_economic_scan_cycles c
			WHERE c.source_instance_id=$1 AND c.stream_id='balances' AND c.cycle_status='published'
		), targets AS (
			SELECT eas.external_account_id,eas.finalized_through,
				eas.eligibility_status,eas.pending_reconciliation_consecutive_matches,
				GREATEST(eas.cutover_at,$2::timestamptz-
					make_interval(secs=>eas.finalization_delay_seconds)) AS requested_through
			FROM source_account_eligibility_state eas
			WHERE eas.source_instance_id=$1 AND eas.catchup_key_hmac IS NULL
				AND eas.eligibility_status<>'syncing'
		), changed AS (
			SELECT t.* FROM targets t WHERE t.requested_through>t.finalized_through AND (
				EXISTS (SELECT 1 FROM source_usage_events e WHERE e.external_account_id=t.external_account_id
					AND e.event_time>t.finalized_through AND e.event_time<=t.requested_through)
				OR EXISTS (SELECT 1 FROM source_credit_events e WHERE e.external_account_id=t.external_account_id
					AND e.event_time>t.finalized_through AND e.event_time<=t.requested_through)
				OR EXISTS (SELECT 1 FROM funding_lots fl WHERE fl.external_account_id=t.external_account_id
					AND fl.completed_at>t.finalized_through AND fl.completed_at<=t.requested_through
					AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH'))
				OR EXISTS (SELECT 1 FROM balance_reconciliation_checkpoints b
					WHERE b.external_account_id=t.external_account_id
						AND b.as_of>t.finalized_through AND b.as_of<=t.requested_through)
				-- XM-INV-PENDING-RECON C1: an idle not_invoiceable_pending_reconciliation
				-- account one matched evaluation short of auto-exit has no facts of its
				-- own to be enqueued on -- that is exactly its problem. A production
				-- account reached pending_reconciliation_consecutive_matches=1 and then
				-- went quiet: no usage, no credit, no payment, and (because the source
				-- agent only emits balance rows whose units/negative flag/deficit
				-- actually changed) no further checkpoint either. Every finalization
				-- pass therefore fell through to the empty-advance UPDATE below, which
				-- moves finalized_through past the published balances cycles that
				-- ensureBalanceCarryForwardProofTx could otherwise still have derived an
				-- idle carry-forward proof from -- burning, once per pass, the very
				-- evidence the account needs in order to leave the state.
				--
				-- Enqueueing here is what stops that: a job row exists, so the
				-- empty-advance UPDATE's own NOT EXISTS skips this account and the cycle
				-- survives until the worker (C2's idle branch) can derive from it.
				-- Deliberately narrow, per the 2026-09-09 ruling on design section 7 D2:
				-- only accounts already at N-1. The same threshold is enforced again
				-- inside the derivation itself (pendingReconciliationIdleMinMatches) --
				-- a job has other sources than this predicate, so a gate that lives
				-- only here is not a gate.
				--
				-- The cycle test reads a scalar CTE computed once for the whole pass,
				-- never a per-account correlated subquery. This statement runs inside
				-- tryPublishEconomicScanCyclesTx, holding the stream's scan-cycle and
				-- watermark rows under the runtime role's lock_timeout=5s -- the exact
				-- position that took the balances stream down for 29 minutes on
				-- 2026-09-04 (see the note above). source_economic_scan_cycles has no
				-- index that serves "published cycles of this stream by ceiling". The
				-- primary key is (source_instance_id, stream_id, scan_cycle_id): its
				-- first two columns do narrow to this stream, and an EXPLAIN shows the
				-- planner using exactly that prefix -- but scan_ceiling_at is not in it,
				-- so every published cycle of the stream is read and filtered. The only
				-- other index is partial on cycle_status IN ('receiving','processing'),
				-- which excludes published rows entirely. A correlated
				-- EXISTS there would re-scan the stream's whole cycle history -- one row
				-- per source per minute, ~500k rows a year -- once for every pending
				-- account matched. As a CTE it is one aggregate for the pass. Adding
				-- the index instead would be a migration, which this release does not
				-- carry.
				--
				-- Only the lower bound is tested. "The newest published cycle is above
				-- finalized_through" is implied by (and weaker than) "a cycle exists
				-- inside the window", so this never misses an account the window test
				-- would have caught; it enqueues a few extra when every published cycle
				-- is still above requested_through. Those jobs derive nothing (the
				-- derivation applies the real window), publish finalized_through as any
				-- job does, and cost one row for the handful of accounts sitting at N-1.
				OR (t.eligibility_status='not_invoiceable_pending_reconciliation'
					AND t.pending_reconciliation_consecutive_matches>=$4
					AND (SELECT newest_published FROM balances_frontier)>t.finalized_through)
			)
		), held AS (
			SELECT j.external_account_id FROM eligibility_projection_jobs j
			WHERE j.external_account_id IN (SELECT external_account_id FROM changed)
			FOR UPDATE SKIP LOCKED
		)
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		SELECT c.external_account_id,c.requested_through,'queued',now() FROM changed c
		WHERE c.external_account_id IN (SELECT external_account_id FROM held)
		   OR NOT EXISTS (SELECT 1 FROM eligibility_projection_jobs j WHERE j.external_account_id=c.external_account_id)
		ON CONFLICT(external_account_id) DO UPDATE SET
			requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
			-- XM-INV-PROJECTION-FAILURE-GRADING: a new fact must never silently
			-- revive a status='dead' job -- that terminal grade exists
			-- specifically so a persistently failing account stops being
			-- retried automatically until an operator investigates (via
			-- invoice-eligibility-repair --kind=projection-requeue-dead), the
			-- same as source_ingest_events' own dead-letter contract. Only
			-- requested_through above still advances while dead, so the
			-- repair tool's requeue immediately picks up every fact that
			-- arrived in the meantime.
			status=CASE WHEN eligibility_projection_jobs.status IN ('dead','processing') THEN eligibility_projection_jobs.status ELSE 'queued' END,
			lease_token=CASE WHEN eligibility_projection_jobs.status='processing' THEN eligibility_projection_jobs.lease_token END,
			lease_expires_at=CASE WHEN eligibility_projection_jobs.status='processing' THEN eligibility_projection_jobs.lease_expires_at END,
			next_attempt_at=CASE
				WHEN eligibility_projection_jobs.status IN ('dead','processing') THEN eligibility_projection_jobs.next_attempt_at
				WHEN eligibility_projection_jobs.status='queued'
					AND eligibility_projection_jobs.last_error_code='BALANCE_PROOF_PENDING'
					AND eligibility_projection_jobs.next_attempt_at<=now()+make_interval(secs=>$3)
				THEN eligibility_projection_jobs.next_attempt_at
				ELSE now()
			END,
			updated_at=CASE WHEN eligibility_projection_jobs.status IN ('dead','processing') THEN eligibility_projection_jobs.updated_at ELSE now() END`,
		sourceID, minWatermark, balanceProofPendingRequeueResetWindow.Seconds(),
		pendingReconciliationIdleMinMatches)
	if err != nil {
		return err
	}
	// Accounts with no facts in the interval are safely advanced in one UPDATE;
	// this covers users with zero events in one or all streams after an empty
	// signed scan cycle publishes its source-level watermark.
	_, err = tx.Exec(ctx, `
		WITH targets AS (
			SELECT eas.external_account_id,
				GREATEST(eas.cutover_at,$2::timestamptz-
					make_interval(secs=>eas.finalization_delay_seconds)) AS requested_through
			FROM source_account_eligibility_state eas
			WHERE eas.source_instance_id=$1 AND eas.catchup_key_hmac IS NULL
				AND eas.eligibility_status<>'syncing'
		)
		UPDATE source_account_eligibility_state eas
		SET finalized_through=t.requested_through,projection_version=eas.projection_version+1,updated_at=now()
		FROM targets t
		WHERE eas.external_account_id=t.external_account_id
			AND t.requested_through>eas.finalized_through
			AND NOT EXISTS (SELECT 1 FROM eligibility_projection_jobs j
				WHERE j.external_account_id=eas.external_account_id)`, sourceID, minWatermark)
	return err
}

func freezeSourceAccountsTx(ctx context.Context, tx pgx.Tx, sourceID, reason, objectType, objectID, revision string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `SELECT external_account_id FROM source_account_eligibility_state WHERE source_instance_id=$1 ORDER BY external_account_id FOR UPDATE`, sourceID)
	if err != nil {
		return err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = freezeEligibilityTx(ctx, tx, id, "", reason, objectType, objectID, revision, actor); err != nil {
			return err
		}
	}
	return nil
}

func tryPublishEconomicScanCyclesTx(ctx context.Context, tx pgx.Tx, sourceID, streamID string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT scan_cycle_id::text,stream_watermark_at,source_cursor,final_sequence,
			COALESCE(scan_snapshot_id,''),COALESCE(scan_snapshot_row_count,-1)
		FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id=$2 AND cycle_status='processing'
			AND final_sequence IS NOT NULL
		ORDER BY first_sequence FOR UPDATE`, sourceID, streamID)
	if err != nil {
		return err
	}
	type cycle struct {
		id, cursor, snapshotID string
		watermark              time.Time
		sequence, snapshotRows int64
	}
	cycles := make([]cycle, 0)
	for rows.Next() {
		var item cycle
		if err = rows.Scan(&item.id, &item.watermark, &item.cursor, &item.sequence,
			&item.snapshotID, &item.snapshotRows); err != nil {
			rows.Close()
			return err
		}
		cycles = append(cycles, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range cycles {
		var incomplete, manifestRecords, checkpointRecords int64
		// XM-INV-POLICY-ANCHOR 2.5: a failed/dead event no longer holds the
		// cycle once its account has already been frozen over it -- the
		// processor freezes before returning a plain (non-retryable) error on
		// payload drift/conflict, so by the time the event reaches
		// failed/dead an open freeze already exists. There is no ingest-layer
		// column identifying which account/fact a frozen, encrypted event
		// belongs to, so the correlation goes through source_revision_hash,
		// which every freeze call site sets to the triggering fact's revision
		// hash -- the same value stored as the ingest event's payload_hash.
		// A failed/dead event with no matching freeze (pure transient
		// exhaustion, retry budget not proven unrecoverable) still holds the
		// cycle.
		//
		// XM-INV-DEAD-CONTAINMENT: the freeze correlation is now rendered
		// from postgresstore's single definition (source_sync.go's
		// sourceEventContainedByOpenFreezeSQL) rather than spelled out here,
		// so this judgment and the four stream-health surfaces are physically
		// one rule. It also had to stop being a LEFT JOIN: two open freezes
		// can share a payload_hash (0009's unique index separates them by
		// reason, and EVENT_PAYLOAD_DRIFT plus EVENT_DEAD legitimately
		// coexist), which duplicated the event row and inflated the manifest
		// and checkpoint counts below -- enough to trip
		// invalidBalanceSnapshot and freeze the whole source with SOURCE_GAP
		// over a cycle that was in fact complete.
		err = tx.QueryRow(ctx, `
			SELECT count(*) FILTER (
					WHERE sie.processing_status NOT IN ('processed','parked_identity')
						AND NOT (sie.processing_status IN ('failed','dead') AND `+sourceEventContainedByOpenFreezeSQL("sie")+`)
				),
				count(*) FILTER (WHERE sie.entity_type='cutover_manifest'),
				count(*) FILTER (WHERE sie.entity_type='balance_checkpoint')
			FROM source_economic_scan_cycle_events m
			JOIN source_ingest_events sie ON sie.source_instance_id=m.source_instance_id
				AND sie.stream_id=m.stream_id AND sie.event_id=m.event_id
			WHERE m.source_instance_id=$1 AND m.stream_id=$2 AND m.scan_cycle_id=$3::uuid`,
			sourceID, streamID, item.id).Scan(&incomplete, &manifestRecords, &checkpointRecords)
		if err != nil {
			return err
		}
		if incomplete != 0 {
			continue
		}
		var configHash string
		var baselineRows int64
		err = tx.QueryRow(ctx, `
			SELECT configuration_hash,baseline_row_count FROM source_cutover_manifests
			WHERE source_instance_id=$1`, sourceID).Scan(&configHash, &baselineRows)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		invalidBalanceSnapshot := streamID == "balances" &&
			(item.snapshotRows < 0 || checkpointRecords != item.snapshotRows ||
				(manifestRecords > 0 && (manifestRecords != 1 || checkpointRecords != baselineRows)))
		if invalidBalanceSnapshot {
			if _, err = tx.Exec(ctx, `UPDATE source_economic_scan_cycles SET cycle_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid`, sourceID, streamID, item.id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE source_ingest_state SET projection_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, streamID); err != nil {
				return err
			}
			if err = freezeSourceAccountsTx(ctx, tx, sourceID, "SOURCE_GAP", "balance_snapshot_cycle",
				item.id, "", actor); err != nil {
				return err
			}
			continue
		}
		var oldAt time.Time
		var oldSequence int64
		var oldCursor string
		err = tx.QueryRow(ctx, `
			SELECT watermark_at,source_sequence,source_cursor
			FROM source_economic_stream_watermarks
			WHERE source_instance_id=$1 AND stream_kind=$2 FOR UPDATE`, sourceID, streamID).Scan(
			&oldAt, &oldSequence, &oldCursor)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		regressed := err == nil && (item.watermark.Before(oldAt) || item.sequence < oldSequence ||
			(item.watermark.Equal(oldAt) && item.sequence == oldSequence && item.cursor != oldCursor))
		if regressed {
			if _, err = tx.Exec(ctx, `UPDATE source_economic_scan_cycles SET cycle_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid`, sourceID, streamID, item.id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE source_ingest_state SET projection_status='blocked',updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2`, sourceID, streamID); err != nil {
				return err
			}
			if err = freezeSourceAccountsTx(ctx, tx, sourceID, "STREAM_WATERMARK_REGRESSION",
				"source_scan_cycle", item.id, "", actor); err != nil {
				return err
			}
			continue
		}
		command, err := tx.Exec(ctx, `
			INSERT INTO source_economic_stream_watermarks(
				source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
			VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET
				watermark_at=EXCLUDED.watermark_at,source_sequence=EXCLUDED.source_sequence,
				source_cursor=EXCLUDED.source_cursor,configuration_hash=EXCLUDED.configuration_hash,updated_at=now()`,
			sourceID, streamID, item.watermark, item.sequence, item.cursor, configHash)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE source_economic_scan_cycles SET cycle_status='published',published_at=now(),updated_at=now()
			WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid
				AND cycle_status='processing'`, sourceID, streamID, item.id)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		if err = finalizeSourceAccountsTx(ctx, tx, sourceID, actor); err != nil {
			return err
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.source_watermark.published", "source_scan_cycle",
			sourceID+"/"+streamID+"/"+item.id, nil, map[string]any{"watermark_at": item.watermark,
				"source_sequence": item.sequence}); err != nil {
			return err
		}
	}
	return nil
}

func completeEligibilityCatchupTx(ctx context.Context, tx pgx.Tx, sourceID, catchupKey string, actor AuditActor) error {
	if !dependencyHMACPattern.MatchString(catchupKey) {
		return domain.ErrConflict
	}
	var remaining int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM source_ingest_events
		WHERE source_instance_id=$1 AND catchup_key_hmac=$2 AND processing_status<>'processed'`,
		sourceID, catchupKey).Scan(&remaining); err != nil {
		return err
	}
	if remaining != 0 {
		return nil
	}
	// Every account still carrying this catch-up key is released, whatever
	// status it reached meanwhile -- not only the ones still 'syncing'
	// (XM-INV-CATCHUP-RELEASE).
	//
	// catchup_key_hmac is what finalizeSourceAccountsTx excludes an account
	// on, so an account that keeps it is excluded from finalization *forever*:
	// no projection job is enqueued for it, its balance checkpoints are never
	// evaluated, its finalized_through stops advancing, and the invoiceable
	// amount an operator sees freezes at whatever the last projection wrote.
	// Filtering on 'syncing' alone left exactly that hole: an account can
	// leave 'syncing' during its own catch-up -- the POLICY_ANCHOR bootstrap
	// parks one in not_invoiceable_pending_reconciliation the moment its
	// triggering checkpoint reports a negative balance, and an open freeze
	// makes another 'frozen' -- and those accounts were then skipped by their
	// own completion. Production found one: a live account stuck since
	// 2026-09-01 while every sibling finalized normally.
	rows, err := tx.Query(ctx, `
		SELECT external_account_id,eligibility_status FROM source_account_eligibility_state
		WHERE source_instance_id=$1 AND catchup_key_hmac=$2
		ORDER BY external_account_id FOR UPDATE`, sourceID, catchupKey)
	if err != nil {
		return err
	}
	type catchupAccount struct{ id, status string }
	accounts := make([]catchupAccount, 0)
	for rows.Next() {
		var item catchupAccount
		if err = rows.Scan(&item.id, &item.status); err != nil {
			rows.Close()
			return err
		}
		accounts = append(accounts, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		// Only an account that is still 'syncing' has its status decided here.
		// Any other status was reached by a rule that owns it -- a freeze, or
		// the self-clearing pending-reconciliation state -- and completing a
		// catch-up is not evidence about that rule, so the status is left
		// exactly as it stands and only the exclusion key is dropped.
		status := account.status
		if status == "syncing" {
			var openFreezes int64
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes WHERE external_account_id=$1 AND status='open'`, account.id).Scan(&openFreezes); err != nil {
				return err
			}
			status = "active"
			if openFreezes > 0 {
				status = "frozen"
			}
		}
		if _, err = tx.Exec(ctx, `
			UPDATE source_account_eligibility_state SET eligibility_status=$1,catchup_key_hmac=NULL,
				projection_version=projection_version+1,updated_at=now()
			WHERE external_account_id=$2`, status, account.id); err != nil {
			return err
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.identity_catchup.completed", "external_account", account.id,
			nil, map[string]any{"status": status, "status_decided_here": account.status == "syncing"}); err != nil {
			return err
		}
		ids = append(ids, account.id)
	}
	if len(ids) > 0 {
		return finalizeSourceAccountsTx(ctx, tx, sourceID, actor)
	}
	return nil
}

func expectedUnitForSource(sourceType domain.SourceType) string {
	if sourceType == domain.SourceSub2API {
		return "SUB2_BALANCE_1E8"
	}
	if sourceType == domain.SourceNewAPI {
		return "NEWAPI_QUOTA"
	}
	return ""
}

func resolveExternalAccountTx(ctx context.Context, tx pgx.Tx, sourceID, externalUserID string) (string, string, domain.SourceType, error) {
	var accountID, principalID string
	var sourceType domain.SourceType
	err := tx.QueryRow(ctx, `
		SELECT ea.id,ea.invoice_user_id,si.source_type
		FROM external_accounts ea JOIN source_instances si ON si.id=ea.source_instance_id
		WHERE ea.source_instance_id=$1 AND ea.external_user_id=$2
			AND ea.binding_status='verified' AND si.enabled
		FOR SHARE OF ea,si`, sourceID, externalUserID).Scan(&accountID, &principalID, &sourceType)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", domain.ErrNotFound
	}
	return accountID, principalID, sourceType, err
}

func getEligibilityAccountTx(ctx context.Context, tx pgx.Tx, accountID string, lock bool) (eligibilityAccount, error) {
	clause := ""
	if lock {
		clause = " FOR UPDATE OF eas"
	}
	var account eligibilityAccount
	var delaySeconds int
	err := tx.QueryRow(ctx, `
		SELECT eas.external_account_id,eas.source_instance_id,ea.invoice_user_id,eas.cutover_at,scm.cutover_at,
			policy.eligibility_start_at,policy.policy_version,
			eas.unit_code,eas.cutover_manifest_hash,scm.configuration_hash,eas.bootstrap_kind,eas.finalized_through,
			eas.finalization_delay_seconds,eas.eligibility_status,eas.projection_version
		FROM source_account_eligibility_state eas
		JOIN external_accounts ea ON ea.id=eas.external_account_id
		JOIN source_cutover_manifests scm ON scm.source_instance_id=eas.source_instance_id
		CROSS JOIN invoice_eligibility_policy policy
		WHERE eas.external_account_id=$1`+clause, accountID).Scan(
		&account.ExternalAccountID, &account.SourceInstanceID, &account.PrincipalID, &account.CutoverAt, &account.GlobalCutoverAt,
		&account.PolicyStartAt, &account.PolicyVersion,
		&account.UnitCode, &account.ManifestHash, &account.ConfigurationHash, &account.BootstrapKind, &account.FinalizedThrough,
		&delaySeconds, &account.Status, &account.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return eligibilityAccount{}, domain.ErrSourceUnavailable
	}
	account.Delay = time.Duration(delaySeconds) * time.Second
	return account, err
}

func verifyFactTrustTx(_ context.Context, _ pgx.Tx, account eligibilityAccount, manifestHash, configurationHash, unitCode, stream string, factWatermark time.Time) error {
	if account.ManifestHash != manifestHash || account.ConfigurationHash != configurationHash || account.UnitCode != unitCode {
		return domain.ErrConflict
	}
	// factWatermark is injected from the verified batch context. It may be
	// ahead of the published completeness watermark while records in that scan
	// cycle are still processing. Publishing happens only after the final page
	// and every record in the cycle has reached processed/idempotent state.
	if !validEconomicStream(stream) || factWatermark.IsZero() {
		return domain.ErrSourceUnavailable
	}
	return nil
}

func verifyFactBatchContextTx(ctx context.Context, tx pgx.Tx, sourceID, streamID, eventID,
	batchID, scanCycleID, revision string, watermarkAt time.Time) (string, error) {
	if !validEconomicStream(streamID) || strings.TrimSpace(eventID) == "" ||
		strings.TrimSpace(batchID) == "" || strings.TrimSpace(scanCycleID) == "" ||
		!validHash(revision) || watermarkAt.IsZero() {
		return "", domain.ErrForbidden
	}
	var storedHash, signingKeyID, cycleStatus string
	var storedWatermark time.Time
	// Lock only the mutable scan-cycle row. source_ingest_batches and the
	// cycle-event binding are append-only; locking b would require UPDATE on an
	// intentionally immutable batch table and break the least-privilege runtime
	// role without adding any consistency guarantee.
	err := tx.QueryRow(ctx, `
		SELECT m.payload_hash,b.signing_key_id,b.scan_ceiling_at,c.cycle_status
		FROM source_economic_scan_cycle_events m
		JOIN source_ingest_batches b ON b.source_instance_id=m.source_instance_id
			AND b.stream_id=m.stream_id AND b.batch_id=m.batch_id
		JOIN source_economic_scan_cycles c ON c.source_instance_id=m.source_instance_id
			AND c.stream_id=m.stream_id AND c.scan_cycle_id=m.scan_cycle_id
		WHERE m.source_instance_id=$1 AND m.stream_id=$2 AND m.event_id=$3::uuid
			AND m.batch_id=$4::uuid AND m.scan_cycle_id=$5::uuid
			AND b.schema_version='3.0' FOR SHARE OF c`, sourceID, streamID, eventID,
		batchID, scanCycleID).Scan(&storedHash, &signingKeyID, &storedWatermark, &cycleStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if storedHash != revision || !storedWatermark.Equal(watermarkAt) ||
		(cycleStatus != "receiving" && cycleStatus != "processing" && cycleStatus != "published") {
		return "", domain.ErrConflict
	}
	return signingKeyID, nil
}

func (s *Store) ValidateEconomicFactContext(ctx context.Context, sourceID, streamID, eventID,
	batchID, scanCycleID, revision string, watermarkAt time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = verifyFactBatchContextTx(ctx, tx, sourceID, streamID, eventID, batchID,
		scanCycleID, revision, watermarkAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ObserveBalanceCheckpoint(ctx context.Context, in BalanceCheckpointObservation, actor AuditActor) error {
	if err := validateFactMetadata(in.SourceInstanceID, in.ExternalUserID, in.ExternalEventID,
		in.SourceRevision, in.SourceCursor, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, in.AsOf, in.ObservedAt, in.StreamWatermarkAt, in.SourceSequence); err != nil {
		return err
	}
	if strings.TrimSpace(in.CheckpointID) == "" || len(in.CheckpointID) > 256 ||
		(in.CheckpointKind != "cutover" && in.CheckpointKind != "reconciliation") {
		return errors.New("invalid balance checkpoint")
	}
	balance, err := parseUnsignedUnits(in.BalanceServiceUnits, "balance service units", true)
	if err != nil {
		return err
	}
	if in.BalanceNegative && balance.Sign() != 0 {
		return errors.New("negative balance checkpoint must carry zero service units")
	}
	// XM-INV-NEGATIVE-DEFICIT: the magnitude of a negative balance travels
	// beside the flag. nil is "unknown" (evidence sealed before the bridge
	// reported it) and is stored as NULL, never as zero.
	var deficit any
	if in.DeficitServiceUnits != nil {
		parsed, deficitErr := parseUnsignedUnits(*in.DeficitServiceUnits, "balance deficit service units", true)
		if deficitErr != nil {
			return deficitErr
		}
		if (parsed.Sign() > 0) != in.BalanceNegative {
			return errors.New("balance checkpoint deficit disagrees with its negative flag")
		}
		deficit = parsed.String()
	}
	snapshotRows, err := parseUnsignedUnits(in.SnapshotRowCount, "snapshot row count", true)
	if err != nil || !snapshotRows.IsInt64() || snapshotRows.Int64() > 2_000_000 || !validHash(in.SourceSnapshotID) {
		return errors.New("invalid balance snapshot row count")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	accountID, principalID, sourceType, err := resolveExternalAccountTx(ctx, tx, in.SourceInstanceID, in.ExternalUserID)
	if err != nil {
		return err
	}
	if in.UnitCode != expectedUnitForSource(sourceType) {
		return domain.ErrConflict
	}
	// XM-INV-PROOF-CONTENTION 2: try, don't block. processEligibilityProjectionJob
	// holds this same per-account lock for its write phase; a source-projection
	// worker call that instead waited here risked a lock-timeout error deep
	// inside its own transaction, and one such failure used to abort the
	// worker's whole batch (see RunOnce, application/source_processor.go).
	// A busy account is routine, expected contention -- the caller reschedules
	// this claim shortly (MarkSourceEventBusy) without spending its retry budget.
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,43))`, accountID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return domain.ErrAccountLockBusy
	}
	if _, err = verifyFactBatchContextTx(ctx, tx, in.SourceInstanceID, "balances", in.ExternalEventID,
		in.BatchID, in.ScanCycleID, in.SourceRevision, in.StreamWatermarkAt); err != nil {
		return err
	}
	var cycleSnapshotID string
	var cycleSnapshotRows int64
	if err = tx.QueryRow(ctx, `SELECT scan_snapshot_id,scan_snapshot_row_count
		FROM source_economic_scan_cycles WHERE source_instance_id=$1 AND stream_id='balances'
			AND scan_cycle_id=$2::uuid FOR SHARE`, in.SourceInstanceID, in.ScanCycleID).Scan(
		&cycleSnapshotID, &cycleSnapshotRows); err != nil {
		return err
	}
	if cycleSnapshotID != in.SourceSnapshotID || cycleSnapshotRows != snapshotRows.Int64() {
		return domain.ErrConflict
	}
	var carryProofKey string
	err = tx.QueryRow(ctx, `SELECT proof_key FROM balance_carry_forward_proofs
		WHERE external_account_id=$1 AND scan_cycle_id=$2::uuid`, accountID, in.ScanCycleID).Scan(&carryProofKey)
	if err == nil {
		if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_PAYLOAD_DRIFT",
			"balance_carry_forward_proof", carryProofKey, in.SourceRevision, actor); freezeErr != nil {
			return freezeErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return domain.ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var manifest CutoverManifest
	err = tx.QueryRow(ctx, `
		SELECT source_instance_id,manifest_hash,source_runtime_version,projection_contract,configuration_hash,unit_code,
			payments_ceiling,usage_ceiling,credits_ceiling,balances_ceiling,
			baseline_snapshot_id,baseline_snapshot_hash,baseline_row_count,
			signing_key_id,cutover_at,database_clock
		FROM source_cutover_manifests WHERE source_instance_id=$1`, in.SourceInstanceID).Scan(
		&manifest.SourceInstanceID, &manifest.ManifestHash, &manifest.SourceRuntimeVersion, &manifest.ProjectionContract, &manifest.ConfigurationHash,
		&manifest.UnitCode, &manifest.PaymentsCeiling, &manifest.UsageCeiling, &manifest.CreditsCeiling,
		&manifest.BalancesCeiling, &manifest.BaselineSnapshotID, &manifest.BaselineSnapshotHash,
		&manifest.BaselineRowCount, &manifest.SigningKeyID, &manifest.CutoverAt, &manifest.DatabaseClock)
	if err != nil {
		return domain.ErrSourceUnavailable
	}
	if manifest.ManifestHash != in.CutoverManifestHash || manifest.ConfigurationHash != in.ConfigurationHash ||
		manifest.UnitCode != in.UnitCode {
		return domain.ErrConflict
	}
	var existingHash string
	err = tx.QueryRow(ctx, `
		SELECT source_revision_hash FROM balance_reconciliation_checkpoints
		WHERE source_instance_id=$1 AND (external_event_id=$2 OR checkpoint_id=$3)`,
		in.SourceInstanceID, in.ExternalEventID, in.CheckpointID).Scan(&existingHash)
	if err == nil {
		if existingHash != in.SourceRevision {
			if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_PAYLOAD_DRIFT", "balance_checkpoint", in.CheckpointID, in.SourceRevision, actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if in.CheckpointKind == "cutover" {
		if !in.BaselineMember || !in.AsOf.Equal(manifest.CutoverAt) || !validHash(in.BaselineSnapshotID) ||
			in.BaselineSnapshotID != manifest.BaselineSnapshotHash || in.SourceSnapshotID != manifest.BaselineSnapshotHash ||
			snapshotRows.Int64() != manifest.BaselineRowCount {
			return domain.ErrConflict
		}
		if _, stateErr := getEligibilityAccountTx(ctx, tx, accountID, true); !errors.Is(stateErr, domain.ErrSourceUnavailable) {
			if stateErr != nil {
				return stateErr
			}
			return domain.ErrConflict
		}
		eligibilityStatus := "active"
		if in.CatchupKeyHMAC != "" {
			if !dependencyHMACPattern.MatchString(in.CatchupKeyHMAC) {
				return errors.New("invalid catch-up dependency key")
			}
			eligibilityStatus = "syncing"
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO source_account_eligibility_state(
				external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
				cutover_manifest_hash,finalized_through,finalization_delay_seconds,catchup_key_hmac,eligibility_status)
			VALUES($1,$2,$3,$4,$5::numeric,$6,$3,$7,NULLIF($8,''),$9)`, accountID, in.SourceInstanceID,
			manifest.CutoverAt.UTC(), in.UnitCode, balance.String(), in.CutoverManifestHash,
			int(defaultEligibilityFinalizationDelay/time.Second), in.CatchupKeyHMAC, eligibilityStatus)
		if err != nil {
			return err
		}
		checkpointID := randomUUID()
		_, err = tx.Exec(ctx, `
			INSERT INTO balance_reconciliation_checkpoints(
				id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
				checkpoint_kind,baseline_snapshot_id,baseline_member,source_snapshot_id,snapshot_row_count,
				as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,
				configuration_hash,reconciliation_status,source_sequence,source_cursor,
				stream_watermark_at,source_revision_hash,observed_at,deficit_service_units)
			VALUES($1,$2,$3,$4,$5,'cutover',$6,TRUE,$7,$8,$9,$10::numeric,$11,$12,$13,$14,
				'cutover_baseline',$15,$16,$17,$18,$19,$20::numeric)`, checkpointID, in.SourceInstanceID,
			accountID, in.ExternalEventID, in.CheckpointID, in.BaselineSnapshotID,
			in.SourceSnapshotID, snapshotRows.Int64(), in.AsOf.UTC(), balance.String(),
			in.BalanceNegative, in.UnitCode, in.CutoverManifestHash, in.ConfigurationHash, in.SourceSequence,
			in.SourceCursor, in.StreamWatermarkAt.UTC(), in.SourceRevision, in.ObservedAt.UTC(), deficit)
		if err != nil {
			return err
		}
		if in.BalanceNegative {
			// XM-INV-ELIG-AUTO-RECONCILE: a negative opening balance at
			// bootstrap no longer opens a manual eligibility_freezes row --
			// see enterPendingReconciliationTx's doc comment.
			detail := fmt.Sprintf("balance_checkpoint %s at %s reported a negative balance at account bootstrap",
				in.CheckpointID, in.AsOf.UTC().Format(time.RFC3339Nano))
			if err = enterPendingReconciliationTx(ctx, tx, accountID, "UNKNOWN_NEGATIVE_BALANCE",
				"balance_checkpoint", in.CheckpointID, detail, false, actor); err != nil {
				return err
			}
		}
		legacyID := randomUUID()
		_, err = tx.Exec(ctx, `
			INSERT INTO source_credit_events(
				id,source_instance_id,external_account_id,external_event_id,external_credit_id,
				event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
				credit_kind,source_sequence,source_cursor,stream_watermark_at,
				source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,
				'LEGACY_NON_INVOICEABLE',0,$11,$6,$12,$13)`, legacyID, in.SourceInstanceID,
			accountID, "legacy:"+in.ExternalEventID, "legacy:"+in.CheckpointID, in.AsOf.UTC(),
			balance.String(), in.UnitCode, in.CutoverManifestHash, in.ConfigurationHash,
			in.SourceCursor, in.SourceRevision, in.ObservedAt.UTC())
		if err != nil {
			return err
		}
		// Per-account watermarks are diagnostic only. Source-scoped rows are the
		// completeness authority used by finalizeSourceAccountsTx.
		for _, watermark := range in.Watermarks {
			if !validEconomicStream(watermark.StreamKind) || watermark.WatermarkAt.Before(manifest.CutoverAt) ||
				strings.TrimSpace(watermark.SourceCursor) == "" {
				return errors.New("invalid cutover account watermark")
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO source_account_stream_watermarks(
					external_account_id,stream_kind,watermark_at,source_sequence,source_cursor)
				VALUES($1,$2,$3,$4,$5)
				ON CONFLICT(external_account_id,stream_kind) DO UPDATE SET
					watermark_at=EXCLUDED.watermark_at,source_sequence=EXCLUDED.source_sequence,
					source_cursor=EXCLUDED.source_cursor,updated_at=now()`, accountID,
				watermark.StreamKind, watermark.WatermarkAt.UTC(), watermark.SourceSequence,
				watermark.SourceCursor)
			if err != nil {
				return err
			}
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.account_cutover.created", "external_account", accountID,
			nil, map[string]any{"principal_id": principalID, "manifest_hash": manifest.ManifestHash,
				"cutover_at": manifest.CutoverAt}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if errors.Is(err, domain.ErrSourceUnavailable) && in.CheckpointKind == "reconciliation" {
		var policyStartAt time.Time
		if err = tx.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
			WHERE singleton_id=1`).Scan(&policyStartAt); err != nil {
			return err
		}
		if manifest.CutoverAt.Before(policyStartAt) && in.AsOf.Before(policyStartAt) {
			// XM-INV-POLICY-ANCHOR 2.1: a reconciliation checkpoint dated
			// before the invoice policy start carries no eligibility-relevant
			// information for an account that has not bootstrapped yet -- the
			// account cannot become invoice-eligible before the policy start
			// regardless of which row eventually establishes its cutover
			// boundary. Acknowledge it instead of parking on the (possibly
			// long-delayed) signed cutover row.
			if err = writeAudit(ctx, tx, actor, "eligibility.pre_policy_checkpoint.skipped",
				"external_account", accountID, nil, map[string]any{"checkpoint_id": in.CheckpointID,
					"as_of": in.AsOf, "baseline_member": in.BaselineMember,
					"policy_start_at": policyStartAt}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		// XM-INV-POLICY-ANCHOR 2.1 (superseding the original POST_CUTOVER_REPLAY
		// non-baseline path -- per the team lead's follow-up decision, after
		// this slice no code path creates a new SIGNED_CUTOVER or
		// POST_CUTOVER_REPLAY row, baseline member or not; a signed cutover
		// row for a no-state account is just a pre-policy checkpoint like any
		// other and is skipped above). This reconciliation checkpoint is
		// at/after the policy start, so bootstrap directly from it instead of
		// waiting for the (possibly indefinitely parked) signed cutover row.
		//
		// XM-INV-ELIG-POLICY-START-ANCHOR (design XM-INV-ELIG-SIMPLIFY section
		// 3(D)): cutover_at is no longer this triggering checkpoint's own
		// as_of -- it is unconditionally policyStartAt, so that facts dated
		// between the policy start and this checkpoint (previously a dead
		// zone: discarded by observeEligibilityFact's cutover-boundary check
		// and excluded from buildEligibilityProjectionTx's window) are
		// persisted and projected normally from now on. cutover_balance_units
		// is derived by unwinding this checkpoint's own observed balance
		// backward across that window (deriveCutoverBalanceUnitsTx) --
		// exactly what the balance would have been at the policy start, using
		// only facts already persisted in the window at this moment. To keep
		// migration 0016's anchor-checkpoint validation intact (cutover_at/
		// cutover_balance_units must exactly match a real reconciliation
		// checkpoint row), a second checkpoint row is inserted at as_of=
		// policyStartAt with the derived balance, borrowing this triggering
		// checkpoint's own provenance columns -- the same technique 2.7/2.8's
		// synthesizeUnknownPositive already uses for a value that is derived,
		// not directly observed (see
		// synthesizePolicyStartReconciliationCheckpointTx). The state row
		// must still be inserted before either checkpoint row: the checkpoint
		// table's own (unchanged, immediate)
		// balance_reconciliation_checkpoints_contract_guard trigger requires
		// a trusted state row to already exist, while this state row's own
		// POLICY_ANCHOR validation (migration 0016, extended by 0021) is
		// deferred to COMMIT specifically so it can, in turn, require the
		// derived checkpoint row to exist by then -- insertion order between
		// the two checkpoint rows themselves does not matter, since both are
		// only checked at COMMIT.
		policyStartAt = policyStartAt.UTC()
		derivedBalance, err := deriveCutoverBalanceUnitsTx(ctx, tx, accountID, policyStartAt, in.AsOf, balance)
		if err != nil {
			return err
		}
		eligibilityStatus := "active"
		if in.CatchupKeyHMAC != "" {
			if !dependencyHMACPattern.MatchString(in.CatchupKeyHMAC) {
				return errors.New("invalid catch-up dependency key")
			}
			eligibilityStatus = "syncing"
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO source_account_eligibility_state(
				external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
				cutover_manifest_hash,bootstrap_kind,finalized_through,finalization_delay_seconds,
				catchup_key_hmac,eligibility_status)
			VALUES($1,$2,$3,$4,$5::numeric,$6,'POLICY_ANCHOR',$3,$7,NULLIF($8,''),$9)`,
			accountID, in.SourceInstanceID, policyStartAt, in.UnitCode, derivedBalance.String(),
			in.CutoverManifestHash, int(defaultEligibilityFinalizationDelay/time.Second),
			in.CatchupKeyHMAC, eligibilityStatus)
		if err != nil {
			return err
		}
		checkpointID := randomUUID()
		_, err = tx.Exec(ctx, `
			INSERT INTO balance_reconciliation_checkpoints(
				id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
				checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
				as_of,balance_service_units,balance_negative,unit_code,
				cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
				source_cursor,stream_watermark_at,source_revision_hash,observed_at,deficit_service_units)
			VALUES($1,$2,$3,$4,$5,'reconciliation',$6,$7,$8,$9,$10::numeric,$11,$12,$13,
				$14,'cutover_baseline',$15,$16,$17,$18,$19,$20::numeric)`, checkpointID, in.SourceInstanceID,
			accountID, in.ExternalEventID, in.CheckpointID, in.BaselineMember, in.SourceSnapshotID,
			snapshotRows.Int64(), in.AsOf.UTC(), balance.String(),
			in.BalanceNegative, in.UnitCode, in.CutoverManifestHash, in.ConfigurationHash,
			in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(), in.SourceRevision,
			in.ObservedAt.UTC(), deficit)
		if err != nil {
			return err
		}
		if err = synthesizePolicyStartReconciliationCheckpointTx(ctx, tx, accountID, in.SourceInstanceID,
			in.UnitCode, in.CutoverManifestHash, in.ConfigurationHash, in.CheckpointID, in.ExternalEventID,
			policyStartAt, derivedBalance, in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt,
			in.SourceRevision, in.ObservedAt); err != nil {
			return err
		}
		if in.BalanceNegative {
			// XM-INV-ELIG-AUTO-RECONCILE: see the SIGNED_CUTOVER bootstrap
			// branch above -- identical treatment for a POLICY_ANCHOR
			// account's own opening balance. Keyed off the triggering
			// checkpoint's own reported negative flag, not the (always
			// non-negative, floored) derived balance -- see
			// deriveCutoverBalanceUnitsTx's own doc comment on the floor.
			detail := fmt.Sprintf("balance_checkpoint %s at %s reported a negative balance at account bootstrap",
				in.CheckpointID, in.AsOf.UTC().Format(time.RFC3339Nano))
			if err = enterPendingReconciliationTx(ctx, tx, accountID, "UNKNOWN_NEGATIVE_BALANCE",
				"balance_checkpoint", in.CheckpointID, detail, false, actor); err != nil {
				return err
			}
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.policy_anchor.bootstrapped", "external_account", accountID,
			nil, map[string]any{"cutover_at": policyStartAt, "opening_units": derivedBalance.String(),
				"triggering_checkpoint_id": in.CheckpointID, "triggering_checkpoint_as_of": in.AsOf,
				"triggering_checkpoint_balance": balance.String(),
				"baseline_member":               in.BaselineMember, "status": eligibilityStatus}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if err = verifyFactTrustTx(ctx, tx, account, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, "balances", in.StreamWatermarkAt); err != nil {
		return err
	}
	// A checkpoint never finalizes itself. Store the immutable fact, then let
	// the bounded worker evaluate it only after the source-wide four-stream
	// completeness boundary has crossed as_of.
	status := "pending_finalization"
	checkpointID := randomUUID()
	_, err = tx.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,baseline_member,source_snapshot_id,snapshot_row_count,
			as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,configuration_hash,
			reconciliation_status,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at,deficit_service_units)
		VALUES($1,$2,$3,$4,$5,'reconciliation',$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,
			$15,$16,$17,$18,$19,$20,$21::numeric)`, checkpointID, in.SourceInstanceID,
		accountID, in.ExternalEventID, in.CheckpointID, in.BaselineMember,
		in.SourceSnapshotID, snapshotRows.Int64(), in.AsOf.UTC(), balance.String(),
		in.BalanceNegative, in.UnitCode, in.CutoverManifestHash,
		in.ConfigurationHash, status, in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(),
		in.SourceRevision, in.ObservedAt.UTC(), deficit)
	if err != nil {
		return err
	}
	if !in.AsOf.After(account.FinalizedThrough) {
		_, err = tx.Exec(ctx, `
			INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
			VALUES($1,$2,'queued',now())
			ON CONFLICT(external_account_id) DO UPDATE SET
				requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
				-- XM-INV-PROJECTION-FAILURE-GRADING: see the identical guard's
				-- own comment in finalizeSourceAccountsTx above -- a new fact
				-- must never silently revive a status='dead' job.
				lease_token=CASE WHEN eligibility_projection_jobs.status='processing' THEN eligibility_projection_jobs.lease_token END,
				lease_expires_at=CASE WHEN eligibility_projection_jobs.status='processing' THEN eligibility_projection_jobs.lease_expires_at END,
				next_attempt_at=CASE
					WHEN eligibility_projection_jobs.status IN ('dead','processing') THEN eligibility_projection_jobs.next_attempt_at
					WHEN eligibility_projection_jobs.status='queued'
						AND eligibility_projection_jobs.last_error_code='BALANCE_PROOF_PENDING'
						AND eligibility_projection_jobs.next_attempt_at<=now()+make_interval(secs=>$3)
					THEN eligibility_projection_jobs.next_attempt_at
					ELSE now()
				END,
				updated_at=CASE WHEN eligibility_projection_jobs.status IN ('dead','processing') THEN eligibility_projection_jobs.updated_at ELSE now() END`,
			accountID, account.FinalizedThrough, balanceProofPendingRequeueResetWindow.Seconds())
		if err != nil {
			return err
		}
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.balance_checkpoint.observed", "external_account", accountID,
		nil, map[string]any{"checkpoint_id": in.CheckpointID, "status": status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// deriveCutoverBalanceUnitsTx implements design XM-INV-ELIG-SIMPLIFY section
// 3(D)'s policy-start balance derivation: checkpointBalance (a real,
// observed reconciliation checkpoint's own balance at checkpointAsOf),
// unwound backward across the window (policyStartAt, checkpointAsOf] by
// subtracting every inflow already persisted in that window at the moment
// this runs -- every non-cash credit AND every verified cash payment (in the
// same service units buildEligibilityProjectionTx's own cash-pool query
// already reads off funding_lot_consumption_state.cash_service_units, not a
// minor-unit amount) -- and adding back every usage fact, the exact inverse
// of buildEligibilityProjectionTx's own window predicate (event_time/
// completed_at>cutover_at for all three fact kinds). Once cutover_at becomes
// policyStartAt and this derived value becomes cutover_balance_units, the
// window's own credits/payments/usage are exactly what
// buildEligibilityProjectionTx will independently (re)count once its window
// opens up to include them, so re-evaluating the real checkpoint this was
// derived from reconciles to zero -- see evaluatePendingBalanceEvidenceTx's
// case-1 self-heal (design XM-INV-ANCHOR-BALANCE 2.7), which turns the
// earlier, empty-window derived checkpoint's own positive difference into
// exactly this credit.
//
// The cash-payment term was missing from an earlier draft of this function
// (matching the design doc's own prose, which named only "credits" without
// spelling out that a real top-up is also an inflow that must be unwound).
// Found to be a live, not merely theoretical, gap during review: any account
// whose very first real cash payment lands between the policy start and its
// first observed checkpoint -- ordinary for a brand-new user who registers,
// recharges, and is then first seen at a checkpoint -- would otherwise have
// its derived opening balance inflated by exactly that payment's own units,
// since the payment's own funding lot independently enters
// buildEligibilityProjectionTx's cash pool once the window includes it.
// Without this term, the account's very next real checkpoint would show a
// permanent negative difference of that same amount, landing the account in
// not_invoiceable_pending_reconciliation for a difference that was never
// real. Included here identically to how credits are handled: read
// (verified, non-refund-frozen WALLET_CASH only, matching that same
// cash-pool query's own predicate exactly -- SUBSCRIPTION_CASH is excluded
// from that pool today and so is excluded here too), summed, subtracted.
//
// Two callers: ObserveBalanceCheckpoint's POLICY_ANCHOR bootstrap branch
// (checkpointAsOf/checkpointBalance are the triggering checkpoint's own
// as_of/balance), and the policy-start-reanchor repair tool (checkpointAsOf/
// checkpointBalance are the account's *existing* cutover_at/
// cutover_balance_units, treated as a checkpoint data point for the same
// unwind) -- both derive the same way from whatever is persisted in the
// window right now.
//
// Floors at zero rather than going negative: cutover_balance_units carries a
// NOT NULL CHECK (>=0). An unwind that would need special handling for
// arriving at a hypothetically-negative implied opening balance is exactly
// the "every branch returns a defined result" hard rule -- flooring, not
// erroring, on a data shape not known to occur in production today (window
// inflows exceeding checkpoint balance plus usage).
func deriveCutoverBalanceUnitsTx(ctx context.Context, tx pgx.Tx, accountID string, policyStartAt, checkpointAsOf time.Time, checkpointBalance *big.Int) (*big.Int, error) {
	var creditsText, usageText, paymentsText string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(service_units),0)::text FROM source_credit_events
		WHERE external_account_id=$1 AND event_time>$2 AND event_time<=$3`,
		accountID, policyStartAt, checkpointAsOf).Scan(&creditsText); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(service_units),0)::text FROM source_usage_events
		WHERE external_account_id=$1 AND event_time>$2 AND event_time<=$3`,
		accountID, policyStartAt, checkpointAsOf).Scan(&usageText); err != nil {
		return nil, err
	}
	// Matches buildEligibilityProjectionExcludingUsageTx's own cash-pool
	// query predicate exactly (eligibility_kind='WALLET_CASH',
	// verification_state='verified', refund_frozen=FALSE) so this counts
	// precisely the payments that query will independently include once the
	// window opens up around them.
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(flcs.cash_service_units),0)::text
		FROM funding_lots fl JOIN funding_lot_consumption_state flcs ON flcs.funding_lot_id=fl.id
		WHERE fl.external_account_id=$1 AND fl.eligibility_kind='WALLET_CASH'
			AND fl.verification_state='verified' AND fl.refund_frozen=FALSE
			AND fl.completed_at>$2 AND fl.completed_at<=$3`,
		accountID, policyStartAt, checkpointAsOf).Scan(&paymentsText); err != nil {
		return nil, err
	}
	credits, ok := new(big.Int).SetString(strings.TrimSpace(creditsText), 10)
	if !ok {
		return nil, fmt.Errorf("invalid summed credit units %q", creditsText)
	}
	usage, ok := new(big.Int).SetString(strings.TrimSpace(usageText), 10)
	if !ok {
		return nil, fmt.Errorf("invalid summed usage units %q", usageText)
	}
	payments, ok := new(big.Int).SetString(strings.TrimSpace(paymentsText), 10)
	if !ok {
		return nil, fmt.Errorf("invalid summed payment units %q", paymentsText)
	}
	derived := new(big.Int).Sub(checkpointBalance, credits)
	derived.Sub(derived, payments)
	derived.Add(derived, usage)
	if derived.Sign() < 0 {
		derived = new(big.Int)
	}
	return derived, nil
}

// synthesizePolicyStartReconciliationCheckpointTx inserts the derived
// reconciliation checkpoint row design XM-INV-ELIG-SIMPLIFY section 3(D)
// requires migration 0016/0021's POLICY_ANCHOR validation to find at COMMIT:
// checkpoint_kind='reconciliation', as_of=policyStartAt,
// balance_service_units=derivedBalance, borrowing the real checkpoint this
// derivation is anchored to (checkpointIDSeed/externalEventIDSeed and the
// five provenance columns: source_sequence/source_cursor/
// stream_watermark_at/source_revision_hash/observed_at) -- the same
// technique 2.7/2.8's synthesizeUnknownPositive already uses for a value
// that is derived, not directly observed. checkpointIDSeed/
// externalEventIDSeed are prefixed so this synthetic row's own business keys
// never collide with the real checkpoint's; ON CONFLICT DO NOTHING is
// defensive idempotency (mirroring synthesizeUnknownPositive's own choice),
// not expected to fire in practice -- both callers only reach this once per
// real checkpoint/account.
func synthesizePolicyStartReconciliationCheckpointTx(ctx context.Context, tx pgx.Tx, accountID, sourceInstanceID,
	unitCode, manifestHash, configurationHash, checkpointIDSeed, externalEventIDSeed string, policyStartAt time.Time,
	derivedBalance *big.Int, sourceSequence int64, sourceCursor string, streamWatermarkAt time.Time,
	sourceRevisionHash string, observedAt time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,baseline_member,as_of,balance_service_units,balance_negative,unit_code,
			cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,'reconciliation',FALSE,$6,$7::numeric,FALSE,$8,$9,$10,
			'cutover_baseline',$11,$12,$13,$14,$15)
		ON CONFLICT(source_instance_id,external_event_id) DO NOTHING`,
		randomUUID(), sourceInstanceID, accountID, "policy-start:"+externalEventIDSeed,
		"policy-start:"+checkpointIDSeed, policyStartAt.UTC(), derivedBalance.String(), unitCode,
		manifestHash, configurationHash, sourceSequence, sourceCursor, streamWatermarkAt.UTC(),
		sourceRevisionHash, observedAt.UTC())
	return err
}

func nullableBigInt(value *big.Int) any {
	if value == nil {
		return nil
	}
	return value.String()
}

func (s *Store) ObserveUsageEvent(ctx context.Context, in UsageObservation, actor AuditActor) error {
	if err := validateFactMetadata(in.SourceInstanceID, in.ExternalUserID, in.ExternalEventID,
		in.SourceRevision, in.SourceCursor, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, in.EventTime, in.ObservedAt, in.StreamWatermarkAt, in.SourceSequence); err != nil {
		return err
	}
	units, err := parseUnsignedUnits(in.ServiceUnits, "usage service units", false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.ExternalUsageID) == "" || len(in.ExternalUsageID) > 256 || in.BillingScope != "wallet" {
		return errors.New("invalid wallet usage fact")
	}
	domainValue, order, err := parseOptionalCausal(in.CausalDomain, in.CausalOrder)
	if err != nil {
		return err
	}
	return s.observeEligibilityFact(ctx, eligibilityFactObservation{
		SourceInstanceID: in.SourceInstanceID, ExternalUserID: in.ExternalUserID,
		ExternalEventID: in.ExternalEventID, ExternalObjectID: in.ExternalUsageID,
		EventTime: in.EventTime, ObservedAt: in.ObservedAt, StreamWatermarkAt: in.StreamWatermarkAt,
		Units: units, UnitCode: in.UnitCode, Kind: "usage", DetailKind: in.BillingScope,
		CausalDomain: domainValue, CausalOrder: order, SourceCursor: in.SourceCursor,
		SourceRevision: in.SourceRevision, CutoverManifestHash: in.CutoverManifestHash,
		ConfigurationHash: in.ConfigurationHash, SourceSequence: in.SourceSequence,
		BatchID: in.BatchID, ScanCycleID: in.ScanCycleID,
	}, actor)
}

func (s *Store) ObserveCreditEvent(ctx context.Context, in CreditObservation, actor AuditActor) error {
	if err := validateFactMetadata(in.SourceInstanceID, in.ExternalUserID, in.ExternalEventID,
		in.SourceRevision, in.SourceCursor, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, in.EventTime, in.ObservedAt, in.StreamWatermarkAt, in.SourceSequence); err != nil {
		return err
	}
	units, err := parseUnsignedUnits(in.ServiceUnits, "credit service units", false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.ExternalCreditID) == "" || len(in.ExternalCreditID) > 256 ||
		(in.CreditKind != "BONUS" && in.CreditKind != "REBATE" && in.CreditKind != "ADMIN" && in.CreditKind != "UNKNOWN_POSITIVE") {
		return errors.New("invalid non-cash credit fact")
	}
	domainValue, order, err := parseOptionalCausal(in.CausalDomain, in.CausalOrder)
	if err != nil {
		return err
	}
	return s.observeEligibilityFact(ctx, eligibilityFactObservation{
		SourceInstanceID: in.SourceInstanceID, ExternalUserID: in.ExternalUserID,
		ExternalEventID: in.ExternalEventID, ExternalObjectID: in.ExternalCreditID,
		EventTime: in.EventTime, ObservedAt: in.ObservedAt, StreamWatermarkAt: in.StreamWatermarkAt,
		Units: units, UnitCode: in.UnitCode, Kind: "credit", DetailKind: in.CreditKind,
		CausalDomain: domainValue, CausalOrder: order, SourceCursor: in.SourceCursor,
		SourceRevision: in.SourceRevision, CutoverManifestHash: in.CutoverManifestHash,
		ConfigurationHash: in.ConfigurationHash, SourceSequence: in.SourceSequence,
		BatchID: in.BatchID, ScanCycleID: in.ScanCycleID,
	}, actor)
}

type eligibilityFactObservation struct {
	SourceInstanceID, ExternalUserID, ExternalEventID, ExternalObjectID  string
	EventTime, ObservedAt, StreamWatermarkAt                             time.Time
	Units                                                                *big.Int
	UnitCode, Kind, DetailKind, CausalDomain                             string
	CausalOrder                                                          *big.Int
	SourceCursor, SourceRevision, CutoverManifestHash, ConfigurationHash string
	SourceSequence                                                       int64
	BatchID, ScanCycleID                                                 string
}

func (s *Store) observeEligibilityFact(ctx context.Context, in eligibilityFactObservation, actor AuditActor) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	accountID, _, sourceType, err := resolveExternalAccountTx(ctx, tx, in.SourceInstanceID, in.ExternalUserID)
	if err != nil {
		return err
	}
	if in.UnitCode != expectedUnitForSource(sourceType) {
		return domain.ErrConflict
	}
	// XM-INV-PROOF-CONTENTION 2: try, don't block -- see the identical
	// comment in ObserveBalanceCheckpoint above.
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,43))`, accountID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return domain.ErrAccountLockBusy
	}
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	stream := "credits"
	if in.Kind == "usage" {
		stream = "usage"
	}
	if _, err = verifyFactBatchContextTx(ctx, tx, in.SourceInstanceID, stream, in.ExternalEventID,
		in.BatchID, in.ScanCycleID, in.SourceRevision, in.StreamWatermarkAt); err != nil {
		return err
	}
	if err = verifyFactTrustTx(ctx, tx, account, in.CutoverManifestHash, in.ConfigurationHash,
		in.UnitCode, stream, in.StreamWatermarkAt); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "UNIT_MISMATCH", in.Kind, in.ExternalObjectID, in.SourceRevision, actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
		}
		return err
	}
	if !in.EventTime.After(account.CutoverAt) {
		if account.BootstrapKind == "POLICY_ANCHOR" {
			// XM-INV-PREANCHOR-USAGE: a POLICY_ANCHOR account's cutover_at is
			// itself a reconciliation checkpoint (design XM-INV-POLICY-ANCHOR
			// 2.1), not a replayed history boundary -- a fact at or before it
			// cannot be attributed to any ledger baseline by construction,
			// whether or not it is also before the account's own policy
			// start. That is not evidence of a missing source event the way
			// it is for a legacy SIGNED_CUTOVER/POST_CUTOVER_REPLAY account
			// (whose cutover replays real history up to a known point), so
			// freezing SOURCE_GAP here is wrong: see the 2026-09-02
			// production incident in docs/handoffs/XM-INV-PREANCHOR-USAGE.md
			// (106 accounts freshly POLICY_ANCHOR-bootstrapped by a balances
			// checkpoint had their own backlog of pre-anchor usage facts
			// rejected this way in the same reconcile pass, wedging the
			// ingestion pipeline dead after 8 retries each). Skip: no
			// freeze, one audit row, commit so the caller marks the source
			// event processed. Deliberately does not persist a
			// source_usage_events/source_credit_events row for this fact --
			// consistent with the existing pre-policy-checkpoint precedent
			// (ObserveBalanceCheckpoint's eligibility.pre_policy_checkpoint.skipped
			// path above), a skipped fact is represented purely by its audit
			// row, never as a stored ledger-visible fact.
			if err = writeAudit(ctx, tx, actor, "eligibility."+in.Kind+".pre_anchor_skipped",
				"external_account", accountID, nil, map[string]any{
					"external_object_id": in.ExternalObjectID, "external_event_id": in.ExternalEventID,
					"event_time": in.EventTime, "cutover_at": account.CutoverAt,
					"source_revision": in.SourceRevision}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		if err = freezeEligibilityTx(ctx, tx, accountID, "", "SOURCE_GAP", in.Kind, in.ExternalObjectID, in.SourceRevision, actor); err != nil {
			return err
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return domain.ErrConflict
	}
	table := "source_credit_events"
	objectColumn := "external_credit_id"
	if in.Kind == "usage" {
		table = "source_usage_events"
		objectColumn = "external_usage_id"
	}
	var existingRevision, existingEventID, existingObjectID string
	query := fmt.Sprintf(`
		SELECT source_revision_hash,external_event_id,%s FROM %s
		WHERE source_instance_id=$1 AND (external_event_id=$2 OR %s=$3)`, objectColumn, table, objectColumn)
	err = tx.QueryRow(ctx, query, in.SourceInstanceID, in.ExternalEventID, in.ExternalObjectID).Scan(
		&existingRevision, &existingEventID, &existingObjectID)
	if err == nil {
		if existingRevision != in.SourceRevision || existingEventID != in.ExternalEventID || existingObjectID != in.ExternalObjectID {
			if freezeErr := freezeEligibilityTx(ctx, tx, accountID, "", "EVENT_PAYLOAD_DRIFT", in.Kind, in.ExternalObjectID, in.SourceRevision, actor); freezeErr != nil {
				return freezeErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return domain.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	id := randomUUID()
	causalOrder := nullableBigInt(in.CausalOrder)
	if in.Kind == "usage" {
		invoiceEligible := !in.EventTime.Before(account.PolicyStartAt)
		_, err = tx.Exec(ctx, `
			INSERT INTO source_usage_events(
				id,source_instance_id,external_account_id,external_event_id,external_usage_id,
				event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
				billing_scope,invoice_eligible,causal_domain,causal_order,source_sequence,source_cursor,
				stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,$12,NULLIF($13,''),$14::numeric,
				$15,$16,$17,$18,$19)`, id, in.SourceInstanceID, accountID, in.ExternalEventID,
			in.ExternalObjectID, in.EventTime.UTC(), in.Units.String(), in.UnitCode,
			in.CutoverManifestHash, in.ConfigurationHash, in.DetailKind, invoiceEligible, in.CausalDomain,
			causalOrder, in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(),
			in.SourceRevision, in.ObservedAt.UTC())
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO source_credit_events(
				id,source_instance_id,external_account_id,external_event_id,external_credit_id,
				event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
				credit_kind,causal_domain,causal_order,source_sequence,source_cursor,
				stream_watermark_at,source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,NULLIF($12,''),$13::numeric,
				$14,$15,$16,$17,$18)`, id, in.SourceInstanceID, accountID, in.ExternalEventID,
			in.ExternalObjectID, in.EventTime.UTC(), in.Units.String(), in.UnitCode,
			in.CutoverManifestHash, in.ConfigurationHash, in.DetailKind, in.CausalDomain,
			causalOrder, in.SourceSequence, in.SourceCursor, in.StreamWatermarkAt.UTC(),
			in.SourceRevision, in.ObservedAt.UTC())
	}
	if err != nil {
		return err
	}
	late := !in.EventTime.After(account.FinalizedThrough)
	if late {
		// XM-INV-ELIG-QUEUE-NARROW (design XM-INV-ELIG-SIMPLIFY section
		// 3(C) item 2): this generalized "any late fact freezes the whole
		// account" defense is removed -- reprojectEligibilityTx alone
		// already opens its own precise, funding_lot-scoped freeze
		// (freezeEligibilityTx call in reprojectEligibilityTx's own
		// lot.RoundedMinor<lot.IssuedMinor branch) whenever a late fact
		// actually causes a red-reversal (an already-issued invoice's
		// recognized amount would drop below what was issued). A late fact
		// that reprojects cleanly no longer freezes anything.
		if err = reprojectEligibilityTx(ctx, tx, accountID, account.FinalizedThrough, actor); err != nil {
			return err
		}
		if err = writeAudit(ctx, tx, actor, "eligibility.late_fact.reprojected", in.Kind, id,
			nil, map[string]any{"external_account_id": accountID, "event_time": in.EventTime,
				"external_object_id": in.ExternalObjectID}); err != nil {
			return err
		}
	}
	if err = writeAudit(ctx, tx, actor, "eligibility."+in.Kind+".observed", in.Kind, id,
		nil, map[string]any{"event_time": in.EventTime, "late": late,
			"source_revision": in.SourceRevision}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanCausal(domainValue, orderValue string) (string, *big.Int, error) {
	if domainValue == "" && orderValue == "" {
		return "", nil, nil
	}
	return parseOptionalCausal(domainValue, orderValue)
}

func buildEligibilityProjectionTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, through time.Time) (eligibilityProjection, error) {
	return buildEligibilityProjectionExcludingUsageTx(ctx, tx, account, through, nil)
}

// buildEligibilityProjectionExcludingUsageTx is buildEligibilityProjectionTx
// with one extra ability: excludeUsageIDs, when non-nil, removes the named
// source_usage_events rows from the projection entirely (as if they had
// never been observed), while every other fact (credits, payments, every
// other usage event) is included exactly as buildEligibilityProjectionTx
// would. This exists solely for XM-INV-BALANCE-BLIP's evidence-evaluation
// boundary rule (evaluatePendingBalanceEvidenceTx): a checkpoint/proof whose
// as_of coincides with a usage event's event_time can disagree with the
// upstream source's own reported balance purely because of that timing
// coincidence -- the source snapshot may have been captured before that
// instant's own debit was applied, while this function's inclusive
// event_time<=through window already subtracts it. Recomputing the
// projection with that one boundary usage fact excluded is how the
// evaluator checks whether the difference is explained entirely by the
// coincidence rather than a real gap. Nothing about the ordinary projection
// path (buildEligibilityProjectionTx, used for real reprojection/allocation
// building) changes: it always passes nil here.
func buildEligibilityProjectionExcludingUsageTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, through time.Time, excludeUsageIDs map[string]bool) (eligibilityProjection, error) {
	projection := eligibilityProjection{
		Lots: map[string]*projectedLot{}, ExpectedBalance: new(big.Int), UnallocatedUnits: new(big.Int),
	}
	facts := make([]eligibilityFact, 0)
	rows, err := tx.Query(ctx, `
		SELECT id,event_time,service_units::text,credit_kind,
			COALESCE(causal_domain,''),COALESCE(causal_order::text,''),source_revision_hash
		FROM source_credit_events
		WHERE external_account_id=$1 AND event_time<=$2
			AND (event_time>$3 OR (event_time=$3 AND credit_kind IN ('LEGACY_NON_INVOICEABLE','UNKNOWN_POSITIVE')))
		ORDER BY event_time,id`, account.ExternalAccountID, through, account.CutoverAt)
	if err != nil {
		return projection, err
	}
	for rows.Next() {
		var id, unitsText, creditKind, causalDomain, causalOrder, revision string
		var at time.Time
		if err = rows.Scan(&id, &at, &unitsText, &creditKind, &causalDomain, &causalOrder, &revision); err != nil {
			rows.Close()
			return projection, err
		}
		units, parseErr := parseUnsignedUnits(unitsText, "stored credit units", true)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		domainValue, order, parseErr := scanCausal(causalDomain, causalOrder)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		facts = append(facts, eligibilityFact{Kind: "credit:" + creditKind, ID: id, CreditID: id,
			At: at, Units: units, CausalDomain: domainValue, CausalOrder: order, Revision: revision})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return projection, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT fl.id,fl.completed_at,fl.verified_cash_minor,fl.reserved_minor,fl.issued_minor,
			flcs.cash_service_units::text,flcs.consumed_service_units::text,
			flcs.rounded_consumed_cash_minor,COALESCE(flcs.causal_domain,''),
			COALESCE(flcs.causal_order::text,''),fl.source_revision_hash
		FROM funding_lots fl
		JOIN funding_lot_consumption_state flcs ON flcs.funding_lot_id=fl.id
		WHERE fl.external_account_id=$1 AND fl.eligibility_kind='WALLET_CASH'
			AND fl.completed_at>$3 AND fl.completed_at<=$2
			AND fl.completed_at>=$4
			AND fl.verification_state='verified' AND fl.refund_frozen=FALSE
		ORDER BY fl.completed_at,fl.id FOR UPDATE OF fl,flcs`, account.ExternalAccountID, through, account.CutoverAt, account.PolicyStartAt)
	if err != nil {
		return projection, err
	}
	for rows.Next() {
		var id, totalText, oldUnitsText, causalDomain, causalOrder, revision string
		var at time.Time
		var paid, reserved, issued, oldMinor int64
		if err = rows.Scan(&id, &at, &paid, &reserved, &issued, &totalText, &oldUnitsText,
			&oldMinor, &causalDomain, &causalOrder, &revision); err != nil {
			rows.Close()
			return projection, err
		}
		total, parseErr := parseUnsignedUnits(totalText, "stored cash service units", false)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		oldUnits, parseErr := parseUnsignedUnits(oldUnitsText, "stored consumed service units", true)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		domainValue, order, parseErr := scanCausal(causalDomain, causalOrder)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		lot := &projectedLot{ID: id, PaidMinor: paid, TotalUnits: total,
			ConsumedUnits: new(big.Int), Numerator: new(big.Int), Remainder: new(big.Int),
			OldMinor: oldMinor, OldUnits: oldUnits, IssuedMinor: issued, ReservedMinor: reserved}
		projection.Lots[id] = lot
		facts = append(facts, eligibilityFact{Kind: "payment", ID: id, LotID: id, At: at,
			Units: new(big.Int).Set(total), PaidMinor: paid, CausalDomain: domainValue,
			CausalOrder: order, Revision: revision})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return projection, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT id,event_time,service_units::text,invoice_eligible,COALESCE(causal_domain,''),
			COALESCE(causal_order::text,''),source_revision_hash
		FROM source_usage_events
		WHERE external_account_id=$1 AND event_time>$3 AND event_time<=$2
		ORDER BY event_time,id`, account.ExternalAccountID, through, account.CutoverAt)
	if err != nil {
		return projection, err
	}
	for rows.Next() {
		var id, unitsText, causalDomain, causalOrder, revision string
		var at time.Time
		var invoiceEligible bool
		if err = rows.Scan(&id, &at, &unitsText, &invoiceEligible, &causalDomain, &causalOrder, &revision); err != nil {
			rows.Close()
			return projection, err
		}
		if excludeUsageIDs != nil && excludeUsageIDs[id] {
			continue
		}
		units, parseErr := parseUnsignedUnits(unitsText, "stored usage units", false)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		domainValue, order, parseErr := scanCausal(causalDomain, causalOrder)
		if parseErr != nil {
			rows.Close()
			return projection, parseErr
		}
		facts = append(facts, eligibilityFact{Kind: "usage", ID: id, UsageID: id,
			At: at, Units: units, InvoiceEligible: invoiceEligible,
			CausalDomain: domainValue, CausalOrder: order, Revision: revision})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return projection, err
	}
	rows.Close()

	sort.Slice(facts, func(i, j int) bool {
		if !facts[i].At.Equal(facts[j].At) {
			return facts[i].At.Before(facts[j].At)
		}
		// UNKNOWN_POSITIVE is intentionally anchored at a trusted interval
		// start and conservatively precedes every fact in that interval.
		if facts[i].Kind == "credit:UNKNOWN_POSITIVE" && facts[j].Kind != facts[i].Kind {
			return true
		}
		if facts[j].Kind == "credit:UNKNOWN_POSITIVE" && facts[i].Kind != facts[j].Kind {
			return false
		}
		if facts[i].CausalOrder != nil && facts[j].CausalOrder != nil && facts[i].CausalDomain == facts[j].CausalDomain {
			if comparison := facts[i].CausalOrder.Cmp(facts[j].CausalOrder); comparison != 0 {
				return comparison < 0
			}
		}
		return facts[i].ID < facts[j].ID
	})
	for start := 0; start < len(facts); {
		end := start + 1
		for end < len(facts) && facts[end].At.Equal(facts[start].At) {
			end++
		}
		if end-start > 1 && ambiguousFactGroup(facts[start:end]) {
			projection.AmbiguousAt = facts[start].At
			return projection, nil
		}
		start = end
	}

	type pool struct {
		id        string
		remaining *big.Int
	}
	nonCash := make([]pool, 0)
	cash := make([]pool, 0)
	// XM-INV-OVERAGE-CARRY-FORWARD: usage that the pools of the moment cannot
	// cover is no longer dropped -- it waits in carried, and the next *cash*
	// top-up settles it.
	//
	// Both sources bill as they go and let a request overdraw: the upstream
	// balance goes negative, and the user's next top-up settles that debt
	// before anything else (confirmed by the product owner). Writing the
	// overdraw off, as this loop used to, left the projection permanently
	// expecting more balance than the source reports -- by exactly the
	// overdrawn amount, on every checkpoint after it. Production account
	// acdcdce9 sat in not_invoiceable_pending_reconciliation behind 380
	// negative differences that were all the same number, 1361800, which is
	// precisely the overage its own row already recorded; every entry into
	// that state resets the consecutive-match counter, so it could never
	// leave. The same write-off also never invoiced consumption the next
	// top-up had really paid for.
	//
	// Only cash settles a carried debt. A debt drained against a non-cash pool
	// -- a gift, a REBATE credit, or an UNKNOWN_POSITIVE the evaluator
	// synthesized to explain a positive difference it could not attribute --
	// would lower the expected balance without any payment behind it, and the
	// consumption would stay non-invoiceable anyway, so dropping it (as
	// before) is already the right answer for that case. Production account
	// 40bd883d is exactly it: its overdraw is not deducted upstream either --
	// its difference reads 0 today -- so carrying it against that account's
	// synthesized credits would have moved a reconciling account off zero for
	// nothing. Non-cash pools therefore never drain a debt, and a usage fact
	// that is not invoice-eligible is never carried at all, because cash is
	// the one thing that could settle it and it may not touch cash.
	type debt struct {
		usageID string
		units   *big.Int
	}
	carried := make([]debt, 0)
	// One usage event can now be paid in instalments -- partly when it
	// happens, the rest whenever a later top-up lands -- so its allocation
	// order has to keep climbing across those separate visits rather than
	// restarting at each one (consumption_allocations is UNIQUE on
	// (usage_event_id, allocation_order)).
	allocationOrder := map[string]int{}
	allocateNonCash := func(usageID string, remaining *big.Int) *big.Int {
		for i := range nonCash {
			if remaining.Sign() == 0 {
				break
			}
			amount := minBig(remaining, nonCash[i].remaining)
			if amount.Sign() == 0 {
				continue
			}
			allocationOrder[usageID]++
			projection.Allocations = append(projection.Allocations, projectedAllocation{
				UsageID: usageID, CreditID: nonCash[i].id, Order: allocationOrder[usageID],
				Units: new(big.Int).Set(amount),
			})
			nonCash[i].remaining.Sub(nonCash[i].remaining, amount)
			remaining.Sub(remaining, amount)
		}
		return remaining
	}
	allocateCash := func(usageID string, remaining *big.Int) (*big.Int, error) {
		for i := range cash {
			if remaining.Sign() == 0 {
				break
			}
			amount := minBig(remaining, cash[i].remaining)
			if amount.Sign() == 0 {
				continue
			}
			lot := projection.Lots[cash[i].id]
			oldRounded := lot.RoundedMinor
			lot.ConsumedUnits.Add(lot.ConsumedUnits, amount)
			rounded, numerator, remainder, roundErr := cumulativeCashRound(lot.PaidMinor, lot.ConsumedUnits, lot.TotalUnits)
			if roundErr != nil {
				return nil, roundErr
			}
			lot.RoundedMinor, lot.Numerator, lot.Remainder = rounded, numerator, remainder
			allocationOrder[usageID]++
			projection.Allocations = append(projection.Allocations, projectedAllocation{
				UsageID: usageID, LotID: cash[i].id, Order: allocationOrder[usageID],
				Units: new(big.Int).Set(amount), CashMinorDelta: rounded - oldRounded,
			})
			cash[i].remaining.Sub(cash[i].remaining, amount)
			remaining.Sub(remaining, amount)
		}
		return remaining, nil
	}
	// drainCarried charges everything still owed against the cash top-up that
	// just arrived, oldest debt first. Oldest-first keeps a debt ahead of any
	// later usage in the queue for that lot, which is the order the source
	// itself settles in.
	drainCarried := func() error {
		kept := carried[:0]
		for _, owed := range carried {
			remaining, allocErr := allocateCash(owed.usageID, owed.units)
			if allocErr != nil {
				return allocErr
			}
			if remaining.Sign() > 0 {
				owed.units = remaining
				kept = append(kept, owed)
			}
		}
		carried = kept
		return nil
	}
	for _, fact := range facts {
		switch {
		case strings.HasPrefix(fact.Kind, "credit:"):
			nonCash = append(nonCash, pool{id: fact.CreditID, remaining: new(big.Int).Set(fact.Units)})
		case fact.Kind == "payment":
			cash = append(cash, pool{id: fact.LotID, remaining: new(big.Int).Set(fact.Units)})
			if drainErr := drainCarried(); drainErr != nil {
				return projection, drainErr
			}
		case fact.Kind == "usage":
			remaining := allocateNonCash(fact.UsageID, new(big.Int).Set(fact.Units))
			if !fact.InvoiceEligible {
				if remaining.Sign() > 0 && projection.ShortfallUsage == "" {
					projection.ShortfallUsage = fact.UsageID
					projection.ShortfallUnits = new(big.Int).Set(remaining)
				}
				if remaining.Sign() > 0 {
					projection.UnallocatedUnits.Add(projection.UnallocatedUnits, remaining)
				}
				continue
			}
			remaining, allocErr := allocateCash(fact.UsageID, remaining)
			if allocErr != nil {
				return projection, allocErr
			}
			if remaining.Sign() > 0 {
				carried = append(carried, debt{usageID: fact.UsageID, units: remaining})
			}
		}
	}
	// What is still owed at the end of the window is the genuinely
	// non-invoiceable overage: consumption no cash has covered. The oldest
	// outstanding debt identifies it, matching the single
	// (usage_event_id, units) pair the account row can hold. A
	// non-invoice-eligible shortfall recorded in the loop above keeps
	// precedence, exactly as it had before this slice.
	if len(carried) > 0 && projection.ShortfallUsage == "" {
		projection.ShortfallUsage = carried[0].usageID
		projection.ShortfallUnits = new(big.Int).Set(carried[0].units)
	}
	for _, owed := range carried {
		projection.UnallocatedUnits.Add(projection.UnallocatedUnits, owed.units)
	}
	for _, item := range nonCash {
		projection.ExpectedBalance.Add(projection.ExpectedBalance, item.remaining)
	}
	for _, item := range cash {
		projection.ExpectedBalance.Add(projection.ExpectedBalance, item.remaining)
	}
	return projection, nil
}

func ambiguousFactGroup(group []eligibilityFact) bool {
	if len(group) < 2 {
		return false
	}
	filtered := make([]eligibilityFact, 0, len(group))
	for _, fact := range group {
		if fact.Kind == "credit:UNKNOWN_POSITIVE" {
			continue
		}
		filtered = append(filtered, fact)
	}
	if len(filtered) < 2 {
		return false
	}
	allCredit, allUsage := true, true
	for _, fact := range filtered {
		allCredit = allCredit && strings.HasPrefix(fact.Kind, "credit:")
		allUsage = allUsage && fact.Kind == "usage"
	}
	if allCredit || allUsage {
		return false
	}
	domainValue := filtered[0].CausalDomain
	if domainValue == "" {
		return true
	}
	seen := map[string]struct{}{}
	for _, fact := range filtered {
		if fact.CausalDomain != domainValue || fact.CausalOrder == nil {
			return true
		}
		key := fact.CausalOrder.String()
		if _, duplicate := seen[key]; duplicate {
			return true
		}
		seen[key] = struct{}{}
	}
	return false
}

func minBig(a, b *big.Int) *big.Int {
	if a.Cmp(b) < 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func cumulativeCashRound(paidMinor int64, consumed, total *big.Int) (int64, *big.Int, *big.Int, error) {
	if paidMinor < 0 || total.Sign() <= 0 || consumed.Sign() < 0 || consumed.Cmp(total) > 0 {
		return 0, nil, nil, errors.New("invalid cumulative cash ratio")
	}
	numerator := new(big.Int).Mul(big.NewInt(paidMinor), consumed)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, total, remainder)
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(total) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Int64() < 0 || quotient.Int64() > paidMinor {
		return 0, nil, nil, errors.New("rounded cumulative cash is outside verified payment")
	}
	rounded := quotient.Int64()
	roundingRemainder := new(big.Int).Sub(new(big.Int).Set(numerator), new(big.Int).Mul(big.NewInt(rounded), total))
	return rounded, numerator, roundingRemainder, nil
}

func reprojectEligibilityTx(ctx context.Context, tx pgx.Tx, accountID string, through time.Time, actor AuditActor) error {
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	projection, err := buildEligibilityProjectionTx(ctx, tx, account, through)
	if err != nil {
		return err
	}
	if !projection.AmbiguousAt.IsZero() {
		return freezeEligibilityTx(ctx, tx, accountID, "", "AMBIGUOUS_EVENT_ORDER", "event_time",
			projection.AmbiguousAt.UTC().Format(time.RFC3339Nano), "", actor)
	}
	if err = recordUsageOverageTx(ctx, tx, accountID, projection.ShortfallUsage, projection.ShortfallUnits, actor); err != nil {
		return err
	}
	// Derived allocations may be rebuilt because source facts themselves are
	// immutable. The account advisory lock and SERIALIZABLE transaction make a
	// replay atomic with invoice reservation/refund transitions.
	if _, err = tx.Exec(ctx, `
		DELETE FROM consumption_allocations ca USING source_usage_events sue
		WHERE ca.usage_event_id=sue.id AND sue.external_account_id=$1`, accountID); err != nil {
		return err
	}
	projectionVersion := account.Version + 1
	for _, allocation := range projection.Allocations {
		_, err = tx.Exec(ctx, `
			INSERT INTO consumption_allocations(
				id,usage_event_id,funding_lot_id,credit_event_id,allocation_order,
				service_units,cash_minor_delta,projection_version)
			VALUES($1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,$5,$6::numeric,$7,$8)`,
			randomUUID(), allocation.UsageID, allocation.LotID, allocation.CreditID,
			allocation.Order, allocation.Units.String(), allocation.CashMinorDelta, projectionVersion)
		if err != nil {
			return err
		}
	}
	lotIDs := make([]string, 0, len(projection.Lots))
	for id := range projection.Lots {
		lotIDs = append(lotIDs, id)
	}
	sort.Strings(lotIDs)
	for _, id := range lotIDs {
		lot := projection.Lots[id]
		if lot.RoundedMinor < lot.OldMinor {
			if err = invalidateLotReservationsTx(ctx, tx, lot.ID, actor); err != nil {
				return err
			}
			if err = tx.QueryRow(ctx, `SELECT reserved_minor,issued_minor FROM funding_lots WHERE id=$1 FOR UPDATE`, lot.ID).
				Scan(&lot.ReservedMinor, &lot.IssuedMinor); err != nil {
				return err
			}
			if lot.RoundedMinor < lot.IssuedMinor {
				if err = markLotIssuedAttentionTx(ctx, tx, lot, actor); err != nil {
					return err
				}
				if err = freezeEligibilityTx(ctx, tx, accountID, lot.ID, "LATE_FINALIZED_EVENT",
					"funding_lot", lot.ID, "", actor); err != nil {
					return err
				}
				// Preserve the last recognized amount as an accounting exposure
				// floor. The open refund case records the lower recomputation.
				continue
			}
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE funding_lots
			SET consumed_cash_minor=$1,eligibility_revision=eligibility_revision+1,updated_at=now()
			WHERE id=$2 AND eligibility_kind='WALLET_CASH'
				AND reserved_minor+issued_minor<=$1`, lot.RoundedMinor, lot.ID)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		_, err = tx.Exec(ctx, `
			UPDATE funding_lot_consumption_state SET
				consumed_service_units=$1::numeric,cumulative_cash_numerator=$2::numeric,
				rounded_consumed_cash_minor=$3,rounding_remainder_numerator=$4::numeric,
				state_version=state_version+1,updated_at=now()
			WHERE funding_lot_id=$5`, lot.ConsumedUnits.String(), lot.Numerator.String(),
			lot.RoundedMinor, lot.Remainder.String(), lot.ID)
		if err != nil {
			return err
		}
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.projection.rebuilt", "external_account", accountID,
		map[string]any{"finalized_through": account.FinalizedThrough, "projection_version": account.Version},
		map[string]any{"requested_through": through, "projection_version": projectionVersion,
			"allocation_count": len(projection.Allocations)}); err != nil {
		return err
	}
	return nil
}

func freezeEligibilityTx(ctx context.Context, tx pgx.Tx, accountID, lotID, reason, objectType, objectID, revision string, actor AuditActor) error {
	if strings.TrimSpace(objectID) == "" {
		objectID = accountID
	}
	if !validHash(revision) {
		revision = ""
	}
	freezeID := randomUUID()
	err := tx.QueryRow(ctx, `
		INSERT INTO eligibility_freezes(
			id,external_account_id,funding_lot_id,freeze_reason,trigger_object_type,
			trigger_object_id,source_revision_hash)
		VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,NULLIF($7,''))
		ON CONFLICT(external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
			WHERE status='open'
		DO UPDATE SET updated_at=eligibility_freezes.updated_at
		RETURNING id`, freezeID, accountID, lotID, reason, objectType, objectID, revision).Scan(&freezeID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET eligibility_status='frozen',projection_version=projection_version+1,
			pending_reconciliation_reason=NULL,pending_reconciliation_trigger_type=NULL,
			pending_reconciliation_trigger_id=NULL,pending_reconciliation_detail=NULL,
			pending_reconciliation_since=NULL,pending_reconciliation_consecutive_matches=0,
			updated_at=now()
		WHERE external_account_id=$1`, accountID); err != nil {
		return err
	}
	if err = invalidateAccountReservationsTx(ctx, tx, accountID, actor); err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.frozen."+strings.ToLower(reason),
		"eligibility_freeze", freezeID, nil, map[string]any{"external_account_id": accountID,
			"funding_lot_id": lotID, "reason": reason, "trigger_type": objectType,
			"trigger_id": objectID}); err != nil {
		return err
	}
	return nil
}

// pendingReconciliationExitMatches (design XM-INV-ELIG-SIMPLIFY section
// 3(A), acceptance-line ruling 2026-09-03) is N: the number of consecutive
// real balance-evidence items (checkpoint or carry-forward proof) that must
// evaluate matched before an account auto-exits
// not_invoiceable_pending_reconciliation back to active. Two, not one, so a
// single lucky reconciliation cannot mask a still-real gap.
const pendingReconciliationExitMatches = 2

// enterPendingReconciliationTx (XM-INV-ELIG-AUTO-RECONCILE, design section
// 3(A)) replaces freezeEligibilityTx for a negative/unreconciled balance
// difference: this downgrades the account to a self-clearing state instead
// of opening a manual eligibility_freezes row -- ResolveEligibilityFreeze/MFA
// is never involved, and no admin queue entry is ever created for this
// reason. Reservations are still invalidated (the account must stop being
// invoiceable immediately, exactly like a real freeze), but the account
// reconciles automatically once pendingReconciliationExitMatches consecutive
// real evaluations come back matched (see advancePendingReconciliationMatchTx
// below, invoked from evaluatePendingBalanceEvidenceTx's writeEvaluation
// closure).
//
// Frozen priority: a real, distinct eligibility_freezes reason always wins.
// If the account is already 'frozen', this call is a no-op for the account
// row and its five pending_reconciliation_* columns (reservations are still
// invalidated defensively, matching freezeEligibilityTx's own unconditional
// behavior) -- it never downgrades a genuinely frozen account, and never
// overwrites which real freeze reason governs it.
//
// Re-entering while already pending (a later negative item, before the
// account has auto-exited) refreshes reason/trigger/detail to the latest
// evidence and resets the consecutive-match counter to zero -- a fresh
// negative difference means the previous streak of matches, if any, did not
// actually resolve the gap -- but preserves the original
// pending_reconciliation_since so operators can see how long the account has
// been unreconciled, not just since its most recent negative item.
//
// preserveStreak (XM-INV-PENDING-RECON C4) is the one exception to that
// reset, and it exists because the reset's own justification does not always
// apply. Evidence whose magnitude is unknown -- balance evidence sealed
// before the bridge started reporting deficits -- never produces a difference
// at all, so it cannot be "a fresh negative difference" showing that the
// streak failed to resolve anything. It says only that the account is
// negative by an amount nobody can state. Resetting on it discards a real
// streak on the strength of no arithmetic, and because every carry-forward
// proof restates its prior verbatim, one such item during a replay becomes
// one per published cycle: a production account collected 419 `entered` audit
// rows exactly that way. With preserveStreak the counter survives, and when
// the account was already in this state no second `entered` row is written --
// restating that an already-pending account is still pending is not an event.
// Evidence that does state a magnitude and disagrees with the ledger keeps
// the original behaviour exactly: reset, and a fresh `entered`.
func enterPendingReconciliationTx(ctx context.Context, tx pgx.Tx, accountID, reason, triggerType, triggerID, detail string, preserveStreak bool, actor AuditActor) error {
	if strings.TrimSpace(triggerID) == "" {
		triggerID = accountID
	}
	// Whether this is a first entry or a re-entry decides whether an
	// `entered` audit row is written, and the UPDATE below cannot report it:
	// its RowsAffected only separates "frozen" from "not frozen", and a
	// RETURNING (xmax=0) test does not survive a HOT update. So the status is
	// read first, under the same row lock the evaluator's own caller already
	// holds -- taking it here as well costs nothing and keeps this function
	// correct when called from a path that does not.
	var alreadyPending bool
	if err := tx.QueryRow(ctx, `SELECT eligibility_status='not_invoiceable_pending_reconciliation'
		FROM source_account_eligibility_state WHERE external_account_id=$1 FOR UPDATE`,
		accountID).Scan(&alreadyPending); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET eligibility_status='not_invoiceable_pending_reconciliation',
			pending_reconciliation_reason=$2,
			pending_reconciliation_trigger_type=$3,
			pending_reconciliation_trigger_id=$4,
			pending_reconciliation_detail=$5,
			pending_reconciliation_since=CASE
				WHEN eligibility_status='not_invoiceable_pending_reconciliation'
				THEN pending_reconciliation_since ELSE now() END,
			pending_reconciliation_consecutive_matches=CASE
				WHEN $6 AND eligibility_status='not_invoiceable_pending_reconciliation'
				THEN pending_reconciliation_consecutive_matches ELSE 0 END,
			projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1 AND eligibility_status<>'frozen'`,
		accountID, reason, triggerType, triggerID, detail, preserveStreak)
	if err != nil {
		return err
	}
	if err = invalidateAccountReservationsTx(ctx, tx, accountID, actor); err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		// Frozen priority: a real, distinct freeze already governs this
		// account -- do not downgrade or overwrite its trigger detail.
		return nil
	}
	if preserveStreak && alreadyPending {
		// The trigger detail above was still refreshed to this item, so an
		// operator sees the most recent evidence; what is suppressed is only
		// the audit row claiming the account entered a state it was in
		// already.
		return nil
	}
	return writeAudit(ctx, tx, actor, "eligibility.pending_reconciliation.entered", "external_account", accountID,
		nil, map[string]any{"reason": reason, "trigger_type": triggerType, "trigger_id": triggerID, "detail": detail})
}

// advancePendingReconciliationMatchTx (XM-INV-ELIG-AUTO-RECONCILE) is called
// by evaluatePendingBalanceEvidenceTx's writeEvaluation closure after it
// records a real "matched" evaluation. Per design section 3(A): each
// consecutive matched evaluation while the account sits in
// not_invoiceable_pending_reconciliation counts toward automatic exit --
// pendingReconciliationExitMatches consecutive matches, and no separate open
// eligibility_freezes row (reusing ResolveEligibilityFreeze's own open-freeze
// guard, count(*)...status='open', for identical semantics -- see
// eligibility_operations.go), clear the five pending_reconciliation_*
// columns and return the account to active. A non-matched real evaluation
// (negative_frozen) already resets the counter to zero as part of
// enterPendingReconciliationTx's own unconditional reset above; a
// non-matched, non-negative evaluation (source_gap_frozen,
// positive_classified_non_cash, positive_blip_ignored) leaves the counter
// untouched, per the design's own enumeration -- this function is only ever
// invoked for status=="matched", so those cases never reach it.
func advancePendingReconciliationMatchTx(ctx context.Context, tx pgx.Tx, accountID string, actor AuditActor) error {
	var matches int
	err := tx.QueryRow(ctx, `
		UPDATE source_account_eligibility_state
		SET pending_reconciliation_consecutive_matches=pending_reconciliation_consecutive_matches+1,updated_at=now()
		WHERE external_account_id=$1 AND eligibility_status='not_invoiceable_pending_reconciliation'
		RETURNING pending_reconciliation_consecutive_matches`, accountID).Scan(&matches)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if matches < pendingReconciliationExitMatches {
		return nil
	}
	var hasOpenFreeze bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM eligibility_freezes WHERE external_account_id=$1 AND status='open')`,
		accountID).Scan(&hasOpenFreeze); err != nil {
		return err
	}
	if hasOpenFreeze {
		return nil
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET eligibility_status='active',
			pending_reconciliation_reason=NULL,pending_reconciliation_trigger_type=NULL,
			pending_reconciliation_trigger_id=NULL,pending_reconciliation_detail=NULL,
			pending_reconciliation_since=NULL,pending_reconciliation_consecutive_matches=0,
			projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1 AND eligibility_status='not_invoiceable_pending_reconciliation'`, accountID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return nil
	}
	return writeAudit(ctx, tx, actor, "eligibility.pending_reconciliation.exited", "external_account", accountID,
		nil, map[string]any{"consecutive_matches": matches})
}

// recordUsageOverageTx (XM-INV-ELIG-AUTO-RECONCILE, design section 3(B))
// replaces reprojectEligibilityTx's old unconditional USAGE_EXCEEDS_LEDGER
// freeze: usage with no non-cash or cash pool left to draw from no longer
// blocks the account -- buildEligibilityProjectionTx never lets a cash lot's
// own consumed_cash_minor exceed its verified_cash_minor, so the unallocated
// overage was never invoiceable in the first place; recording it here is
// purely informational (surfaced later by a read-only ledger query), not a
// new safety mechanism. Every reprojection call updates both columns:
// cleared when the projection no longer has a shortfall (usageID==""),
// otherwise set to the first shortfall usage event's own id and its
// unallocated remainder. Each branch is a no-op (no write, no audit event)
// when the stored value already matches, so a routine reprojection with an
// unchanged (or still absent) overage does not spam the audit log.
func recordUsageOverageTx(ctx context.Context, tx pgx.Tx, accountID, usageID string, units *big.Int, actor AuditActor) error {
	if usageID == "" {
		command, err := tx.Exec(ctx, `
			UPDATE source_account_eligibility_state
			SET non_invoiceable_overage_units=NULL,non_invoiceable_overage_usage_event_id=NULL,updated_at=now()
			WHERE external_account_id=$1 AND non_invoiceable_overage_usage_event_id IS NOT NULL`, accountID)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			return nil
		}
		return writeAudit(ctx, tx, actor, "eligibility.usage.overage_cleared", "external_account", accountID, nil, nil)
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET non_invoiceable_overage_units=$2::numeric,non_invoiceable_overage_usage_event_id=$3::uuid,updated_at=now()
		WHERE external_account_id=$1
			AND (non_invoiceable_overage_units IS DISTINCT FROM $2::numeric
				OR non_invoiceable_overage_usage_event_id IS DISTINCT FROM $3::uuid)`,
		accountID, units.String(), usageID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return nil
	}
	return writeAudit(ctx, tx, actor, "eligibility.usage.overage_recorded", "external_account", accountID, nil,
		map[string]any{"usage_event_id": usageID, "overage_units": units.String()})
}

func freezeRefundedLotTx(ctx context.Context, tx pgx.Tx, accountID, lotID string, oldConsumed int64, revision string, actor AuditActor) error {
	if err := invalidateLotReservationsTx(ctx, tx, lotID, actor); err != nil {
		return err
	}
	var currentCap, verifiedCash, reserved, issued int64
	if err := tx.QueryRow(ctx, `
		SELECT current_cap_minor,verified_cash_minor,reserved_minor,issued_minor
		FROM funding_lots WHERE id=$1 FOR UPDATE`, lotID).Scan(&currentCap, &verifiedCash, &reserved, &issued); err != nil {
		return err
	}
	if verifiedCash > oldConsumed {
		oldConsumed = verifiedCash
	}
	if issued > 0 {
		attention := &projectedLot{ID: lotID, OldMinor: oldConsumed, RoundedMinor: currentCap,
			IssuedMinor: issued, ReservedMinor: reserved}
		if err := markLotIssuedAttentionTx(ctx, tx, attention, actor); err != nil {
			return err
		}
	}
	freezeID := randomUUID()
	if !validHash(revision) {
		revision = ""
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO eligibility_freezes(
			id,external_account_id,funding_lot_id,freeze_reason,trigger_object_type,
			trigger_object_id,source_revision_hash)
		VALUES($1,$2,$3::uuid,'SOURCE_REFUND','funding_lot',$3::text,NULLIF($4,''))
		ON CONFLICT(external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
			WHERE status='open'
		DO UPDATE SET updated_at=now()
		RETURNING id`, freezeID, accountID, lotID, revision).Scan(&freezeID)
	if err != nil {
		return err
	}
	return writeAudit(ctx, tx, actor, "eligibility.funding_lot.refund_frozen", "funding_lot", lotID,
		map[string]any{"verified_cash_minor": verifiedCash, "consumed_cash_minor": oldConsumed},
		map[string]any{"refund_frozen": true, "current_cap_minor": currentCap, "freeze_id": freezeID})
}

func invalidateAccountReservationsTx(ctx context.Context, tx pgx.Tx, accountID string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT ir.id,ir.status,ir.version
		FROM invoice_requests ir
		WHERE EXISTS (
			SELECT 1 FROM invoice_allocations ia
			JOIN funding_lots fl ON fl.id=ia.funding_lot_id
			WHERE ia.invoice_request_id=ir.id AND ia.allocation_state='reserved'
				AND fl.external_account_id=$1)
			AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing')
		ORDER BY ir.id FOR UPDATE OF ir`, accountID)
	if err != nil {
		return err
	}
	type pending struct {
		id      string
		status  domain.RequestStatus
		version int64
	}
	items := make([]pending, 0)
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.status, &item.version); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if err = releaseReservations(ctx, tx, item.id); err != nil {
			return err
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE invoice_requests SET status='rejected',
				review_note='eligibility freeze invalidated this unissued request',
				version=version+1,updated_at=now()
			WHERE id=$1 AND version=$2`, item.id, item.version)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if err = writeAudit(ctx, tx, actor, "invoice_request.invalidated_by_eligibility_freeze",
			"invoice_request", item.id, map[string]any{"status": item.status},
			map[string]any{"status": domain.StatusRejected}); err != nil {
			return err
		}
	}
	return nil
}

func invalidateLotReservationsTx(ctx context.Context, tx pgx.Tx, lotID string, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT ir.id,ir.status,ir.version
		FROM invoice_requests ir
		WHERE EXISTS (
			SELECT 1 FROM invoice_allocations ia
			WHERE ia.invoice_request_id=ir.id AND ia.funding_lot_id=$1
				AND ia.allocation_state='reserved')
			AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing')
		ORDER BY ir.id FOR UPDATE OF ir`, lotID)
	if err != nil {
		return err
	}
	type item struct {
		id      string
		status  domain.RequestStatus
		version int64
	}
	items := make([]item, 0)
	for rows.Next() {
		var value item
		if err = rows.Scan(&value.id, &value.status, &value.version); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, value := range items {
		if err = releaseReservations(ctx, tx, value.id); err != nil {
			return err
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE invoice_requests SET status='rejected',
				review_note='consumption ledger reduction invalidated this unissued request',
				version=version+1,updated_at=now()
			WHERE id=$1 AND version=$2`, value.id, value.version)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if err = writeAudit(ctx, tx, actor, "invoice_request.invalidated_by_consumption_replay",
			"invoice_request", value.id, map[string]any{"status": value.status},
			map[string]any{"status": domain.StatusRejected}); err != nil {
			return err
		}
	}
	return nil
}

func markLotIssuedAttentionTx(ctx context.Context, tx pgx.Tx, lot *projectedLot, actor AuditActor) error {
	rows, err := tx.Query(ctx, `
		SELECT ir.id,ir.status,ir.version,ia.amount_minor,fl.source_revision_hash
		FROM invoice_allocations ia
		JOIN invoice_requests ir ON ir.id=ia.invoice_request_id
		JOIN funding_lots fl ON fl.id=ia.funding_lot_id
		WHERE ia.funding_lot_id=$1 AND ia.allocation_state IN ('issued','refund_attention')
			AND ir.status IN ('issued_awaiting_document','issued','refund_attention')
		ORDER BY ir.id FOR UPDATE OF ir`, lot.ID)
	if err != nil {
		return err
	}
	type attention struct {
		id, revision      string
		status            domain.RequestStatus
		version, exposure int64
	}
	items := make([]attention, 0)
	for rows.Next() {
		var item attention
		if err = rows.Scan(&item.id, &item.status, &item.version, &item.exposure, &item.revision); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if _, err = tx.Exec(ctx, `
			UPDATE invoice_allocations SET allocation_state='refund_attention',updated_at=now()
			WHERE invoice_request_id=$1 AND funding_lot_id=$2
				AND allocation_state IN ('issued','refund_attention')`, item.id, lot.ID); err != nil {
			return err
		}
		caseID := randomUUID()
		observedReduction := lot.OldMinor - lot.RoundedMinor
		if observedReduction <= 0 {
			observedReduction = 1
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO refund_cases(
				id,invoice_request_id,funding_lot_id,source_revision_hash,
				observed_refund_minor,issued_exposure_minor,observed_cap_minor)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(invoice_request_id,funding_lot_id) WHERE status='open'
			DO UPDATE SET observed_refund_minor=GREATEST(refund_cases.observed_refund_minor,EXCLUDED.observed_refund_minor),
				issued_exposure_minor=GREATEST(refund_cases.issued_exposure_minor,EXCLUDED.issued_exposure_minor),
				observed_cap_minor=LEAST(refund_cases.observed_cap_minor,EXCLUDED.observed_cap_minor),updated_at=now()
			RETURNING id`, caseID, item.id, lot.ID, item.revision, observedReduction,
			item.exposure, lot.RoundedMinor).Scan(&caseID)
		if err != nil {
			return err
		}
		if item.status != domain.StatusRefundAttention {
			command, updateErr := tx.Exec(ctx, `
				UPDATE invoice_requests SET status='refund_attention',version=version+1,updated_at=now()
				WHERE id=$1 AND version=$2`, item.id, item.version)
			if updateErr != nil {
				return updateErr
			}
			if command.RowsAffected() != 1 {
				return domain.ErrVersionConflict
			}
		}
		if err = writeAudit(ctx, tx, actor, "invoice_request.consumption_replay_attention",
			"invoice_request", item.id, map[string]any{"status": item.status},
			map[string]any{"status": domain.StatusRefundAttention, "refund_case_id": caseID}); err != nil {
			return err
		}
	}
	return nil
}

// ProcessEligibilityProjectionJobs is a bounded, retryable worker entry point.
// It must run outside the signed ingest transaction. Multiple replicas safely
// share work through SKIP LOCKED leases.
func (s *Store) ProcessEligibilityProjectionJobs(ctx context.Context, limit int, now time.Time, actor AuditActor) (int, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lease := randomUUID()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `
		WITH candidates AS (
			SELECT external_account_id FROM eligibility_projection_jobs
			WHERE (status IN ('queued','failed') AND next_attempt_at<=$1)
				OR (status='processing' AND lease_expires_at<=$1)
			ORDER BY next_attempt_at,external_account_id
			FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE eligibility_projection_jobs j
		SET status='processing',lease_token=$3,lease_expires_at=$1+interval '2 minutes',
			attempt_count=attempt_count+1,updated_at=$1
		FROM candidates c WHERE j.external_account_id=c.external_account_id
		RETURNING j.external_account_id`, now.UTC(), limit, lease)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	processed := 0
	var firstProcessingError error
	for _, accountID := range ids {
		if processErr := s.processEligibilityProjectionJob(ctx, accountID, lease, actor); processErr != nil {
			if errors.Is(processErr, errBalanceCarryForwardProofPending) {
				// Exponential backoff keyed off the job's own attempt_count
				// (already incremented by the claim step's UPDATE above):
				// 30s,60s,120s,240s,480s, capped at balanceProofPendingBackoffCapSeconds
				// from the 6th attempt on. See the doc comment on that
				// constant for the full rationale.
				_, markErr := s.pool.Exec(ctx, fmt.Sprintf(`
					UPDATE eligibility_projection_jobs SET status='queued',lease_token=NULL,lease_expires_at=NULL,
						last_error_code='BALANCE_PROOF_PENDING',
						next_attempt_at=$3::timestamptz+(LEAST(%d,30*power(2,LEAST(attempt_count,11)-1)))::int*interval '1 second',
						updated_at=$3::timestamptz
					WHERE external_account_id=$1 AND lease_token=$2`, balanceProofPendingBackoffCapSeconds),
					accountID, lease, now.UTC())
				if markErr != nil {
					return processed, markErr
				}
				continue
			}
			if firstProcessingError == nil {
				firstProcessingError = fmt.Errorf("eligibility projection job failed: %w", processErr)
			}
			if markErr := s.markEligibilityProjectionJobFailedOrDead(ctx, accountID, lease, now.UTC(), processErr, actor); markErr != nil {
				return processed, markErr
			}
			continue
		}
		processed++
	}
	return processed, firstProcessingError
}

// markEligibilityProjectionJobFailedOrDead grades one per-account processing
// error (XM-INV-PROJECTION-FAILURE-GRADING): below projectionFailureDeadThreshold
// consecutive failures it requeues with exponential backoff, exactly
// mirroring MarkSourceEventFailed's attempt_count>=8 dead-letter escalation
// for source_ingest_events (source_sync.go); at the threshold it escalates
// the job to a terminal status='dead' and writes an audit event so an
// operator (or invoice-eligibility-repair --kind=projection-requeue-dead) can
// find and act on it. Every branch returns a defined result: a lease
// mismatch (another process already reclaimed this row -- not expected under
// the claim step's exclusive SKIP LOCKED lease and this function's serial,
// single-worker-instance processing loop, but never treated as a hard
// failure) is silently ignored, matching the pre-existing BALANCE_PROOF_PENDING
// branch's own tolerance of that case; only a genuine database error is
// returned, and the caller (ProcessEligibilityProjectionJobs) never lets that
// abort processing of any other account already claimed in the same batch.
func (s *Store) markEligibilityProjectionJobFailedOrDead(ctx context.Context, accountID, lease string, now time.Time, processErr error, actor AuditActor) error {
	errText := processErr.Error()
	if len(errText) > 2000 {
		errText = errText[:2000]
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var attempts int64
	var status string
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		UPDATE eligibility_projection_jobs SET
			attempts=attempts+1,
			status=CASE WHEN attempts+1>=%[1]d THEN 'dead' ELSE 'queued' END,
			lease_token=NULL,lease_expires_at=NULL,
			last_error_code=CASE WHEN attempts+1>=%[1]d THEN 'PROJECTION_DEAD' ELSE 'PROJECTION_FAILED' END,
			last_error=$4,
			next_attempt_at=CASE WHEN attempts+1>=%[1]d THEN next_attempt_at
				ELSE $3::timestamptz+(LEAST(%[2]d,%[3]d*power(2,attempts)))::int*interval '1 second' END,
			updated_at=$3::timestamptz
		WHERE external_account_id=$1 AND lease_token=$2
		RETURNING attempts,status`,
		projectionFailureDeadThreshold, projectionFailureBackoffCapSeconds, projectionFailureBackoffBaseSeconds),
		accountID, lease, now, errText).Scan(&attempts, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "dead" {
		if err = writeAudit(ctx, tx, actor, "eligibility.projection.dead", "external_account", accountID, nil,
			map[string]any{"attempts": attempts, "last_error": errText,
				"threshold": projectionFailureDeadThreshold}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func applyFundingObservationEligibilityTx(ctx context.Context, tx pgx.Tx, lotID, accountID string, in SourceObservation, actor AuditActor) error {
	// V1/V2 payment facts are retained for audit but can never infer post-cutover
	// entitlement. Only the complete signed v3 contract enters this branch.
	if in.EligibilityKind == "" || strings.TrimSpace(in.CashServiceUnits) == "" ||
		strings.TrimSpace(in.WalletUnitCode) == "" || !validHash(in.CutoverManifestHash) ||
		!validHash(in.ConfigurationHash) {
		return nil
	}
	if in.EligibilityKind != domain.EligibilityWalletCash && in.EligibilityKind != domain.EligibilitySubscriptionCash {
		return errors.New("source payment eligibility kind must be wallet or subscription cash")
	}
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	if _, err = verifyFactBatchContextTx(ctx, tx, in.Lot.SourceInstanceID, "payments", in.ExternalEventID,
		in.BatchID, in.ScanCycleID, in.Lot.SourceRevision, in.StreamWatermarkAt); err != nil {
		return err
	}
	if err = verifyFactTrustTx(ctx, tx, account, in.CutoverManifestHash, in.ConfigurationHash,
		in.WalletUnitCode, "payments", in.StreamWatermarkAt); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "UNIT_MISMATCH", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
		return err
	}
	sourceCutover := account.CutoverAt
	if in.EligibilityKind == domain.EligibilitySubscriptionCash {
		sourceCutover = account.GlobalCutoverAt
	}
	if in.Lot.CompletedAt.IsZero() || !in.Lot.CompletedAt.After(sourceCutover) {
		return nil
	}
	cashUnits, err := parseUnsignedUnits(in.CashServiceUnits, "wallet cash service units", in.EligibilityKind == domain.EligibilitySubscriptionCash)
	if err != nil {
		return err
	}
	parsedDomain, parsedOrder, err := parseOptionalCausal(in.CausalDomain, in.CausalOrder)
	if err != nil {
		return err
	}
	in.CausalDomain = parsedDomain
	if in.Lot.CompletedAt.Before(account.PolicyStartAt) {
		if in.EligibilityKind == domain.EligibilityWalletCash {
			return applyPrePolicyWalletFundingTx(ctx, tx, lotID, accountID, account, in, cashUnits, parsedOrder, actor)
		}
		return nil
	}
	eligibilityCutover := sourceCutover
	if eligibilityCutover.Before(account.PolicyStartAt) {
		eligibilityCutover = account.PolicyStartAt
	}
	var causalOrder any
	if parsedOrder != nil {
		causalOrder = parsedOrder.String()
	}
	var verification domain.VerificationState
	var originalMinor, currentCap, oldVerified, oldConsumed int64
	var existingKind domain.EligibilityKind
	var sourceRevision string
	var oldRefundFrozen bool
	err = tx.QueryRow(ctx, `
		SELECT verification_state,original_minor,current_cap_minor,verified_cash_minor,
			consumed_cash_minor,eligibility_kind,source_revision_hash,refund_frozen
		FROM funding_lots WHERE id=$1 FOR UPDATE`, lotID).Scan(&verification, &originalMinor,
		&currentCap, &oldVerified, &oldConsumed, &existingKind, &sourceRevision, &oldRefundFrozen)
	if err != nil {
		return err
	}
	verifiedCash := int64(0)
	if verification == domain.VerificationVerified {
		verifiedCash = currentCap
	}
	if verifiedCash > originalMinor {
		return domain.ErrConflict
	}
	refundFrozen := oldRefundFrozen || in.EventKind == "refund" || in.EventKind == "tombstone" ||
		(oldVerified > 0 && currentCap < oldVerified)
	if refundFrozen {
		verifiedCash = oldVerified
		if verifiedCash == 0 && verification == domain.VerificationFrozen {
			verifiedCash = currentCap
		}
	}
	if existingKind != domain.EligibilityLegacyNonInvoiceable && existingKind != in.EligibilityKind {
		return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
			sourceRevision, actor)
	}
	if in.EligibilityKind == domain.EligibilityWalletCash && existingKind == domain.EligibilityWalletCash {
		var oldUnits, oldDomain, oldOrder string
		err = tx.QueryRow(ctx, `
			SELECT cash_service_units::text,COALESCE(causal_domain,''),COALESCE(causal_order::text,'')
			FROM funding_lot_consumption_state WHERE funding_lot_id=$1 FOR UPDATE`, lotID).Scan(
			&oldUnits, &oldDomain, &oldOrder)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		newOrder := ""
		if parsedOrder != nil {
			newOrder = parsedOrder.String()
		}
		if err == nil && (oldUnits != cashUnits.String() || oldDomain != in.CausalDomain || oldOrder != newOrder) {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
	}
	consumed := oldConsumed
	if refundFrozen {
		consumed = oldConsumed
	}
	if consumed > verifiedCash && !refundFrozen {
		return domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `
		UPDATE funding_lots SET eligibility_kind=$1,eligibility_cutover_at=$2,
			verified_cash_minor=$3,consumed_cash_minor=$4,refund_frozen=$5,
			eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE id=$6`, in.EligibilityKind, eligibilityCutover, verifiedCash, consumed, refundFrozen, lotID)
	if err != nil {
		return err
	}
	if in.EligibilityKind == domain.EligibilityWalletCash {
		_, err = tx.Exec(ctx, `
			INSERT INTO funding_lot_consumption_state(
				funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
				rounded_consumed_cash_minor,rounding_remainder_numerator,causal_domain,causal_order)
			VALUES($1,$2::numeric,0,0,$3,0,NULLIF($4,''),$5::numeric)
			ON CONFLICT(funding_lot_id) DO UPDATE SET
				cash_service_units=EXCLUDED.cash_service_units,causal_domain=EXCLUDED.causal_domain,
				causal_order=EXCLUDED.causal_order,state_version=funding_lot_consumption_state.state_version+1,
				updated_at=now()`, lotID, cashUnits.String(), consumed, in.CausalDomain, causalOrder)
		if err != nil {
			return err
		}
	}
	if refundFrozen {
		return nil
	}
	if !in.Lot.CompletedAt.After(account.FinalizedThrough) && oldVerified == 0 {
		// The payment fact itself was present before finalization only when this
		// is a later independent evidence approval. A first-seen payment after a
		// published boundary violates completeness and freezes instead.
		if verification != domain.VerificationVerified || in.Lot.SourceType != domain.SourceNewAPI {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "LATE_FINALIZED_EVENT", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
	}
	return nil
}

// reanchorLegacyEligibilityAccountTx implements design XM-INV-POLICY-ANCHOR
// 2.4: an account still carrying a legacy bootstrap (SIGNED_CUTOVER or
// POST_CUTOVER_REPLAY -- after this slice, no code path creates a new row of
// either kind, so this set only ever shrinks and a re-anchored account never
// matches it again, making the whole flow naturally idempotent) is
// re-anchored to POLICY_ANCHOR in place the next time its projection job
// runs, before projecting. bootstrap_kind is the trigger condition, not
// cutover_at < policy start: that was tried first and reverted after it
// started firing on every legacy account regardless of genuine staleness
// (every real manifest's cutover necessarily precedes the policy start by
// construction) -- see the handoff doc for that history.
//
// The trust-boundary trigger stays immutable by default; migration 0016
// carves out exactly one exception (transaction-local
// invoice.policy_anchor_reanchor='on', legacy OLD kind, POLICY_ANCHOR NEW
// kind, and the same checkpoint/policy-start/manifest/finalized_through
// validation an INSERT gets), which is what makes an UPDATE-based re-anchor
// possible at all -- the original design called for DELETE-and-reinsert,
// blocked by non-deferrable ON DELETE RESTRICT foreign keys on
// source_account_eligibility_state(external_account_id)
// (source_account_stream_watermarks, and eligibility_projection_jobs' own
// row for the account being processed); see the handoff doc for that
// history too.
//
// Returns true when it performed a re-anchor UPDATE, so the caller knows to
// re-read the account before continuing to project with it.
func reanchorLegacyEligibilityAccountTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, actor AuditActor) (bool, error) {
	if account.BootstrapKind != "SIGNED_CUTOVER" && account.BootstrapKind != "POST_CUTOVER_REPLAY" {
		return false, nil
	}
	// An active invoice reservation or issuance on any of the account's
	// funding lots means real invoice accounting already depends on the
	// current (pre-anchor) consumption state -- resetting it would corrupt
	// that, and for an issued lot the funding_lots_consumed_cash_allocation_bound
	// CHECK constraint would refuse the reset outright regardless. Includes
	// refund_attention (an issued lot already under refund review) alongside
	// reserved/issued -- everything except released, which no longer
	// represents a live claim.
	var hasExposure bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM invoice_allocations ia
			JOIN funding_lots fl ON fl.id=ia.funding_lot_id
			WHERE fl.external_account_id=$1 AND ia.allocation_state IN ('reserved','issued','refund_attention')
		)`, account.ExternalAccountID).Scan(&hasExposure); err != nil {
		return false, err
	}
	if hasExposure {
		var alreadyBlocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM eligibility_freezes
			WHERE external_account_id=$1 AND freeze_reason='POLICY_ANCHOR_BLOCKED' AND status='open')`,
			account.ExternalAccountID).Scan(&alreadyBlocked); err != nil {
			return false, err
		}
		if alreadyBlocked {
			return false, nil
		}
		if err := freezeEligibilityTx(ctx, tx, account.ExternalAccountID, "", "POLICY_ANCHOR_BLOCKED",
			"source_account_eligibility_state", account.ExternalAccountID, "", actor); err != nil {
			return false, err
		}
		return false, writeAudit(ctx, tx, actor, "eligibility.policy_anchor.blocked", "external_account",
			account.ExternalAccountID, map[string]any{"bootstrap_kind": account.BootstrapKind,
				"cutover_at": account.CutoverAt}, nil)
	}

	var candidateAsOf time.Time
	var candidateBalance string
	err := tx.QueryRow(ctx, `
		SELECT as_of,balance_service_units::text FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_kind='reconciliation' AND as_of>=$2
		ORDER BY as_of,id LIMIT 1`, account.ExternalAccountID, account.PolicyStartAt).Scan(
		&candidateAsOf, &candidateBalance)
	if errors.Is(err, pgx.ErrNoRows) {
		// No post-policy checkpoint has arrived yet to anchor to. Continue
		// projecting under the existing anchor -- do not block settlement;
		// the next job retries this check.
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var oldBalance string
	if err = tx.QueryRow(ctx, `SELECT cutover_balance_units::text FROM source_account_eligibility_state
		WHERE external_account_id=$1`, account.ExternalAccountID).Scan(&oldBalance); err != nil {
		return false, err
	}
	deleted, err := tx.Exec(ctx, `
		DELETE FROM consumption_allocations
		WHERE usage_event_id IN (SELECT id FROM source_usage_events WHERE external_account_id=$1)`,
		account.ExternalAccountID)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE funding_lot_consumption_state flcs SET
			consumed_service_units=0,cumulative_cash_numerator=0,
			rounded_consumed_cash_minor=0,rounding_remainder_numerator=0,
			state_version=state_version+1,updated_at=now()
		FROM funding_lots fl
		WHERE fl.id=flcs.funding_lot_id AND fl.external_account_id=$1
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')`,
		account.ExternalAccountID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE funding_lots SET consumed_cash_minor=0,eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE external_account_id=$1 AND eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')`,
		account.ExternalAccountID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('invoice.policy_anchor_reanchor','on',true)`); err != nil {
		return false, err
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_account_eligibility_state SET
			cutover_at=$2,cutover_balance_units=$3::numeric,bootstrap_kind='POLICY_ANCHOR',
			finalized_through=$2,projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1`, account.ExternalAccountID, candidateAsOf, candidateBalance)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, domain.ErrConflict
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.policy_anchor.migrated", "external_account",
		account.ExternalAccountID,
		map[string]any{"bootstrap_kind": account.BootstrapKind, "cutover_at": account.CutoverAt,
			"cutover_balance_units": oldBalance},
		map[string]any{"bootstrap_kind": "POLICY_ANCHOR", "cutover_at": candidateAsOf,
			"cutover_balance_units": candidateBalance, "consumption_allocations_deleted": deleted.RowsAffected()},
	); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) processEligibilityProjectionJob(ctx context.Context, accountID, lease string, actor AuditActor) error {
	// XM-INV-CATCHUP-BURST-BACKPRESSURE fix 1b: the carry-forward proof runs
	// before this job takes any row lock, in its own short transaction. The
	// proof phase is the slow part on a contended account (its carry-candidate
	// scans and unmapped-funding probes run for minutes), and it used to run
	// with this job's own eligibility_projection_jobs row already held FOR
	// UPDATE -- so every finalization pass in every stream's cycle publication
	// queued behind a job that had not yet decided whether it could even
	// start. Now a proof-pending job never holds the row at all, and a job
	// that proceeds holds it only for the write phase.
	//
	// The proof's own writes are additive and idempotent (carry-forward proof
	// rows keyed by account and cycle, ON CONFLICT DO NOTHING), which is what
	// makes running it outside the SERIALIZABLE write transaction safe; the
	// write phase re-runs it only if its window differs from the one proved.
	proofRequested, proofDone, err := s.prepareEligibilityProjectionJob(ctx, accountID, lease, actor)
	if err != nil {
		return err
	}
	return s.completeEligibilityProjectionJob(ctx, accountID, lease, proofRequested, proofDone, actor)
}

// prepareEligibilityProjectionJob is the lock-free half: it reads the job's
// window and the account without locking either, and runs the carry-forward
// proof. It returns the window it proved and whether it did. An account that
// still needs its one-time legacy re-anchor (which changes the very fields
// the proof reads) is left to the locked half, exactly as before.
func (s *Store) prepareEligibilityProjectionJob(ctx context.Context, accountID, lease string, actor AuditActor) (time.Time, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `
		SELECT set_config('statement_timeout','300s',true),
		       set_config('idle_in_transaction_session_timeout','300s',true)`); err != nil {
		return time.Time{}, false, err
	}
	var requested time.Time
	err = tx.QueryRow(ctx, `
		SELECT requested_through FROM eligibility_projection_jobs
		WHERE external_account_id=$1 AND status='processing' AND lease_token=$2`, accountID, lease).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, domain.ErrConflict
	}
	if err != nil {
		return time.Time{}, false, err
	}
	account, err := getEligibilityAccountTx(ctx, tx, accountID, false)
	if err != nil {
		return time.Time{}, false, err
	}
	if account.BootstrapKind == "SIGNED_CUTOVER" || account.BootstrapKind == "POST_CUTOVER_REPLAY" {
		return requested, false, nil
	}
	if requested.Before(account.FinalizedThrough) {
		requested = account.FinalizedThrough
	}
	if err = ensureBalanceCarryForwardProofTx(ctx, tx, account, requested, actor); err != nil {
		return time.Time{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return time.Time{}, false, err
	}
	return requested, true, nil
}

// completeEligibilityProjectionJob is the locked half: the SERIALIZABLE
// transaction that holds the job row, replays the account, evaluates its
// evidence and publishes finalized_through. If proofDone, the proof is
// skipped only when the window it proved is exactly the window being
// processed; any difference re-runs it (cheap, since its rows already exist).
// A window raised by a finalization pass while the lock-free half ran is
// never processed unproved: the job clamps to the proved window and, at the
// end, requeues itself for the remainder instead of deleting its row.
func (s *Store) completeEligibilityProjectionJob(ctx context.Context, accountID, lease string, proofRequested time.Time, proofDone bool, actor AuditActor) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `
		SELECT set_config('statement_timeout','300s',true),
		       set_config('idle_in_transaction_session_timeout','300s',true)`); err != nil {
		return err
	}
	var requested time.Time
	err = tx.QueryRow(ctx, `
		SELECT requested_through FROM eligibility_projection_jobs
		WHERE external_account_id=$1 AND status='processing' AND lease_token=$2
		FOR UPDATE`, accountID, lease).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return err
	}
	account, err := getEligibilityAccountTx(ctx, tx, accountID, false)
	if err != nil {
		return err
	}
	if reanchored, err := reanchorLegacyEligibilityAccountTx(ctx, tx, account, actor); err != nil {
		return err
	} else if reanchored {
		if account, err = getEligibilityAccountTx(ctx, tx, accountID, false); err != nil {
			return err
		}
	}
	if requested.Before(account.FinalizedThrough) {
		requested = account.FinalizedThrough
	}
	if proofDone && requested.After(proofRequested) {
		requested = proofRequested
	}
	// Fix 3: bound the evidence pass. evaluatePendingBalanceEvidenceTx rebuilds
	// the whole projection once per pending item (and once more to confirm a
	// deferred positive), so a pile of N pending checkpoints costs N full
	// replays in one transaction -- 379 of them on 2026-09-04. With a limit,
	// the window is cut at the as_of of the limit-th pending item, every item
	// sharing that as_of included; finishEligibilityProjectionJobRowTx then
	// requeues the row for the remainder because it still asks for more than
	// was published. A boundary between two items cannot change a decision:
	// an item deferred at the end of a pass is written nowhere (see the loop's
	// own comment on `pending`), so the next pass re-evaluates it from the
	// same durable facts as its first item, and the consecutive-match counter
	// lives in source_account_eligibility_state. The differential rehearsal
	// (single pass vs bounded on two restores of one backup) is the proof.
	if s.evidenceBatchLimit > 0 {
		// The bound counts only items strictly after finalized_through, so
		// every chunk advances past at least one new item. That is what makes
		// a chunk boundary safe against the deferral rule: an item deferred
		// at the end of a pass is written nowhere and sits at or below the
		// published boundary; it rides along with the next chunk, whose
		// first new item confirms or disconfirms it exactly as the single
		// pass would have. Counting it would let the boundary land on it
		// again -- deferred, unwritten, published at the same instant,
		// requeued -- forever. Carry-forward proofs the lock-free half
		// inserts at old cycles ride along the same way.
		if requested, err = evidenceBatchBoundaryTx(ctx, tx, accountID, account.FinalizedThrough, requested, s.evidenceBatchLimit); err != nil {
			return err
		}
	}
	// A window at or below the one already proved is covered by that proof:
	// its visibilities are a subset, and the proof's own rows already exist.
	if !proofDone {
		if err = ensureBalanceCarryForwardProofTx(ctx, tx, account, requested, actor); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, accountID); err != nil {
		return err
	}
	if err = reprojectEligibilityTx(ctx, tx, accountID, requested, actor); err != nil {
		return err
	}
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, requested, actor); err != nil {
		return err
	}
	// A positive checkpoint may have inserted a conservative non-cash fact at
	// the previous trusted boundary. Rebuild once more before publishing.
	if err = reprojectEligibilityTx(ctx, tx, accountID, requested, actor); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET finalized_through=GREATEST(finalized_through,$1),projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$2`, requested, accountID); err != nil {
		return err
	}
	if err = finishEligibilityProjectionJobRowTx(ctx, tx, accountID, lease, requested); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// finishEligibilityProjectionJobRowTx closes the job's own row once its window
// has been published: deleted when the row still asks for exactly that window,
// requeued -- lease cleared, due now, requested_through kept -- when a
// finalization pass raised the window while this job ran, so the remainder is
// processed with its own proof rather than dropped with the row.
func finishEligibilityProjectionJobRowTx(ctx context.Context, tx pgx.Tx, accountID, lease string, processed time.Time) error {
	var rowRequested time.Time
	err := tx.QueryRow(ctx, `
		SELECT requested_through FROM eligibility_projection_jobs
		WHERE external_account_id=$1 AND status='processing' AND lease_token=$2
		FOR UPDATE`, accountID, lease).Scan(&rowRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return err
	}
	var affected int64
	if rowRequested.After(processed) {
		command, execErr := tx.Exec(ctx, `
			UPDATE eligibility_projection_jobs SET status='queued',lease_token=NULL,lease_expires_at=NULL,
				next_attempt_at=now(),updated_at=now()
			WHERE external_account_id=$1 AND lease_token=$2`, accountID, lease)
		if execErr != nil {
			return execErr
		}
		affected = command.RowsAffected()
	} else {
		command, execErr := tx.Exec(ctx, `DELETE FROM eligibility_projection_jobs WHERE external_account_id=$1 AND lease_token=$2`, accountID, lease)
		if execErr != nil {
			return execErr
		}
		affected = command.RowsAffected()
	}
	if affected != 1 {
		return domain.ErrConflict
	}
	return nil
}

// carryCandidate is one published balances scan cycle that
// ensureBalanceCarryForwardProofTx could derive a delta carry-forward proof
// from for a single account, with the account's own prior real checkpoint
// (the row the proof must restate verbatim, enforced by migration 0026's
// trigger) and both exclusion flags already resolved by the candidate query.
// Package-level rather than function-local since XM-INV-PENDING-RECON, so
// insertBalanceCarryForwardProofTx can be shared by the fact-driven merge
// loop and the idle re-evaluation branch instead of the insert being written
// twice.
type carryCandidate struct {
	cycleID, batchID, snapshotID, cursor, revision string
	priorID, balance                               string
	priorDeficit                                   *string
	asOf, watermark, observed                      time.Time
	snapshotRows, sequence                         int64
	negative, baselineMember, hasRealCheckpoint    bool
	// hasStrandedCheckpoint (XM-INV-DEAD-CONTAINMENT A2) marks a cycle
	// that carries a balance_checkpoint event for this account which is
	// failed or dead behind an open freeze. See the candidate query and the
	// pending returns in ensureBalanceCarryForwardProofTx.
	hasStrandedCheckpoint bool
}

// insertBalanceCarryForwardProofTx writes one derived carry-forward proof for
// `item`, or -- when this account already has a proof at that cycle -- verifies
// that the existing row says exactly the same thing and leaves it alone. It is
// the single writer both of ensureBalanceCarryForwardProofTx's derivation paths
// go through: the fact-driven merge loop, and (XM-INV-PENDING-RECON C2) the
// idle re-evaluation branch, which sets idleReevaluation so the audit row says
// which path derived it. Callers must have already established that the cycle
// carries neither a real checkpoint nor a stranded one for this account.
//
// A conflicting existing row is domain.ErrConflict, never a silent overwrite:
// the (external_account_id, scan_cycle_id) uniqueness plus migration 0014's
// mutually exclusive proof/checkpoint contract make a proof immutable once
// written, so two different answers for the same cycle is a real inconsistency.
func insertBalanceCarryForwardProofTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount,
	item carryCandidate, idleReevaluation bool, actor AuditActor) error {
	proofKey := "carry-forward:" + item.cycleID + ":" + strings.ToLower(account.ExternalAccountID)
	proofID := randomUUID()
	command, err := tx.Exec(ctx, `
		INSERT INTO balance_carry_forward_proofs(
			id,source_instance_id,external_account_id,proof_key,prior_checkpoint_id,
			scan_cycle_id,final_batch_id,as_of,balance_service_units,balance_negative,
			baseline_member,source_snapshot_id,snapshot_row_count,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at,deficit_service_units)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::numeric)
		ON CONFLICT(external_account_id,scan_cycle_id) DO NOTHING`, proofID,
		account.SourceInstanceID, account.ExternalAccountID, proofKey, item.priorID,
		item.cycleID, item.batchID, item.asOf.UTC(), item.balance, item.negative,
		item.baselineMember, item.snapshotID, item.snapshotRows, item.sequence,
		item.cursor, item.watermark.UTC(), item.revision, item.observed.UTC(), item.priorDeficit)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		var existingKey, existingPrior, existingRevision string
		if err = tx.QueryRow(ctx, `SELECT proof_key,prior_checkpoint_id::text,source_revision_hash
			FROM balance_carry_forward_proofs
			WHERE external_account_id=$1 AND scan_cycle_id=$2::uuid`,
			account.ExternalAccountID, item.cycleID).Scan(&existingKey, &existingPrior, &existingRevision); err != nil {
			return err
		}
		if existingKey != proofKey || existingPrior != item.priorID || existingRevision != item.revision {
			return domain.ErrConflict
		}
		return nil
	}
	after := map[string]any{
		"proof_key": proofKey, "scan_cycle_id": item.cycleID,
		"prior_checkpoint_id": item.priorID, "source_revision": item.revision,
	}
	if idleReevaluation {
		after["idle_reevaluation"] = true
	}
	return writeAudit(ctx, tx, actor, "eligibility.balance_carry_forward.derived",
		"balance_carry_forward_proof", proofID, nil, after)
}

// pendingReconciliationIdleMinMatches is the consecutive-match streak at which
// idle re-evaluation becomes allowed: one short of the exit. Both halves of
// the feature read it -- finalizeSourceAccountsTx's enqueue predicate and
// deriveIdlePendingCarryForwardProofTx's own gate -- because a threshold that
// lives only in the enqueue is not a threshold at all. A projection job has
// several other sources (a real checkpoint landing in the window enqueues one
// regardless of any streak), and each of them reaches the derivation.
const pendingReconciliationIdleMinMatches = pendingReconciliationExitMatches - 1

// balanceEvidenceFloorCTE, pendingBalanceCheckpointPredicate and
// pendingBalanceProofPredicate are evaluatePendingBalanceEvidenceTx's own
// selection of "evidence I still owe a verdict on", rendered once so that
// nothing can hold a private copy of it. $1 is the account, $2 the window end.
//
// The floor is XM-INV-PREANCHOR-BALANCE's. Balance evidence dated before a
// POLICY_ANCHOR account's own cutover_at is not evidence about that account's
// ledger, because the ledger does not exist before the anchor: evaluating it
// anyway builds the expected balance from an empty window
// (buildEligibilityProjectionTx floors every fact query at cutover_at), so
// expected comes out 0, the whole reported balance reads as an unexplained
// positive difference, and balanceEvidenceTrustIntervalTx has no interval to
// anchor a synthesis against -- SOURCE_GAP, permanently. It is a fixed point,
// not a transient: RepairBalanceAnchorEligibility resolves the freeze and
// deletes the evaluation so the item goes pending again, the next projection
// redoes the identical arithmetic, and the freeze comes straight back.
// Production account 98cce4c8 sat there with four such checkpoints, from the
// 21 hours between its own first post-policy checkpoint and the RC68 deploy
// that first taught the system to bootstrap an anchor at all. This mirrors
// the fact side, which XM-INV-PREANCHOR-USAGE already taught to skip rather
// than freeze for the same reason (observeEligibilityFact's BootstrapKind
// guard). A legacy SIGNED_CUTOVER/POST_CUTOVER_REPLAY account keeps the old
// behaviour: its cutover replays real history to a known point, so evidence
// before it genuinely is a gap.
//
// Because that evidence is permanently out of scope, a count that omits the
// bound reports work that will never be done -- and anything gating on the
// count being zero then gates forever. The upper bound (as_of <= the window
// end) matters the same way in the other direction: evidence above the window
// is not this pass's work either.
const (
	balanceEvidenceFloorCTE = `evidence_floor AS (
		SELECT CASE WHEN state.bootstrap_kind='POLICY_ANCHOR'
			THEN state.cutover_at ELSE '-infinity'::timestamptz END AS anchor_floor
		FROM source_account_eligibility_state state
		WHERE state.external_account_id=$1
	)`
	pendingBalanceCheckpointPredicate = `checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='reconciliation'
			  AND checkpoint.as_of<=$2
			  AND checkpoint.as_of>=(SELECT anchor_floor FROM evidence_floor)
			  AND NOT EXISTS (SELECT 1 FROM balance_checkpoint_evaluations evaluation
				WHERE evaluation.checkpoint_id=checkpoint.id)`
	// unevaluatedCheckpointAboveFloorPredicate deliberately drops the upper
	// bound. "Does the evaluator still owe a verdict on real evidence?" is not
	// the same question as "what will it judge in this pass", and for the idle
	// derivation only the first one is safe: a checkpoint the finalization
	// delay is still holding back is precisely the one an account must not be
	// released ahead of. Bounding this by the window reopens the durable form
	// of the backfill failure -- the account goes active while the newest thing
	// the source said about it sits on file, unevaluated, disagreeing.
	unevaluatedCheckpointAboveFloorPredicate = `checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='reconciliation'
			  AND checkpoint.as_of>=(SELECT anchor_floor FROM evidence_floor)
			  AND NOT EXISTS (SELECT 1 FROM balance_checkpoint_evaluations evaluation
				WHERE evaluation.checkpoint_id=checkpoint.id)`
	pendingBalanceProofPredicate = `proof.external_account_id=$1 AND proof.as_of<=$2
			  AND proof.as_of>=(SELECT anchor_floor FROM evidence_floor)
			  AND NOT EXISTS (SELECT 1 FROM balance_carry_forward_evaluations evaluation
				WHERE evaluation.proof_id=proof.id)`
)

// countUnevaluatedBalanceEvidenceTx counts the balance evidence the evaluator
// still owes this account a verdict on, split by kind, using the predicates
// above -- literally the same text evaluatePendingBalanceEvidenceTx selects
// its work with. It has exactly two callers on purpose: the idle-derivation
// guard below, and invoice-eligibility-repair --kind=pending-reevaluate's own
// report. An operator reading "unevaluated: 0" and the code deciding whether
// to derive must be answering the same question with the same query.
//
// It used to carry its own copy that omitted both bounds, and the anchor floor
// is the one that bites: evidence below a POLICY_ANCHOR account's own anchor
// is never selected by the evaluator and therefore never gets an evaluation
// row, so such an account counted >0 forever. Both callers gate on zero, so
// the idle derivation would have been closed for that account permanently and
// the repair tool would have refused it permanently -- while telling the
// operator "the worker will handle it", which it never would.
//
// preAnchorCheckpoints is that excluded population, reported separately so the
// tool can say which of the two situations an operator is looking at instead
// of pretending the rows are not there.
type unevaluatedBalanceEvidence struct {
	// Owed is every real checkpoint at or above the anchor floor that has no
	// evaluation row -- whether or not this pass's window reaches it. This is
	// the number the idle-derivation guard and the repair tool gate on.
	Owed int
	// InWindowCheckpoints/InWindowProofs are the subset the current window
	// actually selects: what the evaluator will judge in this pass.
	InWindowCheckpoints int
	InWindowProofs      int
	// PreAnchor is the population the evaluator will never judge at all.
	PreAnchor int
}

func countUnevaluatedBalanceEvidenceTx(ctx context.Context, tx pgx.Tx, accountID string,
	through time.Time) (counts unevaluatedBalanceEvidence, err error) {
	err = tx.QueryRow(ctx, `
		WITH `+balanceEvidenceFloorCTE+`
		SELECT (SELECT count(*) FROM balance_reconciliation_checkpoints checkpoint
			WHERE `+unevaluatedCheckpointAboveFloorPredicate+`),
			(SELECT count(*) FROM balance_reconciliation_checkpoints checkpoint
			WHERE `+pendingBalanceCheckpointPredicate+`),
			(SELECT count(*) FROM balance_carry_forward_proofs proof
			WHERE `+pendingBalanceProofPredicate+`),
			(SELECT count(*) FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='reconciliation'
			  AND checkpoint.as_of<(SELECT anchor_floor FROM evidence_floor)
			  AND NOT EXISTS (SELECT 1 FROM balance_checkpoint_evaluations evaluation
				WHERE evaluation.checkpoint_id=checkpoint.id))`,
		accountID, through.UTC()).Scan(&counts.Owed, &counts.InWindowCheckpoints,
		&counts.InWindowProofs, &counts.PreAnchor)
	return counts, err
}

// deriveIdlePendingCarryForwardProofTx is XM-INV-PENDING-RECON C2: for an
// account parked in not_invoiceable_pending_reconciliation whose window
// carried no facts at all, derive one carry-forward proof so the state can
// clear instead of waiting forever for evidence the source will never send.
//
// Every gate below exists because the first review demonstrated the failure it
// prevents, on a real fixture. They are in this order and none may be skipped:
//
//  1. The streak gate. The acceptance ruling of 2026-09-09 (design section 7
//     D2(a)) narrowed idle re-evaluation to accounts already one match from
//     exiting; that narrowing was implemented only in the enqueue predicate,
//     which bounds nothing, because an ordinary real checkpoint landing in the
//     window enqueues the account whatever its streak is. A zero-streak
//     account was reaching this code and being released.
//
//  2. No unevaluated real checkpoint. This is the one that matters most. A
//     positive difference the ledger cannot explain is not classified on the
//     spot: XM-INV-BALANCE-BLIP defers it and waits for the *next independent*
//     piece of evidence to confirm or disconfirm it. An idle proof is not
//     independent of that checkpoint -- it restates it, at a later cycle
//     ceiling, against a projection that by construction has not moved, so the
//     difference is identical and the confirmation cannot fail. One upstream
//     observation then confirms itself, synthesizes a credit for the whole
//     unexplained amount, and releases the account. So: if the evaluator still
//     owes this account a verdict on real evidence, it gets to give it first.
//
//  3. Never below a real checkpoint. The candidates are walked newest-first,
//     and if the newest one already carries a real checkpoint the derivation
//     stops rather than falling back to an older empty cycle. Backfilling
//     under a newer checkpoint dates a restatement of stale numbers below
//     evidence that disagrees with them; the evaluator reads the stale proof
//     first, counts it as the exit match, and clears the pending columns --
//     including pending_reconciliation_since, which enterPendingReconciliationTx
//     promises to preserve across a re-entry -- before it ever reads the
//     checkpoint that disagrees. In the durable form of it the newer
//     checkpoint is not yet finalizable at all, so the account is released to
//     invoiceable while the newest thing the source said about it sits on file
//     unevaluated.
//
//  4. XM-INV-DEAD-CONTAINMENT A2 wins over deriving. If the cycle carries a
//     stranded balance_checkpoint for this account, wait: writing an immutable
//     "no checkpoint arrived here" proof would make migration 0014's
//     reject_real_checkpoint_after_carry_forward trigger refuse that
//     checkpoint forever, and an idle account stuck one more cycle is never
//     worth losing a replayable balance fact.
//
//  5. An open eligibility_freezes row suppresses derivation entirely.
//     advancePendingReconciliationMatchTx refuses to exit while one is open,
//     so a proof derived now could only produce evidence nobody can act on --
//     and every proof is immutable and burns its cycle's exclusivity
//     permanently.
func deriveIdlePendingCarryForwardProofTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount,
	carryCandidates []carryCandidate, requested time.Time, actor AuditActor) error {
	if len(carryCandidates) == 0 {
		return nil
	}
	var matches int
	if err := tx.QueryRow(ctx, `SELECT pending_reconciliation_consecutive_matches
		FROM source_account_eligibility_state WHERE external_account_id=$1`,
		account.ExternalAccountID).Scan(&matches); err != nil {
		return err
	}
	if matches < pendingReconciliationIdleMinMatches {
		return nil
	}
	unevaluated, err := countUnevaluatedBalanceEvidenceTx(ctx, tx, account.ExternalAccountID, requested)
	if err != nil {
		return err
	}
	if unevaluated.Owed > 0 {
		return nil
	}
	item := carryCandidates[len(carryCandidates)-1]
	if item.hasRealCheckpoint {
		return nil
	}
	if item.hasStrandedCheckpoint {
		return errBalanceCarryForwardProofPending
	}
	var hasOpenFreeze bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM eligibility_freezes WHERE external_account_id=$1 AND status='open')`,
		account.ExternalAccountID).Scan(&hasOpenFreeze); err != nil {
		return err
	}
	if hasOpenFreeze {
		return nil
	}
	return insertBalanceCarryForwardProofTx(ctx, tx, account, item, true, actor)
}

func ensureBalanceCarryForwardProofTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, requested time.Time, actor AuditActor) error {
	// XM-INV-PROOF-CONTENTION 2: the caller (processEligibilityProjectionJob)
	// deliberately does NOT hold the per-account advisory lock or a row lock
	// on the account here -- only its own SERIALIZABLE snapshot, taken
	// before this call, and the job-claim SKIP LOCKED handoff that already
	// guarantees no other processEligibilityProjectionJob execution for this
	// account is concurrently running. This function only reads (the
	// carry-forward proof rows it inserts are additive and idempotent, keyed
	// by (account,cycle) -- see the ON CONFLICT DO NOTHING below -- so a
	// concurrent writer elsewhere in the account's row set cannot corrupt
	// them; the caller re-locks for the write phase that follows). A
	// published delta cycle may still contain parked checkpoints for
	// unrelated identities; those must not block this account's unchanged
	// proof. A later checkpoint for this account is rejected by the mutually
	// exclusive proof/checkpoint contract.
	if account.Status == "syncing" {
		return errBalanceCarryForwardProofPending
	}
	var unmappedFunding bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM funding_lots lot
			WHERE lot.external_account_id=$1
			  AND lot.completed_at>$2 AND lot.completed_at<=$3
			  AND lot.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')
			  AND NOT EXISTS (
				SELECT 1 FROM source_events event
				-- Cast the TEXT side to uuid so this join hits the
			-- (source_instance_id, stream_id, event_id) index: the reverse
			-- cast event_id::text defeated every index and filtered ~63k
			-- rows per probe (75ms each), and a heavy account's replay makes
			-- thousands of such probes -- the first production whale wedged
			-- for hours on exactly this. The CASE guard keeps synthetic
			-- non-uuid external ids (e.g. carry-forward proof rows named
			-- "carry-cutover-event") on the original no-match semantics
			-- instead of a 22P02 cast error; CASE evaluation order is
			-- guaranteed, a bare regex AND is not.
			JOIN source_economic_scan_cycle_events mapped
				  ON mapped.source_instance_id=lot.source_instance_id
				 AND mapped.stream_id='payments'
				 AND mapped.event_id=CASE WHEN event.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN event.external_event_id::uuid END
				 AND mapped.payload_hash=lot.source_revision_hash
				JOIN source_economic_scan_cycles cycle
				  ON cycle.source_instance_id=mapped.source_instance_id
				 AND cycle.stream_id=mapped.stream_id
				 AND cycle.scan_cycle_id=mapped.scan_cycle_id
				WHERE event.id=lot.source_event_id AND cycle.cycle_status='published'
			  )
		)`, account.ExternalAccountID, account.FinalizedThrough, requested).Scan(&unmappedFunding); err != nil {
		return err
	}
	if unmappedFunding {
		return errBalanceCarryForwardProofInvalid
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT visibility_at FROM (
			SELECT stream_watermark_at AS visibility_at
			FROM source_usage_events
			WHERE external_account_id=$1 AND event_time>$2 AND event_time<=$3
			UNION ALL
			SELECT stream_watermark_at
			FROM source_credit_events
			WHERE external_account_id=$1 AND event_time>$2 AND event_time<=$3
			UNION ALL
			SELECT cycle.scan_ceiling_at
			FROM funding_lots lot
			JOIN source_events event ON event.id=lot.source_event_id
			JOIN source_economic_scan_cycle_events mapped
			  ON mapped.source_instance_id=lot.source_instance_id
			 AND mapped.stream_id='payments'
			 AND mapped.event_id=CASE WHEN event.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN event.external_event_id::uuid END
			 AND mapped.payload_hash=lot.source_revision_hash
			JOIN source_economic_scan_cycles cycle
			  ON cycle.source_instance_id=mapped.source_instance_id
			 AND cycle.stream_id=mapped.stream_id
			 AND cycle.scan_cycle_id=mapped.scan_cycle_id
			WHERE lot.external_account_id=$1
			  AND lot.completed_at>$2 AND lot.completed_at<=$3
			  AND lot.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')
			  AND cycle.cycle_status='published'
		) facts ORDER BY visibility_at`, account.ExternalAccountID, account.FinalizedThrough, requested)
	if err != nil {
		return err
	}
	visibilities := make([]time.Time, 0)
	for rows.Next() {
		var visibility time.Time
		if err = rows.Scan(&visibility); err != nil {
			rows.Close()
			return err
		}
		visibilities = append(visibilities, visibility.UTC())
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// XM-INV-PROOF-CONTENTION 4: set-based evaluation, replacing what used to
	// be up to two queries issued per entry of `visibilities` (each ~42ms on
	// production, up to ~1,150 checkpoints in the contention incident's
	// window -- tens of seconds per attempt, spent holding up whatever else
	// needed this account; see the design note on
	// processEligibilityProjectionJob). Both per-visibility lookups below are
	// "smallest candidate ceiling >= some threshold" queries; since
	// `visibilities` is fixed, sorted, and deduplicated up front, prefetch
	// every candidate once (bounded by the account's own real-checkpoint and
	// the source's published-cycle counts in the window -- not by how many
	// distinct visibilities they end up covering) and walk both lists with
	// `visibilities` using an ordinary ascending two-pointer merge, computing
	// identical results to the original per-visibility queries: a
	// same-shape, forward-only pointer is valid here for the same reason a
	// merge join is valid on two sorted inputs, and this loop already
	// establishes that visibilities themselves are processed in ascending,
	// deduplicated order.
	//
	// Real-checkpoint candidates first (query A below, unbounded above like
	// the original -- only the checkpoint's own as_of is bounded by
	// `requested`, not the cycle ceiling it's tied to): a checkpoint's own
	// arrival cycle is preferred over any delta-carry cycle regardless of
	// which has the smaller ceiling, exactly as the original code's
	// try-real-then-fall-back-to-carry order did.
	realCeilings := make([]time.Time, 0, len(visibilities))
	realRows, err := tx.Query(ctx, `
		SELECT cycle.scan_ceiling_at
		FROM balance_reconciliation_checkpoints checkpoint
		JOIN source_economic_scan_cycle_events mapped
		  ON mapped.source_instance_id=checkpoint.source_instance_id
		 AND mapped.stream_id='balances'
		 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
		 AND mapped.payload_hash=checkpoint.source_revision_hash
		JOIN source_economic_scan_cycles cycle
		  ON cycle.source_instance_id=mapped.source_instance_id
		 AND cycle.stream_id=mapped.stream_id
		 AND cycle.scan_cycle_id=mapped.scan_cycle_id
		WHERE checkpoint.external_account_id=$1
		  AND checkpoint.checkpoint_kind='reconciliation'
		  AND checkpoint.as_of>$2 AND checkpoint.as_of<=$3
		  AND cycle.cycle_status='published' AND cycle.scan_ceiling_at>=$2
		ORDER BY cycle.scan_ceiling_at,cycle.first_sequence`,
		account.ExternalAccountID, account.FinalizedThrough, requested.UTC())
	if err != nil {
		return err
	}
	for realRows.Next() {
		var ceiling time.Time
		if err = realRows.Scan(&ceiling); err != nil {
			realRows.Close()
			return err
		}
		realCeilings = append(realCeilings, ceiling.UTC())
	}
	if err = realRows.Err(); err != nil {
		realRows.Close()
		return err
	}
	realRows.Close()

	// Delta-carry candidates (query B below): every published balances cycle
	// in the window, each with its own prior-checkpoint delta and
	// has_real_checkpoint flag precomputed -- identical per-row logic to the
	// original, just evaluated for every candidate cycle once instead of
	// only for the ones an earlier per-visibility query happened to land on.
	carryCandidates := make([]carryCandidate, 0, len(visibilities))
	carryRows, err := tx.Query(ctx, `
		SELECT cycle.scan_cycle_id::text,batch.batch_id::text,cycle.scan_snapshot_id,
			cycle.scan_snapshot_row_count,cycle.scan_ceiling_at,cycle.stream_watermark_at,
			cycle.source_cursor,cycle.final_sequence,batch.body_hash,batch.source_captured_at,
			prior.id::text,prior.balance_service_units::text,prior.balance_negative,prior.baseline_member,
			prior.deficit_service_units::text,
			EXISTS (
				SELECT 1 FROM balance_reconciliation_checkpoints checkpoint
				JOIN source_economic_scan_cycle_events mapped
				  ON mapped.source_instance_id=checkpoint.source_instance_id
				 AND mapped.stream_id='balances'
				 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
				 AND mapped.payload_hash=checkpoint.source_revision_hash
				WHERE checkpoint.external_account_id=$1
				  AND mapped.scan_cycle_id=cycle.scan_cycle_id
			) AS has_real_checkpoint,
			-- XM-INV-DEAD-CONTAINMENT A2. Before this slice a dead
			-- balance_checkpoint held its whole scan cycle unpublished, so
			-- this loop never saw the cycle at all. Containment publishes the
			-- cycle, and the projection worker runs every two seconds -- so
			-- it would reach here long before an operator could requeue the
			-- event, write an immutable carry-forward proof asserting "no
			-- checkpoint arrived in this cycle", and migration 0014's
			-- reject_real_checkpoint_after_carry_forward trigger would then
			-- refuse the real checkpoint forever. A replayable balance fact
			-- would be lost permanently as a side effect of a containment
			-- decision made about a different concern.
			--
			-- So: if this cycle carries a stranded checkpoint for this
			-- account, the proof waits (errBalanceCarryForwardProofPending)
			-- instead of being written. The wait has two keys and both end
			-- it: requeue the event to processed, or acknowledge it as
			-- unreplayable. Both flip this flag false.
			--
			-- Two ways a stranded checkpoint counts as this account's, both
			-- rendered from sourceEventContainedByOpenFreezeSQL so they cannot
			-- drift from what the health surfaces call containment:
			--
			--   1. an open freeze on THIS account contains it -- the ordinary
			--      case, and the reason the account-blind health predicate is
			--      narrowed here;
			--   2. no open freeze contains it at all. Nothing then says whose
			--      checkpoint it is, so assuming it is not this account's is
			--      the assumption that loses a balance fact forever. An
			--      uncontained dead event also holds the whole source fatal
			--      already, so waiting costs nothing that is not already lost.
			--      Reachable only when a freeze was resolved out from under a
			--      still-dead event, which every resolution path now refuses
			--      (assertNoBlockingDeadEventForFreezeTx) -- this branch is
			--      what makes that refusal a defence in depth rather than the
			--      only thing standing between a repair run and the data loss.
			--
			-- A freeze belonging to a DIFFERENT account still does not hold
			-- this account's proof: that is case 1 being false and case 2
			-- being false together, and it is pinned by its own test arm.
			EXISTS (
				SELECT 1
				FROM source_economic_scan_cycle_events mapped
				JOIN source_ingest_events sie
				  ON sie.source_instance_id=mapped.source_instance_id
				 AND sie.stream_id=mapped.stream_id
				 AND sie.event_id=mapped.event_id
				WHERE mapped.source_instance_id=cycle.source_instance_id
				  AND mapped.stream_id='balances'
				  AND mapped.scan_cycle_id=cycle.scan_cycle_id
				  AND sie.entity_type='balance_checkpoint'
				  AND sie.processing_status IN ('failed','dead')
				  AND (`+sourceEventContainedByOpenFreezeSQL("sie", "ef.external_account_id=$1")+`
				       OR NOT `+sourceEventContainedByOpenFreezeSQL("sie")+`)
			) AS has_stranded_checkpoint
		FROM source_economic_scan_cycles cycle
		JOIN source_ingest_batches batch
		  ON batch.source_instance_id=cycle.source_instance_id
		 AND batch.stream_id=cycle.stream_id
		 AND batch.scan_cycle_id=cycle.scan_cycle_id
		 AND batch.sequence=cycle.final_sequence
		JOIN LATERAL (
			SELECT checkpoint.id,checkpoint.balance_service_units,
				checkpoint.balance_negative,checkpoint.baseline_member,checkpoint.deficit_service_units
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<cycle.scan_ceiling_at
			       OR (checkpoint.as_of=cycle.scan_ceiling_at
			           AND checkpoint.source_sequence<cycle.final_sequence))
			ORDER BY checkpoint.as_of DESC,checkpoint.source_sequence DESC,checkpoint.id DESC LIMIT 1
		) prior ON true
		WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
		  AND cycle.cycle_status='published'
		  AND cycle.scan_ceiling_at>=$3 AND cycle.scan_ceiling_at<=$4
		ORDER BY cycle.scan_ceiling_at,cycle.first_sequence`,
		account.ExternalAccountID, account.SourceInstanceID, account.FinalizedThrough, requested.UTC())
	if err != nil {
		return err
	}
	for carryRows.Next() {
		var item carryCandidate
		if err = carryRows.Scan(&item.cycleID, &item.batchID, &item.snapshotID, &item.snapshotRows,
			&item.asOf, &item.watermark, &item.cursor, &item.sequence, &item.revision,
			&item.observed, &item.priorID, &item.balance, &item.negative, &item.baselineMember,
			&item.priorDeficit, &item.hasRealCheckpoint, &item.hasStrandedCheckpoint); err != nil {
			carryRows.Close()
			return err
		}
		item.asOf, item.watermark, item.observed = item.asOf.UTC(), item.watermark.UTC(), item.observed.UTC()
		carryCandidates = append(carryCandidates, item)
	}
	if err = carryRows.Err(); err != nil {
		carryRows.Close()
		return err
	}
	carryRows.Close()

	// XM-INV-PENDING-RECON C2: idle re-evaluation. Everything above derives
	// proofs only at the visibility instants of real facts -- usage, credits,
	// cash lots. That is an efficiency rule, not a correctness one: it exists
	// so an account with nothing happening does not accumulate proofs nobody
	// needs. For an account sitting in not_invoiceable_pending_reconciliation
	// it is also the reason the state cannot clear. Exiting needs
	// pendingReconciliationExitMatches consecutive matched evaluations of real
	// balance evidence, and an idle account produces neither kind: the source
	// agent emits no checkpoint while its balance/negative flag/deficit are
	// unchanged, and with no facts there are no visibilities, so no proof is
	// derived either. A production account sat at one match short for days.
	//
	// So when this account is pending and the window carried no facts at all,
	// derive exactly one proof from the newest published balances cycle that
	// can still take one. This does not relax any exit rule: the proof is an
	// ordinary carry-forward proof (migration 0026's trigger still forces it
	// to restate the latest real checkpoint verbatim, deficit included), it is
	// evaluated by the ordinary evaluator, and it counts exactly as much as
	// any other item. The acceptance ruling of 2026-09-09 (design section 7
	// D1(a)) accepted that for an idle account the second of the N matches is
	// a restatement of the first, and recorded that trade explicitly.
	//
	// Ordering here is deliberate and is the whole reason this block sits
	// after the candidate query rather than before it:
	//
	//  1. XM-INV-DEAD-CONTAINMENT A2 wins. If the cycle we would derive from
	//     carries a stranded balance_checkpoint for this account, we wait,
	//     exactly as the merge loop does -- writing an immutable "no
	//     checkpoint arrived here" proof would make migration 0014's
	//     reject_real_checkpoint_after_carry_forward trigger refuse that
	//     checkpoint forever. An idle account being stuck one more cycle is
	//     never worth losing a replayable balance fact.
	//  2. An open eligibility_freezes row suppresses derivation entirely.
	//     advancePendingReconciliationMatchTx refuses to exit while one is
	//     open, so a proof derived now could only produce evidence that
	//     cannot be acted on -- and every proof is immutable and burns its
	//     cycle's exclusivity permanently. Nothing is lost by waiting: once
	//     the freeze resolves, the next published cycle derives normally.
	if len(visibilities) == 0 && account.Status == "not_invoiceable_pending_reconciliation" {
		return deriveIdlePendingCarryForwardProofTx(ctx, tx, account, carryCandidates, requested, actor)
	}

	coveredVisibility := account.FinalizedThrough.UTC()
	realIndex, carryIndex := 0, 0
	for _, visibility := range visibilities {
		if !visibility.After(coveredVisibility) {
			continue
		}
		// A real signed account checkpoint keeps its established semantics: its
		// own as_of must be finalizable, while the enclosing published cycle must
		// be at or after the fact's receiver visibility. Delta carry proofs are
		// stricter below because they derive as_of from the cycle ceiling itself.
		for realIndex < len(realCeilings) && realCeilings[realIndex].Before(visibility) {
			realIndex++
		}
		if realIndex < len(realCeilings) {
			coveredVisibility = realCeilings[realIndex]
			continue
		}
		for carryIndex < len(carryCandidates) && carryCandidates[carryIndex].asOf.Before(visibility) {
			carryIndex++
		}
		if carryIndex >= len(carryCandidates) {
			return errBalanceCarryForwardProofPending
		}
		item := carryCandidates[carryIndex]
		coveredVisibility = item.asOf
		if item.hasRealCheckpoint {
			continue
		}
		// XM-INV-DEAD-CONTAINMENT A2: wait rather than commit an immutable
		// "no checkpoint here" proof over a checkpoint that is stranded but
		// still replayable. !hasRealCheckpoint is already established above
		// and matters: a cycle whose real checkpoint did land must not be
		// held by some other stranded event of the same account.
		if item.hasStrandedCheckpoint {
			return errBalanceCarryForwardProofPending
		}
		if err = insertBalanceCarryForwardProofTx(ctx, tx, account, item, false, actor); err != nil {
			return err
		}
	}
	return nil
}

func applyPrePolicyWalletFundingTx(
	ctx context.Context,
	tx pgx.Tx,
	lotID, accountID string,
	account eligibilityAccount,
	in SourceObservation,
	cashUnits *big.Int,
	causalOrder *big.Int,
	actor AuditActor,
) error {
	var existingKind domain.EligibilityKind
	var verification domain.VerificationState
	var currentCap, reserved, issued int64
	if err := tx.QueryRow(ctx, `
		SELECT eligibility_kind,verification_state,current_cap_minor,reserved_minor,issued_minor
		FROM funding_lots WHERE id=$1 FOR UPDATE`, lotID).Scan(
		&existingKind, &verification, &currentCap, &reserved, &issued); err != nil {
		return err
	}
	if (existingKind != domain.EligibilityLegacyNonInvoiceable && existingKind != domain.EligibilityNonCash) ||
		reserved != 0 || issued != 0 {
		return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
			in.Lot.SourceRevision, actor)
	}
	verifiedHistoricalCash := int64(0)
	if verification == domain.VerificationVerified {
		verifiedHistoricalCash = currentCap
	}
	command, err := tx.Exec(ctx, `
		UPDATE funding_lots SET eligibility_kind='NON_CASH',eligibility_cutover_at=$1,
			verified_cash_minor=$2,consumed_cash_minor=0,
			eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE id=$3 AND eligibility_kind IN ('LEGACY_NON_INVOICEABLE','NON_CASH')
			AND reserved_minor=0 AND issued_minor=0`, account.PolicyStartAt, verifiedHistoricalCash, lotID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	causalOrderText := ""
	if causalOrder != nil {
		causalOrderText = causalOrder.String()
	}
	var existingUnits, existingUnitCode, existingManifest, existingConfiguration string
	var existingAt time.Time
	var existingDomain, existingOrder string
	err = tx.QueryRow(ctx, `
		SELECT service_units::text,event_time,unit_code,cutover_manifest_hash,
			configuration_hash,COALESCE(causal_domain,''),COALESCE(causal_order::text,'')
		FROM source_credit_events
		WHERE funding_lot_id=$1 AND credit_kind='PRE_POLICY_NON_INVOICEABLE'`, lotID).Scan(&existingUnits, &existingAt, &existingUnitCode,
		&existingManifest, &existingConfiguration, &existingDomain, &existingOrder)
	if err == nil {
		if existingUnits != cashUnits.String() || !existingAt.Equal(in.Lot.CompletedAt.UTC()) ||
			existingUnitCode != in.WalletUnitCode || existingManifest != in.CutoverManifestHash ||
			existingConfiguration != in.ConfigurationHash || existingDomain != in.CausalDomain ||
			existingOrder != causalOrderText {
			return freezeEligibilityTx(ctx, tx, accountID, lotID, "EVENT_PAYLOAD_DRIFT", "funding_lot", lotID,
				in.Lot.SourceRevision, actor)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	derivedID := randomUUID()
	derivedExternalID := "pre-policy:" + lotID
	_, err = tx.Exec(ctx, `
		INSERT INTO source_credit_events(
			id,source_instance_id,external_account_id,external_event_id,external_credit_id,
			funding_lot_id,event_time,service_units,unit_code,cutover_manifest_hash,
			configuration_hash,credit_kind,causal_domain,causal_order,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,$5,$6,$7::numeric,$8,$9,$10,
			'PRE_POLICY_NON_INVOICEABLE',NULLIF($11,''),$12::numeric,$13,$14,$15,$16,$17)`,
		derivedID, in.Lot.SourceInstanceID, accountID, derivedExternalID, lotID,
		in.Lot.CompletedAt.UTC(), cashUnits.String(), in.WalletUnitCode,
		in.CutoverManifestHash, in.ConfigurationHash, in.CausalDomain,
		nullableBigInt(causalOrder), in.SourceSequence, in.SourceCursor,
		in.StreamWatermarkAt.UTC(), in.Lot.SourceRevision, in.Lot.ObservedAt.UTC())
	if err != nil {
		return err
	}
	if !in.Lot.CompletedAt.After(account.FinalizedThrough) {
		return freezeEligibilityTx(ctx, tx, accountID, lotID, "LATE_FINALIZED_EVENT", "funding_lot", lotID,
			in.Lot.SourceRevision, actor)
	}
	return writeAudit(ctx, tx, actor, "eligibility.pre_policy_funding_classified", "funding_lot", lotID,
		nil, map[string]any{"eligibility_start_at": account.PolicyStartAt, "credit_event_id": derivedID})
}

// balanceEvidenceBoundaryTolerance bounds how close a usage event's
// event_time may be to a balance checkpoint/proof's as_of before
// evaluatePendingBalanceEvidenceTx's boundary rule (XM-INV-BALANCE-BLIP)
// treats it as a same-instant timing hazard rather than two genuinely
// distinct moments: the balances stream and the usage stream are captured
// independently on the source side, so two facts that are conceptually
// simultaneous there can still carry timestamps a small amount apart rather
// than bit-identical.
const balanceEvidenceBoundaryTolerance = time.Second

// balanceBlipRebaselineCap bounds XM-INV-BLIP-SOFTFAIL's rebaseline outcome
// (see the case-1 defer branch below): a blip confirmation attempt whose
// pre-check difference matched the deferred item exactly, but whose
// post-synthesis rebuild did not reconcile to zero, ignores the deferred
// item and rebaselines onto the current one rather than erroring. A
// production account whose ledger is missing a real structural fact (e.g. an
// anchor opening balance) can reproduce that exact non-reconciling shape on
// every subsequent checkpoint, forever -- once this many rebaselines have
// happened back to back for an account with no clean confirm/disconfirm in
// between, the account instead gets a single SOURCE_GAP freeze on the
// triggering item (the designed escalation) so the evaluator always makes
// forward progress instead of looping.
const balanceBlipRebaselineCap = 3

// signedExpectedUnits (XM-INV-PENDING-RECON C5, acceptance ruling 2026-09-09
// on design section 7 D4(a)) is the one balance the evaluator compares an
// upstream report against: what the ledger expects the account to hold, minus
// every unit of usage it could not charge to any pool.
//
// ExpectedBalance alone is not that number. It is floored at zero, because it
// is also what the evaluation row's expected_service_units column stores and
// that column carries a >=0 CHECK. The units below zero live in
// UnallocatedUnits -- carried cash debts plus non-invoice-eligible shortfalls
// -- and the source deducted every one of them, so an upstream balance has
// already absorbed them.
//
// XM-INV-NEGATIVE-DEFICIT taught the negative branch to subtract them; the
// positive branch was left comparing against the floored value, which is two
// different definitions of "expected" in one function. The consequence is not
// cosmetic: once an account's pools are exhausted, ExpectedBalance is pinned
// at zero while UnallocatedUnits keeps growing, so any account that carries a
// debt and is then topped up with non-cash credit reports a balance the
// ledger reads as unexplainably high (or, once the sign flips, as case -1)
// and is parked in pending reconciliation on every checkpoint. A production
// account did exactly that for days.
//
// Every comparison in the evaluator goes through this function -- the first
// classification, the blip-confirmation rebuild, the boundary rule, and the
// negative branch -- so the definition is physically in one place and the
// four cannot drift apart. Accounts with nothing unallocated (nearly all of
// them) get the identical value they got before.
func signedExpectedUnits(projection eligibilityProjection) *big.Int {
	expected := new(big.Int).Set(projection.ExpectedBalance)
	if projection.UnallocatedUnits != nil {
		expected.Sub(expected, projection.UnallocatedUnits)
	}
	return expected
}

func evaluatePendingBalanceEvidenceTx(ctx context.Context, tx pgx.Tx, accountID string, through time.Time, actor AuditActor) error {
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		WITH `+balanceEvidenceFloorCTE+`
		SELECT evidence_kind,id,evidence_key,external_event_id,as_of,balance_service_units,
			balance_negative,deficit_service_units,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at
		FROM (
			SELECT 'real'::text AS evidence_kind,checkpoint.id,
				checkpoint.checkpoint_id AS evidence_key,checkpoint.external_event_id,
				checkpoint.as_of,checkpoint.balance_service_units::text AS balance_service_units,
				checkpoint.balance_negative,checkpoint.deficit_service_units::text,
				checkpoint.source_sequence,checkpoint.source_cursor,
				checkpoint.stream_watermark_at,checkpoint.source_revision_hash,checkpoint.observed_at
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE `+pendingBalanceCheckpointPredicate+`
			UNION ALL
			SELECT 'carry'::text,proof.id,proof.proof_key,proof.proof_key,
				proof.as_of,proof.balance_service_units::text,proof.balance_negative,proof.deficit_service_units::text,
				proof.source_sequence,proof.source_cursor,proof.stream_watermark_at,
				proof.source_revision_hash,proof.observed_at
			FROM balance_carry_forward_proofs proof
			WHERE `+pendingBalanceProofPredicate+`
		) pending
		ORDER BY as_of,source_sequence,
			CASE evidence_kind WHEN 'real' THEN 0 ELSE 1 END,id`, accountID, through)
	if err != nil {
		return err
	}
	type proof struct {
		kind, id, key, externalEventID string
		balanceText, cursor, revision  string
		deficitText                    *string
		asOf, watermark, observed      time.Time
		sequence                       int64
		balanceNegative                bool
	}
	items := make([]proof, 0)
	for rows.Next() {
		var item proof
		if err = rows.Scan(&item.kind, &item.id, &item.key, &item.externalEventID,
			&item.asOf, &item.balanceText,
			&item.balanceNegative, &item.deficitText, &item.sequence, &item.cursor, &item.watermark,
			&item.revision, &item.observed); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	objectTypeOf := func(item proof) string {
		if item.kind == "carry" {
			return "balance_carry_forward_proof"
		}
		return "balance_checkpoint"
	}
	writeEvaluation := func(item proof, status string, expected, difference *big.Int) error {
		var execErr error
		if item.kind == "carry" {
			_, execErr = tx.Exec(ctx, `
				INSERT INTO balance_carry_forward_evaluations(
					id,proof_id,projection_version,expected_service_units,
					difference_service_units,evaluation_status)
				VALUES($1,$2,$3,$4::numeric,$5::numeric,$6)`, randomUUID(), item.id,
				account.Version+1, expected.String(), difference.String(), status)
		} else {
			_, execErr = tx.Exec(ctx, `
				INSERT INTO balance_checkpoint_evaluations(
					id,checkpoint_id,projection_version,expected_service_units,
					difference_service_units,evaluation_status)
				VALUES($1,$2,$3,$4::numeric,$5::numeric,$6)`, randomUUID(), item.id,
				account.Version+1, expected.String(), difference.String(), status)
		}
		if execErr != nil {
			return execErr
		}
		action := "eligibility.balance_checkpoint.evaluated"
		if item.kind == "carry" {
			action = "eligibility.balance_carry_forward.evaluated"
		}
		if err := writeAudit(ctx, tx, actor, action, objectTypeOf(item), item.id, nil, map[string]any{
			"status": status, "evidence_key": item.key,
		}); err != nil {
			return err
		}
		if status != "matched" {
			return nil
		}
		// XM-INV-ELIG-AUTO-RECONCILE: a real matched evaluation is exactly
		// the forward-progress signal that lets an account auto-exit
		// not_invoiceable_pending_reconciliation -- see
		// advancePendingReconciliationMatchTx's own doc comment. A no-op for
		// an account not currently in that state.
		return advancePendingReconciliationMatchTx(ctx, tx, accountID, actor)
	}
	// synthesizeUnknownPositive reports whether it actually inserted. The
	// blip-confirmation path below needs to know: its rollback deletes the
	// tentative credit, and "the tentative credit" means the row this
	// transaction just wrote -- never a pre-existing row that happens to sit
	// on the same synthetic event id. ON CONFLICT DO NOTHING makes those two
	// things different, and an older row is not tentative at all: projections
	// since have allocated consumption against it, and deleting it is refused
	// by consumption_allocations' own FK RESTRICT -- which fails the whole
	// account's projection job, on every retry, until it goes dead.
	synthesizeUnknownPositive := func(item proof, intervalStart time.Time, amount *big.Int) (bool, error) {
		command, execErr := tx.Exec(ctx, `
			INSERT INTO source_credit_events(
				id,source_instance_id,external_account_id,external_event_id,external_credit_id,
				event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
				credit_kind,source_sequence,source_cursor,stream_watermark_at,
				source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,
				'UNKNOWN_POSITIVE',$11,$12,$13,$14,$15)
			ON CONFLICT(source_instance_id,external_event_id) DO NOTHING`, randomUUID(),
			account.SourceInstanceID, accountID, "unknown-positive:"+item.externalEventID,
			"unknown-positive:"+item.id, intervalStart.UTC(), amount.String(),
			account.UnitCode, account.ManifestHash, account.ConfigurationHash,
			item.sequence, item.cursor, item.watermark.UTC(), item.revision, item.observed.UTC())
		if execErr != nil {
			return false, execErr
		}
		return command.RowsAffected() == 1, nil
	}

	// pending holds a checkpoint/proof whose positive difference was not
	// resolved by the boundary rule below and has not yet been confirmed or
	// disconfirmed by the next item in this ordered sequence -- see the
	// XM-INV-BALANCE-BLIP comment on the case-1 branch further down. At
	// most one item is ever deferred at a time: each iteration either
	// resolves the previous pending item (confirm or disconfirm) before
	// classifying the current one, or defers the current one in its place.
	type pendingBlip struct {
		item                 proof
		intervalStart        time.Time
		expected, difference *big.Int
	}
	var pending *pendingBlip

	for _, item := range items {
		balance, parseErr := parseUnsignedUnits(item.balanceText, "carry-forward balance", true)
		if parseErr != nil {
			return parseErr
		}
		projection, projectionErr := buildEligibilityProjectionTx(ctx, tx, account, item.asOf)
		if projectionErr != nil {
			return projectionErr
		}
		difference := new(big.Int).Sub(new(big.Int).Set(balance), signedExpectedUnits(projection))

		if pending != nil {
			if !item.balanceNegative && difference.Sign() == 1 && difference.Cmp(pending.difference) == 0 {
				// Tentatively confirmed: the very next piece of evidence
				// shows the exact same positive gap against an
				// otherwise-unchanged ledger -- looks like a genuine,
				// persistent discrepancy, not a one-off blip. Synthesize
				// using the deferred item's own original interval start and
				// amount (nothing was written for it while it was pending,
				// so recomputing its trust interval now would give the
				// identical answer anyway), then rebuild -- not trust
				// algebra -- to actually verify it.
				inserted, synthErr := synthesizeUnknownPositive(pending.item, pending.intervalStart, pending.difference)
				if synthErr != nil {
					return synthErr
				}
				confirmProjection, confirmErr := buildEligibilityProjectionTx(ctx, tx, account, item.asOf)
				if confirmErr != nil {
					return confirmErr
				}
				confirmDifference := new(big.Int).Sub(new(big.Int).Set(balance), signedExpectedUnits(confirmProjection))
				if confirmDifference.Sign() == 0 {
					// The credit just inserted is dated at or before this
					// item (pending.intervalStart <= pending.item.asOf <=
					// item.asOf), so it is now inside
					// buildEligibilityProjectionTx's window for this item,
					// and the rebuild proves it accounts for the entire gap.
					if err = writeEvaluation(pending.item, "positive_classified_non_cash", pending.expected, pending.difference); err != nil {
						return err
					}
					if err = writeEvaluation(item, "matched", confirmProjection.ExpectedBalance, confirmDifference); err != nil {
						return err
					}
					pending = nil
					continue
				}
				// XM-INV-BLIP-SOFTFAIL: the tentative credit does not
				// cleanly reconcile -- the pre-check match was real, but
				// some other fact (e.g. usage that already exceeded the
				// available credit pool at an intervening instant, floored
				// at zero, which the tentative credit only partially
				// recovers once it is dated earlier than that usage) means
				// this was never actually a simple confirmed blip; a
				// production account whose ledger is missing a real
				// structural fact (its own anchor opening balance) hit
				// exactly this shape. Returning an error here would fail
				// the whole account's projection job on every retry,
				// forever (see docs/handoffs/XM-INV-BLIP-SOFTFAIL.md) --
				// instead, undo the tentative credit, record the deferred
				// item as ignored (never confirmed), and let the current
				// item fall through to be classified fresh below, exactly
				// like an ordinary disconfirmation. Its own
				// difference/balanceNegative are unchanged since the top of
				// this iteration -- the deferred item's credit never
				// affected them; they were computed before it existed.
				// Only what this transaction wrote. When the insert above was a
				// no-op -- a credit already occupied that synthetic event id --
				// there is no tentative credit to undo, and the row that is
				// there is an older synthesis that projections have since
				// allocated consumption against. Deleting it is refused by
				// consumption_allocations' FK RESTRICT (SQLSTATE 23001), which
				// fails the whole account's projection job on every retry until
				// the failure grading gives up on it. RC108's shadow evaluation
				// hit exactly that on a production account with 8,336
				// allocations against four such credits.
				//
				// Not deleting it changes nothing else: the rebuild already
				// proved the ledger does not reconcile with that credit in
				// place, which is why this branch is running, and the deferred
				// item is recorded ignored either way.
				if inserted {
					if _, err = tx.Exec(ctx, `SELECT set_config('invoice.balance_blip_repair_delete','on',true)`); err != nil {
						return err
					}
					if _, err = tx.Exec(ctx, `
						DELETE FROM source_credit_events
						WHERE source_instance_id=$1 AND external_event_id=$2 AND credit_kind='UNKNOWN_POSITIVE'`,
						account.SourceInstanceID, "unknown-positive:"+pending.item.externalEventID); err != nil {
						return err
					}
				}
				if err = writeEvaluation(pending.item, "positive_blip_ignored", pending.expected, pending.difference); err != nil {
					return err
				}
				if err = writeAudit(ctx, tx, actor, "eligibility.balance_blip.rebaselined", objectTypeOf(pending.item), pending.item.id, nil, map[string]any{
					"deferred_difference":       pending.difference.String(),
					"confirming_item_key":       item.key,
					"confirming_raw_difference": difference.String(),
					"reconciliation_residual":   confirmDifference.String(),
					"tentative_credit_written":  inserted,
				}); err != nil {
					return err
				}
				pending = nil
			} else {
				// Disconfirmed: the ledger reconciled (or moved a different
				// way) without the deferred item's excess recurring, proving
				// it was transient. Record it as such and fall through to
				// classify the current item on its own merits below.
				if err = writeEvaluation(pending.item, "positive_blip_ignored", pending.expected, pending.difference); err != nil {
					return err
				}
				pending = nil
			}
		}

		status := "matched"
		if item.balanceNegative {
			// XM-INV-NEGATIVE-DEFICIT: a negative balance whose reported
			// magnitude equals the overdraw this projection still carries
			// (ShortfallUnits: usage no pool has covered yet) is the ledger
			// and the source agreeing -- the account burned to zero and its
			// last request overdrew by exactly that much. It evaluates
			// matched and never enters the pending state. Any other
			// magnitude, or an unknown one (evidence sealed before the
			// bridge reported deficits), keeps the XM-INV-ELIG-AUTO-RECONCILE
			// treatment: negative_frozen plus the self-clearing pending
			// state, never a manual eligibility_freezes row.
			// evaluation_status classifies what this piece of evidence
			// showed, independent of how the account is handled for it.
			status = "negative_frozen"
			detail := fmt.Sprintf("%s %s at %s reported a negative balance of unknown magnitude",
				objectTypeOf(item), item.key, item.asOf.UTC().Format(time.RFC3339Nano))
			if item.deficitText != nil {
				deficit, deficitErr := parseUnsignedUnits(*item.deficitText, "balance deficit service units", true)
				if deficitErr != nil {
					return deficitErr
				}
				// Signed arithmetic: the source reports -deficit; the ledger
				// expects ExpectedBalance minus every unit it could not allocate.
				expectedSigned := signedExpectedUnits(projection)
				difference = new(big.Int).Sub(new(big.Int).Neg(deficit), expectedSigned)
				if difference.Sign() == 0 {
					status = "matched"
				} else {
					detail = fmt.Sprintf("%s %s at %s reported balance -%s, expected %s (difference %s)",
						objectTypeOf(item), item.key, item.asOf.UTC().Format(time.RFC3339Nano),
						deficit.String(), expectedSigned.String(), difference.String())
				}
			}
			if status == "negative_frozen" {
				// XM-INV-PENDING-RECON C4: an item whose magnitude the source
				// never reported produced no difference above, so it is not
				// evidence that a previous streak of matches failed -- it
				// keeps the streak and, if the account is already pending,
				// does not log a second entry. An item that did report a
				// magnitude and disagrees keeps the original reset.
				if err = enterPendingReconciliationTx(ctx, tx, accountID, "UNKNOWN_NEGATIVE_BALANCE",
					objectTypeOf(item), item.key, detail, item.deficitText == nil, actor); err != nil {
					return err
				}
			}
		} else {
			switch difference.Sign() {
			case -1:
				status = "negative_frozen"
				// The signed expectation, not the floored ExpectedBalance, so
				// the three numbers in the detail stay self-consistent
				// (balance - expected = difference) and read the same way as
				// the negative branch's own detail above.
				detail := fmt.Sprintf("%s %s at %s reported balance %s, expected %s (difference %s)",
					objectTypeOf(item), item.key, item.asOf.UTC().Format(time.RFC3339Nano),
					balance.String(), signedExpectedUnits(projection).String(), difference.String())
				// A stated magnitude that disagrees with the ledger: the
				// streak really did fail to resolve the gap, so it resets.
				if err = enterPendingReconciliationTx(ctx, tx, accountID, "UNKNOWN_NEGATIVE_BALANCE",
					objectTypeOf(item), item.key, detail, false, actor); err != nil {
					return err
				}
			case 1:
				// XM-INV-BALANCE-BLIP boundary rule: a usage event landing
				// at or immediately before this item's as_of can make this
				// function's own inclusive projection (event_time<=through)
				// subtract a debit the upstream balance snapshot had not
				// yet applied at that exact instant -- production account
				// 40bd883d's checkpoint at 22:24:47Z coincided exactly with
				// a 3,667,080-unit usage event's own event_time (see
				// docs/handoffs/XM-INV-BALANCE-BLIP.md). Recomputing the
				// projection with that boundary usage excluded and
				// accepting an exact match there, before ever considering
				// synthesis or deferral, resolves this specific, narrow
				// timing coincidence immediately.
				resolvedExpected, boundaryErr := resolveBalanceEvidenceBoundaryUsageTx(ctx, tx, account, item.asOf, balance)
				if boundaryErr != nil {
					return boundaryErr
				}
				if resolvedExpected != nil {
					status = "matched"
					projection.ExpectedBalance = resolvedExpected
					difference = big.NewInt(0)
				} else if intervalStart, hasPriorRealEvaluation, intervalErr := balanceEvidenceTrustIntervalTx(
					ctx, tx, accountID, item.asOf, item.sequence); intervalErr != nil || intervalStart.IsZero() {
					status = "source_gap_frozen"
					if err = freezeEligibilityTx(ctx, tx, accountID, "", "SOURCE_GAP",
						objectTypeOf(item), item.key, item.revision, actor); err != nil {
						return err
					}
				} else if !hasPriorRealEvaluation {
					// XM-INV-ANCHOR-BALANCE: the account's first-ever real
					// balance evidence (a fresh POLICY_ANCHOR anchor
					// checkpoint, or a legacy account's first reconciliation
					// checkpoint against its own checkpoint_kind='cutover'
					// row) still self-heals immediately -- there is no
					// "next" evidence to confirm it against yet, and design
					// XM-INV-ANCHOR-BALANCE's own tests require this exact
					// behavior to keep working unchanged.
					status = "positive_classified_non_cash"
					if _, synthErr := synthesizeUnknownPositive(item, intervalStart, difference); synthErr != nil {
						return synthErr
					}
				} else {
					// XM-INV-BALANCE-BLIP: a positive difference on an
					// account that already has real prior balance-evidence
					// history must not synthesize a credit by itself --
					// defer, and let the next item in this ordered sequence
					// confirm (recurs with the exact same difference: a
					// real, persistent gap) or disconfirm (anything else: a
					// transient blip) it. Nothing is written for this item
					// yet; if it is the last item in this batch, it simply
					// stays pending for a future evaluator run to pair with
					// whatever real evidence arrives next.
					//
					// XM-INV-BLIP-SOFTFAIL: unless this account has already
					// rebaselined balanceBlipRebaselineCap times in a row
					// with no clean confirm/disconfirm landing in between --
					// a real, unresolved structural gap can reproduce the
					// exact same non-reconciling shape on every subsequent
					// checkpoint, forever. Escalate to the designed
					// SOURCE_GAP freeze on this item instead of deferring
					// again, so the account always makes forward progress.
					rebaselineStreak, streakErr := countConsecutiveRebaselinedBlipsTx(ctx, tx, accountID, item.asOf, item.sequence)
					if streakErr != nil {
						// XM-INV-PROJECTION-FAILURE-GRADING audit finding 2: this
						// is deliberately left a bare, retryable error, not turned
						// into a freeze -- an infrastructure error here (a
						// serialization failure, a momentary database error) says
						// nothing about whether the account's balance evidence is
						// actually a genuine structural gap, and freezing on it
						// would misclassify a transient problem as a real one.
						// markEligibilityProjectionJobFailedOrDead (above, in
						// ProcessEligibilityProjectionJobs) is what now grades and
						// retries this error with backoff, escalating to a
						// terminal, operator-visible status='dead' only after
						// projectionFailureDeadThreshold consecutive failures --
						// that grading is the correct home for "this keeps
						// failing," not a premature freeze here.
						return streakErr
					}
					if rebaselineStreak >= balanceBlipRebaselineCap {
						status = "source_gap_frozen"
						if err = freezeEligibilityTx(ctx, tx, accountID, "", "SOURCE_GAP",
							objectTypeOf(item), item.key, item.revision, actor); err != nil {
							return err
						}
					} else {
						pending = &pendingBlip{item: item, intervalStart: intervalStart,
							expected:   new(big.Int).Set(projection.ExpectedBalance),
							difference: new(big.Int).Set(difference)}
						continue
					}
				}
			}
		}
		if err = writeEvaluation(item, status, projection.ExpectedBalance, difference); err != nil {
			return err
		}
	}
	return nil
}

// resolveBalanceEvidenceBoundaryUsageTx implements the XM-INV-BALANCE-BLIP
// boundary rule described on evaluatePendingBalanceEvidenceTx's case-1
// branch: it looks for usage events landing within
// balanceEvidenceBoundaryTolerance at or before asOf and, if any exist,
// recomputes the projection with all of them excluded. A non-nil return is
// the exact ExpectedBalance that reconciles balance against that adjusted
// projection (their difference is exactly zero); nil means either no
// boundary usage exists, or excluding it does not exactly explain the
// difference, and the caller falls back to its own classification using the
// unadjusted (inclusive) projection already in hand.
func resolveBalanceEvidenceBoundaryUsageTx(ctx context.Context, tx pgx.Tx, account eligibilityAccount, asOf time.Time, balance *big.Int) (*big.Int, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM source_usage_events
		WHERE external_account_id=$1 AND event_time>$2 AND event_time<=$3 AND event_time>$4`,
		account.ExternalAccountID, asOf.Add(-balanceEvidenceBoundaryTolerance), asOf, account.CutoverAt)
	if err != nil {
		return nil, err
	}
	excludeUsageIDs := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		excludeUsageIDs[id] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(excludeUsageIDs) == 0 {
		return nil, nil
	}
	adjusted, err := buildEligibilityProjectionExcludingUsageTx(ctx, tx, account, asOf, excludeUsageIDs)
	if err != nil {
		return nil, err
	}
	// Compared against the same signed expectation every other comparison in
	// the evaluator uses (signedExpectedUnits); the value returned is still
	// the adjusted ExpectedBalance, because that is what the evaluation row's
	// expected_service_units column stores and that column has a >=0 CHECK.
	if new(big.Int).Sub(new(big.Int).Set(balance), signedExpectedUnits(adjusted)).Sign() != 0 {
		return nil, nil
	}
	return adjusted.ExpectedBalance, nil
}

// balanceEvidenceTrustIntervalTx is evaluatePendingBalanceEvidenceTx's
// trusted-interval-start query (design XM-INV-ANCHOR-BALANCE): for a
// positive difference to ever be treated as self-healing rather than a
// SOURCE_GAP freeze, some earlier point must already be trusted -- a legacy
// account's own checkpoint_kind='cutover' row, an earlier checkpoint/proof
// already evaluated matched or positive_classified_non_cash, or (design
// XM-INV-ANCHOR-BALANCE) a POLICY_ANCHOR account's own cutover_at.
// XM-INV-BALANCE-BLIP adds the second return value, hasPriorRealEvaluation:
// whether that trust came from an actual prior evaluation (the second or
// third UNION branch) rather than only a structural bootstrap anchor (the
// first or fourth branch). The account's very first piece of real balance
// evidence has no "next" item to confirm against and must keep
// self-healing immediately; every later positive difference has real
// history behind it and must defer instead (see evaluatePendingBalanceEvidenceTx's
// case-1 branch).
func balanceEvidenceTrustIntervalTx(ctx context.Context, tx pgx.Tx, accountID string, asOf time.Time, sequence int64) (intervalStart time.Time, hasPriorRealEvaluation bool, err error) {
	err = tx.QueryRow(ctx, `
		SELECT max(q.as_of),COALESCE(bool_or(q.is_real),FALSE) FROM (
			SELECT checkpoint.as_of,FALSE AS is_real FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='cutover'
			  AND (checkpoint.as_of<$2 OR (checkpoint.as_of=$2 AND checkpoint.source_sequence<$3))
			UNION ALL
			SELECT checkpoint.as_of,TRUE FROM balance_reconciliation_checkpoints checkpoint
			JOIN balance_checkpoint_evaluations evaluation ON evaluation.checkpoint_id=checkpoint.id
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<$2 OR (checkpoint.as_of=$2 AND checkpoint.source_sequence<$3))
			  AND evaluation.evaluation_status IN ('matched','positive_classified_non_cash')
			UNION ALL
			SELECT proof.as_of,TRUE FROM balance_carry_forward_proofs proof
			JOIN balance_carry_forward_evaluations evaluation ON evaluation.proof_id=proof.id
			WHERE proof.external_account_id=$1
			  AND (proof.as_of<$2 OR (proof.as_of=$2 AND proof.source_sequence<$3))
			  AND evaluation.evaluation_status IN ('matched','positive_classified_non_cash')
			UNION ALL
			SELECT state.cutover_at,FALSE FROM source_account_eligibility_state state
			WHERE state.external_account_id=$1 AND state.bootstrap_kind='POLICY_ANCHOR'
			  AND state.cutover_at<=$2
		) q`, accountID, asOf, sequence).Scan(&intervalStart, &hasPriorRealEvaluation)
	return intervalStart, hasPriorRealEvaluation, err
}

// countConsecutiveRebaselinedBlipsTx supports XM-INV-BLIP-SOFTFAIL's
// rebaseline cap: it counts how many of this account's most recent
// checkpoint/proof evaluations strictly before (asOf,sequence), taken in
// descending as_of/source_sequence order, are an unbroken run of
// positive_blip_ignored evaluations that were specifically produced by a
// failed blip confirmation attempt (an eligibility.balance_blip.rebaselined
// audit row exists for that exact item), stopping at the first evaluation
// that is either a different status or an ordinary positive_blip_ignored
// disconfirmation (XM-INV-BALANCE-BLIP's own pre-existing rule, not a failed
// rebaseline). Deliberately narrower than counting positive_blip_ignored
// alone: a run of genuine, clean disconfirmations is expected, healthy
// behavior with no risk of looping, and must never itself trip the cap. The
// result is only ever used against balanceBlipRebaselineCap, so the query
// stops early once it can no longer matter.
func countConsecutiveRebaselinedBlipsTx(ctx context.Context, tx pgx.Tx, accountID string, asOf time.Time, sequence int64) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT evaluation_status,rebaselined FROM (
			SELECT checkpoint.as_of,checkpoint.source_sequence,evaluation.evaluation_status,
				EXISTS(SELECT 1 FROM audit_events ae WHERE ae.action='eligibility.balance_blip.rebaselined'
					AND ae.object_type='balance_checkpoint' AND ae.object_id=checkpoint.id::text) AS rebaselined
			FROM balance_checkpoint_evaluations evaluation
			JOIN balance_reconciliation_checkpoints checkpoint ON checkpoint.id=evaluation.checkpoint_id
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<$2 OR (checkpoint.as_of=$2 AND checkpoint.source_sequence<$3))
			UNION ALL
			SELECT proof.as_of,proof.source_sequence,evaluation.evaluation_status,
				EXISTS(SELECT 1 FROM audit_events ae WHERE ae.action='eligibility.balance_blip.rebaselined'
					AND ae.object_type='balance_carry_forward_proof' AND ae.object_id=proof.id::text)
			FROM balance_carry_forward_evaluations evaluation
			JOIN balance_carry_forward_proofs proof ON proof.id=evaluation.proof_id
			WHERE proof.external_account_id=$1
			  AND (proof.as_of<$2 OR (proof.as_of=$2 AND proof.source_sequence<$3))
		) q ORDER BY as_of DESC,source_sequence DESC LIMIT $4`,
		accountID, asOf, sequence, balanceBlipRebaselineCap+1)
	if err != nil {
		return 0, err
	}
	count := 0
	for rows.Next() {
		var status string
		var rebaselined bool
		if scanErr := rows.Scan(&status, &rebaselined); scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		if status != "positive_blip_ignored" || !rebaselined {
			break
		}
		count++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	return count, nil
}

// pendingEvidenceAsOfQuery selects the pending balance-evidence items exactly
// as evaluatePendingBalanceEvidenceTx does (same anchor floor, same two
// sources, same order), restricted to as_of strictly after $4 (the published
// boundary), and returns the as_of of the item at OFFSET $3.
const pendingEvidenceAsOfQuery = `
	WITH evidence_floor AS (
		SELECT CASE WHEN state.bootstrap_kind='POLICY_ANCHOR'
			THEN state.cutover_at ELSE '-infinity'::timestamptz END AS anchor_floor
		FROM source_account_eligibility_state state
		WHERE state.external_account_id=$1
	), pending AS (
		SELECT checkpoint.as_of,checkpoint.source_sequence,0 AS kind_order,checkpoint.id
		FROM balance_reconciliation_checkpoints checkpoint
		WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='reconciliation'
		  AND checkpoint.as_of<=$2 AND checkpoint.as_of>$4
		  AND checkpoint.as_of>=(SELECT anchor_floor FROM evidence_floor)
		  AND NOT EXISTS (SELECT 1 FROM balance_checkpoint_evaluations evaluation
			WHERE evaluation.checkpoint_id=checkpoint.id)
		UNION ALL
		SELECT proof.as_of,proof.source_sequence,1,proof.id
		FROM balance_carry_forward_proofs proof
		WHERE proof.external_account_id=$1 AND proof.as_of<=$2 AND proof.as_of>$4
		  AND proof.as_of>=(SELECT anchor_floor FROM evidence_floor)
		  AND NOT EXISTS (SELECT 1 FROM balance_carry_forward_evaluations evaluation
			WHERE evaluation.proof_id=proof.id)
	)
	SELECT as_of FROM pending ORDER BY as_of,source_sequence,kind_order,id
	OFFSET $3 LIMIT 1`

// evidenceBatchBoundaryTx returns `through` cut down to the as_of of the
// limit-th pending balance-evidence item at or before it -- every item
// sharing that as_of included -- when more than limit items are pending;
// otherwise `through` unchanged.
func evidenceBatchBoundaryTx(ctx context.Context, tx pgx.Tx, accountID string, finalized, through time.Time, limit int) (time.Time, error) {
	if limit <= 0 || !through.After(finalized) {
		return through, nil
	}
	// Is there a (limit+1)-th new item at all? If not, the whole window fits.
	var overflow time.Time
	err := tx.QueryRow(ctx, pendingEvidenceAsOfQuery, accountID, through, limit, finalized).Scan(&overflow)
	if errors.Is(err, pgx.ErrNoRows) {
		return through, nil
	}
	if err != nil {
		return through, err
	}
	var lastIncluded time.Time
	if err = tx.QueryRow(ctx, pendingEvidenceAsOfQuery, accountID, through, limit-1, finalized).Scan(&lastIncluded); err != nil {
		return through, err
	}
	return lastIncluded.UTC(), nil
}
