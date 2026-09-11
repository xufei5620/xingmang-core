// Package staffauth defines the trusted same-process platform session boundary.
package staffauth

import (
	"net/http"
	"time"
)

// Identity is derived from the current authenticated platform staff session.
// CSRFToken is a domain-separated derived token, never the raw session token.
// Issuer and Subject preserve existing administrator audit identity keys.
type Identity struct {
	Issuer      string
	Subject     string
	DisplayName string
	Roles       []string
	MFAAt       *time.Time
	CSRFToken   string
}

// Resolver must verify session validity and invoice permissions on every call.
// Browser headers, invoice cookies, or request-body claims are not identities.
type Resolver func(*http.Request) (Identity, error)
