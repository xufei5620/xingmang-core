# Invoice System

Independent invoice application for Sub2API and New API accounts. It does not
modify or embed either upstream source tree.

V1 product rules:

- ordinary invoices only;
- minimum request amount: CNY 200.00;
- fixed service item: `技术服务`;
- the issuing entity is referenced by a hidden server-side `issuer_code` and is
  never user-editable or displayed in the user application;
- individual and enterprise invoice profiles;
- enterprise name, tax ID, and verified delivery email are required;
- address, phone, bank name, and bank account are optional;
- source instances are financially isolated and cannot be combined;
- multiple orders can be combined and one order can be partially invoiced;
- Sub2API eligibility is based on CNY `pay_amount`, reduced by the exact
  proportional gateway-refund amount and frozen throughout refund states;
- New API top-ups require manual payment verification in V1;
- invoiceable amount is allocated only from real cash-backed service units that
  have actually been consumed after the atomic source cutover; bonuses,
  rebates, administrator credits and legacy balances are non-invoiceable;
- administrators issue invoices manually and upload PDF documents;
- email sends a notification only; users authenticate to download the PDF;
- user pages may be embedded, while sensitive administration uses a top-level
  page restricted by OIDC/MFA, RBAC, and a fixed source-IP allowlist.

Local development uses mock adapters. The production path is a separate OIDC,
PostgreSQL and outbound source-agent deployment; it still does not modify either
upstream source tree. No production server or upstream configuration is changed
by building or testing this repository.

## Local verification

```powershell
# Backend domain, concurrency and HTTP authorization tests
cd G:\xingmang\01-core\invoice\backend
go test -race ./...

# Start the milestone-1 mock API (headers identify local mock users only)
$env:AUTH_MODE = 'mock'
go run ./cmd/api

# User and administrator UI
cd G:\xingmang\01-core\invoice\web
npm install
npm run dev

# Assert that the upstream research trees were not modified
cd G:\xingmang\01-core\invoice
pwsh -NoProfile -File .\scripts\check-upstream-integrity.ps1

# Full local gates, including an isolated disposable PostgreSQL container
# and frontend HTTP-mode mock-header inspection
pwsh -NoProfile -File .\scripts\verify.ps1

# Run only the isolated PostgreSQL migration/concurrency gate
pwsh -NoProfile -File .\scripts\verify-postgres.ps1
```

Running `go test` directly against the shared long-lived PostgreSQL dev
container (rather than `scripts/verify-postgres.ps1`'s isolated one-off
container) with `INVOICE_TEST_DATABASE_URL` pointed at its default
`invoice_test` database is safe from multiple worktrees at once: every
integration test that resets the `public` schema (`backend/internal/
postgresstore`, `oidcretention`, `auth`, `adminsettings`, and one `migrate`
test -- see `backend/internal/testdb`'s package doc) automatically
redirects to a database named after the current git worktree
(`invoice_test_<sanitized name>_<16 hex SHA-256 characters of the cleaned full
worktree path>`), creating it on first use. Equal basenames and names that
sanitize identically retain distinct path identities; the readable part is
truncated to keep the database name within PostgreSQL's 63-byte limit.
Legacy basename-only databases are neither reused automatically nor deleted
or renamed; keep them until their owner reviews any separate cleanup. Moving
a checkout changes its derived database identity. Point
`INVOICE_TEST_DATABASE_URL` at any other, explicit
database name (e.g. `invoice_test_mytask`) to opt out of that redirection
and use exactly that database.

The backend now has separate mock and fail-closed production boot paths. The
production path includes central OIDC/PKCE sessions, administrator LoA2/MFA and
IP policy, PostgreSQL application services, encrypted sensitive fields and PDF
objects, transactional funding allocations, manual New API evidence review,
refund cases, reliable email/source outboxes and signed mTLS source ingestion.
It will not start with missing production secrets, unknown database migrations,
stale source streams, stale ClamAV signatures or an unavailable isolated,
pinned qpdf validator.

PDF uploads remain plaintext only in private tmpfs quarantines. After ClamAV,
the API streams at most 20 MiB over an authenticated Unix socket to a separate
qpdf 12.3.2 scanner container. That container has no network, database,
document volume or application secrets; it has only its read-only scanner
capability, socket volume and bounded tmpfs. Encryption, JavaScript/actions,
Launch/remote URI behavior, interactive forms/XFA, attachments and rich media
are rejected. Accepted PDFs are stored as chunk-authenticated AES-GCM
ciphertext. Email contains only an authenticated UI record link, never a PDF
attachment or bearer token.

Production backups require a write-freeze and atomically cover the database,
encrypted document archive, receiver indexes and all ten complete source state
directories, including pending encrypted spools. Every checksum manifest is
signed by an offline Ed25519 key under the fixed OpenSSH namespace; restore
verifies that signature before any decryption. The drill then admits only
bounded regular-file/directory tar members, restores into disposable
PostgreSQL, rejects unknown migration history and uses the offline field
keyring to decrypt and hash-check sample documents against database metadata.

`deploy/docker-compose.idp.yml` never mounts a bootstrap administrator secret.
Use the one-time `docker-compose.idp.bootstrap.yml` override only for initial
Keycloak setup, then delete/disable that account, remove the host secret and
restart from the base file. The full approval, secret ownership, SMTP, backup,
restore and canary procedure is in `docs/PRODUCTION-RUNBOOK.md`. These are
production-capable artifacts, not evidence that live credentials or the public
deployment have already passed the required canary.

The freeze queue, evidence-protected resolution gates, and user-safe consumed
cash summary are specified in `docs/ELIGIBILITY-OPERATIONS.md`.

The backend Dockerfile defaults to the minimal `api` target, which contains no
qpdf binary. qpdf and the scanner server exist only in the no-network `scanner`
target. Migration, bootstrap, key-generation, orphan-GC and
restore-verification binaries exist
only in the separate `tools` target/image. The build itself runs real qpdf
static/JavaScript/Launch/URI/attachment/encryption fixtures; an API image cannot
be produced if the pinned parser and policy are incompatible.
