# Source Bridge V4 contract

Bridge V4 replaces every source-dependent `invoice_*_projection_*` view. The
invoice system must never leave a PostgreSQL dependency that can block a New
API or Sub2API migration.

## Boundary

- Schema: `invoice_bridge`, owned by the upstream database owner.
- Owner role: `invoice_<source>_bridge_owner`, `NOLOGIN`, `NOINHERIT`,
  connection limit `0`, no membership or elevated attribute.
- Caller roles: one isolated role for each of `payments`, `identities`,
  `usage`, `credits`, and `balances`. Callers have no raw column grant and can
  execute only their own bridge function.
- Functions: `invoice_bridge.<source>_{payments,identities,usage,credits,balances}_v4(text,jsonb)`.
  They are `SECURITY DEFINER`, `LANGUAGE plpgsql`, return `SETOF jsonb`, and
  pin both `search_path=pg_catalog` and `row_security=on`.
- Function bodies contain fixed dynamic SQL only. Parameters are values, never
  identifiers or SQL fragments. The bridge owner receives exact source-column
  grants, but no login role can assume it.

This design intentionally produces zero `pg_depend` edges from a bridge
function to an upstream relation or column. A compatible source `ALTER` works
while the bridge is installed. Removing or incompatibly changing a required
column causes the affected connector call to fail closed; it does not stop the
upstream migration or restart.

## Identity boundary

Sub2API and New API do not use the same provider-key value:

- Sub2API provider key and issuer:
  `https://auth.solov.cc/realms/solov`
- New API provider slug: `solov-sso`
- New API issuer: `https://auth.solov.cc/realms/solov`

The identity callers cannot read raw identity/OAuth tables. The functions
filter to these pinned values before returning a subject.

## Reviewed install order

For one source database:

1. Stop and prove zero source-agent sessions.
2. Back up and verify the upstream database.
3. Export the exact six existing reader definitions/SCRAM verifiers with the
   root-only `preserve-source-reader-roles.sh`; never display its output file.
4. Remove all legacy projection views/roles with the reviewed reconcile flow,
   then restore the six roles from that mode-0600 file.
5. As the cluster superuser that also owns the current database, use
   `invoke-upstream-projection-maintenance` mode `install-source` to apply
   `<source>-source-projection-grants.postgresql.sql`.
6. Use the same wrapper's `install-economic` mode for
   `<source>-economic-projection-grants.postgresql.sql`. Bare psql is forbidden;
   wrappers force `-X`, no password prompt and `ON_ERROR_STOP=1`.
7. Prove exactly five bridge functions, seven roles, no legacy views, and zero
   bridge-to-relation dependencies. The gate also proves exact function body
   hashes, owner column ACL, caller isolation, no unexpected ownership/schema
   CREATE and RLS disabled on every reviewed source relation.
8. Run `source-agent-prod check-db` independently for all five streams before
   starting an agent. Immediately encrypt/archive or securely delete the
   plaintext role-preservation file after all five checks pass.

Both install contracts are idempotent. In particular, applying
`source -> economic -> source` restores the complete bridge-owner ACL; it must
not silently revoke an economic stream.

## Quiesced cutover boundary

The first cutover requires an upstream write quiescence window. Stop the
matching New API/Sub2API application container while leaving PostgreSQL up, run
the reviewed `cutover-quiescence-preflight`, and require zero other client
backends and zero prepared transactions. Keep the application stopped while
`cutover-init` captures and `check-cutover` verifies the encrypted manifest and
baseline. Only then restart the upstream application. Stopping source agents
alone cannot close the in-flight transaction window.

## Complete removal

Use only the source-specific reviewed rollback:

- `sub2api-bridge-v4-rollback.postgresql.sql`
- `newapi-bridge-v4-rollback.postgresql.sql`

Rollback is a single transaction, rejects active reader sessions or an
unexpected object/role, uses `RESTRICT`, contains no `CASCADE`, preserves every
upstream table and row, removes all seven integration roles, and restores the
database's `PUBLIC TEMPORARY` baseline.

Run rollback only through wrapper mode `rollback-bridge` as the cluster
superuser/current database owner. It explicitly revokes the reviewed grants and
then uses direct `DROP ROLE`; unknown ACL/ownership blocks and rolls back the
transaction. `DROP OWNED` is forbidden.

## PostgreSQL matrix

The same contracts and Go connector tests must pass against both source
runtime families before release:

- PostgreSQL 15 (New API production family)
- PostgreSQL 18 (Sub2API production family; currently 18.4)

Run `agents/scripts/verify-bridge-postgres-matrix.ps1`. The integration suite
proves financial semantics, exact caller access, encrypted cutover capture,
configuration drift blocking, install idempotency, `pg_depend=0`, compatible
`ALTER TYPE`, incompatible drop fail-closed behavior, and complete rollback.

These SQL files are review-only. No source agent, application startup, or
automatic migration may install or remove the boundary.
