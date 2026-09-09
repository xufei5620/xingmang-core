package sourceagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxUpstreamResponseBytes int64 = 8 << 20

// Sub2APIHTTPConnector is an explicit fallback for deployments that cannot
// expose a column-level read-only projection. It calls GET endpoints only and
// keeps the Admin Key/JWT outside the invoice web/API process.
type Sub2APIHTTPConnector struct {
	HTTP        *RestrictedHTTPClient
	Credentials CredentialProvider
	Now         func() time.Time
}

func (c *Sub2APIHTTPConnector) SourceType() string { return SourceSub2API }

func (c *Sub2APIHTTPConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.HTTP == nil || c.Credentials == nil {
		return ScanPage{}, errors.New("sub2api HTTP connector is not configured")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	effectiveCursor := req.Cursor
	if effectiveCursor.Completed {
		effectiveCursor.Page = 0
		effectiveCursor.Completed = false
		if req.Mode == ScanFull || req.Mode == ScanReconcile {
			effectiveCursor.BoundaryID = 0
		}
	}
	pageNumber := effectiveCursor.Page
	if pageNumber < 1 {
		pageNumber = 1
	}
	pageSize := boundedAPILimit(req.Limit, 100)
	query := url.Values{
		"page":      {strconv.Itoa(pageNumber)},
		"page_size": {strconv.Itoa(pageSize)},
	}
	var response sub2APIListEnvelope
	if err := c.getJSON(ctx, "/api/v1/admin/payment/orders", query, &response); err != nil {
		return ScanPage{}, err
	}
	if response.Code != 0 {
		return ScanPage{}, fmt.Errorf("sub2api HTTP connector: upstream contract error code %d", response.Code)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	result := ScanPage{
		Projections: make([]Projection, 0, len(response.Data.Items)),
		Warnings: []string{
			"Sub2API Admin API uses created_at-desc offset pagination; complete rescans and per-order refund watches are mandatory",
		},
	}
	var largestID int64
	seenOrderIDs := make(map[int64]struct{}, len(response.Data.Items))
	for _, item := range response.Data.Items {
		row, err := item.toProjectionRow()
		if err != nil {
			return ScanPage{}, fmt.Errorf("sub2api HTTP connector: %w", err)
		}
		projections, err := projectSub2APIOrder(row, now().UTC())
		if err != nil {
			return ScanPage{}, fmt.Errorf("sub2api HTTP connector: order %d: %w", item.ID, err)
		}
		result.Projections = append(result.Projections, projections...)
		seenOrderIDs[item.ID] = struct{}{}
		if item.ID > largestID {
			largestID = item.ID
		}
	}
	if len(req.WatchIDs) > 100 {
		return ScanPage{}, errors.New("sub2api HTTP connector: watch id limit exceeds 100")
	}
	for _, orderID := range req.WatchIDs {
		if orderID <= 0 {
			return ScanPage{}, errors.New("sub2api HTTP connector: invalid watch order id")
		}
		if _, exists := seenOrderIDs[orderID]; exists {
			continue
		}
		var detail sub2APIDetailEnvelope
		if err := c.getJSON(ctx, "/api/v1/admin/payment/orders/"+strconv.FormatInt(orderID, 10), nil, &detail); err != nil {
			return ScanPage{}, err
		}
		if detail.Code != 0 {
			return ScanPage{}, fmt.Errorf("sub2api HTTP connector: watched order contract error code %d", detail.Code)
		}
		row, err := detail.Data.Order.toProjectionRow()
		if err != nil {
			return ScanPage{}, err
		}
		projections, err := projectSub2APIOrder(row, now().UTC())
		if err != nil {
			return ScanPage{}, fmt.Errorf("sub2api HTTP connector: watched order %d: %w", orderID, err)
		}
		result.Projections = append(result.Projections, projections...)
	}
	result.HasMore = len(response.Data.Items) > 0 && pageNumber < response.Data.Pages
	result.NextCursor = ScanCursor{
		Revision:   effectiveCursor.Revision,
		Version:    1,
		Page:       pageNumber + 1,
		BoundaryID: maxInt64(effectiveCursor.BoundaryID, largestID),
		Completed:  !result.HasMore,
	}
	return result, nil
}

func (c *Sub2APIHTTPConnector) getJSON(ctx context.Context, path string, query url.Values, target any) error {
	return getJSONWithRotatingCredential(ctx, c.HTTP, c.Credentials, path, query, target, "sub2api")
}

// NewAPIHTTPConnector reads /api/user/topup using a dedicated administrator
// PAT. It never maps a row to payment_order: success is still emitted only as a
// pending_manual payment_candidate.
type NewAPIHTTPConnector struct {
	HTTP        *RestrictedHTTPClient
	Credentials CredentialProvider
	Now         func() time.Time
}

func (c *NewAPIHTTPConnector) SourceType() string { return SourceNewAPI }

func (c *NewAPIHTTPConnector) Scan(ctx context.Context, req ScanRequest) (ScanPage, error) {
	if c == nil || c.HTTP == nil || c.Credentials == nil {
		return ScanPage{}, errors.New("newapi HTTP connector is not configured")
	}
	if err := validateScanRequest(req); err != nil {
		return ScanPage{}, err
	}
	if len(req.WatchIDs) != 0 {
		return ScanPage{}, fmt.Errorf("%w: New API has no read-only top-up detail endpoint", ErrContractUnavailable)
	}
	effectiveCursor := req.Cursor
	if effectiveCursor.Completed {
		effectiveCursor.Page = 0
		effectiveCursor.Completed = false
		if req.Mode == ScanFull || req.Mode == ScanReconcile {
			effectiveCursor.BoundaryID = 0
		}
	}
	pageNumber := effectiveCursor.Page
	if pageNumber < 1 {
		pageNumber = 1
	}
	pageSize := boundedAPILimit(req.Limit, 100)
	query := url.Values{
		"page":      {strconv.Itoa(pageNumber)},
		"page_size": {strconv.Itoa(pageSize)},
	}
	var response newAPIListEnvelope
	if err := getJSONWithRotatingCredential(ctx, c.HTTP, c.Credentials, "/api/user/topup", query, &response, "newapi"); err != nil {
		return ScanPage{}, err
	}
	if !response.Success {
		return ScanPage{}, ErrContractUnavailable
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	result := ScanPage{
		Projections: make([]Projection, 0, len(response.Data.Items)),
		Warnings: []string{
			"New API top-up DTO has no updated_at, currency, refund, chargeback, or provider-settlement proof",
			"all New API rows, including success, remain pending_manual payment candidates",
		},
	}
	var largestID int64
	for _, item := range response.Data.Items {
		row, err := item.toProjectionRow()
		if err != nil {
			return ScanPage{}, fmt.Errorf("newapi HTTP connector: %w", err)
		}
		projection, err := projectNewAPITopup(row, now().UTC())
		if err != nil {
			return ScanPage{}, fmt.Errorf("newapi HTTP connector: top-up %d: %w", item.ID, err)
		}
		result.Projections = append(result.Projections, projection)
		if item.ID > largestID {
			largestID = item.ID
		}
	}
	pages := 1
	if response.Data.PageSize > 0 && response.Data.Total > 0 {
		pages = (response.Data.Total + response.Data.PageSize - 1) / response.Data.PageSize
	}
	result.HasMore = len(response.Data.Items) > 0 && pageNumber < pages
	result.NextCursor = ScanCursor{
		Revision:   effectiveCursor.Revision,
		Version:    1,
		Page:       pageNumber + 1,
		BoundaryID: maxInt64(effectiveCursor.BoundaryID, largestID),
		Completed:  !result.HasMore,
	}
	return result, nil
}

func getJSONWithRotatingCredential(ctx context.Context, client *RestrictedHTTPClient, credentials CredentialProvider, path string, query url.Values, target any, source string) error {
	for attempt := 0; attempt < 2; attempt++ {
		credential, err := credentials.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("%s HTTP connector: %w", source, ErrCredentialUnavailable)
		}
		req, err := client.NewRequest(ctx, http.MethodGet, path, query)
		if err != nil {
			credential.Destroy()
			return err
		}
		credential.apply(req.Header)
		req.Header.Set("Accept", "application/json")
		response, err := client.Do(req)
		credential.remove(req.Header)
		credential.Destroy()
		if err != nil {
			return fmt.Errorf("%s HTTP connector: read-only request failed: %w", source, err)
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			drainAndClose(response.Body)
			if attempt == 0 {
				continue
			}
			return fmt.Errorf("%s HTTP connector: authentication rejected", source)
		}
		if response.StatusCode != http.StatusOK {
			drainAndClose(response.Body)
			return fmt.Errorf("%s HTTP connector: unexpected upstream status %d", source, response.StatusCode)
		}
		err = decodeBoundedJSON(response.Body, maxUpstreamResponseBytes, target, false)
		response.Body.Close()
		if err != nil {
			return fmt.Errorf("%s HTTP connector: invalid response contract: %w", source, err)
		}
		return nil
	}
	return fmt.Errorf("%s HTTP connector: authentication rejected", source)
}

func decodeBoundedJSON(reader io.Reader, limit int64, target any, disallowUnknown bool) error {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("JSON response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureDecodeEOF(decoder)
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	_ = body.Close()
}

type sub2APIListEnvelope struct {
	Code int `json:"code"`
	Data struct {
		Items    []sub2APIOrderDTO `json:"items"`
		Total    int               `json:"total"`
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
		Pages    int               `json:"pages"`
	} `json:"data"`
}

type sub2APIDetailEnvelope struct {
	Code int `json:"code"`
	Data struct {
		Order sub2APIOrderDTO `json:"order"`
	} `json:"data"`
}

type sub2APIOrderDTO struct {
	ID           int64       `json:"id"`
	UserID       int64       `json:"user_id"`
	Status       string      `json:"status"`
	OrderType    string      `json:"order_type"`
	Amount       json.Number `json:"amount"`
	PayAmount    json.Number `json:"pay_amount"`
	Currency     string      `json:"currency"`
	RefundAmount json.Number `json:"refund_amount"`
	CompletedAt  *string     `json:"completed_at"`
	RefundAt     *string     `json:"refund_at"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	PaymentType  string      `json:"payment_type"`
	ProviderKey  *string     `json:"provider_key"`
}

func (d sub2APIOrderDTO) toProjectionRow() (sub2APIOrderRow, error) {
	amount, err := decimalFromJSONNumber(d.Amount)
	if err != nil {
		return sub2APIOrderRow{}, err
	}
	payAmount, err := decimalFromJSONNumber(d.PayAmount)
	if err != nil {
		return sub2APIOrderRow{}, err
	}
	refundAmount, err := decimalFromJSONNumber(d.RefundAmount)
	if err != nil {
		return sub2APIOrderRow{}, err
	}
	createdAt, err := requiredAPITime(d.CreatedAt)
	if err != nil {
		return sub2APIOrderRow{}, err
	}
	updatedAt, err := requiredAPITime(d.UpdatedAt)
	if err != nil {
		return sub2APIOrderRow{}, err
	}
	completedAt, err := optionalAPITime(d.CompletedAt)
	if err != nil {
		return sub2APIOrderRow{}, err
	}
	refundAt, err := optionalAPITime(d.RefundAt)
	if err != nil {
		return sub2APIOrderRow{}, err
	}
	row := sub2APIOrderRow{
		ID:           d.ID,
		UserID:       d.UserID,
		Status:       d.Status,
		OrderType:    d.OrderType,
		Amount:       decimalValue{Value: amount},
		PayAmount:    decimalValue{Value: payAmount},
		Currency:     d.Currency,
		RefundAmount: decimalValue{Value: refundAmount},
		CompletedAt:  completedAt,
		RefundAt:     refundAt,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		PaymentType:  nullableString(d.PaymentType),
	}
	if d.ProviderKey != nil {
		row.ProviderKey = nullableString(*d.ProviderKey)
	}
	return row, nil
}

type newAPIListEnvelope struct {
	Success bool `json:"success"`
	Data    struct {
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
		Total    int              `json:"total"`
		Items    []newAPITopupDTO `json:"items"`
	} `json:"data"`
}

type newAPITopupDTO struct {
	ID              int64       `json:"id"`
	UserID          int64       `json:"user_id"`
	Amount          int64       `json:"amount"`
	Money           json.Number `json:"money"`
	PaymentMethod   string      `json:"payment_method"`
	PaymentProvider string      `json:"payment_provider"`
	CreateTime      int64       `json:"create_time"`
	CompleteTime    int64       `json:"complete_time"`
	Status          string      `json:"status"`
}

func (d newAPITopupDTO) toProjectionRow() (newAPITopupRow, error) {
	money, err := decimalFromJSONNumber(d.Money)
	if err != nil {
		return newAPITopupRow{}, err
	}
	return newAPITopupRow{
		ID: d.ID, UserID: d.UserID, Amount: d.Amount,
		Money:         decimalValue{Value: money},
		PaymentMethod: d.PaymentMethod, PaymentProvider: d.PaymentProvider,
		CreateTime: d.CreateTime, CompleteTime: d.CompleteTime, Status: d.Status,
	}, nil
}

func decimalFromJSONNumber(value json.Number) (string, error) {
	if strings.TrimSpace(value.String()) == "" {
		return "", errors.New("required decimal is missing")
	}
	return normalizeDecimal(value.String())
}

func requiredAPITime(value string) (nullableTime, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nullableTime{}, errors.New("invalid required API timestamp")
	}
	return nullableTime{Time: parsed.UTC(), Valid: true}, nil
}

func optionalAPITime(value *string) (nullableTime, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nullableTime{}, nil
	}
	return requiredAPITime(*value)
}

func nullableString(value string) sqlNullString {
	value = strings.TrimSpace(value)
	return sqlNullString{String: value, Valid: value != ""}
}

// sqlNullString aliases only the fields used by projection conversion and is
// converted implicitly below to avoid leaking any other DTO fields.
type sqlNullString = struct {
	String string
	Valid  bool
}

func boundedAPILimit(limit, maximum int) int {
	limit = boundedScanLimit(limit)
	if limit > maximum {
		return maximum
	}
	return limit
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
