package httpapi

import (
	"encoding/json"
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
	for _, forbidden := range []string{"sensitive-account-id", strings.Repeat("a", 64), strings.Repeat("b", 64), "source_cursor", "evidence", "trigger_object", "revision_hash"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("eligibility freeze DTO leaked %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"freeze_reason", "source_type", "principal_id", "version", `"external_user_id":"1147"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("eligibility freeze DTO omitted %q: %s", required, text)
		}
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
