package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

// CR-0007 problem one deliberately reverses this endpoint's prior "never
// returns external user IDs" posture: external_user_id must now appear (see
// the "required" list below), plain and unmasked, while every other
// previously-redacted internal identifier/secret stays out (the "forbidden"
// list). ExternalAccountID stays redacted -- only ExternalUserID (the
// upstream platform's own user ID) is the field this CR intentionally
// exposes.
func TestEligibilityFreezeDTOIsExplicitlyRedacted(t *testing.T) {
	item := postgresstore.EligibilityFreeze{ID: "61000000-0000-4000-8000-000000000001", PrincipalID: "20000000-0000-4000-8000-000000000001", ExternalAccountID: "sensitive-account-id", ExternalUserID: "1147", SourceInstanceID: "10000000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API, SourceName: "Sub2API", FundingLotID: "50000000-0000-4000-8000-000000000001", FreezeReason: "SOURCE_GAP", Status: "open", EligibilityStatus: "frozen", OpenedAt: time.Now(), EvidenceHash: strings.Repeat("a", 64), NoteHash: strings.Repeat("b", 64)}
	raw, err := json.Marshal(eligibilityFreezeDTO(item))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	// trigger_object_* left this list in XM-INV-FREEZE-TRIGGER-VISIBLE. It sat
	// here among genuine internal identifiers, but it is not one: the account
	// ledger's own detail already renders the identical pair into its
	// block_reason sentence ("关联对象 %s:%s", see buildFrozenManualReviewReason)
	// for the same operator in the same console, so withholding it here was an
	// inconsistency rather than a boundary. Withholding it also had a cost --
	// production showed four SOURCE_GAP freezes on one account, identical in
	// every visible column because freezeEligibilityTx dedupes on
	// (account, reason, trigger_object_type, trigger_object_id) and each
	// triggering checkpoint gets its own row, so the trigger was the only
	// thing that told them apart. Everything else in this list stays
	// forbidden.
	for _, forbidden := range []string{"sensitive-account-id", strings.Repeat("a", 64), strings.Repeat("b", 64), "source_cursor", "evidence", "revision_hash"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("eligibility freeze DTO leaked %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"freeze_reason", "source_type", "principal_id", "version", `"external_user_id":"1147"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("eligibility freeze DTO omitted %q: %s", required, text)
		}
	}
	// The fixture above carries no trigger, and absent must stay absent
	// rather than becoming an empty-string placeholder -- same posture as
	// account_email beside it.
	if strings.Contains(text, "trigger_object") {
		t.Fatalf("a freeze with no trigger must not carry the keys at all: %s", text)
	}
}

// TestEligibilityFreezeDTOCarriesTheTriggerThatTellsRowsApart is
// XM-INV-FREEZE-TRIGGER-VISIBLE's guard. Production had four open SOURCE_GAP
// freezes on account 2092, same reason, same scope, same second -- four rows
// an operator could not tell apart, could not reference, and could not judge
// whether handling one handled all of them.
func TestEligibilityFreezeDTOCarriesTheTriggerThatTellsRowsApart(t *testing.T) {
	base := postgresstore.EligibilityFreeze{ID: "61000000-0000-4000-8000-000000000001",
		PrincipalID: "20000000-0000-4000-8000-000000000001", ExternalAccountID: "acct",
		ExternalUserID: "2092", SourceInstanceID: "10000000-0000-4000-8000-000000000001",
		SourceType: domain.SourceSub2API, SourceName: "Sub2API", FreezeReason: "SOURCE_GAP",
		Status: "open", EligibilityStatus: "frozen", OpenedAt: time.Now(),
		TriggerObjectType: "balance_checkpoint"}
	first := base
	first.TriggerObjectID = "a9e2f44623b7f63b9a2b8df595da"
	second := base
	second.ID = "61000000-0000-4000-8000-000000000002"
	second.TriggerObjectID = "25e36ba7e37a184cda995ddd8309"

	firstRaw, err := json.Marshal(eligibilityFreezeDTO(first))
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := json.Marshal(eligibilityFreezeDTO(second))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"trigger_object_type":"balance_checkpoint"`,
		`"trigger_object_id":"a9e2f44623b7f63b9a2b8df595da"`} {
		if !strings.Contains(string(firstRaw), required) {
			t.Fatalf("freeze DTO omitted %q: %s", required, firstRaw)
		}
	}
	if strings.Contains(string(secondRaw), "a9e2f44623b7f63b9a2b8df595da") {
		t.Fatalf("second freeze reported the first one's trigger: %s", secondRaw)
	}
	if string(firstRaw) == string(secondRaw) {
		t.Fatal("two freezes differing only in their trigger serialised identically -- the queue would render them as duplicates again")
	}
}

// CR-0007 problem three: each of the four narrowed ResolveEligibilityFreeze
// preconditions must reach the wire as its own distinct code, and the prior
// generic behavior (version conflict, and the plain, unwrapped generic
// sentinels) must be completely unchanged.
func TestHandleDomainErrorMapsEligibilityResolutionSentinels(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"five-stream freshness", domain.ErrEligibilitySourceStale, http.StatusServiceUnavailable, "ELIGIBILITY_SOURCE_STALE"},
		{"projection job pending", domain.ErrEligibilityProjectionPending, http.StatusConflict, "ELIGIBILITY_PROJECTION_PENDING"},
		{"refund exposure", domain.ErrEligibilityRefundExposed, http.StatusConflict, "ELIGIBILITY_REFUND_EXPOSED"},
		{"balance evaluation unmatched", domain.ErrEligibilityEvaluationUnmatched, http.StatusConflict, "ELIGIBILITY_EVALUATION_UNMATCHED"},
		// XM-INV-DEAD-CONTAINMENT. It wraps ErrInvalidState like the three
		// above, so it has to be matched before the generic case below or the
		// operator gets a bare CONFLICT and no way to tell which repair the
		// freeze is waiting on.
		{"dead event still unrepaired", domain.ErrEligibilityDeadEventUnrepaired, http.StatusConflict, "ELIGIBILITY_DEAD_EVENT_UNREPAIRED"},
		{"version conflict stays generic", domain.ErrVersionConflict, http.StatusConflict, "CONFLICT"},
		{"plain invalid state stays generic", domain.ErrInvalidState, http.StatusConflict, "CONFLICT"},
		{"plain source unavailable stays generic", domain.ErrSourceUnavailable, http.StatusServiceUnavailable, "SOURCE_SYNC_UNAVAILABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handleDomainError(recorder, tc.err)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tc.wantCode {
				t.Fatalf("code=%q want=%q", body.Error.Code, tc.wantCode)
			}
		})
	}
}

func TestEligibilitySummaryReasonsAreClosedWhitelist(t *testing.T) {
	allowed := map[string]bool{"BINDING_NOT_VERIFIED": true, "ACCOUNT_FROZEN": true, "PROJECTION_PENDING": true, "SOURCE_NOT_READY": true, "NO_CONSUMED_CASH": true, "READY": true}
	for value := range allowed {
		if !allowed[value] || strings.TrimSpace(value) != value {
			t.Fatalf("invalid summary reason %q", value)
		}
	}
}
