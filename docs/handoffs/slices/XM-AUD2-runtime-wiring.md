sprint-section: 7.5

# XM-AUD2-runtime-wiring · manual archive worker seam and isolated MinIO Compose

## status

READY · manual-only runtime wiring. This slice does not enable or register an
archive scheduler, does not upload/read archive objects by default, and does not
claim production activation.

## branch / base / commit

- branch: `ai/codex/XM-AUD2-runtime-wiring`
- worktree: `K:/星芒统一控制平台/wt-xmAUD2-runtime`
- initial local release base read: `5f15db5`
- final rebased base: `9495e3b` (local corrected `release/v0.1-launch`; includes the
  controlled DBR1 Task2 revert/correction and preserves the approved policy genesis)
- commit: final branch `HEAD` (implementation parent `75c9275`; Handoff is the final commit)
- `git fetch` was attempted before the checks but GitHub DNS was unavailable in this
  environment (`Could not resolve host: github.com`). The remote-tracking ref still
  points at rejected `7cfa342`; the local release branch contains the acceptance-line
  controlled rollback/correction at `9495e3b`, so this branch was rebased to that exact
  corrected local tip and never included the rejected history. No release branch mutation
  was performed.

## summary

This slice closes the AUD2 runtime-wiring gap with a deliberately narrow seam:

1. `jobs.AuditArchiveConfig` and `platform-worker` env parsing carry only explicit
   policy/`CredentialRef` names. The default is disabled. `scheduled` mode and an
   explicit scheduler flag return `ErrAuditArchiveSchedulerGated`; production manual
   activation is also fail-closed until a separate lifecycle approval.
2. `AuditArchiveRequest` carries only a lower-case SHA-256 of an externally approved
   signed envelope. `ArchiveRunner` is injected by the later AUD3/AUD5 lifecycle
   implementation; this slice never resolves secrets, reads payloads, or performs an
   object-store call. `ManualArchiveWorker`/`EnqueueManualArchive` are explicit-only
   helpers and are intentionally absent from `NewClient` and the six-job periodic
   `JobManifest`.
3. `deploy/compose/archive.yaml` defines the independent `xingmang-archive` MinIO
   project with its own internal network and volume, immutable `VERSIONS.lock` image
   digest, loopback-only Docker-assigned ports and `archive-fixture` profile. Credentials
   come only from an operator-managed, ignored env file; no value is committed.
4. The manual contract, static boundary test and AUD2 runbook freeze the scheduler gate,
   Object Lock/retention policy and CredentialRef inventory.

## files_changed

- `internal/platform/jobs/audit_archive_manual.go`
- `internal/platform/jobs/audit_archive_manual_test.go`
- `internal/platform/jobs/client.go`
- `cmd/platform-worker/audit_archive_config.go`
- `cmd/platform-worker/audit_archive_config_test.go`
- `cmd/platform-worker/config.go`
- `cmd/platform-worker/main.go`
- `cmd/platform-worker/README.md`
- `contracts/jobs/audit-archive-manual.v1.json`
- `deploy/compose/archive.yaml`
- `deploy/compose/archive.env.example`
- `docs/runbooks/AUDIT-ARCHIVE.md`
- `docs/modules/audit/RUNBOOK.md`
- `tests/security/audit-archive-runtime-wiring.test.sh`

## scheduler / DB-role gate evidence

- Acceptance tail `2026-08-30T10:35Z` (§7.5) authorizes AUD2 wiring but preserves the
  R2-10/DB-role/production gates; the later `11:14:20Z REJECT/ROLLBACK REQUIRED` and
  `11:21:17Z MERGED` correction lines were read before this final rebase.
- The final release contains DBR1 Task1/2 policy contracts, but no proven R2-10 cluster
  lease and no DBR2/DBR3 production role cutover. Therefore `NewClient` was not changed
  to add a periodic worker or `PeriodicJob`; `RegisteredPeriodicJobSpecs()` remains the
  existing six-job set.
- `AuditArchivePeriodicRegistrationAllowed()` is hard-coded false in this slice and is
  covered by tests. The normal `platform-worker` startup log reports
  `audit_archive_periodic_registered=false` without logging endpoints or refs.
- Production manual mode is intentionally rejected here as an extra fail-closed guard;
  a later AUD3/AUD5 slice may relax it only with lifecycle approval and the required
  signed envelope/role/Kill Switch proof.

## CredentialRef inventory (names only)

| purpose | CredentialRef | status |
|---|---|---|
| MinIO object writer | `secret://archive/minio-runtime` | injected only by a later controlled runtime |
| MinIO KMS secret | `secret://archive/minio-kms` | approved SSE-S3 backing secret; value never committed |
| disposable qualification | `secret://archive/minio-qualification` | one-time AUD2 fixture reference |
| independent security sink | `secret://archive/security-sink` | AUD3 reservation; not read by this slice |

`deploy/compose/archive.env.example` contains blank keys only. `archive.yaml` requires
`XM_ARCHIVE_CREDENTIAL_ENV_FILE` to point outside the repository; no real root/KMS value,
DSN, signed envelope, payload or key material appears in source, tests, logs or this Handoff.

## tests_run

- RED (before implementation):
  `go test ./internal/platform/jobs -run 'TestAuditArchive' -count=1 -v` failed as
  expected because `AuditArchiveConfig`, manual trigger and worker APIs did not yet exist.
- `go fmt ./internal/platform/jobs ./cmd/platform-worker` — PASS.
- `go test ./internal/platform/jobs ./cmd/platform-worker -count=1` — PASS.
- `go test ./internal/platform/jobs ./cmd/platform-worker -run 'TestAuditArchive|TestConfigFromEnv.*Archive' -count=1` — PASS.
- `go test -race ./internal/platform/jobs ./cmd/platform-worker -count=1` — PASS.
- `go vet ./...` — PASS.
- `go test -p 1 ./...` — PASS (all repository Go packages; integration tests without an
  explicit loopback DSN reported their normal SKIP state).
- `bash tests/security/audit-archive-runtime-wiring.test.sh` — PASS (static boundary
  checks; invoked through the repository's WSL wrapper).
- `D:/Git/bin/bash.exe scripts/check-governance.sh` — PASS.
- `docker compose --project-name xingmang-archive --file deploy/compose/archive.yaml
  --profile archive-fixture config --quiet` with the tracked blank env example — PASS.
- `git diff --check` — PASS.
- `gitleaks git --redact --no-banner --log-opts='9495e3b..HEAD'` — PASS (after the final
  commit; no leaks); each changed source/config file was also scanned individually with
  `gitleaks dir` — PASS.

## tests_not_run

- No `deploy-local.sh`, `xingmang-launch` rebuild, staging/production worker restart or
  shared PostgreSQL operation was run. This slice intentionally does not change the
  launch compose or apply migration 000018.
- No MinIO container was started from `archive.yaml`: the required operator-managed
  CredentialRef values are intentionally absent. The prior AUD2-s3-adapter disposable
  qualification remains the provider evidence; this slice only validates the compose
  definition statically and with `docker compose config`.
- Full `go test -race ./...` was not run in this slice; targeted race and full serial Go
  evidence above cover the changed runtime package. Frontend pnpm gates were attempted
  but could not start: `pnpm -r run typecheck` hit the known Windows `EPERM` esbuild rename
  during dependency import, and the no-install retry reported missing `tsc` in the fresh
  worktree. No frontend files changed.

## risks / follow_ups

1. AUD3 must inject a real signed-envelope runner, independent AccessRecorder/security
   sink and exact Reader/Writer/DB-role capabilities. The current worker seam cannot
   execute a lifecycle operation by itself.
2. R2-10 and DBR2/DBR3 evidence must be merged and re-verified before any periodic
   registration or production activation is considered. Do not add a dormant scheduler
   to this branch as a shortcut.
3. A deployment owner must provision the MinIO bucket with versioning, Object Lock
   `COMPLIANCE`, 3650-day retain-until and no lifecycle deletion, then preserve fresh
   qualification/scrub evidence. The loopback fixture is not a production failure domain.
4. `secret://archive/security-sink` is deliberately not consumed here; it belongs to the
   separate AUD3 restricted-read slice.

## references

- `docs/handoffs/ACCEPTANCE-LOG.md` tail (`2026-08-30T10:31Z`, `10:35Z`, `11:14:20Z`
  reject/rollback, and `11:21:17Z` controlled correction)
- `docs/handoffs/CODEX-SPRINT-2026-08-29.md` §7.5
- `docs/superpowers/plans/2026-08-28-audit-archive-implementation.md` Task 2/3/5,
  especially Task 5 Step 7 manual-only branch
- `docs/superpowers/specs/2026-08-28-audit-archive-design.md` §§9.2, 12 and 13
- `docs/handoffs/slices/XM-AUD2-s3-adapter.md`
