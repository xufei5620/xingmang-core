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
	view := economicProjectionView(c.Source, c.Stream)
	domains := economicDomains(c.Source, c.Stream)
	positions, err := parseDomainCursor(cursor.PositionCursor, domains)
	if err != nil {
		return ScanPage{}, err
	}
	ceilings, err := parseDomainCursor(cursor.CeilingCursor, domains)
	if err != nil {
		return ScanPage{}, err
	}
	creditColumn := "NULL::text"
	if c.Stream == StreamCredits {
		creditColumn = "credit_kind"
	}
	clauses := make([]string, 0, len(domains))
	args := make([]any, 0, len(domains)*3+1)
	for index, domain := range domains {
		base := index * 3
		clauses = append(clauses, fmt.Sprintf("(causal_domain=$%d AND source_id>$%d AND source_id<=$%d)", base+1, base+2, base+3))
		args = append(args, domain, positions[domain], ceilings[domain])
	}
	args = append(args, c.Manifest.CutoverAt, mustParseTime(cursor.CeilingAt))
	cutoverIndex := len(args) - 1
	horizonIndex := len(args)
	args = append(args, limit)
	query := fmt.Sprintf(`SELECT source_id,user_id,event_time,service_units,%s,causal_domain,source_cursor
FROM %s WHERE (%s) AND event_time>=$%d AND event_time<=$%d ORDER BY causal_domain,source_id LIMIT $%d`, creditColumn, view, strings.Join(clauses, " OR "), cutoverIndex, horizonIndex, len(args))
	rows, err := c.DB.QueryContext(ctx, query, args...)
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
	return ScanPage{Projections: projections, NextCursor: next, HasMore: hasMore,
		StreamWatermarkAt: next.WatermarkAt, SourceCursor: next.WatermarkCursor,
		ScanCeilingAt: next.CeilingAt, ScanCeilingCursor: next.CeilingCursor,
		ScanCycleID: next.ScanCycleID, ScanComplete: next.Completed}, nil
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
	if !newCycle {
		return cursor, nil
	}
	if c.Stream == StreamCredits {
		zero := map[string]int64{}
		for _, domain := range domains {
			zero[domain] = 0
		}
		cursor.PositionCursor = canonicalDomainCursor(domains, zero)
	} else if req.Mode == ScanFull || req.Mode == ScanReconcile {
		cursor.PositionCursor = canonicalDomainCursor(domains, cutoverPositions)
	}
	var horizon time.Time
	if err := c.DB.QueryRowContext(ctx, `SELECT transaction_timestamp() - $1::interval`, delay.String()).Scan(&horizon); err != nil {
		return ScanCursor{}, errors.New("capture source event horizon failed")
	}
	view := economicProjectionView(c.Source, c.Stream)
	positions, err := parseDomainCursor(cursor.PositionCursor, domains)
	if err != nil {
		return ScanCursor{}, err
	}
	ceilingPositions := make(map[string]int64, len(domains))
	for _, domain := range domains {
		position := positions[domain]
		var ceiling int64
		query := fmt.Sprintf(`SELECT COALESCE(max(source_id) FILTER (WHERE event_time>=$1 AND event_time<=$2),$3)::bigint FROM %s WHERE causal_domain=$4 AND source_id>$3`, view)
		if c.Stream == StreamUsage {
			query = fmt.Sprintf(`SELECT COALESCE(CASE WHEN min(source_id) FILTER (WHERE event_time>$1) IS NOT NULL THEN min(source_id) FILTER (WHERE event_time>$1)-1 ELSE max(source_id) END,$2)::bigint FROM %s WHERE causal_domain=$3 AND source_id>$2`, view)
		}
		if c.Stream == StreamCredits {
			err = c.DB.QueryRowContext(ctx, query, c.Manifest.CutoverAt, horizon, position, domain).Scan(&ceiling)
			if ceiling < cutoverPositions[domain] {
				ceiling = cutoverPositions[domain]
			}
		} else {
			err = c.DB.QueryRowContext(ctx, query, horizon, position, domain).Scan(&ceiling)
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
	cursor.ScanCycleID = deterministicUUID(strings.Join([]string{c.Manifest.SourceID, c.Stream, cursor.CeilingAt, cursor.CeilingCursor}, "\x00"))
	cursor.Completed = false
	return cursor, nil
}

func (c *EconomicDBConnector) projectionHealth(ctx context.Context) (bool, string, error) {
	view := economicHealthView(c.Source, c.Stream)
	var ok bool
	var reason string
	var configurationHash string
	var total, gaps int64
	if err := c.DB.QueryRowContext(ctx, fmt.Sprintf(`SELECT contract_ok,blocked_reason,total_rows,gap_count,configuration_hash FROM %s`, view)).Scan(&ok, &reason, &total, &gaps, &configurationHash); err != nil {
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

func economicProjectionView(source, stream string) string {
	return fmt.Sprintf("public.invoice_%s_%s_projection_v3", source, stream)
}

func economicHealthView(source, stream string) string {
	return fmt.Sprintf("public.invoice_%s_%s_projection_health_v3", source, stream)
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
	contractView, _ := cutoverViewNames(c.Source)
	var currentHash string
	var contractOK bool
	if err := c.DB.QueryRowContext(ctx, fmt.Sprintf(`SELECT contract_ok,configuration_hash FROM %s`, contractView)).Scan(&contractOK, &currentHash); err != nil || !contractOK || currentHash != c.Manifest.ConfigurationHash {
		return ScanPage{}, errors.New("balance projection configuration drifted or is unhealthy")
	}
	cursor := req.Cursor
	var snapshot BalanceSnapshot
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
		cursor.PositionCursor = "balance_snapshot:0"
		cursor.ScanCycleID = deterministicUUID(strings.Join([]string{c.SourceID, StreamBalances, snapshot.AsOf, snapshot.SnapshotID}, "\x00"))
	} else {
		if cursor.Version != 2 || cursor.CutoverAt != c.Manifest.CutoverAt {
			return ScanPage{}, errors.New("balance cursor conflicts with cutover")
		}
		if cursor.Completed {
			var err error
			snapshot, err = c.captureReconciliation(ctx)
			if err != nil {
				return ScanPage{}, err
			}
			if err = c.Current.Replace(ctx, snapshot); err != nil {
				return ScanPage{}, err
			}
			cursor.Page = 0
			cursor.ID = 0
			cursor.Completed = false
			cursor.SnapshotID = snapshot.SnapshotID
			cursor.SnapshotRowCount = int64(len(snapshot.Rows))
			cursor.HasSnapshotMetadata = true
			cursor.CeilingAt = snapshot.AsOf
			cursor.CeilingCursor = "balance_snapshot:" + lastBalanceUserID(snapshot.Rows)
			cursor.PositionCursor = "balance_snapshot:0"
			cursor.ScanCycleID = deterministicUUID(strings.Join([]string{c.SourceID, StreamBalances, snapshot.AsOf, snapshot.SnapshotID}, "\x00"))
		} else {
			store := c.Current
			if cursor.SnapshotID == c.Manifest.BaselineSnapshotID {
				store = c.Baseline
			}
			if err := store.Load(ctx, &snapshot); err != nil {
				return ScanPage{}, err
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
		order := row.ExternalUserID
		cursorValue := "balance_snapshot:" + row.ExternalUserID
		payload := BalanceCheckpointPayload{ExternalUserID: row.ExternalUserID,
			CheckpointID: snapshot.SnapshotID + ":" + row.ExternalUserID, CheckpointKind: snapshot.CheckpointKind,
			AsOf: snapshot.AsOf, BalanceServiceUnits: row.ServiceUnits, UnitCode: snapshot.UnitCode,
			SourceSnapshotID: snapshot.SnapshotID, SnapshotRowCount: strconv.Itoa(len(snapshot.Rows)),
			BalanceNegative: row.BalanceNegative, BaselineMember: row.BaselineMember,
			FactMetadata: FactMetadata{SourceCursor: cursorValue,
				CausalDomain: "balance_snapshot", CausalOrder: &order,
				CutoverManifestHash: c.Manifest.ManifestHash, ConfigurationHash: c.Manifest.ConfigurationHash}}
		projections = append(projections, Projection{EntityType: EntityBalanceCheckpoint,
			ExternalID: payload.CheckpointID, ObservedAt: now().UTC().Format(time.RFC3339Nano), Operation: "upsert", Payload: payload})
	}
	return ScanPage{Projections: projections, NextCursor: next, HasMore: hasMore,
		StreamWatermarkAt: next.WatermarkAt, SourceCursor: next.WatermarkCursor,
		ScanCeilingAt: next.CeilingAt, ScanCeilingCursor: next.CeilingCursor,
		ScanCycleID: next.ScanCycleID, ScanComplete: next.Completed,
		ScanSnapshotID: next.SnapshotID, ScanSnapshotRowCount: snapshotRowCountPtr(next.SnapshotRowCount)}, nil
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
	contractView, view := cutoverViewNames(c.Source)
	var contractOK bool
	var configurationHash string
	if err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT contract_ok,configuration_hash FROM %s`, contractView)).Scan(&contractOK, &configurationHash); err != nil || !contractOK || configurationHash != c.Manifest.ConfigurationHash {
		return BalanceSnapshot{}, errors.New("balance snapshot configuration drifted or is unhealthy")
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT user_id,balance_service_units,balance_negative FROM %s ORDER BY user_id`, view))
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
