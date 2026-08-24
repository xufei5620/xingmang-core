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
	if c.Source == SourceSub2API {
		if req.Mode == ScanFull || req.Mode == ScanReconcile {
			cursor.UpdatedAt = c.Manifest.CutoverAt
			cursor.ID = 0
			cursor.PositionCursor = "payment_orders:0"
		}
		var at time.Time
		var id int64
		relation, relationErr := bridgeJSONRecordRelation(c.Source, StreamPayments, "ceiling", "event_time timestamptz,source_id bigint")
		if relationErr != nil {
			return ScanCursor{}, relationErr
		}
		request, requestErr := marshalBridgeRequest(map[string]any{"horizon": horizon.UTC().Format(time.RFC3339Nano)})
		if requestErr != nil {
			return ScanCursor{}, requestErr
		}
		err := c.DB.QueryRowContext(ctx, `SELECT event_time,source_id FROM `+relation, request).Scan(&at, &id)
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
			relation, relationErr := bridgeJSONRecordRelation(c.Source, StreamPayments, "ceiling", "source_id bigint")
			if relationErr != nil {
				return ScanCursor{}, relationErr
			}
			request, requestErr := marshalBridgeRequest(map[string]any{"horizon": horizon.UTC().Format(time.RFC3339Nano), "position": position, "domain": domain})
			if requestErr != nil {
				return ScanCursor{}, requestErr
			}
			if err = c.DB.QueryRowContext(ctx, `SELECT source_id FROM `+relation, request).Scan(&ceiling); err != nil {
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
	record := `source_id bigint,user_id bigint,event_time timestamptz,causal_domain text,source_cursor text,entity_kind text,status text,order_type text,amount text,pay_amount text,currency text,refund_amount text,gateway_refund_amount text,completed_at timestamptz,refund_at timestamptz,created_at timestamptz,updated_at timestamptz,payment_type text,provider_key text,wallet_cash_service_units text,paid_minor text,verification_state text,refunded boolean`
	relation, err := bridgeJSONRecordRelation(c.Source, StreamPayments, "page", record)
	if err != nil {
		return nil, err
	}
	if c.Source == SourceSub2API {
		ceilingAt := mustParseTime(cursor.CeilingAt)
		parts := strings.Split(cursor.CeilingCursor, ":")
		ceilingID, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		position := mustParseTime(cursor.UpdatedAt)
		request, requestErr := marshalBridgeRequest(map[string]any{
			"cutover": c.Manifest.CutoverAt, "position_at": position.UTC().Format(time.RFC3339Nano), "position_id": cursor.ID,
			"ceiling_at": ceilingAt.UTC().Format(time.RFC3339Nano), "ceiling_id": ceilingID, "limit": limit,
		})
		if requestErr != nil {
			return nil, requestErr
		}
		return c.DB.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s ORDER BY event_time,source_id`, columns, relation), request)
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
	request, requestErr := marshalBridgeRequest(map[string]any{
		"cutover": c.Manifest.CutoverAt, "horizon": cursor.CeilingAt, "subscription_position": positions["subscription_orders"],
		"subscription_ceiling": ceilings["subscription_orders"], "topup_position": positions["top_ups"], "topup_ceiling": ceilings["top_ups"], "limit": limit,
	})
	if requestErr != nil {
		return nil, requestErr
	}
	return c.DB.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s ORDER BY causal_domain,source_id`, columns, relation), request)
}

func (c *PaymentV3DBConnector) health(ctx context.Context) (bool, string, error) {
	relation, err := bridgeJSONRecordRelation(c.Source, StreamPayments, "health", "contract_ok boolean,blocked_reason text,configuration_hash text")
	if err != nil {
		return false, "", err
	}
	request, err := marshalBridgeRequest(nil)
	if err != nil {
		return false, "", err
	}
	var ok bool
	var reason, hash string
	if err := c.DB.QueryRowContext(ctx, `SELECT contract_ok,blocked_reason,configuration_hash FROM `+relation, request).Scan(&ok, &reason, &hash); err != nil {
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
