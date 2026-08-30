package cpaboundary

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func digest(fill byte) string {
	return hex.EncodeToString(bytes.Repeat([]byte{fill}, sha256.Size))
}

func validRoute(plane, method, path, capability, risk string, public, mtls, inject bool) RouteRule {
	return RouteRule{
		Plane: plane, Method: method, Path: path, CapabilityID: capability, RiskClass: risk,
		MaxBodyBytes: 1 << 20, TimeoutMillis: 5000, RatePerMinute: 60,
		InjectsManagementKey: inject, RequiresMTLS: mtls, Public: public,
		ResponseProjection: "status,model,usage",
	}
}

func validBoundary() ManagementBoundary {
	return ManagementBoundary{
		Version: 1, CPAMinVersion: "1.0.0", CPAMaxVersion: "1.0.0", RouteInventorySHA256: digest('a'),
		Inference: []RouteRule{
			validRoute("inference", "POST", "/v1/chat/completions", "inference.chat", "inference", true, false, false),
			validRoute("inference", "GET", "/v1/models", "inference.models", "inference", true, false, false),
		},
		Callback: []RouteRule{
			validRoute("callback", "GET", "/v0/management/oauth-callback", "callback.oauth", "callback", true, false, false),
			validRoute("callback", "POST", "/v0/management/oauth-callback", "callback.oauth", "callback", true, false, false),
		},
		PrivateCapabilities: []RouteRule{
			validRoute("private", "GET", "/v0/management/health", "management.health", "private", false, true, true),
		},
		Denied: []RouteRule{
			validRoute("denied", "ANY", "/v0/management", "management.wildcard", "deny", false, false, false),
			validRoute("denied", "ANY", "/v0/management/config", "management.config", "deny", false, false, false),
			validRoute("denied", "ANY", "/v0/management/auth", "management.auth", "deny", false, false, false),
			validRoute("denied", "ANY", "/v0/management/api-call", "management.api-call", "deny", false, false, false),
			validRoute("denied", "ANY", "/v0/management/plugins", "management.plugin", "deny", false, false, false),
			validRoute("denied", "ANY", "/v0/management/usage-queue", "management.usage", "deny", false, false, false),
		},
		RequiredFacts: map[string]string{
			"allow_remote":                 "false",
			"management_password":          "absent",
			"raw_port_bind":                "loopback",
			"management_key_location":      "adapter_local",
			"inference_version_evidence":   "required",
			"route_inventory":              "exact",
			"public_callback_hostname":     "per_instance",
			"private_management_transport": "mtls",
		},
	}
}

func boundaryJSON(t *testing.T, b ManagementBoundary) []byte {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBoundaryFreezesOfficialManagementRiskFacts(t *testing.T) {
	b, hash, err := LoadBoundary(boundaryJSON(t, validBoundary()))
	if err != nil {
		t.Fatalf("valid boundary rejected: %v", err)
	}
	if hash == "" || len(hash) != sha256.Size*2 {
		t.Fatalf("canonical hash = %q", hash)
	}
	if b.RequiredFacts["management_password"] != "absent" || b.RequiredFacts["allow_remote"] != "false" {
		t.Fatalf("official risk facts not frozen: %#v", b.RequiredFacts)
	}
	mutated := validBoundary()
	mutated.CPAMinVersion = "1.x"
	if _, _, err := LoadBoundary(boundaryJSON(t, mutated)); err == nil {
		t.Fatal("version range must be rejected")
	}
	mutated = validBoundary()
	mutated.RouteInventorySHA256 = "not-a-digest"
	if _, _, err := LoadBoundary(boundaryJSON(t, mutated)); err == nil {
		t.Fatal("route inventory digest must be rejected")
	}
}

func TestBoundaryAllowsOnlyExactCallbackGETPOSTPublicly(t *testing.T) {
	b, _, err := LoadBoundary(boundaryJSON(t, validBoundary()))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Allows("callback", "GET", "/v0/management/oauth-callback") || !b.Allows("callback", "POST", "/v0/management/oauth-callback") {
		t.Fatal("exact callback GET/POST should be allowed")
	}
	for _, route := range []struct{ method, path string }{
		{"PUT", "/v0/management/oauth-callback"},
		{"GET", "/v0/management/oauth-callback/child"},
		{"GET", "/v0/management/oauth-callback?redirect_url=https://bad.invalid"},
	} {
		if b.Allows("callback", route.method, route.path) {
			t.Fatalf("callback variant allowed: %s %s", route.method, route.path)
		}
	}
}

func TestInferenceHostnameDeniesCallbackAndCallbackHostnameDeniesAllSiblings(t *testing.T) {
	b, _, err := LoadBoundary(boundaryJSON(t, validBoundary()))
	if err != nil {
		t.Fatal(err)
	}
	if b.Allows("inference", "GET", "/v0/management/oauth-callback") {
		t.Fatal("inference plane must deny callback")
	}
	if b.Allows("callback", "GET", "/v0/management/config") {
		t.Fatal("callback plane must deny management sibling")
	}
}

func TestBoundaryDeniesWildcardConfigAuthAPICallPluginUsageAndWrites(t *testing.T) {
	b := validBoundary()
	for _, capability := range []string{"management.config", "management.auth", "management.api-call", "management.plugin", "management.usage"} {
		found := false
		for _, route := range b.Denied {
			if route.CapabilityID == capability {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing deny-list capability %q", capability)
		}
	}
	b.PrivateCapabilities = append(b.PrivateCapabilities, validRoute("private", "POST", "/v0/management/config", "management.config.write", "write", false, true, true))
	if err := b.Validate(); err == nil {
		t.Fatal("write/config capability must be denied")
	}
}

func TestBoundaryRejectsOverlappingEncodedWildcardAndGenericProxyRoutes(t *testing.T) {
	for name, mutate := range map[string]func(*ManagementBoundary){
		"overlap": func(b *ManagementBoundary) {
			b.Inference = append(b.Inference, validRoute("inference", "GET", "/v1/models", "other", "inference", true, false, false))
		},
		"encoded": func(b *ManagementBoundary) {
			b.Inference[0].Path = "/v1/%6dodels"
		},
		"wildcard": func(b *ManagementBoundary) {
			b.Inference[0].Path = "/v1/*"
		},
		"proxy": func(b *ManagementBoundary) {
			b.PrivateCapabilities[0].CapabilityID = "generic.proxy"
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := validBoundary()
			mutate(&b)
			if _, _, err := LoadBoundary(boundaryJSON(t, b)); err == nil {
				t.Fatalf("%s variant accepted", name)
			}
		})
	}
}

func TestBoundaryRequiresAllowRemoteFalseAndManagementPasswordAbsent(t *testing.T) {
	for key, value := range map[string]string{"allow_remote": "true", "management_password": "present"} {
		b := validBoundary()
		b.RequiredFacts[key] = value
		if _, _, err := LoadBoundary(boundaryJSON(t, b)); err == nil {
			t.Fatalf("unsafe %s fact accepted", key)
		}
	}
	b := validBoundary()
	delete(b.RequiredFacts, "management_password")
	if _, _, err := LoadBoundary(boundaryJSON(t, b)); err == nil {
		t.Fatal("missing management password fact accepted")
	}
}

func TestCapabilityDependenciesRequireFoundationBR210R214R215(t *testing.T) {
	b := validBoundary()
	usage := validRoute("private", "GET", "/v0/management/usage", "usage.queue.pop", "destructive", false, true, true)
	usage.RequiresFoundationB = true
	usage.RequiresR210 = true
	usage.RequiresR215 = true
	b.PrivateCapabilities = append(b.PrivateCapabilities, usage)
	if err := b.Validate(); err == nil {
		t.Fatal("usage capability must remain denied without explicit approved dependency facts")
	}
	plugin := validRoute("private", "GET", "/v0/management/plugin", "plugin.inspect", "plugin", false, true, true)
	plugin.RequiresFoundationB = true
	plugin.RequiresR214 = true
	b.PrivateCapabilities = []RouteRule{plugin}
	if err := b.Validate(); err == nil {
		t.Fatal("plugin capability must remain denied in R213-1")
	}
}

func TestResponseProjectionRejectsCredentialConfigAuthAndHeaderKeys(t *testing.T) {
	for _, projection := range []string{"authorization", "cookie", "api_key", "config", "auth_file", "headers"} {
		b := validBoundary()
		b.PrivateCapabilities[0].ResponseProjection = projection
		if _, _, err := LoadBoundary(boundaryJSON(t, b)); err == nil {
			t.Fatalf("forbidden response projection %q accepted", projection)
		}
	}
	b, _, err := LoadBoundary(boundaryJSON(t, validBoundary()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProjectResponse("management.health", []byte(`{"status":"ok","authorization":"blocked"}`)); err == nil {
		t.Fatal("sensitive response key was projected")
	}
}

func TestBoundaryRejectsDestructiveAndQueueFlagsEvenWithDependencyFacts(t *testing.T) {
	b := validBoundary()
	b.RequiredFacts["foundation_b"] = "approved"
	b.RequiredFacts["r210"] = "approved"
	b.RequiredFacts["r215"] = "approved"
	r := validRoute("private", "GET", "/v0/management/telemetry", "telemetry.read", "private", false, true, true)
	r.Destructive = true
	b.PrivateCapabilities = append(b.PrivateCapabilities, r)
	if err := b.Validate(); err == nil {
		t.Fatal("destructive capability accepted")
	}
	b = validBoundary()
	r = validRoute("private", "GET", "/v0/management/telemetry", "telemetry.read", "private", false, true, true)
	r.ConsumesQueue = true
	r.RequiresFoundationB, r.RequiresR210, r.RequiresR215 = true, true, true
	b.PrivateCapabilities = append(b.PrivateCapabilities, r)
	if err := b.Validate(); err == nil {
		t.Fatal("queue capability accepted")
	}
}

func TestBoundaryDeniesANYAndEncodedVariants(t *testing.T) {
	b, _, err := LoadBoundary(boundaryJSON(t, validBoundary()))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"ANY", "any", "CONNECT"} {
		if b.Allows("callback", method, "/v0/management/oauth-callback") {
			t.Fatalf("unsafe callback method allowed: %s", method)
		}
	}
	if b.Allows("inference", "GET", "/v0/%6dmanagement") {
		t.Fatal("encoded management path allowed")
	}
}

func TestBoundaryRejectsUnknownAndDuplicateJSONFields(t *testing.T) {
	raw := boundaryJSON(t, validBoundary())
	raw = append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"unexpected":true}`)...)
	if _, _, err := LoadBoundary(raw); err == nil {
		t.Fatal("unknown field accepted")
	}
	duplicate := strings.Replace(string(boundaryJSON(t, validBoundary())), `"version":1`, `"version":1,"version":1`, 1)
	if _, _, err := LoadBoundary([]byte(duplicate)); err == nil {
		t.Fatal("duplicate field accepted")
	}
}

func TestBoundaryCanonicalDigestIsStableAcrossRouteOrder(t *testing.T) {
	a := validBoundary()
	b := validBoundary()
	b.Inference[0], b.Inference[1] = b.Inference[1], b.Inference[0]
	b.Denied[0], b.Denied[len(b.Denied)-1] = b.Denied[len(b.Denied)-1], b.Denied[0]
	_, hashA, err := LoadBoundary(boundaryJSON(t, a))
	if err != nil {
		t.Fatal(err)
	}
	_, hashB, err := LoadBoundary(boundaryJSON(t, b))
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Fatalf("route ordering changed canonical hash: %s != %s", hashA, hashB)
	}
}

func TestBoundaryZeroInventoryDigestRequiresExplicitPlaceholder(t *testing.T) {
	b := validBoundary()
	b.RouteInventorySHA256 = strings.Repeat("0", 64)
	if _, _, err := LoadBoundary(boundaryJSON(t, b)); err == nil {
		t.Fatal("zero route inventory digest accepted without placeholder")
	}
	b.RouteInventoryPlaceholder = true
	loaded, _, err := LoadBoundary(boundaryJSON(t, b))
	if err != nil || !loaded.RouteInventoryPlaceholder {
		t.Fatalf("explicit placeholder rejected: %+v %v", loaded, err)
	}
}
