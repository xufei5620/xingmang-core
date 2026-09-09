// Package cpaplugin contains the offline, fail-closed contracts used to
// assess CPA native plugins.  Nothing in this package can fetch, load, or
// execute a plugin; it only validates caller-supplied JSON and evidence.
package cpaplugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	PolicyVersionV1       = 1
	StoreSourcesVersionV1 = 1
)

var (
	pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40,128}$`)
	versionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
)

// PluginAdmission is one exact, human-approved admission record.  The
// record deliberately contains no environment wildcard: one record applies
// to one plugin artifact, CPA build, and target platform only.
type PluginAdmission struct {
	PluginID               string   `json:"plugin_id"`
	SourceID               string   `json:"source_id"`
	Repository             string   `json:"repository"`
	Author                 string   `json:"author,omitempty"`
	ExactTag               string   `json:"exact_tag"`
	ArtifactName           string   `json:"artifact_name"`
	ArtifactSHA256         string   `json:"artifact_sha256"`
	ArtifactSize           int64    `json:"artifact_size"`
	SourceCommit           string   `json:"source_commit"`
	SourceArchiveSHA256    string   `json:"source_archive_sha256"`
	ProvenanceType         string   `json:"provenance_type"`
	ProvenanceDigest       string   `json:"provenance_digest"`
	SBOMDigest             string   `json:"sbom_digest"`
	LicenseSPDX            string   `json:"license_spdx"`
	LicenseTextSHA256      string   `json:"license_text_sha256"`
	NoticeSHA256           string   `json:"notice_sha256"`
	LicenseApprovalRef     string   `json:"license_approval_ref"`
	CPAExactBuildDigest    string   `json:"cpa_exact_build_digest"`
	CPAVersion             string   `json:"cpa_version"`
	GOOS                   string   `json:"goos"`
	GOARCH                 string   `json:"goarch"`
	Variant                string   `json:"variant"`
	FileExtension          string   `json:"file_extension"`
	ABISchemaVersion       int      `json:"abi_schema_version"`
	DeclaredCapabilities   []string `json:"declared_capabilities"`
	AllowedCapabilities    []string `json:"allowed_capabilities"`
	ForbiddenCapabilities  []string `json:"forbidden_capabilities"`
	ConfigSchemaSHA256     string   `json:"config_schema_sha256"`
	ManagementRoutesSHA256 string   `json:"management_routes_sha256"`
	ResourceRoutesSHA256   string   `json:"resource_routes_sha256"`
	CanaryProfile          string   `json:"canary_profile"`
	RollbackArtifactSHA256 string   `json:"rollback_artifact_sha256"`
	ChangeRef              string   `json:"change_ref"`
	Prerelease             bool     `json:"prerelease,omitempty"`
	AutoUpdate             bool     `json:"auto_update,omitempty"`
	Latest                 bool     `json:"latest,omitempty"`
	ManualUnverified       bool     `json:"manual_unverified,omitempty"`

	// UnsandboxedResidualRisk is derived, not an approval input.  It is always
	// true for a native CPA plugin and is surfaced to callers so a clean policy
	// cannot be mistaken for a sandbox guarantee.
	UnsandboxedResidualRisk bool `json:"unsandboxed_residual_risk,omitempty"`
}

// AdmissionPolicyV1 is the checked-in policy envelope. deny_by_default must be
// explicitly true on the JSON wire; this prevents an omitted flag from being
// interpreted as an accidental allow mode.
type AdmissionPolicyV1 struct {
	PolicyVersion int               `json:"policy_version"`
	DenyByDefault bool              `json:"deny_by_default,omitempty"`
	Admissions    []PluginAdmission `json:"admissions"`
}

// PluginAdmissionPolicyV1 is a descriptive alias used by callers.
type PluginAdmissionPolicyV1 = AdmissionPolicyV1

// LoadAdmissionPolicy decodes and validates a policy.  It returns a map keyed
// by plugin ID and a SHA-256 digest of canonical JSON (hex, lower case).
func LoadAdmissionPolicy(data []byte) (map[string]PluginAdmission, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, "", errors.New("empty plugin admission policy")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, "", err
	}
	for _, field := range []string{"policy_version", "deny_by_default", "admissions"} {
		if !topLevelFieldPresent(data, field) {
			return nil, "", fmt.Errorf("plugin admission policy is missing %s", field)
		}
	}
	if raw, ok := topLevelRawField(data, "admissions"); !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || bytes.TrimSpace(raw)[0] != '[' {
		return nil, "", errors.New("admissions must be an array")
	}
	if has, value := topLevelBoolField(data, "deny_by_default"); !has || !value {
		return nil, "", errors.New("deny_by_default must be explicitly true")
	}
	// This is a derived risk annotation, not a policy input.  Rejecting it on
	// the wire prevents a producer from presenting a fabricated trust bit.
	if jsonHasKey(data, "unsandboxed_residual_risk") {
		return nil, "", errors.New("unsandboxed_residual_risk is derived and must not be supplied")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var policy AdmissionPolicyV1
	if err := dec.Decode(&policy); err != nil {
		return nil, "", fmt.Errorf("decode plugin admission policy: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, "", errors.New("plugin admission policy has trailing JSON")
		}
		return nil, "", fmt.Errorf("decode trailing policy data: %w", err)
	}
	policy.DenyByDefault = true
	if err := policy.Validate(); err != nil {
		return nil, "", err
	}
	// The canonical policy digest covers only signed/approved input fields. The
	// derived residual-risk bit is deliberately excluded, so adding a display
	// annotation never invalidates a policy signature.
	for i := range policy.Admissions {
		policy.Admissions[i].UnsandboxedResidualRisk = true
		policy.Admissions[i].DeclaredCapabilities = sortedCopy(policy.Admissions[i].DeclaredCapabilities)
		policy.Admissions[i].AllowedCapabilities = sortedCopy(policy.Admissions[i].AllowedCapabilities)
		policy.Admissions[i].ForbiddenCapabilities = sortedCopy(policy.Admissions[i].ForbiddenCapabilities)
	}
	sort.Slice(policy.Admissions, func(i, j int) bool { return policy.Admissions[i].PluginID < policy.Admissions[j].PluginID })
	canonicalPolicy := policy
	canonicalPolicy.Admissions = append([]PluginAdmission(nil), policy.Admissions...)
	for i := range canonicalPolicy.Admissions {
		canonicalPolicy.Admissions[i].UnsandboxedResidualRisk = false
	}
	canonical, err := json.Marshal(canonicalPolicy)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize plugin admission policy: %w", err)
	}
	sum := sha256.Sum256(canonical)
	result := make(map[string]PluginAdmission, len(policy.Admissions))
	for _, admission := range policy.Admissions {
		result[admission.PluginID] = admission
	}
	return result, hex.EncodeToString(sum[:]), nil
}

// LoadAdmissionPolicyBytes is an explicit alias for code that prefers a
// bytes-oriented name.
func LoadAdmissionPolicyBytes(data []byte) (map[string]PluginAdmission, string, error) {
	return LoadAdmissionPolicy(data)
}

// Validate checks every immutable admission field.  Empty admissions are
// valid and represent the deny-by-default, plugin-free baseline.
func (p AdmissionPolicyV1) Validate() error {
	if p.PolicyVersion != PolicyVersionV1 {
		return fmt.Errorf("unsupported plugin admission policy version %d", p.PolicyVersion)
	}
	if !p.DenyByDefault {
		return errors.New("plugin admission policy must be deny-by-default")
	}
	seen := make(map[string]struct{}, len(p.Admissions))
	seenFolded := make(map[string]string, len(p.Admissions))
	for i, admission := range p.Admissions {
		if err := admission.Validate(); err != nil {
			return fmt.Errorf("admission[%d]: %w", i, err)
		}
		if _, exists := seen[admission.PluginID]; exists {
			return fmt.Errorf("duplicate plugin ID %q", admission.PluginID)
		}
		folded := strings.ToLower(admission.PluginID)
		if previous, exists := seenFolded[folded]; exists {
			return fmt.Errorf("plugin IDs %q and %q collide case-insensitively", previous, admission.PluginID)
		}
		seen[admission.PluginID] = struct{}{}
		seenFolded[folded] = admission.PluginID
	}
	return nil
}

// Validate checks one exact admission record.
func (a PluginAdmission) Validate() error {
	if !pluginIDPattern.MatchString(a.PluginID) {
		return fmt.Errorf("plugin ID %q is invalid", a.PluginID)
	}
	for field, value := range map[string]string{
		"source_id": a.SourceID, "repository": a.Repository, "exact_tag": a.ExactTag,
		"artifact_name": a.ArtifactName, "source_commit": a.SourceCommit,
		"provenance_type": a.ProvenanceType, "provenance_digest": a.ProvenanceDigest,
		"sbom_digest": a.SBOMDigest, "license_spdx": a.LicenseSPDX,
		"license_text_sha256": a.LicenseTextSHA256, "notice_sha256": a.NoticeSHA256,
		"license_approval_ref": a.LicenseApprovalRef, "cpa_exact_build_digest": a.CPAExactBuildDigest,
		"cpa_version": a.CPAVersion, "goos": a.GOOS, "goarch": a.GOARCH,
		"variant": a.Variant, "file_extension": a.FileExtension,
		"canary_profile": a.CanaryProfile, "rollback_artifact_sha256": a.RollbackArtifactSHA256,
		"change_ref": a.ChangeRef, "config_schema_sha256": a.ConfigSchemaSHA256,
		"management_routes_sha256": a.ManagementRoutesSHA256, "resource_routes_sha256": a.ResourceRoutesSHA256,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
		if containsWildcardOrRange(value) {
			return fmt.Errorf("%s must be exact, not a wildcard or range", field)
		}
	}
	if strings.ContainsAny(a.Author, "\r\n\t") {
		return errors.New("author must not contain control characters")
	}
	if !commitPattern.MatchString(a.SourceCommit) {
		return errors.New("source_commit must be a hexadecimal commit digest")
	}
	if !digestPattern.MatchString(a.ArtifactSHA256) || !digestPattern.MatchString(a.SourceArchiveSHA256) || !digestPattern.MatchString(a.ProvenanceDigest) || !digestPattern.MatchString(a.SBOMDigest) || !digestPattern.MatchString(a.LicenseTextSHA256) || !digestPattern.MatchString(a.NoticeSHA256) || !digestPattern.MatchString(a.CPAExactBuildDigest) || !digestPattern.MatchString(a.ConfigSchemaSHA256) || !digestPattern.MatchString(a.ManagementRoutesSHA256) || !digestPattern.MatchString(a.ResourceRoutesSHA256) || !digestPattern.MatchString(a.RollbackArtifactSHA256) {
		return errors.New("all artifact, provenance, license, build, config and route digests must be 64 lowercase hex characters")
	}
	if a.ArtifactSize <= 0 || a.ArtifactSize > 2<<30 {
		return errors.New("artifact_size must be between 1 byte and 2 GiB")
	}
	if path.Base(a.ArtifactName) != a.ArtifactName || strings.ContainsAny(a.ArtifactName, `/\\`) {
		return errors.New("artifact_name must be a single filename")
	}
	if !strings.HasSuffix(a.ArtifactName, a.FileExtension) || strings.TrimSuffix(a.ArtifactName, a.FileExtension) != a.PluginID {
		return errors.New("artifact filename stem must equal plugin_id and extension")
	}
	if a.FileExtension != ".so" && a.FileExtension != ".dylib" && a.FileExtension != ".dll" {
		return errors.New("file_extension must be .so, .dylib, or .dll")
	}
	if a.ExactTag == "latest" || a.ExactTag == "main" || a.ExactTag == "master" || a.ExactTag == "HEAD" || !versionPattern.MatchString(a.ExactTag) {
		return errors.New("exact_tag must identify one immutable release tag")
	}
	if strings.EqualFold(a.ProvenanceType, "unsigned") || strings.EqualFold(a.ProvenanceType, "opaque") {
		return errors.New("unsigned opaque native artifacts are not admissible")
	}
	if a.ProvenanceType != "signed" && a.ProvenanceType != "source_pinned_rebuild" {
		return errors.New("provenance_type must be signed or source_pinned_rebuild")
	}
	if !commitPattern.MatchString(a.SourceCommit) {
		return errors.New("source_commit must be a pinned commit")
	}
	if err := validateHTTPSRepository(a.Repository); err != nil {
		return err
	}
	if a.ABISchemaVersion <= 0 {
		return errors.New("abi_schema_version must be positive")
	}
	if !validGOOS(a.GOOS) || !validGOARCH(a.GOARCH) {
		return errors.New("goos/goarch must identify one concrete target")
	}
	if containsWildcardOrRange(a.Variant) || strings.EqualFold(a.Variant, "any") {
		return errors.New("variant must identify one concrete target")
	}
	if len(a.DeclaredCapabilities) == 0 {
		return errors.New("declared_capabilities must not be empty")
	}
	if err := validateCapabilities(a.DeclaredCapabilities, "declared"); err != nil {
		return err
	}
	if err := validateCapabilities(a.AllowedCapabilities, "allowed"); err != nil {
		return err
	}
	if err := validateCapabilities(a.ForbiddenCapabilities, "forbidden"); err != nil {
		return err
	}
	declared := stringSet(a.DeclaredCapabilities)
	allowed := stringSet(a.AllowedCapabilities)
	for capability := range allowed {
		if _, ok := declared[capability]; !ok {
			return fmt.Errorf("allowed capability %q is not declared", capability)
		}
	}
	for capability := range stringSet(a.ForbiddenCapabilities) {
		if _, ok := declared[capability]; ok {
			return fmt.Errorf("capability %q is both declared and forbidden", capability)
		}
	}
	if a.Prerelease || a.AutoUpdate || a.Latest || a.ManualUnverified {
		return errors.New("prerelease, latest, manual-unverified and auto-update flags must all be false")
	}
	if a.UnsandboxedResidualRisk {
		// The field is derived and must not be supplied as a trust assertion.
		// Accepting true is harmless, but callers cannot use it to bypass any
		// other validation.
	}
	return nil
}

var knownCapabilities = map[string]struct{}{
	"metadata.model.register": {}, "metadata.model.read": {},
	"auth.provider": {}, "frontend.auth": {}, "command.line": {},
	"host.auth.get": {}, "host.auth.save": {}, "scheduler": {},
	"router": {}, "executor": {}, "request.intercept": {},
	"response.intercept": {}, "stream.intercept": {}, "usage": {},
	"management.route": {}, "resource.route": {},
}

func validateCapabilities(values []string, label string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || strings.ToLower(value) != value {
			return fmt.Errorf("%s capability %q is not canonical", label, value)
		}
		if _, ok := knownCapabilities[value]; !ok {
			return fmt.Errorf("unknown %s capability %q", label, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("duplicate %s capability %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func containsWildcardOrRange(value string) bool {
	if strings.ContainsAny(value, `*^~<>=|,`) || strings.Contains(value, "..") || strings.ContainsAny(value, "\r\n\t") || strings.Contains(value, " latest") {
		return true
	}
	// Semver wildcards such as 1.x, 1.2.x and partial ranges are not exact.
	for _, part := range strings.Split(value, ".") {
		if strings.EqualFold(part, "x") {
			return true
		}
	}
	return false
}

func validateHTTPSRepository(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("repository must be an HTTPS URL without credentials, query or fragment")
	}
	if strings.ContainsAny(u.Host, "*\\/\r\n\t ") || u.Hostname() != strings.ToLower(u.Hostname()) {
		return errors.New("repository host must be one lowercase exact host without wildcard")
	}
	return nil
}

func validGOOS(value string) bool {
	switch value {
	case "linux", "darwin", "windows":
		return true
	default:
		return false
	}
}

func validGOARCH(value string) bool {
	switch value {
	case "amd64", "arm64", "386", "arm":
		return true
	default:
		return false
	}
}

// rejectDuplicateJSONKeys performs a token-level walk before encoding/json
// decodes into structs.  encoding/json otherwise silently accepts duplicate
// keys (last value wins), which is unsafe for an immutable policy.
func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(dec); err != nil {
		return fmt.Errorf("invalid policy JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("policy JSON has trailing values")
		}
		return fmt.Errorf("invalid trailing policy JSON: %w", err)
	}
	return nil
}

// topLevelBoolField distinguishes an omitted deny_by_default from an
// explicitly supplied false value without weakening the exported struct's
// ergonomics for direct callers.
func topLevelBoolField(data []byte, wanted string) (bool, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return false, false
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return false, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return false, false
		}
		if key == wanted {
			var value bool
			if err := dec.Decode(&value); err != nil {
				return true, false
			}
			return true, value
		}
		var ignored any
		if err := dec.Decode(&ignored); err != nil {
			return false, false
		}
	}
	return false, false
}

func topLevelFieldPresent(data []byte, wanted string) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := keyToken.(string)
		if !ok {
			return false
		}
		if key == wanted {
			return true
		}
		var ignored any
		if err := dec.Decode(&ignored); err != nil {
			return false
		}
	}
	return false
}

func topLevelRawField(data []byte, wanted string) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		if key == wanted {
			return raw, true
		}
	}
	return nil, false
}

// jsonHasKey detects a key at any nesting level, including escaped key names,
// so derived trust annotations cannot bypass the wire check with \u escapes.
func jsonHasKey(data []byte, wanted string) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	return jsonHasKeyDecoder(dec, wanted)
}

func jsonHasKeyDecoder(dec *json.Decoder, wanted string) bool {
	token, err := dec.Token()
	if err != nil {
		return false
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return false
				}
				key, ok := keyToken.(string)
				if !ok {
					return false
				}
				if key == wanted {
					return true
				}
				if jsonHasKeyDecoder(dec, wanted) {
					return true
				}
			}
			_, _ = dec.Token()
		case '[':
			for dec.More() {
				if jsonHasKeyDecoder(dec, wanted) {
					return true
				}
			}
			_, _ = dec.Token()
		}
	}
	return false
}

func walkJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walkJSONValue(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("unterminated JSON object")
			}
		case '[':
			for dec.More() {
				if err := walkJSONValue(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("unterminated JSON array")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	return nil
}

// sortedCopy is shared by inventory and evidence for deterministic output.
func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
