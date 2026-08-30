package dbroles

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgreadonly"
)

// CatalogRole is the read-only projection of pg_roles used by the verifier.
type CatalogRole struct {
	Name                       string `json:"name"`
	Login                      bool   `json:"login"`
	Inherit                    bool   `json:"inherit"`
	Superuser                  bool   `json:"superuser"`
	CreateDB                   bool   `json:"createdb"`
	CreateRole                 bool   `json:"createrole"`
	Replication                bool   `json:"replication"`
	BypassRLS                  bool   `json:"bypassrls"`
	DefaultTransactionReadOnly bool   `json:"default_transaction_read_only"`
}

// CatalogMembership is the pg_auth_members projection.
type CatalogMembership struct {
	Role    string `json:"role"`
	Member  string `json:"member"`
	Inherit bool   `json:"inherit"`
	Set     bool   `json:"set"`
	Admin   bool   `json:"admin"`
}

// CatalogObject is one relation/routine/type/schema-independent object ACL
// projection. Privileges is used for non-table objects; TablePrivileges and
// ColumnPrivileges preserve the table/column ACL distinction.
type CatalogObject struct {
	Kind             string                         `json:"kind"`
	Schema           string                         `json:"schema"`
	Name             string                         `json:"name"`
	Owner            string                         `json:"owner"`
	Columns          []string                       `json:"columns,omitempty"`
	Privileges       map[string][]string            `json:"privileges,omitempty"`
	TablePrivileges  map[string][]string            `json:"table_privileges,omitempty"`
	ColumnPrivileges map[string]map[string][]string `json:"column_privileges,omitempty"`
	NoRuntimeAccess  bool                           `json:"no_runtime_access,omitempty"`
}

func (o CatalogObject) Key() string {
	if o.Schema == "" {
		return o.Name
	}
	return o.Schema + "." + o.Name
}
func catalogObjectIdentity(o CatalogObject) string {
	return o.Kind + "\x00" + o.Schema + "\x00" + o.Name
}

// CatalogDefaultACL is the normalized pg_default_acl projection.
type CatalogDefaultACL struct {
	Owner      string              `json:"owner"`
	Schema     string              `json:"schema"`
	ObjectKind string              `json:"object_kind"`
	Privileges map[string][]string `json:"privileges"`
}

// CatalogSession is the small pg_stat_activity projection needed to prove
// old A identities have been drained and application_name is present.
type CatalogSession struct {
	Role            string `json:"role"`
	ApplicationName string `json:"application_name"`
}

// CatalogSnapshot is a complete, deterministic read-only catalog projection.
// Callers may build one from fixtures and use VerifySnapshot without any DB.
type CatalogSnapshot struct {
	DatabaseName             string              `json:"database_name"`
	Roles                    []CatalogRole       `json:"roles"`
	Memberships              []CatalogMembership `json:"memberships"`
	Objects                  []CatalogObject     `json:"objects"`
	DefaultACLs              []CatalogDefaultACL `json:"default_acls"`
	PublicDatabasePrivileges []string            `json:"public_database_privileges"`
	PublicSchemaPrivileges   []string            `json:"public_schema_privileges"`
	PublicTypePrivileges     []string            `json:"public_type_privileges"`
	PublicRoutinePrivileges  []string            `json:"public_routine_privileges"`
	Sessions                 []CatalogSession    `json:"sessions"`
	RunwayApprovedMergeSHA   string              `json:"runway_approved_merge_sha,omitempty"`
}

// VerifySnapshot compares a catalog snapshot to policy and returns stable,
// sorted violations. It performs no I/O and never includes passwords/DSNs.
func VerifySnapshot(policy Policy, snapshot CatalogSnapshot, now time.Time) []Violation {
	violations := append([]Violation(nil), policy.ValidateCurrentAt(now)...)
	add := func(code, capability, identity, object, message string) {
		violations = append(violations, Violation{Code: code, Capability: capability, Identity: identity, Object: object, Message: message})
	}
	if snapshot.DatabaseName == "" {
		add("CATALOG_DATABASE_MISSING", "", "", "", "current database name is missing")
	} else if snapshot.DatabaseName != policy.ProductionDatabaseName {
		add("CATALOG_DATABASE_MISMATCH", "", snapshot.DatabaseName, "", "current database does not match policy production_database_name")
	}
	verifyRoles(policy, snapshot.Roles, add)
	verifyMemberships(policy, snapshot.Memberships, add)
	verifyObjects(policy, snapshot.Objects, add)
	verifyDefaultACLs(policy, snapshot.DefaultACLs, add)
	if !sameStringSetExact(snapshot.PublicDatabasePrivileges, policy.PublicDatabasePrivileges) {
		add("PUBLIC_DATABASE_PRIVILEGE_DRIFT", "PUBLIC", "", "database", "PUBLIC database privileges differ from policy")
	}
	if !sameStringSetExact(snapshot.PublicSchemaPrivileges, policy.PublicSchemaPrivileges) {
		add("PUBLIC_SCHEMA_PRIVILEGE_DRIFT", "PUBLIC", "", "public", "PUBLIC schema privileges differ from policy")
	}
	if !sameStringSetExact(snapshot.PublicTypePrivileges, policy.PublicTypePrivileges) {
		add("PUBLIC_TYPE_PRIVILEGE_DRIFT", "PUBLIC", "", "types", "PUBLIC type privileges differ from policy")
	}
	if !sameStringSetExact(snapshot.PublicRoutinePrivileges, policy.PublicRoutinePrivileges) {
		add("PUBLIC_ROUTINE_PRIVILEGE_DRIFT", "PUBLIC", "", "routines", "PUBLIC routine privileges differ from policy")
	}
	for _, session := range snapshot.Sessions {
		if _, known := policy.Roles[session.Role]; !known {
			add("CATALOG_UNKNOWN_SESSION_ROLE", "", session.Role, "pg_stat_activity", "session identity is outside policy")
		}
		if strings.TrimSpace(session.ApplicationName) == "" {
			add("CATALOG_APPLICATION_NAME_MISSING", "", session.Role, "pg_stat_activity", "runtime session application_name is blank")
		}
		for capability, rotation := range policy.Rotations {
			if rotation.State == RotationSteadyB && session.Role == "xm_"+identityPrefix(capability)+"_a" {
				add("ROTATION_STALE_SESSION", capability, session.Role, "pg_stat_activity", "steady-b has an active old identity session")
			}
			if rotation.State == RotationSteadyA && session.Role == "xm_"+identityPrefix(capability)+"_b" {
				add("ROTATION_UNEXPECTED_SESSION", capability, session.Role, "pg_stat_activity", "steady-a has an active inactive identity session")
			}
		}
	}
	if snapshot.RunwayApprovedMergeSHA != "" && snapshot.RunwayApprovedMergeSHA != policy.RunwayApprovedMergeSHA {
		add("RUNWAY_APPROVAL_SHA_MISMATCH", "", snapshot.RunwayApprovedMergeSHA, "RUNWAY", "catalog RUNWAY provenance does not match policy")
	}
	return sortViolations(violations)
}

func VerifyCatalogSnapshot(policy Policy, snapshot CatalogSnapshot, now time.Time) []Violation {
	return VerifySnapshot(policy, snapshot, now)
}
func VerifyPolicy(policy Policy, snapshot CatalogSnapshot, now time.Time) []Violation {
	return VerifySnapshot(policy, snapshot, now)
}

// VerifyCatalog reads the allowlisted PostgreSQL catalog through a read-only
// pgx-compatible connection and verifies the resulting snapshot.
func VerifyCatalog(ctx context.Context, conn CatalogQueryer, policy Policy, now time.Time) ([]Violation, error) {
	if conn == nil {
		return nil, errors.New("dbroles verifier: nil catalog connection")
	}
	snapshot, err := ReadCatalogSnapshot(ctx, conn, policy)
	if err != nil {
		return nil, err
	}
	return VerifySnapshot(policy, snapshot, now), nil
}

// Verify is a descriptive alias retained for callers that use the shorter
// verifier name.
func Verify(ctx context.Context, conn CatalogQueryer, policy Policy, now time.Time) ([]Violation, error) {
	return VerifyCatalog(ctx, conn, policy, now)
}

// CatalogQueryer is implemented by *pgx.Conn and *pgxpool.Pool. It is kept
// narrow so pure tests can provide a deterministic reader without granting any
// write method.
type CatalogQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// CatalogQueries is the complete SQL allowlist. Every statement is a SELECT;
// the verifier rejects accidental additions that are not SELECT-only.
var CatalogQueries = []string{catalogDatabaseSQL, catalogRolesSQL, catalogMembershipsSQL, catalogRelationsSQL, catalogColumnsSQL, catalogRoutinesSQL, catalogTypesSQL, catalogDefaultACLsSQL, catalogSessionsSQL, catalogPublicDatabasePrivilegesSQL, catalogPublicSchemaPrivilegesSQL, catalogSchemasSQL, catalogDatabaseACLsSQL}

// CatalogQueryAllowlist returns a defensive copy for audits/tests. Runtime
// verification uses fresh literals so callers cannot mutate the safety list.
func CatalogQueryAllowlist() []string {
	return append([]string(nil), []string{catalogDatabaseSQL, catalogRolesSQL, catalogMembershipsSQL, catalogRelationsSQL, catalogColumnsSQL, catalogRoutinesSQL, catalogTypesSQL, catalogDefaultACLsSQL, catalogSessionsSQL, catalogPublicDatabasePrivilegesSQL, catalogPublicSchemaPrivilegesSQL, catalogSchemasSQL, catalogDatabaseACLsSQL}...)
}

const catalogDatabaseSQL = "SELECT current_database()"
const catalogRolesSQL = `SELECT r.rolname, r.rolcanlogin, r.rolinherit, r.rolsuper, r.rolcreatedb, r.rolcreaterole, r.rolreplication, r.rolbypassrls, COALESCE((SELECT lower(split_part(setting, '=', 2)) IN ('on','true','1','yes','t','y') FROM unnest(COALESCE(r.rolconfig, ARRAY[]::text[])) AS setting WHERE setting LIKE 'default_transaction_read_only=%' LIMIT 1), false) FROM pg_roles AS r WHERE r.rolname = 'xm_migrator' OR r.rolname LIKE 'xm\_%' ORDER BY r.rolname`
const catalogMembershipsSQL = `SELECT parent.rolname, member.rolname, m.inherit_option, m.set_option, m.admin_option FROM pg_auth_members AS m JOIN pg_roles AS parent ON parent.oid = m.roleid JOIN pg_roles AS member ON member.oid = m.member WHERE parent.rolname LIKE 'xm\_%' OR member.rolname LIKE 'xm\_%' ORDER BY parent.rolname, member.rolname`
const catalogRelationsSQL = `SELECT CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'table' WHEN 'v' THEN 'view' WHEN 'S' THEN 'sequence' END, n.nspname, c.relname, owner.rolname, c.relkind, COALESCE(grantee.rolname, 'PUBLIC'), ax.privilege_type FROM pg_class AS c JOIN pg_namespace AS n ON n.oid = c.relnamespace JOIN pg_roles AS owner ON owner.oid = c.relowner LEFT JOIN LATERAL aclexplode(COALESCE(c.relacl, acldefault(CASE WHEN c.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END, c.relowner))) AS ax ON true LEFT JOIN pg_roles AS grantee ON grantee.oid = ax.grantee WHERE n.nspname IN ('public','core','action','audit','ops','alerts','finance','ui') AND c.relkind IN ('r','p','v','S') AND c.relname NOT LIKE 'pg\_%' ORDER BY n.nspname, c.relname, grantee.rolname, ax.privilege_type`
const catalogColumnsSQL = `SELECT n.nspname, c.relname, a.attname, COALESCE(grantee.rolname, ''), COALESCE(ax.privilege_type, '') FROM pg_class AS c JOIN pg_namespace AS n ON n.oid = c.relnamespace JOIN pg_attribute AS a ON a.attrelid = c.oid LEFT JOIN LATERAL aclexplode(a.attacl) AS ax ON true LEFT JOIN pg_roles AS grantee ON grantee.oid = ax.grantee WHERE n.nspname IN ('public','core','action','audit','ops','alerts','finance','ui') AND c.relkind IN ('r','p','v') AND a.attnum > 0 AND NOT a.attisdropped ORDER BY n.nspname, c.relname, a.attnum, grantee.rolname, ax.privilege_type`
const catalogRoutinesSQL = `SELECT n.nspname, p.oid::text, p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', owner.rolname, COALESCE(grantee.rolname, 'PUBLIC'), ax.privilege_type FROM pg_proc AS p JOIN pg_namespace AS n ON n.oid = p.pronamespace JOIN pg_roles AS owner ON owner.oid = p.proowner LEFT JOIN LATERAL aclexplode(COALESCE(p.proacl, acldefault('f'::"char", p.proowner))) AS ax ON true LEFT JOIN pg_roles AS grantee ON grantee.oid = ax.grantee WHERE n.nspname IN ('public','core','action','audit','ops','alerts','finance','ui') ORDER BY n.nspname, p.proname, grantee.rolname, ax.privilege_type`
const catalogTypesSQL = `SELECT n.nspname, t.typname, owner.rolname, CASE WHEN t.typtype = 'd' THEN 'domain' ELSE 'type' END, COALESCE(grantee.rolname, 'PUBLIC'), ax.privilege_type FROM pg_type AS t JOIN pg_namespace AS n ON n.oid = t.typnamespace JOIN pg_roles AS owner ON owner.oid = t.typowner LEFT JOIN LATERAL aclexplode(COALESCE(t.typacl, acldefault('T'::"char", t.typowner))) AS ax ON true LEFT JOIN pg_roles AS grantee ON grantee.oid = ax.grantee WHERE n.nspname IN ('public','core','action','audit','ops','alerts','finance','ui') AND t.typtype IN ('d','e') ORDER BY n.nspname, t.typname, grantee.rolname, ax.privilege_type`
const catalogDefaultACLsSQL = `SELECT owner.rolname, COALESCE(ns.nspname, ''), d.defaclobjtype, COALESCE(grantee.rolname, 'PUBLIC'), COALESCE(ax.privilege_type, '') FROM pg_default_acl AS d JOIN pg_roles AS owner ON owner.oid = d.defaclrole LEFT JOIN pg_namespace AS ns ON ns.oid = d.defaclnamespace LEFT JOIN LATERAL aclexplode(d.defaclacl) AS ax ON true LEFT JOIN pg_roles AS grantee ON grantee.oid = ax.grantee ORDER BY owner.rolname, ns.nspname, d.defaclobjtype, grantee.rolname, ax.privilege_type`
const catalogSessionsSQL = `SELECT usename, COALESCE(application_name, '') FROM pg_stat_activity WHERE usename = 'xm_migrator' OR usename LIKE 'xm\_%' ORDER BY usename, application_name`
const catalogPublicDatabasePrivilegesSQL = `SELECT privilege_type FROM (VALUES ('CONNECT'), ('CREATE'), ('TEMPORARY')) AS privileges(privilege_type) WHERE has_database_privilege('PUBLIC', current_database(), privilege_type) ORDER BY privilege_type`
const catalogPublicSchemaPrivilegesSQL = `SELECT privilege_type FROM (VALUES ('USAGE'), ('CREATE')) AS privileges(privilege_type) WHERE has_schema_privilege('PUBLIC', 'public', privilege_type) ORDER BY privilege_type`
const catalogSchemasSQL = `SELECT n.nspname, owner.rolname, grantees.rolname, privileges.privilege_type FROM pg_namespace AS n JOIN pg_roles AS owner ON owner.oid = n.nspowner CROSS JOIN (VALUES ('xm_migrator'), ('xm_api_runtime'), ('xm_worker_runtime'), ('xm_lifecycle_runtime'), ('xm_ops_read'), ('xm_backup_read'), ('PUBLIC')) AS grantees(rolname) CROSS JOIN (VALUES ('USAGE'), ('CREATE')) AS privileges(privilege_type) WHERE n.nspname IN ('public','core','action','audit','ops','alerts','finance','ui') AND has_schema_privilege(grantees.rolname, n.nspname, privileges.privilege_type) ORDER BY n.nspname, grantees.rolname, privileges.privilege_type`
const catalogDatabaseACLsSQL = `SELECT d.datname, owner.rolname, grantees.rolname, privileges.privilege_type FROM pg_database AS d JOIN pg_roles AS owner ON owner.oid = d.datdba CROSS JOIN (VALUES ('xm_migrator'), ('xm_api_runtime'), ('xm_worker_runtime'), ('xm_lifecycle_runtime'), ('xm_ops_read'), ('xm_backup_read'), ('PUBLIC')) AS grantees(rolname) CROSS JOIN (VALUES ('CONNECT'), ('CREATE'), ('TEMPORARY')) AS privileges(privilege_type) WHERE d.datname = current_database() AND has_database_privilege(grantees.rolname, d.datname, privileges.privilege_type) ORDER BY grantees.rolname, privileges.privilege_type`

// ReadCatalogSnapshot executes only the constants above and assembles a
// normalized projection. It intentionally does not query data tables.
func ReadCatalogSnapshot(ctx context.Context, conn CatalogQueryer, policy Policy) (CatalogSnapshot, error) {
	for _, query := range CatalogQueryAllowlist() {
		if err := pgreadonly.AssertSelectOnly(query); err != nil {
			return CatalogSnapshot{}, fmt.Errorf("dbroles verifier query allowlist: %w", err)
		}
	}
	var snapshot CatalogSnapshot
	if err := conn.QueryRow(ctx, catalogDatabaseSQL).Scan(&snapshot.DatabaseName); err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier database query: %w", err)
	}
	roles, err := conn.Query(ctx, catalogRolesSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier roles query: %w", err)
	}
	for roles.Next() {
		var role CatalogRole
		if err := roles.Scan(&role.Name, &role.Login, &role.Inherit, &role.Superuser, &role.CreateDB, &role.CreateRole, &role.Replication, &role.BypassRLS, &role.DefaultTransactionReadOnly); err != nil {
			roles.Close()
			return CatalogSnapshot{}, err
		}
		snapshot.Roles = append(snapshot.Roles, role)
	}
	if err := roles.Err(); err != nil {
		roles.Close()
		return CatalogSnapshot{}, err
	}
	roles.Close()
	memberships, err := conn.Query(ctx, catalogMembershipsSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier memberships query: %w", err)
	}
	for memberships.Next() {
		var m CatalogMembership
		if err := memberships.Scan(&m.Role, &m.Member, &m.Inherit, &m.Set, &m.Admin); err != nil {
			memberships.Close()
			return CatalogSnapshot{}, err
		}
		snapshot.Memberships = append(snapshot.Memberships, m)
	}
	if err := memberships.Err(); err != nil {
		memberships.Close()
		return CatalogSnapshot{}, err
	}
	memberships.Close()
	objects, err := conn.Query(ctx, catalogRelationsSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier relations query: %w", err)
	}
	objectMap := map[string]*CatalogObject{}
	for objects.Next() {
		var kind, schema, name, owner, relkind, grantee, privilege string
		if err := objects.Scan(&kind, &schema, &name, &owner, &relkind, &grantee, &privilege); err != nil {
			objects.Close()
			return CatalogSnapshot{}, err
		}
		if kind == "" {
			continue
		}
		key := kind + "\x00" + schema + "\x00" + name
		o := objectMap[key]
		if o == nil {
			o = &CatalogObject{Kind: kind, Schema: schema, Name: name, Owner: owner, Privileges: map[string][]string{}, TablePrivileges: map[string][]string{}, ColumnPrivileges: map[string]map[string][]string{}}
			objectMap[key] = o
		}
		if grantee != "" && privilege != "" && grantee != owner {
			if kind == "table" || kind == "view" {
				o.TablePrivileges[grantee] = append(o.TablePrivileges[grantee], privilege)
			} else {
				o.Privileges[grantee] = append(o.Privileges[grantee], privilege)
			}
		}
	}
	if err := objects.Err(); err != nil {
		objects.Close()
		return CatalogSnapshot{}, err
	}
	objects.Close()
	columns, err := conn.Query(ctx, catalogColumnsSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier columns query: %w", err)
	}
	for columns.Next() {
		var schema, name, column, grantee, privilege string
		if err := columns.Scan(&schema, &name, &column, &grantee, &privilege); err != nil {
			columns.Close()
			return CatalogSnapshot{}, err
		}
		for _, kind := range []string{"table", "view"} {
			if o := objectMap[kind+"\x00"+schema+"\x00"+name]; o != nil {
				o.Columns = append(o.Columns, column)
				if grantee != "" && privilege != "" {
					if o.ColumnPrivileges[grantee] == nil {
						o.ColumnPrivileges[grantee] = map[string][]string{}
					}
					o.ColumnPrivileges[grantee][column] = append(o.ColumnPrivileges[grantee][column], privilege)
				}
			}
		}
	}
	if err := columns.Err(); err != nil {
		columns.Close()
		return CatalogSnapshot{}, err
	}
	columns.Close()
	for _, object := range objectMap {
		for _, expected := range policy.Objects {
			if expected.Kind == object.Kind && expected.Schema == object.Schema && expected.Name == object.Name {
				object.NoRuntimeAccess = expected.NoRuntimeAccess
				break
			}
		}
		object.Columns = sortedUnique(object.Columns)
		object.Privileges = normalizeGrantMap(object.Privileges)
		object.TablePrivileges = normalizeGrantMap(object.TablePrivileges)
		object.ColumnPrivileges = normalizeColumnMap(object.ColumnPrivileges)
		snapshot.Objects = append(snapshot.Objects, *object)
	}
	routines, err := conn.Query(ctx, catalogRoutinesSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier routines query: %w", err)
	}
	routineMap := map[string]*CatalogObject{}
	for routines.Next() {
		var schema, oid, name, owner, grantee, privilege string
		if err := routines.Scan(&schema, &oid, &name, &owner, &grantee, &privilege); err != nil {
			routines.Close()
			return CatalogSnapshot{}, err
		}
		key := "routine\x00" + schema + "\x00" + name
		o := routineMap[key]
		if o == nil {
			o = &CatalogObject{Kind: "routine", Schema: schema, Name: name, Owner: owner, Privileges: map[string][]string{}}
			routineMap[key] = o
		}
		if grantee != "" && privilege != "" && grantee != owner {
			o.Privileges[grantee] = append(o.Privileges[grantee], privilege)
		}
	}
	if err := routines.Err(); err != nil {
		routines.Close()
		return CatalogSnapshot{}, err
	}
	routines.Close()
	for _, o := range routineMap {
		for _, expected := range policy.Objects {
			if expected.Kind == o.Kind && expected.Schema == o.Schema && expected.Name == o.Name {
				o.NoRuntimeAccess = expected.NoRuntimeAccess
				break
			}
		}
		o.Privileges = normalizeGrantMap(o.Privileges)
		snapshot.Objects = append(snapshot.Objects, *o)
	}
	types, err := conn.Query(ctx, catalogTypesSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier types query: %w", err)
	}
	typeMap := map[string]*CatalogObject{}
	for types.Next() {
		var schema, name, owner, kind, grantee, privilege string
		if err := types.Scan(&schema, &name, &owner, &kind, &grantee, &privilege); err != nil {
			types.Close()
			return CatalogSnapshot{}, err
		}
		key := kind + "\x00" + schema + "\x00" + name
		o := typeMap[key]
		if o == nil {
			o = &CatalogObject{Kind: kind, Schema: schema, Name: name, Owner: owner, Privileges: map[string][]string{}}
			typeMap[key] = o
		}
		if grantee != "" && privilege != "" && grantee != owner {
			o.Privileges[grantee] = append(o.Privileges[grantee], privilege)
		}
	}
	if err := types.Err(); err != nil {
		types.Close()
		return CatalogSnapshot{}, err
	}
	types.Close()
	for _, o := range typeMap {
		for _, expected := range policy.Objects {
			if expected.Kind == o.Kind && expected.Schema == o.Schema && expected.Name == o.Name {
				o.NoRuntimeAccess = expected.NoRuntimeAccess
				break
			}
		}
		o.Privileges = normalizeGrantMap(o.Privileges)
		snapshot.Objects = append(snapshot.Objects, *o)
	}
	acls, err := conn.Query(ctx, catalogDefaultACLsSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier default ACL query: %w", err)
	}
	aclMap := map[string]*CatalogDefaultACL{}
	for acls.Next() {
		var owner, schema, objkind, grantee, privilege string
		if err := acls.Scan(&owner, &schema, &objkind, &grantee, &privilege); err != nil {
			acls.Close()
			return CatalogSnapshot{}, err
		}
		kind := defaultACLKind(objkind)
		key := owner + "\x00" + schema + "\x00" + kind
		a := aclMap[key]
		if a == nil {
			a = &CatalogDefaultACL{Owner: owner, Schema: schema, ObjectKind: kind, Privileges: map[string][]string{}}
			aclMap[key] = a
		}
		if grantee != "" && privilege != "" {
			a.Privileges[grantee] = append(a.Privileges[grantee], privilege)
		}
	}
	if err := acls.Err(); err != nil {
		acls.Close()
		return CatalogSnapshot{}, err
	}
	acls.Close()
	for _, a := range aclMap {
		a.Privileges = normalizeGrantMap(a.Privileges)
		snapshot.DefaultACLs = append(snapshot.DefaultACLs, *a)
	}
	sessions, err := conn.Query(ctx, catalogSessionsSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier sessions query: %w", err)
	}
	for sessions.Next() {
		var s CatalogSession
		if err := sessions.Scan(&s.Role, &s.ApplicationName); err != nil {
			sessions.Close()
			return CatalogSnapshot{}, err
		}
		snapshot.Sessions = append(snapshot.Sessions, s)
	}
	if err := sessions.Err(); err != nil {
		sessions.Close()
		return CatalogSnapshot{}, err
	}
	sessions.Close()
	publicDB, err := conn.Query(ctx, catalogPublicDatabasePrivilegesSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier PUBLIC database query: %w", err)
	}
	for publicDB.Next() {
		var privilege string
		if err := publicDB.Scan(&privilege); err != nil {
			publicDB.Close()
			return CatalogSnapshot{}, err
		}
		snapshot.PublicDatabasePrivileges = append(snapshot.PublicDatabasePrivileges, privilege)
	}
	if err := publicDB.Err(); err != nil {
		publicDB.Close()
		return CatalogSnapshot{}, err
	}
	publicDB.Close()
	publicSchema, err := conn.Query(ctx, catalogPublicSchemaPrivilegesSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier PUBLIC schema query: %w", err)
	}
	for publicSchema.Next() {
		var privilege string
		if err := publicSchema.Scan(&privilege); err != nil {
			publicSchema.Close()
			return CatalogSnapshot{}, err
		}
		snapshot.PublicSchemaPrivileges = append(snapshot.PublicSchemaPrivileges, privilege)
	}
	if err := publicSchema.Err(); err != nil {
		publicSchema.Close()
		return CatalogSnapshot{}, err
	}
	publicSchema.Close()
	schemas, err := conn.Query(ctx, catalogSchemasSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier schemas query: %w", err)
	}
	schemaMap := map[string]*CatalogObject{}
	for schemas.Next() {
		var name, owner, grantee, privilege string
		if err := schemas.Scan(&name, &owner, &grantee, &privilege); err != nil {
			schemas.Close()
			return CatalogSnapshot{}, err
		}
		key := "schema\x00\x00\x00" + name
		object := schemaMap[key]
		if object == nil {
			object = &CatalogObject{Kind: "schema", Name: name, Owner: owner, Privileges: map[string][]string{}}
			schemaMap[key] = object
		}
		if grantee != "" && privilege != "" {
			object.Privileges[grantee] = append(object.Privileges[grantee], privilege)
		}
	}
	if err := schemas.Err(); err != nil {
		schemas.Close()
		return CatalogSnapshot{}, err
	}
	schemas.Close()
	for _, object := range schemaMap {
		object.Privileges = normalizeGrantMap(object.Privileges)
		snapshot.Objects = append(snapshot.Objects, *object)
	}
	databaseACLs, err := conn.Query(ctx, catalogDatabaseACLsSQL)
	if err != nil {
		return CatalogSnapshot{}, fmt.Errorf("dbroles verifier database ACL query: %w", err)
	}
	var databaseObject *CatalogObject
	for databaseACLs.Next() {
		var name, owner, grantee, privilege string
		if err := databaseACLs.Scan(&name, &owner, &grantee, &privilege); err != nil {
			databaseACLs.Close()
			return CatalogSnapshot{}, err
		}
		if databaseObject == nil {
			databaseObject = &CatalogObject{Kind: "database", Name: "current_database", Owner: owner, Privileges: map[string][]string{}}
		}
		if grantee != "" && privilege != "" {
			databaseObject.Privileges[grantee] = append(databaseObject.Privileges[grantee], privilege)
		}
	}
	if err := databaseACLs.Err(); err != nil {
		databaseACLs.Close()
		return CatalogSnapshot{}, err
	}
	databaseACLs.Close()
	if databaseObject != nil {
		databaseObject.Privileges = normalizeGrantMap(databaseObject.Privileges)
		snapshot.Objects = append(snapshot.Objects, *databaseObject)
	}
	sort.Slice(snapshot.Roles, func(i, j int) bool { return snapshot.Roles[i].Name < snapshot.Roles[j].Name })
	sort.Slice(snapshot.Memberships, func(i, j int) bool {
		return snapshot.Memberships[i].Role+snapshot.Memberships[i].Member < snapshot.Memberships[j].Role+snapshot.Memberships[j].Member
	})
	sort.Slice(snapshot.Objects, func(i, j int) bool {
		return catalogObjectIdentity(snapshot.Objects[i]) < catalogObjectIdentity(snapshot.Objects[j])
	})
	sort.Slice(snapshot.DefaultACLs, func(i, j int) bool {
		return snapshot.DefaultACLs[i].Owner+snapshot.DefaultACLs[i].Schema+snapshot.DefaultACLs[i].ObjectKind < snapshot.DefaultACLs[j].Owner+snapshot.DefaultACLs[j].Schema+snapshot.DefaultACLs[j].ObjectKind
	})
	return snapshot, nil
}

func defaultACLKind(kind string) string {
	switch kind {
	case "r":
		return "table"
	case "S":
		return "sequence"
	case "f":
		return "routine"
	case "T":
		return "type"
	case "d":
		return "domain"
	default:
		return kind
	}
}

func verifyRoles(policy Policy, actual []CatalogRole, add func(string, string, string, string, string)) {
	got := map[string]CatalogRole{}
	for _, r := range actual {
		if _, ok := got[r.Name]; ok {
			add("CATALOG_ROLE_DUPLICATE", "", r.Name, "pg_roles", "duplicate role row")
		}
		got[r.Name] = r
	}
	for name, expected := range policy.Roles {
		role, ok := got[name]
		if !ok {
			add("CATALOG_ROLE_MISSING", expected.Capability, name, "pg_roles", "required role missing")
			continue
		}
		if role.Login != expected.Login || role.Inherit != expected.Inherit || role.Superuser != expected.Superuser || role.CreateDB != expected.CreateDB || role.CreateRole != expected.CreateRole || role.Replication != expected.Replication || role.BypassRLS != expected.BypassRLS || role.DefaultTransactionReadOnly != expected.DefaultTransactionReadOnly {
			add("CATALOG_ROLE_ATTRIBUTES_DRIFT", expected.Capability, name, "pg_roles", "role attributes differ from policy")
		}
		delete(got, name)
	}
	for name := range got {
		add("CATALOG_UNKNOWN_ROLE", "", name, "pg_roles", "catalog role is outside policy")
	}
}
func verifyMemberships(policy Policy, actual []CatalogMembership, add func(string, string, string, string, string)) {
	expected := map[string]Membership{}
	for _, m := range policy.Memberships {
		expected[m.Role+"\x00"+m.Member] = m
	}
	seen := map[string]bool{}
	for _, m := range actual {
		key := m.Role + "\x00" + m.Member
		if seen[key] {
			add("CATALOG_MEMBERSHIP_DUPLICATE", m.Role, m.Member, "pg_auth_members", "duplicate membership")
		}
		seen[key] = true
		e, ok := expected[key]
		if !ok {
			add("CATALOG_UNKNOWN_MEMBERSHIP", m.Role, m.Member, "pg_auth_members", "membership is outside policy")
			continue
		}
		if e.Inherit != m.Inherit || e.Set != m.Set || e.Admin != m.Admin {
			add("CATALOG_MEMBERSHIP_OPTIONS_DRIFT", m.Role, m.Member, "pg_auth_members", "membership options differ")
		}
	}
	for key, e := range expected {
		if !seen[key] {
			add("CATALOG_MEMBERSHIP_MISSING", e.Role, e.Member, "pg_auth_members", "required membership missing")
		}
	}
}
func verifyObjects(policy Policy, actual []CatalogObject, add func(string, string, string, string, string)) {
	expected := map[string]ObjectGrant{}
	for _, o := range policy.Objects {
		expected[o.Kind+"\x00"+o.Schema+"\x00"+o.Name] = o
	}
	seen := map[string]bool{}
	for _, o := range actual {
		key := catalogObjectIdentity(o)
		if seen[key] {
			add("CATALOG_OBJECT_DUPLICATE", "", "", o.Key(), "duplicate object row")
		}
		seen[key] = true
		e, ok := expected[key]
		if !ok {
			add("CATALOG_UNKNOWN_OBJECT", "", "", o.Key(), "catalog object is outside policy")
			continue
		}
		if e.Owner != o.Owner || e.NoRuntimeAccess != o.NoRuntimeAccess || !sameStringSetExact(e.Columns, o.Columns) {
			add("CATALOG_OBJECT_SHAPE_DRIFT", "", "", o.Key(), "owner/no-runtime/columns differ")
		}
		if !grantMapsEqual(e.Privileges, o.Privileges) || !grantMapsEqual(e.TablePrivileges, o.TablePrivileges) || !columnGrantMapsEqual(e.ColumnPrivileges, o.ColumnPrivileges) {
			add("CATALOG_OBJECT_PRIVILEGE_DRIFT", "", "", o.Key(), "object or column privileges differ")
		}
	}
	for key, e := range expected {
		if !seen[key] {
			add("CATALOG_OBJECT_MISSING", "", "", e.Key(), "required object missing")
		}
	}
}
func verifyDefaultACLs(policy Policy, actual []CatalogDefaultACL, add func(string, string, string, string, string)) {
	expected := map[string]DefaultACLSpec{}
	for _, a := range policy.DefaultACLs {
		expected[a.Owner+"\x00"+a.Schema+"\x00"+a.ObjectKind] = a
	}
	seen := map[string]bool{}
	for _, a := range actual {
		key := a.Owner + "\x00" + a.Schema + "\x00" + a.ObjectKind
		if seen[key] {
			add("CATALOG_DEFAULT_ACL_DUPLICATE", "", a.Owner, a.Schema+"."+a.ObjectKind, "duplicate default ACL")
		}
		seen[key] = true
		e, ok := expected[key]
		if !ok {
			add("CATALOG_UNKNOWN_DEFAULT_ACL", "", a.Owner, a.Schema+"."+a.ObjectKind, "default ACL outside policy")
			continue
		}
		if !grantMapsEqualIgnoringRole(e.Privileges, a.Privileges, a.Owner) {
			add("CATALOG_DEFAULT_ACL_DRIFT", "", a.Owner, a.Schema+"."+a.ObjectKind, "default ACL privileges differ")
		}
	}
	for key, e := range expected {
		if !seen[key] {
			add("CATALOG_DEFAULT_ACL_MISSING", "", e.Owner, e.Schema+"."+e.ObjectKind, "required default ACL missing")
		}
	}
}

func grantMapsEqualIgnoringRole(a, b map[string][]string, ignored string) bool {
	left, right := cloneGrantMap(a), cloneGrantMap(b)
	delete(left, ignored)
	delete(right, ignored)
	return grantMapsEqual(left, right)
}
func grantMapsEqual(a, b map[string][]string) bool {
	roles := map[string]bool{}
	for role := range a {
		roles[role] = true
	}
	for role := range b {
		roles[role] = true
	}
	for role := range roles {
		pa, pb := a[role], b[role]
		if len(pa) == 0 && len(pb) == 0 {
			continue
		}
		if !sameStringSetExact(pa, pb) {
			return false
		}
	}
	return true
}
func columnGrantMapsEqual(a, b map[string]map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for role, ca := range a {
		cb, ok := b[role]
		if !ok || len(ca) != len(cb) {
			return false
		}
		for col, pa := range ca {
			if !sameStringSetExact(pa, cb[col]) {
				return false
			}
		}
	}
	return true
}

// OpenVerifierPool validates a caller-supplied DSN and opens a pgxpool. It is
// a convenience for the CLI; the verifier itself never accepts a password in
// policy/evidence output.
func OpenVerifierPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if err := pgdsn.Validate(dsn, true); err != nil {
		return nil, errors.New("DATABASE_URL rejected")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("DATABASE_URL parse failed")
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams[pgreadonly.ReadOnlyParam] = "on"
	cfg.AfterConnect = pgreadonly.VerifyReadOnly
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("database connection failed")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("database ping failed")
	}
	return pool, nil
}
