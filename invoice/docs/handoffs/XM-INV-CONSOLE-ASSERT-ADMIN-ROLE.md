# XM-INV-CONSOLE-ASSERT-ADMIN-ROLE: configure the console administrator role apart from the OIDC realm role

- **status:** implemented on the release branch, gated locally, and shipped
  as part of RC84. Two role vocabularies now exist side by side and the
  exchange endpoint reads the one that belongs to the issuer it is verifying.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line), based on
  `702990f` (RC83, in production).
- **supersedes nothing:** `XM-INV-CONSOLE-ASSERT`,
  `XM-INV-ASSERT-HANDSHAKE` and `XM-INV-ASSERT-ORIGIN` all stay in force;
  this is the third and last mismatch found between the two repositories'
  halves of the CR-0006 contract.

## Production finding (2026-09-03 16:38Z, third canary after RC83)

The handshake fires (`XM-INV-ASSERT-HANDSHAKE`) and the same-origin redeem
is accepted (`XM-INV-ASSERT-ORIGIN`), so the exchange endpoint finally
reached claim verification with a real console assertion. Every attempt was
then rejected, five in a row, audit action `auth.console_assertion.rejected`:

```
assertion_invalid: console assertion is invalid, expired, or already used:
roles does not include the configured administrator role
```

**Cause: two systems, two role vocabularies, one configured name.** The
console signs `core.staff_account.roles` verbatim -- the frozen design spec
says so in its claim table ("直传 `core.staff_account.roles`，供开票侧
`AdminPolicy.Role` 判定"), and the production operator's roles are
`{admin, credential-admin, staff}`. The invoice exchange endpoint passed
`a.Admin.Role` as the role to require, and `a.Admin.Role` is
`OIDC_ADMIN_ROLE` = `invoice-admin`, this deployment's **Keycloak realm**
role. No single configured value can satisfy both issuers while the
transitional OIDC login stays enabled, which is the entire point of
CR-0006 phase 2's dual-path period.

The same mismatch had a second, hidden half. Even with the check fixed,
`ConsolePrincipalFromClaims` copied the assertion's roles verbatim onto the
`Principal`, so the issued session would have carried
`{admin, credential-admin, staff}` into a system whose every admin check is
`slices.Contains(session.Roles, a.Admin.Role)` (`production_auth.go`
`requireAdmin` and the session-status handler). The exchange would have
succeeded and every admin route would still have refused the session it had
just issued.

## Fix

**1. The verified role comes from the console's own configuration.**
`ConsoleAssertionConfig` gains `AdminRole`, validated exactly like
`OIDCConfig.AdminRole` (non-empty, no surrounding whitespace, no control
characters, bounded length). `VerifyConsoleAssertion` no longer takes a
`requiredRole` argument at all -- it reads `cfg.AdminRole`. Dropping the
parameter is the point: the production defect was a caller passing a role
name from a different issuer's vocabulary, and a pure function that reads
its own config cannot be called that way.

**2. The issued principal speaks this system's vocabulary.**
`ConsolePrincipalFromClaims` takes the caller's currently configured
`AdminPolicy.Role` and makes it the principal's only role. This is the same
translation the function already performed for `acr`: the assertion carries
the console's fixed protocol constant and the principal records the invoice
policy's configured `RequiredACR`, because verification has already
established the equivalent fact. Roles now work the same way -- verification
has already established that the assertion carries the configured console
administrator role, so the principal records "invoice administrator" in the
only vocabulary this system has. The console's raw roles remain in the
verified `ConsoleAssertionClaims` for auditing.

## Configuration

| variable | meaning | default |
|---|---|---|
| `CONSOLE_ASSERTION_ADMIN_ROLE` | role the assertion's `roles` claim must contain | `OIDC_ADMIN_ROLE` |
| `OIDC_ADMIN_ROLE` | Keycloak realm role, and the role granted to every issued admin session | unchanged |

The default keeps every release before this one behaving identically: a
deployment that never sets the new variable requires, and grants, the same
single role it always did. Production sets `CONSOLE_ASSERTION_ADMIN_ROLE=admin`
(the console's staff role) while `OIDC_ADMIN_ROLE` stays `invoice-admin`.
`docker-compose.prod.yml` passes the variable with an empty default, so it is
optional and no existing `.env.production` becomes invalid. No migration.

## Tests

- `TestVerifyConsoleAssertionChecksTheConfiguredConsoleRole` (auth): an
  assertion carrying `{admin, credential-admin, staff}` verifies when the
  config names `admin`, and is rejected when the config names the realm role.
  The verified claims keep the console's own roles.
- `TestConsolePrincipalFromClaimsGrantsTheConfiguredAdminRole` (auth): the
  principal carries exactly the configured invoice role and satisfies
  `AdminPolicy.verifyClaims`.
- `TestConsoleAssertionExchangeRejectsTheOIDCRealmRole` (httpapi): an
  assertion carrying only `invoice-admin` is rejected with 401
  `ASSERTION_INVALID` and sets no cookies.
- The whole httpapi console-assertion suite now signs its fixture assertions
  with the console vocabulary `{admin, credential-admin, staff}` against an
  `AdminPolicy.Role` of `invoice-admin`, so the pre-existing tests --
  including the one asserting a fresh exchanged session passes an admin route
  and reports role `admin` -- exercise the translation end to end. Before
  this change that fixture signed `invoice-admin`, which is exactly why three
  release cycles of green tests never caught the mismatch.
- `TestLoadConsoleAssertionRuntimeConfigAdminRoleOverride` and
  `...RejectsInvalidAdminRole` (cmd/api), plus the default assertion added to
  the existing enabled-load test.

## Verification after deploy

1. Open the console's Sub2API "支付与财务 → 开票" tab as a signed-in operator.
   The embedded admin console must render signed in, with no login card.
2. Invoice `audit_events`: `auth.console_assertion.exchanged` rows appear
   (previously only `auth.console_assertion.rejected`).
3. The transitional OIDC admin login must keep working unchanged.
