package dbroles

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgreadonly"
)

func TestVerifySnapshotAcceptsPolicyDerivedCatalog(t *testing.T) {
	policy := DefaultPolicyV1()
	snapshot := snapshotFromPolicy(policy)
	if violations := VerifySnapshot(policy, snapshot, time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC)); len(violations) != 0 {
		t.Fatalf("policy-derived catalog rejected: %+v", violations)
	}
}

func TestVerifySnapshotFailsClosedForRoleObjectAndPublicDrift(t *testing.T) {
	policy := DefaultPolicyV1()
	cases := map[string]func(*CatalogSnapshot){
		"superuser":    func(s *CatalogSnapshot) { r := s.Roles[0]; r.Superuser = true; s.Roles[0] = r },
		"unknown-role": func(s *CatalogSnapshot) { s.Roles = append(s.Roles, CatalogRole{Name: "xm_unknown"}) },
		"membership":   func(s *CatalogSnapshot) { s.Memberships[0].Set = true },
		"object-owner": func(s *CatalogSnapshot) { s.Objects[0].Owner = "xm_api_a" },
		"object-grant": func(s *CatalogSnapshot) {
			for i := range s.Objects {
				if s.Objects[i].Key() == "audit.audit_event" {
					s.Objects[i].TablePrivileges["xm_api_runtime"] = []string{"INSERT", "SELECT", "UPDATE"}
				}
			}
		},
		"public-connect": func(s *CatalogSnapshot) { s.PublicDatabasePrivileges = []string{"CONNECT"} },
		"blank-application": func(s *CatalogSnapshot) {
			s.Sessions = append(s.Sessions, CatalogSession{Role: "xm_api_a", ApplicationName: ""})
		},
		"readonly-role": func(s *CatalogSnapshot) {
			for i := range s.Roles {
				if s.Roles[i].Name == "xm_ops_a" {
					s.Roles[i].DefaultTransactionReadOnly = false
				}
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := snapshotFromPolicy(policy)
			mutate(&snapshot)
			if violations := VerifySnapshot(policy, snapshot, time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC)); len(violations) == 0 {
				t.Fatal("unsafe catalog unexpectedly accepted")
			}
		})
	}
}

func TestVerifySnapshotChecksRotationSessionsAndObjectKinds(t *testing.T) {
	policy := DefaultPolicyV1()
	if err := policy.SetRotationTopology("xm_api_runtime", RotationSteadyB); err != nil {
		t.Fatal(err)
	}
	r := policy.Rotations["xm_api_runtime"]
	r.ApprovedChangeRequest = "cr-1"
	r.OldIdentity = "xm_api_a"
	r.NewIdentity = "xm_api_b"
	r.StartedAt = "2026-08-30T08:00:00Z"
	r.Deadline = "2026-08-30T12:00:00Z"
	r.ClosureEvidence = "sha256:" + strings.Repeat("a", 64)
	r.ClosedAt = "2026-08-30T10:00:00Z"
	policy.Rotations["xm_api_runtime"] = r
	snapshot := snapshotFromPolicy(policy)
	snapshot.Sessions = []CatalogSession{{Role: "xm_api_a", ApplicationName: "platform-api"}}
	violations := VerifySnapshot(policy, snapshot, time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC))
	assertViolationCode(t, violations, "ROTATION_STALE_SESSION")

	for i := range snapshot.Objects {
		if snapshot.Objects[i].Key() == "finance.runway_threshold_current_verified" {
			snapshot.Objects[i].Kind = "table"
		}
	}
	violations = VerifySnapshot(policy, snapshot, time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC))
	assertViolationCode(t, violations, "CATALOG_UNKNOWN_OBJECT")
}

func TestVerifySnapshotCoversChainRootSequenceAndDefaultACLBoundaries(t *testing.T) {
	policy := DefaultPolicyV1()
	baseline := snapshotFromPolicy(policy)
	cases := []struct {
		name   string
		mutate func(*CatalogSnapshot)
		code   string
	}{
		{"chain-root-table-update", func(s *CatalogSnapshot) {
			for i := range s.Objects {
				if s.Objects[i].Key() == "audit.chain_root" {
					s.Objects[i].TablePrivileges["xm_lifecycle_runtime"] = []string{"SELECT", "INSERT", "UPDATE"}
				}
			}
		}, "CATALOG_OBJECT_PRIVILEGE_DRIFT"},
		{"chain-root-column-update", func(s *CatalogSnapshot) {
			for i := range s.Objects {
				if s.Objects[i].Key() == "audit.chain_root" {
					s.Objects[i].ColumnPrivileges["xm_lifecycle_runtime"]["root_hash"] = []string{"UPDATE"}
				}
			}
		}, "CATALOG_OBJECT_PRIVILEGE_DRIFT"},
		{"sequence-update", func(s *CatalogSnapshot) {
			for i := range s.Objects {
				if s.Objects[i].Key() == "ops.metric_observation_sample_id_seq" {
					s.Objects[i].Privileges["xm_worker_runtime"] = []string{"USAGE", "UPDATE"}
				}
			}
		}, "CATALOG_OBJECT_PRIVILEGE_DRIFT"},
		{"default-acl", func(s *CatalogSnapshot) { s.DefaultACLs[0].Privileges["PUBLIC"] = []string{"USAGE"} }, "CATALOG_DEFAULT_ACL_DRIFT"},
		{"public-owner", func(s *CatalogSnapshot) {
			for i := range s.Objects {
				if s.Objects[i].Kind == "schema" && s.Objects[i].Name == "public" {
					s.Objects[i].Owner = "xm_migrator"
				}
			}
		}, "CATALOG_OBJECT_SHAPE_DRIFT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := baseline
			snapshot.Objects = append([]CatalogObject(nil), baseline.Objects...)
			snapshot.DefaultACLs = append([]CatalogDefaultACL(nil), baseline.DefaultACLs...)
			for i := range snapshot.Objects {
				snapshot.Objects[i].Privileges = cloneGrantMap(snapshot.Objects[i].Privileges)
				snapshot.Objects[i].TablePrivileges = cloneGrantMap(snapshot.Objects[i].TablePrivileges)
				snapshot.Objects[i].ColumnPrivileges = cloneColumnMap(snapshot.Objects[i].ColumnPrivileges)
			}
			for i := range snapshot.DefaultACLs {
				snapshot.DefaultACLs[i].Privileges = cloneGrantMap(snapshot.DefaultACLs[i].Privileges)
			}
			tc.mutate(&snapshot)
			assertViolationCode(t, VerifySnapshot(policy, snapshot, time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC)), tc.code)
		})
	}
}

func TestCatalogQueriesAreSelectOnlyAndPinned(t *testing.T) {
	if len(CatalogQueries) < 9 {
		t.Fatalf("catalog query allowlist too small: %d", len(CatalogQueries))
	}
	for _, query := range CatalogQueries {
		if err := pgreadonly.AssertSelectOnly(query); err != nil {
			t.Fatalf("query is not read-only: %v\n%s", err, query)
		}
	}
}

func TestRoutineCatalogQueryNormalizesEmptyACLPrivilegeRows(t *testing.T) {
	for name, query := range map[string]string{
		"relation": catalogRelationsSQL,
		"routine":  catalogRoutinesSQL,
		"type":     catalogTypesSQL,
	} {
		lower := strings.ToLower(query)
		if !strings.Contains(lower, "coalesce(ax.privilege_type, '')") {
			t.Fatalf("%s catalog query must normalize NULL privilege_type from an empty ACL: %s", name, query)
		}
	}
}

func TestRelationCatalogQueryIncludesAuditSchema(t *testing.T) {
	if !strings.Contains(strings.ToLower(catalogRelationsSQL), "n.nspname in ('public','core','action','audit'") {
		t.Fatalf("relation catalog query must include audit objects in its allowlisted schema set: %s", catalogRelationsSQL)
	}
}

func TestDefaultACLProjectionExpandsPG18TypeFamilyAndEmptyRows(t *testing.T) {
	aclMap := map[string]*CatalogDefaultACL{}
	// A LEFT JOIN over an empty ACL emits one row with an empty privilege;
	// both logical families must still be represented for verifier completeness.
	projectDefaultACLRow(aclMap, "xm_migrator", "core", "T", "PUBLIC", "")
	if len(aclMap) != 2 {
		t.Fatalf("PG18 T default ACL must project type and domain, got %d entries", len(aclMap))
	}
	for _, kind := range []string{"type", "domain"} {
		key := "xm_migrator\x00core\x00" + kind
		acl, ok := aclMap[key]
		if !ok {
			t.Fatalf("missing projected %s default ACL", kind)
		}
		if len(acl.Privileges) != 0 {
			t.Fatalf("empty ACL sentinel must not create a privilege grant for %s: %#v", kind, acl.Privileges)
		}
	}
	projectDefaultACLRow(aclMap, "xm_migrator", "core", "T", "xm_worker_runtime", "USAGE")
	for _, kind := range []string{"type", "domain"} {
		acl := aclMap["xm_migrator\x00core\x00"+kind]
		if got := acl.Privileges["xm_worker_runtime"]; len(got) != 1 || got[0] != "USAGE" {
			t.Fatalf("T ACL privilege not mirrored to %s: %#v", kind, acl.Privileges)
		}
	}
}

func TestPublicPrivilegeQueriesDoNotPassPublicPseudoRoleToPrivilegeFunctions(t *testing.T) {
	for _, query := range CatalogQueryAllowlist() {
		lower := strings.ToLower(query)
		if strings.Contains(lower, "has_database_privilege('public'") || strings.Contains(lower, "has_schema_privilege('public'") {
			t.Fatalf("catalog query passes the PUBLIC pseudo-role as a privilege-function argument: %s", query)
		}
	}
	for name, query := range map[string]string{
		"database": catalogPublicDatabasePrivilegesSQL,
		"schema":   catalogPublicSchemaPrivilegesSQL,
	} {
		lower := strings.ToLower(query)
		if !strings.Contains(lower, "aclexplode") || !strings.Contains(lower, "grantee = 0") {
			t.Fatalf("%s PUBLIC privilege query must inspect ACL grantee oid 0: %s", name, query)
		}
	}
	for name, query := range map[string]string{
		"schema object":   catalogSchemasSQL,
		"database object": catalogDatabaseACLsSQL,
	} {
		lower := strings.ToLower(query)
		if !strings.Contains(lower, "union all") || !strings.Contains(lower, "ax.grantee = 0") {
			t.Fatalf("%s query must retain a PUBLIC ACL-OID branch without passing the pseudo-role to has_*_privilege: %s", name, query)
		}
	}
}

func TestVerifySnapshotDetectsInjectedPublicObjectGrant(t *testing.T) {
	policy := DefaultPolicyV1()
	for _, objectName := range []string{"public", "current_database"} {
		snapshot := snapshotFromPolicy(policy)
		found := false
		for i := range snapshot.Objects {
			if (objectName == "public" && snapshot.Objects[i].Kind == "schema" && snapshot.Objects[i].Name == objectName) || (objectName == "current_database" && snapshot.Objects[i].Kind == "database" && snapshot.Objects[i].Name == objectName) {
				snapshot.Objects[i].Privileges["PUBLIC"] = []string{"USAGE"}
				found = true
			}
		}
		if !found {
			t.Fatalf("policy fixture is missing %s object", objectName)
		}
		assertViolationCode(t, VerifySnapshot(policy, snapshot, time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC)), "CATALOG_OBJECT_PRIVILEGE_DRIFT")
	}
}

func snapshotFromPolicy(policy Policy) CatalogSnapshot {
	snapshot := CatalogSnapshot{DatabaseName: policy.ProductionDatabaseName, PublicDatabasePrivileges: append([]string(nil), policy.PublicDatabasePrivileges...), PublicSchemaPrivileges: append([]string(nil), policy.PublicSchemaPrivileges...), PublicTypePrivileges: append([]string(nil), policy.PublicTypePrivileges...), PublicRoutinePrivileges: append([]string(nil), policy.PublicRoutinePrivileges...), RunwayApprovedMergeSHA: policy.RunwayApprovedMergeSHA}
	for name, role := range policy.Roles {
		snapshot.Roles = append(snapshot.Roles, CatalogRole{Name: name, Login: role.Login, Inherit: role.Inherit, Superuser: role.Superuser, CreateDB: role.CreateDB, CreateRole: role.CreateRole, Replication: role.Replication, BypassRLS: role.BypassRLS, DefaultTransactionReadOnly: role.DefaultTransactionReadOnly})
	}
	for _, membership := range policy.Memberships {
		snapshot.Memberships = append(snapshot.Memberships, CatalogMembership{Role: membership.Role, Member: membership.Member, Inherit: membership.Inherit, Set: membership.Set, Admin: membership.Admin})
	}
	for _, object := range policy.Objects {
		snapshot.Objects = append(snapshot.Objects, CatalogObject{Kind: object.Kind, Schema: object.Schema, Name: object.Name, Owner: object.Owner, Columns: append([]string(nil), object.Columns...), Privileges: cloneGrantMap(object.Privileges), TablePrivileges: cloneGrantMap(object.TablePrivileges), ColumnPrivileges: cloneColumnMap(object.ColumnPrivileges), NoRuntimeAccess: object.NoRuntimeAccess})
	}
	for _, acl := range policy.DefaultACLs {
		snapshot.DefaultACLs = append(snapshot.DefaultACLs, CatalogDefaultACL{Owner: acl.Owner, Schema: acl.Schema, ObjectKind: acl.ObjectKind, Privileges: cloneGrantMap(acl.Privileges)})
	}
	return snapshot
}
