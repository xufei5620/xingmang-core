# SavedView Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox ([ ]) syntax for tracking.

**Goal:** Persist per-human DataTableV2 named views through the platform Query/Action stack without crossing Principal or Environment boundaries or falling back to browser storage.

**Architecture:** XM-B003b adds ui.saved_view storage, a savedviews domain/Store, a Principal-scoped Query, and HUMAN-only L0 set/remove Actions under ui.saved_view.manage. After B003b is merged, XM-B003c keeps ui-admin transport-agnostic while admin-web owns API calls, TanStack Query, Router Search Params, stable table keys, and page integration.

**Tech Stack:** Go 1.27, chi, pgx v5, sqlc, PostgreSQL 18, React 19, TypeScript 5.9, React Router 7, TanStack Query 5, Vitest, Testing Library, Storybook 10, pnpm 11.

**Spec:** docs/superpowers/specs/2026-08-28-saved-view-persistence-design.md

## Global Constraints

- This plan is not implementation authorization. Do not begin XM-B003b until the product owner approves every item in the spec's Hard approval gate.
- XM-B003b and XM-B003c use separate worktrees, branches, commits, and PRs; Codex does not merge either PR.
- All reads use Query. All durable writes use the existing Action HTTP endpoint and Action Kernel.
- ui.saved_view.set@1 and ui.saved_view.remove@1 are L0, permission ui.saved_view.manage, HUMAN-only, and explicitly allowed in development/staging/production.
- Owner issuer/subject/identity zone and Environment come only from Principal and never from client parameters.
- State v1 persists query, filters, sort, known/visible columns, and density. It excludes page, cursor, selection, preview, expanded rows, route tab/sub-tab, Environment, row data, and request state.
- No localStorage, sessionStorage, IndexedDB, cookie, or hidden session-persistence fallback is permitted.
- Query/filter/name/column content must not enter logs or append-only audit; before/after state
  summaries contain only canonical state SHA-256.
- Explicit valid Router Search Params take precedence over selected SavedView state.
- Built-in 全部 remains the initial fallback. v1 does not add an automatic custom default.
- Versions remain locked by VERSIONS.lock; do not add or upgrade dependencies.
- Format changed Go packages with go fmt, not a bare gofmt command.
- The migration filename is current maximum + 1 at XM-B003b start. PR #103 uses 000014; 000015 is expected only if it remains the maximum.

---

## Approval checkpoint before implementation

- [ ] **Step 1: Record explicit product approval**

Require an XM-B003a Issue/PR comment containing:

~~~text
APPROVED:
- ui.saved_view relation and max+1 migration
- SavedView state contract v1
- ui.saved_view.manage scope
- default staff + admin grant
- oidcauth ui. drift prefix
- ui.saved_view.set@1
- ui.saved_view.remove@1
- first enabled table keys
- separate B003b/B003c PRs
~~~

Expected: every line is approved without an unresolved qualification. If any line is missing, do
not create a worktree, migration, contract, or scope change.

---

# Part I — XM-B003b Backend Persistence

## Task 1: Create the backend worktree and allocate the migration

**Files:**

- Inspect: db/migrations/
- Inspect: docs/superpowers/specs/2026-08-28-saved-view-persistence-design.md
- Expected create when 000014 remains maximum:
  - db/migrations/000015_ui_saved_views.up.sql
  - db/migrations/000015_ui_saved_views.down.sql

**Interfaces:**

- Consumes: approved XM-B003a design and updated origin/release/v0.1-launch.
- Produces: ai/codex/XM-B003b-saved-view-backend with a verified unique migration number.

- [ ] **Step 1: Confirm PR #103 is merged**

Run:

~~~powershell
gh pr view 103 --repo xufei5620/xingmang-platform --json state,mergedAt,mergeCommit
~~~

Expected: state MERGED and a mergeCommit. If it remains open, stop because XM-B003b depends on its
000014 allocation.

- [ ] **Step 2: Create the isolated worktree**

Run:

~~~powershell
git fetch origin
git worktree add K:/星芒统一控制平台/wt-xmB003b -b ai/codex/XM-B003b-saved-view-backend origin/release/v0.1-launch
~~~

Expected: clean worktree on the named branch.

- [ ] **Step 3: Re-read the approved spec**

Run:

~~~powershell
Get-Content -Raw docs/superpowers/specs/2026-08-28-saved-view-persistence-design.md
~~~

- [ ] **Step 4: Allocate the migration number**

Run:

~~~powershell
Get-ChildItem db/migrations -File | Where-Object Name -Match '^[0-9]{6}_.*\.up\.sql$' | Sort-Object Name | Select-Object -Last 3 -ExpandProperty Name
~~~

Expected if PR #103 is still latest: 000014_finance_upstream_metadata.up.sql is last, so use
000015_ui_saved_views.{up,down}.sql. If another migration is later, stop and amend both approved
documents with the actual maximum + 1 before creating SQL.

## Task 2: Add relation and sqlc contract

**Files:**

- Create: db/migrations/000015_ui_saved_views.up.sql
- Create: db/migrations/000015_ui_saved_views.down.sql
- Create: db/queries/savedviews.sql
- Modify: sqlc.yaml
- Generate: internal/platform/savedviews/gen/
- Review generated model diffs in action, alerts, audit, finance, ops, and registry packages.
- Test: internal/platform/savedviews/store_integration_test.go

**Interfaces:**

- Consumes: core.environment and PostgreSQL.
- Produces: ui.saved_view plus typed List/Get/Count/Upsert/Delete queries.

- [ ] **Step 1: Write the failing round-trip test**

~~~go
func TestStoreRoundTripsSavedViewStateV1(t *testing.T) {
    owner := Owner{
        Issuer: "https://auth.example/realms/staff",
        Subject: "staff-sub-1",
        IdentityZone: "staff",
        Environment: "staging",
    }
    in := SavedView{
        ID: uuid.New(),
        TableKey: "platform.sub2api.channels",
        Name: "需关注",
        State: StateV1{
            SchemaVersion: 1,
            Query: "openai",
            Filters: map[string]string{"status": "需关注"},
            Sort: &Sort{ColumnID: "grossProfit", Direction: SortDesc},
            KnownColumns: []string{"name", "status", "grossProfit"},
            VisibleColumns: []string{"name", "status"},
            Density: DensityCompact,
        },
    }
    result, err := store.Set(ctx, owner, in)
    if err != nil {
        t.Fatal(err)
    }
    if result.BeforeHash != nil {
        t.Fatalf("create before hash = %q, want nil", *result.BeforeHash)
    }
    if diff := cmp.Diff(in.State, result.After.State); diff != "" {
        t.Fatal(diff)
    }
}
~~~

- [ ] **Step 2: Run it to verify failure**

~~~powershell
go test ./internal/platform/savedviews -run TestStoreRoundTripsSavedViewStateV1 -count=1
~~~

Expected: FAIL because the package/schema does not exist.

- [ ] **Step 3: Write migration**

Create schema ui and ui.saved_view with every column/constraint in spec section 11. Add:

~~~sql
CREATE UNIQUE INDEX saved_view_owner_table_name_key
    ON ui.saved_view (
        owner_issuer, owner_subject, identity_zone, environment, table_key, name
    );

CREATE INDEX saved_view_owner_table_updated_idx
    ON ui.saved_view (
        owner_issuer, owner_subject, identity_zone, environment, table_key, updated_at DESC
    );
~~~

The down file drops ui.saved_view and ui for local reset. Production recovery remains forward
repair or backup restore.

- [ ] **Step 4: Add sqlc queries**

db/queries/savedviews.sql contains owner-scoped List, owner+Environment and owner+table counts,
GetSavedViewForUpdate, Upsert, and DeleteSavedViewOwned.

GetSavedViewForUpdate runs inside Store.Set's transaction:

~~~sql
-- name: GetSavedViewForUpdate :one
SELECT state_hash
FROM ui.saved_view
WHERE owner_issuer = $1
  AND owner_subject = $2
  AND identity_zone = $3
  AND environment = $4
  AND table_key = $5
  AND name = $6
FOR UPDATE;
~~~

Upsert uses this conflict key:

~~~sql
ON CONFLICT (
    owner_issuer, owner_subject, identity_zone, environment, table_key, name
) DO UPDATE SET
    state_version = EXCLUDED.state_version,
    query = EXCLUDED.query,
    filters = EXCLUDED.filters,
    sort_column = EXCLUDED.sort_column,
    sort_direction = EXCLUDED.sort_direction,
    known_columns = EXCLUDED.known_columns,
    visible_columns = EXCLUDED.visible_columns,
    density = EXCLUDED.density,
    state_hash = EXCLUDED.state_hash,
    updated_at = now()
RETURNING *;
~~~

Delete atomically returns mutation evidence:

~~~sql
-- name: DeleteSavedViewOwned :one
DELETE FROM ui.saved_view
WHERE id = $1
  AND owner_issuer = $2
  AND owner_subject = $3
  AND identity_zone = $4
  AND environment = $5
RETURNING id, state_hash;
~~~

- [ ] **Step 5: Add sqlc block and generate**

Use db/queries/savedviews.sql, package gen, output internal/platform/savedviews/gen, pgx/v5,
pointer nullable types, empty slices, and the repository UUID override.

~~~powershell
go tool sqlc generate
git status --short
~~~

Expected: savedviews/gen appears; all other generated changes are mechanical and reviewed.

- [ ] **Step 6: Verify migration up/down/re-up**

~~~powershell
go run github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1 -path db/migrations -database $env:XM_SCRATCH_DATABASE_URL up
go run github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1 -path db/migrations -database $env:XM_SCRATCH_DATABASE_URL down 1
go run github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1 -path db/migrations -database $env:XM_SCRATCH_DATABASE_URL up 1
~~~

Expected: all exit 0; remove the scratch database.

- [ ] **Step 7: Commit schema slice**

Stage only migration/query/sqlc/generated files and commit:

~~~powershell
git commit -m "feat(savedviews): add personal view storage"
~~~

## Task 3: Add validated domain state and owner derivation

**Files:**

- Create: internal/platform/savedviews/model.go
- Create: internal/platform/savedviews/validation.go
- Create: internal/platform/savedviews/owner.go
- Create: internal/platform/savedviews/hash.go
- Create: internal/platform/savedviews/permissions.go
- Test: model_test.go, owner_test.go, hash_test.go in the same package.

**Interfaces:**

- Produces Owner, StateV1, SavedView, NormalizeName, ValidateState, OwnerFromPrincipal, and
  CanonicalStateHash. The bounded JSON helpers are DecodeFiltersJSON(raw []byte) and
  DecodeStateV1JSON(raw []byte).

- [ ] **Step 1: Write failing tests**

~~~go
func TestOwnerFromPrincipalUsesImmutableOIDCSubject(t *testing.T)
func TestOwnerFromPrincipalAllowsDevIDFallbackOutsideProduction(t *testing.T)
func TestOwnerFromPrincipalRejectsMissingOIDCSubject(t *testing.T)
func TestOwnerFromPrincipalRejectsMachineTypes(t *testing.T)
func TestStateV1ValidationLimits(t *testing.T)
func TestVisibleColumnsMustBeSubsetOfKnown(t *testing.T)
func TestStateV1RejectsDuplicateColumns(t *testing.T)
func TestNormalizeNameRejectsReservedNames(t *testing.T)
func TestCanonicalHashIgnoresFilterMapInsertionOrder(t *testing.T)
~~~

- [ ] **Step 2: Define exact types**

~~~go
const ScopeManage = "ui.saved_view.manage"

type Owner struct {
    Issuer string
    Subject string
    IdentityZone string
    Environment string
}

type Sort struct {
    ColumnID string
    Direction SortDirection
}

type StateV1 struct {
    SchemaVersion int
    Query string
    Filters map[string]string
    Sort *Sort
    KnownColumns []string
    VisibleColumns []string
    Density Density
}

type SavedView struct {
    ID uuid.UUID
    TableKey string
    Name string
    State StateV1
    StateHash string
    CreatedAt time.Time
    UpdatedAt time.Time
}
~~~

SortDirection is asc/desc. Density is compact/standard/comfortable.

- [ ] **Step 3: Implement owner, validation, and hash**

Use Subject for OIDC ownership. Permit ID fallback only for issuer dev://header-resolver outside
production. Enforce every limit in spec section 12. Both raw JSON helpers validate UTF-8 and byte
length before constructing a decoder, decode one object, and require a second Decode to return
io.EOF. Canonicalize filter keys before SHA-256 and return lowercase hex.

- [ ] **Step 4: Run and commit**

~~~powershell
go test ./internal/platform/savedviews -run 'Test(Owner|State|Visible|Normalize|Canonical)' -count=1
git add internal/platform/savedviews
git commit -m "feat(savedviews): define validated personal view state"
~~~

Expected: focused tests pass.

## Task 4: Implement Store isolation and quota

**Files:**

- Create: internal/platform/savedviews/store.go
- Modify: internal/platform/savedviews/store_integration_test.go
- Test: internal/platform/savedviews/store_test.go

**Interfaces:**

- NewStore(*pgxpool.Pool) *Store
- List(context.Context, Owner, string) ([]SavedView, error)
- Set(context.Context, Owner, SavedView) (SetResult, error)
- Remove(context.Context, Owner, uuid.UUID) (RemoveResult, error)

~~~go
type SetResult struct {
    BeforeHash *string
    After SavedView
}

type RemoveResult struct {
    ID uuid.UUID
    BeforeHash string
}
~~~

- [ ] **Step 1: Write failing isolation/quota tests**

~~~go
func TestStoreListIsolatesSubject(t *testing.T)
func TestStoreListIsolatesIssuer(t *testing.T)
func TestStoreListIsolatesEnvironment(t *testing.T)
func TestStoreListIsolatesTableKey(t *testing.T)
func TestStoreRemoveDoesNotRevealForeignOwner(t *testing.T)
func TestStoreEnforcesTwentyViewsPerTable(t *testing.T)
func TestStoreAllowsOverwriteAtTableLimit(t *testing.T)
func TestStoreEnforcesTwoHundredViewsPerEnvironment(t *testing.T)
func TestStoreConcurrentCreatesCannotBypassQuota(t *testing.T)
func TestConcurrentSetsReturnSerializedHashChain(t *testing.T)
func TestConcurrentSetAndRemoveReturnTransactionConsistentHashes(t *testing.T)
~~~

- [ ] **Step 2: Implement List and row conversion**

Decode JSONB filters only into map[string]string. Malformed stored data returns a fixed internal
error without raw JSON.

- [ ] **Step 3: Implement transactional Set**

Validate input, compute hash, begin one transaction, acquire a server-derived owner/Environment
advisory transaction lock, select the existing owner/table/name row FOR UPDATE, retain its
state_hash as BeforeHash, enforce 20/200 only for create, upsert with RETURNING, commit, and return
SetResult{BeforeHash, After}. No caller performs a pre-read.

- [ ] **Step 4: Implement owner-scoped Remove**

Begin one transaction, acquire the same server-derived owner/Environment advisory lock used by
Set, execute owner/Environment-scoped DELETE RETURNING id, state_hash, commit, and return
RemoveResult. Foreign and nonexistent UUIDs return the same ErrNotFound. No caller performs a
pre-read.

- [ ] **Step 5: Run and commit**

~~~powershell
go test ./internal/platform/savedviews -count=1
git add internal/platform/savedviews db/queries/savedviews.sql internal/platform/savedviews/gen
git commit -m "feat(savedviews): enforce owner and environment isolation"
~~~

Expected: all Store tests pass, including concurrent quota and set/set plus set/remove hash-chain
tests.

## Task 5: Add L0 set/remove Actions and redacted audit

**Files:**

- Create: internal/platform/savedviews/actions.go
- Create: internal/platform/savedviews/actions_test.go
- Create: internal/platform/savedviews/actions_integration_test.go
- Create: contracts/actions/ui.saved_view.set.v1.json
- Create: contracts/actions/ui.saved_view.remove.v1.json

**Interfaces:**

- Produces ActionSet, ActionRemove, and RegisterActions(*action.Registry, *Store) error.

- [ ] **Step 1: Write failing definition, parameter, and audit tests**

Assert both definitions are L0, ScopeManage, HUMAN-only, and explicitly allow all three
Environments. Test bounded filters_json, trailing JSON rejection, sort-pair validation, unknown
owner/Environment params, and all domain limits. Use action.CaptureAudit to prove each non-empty
before/after summary has exactly one key named state_hash.

Add exact decoder tests:

~~~go
func TestDecodeFiltersRejectsMoreThan4096RawBytesBeforeDecode(t *testing.T)
func TestDecodeFiltersRejectsOverCapWhitespace(t *testing.T)
func TestDecodeFiltersRejectsInvalidUTF8(t *testing.T)
func TestDecodeFiltersRejectsEscapedValueExpansion(t *testing.T)
func TestDecodeFiltersRejectsTrailingJSONObject(t *testing.T)
func TestDecodeStateV1RejectsMoreThan16384RawBytesBeforeDecode(t *testing.T)
func TestCanonicalStateRejectsMoreThan16384BytesBeforeHash(t *testing.T)
~~~

Instrument the decoder in the over-cap tests and assert Decode was never called. For accepted raw
length, decode exactly one object and require a second Decode to return io.EOF.

- [ ] **Step 2: Implement set schema and handler**

Exact primitive params:

~~~text
table_key:string required
name:string required
schema_version:int required
query:string
filters_json:string required
sort_column:string
sort_direction:string enum("", "asc", "desc")
known_columns:string_slice required
visible_columns:string_slice required
density:string enum(compact, standard, comfortable) required
~~~

Reject invalid UTF-8 and len([]byte(filters_json)) > 4096 before constructing json.Decoder. Decode
exactly one map[string]string and require the second Decode to return io.EOF. Validate decoded
entry/key/expanded-value limits, build StateV1, enforce canonical JSON <= 16384 bytes before hash,
and call Store.Set.

The Handler must not query the Store before Set. It uses SetResult.BeforeHash and
SetResult.After.StateHash to build summaries containing only state_hash, with the returned UUID as
resource ID.

- [ ] **Step 3: Implement remove schema and handler**

The only param is saved_view_id:string required. Derive Owner, parse UUID, call Store.Remove
directly, and build the before summary from RemoveResult.BeforeHash. The Handler is forbidden from
loading the row before Remove and always omits after summary.

- [ ] **Step 4: Write Action JSON contracts**

Set declares compensation_mode MANUAL; remove declares NOT_POSSIBLE. Both declare L0,
ui.saved_view.manage, HUMAN, all Environments, and approval.required false.

- [ ] **Step 5: Run and commit**

~~~powershell
go test ./internal/platform/savedviews -run 'Test(Set|Remove)' -count=1
git add internal/platform/savedviews/actions.go internal/platform/savedviews/actions_test.go internal/platform/savedviews/actions_integration_test.go contracts/actions/ui.saved_view.set.v1.json contracts/actions/ui.saved_view.remove.v1.json
git commit -m "feat(savedviews): add governed L0 actions"
~~~

Expected: definitions, strict byte-before-decode JSON handling, owner derivation, atomic
Store-result audit, and redaction tests pass.

## Task 6: Add owner-scoped Query and API wiring

**Files:**

- Create: internal/platform/httpapi/savedviews.go
- Create: internal/platform/httpapi/savedviews_test.go
- Create: internal/platform/httpapi/savedviews_security_test.go
- Modify: internal/platform/httpapi/router.go
- Modify: internal/platform/httpapi/router_test.go
- Modify: cmd/platform-api/main.go

**Interfaces:**

- Produces GET /api/v1/ui/saved-views?table_key=... under
  RequireScope(savedviews.ScopeManage).

- [ ] **Step 1: Write failing HTTP tests**

Add tests for the exact response DTO, missing scope 403, machine Principal rejection, required
table_key, server-derived Owner/Environment, and absence of owner, Environment, and state_hash in
the response.

Add malicious-marker coverage through the real ExecuteActionHandler + Kernel + captured logger:

~~~go
func TestSavedViewInvalidParamsDoNotLeakMarkersToHTTPLogOrAudit(t *testing.T)
func TestSavedViewInternalErrorDoesNotLeakMarkersToHTTPLogOrAudit(t *testing.T)
~~~

Use distinct markers in name, query, filter key/value, known columns, and visible columns. Capture
the WriteError body, a bytes.Buffer-backed slog JSON handler, and audit sink events. Assert none of
the markers occurs in any serialization.

For invalid params, assert the public body has exactly error.code=INVALID_PARAMS,
error.message=SavedView 参数无效, and error.request_id, with no extra error fields. For a fixed Store
failure, assert code INTERNAL and the repository's fixed internal-error message. The captured
WriteError log must retain exactly its existing module/request_id/path/method/status/error_code/err
field names; err must be the stable Action Error() string. Do not add params, state, query,
filters, name, columns, or any wrapped cause text.

- [ ] **Step 2: Define explicit DTOs**

~~~go
type savedViewStateResponse struct {
    SchemaVersion int
    Query string
    Filters map[string]string
    Sort *savedViewSortResponse
    Columns savedViewColumnsResponse
    Density string
}

type savedViewItemResponse struct {
    ID string
    TableKey string
    Name string
    StateVersion int
    State savedViewStateResponse
    CreatedAt string
    UpdatedAt string
}
~~~

Add explicit JSON tags in implementation. Do not encode the domain row directly.

- [ ] **Step 3: Implement handler**

Read Principal from context, call OwnerFromPrincipal, validate required table_key, list through the
interface, and return a non-null items array.

- [ ] **Step 4: Add route and process wiring**

Add SavedViews to httpapi.Deps and register:

~~~go
api.With(RequireScope(savedviews.ScopeManage)).
    Get("/ui/saved-views", ListSavedViewsHandler(d.SavedViews))
~~~

platform-api constructs one savedviews.Store, registers its Actions before Kernel creation, and
passes the same Store to the Query dependency. Registration failure rejects startup.

- [ ] **Step 5: Run and commit**

~~~powershell
go test ./internal/platform/httpapi ./cmd/platform-api -count=1
git add internal/platform/httpapi/savedviews.go internal/platform/httpapi/savedviews_test.go internal/platform/httpapi/savedviews_security_test.go internal/platform/httpapi/router.go internal/platform/httpapi/router_test.go cmd/platform-api/main.go
git commit -m "feat(httpapi): expose principal-scoped saved views"
~~~

Expected: Query scope, HUMAN enforcement, DTO, route, fixed error envelope, malicious-marker
redaction, logger-field, and wiring tests pass.

## Task 7: Add scope mapping and authorization docs

**Files:**

- Modify: internal/platform/oidcauth/rolemap.go
- Modify: internal/platform/oidcauth/rolemap_test.go
- Modify: internal/platform/oidcauth/resolver_test.go
- Modify: docs/modules/httpapi/PERMISSIONS.md
- Modify: docs/modules/httpapi/AUTH-SWITCH.md
- Modify: deploy/compose/.env.example

**Interfaces:**

- Produces ui. drift recognition and staff/admin default ui.saved_view.manage.

- [ ] **Step 1: Write failing role-map tests**

~~~go
if !slices.Contains(DefaultRoleScopeMap()["staff"], "ui.saved_view.manage") {
    t.Fatal("staff requires its self-only personal-view scope")
}
if !slices.Contains(DefaultRoleScopeMap()["admin"], "ui.saved_view.manage") {
    t.Fatal("admin requires the baseline personal-view scope")
}
if !looksLikePlatformScope("ui.saved_view.manage") {
    t.Fatal("ui. must be rejected as Keycloak fine-grained role drift")
}
~~~

- [ ] **Step 2: Update mapping**

Add ui. to platformScopePrefixes and ui.saved_view.manage to staff/admin. Do not add a Keycloak
Realm Role.

- [ ] **Step 3: Update documentation**

Document why self-only Query and write share one scope, why unrelated scopes are not reused, why
fine-grained ui.* roles in Keycloak are drift, and how explicit XM_OIDC_ROLE_SCOPES values must be
updated during deployment approval.

- [ ] **Step 4: Run and commit**

~~~powershell
go test ./internal/platform/oidcauth -count=1
git add internal/platform/oidcauth/rolemap.go internal/platform/oidcauth/rolemap_test.go internal/platform/oidcauth/resolver_test.go docs/modules/httpapi/PERMISSIONS.md docs/modules/httpapi/AUTH-SWITCH.md deploy/compose/.env.example
git commit -m "feat(authz): grant personal saved-view scope"
~~~

Expected: role translation and drift tests pass.

## Task 8: Verify and deliver XM-B003b

**Files:** Review every branch file.

**Interfaces:** Produces one backend PR and complete Handoff; no merge.

- [ ] **Step 1: Format and verify generated code**

~~~powershell
go fmt ./internal/platform/savedviews ./internal/platform/httpapi ./internal/platform/oidcauth ./cmd/platform-api
git status --short
~~~

Expected: formatting exits 0. If it changes a task file, inspect and commit that formatting before
continuing.

Run sqlc again only after formatting changes are committed:

~~~powershell
go tool sqlc generate
git diff --exit-code
~~~

Expected: sqlc is idempotent and the worktree remains clean.

- [ ] **Step 2: Run the full backend and frontend gates**

~~~powershell
go vet ./...
go test -p 1 -count=1 ./...
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
bash scripts/check-governance.sh
git diff --check origin/release/v0.1-launch...HEAD
~~~

Expected: Go vet/tests, full workspace frontend typecheck/tests, Storybook, governance, and diff
check all exit 0. A backend-only slice does not waive the project-wide frontend gates.

- [ ] **Step 3: Run secret and audit-redaction checks**

Run the CI-equivalent secret scan. If gitleaks is locally available:

~~~powershell
gitleaks git --redact --no-banner
~~~

Inspect RecordBefore, RecordAfter, and logger calls in savedviews Actions. Expected: no raw
query/filter/name/column state and no new allowlist.

- [ ] **Step 4: Verify branch scope**

~~~powershell
git diff --name-only origin/release/v0.1-launch...HEAD
git status --short
~~~

Expected: only SavedView backend, migration, generated, contracts, auth-scope, and permissions
documentation changes; status clean.

- [ ] **Step 5: Push and create PR**

~~~powershell
git push -u origin ai/codex/XM-B003b-saved-view-backend
gh pr create --base release/v0.1-launch --title "feat(savedviews): persist personal table views (XM-B003b)"
~~~

Handoff includes approval evidence, migration allocation, exact tests, audit redaction, risks, and
follow-ups. Do not merge.

---

# Part II — XM-B003c DataTableV2 Integration

## Task 9: Create the frontend worktree after B003b merges

**Files:** Inspect merged backend DTO/contracts and current DataTableV2.

**Interfaces:** Produces ai/codex/XM-B003c-saved-view-ui.

- [ ] **Step 1: Confirm B003b merged with green CI**

Record its PR number, four required CI checks, and merge commit.

- [ ] **Step 2: Create a fresh worktree**

~~~powershell
git fetch origin
git worktree add K:/星芒统一控制平台/wt-xmB003c -b ai/codex/XM-B003c-saved-view-ui origin/release/v0.1-launch
~~~

- [ ] **Step 3: Read merged contracts**

~~~powershell
Get-Content -Raw internal/platform/httpapi/savedviews.go
Get-Content -Raw contracts/actions/ui.saved_view.set.v1.json
Get-Content -Raw contracts/actions/ui.saved_view.remove.v1.json
~~~

Copy names and types from merged code, not memory.

## Task 10: Add pure state v1 reconciliation and URL codec

**Files:**

- Modify: web/packages/ui-admin/src/dataTable.ts
- Modify: web/packages/ui-admin/src/dataTable.test.ts
- Modify: web/packages/ui-admin/src/index.ts
- Create: web/apps/admin-web/src/lib/savedViewSearchParams.ts
- Create: web/apps/admin-web/src/lib/savedViewSearchParams.test.ts

**Interfaces:**

- Produces SavedViewStateV1, PersistedSavedView, reconcileSavedViewState,
  serializeSavedViewSearchParams, and parseSavedViewSearchParams.

- [ ] **Step 1: Write failing reconciliation tests**

Test:

- a deliberately hidden known column remains hidden;
- a newly added non-defaultHidden column appears;
- a newly added defaultHidden column remains hidden;
- every primary column is forced visible;
- removed columns and invalid sort/filter references are dropped;
- an unsupported version is not guessed;
- page, cursor, selection, preview, expanded rows, and request state are absent.
- a saved grossProfit sort remains pending while schemaReady=false and applies after the static
  capability schema becomes ready;
- loading/missing margin row values do not remove the grossProfit sortable capability.

- [ ] **Step 2: Define wire types**

~~~typescript
export interface SavedViewStateV1 {
  schema_version: 1;
  query: string;
  filters: Readonly<Record<string, string>>;
  sort: { column_id: string; direction: "asc" | "desc" } | null;
  columns: {
    known: readonly string[];
    visible: readonly string[];
  };
  density: Density;
}

export interface PersistedSavedView {
  id: string;
  table_key: string;
  name: string;
  state_version: 1;
  state: SavedViewStateV1;
  created_at: string;
  updated_at: string;
}
~~~

- [ ] **Step 3: Implement reconciliation**

Return:

~~~typescript
export interface ReconciledSavedView {
  state: TableViewState;
  warnings: readonly string[];
}

export interface TableColumnCapability {
  id: string;
  sortable: boolean;
  primary: boolean;
  defaultHidden: boolean;
}
~~~

Never mutate persisted input. Apply every compatibility rule from spec section 10. Reconciliation
requires a complete static capability list; when schemaReady=false, return a deferred result and
do not null a saved sort.

- [ ] **Step 4: Write failing URL tests**

Use exact params:

~~~text
dt_q
dt_f.<filter-id>
dt_sort
dt_known
dt_visible
dt_density
dt_view
~~~

Assert invalid values are dropped, URL values override a selected view field-by-field, and an
unavailable private dt_view still restores materialized criteria as 自定义.

- [ ] **Step 5: Implement URL codec**

Keep it in admin-web because React Router is application context, not a ui-admin concern. IDs
exclude commas, so known/visible arrays can be comma-separated after normal URL encoding.

- [ ] **Step 6: Run and commit**

~~~powershell
pnpm --config.verify-deps-before-run=false --filter @xingmang/ui-admin test -- dataTable.test.ts
pnpm --config.verify-deps-before-run=false --filter admin-web test -- savedViewSearchParams.test.ts
git add web/packages/ui-admin/src/dataTable.ts web/packages/ui-admin/src/dataTable.test.ts web/packages/ui-admin/src/index.ts web/apps/admin-web/src/lib/savedViewSearchParams.ts web/apps/admin-web/src/lib/savedViewSearchParams.test.ts
git commit -m "feat(ui-admin): reconcile persistent saved views"
~~~

Expected: focused tests pass.

## Task 11: Add SavedView API client and hook

**Files:**

- Create: web/apps/admin-web/src/api/savedViews.ts
- Create: web/apps/admin-web/src/api/savedViews.test.ts
- Create: web/apps/admin-web/src/hooks/useSavedViews.ts
- Create: web/apps/admin-web/src/hooks/useSavedViews.test.tsx
- Modify: web/apps/admin-web/src/api/config.ts
- Modify: web/apps/admin-web/src/router.test.tsx

**Interfaces:**

- Produces SAVED_VIEW_MANAGE_PERMISSION, listSavedViews, setSavedView, removeSavedView, and
  useSavedViews.

- [ ] **Step 1: Write failing API tests**

Assert percent-encoded table_key, exact Action IDs/version, flattened primitive set params,
filters_json serialization, no owner/Environment fields, and action_run_id propagation.

- [ ] **Step 2: Implement API module**

~~~typescript
export const SAVED_VIEW_MANAGE_PERMISSION = "ui.saved_view.manage";

export interface SavedViewListResponse {
  items: PersistedSavedView[];
}
~~~

Use apiClient.get for Query and executeAction for set/remove.

- [ ] **Step 3: Write failing hook tests**

~~~typescript
it("uses a table-key-specific query cache")
it("invalidates only that table after set or remove")
it("does not insert a durable item before Action success")
it("keeps built-ins available when Query fails")
~~~

- [ ] **Step 4: Implement hook**

Query key is ["ui", "savedViews", tableKey]. Return items, pending/error/denied state, set/remove
mutations, last Action run ID, and retry.

- [ ] **Step 5: Add development scope**

Add ui.saved_view.manage to DEFAULT_SCOPES and update router.test.tsx's exact X-Dev-Scopes string.
Do not change production auth behavior.

- [ ] **Step 6: Run and commit**

~~~powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- savedViews.test.ts useSavedViews.test.tsx router.test.tsx
git add web/apps/admin-web/src/api/savedViews.ts web/apps/admin-web/src/api/savedViews.test.ts web/apps/admin-web/src/hooks/useSavedViews.ts web/apps/admin-web/src/hooks/useSavedViews.test.tsx web/apps/admin-web/src/api/config.ts web/apps/admin-web/src/router.test.tsx
git commit -m "feat(admin-web): connect personal saved views"
~~~

Expected: API/hook/config tests pass.

## Task 12: Make DataTableV2 persistence-aware

**Files:**

- Modify: web/packages/ui-admin/src/DataTableV2.tsx
- Modify: web/packages/ui-admin/src/DataTableV2.test.tsx
- Modify: web/packages/ui-admin/src/DataTableV2.stories.tsx
- Modify: web/packages/ui-admin/src/dataTable.ts
- Modify: web/packages/ui-admin/src/index.ts

**Interfaces:**

- Consumes remote items/callbacks without owning transport.
- Produces accessible save/remove/apply UI plus optional controlled state.

- [ ] **Step 1: Write failing component tests**

Test separate built-in/personal groups, success only after Promise resolution, run ID display,
failure honesty, no browser storage, atomic transient clearing, 自定义 rules, built-in name
collision, and removal only for persistent custom views.

- [ ] **Step 2: Add exact props**

~~~typescript
export interface DataTableViewPersistence {
  tableKey: string;
  items: readonly PersistedSavedView[];
  schemaReady: boolean;
  columnCapabilities: readonly TableColumnCapability[];
  status: "loading" | "ready" | "denied" | "error";
  message?: string;
  onRetry?: () => void;
  onSave: (
    name: string,
    state: SavedViewStateV1
  ) => Promise<{ runId: string }>;
  onRemove: (id: string) => Promise<{ runId: string }>;
}

viewState?: TableViewState;
onViewStateChange?: (
  state: TableViewState,
  reason: TableViewChangeReason
) => void;
~~~

Keep current uncontrolled behavior for tables not opted into persistence.

- [ ] **Step 3: Implement persistent controls**

For persistence-enabled tables, await callbacks, never add sessionViews, announce run ID only on
success, and keep current presentation on failure. Denied/error does not remove built-ins. Do not
reconcile remote state until schemaReady=true.

- [ ] **Step 4: Implement atomic apply**

One state transition applies reconciled query/filter/sort/columns/density and clears page,
selection, preview, expanded rows, and open productivity panels. Avoid intermediate renders with
stale selection. Channel/upstream column capabilities are defined independently of async margin
row values, so grossProfit remains sortable during loading with null cell sort values.

- [ ] **Step 5: Add Storybook states**

Cover ready, loading, denied, Query error, save error, unsupported version, save success, and
remove success using deterministic local callbacks only.

- [ ] **Step 6: Run and commit**

~~~powershell
pnpm --config.verify-deps-before-run=false --filter @xingmang/ui-admin test
pnpm --config.verify-deps-before-run=false --filter @xingmang/ui-admin typecheck
git add web/packages/ui-admin
git commit -m "feat(ui-admin): add persistent SavedView controls"
~~~

Expected: ui-admin tests and typecheck pass.

## Task 13: Wire first pages and stable keys

**Files:**

- Create: web/apps/admin-web/src/components/PersistentDataTable.tsx
- Create: web/apps/admin-web/src/components/PersistentDataTable.test.tsx
- Modify/test: web/apps/admin-web/src/pages/AuditPage.tsx
- Modify/test: web/apps/admin-web/src/components/ChannelTable.tsx
- Modify/test: web/apps/admin-web/src/components/UpstreamAccountsPanel.tsx

**Interfaces:**

- Consumes hook, URL codec, and DataTable persistence props.
- Produces the five approved stable table keys.

- [ ] **Step 1: Write failing adapter tests**

Assert exact table key, URL overlay, dt_* materialization, unavailable dt_view fallback, and
underlying-table survival on Query failure.

- [ ] **Step 2: Implement PersistentDataTable**

Compose useSearchParams, useSavedViews, URL codec, and DataTableV2. Keep transport and router
concerns out of ui-admin.

- [ ] **Step 3: Wire exact keys**

~~~text
global.audit.events
platform.sub2api.channels
platform.newapi.channels
platform.sub2api.upstreams
platform.newapi.upstreams
~~~

Derive platform keys from an exhaustive map, not arbitrary route string interpolation.

- [ ] **Step 4: Add built-in 全部**

Derive columns from the static capability schema rather than copying IDs. Add page-owned presets.
Keep users and requests excluded and document their external server-state blocker at those call
sites.

Keep global.alerts excluded from XM-B003c. AlertsPage currently owns active/all in component
useState, and that value changes the server Query result. A separate prerequisite slice must first
move it to an explicit Router Search Param such as scope=active|all, make that context restorable,
and add its tests. Only then may global.alerts be added to SavedView.

- [ ] **Step 5: Run and commit**

~~~powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- PersistentDataTable.test.tsx AuditPage.test.tsx ChannelTable.test.tsx UpstreamAccountsPanel.test.tsx
git add web/apps/admin-web/src
git commit -m "feat(admin-web): persist views on first data tables"
~~~

Expected: adapter and first-page tests pass.

## Task 14: Verify and deliver XM-B003c

**Files:** Review every branch file.

**Interfaces:** Produces one frontend PR with Handoff and screenshots; no merge.

- [ ] **Step 1: Run complete gates**

~~~powershell
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
go vet ./...
go test -p 1 -count=1 ./...
bash scripts/check-governance.sh
git diff --check origin/release/v0.1-launch...HEAD
~~~

Expected: full workspace frontend typecheck/tests, Storybook, Go vet/tests, governance, and diff
check all exit 0. A frontend-only slice does not waive the project-wide Go gates. Run the
CI-equivalent secret scan with no allowlist additions.

- [ ] **Step 2: Browser-verify durability and transient exclusion**

With a B003b-backed local stack, configure all five persisted state categories, save, record
action_run_id, route away/back, hard refresh, reapply, and verify page 1 with no selection,
preview, or expanded row. Confirm browser storage contains no SavedView entry.

- [ ] **Step 3: Browser-verify isolation, URL precedence, and failures**

Use two dev Principals and two local Environments; verify no cross-boundary views. Copy a
materialized URL to another Principal and verify criteria restore as 自定义. Force Query 500 and
Action 500; built-ins remain and no false saved result appears.

- [ ] **Step 4: Browser-verify layout**

Capture 1440, 1024, 800, and 390 pixel widths. Panels remain viewport-contained and horizontal
overflow stays in the table container.

- [ ] **Step 5: Verify scope and create PR**

~~~powershell
git diff --name-only origin/release/v0.1-launch...HEAD
git status --short
git push -u origin ai/codex/XM-B003c-saved-view-ui
gh pr create --base release/v0.1-launch --title "feat(ui): persist personal table views (XM-B003c)"
~~~

Handoff includes exact tests/counts, screenshots, failure/no-storage/URL/isolation evidence, and
alerts/users/requests exclusion rationale. Do not merge.

---

## Plan self-review checklist

- [ ] Every spec requirement maps to a Task above.
- [ ] No implementation step bypasses Query, Action, Principal, scope, Environment, or audit.
- [ ] No client input carries owner or Environment.
- [ ] State v1 names match across Go, HTTP, TypeScript, and tests.
- [ ] known/visible compatibility is tested before DataTable integration.
- [ ] users and requests remain excluded until their external server state is controlled.
- [ ] global.alerts remains excluded until active/all is URL-owned and restorable.
- [ ] The migration number is revalidated rather than assumed.
- [ ] Full backend/frontend/governance/secret/browser gates precede success claims.
