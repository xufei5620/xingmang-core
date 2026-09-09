package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/postgresstore"
)

// Mirrors TestAdminEndpointsRequireAdminRole's own coverage of
// /api/v1/admin/eligibility-freezes, for both new CR-0009 ledger routes.
func TestAccountLedgerHandlersRequireAdminRole(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/accounts/ledger", "/api/v1/admin/accounts/61000000-0000-4000-8000-000000000001/ledger"} {
		server, _ := sourceFilterTestServer(t)
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = "127.0.0.1:443"
		request.Header.Set("X-Mock-User-ID", "u1")
		request.Header.Set("X-Mock-Role", "user")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("path=%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

// Mirrors TestAdminIPAllowlistDoesNotTrustSpoofedForwardingHeader /
// TestAdminIPAllowlistAcceptsHeaderOnlyFromTrustedProxy's own per-framework
// coverage, applied specifically to both new routes (the framework enforces
// IP allowlisting identically for every s.require("admin", ...) route, but
// the task brief asked for this endpoint's own dedicated test).
func TestAccountLedgerHandlersEnforceAdminIPAllowlist(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/accounts/ledger", "/api/v1/admin/accounts/61000000-0000-4000-8000-000000000001/ledger"} {
		fake := &fakeSourceFilterOperations{Service: ledger.NewService()}
		server, err := NewWithConfig(fake, Config{AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.9/32"}}, nil)
		if err != nil {
			t.Fatal(err)
		}

		denied := httptest.NewRequest(http.MethodGet, path, nil)
		denied.RemoteAddr = "198.51.100.22:443"
		denied.Header.Set("X-Mock-User-ID", "admin-1")
		denied.Header.Set("X-Mock-Role", "admin")
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, denied)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("path=%s denied status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}

		allowed := httptest.NewRequest(http.MethodGet, path, nil)
		allowed.RemoteAddr = "203.0.113.9:443"
		allowed.Header.Set("X-Mock-User-ID", "admin-1")
		allowed.Header.Set("X-Mock-Role", "admin")
		recorder = httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, allowed)
		if recorder.Code != http.StatusOK && recorder.Code != http.StatusNotFound {
			// The detail route's fake backing store returns a zero-value
			// AccountLedgerDetail with no error -- a 200 with an empty body
			// shape is fine here; this test isolates the IP-allowlist
			// middleware, not the handler's own response shape (that is
			// TestAccountLedgerListHandlerReturnsContractShape/
			// TestAccountLedgerDetailHandlerReturnsContractShape below).
			t.Fatalf("path=%s allowed status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

// XM-INV-LEDGER-ACCOUNT-EMAIL: account_email is additive and optional. It
// appears only for accounts that actually have a verified address on file,
// and the key is absent -- not an empty string -- for the others, so an
// operator can tell "no address" from "blank address".
func TestAccountLedgerListHandlerEmitsAccountEmailOnlyWhenPresent(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	fake.accountLedgerPage = postgresstore.AccountLedgerPage{
		Items: []postgresstore.AccountLedgerListEntry{
			{
				ExternalAccountID: "1a2b0000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API,
				ExternalUserID: "1147", BlockState: postgresstore.AccountBlockStateInvoiceable,
				AccountEmail: "chen.yuan@example.com",
			},
			{
				ExternalAccountID: "1a2b0000-0000-4000-8000-000000000002", SourceType: domain.SourceNewAPI,
				ExternalUserID: "56", BlockState: postgresstore.AccountBlockStateBelowThreshold,
			},
		},
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, adminRequest("GET", "/api/v1/admin/accounts/ledger", "127.0.0.1", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected two items, got %d", len(body.Items))
	}
	if body.Items[0]["account_email"] != "chen.yuan@example.com" {
		t.Fatalf("first item did not carry the account email: %+v", body.Items[0])
	}
	if _, present := body.Items[1]["account_email"]; present {
		t.Fatalf("second item must omit account_email entirely, got %+v", body.Items[1])
	}
}

// Pins CR-0009's list wire contract: field set, threshold_reached computed
// from the real (admin-configurable) minimum invoice amount rather than a
// hardcoded value, null last_checkpoint_at for a never-evaluated account,
// and every filter/sort/cursor query parameter forwarded to the store query
// unchanged.
func TestAccountLedgerListHandlerReturnsContractShape(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	checkpointAt := time.Date(2026, 9, 3, 6, 25, 11, 0, time.UTC)
	minor := int64(31_800)
	fake.accountLedgerPage = postgresstore.AccountLedgerPage{
		Items: []postgresstore.AccountLedgerListEntry{
			{
				ExternalAccountID: "1a2b0000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API,
				ExternalUserID: "1147", PolicyStartAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				RechargesSinceStartCount: 3, RechargesSinceStartMinor: 128_000,
				ConsumedSinceStartMinor: 96_000, InvoiceableNowMinor: 31_800, IssuedMinor: 0,
				BlockState: postgresstore.AccountBlockStateInvoiceable, LastCheckpointAt: checkpointAt,
			},
			{
				ExternalAccountID: "1a2b0000-0000-4000-8000-000000000002", SourceType: domain.SourceNewAPI,
				ExternalUserID: "56", RechargesSinceStartMinor: 5_000, ConsumedSinceStartMinor: 1_000,
				InvoiceableNowMinor: 4_000, BlockState: postgresstore.AccountBlockStateBelowThreshold,
			},
		},
		HasMore: true, NextBeforeInvoiceableMinor: &minor, NextBeforeID: "1a2b0000-0000-4000-8000-000000000002",
	}

	request := adminRequest("GET", "/api/v1/admin/accounts/ledger?external_user_id=1147&source_instance_id=10000000-0000-4000-8000-000000000001&sort=block_state&before_id=99000000-0000-4000-8000-000000000099&limit=2", "127.0.0.1", "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.accountLedgerQuery.ExternalUserID != "1147" {
		t.Fatalf("handler did not forward external_user_id: query=%+v", fake.accountLedgerQuery)
	}
	if fake.accountLedgerQuery.SourceInstanceID != "10000000-0000-4000-8000-000000000001" {
		t.Fatalf("handler did not forward source_instance_id: query=%+v", fake.accountLedgerQuery)
	}
	if fake.accountLedgerQuery.Sort != "block_state" {
		t.Fatalf("handler did not forward sort: query=%+v", fake.accountLedgerQuery)
	}
	if fake.accountLedgerQuery.BeforeID != "99000000-0000-4000-8000-000000000099" {
		t.Fatalf("handler did not forward before_id: query=%+v", fake.accountLedgerQuery)
	}
	if fake.accountLedgerQuery.Limit != 2 {
		t.Fatalf("handler did not forward limit: query=%+v", fake.accountLedgerQuery)
	}
	if fake.accountLedgerQuery.ThresholdMinor != domain.MinimumRequestMinor {
		t.Fatalf("handler did not pass the real minimum request amount as threshold: %+v", fake.accountLedgerQuery)
	}

	var body struct {
		Items []struct {
			ExternalAccountID        string  `json:"external_account_id"`
			SourceType               string  `json:"source_type"`
			ExternalUserID           string  `json:"external_user_id"`
			PolicyStartAt            string  `json:"policy_start_at"`
			RechargesSinceStartCount int     `json:"recharges_since_start_count"`
			RechargesSinceStartMinor int64   `json:"recharges_since_start_minor"`
			ConsumedSinceStartMinor  int64   `json:"consumed_since_start_minor"`
			InvoiceableNowMinor      int64   `json:"invoiceable_now_minor"`
			IssuedMinor              int64   `json:"issued_minor"`
			ThresholdReached         bool    `json:"threshold_reached"`
			BlockState               string  `json:"block_state"`
			LastCheckpointAt         *string `json:"last_checkpoint_at"`
		} `json:"items"`
		HasMore                    bool   `json:"has_more"`
		NextBeforeInvoiceableMinor *int64 `json:"next_before_invoiceable_minor"`
		NextBeforeID               string `json:"next_before_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, recorder.Body.String())
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected 2 items, got %d: %s", len(body.Items), recorder.Body.String())
	}
	if !body.HasMore || body.NextBeforeID != "1a2b0000-0000-4000-8000-000000000002" || body.NextBeforeInvoiceableMinor == nil || *body.NextBeforeInvoiceableMinor != 31_800 {
		t.Fatalf("pagination envelope not forwarded: %+v", body)
	}

	first := body.Items[0]
	if first.ExternalAccountID != "1a2b0000-0000-4000-8000-000000000001" {
		t.Fatalf("external_account_id missing from the list contract: %+v", first)
	}
	if first.RechargesSinceStartCount != 3 || first.RechargesSinceStartMinor != 128_000 || first.ConsumedSinceStartMinor != 96_000 || first.InvoiceableNowMinor != 31_800 {
		t.Fatalf("amounts: %+v", first)
	}
	if !first.ThresholdReached {
		t.Fatalf("expected threshold_reached=true for invoiceable_now_minor=31800 >= threshold=%d: %+v", domain.MinimumRequestMinor, first)
	}
	if first.BlockState != "invoiceable" {
		t.Fatalf("block_state=%q", first.BlockState)
	}
	if first.LastCheckpointAt == nil {
		t.Fatal("expected last_checkpoint_at to survive the wire for a checkpointed account")
	}

	second := body.Items[1]
	if second.ThresholdReached {
		t.Fatalf("expected threshold_reached=false for invoiceable_now_minor=4000 < threshold=%d: %+v", domain.MinimumRequestMinor, second)
	}
	if second.BlockState != "below_threshold" {
		t.Fatalf("block_state=%q", second.BlockState)
	}
	if second.LastCheckpointAt != nil {
		t.Fatalf("expected null last_checkpoint_at for a never-evaluated account, got %v", *second.LastCheckpointAt)
	}

	// Omitting every filter must leave them empty (unscoped, default sort),
	// same convention as every other optional admin-list filter in this
	// package.
	unscoped := adminRequest("GET", "/api/v1/admin/accounts/ledger", "127.0.0.1", "")
	unscopedRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(unscopedRecorder, unscoped)
	if unscopedRecorder.Code != http.StatusOK {
		t.Fatalf("unscoped status=%d body=%s", unscopedRecorder.Code, unscopedRecorder.Body.String())
	}
	if fake.accountLedgerQuery.ExternalUserID != "" || fake.accountLedgerQuery.SourceInstanceID != "" || fake.accountLedgerQuery.Sort != "" || fake.accountLedgerQuery.BeforeID != "" {
		t.Fatalf("expected no filters, got query=%+v", fake.accountLedgerQuery)
	}
	if fake.accountLedgerQuery.Limit != 100 {
		t.Fatalf("expected default limit 100, got %d", fake.accountLedgerQuery.Limit)
	}
}

// Pins CR-0009's detail wire contract: every list field plus
// opening_balance_units, recharges_since_start[], consumption_timeline,
// block_reason (a concrete Chinese sentence, never null for a blocked
// account) and last_reconciled_at.
func TestAccountLedgerDetailHandlerReturnsContractShape(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	since := time.Date(2026, 9, 3, 6, 25, 11, 0, time.UTC)
	fake.accountLedgerDetail = postgresstore.AccountLedgerDetail{
		AccountLedgerListEntry: postgresstore.AccountLedgerListEntry{
			ExternalAccountID: "1a2b0000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API,
			ExternalUserID: "1147", PolicyStartAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			RechargesSinceStartCount: 1, RechargesSinceStartMinor: 50_000, ConsumedSinceStartMinor: 96_000,
			InvoiceableNowMinor: 31_800, IssuedMinor: 0,
			BlockState:       postgresstore.AccountBlockStateNotInvoiceablePendingReconciliation,
			LastCheckpointAt: since,
		},
		OpeningBalanceServiceUnits: "48200", OpeningBalanceUnitCode: "USD_MICRO",
		Recharges: []postgresstore.AccountLedgerRecharge{
			{FundingLotID: "f9e80000-0000-4000-8000-000000000001", CompletedAt: time.Date(2026, 9, 2, 3, 11, 0, 0, time.UTC),
				AmountMinor: 50_000, EligibilityKind: "WALLET_CASH", RefundFrozen: false},
		},
		ConsumptionTimeline:         []postgresstore.AccountLedgerConsumptionDay{{Date: "2026-09-02", ConsumedMinor: 96_000}},
		PendingReconciliationReason: "UNKNOWN_NEGATIVE_BALANCE", PendingReconciliationTriggerType: "balance_checkpoint",
		PendingReconciliationTriggerID: "ckpt-xxx", PendingReconciliationSince: since,
		LatestEvaluationKind: "balance_checkpoint", LatestEvaluationKey: "ckpt-xxx", LatestEvaluationStatus: "negative_frozen",
		LatestEvaluationExpectedUnits: "380", LatestEvaluationDifferenceUnits: "-500",
	}

	request := adminRequest("GET", "/api/v1/admin/accounts/1a2b0000-0000-4000-8000-000000000001/ledger", "127.0.0.1", "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.accountLedgerDetailID != "1a2b0000-0000-4000-8000-000000000001" {
		t.Fatalf("handler did not forward the path id: got %q", fake.accountLedgerDetailID)
	}

	var body struct {
		ExternalAccountID   string `json:"external_account_id"`
		BlockState          string `json:"block_state"`
		BlockReason         string `json:"block_reason"`
		OpeningBalanceUnits struct {
			ServiceUnits string `json:"service_units"`
			UnitCode     string `json:"unit_code"`
		} `json:"opening_balance_units"`
		RechargesSinceStart []struct {
			FundingLotID    string `json:"funding_lot_id"`
			AmountMinor     int64  `json:"amount_minor"`
			EligibilityKind string `json:"eligibility_kind"`
			RefundFrozen    bool   `json:"refund_frozen"`
		} `json:"recharges_since_start"`
		ConsumptionTimeline []struct {
			Date          string `json:"date"`
			ConsumedMinor int64  `json:"consumed_minor"`
		} `json:"consumption_timeline"`
		LastReconciledAt *string `json:"last_reconciled_at"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, recorder.Body.String())
	}
	if body.ExternalAccountID != "1a2b0000-0000-4000-8000-000000000001" {
		t.Fatalf("external_account_id=%q", body.ExternalAccountID)
	}
	if body.BlockState != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("block_state=%q", body.BlockState)
	}
	if body.BlockReason == "" {
		t.Fatal("expected a non-empty, concrete block_reason for a blocked account")
	}
	if body.BlockReason == "数据异常" {
		t.Fatalf("block_reason must never be a generic phrase: %q", body.BlockReason)
	}
	if body.OpeningBalanceUnits.ServiceUnits != "48200" || body.OpeningBalanceUnits.UnitCode != "USD_MICRO" {
		t.Fatalf("opening_balance_units=%+v", body.OpeningBalanceUnits)
	}
	if len(body.RechargesSinceStart) != 1 || body.RechargesSinceStart[0].AmountMinor != 50_000 {
		t.Fatalf("recharges_since_start=%+v", body.RechargesSinceStart)
	}
	if len(body.ConsumptionTimeline) != 1 || body.ConsumptionTimeline[0].ConsumedMinor != 96_000 {
		t.Fatalf("consumption_timeline=%+v", body.ConsumptionTimeline)
	}
	if body.LastReconciledAt != nil {
		t.Fatalf("expected null last_reconciled_at (this account has never matched), got %v", *body.LastReconciledAt)
	}
}

// Unknown account -> 404, existing error-envelope conventions (task brief
// item 2's explicit requirement).
func TestAccountLedgerDetailHandlerReturnsNotFoundForUnknownAccount(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	fake.accountLedgerDetailErr = domain.ErrNotFound

	request := adminRequest("GET", "/api/v1/admin/accounts/00000000-0000-4000-8000-000000000000/ledger", "127.0.0.1", "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, recorder.Body.String())
	}
	if body.Error.Code != "NOT_FOUND" {
		t.Fatalf("code=%q, want the existing NOT_FOUND error envelope", body.Error.Code)
	}
}

// An unblocked account (block_state=invoiceable) must report a null, not
// empty-string, block_reason -- matching every other null-when-unblocked
// field's own convention on this page.
func TestAccountLedgerDetailHandlerNullsBlockReasonWhenInvoiceable(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	fake.accountLedgerDetail = postgresstore.AccountLedgerDetail{
		AccountLedgerListEntry: postgresstore.AccountLedgerListEntry{
			ExternalAccountID: "1a2b0000-0000-4000-8000-000000000009", BlockState: postgresstore.AccountBlockStateInvoiceable,
		},
	}
	request := adminRequest("GET", "/api/v1/admin/accounts/1a2b0000-0000-4000-8000-000000000009/ledger", "127.0.0.1", "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		BlockReason *string `json:"block_reason"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, recorder.Body.String())
	}
	if body.BlockReason != nil {
		t.Fatalf("expected null block_reason for an invoiceable account, got %q", *body.BlockReason)
	}
}

// TestAccountBlockReasonNamesTheEvaluationColumnsForWhatTheyHold is
// XM-INV-PENDING-RECON's first review, finding m5. The evaluation row stores
// two numbers that do not form a pair: difference_service_units is measured
// against the signed expectation (the ledger's pools minus every unit of usage
// no pool covered), while expected_service_units holds only the pools, floored
// at zero by that column's own >=0 CHECK. On an account carrying an
// unallocated debt they disagree by exactly that debt -- reported 300, pools 0,
// difference 350 -- and the sentence used to print the floored value as 预期,
// so the same evaluation stated two different expectations to whoever read it.
func TestAccountBlockReasonNamesTheEvaluationColumnsForWhatTheyHold(t *testing.T) {
	detail := postgresstore.AccountLedgerDetail{
		AccountLedgerListEntry: postgresstore.AccountLedgerListEntry{
			BlockState:       postgresstore.AccountBlockStateNotInvoiceablePendingReconciliation,
			LastCheckpointAt: time.Date(2026, 9, 6, 11, 29, 30, 0, time.UTC),
		},
		OpeningBalanceUnitCode:          "SUB2_BALANCE_1E8",
		LatestEvaluationKind:            "balance_checkpoint",
		LatestEvaluationKey:             "ckpt-debt",
		LatestEvaluationStatus:          "negative_frozen",
		LatestEvaluationExpectedUnits:   "0",
		LatestEvaluationDifferenceUnits: "350",
	}
	reason := buildAccountBlockReason(detail, domain.MinimumRequestMinor)
	if !strings.Contains(reason, "上报余额与账面预期相差 350") {
		t.Fatalf("the difference must be named as a difference against the signed expectation: %q", reason)
	}
	if !strings.Contains(reason, "账面剩余池 0") {
		t.Fatalf("the stored column must be named as the pool total it is: %q", reason)
	}
	if strings.Contains(reason, "预期 0") {
		t.Fatalf("the floored pool total must not be presented as the expectation: %q", reason)
	}
}
