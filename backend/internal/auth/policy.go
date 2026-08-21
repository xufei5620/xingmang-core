package auth

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type AdminPolicy struct {
	Role         string
	RequiredACR  string
	RequiredAMR  []string
	StepUpMaxAge time.Duration
	ClockSkew    time.Duration
}

func (p AdminPolicy) Validate() error {
	if strings.TrimSpace(p.Role) == "" || strings.TrimSpace(p.Role) != p.Role || strings.TrimSpace(p.RequiredACR) == "" || strings.TrimSpace(p.RequiredACR) != p.RequiredACR || len(p.RequiredAMR) == 0 {
		return errors.New("administrator policy requires role, ACR and AMR constraints")
	}
	for _, method := range p.RequiredAMR {
		if strings.TrimSpace(method) == "" || strings.TrimSpace(method) != method || hasControl(method) {
			return errors.New("administrator AMR policy contains an invalid value")
		}
	}
	if p.maxAge() < time.Minute || p.maxAge() > time.Hour {
		return errors.New("administrator step-up max age must be between 1 minute and 1 hour")
	}
	return nil
}

func (p AdminPolicy) VerifyFreshPrincipal(principal Principal, now, flowStartedAt time.Time) error {
	if err := p.verifyClaims(principal.Roles, principal.ACR, principal.AMR); err != nil {
		return err
	}
	if principal.AuthTime.IsZero() || principal.AuthTime.After(now.Add(p.skew())) || principal.AuthTime.Before(now.Add(-p.maxAge())) {
		return ErrMFAStepUpRequired
	}
	if !flowStartedAt.IsZero() && principal.AuthTime.Before(flowStartedAt.Add(-p.skew())) {
		return ErrMFAStepUpRequired
	}
	return nil
}

func (p AdminPolicy) AuthorizeSession(session Session, now time.Time) error {
	if err := p.verifyClaims(session.Roles, session.ACR, session.AMR); err != nil {
		return err
	}
	if session.MFAAt == nil || session.MFAAt.Before(now.Add(-p.maxAge())) || session.MFAAt.After(now.Add(p.skew())) {
		return ErrMFAStepUpRequired
	}
	return nil
}

func (p AdminPolicy) verifyClaims(roles []string, acr string, amr []string) error {
	if !slices.Contains(roles, p.Role) {
		return ErrAdminRoleRequired
	}
	if p.RequiredACR == "" || acr != p.RequiredACR {
		return ErrMFAStepUpRequired
	}
	if len(p.RequiredAMR) == 0 {
		return ErrMFAStepUpRequired
	}
	for _, required := range p.RequiredAMR {
		if !slices.Contains(amr, required) {
			return ErrMFAStepUpRequired
		}
	}
	return nil
}

func (p AdminPolicy) maxAge() time.Duration {
	if p.StepUpMaxAge == 0 {
		return defaultStepUpMaxAge
	}
	return p.StepUpMaxAge
}

func (p AdminPolicy) skew() time.Duration {
	if p.ClockSkew == 0 {
		return defaultClockSkew
	}
	return p.ClockSkew
}

type CSRFPolicy struct {
	allowedOrigins map[string]struct{}
	headerName     string
}

func (p CSRFPolicy) Validate() error {
	if len(p.allowedOrigins) == 0 || strings.TrimSpace(p.headerName) == "" {
		return errors.New("CSRF policy is not configured")
	}
	return nil
}

func NewCSRFPolicy(allowedOrigins []string) (CSRFPolicy, error) {
	if len(allowedOrigins) == 0 {
		return CSRFPolicy{}, errors.New("CSRF origin allowlist is empty")
	}
	policy := CSRFPolicy{allowedOrigins: make(map[string]struct{}, len(allowedOrigins)), headerName: "X-CSRF-Token"}
	for _, origin := range allowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return CSRFPolicy{}, errors.New("CSRF origins must be exact HTTPS origins without a path")
		}
		if strings.Contains(u.Host, "*") || hasControl(origin) {
			return CSRFPolicy{}, errors.New("CSRF origins must not contain wildcards or control characters")
		}
		policy.allowedOrigins[origin] = struct{}{}
	}
	return policy, nil
}

// ValidateMutation requires both a synchronizer token and an exact Origin. It
// intentionally rejects requests where proxies coalesce duplicate security
// headers, rather than guessing which value a browser meant.
func (p CSRFPolicy) ValidateMutation(request *http.Request, session Session) error {
	if request == nil {
		return errors.New("request is nil")
	}
	if slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace}, request.Method) {
		return nil
	}
	origins := request.Header.Values("Origin")
	if len(origins) != 1 {
		return errors.New("exactly one Origin header is required")
	}
	if _, ok := p.allowedOrigins[origins[0]]; !ok {
		return errors.New("request Origin is not allowed")
	}
	if site := request.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "none" {
		return errors.New("cross-site mutation request is forbidden")
	}
	values := request.Header.Values(p.headerName)
	if len(values) != 1 || len(values[0]) < 43 || len(values[0]) > 128 || hasControl(values[0]) {
		return errors.New("exactly one valid CSRF token is required")
	}
	if session.CSRFHash == "" || !secureEqualHex(sha256Hex(values[0]), session.CSRFHash) {
		return errors.New("CSRF token is invalid")
	}
	return nil
}
