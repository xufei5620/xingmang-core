package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Same discipline as sub2api_test.go: every input here is a recorded,
// synthetic fixture under testdata/newapi/ - no network, no real data.

func TestParseNewAPIStatusFromFixture(t *testing.T) {
	body := readTestdata(t, "newapi", "status.sample.json")
	status, err := parseNewAPIStatus(body)
	if err != nil {
		t.Fatalf("parseNewAPIStatus: %v", err)
	}
	if status.Version != "v0.9.12" {
		t.Errorf("version = %q, want v0.9.12", status.Version)
	}
	if status.QuotaPerUnit != 500000 {
		t.Errorf("quota_per_unit = %d, want 500000", status.QuotaPerUnit)
	}
}

func TestParseNewAPIStatusRejectsSuccessFalse(t *testing.T) {
	body := []byte(`{"success":false,"message":"denied","data":null}`)
	if _, err := parseNewAPIStatus(body); err == nil {
		t.Fatal("parseNewAPIStatus accepted success:false (HTTP 200 business failure)")
	}
}

func TestParseNewAPIStatusRejectsZeroQuotaPerUnit(t *testing.T) {
	// A zero/negative quota_per_unit is a documented upstream failure mode
	// (connectors/newapi/upstream.go quotaPerUnit: the option write path can
	// drop a bad ParseFloat and leave this at 0) - evidence capture must not
	// silently report a nonsense conversion basis.
	body := []byte(`{"success":true,"message":"","data":{"version":"v1","quota_per_unit":0}}`)
	if _, err := parseNewAPIStatus(body); err == nil {
		t.Fatal("parseNewAPIStatus accepted quota_per_unit=0")
	}
}

func TestRedactNewAPIUsersPageFromFixture(t *testing.T) {
	body := readTestdata(t, "newapi", "users_page.sample.json")
	salt := []byte("newapi-fixture-salt1")

	envelope, detail, kept, dropped, err := redactNewAPIUsersPage(body, salt)
	if err != nil {
		t.Fatalf("redactNewAPIUsersPage: %v", err)
	}

	envText := string(envelope)
	for _, mustNotContain := range []string{
		"20031", "20032",
		"test-user-a@example.invalid", "test-user-b@example.invalid",
		"test-user-a", "test-user-b", "Test User A",
		"should-be-dropped", "internal note",
	} {
		if strings.Contains(envText, mustNotContain) {
			t.Errorf("redacted envelope leaked raw value %q:\n%s", mustNotContain, envText)
		}
	}

	var check struct {
		Success bool `json:"success"`
		Data    struct {
			Items []map[string]json.RawMessage `json:"items"`
			Total int                          `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(envelope, &check); err != nil {
		t.Fatalf("redacted envelope is not the expected {success,message,data:{items,total}} shape: %v", err)
	}
	if !check.Success || check.Data.Total != 2 || len(check.Data.Items) != 2 {
		t.Fatalf("redacted envelope lost success/items/total: %+v", check)
	}

	// The soft-delete marker on the second row must survive redaction as-is
	// (design doc explicitly wants "soft-delete 形态" evidence) - both rows
	// are kept, unlike the future real client which will skip soft-deleted
	// users entirely.
	secondRow := check.Data.Items[1]
	deletedAtRaw, ok := secondRow["DeletedAt"]
	if !ok {
		t.Fatal("redacted second item lost the DeletedAt field")
	}
	if string(deletedAtRaw) == "null" {
		t.Fatal("redacted second item's DeletedAt should carry the soft-delete timestamp, not null")
	}
	firstRow := check.Data.Items[0]
	if v, ok := firstRow["DeletedAt"]; !ok || string(v) != "null" {
		t.Fatalf("redacted first item's DeletedAt = %s, want null (not soft-deleted)", v)
	}

	wantKept := []string{"DeletedAt", "display_name", "email", "id", "last_login_at", "quota", "status", "username"}
	if strings.Join(kept, ",") != strings.Join(wantKept, ",") {
		t.Errorf("kept = %v, want %v", kept, wantKept)
	}
	wantDropped := []string{"github_id", "remark"}
	if strings.Join(dropped, ",") != strings.Join(wantDropped, ",") {
		t.Errorf("dropped = %v, want %v", dropped, wantDropped)
	}

	if detail == nil {
		t.Fatal("detail was nil")
	}
	if strings.Contains(string(detail), "20031") {
		t.Fatalf("detail leaked the raw id: %s", detail)
	}

	if violations := scanForbidden(envelope); len(violations) != 0 {
		t.Fatalf("scanForbidden flagged the redacted envelope: %v", violations)
	}
	if violations := scanForbidden(detail); len(violations) != 0 {
		t.Fatalf("scanForbidden flagged the redacted detail: %v", violations)
	}
}

func TestRedactNewAPIUsersPageEmptyDisplayNameStaysEmpty(t *testing.T) {
	body := readTestdata(t, "newapi", "users_page.sample.json")
	envelope, _, _, _, err := redactNewAPIUsersPage(body, []byte("salt"))
	if err != nil {
		t.Fatalf("redactNewAPIUsersPage: %v", err)
	}
	var check struct {
		Data struct {
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(envelope, &check); err != nil {
		t.Fatal(err)
	}
	if string(check.Data.Items[1]["display_name"]) != `""` {
		t.Fatalf("empty display_name should redact to an empty string, got %s", check.Data.Items[1]["display_name"])
	}
}
