package httpapi

import "net/http"

// ConsoleAssertionHandlers is the minimal interface for CR-0006/XM-INVCON1's
// assertion-issuance endpoint (*consoleassertion.Handlers satisfies it).
//
// httpapi does not import internal/platform/consoleassertion for the
// concrete type -- same assembly discipline as LocalAuthHandlers (see that
// interface's own comment): the wiring point is cmd/platform-api, gated on
// XM_INVOICE_CONSOLE_ASSERTION_ENABLED, and httpapi itself stays agnostic of
// how the signer or TOTP freshness check are implemented.
type ConsoleAssertionHandlers interface {
	Issue(w http.ResponseWriter, r *http.Request)
}
