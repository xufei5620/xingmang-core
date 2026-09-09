sprint-section: 7.5

# XM-DBR1 Task1/Task2 · role policy, rotation transition, and read-only verifier

## status

READY（Task1 policy/transition + Task2 read-only catalog verifier/CLI；无写 SQL、凭据或部署）

- branch: `ai/codex/XM-DBR1-task2-correction`
- worktree: `K:/星芒统一控制平台/wt-xmDBR1-task2-correction`
- base release: `release/v0.1-launch` at `2bfd623ec80ca08bb6cbf3b14a8f2e82a4eea524`
- approval: `2026-08-30T09:40Z APPROVED DBR1 Task1/Task2 ... base 00fe49b`
  from `docs/handoffs/ACCEPTANCE-LOG.md`; approved `00fe49b` is an ancestor of the
  current base. Task1 was already accepted as merge `1aafabc`; this correction
  branch carries the read-only Task2 verifier/CLI and final Task1 inventory
  fixes without inheriting rejected `a58b8c0`.
  Task3 harness remains the separately accepted slice.
- delivery commit: branch tip (the exact SHA is reported in the READY line;
  acceptance should use `git rev-parse HEAD` after any review fix).

## summary

This slice freezes the PostgreSQL role topology, exact object/column ACL
inventory, PUBLIC/default-ACL contract, and the A/B credential-rotation state
machine as offline artifacts. `Policy` exposes deterministic `Grant`,
`Allows`, `AllowsTable`, and `AllowsColumns` lookups. Strict loaders reject
unknown/duplicate/missing fields, invalid UTF-8, trailing JSON, unknown roles,
objects, columns, privileges, direct login grants, and stale/no-runtime marker
drift. The policy enumerates the current release catalog (including UI,
RUNWAY, and AUD2 archive objects) while keeping AUD archive capability roles
out of v1.

`RolePolicyStateEvent` and `ChangeRequestState` use canonical JSON and a
digest-chained JSONL history. `ValidateTransition` accepts only genesis,
rotation-start (`steady-a → rotating-a-b`), rotation-close
(`rotating-a-b → steady-b`), and rotation-invariant policy updates. It binds
exact policy bytes, predecessor event/policy digests, the approved/closed CR
artifact, UTC windows, and closure evidence, and returns stable violation
codes for replay, skipped transitions, expiry, and digest mismatches.

Task2 adds a pure `CatalogSnapshot` verifier plus a pgx/pgxpool read path. It
compares roles, memberships, owners, relation/column/sequence/routine/type ACLs,
default ACL families, PUBLIC privileges, RUNWAY provenance, and
`pg_stat_activity` session evidence with deterministic violation ordering. The
`db-role-verify` CLI validates `DATABASE_URL` through `pgdsn.Validate`, opens a
forced-read-only pool, and returns exit `0` (match), `1` (policy violation), or
`2` (configuration/connection error) without echoing DSNs or passwords.

## files_changed

- `contracts/database/role-policy.v1.json`
- `contracts/database/role-policy-state-event.v1.schema.json`
- `contracts/database/role-policy-state-events.v1.jsonl`
- `contracts/database/role-policy-change-request-state.v1.schema.json`
- `internal/platform/dbroles/policy.go`
- `internal/platform/dbroles/policy_test.go`
- `internal/platform/dbroles/transition.go`
- `internal/platform/dbroles/transition_test.go`
- `internal/platform/dbroles/verifier.go`
- `internal/platform/dbroles/verifier_test.go`
- `internal/platform/dbroles/verifier_integration_test.go`
- `cmd/db-role-verify/main.go`
- `cmd/db-role-verify/main_test.go`
- this Handoff

No SQL, migration, GRANT/REVOKE/ALTER, compose identity change, worker/API
runtime wiring, credential, or release file was changed; the verifier CLI is a
read-only inspection tool only.

## contract and data-source mapping

| contract area | source / authority | implementation boundary |
| --- | --- | --- |
| owner/capability/login roles | DBR design §5 and DBR1 plan Task1 | fixed `xm_migrator`, five NOLOGIN capabilities, A/B identities; attributes and memberships are declarative only |
| database/schema/PUBLIC | design §§6, 8 | `current_database` selector, public owner `pg_database_owner`, PUBLIC database/schema/type/routine privileges empty; no SQL execution |
| table/column ACL | design §7 and approved release migrations 000001–000018 | exact relation/column inventory, separate table vs column privileges; chain_root UPDATE only `exported_at`,`export_target` |
| sequences/routines/types/domains | design §§7–8 plus current shared-catalog read-only inventory | exact sequence USAGE/SELECT, routine EXECUTE, River enum USAGE; no wildcard/ALL |
| RUNWAY | approved merge `809eec2df487abb53ab99c7a1295bd20750cc324` (ancestor) | config/history/view objects and 40-hex merge SHA gate are represented; live activation remains DBR2/3 |
| AUD2 archive | approved AUD2 merge/input/generated records in ACCEPTANCE-LOG | four archive tables are explicit `no_runtime_access`; dedicated future AUD roles remain unknown/fail-closed in policy v1 |
| rotation state | design §11 | nine-field state object, A/B identity and membership topology, UTC windows, optional active-session evidence |
| state history | design §11 and plan Task1 | exact-byte policy/CR digests, genesis JSONL, sequence/hash/CR/closure validation; no signing key or external history lookup |
| catalog verifier | DBR1 plan Task2 / PostgreSQL catalog allowlist | read-only pgx queries only; pure snapshot comparison and CLI exit contract |

## decisions

1. **Canonical version representation.** The repository's contract convention
   uses numeric JSON `version: 1` (as in the other signed contracts and the
   DBR1 harness). The plan's illustrative string `"1"` is accepted only as a
   compatibility spelling by `LoadPolicy` and is normalized to numeric `1`;
   emitted policy/state artifacts are numeric and schemas pin numeric `1`.
2. **Two digest meanings are explicit.** `LoadPolicy`/`Policy.Canonical`
   return a semantic digest of normalized canonical JSON for content identity;
   `RawDigest` and `TrustedPolicyState.PolicySHA256` bind exact artifact bytes
   (including line endings) for event history. Transition code never substitutes
   the semantic digest for the raw binding.
3. **Current catalog inventory.** The policy follows a read-only shared-catalog
   census at the current release: `river_job_notify()` was dropped by River
   migration 004 and is not listed; `river_job_state_in_bitmask(bit,
   river_job_state)` is listed with its identity arguments; `river_leader` has
   `leader_id`; `balance_history_id_seq` grants worker USAGE and backup/ops
   SELECT. `upstream_account`/`profit_daily` include `platform_id`.
4. **Archive boundary.** AUD2 archive tables are represented with an explicit
   `no_runtime_access` marker. AUD-specific capability roles are intentionally
   not added to DBR policy v1, so a catalog containing them fails closed until a
   separately approved policy version.
5. **Rotation provenance.** `steady-b` retains old/new/CR/start/deadline fields
   as required by §11; role login/membership state and optional
   `active_sessions` evidence prove the old identity is no longer active. The
   helper `SetRotationTopology` is offline fixture construction only.

## verification

Executed from this linked worktree created directly at `2bfd623` (if release moves
again, rebase and rerun before acceptance):

- TDD RED: `go test -p 1 ./internal/platform/dbroles -count=1` before
  implementation failed at compile time with undefined `Policy`,
  `RolePolicyStateEvent`, and `ValidateTransition` symbols (expected missing
  package behavior).
- GREEN: `go fmt ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go vet ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go vet ./internal/platform/dbroles ./cmd/db-role-verify` — PASS.
- Task2 RED: `go test -p 1 ./internal/platform/dbroles -run TestVerifySnapshot`
  initially failed with undefined `CatalogSnapshot`/`VerifySnapshot` before
  verifier implementation; subsequent GREEN covers role, membership, owner,
  object/column, sequence, default-ACL, PUBLIC, rotation-session, and
  application-name drift cases.
- `go test -p 1 ./... -count=1` — PASS (all repository packages).
- `go vet ./...` — PASS.
- `python` JSON parse of all `contracts/database/*.json` — PASS; policy has
  50 exact objects and five rotations.
- policy/event raw digest check — PASS: the release genesis line is byte-for-byte
  unchanged (`current_policy_sha256=43c54c…`); appended sequence 2
  (`policy-update`) binds corrected policy bytes (`current_policy_sha256=c9c79c…`).
  `LoadStateEvents` replays both lines and rejects edits/reordering.
- `GOVERNANCE_BASE_REF=release/v0.1-launch GOVERNANCE_REQUIRE_BASE=1 bash
  scripts/check-governance.sh` — PASS (Git Bash linked-worktree invocation).
- `git diff --check release/v0.1-launch` — PASS before commit.
- `gitleaks git --redact --no-banner --staged` — PASS (0 leaks; 0 commits in
  staged snapshot before commit).
- `gitleaks git --redact --no-banner --log-opts=2bfd623ec80ca08bb6cbf3b14a8f2e82a4eea524..HEAD` — rerun after final commit.

## not_run / intentionally out of scope

- DBR1 disposable PostgreSQL harness execution (Task3 is already a separate
  accepted slice); the Task2 live query path is implemented but no PostgreSQL
  connection was opened in this worktree, and no shared stack, staging, or
  production access occurred.
- No SQL or role operation of any kind: no CREATE/ALTER, GRANT, REVOKE, SET
  ROLE, owner transfer, migration, compose change, or runtime wiring.
- No real credentials, secret refs/values, network calls, MinIO/AUD2 access,
  signing keys, or external service interaction.
- No release merge, acceptance-log edit, deployment, or GitHub PR/Action.

## risks and follow_ups

1. The verifier remains a read-only Task2 foundation. It must consume the exact
   object/column inventory and independently query the catalog allowlist; it
   must not infer live state from policy bytes alone.
2. The policy's RUNWAY merge SHA is a provenance gate, not proof of a deployed
   database. DBR2 exact owner/ACL SQL and DBR3 runtime identity cutover remain
   separately approved work.
3. The checked-in policy uses numeric version `1`; if product governance later
   requires string wire versions, issue a new policy version rather than
   silently changing v1 canonical bytes.
4. Rotation `active_sessions` is optional evidence; a verifier must require and
   validate catalog session evidence when closing A→B, and must keep open
   rotating states fail-closed after their deadline.
5. Before acceptance, re-read `ACCEPTANCE-LOG.md` and rebase if release moves;
   recompute the Handoff base and rerun all gates. Acceptance owns merge,
   deployment, and the final READY/REJECT decision.

## explicit boundary

`NO LIVE DB CHANGE` · `NO STAGING/PRODUCTION` · `NO REAL CREDENTIALS` ·
`NO SQL/GRANT/REVOKE/ALTER` · `READ-ONLY VERIFIER/CLI ONLY` · `NO MERGE` · `NO DEPLOY`
