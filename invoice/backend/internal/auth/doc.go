// Package auth implements the invoice service's independent identity boundary.
// It never validates or persists Sub2API/New API browser tokens.
//
// Browser integration order:
//
//  1. EnforceProductionAuthMode before opening a listener.
//  2. Construct OIDCClient with NewOIDCClient (client secret is _FILE-only).
//  3. On login, reuse the existing __Host-invoice_oidc_flow cookie value when
//     calling Begin, set the returned flow cookie, then issue a top-level 303 to
//     AuthorizationRequest.URL. Never render/log that URL.
//  4. On the exact callback path, pass code/state plus the flow cookie to
//     Callback. ResolveOrCreate the verified (issuer, subject), register only a
//     signed email_verified claim in encrypted persistence, then Issue a local
//     opaque session and clear the flow cookie.
//  5. Authenticate browser requests only with __Host-invoice_session. Return
//     the synchronizer CSRF token from an authenticated same-origin endpoint;
//     require CSRFPolicy.ValidateMutation on every unsafe method.
//  6. Begin administrator step-up as FlowAdminStepUp bound to the current
//     identity and session ID. After Callback, Rotate that exact session and
//     require AdminPolicy.AuthorizeSession for each privileged request.
//  7. Local logout must revoke the opaque session before returning the
//     discovery-derived RP-initiated logout URL. Register the signed OIDC
//     back-channel logout endpoint and atomically persist jti replay state,
//     revoke by provider sid (or issuer+subject), and append its audit event.
//
// Desktop integration is separate: ExtractDesktopBearer accepts one
// Authorization header only, and BearerVerifier validates a signed, short-lived
// JWT access token with exact API audience and desktop-client azp allowlists.
// Browser endpoints must not call this bearer path.
package auth
