package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests never touch the network - every input is a recorded fixture
// under testdata/sub2api/, synthetic (example.invalid emails, no real user
// data), modeled on the field shapes connectors/platformusers/upstream.go
// already verified against source.

func readTestdata(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, parts...)...))
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	return b
}

func TestParseSub2APIVersionFromFixture(t *testing.T) {
	body := readTestdata(t, "sub2api", "version.sample.json")
	v, err := parseSub2APIVersion(body)
	if err != nil {
		t.Fatalf("parseSub2APIVersion: %v", err)
	}
	if v != "0.1.183" {
		t.Fatalf("version = %q, want 0.1.183", v)
	}
}

func TestParseSub2APIVersionRejectsBusinessError(t *testing.T) {
	body := []byte(`{"code":1,"message":"unauthorized","data":null}`)
	if _, err := parseSub2APIVersion(body); err == nil {
		t.Fatal("parseSub2APIVersion accepted a non-zero business code")
	}
}

func TestRedactSub2APIUsersPageFromFixture(t *testing.T) {
	body := readTestdata(t, "sub2api", "users_page.sample.json")
	salt := []byte("sub2api-fixture-salt")

	envelope, detail, kept, dropped, err := redactSub2APIUsersPage(body, salt)
	if err != nil {
		t.Fatalf("redactSub2APIUsersPage: %v", err)
	}

	envText := string(envelope)
	for _, mustNotContain := range []string{
		"u_10241", "u_10242",
		"test-user-one@example.invalid", "test-user-two@example.invalid",
		"test-user-one", "test-user-two",
		"1234.56000000", "-5.00000000",
		"superadmin", "999.00000000",
	} {
		if strings.Contains(envText, mustNotContain) {
			t.Errorf("redacted envelope leaked raw value %q:\n%s", mustNotContain, envText)
		}
	}
	// The envelope shape itself (code/message/data/items/total) must survive -
	// this file is meant to be dropped straight into
	// connectors/platformusers/testdata/sub2api/users_page.redacted.json.
	var check struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Items []map[string]json.RawMessage `json:"items"`
			Total int                          `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(envelope, &check); err != nil {
		t.Fatalf("redacted envelope is not the expected {code,message,data:{items,total}} shape: %v", err)
	}
	if check.Data.Total != 2 || len(check.Data.Items) != 2 {
		t.Fatalf("redacted envelope lost items/total: %+v", check.Data)
	}

	wantKept := []string{"balance", "email", "id", "last_active_at", "status", "username"}
	if strings.Join(kept, ",") != strings.Join(wantKept, ",") {
		t.Errorf("kept = %v, want %v", kept, wantKept)
	}
	wantDropped := []string{"frozen_balance", "role"}
	if strings.Join(dropped, ",") != strings.Join(wantDropped, ",") {
		t.Errorf("dropped = %v, want %v", dropped, wantDropped)
	}

	if detail == nil {
		t.Fatal("detail was nil")
	}
	detailText := string(detail)
	if strings.Contains(detailText, "u_10241") {
		t.Fatalf("detail leaked the raw id: %s", detailText)
	}
	if !strings.Contains(detailText, hashUserID(salt, "u_10241")) {
		t.Fatalf("detail = %s, want it derived from the first item (u_10241)", detailText)
	}

	// The final safety net must also see this as clean, the same gate
	// capture.go runs before ever writing a file.
	if violations := scanForbidden(envelope); len(violations) != 0 {
		t.Fatalf("scanForbidden flagged the redacted envelope: %v", violations)
	}
	if violations := scanForbidden(detail); len(violations) != 0 {
		t.Fatalf("scanForbidden flagged the redacted detail: %v", violations)
	}
}

func TestRedactSub2APIUsersPageStableIDAcrossListAndDetail(t *testing.T) {
	body := readTestdata(t, "sub2api", "users_page.sample.json")
	salt := []byte("stability-check-salt")

	envelope, detail, _, _, err := redactSub2APIUsersPage(body, salt)
	if err != nil {
		t.Fatalf("redactSub2APIUsersPage: %v", err)
	}
	hashedID := hashUserID(salt, "u_10241")
	if !strings.Contains(string(envelope), hashedID) {
		t.Fatalf("envelope does not contain the expected pseudonym %s", hashedID)
	}
	if !strings.Contains(string(detail), hashedID) {
		t.Fatalf("detail does not contain the same pseudonym %s as the envelope - a reviewer must be able to confirm these are the same row", hashedID)
	}
}
