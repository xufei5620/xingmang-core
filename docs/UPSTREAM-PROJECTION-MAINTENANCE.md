# Upstream projection reconciliation and upgrade gate

This runbook removes the legacy invoice projection boundary that used public
views in the New API or Sub2API application database. Those views created
persistent PostgreSQL dependencies on application columns and can block an
upstream startup migration. The replacement `invoice_bridge` V4 boundary uses
dynamic bridge functions and must have zero persistent dependencies on upstream
relations.

The procedures below do not modify New API or Sub2API source code. The cleanup
contract changes only the exact legacy invoice views, exact invoice reader
roles, their grants, and the database-wide `PUBLIC TEMPORARY` setting that the
legacy installation previously revoked. It never edits an application table or
row and never terminates a database session.

## 1. Tools and guarantees

- `contracts/projection-reconcile.postgresql.sql` audits or removes the legacy
  boundary. Audit mode uses a read-only transaction and rolls back. Apply mode
  is one transaction and is safe to repeat.
- `contracts/upstream-upgrade-preflight.postgresql.sql` is a read-only upgrade
  gate. It supports a fully `detached` boundary and an installed `bridge-v4`
  boundary.
- `scripts/invoke-upstream-projection-maintenance.sh` is the production-host
  path. It explicitly verifies the running database container, database and
  database-user tuple, then streams the reviewed SQL to `psql -X` inside that
  container. It copies no SQL file and uses no DSN or password.
- `scripts/invoke-upstream-projection-maintenance.ps1` is the workstation/CI
  alternative. It reads a one-line DSN from a regular, non-link file, keeps the
  connection string out of process arguments, clears ambient `PG*` connection
  settings, disables password prompting and ignores `psqlrc`.

Both wrappers always invoke `psql -X --no-password -v ON_ERROR_STOP=1`. The
V4 install/rollback files intentionally remain SQL-only because the Go
integration suite also executes them directly; they do not contain psql meta
commands. **Bare `psql`, shell redirection into psql, and manual copy/paste are
forbidden.** Use a wrapper mode so an aborted transaction cannot return a false
zero exit status. All contracts use short timeouts and exact source fingerprints.
They fail without committing if they find any of the following:

- a wrong database or missing upstream fingerprint table;
- an unknown invoice-prefixed object or role;
- an elevated invoice role, role membership or role-owned object;
- an active invoice source-agent session;
- an object/grant dependency that prevents an exact restricted drop;
- a nonzero persistent `pg_depend` edge to an upstream application relation;
- a boundary-specific `PUBLIC TEMPORARY` state mismatch.

The tools never kill sessions. Stop the source agents and investigate if a
session check fails.

## 2. Required production preparation

Do not perform cleanup while an upstream image upgrade, migration, restore or
other DDL operation is running.

1. Keep invoice submission disabled and record the exact New API, Sub2API and
   invoice image digests.
2. Stop every source-agent container for the selected source. Preserve its
   state, pending encrypted spool, signing key and cutover files.
3. Before legacy cleanup, run `scripts/preserve-source-reader-roles.sh --mode
   export` for that source into a newly created root-only absolute path. It
   audits exactly five LOGIN roles with SCRAM verifiers plus one NOLOGIN
   compatibility role and writes a mode-0600 file. Never print, `cat`, attach or
   copy this file into ordinary backups/logs.
4. Verify a current source-database backup and a successful isolated restore.
   Merely having a backup file is not sufficient.
5. Confirm the upstream application is healthy before touching its auxiliary
   boundary.
6. Use the cluster superuser that is also the current source database owner for
   every V4 install/rollback. A non-superuser database owner is deliberately
   rejected because role creation and function ownership transfer are required.
7. Put that connection string in a one-line regular file
   readable only by the operator. Do not pass a password on the command line.

The acknowledgement switches on apply record operator prerequisites; they do
not stop containers or verify a backup automatically.

## 3. Reconcile the current legacy production state

Always audit both sources before either apply. On the Linux production host,
use the database container name, database name and owner user verified directly
from the source's current Compose configuration. The wrapper has no guessed
defaults and the SQL independently checks the source-specific application-table
fingerprint. The audit commits no change:

```bash
bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source newapi --mode audit \
  --container <verified-newapi-postgres-container> \
  --database <verified-newapi-database> \
  --user <verified-newapi-database-owner>

bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source sub2api --mode audit \
  --container <verified-sub2api-postgres-container> \
  --database <verified-sub2api-database> \
  --user <verified-sub2api-database-owner>
```

The equivalent workstation invocation is:

```powershell
pwsh ./scripts/invoke-upstream-projection-maintenance.ps1 `
  -Source newapi `
  -DsnFile C:\secure\newapi-owner.dsn `
  -Mode Audit

pwsh ./scripts/invoke-upstream-projection-maintenance.ps1 `
  -Source sub2api `
  -DsnFile C:\secure\sub2api-owner.dsn `
  -Mode Audit
```

For the production state observed on 2026-08-24, the expected starting
inventory is:

| Source | Legacy views | Legacy roles | `PUBLIC TEMPORARY` | Active readers |
| --- | ---: | ---: | --- | ---: |
| New API | 0 | 6 | revoked | 0 |
| Sub2API | 12 | 6 | revoked | 0 |

Treat this table only as the expected starting state. The live audit is the
authority. Any different count must be reviewed before apply.

Apply New API cleanup first. This specifically handles the partial 0-view,
6-role state that the old all-or-nothing rollback could not handle:

```bash
bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source newapi --mode apply \
  --container <verified-newapi-postgres-container> \
  --database <verified-newapi-database> \
  --user <verified-newapi-database-owner> \
  --ack-source-agents-stopped \
  --ack-backup-verified
```

Workstation alternative:

```powershell
pwsh ./scripts/invoke-upstream-projection-maintenance.ps1 `
  -Source newapi `
  -DsnFile C:\secure\newapi-owner.dsn `
  -Mode Apply `
  -AcknowledgeSourceAgentsStopped `
  -AcknowledgeBackupVerified
```

Then run `Audit` again. It must report zero legacy views, zero legacy roles,
zero active sessions, zero upstream dependency edges and
`public_temporary = true`.

Only after New API passes should the same controlled sequence be performed for
Sub2API:

```bash
bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source sub2api --mode apply \
  --container <verified-sub2api-postgres-container> \
  --database <verified-sub2api-database> \
  --user <verified-sub2api-database-owner> \
  --ack-source-agents-stopped \
  --ack-backup-verified
```

Workstation alternative:

```powershell
pwsh ./scripts/invoke-upstream-projection-maintenance.ps1 `
  -Source sub2api `
  -DsnFile C:\secure\sub2api-owner.dsn `
  -Mode Apply `
  -AcknowledgeSourceAgentsStopped `
  -AcknowledgeBackupVerified
```

Run its audit again and require the same all-zero/restored result. If any apply
statement fails, PostgreSQL rolls back the role locks, grant changes, view
drops and permission restoration together. Do not work around a failure with
manual broad drops.

### Install Bridge V4 after legacy cleanup

Restore the six preserved reader roles with
`preserve-source-reader-roles.sh --mode restore`. The restore streams the
root-only file with `ON_ERROR_STOP=1` and requires zero existing source roles;
it never displays a verifier. Then use the wrapper—never bare psql—to apply
source followed by economic contracts:

```bash
bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source newapi --mode install-source \
  --container <verified-newapi-postgres-container> \
  --database <verified-newapi-database> --user <cluster-superuser-and-db-owner> \
  --ack-source-agents-stopped --ack-backup-verified
bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source newapi --mode install-economic \
  --container <verified-newapi-postgres-container> \
  --database <verified-newapi-database> --user <cluster-superuser-and-db-owner> \
  --ack-source-agents-stopped --ack-backup-verified
```

Repeat for Sub2API, then require the `bridge-v4` upgrade gate and all five
source-specific `check-db` commands. Immediately after all five pass, move the
preservation file into the approved encrypted secret archive or securely remove
it. Do not leave a plaintext SCRAM-verifier SQL file on the host.

## 4. Detached upgrade preflight

Run this after legacy cleanup and immediately before an upstream restart or
upgrade while no bridge is installed:

```bash
bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source newapi --mode upgrade-preflight --boundary-state detached \
  --container <verified-newapi-postgres-container> \
  --database <verified-newapi-database> \
  --user <verified-newapi-database-owner>
```

Workstation alternative:

```powershell
pwsh ./scripts/invoke-upstream-projection-maintenance.ps1 `
  -Source newapi `
  -DsnFile C:\secure\newapi-owner.dsn `
  -Mode UpgradePreflight `
  -BoundaryState Detached
```

Repeat with `-Source sub2api` and its own DSN. The gate requires:

- no legacy public projection object;
- no invoice role or `invoice_bridge` schema;
- no active source session;
- exactly zero upstream `pg_depend` edges;
- the PostgreSQL default `PUBLIC TEMPORARY` grant restored.

Do not upgrade unless the gate prints `upgrade-preflight-passed`.

## 5. Bridge V4 upgrade preflight

After the reviewed V4 bridge contract is installed, its least-privilege reader
roles are allowed to remain during an upstream upgrade, but every source agent
must still be stopped. Run:

```bash
bash ./scripts/invoke-upstream-projection-maintenance.sh \
  --source newapi --mode upgrade-preflight --boundary-state bridge-v4 \
  --container <verified-newapi-postgres-container> \
  --database <verified-newapi-database> \
  --user <verified-newapi-database-owner>
```

Workstation alternative:

```powershell
pwsh ./scripts/invoke-upstream-projection-maintenance.ps1 `
  -Source newapi `
  -DsnFile C:\secure\newapi-owner.dsn `
  -Mode UpgradePreflight `
  -BoundaryState BridgeV4
```

The V4 gate requires:

- no legacy `public.invoice_<source>_*` object;
- exactly the six source reader roles plus the source-specific NOLOGIN bridge
  owner, with no elevated attributes, inheritance or memberships. The legacy
  compatibility payments reader and owner are NOLOGIN with connection limit 0;
  the other five readers are LOGIN with connection limit 2;
- every reader has exactly `default_transaction_read_only=on`,
  `statement_timeout=15s`, `lock_timeout=5s` and
  `idle_in_transaction_session_timeout=15s`; the owner has no role GUC;
- the database-owner-owned `invoice_bridge` schema grants no bridge role CREATE
  and contains exactly five functions:
  `<source>_payments_v4`, `<source>_usage_v4`, `<source>_credits_v4`,
  `<source>_balances_v4` and `<source>_identities_v4`;
- every function has the exact `(text,jsonb) RETURNS SETOF jsonb` signature,
  `LANGUAGE plpgsql`, `SECURITY DEFINER`, `VOLATILE`, source bridge-owner
  ownership, exact reviewed body SHA-256 and only
  `search_path=pg_catalog,row_security=on` in `proconfig`;
- bridge owner effective SELECT columns equal the exact five-stream allowlist,
  no caller has any raw SELECT/mutation/sequence privilege, and no bridge role
  owns anything except the owner's five functions;
- every source relation read by the bridge has RLS disabled. Any future RLS
  policy requires a new design/review rather than accepting silent filtering;
- no bridge role has effective CREATE on any non-system schema; PUBLIC has no
  invoice_bridge USAGE or function EXECUTE. PUBLIC schema USAGE may remain at
  the upstream database's default because callers do not depend on it;
- each reader has only
  the exact stream function EXECUTE grant (both compatibility and V3 payments
  readers map to the payments function);
- no connected source-agent session;
- `PUBLIC TEMPORARY` still revoked while the bridge is enabled;
- exactly zero persistent dependency edges from invoice views/functions to
  public upstream relations.

Run this gate twice: immediately before the upstream upgrade and again after
the upstream reports healthy. Then run the source connector schema/privilege
check against the upgraded version before restarting any source agent.

## 6. Mandatory upstream upgrade sequence

For either source, the sequence is:

1. stop that source's agents and prove zero sessions;
2. verify backup plus isolated restore;
3. run the matching `detached` or `bridge-v4` upgrade preflight;
4. upgrade/restart only the upstream application through its normal release
   procedure;
5. prove the upstream application and database migrations are healthy;
6. rerun the same read-only upgrade preflight;
7. run the connector database-contract check for every stream;
8. start one canary agent, verify a complete signed cycle and exact ACK, then
   start the remaining streams;
9. keep invoice submission disabled until all stream health and reconciliation
   watermarks are current.

If the post-upgrade fingerprint or bridge function fails, leave all source
agents stopped. Adapt and review the bridge contract against the new upstream
schema; do not weaken the gate or recreate the old public views.

## 7. Quiesced first cutover (mandatory)

A Repeatable Read snapshot cannot include an upstream transaction that started
before the snapshot but commits afterward. Therefore stopping only source
agents is insufficient. For the first cutover of each source:

1. disable invoice submission and stop all source agents;
2. stop the matching New API or Sub2API **application container**, while keeping
   its PostgreSQL container running;
3. keep the application stopped and run:

   ```bash
   bash ./scripts/invoke-upstream-projection-maintenance.sh \
     --source newapi --mode cutover-quiescence-preflight \
     --container <verified-newapi-postgres-container> \
     --database <verified-newapi-database> --user <cluster-superuser-and-db-owner> \
     --ack-upstream-app-stopped
   ```

4. the gate must report zero other client backends and zero prepared
   transactions. SQL cannot prove that Docker remains stopped; this is an
   operator-controlled maintenance invariant;
5. without restarting the upstream application, immediately run the one-time
   `cutover-init`, then `check-cutover`, and verify nonempty encrypted
   `manifest.enc`/`baseline.enc` plus recorded hashes;
6. only after verification may the upstream application restart; initialize
   agent state and proceed with the balances-first canary sequence.

If any connection reappears or cutover verification fails, do not recapture or
overwrite the cutover files. Investigate and restart the reviewed procedure.

## 8. Fully removing Bridge V4

Use the reviewed source-specific bridge rollback contract, not the legacy
reconcile contract. The legacy contract deliberately refuses to run when an
`invoice_bridge` schema exists. A complete bridge rollback must stop agents,
remove only the allowlisted bridge functions/schema and seven source roles,
prove zero remaining upstream dependencies, and restore
`PUBLIC TEMPORARY = true` in one transaction.

Invoke it only through `--mode rollback-bridge` with both stop/backup
acknowledgements. Unknown ACL or ownership makes direct `DROP ROLE` fail and
rolls back the entire removal; the rollback never uses `DROP OWNED`.
