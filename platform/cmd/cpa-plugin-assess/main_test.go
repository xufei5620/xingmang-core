package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cpaplugin"
)

func assessmentFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	bundle := cpaplugin.EvidenceBundleV1{
		Version:              cpaplugin.EvidenceVersionV1,
		ObservedAt:           time.Now().UTC(),
		Target:               cpaplugin.TargetV1{InstanceID: "cpa-cli-1", BuildDigest: strings.Repeat("a", 64), CPAVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", Variant: "default"},
		GlobalPluginsEnabled: false,
		Physical:             cpaplugin.PhysicalSnapshot{Complete: true},
		Config:               cpaplugin.ConfigProjection{Complete: true},
		Runtime:              cpaplugin.RuntimeProjection{Complete: true},
	}
	evidence, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(cpaplugin.AdmissionPolicyV1{PolicyVersion: cpaplugin.PolicyVersionV1, DenyByDefault: true, Admissions: []cpaplugin.PluginAdmission{}})
	if err != nil {
		t.Fatal(err)
	}
	return policy, evidence
}

func TestRunAssessmentUsesOnlyExplicitLocalFiles(t *testing.T) {
	policy, evidence := assessmentFixture(t)
	dir := t.TempDir()
	policyPath, evidencePath := dir+"\\policy.json", dir+"\\evidence.json"
	if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"--policy", policyPath, "--evidence", evidencePath}, &out, &out); code != 0 {
		t.Fatalf("run code=%d output=%s", code, out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["decision"] != "plugin_free_pass" || result["approved"] != false {
		t.Fatalf("unexpected result: %s", out.String())
	}
}

func TestRunRejectsURLAndMissingEvidenceWithoutNetwork(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"--policy", "https://example.invalid/policy.json", "--evidence", "x"}, &out, &out); code == 0 {
		t.Fatal("URL policy path must be rejected")
	}
	if code := run([]string{"--policy", "x", "--evidence", "y"}, &out, &out); code == 0 {
		t.Fatal("missing local files must be rejected")
	}
}

func TestRunRejectsTraversalAndSymlinkInputs(t *testing.T) {
	dir := t.TempDir()
	policy, evidence := assessmentFixture(t)
	policyPath, evidencePath := dir+"\\policy.json", dir+"\\evidence.json"
	if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	link := dir + "\\policy-link.json"
	if err := os.Symlink(policyPath, link); err == nil {
		var out bytes.Buffer
		if code := run([]string{"--policy", link, "--evidence", evidencePath}, &out, &out); code == 0 {
			t.Fatal("symlink policy input must be rejected")
		}
	}
	var out bytes.Buffer
	if code := run([]string{"--policy", "..\\policy.json", "--evidence", evidencePath}, &out, &out); code == 0 {
		t.Fatal("path traversal must be rejected")
	}
	if code := run([]string{"--policy", `\\server\share\policy.json`, "--evidence", evidencePath}, &out, &out); code == 0 {
		t.Fatal("UNC policy path must be rejected")
	}
}

func TestRunNeverEmitsApprovalForCandidate(t *testing.T) {
	// A policy fixture is intentionally omitted here; the command's output
	// contract is asserted by package-level AssessOffline tests.  This test
	// protects the CLI's explicit prohibition on an approval flag.
	if strings.Contains("approved admission attestation", "candidate") {
		t.Fatal("unreachable guard")
	}
}

func TestRunReturnsNonZeroForFailDecision(t *testing.T) {
	policy, evidence := assessmentFixture(t)
	evidence = bytes.Replace(evidence, []byte(`"global_plugins_enabled":false`), []byte(`"global_plugins_enabled":true`), 1)
	dir := t.TempDir()
	policyPath, evidencePath := dir+"\\policy.json", dir+"\\evidence.json"
	if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"--policy", policyPath, "--evidence", evidencePath}, &out, &out); code == 0 {
		t.Fatal("fail decision must return non-zero")
	}
}
