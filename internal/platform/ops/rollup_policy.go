package ops

// Versioned metric rollup policy validation.  The policy is intentionally
// independent from connectors: it is a small, immutable contract which can be
// loaded by a rollup process without importing any upstream client.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	RollupPolicyVersion int16 = 1

	ValueGauge          ValueKind = "gauge"
	ValueDailySnapshot  ValueKind = "daily_snapshot"
	ValueAdditiveDelta  ValueKind = "additive_delta"
	ValueDocumentStatus ValueKind = "document_status"

	PrimaryCount      PrimaryKind = "count"
	PrimaryMoneyMinor PrimaryKind = "money_minor"
	PrimaryFixedPoint PrimaryKind = "fixed_point"
	PrimaryNone       PrimaryKind = "none"
)

// ValueKind controls the semantic family of a sample.
type ValueKind string

// PrimaryKind controls the scalar extracted from value_json.
type PrimaryKind string

// RollupPolicy is the checked, runtime representation of one metric policy.
// PolicyHash is populated by LoadRollupPolicies and is not part of the policy
// bytes themselves.
type RollupPolicy struct {
	Version                 int16
	MetricKey               string
	ValueKind               ValueKind
	PrimaryKind             PrimaryKind
	PrimaryJSONPointer      string
	CurrencyJSONPointer     string
	BusinessDayJSONPointer  string
	Unit                    string
	Scale                   int64
	SumMode                 string
	BucketTimezone          string
	ExpectedIntervalSeconds *int32
	FullValueRequired       bool
	PartialValueAllowed     bool
	PolicyHash              string
}

// ExcludedRollupMetric records a known metric which is deliberately not
// active in this policy version.  Invoice metrics remain here until CR-0002 is
// frozen; omission without an explicit reason would make the gap invisible.
type ExcludedRollupMetric struct {
	MetricKey string
	Gate      string
	Reason    string
}

var excludedRollupMetrics = []ExcludedRollupMetric{
	{MetricKey: "invoice.amount.daily", Gate: "CR-0002", Reason: "invoice contract is not frozen"},
	{MetricKey: "invoice.requests.daily", Gate: "CR-0002", Reason: "invoice contract is not frozen"},
	// XM-PAY0: value_json carries a by_status map keyed by a variable set of
	// normalized buckets (succeeded/pending/failed/refunded, each with its
	// own count and amount_minor_units) instead of one scalar. The v1 policy
	// schema only supports a single primary_json_pointer per metric, so this
	// metric cannot get an active policy until the schema grows a per-bucket
	// (or repeated-pointer) shape. Raw observations still flow through
	// /metrics and /metrics/history normally; only downsampled rollups are
	// gated.
	{MetricKey: "sub2api.payments.daily", Gate: "ROLLUP-MULTI-BUCKET", Reason: "by_status has multiple money buckets; v1 policy schema supports only one primary_json_pointer per metric"},
	{MetricKey: "newapi.payments.daily", Gate: "ROLLUP-MULTI-BUCKET", Reason: "by_status has multiple money buckets; v1 policy schema supports only one primary_json_pointer per metric"},
}

// ExcludedRollupMetricKeys returns the deterministic list of gated keys.
func ExcludedRollupMetricKeys() []string {
	out := make([]string, 0, len(excludedRollupMetrics))
	for _, item := range excludedRollupMetrics {
		out = append(out, item.MetricKey)
	}
	sort.Strings(out)
	return out
}

// ExcludedRollupMetrics returns a defensive copy including gate explanations.
func ExcludedRollupMetrics() []ExcludedRollupMetric {
	out := append([]ExcludedRollupMetric(nil), excludedRollupMetrics...)
	sort.Slice(out, func(i, j int) bool { return out[i].MetricKey < out[j].MetricKey })
	return out
}

// ErrInvoiceRollupGated is returned when a caller asks for a policy which is
// intentionally unavailable pending the invoice change request.
var ErrInvoiceRollupGated = errors.New("CR-0002 pending: invoice metric rollup policy is gated")

type rollupPolicyWire struct {
	PolicyVersion           int16  `json:"policy_version"`
	MetricKey               string `json:"metric_key"`
	ValueKind               string `json:"value_kind"`
	PrimaryKind             string `json:"primary_kind"`
	PrimaryJSONPointer      string `json:"primary_json_pointer"`
	CurrencyJSONPointer     string `json:"currency_json_pointer"`
	BusinessDayJSONPointer  string `json:"business_day_json_pointer"`
	Unit                    string `json:"unit"`
	Scale                   int64  `json:"scale"`
	SumMode                 string `json:"sum_mode"`
	BucketTimezone          string `json:"bucket_timezone"`
	ExpectedIntervalSeconds *int32 `json:"expected_interval_seconds"`
	FullValueRequired       *bool  `json:"full_value_required"`
	PartialValueAllowed     *bool  `json:"partial_value_allowed"`
}

type excludedRollupWire struct {
	MetricKey string `json:"metric_key"`
	Gate      string `json:"gate"`
	Reason    string `json:"reason"`
}

type rollupPolicyDocumentWire struct {
	PolicyVersion      int16                `json:"policy_version"`
	ExcludedMetricKeys []excludedRollupWire `json:"excluded_metric_keys"`
	Policies           []rollupPolicyWire   `json:"policies"`
}

// LoadRollupPolicies strictly decodes a policy document and returns active
// policies plus the SHA-256 of deterministic canonical bytes.
func LoadRollupPolicies(data []byte) (map[string]RollupPolicy, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, "", errors.New("metric rollup policy is empty")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, "", err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var doc rollupPolicyDocumentWire
	if err := dec.Decode(&doc); err != nil {
		return nil, "", fmt.Errorf("decode metric rollup policy: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, "", errors.New("metric rollup policy has trailing JSON")
		}
		return nil, "", fmt.Errorf("decode trailing metric rollup policy: %w", err)
	}
	if doc.PolicyVersion != RollupPolicyVersion {
		return nil, "", fmt.Errorf("unsupported metric rollup policy version %d", doc.PolicyVersion)
	}
	if len(doc.Policies) == 0 {
		return nil, "", errors.New("metric rollup policy has no active policies")
	}

	excluded, err := validateExclusions(doc.ExcludedMetricKeys)
	if err != nil {
		return nil, "", err
	}
	registered := RegisteredMetricKeys()
	registeredSet := make(map[string]struct{}, len(registered))
	for _, key := range registered {
		registeredSet[key] = struct{}{}
	}
	for key := range excluded {
		if _, ok := registeredSet[key]; !ok {
			return nil, "", fmt.Errorf("excluded metric key %q is not registered", key)
		}
	}

	policies := make(map[string]RollupPolicy, len(doc.Policies))
	wires := make([]rollupPolicyWire, 0, len(doc.Policies))
	for i, wire := range doc.Policies {
		if _, gated := excluded[wire.MetricKey]; gated {
			return nil, "", fmt.Errorf("%w: policy entry %q must remain excluded", ErrInvoiceRollupGated, wire.MetricKey)
		}
		if _, exists := policies[wire.MetricKey]; exists {
			return nil, "", fmt.Errorf("duplicate metric policy %q", wire.MetricKey)
		}
		policy, err := validatePolicyWire(wire, i)
		if err != nil {
			return nil, "", err
		}
		policies[policy.MetricKey] = policy
		wires = append(wires, wire)
	}
	for key := range policies {
		if _, ok := registeredSet[key]; !ok {
			return nil, "", fmt.Errorf("metric policy key %q is not registered", key)
		}
	}
	for _, key := range registered {
		if _, ok := policies[key]; ok {
			continue
		}
		if _, ok := excluded[key]; ok {
			continue
		}
		return nil, "", fmt.Errorf("registered metric key %q has no policy or explicit exclusion", key)
	}
	if len(policies)+len(excluded) != len(registered) {
		return nil, "", fmt.Errorf("policy coverage mismatch: active=%d excluded=%d registered=%d", len(policies), len(excluded), len(registered))
	}

	canonical := canonicalPolicyDocument(doc, wires)
	canonicalBytes, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize metric rollup policy: %w", err)
	}
	digest := sha256.Sum256(canonicalBytes)
	hash := hex.EncodeToString(digest[:])
	for key, policy := range policies {
		policy.PolicyHash = hash
		policies[key] = policy
	}
	return policies, hash, nil
}

func validateExclusions(items []excludedRollupWire) (map[string]struct{}, error) {
	if len(items) != len(excludedRollupMetrics) {
		return nil, fmt.Errorf("expected exactly %d explicit metric exclusions, got %d", len(excludedRollupMetrics), len(items))
	}
	want := make(map[string]ExcludedRollupMetric, len(excludedRollupMetrics))
	for _, item := range excludedRollupMetrics {
		want[item.MetricKey] = item
	}
	seen := make(map[string]struct{}, len(items))
	for _, wire := range items {
		if _, ok := seen[wire.MetricKey]; ok {
			return nil, fmt.Errorf("duplicate excluded metric key %q", wire.MetricKey)
		}
		seen[wire.MetricKey] = struct{}{}
		expected, ok := want[wire.MetricKey]
		if !ok {
			return nil, fmt.Errorf("unknown excluded metric key %q", wire.MetricKey)
		}
		if wire.Gate != expected.Gate || strings.TrimSpace(wire.Reason) == "" {
			return nil, fmt.Errorf("excluded metric %q must carry gate %s and a reason", wire.MetricKey, expected.Gate)
		}
		if wire.Reason != expected.Reason {
			return nil, fmt.Errorf("excluded metric %q reason drifted", wire.MetricKey)
		}
	}
	for key := range want {
		if _, ok := seen[key]; !ok {
			return nil, fmt.Errorf("excluded metric key %q is missing", key)
		}
	}
	return seen, nil
}

func validatePolicyWire(w rollupPolicyWire, index int) (RollupPolicy, error) {
	if w.PolicyVersion != RollupPolicyVersion {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q has unsupported version %d", index, w.MetricKey, w.PolicyVersion)
	}
	if !ValidMetricKey(w.MetricKey) {
		return RollupPolicy{}, fmt.Errorf("policy[%d] has invalid metric key %q", index, w.MetricKey)
	}
	valueKind := ValueKind(w.ValueKind)
	switch valueKind {
	case ValueGauge, ValueDailySnapshot, ValueAdditiveDelta, ValueDocumentStatus:
	default:
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q has unknown value kind %q", index, w.MetricKey, w.ValueKind)
	}
	primaryKind := PrimaryKind(w.PrimaryKind)
	switch primaryKind {
	case PrimaryCount, PrimaryMoneyMinor, PrimaryFixedPoint, PrimaryNone:
	default:
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q has unknown primary kind %q", index, w.MetricKey, w.PrimaryKind)
	}
	if strings.TrimSpace(w.Unit) == "" || strings.ContainsAny(w.Unit, "\r\n\t ") {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q has invalid unit", index, w.MetricKey)
	}
	if w.Scale <= 0 {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q scale must be positive", index, w.MetricKey)
	}
	if w.BucketTimezone != "UTC" {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q bucket timezone must be UTC", index, w.MetricKey)
	}
	if w.SumMode != "forbidden" && w.SumMode != "additive" {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q has unknown sum mode %q", index, w.MetricKey, w.SumMode)
	}
	if valueKind != ValueAdditiveDelta && w.SumMode != "forbidden" {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q snapshot sum mode must be forbidden", index, w.MetricKey)
	}
	if valueKind == ValueAdditiveDelta && w.SumMode != "additive" {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q additive delta must use additive sum mode", index, w.MetricKey)
	}
	if w.ExpectedIntervalSeconds != nil && *w.ExpectedIntervalSeconds <= 0 {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q expected interval must be positive", index, w.MetricKey)
	}
	if w.FullValueRequired == nil || w.PartialValueAllowed == nil {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q must specify full_value_required and partial_value_allowed", index, w.MetricKey)
	}
	if err := validateJSONPointer(w.PrimaryJSONPointer); err != nil {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q primary pointer: %w", index, w.MetricKey, err)
	}
	if err := validateJSONPointer(w.CurrencyJSONPointer); err != nil {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q currency pointer: %w", index, w.MetricKey, err)
	}
	if err := validateJSONPointer(w.BusinessDayJSONPointer); err != nil {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q business day pointer: %w", index, w.MetricKey, err)
	}
	if primaryKind == PrimaryNone {
		if w.PrimaryJSONPointer != "" {
			return RollupPolicy{}, fmt.Errorf("policy[%d] %q primary none requires empty pointer", index, w.MetricKey)
		}
	} else if w.PrimaryJSONPointer == "" {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q primary kind requires pointer", index, w.MetricKey)
	}
	if primaryKind == PrimaryMoneyMinor && w.CurrencyJSONPointer == "" {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q money primary requires currency pointer", index, w.MetricKey)
	}
	if primaryKind != PrimaryMoneyMinor && w.CurrencyJSONPointer != "" {
		return RollupPolicy{}, fmt.Errorf("policy[%d] %q non-money primary must not carry currency pointer", index, w.MetricKey)
	}
	return RollupPolicy{
		Version: RollupPolicyVersion, MetricKey: w.MetricKey, ValueKind: valueKind,
		PrimaryKind: primaryKind, PrimaryJSONPointer: w.PrimaryJSONPointer,
		CurrencyJSONPointer: w.CurrencyJSONPointer, BusinessDayJSONPointer: w.BusinessDayJSONPointer,
		Unit: w.Unit, Scale: w.Scale, SumMode: w.SumMode, BucketTimezone: w.BucketTimezone,
		ExpectedIntervalSeconds: w.ExpectedIntervalSeconds, FullValueRequired: *w.FullValueRequired,
		PartialValueAllowed: *w.PartialValueAllowed,
	}, nil
}

func validateJSONPointer(pointer string) error {
	if pointer == "" {
		return nil
	}
	if !strings.HasPrefix(pointer, "/") || strings.ContainsAny(pointer, "\r\n\t") {
		return errors.New("must be an RFC 6901 pointer or empty")
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] != '~' {
			continue
		}
		if i+1 >= len(pointer) || (pointer[i+1] != '0' && pointer[i+1] != '1') {
			return errors.New("contains invalid escape")
		}
		i++
	}
	return nil
}

// RollupPolicyFor resolves a known active policy.  Gated invoice keys return a
// typed sentinel so callers can distinguish an intentional gate from an
// unknown metric.
func RollupPolicyFor(policies map[string]RollupPolicy, metricKey string) (RollupPolicy, error) {
	if _, gated := excludedRollupKeySet()[metricKey]; gated {
		return RollupPolicy{}, fmt.Errorf("%w: %s", ErrInvoiceRollupGated, metricKey)
	}
	policy, ok := policies[metricKey]
	if !ok {
		return RollupPolicy{}, fmt.Errorf("unknown metric rollup policy %q", metricKey)
	}
	if policy.Version != RollupPolicyVersion || policy.MetricKey != metricKey {
		return RollupPolicy{}, fmt.Errorf("metric rollup policy %q has unsupported version or key", metricKey)
	}
	return policy, nil
}

func excludedRollupKeySet() map[string]struct{} {
	out := make(map[string]struct{}, len(excludedRollupMetrics))
	for _, item := range excludedRollupMetrics {
		out[item.MetricKey] = struct{}{}
	}
	return out
}

// canonicalPolicyDocument returns a sorted representation for hashing and
// makes policy bytes independent of array ordering.
func canonicalPolicyDocument(doc rollupPolicyDocumentWire, policies []rollupPolicyWire) rollupPolicyDocumentWire {
	out := doc
	out.ExcludedMetricKeys = append([]excludedRollupWire(nil), doc.ExcludedMetricKeys...)
	sort.Slice(out.ExcludedMetricKeys, func(i, j int) bool { return out.ExcludedMetricKeys[i].MetricKey < out.ExcludedMetricKeys[j].MetricKey })
	out.Policies = append([]rollupPolicyWire(nil), policies...)
	sort.Slice(out.Policies, func(i, j int) bool { return out.Policies[i].MetricKey < out.Policies[j].MetricKey })
	return out
}

// rejectDuplicateJSONKeys catches duplicate object members, which encoding/json
// otherwise accepts with last-value-wins semantics.
func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, isDelim := tok.(json.Delim)
		if !isDelim {
			// Primitive values (numbers, strings, booleans and null) have no
			// child object members to inspect.
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("metric rollup policy object key is not a string")
				}
				if _, exists := seen[name]; exists {
					return fmt.Errorf("metric rollup policy has duplicate field %q", name)
				}
				seen[name] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token() // closing brace
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token() // closing bracket
			return err
		default:
			return nil
		}
	}
	if err := walk(); err != nil {
		return fmt.Errorf("decode metric rollup policy keys: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("metric rollup policy has multiple JSON values")
		}
		return fmt.Errorf("decode metric rollup policy trailer: %w", err)
	}
	return nil
}
