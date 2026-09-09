# XM-INV-LEDGER-ACCOUNT-EMAIL: label operator lists with the account's verified email

- **status:** implemented on the release branch and gated locally (backend
  build/vet, full `go test -p 1 -count=1 ./...` against real PostgreSQL, web
  typecheck, 166 tests across 12 files, production build). Not yet released.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line).
- **asked for by the product owner**, 2026-09-04, looking at the embedded
  admin console: the 用户账本 and 资格冻结队列 lists identify an account only
  by its upstream numeric ID (12, 34, 1147, 2092 …), which tells a human
  nothing about who the account belongs to.

## Where the address comes from

`verified_emails` — the same row `GetCurrentUser` reads to show a user their
own address, i.e. an address that was actually verified, not one typed into a
profile. It is stored encrypted, bound by AAD to
`(invoice_user_id, normalized_email_hmac)`.

An account can legitimately have **no** verified email: it was provisioned
through a login path that never verified one. In production today 7 of 8 bound
accounts have one. That absence is a normal state, not an error, and the whole
change is built around saying so honestly.

## Change

**Store.** New `ListAccountVerifiedEmails(ctx, externalAccountIDs)` returns the
latest non-revoked verified email per external account, as ciphertext — the
store never holds the keyring. Accounts without one are simply absent from the
result.

This is deliberately a **second, separate query** rather than a join inside the
ledger and freeze selects. Those two carry keyset cursors, a computed
`block_state` sort rank and a `FOR UPDATE` lock path; widening them for a
display-only column would put all of that at risk for no gain. One extra round
trip per page (at most 100 accounts) is the cheaper trade.

**Application.** `Service.accountEmails` opens the ciphertext with the AAD the
verified-email path already uses and returns a map keyed by external account.
`ListAccountLedgerPage`, `GetAccountLedgerDetail` and
`ListEligibilityFreezesPage` stop being bare passthroughs and fill
`AccountEmail` on each row. A ciphertext that fails to open is a genuine
keyring/data fault and fails the request closed, the same posture
`decryptProfile` and `GetCurrentUser` already take; an account with no address
is not a fault and yields an empty field.

**HTTP.** Both DTOs gain `account_email`, **present only when non-empty**. An
absent key is the honest wire shape for "no address on file" and stops an
operator reading an empty string as an empty mailbox. The field is additive to
CR-0009's set and identifies nothing on its own: `external_user_id` remains the
identifier every filter and cursor uses.

**Web.** `AccountEmailLine` renders the address under the numeric ID in both
tables and in the freeze detail, and renders **nothing** when there is none —
no `未知` placeholder, which would read as a failed lookup rather than an
absent address. The client validates the optional field with the same bound
the other free-text fields get (non-empty, ≤320 chars, no control characters)
and deliberately does not pattern-match the address itself: the server already
canonicalized it, and a stricter client rule could only reject a legitimate
address.

No migration. No change to any filter, cursor, sort or authorization path.

## Privacy posture

Showing the address to an operator is consistent with what this console
already does: `eligibilityFreezeDTO` documents why the upstream user ID is
deliberately unmasked here, and the admin request list already returns the
requester's profile email in the clear. The masking in
`ListExternalAccounts` applies to the **self-service** endpoint, a different
audience. The embedded admin console is reachable only by a console
administrator holding a fresh MFA session.

## Tests

- `TestAccountEmailsResolvesVerifiedAddressPerAccount` (application,
  integration, real PostgreSQL): an account with a verified address resolves to
  it; an account whose user has none is absent from the map rather than an
  error or an empty entry; an unknown account id is not an error; an empty
  input never touches the database. This runs against a real database on
  purpose — the AAD is exactly the kind of detail a hand-written fake would get
  wrong without failing.
- `TestAccountLedgerListHandlerEmitsAccountEmailOnlyWhenPresent` (httpapi): the
  key carries the address for one row and is **absent entirely** for the other.
- `web/src/lib/http-api.account-email.test.ts` (6 cases): both lists map the
  address when sent, leave it `undefined` when the key is absent, and reject a
  malformed one (empty string; embedded control character) with each endpoint's
  own error code.
- The mock API gives most accounts an address and deliberately leaves one
  without, so mock mode exercises both branches of `AccountEmailLine`.

## Gates

`go build ./...`, `go vet ./...`, `go test -p 1 -count=1 ./...` all exit 0
against real PostgreSQL; `npm run typecheck` exit 0; `npm test -- --run` 12
files / 166 tests passed; `npm run build` exit 0; `gofmt` clean on every
changed Go file (checked against CRLF-stripped copies, per this repository's
autocrlf convention).
