package sourceagent

import (
	"strings"
	"testing"
	"time"
)

func TestSub2APIPaymentQueryRequiresPerOrderCurrencyView(t *testing.T) {
	t.Parallel()
	query, _, err := sub2APIKeysetQuery(DialectPostgres, time.Unix(0, 0).UTC(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "FROM public.invoice_sub2api_payment_projection_v1") ||
		!strings.Contains(query, "CAST(refund_amount AS TEXT), currency") ||
		strings.Contains(query, "FROM payment_orders") {
		t.Fatalf("query bypassed reviewed per-order currency view: %s", query)
	}
}

func TestSub2APIPaymentHealthQueryIsAggregateOnly(t *testing.T) {
	t.Parallel()
	if strings.Contains(sub2APIProjectionHealthQuery, " id") || strings.Contains(sub2APIProjectionHealthQuery, "provider_snapshot") ||
		!strings.Contains(sub2APIProjectionHealthQuery, "unsupported_known_non_cny_rows") ||
		!strings.Contains(sub2APIProjectionHealthQuery, "blocked_unknown_currency_rows") {
		t.Fatalf("health query exposed row identity/snapshot or missed blocked aggregate: %s", sub2APIProjectionHealthQuery)
	}
}

func TestSub2APIProjectionHealthSeparatesUnsupportedFromBlocked(t *testing.T) {
	t.Parallel()
	warnings, blocked, err := sub2APIProjectionHealthResult(10, 8, 2, 0)
	if err != nil || blocked || len(warnings) != 1 || !strings.Contains(warnings[0], "non-CNY") {
		t.Fatalf("known non-CNY result warnings=%v blocked=%v err=%v", warnings, blocked, err)
	}
	warnings, blocked, err = sub2APIProjectionHealthResult(10, 8, 0, 2)
	if err != nil || !blocked || len(warnings) != 1 || !strings.Contains(warnings[0], "quarantined") {
		t.Fatalf("unknown currency result warnings=%v blocked=%v err=%v", warnings, blocked, err)
	}
	if _, _, err = sub2APIProjectionHealthResult(10, 8, 1, 0); err == nil {
		t.Fatal("inconsistent health aggregate was accepted")
	}
}

func TestProratedGatewayRefundExactSemantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, amount, paid, refunded, currency, want string
	}{
		{name: "balance multiplier partial", amount: "120", paid: "100", refunded: "60", currency: "CNY", want: "50"},
		{name: "gateway fee may exceed business amount", amount: "100", paid: "103", refunded: "20", currency: "CNY", want: "20.6"},
		{name: "subscription conversion partial", amount: "9.99", paid: "72.43", refunded: "5.00", currency: "CNY", want: "36.25"},
		{name: "full refund returns exact paid amount", amount: "9.99", paid: "72.43", refunded: "9.99", currency: "CNY", want: "72.43"},
		{name: "half cent rounds up", amount: "8", paid: "1", refunded: "1", currency: "CNY", want: "0.13"},
		{name: "zero decimal half rounds up", amount: "2", paid: "1", refunded: "1", currency: "JPY", want: "1"},
		{name: "three decimal currency", amount: "8", paid: "1", refunded: "1", currency: "KWD", want: "0.125"},
		{name: "large exact rational does not overflow", amount: "999999999999999999", paid: "999999999999999998", refunded: "500000000000000000", currency: "CNY", want: "499999999999999999.5"},
		{name: "zero refund", amount: "100", paid: "80", refunded: "0", currency: "CNY", want: "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := proratedGatewayRefund(tc.amount, tc.paid, tc.refunded, tc.currency)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("gateway refund=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestProratedGatewayRefundRejectsInconsistentSourceMoney(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ amount, paid, refunded, currency string }{
		{amount: "0", paid: "10", refunded: "1", currency: "CNY"},
		{amount: "100", paid: "10", refunded: "100.01", currency: "CNY"},
		{amount: "100", paid: "-1", refunded: "1", currency: "CNY"},
		{amount: "100", paid: "10", refunded: "-1", currency: "CNY"},
		{amount: "100", paid: "10", refunded: "1", currency: "cny"},
		{amount: "not-money", paid: "10", refunded: "1", currency: "CNY"},
	} {
		if got, err := proratedGatewayRefund(tc.amount, tc.paid, tc.refunded, tc.currency); err == nil {
			t.Fatalf("inconsistent source money accepted as %q: %+v", got, tc)
		}
	}
}

func TestProjectSub2APIOrderUsesPaidMoneyAndOnlyGatewayRefundAdjustment(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	row := sub2APIOrderRow{
		ID: 9, UserID: 3, Status: "PARTIALLY_REFUNDED", OrderType: "balance",
		Amount: decimalValue{Value: "100"}, PayAmount: decimalValue{Value: "90"},
		RefundAmount: decimalValue{Value: "20"}, Currency: "CNY",
		CreatedAt: nullableTime{Time: now.Add(-time.Hour), Valid: true},
		UpdatedAt: nullableTime{Time: now, Valid: true},
	}
	projections, err := projectSub2APIOrder(row, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 2 {
		t.Fatalf("got %d projections; amount/pay difference must not become an adjustment", len(projections))
	}
	order := projections[0].Payload.(PaymentOrderPayload)
	if order.Amount != "100" || order.PayAmount != "90" || order.RefundAmount != "20" || order.GatewayRefundAmount != "18" {
		t.Fatalf("unexpected order money: %+v", order)
	}
	adjustment := projections[1].Payload.(PaymentAdjustmentPayload)
	if adjustment.AdjustmentType != AdjustmentRefund || adjustment.Amount != "18" || adjustment.Basis != "absolute_cumulative_gateway_refund" {
		t.Fatalf("unexpected adjustment: %+v", adjustment)
	}

	row.Status = "COMPLETED"
	row.RefundAmount = decimalValue{Value: "0"}
	projections, err = projectSub2APIOrder(row, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 1 {
		t.Fatalf("unrefunded amount/pay difference emitted a false adjustment: %+v", projections)
	}
}
