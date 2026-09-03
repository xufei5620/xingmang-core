package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/internal/postgresstore"
)

// Mirrors TestAdminEndpointsRequireAdminRole's own coverage of
// /api/v1/admin/eligibility-freezes, for the new ledger endpoint.
func TestEligibilityLedgerHandlerRequiresAdminRole(t *testing.T) {
	server, _ := sourceFilterTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/eligibility-ledger", nil)
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("X-Mock-User-ID", "u1")
	request.Header.Set("X-Mock-Role", "user")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// Mirrors TestAdminIPAllowlistDoesNotTrustSpoofedForwardingHeader /
// TestAdminIPAllowlistAcceptsHeaderOnlyFromTrustedProxy's own per-framework
// coverage, applied specifically to the new route (the framework enforces IP
// allowlisting identically for every s.require("admin", ...) route, but the
// task brief asked for this endpoint's own dedicated test).
func TestEligibilityLedgerHandlerEnforcesAdminIPAllowlist(t *testing.T) {
	fake := &fakeSourceFilterOperations{Service: ledger.NewService()}
	server, err := NewWithConfig(fake, Config{AuthMode: "mock", AdminIPAllowlist: []string{"203.0.113.9/32"}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	denied := httptest.NewRequest(http.MethodGet, "/api/v1/admin/eligibility-ledger", nil)
	denied.RemoteAddr = "198.51.100.22:443"
	denied.Header.Set("X-Mock-User-ID", "admin-1")
	denied.Header.Set("X-Mock-Role", "admin")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, denied)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("denied status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	allowed := httptest.NewRequest(http.MethodGet, "/api/v1/admin/eligibility-ledger", nil)
	allowed.RemoteAddr = "203.0.113.9:443"
	allowed.Header.Set("X-Mock-User-ID", "admin-1")
	allowed.Header.Set("X-Mock-Role", "admin")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, allowed)
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// Pins the design section 3(E) wire contract: field set, threshold_reached
// computed from the real (admin-configurable) minimum invoice amount rather
// than a hardcoded value, null block_* for an unblocked account, and the
// external_user_id/before_id filters forwarded to the store query unchanged.
func TestEligibilityLedgerHandlerReturnsContractShapeAndForwardsFilters(t *testing.T) {
	server, fake := sourceFilterTestServer(t)
	blockedSince := time.Date(2026, 9, 3, 10, 0, 12, 0, time.UTC)
	fake.eligibilityLedgerPage = postgresstore.EligibilityLedgerPage{
		Items: []postgresstore.EligibilityLedgerEntry{
			{
				SourceInstanceID: "10000000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API,
				SourceName: "Sub2API", ExternalUserID: "34",
				RechargedSincePolicyStartMinor: 500_000, ConsumedMinor: 320_000, InvoiceableMinor: 180_000,
				EligibilityStatus: "not_invoiceable_pending_reconciliation",
				BlockReason:       "UNKNOWN_NEGATIVE_BALANCE",
				BlockDetail:       "balance_checkpoint ckpt-xxx at 2026-09-03T10:00:00Z reported balance -120, expected 380",
				BlockSince:        blockedSince,
			},
			{
				SourceInstanceID: "10000000-0000-4000-8000-000000000002", SourceType: domain.SourceNewAPI,
				SourceName: "NewAPI", ExternalUserID: "56",
				RechargedSincePolicyStartMinor: 30_000, ConsumedMinor: 10_000, InvoiceableMinor: 10_000,
				EligibilityStatus: "active",
			},
		},
		HasMore: true, NextBeforeID: "20000000-0000-4000-8000-000000000002",
	}

	request := adminRequest("GET", "/api/v1/admin/eligibility-ledger?external_user_id=34&before_id=20000000-0000-4000-8000-000000000099&limit=2", "127.0.0.1", "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.eligibilityLedgerQuery.ExternalUserID != "34" {
		t.Fatalf("handler did not forward external_user_id: query=%+v", fake.eligibilityLedgerQuery)
	}
	if fake.eligibilityLedgerQuery.BeforeID != "20000000-0000-4000-8000-000000000099" {
		t.Fatalf("handler did not forward before_id: query=%+v", fake.eligibilityLedgerQuery)
	}
	if fake.eligibilityLedgerQuery.Limit != 2 {
		t.Fatalf("handler did not forward limit: query=%+v", fake.eligibilityLedgerQuery)
	}

	var body struct {
		Items []struct {
			SourceInstanceID               string  `json:"source_instance_id"`
			SourceType                     string  `json:"source_type"`
			SourceName                     string  `json:"source_name"`
			ExternalUserID                 string  `json:"external_user_id"`
			RechargedSincePolicyStartMinor int64   `json:"recharged_since_policy_start_minor"`
			ConsumedMinor                  int64   `json:"consumed_minor"`
			InvoiceableMinor               int64   `json:"invoiceable_minor"`
			ThresholdMinor                 int64   `json:"threshold_minor"`
			ThresholdReached               bool    `json:"threshold_reached"`
			EligibilityStatus              string  `json:"eligibility_status"`
			BlockReason                    *string `json:"block_reason"`
			BlockDetail                    *string `json:"block_detail"`
			BlockSince                     *string `json:"block_since"`
		} `json:"items"`
		HasMore      bool   `json:"has_more"`
		NextBeforeID string `json:"next_before_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, recorder.Body.String())
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected 2 items, got %d: %s", len(body.Items), recorder.Body.String())
	}
	if !body.HasMore || body.NextBeforeID != "20000000-0000-4000-8000-000000000002" {
		t.Fatalf("pagination envelope not forwarded: %+v", body)
	}

	blocked := body.Items[0]
	if blocked.ThresholdMinor != domain.MinimumRequestMinor {
		t.Fatalf("threshold_minor=%d want=%d", blocked.ThresholdMinor, domain.MinimumRequestMinor)
	}
	if !blocked.ThresholdReached {
		t.Fatalf("blocked item invoiceable_minor=%d should have reached threshold=%d", blocked.InvoiceableMinor, blocked.ThresholdMinor)
	}
	if blocked.EligibilityStatus != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("unexpected eligibility_status: %+v", blocked)
	}
	if blocked.BlockReason == nil || *blocked.BlockReason != "UNKNOWN_NEGATIVE_BALANCE" {
		t.Fatalf("expected block_reason to survive the wire: %+v", blocked)
	}
	if blocked.BlockDetail == nil || *blocked.BlockDetail == "" {
		t.Fatalf("expected block_detail to survive the wire: %+v", blocked)
	}
	if blocked.BlockSince == nil || *blocked.BlockSince == "" {
		t.Fatalf("expected block_since to survive the wire: %+v", blocked)
	}

	active := body.Items[1]
	if active.ThresholdReached {
		t.Fatalf("active item invoiceable_minor=%d should be below threshold=%d", active.InvoiceableMinor, active.ThresholdMinor)
	}
	if active.BlockReason != nil || active.BlockDetail != nil || active.BlockSince != nil {
		t.Fatalf("unblocked account must report null block_*: %+v", active)
	}

	// Omitting the filters must leave them empty (unscoped), same convention
	// as every other optional admin-list filter in this package.
	unscoped := adminRequest("GET", "/api/v1/admin/eligibility-ledger", "127.0.0.1", "")
	unscopedRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(unscopedRecorder, unscoped)
	if unscopedRecorder.Code != http.StatusOK {
		t.Fatalf("unscoped status=%d body=%s", unscopedRecorder.Code, unscopedRecorder.Body.String())
	}
	if fake.eligibilityLedgerQuery.ExternalUserID != "" || fake.eligibilityLedgerQuery.BeforeID != "" {
		t.Fatalf("expected no filters, got query=%+v", fake.eligibilityLedgerQuery)
	}
	if fake.eligibilityLedgerQuery.Limit != 100 {
		t.Fatalf("expected default limit 100, got %d", fake.eligibilityLedgerQuery.Limit)
	}
}
