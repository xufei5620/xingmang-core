package dbroles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDefaultPolicyFreezesRoleTopologyAndLeastPrivilege(t *testing.T) {
	policy := DefaultPolicyV1()
	if policy.Version != PolicyVersionV1 {
		t.Fatalf("policy version=%d, want %d", policy.Version, PolicyVersionV1)
	}
	if policy.DatabaseSelector != "current_database" || policy.ProductionDatabaseName != "xingmang" {
		t.Fatalf("database selector/name not frozen: %#v", policy)
	}
	if policy.Owner != "xm_migrator" {
		t.Fatalf("owner=%q", policy.Owner)
	}
	wantCapabilities := []string{"xm_api_runtime", "xm_worker_runtime", "xm_lifecycle_runtime", "xm_ops_read", "xm_backup_read"}
	if !slices.Equal(policy.CapabilityRoles, wantCapabilities) {
		t.Fatalf("capability roles=%v, want %v", policy.CapabilityRoles, wantCapabilities)
	}
	for _, capability := range wantCapabilities {
		role, ok := policy.Roles[capability]
		if !ok {
			t.Fatalf("missing capability role %q", capability)
		}
		if role.Login || role.Superuser || role.CreateRole || role.CreateDB || role.Replication || role.BypassRLS {
			t.Fatalf("capability %q has unsafe attributes: %+v", capability, role)
		}
		if got := policy.Rotation(capability); got.State != RotationSteadyA || got.SteadyIdentity == "" {
			t.Fatalf("capability %q rotation=%+v", capability, got)
		}
	}
	for _, identity := range []string{"xm_api_a", "xm_worker_a", "xm_lifecycle_a", "xm_ops_a", "xm_backup_a"} {
		role, ok := policy.Roles[identity]
		if !ok || !role.Login || role.Superuser || role.CreateRole || role.CreateDB || role.Replication || role.BypassRLS || !role.Inherit {
			t.Fatalf("unsafe/missing login identity %q: %+v", identity, role)
		}
	}
	if got := policy.Grant("audit", "audit_event", "xm_api_runtime"); !slices.Equal(got, []string{"INSERT", "SELECT"}) {
		t.Fatalf("audit_event API grant=%v", got)
	}
	if policy.Allows("xm_worker_runtime", "action.action_run", "INSERT") {
		t.Fatal("worker must not write ActionRun")
	}
	if policy.AllowsTable("xm_lifecycle_runtime", "audit.chain_root", "UPDATE") {
		t.Fatal("chain_root table UPDATE must be absent")
	}
	if !policy.AllowsColumns("xm_lifecycle_runtime", "audit.chain_root", "UPDATE", []string{"exported_at", "export_target"}) {
		t.Fatal("chain_root lifecycle column UPDATE must be exact")
	}
	if policy.AllowsColumns("xm_lifecycle_runtime", "audit.chain_root", "UPDATE", []string{"root_hash"}) {
		t.Fatal("chain_root protected column UPDATE must be absent")
	}
	if policy.Allows("PUBLIC", "public.river_job_state", "USAGE") {
		t.Fatal("PUBLIC type USAGE must be revoked")
	}
	if policy.PublicSchemaOwner != "pg_database_owner" || policy.CustomSchemaDefault != "deny" || policy.PublicSchemaContract != "river-only" {
		t.Fatalf("public/default contract drift: %+v", policy)
	}
}

func TestLoadPolicyStrictlyRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	valid := policyJSON(t, DefaultPolicyV1())
	unknown := append(bytes.TrimSuffix(valid, []byte("}")), []byte(`,"unexpected":true}`)...)
	if _, _, err := LoadPolicy(unknown); err == nil {
		t.Fatal("unknown policy field accepted")
	}
	duplicate := bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
	if _, _, err := LoadPolicy(duplicate); err == nil {
		t.Fatal("duplicate policy field accepted")
	}
	trailing := append(append([]byte(nil), valid...), []byte(` {}`)...)
	if _, _, err := LoadPolicy(trailing); err == nil {
		t.Fatal("trailing policy JSON accepted")
	}
	if _, _, err := LoadPolicy([]byte(`{"version":1}`)); err == nil {
		t.Fatal("incomplete policy accepted")
	}
}

func TestLoadPolicyAcceptsLegacyStringVersionOnlyAsVersionOne(t *testing.T) {
	valid := policyJSON(t, DefaultPolicyV1())
	legacy := bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":"1"`), 1)
	if _, _, err := LoadPolicy(legacy); err != nil {
		t.Fatalf("legacy version spelling should normalize: %v", err)
	}
	bad := bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":"2"`), 1)
	if _, _, err := LoadPolicy(bad); err == nil {
		t.Fatal("unsupported string version accepted")
	}
}

func TestPolicyFailsClosedForUnknownObjectDirectLoginAndColumnDrift(t *testing.T) {
	mutated := DefaultPolicyV1()
	mutated.Objects = append(mutated.Objects, ObjectGrant{Kind: "table", Schema: "audit", Name: "future_table", Owner: "xm_migrator", NoRuntimeAccess: true})
	assertViolationCode(t, mutated.ValidateCurrentAt(time.Time{}), "OBJECT_UNKNOWN")
	mutated = DefaultPolicyV1()
	for i := range mutated.Objects {
		if mutated.Objects[i].Kind == "table" && mutated.Objects[i].TablePrivileges != nil {
			mutated.Objects[i].TablePrivileges["xm_api_a"] = []string{"SELECT"}
			break
		}
	}
	assertViolationCode(t, mutated.ValidateCurrentAt(time.Time{}), "GRANT_DIRECT_LOGIN_ROLE")
	mutated = DefaultPolicyV1()
	for i := range mutated.Objects {
		if mutated.Objects[i].Key() == "audit.chain_root" {
			mutated.Objects[i].ColumnPrivileges["xm_lifecycle_runtime"]["root_hash"] = []string{"UPDATE"}
		}
	}
	assertViolationCode(t, mutated.ValidateCurrentAt(time.Time{}), "COLUMN_PROTECTED_GRANT")
	mutated = DefaultPolicyV1()
	for i := range mutated.Objects {
		if mutated.Objects[i].Key() == "audit.audit_event" {
			mutated.Objects[i].Columns = mutated.Objects[i].Columns[:1]
			break
		}
	}
	assertViolationCode(t, mutated.ValidateCurrentAt(time.Time{}), "OBJECT_COLUMNS_DRIFT")
}

func TestPolicyCanonicalizationDoesNotMutateCaller(t *testing.T) {
	policy := DefaultPolicyV1()
	before := append([]string(nil), policy.Memberships[0].Role+":"+policy.Memberships[0].Member)
	_, _, err := policy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	after := policy.Memberships[0].Role + ":" + policy.Memberships[0].Member
	if len(before) != 1 || before[0] != after {
		t.Fatalf("Canonical mutated caller memberships: before=%v after=%s", before, after)
	}
}

func TestLoadPolicyCanonicalDigestAndRawDigestAreDistinct(t *testing.T) {
	valid := policyJSON(t, DefaultPolicyV1())
	loaded, digest, err := LoadPolicy(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
		t.Fatalf("digest=%q", digest)
	}
	canonical, canonicalDigest, err := loaded.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, valid) {
		t.Fatalf("policy JSON is not canonical; got %s want %s", canonical, valid)
	}
	if digest != canonicalDigest {
		t.Fatalf("canonical digest mismatch: load=%s canonical=%s", digest, canonicalDigest)
	}
	pretty := append([]byte("\n  "), append(valid, []byte("\n")...)...)
	_, prettyDigest, err := LoadPolicy(pretty)
	if err != nil {
		t.Fatal(err)
	}
	if prettyDigest != digest {
		t.Fatalf("formatting should not alter normalized policy digest: %s != %s", prettyDigest, digest)
	}
}

func TestRotationSteadyARequiresOnlyA(t *testing.T) {
	policy := DefaultPolicyV1()
	rotation := policy.Rotations["xm_api_runtime"]
	rotation.SteadyIdentity = "xm_api_b"
	policy.Rotations["xm_api_runtime"] = rotation
	violations := policy.ValidateCurrent()
	assertViolationCode(t, violations, "ROTATION_STEADY_IDENTITY_INVALID")
	assertViolationMentions(t, violations, "xm_api_runtime", "xm_api_b")

	policy = DefaultPolicyV1()
	rotation = policy.Rotations["xm_api_runtime"]
	rotation.OldIdentity = "xm_api_a"
	policy.Rotations["xm_api_runtime"] = rotation
	violations = policy.ValidateCurrent()
	assertViolationCode(t, violations, "ROTATION_STEADY_FIELDS_INVALID")
}

func TestRotationRotatingRequiresCRTimesAndDistinctCapabilityIdentities(t *testing.T) {
	policy := DefaultPolicyV1()
	rotation := policy.Rotations["xm_api_runtime"]
	rotation.State = RotationRotatingAB
	rotation.SteadyIdentity = ""
	rotation.OldIdentity = "xm_api_a"
	rotation.NewIdentity = "xm_api_a"
	rotation.ApprovedChangeRequest = "cr-1"
	policy.Rotations["xm_api_runtime"] = rotation
	violations := policy.ValidateCurrentAt(testNow())
	assertViolationCode(t, violations, "ROTATION_IDENTITIES_NOT_DISTINCT")

	rotation.NewIdentity = "xm_api_b"
	rotation.StartedAt = "2026-08-30T10:00:00Z"
	rotation.Deadline = "2026-08-30T09:00:00Z"
	policy.Rotations["xm_api_runtime"] = rotation
	violations = policy.ValidateCurrentAt(testNow())
	assertViolationCode(t, violations, "ROTATION_TIME_WINDOW_INVALID")

	rotation.Deadline = "2026-08-30T11:00:00Z"
	rotation.ApprovedChangeRequest = ""
	policy.Rotations["xm_api_runtime"] = rotation
	violations = policy.ValidateCurrentAt(testNow())
	assertViolationCode(t, violations, "ROTATION_CHANGE_REQUEST_REQUIRED")
}

func TestRotationSteadyBRequiresClosureAndNoOldAccess(t *testing.T) {
	policy := DefaultPolicyV1()
	rotation := policy.Rotations["xm_api_runtime"]
	rotation.State = RotationSteadyB
	rotation.SteadyIdentity = "xm_api_b"
	rotation.OldIdentity = "xm_api_a"
	rotation.NewIdentity = "xm_api_b"
	rotation.ApprovedChangeRequest = "cr-1"
	rotation.StartedAt = "2026-08-30T08:00:00Z"
	rotation.Deadline = "2026-08-30T11:00:00Z"
	rotation.ClosedAt = "2026-08-30T10:00:00Z"
	policy.Rotations["xm_api_runtime"] = rotation
	violations := policy.ValidateCurrentAt(testNow())
	assertViolationCode(t, violations, "ROTATION_CLOSURE_EVIDENCE_REQUIRED")

	rotation.ClosureEvidence = "sha256:" + strings.Repeat("a", 64)
	policy.Rotations["xm_api_runtime"] = rotation
	if err := policy.SetRotationTopology("xm_api_runtime", RotationSteadyB); err != nil {
		t.Fatal(err)
	}
	violations = policy.ValidateCurrentAt(testNow())
	if len(violations) != 0 {
		t.Fatalf("valid steady-b rejected: %+v", violations)
	}
	policy.ActiveSessions = []string{"xm_api_a"}
	violations = policy.ValidateCurrentAt(testNow())
	assertViolationCode(t, violations, "ROTATION_STALE_SESSION")

	rotation.OldIdentity = "xm_worker_a"
	policy.Rotations["xm_api_runtime"] = rotation
	violations = policy.ValidateCurrentAt(testNow())
	assertViolationCode(t, violations, "ROTATION_IDENTITY_WRONG_CAPABILITY")
}

func TestRotationRejectsExpiredCrossCapabilityAndStaleSession(t *testing.T) {
	policy := DefaultPolicyV1()
	rotation := policy.Rotations["xm_api_runtime"]
	rotation.State = RotationRotatingAB
	rotation.SteadyIdentity = ""
	rotation.OldIdentity = "xm_api_a"
	rotation.NewIdentity = "xm_worker_b"
	rotation.ApprovedChangeRequest = "cr-1"
	rotation.StartedAt = "2026-08-30T07:00:00Z"
	rotation.Deadline = "2026-08-30T08:00:00Z"
	policy.Rotations["xm_api_runtime"] = rotation
	violations := policy.ValidateCurrentAt(testNow())
	assertViolationCode(t, violations, "ROTATION_IDENTITY_WRONG_CAPABILITY")
	assertViolationCode(t, violations, "ROTATION_DEADLINE_EXPIRED")

	rotation.NewIdentity = "xm_api_b"
	rotation.Deadline = "2026-08-30T11:00:00Z"
	policy.Rotations["xm_api_runtime"] = rotation
	if err := policy.SetRotationTopology("xm_api_runtime", RotationRotatingAB); err != nil {
		t.Fatal(err)
	}
	violations = policy.ValidateCurrentAt(testNow())
	if len(violations) != 0 {
		t.Fatalf("structurally valid rotating state rejected: %+v", violations)
	}
}

func TestCheckedInPolicyContractLoads(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(source), "..", "..", "..", "contracts", "database", "role-policy.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, digest, err := LoadPolicy(data)
	if err != nil {
		t.Fatalf("checked-in policy rejected: %v", err)
	}
	if digest == "" || policy.Version != PolicyVersionV1 {
		t.Fatalf("loaded policy=%+v digest=%q", policy, digest)
	}
	if got := hex.EncodeToString(sha256Bytes(data)); got == "" {
		t.Fatal("sha helper unexpectedly empty")
	}
	eventData, err := os.ReadFile(filepath.Join(filepath.Dir(path), "role-policy-state-events.v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := LoadStateEvents(eventData)
	if err != nil || len(events) != 1 {
		t.Fatalf("checked-in event log rejected: %v (%d events)", err, len(events))
	}
	if events[0].CurrentPolicySHA256 != RawDigest(data) {
		t.Fatalf("genesis does not bind exact policy bytes: %s != %s", events[0].CurrentPolicySHA256, RawDigest(data))
	}
}

func policyJSON(t *testing.T, policy Policy) []byte {
	t.Helper()
	b, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertViolationCode(t *testing.T, violations []Violation, code string) {
	t.Helper()
	for _, violation := range violations {
		if violation.Code == code {
			return
		}
	}
	t.Fatalf("missing violation code %q in %+v", code, violations)
}

func assertViolationMentions(t *testing.T, violations []Violation, values ...string) {
	t.Helper()
	for _, violation := range violations {
		text := violation.Capability + " " + violation.Identity + " " + violation.Message
		matched := true
		for _, value := range values {
			matched = matched && strings.Contains(text, value)
		}
		if matched {
			return
		}
	}
	t.Fatalf("no violation mentions %v: %+v", values, violations)
}

func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func testNow() time.Time {
	return time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC)
}
