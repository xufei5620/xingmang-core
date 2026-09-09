# XM-CPA Consistent Snapshot Design

## Status and approval

Approved by the product owner on 2026-09-01. This design replaces direct
container reads of the active cpa-manager-plus WAL database. It does not modify
the CPA source database and does not mount CPA credential directories.

## Problem

Production proved two independent defects:

1. `codex_inspection_runs` uses `id INTEGER` and `started_at_ms`; the current
   connector queries a nonexistent text `run_id` and drops the run time.
2. ncruces SQLite cannot open the active WAL database through the current
   read-only Docker bind because shared-memory coordination still needs host
   filesystem access. Mounting the active directory read-only is therefore not
   a usable production contract.

## Architecture

A versioned host lifecycle tool, `cpa-snapshot`, opens only
`/root/cpa-stack/cpam-data/usage.sqlite` with SQLite `mode=ro` and uses the
pinned ncruces online-backup API to copy a consistent view into a temporary
file under `/var/lib/xingmang/cpa-snapshot/published`. It never executes SQL against the
source database other than reads required by SQLite backup coordination.

The destination is made standalone before publication:

- write one `xingmang_snapshot_metadata_v1` row containing a random generation
  and `observed_at_ms`;
- require `PRAGMA journal_mode=DELETE`;
- require `PRAGMA quick_check` to return exactly `ok`;
- close the database and reject leftover destination `-wal`/`-shm` files;
- chmod/chown the file for UID/GID 10001 read access, fsync the file, atomically
  hardlink the verified current file to root-only `history/<generation>.sqlite`,
  rename the new file over `usage.sqlite`, then fsync the containing directories.

Every pre-rename failure removes only the unpublished temporary file. The last
published snapshot is never deleted or replaced by a failed generation; one
bounded previous generations are retained outside the consumer mount.

The binary is built in the existing Docker builder and copied from the exited
`migrate` container by `deploy-local.sh`; no development tool is installed on
the server. A root-owned systemd oneshot service runs the binary and a timer
repeats it every five minutes. The first snapshot must succeed before
platform-api/platform-worker are started in production file mode.

Only `/var/lib/xingmang/cpa-snapshot/published:/var/lib/xm/cpa:ro` is mounted into the API and
worker. The active `/root/cpa-stack/cpam-data` directory is no longer mounted
into either platform container.

## Read contract

Each successful connector read must first read the embedded snapshot metadata
from the same SQLite handle as its business queries:

- `Snapshot.ObservedAt = time.UnixMilli(observed_at_ms).UTC()`;
- `Snapshot.Watermark = generation`;
- missing, malformed, future-dated, or duplicate metadata is `bad_response`.

`AccountHealth` selects the latest real run as:

```sql
SELECT id, started_at_ms
FROM codex_inspection_runs
ORDER BY started_at_ms DESC, id DESC
LIMIT 1
```

It joins `codex_inspection_results.run_id` with the integer ID, exposes the ID
as a decimal string, and exposes `RunAt` as UTC `time.UnixMilli`.

The worker still permits independent query failures, but before writing any
successful observation it requires every successful result in the round to
carry the same non-empty generation. A mixed generation fails all four CPA
metrics for that round and preserves the last good observations.

`cpa.accounts.health` includes `run_at` as RFC3339. The overview and assurance
screens display it next to the run ID. Data freshness in every CPA card comes
from the snapshot capture time, never from the worker's current clock.

## Failure and rollback

- Snapshot failure: retain the previous snapshot; timer retries; observations
  age naturally and become stale.
- Mixed generation: write four failed sync states, keep prior values, and retry
  next cycle.
- Missing snapshot at deploy: fail before API/worker startup.
- Rollback: set `XM_CPA_MODE=off`, redeploy, then disable the timer. Never point
  platform containers back at the active WAL directory.

## Security boundaries

- No CPA `auths/`, `config.yaml`, raw API keys, or OAuth material are read or
  mounted.
- The snapshot producer is a Platform Lifecycle Operation, not an Action
  bypass and not part of user request handling.
- The source database is opened `mode=ro`; only SQLite WAL coordination files
  may be touched by the host process.
- Published snapshots are root-owned, group-readable only by GID 10001, and
  mounted read-only into platform containers.
- File names and paths are fixed by the unit; the platform still rejects path
  traversal and SQLite URI injection.

## Verification

Tests must cover live WAL writes during repeated online backups, source/dest
schema equality, metadata accuracy, `quick_check`, atomic replacement,
failure retention, no destination WAL/SHM, UID 10001 read access, real
inspection schema ordering, RunAt propagation, and mixed-generation rejection.
Both Compose configurations, deploy-contract tests, Go/front-end suites,
Storybook, governance, sqlc zero-diff, and gitleaks must exit 0 before push.
