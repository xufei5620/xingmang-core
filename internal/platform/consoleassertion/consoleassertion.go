// Package consoleassertion implements CR-0006 XM-INVCON1: the platform-side
// half of "control console attests, invoice system trusts" login for the
// embedded invoice admin console (EmbeddedConsoleFrame). It signs short-lived
// (<=5 minute) Ed25519-signed JWS assertions proving "this operator's console
// session just passed password + TOTP", which the invoice system's own
// exchange endpoint (backend/internal/auth/console_assertion.go, a different
// repository/system, branch ai/claude/XM-INV-CONSOLE-ASSERT) turns into its
// own session -- this package never touches invoice data, invoice sessions,
// or any invoice database (ADR-018's read-only boundary between the two
// systems is unaffected: this is a new authentication handshake, not a data
// channel).
//
// Deliberately not an Action (internal/platform/action): CR-0006's own text
// reasons that issuing an assertion changes no platform state -- no row is
// written or updated, nothing is upserted -- it is "sign a short-lived proof
// from the current session", the same category as localauth.Login/LoginTOTP,
// neither of which has ever gone through action.Registry either. See
// docs/change-requests/CR-0006-console-auth-for-invoice-admin.md and its
// frozen technical specification,
// docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md
// (particularly §3 for the wire contract and §5.1 for the endpoint/error
// table this package implements).
package consoleassertion

import "time"

const (
	// DefaultAudience is the aud claim value both sides agree on when
	// XM_INVOICE_CONSOLE_ASSERTION_AUDIENCE is unset (spec §3.2, §3.4).
	DefaultAudience = "xingmang-console-assertion-v1"

	// TokenType is the JWS header's typ claim -- a dedicated value, not bare
	// "JWT", so this token can never be accepted by a generic JWT/at+jwt
	// verifier elsewhere in either system (spec §3.1).
	TokenType = "xm-console-assertion+jwt"

	// Algorithm is the only accepted signature algorithm. No negotiation:
	// signing always uses EdDSA (Ed25519); the invoice side's verifier
	// rejects any assertion whose header claims anything else.
	Algorithm = "EdDSA"

	// ACR is the fixed acr claim value that replaces what Keycloak used to
	// issue (spec §3.2). The invoice side's own session-issuance code
	// reconciles this against its currently configured OIDC_REQUIRED_ADMIN_ACR
	// -- nothing in this package needs to know that value.
	ACR = "xingmang-console-totp-v1"

	// KeyringPurpose / KeyringProtocol give this signing domain its own
	// namespace, mirroring internal/platform/jobs/fleet_keyring.go's
	// domain-separation discipline: a signature (or a manifest record) from
	// one purpose/protocol pair must never be mistakable for another.
	KeyringPurpose  = "console_admin_assertion_signing"
	KeyringProtocol = "xm-console-assertion-v1"

	// assertionTTL is the hard structural cap on exp-iat (spec §3.2: "严格
	// 上限 5 分钟...服务端拒绝任何声称更长有效期的断言"). Not configurable --
	// an env var able to raise it would undercut the one property the whole
	// design leans on: a stolen assertion's usable window is tiny.
	assertionTTL = 5 * time.Minute

	// nonceBytes is the random nonce length (spec §3.2: 32 bytes -> 43
	// base64url characters, doubling as the JWT jti).
	nonceBytes = 32
)
