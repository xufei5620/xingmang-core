# XM-B003 SavedView Persistence Design

> Status: APPROVAL REQUIRED — design only; implementation is not authorized by this document.
>
> Task: XM-B003a
>
> Base inspected: release/v0.1-launch at 543087fab9f8f1dee3a3b3412681e1b49a1ca7f5
>
> Follow-on slices: XM-B003b (backend persistence) and XM-B003c (DataTableV2 integration)

## 1. Decision summary

SavedView persistence will be a server-side, per-human preference capability in the Xingmang
control plane. It will not use localStorage, sessionStorage, cookies, or a second direct-write
endpoint.

The selected design has these properties:

1. Reads use a Principal-scoped Query.
2. Writes use two L0 Actions: ui.saved_view.set@1 and ui.saved_view.remove@1.
3. A single new scope, ui.saved_view.manage, gates both reading and writing a caller's own
   views.
4. Ownership and Environment come only from the authenticated Principal. Neither may appear in
   client-controlled parameters.
5. State contract v1 contains query, filters, sort, known/visible columns, and density.
6. Page number, cursor, selection, bulk preview, expanded rows, route tabs, and Environment are
   transient/context state and are never persisted.
7. Built-in views remain code-owned. Persistent views are user-owned overlays.
8. Explicit URL criteria take precedence over a saved view. Applying a view materializes its
   shareable criteria into Router Search Params.
9. A failed Query or Action never falls back to browser storage and never displays a false
   durable-success message.

This is the smallest design that delivers persistence across refresh, route changes, browser
sessions, and devices while preserving the platform's Query/Action, identity, Environment, and
audit boundaries.

## 2. Authority and current-state evidence

The design is derived from:

- PROJECT-CONSTITUTION.md clauses 2, 6, 11, 15, 17, and 19;
- docs/adr/ADR-003-Action唯一写入口.md, which explicitly classifies saving a personal view
  or low-impact preference as L0 and still requires permission plus basic audit;
- docs/adr/ADR-005-身份分域.md and docs/adr/ADR-016-保留Keycloak双Realm.md;
- docs/architecture/ADMIN-IA.md, especially the Router Search Params and DataTableV2 rules;
- the final prototype design-system/xingmang-control/MASTER.md SavedView contract;
- web/packages/ui-admin/src/DataTableV2.tsx and dataTable.ts.

Current implementation facts:

- DataTableV2 stores named views in component useState only.
- The UI truthfully says that a saved session view is not synchronized or written to browser
  storage.
- TableViewState currently contains query, filters, sort, visibleColumns, and density.
- There is no SavedView/user-preference table, Store, Query, API, or Action in this repository.
- Six source call sites use DataTableV2, but only ChannelTable currently supplies presets and
  therefore exposes the session-view controls.

## 3. Goals

XM-B003 must:

- keep a human staff member's named table views after refresh and re-login;
- synchronize those views across that staff member's browsers/devices in the same Environment;
- prevent any read, overwrite, or delete across Principal or Environment boundaries;
- preserve current DataTableV2 view semantics, including query;
- reconcile saved column choices safely when columns are added or removed;
- keep built-in views and table functionality usable when persistence is unavailable;
- make every durable write traceable through Action run and audit evidence;
- keep copied URLs useful even when the recipient cannot access the sender's private SavedView;
- provide an explicit delete path so persistent preferences do not accumulate forever.

## 4. Non-goals

XM-B003 does not:

- add shared/team/global views;
- add view sharing, ownership transfer, cloning, or ACLs;
- add a user-selectable automatic default view;
- persist current page, server cursor, selection, bulk preview, or expanded rows;
- persist platform tab/sub-tab or Environment;
- add cross-Environment preference synchronization;
- introduce localStorage as a cache or offline write queue;
- extend Action Schema with an object/map field type;
- enable SavedView on a table whose meaningful filters/sort are still owned outside
  DataTableV2 unless those external controls are first included in the controlled state;
- change upstream systems or write any upstream database.

## 5. Options considered

### 5.1 Browser localStorage

Rejected. It is browser/device-specific, can leak between staff using the same browser profile,
cannot be server-authorized or server-audited, and cannot deliver cross-device persistence.

### 5.2 Application-level session store

Rejected as the final solution. It can survive route unmounts but still disappears on refresh and
does not satisfy the roadmap item.

### 5.3 Server-side personal SavedView

Selected. The platform already has authenticated Principal, Environment, Action, Query, and audit
boundaries. A low-risk personal preference fits those boundaries without involving an upstream
system or Foundation-B approval controls.

## 6. Architecture and data flow

### 6.1 Read path

~~~text
DataTable page
  -> GET /api/v1/ui/saved-views?table_key=<stable-key>
  -> RequirePrincipal
  -> RequireScope(ui.saved_view.manage)
  -> require HUMAN Principal
  -> derive owner + Environment from Principal
  -> Store.List(owner, tableKey)
  -> return only that owner's rows in that Environment
~~~

No owner, issuer, subject, identity zone, or Environment parameter is accepted.

### 6.2 Write path

~~~text
DataTable save/remove control
  -> POST /api/v1/actions/<action>/versions/1/execute
  -> Action Kernel
  -> Principal type / scope / Environment / schema validation
  -> savedviews Handler derives owner from Principal
  -> Store transaction
  -> Action result with action_run_id
  -> audit event with resource UUID and state hash only
  -> refetch Query
~~~

The UI may optimistically keep the current unsaved table presentation while the request is in
flight, but it may not add a durable view to the selector or announce success until the Action
returns successfully.

## 7. Ownership and Environment isolation

### 7.1 Stable owner identity

Production ownership uses:

~~~text
owner_issuer  = Principal.Issuer
owner_subject = Principal.Subject
identity_zone = Principal.IdentityZone
environment   = Principal.Environment
~~~

Principal.Subject is the immutable Keycloak sub. Principal.ID is a human-readable username and
must not be the production ownership key because username changes would orphan or transfer
preferences.

The development header resolver has no Subject. Only when all of the following hold may the
implementation use Principal.ID as owner_subject:

- Principal.Issuer is exactly dev://header-resolver;
- Principal.Environment is not production;
- Principal.ID is non-empty.

Any other HUMAN Principal without Subject is rejected. SERVICE, AI, and SERVER_AGENT are rejected
for both Query and Actions.

### 7.2 Isolation key

The complete isolation key is:

~~~text
(owner_issuer, owner_subject, identity_zone, environment, table_key)
~~~

The same staff subject gets independent views in development, staging, and production. There is
no cross-Environment fallback or copying.

### 7.3 Client boundary

The following fields never appear in Query parameters or Action params:

- owner_issuer;
- owner_subject;
- identity_zone;
- environment.

Adding any of them later is a security-sensitive contract change and requires a new design
approval.

## 8. Stable table keys

Table keys are lowercase product identifiers, not captions, route strings, React component names,
or translated labels. They match:

~~~text
^[a-z0-9][a-z0-9._-]{0,127}$
~~~

First enabled keys:

| Key | Page/table | Why eligible |
|---|---|---|
| global.alerts | 告警与故障 / 告警表 | Search/filter/sort/columns/density are table-owned |
| global.audit.events | 审计记录 / 事件表 | Search/filter/sort/columns/density are table-owned |
| platform.sub2api.channels | Sub2API / 渠道管理 | Current presets and local table state already exist |
| platform.newapi.channels | NewAPI / 渠道管理 | Same component, separate platform ownership |
| platform.sub2api.upstreams | Sub2API / 上游管理 | Local search/filter/sort/columns/density |
| platform.newapi.upstreams | NewAPI / 上游管理 | Same data component, separate page/table identity |

Reserved future keys:

| Key | Enable condition |
|---|---|
| server.assets | Real server-assets DataTable exists |
| global.actions.pending-approvals | Foundation-B pending-approvals table exists |

Not enabled in XM-B003c:

- platform.<p>.users: server-side period and sort controls live outside DataTableV2;
- platform.<p>.requests: server-side filters and opaque cursor live outside DataTableV2.

Enabling either now would save only half of the state while presenting it as a complete view.
They require a later controlled-state adapter that includes their server-side criteria.

## 9. State contract v1

### 9.1 Wire shape

~~~json
{
  "schema_version": 1,
  "query": "",
  "filters": {
    "status": "需关注"
  },
  "sort": {
    "column_id": "grossProfit",
    "direction": "desc"
  },
  "columns": {
    "known": ["name", "status", "grossProfit"],
    "visible": ["name", "status", "grossProfit"]
  },
  "density": "compact"
}
~~~

sort may be null. filters is a string-to-string object. density is compact, standard, or
comfortable.

### 9.2 Persisted fields

| Field | Meaning |
|---|---|
| query | Normalized table-local search query |
| filters | Active table-local column filters |
| sort | One optional sortable column and asc/desc direction |
| columns.known | All column IDs known when the view was saved |
| columns.visible | The subset visible when the view was saved |
| density | compact / standard / comfortable |

### 9.3 Explicitly excluded fields

The snapshot never contains:

- client page or total pages;
- server cursor/cursor stack;
- selected row IDs or select-all state;
- bulk preview action/evidence;
- expanded row IDs;
- panel-open/focus state;
- route tab/sub-tab;
- Environment;
- last fetch time, loading/error state, or row data.

Applying a view always:

1. applies reconciled query/filter/sort/columns/density;
2. returns client pagination to page 1;
3. clears selection and bulk preview;
4. closes expanded rows and productivity panels;
5. renders once after state reconciliation.

## 10. Column and contract compatibility

The persisted known and visible sets distinguish a newly added column from a column the user
deliberately hid.

On load:

1. unknown persisted column IDs are discarded;
2. a current column present in known honors its saved visible/hidden state;
3. a current column absent from known is new and uses the current defaultHidden setting;
4. every primary column is forced visible;
5. an empty resulting visible set is invalid and falls back to current default columns;
6. a sort referencing a missing or non-sortable column becomes null;
7. unknown filter IDs or values no longer present in that filter's option set are dropped;
8. an invalid density becomes the current table default.

If schema_version is not 1, the record remains listable/removable but is not applied. The UI shows
an inline warning and uses the built-in 全部 state. No migration of an unknown future version is
guessed client-side.

## 11. Database contract

The planned relation is ui.saved_view:

~~~text
id                uuid PRIMARY KEY
owner_issuer      text NOT NULL
owner_subject     text NOT NULL
identity_zone     text NOT NULL
environment       text NOT NULL REFERENCES core.environment(id) ON DELETE RESTRICT
table_key         text NOT NULL
name              text NOT NULL
state_version     smallint NOT NULL
query             text NOT NULL
filters           jsonb NOT NULL
sort_column       text NULL
sort_direction    text NULL
known_columns     text[] NOT NULL
visible_columns   text[] NOT NULL
density           text NOT NULL
state_hash        text NOT NULL
created_at        timestamptz NOT NULL DEFAULT now()
updated_at        timestamptz NOT NULL DEFAULT now()
~~~

Unique key:

~~~text
(owner_issuer, owner_subject, identity_zone, environment, table_key, name)
~~~

Database constraints must cover:

- table_key format and maximum 128 ASCII characters;
- name non-empty and at most 24 Unicode code points;
- state_version = 1;
- filters is a JSON object;
- sort column/direction are both null or both present, with direction asc/desc;
- known/visible arrays are non-null and visible is contained by known;
- density is compact/standard/comfortable;
- state_hash is 64 lowercase hexadecimal characters.

Domain validation remains the first line; database constraints are defense in depth.

### 11.1 Migration number gate

The implementation migration is always allocated as current maximum migration number + 1.

PR #103 currently occupies 000014_finance_upstream_metadata. If #103 is merged before XM-B003b,
the expected filenames are:

~~~text
db/migrations/000015_ui_saved_views.up.sql
db/migrations/000015_ui_saved_views.down.sql
~~~

This number is not pre-authorized as a fixed value. At XM-B003b start, the implementer must list
db/migrations in the newly created worktree. If the maximum is not 000014, stop, choose the actual
maximum + 1, and update this spec/plan before creating migration files.

## 12. Validation, limits, and quota

Exact v1 limits:

| Item | Limit |
|---|---|
| Views per owner/Environment/table | 20 |
| Views per owner/Environment total | 200 |
| Name | 1–24 Unicode code points after whitespace normalization |
| Table key | 1–128 lowercase ASCII characters |
| Query | 256 Unicode code points |
| Filters | 16 entries |
| Filter key | 1–64 ASCII identifier characters |
| Filter value | 256 Unicode code points |
| Known columns | 1–64 unique IDs |
| Visible columns | 1–64 unique IDs, subset of known |
| Column ID | 1–128 ASCII identifier characters |
| Serialized state | 16 KiB after canonical JSON encoding |

Name normalization trims, collapses internal whitespace, and preserves case. 自定义 is reserved
and rejected. 全部 remains the built-in fallback and is also rejected as a persistent name.

Quota checks happen only for creation; overwriting the same normalized name remains allowed at
the limit. Store creation must serialize quota checks per owner/Environment so concurrent
requests cannot bypass the limit.

## 13. Query contract

Endpoint:

~~~http
GET /api/v1/ui/saved-views?table_key=platform.sub2api.channels
~~~

Permission: ui.saved_view.manage

Response:

~~~json
{
  "items": [
    {
      "id": "3c6d7c6f-5eec-4db4-8a23-55754aa50ceb",
      "table_key": "platform.sub2api.channels",
      "name": "需关注",
      "state_version": 1,
      "state": {
        "schema_version": 1,
        "query": "",
        "filters": {"status": "需关注"},
        "sort": null,
        "columns": {
          "known": ["name", "status"],
          "visible": ["name", "status"]
        },
        "density": "compact"
      },
      "created_at": "2026-08-28T08:00:00Z",
      "updated_at": "2026-08-28T08:00:00Z"
    }
  ]
}
~~~

table_key is required. Invalid/missing keys return INVALID_PARAMS. The endpoint inherits
RequirePrincipal, NoStore, request timeout, access logging, and rate limiting from /api/v1.

## 14. Action contracts

### 14.1 ui.saved_view.set@1

- risk_level: L0
- permission: ui.saved_view.manage
- principal_types: HUMAN
- environments: development, staging, production
- approval.required: false
- compensation_mode: MANUAL

Action params use only the existing Action Schema primitive types:

~~~json
{
  "table_key": "platform.sub2api.channels",
  "name": "需关注",
  "schema_version": 1,
  "query": "",
  "filters_json": "{\"status\":\"需关注\"}",
  "sort_column": "",
  "sort_direction": "",
  "known_columns": ["name", "status"],
  "visible_columns": ["name", "status"],
  "density": "compact"
}
~~~

filters_json is parsed with a bounded decoder into map[string]string, rejects trailing JSON and
unknown shapes, and is then canonicalized server-side. Empty sort_column and sort_direction mean
no sort and must appear together.

The Action upserts by the complete owner/Environment/table/name unique key and returns the SavedView
item plus action_run_id from the generic Action envelope.

### 14.2 ui.saved_view.remove@1

- risk_level: L0
- permission: ui.saved_view.manage
- principal_types: HUMAN
- environments: development, staging, production
- approval.required: false
- compensation_mode: NOT_POSSIBLE

Params:

~~~json
{
  "saved_view_id": "3c6d7c6f-5eec-4db4-8a23-55754aa50ceb"
}
~~~

Removal is owner- and Environment-scoped. A UUID owned by another Principal or Environment must
not be distinguishable from a nonexistent UUID.

### 14.3 Why one scope

Both Query and Actions use ui.saved_view.manage. A separate read scope would not reduce exposure:
the Query can only return the caller's own low-impact preferences. Two scopes would add role-map
and deployment configuration without adding a meaningful isolation boundary.

Reusing registry.read, ops.read, or another domain scope is forbidden because it would couple
unrelated authorization decisions.

## 15. Scope and role mapping

ui.saved_view.manage is a new platform fine-grained scope and requires explicit product-owner
approval before implementation.

On approval:

1. add ui. to oidcauth.platformScopePrefixes so a Keycloak role shaped like a fine-grained UI
   scope is ignored and logged as drift;
2. add ui.saved_view.manage to the default staff and admin role translations;
3. add it to the admin-web development DEFAULT_SCOPES;
4. update permissions/auth-switch documentation;
5. update any explicit XM_OIDC_ROLE_SCOPES deployment value during deployment review.

No Keycloak Realm Role is added. Keycloak continues to provide coarse staff identity; the platform
maps it to fine-grained scopes.

## 16. Audit and sensitive-data boundary

Saved query/filter values can contain usernames, email fragments, request IDs, or text pasted by
mistake. They may be stored only in ui.saved_view and returned only to the owner.

The Action audit contribution contains only the canonical state hash as its state summary:

~~~json
{
  "resource_type": "ui.saved_view",
  "resource_id": "<saved-view-uuid>",
  "before_summary": {
    "state_hash": "<sha256>"
  },
  "after_summary": {
    "state_hash": "<sha256>"
  }
}
~~~

For remove, after_summary is absent. Logs and audit must not contain:

- query;
- filter keys or values;
- view name;
- column lists;
- raw state JSON.

The Action envelope already records Action ID/version, Principal, Environment, run ID, and the
SavedView resource UUID. The SHA-256 input is canonical JSON of state v1. The hash proves which
snapshot changed without duplicating potentially sensitive state into the append-only audit chain.

## 17. Built-in views and default behavior

- Built-in presets remain compile-time/page-owned data.
- Persistent custom views are returned separately and grouped separately in the selector.
- The initial table state remains built-in 全部.
- v1 has no “设为默认” switch and never auto-applies a custom view on page entry.
- Saving the same normalized custom name updates that custom record.
- Persistent names 自定义 and 全部 are rejected.
- If a custom name collides with another built-in label, the UI must reject it before calling the
  Action and explain that the name is reserved on that table.

This avoids a hidden persistent filter being mistaken for the unfiltered default.

## 18. Router Search Params and precedence

ADMIN-IA requires filters and views to be shareable/recoverable. A private SavedView UUID cannot
be the only URL state because another staff member cannot read the owner's record.

Precedence:

1. explicit valid Router Search Params;
2. a user-selected persistent or built-in view;
3. built-in 全部.

When a view is applied, the page adapter materializes the state using:

~~~text
dt_q=<query>
dt_f.<filter-id>=<value>
dt_sort=<column-id>:<asc|desc>
dt_known=<comma-separated-column-ids>
dt_visible=<comma-separated-column-ids>
dt_density=<compact|standard|comfortable>
dt_view=<saved-view-uuid-or-built-in-name>
~~~

dt_view is a label/reference only. The materialized criteria are authoritative, so a copied URL
still restores the view when the recipient cannot resolve dt_view; in that case the UI labels it
自定义.

Platform tab/sub parameters remain separate and are never included in SavedView state.

## 19. Failure and degraded behavior

| Failure | Required UI behavior |
|---|---|
| Query loading | Built-ins/table remain usable; persistent group shows loading |
| Query 403 | Hide write controls, show “缺少 ui.saved_view.manage” inline |
| Query network/server error | Keep built-ins; show persistence unavailable and retry |
| Set/remove Action error | Keep current presentation; no saved/success claim |
| Unsupported state version | Keep record removable; do not apply; fall back to 全部 |
| Invalid saved filter/column | Reconcile valid parts, warn inline |
| View removed on another device | Refetch; fall back to materialized URL/custom state |

No failure writes localStorage, sessionStorage, cookies, IndexedDB, or a hidden in-memory
“persistent” record.

## 20. Frontend component boundary

ui-admin remains transport-agnostic. DataTableV2 receives:

- stable tableKey;
- built-in views;
- remote persistent view items and load state;
- save/remove callbacks supplied by admin-web;
- optional controlled TableViewState and onViewStateChange for URL ownership.

admin-web owns:

- Query and Action API calls;
- TanStack Query caching/invalidation;
- permission/error presentation;
- Router Search Param serialization;
- page-specific stable table keys.

DataTableV2 owns:

- reconciliation against current columns/filter specs;
- active-view matching;
- page/selection/preview/expanded clearing;
- accessible save/remove UI and inline status.

## 21. Delivery slices

### XM-B003b — backend persistence

One backend PR after this design is approved:

- migration and sqlc;
- savedviews domain/store;
- Query;
- L0 set/remove Actions and JSON contracts;
- new scope and role-map changes;
- permission/auth documentation;
- unit, HTTP, integration, migration, audit-redaction, and isolation tests.

### XM-B003c — DataTableV2 persistence integration

One frontend PR after XM-B003b is merged:

- API client and TanStack Query hook;
- DataTableV2 controlled/persistent view adapter;
- state reconciliation and Router Search Params;
- save/remove UI;
- first enabled tables from section 8;
- component/page/API/browser tests.

Neither slice is merged by Codex. Each uses an independent branch/worktree and waits for human
review.

## 22. Acceptance criteria

### Backend

- Query returns only the authenticated HUMAN subject's views in the current Environment.
- Set/remove cannot target another subject or Environment.
- Both Actions are L0, HUMAN-only, and require ui.saved_view.manage.
- Every success/failure has an Action run; successful mutations emit only the state hash in
  before/after audit summaries.
- No query/filter/name/column content appears in audit or logs.
- Quotas and all exact limits are enforced under concurrent creation.
- Migration up/down/re-up and sqlc regeneration are verified.

### Frontend

- Refresh and a second browser session reload the same current-Environment custom views.
- Sub2API/NewAPI tables never share view rows.
- Applying a view restores v1 state and clears all excluded transient state.
- New/removed columns reconcile according to section 10.
- Explicit URL criteria win and copied URLs work without access to the owner's view.
- Persistence failure never claims success and never writes browser storage.
- Built-in 全部 remains usable in every degraded state.

### Project gates

- pnpm -r run typecheck;
- pnpm -r run test;
- pnpm --filter ui-storybook run build;
- go fmt on changed Go packages;
- go vet ./...;
- go test -p 1 ./...;
- bash scripts/check-governance.sh;
- secret scan;
- browser verification for refresh, route restore, failure, and 1440/1024/mobile layouts.

## 23. Hard approval gate

Implementation must not start until the product owner explicitly approves all of:

1. a new ui.saved_view relation and a migration allocated as current maximum + 1;
2. state contract v1, including query and known/visible column compatibility;
3. the new ui.saved_view.manage scope;
4. default staff and admin authorization for that scope;
5. adding ui. to the OIDC fine-grained-scope drift guard;
6. ui.saved_view.set@1 and ui.saved_view.remove@1 Action contracts;
7. the first enabled table-key list;
8. B003b/B003c as separate PRs.

Approval of this documentation PR authorizes design review only. It does not authorize migration,
scope, contract, backend, or frontend implementation.
