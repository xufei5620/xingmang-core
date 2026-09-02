# XM-INV-CONSOLE-ASSERT-DEPLOY: wire console-assertion compose plumbing (CR-0006 phase 2, step 2)

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet` clean; `go test -p 1 -count=1 ./internal/auth/... ./cmd/api/...`
  passing with proxy env vars unset; `docker compose ... config` renders
  successfully against a throwaway env derived from
  `deploy/.env.production.example`; `scripts/test-release-image-gate.ps1`
  offline/static fixtures passing; gitleaks scan of the commit clean --
  see "Tests run"). Not deployed, no production or server contact of any
  kind, no release ceremony run, no RC advanced.
- **branch:** `ai/claude/XM-INV-CONSOLE-ASSERT-DEPLOY`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `4d21cc9` (production RC73 line),
  worktree `K:/发票/wt-XM-INV-ASSERT-DEPLOY`.
- **commits:**
  - `4683ff4` feat(deploy): wire console-assertion compose plumbing (CR-0006 phase 2 step 2)
  - *(this commit)* docs(handoff): add XM-INV-CONSOLE-ASSERT-DEPLOY handoff

## Summary

CR-0006 phase 2, step 2 of the platform repo's rollout plan
(`docs/superpowers/plans/2026-09-03-cr0006-phase2-rollout.md`): wires the
`docker-compose.prod.yml` plumbing that `XM-INV-CONSOLE-ASSERT` (already on
RC72/RC73 production) deliberately left undone, so console-assertion admin
login has an actual path from a reviewed public-key manifest on the host
into the running `api` container. **This ships dark**: both flags keep
their existing safe defaults (`OIDC_ADMIN_LOGIN_ENABLED` true,
`CONSOLE_ASSERTION_ENABLED` false), so production behavior is unchanged the
moment this lands -- but **the release that carries this branch is a
deploy-file-changing release, not a pure code release** (new required
compose variable, new bind mount; see Risks).

### a. Compose wiring (`deploy/docker-compose.prod.yml`)

The `api` service's `environment:` block gains, immediately after the
existing `OIDC_*` variables:

```yaml
OIDC_ADMIN_LOGIN_ENABLED: ${OIDC_ADMIN_LOGIN_ENABLED:-true}
CONSOLE_ASSERTION_ENABLED: ${CONSOLE_ASSERTION_ENABLED:-false}
CONSOLE_ASSERTION_ISSUER: ${CONSOLE_ASSERTION_ISSUER:-}
CONSOLE_ASSERTION_AUDIENCE: ${CONSOLE_ASSERTION_AUDIENCE:-xingmang-console-assertion-v1}
CONSOLE_ASSERTION_KEYS_FILE: /config/console-assertion-keyring.json
```

and its `volumes:` block gains, matching the existing
`SOURCE_TRUST_CONFIG_FILE:/config/source-trust.json:ro` pattern exactly
(dedicated host-side variable name, required with no default, fixed
container-side literal):

```yaml
- ${CONSOLE_ASSERTION_KEYRING_FILE:?set CONSOLE_ASSERTION_KEYRING_FILE}:/config/console-assertion-keyring.json:ro
```

This is the platform rollout plan's step 2 YAML verbatim. `deploy/
.env.production.example` documents all six variables (the five above plus
`CONSOLE_ASSERTION_KEYRING_FILE`, pointing at
`/root/invoice-system/config/console-assertion-keyring.json` -- the exact
host path the plan's step 1 installs the reviewed manifest to) and
`docs/PRODUCTION-RUNBOOK.md` gains section 4.1 with the full enable order,
manifest-custody notes, and a rollback bullet in section 12.

### b. The Docker empty-directory-on-missing-bind-mount-source guard

Docker materializes an empty **directory** (not a file) at a bind-mount
source path that does not exist when a container is first created against
it. Since `CONSOLE_ASSERTION_KEYRING_FILE`'s real target (the reviewed
keyring, generated in the platform rollout plan's step 1) is populated on
a separate, later timeline from this compose change landing, the very
first `docker compose ... up -d --no-build` that includes this branch
could easily run before that file exists -- silently turning the mount
into a directory, which would then require a full container recreate
(not just a restart) once the real file is later installed and someone
tries to flip `CONSOLE_ASSERTION_ENABLED` on.

Fix: `deploy/roll-forward.sh` gains a preflight, right after the existing
backup-freshness check and before any Compose invocation touches
`docker-compose.prod.yml` (including the migration one-shot, since Compose
interpolates every required variable in the file regardless of which
service is targeted): if `.env.production` sets
`CONSOLE_ASSERTION_KEYRING_FILE`, its parent directory must already exist
(refuse with exit 2 otherwise -- matching this script's existing
precondition-checking style), and if the file itself is still missing, an
empty placeholder is created (never overwriting a file that is already
there -- verified in isolation, see Tests run). This does not weaken the
fail-closed contract: the variable itself is still hard-required by
Compose with no default, and an **enabled** flag against an empty/missing
keys file still fails the process closed at startup (see part c) -- this
preflight only prevents the file/directory *type* footgun for the common
case where the flag stays off.

I chose the delegation message's option (a) (automated preflight in
`roll-forward.sh`) over option (b) (a committed placeholder file under
`deploy/config/`): no `deploy/config/` directory exists in this repo, and
every existing analogous host-config-file variable
(`SOURCE_TRUST_CONFIG_FILE`, `ADMIN_SETTINGS_BOOTSTRAP_FILE`,
`SOURCE_INSTANCES_CONFIG_FILE`) follows the same pattern: a required
host-managed path with a committed `.example.json` *template* elsewhere in
the repo, never a committed file actually sitting at the deployed path.
Option (a) matches that convention; option (b) would not have.

### c. Config-loading guard (`backend/cmd/api/runtime.go`)

Requirement 1 asked me to verify the keyring is only loaded when
`CONSOLE_ASSERTION_ENABLED=true`, adding a guard "if the api currently
loads the file eagerly." **It already did not** -- `buildProductionRuntime`
already wrapped the `CONSOLE_ASSERTION_ISSUER`/`_AUDIENCE`/`_KEYS_FILE`
reads and the keyring decode entirely inside `if consoleAssertionEnabled
{ ... }`, a pre-existing correct guard from `XM-INV-CONSOLE-ASSERT`. There
was no bug to fix. What I did instead: extracted that block into a new,
directly unit-testable function, `loadConsoleAssertionRuntimeConfig(enabled
bool) (*auth.ConsoleAssertionKeyring, auth.ConsoleAssertionConfig, error)`
-- same extraction pattern this file already uses for
`eligibilityProjectionReady` (XM-INV-READY-PENDING), since
`buildProductionRuntime` itself needs a live PostgreSQL connection and
cannot be unit tested directly. Four new tests in `runtime_test.go` prove:
disabled mode returns `(nil, ConsoleAssertionConfig{}, nil)` even with
garbage/absent env values and a nonexistent keys-file path (so the guard
regresses loudly if ever removed); enabled mode with a freshly-generated
Ed25519 test key loads correctly; enabled mode requires
`CONSOLE_ASSERTION_KEYS_FILE` to be set; and enabled mode against a
genuinely empty (0-byte) keys file -- the exact shape
`deploy/roll-forward.sh`'s placeholder produces -- still fails closed
(`auth.LoadConsoleAssertionKeyringJSON` already refuses zero keys by
design; this is the desired behavior, not a bug, since it forces an
operator to notice at startup if they enable the feature before the real
manifest is installed).

## Files changed

- `deploy/docker-compose.prod.yml` -- `api` service: five new environment
  entries, one new read-only volume mount.
- `deploy/roll-forward.sh` -- header precondition bullet; new preflight
  block creating an empty keyring placeholder only when missing.
- `deploy/.env.production.example` -- documents
  `CONSOLE_ASSERTION_KEYRING_FILE` (new), refreshes the now-stale "not
  wired into docker-compose.prod.yml yet" comments on the five variables
  that slice originally added.
- `docs/PRODUCTION-RUNBOOK.md` -- new section 4.1 (flags, manifest custody,
  Docker directory-vs-file footgun, enable order, rollback pointer); one
  new bullet in section 12 (Rollback).
- `docs/handoffs/XM-INV-CONSOLE-ASSERT.md` -- rollout step 3 and follow-up
  3 updated to point at this slice instead of describing the compose
  wiring as future work; corrects the host-side variable name from that
  doc's own speculated `CONSOLE_ASSERTION_KEYS_CONFIG_FILE` to the actual
  `CONSOLE_ASSERTION_KEYRING_FILE`.
- `backend/cmd/api/runtime.go` -- extracted
  `loadConsoleAssertionRuntimeConfig` (no behavior change).
- `backend/cmd/api/runtime_test.go` -- four new test functions plus a
  small Ed25519-keyring-file test helper.
- **Not touched:** `backend/Dockerfile`; `RELEASE-READINESS.md`; `docs/
  IMAGE-SCAN-REVIEW.md`; `scripts/release-image-gate-lib.ps1`; `scripts/
  verify-release-image-artifacts.ps1`; `scripts/test-release-image-gate.
  ps1`'s identity strings; `docs/PRODUCTION-RUNBOOK.md`'s existing
  release-identity lines (RC73 tag/hash literals) beyond the appended
  prose; `deploy/.env.example` (dev/mock-mode file -- see deviation 4
  below); any RC advancement; `contracts/`; frontend (`web/`).

## Deviations from the delegation message

Flagging every point where the chat delegation and the frozen rollout
plan document (or repo convention) diverged, as I hit each one, per this
session's own `flag-spec-deviations-inline` practice -- not held back for
a single end-of-task audit:

1. **Container-internal mount path.** The delegation message's own text
   named `/run/invoice/console-assertion-keyring.json` as an example,
   parenthetically deferring to "whatever the plan states." The rollout
   plan's step 2 states `/config/console-assertion-keyring.json`, which
   also matches this repo's own established convention
   (`SOURCE_TRUST_CONFIG_FILE:/config/source-trust.json:ro`). I
   implemented the plan/convention value. The env var *name*
   (`CONSOLE_ASSERTION_KEYS_FILE`) is unaffected -- only the path literal
   differs from the delegation message's example.
2. **Host-side compose variable name.** Not stated explicitly in the
   delegation message; I used `CONSOLE_ASSERTION_KEYRING_FILE`, the exact
   name the rollout plan's own step 2 YAML uses.
3. **Placeholder/manifest filename.** The delegation message's
   parenthetical example wrote `console-assertion-keyring.v1.json` for the
   host path. Both the rollout plan's step 1 (the `scp`/`install` target)
   and my implementation use `console-assertion-keyring.json` (no `.v1`)
   for the *deployed host path* -- the `.v1` suffix belongs only to the
   repository-internal contract file's name
   (`contracts/auth/console-assertion-keyring.v1.json`), not the runtime
   deployment path, matching the plan's own literal text.
4. **`.env.example` vs `.env.production.example`.** The delegation message
   named `deploy/.env.example` as one of two files to document the flags
   in. That file is explicitly scoped to "Local development only" (its own
   header) and mock-mode auth, which never exercises this compose wiring
   at all -- its existing console-assertion comment already correctly
   says so and needed no change. The content actually requested (manifest
   path, enable order, rollback) is production rollout material, so I
   updated `deploy/.env.production.example` instead, which also needed a
   correction regardless (it still said "not wired into docker-compose.
   prod.yml yet," which this slice makes false). Left `deploy/.env.example`
   untouched. For the same reason, the `docker compose ... config`
   validation gate used a throwaway copy of `.env.production.example` (it
   already satisfies every other `:?set ...` variable in the prod compose
   file, confirmed by a clean render), not `.env.example` (a dev-mode file
   that does not set most of those production-only required variables at
   all).
5. **Preflight vs. checklist-only.** The rollout plan's own step 2 text,
   after describing this exact same directory-vs-file risk, recommends
   handling it as a purely manual item on an "RC74 release checklist" --
   narrower than the delegation message's explicit request for an
   automated `deploy/roll-forward.sh` preflight (its option (a)). The two
   are not actually in conflict (the plan's suggestion is a fallback, not
   a prohibition on automating it), so I implemented the delegation
   message's option (a) as instructed and additionally kept the
   documentation trail the plan asked for (`docs/PRODUCTION-RUNBOOK.md`
   section 4.1), satisfying both.

A process note, separate from the above: mid-task I mistakenly ran the two
`docs/handoffs/XM-INV-CONSOLE-ASSERT.md` edits against
`K:/发票/wt-XM-INV-AUTOLOGIN` (the base worktree) instead of this task's
own `K:/发票/wt-XM-INV-ASSERT-DEPLOY` worktree, immediately noticed it,
and ran `git checkout -- docs/handoffs/XM-INV-CONSOLE-ASSERT.md` in the
AUTOLOGIN worktree before doing anything else there -- confirmed clean
(`git status --short` empty) before proceeding. The edits were then
redone correctly in this task's own worktree. No other file in the
AUTOLOGIN worktree was touched.

## Tests run

Backend (from `backend/`; proxy env vars unset per this repo's known
Windows/httptest quirk):

```
go build ./...                                                          # clean
go vet ./...                                                            # clean
go test -p 1 -count=1 ./internal/auth/... ./cmd/api/...                 # both packages: ok
  # internal/auth: 68 passed, 9 skipped (real-Postgres integration tests,
  # no INVOICE_TEST_DATABASE_URL configured in this shell -- pre-existing
  # boundary, not touched by this slice)
  # cmd/api: all tests pass, including the 4 new
  # TestLoadConsoleAssertionRuntimeConfig* functions
```

gofmt: the raw Windows working-tree copies of the two touched Go files
read as "needs formatting" under a naive `gofmt -l` because the checkout
is CRLF and `core.autocrlf=true` (a known repo-wide false positive, see
this session's own `windows-toolchain-quirks` notes) -- confirmed as pure
line-ending noise, not a real style issue, by extracting the actual
**staged** blob content (`git cat-file -p :<path>`, i.e. exactly what git
commits after its own autocrlf clean filter) to a temp file and running
`gofmt -l` against that: clean (exit 0, no output) for both files, same
as the pre-existing baseline blob. No `gofmt -w`/`go fmt` was run against
the working tree, since that would have force-converted the whole
CRLF file and produced a large unrelated diff.

Compose:

```
docker compose --env-file <throwaway copy of deploy/.env.production.example> \
  -f deploy/docker-compose.prod.yml config
# exit 0; rendered output confirmed both new env vars and the new volume
# mount (source: /root/invoice-system/config/console-assertion-keyring.json,
# target: /config/console-assertion-keyring.json) resolve exactly as
# designed. Throwaway env file was deleted immediately after, never committed.
```

PowerShell:

```
pwsh -NoProfile -File scripts/test-release-image-gate.ps1   # PASS (exit 0)
```

This caught one real issue during development: my first draft of the
`docs/PRODUCTION-RUNBOOK.md` prose contained the literal substring `up -d`
without `--no-build` on the same line, tripping this gate's line-scan
guard against documenting a Compose command that could rebuild outside
the release gate. Fixed by writing `up -d --no-build` in that sentence
(also more accurate, matching every actual invocation shown elsewhere in
the runbook).

`deploy/roll-forward.sh`: `bash -n` syntax-checked clean. The new
preflight block's shell logic (not the whole script -- that needs a real
release directory layout this task never had) was extracted and exercised
in isolation in the scratchpad against three cases: variable set with an
existing parent directory and a missing file (creates an empty
placeholder), the same case run a second time with a real file already in
place (confirmed **not** clobbered/truncated), and variable set but its
parent directory missing (refuses with exit 2). A fourth case, variable
unset entirely, confirmed as a no-op (Compose's own `:?set ...` on
`CONSOLE_ASSERTION_KEYRING_FILE` is the fail-closed backstop for that
case, not this preflight).

gitleaks (native binary, `/c/Users/58439/.local/bin/gitleaks`, no Docker
needed):

```
gitleaks git --no-banner --log-opts="4d21cc9..HEAD" .
# 1 commit scanned, ~17.92 KB, no leaks found
```

## Not run / not verified

- **No real `docker compose ... up -d` against the new mount.** Validated
  with `docker compose ... config` only (variable interpolation + volume
  resolution, both confirmed correct); an actual container start against
  a real or disposable host path was out of reach without production/
  server access. See Risks.
- **No real production or server contact of any kind**; no RC
  advancement; the release-identity files listed under "Files changed"
  were deliberately left untouched.
- **`deploy/roll-forward.sh` as a whole script was not executed** (it
  requires a real `/root/invoice-system/app/releases/<sha>/` layout,
  loaded release images, and a signed backup -- none of which exist in
  this environment); only the new preflight block's logic was isolated
  and tested, as described above.
- Frontend/`web/` not touched; no frontend tests applicable.

## Risks

1. **This makes the next release that carries this branch a
   deploy-file-changing release, not a pure code release.** New required
   compose variable (`CONSOLE_ASSERTION_KEYRING_FILE`), new bind mount.
   Whoever runs that release ceremony should review the rendered compose
   output (e.g. `docker compose config`) directly, not just the source
   diff, before signing.
2. **`.env.production` on the host must define
   `CONSOLE_ASSERTION_KEYRING_FILE` before the first `docker compose ...
   up -d --no-build` that includes this branch's compose file**, or every
   service in `docker-compose.prod.yml` (not just `api`) fails to start at
   all -- Compose interpolates every `${VAR:?...}` in the file up front,
   regardless of which service is targeted. This is intentional
   fail-closed behavior, not a defect, but it means the host's
   `.env.production` needs updating in lockstep with this deploy, before
   the first roll-forward that uses it (see `deploy/.env.production.
   example`'s new line for the value to add).
3. **The empty-directory bind-mount footgun and its preflight fix are
   both reasoned from documented Docker bind-mount semantics, not
   observed against a real mount** (no production host access this task).
   Recommend one real `docker compose ... up -d --no-build` dry run
   against a disposable host path -- confirming the mount resolves as a
   plain file, not a directory -- before the first production
   roll-forward that carries this compose change.
4. **The config-loading guard (requirement 1) needed no code fix, only an
   extraction for testability** -- please confirm this reading of
   `runtime.go`'s pre-existing `if consoleAssertionEnabled { ... }` gate
   is correct; "no bug found" should not be mistaken for "not checked."

## Follow-ups

1. Real end-to-end `docker compose up -d --no-build` dry run against the
   new mount before the first production roll-forward that carries this
   change (Risk 3).
2. Populate `.env.production` on the host with
   `CONSOLE_ASSERTION_KEYRING_FILE` before that same roll-forward (Risk
   2); coordinate with the platform rollout plan's step 1 (key
   generation and manifest install), since both target the same host
   path.
3. When this branch actually ships as a numbered RC, the six
   release-identity files and `docs/PRODUCTION-RUNBOOK.md`'s RC-specific
   literals need their own bump -- a separate ceremony, out of this
   slice's scope by instruction.
