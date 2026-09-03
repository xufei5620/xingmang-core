package sourceagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultEconomicSafetyDelay = 5 * time.Minute

type EconomicDBConnector struct {
	DB          *sql.DB
	Source      string
	Stream      string
	Manifest    CutoverManifest
	SafetyDelay time.Duration
	Now         func() time.Time
}

func (c *EconomicDBConnector) SourceType() string {
	if c == nil {
		return ""
	}
	return c.Source
}

type economicRow struct {
	SourceID     int64
	UserID       int64
	EventTime    time.Time
	ServiceUnits string
	CreditKind   sql.NullString
	CausalDomain string
	SourceCursor string
}

func (c *EconomicDBConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.DB == nil || (c.Source != SourceSub2API && c.Source != SourceNewAPI) ||
		(c.Stream != StreamUsage && c.Stream != StreamCredits) {
		return ScanPage{}, errors.New("economic connector is not configured")
	}
	if err := validateCutoverManifest(c.Manifest); err != nil || c.Manifest.SourceType != c.Source {
		return ScanPage{}, errors.New("economic connector cutover manifest is invalid")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	delay := c.SafetyDelay
	if delay == 0 {
		delay = defaultEconomicSafetyDelay
	}
	if delay < time.Minute || delay > 24*time.Hour {
		return ScanPage{}, errors.New("economic safety delay is outside the reviewed range")
	}
	abandoningLegacyReconcile := shouldAbandonLegacyReconcileCycle(req.Cursor,
		req.Cursor.Version == 0 || req.Cursor.Completed, req.Mode, c.Stream)
	cursor, err := c.prepareCursor(ctx, req, delay)
	if err != nil {
		return ScanPage{}, err
	}
	healthy, warning, err := c.projectionHealth(ctx)
	if err != nil {
		return ScanPage{}, err
	}
	if !healthy {
		cursor.Completed = true
		cursor.ProjectionBlocked = true
		return ScanPage{NextCursor: cursor, HasMore: false, ReconcileBlocked: true,
			Warnings: []string{warning}, StreamWatermarkAt: cursor.WatermarkAt, SourceCursor: cursor.WatermarkCursor,
			ScanCeilingAt: cursor.CeilingAt, ScanCeilingCursor: cursor.CeilingCursor,
			ScanCycleID: cursor.ScanCycleID, ScanComplete: false}, nil
	}
	limit := boundedScanLimit(req.Limit)
	domains := economicDomains(c.Source, c.Stream)
	positions, err := parseDomainCursor(cursor.PositionCursor, domains)
	if err != nil {
		return ScanPage{}, err
	}
	ceilings, err := parseDomainCursor(cursor.CeilingCursor, domains)
	if err != nil {
		return ScanPage{}, err
	}
	requestValues := map[string]any{"cutover": c.Manifest.CutoverAt, "horizon": cursor.CeilingAt, "limit": limit}
	for _, domain := range domains {
		requestValues[domain+"_position"] = positions[domain]
		requestValues[domain+"_ceiling"] = ceilings[domain]
	}
	request, err := marshalBridgeRequest(requestValues)
	if err != nil {
		return ScanPage{}, err
	}
	relation, err := bridgeJSONRecordRelation(c.Source, c.Stream, "page", "source_id bigint,user_id bigint,event_time timestamptz,service_units text,credit_kind text,causal_domain text,source_cursor text")
	if err != nil {
		return ScanPage{}, err
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT source_id,user_id,event_time,service_units,credit_kind,causal_domain,source_cursor FROM `+relation+` ORDER BY causal_domain,source_id`, request)
	if err != nil {
		return ScanPage{}, fmt.Errorf("read %s economic projection: %w", c.Stream, err)
	}
	defer rows.Close()
	values := make([]economicRow, 0, limit)
	for rows.Next() {
		var row economicRow
		if err = rows.Scan(&row.SourceID, &row.UserID, &row.EventTime, &row.ServiceUnits, &row.CreditKind, &row.CausalDomain, &row.SourceCursor); err != nil {
			return ScanPage{}, fmt.Errorf("scan %s economic projection: %w", c.Stream, err)
		}
		if row.SourceID <= 0 || row.UserID <= 0 || !serviceUnitsPattern.MatchString(row.ServiceUnits) || row.ServiceUnits == "0" ||
			!stableCursorPattern.MatchString(row.SourceCursor) || !causalDomainPattern.MatchString(row.CausalDomain) {
			return ScanPage{}, errors.New("economic projection returned an invalid row")
		}
		values = append(values, row)
	}
	if err = rows.Err(); err != nil {
		return ScanPage{}, errors.New("iterate economic projection failed")
	}
	hasMore := len(values) == limit
	next := cursor
	next.Completed = !hasMore
	next.ProjectionBlocked = false
	for _, value := range values {
		if value.SourceID <= positions[value.CausalDomain] {
			return ScanPage{}, errors.New("economic cursor did not advance")
		}
		positions[value.CausalDomain] = value.SourceID
	}
	next.PositionCursor = canonicalDomainCursor(domains, positions)
	if !hasMore {
		next.WatermarkAt = next.CeilingAt
		next.WatermarkCursor = next.CeilingCursor
		next.ReconcileBaselineCursor = reconcileBaselineCursorAfterCompletion(req.Mode, c.Stream, next.WatermarkCursor)
	}
	var pageWarnings []string
	if abandoningLegacyReconcile {
		pageWarnings = append(pageWarnings, "legacy_reconcile_cycle_abandoned")
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	projections := make([]Projection, 0, len(values))
	for _, row := range values {
		order := strconv.FormatInt(row.SourceID, 10)
		metadata := FactMetadata{SourceCursor: row.SourceCursor,
			CausalDomain: row.CausalDomain, CausalOrder: &order,
			CutoverManifestHash: c.Manifest.ManifestHash, ConfigurationHash: c.Manifest.ConfigurationHash}
		if c.Stream == StreamUsage {
			payload := UsageEventPayload{ExternalUserID: strconv.FormatInt(row.UserID, 10), ExternalUsageID: row.SourceCursor,
				OccurredAt: row.EventTime.UTC().Format(time.RFC3339Nano), ServiceUnits: row.ServiceUnits,
				UnitCode: unitCodeForSource(c.Source), BillingScope: "wallet", FactMetadata: metadata}
			projections = append(projections, Projection{EntityType: EntityUsageEvent, ExternalID: row.SourceCursor,
				ObservedAt: now().UTC().Format(time.RFC3339Nano), Operation: "upsert", Payload: payload})
			continue
		}
		kind := strings.TrimSpace(row.CreditKind.String)
		if kind != "bonus" && kind != "rebate" && kind != "admin" && kind != "unknown_positive" {
			return ScanPage{}, errors.New("credit projection returned an invalid kind")
		}
		payload := CreditEventPayload{ExternalUserID: strconv.FormatInt(row.UserID, 10), ExternalCreditID: row.SourceCursor,
			OccurredAt: row.EventTime.UTC().Format(time.RFC3339Nano), ServiceUnits: row.ServiceUnits,
			UnitCode: unitCodeForSource(c.Source), CreditKind: kind, FactMetadata: metadata}
		projections = append(projections, Projection{EntityType: EntityCreditEvent, ExternalID: row.SourceCursor,
			ObservedAt: now().UTC().Format(time.RFC3339Nano), Operation: "upsert", Payload: payload})
	}
	return ScanPage{Projections: projections, NextCursor: next, HasMore: hasMore, Warnings: pageWarnings,
		StreamWatermarkAt: next.WatermarkAt, SourceCursor: next.WatermarkCursor,
		ScanCeilingAt: next.CeilingAt, ScanCeilingCursor: next.CeilingCursor,
		ScanCycleID: next.ScanCycleID, ScanComplete: next.Completed}, nil
}

// shouldAbandonLegacyReconcileCycle reports whether an in-flight (not yet
// completed) ScanReconcile cycle must be abandoned and restarted fresh from
// the rolling-window baseline instead of resumed from its stored position.
// This is XM-INV-AGENT-RESTART-GRACE part B's upgrade path: an older agent
// binary always rewound a new ScanReconcile cycle to the cutover manifest, so
// a cycle left in flight by that binary (Completed=false, position possibly
// far behind the current watermark) predates the rolling reconcile window and
// would otherwise resume a stale, unbounded rescan for the rest of its
// duration. ReconcileWindowBounded is only ever set true by this package once
// a cycle's starting position has been computed by the new logic, so a stored
// cursor that lacks it (including every cursor written before this field
// existed) is unambiguously such a legacy cycle.
//
// Only the usage stream can carry this legacy state: credits already
// restarts its position at zero on every new cycle regardless of mode (see
// prepareCursor below), so it never rewound to the cutover manifest on
// ScanReconcile in the first place and has nothing to abandon.
func shouldAbandonLegacyReconcileCycle(cursor ScanCursor, newCycle bool, mode ScanMode, stream string) bool {
	return !newCycle && mode == ScanReconcile && stream == StreamUsage && !cursor.ReconcileWindowBounded
}

// reconcileBaselineCursorAfterCompletion decides the ReconcileBaselineCursor
// value to record when a scan page completes a cycle (!hasMore in Scan). It
// is usage-only: credits resets its position to zero on every new cycle
// regardless of mode (see prepareCursor's StreamCredits case below), so a
// rolling-window baseline is meaningless for it, and the stored-cursor
// validator (state_store_file.go validateStoredFileCursorForStream) rejects
// a non-empty ReconcileBaselineCursor on any stream other than StreamUsage.
//
// XM-INV-AGENT-CREDITS-RECONCILE-FIX: stamping this field unconditionally on
// every completed ScanReconcile page (regardless of stream) made every
// completed credits reconcile page produce a cursor the pending spool then
// permanently refused to accept, looping the credits stream's periodic
// reconcile forever in production and leaving its watermark stale.
func reconcileBaselineCursorAfterCompletion(mode ScanMode, stream, completedWatermarkCursor string) string {
	if mode == ScanReconcile && stream == StreamUsage {
		return completedWatermarkCursor
	}
	return ""
}

// reconcileWindowStart picks the domain positions a ScanReconcile cycle
// starts from, implementing the XM-INV-AGENT-RESTART-GRACE rolling reconcile
// window: resume from the previous completed reconcile's baseline so each
// periodic reconcile re-verifies only what was ingested since the last one,
// instead of rewinding all the way back to the cutover manifest. When no
// baseline is recorded yet (the very first reconcile ever, or a legacy
// in-flight cycle being abandoned on upgrade) it falls back to the live
// watermark -- i.e. no rewind beyond what is already known complete. Either
// value failing to parse against the current domain set (corrupt or
// stream-mismatched state) falls back the same way, and the cutover manifest
// position is always the floor: this must never rewind to before cutover.
func reconcileWindowStart(domains []string, baselineCursor, watermarkCursor string, cutoverPositions map[string]int64) map[string]int64 {
	positions, err := parseDomainCursor(baselineCursor, domains)
	if err != nil {
		positions, err = parseDomainCursor(watermarkCursor, domains)
	}
	if err != nil {
		positions = make(map[string]int64, len(domains))
		for _, domain := range domains {
			positions[domain] = cutoverPositions[domain]
		}
	}
	for _, domain := range domains {
		if positions[domain] < cutoverPositions[domain] {
			positions[domain] = cutoverPositions[domain]
		}
	}
	return positions
}

func (c *EconomicDBConnector) prepareCursor(ctx context.Context, req ScanRequest, delay time.Duration) (ScanCursor, error) {
	cursor := req.Cursor
	newCycle := cursor.Version == 0 || cursor.Completed
	domains := economicDomains(c.Source, c.Stream)
	cutoverCursor := c.Manifest.HighWaters[c.Stream].Cursor
	cutoverPositions, err := parseDomainCursor(cutoverCursor, domains)
	if err != nil {
		return ScanCursor{}, errors.New("cutover manifest stream ceiling is invalid")
	}
	if cursor.Version == 0 {
		positionCursor := cutoverCursor
		if c.Stream == StreamCredits {
			zero := map[string]int64{}
			for _, domain := range domains {
				zero[domain] = 0
			}
			positionCursor = canonicalDomainCursor(domains, zero)
		}
		cursor = ScanCursor{Revision: req.Cursor.Revision, Version: 2, CutoverAt: c.Manifest.CutoverAt,
			WatermarkAt: c.Manifest.CutoverAt, WatermarkCursor: cutoverCursor,
			CeilingAt: c.Manifest.CutoverAt, CeilingCursor: positionCursor, PositionCursor: positionCursor}
	} else if cursor.Version != 2 || cursor.CutoverAt != c.Manifest.CutoverAt {
		return ScanCursor{}, errors.New("economic cursor conflicts with the cutover manifest")
	}
	abandonLegacyCycle := shouldAbandonLegacyReconcileCycle(cursor, newCycle, req.Mode, c.Stream)
	if !newCycle && !abandonLegacyCycle {
		return cursor, nil
	}
	switch {
	case c.Stream == StreamCredits:
		zero := map[string]int64{}
		for _, domain := range domains {
			zero[domain] = 0
		}
		cursor.PositionCursor = canonicalDomainCursor(domains, zero)
	case req.Mode == ScanFull:
		cursor.PositionCursor = canonicalDomainCursor(domains, cutoverPositions)
	case req.Mode == ScanReconcile:
		// XM-INV-AGENT-RESTART-GRACE part B: a rolling window, not a rewind to
		// cutover. abandonLegacyCycle reaches this branch precisely when a
		// pre-upgrade in-flight cycle is being discarded; either way the
		// starting position is recomputed the same way a fresh cycle would be.
		cursor.PositionCursor = canonicalDomainCursor(domains,
			reconcileWindowStart(domains, cursor.ReconcileBaselineCursor, cursor.WatermarkCursor, cutoverPositions))
		cursor.ReconcileWindowBounded = true
	}
	var horizon time.Time
	if err := c.DB.QueryRowContext(ctx, `SELECT transaction_timestamp() - $1::interval`, delay.String()).Scan(&horizon); err != nil {
		return ScanCursor{}, errors.New("capture source event horizon failed")
	}
	positions, err := parseDomainCursor(cursor.PositionCursor, domains)
	if err != nil {
		return ScanCursor{}, err
	}
	ceilingPositions := make(map[string]int64, len(domains))
	for _, domain := range domains {
		position := positions[domain]
		var ceiling int64
		request, requestErr := marshalBridgeRequest(map[string]any{"cutover": c.Manifest.CutoverAt, "horizon": horizon.UTC().Format(time.RFC3339Nano), "position": position, "domain": domain})
		if requestErr != nil {
			return ScanCursor{}, requestErr
		}
		relation, relationErr := bridgeJSONRecordRelation(c.Source, c.Stream, "ceiling", "source_id bigint")
		if relationErr != nil {
			return ScanCursor{}, relationErr
		}
		err = c.DB.QueryRowContext(ctx, `SELECT source_id FROM `+relation, request).Scan(&ceiling)
		if c.Stream == StreamCredits && ceiling < cutoverPositions[domain] {
			ceiling = cutoverPositions[domain]
		}
		if err != nil {
			return ScanCursor{}, fmt.Errorf("capture %s domain ceiling: %w", c.Stream, err)
		}
		if ceiling < position {
			return ScanCursor{}, errors.New("economic projection domain ceiling regressed")
		}
		ceilingPositions[domain] = ceiling
	}
	if horizon.Before(mustParseTime(c.Manifest.CutoverAt)) {
		horizon = mustParseTime(c.Manifest.CutoverAt)
	}
	cursor.CeilingAt = horizon.UTC().Format(time.RFC3339Nano)
	cursor.CeilingCursor = canonicalDomainCursor(domains, ceilingPositions)
	cursor.ScanCycleID = deterministicEconomicScanCycleID(c.Manifest.SourceID, c.Stream, cursor.CeilingAt, cursor.CeilingCursor, cursor.Revision)
	cursor.Completed = false
	return cursor, nil
}

func (c *EconomicDBConnector) projectionHealth(ctx context.Context) (bool, string, error) {
	relation, err := bridgeJSONRecordRelation(c.Source, c.Stream, "health", "contract_ok boolean,blocked_reason text,total_rows bigint,gap_count bigint,configuration_hash text")
	if err != nil {
		return false, "", err
	}
	request, err := marshalBridgeRequest(nil)
	if err != nil {
		return false, "", err
	}
	var ok bool
	var reason string
	var configurationHash string
	var total, gaps int64
	if err := c.DB.QueryRowContext(ctx, `SELECT contract_ok,blocked_reason,total_rows,gap_count,configuration_hash FROM `+relation, request).Scan(&ok, &reason, &total, &gaps, &configurationHash); err != nil {
		return false, "", fmt.Errorf("read %s projection health: %w", c.Stream, err)
	}
	if total < 0 || gaps < 0 || (ok && reason != "") || (!ok && strings.TrimSpace(reason) == "") {
		return false, "", errors.New("economic projection health aggregate is inconsistent")
	}
	if configurationHash != c.Manifest.ConfigurationHash {
		return false, "configuration_drift", nil
	}
	return ok, reason, nil
}

func economicDomains(source, stream string) []string {
	if stream == StreamUsage {
		return []string{map[string]string{SourceSub2API: "usage_logs", SourceNewAPI: "logs"}[source]}
	}
	if source == SourceSub2API {
		return []string{"promo_code_usages", "user_affiliate_ledger", "redeem_codes"}
	}
	return []string{"checkins", "redemptions"}
}

func parseDomainCursor(raw string, domains []string) (map[string]int64, error) {
	result := make(map[string]int64, len(domains))
	for _, domain := range domains {
		result[domain] = -1
	}
	parts := strings.Split(raw, ";")
	for _, part := range parts {
		pair := strings.Split(part, ":")
		if len(pair) != 2 {
			return nil, errors.New("economic domain cursor is invalid")
		}
		if _, ok := result[pair[0]]; !ok || result[pair[0]] != -1 {
			return nil, errors.New("economic domain cursor has an unknown or duplicate domain")
		}
		id, err := strconv.ParseInt(pair[1], 10, 64)
		if err != nil || id < 0 {
			return nil, errors.New("economic domain cursor id is invalid")
		}
		result[pair[0]] = id
	}
	for _, domain := range domains {
		if result[domain] < 0 {
			return nil, errors.New("economic domain cursor is incomplete")
		}
	}
	return result, nil
}

func canonicalDomainCursor(domains []string, values map[string]int64) string {
	parts := make([]string, 0, len(domains))
	for _, domain := range domains {
		parts = append(parts, domain+":"+strconv.FormatInt(values[domain], 10))
	}
	return strings.Join(parts, ";")
}

func deterministicEconomicScanCycleID(sourceID, stream, ceilingAt, ceilingCursor string, committedRevision uint64) string {
	return deterministicUUID(strings.Join([]string{
		sourceID, stream, ceilingAt, ceilingCursor, strconv.FormatUint(committedRevision, 10),
	}, "\x00"))
}

func mustParseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

type BalanceDBConnector struct {
	DB       *sql.DB
	Source   string
	SourceID string
	Manifest CutoverManifest
	Baseline EncryptedStateFile
	Current  EncryptedStateFile
	Now      func() time.Time
}

const (
	balanceReconciliationStateSchemaVersionV1 = 1
	balanceReconciliationStateSchemaVersion   = 2
	maxRetiredBalanceAccounts                 = 100_000
)

// balanceReconciliationState is the durable sender-side bridge between an
// acknowledged balance cycle and the next one. CapturedSnapshot is the full
// source state used for the following comparison. EmissionSnapshot contains
// only new or semantically changed accounts and is the exact signed snapshot
// whose row count the receiver verifies. BaseSnapshotID makes a prepared cycle
// recoverable if the process stops after replacing Current but before writing
// the pending batch spool.
type balanceReconciliationState struct {
	SchemaVersion         int             `json:"schema_version"`
	StateKind             string          `json:"state_kind"`
	BaseSnapshotID        string          `json:"base_snapshot_id"`
	CapturedSnapshot      BalanceSnapshot `json:"captured_snapshot"`
	EmissionSnapshot      BalanceSnapshot `json:"emission_snapshot"`
	RetiredZeroAccountIDs []string        `json:"retired_zero_account_ids,omitempty"`
}

type preparedBalanceCycle struct {
	Snapshot      BalanceSnapshot
	CeilingCursor string
}

type economicContractQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkLiveEconomicContract(ctx context.Context, queryer economicContractQueryer, source, stream string, manifest CutoverManifest) error {
	if queryer == nil || manifest.SourceType != source || !hexHashPattern.MatchString(manifest.ConfigurationHash) {
		return errors.New("live economic contract configuration is invalid")
	}
	expectedContract, err := expectedEconomicProjectionContract(source)
	if err != nil || manifest.ProjectionContract != expectedContract {
		return errors.New("cutover projection contract does not match source type")
	}
	operation := "health"
	recordDefinition := "contract_ok boolean,configuration_hash text"
	if stream == StreamBalances {
		operation = "contract"
		recordDefinition = "projection_contract text,contract_ok boolean,configuration_hash text"
	} else if stream != StreamPayments && stream != StreamUsage && stream != StreamCredits {
		return errors.New("live economic contract stream is invalid")
	}
	relation, err := bridgeJSONRecordRelation(source, stream, operation, recordDefinition)
	if err != nil {
		return err
	}
	request, err := marshalBridgeRequest(nil)
	if err != nil {
		return err
	}
	var liveContract, liveHash string
	var contractOK bool
	query := `SELECT contract_ok,configuration_hash FROM ` + relation
	destinations := []any{&contractOK, &liveHash}
	if stream == StreamBalances {
		query = `SELECT projection_contract,contract_ok,configuration_hash FROM ` + relation
		destinations = []any{&liveContract, &contractOK, &liveHash}
	}
	if err = queryer.QueryRowContext(ctx, query, request).Scan(destinations...); err != nil {
		return fmt.Errorf("read live economic projection contract: %w", err)
	}
	if !contractOK {
		return errors.New("live economic projection contract is unhealthy")
	}
	if stream == StreamBalances && liveContract != manifest.ProjectionContract {
		return errors.New("live economic projection contract drifted")
	}
	if liveHash != manifest.ConfigurationHash {
		return errors.New("live economic configuration hash drifted")
	}
	return nil
}

// CheckLiveEconomicContract is the read-only V3 startup canary. It binds the
// currently callable bridge semantics to the encrypted, create-only cutover
// manifest before a production process is allowed to start its runner.
func CheckLiveEconomicContract(ctx context.Context, database *sql.DB, source, stream string, manifest CutoverManifest) error {
	if database == nil {
		return errors.New("live economic contract database is required")
	}
	return checkLiveEconomicContract(ctx, database, source, stream, manifest)
}

func (c *BalanceDBConnector) SourceType() string {
	if c == nil {
		return ""
	}
	return c.Source
}

func (c *BalanceDBConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.DB == nil || !uuidPattern.MatchString(c.SourceID) || c.Manifest.SourceID != c.SourceID || c.Manifest.SourceType != c.Source ||
		(c.Source != SourceSub2API && c.Source != SourceNewAPI) {
		return ScanPage{}, errors.New("balance connector is not configured")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	if err := checkLiveEconomicContract(ctx, c.DB, c.Source, StreamBalances, c.Manifest); err != nil {
		return ScanPage{}, errors.New("balance projection configuration drifted or is unhealthy")
	}
	cursor := req.Cursor
	var snapshot BalanceSnapshot
	var ceilingCursor string
	if cursor.Version == 0 {
		if err := c.Baseline.Load(ctx, &snapshot); err != nil {
			return ScanPage{}, err
		}
		if err := validateBalanceSnapshot(snapshot); err != nil || snapshot.SnapshotID != c.Manifest.BaselineSnapshotID {
			return ScanPage{}, errors.New("baseline snapshot does not match cutover manifest")
		}
		cursor = ScanCursor{Revision: req.Cursor.Revision, Version: 2, CutoverAt: c.Manifest.CutoverAt,
			WatermarkAt: c.Manifest.CutoverAt, WatermarkCursor: c.Manifest.HighWaters[StreamBalances].Cursor,
			CeilingAt: snapshot.AsOf, CeilingCursor: "balance_snapshot:" + lastBalanceUserID(snapshot.Rows), SnapshotID: snapshot.SnapshotID, SnapshotRowCount: int64(len(snapshot.Rows)), HasSnapshotMetadata: true}
		ceilingCursor = cursor.CeilingCursor
		cursor.PositionCursor = "balance_snapshot:0"
		cursor.ScanCycleID = deterministicUUID(strings.Join([]string{c.SourceID, StreamBalances, snapshot.AsOf, snapshot.SnapshotID}, "\x00"))
	} else {
		if cursor.Version != 2 || cursor.CutoverAt != c.Manifest.CutoverAt {
			return ScanPage{}, errors.New("balance cursor conflicts with cutover")
		}
		if cursor.Completed {
			cycle, err := c.prepareReconciliationCycle(ctx, cursor, c.captureReconciliation)
			if err != nil {
				return ScanPage{}, err
			}
			snapshot = cycle.Snapshot
			ceilingCursor = cycle.CeilingCursor
			cursor.Page = 0
			cursor.ID = 0
			cursor.Completed = false
			cursor.SnapshotID = snapshot.SnapshotID
			cursor.SnapshotRowCount = int64(len(snapshot.Rows))
			cursor.HasSnapshotMetadata = true
			cursor.CeilingAt = snapshot.AsOf
			cursor.CeilingCursor = ceilingCursor
			cursor.PositionCursor = "balance_snapshot:0"
			cursor.ScanCycleID = deterministicUUID(strings.Join([]string{c.SourceID, StreamBalances, snapshot.AsOf, snapshot.SnapshotID}, "\x00"))
		} else {
			cycle, err := c.loadBalanceCycle(ctx, cursor.SnapshotID)
			if err != nil {
				return ScanPage{}, err
			}
			snapshot = cycle.Snapshot
			ceilingCursor = cycle.CeilingCursor
			if cursor.CeilingCursor != ceilingCursor {
				return ScanPage{}, errors.New("durable balance cycle ceiling differs from cursor")
			}
		}
	}
	if err := validateBalanceSnapshot(snapshot); err != nil || snapshot.SnapshotID != cursor.SnapshotID {
		return ScanPage{}, errors.New("durable balance snapshot differs from cursor")
	}
	limit := boundedScanLimit(req.Limit)
	start := cursor.Page
	if start < 0 || start > len(snapshot.Rows) {
		return ScanPage{}, errors.New("balance cursor offset is invalid")
	}
	includeManifest := snapshot.CheckpointKind == "cutover" && cursor.BoundaryID == 0
	balanceLimit := limit
	if includeManifest {
		balanceLimit = 0
	}
	if balanceLimit < 0 {
		balanceLimit = 0
	}
	end := start + balanceLimit
	if end > len(snapshot.Rows) {
		end = len(snapshot.Rows)
	}
	hasMore := includeManifest || end < len(snapshot.Rows)
	next := cursor
	next.Page = end
	if end > 0 {
		next.PositionCursor = "balance_snapshot:" + snapshot.Rows[end-1].ExternalUserID
	}
	if includeManifest {
		next.BoundaryID = 1
	}
	next.Completed = !hasMore
	if !hasMore {
		next.WatermarkAt = snapshot.AsOf
		next.WatermarkCursor = next.CeilingCursor
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	projections := make([]Projection, 0, end-start+1)
	if includeManifest {
		projections = append(projections, Projection{EntityType: EntityCutoverManifest,
			ExternalID: c.Manifest.ManifestHash, ObservedAt: now().UTC().Format(time.RFC3339Nano),
			Operation: "upsert", Payload: c.Manifest.Payload()})
	}
	for _, row := range snapshot.Rows[start:end] {
		projections = append(projections, balanceCheckpointProjection(c.Manifest, snapshot, row, now().UTC()))
	}
	return ScanPage{Projections: projections, NextCursor: next, HasMore: hasMore,
		StreamWatermarkAt: next.WatermarkAt, SourceCursor: next.WatermarkCursor,
		ScanCeilingAt: next.CeilingAt, ScanCeilingCursor: next.CeilingCursor,
		ScanCycleID: next.ScanCycleID, ScanComplete: next.Completed,
		ScanSnapshotID: next.SnapshotID, ScanSnapshotRowCount: snapshotRowCountPtr(next.SnapshotRowCount)}, nil
}

func balanceCheckpointProjection(manifest CutoverManifest, snapshot BalanceSnapshot, row BalanceSnapshotRow, observedAt time.Time) Projection {
	order := row.ExternalUserID
	cursorValue := "balance_snapshot:" + row.ExternalUserID
	payload := BalanceCheckpointPayload{ExternalUserID: row.ExternalUserID,
		CheckpointID: snapshot.SnapshotID + ":" + row.ExternalUserID, CheckpointKind: snapshot.CheckpointKind,
		AsOf: snapshot.AsOf, BalanceServiceUnits: row.ServiceUnits, UnitCode: snapshot.UnitCode,
		SourceSnapshotID: snapshot.SnapshotID, SnapshotRowCount: strconv.Itoa(len(snapshot.Rows)),
		BalanceNegative: row.BalanceNegative, BaselineMember: row.BaselineMember,
		FactMetadata: FactMetadata{SourceCursor: cursorValue,
			CausalDomain: "balance_snapshot", CausalOrder: &order,
			CutoverManifestHash: manifest.ManifestHash, ConfigurationHash: manifest.ConfigurationHash}}
	return Projection{EntityType: EntityBalanceCheckpoint, ExternalID: payload.CheckpointID,
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano), Operation: "upsert", Payload: payload}
}

func (c *BalanceDBConnector) loadBalanceCycle(ctx context.Context, snapshotID string) (preparedBalanceCycle, error) {
	if snapshotID == c.Manifest.BaselineSnapshotID {
		var baseline BalanceSnapshot
		if err := c.Baseline.Load(ctx, &baseline); err != nil {
			return preparedBalanceCycle{}, err
		}
		if err := validateBalanceSnapshot(baseline); err != nil || baseline.SnapshotID != snapshotID {
			return preparedBalanceCycle{}, errors.New("baseline balance cycle is invalid")
		}
		return preparedBalanceCycle{Snapshot: baseline, CeilingCursor: "balance_snapshot:" + lastBalanceUserID(baseline.Rows)}, nil
	}
	state, legacy, exists, err := loadCurrentBalanceReconciliationState(ctx, c.Current)
	if err != nil {
		return preparedBalanceCycle{}, err
	}
	if !exists {
		return preparedBalanceCycle{}, errors.New("durable current balance cycle is missing")
	}
	if legacy != nil {
		if legacy.SnapshotID != snapshotID {
			return preparedBalanceCycle{}, errors.New("legacy durable balance cycle differs from cursor")
		}
		return preparedBalanceCycle{Snapshot: *legacy, CeilingCursor: "balance_snapshot:" + lastBalanceUserID(legacy.Rows)}, nil
	}
	if state.EmissionSnapshot.SnapshotID != snapshotID {
		return preparedBalanceCycle{}, errors.New("durable balance emission differs from cursor")
	}
	return preparedBalanceCycle{Snapshot: state.EmissionSnapshot,
		CeilingCursor: "balance_snapshot:" + lastBalanceUserID(state.CapturedSnapshot.Rows)}, nil
}

func (c *BalanceDBConnector) prepareReconciliationCycle(ctx context.Context, cursor ScanCursor, capture func(context.Context) (BalanceSnapshot, error)) (preparedBalanceCycle, error) {
	if !cursor.Completed || !hexHashPattern.MatchString(cursor.SnapshotID) || capture == nil {
		return preparedBalanceCycle{}, errors.New("balance reconciliation cycle preparation is invalid")
	}
	state, legacy, exists, err := loadCurrentBalanceReconciliationState(ctx, c.Current)
	if err != nil {
		return preparedBalanceCycle{}, err
	}
	var previous BalanceSnapshot
	var retired []string
	if exists && state != nil {
		switch cursor.SnapshotID {
		case state.BaseSnapshotID:
			// Current was durably prepared but the pending batch was not yet
			// written. Reuse it byte-for-byte instead of observing a newer state.
			return preparedBalanceCycle{Snapshot: state.EmissionSnapshot,
				CeilingCursor: "balance_snapshot:" + lastBalanceUserID(state.CapturedSnapshot.Rows)}, nil
		case state.EmissionSnapshot.SnapshotID:
			previous = state.CapturedSnapshot
			retired = state.RetiredZeroAccountIDs
		default:
			return preparedBalanceCycle{}, errors.New("durable balance reconciliation state conflicts with cursor")
		}
	} else if exists && legacy != nil {
		if cursor.SnapshotID != legacy.SnapshotID {
			return preparedBalanceCycle{}, errors.New("legacy durable balance snapshot conflicts with cursor")
		}
		previous = *legacy
	} else {
		if cursor.SnapshotID != c.Manifest.BaselineSnapshotID {
			return preparedBalanceCycle{}, errors.New("current balance state is missing after baseline")
		}
		if err = c.Baseline.Load(ctx, &previous); err != nil {
			return preparedBalanceCycle{}, err
		}
		if err = validateBalanceSnapshot(previous); err != nil || previous.SnapshotID != c.Manifest.BaselineSnapshotID {
			return preparedBalanceCycle{}, errors.New("immutable balance baseline is invalid")
		}
	}

	captured, err := capture(ctx)
	if err != nil {
		return preparedBalanceCycle{}, err
	}
	prepared, err := buildBalanceReconciliationStateWithRetired(cursor.SnapshotID, previous, captured, retired)
	if err != nil {
		return preparedBalanceCycle{}, err
	}
	if err = c.Current.Replace(ctx, prepared); err != nil {
		return preparedBalanceCycle{}, err
	}
	return preparedBalanceCycle{Snapshot: prepared.EmissionSnapshot,
		CeilingCursor: "balance_snapshot:" + lastBalanceUserID(prepared.CapturedSnapshot.Rows)}, nil
}

func loadCurrentBalanceReconciliationState(ctx context.Context, store EncryptedStateFile) (*balanceReconciliationState, *BalanceSnapshot, bool, error) {
	exists, err := store.Exists()
	if err != nil || !exists {
		return nil, nil, exists, err
	}
	var state balanceReconciliationState
	if stateErr := store.Load(ctx, &state); stateErr == nil {
		switch state.SchemaVersion {
		case balanceReconciliationStateSchemaVersion:
			if err = validateBalanceReconciliationState(state); err != nil {
				return nil, nil, true, err
			}
		case balanceReconciliationStateSchemaVersionV1:
			if state.StateKind != "balance_delta_v1" || len(state.RetiredZeroAccountIDs) != 0 {
				return nil, nil, true, errors.New("durable version-1 balance reconciliation state is invalid")
			}
			if err = validateBalanceReconciliationStateSnapshots(state); err != nil {
				return nil, nil, true, err
			}
			state.SchemaVersion = balanceReconciliationStateSchemaVersion
			state.StateKind = "balance_delta_v2"
			state.RetiredZeroAccountIDs = []string{}
		default:
			return nil, nil, true, errors.New("durable balance reconciliation state version is invalid")
		}
		return &state, nil, true, nil
	}
	var legacy BalanceSnapshot
	if legacyErr := store.Load(ctx, &legacy); legacyErr != nil {
		return nil, nil, true, errors.New("decode durable current balance state failed")
	}
	if err = validateBalanceSnapshot(legacy); err != nil || legacy.CheckpointKind != "reconciliation" {
		return nil, nil, true, errors.New("legacy durable current balance snapshot is invalid")
	}
	return nil, &legacy, true, nil
}

func buildBalanceReconciliationState(baseSnapshotID string, previous, captured BalanceSnapshot) (balanceReconciliationState, error) {
	return buildBalanceReconciliationStateWithRetired(baseSnapshotID, previous, captured, nil)
}

func buildBalanceReconciliationStateWithRetired(
	baseSnapshotID string,
	previous, captured BalanceSnapshot,
	retired []string,
) (balanceReconciliationState, error) {
	if !hexHashPattern.MatchString(baseSnapshotID) || validateBalanceSnapshot(previous) != nil || validateBalanceSnapshot(captured) != nil ||
		captured.CheckpointKind != "reconciliation" || previous.SourceID != captured.SourceID || previous.SourceType != captured.SourceType ||
		previous.CutoverAt != captured.CutoverAt || previous.UnitCode != captured.UnitCode {
		return balanceReconciliationState{}, errors.New("balance reconciliation snapshots are incompatible")
	}
	if err := validateRetiredBalanceAccountIDs(retired, captured.Rows); err != nil {
		return balanceReconciliationState{}, err
	}
	changed, newlyRetired, err := changedBalanceRowsAndRetirements(previous.Rows, captured.Rows)
	if err != nil {
		return balanceReconciliationState{}, err
	}
	retirementUnion, err := mergeRetiredBalanceAccountIDs(retired, newlyRetired)
	if err != nil {
		return balanceReconciliationState{}, err
	}
	emission := BalanceSnapshot{SchemaVersion: cutoverSchemaVersion, SourceID: captured.SourceID,
		SourceType: captured.SourceType, PreviousSnapshotID: baseSnapshotID, CheckpointKind: "reconciliation",
		AsOf: captured.AsOf, CutoverAt: captured.CutoverAt, UnitCode: captured.UnitCode, Rows: changed}
	emission.SnapshotID, err = balanceSnapshotID(emission)
	if err != nil {
		return balanceReconciliationState{}, err
	}
	state := balanceReconciliationState{SchemaVersion: balanceReconciliationStateSchemaVersion,
		StateKind: "balance_delta_v2", BaseSnapshotID: baseSnapshotID,
		CapturedSnapshot: captured, EmissionSnapshot: emission, RetiredZeroAccountIDs: retirementUnion}
	if err = validateBalanceReconciliationState(state); err != nil {
		return balanceReconciliationState{}, err
	}
	return state, nil
}

func changedBalanceRowsAndRetirements(previous, captured []BalanceSnapshotRow) ([]BalanceSnapshotRow, []string, error) {
	changed := make([]BalanceSnapshotRow, 0)
	newlyRetired := make([]string, 0)
	priorIndex := 0
	for _, row := range captured {
		for priorIndex < len(previous) && compareDecimalIDs(previous[priorIndex].ExternalUserID, row.ExternalUserID) < 0 {
			prior := previous[priorIndex]
			if prior.ServiceUnits != "0" || prior.BalanceNegative {
				return nil, nil, errors.New("balance projection removed an account; delta publication is unsafe")
			}
			newlyRetired = append(newlyRetired, prior.ExternalUserID)
			priorIndex++
		}
		if priorIndex >= len(previous) || compareDecimalIDs(previous[priorIndex].ExternalUserID, row.ExternalUserID) > 0 {
			if row.BaselineMember {
				return nil, nil, errors.New("post-cutover balance account claimed baseline membership")
			}
			changed = append(changed, row)
			continue
		}
		prior := previous[priorIndex]
		priorIndex++
		if prior.BaselineMember != row.BaselineMember {
			return nil, nil, errors.New("immutable balance baseline membership changed")
		}
		if prior.ServiceUnits != row.ServiceUnits || prior.BalanceNegative != row.BalanceNegative {
			changed = append(changed, row)
		}
	}
	for priorIndex < len(previous) {
		prior := previous[priorIndex]
		if prior.ServiceUnits != "0" || prior.BalanceNegative {
			return nil, nil, errors.New("balance projection removed an account; delta publication is unsafe")
		}
		newlyRetired = append(newlyRetired, prior.ExternalUserID)
		priorIndex++
	}
	return changed, newlyRetired, nil
}

func mergeRetiredBalanceAccountIDs(previous, newlyRetired []string) ([]string, error) {
	if len(previous) > maxRetiredBalanceAccounts || len(newlyRetired) > maxRetiredBalanceAccounts-len(previous) {
		return nil, errors.New("retired zero-balance account set exceeds the safety limit")
	}
	merged := make([]string, 0, len(previous)+len(newlyRetired))
	previousIndex := 0
	newIndex := 0
	for previousIndex < len(previous) && newIndex < len(newlyRetired) {
		switch compareDecimalIDs(previous[previousIndex], newlyRetired[newIndex]) {
		case -1:
			merged = append(merged, previous[previousIndex])
			previousIndex++
		case 0:
			return nil, errors.New("retired zero-balance account IDs are duplicated")
		default:
			merged = append(merged, newlyRetired[newIndex])
			newIndex++
		}
	}
	merged = append(merged, previous[previousIndex:]...)
	merged = append(merged, newlyRetired[newIndex:]...)
	return merged, nil
}

func validateBalanceReconciliationState(state balanceReconciliationState) error {
	if state.SchemaVersion != balanceReconciliationStateSchemaVersion || state.StateKind != "balance_delta_v2" {
		return errors.New("durable balance reconciliation state is invalid")
	}
	if err := validateBalanceReconciliationStateSnapshots(state); err != nil {
		return err
	}
	return validateRetiredBalanceAccountIDs(state.RetiredZeroAccountIDs, state.CapturedSnapshot.Rows)
}

func validateBalanceReconciliationStateSnapshots(state balanceReconciliationState) error {
	if !hexHashPattern.MatchString(state.BaseSnapshotID) || validateBalanceSnapshot(state.CapturedSnapshot) != nil ||
		validateBalanceSnapshot(state.EmissionSnapshot) != nil || state.CapturedSnapshot.CheckpointKind != "reconciliation" ||
		state.EmissionSnapshot.CheckpointKind != "reconciliation" || state.EmissionSnapshot.PreviousSnapshotID != state.BaseSnapshotID ||
		state.CapturedSnapshot.SourceID != state.EmissionSnapshot.SourceID || state.CapturedSnapshot.SourceType != state.EmissionSnapshot.SourceType ||
		state.CapturedSnapshot.AsOf != state.EmissionSnapshot.AsOf || state.CapturedSnapshot.CutoverAt != state.EmissionSnapshot.CutoverAt ||
		state.CapturedSnapshot.UnitCode != state.EmissionSnapshot.UnitCode {
		return errors.New("durable balance reconciliation state is invalid")
	}
	capturedIndex := 0
	for _, row := range state.EmissionSnapshot.Rows {
		for capturedIndex < len(state.CapturedSnapshot.Rows) && compareDecimalIDs(state.CapturedSnapshot.Rows[capturedIndex].ExternalUserID, row.ExternalUserID) < 0 {
			capturedIndex++
		}
		if capturedIndex >= len(state.CapturedSnapshot.Rows) || state.CapturedSnapshot.Rows[capturedIndex] != row {
			return errors.New("balance emission snapshot is not an exact captured subset")
		}
		capturedIndex++
	}
	return nil
}

func validateRetiredBalanceAccountIDs(ids []string, captured []BalanceSnapshotRow) error {
	if len(ids) > maxRetiredBalanceAccounts {
		return errors.New("retired zero-balance account set exceeds the safety limit")
	}
	previousID := ""
	capturedIndex := 0
	for _, id := range ids {
		if !externalReferencePattern.MatchString(id) || (previousID != "" && compareDecimalIDs(previousID, id) >= 0) {
			return errors.New("retired zero-balance account IDs are invalid or unordered")
		}
		for capturedIndex < len(captured) && compareDecimalIDs(captured[capturedIndex].ExternalUserID, id) < 0 {
			capturedIndex++
		}
		if capturedIndex < len(captured) && captured[capturedIndex].ExternalUserID == id {
			return errors.New("retired zero-balance account appears in captured rows")
		}
		previousID = id
	}
	return nil
}

func snapshotRowCountPtr(value int64) *int64 { return &value }

func (c *BalanceDBConnector) captureReconciliation(ctx context.Context) (BalanceSnapshot, error) {
	var baseline BalanceSnapshot
	if err := c.Baseline.Load(ctx, &baseline); err != nil {
		return BalanceSnapshot{}, fmt.Errorf("load immutable balance baseline membership: %w", err)
	}
	if err := validateBalanceSnapshot(baseline); err != nil || baseline.CheckpointKind != "cutover" || baseline.SnapshotID != c.Manifest.BaselineSnapshotID {
		return BalanceSnapshot{}, errors.New("immutable balance baseline membership is invalid")
	}
	baselineUsers := make(map[string]struct{}, len(baseline.Rows))
	for _, row := range baseline.Rows {
		baselineUsers[row.ExternalUserID] = struct{}{}
	}
	tx, err := c.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return BalanceSnapshot{}, err
	}
	defer tx.Rollback()
	var readOnly string
	if err = tx.QueryRowContext(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil || readOnly != "on" {
		return BalanceSnapshot{}, errors.New("balance snapshot transaction is not read only")
	}
	var asOf time.Time
	if err = tx.QueryRowContext(ctx, `SELECT transaction_timestamp()`).Scan(&asOf); err != nil {
		return BalanceSnapshot{}, err
	}
	if err = checkLiveEconomicContract(ctx, tx, c.Source, StreamBalances, c.Manifest); err != nil {
		return BalanceSnapshot{}, errors.New("balance snapshot configuration drifted or is unhealthy")
	}
	balanceRelation, err := bridgeJSONRecordRelation(c.Source, StreamBalances, "rows", "user_id bigint,balance_service_units text,balance_negative boolean")
	if err != nil {
		return BalanceSnapshot{}, err
	}
	request, err := marshalBridgeRequest(nil)
	if err != nil {
		return BalanceSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id,balance_service_units,balance_negative FROM `+balanceRelation+` ORDER BY user_id`, request)
	if err != nil {
		return BalanceSnapshot{}, err
	}
	values := make([]BalanceSnapshotRow, 0, 4096)
	for rows.Next() {
		var id int64
		var units string
		var negative bool
		if err = rows.Scan(&id, &units, &negative); err != nil || id <= 0 || !serviceUnitsPattern.MatchString(units) {
			_ = rows.Close()
			return BalanceSnapshot{}, errors.New("balance projection row is invalid")
		}
		externalID := strconv.FormatInt(id, 10)
		_, member := baselineUsers[externalID]
		values = append(values, BalanceSnapshotRow{ExternalUserID: externalID, ServiceUnits: units, BalanceNegative: negative, BaselineMember: member})
		if len(values) > balanceSnapshotMaxRows {
			_ = rows.Close()
			return BalanceSnapshot{}, errors.New("balance projection exceeds bound")
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return BalanceSnapshot{}, err
	}
	if err = rows.Close(); err != nil {
		return BalanceSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return BalanceSnapshot{}, err
	}
	snapshot := BalanceSnapshot{SchemaVersion: cutoverSchemaVersion, SourceID: c.SourceID, SourceType: c.Source,
		CheckpointKind: "reconciliation", AsOf: asOf.UTC().Format(time.RFC3339Nano), CutoverAt: c.Manifest.CutoverAt,
		UnitCode: unitCodeForSource(c.Source), Rows: values}
	snapshot.SnapshotID, err = balanceSnapshotID(snapshot)
	return snapshot, err
}

func lastBalanceUserID(rows []BalanceSnapshotRow) string {
	if len(rows) == 0 {
		return "0"
	}
	return rows[len(rows)-1].ExternalUserID
}
