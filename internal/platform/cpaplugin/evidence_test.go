package cpaplugin

import (
	"strings"
	"testing"
	"time"
)

func TestAssessOfflineEmitsUnsignedCandidateNeverApproval(t *testing.T) {
	p := validAdmission()
	bundle := EvidenceBundleV1{Version: EvidenceVersionV1, ObservedAt: time.Now().UTC(), Target: TargetV1{InstanceID: "cpa-offline-1", BuildDigest: p.CPAExactBuildDigest, CPAVersion: p.CPAVersion, GOOS: p.GOOS, GOARCH: p.GOARCH, Variant: p.Variant}, GlobalPluginsEnabled: false, Physical: PhysicalSnapshot{Complete: true}, Config: ConfigProjection{Complete: true}, Runtime: RuntimeProjection{Complete: true}}
	result, err := AssessOffline(map[string]PluginAdmission{p.PluginID: p}, []PluginStoreSource{validSource()}, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if result.Approved || !result.Unsigned || len(result.Candidates) != 1 || result.Candidates[0].Decision != "candidate" {
		t.Fatalf("assessment must remain unsigned candidate: %+v", result)
	}
}

func TestAssessOfflinePluginFreePassAndStaleTargetFailure(t *testing.T) {
	empty := EvidenceBundleV1{Version: EvidenceVersionV1, ObservedAt: time.Now().UTC(), Target: TargetV1{InstanceID: "cpa-offline-empty", BuildDigest: strings.Repeat("a", 64), CPAVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", Variant: "default"}, GlobalPluginsEnabled: false, Physical: PhysicalSnapshot{Complete: true}, Config: ConfigProjection{Complete: true}, Runtime: RuntimeProjection{Complete: true}}
	result, err := AssessOffline(nil, []PluginStoreSource{}, empty)
	if err != nil || result.Decision != "plugin_free_pass" || !result.PluginFreePass {
		t.Fatalf("plugin-free evidence should pass: %+v err=%v", result, err)
	}
	admission := validAdmission()
	result, err = AssessOffline(map[string]PluginAdmission{admission.PluginID: admission}, []PluginStoreSource{validSource()}, empty)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision == "plugin_free_pass" {
		t.Fatal("non-empty policy must not be plugin-free pass")
	}
}

func TestAssessOfflineRejectsSecretLookingEvidenceAndIncompleteBundle(t *testing.T) {
	bundle := EvidenceBundleV1{Version: EvidenceVersionV1, ObservedAt: time.Now().UTC(), Target: TargetV1{InstanceID: "cpa-offline-secret", BuildDigest: strings.Repeat("a", 64), CPAVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", Variant: "default"}, RawFields: map[string]string{"token": "redacted"}}
	if _, err := AssessOffline(nil, nil, bundle); err == nil {
		t.Fatal("secret-looking fields must be rejected")
	}
	bundle.RawFields = nil
	if _, err := AssessOffline(nil, nil, bundle); err == nil {
		t.Fatal("incomplete evidence must be rejected")
	}
}

func TestAssessOfflineRejectsSecretLookingValuesAndMissingFreshness(t *testing.T) {
	bundle := EvidenceBundleV1{Version: EvidenceVersionV1, ObservedAt: time.Now().UTC(), Target: TargetV1{InstanceID: "cpa-offline-value", BuildDigest: strings.Repeat("a", 64), CPAVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", Variant: "default"}, RawFields: map[string]string{"note": "Authorization: Bearer redacted"}}
	if _, err := AssessOffline(nil, []PluginStoreSource{}, bundle); err == nil {
		t.Fatal("secret-looking values must be rejected")
	}
	bundle.RawFields = nil
	bundle.ObservedAt = time.Time{}
	if _, err := AssessOffline(nil, []PluginStoreSource{}, bundle); err == nil {
		t.Fatal("missing freshness timestamp must be rejected")
	}
}

func TestAssessOfflineRequiresStoreProjectionForNonEmptyPolicy(t *testing.T) {
	p := validAdmission()
	bundle := EvidenceBundleV1{Version: EvidenceVersionV1, ObservedAt: time.Now().UTC(), Target: TargetV1{InstanceID: "cpa-offline-source", BuildDigest: p.CPAExactBuildDigest, CPAVersion: p.CPAVersion, GOOS: p.GOOS, GOARCH: p.GOARCH, Variant: p.Variant}, Physical: PhysicalSnapshot{Complete: true}, Config: ConfigProjection{Complete: true}, Runtime: RuntimeProjection{Complete: true}}
	if _, err := AssessOffline(map[string]PluginAdmission{p.PluginID: p}, nil, bundle); err == nil {
		t.Fatal("non-empty policy without store projection must fail closed")
	}
	if _, err := AssessOffline(map[string]PluginAdmission{p.PluginID: p}, []PluginStoreSource{}, bundle); err == nil {
		t.Fatal("non-empty policy with empty store projection must fail closed")
	}
}

func TestAssessOfflineSourceAndTargetDriftRejectCandidate(t *testing.T) {
	p := validAdmission()
	bundle := EvidenceBundleV1{Version: EvidenceVersionV1, ObservedAt: time.Now().UTC(), Target: TargetV1{InstanceID: "cpa-offline-drift", BuildDigest: strings.Repeat("f", 64), CPAVersion: "9.9.9", GOOS: p.GOOS, GOARCH: p.GOARCH, Variant: p.Variant}, Physical: PhysicalSnapshot{Complete: true}, Config: ConfigProjection{Complete: true}, Runtime: RuntimeProjection{Complete: true}}
	result, err := AssessOffline(map[string]PluginAdmission{p.PluginID: p}, []PluginStoreSource{validSource()}, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "fail" || len(result.Candidates) != 1 || result.Candidates[0].Decision != "rejected" {
		t.Fatalf("drift must reject candidate: %+v", result)
	}

	source := validSource()
	source.SourceID = "different"
	result, err = AssessOffline(map[string]PluginAdmission{p.PluginID: p}, []PluginStoreSource{source}, EvidenceBundleV1{Version: EvidenceVersionV1, ObservedAt: time.Now().UTC(), Target: TargetV1{InstanceID: "cpa-offline-source-drift", BuildDigest: p.CPAExactBuildDigest, CPAVersion: p.CPAVersion, GOOS: p.GOOS, GOARCH: p.GOARCH, Variant: p.Variant}, Physical: PhysicalSnapshot{Complete: true}, Config: ConfigProjection{Complete: true}, Runtime: RuntimeProjection{Complete: true}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != "fail" || result.Candidates[0].Decision != "rejected" {
		t.Fatalf("source drift must reject candidate: %+v", result)
	}
}

func TestLoadEvidenceBundleRequiresTopLevelCoverageFields(t *testing.T) {
	data := []byte(`{"version":1,"observed_at":"2026-08-30T00:00:00Z","target":{"instance_id":"cpa-1","build_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","cpa_version":"1.0.0","goos":"linux","goarch":"amd64","variant":"default"},"physical":{"complete":true,"files":[]},"config":{"complete":true,"plugins":[]},"runtime":{"complete":true,"plugins":[]}}`)
	if _, err := LoadEvidenceBundle(data); err == nil {
		t.Fatal("missing global_plugins_enabled must fail closed")
	}
}
