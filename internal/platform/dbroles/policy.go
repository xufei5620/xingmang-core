// Package dbroles contains the offline, machine-readable contract for the
// PostgreSQL role split.  Task 1 intentionally has no database or credential
// dependencies: it only parses and validates immutable policy artifacts.
package dbroles

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
	"time"
	"unicode/utf8"
)

const (
	PolicyVersionV1 = 1

	RotationSteadyA    = "steady-a"
	RotationRotatingAB = "rotating-a-b"
	RotationSteadyB    = "steady-b"

	EventKindGenesis       = "genesis"
	EventKindPolicyUpdate  = "policy-update"
	EventKindRotationStart = "rotation-start"
	EventKindRotationClose = "rotation-close"
)

var (
	digestPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	rolePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	namePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	routineNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}\([^\r\n;{}]*\)$`)
	shaPrefixPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Violation is a deterministic, secret-free policy finding.  Code is stable
// and suitable for automation; the other fields identify the offending
// capability/object without embedding credentials or DSNs.
type Violation struct {
	Code       string `json:"code"`
	Capability string `json:"capability,omitempty"`
	Identity   string `json:"identity,omitempty"`
	Object     string `json:"object,omitempty"`
	Message    string `json:"message"`
}

// RoleSpec describes one role's immutable attributes. Capability roles are
// NOLOGIN; login identities inherit exactly one capability role.
type RoleSpec struct {
	Kind                       string `json:"kind"`
	Capability                 string `json:"capability,omitempty"`
	Login                      bool   `json:"login"`
	Inherit                    bool   `json:"inherit"`
	Superuser                  bool   `json:"superuser"`
	CreateDB                   bool   `json:"createdb"`
	CreateRole                 bool   `json:"createrole"`
	Replication                bool   `json:"replication"`
	BypassRLS                  bool   `json:"bypassrls"`
	DefaultTransactionReadOnly bool   `json:"default_transaction_read_only"`
}

// Membership is an exact role membership edge. PostgreSQL 16+ exposes the
// inherit/set/admin options; all runtime edges are pinned explicitly.
type Membership struct {
	Role    string `json:"role"`
	Member  string `json:"member"`
	Inherit bool   `json:"inherit"`
	Set     bool   `json:"set"`
	Admin   bool   `json:"admin"`
}

// ObjectGrant enumerates one database object and its role privileges. For
// tables, TablePrivileges and ColumnPrivileges are intentionally separate so
// table UPDATE cannot be confused with a narrow column UPDATE.
type ObjectGrant struct {
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

// DefaultACLSpec describes defaults created by the migrator for one object
// family. Empty privilege arrays are meaningful and are never omitted from
// the checked-in contract.
type DefaultACLSpec struct {
	Owner      string              `json:"owner"`
	Schema     string              `json:"schema"`
	ObjectKind string              `json:"object_kind"`
	Privileges map[string][]string `json:"privileges"`
}

// RotationSpec is the nine-field A/B rotation state required by DBR0. Empty
// strings represent JSON null for nullable fields when marshalled. Keeping
// the Go fields as strings makes callers able to construct fixtures without a
// custom nullable type while MarshalJSON still emits the contract's nulls.
type RotationSpec struct {
	State                 string `json:"state"`
	SteadyIdentity        string `json:"steady_identity"`
	OldIdentity           string `json:"old_identity"`
	NewIdentity           string `json:"new_identity"`
	ApprovedChangeRequest string `json:"approved_change_request"`
	StartedAt             string `json:"started_at"`
	Deadline              string `json:"deadline"`
	ClosureEvidence       string `json:"closure_evidence"`
	ClosedAt              string `json:"closed_at"`
}

func (r RotationSpec) MarshalJSON() ([]byte, error) {
	type wire struct {
		State                 string `json:"state"`
		SteadyIdentity        any    `json:"steady_identity"`
		OldIdentity           any    `json:"old_identity"`
		NewIdentity           any    `json:"new_identity"`
		ApprovedChangeRequest any    `json:"approved_change_request"`
		StartedAt             any    `json:"started_at"`
		Deadline              any    `json:"deadline"`
		ClosureEvidence       any    `json:"closure_evidence"`
		ClosedAt              any    `json:"closed_at"`
	}
	nullable := func(value string) any {
		if value == "" {
			return nil
		}
		return value
	}
	return json.Marshal(wire{State: r.State, SteadyIdentity: nullable(r.SteadyIdentity), OldIdentity: nullable(r.OldIdentity), NewIdentity: nullable(r.NewIdentity), ApprovedChangeRequest: nullable(r.ApprovedChangeRequest), StartedAt: nullable(r.StartedAt), Deadline: nullable(r.Deadline), ClosureEvidence: nullable(r.ClosureEvidence), ClosedAt: nullable(r.ClosedAt)})
}

// Policy is the complete v1 role/ACL contract.
type Policy struct {
	Schema                   string                  `json:"$schema,omitempty"`
	ID                       string                  `json:"$id,omitempty"`
	Version                  int                     `json:"version"`
	DatabaseSelector         string                  `json:"database_selector"`
	ProductionDatabaseName   string                  `json:"production_database_name"`
	Owner                    string                  `json:"owner"`
	CapabilityRoles          []string                `json:"capability_roles"`
	Roles                    map[string]RoleSpec     `json:"roles"`
	Memberships              []Membership            `json:"memberships"`
	Objects                  []ObjectGrant           `json:"objects"`
	DefaultACLs              []DefaultACLSpec        `json:"default_acls"`
	PublicDatabasePrivileges []string                `json:"public_database_privileges"`
	PublicSchemaPrivileges   []string                `json:"public_schema_privileges"`
	PublicTypePrivileges     []string                `json:"public_type_privileges"`
	PublicRoutinePrivileges  []string                `json:"public_routine_privileges"`
	PublicSchemaOwner        string                  `json:"public_schema_owner"`
	CustomSchemaDefault      string                  `json:"custom_schema_default"`
	PublicSchemaContract     string                  `json:"public_schema_contract"`
	RunwayApprovedMergeSHA   string                  `json:"runway_approved_merge_sha"`
	RunwayObjects            []string                `json:"runway_objects"`
	Rotations                map[string]RotationSpec `json:"rotations"`
	ActiveSessions           []string                `json:"active_sessions,omitempty"`
}

// Rotation returns a copy of a capability's rotation object. Unknown names
// return the zero value, allowing callers to treat absence as a violation.
func (p Policy) Rotation(capability string) RotationSpec { return p.Rotations[capability] }

// SetRotationTopology updates the declarative role/membership side of one
// rotation state. It is an offline convenience for transition builders; it
// does not execute SQL or touch a database.
func (p *Policy) SetRotationTopology(capability, state string) error {
	if p == nil {
		return errors.New("dbroles policy: nil policy")
	}
	r, ok := p.Rotations[capability]
	if !ok {
		return fmt.Errorf("dbroles policy: unknown capability %q", capability)
	}
	a, b := "xm_"+identityPrefix(capability)+"_a", "xm_"+identityPrefix(capability)+"_b"
	switch state {
	case RotationSteadyA:
		r.State, r.SteadyIdentity, r.OldIdentity, r.NewIdentity = state, a, "", ""
		r.ApprovedChangeRequest, r.StartedAt, r.Deadline, r.ClosureEvidence, r.ClosedAt = "", "", "", "", ""
	case RotationRotatingAB:
		r.State, r.SteadyIdentity, r.OldIdentity, r.NewIdentity = state, "", a, b
		r.ClosureEvidence, r.ClosedAt = "", ""
	case RotationSteadyB:
		r.State, r.SteadyIdentity = state, b
	default:
		return fmt.Errorf("dbroles policy: unknown rotation state %q", state)
	}
	p.Rotations[capability] = r
	if role, ok := p.Roles[a]; ok {
		role.Login = state == RotationSteadyA || state == RotationRotatingAB
		p.Roles[a] = role
	}
	if role, ok := p.Roles[b]; ok {
		role.Login = state == RotationRotatingAB || state == RotationSteadyB
		p.Roles[b] = role
	}
	filtered := p.Memberships[:0]
	for _, membership := range p.Memberships {
		if membership.Role == capability && (membership.Member == a || membership.Member == b) {
			continue
		}
		filtered = append(filtered, membership)
	}
	for _, member := range []string{a, b} {
		if (state == RotationSteadyA && member == a) || (state == RotationRotatingAB) || (state == RotationSteadyB && member == b) {
			filtered = append(filtered, Membership{Role: capability, Member: member, Inherit: true, Set: false, Admin: false})
		}
	}
	p.Memberships = filtered
	return nil
}

// Grant returns the exact table/object privileges for role, sorted and
// deduplicated. It accepts either a schema/object pair or a fully qualified
// object in schema when schema is empty.
func (p Policy) Grant(schema, object, role string) []string {
	if len(p.ValidateCurrentAt(time.Time{})) > 0 {
		return nil
	}
	key := objectKey(schema, object)
	for _, grant := range p.Objects {
		if grant.Key() != key {
			continue
		}
		values := append([]string(nil), grant.Privileges[role]...)
		values = append(values, grant.TablePrivileges[role]...)
		return sortedUnique(values)
	}
	return nil
}

// Allows checks a table/object-level privilege. It fails closed for unknown
// roles, objects, privileges, and column-only grants.
func (p Policy) Allows(role, object, privilege string) bool {
	if role == "" || object == "" || privilege == "" {
		return false
	}
	if len(p.ValidateCurrentAt(time.Time{})) > 0 {
		return false
	}
	for _, grant := range p.Objects {
		if grant.Key() != object {
			continue
		}
		if contains(grant.Privileges[role], privilege) || contains(grant.TablePrivileges[role], privilege) {
			return true
		}
		return false
	}
	return false
}

// AllowsTable is explicit table-level lookup; it never treats column ACL as
// table UPDATE.
func (p Policy) AllowsTable(role, object, privilege string) bool {
	if len(p.ValidateCurrentAt(time.Time{})) > 0 {
		return false
	}
	for _, grant := range p.Objects {
		if grant.Key() == object {
			return contains(grant.TablePrivileges[role], privilege) || contains(grant.Privileges[role], privilege)
		}
	}
	return false
}

// AllowsColumns requires the requested columns to each have the privilege.
// An empty column list is false, preventing accidental broad UPDATE checks.
func (p Policy) AllowsColumns(role, object, privilege string, columns []string) bool {
	if len(columns) == 0 {
		return false
	}
	if len(p.ValidateCurrentAt(time.Time{})) > 0 {
		return false
	}
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		if column == "" || seen[column] {
			return false
		}
		seen[column] = true
	}
	for _, grant := range p.Objects {
		if grant.Key() != object {
			continue
		}
		for _, column := range columns {
			if !contains(grant.Columns, column) {
				return false
			}
			if !contains(grant.ColumnPrivileges[role][column], privilege) {
				return false
			}
		}
		return true
	}
	return false
}

func (o ObjectGrant) Key() string { return objectKey(o.Schema, o.Name) }
func objectKey(schema, object string) string {
	if schema == "" {
		return object
	}
	if strings.Contains(object, ".") {
		return object
	}
	return schema + "." + object
}

// DefaultPolicyV1 returns the approved initial steady-a policy. It is fully
// offline and contains no secrets or external state.
func DefaultPolicyV1() Policy {
	capabilities := []string{"xm_api_runtime", "xm_worker_runtime", "xm_lifecycle_runtime", "xm_ops_read", "xm_backup_read"}
	roles := map[string]RoleSpec{
		"xm_migrator": {Kind: "owner", Login: true, Inherit: true},
	}
	memberships := make([]Membership, 0, len(capabilities))
	for _, capability := range capabilities {
		roles[capability] = RoleSpec{Kind: "capability", Inherit: true}
		prefix := strings.TrimPrefix(capability, "xm_")
		prefix = strings.TrimSuffix(prefix, "_runtime")
		if capability == "xm_ops_read" {
			prefix = "ops"
		}
		if capability == "xm_backup_read" {
			prefix = "backup"
		}
		a := "xm_" + prefix + "_a"
		b := "xm_" + prefix + "_b"
		roles[a] = RoleSpec{Kind: "login", Capability: capability, Login: true, Inherit: true, DefaultTransactionReadOnly: capability == "xm_ops_read" || capability == "xm_backup_read"}
		roles[b] = RoleSpec{Kind: "login", Capability: capability, Login: false, Inherit: true, DefaultTransactionReadOnly: capability == "xm_ops_read" || capability == "xm_backup_read"}
		memberships = append(memberships, Membership{Role: capability, Member: a, Inherit: true, Set: false, Admin: false})
	}
	objects := defaultObjects()
	policy := Policy{
		Schema:           "https://json-schema.org/draft/2020-12/schema",
		ID:               "https://xingmang.local/contracts/database/role-policy.v1",
		Version:          PolicyVersionV1,
		DatabaseSelector: "current_database", ProductionDatabaseName: "xingmang", Owner: "xm_migrator",
		CapabilityRoles: capabilities, Roles: roles, Memberships: memberships, Objects: objects,
		DefaultACLs: defaultACLs(capabilities), PublicDatabasePrivileges: []string{}, PublicSchemaPrivileges: []string{}, PublicTypePrivileges: []string{}, PublicRoutinePrivileges: []string{},
		PublicSchemaOwner: "pg_database_owner", CustomSchemaDefault: "deny", PublicSchemaContract: "river-only",
		RunwayApprovedMergeSHA: "809eec2df487abb53ab99c7a1295bd20750cc324", RunwayObjects: []string{"finance.runway_threshold_config", "finance.runway_threshold_history", "finance.runway_threshold_current_verified"},
		Rotations: defaultRotations(capabilities),
	}
	normalizePolicy(&policy)
	return policy
}

func defaultRotations(capabilities []string) map[string]RotationSpec {
	rotations := make(map[string]RotationSpec, len(capabilities))
	for _, capability := range capabilities {
		prefix := strings.TrimPrefix(capability, "xm_")
		prefix = strings.TrimSuffix(prefix, "_runtime")
		if capability == "xm_ops_read" {
			prefix = "ops"
		}
		if capability == "xm_backup_read" {
			prefix = "backup"
		}
		rotations[capability] = RotationSpec{State: RotationSteadyA, SteadyIdentity: "xm_" + prefix + "_a"}
	}
	return rotations
}

func defaultObjects() []ObjectGrant {
	var out []ObjectGrant
	add := func(kind, schema, name string, grants map[string][]string) {
		object := ObjectGrant{Kind: kind, Schema: schema, Name: name, Owner: "xm_migrator", Columns: tableColumns(kind, schema, name)}
		if kind == "table" || kind == "view" {
			object.TablePrivileges = cloneGrantMap(grants)
		} else {
			object.Privileges = cloneGrantMap(grants)
		}
		out = append(out, object)
	}
	addCols := func(kind, schema, name string, grants map[string][]string, cols map[string]map[string][]string) {
		out = append(out, ObjectGrant{Kind: kind, Schema: schema, Name: name, Owner: "xm_migrator", Columns: tableColumns(kind, schema, name), TablePrivileges: cloneGrantMap(grants), ColumnPrivileges: cloneColumnMap(cols)})
	}
	readAll := map[string][]string{"xm_api_runtime": {"SELECT"}, "xm_worker_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}
	add("database", "", "current_database", map[string][]string{"xm_migrator": {"CONNECT", "CREATE", "TEMPORARY"}, "xm_api_runtime": {"CONNECT"}, "xm_worker_runtime": {"CONNECT"}, "xm_lifecycle_runtime": {"CONNECT"}, "xm_ops_read": {"CONNECT"}, "xm_backup_read": {"CONNECT"}})
	for _, schema := range []string{"core", "action", "audit", "ops", "alerts", "finance", "ui"} {
		add("schema", "", schema, map[string][]string{"xm_migrator": {"USAGE", "CREATE"}, "xm_api_runtime": {"USAGE"}, "xm_worker_runtime": {"USAGE"}, "xm_lifecycle_runtime": {"USAGE"}, "xm_ops_read": {"USAGE"}, "xm_backup_read": {"USAGE"}})
	}
	add("schema", "", "public", map[string][]string{"xm_migrator": {"USAGE", "CREATE"}, "xm_worker_runtime": {"USAGE"}})
	out[len(out)-1].Owner = "pg_database_owner"
	add("table", "core", "environment", map[string][]string{"xm_api_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT", "INSERT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "core", "service", map[string][]string{"xm_api_runtime": {"SELECT", "INSERT", "UPDATE"}, "xm_lifecycle_runtime": {"SELECT", "INSERT"}, "xm_worker_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "core", "connector", map[string][]string{"xm_api_runtime": {"SELECT", "INSERT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "core", "connection", map[string][]string{"xm_api_runtime": {"SELECT", "INSERT", "UPDATE"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	// core.schema_migration_state (migration 000050) is the sanctioned read-only
	// projection of public.schema_migrations. Design §7.6 keeps `public`
	// River-only and marks schema_migrations no-runtime-access; the same section
	// names "another approved read-only view" as the way out, and this is it.
	// Reading through a core view means no runtime role needs USAGE on `public`.
	//
	// xm_worker_runtime is deliberately absent: §7.6 states the worker gets no
	// access to schema_migrations, and granting it here would be an end-run
	// around that decision. xm_lifecycle_runtime is absent for lack of any need.
	add("view", "core", "schema_migration_state", map[string][]string{"xm_api_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "action", "action_run", map[string][]string{"xm_api_runtime": {"INSERT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "audit", "audit_event", map[string][]string{"xm_api_runtime": {"SELECT", "INSERT"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	addCols("table", "audit", "chain_root", map[string][]string{"xm_lifecycle_runtime": {"SELECT", "INSERT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}, map[string]map[string][]string{"xm_lifecycle_runtime": {"exported_at": {"UPDATE"}, "export_target": {"UPDATE"}}})
	add("table", "ops", "metric_observation", map[string][]string{"xm_api_runtime": {"SELECT"}, "xm_worker_runtime": {"SELECT", "INSERT", "UPDATE"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "ops", "metric_observation_sample", map[string][]string{"xm_api_runtime": {"SELECT"}, "xm_worker_runtime": {"SELECT", "INSERT", "DELETE"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "alerts", "alert", map[string][]string{"xm_api_runtime": {"SELECT", "UPDATE"}, "xm_worker_runtime": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	add("table", "alerts", "alert_silence", map[string][]string{"xm_api_runtime": {"SELECT", "INSERT"}, "xm_worker_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	for _, name := range []string{"upstream_account", "token_map", "profit_daily", "proxy_asset", "subscription_cost_batch", "amortization_loss", "balance_history", "platform_channel_binding", "runway_threshold_config", "runway_threshold_history", "runway_threshold_current_verified"} {
		grants := readAll
		if name == "upstream_account" {
			grants = map[string][]string{"xm_api_runtime": {"SELECT", "INSERT", "UPDATE"}, "xm_worker_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}
		}
		if name == "token_map" || name == "platform_channel_binding" {
			grants = map[string][]string{"xm_api_runtime": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "xm_worker_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}
		}
		if name == "profit_daily" || name == "balance_history" || name == "amortization_loss" {
			grants = map[string][]string{"xm_api_runtime": {"SELECT"}, "xm_worker_runtime": {"SELECT", "INSERT", "UPDATE"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}
		}
		if name == "proxy_asset" || name == "subscription_cost_batch" {
			grants = map[string][]string{"xm_api_runtime": {"SELECT", "INSERT", "UPDATE"}, "xm_worker_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}
		}
		if name == "runway_threshold_config" {
			grants = map[string][]string{"xm_api_runtime": {"SELECT", "UPDATE"}, "xm_worker_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT", "INSERT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}
		}
		if name == "runway_threshold_history" {
			grants = map[string][]string{"xm_api_runtime": {"SELECT", "INSERT"}, "xm_worker_runtime": {"SELECT"}, "xm_lifecycle_runtime": {"SELECT", "INSERT"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}}
		}
		kind := "table"
		if name == "runway_threshold_current_verified" {
			kind = "view"
		}
		add(kind, "finance", name, grants)
	}
	add("table", "ui", "saved_view", map[string][]string{"xm_api_runtime": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	for _, name := range []string{"archive_segment", "archive_operation_intent", "archive_put_receipt", "archive_terminal_receipt"} {
		add("table", "audit", name, map[string][]string{})
		out[len(out)-1].NoRuntimeAccess = true
	}
	add("table", "public", "schema_migrations", map[string][]string{})
	out[len(out)-1].NoRuntimeAccess = true
	add("table", "public", "river_migration", map[string][]string{"xm_worker_runtime": {"SELECT", "INSERT", "UPDATE", "DELETE"}})
	for _, name := range []string{"river_queue", "river_notification", "river_leader"} {
		add("table", "public", name, map[string][]string{"xm_worker_runtime": {"SELECT", "INSERT", "UPDATE", "DELETE"}})
	}
	add("table", "public", "river_job", map[string][]string{"xm_worker_runtime": {"SELECT", "INSERT", "UPDATE", "DELETE"}})
	add("sequence", "ops", "metric_observation_sample_id_seq", map[string][]string{"xm_worker_runtime": {"USAGE"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	for _, name := range []string{"balance_history_id_seq"} {
		add("sequence", "finance", name, map[string][]string{"xm_worker_runtime": {"USAGE"}, "xm_ops_read": {"SELECT"}, "xm_backup_read": {"SELECT"}})
	}
	for _, name := range []string{"river_job_id_seq", "river_notification_id_seq"} {
		add("sequence", "public", name, map[string][]string{"xm_worker_runtime": {"USAGE"}})
	}
	add("type", "public", "river_job_state", map[string][]string{"xm_worker_runtime": {"USAGE"}})
	add("routine", "finance", "reject_runway_threshold_history_mutation()", map[string][]string{})
	add("routine", "audit", "reject_archive_catalog_mutation()", map[string][]string{})
	// River's routine inventory is explicit and uses identity-argument names
	// so overloaded functions cannot be collapsed into one ACL entry.
	add("routine", "public", "river_job_state_in_bitmask(bit, river_job_state)", map[string][]string{"xm_worker_runtime": {"EXECUTE"}})
	return out
}

// tableColumns is the checked-in relation-column inventory used to reject
// column ACL drift. It mirrors the migrations at the approved release base;
// a new migration must update the policy before its column can be granted.
func tableColumns(kind, schema, name string) []string {
	if kind != "table" && kind != "view" {
		return nil
	}
	key := schema + "." + name
	columns := map[string][]string{
		"core.environment": {"id", "description", "created_at"},
		"core.service":     {"id", "service_type", "instance_id", "environment", "endpoint", "internal_endpoint", "owner", "health_check_path", "native_console_url", "runbook_path", "status", "source_watermark", "observed_at", "created_at", "updated_at"},
		"core.connector":   {"id", "key", "version", "contract_version", "connection_schema_path", "target_allowlist", "read_capabilities", "write_capabilities", "supported_upstream_versions", "compatibility_test_path", "created_at", "updated_at"},
		"core.connection":  {"id", "connector_id", "service_id", "environment", "credential_ref", "target_allowlist", "granted_capabilities", "kill_switch", "status", "detected_upstream_version", "version_fingerprint", "last_verified_at", "created_at", "updated_at"},
		// Mirrors public.schema_migrations exactly: golang-migrate's two columns.
		"core.schema_migration_state":               {"version", "dirty"},
		"action.action_run":                         {"id", "action_id", "action_version", "principal_id", "principal_type", "environment", "request_id", "risk_level", "status", "error_code", "duration_ms", "started_at", "finished_at"},
		"audit.audit_event":                         {"id", "sequence", "occurred_at", "recorded_at", "principal_id", "principal_type", "action_id", "action_version", "action_run_id", "resource_type", "resource_id", "environment", "reason", "approval_id", "request_id", "trace_id", "source_ip", "before_summary", "after_summary", "connector_request_summary", "connector_response_summary", "result", "compensation_result", "prev_hash", "event_hash", "canonical_version"},
		"audit.chain_root":                          {"id", "computed_at", "from_sequence", "to_sequence", "root_hash", "signature", "key_id", "exported_at", "export_target"},
		"audit.archive_segment":                     {"id", "format_version", "from_sequence", "to_sequence", "row_count", "first_prev_hash", "last_event_hash", "canonical_version_counts", "environment_counts", "payload_object_key", "payload_version_id", "payload_sha256", "payload_size_bytes", "projections", "manifest_object_key", "manifest_version_id", "manifest_sha256", "manifest_signature", "manifest_key_id", "chain_root_id", "chain_root_hash", "checkpoint_sha256", "recovery_generation", "committed_at", "verified_at"},
		"audit.archive_operation_intent":            {"operation_id", "approval_envelope_sha256", "deterministic_bytes_digest", "canonical_intent_bytes", "created_at"},
		"audit.archive_put_receipt":                 {"operation_id", "ordinal", "object_version_bytes", "object_version_sha256", "recorded_at"},
		"audit.archive_terminal_receipt":            {"operation_id", "signed_result_bytes", "terminal_result_digest", "optional_artifact_ref_bytes", "recorded_at"},
		"ops.metric_observation":                    {"id", "metric_key", "source", "environment", "observed_at", "synced_at", "watermark", "status", "is_partial", "last_success", "last_error_code", "staleness_threshold_seconds", "value_json", "updated_at"},
		"ops.metric_observation_sample":             {"id", "metric_key", "source", "environment", "observed_at", "synced_at", "status", "is_partial", "watermark", "last_error_code", "value_json"},
		"alerts.alert":                              {"id", "rule_key", "dedup_key", "severity", "status", "title", "detail", "environment", "opened_at", "acknowledged_at", "resolved_at", "last_seen_at", "fire_count", "source_metric_key", "notify_status", "notify_error", "notified_at", "created_at", "updated_at"},
		"alerts.alert_silence":                      {"id", "rule_key", "environment", "reason", "starts_at", "ends_at", "created_by", "created_at"},
		"finance.upstream_account":                  {"id", "system_type", "access_method", "base_url", "credential_ref", "recharge_ratio", "currency", "business_day_tz", "status", "environment", "created_at", "updated_at", "platform_id", "group_rate", "upstream_name", "upstream_contact", "upstream_group"},
		"finance.token_map":                         {"upstream_account_id", "upstream_token_id", "own_account_id", "credential_ref", "created_at", "updated_at"},
		"finance.profit_daily":                      {"upstream_account_id", "business_day", "business_day_tz", "token_id", "account_id", "platform_id", "revenue_minor", "cost_minor", "profit_minor", "currency", "ratio_snapshot", "source", "cost_observed_at", "revenue_observed_at", "updated_at"},
		"finance.proxy_asset":                       {"id", "paid_minor", "surcharge_minor", "refunded_minor", "refunded_on", "currency", "opened_on", "expires_on", "terminated_on", "shared_account_count", "buy_platform", "buy_address", "credential_ref", "mounted", "environment", "created_at", "updated_at"},
		"finance.subscription_cost_batch":           {"id", "upstream_account_id", "paid_minor", "surcharge_minor", "refunded_minor", "refunded_on", "currency", "starts_on", "expires_on", "terminated_on", "account_count", "proxy_batch_id", "created_at", "updated_at"},
		"finance.amortization_loss":                 {"id", "batch_id", "proxy_asset_id", "loss_minor", "currency", "booked_on", "created_at", "updated_at"},
		"finance.balance_history":                   {"id", "upstream_account_id", "balance_minor", "currency", "captured_at", "observed_at", "source"},
		"finance.platform_channel_binding":          {"id", "environment", "service_id", "external_channel_id", "upstream_account_id", "valid_from", "valid_to", "provenance", "reason", "created_by", "created_at"},
		"finance.runway_threshold_config":           {"environment", "critical_days", "warning_days", "serious_days", "revision", "updated_at", "updated_by", "reason", "request_id"},
		"finance.runway_threshold_history":          {"environment", "revision", "critical_days", "warning_days", "serious_days", "changed_at", "changed_by", "reason", "request_id", "change_source"},
		"finance.runway_threshold_current_verified": {"environment", "critical_days", "warning_days", "serious_days", "revision", "updated_at", "updated_by", "reason", "request_id"},
		"ui.saved_view":                             {"id", "owner_issuer", "owner_subject", "identity_zone", "environment", "table_key", "name", "state_version", "query", "filters", "sort_column", "sort_direction", "known_columns", "visible_columns", "density", "state_hash", "created_at", "updated_at"},
		"public.schema_migrations":                  {"version", "dirty"},
		"public.river_migration":                    {"line", "version", "created_at"},
		"public.river_job":                          {"id", "state", "attempt", "max_attempts", "attempted_at", "created_at", "finalized_at", "scheduled_at", "priority", "attempted_by", "errors", "kind", "args", "metadata", "queue", "tags", "unique_key", "unique_states"},
		"public.river_queue":                        {"name", "created_at", "metadata", "paused_at", "updated_at"},
		"public.river_notification":                 {"id", "created_at", "payload", "topic"},
		"public.river_leader":                       {"elected_at", "expires_at", "leader_id", "name"},
	}
	return append([]string(nil), columns[key]...)
}

func defaultACLs(capabilities []string) []DefaultACLSpec {
	var out []DefaultACLSpec
	for _, schema := range []string{"core", "action", "audit", "ops", "alerts", "finance", "ui"} {
		for _, kind := range []string{"table", "sequence", "routine", "type", "domain"} {
			privileges := map[string][]string{"PUBLIC": {}}
			for _, capability := range capabilities {
				privileges[capability] = []string{}
			}
			out = append(out, DefaultACLSpec{Owner: "xm_migrator", Schema: schema, ObjectKind: kind, Privileges: privileges})
		}
	}
	for _, kind := range []string{"table", "sequence", "routine", "type", "domain"} {
		worker := []string{}
		switch kind {
		case "table":
			worker = []string{"SELECT", "INSERT", "UPDATE", "DELETE"}
		case "sequence":
			worker = []string{"USAGE"}
		case "routine":
			worker = []string{"EXECUTE"}
		}
		out = append(out, DefaultACLSpec{Owner: "xm_migrator", Schema: "public", ObjectKind: kind, Privileges: map[string][]string{"PUBLIC": {}, "xm_worker_runtime": worker}})
	}
	return out
}

func cloneGrantMap(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for role, values := range in {
		out[role] = sortedUnique(values)
	}
	return out
}
func cloneColumnMap(in map[string]map[string][]string) map[string]map[string][]string {
	out := make(map[string]map[string][]string, len(in))
	for role, cols := range in {
		out[role] = make(map[string][]string, len(cols))
		for col, values := range cols {
			out[role][col] = sortedUnique(values)
		}
	}
	return out
}
func sortedUnique(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	j := 0
	for _, value := range out {
		if j == 0 || out[j-1] != value {
			out[j] = value
			j++
		}
	}
	return out[:j]
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// LoadPolicy strictly decodes a policy document, validates its current state,
// normalizes order-insensitive collections, and returns the semantic
// canonical SHA-256. The raw artifact digest is deliberately separate:
// transition events bind exact bytes through RawDigest.
func LoadPolicy(data []byte) (Policy, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Policy{}, "", errors.New("dbroles policy: empty document")
	}
	policy, err := loadPolicyUnchecked(data)
	if err != nil {
		return Policy{}, "", err
	}
	if violations := policy.ValidateCurrentAt(time.Now().UTC()); len(violations) > 0 {
		return Policy{}, "", fmt.Errorf("dbroles policy invalid: %s", violations[0].Message)
	}
	normalizePolicy(&policy)
	canonical, err := json.Marshal(policy)
	if err != nil {
		return Policy{}, "", fmt.Errorf("dbroles policy canonical encoding: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return policy, hex.EncodeToString(sum[:]), nil
}

// loadPolicyUnchecked performs strict decoding and shape checks but leaves
// wall-clock rotation expiry to the caller. This lets transition validation
// return the stable ROTATION_DEADLINE_EXPIRED code instead of collapsing it
// into a generic decode error.
func loadPolicyUnchecked(data []byte) (Policy, error) {
	if !utf8.Valid(data) {
		return Policy{}, errors.New("dbroles policy: invalid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Policy{}, err
	}
	for _, field := range []string{"version", "database_selector", "production_database_name", "owner", "capability_roles", "roles", "memberships", "objects", "default_acls", "public_database_privileges", "public_schema_privileges", "public_type_privileges", "public_routine_privileges", "public_schema_owner", "custom_schema_default", "public_schema_contract", "runway_approved_merge_sha", "runway_objects", "rotations"} {
		if !topLevelFieldPresent(data, field) {
			return Policy{}, fmt.Errorf("dbroles policy: missing %s", field)
		}
	}
	decodeData := data
	// The original DBR1 plan example used a string literal for version while
	// the repository's contracts use numeric versions. Accept that one legacy
	// spelling but canonicalize to numeric 1; all other versions/types fail.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Policy{}, fmt.Errorf("dbroles policy decode: %w", err)
	}
	if raw, ok := top["version"]; ok && len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || value != "1" {
			return Policy{}, errors.New("dbroles policy: version must be 1")
		}
		top["version"] = json.RawMessage("1")
		normalized, err := json.Marshal(top)
		if err != nil {
			return Policy{}, err
		}
		decodeData = normalized
	}
	dec := json.NewDecoder(bytes.NewReader(decodeData))
	dec.DisallowUnknownFields()
	var policy Policy
	if err := dec.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("dbroles policy decode: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Policy{}, errors.New("dbroles policy: trailing JSON")
		}
		return Policy{}, fmt.Errorf("dbroles policy trailing data: %w", err)
	}
	if err := validateRotationFieldPresence(data); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// LoadPolicyBytes is an alias for callers that prefer an explicit bytes name.
func LoadPolicyBytes(data []byte) (Policy, string, error) { return LoadPolicy(data) }

// CanonicalHash returns the semantic digest of a validated policy value.
func (p Policy) CanonicalHash() (string, error) { _, digest, err := p.Canonical(); return digest, err }

// RawDigest returns the SHA-256 of exact artifact bytes, without trimming or
// JSON normalization. It is used by state-event bindings.
func RawDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Canonical returns normalized policy bytes and their semantic digest.
func (p Policy) Canonical() ([]byte, string, error) {
	// Canonicalization is a content operation; expiry is evaluated separately
	// by LoadPolicy/ValidateCurrentAt and must not make historical artifacts
	// impossible to hash for transition review.
	if violations := p.ValidateCurrentAt(time.Time{}); len(violations) > 0 {
		return nil, "", fmt.Errorf("dbroles policy invalid: %s", violations[0].Message)
	}
	normalized := clonePolicy(p)
	normalizePolicy(&normalized)
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", err
	}
	return b, RawDigest(b), nil
}

// clonePolicy keeps Canonical side-effect free even though normalization
// sorts nested slices/maps. JSON round-tripping is acceptable here because
// policy artifacts are small and this path is offline review code.
func clonePolicy(p Policy) Policy {
	b, err := json.Marshal(p)
	if err != nil {
		return p
	}
	var copy Policy
	if err := json.Unmarshal(b, &copy); err != nil {
		return p
	}
	return copy
}

// Validate checks the current policy against wall-clock state.
func (p Policy) Validate() error { return p.ValidateAt(time.Now().UTC()) }
func (p Policy) ValidateAt(now time.Time) error {
	violations := p.ValidateCurrentAt(now)
	if len(violations) > 0 {
		return errors.New(violations[0].Message)
	}
	return nil
}

// ValidateCurrent is a structured form useful to the catalog verifier.
func (p Policy) ValidateCurrent() []Violation { return p.ValidateCurrentAt(time.Now().UTC()) }

// ValidateCurrentAt validates structural policy and the nine-field rotation
// state. It performs no I/O and never emits secret values.
func (p Policy) ValidateCurrentAt(now time.Time) []Violation {
	var violations []Violation
	add := func(code, capability, identity, object, message string) {
		violations = append(violations, Violation{Code: code, Capability: capability, Identity: identity, Object: object, Message: message})
	}
	if p.Version != PolicyVersionV1 {
		add("POLICY_VERSION_UNSUPPORTED", "", "", "", fmt.Sprintf("unsupported policy version %d", p.Version))
		return violations
	}
	if p.Schema != "" && p.Schema != "https://json-schema.org/draft/2020-12/schema" {
		add("POLICY_SCHEMA_INVALID", "", "", "", "$schema must use the pinned JSON Schema URI")
	}
	if p.ID != "" && p.ID != "https://xingmang.local/contracts/database/role-policy.v1" {
		add("POLICY_ID_INVALID", "", "", "", "$id must use the pinned role policy URI")
	}
	if p.DatabaseSelector != "current_database" {
		add("POLICY_DATABASE_SELECTOR_INVALID", "", "", "", "database_selector must be current_database")
	}
	if p.ProductionDatabaseName == "" || !namePattern.MatchString(p.ProductionDatabaseName) {
		add("POLICY_DATABASE_NAME_INVALID", "", "", "", "production_database_name must be a canonical identifier")
	}
	if p.Owner != "xm_migrator" {
		add("POLICY_OWNER_INVALID", "", p.Owner, "", "owner must be xm_migrator")
	}
	wantCaps := []string{"xm_api_runtime", "xm_worker_runtime", "xm_lifecycle_runtime", "xm_ops_read", "xm_backup_read"}
	if !equalStrings(p.CapabilityRoles, wantCaps) {
		add("POLICY_CAPABILITY_ROLES_INVALID", "", "", "", "capability_roles must contain the five fixed roles")
	}
	known := map[string]bool{"xm_migrator": true}
	for _, c := range wantCaps {
		known[c] = true
		prefix := identityPrefix(c)
		known["xm_"+prefix+"_a"] = true
		known["xm_"+prefix+"_b"] = true
	}
	if p.CapabilityRoles == nil || p.Roles == nil || p.Memberships == nil || p.Objects == nil || p.DefaultACLs == nil || p.RunwayObjects == nil || p.Rotations == nil {
		add("POLICY_NULL_COLLECTION", "", "", "", "required policy collections must be non-null")
	}
	if len(p.Roles) == 0 {
		add("POLICY_ROLES_MISSING", "", "", "", "roles must not be empty")
	}
	for name := range known {
		if _, ok := p.Roles[name]; !ok {
			add("POLICY_ROLE_MISSING", "", name, "", "required role is missing")
		}
	}
	for name, role := range p.Roles {
		if !known[name] || !rolePattern.MatchString(name) {
			add("POLICY_UNKNOWN_ROLE", "", name, "", fmt.Sprintf("unknown role %q", name))
			continue
		}
		if role.Kind == "capability" {
			if role.Login || role.Superuser || role.CreateRole || role.CreateDB || role.Replication || role.BypassRLS {
				add("ROLE_CAPABILITY_PRIVILEGED", name, name, "", "capability role must be NOLOGIN and unprivileged")
			}
		} else if role.Kind == "login" {
			if role.Capability == "" || !known[role.Capability] || role.Capability == "xm_migrator" {
				add("ROLE_LOGIN_CAPABILITY_INVALID", role.Capability, name, "", "login role must point to one capability")
			}
			if role.Superuser || role.CreateRole || role.CreateDB || role.Replication || role.BypassRLS || !role.Inherit {
				add("ROLE_LOGIN_PRIVILEGED", role.Capability, name, "", "login identity has unsafe attributes")
			}
		} else if name == "xm_migrator" {
			if role.Kind != "owner" || !role.Login || role.Superuser || role.CreateRole || role.CreateDB || role.Replication || role.BypassRLS {
				add("ROLE_OWNER_INVALID", "", name, "", "migrator owner attributes invalid")
			}
		} else {
			add("ROLE_KIND_INVALID", "", name, "", "unknown role kind")
		}
	}
	if _, ok := p.Roles[p.Owner]; !ok {
		add("POLICY_OWNER_MISSING", "", p.Owner, "", "owner role missing")
	}
	validateMemberships(p, known, add)
	validateObjects(p, known, add)
	validateDefaults(p, known, add)
	if p.PublicSchemaOwner != "pg_database_owner" {
		add("PUBLIC_SCHEMA_OWNER_INVALID", "", p.PublicSchemaOwner, "public", "public schema owner must be pg_database_owner")
	}
	if p.CustomSchemaDefault != "deny" {
		add("CUSTOM_SCHEMA_DEFAULT_INVALID", "", "", "", "custom schema default must be deny")
	}
	if p.PublicSchemaContract != "river-only" {
		add("PUBLIC_SCHEMA_CONTRACT_INVALID", "", "", "public", "public schema contract must be river-only")
	}
	if len(p.PublicDatabasePrivileges) != 0 || len(p.PublicSchemaPrivileges) != 0 || len(p.PublicTypePrivileges) != 0 || len(p.PublicRoutinePrivileges) != 0 {
		add("PUBLIC_PRIVILEGES_NOT_REVOKED", "PUBLIC", "", "", "PUBLIC database/schema/type/routine privileges must be empty")
	}
	if !commitPattern.MatchString(p.RunwayApprovedMergeSHA) {
		add("RUNWAY_APPROVAL_DIGEST_INVALID", "", "", "", "runway approved merge SHA must be lowercase SHA-256")
	}
	seenRunway := map[string]bool{}
	for _, object := range p.RunwayObjects {
		if seenRunway[object] {
			add("RUNWAY_OBJECT_DUPLICATE", "", "", object, "duplicate runway object")
		}
		seenRunway[object] = true
	}
	for _, object := range []string{"finance.runway_threshold_config", "finance.runway_threshold_history", "finance.runway_threshold_current_verified"} {
		if !seenRunway[object] {
			add("RUNWAY_OBJECT_MISSING", "", "", object, "approved RUNWAY object is missing")
		}
	}
	for object := range seenRunway {
		if object != "finance.runway_threshold_config" && object != "finance.runway_threshold_history" && object != "finance.runway_threshold_current_verified" {
			add("RUNWAY_OBJECT_UNKNOWN", "", "", object, "RUNWAY object is outside the approved set")
		}
	}
	if len(p.Rotations) != len(wantCaps) {
		add("ROTATION_CAPABILITIES_INCOMPLETE", "", "", "", "every capability must have one rotation object")
	}
	for _, capability := range wantCaps {
		r, ok := p.Rotations[capability]
		if !ok {
			add("ROTATION_MISSING", capability, "", "", "rotation object missing")
			continue
		}
		validateRotation(capability, r, now, p, add)
	}
	for capability := range p.Rotations {
		if !contains(wantCaps, capability) {
			add("ROTATION_UNKNOWN_CAPABILITY", capability, "", "", "rotation references unknown capability")
		}
	}
	for _, session := range p.ActiveSessions {
		if !rolePattern.MatchString(session) {
			add("ROTATION_SESSION_IDENTITY_INVALID", "", session, "", "active session identity is not canonical")
		}
	}
	for capability, rotation := range p.Rotations {
		if rotation.State == RotationSteadyB {
			old := "xm_" + identityPrefix(capability) + "_a"
			for _, session := range p.ActiveSessions {
				if session == old {
					add("ROTATION_STALE_SESSION", capability, session, "", "steady-b cannot retain an active old-identity session")
				}
			}
		}
	}
	validateRoleTopology(p, add)
	return sortViolations(violations)
}

func identityPrefix(capability string) string {
	prefix := strings.TrimPrefix(capability, "xm_")
	prefix = strings.TrimSuffix(prefix, "_runtime")
	if capability == "xm_ops_read" {
		return "ops"
	}
	if capability == "xm_backup_read" {
		return "backup"
	}
	return prefix
}

func validateRotation(capability string, r RotationSpec, now time.Time, p Policy, add func(string, string, string, string, string)) {
	a := "xm_" + identityPrefix(capability) + "_a"
	b := "xm_" + identityPrefix(capability) + "_b"
	if r.State != RotationSteadyA && r.State != RotationRotatingAB && r.State != RotationSteadyB {
		add("ROTATION_STATE_INVALID", capability, "", "", "rotation state is unknown")
		return
	}
	parse := func(value, field string, required bool) (time.Time, bool) {
		if value == "" {
			if required {
				add("ROTATION_TIME_REQUIRED", capability, "", field, field+" is required")
			}
			return time.Time{}, false
		}
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || t.Location() != time.UTC || !strings.HasSuffix(value, "Z") || t.Format(time.RFC3339Nano) != value {
			add("ROTATION_TIME_INVALID", capability, "", field, field+" must be UTC RFC3339Nano")
			return time.Time{}, false
		}
		return t, true
	}
	switch r.State {
	case RotationSteadyA:
		if r.SteadyIdentity != a {
			add("ROTATION_STEADY_IDENTITY_INVALID", capability, r.SteadyIdentity, "", "steady-a must use identity A")
		}
		if r.OldIdentity != "" || r.NewIdentity != "" || r.ApprovedChangeRequest != "" || r.StartedAt != "" || r.Deadline != "" || r.ClosureEvidence != "" || r.ClosedAt != "" {
			add("ROTATION_STEADY_FIELDS_INVALID", capability, "", "", "steady-a cannot retain rotation fields")
		}
	case RotationRotatingAB:
		if r.SteadyIdentity != "" {
			add("ROTATION_ROTATING_STEADY_IDENTITY_INVALID", capability, r.SteadyIdentity, "", "rotating state has no steady identity")
		}
		if r.OldIdentity == r.NewIdentity || r.OldIdentity == "" || r.NewIdentity == "" {
			add("ROTATION_IDENTITIES_NOT_DISTINCT", capability, r.OldIdentity, "", "rotation old/new identities must be distinct")
		}
		if r.OldIdentity != a || r.NewIdentity != b {
			add("ROTATION_IDENTITY_WRONG_CAPABILITY", capability, r.NewIdentity, "", "rotation identities must belong to capability")
		}
		if r.ApprovedChangeRequest == "" {
			add("ROTATION_CHANGE_REQUEST_REQUIRED", capability, "", "", "rotating state requires approved change request")
		}
		started, startedOK := parse(r.StartedAt, "started_at", true)
		deadline, deadlineOK := parse(r.Deadline, "deadline", true)
		if startedOK && deadlineOK {
			if !started.Before(deadline) {
				add("ROTATION_TIME_WINDOW_INVALID", capability, "", "", "started_at must precede deadline")
			}
			if !now.IsZero() && !now.Before(deadline) {
				add("ROTATION_DEADLINE_EXPIRED", capability, "", "", "rotating state deadline expired")
			}
		}
		if r.ClosureEvidence != "" || r.ClosedAt != "" {
			add("ROTATION_ROTATING_CLOSURE_FIELDS_INVALID", capability, "", "", "open rotating state cannot have closure fields")
		}
	case RotationSteadyB:
		if r.SteadyIdentity != b {
			add("ROTATION_STEADY_IDENTITY_INVALID", capability, r.SteadyIdentity, "", "steady-b must use identity B")
		}
		if r.OldIdentity != a || r.NewIdentity != b {
			add("ROTATION_IDENTITY_WRONG_CAPABILITY", capability, r.OldIdentity, "", "steady-b must retain the A/B transition identities")
		}
		if r.ApprovedChangeRequest == "" {
			add("ROTATION_CHANGE_REQUEST_REQUIRED", capability, "", "", "steady-b requires the approved change request")
		}
		started, startedOK := parse(r.StartedAt, "started_at", true)
		deadline, deadlineOK := parse(r.Deadline, "deadline", true)
		closed, closedOK := parse(r.ClosedAt, "closed_at", true)
		if r.ClosureEvidence == "" || !shaPrefixPattern.MatchString(r.ClosureEvidence) {
			add("ROTATION_CLOSURE_EVIDENCE_REQUIRED", capability, "", "", "steady-b requires sha256 closure evidence")
		}
		if startedOK && deadlineOK && closedOK {
			if !started.Before(deadline) || !started.Before(closed) || closed.After(deadline) {
				add("ROTATION_TIME_WINDOW_INVALID", capability, "", "", "steady-b closure must be within deadline")
			}
		}
	}
	_ = p // policy role/membership evidence is checked by the catalog verifier (Task 2).
}

func validateMemberships(p Policy, known map[string]bool, add func(string, string, string, string, string)) {
	seen := map[string]Membership{}
	for _, m := range p.Memberships {
		if !known[m.Role] || !known[m.Member] || m.Role == "xm_migrator" {
			add("MEMBERSHIP_UNKNOWN_ROLE", m.Role, m.Member, "", "membership references unknown/owner role")
		}
		if role, ok := p.Roles[m.Role]; ok && role.Kind != "capability" {
			add("MEMBERSHIP_ROLE_NOT_CAPABILITY", m.Role, m.Member, "", "membership target must be a capability role")
		}
		if member, ok := p.Roles[m.Member]; ok {
			if member.Kind != "login" || member.Capability != m.Role {
				add("MEMBERSHIP_CROSS_CAPABILITY", m.Role, m.Member, "", "membership member must be a login identity of the target capability")
			}
		}
		key := m.Role + "\x00" + m.Member
		if _, ok := seen[key]; ok {
			add("MEMBERSHIP_DUPLICATE", m.Role, m.Member, "", "duplicate membership")
		}
		seen[key] = m
		if !m.Inherit || m.Set || m.Admin {
			add("MEMBERSHIP_OPTIONS_INVALID", m.Role, m.Member, "", "runtime membership must be INHERIT true, SET false, ADMIN false")
		}
	}
	for _, capability := range []string{"xm_api_runtime", "xm_worker_runtime", "xm_lifecycle_runtime", "xm_ops_read", "xm_backup_read"} {
		r := p.Rotations[capability]
		a, b := "xm_"+identityPrefix(capability)+"_a", "xm_"+identityPrefix(capability)+"_b"
		want := map[string]bool{}
		switch r.State {
		case RotationSteadyA:
			want[a] = true
		case RotationRotatingAB:
			want[a], want[b] = true, true
		case RotationSteadyB:
			want[b] = true
		}
		for _, identity := range []string{a, b} {
			if want[identity] {
				if m, ok := seen[capability+"\x00"+identity]; !ok || !m.Inherit || m.Set || m.Admin {
					add("MEMBERSHIP_ROTATION_IDENTITY_INVALID", capability, identity, "", "rotation membership missing or unsafe")
				}
			} else if _, ok := seen[capability+"\x00"+identity]; ok {
				add("MEMBERSHIP_OLD_IDENTITY_PRESENT", capability, identity, "", "inactive rotation identity must not retain membership")
			}
		}
	}
}

func validateRoleTopology(p Policy, add func(string, string, string, string, string)) {
	for _, capability := range []string{"xm_api_runtime", "xm_worker_runtime", "xm_lifecycle_runtime", "xm_ops_read", "xm_backup_read"} {
		r := p.Rotations[capability]
		a, b := "xm_"+identityPrefix(capability)+"_a", "xm_"+identityPrefix(capability)+"_b"
		wantA, wantB := r.State == RotationSteadyA || r.State == RotationRotatingAB, r.State == RotationRotatingAB || r.State == RotationSteadyB
		if role, ok := p.Roles[a]; !ok {
			add("ROLE_IDENTITY_MISSING", capability, a, "", "identity A role is missing")
		} else if role.Login != wantA {
			add("ROTATION_LOGIN_STATE_INVALID", capability, a, "", "identity A LOGIN state does not match rotation")
		}
		if role, ok := p.Roles[b]; !ok {
			add("ROLE_IDENTITY_MISSING", capability, b, "", "identity B role is missing")
		} else if role.Login != wantB {
			add("ROTATION_LOGIN_STATE_INVALID", capability, b, "", "identity B LOGIN state does not match rotation")
		}
	}
}

func validateObjects(p Policy, known map[string]bool, add func(string, string, string, string, string)) {
	seen := map[string]bool{}
	validKinds := map[string]bool{"table": true, "sequence": true, "routine": true, "type": true, "domain": true, "schema": true, "database": true, "view": true}
	for _, object := range p.Objects {
		key := object.Kind + "\x00" + object.Schema + "\x00" + object.Name
		if seen[key] {
			add("OBJECT_DUPLICATE", "", "", object.Key(), "duplicate object grant")
		}
		seen[key] = true
		nameOK := namePattern.MatchString(object.Name)
		if object.Kind == "routine" {
			nameOK = routineNamePattern.MatchString(object.Name) && validRoutineName(object.Name)
		}
		schemaOK := object.Schema != "" && namePattern.MatchString(object.Schema)
		if object.Kind == "schema" || object.Kind == "database" {
			schemaOK = object.Schema == ""
		}
		if !validKinds[object.Kind] || !schemaOK || object.Name == "" || !nameOK {
			add("OBJECT_SHAPE_INVALID", "", "", object.Key(), "object kind/schema/name invalid")
		}
		if object.Owner != p.Owner && !(object.Kind == "schema" && object.Name == "public" && object.Owner == "pg_database_owner") {
			add("OBJECT_OWNER_INVALID", "", object.Owner, object.Key(), "object owner must be xm_migrator")
		}
		if object.NoRuntimeAccess && (len(object.Privileges) != 0 || len(object.TablePrivileges) != 0 || len(object.ColumnPrivileges) != 0) {
			add("OBJECT_NO_RUNTIME_GRANT", "", "", object.Key(), "no-runtime object must not grant runtime privileges")
		}
		if object.Kind == "table" || object.Kind == "view" {
			expectedColumns := tableColumns(object.Kind, object.Schema, object.Name)
			if len(expectedColumns) == 0 || !sameStringSetExact(object.Columns, expectedColumns) {
				add("OBJECT_COLUMNS_DRIFT", "", "", object.Key(), "relation columns must match the approved inventory exactly")
			}
		} else if len(object.Columns) != 0 {
			add("OBJECT_COLUMNS_NOT_APPLICABLE", "", "", object.Key(), "non-relation object cannot declare columns")
		}
		for role, privileges := range object.Privileges {
			validateGrantRole(role, known, add, object.Key())
			validatePrivileges(object.Kind, privileges, add, object.Key())
			if role == "PUBLIC" && len(privileges) > 0 {
				add("PUBLIC_OBJECT_PRIVILEGE", "PUBLIC", "", object.Key(), "PUBLIC object privileges must be explicitly revoked")
			}
			if isLoginRole(role, p) {
				add("GRANT_DIRECT_LOGIN_ROLE", role, role, object.Key(), "object grants must target capability roles, not login identities")
			}
		}
		for role, privileges := range object.TablePrivileges {
			validateGrantRole(role, known, add, object.Key())
			validatePrivileges(object.Kind, privileges, add, object.Key())
			if role == "PUBLIC" && len(privileges) > 0 {
				add("PUBLIC_OBJECT_PRIVILEGE", "PUBLIC", "", object.Key(), "PUBLIC object privileges must be explicitly revoked")
			}
			if isLoginRole(role, p) {
				add("GRANT_DIRECT_LOGIN_ROLE", role, role, object.Key(), "object grants must target capability roles, not login identities")
			}
		}
		for role, columns := range object.ColumnPrivileges {
			validateGrantRole(role, known, add, object.Key())
			if object.Kind != "table" {
				add("COLUMN_GRANT_KIND_INVALID", role, "", object.Key(), "column privileges require table object")
			}
			if isLoginRole(role, p) {
				add("GRANT_DIRECT_LOGIN_ROLE", role, role, object.Key(), "column grants must target capability roles, not login identities")
			}
			for column, privileges := range columns {
				if column == "" || strings.ContainsAny(column, "*%") {
					add("COLUMN_NAME_INVALID", role, column, object.Key(), "column name must be exact")
				}
				if !approvedColumn(object.Key(), column) {
					add("COLUMN_UNKNOWN", role, column, object.Key(), "column is outside the approved inventory")
				}
				if object.Key() == "audit.chain_root" && (column != "exported_at" && column != "export_target") && contains(privileges, "UPDATE") {
					add("COLUMN_PROTECTED_GRANT", role, column, object.Key(), "chain_root UPDATE is limited to exported_at/export_target")
				}
				validatePrivileges("table", privileges, add, object.Key())
				if role == "PUBLIC" && len(privileges) > 0 {
					add("PUBLIC_OBJECT_PRIVILEGE", "PUBLIC", "", object.Key(), "PUBLIC column privileges must be explicitly revoked")
				}
			}
		}
	}
	expected := map[string]bool{}
	expectedNoRuntime := map[string]bool{}
	for _, object := range defaultObjects() {
		key := object.Kind + "\x00" + object.Schema + "\x00" + object.Name
		expected[key] = true
		expectedNoRuntime[key] = object.NoRuntimeAccess
	}
	for key := range seen {
		if !expected[key] {
			add("OBJECT_UNKNOWN", "", "", key, "object is outside the approved v1 inventory")
		}
	}
	for key := range expected {
		if !seen[key] {
			add("OBJECT_MISSING", "", "", key, "approved v1 object is missing")
		}
	}
	for _, object := range p.Objects {
		key := object.Kind + "\x00" + object.Schema + "\x00" + object.Name
		if expectedNoRuntime[key] && !object.NoRuntimeAccess {
			add("OBJECT_NO_RUNTIME_MARKER_MISSING", "", "", object.Key(), "approved no-runtime object must carry explicit marker")
		}
	}
}

func approvedColumn(object, column string) bool {
	parts := strings.SplitN(object, ".", 2)
	if len(parts) != 2 {
		return false
	}
	for _, kind := range []string{"table", "view"} {
		if columns := tableColumns(kind, parts[0], parts[1]); len(columns) > 0 {
			return contains(columns, column)
		}
	}
	return false
}

func isLoginRole(role string, p Policy) bool {
	if role == "PUBLIC" {
		return false
	}
	if spec, ok := p.Roles[role]; ok {
		return spec.Kind == "login"
	}
	return false
}

func validRoutineName(value string) bool {
	open := strings.IndexByte(value, '(')
	if open <= 0 || !strings.HasSuffix(value, ")") || !namePattern.MatchString(value[:open]) {
		return false
	}
	for _, r := range value[open+1 : len(value)-1] {
		if !(r == ' ' || r == ',' || r == '.' || r == '_' || r == '(' || r == ')' || r == '[' || r == ']' || r == ':' || r == '"' || r == '\'' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func validateGrantRole(role string, known map[string]bool, add func(string, string, string, string, string), object string) {
	if role != "PUBLIC" && !known[role] {
		add("GRANT_UNKNOWN_ROLE", role, role, object, "grant references unknown role")
	}
}
func validatePrivileges(kind string, privileges []string, add func(string, string, string, string, string), object string) {
	allowedByKind := map[string]map[string]bool{
		"table":    {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true, "MAINTAIN": true},
		"view":     {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true},
		"sequence": {"USAGE": true, "SELECT": true, "UPDATE": true},
		"routine":  {"EXECUTE": true},
		"type":     {"USAGE": true}, "domain": {"USAGE": true},
		"schema":   {"USAGE": true, "CREATE": true},
		"database": {"CONNECT": true, "TEMPORARY": true, "CREATE": true},
	}
	allowed := allowedByKind[kind]
	seen := map[string]bool{}
	for _, privilege := range privileges {
		if seen[privilege] {
			add("GRANT_PRIVILEGE_DUPLICATE", "", privilege, object, "duplicate privilege is not canonical")
		}
		seen[privilege] = true
		if privilege == "ALL" || strings.ContainsAny(privilege, "*%") || !allowed[privilege] {
			add("GRANT_PRIVILEGE_INVALID", "", privilege, object, "privileges must be explicit and known")
		}
	}
}
func validateDefaults(p Policy, known map[string]bool, add func(string, string, string, string, string)) {
	seen := map[string]bool{}
	allowedSchemas := map[string]bool{"core": true, "action": true, "audit": true, "ops": true, "alerts": true, "finance": true, "ui": true, "public": true}
	allowedKinds := map[string]bool{"table": true, "sequence": true, "routine": true, "type": true, "domain": true}
	for _, d := range p.DefaultACLs {
		key := d.Owner + "\x00" + d.Schema + "\x00" + d.ObjectKind
		if seen[key] {
			add("DEFAULT_ACL_DUPLICATE", "", "", d.Schema+"."+d.ObjectKind, "duplicate default ACL")
		}
		seen[key] = true
		if d.Owner != p.Owner || !allowedSchemas[d.Schema] || !allowedKinds[d.ObjectKind] {
			add("DEFAULT_ACL_SHAPE_INVALID", "", d.Owner, d.Schema, "default ACL owner/schema invalid")
		}
		for role, priv := range d.Privileges {
			validateGrantRole(role, known, add, d.Schema+"."+d.ObjectKind)
			validatePrivileges(d.ObjectKind, priv, add, d.Schema+"."+d.ObjectKind)
			if role != "PUBLIC" && isLoginRole(role, p) {
				add("DEFAULT_ACL_DIRECT_LOGIN_ROLE", role, role, d.Schema+"."+d.ObjectKind, "default ACL must target capability/PUBLIC, not login identity")
			}
		}
	}
	for _, schema := range []string{"core", "action", "audit", "ops", "alerts", "finance", "ui", "public"} {
		for _, kind := range []string{"table", "sequence", "routine", "type", "domain"} {
			if !seen[p.Owner+"\x00"+schema+"\x00"+kind] {
				add("DEFAULT_ACL_MISSING", "", "", schema+"."+kind, "default ACL family is missing")
			}
		}
	}
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStringSetExact(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, value := range a {
		if value == "" || seen[value] {
			return false
		}
		seen[value] = true
	}
	for _, value := range b {
		if !seen[value] {
			return false
		}
	}
	return true
}

func normalizePolicy(p *Policy) {
	// Capability order is part of the v1 contract (it keeps generated policy
	// bytes stable); unlike grants it is not an order-insensitive collection.
	sort.Slice(p.Memberships, func(i, j int) bool {
		if p.Memberships[i].Role != p.Memberships[j].Role {
			return p.Memberships[i].Role < p.Memberships[j].Role
		}
		return p.Memberships[i].Member < p.Memberships[j].Member
	})
	for i := range p.Objects {
		p.Objects[i].Columns = sortedUnique(p.Objects[i].Columns)
		p.Objects[i].Privileges = normalizeGrantMap(p.Objects[i].Privileges)
		p.Objects[i].TablePrivileges = normalizeGrantMap(p.Objects[i].TablePrivileges)
		p.Objects[i].ColumnPrivileges = normalizeColumnMap(p.Objects[i].ColumnPrivileges)
	}
	sort.Slice(p.Objects, func(i, j int) bool {
		return p.Objects[i].Kind+"\x00"+p.Objects[i].Schema+"\x00"+p.Objects[i].Name < p.Objects[j].Kind+"\x00"+p.Objects[j].Schema+"\x00"+p.Objects[j].Name
	})
	sort.Slice(p.DefaultACLs, func(i, j int) bool {
		a, b := p.DefaultACLs[i], p.DefaultACLs[j]
		return a.Schema+"\x00"+a.ObjectKind < b.Schema+"\x00"+b.ObjectKind
	})
	sort.Strings(p.RunwayObjects)
}
func normalizeGrantMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for role, vals := range in {
		out[role] = sortedUnique(vals)
	}
	return out
}
func normalizeColumnMap(in map[string]map[string][]string) map[string]map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string]map[string][]string, len(in))
	for role, cols := range in {
		out[role] = make(map[string][]string, len(cols))
		for col, vals := range cols {
			out[role][col] = sortedUnique(vals)
		}
	}
	return out
}

func validateRotationFieldPresence(data []byte) error {
	var envelope map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&envelope); err != nil {
		return fmt.Errorf("dbroles policy rotations: %w", err)
	}
	var rotations map[string]json.RawMessage
	if raw, ok := envelope["rotations"]; !ok {
		return errors.New("dbroles policy: rotations missing")
	} else if err := json.Unmarshal(raw, &rotations); err != nil {
		return fmt.Errorf("dbroles policy rotations: %w", err)
	}
	required := []string{"state", "steady_identity", "old_identity", "new_identity", "approved_change_request", "started_at", "deadline", "closure_evidence", "closed_at"}
	for capability, raw := range rotations {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("dbroles policy rotation %s: %w", capability, err)
		}
		for _, field := range required {
			if _, ok := obj[field]; !ok {
				return fmt.Errorf("dbroles policy rotation %s: missing %s", capability, field)
			}
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSON(dec); err != nil {
		return fmt.Errorf("dbroles policy JSON: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("dbroles policy JSON: trailing values")
		}
		return fmt.Errorf("dbroles policy JSON trailing: %w", err)
	}
	return nil
}
func walkJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("object key is not string")
				}
				if seen[name] {
					return fmt.Errorf("duplicate JSON field %q", name)
				}
				seen[name] = true
				if err := walkJSON(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("unterminated JSON object")
			}
		case '[':
			for dec.More() {
				if err := walkJSON(dec); err != nil {
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
func topLevelFieldPresent(data []byte, wanted string) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok {
			return false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return false
		}
		if name == wanted {
			return true
		}
	}
	return false
}
func canonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }
