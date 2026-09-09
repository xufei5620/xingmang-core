# XM-CPA Consistent Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace direct reads of the active CPA WAL database with a host-published consistent snapshot and fix inspection run identity/time end to end.

**Architecture:** A Docker-built static host tool performs ncruces online backup, embeds generation metadata, validates and atomically publishes one standalone SQLite file. Systemd refreshes it every five minutes; API and worker consume only the published read-only snapshot and reject mixed generations.

**Tech Stack:** Go 1.27, github.com/ncruces/go-sqlite3 v0.32.0, Docker Compose, systemd, React/TypeScript/Vitest.

**Spec:** `docs/superpowers/specs/2026-09-01-xm-cpa-consistent-snapshot-design.md`

## Global Constraints

- Do not modify the CPA source database or mount `/root/cpa-stack/cpa/auths` or any CPA `config.yaml`.
- Do not install Go, Node, pnpm, gitleaks, or any development tool on fiberstate.
- Do not use `immutable=1`, broaden platform-container access to the active database, or fall back to direct WAL reads.
- Every production behavior change follows red-green TDD.
- Push is blocked unless gitleaks and every listed gate independently exit 0.

---

### Task 1: Snapshot producer core and command

**Files:**
- Create: `internal/platform/cpasnapshot/snapshot.go`
- Create: `internal/platform/cpasnapshot/snapshot_test.go`
- Create: `cmd/cpa-snapshot/main.go`
- Create: `cmd/cpa-snapshot/main_test.go`

**Interfaces:**
- Produces: `cpasnapshot.Publish(ctx context.Context, Options) (Metadata, error)`.
- Produces: `cpasnapshot.Verify(ctx context.Context, path string) (Metadata, error)`.

- [ ] **Step 1: Write failing online-backup and failure-retention tests**

Create a WAL-mode source, keep a writer active, publish repeatedly, and assert
each output passes `quick_check`, has no `-wal`/`-shm`, and contains either the
entire pre-write or post-write transaction. Seed a valid old target, inject a
failure before rename, and assert its bytes remain unchanged.

- [ ] **Step 2: Run the focused tests and confirm RED**

Run: `go test ./internal/platform/cpasnapshot ./cmd/cpa-snapshot`

Expected: build/test failure because `Publish`, `Verify`, and command parsing do
not exist.

- [ ] **Step 3: Implement the minimum publisher**

Use `driver.Conn.Raw().BackupInit("main", tempURI)`, check both `Step(-1)` and
`Close()`, insert exactly one
`xingmang_snapshot_metadata_v1` row, require DELETE journal and `quick_check`,
sync, rename, and sync the directory. Keep source DSN `mode=ro`; never log row
contents or paths outside the fixed source/target pair.

- [ ] **Step 4: Run focused tests and confirm GREEN**

Run: `go test ./internal/platform/cpasnapshot ./cmd/cpa-snapshot`

- [ ] **Step 5: Commit the producer slice**

```bash
git add internal/platform/cpasnapshot cmd/cpa-snapshot
git commit -S -m "feat(cpa): publish consistent sqlite snapshots"
```

### Task 2: Connector metadata and real inspection schema

**Files:**
- Modify: `connectors/cpa/contract.go`
- Modify: `connectors/cpa/file_client.go`
- Modify: `connectors/cpa/file_client_test.go`
- Modify: `connectors/cpa/observations.go`
- Modify: `connectors/cpa/observations_test.go`

**Interfaces:**
- Consumes: `xingmang_snapshot_metadata_v1(generation, observed_at_ms)`.
- Produces: decimal `RunID`, non-nil `RunAt`, and snapshot generation/time.

- [ ] **Step 1: Change synthetic schema and write failing tests**

Use `codex_inspection_runs(id INTEGER PRIMARY KEY, started_at_ms INTEGER)` and
integer `codex_inspection_results.run_id`. Insert rows out of ID/insertion order
so only `ORDER BY started_at_ms DESC, id DESC` can pass. Assert every read uses
metadata time/generation and missing metadata fails closed.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `go test ./connectors/cpa`

Expected: the current `run_id` query and `c.clock()` snapshots fail assertions.

- [ ] **Step 3: Implement metadata reads and AccountHealth fix**

Read metadata on the same DB handle, replace all read-time snapshots, query
`id, started_at_ms`, join with `int64`, format ID with `strconv.FormatInt`, set
`RunAt`, and add RFC3339 `run_at` to the accounts observation.

- [ ] **Step 4: Run focused tests and confirm GREEN**

Run: `go test ./connectors/cpa`

- [ ] **Step 5: Commit the connector slice**

```bash
git add connectors/cpa
git commit -S -m "fix(cpa): bind health to real inspection runs"
```

### Task 3: Same-generation sync and UI time

**Files:**
- Modify: `internal/platform/jobs/cpa_sync.go`
- Modify: `internal/platform/jobs/cpa_sync_test.go`
- Modify: `web/apps/admin-web/src/api/cpa.ts`
- Modify: `web/apps/admin-web/src/components/CPAOverviewPanel.tsx`
- Modify: `web/apps/admin-web/src/components/CPAAssurancePanel.tsx`
- Create: `web/apps/admin-web/src/components/CPAHealthPanels.test.tsx`

**Interfaces:**
- Consumes: non-empty `Snapshot.Watermark` generation and `run_at` RFC3339.
- Produces: four all-failed observations on generation mismatch.

- [ ] **Step 1: Write failing worker/UI tests**

Assert mixed `wm-a`/`wm-b` results write four failed observations while
preserving prior values. Render an accounts metric with `run_at` and assert both
CPA views display the formatted run time.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `go test ./internal/platform/jobs -run CPA`

Run: `pnpm --config.verify-deps-before-run=false --filter admin-web run test -- CPAHealthPanels`

- [ ] **Step 3: Implement generation gate and UI propagation**

Collect successful watermarks before conversion; if more than one non-empty
generation appears, replace all four results with `bad_response` failures.
Expose `run_at?: string` and render it without changing any Action path.

- [ ] **Step 4: Run focused tests and confirm GREEN**

Repeat both commands from Step 2.

- [ ] **Step 5: Commit the sync/UI slice**

```bash
git add internal/platform/jobs web/apps/admin-web/src
git commit -S -m "fix(cpa): keep sync rounds on one snapshot generation"
```

### Task 4: Host service, deploy integration, and contracts

**Files:**
- Modify: `deploy/docker/go.Dockerfile`
- Create: `deploy/cpa-snapshot/xingmang-cpa-snapshot.service`
- Create: `deploy/cpa-snapshot/xingmang-cpa-snapshot.timer`
- Modify: `deploy/scripts/deploy-local.sh`
- Modify: `deploy/compose/server-prod.yaml`
- Modify: `deploy/compose/.env.example`
- Modify: `tests/deploy/deploy-local.test.sh`
- Modify: `tests/deploy/deploy0-b.test.sh`
- Create: `tests/security/cpa-snapshot-runtime-wiring.test.sh`
- Modify: `contracts/connectors/cpa.read.v1.md`
- Create: `docs/runbooks/CPA-SNAPSHOT.md`
- Create: `docs/handoffs/slices/XM-CPA-SNAPSHOT.md`

**Interfaces:**
- Produces host file `/var/lib/xingmang/cpa-snapshot/published/usage.sqlite` mode 0640, root:10001.
- Produces systemd units `xingmang-cpa-snapshot.service/.timer`.
- Compose consumes only `/var/lib/xingmang/cpa-snapshot/published:/var/lib/xm/cpa:ro`.

- [ ] **Step 1: Write failing deploy-contract tests**

Assert no active cpam-data bind remains, both consumers mount the snapshot
directory read-only, the Docker build contains the tool, deployment installs
and runs the service before API/worker, and the unit/timer use only fixed paths.

- [ ] **Step 2: Run deploy tests and confirm RED**

Run: `bash tests/security/cpa-snapshot-runtime-wiring.test.sh`

Run: `bash tests/deploy/deploy-local.test.sh`

- [ ] **Step 3: Implement packaging and deployment**

Build/copy `cpa-snapshot` into the migrate image. In production file mode,
copy it from the exited migrate container, install root-owned units, start one
snapshot synchronously, verify the target, enable the timer, then start API and
worker. Leave staging and CPA-off flows unchanged.

- [ ] **Step 4: Run deploy and Compose gates**

Run all `tests/deploy/*.test.sh`, then render `launch.yaml` and
`launch.yaml + server-prod.yaml` with the reviewed placeholder environment.

- [ ] **Step 5: Update contract, runbook, and handoff**

Remove every statement that containers read the active WAL directory. Record
the real run schema, embedded metadata contract, failure behavior, install and
rollback commands, tests run/not run, and residual risks.

- [ ] **Step 6: Commit the deployment slice**

```bash
git add deploy tests/deploy contracts/connectors/cpa.read.v1.md docs/runbooks/CPA-SNAPSHOT.md docs/handoffs/slices/XM-CPA-SNAPSHOT.md
git commit -S -m "feat(deploy): publish CPA snapshots on the host"
```

### Task 5: Full gates, push, deploy, and production evidence

**Files:**
- Modify: `docs/handoffs/ACCEPTANCE-LOG.md`

- [ ] **Step 1: Run all local gates separately**

Run `gofmt`, `go test ./...`, `go vet ./...`, `go build ./...`, all deploy
tests, sqlc generate with zero diff, front-end typecheck/test/build, Storybook,
governance, Compose rendering, and `git diff --check`. Capture each exit code.

- [ ] **Step 2: Run gitleaks as the push gate**

Scan `4c1e7bd669d262f39805aebe3c681806b83648f4..<final HEAD>` so commits
`1186061`, `639bfb4`, `b51c740`, `1a448f6`, the RC53 evidence log, and every CPA
commit are covered. Require exit 0.

- [ ] **Step 3: Verify signed commits and push explicit server ref**

Verify every new commit with `git verify-commit`, then push exactly
`server release/v0.1-launch:release/v0.1-launch`. Confirm `git ls-remote server`
returns the final SHA.

- [ ] **Step 4: Deploy and verify production**

Run the approved `nice -n 10 bash deploy/scripts/deploy-local.sh` command with
the production override. Require final `DEPLOY LOCAL PASS`, timer enabled,
fresh snapshot metadata, no source bind in API/worker, four successful
same-generation `cpa_sync` observations, non-null `run_at`, and real CPA pages.

- [ ] **Step 5: Append final acceptance evidence**

Record final SHA, gate exit codes, snapshot generation/age (not business rows),
service/timer status, container mounts, CPA observation success, endpoint/page
checks, deployment result, and rollback boundary.
