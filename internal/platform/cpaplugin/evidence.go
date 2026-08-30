package cpaplugin

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
	"time"
)

const EvidenceVersionV1 = 1

type TargetV1 struct {
	InstanceID  string `json:"instance_id,omitempty"`
	BuildDigest string `json:"build_digest"`
	CPAVersion  string `json:"cpa_version"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	Variant     string `json:"variant"`
}

// EvidenceBundleV1 is an explicit local evidence bundle.  It is data supplied
// to the CLI; no constructor in this package reads a path or contacts a
// service.  RawFields exists only for redaction checks and is never echoed in
// assessment output.
type EvidenceBundleV1 struct {
	Version              int               `json:"version"`
	ObservedAt           time.Time         `json:"observed_at,omitempty"`
	Target               TargetV1          `json:"target"`
	GlobalPluginsEnabled bool              `json:"global_plugins_enabled"`
	Physical             PhysicalSnapshot  `json:"physical"`
	Config               ConfigProjection  `json:"config"`
	Runtime              RuntimeProjection `json:"runtime"`
	RawFields            map[string]string `json:"raw_fields,omitempty"`
}

type UnsignedCandidate struct {
	PluginID                string `json:"plugin_id"`
	SourceID                string `json:"source_id"`
	ArtifactSHA256          string `json:"artifact_sha256"`
	UnsandboxedResidualRisk bool   `json:"unsandboxed_residual_risk"`
	Decision                string `json:"decision"`
	Reason                  string `json:"reason"`
	Approved                bool   `json:"approved"`
}

type AssessmentResultV1 struct {
	Version        int                 `json:"version"`
	Decision       string              `json:"decision"`
	Approved       bool                `json:"approved"`
	Unsigned       bool                `json:"unsigned"`
	PluginFreePass bool                `json:"plugin_free_pass"`
	PolicySHA256   string              `json:"policy_sha256"`
	EvidenceSHA256 string              `json:"evidence_sha256"`
	Inventory      PluginInventory     `json:"inventory"`
	Findings       []Finding           `json:"findings"`
	Candidates     []UnsignedCandidate `json:"candidates"`
}

// AssessOffline performs policy/evidence comparison only.  It does not
// resolve CredentialRef, fetch source metadata, connect to CPA, or execute a
// candidate.  A non-empty policy always yields unsigned candidates with
// Approved=false, even if every offline check is clean.
func AssessOffline(policyValue any, sources []PluginStoreSource, bundle EvidenceBundleV1) (AssessmentResultV1, error) {
	policy, err := normalizePolicy(policyValue)
	if err != nil {
		return AssessmentResultV1{}, err
	}
	if err := bundle.validate(); err != nil {
		return AssessmentResultV1{}, err
	}
	if len(policy) > 0 && len(sources) == 0 {
		return AssessmentResultV1{}, errors.New("at least one store source is required for a non-empty admission policy")
	}
	if len(policy) == 0 && sources == nil {
		// An empty policy has no configured store sources; represent that
		// explicit empty projection as complete rather than partial.
		sources = []PluginStoreSource{}
	}
	for index, source := range sources {
		if err := source.Validate(); err != nil {
			return AssessmentResultV1{}, fmt.Errorf("source[%d]: %w", index, err)
		}
	}
	sourceIDs := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		sourceIDs[source.SourceID] = struct{}{}
	}
	canonicalPolicy := canonicalPolicyDigest(policy)
	bundle.Config.GlobalEnabled = bundle.GlobalPluginsEnabled
	inv, err := JoinInventory(policy, bundle.Physical, bundle.Config, bundle.Runtime, sources)
	if err != nil {
		return AssessmentResultV1{}, err
	}
	result := AssessmentResultV1{Version: EvidenceVersionV1, Unsigned: len(policy) > 0, Approved: false, PolicySHA256: canonicalPolicy, EvidenceSHA256: evidenceDigest(bundle), Inventory: inv, Findings: append([]Finding(nil), inv.Findings...)}
	for id, admission := range policy {
		if len(sources) > 0 {
			if _, ok := sourceIDs[admission.SourceID]; !ok {
				result.Findings = append(result.Findings, Finding{Code: "source_missing", PluginID: id, Severity: "error", Message: "admission source is absent from supplied source policy"})
			}
		}
		if bundle.Target.BuildDigest != admission.CPAExactBuildDigest || bundle.Target.CPAVersion != admission.CPAVersion || bundle.Target.GOOS != admission.GOOS || bundle.Target.GOARCH != admission.GOARCH || bundle.Target.Variant != admission.Variant {
			result.Findings = append(result.Findings, Finding{Code: "target_drift", PluginID: id, Severity: "error", Message: "evidence target does not match exact admission target"})
		}
		candidate := UnsignedCandidate{PluginID: id, SourceID: admission.SourceID, ArtifactSHA256: admission.ArtifactSHA256, UnsandboxedResidualRisk: true, Decision: "candidate", Reason: "offline evidence only; human admission and canary are still required", Approved: false}
		for _, finding := range result.Findings {
			if finding.PluginID == id && finding.Severity == "error" {
				candidate.Decision = "rejected"
				candidate.Reason = finding.Message
				break
			}
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	sort.Slice(result.Candidates, func(i, j int) bool { return result.Candidates[i].PluginID < result.Candidates[j].PluginID })
	if len(policy) == 0 {
		result.PluginFreePass = inv.PluginFreePass
		if result.PluginFreePass {
			result.Decision = "plugin_free_pass"
		} else {
			result.Decision = "fail"
		}
	} else {
		result.PluginFreePass = false
		result.Decision = "candidate"
		for _, finding := range result.Findings {
			if finding.Severity == "error" {
				result.Decision = "fail"
				break
			}
		}
	}
	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].Code != result.Findings[j].Code {
			return result.Findings[i].Code < result.Findings[j].Code
		}
		if result.Findings[i].PluginID != result.Findings[j].PluginID {
			return result.Findings[i].PluginID < result.Findings[j].PluginID
		}
		return result.Findings[i].Message < result.Findings[j].Message
	})
	return result, nil
}

func Assess(policyValue any, sources []PluginStoreSource, bundle EvidenceBundleV1) (AssessmentResultV1, error) {
	return AssessOffline(policyValue, sources, bundle)
}

// LoadEvidenceBundle decodes one explicit local evidence file. Unknown and
// duplicate JSON fields are rejected before decoding. raw_fields is accepted
// only so the redaction checker can fail closed and is never echoed by an
// assessment result.
func LoadEvidenceBundle(data []byte) (EvidenceBundleV1, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return EvidenceBundleV1{}, errors.New("empty evidence bundle")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return EvidenceBundleV1{}, err
	}
	for _, field := range []string{"version", "observed_at", "target", "global_plugins_enabled", "physical", "config", "runtime"} {
		if !topLevelFieldPresent(data, field) {
			return EvidenceBundleV1{}, fmt.Errorf("evidence bundle is missing %s", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var bundle EvidenceBundleV1
	if err := dec.Decode(&bundle); err != nil {
		return EvidenceBundleV1{}, fmt.Errorf("decode evidence bundle: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return EvidenceBundleV1{}, errors.New("evidence bundle has trailing JSON")
		}
		return EvidenceBundleV1{}, fmt.Errorf("decode trailing evidence data: %w", err)
	}
	if err := bundle.validate(); err != nil {
		return EvidenceBundleV1{}, err
	}
	return bundle, nil
}

func canonicalPolicyDigest(policy map[string]PluginAdmission) string {
	ids := make([]string, 0, len(policy))
	for id := range policy {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	admissions := make([]PluginAdmission, 0, len(ids))
	for _, id := range ids {
		admission := policy[id]
		admission.UnsandboxedResidualRisk = true
		admissions = append(admissions, admission)
	}
	canonical, err := json.Marshal(AdmissionPolicyV1{PolicyVersion: PolicyVersionV1, DenyByDefault: true, Admissions: admissions})
	if err != nil {
		// The values were validated before reaching this function.  Keep a
		// deterministic failure digest rather than exposing an internal error.
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func evidenceDigest(bundle EvidenceBundleV1) string {
	// RawFields is intentionally excluded: it is only a redaction sentinel and
	// may contain values that must never enter a digest or output artifact.
	bundle.RawFields = nil
	canonical, err := json.Marshal(bundle)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func (b EvidenceBundleV1) validate() error {
	if b.Version != EvidenceVersionV1 {
		return fmt.Errorf("unsupported evidence bundle version %d", b.Version)
	}
	if err := validateTarget(b.Target); err != nil {
		return err
	}
	if len(b.RawFields) != 0 {
		for key, value := range b.RawFields {
			if looksSecretLike(key) || looksSecretLikeValue(value) {
				return errors.New("evidence contains a secret-looking field")
			}
		}
	}
	if !b.Physical.Complete || !b.Config.Complete || !b.Runtime.Complete {
		return errors.New("evidence bundle is incomplete: physical, config and runtime coverage are required")
	}
	if b.ObservedAt.IsZero() {
		return errors.New("evidence observed_at is required")
	}
	if b.ObservedAt.After(time.Now().UTC().Add(5 * time.Minute)) {
		return errors.New("evidence observed_at is in the future")
	}
	if _, offset := b.ObservedAt.Zone(); offset != 0 {
		return errors.New("evidence observed_at must be UTC")
	}
	return nil
}

func validateTarget(target TargetV1) error {
	if strings.TrimSpace(target.InstanceID) == "" || len(target.InstanceID) > 128 || strings.ContainsAny(target.InstanceID, "\r\n\t/\\") {
		return errors.New("target instance_id is required and must be an opaque identifier")
	}
	if !digestPattern.MatchString(target.BuildDigest) {
		return errors.New("target build_digest must be 64 lowercase hex characters")
	}
	if target.CPAVersion == "" || containsWildcardOrRange(target.CPAVersion) || !versionPattern.MatchString(target.CPAVersion) {
		return errors.New("target cpa_version must be one exact version")
	}
	if !validGOOS(target.GOOS) || !validGOARCH(target.GOARCH) || target.Variant == "" || containsWildcardOrRange(target.Variant) {
		return errors.New("target goos/goarch/variant must be concrete")
	}
	return nil
}

func looksSecretLike(key string) bool {
	lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, token := range []string{"token", "password", "passwd", "secret", "authorization", "cookie", "api_key", "private_key", "client_secret", "credential"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func looksSecretLikeValue(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "basic ", "password=", "passwd=", "secret=", "api_key=", "authorization:", "cookie:", "-----begin ", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
