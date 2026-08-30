package ops

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func policyFixtureBytes(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "contracts", "ops", "metric-rollup-policy.v1.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read policy fixture: %v", err)
	}
	return b
}

func TestPolicyCoversExactlyRegisteredMetrics(t *testing.T) {
	policies, _, err := LoadRollupPolicies(policyFixtureBytes(t))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	excluded := ExcludedRollupMetricKeys()
	got := make([]string, 0, len(policies)+len(excluded))
	for key := range policies {
		got = append(got, key)
	}
	got = append(got, excluded...)
	sort.Strings(got)
	want := RegisteredMetricKeys()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("policy + explicit exclusions = %v, registry = %v", got, want)
	}
	if len(policies) != 22 || len(excluded) != 10 {
		t.Fatalf("active policy/exclusion counts = %d/%d, want 22/10", len(policies), len(excluded))
	}
}

func TestPolicyFreezesFifteenMetricKindsAndPointers(t *testing.T) {
	policies, _, err := LoadRollupPolicies(policyFixtureBytes(t))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	cases := map[string]struct {
		valueKind   ValueKind
		primaryKind PrimaryKind
		pointer     string
		currency    string
	}{
		"sub2api.users.total":       {ValueGauge, PrimaryCount, "/total_users", ""},
		"sub2api.users.balance":     {ValueGauge, PrimaryMoneyMinor, "/balance_minor_units", "/currency"},
		"sub2api.revenue.daily":     {ValueDailySnapshot, PrimaryMoneyMinor, "/amount_minor_units", "/currency"},
		"sub2api.cost.daily":        {ValueDailySnapshot, PrimaryMoneyMinor, "/amount_minor_units", "/currency"},
		"sub2api.channels.balance":  {ValueDocumentStatus, PrimaryCount, "/channel_count", ""},
		"sub2api.channels.status":   {ValueDocumentStatus, PrimaryCount, "/channel_count", ""},
		"newapi.users.total":        {ValueGauge, PrimaryCount, "/total_users", ""},
		"newapi.recharge.daily":     {ValueDailySnapshot, PrimaryMoneyMinor, "/amount_minor_units", "/currency"},
		"newapi.subscription.daily": {ValueDailySnapshot, PrimaryMoneyMinor, "/amount_minor_units", "/currency"},
		"newapi.channels.status":    {ValueDocumentStatus, PrimaryCount, "/channel_count", ""},
		"newapi.models.usage":       {ValueDailySnapshot, PrimaryCount, "/total_request_count", ""},
		"finance.cost.daily":        {ValueDailySnapshot, PrimaryMoneyMinor, "/total_cost_minor_units", "/currency"},
		"finance.revenue.daily":     {ValueDailySnapshot, PrimaryMoneyMinor, "/total_revenue_minor_units", "/currency"},
		"finance.profit.daily":      {ValueDocumentStatus, PrimaryMoneyMinor, "/profit_minor_units", "/currency"},
		"cpa.requests.daily":        {ValueDailySnapshot, PrimaryCount, "/total_request_count", ""},
		"cpa.cost.daily":            {ValueDailySnapshot, PrimaryMoneyMinor, "/total_cost_minor_units", "/currency"},
		"cpa.keys.usage":            {ValueDocumentStatus, PrimaryCount, "/key_count", ""},
		"cpa.accounts.health":       {ValueDocumentStatus, PrimaryCount, "/account_count", ""},
	}
	for key, want := range cases {
		got, ok := policies[key]
		if !ok {
			t.Fatalf("missing active policy for %s", key)
		}
		if got.Version != 1 || got.MetricKey != key || got.ValueKind != want.valueKind || got.PrimaryKind != want.primaryKind || got.PrimaryJSONPointer != want.pointer || got.CurrencyJSONPointer != want.currency {
			t.Errorf("%s policy = %+v, want kind/primary/pointer %s/%s/%s/%s", key, got, want.valueKind, want.primaryKind, want.pointer, want.currency)
		}
		if got.BucketTimezone != "UTC" || got.Scale <= 0 || got.SumMode != "forbidden" {
			t.Errorf("%s has unsafe bucket/scale/sum: %+v", key, got)
		}
		if !got.FullValueRequired && key != "finance.profit.daily" {
			t.Errorf("%s must require full value", key)
		}
	}
}

func TestInvoicePoliciesRequireFrozenCR0002(t *testing.T) {
	policies, _, err := LoadRollupPolicies(policyFixtureBytes(t))
	if err != nil {
		t.Fatalf("load gated policy: %v", err)
	}
	for _, key := range []string{"invoice.requests.daily", "invoice.amount.daily"} {
		if _, ok := policies[key]; ok {
			t.Fatalf("invoice key %q must not produce v1 policy while CR-0002 is pending", key)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(policyFixtureBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	entries, ok := doc["policies"].([]any)
	if !ok {
		t.Fatal("fixture policies is not an array")
	}
	entries = append(entries, map[string]any{
		"policy_version": 1, "metric_key": "invoice.requests.daily", "value_kind": "daily_snapshot",
		"primary_kind": "count", "primary_json_pointer": "/count", "currency_json_pointer": "",
		"business_day_json_pointer": "/day", "unit": "count", "scale": 1, "sum_mode": "forbidden",
		"bucket_timezone": "UTC", "expected_interval_seconds": nil, "full_value_required": true,
		"partial_value_allowed": true,
	})
	doc["policies"] = entries
	mutated, _ := json.Marshal(doc)
	if _, _, err := LoadRollupPolicies(mutated); err == nil || !strings.Contains(strings.ToLower(err.Error()), "cr-0002") {
		t.Fatalf("invoice policy must fail closed with CR-0002 reason, got %v", err)
	}
}

func TestPolicyForbidsSumForAllV1Snapshots(t *testing.T) {
	mutated := bytes.Replace(policyFixtureBytes(t), []byte(`"sum_mode": "forbidden"`), []byte(`"sum_mode": "additive"`), 1)
	if _, _, err := LoadRollupPolicies(mutated); err == nil {
		t.Fatal("snapshot additive sum must be rejected")
	}
}

func TestPolicyRequiresUTCAndPositiveIntegerScale(t *testing.T) {
	for _, replacement := range []struct{ from, to string }{
		{`"bucket_timezone": "UTC"`, `"bucket_timezone": "Asia/Shanghai"`},
		{`"scale": 1`, `"scale": 0`},
	} {
		mutated := bytes.Replace(policyFixtureBytes(t), []byte(replacement.from), []byte(replacement.to), 1)
		if _, _, err := LoadRollupPolicies(mutated); err == nil {
			t.Fatalf("mutation %q -> %q must fail", replacement.from, replacement.to)
		}
	}
}

func TestPolicyRejectsUnknownKeyVersionKindPointerAndFloatScale(t *testing.T) {
	mutations := []struct {
		name string
		fn   func(map[string]any)
	}{
		{"unknown key", func(doc map[string]any) {
			items := doc["policies"].([]any)
			items[0].(map[string]any)["metric_key"] = "unknown.metric"
		}},
		{"version", func(doc map[string]any) { doc["policy_version"] = 2 }},
		{"kind", func(doc map[string]any) { doc["policies"].([]any)[0].(map[string]any)["value_kind"] = "wat" }},
		{"pointer", func(doc map[string]any) {
			doc["policies"].([]any)[0].(map[string]any)["primary_json_pointer"] = "not-a-pointer"
		}},
		{"float scale", func(doc map[string]any) { doc["policies"].([]any)[0].(map[string]any)["scale"] = 1.5 }},
	}
	for _, mutation := range mutations {
		var doc map[string]any
		if err := json.Unmarshal(policyFixtureBytes(t), &doc); err != nil {
			t.Fatal(err)
		}
		mutation.fn(doc)
		b, _ := json.Marshal(doc)
		if _, _, err := LoadRollupPolicies(b); err == nil {
			t.Errorf("%s mutation unexpectedly accepted", mutation.name)
		}
	}
}

func TestMoneyPolicyRequiresCurrencyPointer(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(policyFixtureBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	for _, item := range doc["policies"].([]any) {
		p := item.(map[string]any)
		if p["primary_kind"] == "money_minor" {
			p["currency_json_pointer"] = ""
			break
		}
	}
	b, _ := json.Marshal(doc)
	if _, _, err := LoadRollupPolicies(b); err == nil {
		t.Fatal("money policy without currency pointer must fail")
	}
}

func TestPolicyRequiresFullValueAndPartialValueFlags(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(policyFixtureBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	p := doc["policies"].([]any)[0].(map[string]any)
	delete(p, "full_value_required")
	b, _ := json.Marshal(doc)
	if _, _, err := LoadRollupPolicies(b); err == nil {
		t.Fatal("missing full_value_required must fail")
	}
	if err := json.Unmarshal(policyFixtureBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	p = doc["policies"].([]any)[0].(map[string]any)
	delete(p, "partial_value_allowed")
	b, _ = json.Marshal(doc)
	if _, _, err := LoadRollupPolicies(b); err == nil {
		t.Fatal("missing partial_value_allowed must fail")
	}
}
