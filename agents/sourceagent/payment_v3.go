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

type PaymentV3DBConnector struct {
	DB          *sql.DB
	Source      string
	Manifest    CutoverManifest
	SafetyDelay time.Duration
	Now         func() time.Time
}

func (c *PaymentV3DBConnector) SourceType() string {
	if c == nil {
		return ""
	}
	return c.Source
}

type paymentV3Row struct {
	SourceID                                                  int64
	UserID                                                    int64
	EventTime                                                 time.Time
	CausalDomain, SourceCursor, EntityKind, Status, OrderType string
	Amount, PayAmount, RefundAmount, GatewayRefundAmount      string
	Currency                                                  string
	CompletedAt, RefundAt, CreatedAt, UpdatedAt               nullableTime
	PaymentType, ProviderKey                                  sql.NullString
	WalletUnits, PaidMinor                                    sql.NullString
	VerificationState                                         sql.NullString
	Refunded                                                  bool
}

func (c *PaymentV3DBConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.DB == nil || (c.Source != SourceSub2API && c.Source != SourceNewAPI) || c.Manifest.SourceType != c.Source || validateCutoverManifest(c.Manifest) != nil {
		return ScanPage{}, errors.New("payment V3 connector is not configured")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	delay := c.SafetyDelay
	if delay == 0 {
		delay = defaultEconomicSafetyDelay
	}
	if delay < time.Minute || delay > 24*time.Hour {
		return ScanPage{}, errors.New("payment safety delay is invalid")
	}
	cursor, err := c.prepare(ctx, req, delay)
	if err != nil {
		return ScanPage{}, err
	}
	healthy, reason, err := c.health(ctx)
	if err != nil {
		return ScanPage{}, err
	}
	if !healthy {
		cursor.Completed = true
		cursor.ProjectionBlocked = true
		return ScanPage{NextCursor: cursor, ReconcileBlocked: true, Warnings: []string{reason}, StreamWatermarkAt: cursor.WatermarkAt, SourceCursor: cursor.WatermarkCursor, ScanCeilingAt: cursor.CeilingAt, ScanCeilingCursor: cursor.CeilingCursor, ScanCycleID: cursor.ScanCycleID}, nil
	}
	limit := boundedScanLimit(req.Limit)
	rows, err := c.queryRows(ctx, cursor, limit)
	if err != nil {
		return ScanPage{}, err
	}
	defer rows.Close()
	values := make([]paymentV3Row, 0, limit)
	for rows.Next() {
		var row paymentV3Row
		if err = rows.Scan(&row.SourceID, &row.UserID, &row.EventTime, &row.CausalDomain, &row.SourceCursor, &row.EntityKind, &row.Status, &row.OrderType, &row.Amount, &row.PayAmount, &row.Currency, &row.RefundAmount, &row.GatewayRefundAmount, &row.CompletedAt, &row.RefundAt, &row.CreatedAt, &row.UpdatedAt, &row.PaymentType, &row.ProviderKey, &row.WalletUnits, &row.PaidMinor, &row.VerificationState, &row.Refunded); err != nil {
			return ScanPage{}, fmt.Errorf("scan V3 payment projection: %w", err)
		}
		if row.SourceID <= 0 || row.UserID <= 0 || !stableCursorPattern.MatchString(row.SourceCursor) || !causalDomainPattern.MatchString(row.CausalDomain) {
			return ScanPage{}, errors.New("payment projection row is invalid")
		}
		values = append(values, row)
	}
	if err = rows.Err(); err != nil {
		return ScanPage{}, err
	}
	hasMore := len(values) == limit
	next := cursor
	next.Completed = !hasMore
	next.ProjectionBlocked = false
	if c.Source == SourceSub2API {
		if len(values) > 0 {
			last := values[len(values)-1]
			next.UpdatedAt = last.EventTime.UTC().Format(time.RFC3339Nano)
			next.ID = last.SourceID
			next.PositionCursor = last.SourceCursor
		}
	} else {
		domains := []string{"subscription_orders", "top_ups"}
		positions, parseErr := parseDomainCursor(cursor.PositionCursor, domains)
		if parseErr != nil {
			return ScanPage{}, parseErr
		}
		for _, row := range values {
			if row.SourceID <= positions[row.CausalDomain] {
				return ScanPage{}, errors.New("newapi payment cursor did not advance")
			}
			positions[row.CausalDomain] = row.SourceID
		}
		next.PositionCursor = canonicalDomainCursor(domains, positions)
	}
	if !hasMore {
		next.WatermarkAt = next.CeilingAt
		next.WatermarkCursor = next.CeilingCursor
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	projections := make([]Projection, 0, len(values)*2)
	for _, row := range values {
		projected, projectErr := c.project(row, now().UTC())
		if projectErr != nil {
			return ScanPage{}, projectErr
		}
		projections = append(projections, projected...)
	}
	return ScanPage{Projections: projections, NextCursor: next, HasMore: hasMore, StreamWatermarkAt: next.WatermarkAt, SourceCursor: next.WatermarkCursor, ScanCeilingAt: next.CeilingAt, ScanCeilingCursor: next.CeilingCursor, ScanCycleID: next.ScanCycleID, ScanComplete: next.Completed}, nil
}

func (c *PaymentV3DBConnector) prepare(ctx context.Context, req ScanRequest, delay time.Duration) (ScanCursor, error) {
	cursor := req.Cursor
	newCycle := cursor.Version == 0 || cursor.Completed
	if cursor.Version == 0 {
		cut := c.Manifest.HighWaters[StreamPayments].Cursor
		cursor = ScanCursor{Revision: req.Cursor.Revision, Version: 2, CutoverAt: c.Manifest.CutoverAt, WatermarkAt: c.Manifest.CutoverAt, WatermarkCursor: cut, CeilingAt: c.Manifest.CutoverAt, CeilingCursor: cut, PositionCursor: cut}
		if c.Source == SourceSub2API {
			cursor.UpdatedAt = c.Manifest.CutoverAt
			cursor.ID = 0
		} else {
			cursor.PositionCursor = "subscription_orders:0;top_ups:0"
		}
	}
	if cursor.Version != 2 || cursor.CutoverAt != c.Manifest.CutoverAt {
		return ScanCursor{}, errors.New("payment cursor conflicts with cutover manifest")
	}
	if !newCycle {
		return cursor, nil
	}
	var horizon time.Time
	if err := c.DB.QueryRowContext(ctx, `SELECT transaction_timestamp()-$1::interval`, delay.String()).Scan(&horizon); err != nil {
		return ScanCursor{}, err
	}
	if horizon.Before(mustParseTime(c.Manifest.CutoverAt)) {
		horizon = mustParseTime(c.Manifest.CutoverAt)
	}
	view := paymentProjectionView(c.Source)
	if c.Source == SourceSub2API {
		if req.Mode == ScanFull || req.Mode == ScanReconcile {
			cursor.UpdatedAt = c.Manifest.CutoverAt
			cursor.ID = 0
			cursor.PositionCursor = "payment_orders:0"
		}
		var at time.Time
		var id int64
		err := c.DB.QueryRowContext(ctx, fmt.Sprintf(`SELECT event_time,source_id FROM %s WHERE event_time<=$1 ORDER BY event_time DESC,source_id DESC LIMIT 1`, view), horizon).Scan(&at, &id)
		if errors.Is(err, sql.ErrNoRows) {
			at = horizon
			id = 0
		} else if err != nil {
			return ScanCursor{}, err
		}
		cursor.CeilingAt = at.UTC().Format(time.RFC3339Nano)
		cursor.CeilingCursor = "payment_orders:" + strconv.FormatInt(id, 10)
	} else {
		domains := []string{"subscription_orders", "top_ups"}
		// top_ups has no updated_at. Every publishable cycle therefore starts a
		// complete post-cutover rescan, including pre-cutover pending IDs that
		// acquired complete_time after cutover.
		cursor.PositionCursor = "subscription_orders:0;top_ups:0"
		positions, err := parseDomainCursor(cursor.PositionCursor, domains)
		if err != nil {
			return ScanCursor{}, err
		}
		ceil := map[string]int64{}
		for _, domain := range domains {
			position := positions[domain]
			var ceiling int64
			query := fmt.Sprintf(`SELECT COALESCE(CASE WHEN min(source_id) FILTER (WHERE event_time>$1) IS NOT NULL THEN min(source_id) FILTER (WHERE event_time>$1)-1 ELSE max(source_id) END,$2)::bigint FROM %s WHERE causal_domain=$3 AND source_id>$2`, view)
			if err = c.DB.QueryRowContext(ctx, query, horizon, position, domain).Scan(&ceiling); err != nil {
				return ScanCursor{}, err
			}
			if ceiling < position {
				return ScanCursor{}, errors.New("New API payment ceiling regressed")
			}
			ceil[domain] = ceiling
		}
		cursor.CeilingAt = horizon.UTC().Format(time.RFC3339Nano)
		cursor.CeilingCursor = canonicalDomainCursor(domains, ceil)
	}
	cursor.ScanCycleID = deterministicUUID(strings.Join([]string{c.Manifest.SourceID, StreamPayments, cursor.CeilingAt, cursor.CeilingCursor}, "\x00"))
	cursor.Completed = false
	return cursor, nil
}

func (c *PaymentV3DBConnector) queryRows(ctx context.Context, cursor ScanCursor, limit int) (*sql.Rows, error) {
	columns := `source_id,user_id,event_time,causal_domain,source_cursor,entity_kind,status,order_type,amount,pay_amount,currency,refund_amount,gateway_refund_amount,completed_at,refund_at,created_at,updated_at,payment_type,provider_key,wallet_cash_service_units,paid_minor,verification_state,refunded`
	view := paymentProjectionView(c.Source)
	if c.Source == SourceSub2API {
		ceilingAt := mustParseTime(cursor.CeilingAt)
		parts := strings.Split(cursor.CeilingCursor, ":")
		ceilingID, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		position := mustParseTime(cursor.UpdatedAt)
		return c.DB.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE completed_at>=$1 AND (event_time,source_id)>($2,$3) AND (event_time,source_id)<=($4,$5) ORDER BY event_time,source_id LIMIT $6`, columns, view), c.Manifest.CutoverAt, position, cursor.ID, ceilingAt, ceilingID, limit)
	}
	domains := []string{"subscription_orders", "top_ups"}
	positions, err := parseDomainCursor(cursor.PositionCursor, domains)
	if err != nil {
		return nil, err
	}
	ceilings, err := parseDomainCursor(cursor.CeilingCursor, domains)
	if err != nil {
		return nil, err
	}
	return c.DB.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE completed_at>=$1 AND event_time<=$2 AND ((causal_domain='subscription_orders' AND source_id>$3 AND source_id<=$4) OR (causal_domain='top_ups' AND source_id>$5 AND source_id<=$6)) ORDER BY causal_domain,source_id LIMIT $7`, columns, view), c.Manifest.CutoverAt, mustParseTime(cursor.CeilingAt), positions["subscription_orders"], ceilings["subscription_orders"], positions["top_ups"], ceilings["top_ups"], limit)
}

func (c *PaymentV3DBConnector) health(ctx context.Context) (bool, string, error) {
	var ok bool
	var reason, hash string
	if err := c.DB.QueryRowContext(ctx, fmt.Sprintf(`SELECT contract_ok,blocked_reason,configuration_hash FROM %s`, paymentHealthView(c.Source))).Scan(&ok, &reason, &hash); err != nil {
		return false, "", err
	}
	if hash != c.Manifest.ConfigurationHash {
		return false, "configuration_drift", nil
	}
	if ok && reason != "" || !ok && reason == "" {
		return false, "", errors.New("payment health is inconsistent")
	}
	return ok, reason, nil
}

func (c *PaymentV3DBConnector) project(row paymentV3Row, observed time.Time) ([]Projection, error) {
	order := strconv.FormatInt(row.SourceID, 10)
	meta := FactMetadata{SourceCursor: row.SourceCursor, CausalDomain: row.CausalDomain, CausalOrder: &order, CutoverManifestHash: c.Manifest.ManifestHash, ConfigurationHash: c.Manifest.ConfigurationHash}
	userID := strconv.FormatInt(row.UserID, 10)
	orderID := strconv.FormatInt(row.SourceID, 10)
	if row.EntityKind == EntitySubscriptionPurchase {
		if !row.CompletedAt.Valid || !row.PaidMinor.Valid || !serviceUnitsPattern.MatchString(row.PaidMinor.String) || row.PaidMinor.String == "0" {
			return nil, errors.New("subscription projection is missing exact paid minor")
		}
		state := strings.TrimSpace(row.VerificationState.String)
		if state != "verified" && state != "pending" && state != "frozen" {
			return nil, errors.New("subscription verification state is invalid")
		}
		payload := SubscriptionPurchasePayload{ExternalUserID: userID, ExternalOrderID: orderID, CompletedAt: row.CompletedAt.Time.UTC().Format(time.RFC3339Nano), PaidMinor: row.PaidMinor.String, Currency: "CNY", VerificationState: state, Refunded: row.Refunded, FactMetadata: meta}
		return []Projection{{EntityType: EntitySubscriptionPurchase, ExternalID: orderID, ObservedAt: observed.Format(time.RFC3339Nano), Operation: "upsert", Payload: payload}}, nil
	}
	if c.Source == SourceNewAPI {
		if row.EntityKind != EntityPaymentCandidate {
			return nil, errors.New("New API payment projection returned an unknown entity kind")
		}
		var wallet *string
		var unit *string
		if row.WalletUnits.Valid {
			value := row.WalletUnits.String
			if !serviceUnitsPattern.MatchString(value) || value == "0" {
				return nil, errors.New("New API wallet units are invalid")
			}
			wallet = &value
			unitValue := unitCodeForSource(c.Source)
			unit = &unitValue
		}
		currency := "CNY"
		payload := PaymentCandidatePayload{ExternalOrderID: orderID, ExternalUserID: userID, SourceStatus: row.Status, OrderType: "topup", QuotedAmount: row.Amount, ObservedPayAmount: row.PayAmount, Currency: &currency, VerificationState: VerificationPendingManual, VerificationReason: "New API settlement remains pending manual evidence; wallet units are accepted only under the pinned rc.25 provider contract", CompletedAt: nullableTimeString(row.CompletedAt), CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano), ObservedAt: row.EventTime.UTC().Format(time.RFC3339Nano), PaymentType: row.PaymentType.String, ProviderKey: row.ProviderKey.String, WalletCashServiceUnits: wallet, WalletUnitCode: unit, FactMetadata: meta}
		return []Projection{{EntityType: EntityPaymentCandidate, ExternalID: orderID, ObservedAt: observed.Format(time.RFC3339Nano), Operation: "upsert", Payload: payload}}, nil
	}
	if row.EntityKind != EntityPaymentOrder {
		return nil, errors.New("Sub2API payment projection returned an unknown entity kind")
	}
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return nil, errors.New("Sub2API payment timestamps are missing")
	}
	var wallet *string
	var unit *string
	if row.OrderType == "balance" {
		if !row.WalletUnits.Valid || !serviceUnitsPattern.MatchString(row.WalletUnits.String) || row.WalletUnits.String == "0" {
			return nil, errors.New("Sub2API balance payment has no provable wallet units")
		}
		value := row.WalletUnits.String
		wallet = &value
		unitValue := unitCodeForSource(c.Source)
		unit = &unitValue
	}
	payload := PaymentOrderPayload{ExternalOrderID: orderID, ExternalUserID: userID, Status: row.Status, OrderType: row.OrderType, Amount: row.Amount, PayAmount: row.PayAmount, Currency: row.Currency, RefundAmount: row.RefundAmount, GatewayRefundAmount: row.GatewayRefundAmount, CompletedAt: nullableTimeString(row.CompletedAt), RefundAt: nullableTimeString(row.RefundAt), CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano), UpdatedAt: row.UpdatedAt.Time.UTC().Format(time.RFC3339Nano), PaymentType: row.PaymentType.String, ProviderKey: row.ProviderKey.String, WalletCashServiceUnits: wallet, WalletUnitCode: unit, FactMetadata: meta}
	out := []Projection{{EntityType: EntityPaymentOrder, ExternalID: orderID, ObservedAt: observed.Format(time.RFC3339Nano), Operation: "upsert", Payload: payload}}
	positive, _ := decimalPositive(row.GatewayRefundAmount)
	if positive {
		out = append(out, Projection{EntityType: EntityPaymentAdjustment, ExternalID: orderID + ":refund", ObservedAt: observed.Format(time.RFC3339Nano), Operation: "upsert", Payload: PaymentAdjustmentPayload{ExternalOrderID: orderID, ExternalUserID: userID, AdjustmentType: AdjustmentRefund, Amount: row.GatewayRefundAmount, Currency: row.Currency, EffectiveAt: nullableTimeString(row.RefundAt), SourceStatus: row.Status, SourceUpdatedAt: payload.UpdatedAt, Basis: "absolute_cumulative_gateway_refund", FactMetadata: meta}})
	}
	return out, nil
}

func paymentProjectionView(source string) string {
	return fmt.Sprintf("public.invoice_%s_payments_projection_v3", source)
}
func paymentHealthView(source string) string {
	return fmt.Sprintf("public.invoice_%s_payments_projection_health_v3", source)
}
