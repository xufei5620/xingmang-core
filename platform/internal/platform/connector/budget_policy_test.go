package connector

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBudgetPolicyCoversEveryRegisteredRouteExactlyOnce(t *testing.T) {
	policy := DefaultPolicyV1()
	if err := policy.Validate(); err != nil {
		t.Fatalf("default policy must validate: %v", err)
	}
	seen := map[string]string{}
	for _, cap := range policy.Capabilities {
		for _, route := range cap.Routes {
			if previous, ok := seen[route.RouteID]; ok {
				t.Fatalf("route %q appears in %q and %q", route.RouteID, previous, cap.Capability)
			}
			seen[route.RouteID] = cap.Capability
		}
	}
	for _, route := range RegisteredRouteSpecs() {
		if _, ok := seen[route.RouteID]; !ok {
			t.Fatalf("registered route %q has no budget", route.RouteID)
		}
	}
}

func TestCurrentSub2APINewAPIMeteringCapabilitiesHaveFiniteRunCaps(t *testing.T) {
	policy := DefaultPolicyV1()
	for _, cap := range policy.Capabilities {
		if !strings.HasPrefix(cap.ConnectorType, "sub2api") &&
			!strings.HasPrefix(cap.ConnectorType, "newapi") &&
			!strings.HasPrefix(cap.ConnectorType, "metering") {
			continue
		}
		if cap.MaxRequests <= 0 || cap.MaxPages <= 0 || cap.MaxRows <= 0 ||
			cap.MaxBytes <= 0 || cap.MaxCostUnits <= 0 || cap.MaxRunMillis <= 0 {
			t.Errorf("%s has an unbounded/zero run cap: %+v", cap.Capability, cap)
		}
	}
}

func TestV1BaseEqualsMinAndNeverAcceleratesCurrentCadence(t *testing.T) {
	for _, cap := range DefaultPolicyV1().Capabilities {
		if cap.Poll.MinIntervalSeconds != cap.Poll.BaseIntervalSeconds {
			t.Errorf("%s min interval %d != base %d", cap.Capability, cap.Poll.MinIntervalSeconds, cap.Poll.BaseIntervalSeconds)
		}
		if cap.Poll.BaseIntervalSeconds < 300 {
			t.Errorf("%s base interval %d is faster than current 300s cadence", cap.Capability, cap.Poll.BaseIntervalSeconds)
		}
	}
}

func TestCountHeavyAndPaginationRoutesHaveExplicitCost(t *testing.T) {
	for _, cap := range DefaultPolicyV1().Capabilities {
		for _, route := range cap.Routes {
			if route.CostUnits <= 0 || route.MaxResponseBytes <= 0 || route.MaxRows <= 0 {
				t.Errorf("%s/%s has incomplete route budget: %+v", cap.Capability, route.RouteID, route)
			}
			if cap.CostClass == CostClassCountHeavy && route.CostUnits < 2 {
				t.Errorf("count-heavy route %s must cost at least two units", route.RouteID)
			}
		}
	}
}

func TestUnknownRouteVersionProviderScopeAndWriteCapabilityFailClosed(t *testing.T) {
	policy := DefaultPolicyV1()
	bad := policy
	bad.Capabilities = append([]CapabilityBudget(nil), policy.Capabilities...)
	bad.Capabilities[0].Routes = append([]RouteCost(nil), bad.Capabilities[0].Routes...)
	bad.Capabilities[0].Routes[0].RouteID = "route-unknown"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown route must fail closed")
	}

	bad = policy
	bad.Capabilities = append([]CapabilityBudget(nil), policy.Capabilities...)
	bad.Capabilities[0].ConnectorType = "unknown-connector"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown connector/version must fail closed")
	}

	bad = policy
	bad.Capabilities = append([]CapabilityBudget(nil), policy.Capabilities...)
	bad.Capabilities[0].ProviderScopeRule = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("empty provider scope rule must fail closed")
	}

	bad = policy
	bad.Capabilities = append([]CapabilityBudget(nil), policy.Capabilities...)
	bad.Capabilities[0].CostClass = CostClassWrite
	if err := bad.Validate(); err == nil {
		t.Fatal("write capability must fail closed in v1")
	}
}

func TestFutureCPAUsageIsDestructiveAndDisabled(t *testing.T) {
	for _, cap := range DefaultPolicyV1().Capabilities {
		if strings.Contains(strings.ToLower(cap.Capability), "cpa") {
			t.Fatalf("future CPA capability must not be enabled in R215-1: %s", cap.Capability)
		}
	}
}

func TestLoadPolicyRejectsUnknownFieldsAndOverflow(t *testing.T) {
	valid := `{"policy_version":1,"capabilities":[]}`
	if _, err := LoadPolicy(strings.NewReader(valid)); err == nil {
		t.Fatal("empty policy should fail validation")
	}
	// The unknown field must be the only defect. An empty capabilities list
	// is already invalid and cannot prove that strict decoding is enabled.
	data, err := os.ReadFile("../../../contracts/connectors/budgets.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyBytes(data); err != nil {
		t.Fatalf("unknown-field control must be valid: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpected"] = json.RawMessage("true")
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyBytes(unknown); err == nil || !strings.Contains(err.Error(), `unknown field "unexpected"`) {
		t.Fatalf("unknown fields must be rejected by strict decoding: %v", err)
	}
	overflow := `{"policy_version":1,"capabilities":[{"connector_type":"x","capability":"x.y","provider_scope_rule":"scope","cost_class":"probe","max_requests":9223372036854775808}]}`
	if _, err := LoadPolicy(strings.NewReader(overflow)); err == nil {
		t.Fatal("integer overflow must be rejected")
	}
}

func TestCheckedInBudgetArtifactLoadsWithStrictDecoder(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/connectors/budgets.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyBytes(data); err != nil {
		t.Fatalf("checked-in budget artifact: %v", err)
	}
}
