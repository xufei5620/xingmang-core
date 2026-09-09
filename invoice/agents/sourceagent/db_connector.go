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

type SQLDialect string

const (
	DialectPostgres  SQLDialect = "postgres"
	DialectMySQL     SQLDialect = "mysql"
	DialectSQLite    SQLDialect = "sqlite"
	defaultScanLimit            = 100
	maxScanLimit                = 500
)

// Sub2APIDBConnector reads the reviewed bridge boundary. The LOGIN caller has
// EXECUTE only and cannot read payment_orders or provider_snapshot directly.
type Sub2APIDBConnector struct {
	DB      *sql.DB
	Dialect SQLDialect
	Now     func() time.Time
}

func (c *Sub2APIDBConnector) SourceType() string { return SourceSub2API }

func (c *Sub2APIDBConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.DB == nil {
		return ScanPage{}, errors.New("sub2api db connector: database is required")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	effectiveCursor := req.Cursor
	if (req.Mode == ScanFull || req.Mode == ScanReconcile) && effectiveCursor.Completed {
		effectiveCursor = ScanCursor{Revision: effectiveCursor.Revision}
	}
	limit := boundedScanLimit(req.Limit)
	cursorTime, err := parseCursorTime(effectiveCursor.UpdatedAt)
	if err != nil {
		return ScanPage{}, fmt.Errorf("sub2api db connector: %w", err)
	}
	warnings, reconcileBlocked, err := c.paymentProjectionHealth(ctx)
	if err != nil {
		return ScanPage{}, err
	}

	query, args, err := sub2APIKeysetQuery(c.Dialect, cursorTime, effectiveCursor.ID, limit)
	if err != nil {
		return ScanPage{}, err
	}
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return ScanPage{}, fmt.Errorf("sub2api db connector: read payment projection: %w", err)
	}
	defer rows.Close()

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	page := ScanPage{
		Projections: make([]Projection, 0, limit), Warnings: warnings,
		ReconcileBlocked: reconcileBlocked,
	}
	next := effectiveCursor
	next.Version = 1
	next.Page = 0
	next.Completed = false
	for rows.Next() {
		var row sub2APIOrderRow
		if err := rows.Scan(
			&row.ID, &row.UserID, &row.Status, &row.OrderType,
			&row.Amount, &row.PayAmount, &row.RefundAmount, &row.Currency,
			&row.CompletedAt, &row.RefundAt, &row.CreatedAt, &row.UpdatedAt,
			&row.PaymentType, &row.ProviderKey,
		); err != nil {
			return ScanPage{}, fmt.Errorf("sub2api db connector: scan payment projection: %w", err)
		}
		projections, err := projectSub2APIOrder(row, now().UTC())
		if err != nil {
			return ScanPage{}, fmt.Errorf("sub2api db connector: order %d: %w", row.ID, err)
		}
		page.Projections = append(page.Projections, projections...)
		next.UpdatedAt = row.UpdatedAt.Time.UTC().Format(time.RFC3339Nano)
		next.ID = row.ID
	}
	if err := rows.Err(); err != nil {
		return ScanPage{}, fmt.Errorf("sub2api db connector: iterate payment projection: %w", err)
	}

	// An extra projection can be emitted for a refund/reduction, so HasMore is
	// based on source rows rather than projection count.
	sourceRows := 0
	if next.UpdatedAt != effectiveCursor.UpdatedAt || next.ID != effectiveCursor.ID {
		// The cursor advances once per source row. Count conservatively from the
		// primary payment_order projections.
		for _, projection := range page.Projections {
			if projection.EntityType == EntityPaymentOrder {
				sourceRows++
			}
		}
	}
	page.HasMore = sourceRows == limit
	next.Completed = !page.HasMore
	page.NextCursor = next
	return page, nil
}

func (c *Sub2APIDBConnector) paymentProjectionHealth(ctx context.Context) ([]string, bool, error) {
	if c.Dialect != DialectPostgres {
		return nil, false, nil
	}
	relation, err := bridgeJSONRecordRelation(SourceSub2API, StreamPayments, "legacy_health",
		"total_rows bigint,exposed_cny_rows bigint,unsupported_known_non_cny_rows bigint,blocked_unknown_currency_rows bigint")
	if err != nil {
		return nil, false, err
	}
	request, err := marshalBridgeRequest(nil)
	if err != nil {
		return nil, false, err
	}
	var total, exposedCNY, unsupportedNonCNY, blockedUnknown int64
	err = c.DB.QueryRowContext(ctx, `SELECT total_rows,exposed_cny_rows,unsupported_known_non_cny_rows,blocked_unknown_currency_rows FROM `+relation, request).
		Scan(&total, &exposedCNY, &unsupportedNonCNY, &blockedUnknown)
	if err != nil {
		return nil, false, fmt.Errorf("sub2api db connector: read payment projection health: %w", err)
	}
	return sub2APIProjectionHealthResult(total, exposedCNY, unsupportedNonCNY, blockedUnknown)
}

func sub2APIProjectionHealthResult(total, exposedCNY, unsupportedNonCNY, blockedUnknown int64) ([]string, bool, error) {
	if total < 0 || exposedCNY < 0 || unsupportedNonCNY < 0 || blockedUnknown < 0 ||
		exposedCNY+unsupportedNonCNY+blockedUnknown != total {
		return nil, false, errors.New("sub2api db connector: invalid payment projection health aggregate")
	}
	warnings := make([]string, 0, 2)
	if unsupportedNonCNY > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"sub2api currency projection excluded %d known non-CNY payment rows from the CNY-only invoice ledger",
			unsupportedNonCNY,
		))
	}
	if blockedUnknown > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"sub2api currency projection quarantined %d of %d payment rows with missing, contradictory or invalid currency evidence",
			blockedUnknown, total,
		))
	}
	return warnings, blockedUnknown > 0, nil
}

// NewAPIDBConnector reads only top_ups columns. Because that table has no
// updated_at or refund fields, incremental scans discover new IDs only. A
// scheduled full/reconcile scan is mandatory for status changes, and every row
// remains a pending_manual payment candidate even when status is success.
type NewAPIDBConnector struct {
	DB      *sql.DB
	Dialect SQLDialect
	Now     func() time.Time
}

func (c *NewAPIDBConnector) SourceType() string { return SourceNewAPI }

func (c *NewAPIDBConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.DB == nil {
		return ScanPage{}, errors.New("newapi db connector: database is required")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	effectiveCursor := req.Cursor
	if (req.Mode == ScanFull || req.Mode == ScanReconcile) && effectiveCursor.Completed {
		effectiveCursor = ScanCursor{Revision: effectiveCursor.Revision}
	}
	limit := boundedScanLimit(req.Limit)
	query, args, err := newAPIKeysetQuery(c.Dialect, effectiveCursor.ID, limit)
	if err != nil {
		return ScanPage{}, err
	}
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return ScanPage{}, fmt.Errorf("newapi db connector: read top-up projection: %w", err)
	}
	defer rows.Close()

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	page := ScanPage{
		Projections: make([]Projection, 0, limit),
		Warnings: []string{
			"newapi top_ups has no updated_at/refund evidence; schedule complete reconciliation scans",
			"newapi candidates are pending_manual and never automatic invoice entitlement",
		},
	}
	next := effectiveCursor
	next.Version = 1
	next.UpdatedAt = ""
	next.Page = 0
	next.Completed = false
	rowsRead := 0
	for rows.Next() {
		var row newAPITopupRow
		if err := rows.Scan(
			&row.ID, &row.UserID, &row.Amount, &row.Money,
			&row.PaymentMethod, &row.PaymentProvider, &row.CreateTime,
			&row.CompleteTime, &row.Status,
		); err != nil {
			return ScanPage{}, fmt.Errorf("newapi db connector: scan top-up projection: %w", err)
		}
		projection, err := projectNewAPITopup(row, now().UTC())
		if err != nil {
			return ScanPage{}, fmt.Errorf("newapi db connector: top-up %d: %w", row.ID, err)
		}
		page.Projections = append(page.Projections, projection)
		next.ID = row.ID
		rowsRead++
	}
	if err := rows.Err(); err != nil {
		return ScanPage{}, fmt.Errorf("newapi db connector: iterate top-up projection: %w", err)
	}
	page.HasMore = rowsRead == limit
	next.Completed = !page.HasMore
	page.NextCursor = next
	return page, nil
}

type sub2APIOrderRow struct {
	ID           int64
	UserID       int64
	Status       string
	OrderType    string
	Amount       decimalValue
	PayAmount    decimalValue
	Currency     string
	RefundAmount decimalValue
	CompletedAt  nullableTime
	RefundAt     nullableTime
	CreatedAt    nullableTime
	UpdatedAt    nullableTime
	PaymentType  sql.NullString
	ProviderKey  sql.NullString
}

type newAPITopupRow struct {
	ID              int64
	UserID          int64
	Amount          int64
	Money           decimalValue
	PaymentMethod   string
	PaymentProvider string
	CreateTime      int64
	CompleteTime    int64
	Status          string
}

func sub2APIKeysetQuery(dialect SQLDialect, updatedAt time.Time, id int64, limit int) (string, []any, error) {
	decimalCast, err := decimalCastFor(dialect)
	if err != nil {
		return "", nil, err
	}
	columns := fmt.Sprintf(`id, user_id, status, order_type, %s, %s, %s, currency,
completed_at, refund_at, created_at, updated_at, payment_type, provider_key`,
		fmt.Sprintf(decimalCast, "amount"),
		fmt.Sprintf(decimalCast, "pay_amount"),
		fmt.Sprintf(decimalCast, "refund_amount"),
	)
	switch dialect {
	case DialectPostgres:
		relation, relationErr := bridgeJSONRecordRelation(SourceSub2API, StreamPayments, "legacy_page",
			"id bigint,user_id bigint,status text,order_type text,amount text,pay_amount text,refund_amount text,currency text,completed_at timestamptz,refund_at timestamptz,created_at timestamptz,updated_at timestamptz,payment_type text,provider_key text")
		if relationErr != nil {
			return "", nil, relationErr
		}
		request, requestErr := marshalBridgeRequest(map[string]any{"updated_at": updatedAt.UTC().Format(time.RFC3339Nano), "id": id, "limit": limit})
		if requestErr != nil {
			return "", nil, requestErr
		}
		return `SELECT ` + columns + ` FROM ` + relation + ` ORDER BY updated_at ASC,id ASC`, []any{request}, nil
	case DialectMySQL, DialectSQLite:
		// Sub2API's production schema is PostgreSQL. These branches exist for
		// contract tests/private mirrors and avoid row-value comparison drift.
		return `SELECT ` + columns + ` FROM invoice_sub2api_payment_projection_v1
WHERE updated_at > ? OR (updated_at = ? AND id > ?)
ORDER BY updated_at ASC, id ASC LIMIT ?`, []any{updatedAt, updatedAt, id, limit}, nil
	default:
		return "", nil, fmt.Errorf("unsupported SQL dialect %q", dialect)
	}
}

func newAPIKeysetQuery(dialect SQLDialect, id int64, limit int) (string, []any, error) {
	if dialect == DialectPostgres {
		relation, err := bridgeJSONRecordRelation(SourceNewAPI, StreamPayments, "legacy_page",
			"id bigint,user_id bigint,amount bigint,money text,payment_method text,payment_provider text,create_time bigint,complete_time bigint,status text")
		if err != nil {
			return "", nil, err
		}
		request, err := marshalBridgeRequest(map[string]any{"id": id, "limit": limit})
		if err != nil {
			return "", nil, err
		}
		return `SELECT id,user_id,amount,money,payment_method,payment_provider,create_time,complete_time,status FROM ` + relation + ` ORDER BY id ASC`, []any{request}, nil
	}
	decimalCast, err := decimalCastFor(dialect)
	if err != nil {
		return "", nil, err
	}
	query := fmt.Sprintf(`SELECT id, user_id, amount, %s, payment_method,
payment_provider, create_time, complete_time, status
FROM top_ups WHERE id > %s ORDER BY id ASC LIMIT %s`,
		fmt.Sprintf(decimalCast, "money"), placeholder(dialect, 1), placeholder(dialect, 2))
	return query, []any{id, limit}, nil
}

func decimalCastFor(dialect SQLDialect) (string, error) {
	switch dialect {
	case DialectPostgres, DialectSQLite:
		return "CAST(%s AS TEXT)", nil
	case DialectMySQL:
		return "CAST(%s AS CHAR)", nil
	default:
		return "", fmt.Errorf("unsupported SQL dialect %q", dialect)
	}
}

func placeholder(dialect SQLDialect, index int) string {
	if dialect == DialectPostgres {
		return "$" + strconv.Itoa(index)
	}
	return "?"
}

func validateScanRequest(req ScanRequest) error {
	if req.Mode != ScanFull && req.Mode != ScanIncremental && req.Mode != ScanReconcile {
		return fmt.Errorf("invalid scan mode %q", req.Mode)
	}
	if req.Cursor.Version != 0 && req.Cursor.Version != 1 && req.Cursor.Version != 2 {
		return fmt.Errorf("unsupported cursor version %d", req.Cursor.Version)
	}
	if req.Cursor.ID < 0 || req.Cursor.Page < 0 || req.Cursor.BoundaryID < 0 {
		return errors.New("cursor values must not be negative")
	}
	return nil
}

func boundedScanLimit(limit int) int {
	if limit <= 0 {
		return defaultScanLimit
	}
	if limit > maxScanLimit {
		return maxScanLimit
	}
	return limit
}

func parseCursorTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Unix(0, 0).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cursor updated_at: %w", err)
	}
	return parsed.UTC(), nil
}

type decimalValue struct{ Value string }

func (d *decimalValue) Scan(src any) error {
	if src == nil {
		return errors.New("decimal value is null")
	}
	var raw string
	switch value := src.(type) {
	case string:
		raw = value
	case []byte:
		raw = string(value)
	case int64:
		raw = strconv.FormatInt(value, 10)
	case float64:
		raw = strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Errorf("unsupported decimal database type %T", src)
	}
	normalized, err := normalizeDecimal(raw)
	if err != nil {
		return err
	}
	d.Value = normalized
	return nil
}

type nullableTime struct {
	Time  time.Time
	Valid bool
}

func (t *nullableTime) Scan(src any) error {
	if src == nil {
		t.Time = time.Time{}
		t.Valid = false
		return nil
	}
	var parsed time.Time
	var err error
	switch value := src.(type) {
	case time.Time:
		parsed = value
	case string:
		parsed, err = parseDatabaseTime(value)
	case []byte:
		parsed, err = parseDatabaseTime(string(value))
	default:
		return fmt.Errorf("unsupported time database type %T", src)
	}
	if err != nil {
		return err
	}
	t.Time = parsed.UTC()
	t.Valid = true
	return nil
}

func parseDatabaseTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	formats := []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"}
	for _, format := range formats {
		if parsed, err := time.Parse(format, raw); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid database timestamp")
}
