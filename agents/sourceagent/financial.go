package sourceagent

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const (
	VerificationPendingManual = "pending_manual"
	AdjustmentRefund          = "refund"
)

var sub2APIStatuses = map[string]struct{}{
	"PENDING": {}, "PAID": {}, "RECHARGING": {}, "COMPLETED": {},
	"EXPIRED": {}, "CANCELLED": {}, "FAILED": {},
	"REFUND_REQUESTED": {}, "REFUNDING": {}, "REFUND_PENDING": {},
	"PARTIALLY_REFUNDED": {}, "REFUNDED": {}, "REFUND_FAILED": {},
}

func projectSub2APIOrder(row sub2APIOrderRow, observedAt time.Time) ([]Projection, error) {
	if row.ID <= 0 || row.UserID <= 0 {
		return nil, errors.New("invalid order or user id")
	}
	status := strings.ToUpper(strings.TrimSpace(row.Status))
	if _, ok := sub2APIStatuses[status]; !ok {
		return nil, fmt.Errorf("unknown payment status %q", row.Status)
	}
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return nil, errors.New("created_at and updated_at are required")
	}
	currency := strings.ToUpper(strings.TrimSpace(row.Currency))
	if !currencyPattern.MatchString(currency) {
		return nil, fmt.Errorf("invalid currency %q", row.Currency)
	}
	gatewayRefundAmount, err := proratedGatewayRefund(
		row.Amount.Value,
		row.PayAmount.Value,
		row.RefundAmount.Value,
		currency,
	)
	if err != nil {
		return nil, fmt.Errorf("derive gateway refund: %w", err)
	}
	orderID := strconv.FormatInt(row.ID, 10)
	userID := strconv.FormatInt(row.UserID, 10)
	payload := PaymentOrderPayload{
		ExternalOrderID:     orderID,
		ExternalUserID:      userID,
		Status:              status,
		OrderType:           strings.TrimSpace(row.OrderType),
		Amount:              row.Amount.Value,
		PayAmount:           row.PayAmount.Value,
		Currency:            currency,
		RefundAmount:        row.RefundAmount.Value,
		GatewayRefundAmount: gatewayRefundAmount,
		CompletedAt:         nullableTimeString(row.CompletedAt),
		RefundAt:            nullableTimeString(row.RefundAt),
		CreatedAt:           row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		UpdatedAt:           row.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
		PaymentType:         strings.TrimSpace(row.PaymentType.String),
		ProviderKey:         strings.TrimSpace(row.ProviderKey.String),
	}
	if payload.OrderType == "" {
		return nil, errors.New("order_type is required")
	}
	projections := []Projection{{
		EntityType: EntityPaymentOrder,
		ExternalID: orderID,
		ObservedAt: observedAt.Format(time.RFC3339Nano),
		Operation:  "upsert",
		Payload:    payload,
	}}

	if positive, err := decimalPositive(payload.GatewayRefundAmount); err != nil {
		return nil, err
	} else if positive {
		projections = append(projections, Projection{
			EntityType: EntityPaymentAdjustment,
			ExternalID: orderID + ":refund",
			ObservedAt: observedAt.Format(time.RFC3339Nano),
			Operation:  "upsert",
			Payload: PaymentAdjustmentPayload{
				ExternalOrderID: orderID,
				ExternalUserID:  userID,
				AdjustmentType:  AdjustmentRefund,
				Amount:          payload.GatewayRefundAmount,
				Currency:        currency,
				EffectiveAt:     payload.RefundAt,
				SourceStatus:    status,
				SourceUpdatedAt: payload.UpdatedAt,
				Basis:           "absolute_cumulative_gateway_refund",
			},
		})
	}
	return projections, nil
}

func projectNewAPITopup(row newAPITopupRow, observedAt time.Time) (Projection, error) {
	if row.ID <= 0 || row.UserID <= 0 || row.CreateTime <= 0 {
		return Projection{}, errors.New("invalid top-up identity or create_time")
	}
	createdAt := time.Unix(row.CreateTime, 0).UTC().Format(time.RFC3339)
	var completedAt *string
	if row.CompleteTime > 0 {
		value := time.Unix(row.CompleteTime, 0).UTC().Format(time.RFC3339)
		completedAt = &value
	}
	sourceObservedAt := createdAt
	if completedAt != nil {
		sourceObservedAt = *completedAt
	}
	payload := PaymentCandidatePayload{
		ExternalOrderID:    strconv.FormatInt(row.ID, 10),
		ExternalUserID:     strconv.FormatInt(row.UserID, 10),
		SourceStatus:       strings.TrimSpace(row.Status),
		OrderType:          "topup",
		QuotedAmount:       strconv.FormatInt(row.Amount, 10),
		ObservedPayAmount:  row.Money.Value,
		VerificationState:  VerificationPendingManual,
		VerificationReason: "source DTO cannot distinguish provider settlement from administrator completion and has no refund or currency evidence",
		CompletedAt:        completedAt,
		CreatedAt:          createdAt,
		ObservedAt:         sourceObservedAt,
		PaymentType:        strings.TrimSpace(row.PaymentMethod),
		ProviderKey:        strings.TrimSpace(row.PaymentProvider),
	}
	return Projection{
		EntityType: EntityPaymentCandidate,
		ExternalID: payload.ExternalOrderID,
		ObservedAt: observedAt.Format(time.RFC3339Nano),
		Operation:  "upsert",
		Payload:    payload,
	}, nil
}

func nullableTimeString(value nullableTime) *string {
	if !value.Valid {
		return nil
	}
	formatted := value.Time.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func normalizeDecimal(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, "eE+") {
		return "", fmt.Errorf("invalid non-negative decimal")
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("invalid decimal")
	}
	integer := strings.TrimLeft(parts[0], "0")
	if integer == "" {
		integer = "0"
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = strings.TrimRight(parts[1], "0")
		if len(parts[1]) > 8 {
			return "", fmt.Errorf("decimal precision exceeds eight places")
		}
	}
	normalized := integer
	if fraction != "" {
		normalized += "." + fraction
	}
	if !decimalPattern.MatchString(normalized) {
		return "", fmt.Errorf("decimal is outside the projection range")
	}
	return normalized, nil
}

func decimalPositive(value string) (bool, error) {
	normalized, err := normalizeDecimal(value)
	if err != nil {
		return false, err
	}
	return normalized != "0", nil
}

// proratedGatewayRefund mirrors Sub2API v0.1.179's authoritative refund
// formula without binary floating-point: pay_amount * refund_amount / amount.
// Positive half units round away from zero, matching shopspring/decimal.Round.
func proratedGatewayRefund(amount, payAmount, refundAmount, currency string) (string, error) {
	amountRat, ok := new(big.Rat).SetString(amount)
	if !ok || amountRat.Sign() <= 0 {
		return "", errors.New("amount must be a positive decimal")
	}
	payRat, ok := new(big.Rat).SetString(payAmount)
	if !ok || payRat.Sign() < 0 {
		return "", errors.New("pay_amount must be a non-negative decimal")
	}
	refundRat, ok := new(big.Rat).SetString(refundAmount)
	if !ok || refundRat.Sign() < 0 || refundRat.Cmp(amountRat) > 0 {
		return "", errors.New("refund_amount must be between zero and amount")
	}
	if !currencyPattern.MatchString(currency) {
		return "", errors.New("currency must be three uppercase letters")
	}
	if refundRat.Sign() == 0 || payRat.Sign() == 0 {
		return "0", nil
	}
	gateway := new(big.Rat)
	if refundRat.Cmp(amountRat) == 0 {
		gateway.Set(payRat)
	} else {
		gateway.Mul(payRat, refundRat)
		gateway.Quo(gateway, amountRat)
	}
	return roundPositiveRat(gateway, currencyFractionDigits(currency))
}

func currencyFractionDigits(currency string) int {
	switch currency {
	case "BIF", "CLP", "DJF", "GNF", "JPY", "KMF", "KRW", "MGA", "PYG", "RWF", "VND", "VUV", "XAF", "XOF", "XPF", "ISK", "UGX":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	default:
		return 2
	}
}

func roundPositiveRat(value *big.Rat, digits int) (string, error) {
	if value == nil || value.Sign() < 0 || digits < 0 || digits > 8 {
		return "", errors.New("invalid monetary rational")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	scaledNumerator := new(big.Int).Mul(value.Num(), scale)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaledNumerator, value.Denom(), remainder)
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	integer := new(big.Int).Quo(new(big.Int).Set(quotient), scale)
	if digits == 0 {
		return normalizeDecimal(integer.String())
	}
	fraction := new(big.Int).Mod(new(big.Int).Set(quotient), scale).String()
	fraction = strings.Repeat("0", digits-len(fraction)) + fraction
	return normalizeDecimal(integer.String() + "." + fraction)
}
