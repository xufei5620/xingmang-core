# XM-INV-IDENTITY-MIGRATE: admin identity rebind tool (CR-0006 change item f)

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...` against real PostgreSQL, clean
  across all 32 packages -- 27 test-bearing packages `ok`, 5 with no test
  files; `gofmt -l` clean on every file this slice touched;
  `scripts/test-release-image-gate.ps1` offline/static fixtures passing;
  gitleaks scan of both commits on this branch since the base -- see "Tests
  run"). Not deployed, no production or server contact of any kind, no
  release ceremony run, no RC advanced, no real migration ever executed
  against a real admin identity.
- **branch:** `ai/claude/XM-INV-IDENTITY-MIGRATE`, based on
  `ai/claude/XM-INV-CONSOLE-ASSERT` at `c91963d`, worktree
  `K:/发票/wt-XM-INV-IDMIGRATE`.
- **commits:** two commits on top of the base, see `git log --oneline
  c91963d..HEAD`:
  - `0ff6e9a` feat(auth): add identity-migrate CLI for CR-0006 admin identity rebind
  - `cb2ab50` fix(auth): correct misplaced MigrateOIDCBinding doc comment
    (a doc-comment-only follow-up to the previous commit; no behavior
    change, caught on self-review before this handoff was written)

## Summary

CR-0006 (`docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`,
in the xingmang-platform repo) change item f, flagged as a real, explicit
gap in `docs/handoffs/XM-INV-CONSOLE-ASSERT.md`'s own "Not run"/Risk 3: a
`dry-run`/`apply` lifecycle CLI, `backend/cmd/identity-migrate`, that
rewrites the invoice system's one existing admin `invoice_users` row's
`oidc_issuer`/`oidc_subject` from Keycloak's values to the console
assertion's, so that once `CONSOLE_ASSERTION_ENABLED=true` and an operator
completes a real console → embedded-admin login, `auth.ResolveOrCreate`
(keyed exclusively on `(oidc_issuer, oidc_subject)`) finds and reuses the
admin's existing historical row instead of minting an orphaned second
identity with no prior audit/ticket history attached to it. Mirrors
`cmd/eligibility-repair`'s CLI shape exactly, as instructed: absolute-path
flags, a one-line secret-file reader, `migrate.Verify` before touching any
data, one all-or-nothing transaction, a printed summary, `--dry-run` as the
default.

### Why this lives in `internal/auth`, not `internal/postgresstore`

`eligibility-repair` calls into `postgresstore.Store`, but `invoice_users`'
login-boundary columns (`oidc_issuer`/`oidc_subject`/`status`) and
`auth_sessions`/`audit_events` for that boundary are otherwise managed
directly with raw SQL inside `internal/auth` (`identity.go`, `postgres.go`)
-- `postgresstore.Store` touches the *same* `invoice_users` table but a
different column set (`email_ciphertext`/`email_verified` for the business
Sub2API/NewAPI identity and its email-verification flow, via
`application.Service.EnsureUser`/`EnsureUserAndSyncOIDCEmail`), not the
admin login path. `MigrateOIDCBinding` (`backend/internal/auth/
identity_migrate.go`) therefore lives in `internal/auth`, next to
`ResolveOrCreate`/`PostgresSessionStore`/`insertSecurityAudit`, which is
where every primitive it needs (`sha256Hex`, `validUUIDString`,
`validateExactHTTPSURL`, the session-revocation SQL shape, the audit-event
insert helper) already exists. `internal/auth`'s own `doc.go` describes the
package as "the invoice service's independent identity boundary" and it
deliberately never imports the much larger `application` package (which
does import `auth`, in a test file only, but importing the other direction
in production code would be a real layering violation) -- see the next
section for the one place this constrains the implementation.

### A real correctness gap the literal task brief did not mention: email ciphertext re-encryption

`invoice_users.email_ciphertext` (business/OIDC-synced email, unrelated to
the console assertion's own claims, which carry no email at all -- see CR-
0006's claim table) is encrypted under `userEmailAAD(issuer, subject)`
(`application/crypto.go`, called once, at write time, from
`application.Service.EnsureUser`) -- an AEAD authenticated-additional-data
string keyed on the row's **current** `(oidc_issuer, oidc_subject)` pair. If
this migration rewrote only the two identity columns and left
`email_ciphertext` untouched, that ciphertext would become **permanently
undecryptable** the moment the row's issuer/subject change: any future
decrypt attempt would need to supply the *old* AAD to succeed, but nothing
in this codebase retains the old pair once the columns are overwritten.

I traced every call site of `userEmailAAD`/`invoice_users.email_ciphertext`
and confirmed nothing in this codebase currently *decrypts* that column
(only `application.Service.EnsureUser` ever encrypts into it; the
`application/crypto.go` decrypt helper of the same name is for
`invoice_profiles.email_ciphertext`, a different table under a different
AAD) -- so today this is a *latent* data-hygiene gap, not a live bug. But it
is exactly the kind of thing a "one-time, human-approved rewrite" tool
should get right the first time, since there will be no second chance to
recover the old AAD once the row is overwritten. `MigrateOIDCBinding`
therefore decrypts any existing `email_ciphertext` under the **old**
`(from-issuer, from-subject)` AAD and, if present, re-encrypts it under the
**new** `(to-issuer, to-subject)` AAD inside the same transaction as the
issuer/subject rewrite -- so the row's encrypted state stays internally
consistent with itself no matter what future code eventually reads it. If
decryption of an existing ciphertext ever fails (corruption, wrong keyring),
apply refuses outright rather than silently carrying forward unreadable
bytes.

This was not in the task brief's literal scope (which said "updating
oidc_issuer/oidc_subject") -- flagging it here explicitly, at implementation
time, rather than only in a later self-audit, per this project's own
"flag deviations inline" practice.

## CLI contract

```
identity-migrate \
  --database-url-file=/path/to/db-url-secret \
  --field-keyring-file=/path/to/keyring.json \
  --migrations-dir=/app/migrations \
  --from-issuer=<current oidc_issuer, exact HTTPS URL> \
  --from-subject=<current oidc_subject, UUID> \
  --to-issuer=<new oidc_issuer, exact HTTPS URL> \
  --to-subject=<new oidc_subject, UUID> \
  [--apply --operator-id=<admin-uuid>]
```

Defaults to a dry run (no `--apply`). `--apply` requires `--operator-id` to
be a well-formed UUID (same `errors.New("a valid operator UUID is
required to apply")` convention `postgresstore.RepairPreAnchorUsageEligibility`
already uses). Both `--from-issuer`/`--to-issuer` are validated with the
exact same `validateExactHTTPSURL` call `auth.ResolveOrCreate` itself uses
(absolute HTTPS, no userinfo/fragment/query/wildcard-host/control-chars, no
implicit trailing-slash normalization -- a typo is caught, not silently
"fixed"); both subjects must be well-formed UUIDs (`validUUIDString`,
matching CR-0006's own claim table: Keycloak's `sub` and the console's
`core.staff_account.id` are both UUIDs). Every one of these checks is
factored into a standalone, database-free `validateIdentityMigrationInput`
so it has its own unit test independent of PostgreSQL.

### Guards (dry run and apply validate identically; only apply mutates)

- 0 rows match `(--from-issuer, --from-subject)` -> refused, **unless** a
  row already exists at `(--to-issuer, --to-subject)` whose own audit trail
  proves *this exact* prior migration already ran (see Idempotency below),
  in which case the result is `AlreadyMigrated`, not a refusal.
- More than 1 row matches `(--from-issuer, --from-subject)` -> refused. (The
  schema's own `UNIQUE(oidc_issuer, oidc_subject)` index makes this
  unreachable in practice; the guard exists anyway because the task brief
  asked for it explicitly and it costs nothing.)
- The matched row's `status <> 'active'` -> refused.
- A row already exists at `(--to-issuer, --to-subject)` that is a genuinely
  different identity (not the same row migrated by an earlier run of this
  tool) -> refused, both when the source row was found (a hard conflict)
  and when it was not (see Idempotency below, "not a match" branch).
- `--from-issuer`/`--to-issuer` not exact HTTPS, or `--from-subject`/
  `--to-subject` not a UUID, or `--from`/`--to` name the identical identity
  -> refused before any database query runs.

### Apply

One transaction, `FOR UPDATE`-locked on both the source and target rows for
its duration (same `pgx.Serializable` isolation level
`postgresstore.RepairPreAnchorUsageEligibility` uses): rewrites
`oidc_issuer`/`oidc_subject` (re-asserting the full original `WHERE
id=... AND oidc_issuer=... AND oidc_subject=...` predicate, so a
`RowsAffected()<>1` -- the row changed concurrently -- aborts the whole
transaction with nothing written, same discipline
`eligibility-repair`/`RepairPreAnchorUsageEligibility` uses); re-encrypts
`email_ciphertext` if present (see above); invalidates every currently-live
(`revoked_at IS NULL`) `auth_sessions` row for that user (an
already-revoked session's `revoked_reason` is left untouched -- verified by
an integration test); writes one `identity.oidc_binding.migrated` audit row
(`actor_type=admin`, `actor_id=<operator id>`, `before_hash`/`after_hash` =
`sha256Hex(issuer+"\n"+subject)` for the old/new identity -- the exact same
hashing convention `Principal.IdentityHash()` already uses elsewhere in this
package).

### Idempotency

A second `--apply` with the identical four identity flags finds 0 rows at
`(--from-issuer, --from-subject)` (the row was already rewritten to the
`--to-*` pair) and then finds exactly 1 row at `(--to-issuer,
--to-subject)`; it checks that row's own audit trail for an
`identity.oidc_binding.migrated` event whose `before_hash`/`after_hash`
exactly match this invocation's `--from`/`--to` pair. If found: reports
`AlreadyMigrated`, changes nothing, exits 0 -- this is a normal, expected
outcome for a repeat run, not a failure. If a row exists at the target pair
but *without* that specific audit trail (e.g. it was created some other
way), the tool refuses rather than guessing -- distinguished from the
"already migrated by this tool" case with its own, more specific error
text.

### Dry-run/printed summary shape

Both modes print the identical block (only the banner and closing line
differ):

```
identity-migrate XM-INV-IDENTITY-MIGRATE: DRY RUN (nothing was changed)

from: https://auth.solov.cc/realms/solov / cd680af8-...
to:   https://console.solov.cc / <core.staff_account.id>

user_id:             99ed401b-e78a-4883-b9bf-f4cb4ba1cf17
status:              active
email:               a***b
created_at:          2026-01-01T00:00:00Z
auth_sessions_total: 3
auth_sessions_live:  1
audit_rows:          12

planned: rewrite oidc_issuer/oidc_subject, invalidate 1 live auth_sessions row(s), write identity.oidc_binding.migrated audit row.
```

`APPLIED` prints "invalidated N live auth_sessions row(s); wrote
identity.oidc_binding.migrated audit row." instead of the `planned:` line;
`ALREADY MIGRATED (nothing was changed)` prints "this identity was already
migrated to --to-issuer/--to-subject; no changes made." Subject values are
printed in full (not masked) -- they are opaque IdP-assigned UUIDs, not
personal data, and the whole point of a dry run is letting an operator
visually confirm the exact before/after pair before approving `--apply`.
Only the email is masked (`a***b`, or `(no email on file)` when
`email_ciphertext` is NULL), matching `httpapi.maskedEmailName`'s existing
convention byte-for-byte (duplicated locally -- see the code comment on
`maskIdentityEmail` for why `internal/auth` cannot import `internal/httpapi`
or `internal/application` to reuse either existing helper directly).

## Production runbook

**Do not run this anywhere but the test database from this task.** The
following is the intended production sequence for whoever runs it later,
once this branch and XM-INVCON1 (the platform-side signing endpoint) are
both released and CR-0006's phase-1 rollout is actually being executed --
see `docs/handoffs/XM-INV-CONSOLE-ASSERT.md`'s own "Production rollout"
section for the full six-step sequence this is step 4 of.

### 1. Obtain the console's `sub` value: query `core.staff_account` on the platform side

The assertion's `sub` claim (this tool's `--to-subject`) is the admin
operator's `core.staff_account.id` in the **xingmang-platform** database (a
separate database/repo from this one -- ADR-018's read-only boundary
applies; this is a manual, human-run lookup for the runbook, not a new
cross-system dependency this tool introduces). On the platform side:

```sql
SELECT id, username, roles
FROM core.staff_account
WHERE username = '<the admin operator's console username>';
```

`id` is `--to-subject`. `--to-issuer` is whatever `CONSOLE_ASSERTION_ISSUER`
is configured as in this environment's `deploy/.env.production` (e.g.
`https://console.solov.cc`) -- it must be byte-identical to that env var,
since that is the exact string the real assertion's own `iss` claim (and
therefore what `auth.ResolveOrCreate` will actually look up) will carry.

### 2. Obtain the current Keycloak identity: query `invoice_users` on this side

```sql
SELECT id, oidc_issuer, oidc_subject, status
FROM invoice_users
WHERE status = 'active';
```

CR-0006's own design document (§ Background fact 3) records a previously
observed value for this row (`id=99ed401b-e78a-4883-b9bf-f4cb4ba1cf17`,
Keycloak account `1187166666@qq.com`, subject beginning `cd680af8`) but
explicitly states that document is **not the authoritative source** and the
value must be re-queried against the live database immediately before
running this tool -- repeating that same caveat here rather than trusting
either document's snapshot. `oidc_issuer`/`oidc_subject` from this query are
`--from-issuer`/`--from-subject`.

### 3. Dry run, then apply, via the tools image (no compose service defined -- ad hoc `docker run`, same shape as `eligibility-repair`)

Neither `eligibility-repair` nor `identity-migrate` has a
`deploy/docker-compose.prod.yml` service entry (confirmed by grep: the only
`profiles: ["tools"]` services are `migrate`, `bootstrap-settings`,
`bootstrap-sources`, `document-gc`, `oidc-logout-retention`,
`oidc-preflight` -- `scripts/verify.ps1` only asserts those six by name).
Both are one-off, rarely-run lifecycle tools invoked directly against the
already-built `invoice-system-tools` image, reusing the exact secret files
and network the compose-managed tools already mount
(`deploy/docker-compose.prod.yml`'s `invoice_db` network resolves to
`invoice-system-prod_invoice_db` under this file's own top-level `name:
invoice-system-prod`; the owner DSN and field keyring are the same
`${SECRETS_DIR}/invoice_owner_database_url` and
`${SECRETS_DIR}/invoice_field_keyring.json` host files the `migrate`/`api`
services already use, at the same `/run/secrets/...` in-container paths):

```bash
docker run --rm --pull=never \
  --user 10001:10001 \
  --network invoice-system-prod_invoice_db \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -v "$SECRETS_DIR/invoice_owner_database_url:/run/secrets/invoice_owner_database_url:ro" \
  -v "$SECRETS_DIR/invoice_field_keyring.json:/run/secrets/invoice_field_keyring:ro" \
  "invoice-system-tools:$INVOICE_IMAGE_TAG" \
  /usr/local/bin/invoice-identity-migrate \
  --database-url-file /run/secrets/invoice_owner_database_url \
  --field-keyring-file /run/secrets/invoice_field_keyring \
  --migrations-dir /app/migrations \
  --from-issuer "https://auth.solov.cc/realms/solov" \
  --from-subject "<value from step 2>" \
  --to-issuer "https://console.solov.cc" \
  --to-subject "<value from step 1>"
```

Review the dry-run output (row identity, session/audit counts, planned
change) with whoever is authorizing the change. Then re-run with `--apply
--operator-id=<the approving operator's admin UUID>` appended. Confirm the
printed summary shows `APPLIED` and the expected session-invalidation count;
re-run the dry run once more afterward and confirm it now reports `ALREADY
MIGRATED`.

### 4. Ordering constraint, and a flagged discrepancy with the CONSOLE-ASSERT handoff's own step order

The task brief for this tool states the ordering constraint as: run this
migration **only after** `CONSOLE_ASSERTION_ENABLED` is turned on, and
**before** `OIDC_ADMIN_LOGIN_ENABLED` is ever turned off. I want to flag,
explicitly, that this is the **opposite order** from what
`docs/handoffs/XM-INV-CONSOLE-ASSERT.md`'s own "Production rollout" section
recommends (its step 4 says to run this migration *before* flipping
`CONSOLE_ASSERTION_ENABLED=true`).

Having worked through both orderings, I believe **the task brief's order is
the safer one**, and the earlier handoff's suggested order has a real gap:

- **Migrate-then-enable** (the earlier handoff's order): the instant this
  tool applies, `invoice_users`' one active-admin row stops matching
  Keycloak's `(issuer, subject)`. But `CONSOLE_ASSERTION_ENABLED` is still
  `false` at that point (it has not been flipped yet), so the console
  assertion exchange endpoint answers `503 CONSOLE_ASSERTION_DISABLED`.
  `OIDC_ADMIN_LOGIN_ENABLED` is still `true` throughout phase 1 (by design),
  so Keycloak OIDC login is the *only* currently-working path -- but
  `auth.ResolveOrCreate` no longer finds a row at Keycloak's pair, so a
  login attempt during this window does not fail closed, it **creates a
  brand-new orphaned identity for the Keycloak login itself** -- exactly the
  failure mode this whole change item exists to prevent, just on the
  opposite login path. There is a real window, however short, where the
  admin cannot log in via *either* mechanism without side effects.
- **Enable-then-migrate** (the task brief's order, and what this runbook
  documents above): after deploying with `CONSOLE_ASSERTION_ENABLED=true`
  but before this tool has run, the row still matches Keycloak's pair, so
  the OIDC path keeps working completely normally, unaffected. The only
  risk window is: if someone attempts a **console assertion** login before
  this tool runs, `ResolveOrCreate` would not find a row at the console's
  pair either, and would create the same kind of orphan -- but the
  console-assertion login path is brand new and not yet exercised by
  anyone's habitual workflow, so this is a substantially narrower, easier-
  to-control risk than breaking the *existing, currently-relied-upon*
  Keycloak path.

Recommendation for whoever actually runs this in production: follow this
runbook's order (flip `CONSOLE_ASSERTION_ENABLED=true` first, run this
tool immediately after in the same maintenance window, and **do not have
the admin operator attempt a console → embedded-admin login until this
tool's apply step has completed** -- closing the narrower risk window by
procedure, not just by flag state). I have not corrected
`XM-INV-CONSOLE-ASSERT.md`'s own text since that is a different commit
history / already-delivered handoff on a different branch; flagging it here
so whoever plans the actual rollout sees both documents and the
reasoning, and can decide whether that earlier handoff's step 4 should be
amended.

## Files changed

- `backend/internal/auth/identity_migrate.go` -- **new**. `MigrateOIDCBinding`,
  `validateIdentityMigrationInput`, candidate/result types, the email-AAD
  re-encryption, the audit-trail idempotency check.
- `backend/internal/auth/identity_migrate_test.go` -- **new**, unit tests for
  `validateIdentityMigrationInput` (every refusal case from the CLI contract
  above, table-driven) and `maskIdentityEmail`. No database required.
- `backend/internal/auth/identity_migrate_integration_test.go` -- **new**,
  5 integration tests against real PostgreSQL: no-match refusal,
  not-active refusal, target-exists refusal, the full apply happy path
  (session invalidation, email re-encryption, audit row), and idempotency
  (second apply + a dry run after the fact both report `AlreadyMigrated`).
- `backend/cmd/identity-migrate/main.go` -- **new**. Flag parsing, secret-file
  reading, `migrate.Verify`, `printSummary`. Mirrors
  `cmd/eligibility-repair/main.go` line for line where the shape is
  identical.
- `backend/cmd/identity-migrate/main_test.go` -- **new**, 5 test functions:
  CLI wiring smoke tests (refused-empty-database, apply-without-operator-id)
  plus `printSummary`'s three banner variants.
- `backend/Dockerfile` -- added the `identity-migrate` build step and its
  binary to the `tools` stage's `COPY` list, inserted *before*
  `/out/invoice-oidc-preflight` (not after) so the literal substring
  `scripts/verify.ps1` asserts (`/out/invoice-oidc-preflight
  /usr/local/bin/`) stays intact -- confirmed by grep after the edit.

**Not touched:** `deploy/docker-compose.prod.yml` (no compose service for
this tool, matching `eligibility-repair`'s own precedent -- see "Production
runbook" above), any other compose file, `release/`, `RELEASE-READINESS.md`,
`docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`docs/superpowers/`, any release-identity file, `scripts/release-image-
gate-lib.ps1`, any migration file (no schema change -- every table this tool
touches already exists), `web/`, any file outside this slice's stated scope.

## Tests run

From `backend/`, proxy env vars unset (this repo's known Windows/httptest
quirk), `INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable`
(the shared default, which `internal/testdb` automatically rewrites to a
per-worktree database on first use, per this worktree's own isolation):

```
go build ./...                                              # clean, all packages
go vet ./...                                                 # clean
go test -p 1 -count=1 ./...                                  # 27 test-bearing packages: ok (plus packages with no test files)
"$(go env GOROOT)/bin/gofmt" -l <every file this slice touched>   # clean (repo-wide gofmt -l flags nearly every
                                                                   # pre-existing file due to this repo's CRLF line-
                                                                   # ending baseline -- not this slice's concern;
                                                                   # every file this slice added or edited individually
                                                                   # gofmt-clean)
```

No loopback flakes observed on this run; ran the full suite twice across
this session (once before, once after a documentation-only follow-up
commit) with identical results both times.

New tests specifically: 7 functions in `identity_migrate_test.go`
(`TestValidateIdentityMigrationInput` with 17 sub-tests covering every
guard, plus `TestMaskIdentityEmail`), 5 functions in
`identity_migrate_integration_test.go`, 5 functions in `cmd/identity-
migrate/main_test.go` (see "Files changed" above for what each covers).

```
pwsh -NoProfile -File scripts/test-release-image-gate.ps1   # PASS (offline/static fixtures)
```

gitleaks (from a plain clone of this worktree, since a worktree's `.git` is
a file some scanning setups cannot resolve -- same precedent
`XM-INV-CONSOLE-ASSERT.md` recorded -- `git clone --no-local --branch
ai/claude/XM-INV-IDENTITY-MIGRATE --single-branch <worktree> <tmp>`, then a
locally-installed `gitleaks.exe` (v8.30.1) `detect --source=<tmp>
--log-opts=c91963d..HEAD --verbose --redact=0`): **PASS, no leaks found**
across both commits (~42KB scanned). No credential-shaped literal appears
anywhere in this slice; every test-fixture keyring/UUID/hash is either a
fixed non-secret test constant or generated at test-run time.

## Not run / not verified

- **`scripts/verify.ps1`** (the broader script whose Dockerfile-substring
  assertion this slice specifically had to preserve) was **not** run in
  full -- it also runs ClamAV healthcheck tests, PostgreSQL-15 compatibility
  fixtures, and other checks unrelated to this slice's scope, none of which
  this task's gate list named. I confirmed by direct inspection (`grep -n
  '/out/invoice-oidc-preflight /usr/local/bin/' backend/Dockerfile`) that
  the one assertion this slice's change could plausibly break still holds
  after the edit. `scripts/test-release-image-gate.ps1`, which the task's
  gate list *did* name, was run and passes (see "Tests run").
- **No real production/server contact of any kind.** No `release/`,
  `RELEASE-READINESS.md`, `docs/PRODUCTION-RUNBOOK.md`,
  `docs/IMAGE-SCAN-REVIEW.md` touched; no RC advancement; no tag created.
- **No real console/platform counterpart to migrate against.** Same
  boundary `XM-INV-CONSOLE-ASSERT.md` already recorded: XM-INVCON1 (the
  platform-side signing endpoint) was not confirmed live as of this commit,
  so `--to-issuer`/`--to-subject` in this handoff's runbook example are
  illustrative, not values ever exercised against a real console-issued
  assertion.
- **The `>1 rows matched` guard's true-positive branch is untestable**
  against this schema: `invoice_users`' own `UNIQUE(oidc_issuer,
  oidc_subject)` index makes inserting two rows at the same pair a
  constraint violation, so no integration test exercises that specific
  refusal path directly (the guard's *code* is exercised by every other
  test that calls `queryIdentityMigrationCandidates`, just never with
  `len(rows)>1`). Documented here rather than silently omitted.

## Risks / things to sign off on

1. **The ordering-constraint discrepancy with `XM-INV-CONSOLE-ASSERT.md`'s
   own rollout step 4** -- see "Production runbook" § 4 above for the full
   reasoning. I believe this slice's brief (enable the flag, then migrate)
   is the safer order and documented it as such, but the two documents now
   disagree and someone should decide whether the earlier handoff's text
   gets amended before this is actually run in production.
2. **The email-ciphertext re-encryption is a proactive addition beyond the
   literal task brief** (which only asked for `oidc_issuer`/`oidc_subject`).
   I judged it necessary because leaving it out would silently and
   irreversibly orphan that ciphertext's AAD the moment the row's identity
   changes -- see the dedicated section above. Please confirm this was the
   right call; the alternative (documented but not implemented) would be to
   instead just `NULL` out `email_ciphertext`/`email_verified` on migration,
   which is simpler but discards the (currently never-read, per my trace)
   email value rather than preserving it.
3. **Never verified against a real signer, real console, or real
   production data.** Everything here is proven correct against fixture
   data and this task's own test database.
4. Ran directly, single agent, no sub-agents or forks dispatched for
   research or implementation. No server/production contact of any kind.

## Follow-ups (recommended, not blocking this slice's delivery)

1. Resolve the ordering-constraint discrepancy noted in Risk 1 --
   ideally before anyone actually runs this in production, since the two
   documents currently contradict each other.
2. Once XM-INVCON1 lands and issues a real assertion, re-verify this tool's
   `--to-issuer`/`--to-subject` inputs against a real `core.staff_account`
   row and a real console-issued `sub`, not just the illustrative values in
   this runbook.
3. Wire `docker-compose.prod.yml`'s `environment:`/volume plumbing for
   `CONSOLE_ASSERTION_*` (already tracked as a follow-up in
   `XM-INV-CONSOLE-ASSERT.md`'s own "Production rollout" step 3, not
   duplicated here) -- unrelated to this tool directly but on the same
   critical path before step 5 (canary) of that handoff's rollout sequence.
4. Consider whether `docs/SECURITY-ARCHITECTURE.md` should record this
   lifecycle tool's existence, mirroring its treatment of
   `eligibility-repair` and other Platform Lifecycle Operations, if that
   document tracks those. Not checked here since it was not in this task's
   explicit scope.
