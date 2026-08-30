package cpaplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func validAdmission() PluginAdmission {
	h := strings.Repeat("a", sha256.Size*2)
	return PluginAdmission{
		PluginID: "safe-plugin", SourceID: "internal", Repository: "https://github.com/example/safe-plugin",
		ExactTag: "v1.2.3", ArtifactName: "safe-plugin.so", ArtifactSHA256: h, ArtifactSize: 123,
		SourceCommit: strings.Repeat("b", 40), SourceArchiveSHA256: h,
		ProvenanceType: "source_pinned_rebuild", ProvenanceDigest: h, SBOMDigest: h,
		LicenseSPDX: "MIT", LicenseTextSHA256: h, NoticeSHA256: h, LicenseApprovalRef: "CR-214-safe-plugin",
		CPAExactBuildDigest: h, CPAVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", Variant: "default", FileExtension: ".so", ABISchemaVersion: 1,
		DeclaredCapabilities: []string{"metadata.model.register"}, AllowedCapabilities: []string{"metadata.model.register"},
		ConfigSchemaSHA256: h, ManagementRoutesSHA256: h, ResourceRoutesSHA256: h,
		CanaryProfile: "metadata-only", RollbackArtifactSHA256: h, ChangeRef: "CR-214-safe-plugin",
	}
}

func policyJSON(t *testing.T, admissions []PluginAdmission) []byte {
	t.Helper()
	if admissions == nil {
		admissions = []PluginAdmission{}
	}
	b, err := json.Marshal(AdmissionPolicyV1{PolicyVersion: PolicyVersionV1, DenyByDefault: true, Admissions: admissions})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPolicyRequiresExactSourceTagArtifactSizeAndSHA(t *testing.T) {
	p := validAdmission()
	p.ExactTag = "latest"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("latest tag must be rejected")
	}
	p = validAdmission()
	p.ArtifactSHA256 = "not-a-digest"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("invalid artifact digest must be rejected")
	}
}

func TestPolicyRequiresOfficialPluginIDAndFilenameStem(t *testing.T) {
	p := validAdmission()
	p.PluginID = "bad id"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("invalid plugin id must be rejected")
	}
	p = validAdmission()
	p.ArtifactName = "other.so"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("filename stem mismatch must be rejected")
	}
}

func TestPolicyRejectsLatestRangePrereleaseManualFallbackAndAutoUpdate(t *testing.T) {
	for name, mutate := range map[string]func(*PluginAdmission){
		"prerelease":  func(p *PluginAdmission) { p.Prerelease = true },
		"auto-update": func(p *PluginAdmission) { p.AutoUpdate = true },
		"latest":      func(p *PluginAdmission) { p.Latest = true },
		"manual":      func(p *PluginAdmission) { p.ManualUnverified = true },
		"range":       func(p *PluginAdmission) { p.ExactTag = ">=1.0" },
	} {
		t.Run(name, func(t *testing.T) {
			p := validAdmission()
			mutate(&p)
			if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

func TestPolicyRequiresSignatureOrApprovedSourcePinnedRebuildAndSBOM(t *testing.T) {
	p := validAdmission()
	p.ProvenanceType = "unsigned"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("unsigned opaque artifact must be rejected")
	}
	p = validAdmission()
	p.SBOMDigest = ""
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("missing SBOM must be rejected")
	}
}

func TestPolicyRequiresLicenseTextNoticeAndHumanApprovalRef(t *testing.T) {
	for name, mutate := range map[string]func(*PluginAdmission){
		"license":      func(p *PluginAdmission) { p.LicenseSPDX = "" },
		"license-hash": func(p *PluginAdmission) { p.LicenseTextSHA256 = "" },
		"notice-hash":  func(p *PluginAdmission) { p.NoticeSHA256 = "" },
		"approval":     func(p *PluginAdmission) { p.LicenseApprovalRef = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p := validAdmission()
			mutate(&p)
			if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
				t.Fatalf("missing %s must be rejected", name)
			}
		})
	}
}

func TestPolicyPinsExactCPABuildOSArchVariantExtensionAndABI(t *testing.T) {
	p := validAdmission()
	p.GOARCH = "any"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("wildcard architecture must be rejected")
	}
	p = validAdmission()
	p.FileExtension = ".zip"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("non-native extension must be rejected")
	}
}

func TestPolicyRejectsUnknownExtraAndForbiddenCapabilities(t *testing.T) {
	p := validAdmission()
	p.DeclaredCapabilities = []string{"unknown.capability"}
	p.AllowedCapabilities = []string{"unknown.capability"}
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("unknown capability must be rejected")
	}
	p = validAdmission()
	p.ForbiddenCapabilities = []string{"metadata.model.register"}
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("declared forbidden capability must be rejected")
	}
}

func TestPolicyTreatsNativePluginAsUnsandboxedResidualRisk(t *testing.T) {
	p := validAdmission()
	loaded, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p}))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded[p.PluginID]
	if !ok || !got.UnsandboxedResidualRisk {
		t.Fatal("native admission must expose unsandboxed residual risk")
	}
}

func TestOneAdmissionCannotWildcardEnvironmentInstanceVersionOrPlatform(t *testing.T) {
	p := validAdmission()
	p.CPAVersion = "1.x"
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("version range must be rejected")
	}
}

func TestEmptyPolicyIsValidAndDigestStable(t *testing.T) {
	data := policyJSON(t, nil)
	first, digest, err := LoadAdmissionPolicy(data)
	if err != nil || len(first) != 0 || len(digest) != sha256.Size*2 {
		t.Fatalf("empty policy: map=%v digest=%q err=%v", first, digest, err)
	}
	second, digest2, err := LoadAdmissionPolicy(data)
	if err != nil || digest != digest2 || len(second) != 0 {
		t.Fatalf("unstable empty policy digest: %q vs %q", digest, digest2)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyRejectsUnknownAndDuplicateJSONFields(t *testing.T) {
	unknown := []byte(`{"policy_version":1,"admissions":[],"unexpected":true}`)
	if _, _, err := LoadAdmissionPolicy(unknown); err == nil {
		t.Fatal("unknown field must be rejected")
	}
	duplicate := []byte(`{"policy_version":1,"policy_version":1,"admissions":[]}`)
	if _, _, err := LoadAdmissionPolicy(duplicate); err == nil {
		t.Fatal("duplicate field must be rejected")
	}
}

func TestPolicyRejectsExplicitNonDenyDefaultAndDerivedRiskInput(t *testing.T) {
	if _, _, err := LoadAdmissionPolicy([]byte(`{"policy_version":1,"deny_by_default":false,"admissions":[]}`)); err == nil {
		t.Fatal("explicit deny_by_default=false must fail closed")
	}
	p := validAdmission()
	data := strings.Replace(string(policyJSON(t, []PluginAdmission{p})), `"admissions":[`, `"admissions":[`, 1)
	data = strings.TrimSuffix(data, "}") + `,"unsandboxed_residual_risk":true}`
	if _, _, err := LoadAdmissionPolicy([]byte(data)); err == nil {
		t.Fatal("derived residual risk input must be rejected")
	}
	escaped := strings.Replace(data, "unsandboxed_residual_risk", "\\u0075nsandboxed_residual_risk", 1)
	if _, _, err := LoadAdmissionPolicy([]byte(escaped)); err == nil {
		t.Fatal("escaped derived residual risk input must be rejected")
	}
}

func TestPolicyRejectsUppercaseSourceCommit(t *testing.T) {
	p := validAdmission()
	p.SourceCommit = strings.Repeat("A", 40)
	if _, _, err := LoadAdmissionPolicy(policyJSON(t, []PluginAdmission{p})); err == nil {
		t.Fatal("source commit must use lowercase canonical hex")
	}
}

func TestPolicyRequiresTopLevelAdmissionsField(t *testing.T) {
	if _, _, err := LoadAdmissionPolicy([]byte(`{"policy_version":1}`)); err == nil {
		t.Fatal("missing admissions field must fail closed")
	}
	if _, _, err := LoadAdmissionPolicy([]byte(`{"policy_version":1,"admissions":[]}`)); err == nil {
		t.Fatal("missing deny_by_default field must fail closed")
	}
}

func TestPolicyDigestIsIndependentOfAdmissionOrder(t *testing.T) {
	a, b := validAdmission(), validAdmission()
	b.PluginID = "other-plugin"
	b.ArtifactName = "other-plugin.so"
	first := AdmissionPolicyV1{PolicyVersion: PolicyVersionV1, DenyByDefault: true, Admissions: []PluginAdmission{a, b}}
	second := AdmissionPolicyV1{PolicyVersion: PolicyVersionV1, DenyByDefault: true, Admissions: []PluginAdmission{b, a}}
	data1, _ := json.Marshal(first)
	data2, _ := json.Marshal(second)
	_, digest1, err := LoadAdmissionPolicy(data1)
	if err != nil {
		t.Fatal(err)
	}
	_, digest2, err := LoadAdmissionPolicy(data2)
	if err != nil {
		t.Fatal(err)
	}
	if digest1 != digest2 {
		t.Fatalf("canonical policy digest depends on order: %s vs %s", digest1, digest2)
	}
	b.ArtifactSize++
	data3, _ := json.Marshal(AdmissionPolicyV1{PolicyVersion: PolicyVersionV1, DenyByDefault: true, Admissions: []PluginAdmission{a, b}})
	_, digest3, err := LoadAdmissionPolicy(data3)
	if err != nil {
		t.Fatal(err)
	}
	if digest3 == digest1 {
		t.Fatal("policy digest must cover all admission fields")
	}
}
