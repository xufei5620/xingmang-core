// Package invoicehost composes invoice into the platform process without a
// network authentication exchange or a second administrator session.
package invoicehost

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"invoice-system/backend/staffauth"
)

type SessionStore interface {
	LookupSession(context.Context, string) (localauth.Account, error)
}

type StaffConfig struct {
	Origin            string
	Environment       string
	RoleScopes        map[string][]string
	AdminIPAllowlist  localauth.AdminIPAllowlist
	TrustedProxyCIDRs []string
}

func NewStaffResolver(store SessionStore, cfg StaffConfig) (staffauth.Resolver, error) {
	origin, err := ExactOrigin(cfg.Origin)
	if err != nil {
		return nil, err
	}
	if store == nil || cfg.Environment == "" {
		return nil, errors.New("staff resolver dependencies are missing")
	}
	var proxies []*net.IPNet
	for _, raw := range cfg.TrustedProxyCIDRs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return nil, errors.New("invalid trusted proxy CIDR")
		}
		proxies = append(proxies, network)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("cannot create staff CSRF key")
	}
	return func(r *http.Request) (staffauth.Identity, error) {
		denied := func() (staffauth.Identity, error) {
			return staffauth.Identity{}, errors.New("staff session unavailable")
		}
		if r == nil || len(r.Header.Values("Authorization")) != 0 {
			return denied()
		}
		var token string
		for _, cookie := range r.Cookies() {
			if cookie.Name == localauth.SessionCookieName {
				if token != "" || cookie.Value == "" {
					return denied()
				}
				token = cookie.Value
			}
		}
		if token == "" {
			return denied()
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			values := r.Header.Values("X-Requested-With")
			if len(values) != 1 || values[0] != "xingmang" {
				return denied()
			}
		}
		acc, err := store.LookupSession(r.Context(), token)
		if err != nil || acc.ID == uuid.Nil || acc.Username == "" || acc.Disabled || acc.MustChangePassword {
			return denied()
		}
		// The real store is read once. Resolve the validated snapshot through the
		// same role mapping and CSRF policy used by the platform API.
		p, err := localauth.NewResolver(accountSnapshot{acc}, cfg.Environment, localauth.RoleScopesFrom(cfg.RoleScopes)).Resolve(r)
		if err != nil || !p.HasScope("finance.read") {
			return denied()
		}
		client, err := trustedClientIP(r, proxies)
		if err != nil || !cfg.AdminIPAllowlist.Allowed(client.String()) {
			return denied()
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte("xingmang/invoice/staff-csrf/v1\x00"))
		mac.Write([]byte(token))
		identity := staffauth.Identity{Issuer: origin, Subject: acc.ID.String(), DisplayName: acc.Username, Roles: append([]string(nil), acc.Roles...), CSRFToken: base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}
		if acc.SessionMFAAt != nil && slices.Contains(acc.SessionAMR, "pwd") && slices.Contains(acc.SessionAMR, "otp") {
			mfaAt := *acc.SessionMFAAt
			identity.MFAAt = &mfaAt
		}
		return identity, nil
	}, nil
}

type accountSnapshot struct{ account localauth.Account }

func (s accountSnapshot) LookupSession(context.Context, string) (localauth.Account, error) {
	return s.account, nil
}

// ExactOrigin preserves the explicit historic staff identity origin. A request
// Host or a redirect target must never become an identity issuer.
func ExactOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "#*\\\r\n\t ") {
		return "", errors.New("INVOICE_STAFF_ORIGIN must be an exact HTTPS origin without path")
	}
	return raw, nil
}

func trustedClientIP(r *http.Request, proxies []*net.IPNet) (net.IP, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	if remote == nil {
		return nil, errors.New("invalid remote address")
	}
	for _, network := range proxies {
		if network.Contains(remote) {
			values := r.Header.Values("CF-Connecting-IP")
			if len(values) == 0 {
				return remote, nil
			}
			if len(values) != 1 {
				return nil, errors.New("ambiguous client address")
			}
			client := net.ParseIP(strings.TrimSpace(values[0]))
			if client == nil {
				return nil, errors.New("invalid client address")
			}
			return client, nil
		}
	}
	return remote, nil
}
