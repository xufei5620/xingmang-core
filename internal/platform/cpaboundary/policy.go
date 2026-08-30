// Package cpaboundary contains the immutable, offline-only boundary contract
// for a CPA instance.  It deliberately has no network, filesystem,
// credential, or process dependencies.  R213-1 can therefore be used by a
// review tool without accidentally becoming a management client.
package cpaboundary

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	BoundaryVersionV1 = 1
	// CallbackPath is the one unauthenticated callback exception.  The
	// hostname/plane remains part of the route identity; this path is never a
	// licence to expose the complete management prefix.
	CallbackPath     = "/v0/management/oauth-callback"
	ManagementPrefix = "/v0/management"
)

var (
	digestPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionPattern       = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	capabilityPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	projectionKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
)

// RouteRule is one exact route identity and its finite safety limits.  Path
// is an exact path, never a prefix/template/URL/query.  The dependency flags
// are explicit review facts; they are not inferred from caller identity.
type RouteRule struct {
	Plane                string `json:"plane"`
	Method               string `json:"method"`
	Path                 string `json:"path"`
	CapabilityID         string `json:"capability_id"`
	RiskClass            string `json:"risk_class"`
	MaxBodyBytes         int    `json:"max_body_bytes"`
	TimeoutMillis        int    `json:"timeout_millis"`
	RatePerMinute        int    `json:"rate_per_minute"`
	InjectsManagementKey bool   `json:"injects_management_key"`
	RequiresMTLS         bool   `json:"requires_mtls"`
	Public               bool   `json:"public"`
	ResponseProjection   string `json:"response_projection,omitempty"`
	RequiresFoundationB  bool   `json:"requires_foundation_b,omitempty"`
	RequiresR210         bool   `json:"requires_r210,omitempty"`
	RequiresR214         bool   `json:"requires_r214,omitempty"`
	RequiresR215         bool   `json:"requires_r215,omitempty"`
	Destructive          bool   `json:"destructive,omitempty"`
	ConsumesQueue        bool   `json:"consumes_queue,omitempty"`
}

// Validate checks a standalone route using its declared plane.
func (r RouteRule) Validate() error { return validateRoute(r, r.Plane, 0) }

// ManagementBoundary is the versioned CPA boundary contract.  Slice order is
// not semantically meaningful; LoadBoundary sorts each route set before
// calculating the canonical digest.
type ManagementBoundary struct {
	Schema               string `json:"$schema,omitempty"`
	ID                   string `json:"$id,omitempty"`
	Version              int    `json:"version"`
	CPAMinVersion        string `json:"cpa_min_version"`
	CPAMaxVersion        string `json:"cpa_max_version"`
	RouteInventorySHA256 string `json:"route_inventory_sha256"`
	// RouteInventoryPlaceholder is explicit metadata for the checked-in
	// sample. It can never be mistaken for target evidence; AuditOffline marks
	// such a boundary partial until a real digest is supplied.
	RouteInventoryPlaceholder bool              `json:"route_inventory_placeholder,omitempty"`
	Inference                 []RouteRule       `json:"inference"`
	Callback                  []RouteRule       `json:"callback"`
	PrivateCapabilities       []RouteRule       `json:"private_capabilities"`
	Denied                    []RouteRule       `json:"denied"`
	RequiredFacts             map[string]string `json:"required_facts"`
}

// ManagementBoundaryV1 is the explicit versioned name used by contract
// consumers; it aliases ManagementBoundary so callers cannot accidentally
// mix wire versions.
type ManagementBoundaryV1 = ManagementBoundary

// DefaultBoundaryV1 returns the checked-in, deny-by-default policy used by
// the offline examples.  Inference paths are concrete examples from the
// approved v1 route inventory; callers must replace the inventory digest and
// route set when targeting another CPA build.
func DefaultBoundaryV1() ManagementBoundary {
	route := func(plane, method, path, capability, risk string, public, mtls, inject bool) RouteRule {
		return RouteRule{
			Plane: plane, Method: method, Path: path, CapabilityID: capability,
			RiskClass: risk, MaxBodyBytes: 1 << 20, TimeoutMillis: 5000,
			RatePerMinute: 60, Public: public, RequiresMTLS: mtls,
			InjectsManagementKey: inject, ResponseProjection: "status,model,usage",
		}
	}
	return ManagementBoundary{
		Schema:  "https://json-schema.org/draft/2020-12/schema",
		ID:      "https://xingmang.local/contracts/cpa/management-boundary.v1",
		Version: BoundaryVersionV1, CPAMinVersion: "1.0.0", CPAMaxVersion: "1.0.0",
		RouteInventorySHA256: strings.Repeat("0", sha256.Size*2), RouteInventoryPlaceholder: true,
		Inference: []RouteRule{
			route("inference", "GET", "/v1/models", "inference.models", "inference", true, false, false),
			route("inference", "POST", "/v1/chat/completions", "inference.chat", "inference", true, false, false),
		},
		Callback: []RouteRule{
			route("callback", "GET", CallbackPath, "callback.oauth", "callback", true, false, false),
			route("callback", "POST", CallbackPath, "callback.oauth", "callback", true, false, false),
		},
		PrivateCapabilities: []RouteRule{
			route("private", "GET", "/v0/management/health", "management.health", "private", false, true, true),
		},
		Denied: []RouteRule{
			route("denied", "ANY", ManagementPrefix, "management.wildcard", "deny", false, false, false),
			route("denied", "ANY", ManagementPrefix+"/config", "management.config", "config", false, false, false),
			route("denied", "ANY", ManagementPrefix+"/auth", "management.auth", "auth", false, false, false),
			route("denied", "ANY", ManagementPrefix+"/api-call", "management.api-call", "api-call", false, false, false),
			route("denied", "ANY", ManagementPrefix+"/plugins", "management.plugin", "plugin", false, false, false),
			route("denied", "ANY", ManagementPrefix+"/usage-queue", "management.usage", "usage", false, false, false),
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

// LoadBoundary strictly decodes one local contract and returns the validated
// value plus the lowercase SHA-256 of canonical JSON bytes.  Unknown and
// duplicate fields are rejected; formatting and route order do not affect the
// digest.
func LoadBoundary(data []byte) (ManagementBoundary, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return ManagementBoundary{}, "", errors.New("cpa boundary: empty document")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return ManagementBoundary{}, "", fmt.Errorf("cpa boundary: %w", err)
	}
	for _, field := range []string{"version", "cpa_min_version", "cpa_max_version", "route_inventory_sha256", "inference", "callback", "private_capabilities", "denied", "required_facts"} {
		if !topLevelFieldPresent(data, field) {
			return ManagementBoundary{}, "", fmt.Errorf("cpa boundary: missing %s", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var boundary ManagementBoundary
	if err := dec.Decode(&boundary); err != nil {
		return ManagementBoundary{}, "", fmt.Errorf("cpa boundary decode: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ManagementBoundary{}, "", errors.New("cpa boundary has trailing JSON")
		}
		return ManagementBoundary{}, "", fmt.Errorf("cpa boundary trailing data: %w", err)
	}
	if err := boundary.Validate(); err != nil {
		return ManagementBoundary{}, "", err
	}
	normalizeBoundary(&boundary)
	canonical, err := json.Marshal(boundary)
	if err != nil {
		return ManagementBoundary{}, "", fmt.Errorf("cpa boundary canonical encoding: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return boundary, hex.EncodeToString(sum[:]), nil
}

// LoadBoundaryBytes is a descriptive alias for callers that prefer a bytes
// suffix in their API names.
func LoadBoundaryBytes(data []byte) (ManagementBoundary, string, error) {
	return LoadBoundary(data)
}

// CanonicalHash returns the digest of a validated, normalized boundary.
func CanonicalHash(b ManagementBoundary) (string, error) {
	_, hash, err := b.Canonical()
	return hash, err
}

// Canonical returns normalized JSON bytes and its digest.  It is useful for
// evidence tooling and does not perform any I/O.
func (b ManagementBoundary) Canonical() ([]byte, string, error) {
	if err := b.Validate(); err != nil {
		return nil, "", err
	}
	normalizeBoundary(&b)
	data, err := json.Marshal(b)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// Validate applies the immutable deny-by-default contract rules.
func (b ManagementBoundary) Validate() error {
	if b.Version != BoundaryVersionV1 {
		return fmt.Errorf("cpa boundary: unsupported version %d", b.Version)
	}
	if !exactVersion(b.CPAMinVersion) || !exactVersion(b.CPAMaxVersion) {
		return errors.New("cpa boundary: CPA version range must contain exact versions")
	}
	if !digestPattern.MatchString(b.RouteInventorySHA256) {
		return errors.New("cpa boundary: route inventory digest must be lowercase SHA-256")
	}
	if strings.Trim(b.RouteInventorySHA256, "0") == "" && !b.RouteInventoryPlaceholder {
		return errors.New("cpa boundary: zero route inventory digest requires explicit placeholder metadata")
	}
	if b.RouteInventoryPlaceholder && strings.Trim(b.RouteInventorySHA256, "0") != "" {
		return errors.New("cpa boundary: route inventory placeholder must use a zero digest")
	}
	if len(b.Inference) == 0 {
		return errors.New("cpa boundary: inference allowlist is empty")
	}
	if err := validateRequiredFacts(b.RequiredFacts); err != nil {
		return err
	}
	seen := make(map[string]string)
	for i, r := range b.Inference {
		if err := validateRoute(r, "inference", i); err != nil {
			return err
		}
		if r.Public == false || r.RequiresMTLS || r.InjectsManagementKey {
			return fmt.Errorf("cpa boundary: inference[%d] has unsafe exposure flags", i)
		}
		if strings.HasPrefix(r.Path, ManagementPrefix) {
			return fmt.Errorf("cpa boundary: inference[%d] exposes management prefix", i)
		}
		if err := addUniqueRoute(seen, r, "inference"); err != nil {
			return err
		}
	}
	if len(b.Callback) != 2 {
		return errors.New("cpa boundary: callback must contain exactly GET and POST")
	}
	callbackMethods := map[string]bool{}
	for i, r := range b.Callback {
		if err := validateRoute(r, "callback", i); err != nil {
			return err
		}
		if r.Path != CallbackPath || (r.Method != "GET" && r.Method != "POST") || callbackMethods[r.Method] {
			return errors.New("cpa boundary: callback must allow only one exact GET and POST route")
		}
		callbackMethods[r.Method] = true
		if !r.Public || r.RequiresMTLS || r.InjectsManagementKey {
			return errors.New("cpa boundary: callback must be public without mTLS or management key")
		}
		if err := addUniqueRoute(seen, r, "callback"); err != nil {
			return err
		}
	}
	if !callbackMethods["GET"] || !callbackMethods["POST"] {
		return errors.New("cpa boundary: callback GET and POST are both required")
	}
	for i, r := range b.PrivateCapabilities {
		if err := validateRoute(r, "private", i); err != nil {
			return err
		}
		if r.Public || !r.RequiresMTLS || !r.InjectsManagementKey {
			return fmt.Errorf("cpa boundary: private[%d] must be private mTLS with adapter key injection", i)
		}
		if r.Destructive || r.ConsumesQueue {
			return fmt.Errorf("cpa boundary: private capability %q is destructive or consumes a queue", r.CapabilityID)
		}
		if dangerousCapability(r.CapabilityID) || dangerousPath(r.Path) {
			return fmt.Errorf("cpa boundary: private capability %q is denied in v1", r.CapabilityID)
		}
		if err := validateDependencies(r, b.RequiredFacts); err != nil {
			return err
		}
		if err := addUniqueRoute(seen, r, "private"); err != nil {
			return err
		}
	}
	for i, r := range b.Denied {
		if err := validateRoute(r, "denied", i); err != nil {
			return err
		}
		if r.Plane != "denied" || r.Public || r.RequiresMTLS || r.InjectsManagementKey {
			return fmt.Errorf("cpa boundary: denied[%d] has invalid plane/exposure flags", i)
		}
		if err := addUniqueRoute(seen, r, "denied"); err != nil {
			return err
		}
		if wildcardPath(r.Path) || strings.Contains(r.CapabilityID, "proxy") {
			return fmt.Errorf("cpa boundary: denied[%d] uses wildcard or generic proxy", i)
		}
	}
	if !containsDenyCapabilities(b.Denied) {
		return errors.New("cpa boundary: deny-list must cover config/auth/api-call/plugin/usage and management prefix")
	}
	return nil
}

func validateRequiredFacts(facts map[string]string) error {
	if facts == nil {
		return errors.New("cpa boundary: required_facts is null")
	}
	known := map[string]struct{}{
		"allow_remote": {}, "management_password": {}, "raw_port_bind": {},
		"management_key_location": {}, "inference_version_evidence": {},
		"route_inventory": {}, "public_callback_hostname": {}, "private_management_transport": {},
		"foundation_b": {}, "r210": {}, "r214": {}, "r215": {},
	}
	for key := range facts {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("cpa boundary: unknown required fact %q", key)
		}
	}
	required := map[string]string{
		"allow_remote":                 "false",
		"management_password":          "absent",
		"raw_port_bind":                "loopback",
		"management_key_location":      "adapter_local",
		"inference_version_evidence":   "required",
		"route_inventory":              "exact",
		"public_callback_hostname":     "per_instance",
		"private_management_transport": "mtls",
	}
	for key, want := range required {
		if got, ok := facts[key]; !ok || got != want {
			return fmt.Errorf("cpa boundary: required fact %s must be %q", key, want)
		}
	}
	return nil
}

func validateRoute(r RouteRule, expectedPlane string, index int) error {
	if r.Plane != expectedPlane {
		return fmt.Errorf("cpa boundary: %s[%d] has plane %q, want %q", expectedPlane, index, r.Plane, expectedPlane)
	}
	if r.Method != "ANY" && r.Method != "GET" && r.Method != "POST" && r.Method != "HEAD" {
		return fmt.Errorf("cpa boundary: %s[%d] has unsupported method %q", expectedPlane, index, r.Method)
	}
	if expectedPlane != "denied" && r.Method == "ANY" {
		return fmt.Errorf("cpa boundary: %s[%d] cannot use ANY method", expectedPlane, index)
	}
	if r.Path == "" || !strings.HasPrefix(r.Path, "/") || strings.ContainsAny(r.Path, "?#:\\\r\n\t") {
		return fmt.Errorf("cpa boundary: %s[%d] path must be an exact local path", expectedPlane, index)
	}
	if strings.Contains(r.Path, "%") || strings.Contains(r.Path, "//") || strings.Contains(r.Path, "/./") || strings.Contains(r.Path, "/../") || strings.HasSuffix(r.Path, "/.") || strings.HasSuffix(r.Path, "/..") {
		return fmt.Errorf("cpa boundary: %s[%d] path contains encoded or traversal variant", expectedPlane, index)
	}
	if wildcardPath(r.Path) {
		return fmt.Errorf("cpa boundary: %s[%d] wildcard path is forbidden", expectedPlane, index)
	}
	if !capabilityPattern.MatchString(r.CapabilityID) || strings.Contains(r.CapabilityID, "proxy") {
		return fmt.Errorf("cpa boundary: %s[%d] capability must be a non-proxy exact ID", expectedPlane, index)
	}
	if !allowedRisk(r.RiskClass) {
		return fmt.Errorf("cpa boundary: %s[%d] unknown risk class %q", expectedPlane, index, r.RiskClass)
	}
	if r.MaxBodyBytes <= 0 || r.MaxBodyBytes > 16<<20 || r.TimeoutMillis <= 0 || r.TimeoutMillis > 120_000 || r.RatePerMinute <= 0 || r.RatePerMinute > 10_000 {
		return fmt.Errorf("cpa boundary: %s[%d] finite body/timeout/rate limits are required", expectedPlane, index)
	}
	if err := validateProjection(r.ResponseProjection); err != nil {
		return fmt.Errorf("cpa boundary: %s[%d] response projection: %w", expectedPlane, index, err)
	}
	return nil
}

func addUniqueRoute(seen map[string]string, r RouteRule, source string) error {
	key := r.Plane + "\x00" + r.Method + "\x00" + r.Path
	if previous, ok := seen[key]; ok {
		return fmt.Errorf("cpa boundary: route %s %s %s overlaps %s and %s", r.Method, r.Path, r.Plane, previous, source)
	}
	seen[key] = source
	// An exact callback path on the callback plane must not be repeated under a
	// different capability, and an exact route cannot be both allowed and
	// denied.  Prefix overlap is handled by the plane-specific deny logic.
	if source != "denied" {
		for _, method := range []string{r.Method, "ANY"} {
			denyKey := "denied\x00" + method + "\x00" + r.Path
			if previous, ok := seen[denyKey]; ok {
				return fmt.Errorf("cpa boundary: route overlaps deny rule from %s", previous)
			}
		}
	} else {
		// A deny ANY rule conflicts with every exact allow method on the same
		// plane/path.  Keep this check symmetric because denied rules are
		// validated after allowlists.
		for _, method := range []string{"GET", "POST", "HEAD", "ANY"} {
			for _, plane := range []string{"inference", "callback", "private"} {
				allowKey := plane + "\x00" + method + "\x00" + r.Path
				if previous, ok := seen[allowKey]; ok {
					return fmt.Errorf("cpa boundary: deny route overlaps allow rule from %s", previous)
				}
			}
		}
	}
	return nil
}

func containsDenyCapabilities(routes []RouteRule) bool {
	want := map[string]bool{"management.wildcard": false, "management.config": false, "management.auth": false, "management.api-call": false, "management.plugin": false, "management.usage": false}
	for _, r := range routes {
		if _, ok := want[r.CapabilityID]; ok {
			want[r.CapabilityID] = true
		}
	}
	for _, ok := range want {
		if !ok {
			return false
		}
	}
	return true
}

func validateDependencies(r RouteRule, facts map[string]string) error {
	checks := []struct {
		name string
		flag bool
	}{
		{"foundation_b", r.RequiresFoundationB}, {"r210", r.RequiresR210},
		{"r214", r.RequiresR214}, {"r215", r.RequiresR215},
	}
	for _, check := range checks {
		if check.flag && facts[check.name] != "approved" {
			return fmt.Errorf("cpa boundary: capability %q requires approved dependency %s", r.CapabilityID, check.name)
		}
	}
	if r.ConsumesQueue && (!r.RequiresFoundationB || !r.RequiresR210 || !r.RequiresR215) {
		return fmt.Errorf("cpa boundary: queue capability %q must declare Foundation-B/R210/R215 dependencies", r.CapabilityID)
	}
	return nil
}

func dangerousCapability(id string) bool {
	lower := strings.ToLower(id)
	for _, marker := range []string{"config", "auth", "api-call", "plugin", "usage", "queue", "write", "credential", "proxy"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func dangerousPath(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range []string{"/config", "/auth", "/api-call", "/plugin", "/usage", "/credential"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func wildcardPath(path string) bool {
	return strings.ContainsAny(path, "*{}[]") || strings.Contains(path, "...")
}

func allowedRisk(value string) bool {
	switch value {
	case "inference", "callback", "private", "deny", "public", "management", "destructive", "write", "plugin", "auth", "config", "usage", "api-call", "low", "medium", "high", "critical":
		return true
	default:
		return false
	}
}

func exactVersion(value string) bool {
	if !versionPattern.MatchString(value) || strings.EqualFold(value, "latest") || strings.EqualFold(value, "head") {
		return false
	}
	if strings.ContainsAny(value, "*^~<>=|, ") || strings.Contains(value, "..") {
		return false
	}
	for _, component := range strings.Split(value, ".") {
		if component == "x" || component == "X" {
			return false
		}
	}
	return true
}

func validateProjection(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, key := range strings.Split(value, ",") {
		key = strings.TrimSpace(key)
		if !projectionKeyPattern.MatchString(key) || seen[key] {
			return fmt.Errorf("projection key %q is invalid or duplicated", key)
		}
		seen[key] = true
		lower := strings.ToLower(key)
		for _, marker := range []string{"credential", "secret", "password", "token", "authorization", "cookie", "header", "config", "auth_file", "private_key", "api_key", "body", "raw"} {
			if strings.Contains(lower, marker) {
				return fmt.Errorf("projection key %q is sensitive", key)
			}
		}
	}
	return nil
}

func normalizeBoundary(b *ManagementBoundary) {
	less := func(a, c RouteRule) bool {
		ak := a.Plane + "\x00" + a.Method + "\x00" + a.Path + "\x00" + a.CapabilityID
		ck := c.Plane + "\x00" + c.Method + "\x00" + c.Path + "\x00" + c.CapabilityID
		return ak < ck
	}
	for _, routes := range [][]RouteRule{b.Inference, b.Callback, b.PrivateCapabilities, b.Denied} {
		sort.Slice(routes, func(i, j int) bool { return less(routes[i], routes[j]) })
	}
}

// Allows reports whether an exact route is in an allowlist. Query strings,
// encoded paths, management siblings, and unknown planes always return false.
func (b ManagementBoundary) Allows(plane, method, routePath string) bool {
	if err := b.Validate(); err != nil {
		return false
	}
	if method != "GET" && method != "POST" && method != "HEAD" {
		return false
	}
	if strings.Contains(routePath, "?") || strings.Contains(routePath, "#") {
		return false
	}
	if _, err := normalizeExactPath(routePath); err != nil {
		return false
	}
	if plane == "inference" && strings.HasPrefix(routePath, ManagementPrefix) {
		return false
	}
	if plane == "callback" && routePath != CallbackPath {
		return false
	}
	var routes []RouteRule
	switch plane {
	case "inference":
		routes = b.Inference
	case "callback":
		routes = b.Callback
	case "private":
		routes = b.PrivateCapabilities
	default:
		return false
	}
	for _, r := range routes {
		if r.Method == method && r.Path == routePath {
			return true
		}
	}
	return false
}

// RouteAllowed is a descriptive alias for Allows.
func (b ManagementBoundary) RouteAllowed(plane, method, routePath string) bool {
	return b.Allows(plane, method, routePath)
}

// IsAllowed is a short alias useful to route auditors.
func (b ManagementBoundary) IsAllowed(plane, method, routePath string) bool {
	return b.Allows(plane, method, routePath)
}

// CapabilityAllowed checks a private capability against the boundary and
// dependency facts.  A missing/unknown capability fails closed.
func (b ManagementBoundary) CapabilityAllowed(capabilityID string, facts map[string]string) error {
	if err := b.Validate(); err != nil {
		return err
	}
	for _, route := range b.PrivateCapabilities {
		if route.CapabilityID == capabilityID {
			return validateDependencies(route, facts)
		}
	}
	return fmt.Errorf("cpa boundary: capability %q is not approved", capabilityID)
}

// ProjectResponse applies the route's allowlisted response projection to one
// JSON object. Sensitive/unknown fields are rejected rather than echoed.
func (b ManagementBoundary) ProjectResponse(capabilityID string, payload []byte) ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	var route *RouteRule
	for i := range b.PrivateCapabilities {
		if b.PrivateCapabilities[i].CapabilityID == capabilityID {
			route = &b.PrivateCapabilities[i]
			break
		}
	}
	if route == nil {
		return nil, fmt.Errorf("cpa boundary: unknown response capability %q", capabilityID)
	}
	var object map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields() // object values are raw, but duplicate keys are checked below
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return nil, err
	}
	if err := dec.Decode(&object); err != nil {
		return nil, fmt.Errorf("response must be a JSON object: %w", err)
	}
	if object == nil {
		return nil, errors.New("response must be a non-null JSON object")
	}
	allowed := map[string]bool{}
	for _, key := range strings.Split(route.ResponseProjection, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			allowed[key] = true
		}
	}
	for key := range object {
		if err := validateProjection(key); err != nil {
			return nil, err
		}
		if !allowed[key] {
			return nil, fmt.Errorf("response field %q is not in projection", key)
		}
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func normalizeExactPath(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#%\\\r\n\t") || strings.Contains(value, "//") || strings.Contains(value, "/./") || strings.Contains(value, "/../") || wildcardPath(value) {
		return "", errors.New("path is not an exact unencoded path")
	}
	return value, nil
}

// NormalizePath exposes the same fail-closed path check used by Allows.
func NormalizePath(value string) (string, error) { return normalizeExactPath(value) }
