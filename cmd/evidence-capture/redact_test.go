package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHashUserIDStableWithinSaltDistinctAcrossSalts(t *testing.T) {
	salt1 := []byte("salt-one-16bytes")
	salt2 := []byte("salt-two-16bytes")

	a := hashUserID(salt1, "u_10241")
	b := hashUserID(salt1, "u_10241")
	if a != b {
		t.Fatalf("hashUserID not stable within one salt: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "u_") {
		t.Fatalf("hashUserID must be prefixed u_, got %q", a)
	}
	if strings.Contains(a, "10241") {
		t.Fatalf("hashUserID leaked the raw id into its output: %q", a)
	}

	c := hashUserID(salt2, "u_10241")
	if a == c {
		t.Fatalf("hashUserID must differ across salts, got the same value %q for both", a)
	}
}

func TestHashUserIDEmptyIsEmpty(t *testing.T) {
	if got := hashUserID([]byte("salt"), ""); got != "" {
		t.Fatalf("hashUserID(\"\") = %q, want empty", got)
	}
}

func TestHashNameAndHashIDDoNotCollideOnEqualInput(t *testing.T) {
	salt := []byte("shared-salt-value")
	id := hashUserID(salt, "20031")
	name := hashName(salt, "20031")
	if id == name {
		t.Fatalf("hashUserID and hashName must not collide on equal raw input: id=%q name=%q", id, name)
	}
}

func TestScrubDigitsPreservesShape(t *testing.T) {
	cases := map[string]string{
		"1234.56000000": "0000.00000000",
		"-42":           "-00",
		"500000":        "000000",
		"":              "",
		"n/a":           "n/a",
	}
	for in, want := range cases {
		if got := scrubDigits(in); got != want {
			t.Errorf("scrubDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScrubBareJSONNumberProducesValidJSON(t *testing.T) {
	cases := []string{"0", "5", "500000", "-500000", "1234", "-7", "1798531200"}
	for _, in := range cases {
		out := scrubBareJSONNumber(in)
		var n json.Number
		if err := json.Unmarshal([]byte(out), &n); err != nil {
			t.Errorf("scrubBareJSONNumber(%q) = %q, not valid JSON: %v", in, out, err)
		}
		if len(out) != len(in) {
			t.Errorf("scrubBareJSONNumber(%q) = %q, digit count/shape not preserved (lengths %d vs %d)", in, out, len(out), len(in))
		}
	}
}

func TestRawJSONScalarAsString(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{`"u_10241"`, "u_10241", true},
		{`20031`, "20031", true},
		{`null`, "", false},
		{``, "", false},
		{`{"a":1}`, "", false},
		{`[1,2]`, "", false},
	}
	for _, c := range cases {
		got, ok := rawJSONScalarAsString(json.RawMessage(c.raw))
		if ok != c.ok || got != c.want {
			t.Errorf("rawJSONScalarAsString(%q) = (%q,%v), want (%q,%v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestRedactItemDropsFieldsNotInPolicy(t *testing.T) {
	salt := []byte("test-salt-value1")
	policy := ItemPolicy{
		"id":    redactIDField(),
		"email": redactEmailField(),
	}
	raw := json.RawMessage(`{"id":"u_1","email":"zhangwei@example.com","role":"superadmin","frozen_balance":"999.00"}`)

	redacted, kept, dropped, err := redactItem(raw, policy, salt)
	if err != nil {
		t.Fatalf("redactItem: %v", err)
	}
	if len(kept) != 2 || kept[0] != "email" || kept[1] != "id" {
		t.Fatalf("kept = %v, want [email id]", kept)
	}
	if len(dropped) != 2 || dropped[0] != "frozen_balance" || dropped[1] != "role" {
		t.Fatalf("dropped = %v, want [frozen_balance role]", dropped)
	}
	text := string(redacted)
	if strings.Contains(text, "superadmin") || strings.Contains(text, "999.00") {
		t.Fatalf("redacted item leaked a dropped field's value: %s", text)
	}
	if strings.Contains(text, "u_1\"") || strings.Contains(text, "zhangwei") {
		t.Fatalf("redacted item leaked raw id/email: %s", text)
	}
}

func TestRedactEmailFieldUsesPlatformusersMaskEmail(t *testing.T) {
	salt := []byte("unused-for-email-x")
	rule := redactEmailField()
	out, ok := rule(salt, json.RawMessage(`"zhangwei@example.com"`))
	if !ok {
		t.Fatal("redactEmailField refused a normal email")
	}
	var s string
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatalf("output not valid JSON string: %v", err)
	}
	if s != "zh***@example.com" {
		t.Fatalf("redactEmailField = %q, want zh***@example.com (platformusers.MaskEmail's own contract)", s)
	}
}

func TestRedactAmountFieldPreservesStringVsNumberShape(t *testing.T) {
	rule := redactAmountField()

	strOut, ok := rule(nil, json.RawMessage(`"1234.56000000"`))
	if !ok {
		t.Fatal("redactAmountField refused a decimal string")
	}
	if !strings.HasPrefix(string(strOut), `"`) {
		t.Fatalf("string-shaped amount must stay a JSON string, got %s", strOut)
	}
	var s string
	_ = json.Unmarshal(strOut, &s)
	if s != "0000.00000000" {
		t.Fatalf("redactAmountField(string) = %q, want 0000.00000000", s)
	}

	numOut, ok := rule(nil, json.RawMessage(`500000`))
	if !ok {
		t.Fatal("redactAmountField refused a bare number")
	}
	if strings.HasPrefix(string(numOut), `"`) {
		t.Fatalf("number-shaped amount must stay a bare JSON number, got %s", numOut)
	}
	// Not "000000": JSON forbids a leading zero on a multi-digit number, so
	// the leading digit becomes 1 - see scrubBareJSONNumber. This is itself
	// re-parsed to confirm the output is valid JSON, not just eyeballed.
	if string(numOut) != "100000" {
		t.Fatalf("redactAmountField(number) = %s, want 100000", numOut)
	}
	var reparsed json.Number
	if err := json.Unmarshal(numOut, &reparsed); err != nil {
		t.Fatalf("redactAmountField(number) produced invalid JSON %s: %v", numOut, err)
	}

	nullOut, ok := rule(nil, json.RawMessage(`null`))
	if !ok || string(nullOut) != "null" {
		t.Fatalf("redactAmountField(null) = (%s,%v), want (null,true) - null is itself evidence, not a value to scrub", nullOut, ok)
	}
}

func TestRedactNameFieldEmptyStaysEmptyString(t *testing.T) {
	rule := redactNameField()
	out, ok := rule([]byte("salt"), json.RawMessage(`""`))
	if !ok || string(out) != `""` {
		t.Fatalf("redactNameField(\"\") = (%s,%v), want (\"\",true)", out, ok)
	}
}

func TestScanForbiddenAllowsMaskedEmail(t *testing.T) {
	redacted := []byte(`{"email":"zh***@example.com","email2":"***@example.com"}`)
	if v := scanForbidden(redacted); len(v) != 0 {
		t.Fatalf("scanForbidden flagged a properly masked email: %v", v)
	}
}

func TestScanForbiddenCatchesUnmaskedEmail(t *testing.T) {
	leaked := []byte(`{"note":"contact zhangwei@example.com for access"}`)
	v := scanForbidden(leaked)
	if len(v) == 0 {
		t.Fatal("scanForbidden did not catch an unmasked email-shaped string")
	}
}

func TestScanForbiddenCatchesForbiddenKeywords(t *testing.T) {
	for _, word := range []string{"secret", "password", "api_key", "full_key", "bearer "} {
		body := []byte(`{"field":"x-` + word + `-y"}`)
		if v := scanForbidden(body); len(v) == 0 {
			t.Errorf("scanForbidden did not catch forbidden keyword %q", word)
		}
	}
}

func TestScanForbiddenCleanOnOrdinaryRedactedShape(t *testing.T) {
	clean := []byte(`{"id":"u_a1b2c3d4e5f6a7b8","email":"zh***@example.com","status":"active","balance":"0000.00000000"}`)
	if v := scanForbidden(clean); len(v) != 0 {
		t.Fatalf("scanForbidden flagged an ordinary redacted item: %v", v)
	}
}

func TestRedactItemsPageExtractsFirstItemAsDetail(t *testing.T) {
	salt := []byte("page-detail-salt1")
	policy := ItemPolicy{"id": redactIDField()}
	items := []json.RawMessage{
		json.RawMessage(`{"id":"u_1"}`),
		json.RawMessage(`{"id":"u_2"}`),
	}
	redactedItems, kept, dropped, detail, err := redactItemsPage(items, policy, salt)
	if err != nil {
		t.Fatalf("redactItemsPage: %v", err)
	}
	if len(redactedItems) != 2 {
		t.Fatalf("got %d redacted items, want 2", len(redactedItems))
	}
	if len(dropped) != 0 || len(kept) != 1 || kept[0] != "id" {
		t.Fatalf("kept=%v dropped=%v, want kept=[id] dropped=[]", kept, dropped)
	}
	if detail == nil {
		t.Fatal("detail was nil for a non-empty page")
	}
	wantFirst := hashUserID(salt, "u_1")
	if !strings.Contains(string(detail), wantFirst) {
		t.Fatalf("detail = %s, want it to be the first item (hash %s)", detail, wantFirst)
	}
}

func TestRedactItemsPageEmptyHasNilDetail(t *testing.T) {
	_, _, _, detail, err := redactItemsPage(nil, ItemPolicy{"id": redactIDField()}, []byte("salt"))
	if err != nil {
		t.Fatalf("redactItemsPage: %v", err)
	}
	if detail != nil {
		t.Fatalf("detail = %s, want nil for an empty page", detail)
	}
}
